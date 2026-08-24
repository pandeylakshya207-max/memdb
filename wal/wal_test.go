package wal

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pandeylakshya207-max/memdb/page"
)

// -------------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------------

func tmpDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func walPath(dir string) string { return filepath.Join(dir, "test.wal") }
func dbPath(dir string) string  { return filepath.Join(dir, "test.db") }

func mustCreateWAL(t *testing.T, path string) *WAL {
	t.Helper()
	w, err := Create(path)
	if err != nil {
		t.Fatalf("Create WAL: %v", err)
	}
	return w
}

func mustOpenWAL(t *testing.T, path string) *WAL {
	t.Helper()
	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open WAL: %v", err)
	}
	return w
}

func mustCreateDB(t *testing.T, path string) *page.PageFile {
	t.Helper()
	pf, err := page.Create(path)
	if err != nil {
		t.Fatalf("Create PageFile: %v", err)
	}
	return pf
}

func mustOpenDB(t *testing.T, path string) *page.PageFile {
	t.Helper()
	pf, err := page.Open(path)
	if err != nil {
		t.Fatalf("Open PageFile: %v", err)
	}
	return pf
}

func makePageData(seed byte) *page.Page {
	var p page.Page
	for i := range p {
		p[i] = seed
	}
	return &p
}

// -------------------------------------------------------------------------
// Basic WAL tests
// -------------------------------------------------------------------------

func TestWALCreateAndClose(t *testing.T) {
	dir := tmpDir(t)
	w := mustCreateWAL(t, walPath(dir))
	if w.NextLSN() != 1 {
		t.Fatalf("fresh WAL nextLSN=%d want 1", w.NextLSN())
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestWALAppendWrite(t *testing.T) {
	dir := tmpDir(t)
	w := mustCreateWAL(t, walPath(dir))
	defer w.Close()

	p := makePageData(0xAB)
	lsn, err := w.AppendWrite(page.PageID(1), p)
	if err != nil {
		t.Fatalf("AppendWrite: %v", err)
	}
	if lsn != 1 {
		t.Fatalf("first AppendWrite lsn=%d want 1", lsn)
	}
	if w.NextLSN() != 2 {
		t.Fatalf("nextLSN=%d want 2", w.NextLSN())
	}
}

func TestWALAppendCommit(t *testing.T) {
	dir := tmpDir(t)
	w := mustCreateWAL(t, walPath(dir))
	defer w.Close()

	p := makePageData(0x01)
	w.AppendWrite(page.PageID(1), p)
	lsn, err := w.AppendCommit()
	if err != nil {
		t.Fatalf("AppendCommit: %v", err)
	}
	if lsn != 2 {
		t.Fatalf("commit lsn=%d want 2", lsn)
	}
}

func TestWALAppendCheckpoint(t *testing.T) {
	dir := tmpDir(t)
	w := mustCreateWAL(t, walPath(dir))
	defer w.Close()

	lsn, err := w.AppendCheckpoint()
	if err != nil {
		t.Fatalf("AppendCheckpoint: %v", err)
	}
	if lsn != 1 {
		t.Fatalf("checkpoint lsn=%d want 1", lsn)
	}
}

func TestWALOpenResumesLSN(t *testing.T) {
	dir := tmpDir(t)
	wp := walPath(dir)

	w := mustCreateWAL(t, wp)
	p := makePageData(0x55)
	w.AppendWrite(page.PageID(1), p)
	w.AppendWrite(page.PageID(2), p)
	w.AppendCommit()
	w.Close()

	// Re-open: nextLSN should resume at 4.
	w2 := mustOpenWAL(t, wp)
	defer w2.Close()
	if w2.NextLSN() != 4 {
		t.Fatalf("reopened nextLSN=%d want 4", w2.NextLSN())
	}
}

func TestWALScanRecords(t *testing.T) {
	dir := tmpDir(t)
	wp := walPath(dir)

	w := mustCreateWAL(t, wp)
	p1 := makePageData(0x11)
	p2 := makePageData(0x22)
	w.AppendWrite(page.PageID(10), p1)
	w.AppendWrite(page.PageID(20), p2)
	w.AppendCommit()
	w.Close()

	// Scan via Open.
	w2 := mustOpenWAL(t, wp)
	defer w2.Close()

	var records []Record
	w2.scan(func(r Record) bool {
		records = append(records, r)
		return true
	})

	if len(records) != 3 {
		t.Fatalf("scan returned %d records, want 3", len(records))
	}
	if records[0].Type != TypeWrite || records[0].PageID != 10 {
		t.Fatalf("record[0]: type=%v pageID=%v", records[0].Type, records[0].PageID)
	}
	if records[1].Type != TypeWrite || records[1].PageID != 20 {
		t.Fatalf("record[1]: type=%v pageID=%v", records[1].Type, records[1].PageID)
	}
	if records[2].Type != TypeCommit {
		t.Fatalf("record[2]: expected Commit, got %v", records[2].Type)
	}
}

func TestWALDataIntegrity(t *testing.T) {
	dir := tmpDir(t)
	wp := walPath(dir)

	w := mustCreateWAL(t, wp)
	orig := makePageData(0xDE)
	orig[0] = 0xDE
	orig[page.PageSize-1] = 0xAD
	w.AppendWrite(page.PageID(5), orig)
	w.AppendCommit()
	w.Close()

	w2 := mustOpenWAL(t, wp)
	defer w2.Close()

	var got []Record
	w2.scan(func(r Record) bool { got = append(got, r); return true })

	if len(got) < 1 {
		t.Fatal("no records found")
	}
	r := got[0]
	if r.Data[0] != 0xDE || r.Data[page.PageSize-1] != 0xAD {
		t.Fatalf("data corruption: [0]=%02x [last]=%02x", r.Data[0], r.Data[page.PageSize-1])
	}
}

// -------------------------------------------------------------------------
// CRC corruption test
// -------------------------------------------------------------------------

func TestWALCRCCorruptionDetected(t *testing.T) {
	dir := tmpDir(t)
	wp := walPath(dir)

	w := mustCreateWAL(t, wp)
	p := makePageData(0xFF)
	w.AppendWrite(page.PageID(1), p)
	w.AppendCommit()
	w.Close()

	// Corrupt a byte in the middle of the first record's data.
	f, err := os.OpenFile(wp, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Skip header (hdrSize=21) and flip a byte in the page data.
	if _, err := f.WriteAt([]byte{0x00}, int64(hdrSize+100)); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Scan should stop at the corrupted record — return 0 valid records.
	w2 := mustOpenWAL(t, wp)
	defer w2.Close()
	var records []Record
	w2.scan(func(r Record) bool { records = append(records, r); return true })

	// The corrupted first record stops the scan — nothing should come through.
	if len(records) != 0 {
		t.Fatalf("expected 0 records after CRC corruption, got %d", len(records))
	}
}

// -------------------------------------------------------------------------
// Recovery tests — the critical ones
// -------------------------------------------------------------------------

// TestRecoverNoWAL verifies Recover is a no-op when the WAL doesn't exist.
func TestRecoverNoWAL(t *testing.T) {
	dir := tmpDir(t)
	pf := mustCreateDB(t, dbPath(dir))
	defer pf.Close()

	n, err := Recover(walPath(dir), pf)
	if err != nil {
		t.Fatalf("Recover with no WAL: %v", err)
	}
	if n != 0 {
		t.Fatalf("Recover with no WAL: replayed %d want 0", n)
	}
}

// TestRecoverCommittedTransaction verifies that a committed write is replayed.
func TestRecoverCommittedTransaction(t *testing.T) {
	dir := tmpDir(t)
	wp, dp := walPath(dir), dbPath(dir)

	// Setup: create DB page and WAL with a committed write.
	pf := mustCreateDB(t, dp)
	id, _ := pf.Allocate()
	pf.Close()

	w := mustCreateWAL(t, wp)
	newData := makePageData(0xBB)
	w.AppendWrite(id, newData)
	w.AppendCommit()
	w.Close()

	// Recovery: open the DB (without the WAL write applied) and recover.
	pf2 := mustOpenDB(t, dp)
	defer pf2.Close()

	n, err := Recover(wp, pf2)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if n != 1 {
		t.Fatalf("Recover replayed %d want 1", n)
	}

	// Verify the page was updated on disk.
	var got page.Page
	pf2.ReadPage(id, &got)
	for i, b := range got {
		if b != 0xBB {
			t.Fatalf("recovered page[%d]=%02x want 0xBB", i, b)
		}
	}
}

// TestRecoverUncommittedTransactionDiscarded verifies that a crash mid-write
// (no Commit record) does NOT modify the page file.
func TestRecoverUncommittedTransactionDiscarded(t *testing.T) {
	dir := tmpDir(t)
	wp, dp := walPath(dir), dbPath(dir)

	pf := mustCreateDB(t, dp)
	id, _ := pf.Allocate()

	// Write original data and flush.
	origData := makePageData(0x11)
	pf.WritePage(id, origData)
	pf.Close()

	// WAL has a Write but NO Commit — simulating a crash mid-transaction.
	w := mustCreateWAL(t, wp)
	crashData := makePageData(0xFF)
	w.AppendWrite(id, crashData)
	// No AppendCommit — power cut here.
	w.f.Sync()
	w.f.Close()

	// Recovery must NOT apply the uncommitted write.
	pf2 := mustOpenDB(t, dp)
	defer pf2.Close()

	n, err := Recover(wp, pf2)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if n != 0 {
		t.Fatalf("uncommitted write was replayed (%d pages) — must not be", n)
	}

	var got page.Page
	pf2.ReadPage(id, &got)
	if got[0] != 0x11 {
		t.Fatalf("page corrupted by uncommitted recovery: got %02x want 0x11", got[0])
	}
}

// TestRecoverMultipleTransactions verifies that all committed transactions
// are replayed and uncommitted ones are not.
func TestRecoverMultipleTransactions(t *testing.T) {
	dir := tmpDir(t)
	wp, dp := walPath(dir), dbPath(dir)

	pf := mustCreateDB(t, dp)
	ids := make([]page.PageID, 4)
	for i := range ids {
		ids[i], _ = pf.Allocate()
	}
	pf.Close()

	w := mustCreateWAL(t, wp)

	// Transaction 1: writes pages 0 and 1, commits.
	w.AppendWrite(ids[0], makePageData(0xAA))
	w.AppendWrite(ids[1], makePageData(0xBB))
	w.AppendCommit()

	// Transaction 2: writes page 2, commits.
	w.AppendWrite(ids[2], makePageData(0xCC))
	w.AppendCommit()

	// Transaction 3 (incomplete — crash before commit).
	w.AppendWrite(ids[3], makePageData(0xFF))
	// No commit.
	w.f.Sync()
	w.f.Close()

	pf2 := mustOpenDB(t, dp)
	defer pf2.Close()

	n, err := Recover(wp, pf2)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if n != 3 {
		t.Fatalf("Recover replayed %d pages, want 3 (from 2 committed txns)", n)
	}

	check := func(id page.PageID, want byte) {
		t.Helper()
		var p page.Page
		pf2.ReadPage(id, &p)
		if p[0] != want {
			t.Errorf("page %d: got %02x want %02x", id, p[0], want)
		}
	}
	check(ids[0], 0xAA)
	check(ids[1], 0xBB)
	check(ids[2], 0xCC)
	check(ids[3], 0x00) // uncommitted — must stay zero
}

// TestRecoverAfterCheckpoint verifies that pre-checkpoint records are skipped
// and only post-checkpoint committed writes are replayed.
func TestRecoverAfterCheckpoint(t *testing.T) {
	dir := tmpDir(t)
	wp, dp := walPath(dir), dbPath(dir)

	pf := mustCreateDB(t, dp)
	ids := make([]page.PageID, 3)
	for i := range ids {
		ids[i], _ = pf.Allocate()
	}
	pf.Close()

	w := mustCreateWAL(t, wp)

	// Transaction before checkpoint (already durable in our "DB").
	w.AppendWrite(ids[0], makePageData(0x01))
	w.AppendCommit()

	// Checkpoint — marks that everything above is already on disk.
	w.AppendCheckpoint()

	// Transaction after checkpoint — must be replayed.
	w.AppendWrite(ids[1], makePageData(0x02))
	w.AppendCommit()

	// Incomplete after checkpoint — must NOT be replayed.
	w.AppendWrite(ids[2], makePageData(0xFF))
	// No commit.
	w.f.Sync()
	w.f.Close()

	pf2 := mustOpenDB(t, dp)
	defer pf2.Close()

	n, err := Recover(wp, pf2)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	// Only ids[1] is post-checkpoint + committed.
	if n != 1 {
		t.Fatalf("Recover replayed %d pages, want 1", n)
	}

	var p page.Page
	pf2.ReadPage(ids[1], &p)
	if p[0] != 0x02 {
		t.Fatalf("ids[1] page[0]=%02x want 0x02", p[0])
	}
	pf2.ReadPage(ids[2], &p)
	if p[0] != 0x00 {
		t.Fatalf("ids[2] should be zero (uncommitted), got %02x", p[0])
	}
}

// TestRecoverTruncatedRecord simulates a crash that truncated a WAL record
// mid-write (partial header).  Recovery must not error — just stop at the
// truncated tail.
func TestRecoverTruncatedRecord(t *testing.T) {
	dir := tmpDir(t)
	wp, dp := walPath(dir), dbPath(dir)

	pf := mustCreateDB(t, dp)
	id, _ := pf.Allocate()
	pf.Close()

	// Write a valid committed record followed by a partial (truncated) one.
	w := mustCreateWAL(t, wp)
	w.AppendWrite(id, makePageData(0xAA))
	w.AppendCommit()
	w.f.Sync()
	w.f.Close()

	// Append a partial header (simulates crash mid-write).
	f, _ := os.OpenFile(wp, os.O_APPEND|os.O_WRONLY, 0)
	f.Write([]byte{0x01, 0x02, 0x03}) // truncated — not a full record
	f.Close()

	pf2 := mustOpenDB(t, dp)
	defer pf2.Close()

	n, err := Recover(wp, pf2)
	if err != nil {
		t.Fatalf("Recover with truncated tail: %v", err)
	}
	if n != 1 {
		t.Fatalf("Recover replayed %d want 1 (the committed txn)", n)
	}
}

// TestRecoverOverwriteLatest verifies that when the same page is written
// multiple times in committed transactions, only the latest value survives.
func TestRecoverOverwriteLatest(t *testing.T) {
	dir := tmpDir(t)
	wp, dp := walPath(dir), dbPath(dir)

	pf := mustCreateDB(t, dp)
	id, _ := pf.Allocate()
	pf.Close()

	w := mustCreateWAL(t, wp)
	w.AppendWrite(id, makePageData(0x01)) // txn1
	w.AppendCommit()
	w.AppendWrite(id, makePageData(0x02)) // txn2 overwrites same page
	w.AppendCommit()
	w.AppendWrite(id, makePageData(0x03)) // txn3
	w.AppendCommit()
	w.Close()

	pf2 := mustOpenDB(t, dp)
	defer pf2.Close()

	n, err := Recover(wp, pf2)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	// Only 1 unique page, 1 write applied (latest).
	if n != 1 {
		t.Fatalf("Recover replayed %d want 1", n)
	}

	var p page.Page
	pf2.ReadPage(id, &p)
	if p[0] != 0x03 {
		t.Fatalf("latest value not applied: got %02x want 0x03", p[0])
	}
}

// -------------------------------------------------------------------------
// End-to-end: WAL + BufferPool integration
// -------------------------------------------------------------------------

// TestEndToEndWALAndBufferPool exercises the full stack:
// write through the buffer pool, log to WAL, simulate crash (don't flush BP),
// recover from WAL, verify data is on disk.
func TestEndToEndWALAndBufferPool(t *testing.T) {
	dir := tmpDir(t)
	wp, dp := walPath(dir), dbPath(dir)

	// --- "Normal operation" phase ---
	pf := mustCreateDB(t, dp)
	bp := page.NewBufferPool(pf, 8)

	// Allocate a page via PageFile directly.
	id, err := pf.Allocate()
	if err != nil {
		t.Fatal(err)
	}

	// Fetch, modify in memory only (don't flush to disk yet).
	pg, err := bp.FetchPin(id)
	if err != nil {
		t.Fatal(err)
	}
	pg[0] = 0xCA
	pg[1] = 0xFE
	bp.MarkDirty(id)
	bp.Unpin(id, false)

	// Log the write to WAL BEFORE flushing to disk.
	w := mustCreateWAL(t, wp)
	var walPage page.Page
	copy(walPage[:], pg[:]) // capture the in-memory version
	walPage[0] = 0xCA
	walPage[1] = 0xFE
	w.AppendWrite(id, &walPage)
	commitLSN, err := w.AppendCommit()
	if err != nil {
		t.Fatalf("WAL commit: %v", err)
	}
	_ = commitLSN

	// CRASH: close WAL and page file WITHOUT flushing the buffer pool.
	w.Close()
	// Don't call bp.FlushAll() — simulates power cut after WAL write but
	// before buffer pool flush.
	pf.Close()

	// --- "Recovery" phase ---
	pf2 := mustOpenDB(t, dp)
	defer pf2.Close()

	// Page on disk should still be zero (buffer pool was never flushed).
	var before page.Page
	pf2.ReadPage(id, &before)
	if before[0] != 0x00 {
		t.Fatalf("pre-recovery: page[0]=%02x, expected 0x00 (not flushed)", before[0])
	}

	// Recover from WAL.
	n, err := Recover(wp, pf2)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if n != 1 {
		t.Fatalf("Recover replayed %d pages, want 1", n)
	}

	// Now the page should have the WAL-committed value.
	var after page.Page
	pf2.ReadPage(id, &after)
	if after[0] != 0xCA || after[1] != 0xFE {
		t.Fatalf("post-recovery: page[0:2]=%02x%02x, want 0xCAFE", after[0], after[1])
	}
}

func TestWALCloseSync(t *testing.T) {
	dir := tmpDir(t)
	w := mustCreateWAL(t, walPath(dir))
	p := makePageData(0x77)
	w.AppendWrite(page.PageID(1), p)
	// Close must fsync without error.
	if err := w.Close(); err != nil {
		t.Fatalf("WAL Close: %v", err)
	}
}

func TestWALCheckpointResumesOnOpen(t *testing.T) {
	dir := tmpDir(t)
	wp := walPath(dir)
	w := mustCreateWAL(t, wp)
	w.AppendWrite(page.PageID(1), makePageData(0x01))
	w.AppendCommit()
	cpLSN, _ := w.AppendCheckpoint()
	w.Close()

	w2 := mustOpenWAL(t, wp)
	defer w2.Close()
	// Internal checkpointLSN must match what we wrote.
	if w2.checkpointLSN != cpLSN {
		t.Fatalf("reopened checkpointLSN=%d want %d", w2.checkpointLSN, cpLSN)
	}
}
