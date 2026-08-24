// Package page implements a fixed-size page manager over an OS file.
//
// Layout:
//   - Every page is PageSize bytes (4096, matching typical OS page size).
//   - Page 0 is a superblock: [magic(8)] [pageCount(8)] [freeHead(8)] [reserved(4072)]
//   - Pages 1..N are data pages allocated on demand.
//
// Thread-safety: all exported methods on PageFile are safe for concurrent use.
package page

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

const (
	PageSize  = 4096
	Magic     = uint64(0x4D454D44425F5057) // "MEMDB_PW"
	SuperPage = PageID(0)

	// Superblock field offsets within page 0.
	sbOffMagic     = 0
	sbOffPageCount = 8
	sbOffFreeHead  = 16
)

// PageID identifies a page by its zero-based index within the file.
type PageID uint64

// InvalidPage is a sentinel for "no page".
const InvalidPage = PageID(^uint64(0))

// Page is a fixed-size byte array.
type Page [PageSize]byte

// ErrInvalidMagic is returned when the file header has an unexpected magic number.
var ErrInvalidMagic = errors.New("page: invalid magic number — not a memdb page file")

// -------------------------------------------------------------------------
// PageFile — the raw file abstraction
// -------------------------------------------------------------------------

// PageFile manages a file as a sequence of fixed-size pages.
// It provides Allocate/Free for page lifecycle and Read/Write for I/O.
type PageFile struct {
	mu        sync.Mutex
	f         *os.File
	pageCount uint64 // total pages allocated so far (including superblock)
	freeHead  PageID // head of free-list (InvalidPage = empty)
}

// Create creates a new page file at path, overwriting any existing file.
func Create(path string) (*PageFile, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("page.Create: %w", err)
	}
	pf := &PageFile{
		f:         f,
		pageCount: 1, // superblock
		freeHead:  InvalidPage,
	}
	// Write superblock.
	if err := pf.writeSuperblock(); err != nil {
		f.Close()
		return nil, err
	}
	return pf, nil
}

// Open opens an existing page file at path for reading and writing.
func Open(path string) (*PageFile, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("page.Open: %w", err)
	}
	pf := &PageFile{f: f}
	if err := pf.readSuperblock(); err != nil {
		f.Close()
		return nil, err
	}
	return pf, nil
}

// Close flushes and closes the underlying file.
func (pf *PageFile) Close() error {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	if err := pf.writeSuperblock(); err != nil {
		return err
	}
	return pf.f.Close()
}

// PageCount returns the total number of allocated pages (including the superblock).
func (pf *PageFile) PageCount() uint64 {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	return pf.pageCount
}

// -------------------------------------------------------------------------
// Allocate / Free
// -------------------------------------------------------------------------

// Allocate returns a PageID for a new page, reusing freed pages when possible.
// The caller owns the page until Free is called; the contents are zero-filled
// if newly extended, or whatever the freed page last contained.
func (pf *PageFile) Allocate() (PageID, error) {
	pf.mu.Lock()
	defer pf.mu.Unlock()

	if pf.freeHead != InvalidPage {
		// Pop from free list.
		id := pf.freeHead
		var p Page
		if err := pf.readPageLocked(id, &p); err != nil {
			return InvalidPage, err
		}
		pf.freeHead = PageID(binary.LittleEndian.Uint64(p[0:8]))
		if err := pf.writeSuperblock(); err != nil {
			return InvalidPage, err
		}
		return id, nil
	}

	// Extend the file.
	id := PageID(pf.pageCount)
	pf.pageCount++
	var p Page // zero
	if err := pf.writePageLocked(id, &p); err != nil {
		pf.pageCount--
		return InvalidPage, err
	}
	if err := pf.writeSuperblock(); err != nil {
		return InvalidPage, err
	}
	return id, nil
}

// Free returns a page to the free list.
// The page's first 8 bytes are overwritten with the previous free-list head.
func (pf *PageFile) Free(id PageID) error {
	pf.mu.Lock()
	defer pf.mu.Unlock()

	if id == SuperPage {
		return fmt.Errorf("page.Free: cannot free superblock")
	}
	var p Page
	binary.LittleEndian.PutUint64(p[0:8], uint64(pf.freeHead))
	if err := pf.writePageLocked(id, &p); err != nil {
		return err
	}
	pf.freeHead = id
	return pf.writeSuperblock()
}

// -------------------------------------------------------------------------
// Read / Write
// -------------------------------------------------------------------------

// ReadPage reads page id into dst.
func (pf *PageFile) ReadPage(id PageID, dst *Page) error {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	return pf.readPageLocked(id, dst)
}

// WritePage writes src to page id on disk.
func (pf *PageFile) WritePage(id PageID, src *Page) error {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	return pf.writePageLocked(id, src)
}

func (pf *PageFile) readPageLocked(id PageID, dst *Page) error {
	offset := int64(id) * PageSize
	_, err := pf.f.ReadAt(dst[:], offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("page.Read(%d): %w", id, err)
	}
	return nil
}

func (pf *PageFile) writePageLocked(id PageID, src *Page) error {
	offset := int64(id) * PageSize
	if _, err := pf.f.WriteAt(src[:], offset); err != nil {
		return fmt.Errorf("page.Write(%d): %w", id, err)
	}
	return nil
}

// -------------------------------------------------------------------------
// Superblock
// -------------------------------------------------------------------------

func (pf *PageFile) writeSuperblock() error {
	var p Page
	binary.LittleEndian.PutUint64(p[sbOffMagic:], Magic)
	binary.LittleEndian.PutUint64(p[sbOffPageCount:], pf.pageCount)
	binary.LittleEndian.PutUint64(p[sbOffFreeHead:], uint64(pf.freeHead))
	return pf.writePageLocked(SuperPage, &p)
}

func (pf *PageFile) readSuperblock() error {
	var p Page
	if err := pf.readPageLocked(SuperPage, &p); err != nil {
		return err
	}
	if m := binary.LittleEndian.Uint64(p[sbOffMagic:]); m != Magic {
		return ErrInvalidMagic
	}
	pf.pageCount = binary.LittleEndian.Uint64(p[sbOffPageCount:])
	pf.freeHead = PageID(binary.LittleEndian.Uint64(p[sbOffFreeHead:]))
	return nil
}
