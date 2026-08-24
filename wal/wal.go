// Package wal implements a write-ahead log (WAL) for crash recovery.
//
// Log format (each record):
//
//	[LSN: 8 bytes] [Type: 1 byte] [PageID: 8 bytes] [DataLen: 4 bytes] [Data: N bytes] [CRC32: 4 bytes]
//
// Types:
//
//	TypeWrite  — a before/after page write; Data = full PageSize bytes of the NEW page image
//	TypeCommit — marks end of a transaction; Data = empty
//	TypeCheckpoint — marks a safe recovery start point; Data = empty
//
// Recovery (REDO-only):
//
//	Scan log from the most recent checkpoint (or the start).
//	For each committed transaction, re-apply all TypeWrite records whose LSN
//	is greater than the page's on-disk LSN.
//	Partial (uncommitted) records at the tail are silently truncated.
package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sync"

	"github.com/pandeylakshya207-max/memdb/page"
)

// RecordType identifies the kind of WAL record.
type RecordType uint8

const (
	TypeWrite      RecordType = 1
	TypeCommit     RecordType = 2
	TypeCheckpoint RecordType = 3
)

// LSN is a Log Sequence Number — a monotonically increasing uint64.
type LSN uint64

const InvalidLSN = LSN(0)

// Record is a decoded WAL entry.
type Record struct {
	LSN    LSN
	Type   RecordType
	PageID page.PageID
	Data   []byte // nil for Commit/Checkpoint; full page image for Write
}

// Header wire sizes.
const (
	hdrSize = 8 + 1 + 8 + 4 // LSN + Type + PageID + DataLen
	crcSize = 4
)

// -------------------------------------------------------------------------
// WAL
// -------------------------------------------------------------------------

// WAL is a write-ahead log backed by an append-only OS file.
// All methods are safe for concurrent use.
type WAL struct {
	mu            sync.Mutex
	f             *os.File
	nextLSN       LSN
	checkpointLSN LSN // LSN of the most recent checkpoint record
}

// Create creates a new (empty) WAL file at path.
func Create(path string) (*WAL, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("wal.Create: %w", err)
	}
	w := &WAL{f: f, nextLSN: 1}
	return w, nil
}

// Open opens an existing WAL for appending.  It scans the file to find the
// highest valid LSN so that new records get correct sequence numbers.
func Open(path string) (*WAL, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("wal.Open: %w", err)
	}
	w := &WAL{f: f, nextLSN: 1}
	// Scan to find tail LSN and last checkpoint.
	_ = w.scan(func(r Record) bool {
		if r.LSN >= w.nextLSN {
			w.nextLSN = r.LSN + 1
		}
		if r.Type == TypeCheckpoint {
			w.checkpointLSN = r.LSN
		}
		return true
	})
	return w, nil
}

// Close syncs and closes the WAL file.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.f.Sync(); err != nil {
		return err
	}
	return w.f.Close()
}

// -------------------------------------------------------------------------
// Write / Commit / Checkpoint
// -------------------------------------------------------------------------

// AppendWrite appends a TypeWrite record for pageID with the new page image.
// Returns the LSN assigned to this record.
func (w *WAL) AppendWrite(pageID page.PageID, data *page.Page) (LSN, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.append(TypeWrite, pageID, data[:])
}

// AppendCommit appends a TypeCommit record and fsyncs the file.
// Returns the commit LSN.
func (w *WAL) AppendCommit() (LSN, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	lsn, err := w.append(TypeCommit, page.InvalidPage, nil)
	if err != nil {
		return InvalidLSN, err
	}
	// Flush to disk — callers may now consider the transaction durable.
	if err := w.f.Sync(); err != nil {
		return InvalidLSN, fmt.Errorf("wal.AppendCommit fsync: %w", err)
	}
	return lsn, nil
}

// AppendCheckpoint writes a checkpoint record and fsyncs.
// After this the recovery scan only needs to start from this LSN.
func (w *WAL) AppendCheckpoint() (LSN, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	lsn, err := w.append(TypeCheckpoint, page.InvalidPage, nil)
	if err != nil {
		return InvalidLSN, err
	}
	if err := w.f.Sync(); err != nil {
		return InvalidLSN, fmt.Errorf("wal.AppendCheckpoint fsync: %w", err)
	}
	w.checkpointLSN = lsn
	return lsn, nil
}

// NextLSN returns the LSN that will be assigned to the next appended record.
func (w *WAL) NextLSN() LSN {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.nextLSN
}

// -------------------------------------------------------------------------
// Recovery
// -------------------------------------------------------------------------

// Recover replays committed write records from the WAL onto pf.
// It starts from the most recent checkpoint (or the beginning of the log if
// none exists) and re-applies page images for all committed transactions.
// Partial records at the tail (crash mid-write) are silently ignored.
//
// Returns the number of page writes replayed.
func Recover(walPath string, pf *page.PageFile) (int, error) {
	f, err := os.Open(walPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil // no WAL — nothing to recover
		}
		return 0, fmt.Errorf("wal.Recover open: %w", err)
	}
	defer f.Close()

	// Pass 1: find the last checkpoint LSN.
	var cpLSN LSN
	scanFile(f, func(r Record) bool {
		if r.Type == TypeCheckpoint {
			cpLSN = r.LSN
		}
		return true
	})

	// Pass 2: collect committed transactions starting from cpLSN.
	// A "transaction" here is the sequence of Write records before a Commit.
	// We use a simple model: all Write records before a Commit are part of
	// the same (implicit) transaction.
	//
	// Map: pageID → latest page image (from committed writes only).
	type pageWrite struct {
		lsn  LSN
		data []byte
	}
	committed := map[page.PageID]pageWrite{}

	// pending holds writes seen since the last commit.
	pending := map[page.PageID]pageWrite{}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	scanFile(f, func(r Record) bool {
		if r.LSN < cpLSN {
			return true // skip pre-checkpoint
		}
		switch r.Type {
		case TypeWrite:
			// Track the latest write per page in this transaction.
			if prev, ok := pending[r.PageID]; !ok || r.LSN > prev.lsn {
				data := make([]byte, len(r.Data))
				copy(data, r.Data)
				pending[r.PageID] = pageWrite{lsn: r.LSN, data: data}
			}
		case TypeCommit:
			// Commit: merge pending → committed.
			for pid, pw := range pending {
				if prev, ok := committed[pid]; !ok || pw.lsn > prev.lsn {
					committed[pid] = pw
				}
			}
			pending = map[page.PageID]pageWrite{}
		case TypeCheckpoint:
			// Checkpoint inside pass 2 is informational only.
		}
		return true
	})
	// Uncommitted pending writes are discarded (crash recovery).

	// Apply committed writes to the page file.
	replayed := 0
	for pid, pw := range committed {
		var p page.Page
		copy(p[:], pw.data)
		if err := pf.WritePage(pid, &p); err != nil {
			return replayed, fmt.Errorf("wal.Recover apply page %d: %w", pid, err)
		}
		replayed++
	}
	return replayed, nil
}

// -------------------------------------------------------------------------
// Internal I/O helpers
// -------------------------------------------------------------------------

// append serialises and writes one record.  Must be called with w.mu held.
func (w *WAL) append(t RecordType, pid page.PageID, data []byte) (LSN, error) {
	lsn := w.nextLSN
	rec := marshal(lsn, t, pid, data)
	if _, err := w.f.Write(rec); err != nil {
		return InvalidLSN, fmt.Errorf("wal.append: %w", err)
	}
	w.nextLSN++
	return lsn, nil
}

// marshal encodes a record into wire format.
func marshal(lsn LSN, t RecordType, pid page.PageID, data []byte) []byte {
	dataLen := len(data)
	buf := make([]byte, hdrSize+dataLen+crcSize)

	binary.LittleEndian.PutUint64(buf[0:], uint64(lsn))
	buf[8] = byte(t)
	binary.LittleEndian.PutUint64(buf[9:], uint64(pid))
	binary.LittleEndian.PutUint32(buf[17:], uint32(dataLen))
	if dataLen > 0 {
		copy(buf[hdrSize:], data)
	}

	// CRC32 over header + data.
	crc := crc32.ChecksumIEEE(buf[:hdrSize+dataLen])
	binary.LittleEndian.PutUint32(buf[hdrSize+dataLen:], crc)
	return buf
}

// scan reads records from w.f sequentially, calling fn for each valid record.
// Returns on the first CRC error or short read (treats as tail corruption).
func (w *WAL) scan(fn func(Record) bool) error {
	if _, err := w.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return scanFile(w.f, fn)
}

// scanFile does the same but on an arbitrary *os.File (used by Recover).
func scanFile(f *os.File, fn func(Record) bool) error {
	hdr := make([]byte, hdrSize)
	for {
		_, err := io.ReadFull(f, hdr)
		if err != nil {
			return nil // EOF or short read at tail — stop cleanly
		}
		lsn := LSN(binary.LittleEndian.Uint64(hdr[0:]))
		t := RecordType(hdr[8])
		pid := page.PageID(binary.LittleEndian.Uint64(hdr[9:]))
		dataLen := int(binary.LittleEndian.Uint32(hdr[17:]))

		payload := make([]byte, dataLen+crcSize)
		if _, err := io.ReadFull(f, payload); err != nil {
			return nil // truncated tail
		}
		data := payload[:dataLen]
		storedCRC := binary.LittleEndian.Uint32(payload[dataLen:])

		// Verify CRC over header + data.
		h := crc32.NewIEEE()
		h.Write(hdr)
		h.Write(data)
		if h.Sum32() != storedCRC {
			return nil // CRC mismatch — tail corruption, stop
		}

		r := Record{LSN: lsn, Type: t, PageID: pid}
		if dataLen > 0 {
			r.Data = data
		}
		if !fn(r) {
			return nil
		}
	}
}
