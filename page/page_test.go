package page

import (
	"os"
	"path/filepath"
	"testing"
)

// -------------------------------------------------------------------------
// PageFile tests
// -------------------------------------------------------------------------

func tmpPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "test.db")
}

func TestCreateAndOpen(t *testing.T) {
	path := tmpPath(t)
	pf, err := Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Superblock is page 0 — pageCount should be 1.
	if pf.PageCount() != 1 {
		t.Fatalf("pageCount=%d want 1", pf.PageCount())
	}
	if err := pf.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-open and verify.
	pf2, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if pf2.PageCount() != 1 {
		t.Fatalf("re-opened pageCount=%d want 1", pf2.PageCount())
	}
	pf2.Close()
}

func TestOpenInvalidMagic(t *testing.T) {
	path := tmpPath(t)
	// Write a file with garbage content.
	if err := os.WriteFile(path, make([]byte, PageSize), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(path)
	if err == nil {
		t.Fatal("expected error for bad magic, got nil")
	}
}

func TestAllocateAndReadWrite(t *testing.T) {
	path := tmpPath(t)
	pf, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer pf.Close()

	id, err := pf.Allocate()
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if id != 1 {
		t.Fatalf("first allocated page should be 1, got %d", id)
	}

	// Write a recognisable pattern.
	var p Page
	for i := range p {
		p[i] = byte(i % 256)
	}
	if err := pf.WritePage(id, &p); err != nil {
		t.Fatalf("WritePage: %v", err)
	}

	var got Page
	if err := pf.ReadPage(id, &got); err != nil {
		t.Fatalf("ReadPage: %v", err)
	}
	if got != p {
		t.Fatal("read-back data doesn't match written data")
	}
}

func TestMultipleAllocations(t *testing.T) {
	path := tmpPath(t)
	pf, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer pf.Close()

	ids := make([]PageID, 10)
	for i := range ids {
		id, err := pf.Allocate()
		if err != nil {
			t.Fatalf("Allocate %d: %v", i, err)
		}
		ids[i] = id
	}
	if pf.PageCount() != 11 { // superblock + 10
		t.Fatalf("pageCount=%d want 11", pf.PageCount())
	}
	// IDs should be contiguous.
	for i, id := range ids {
		if int(id) != i+1 {
			t.Fatalf("ids[%d]=%d want %d", i, id, i+1)
		}
	}
}

func TestFreeListReuse(t *testing.T) {
	path := tmpPath(t)
	pf, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer pf.Close()

	id1, _ := pf.Allocate()
	id2, _ := pf.Allocate()

	// Free id1.
	if err := pf.Free(id1); err != nil {
		t.Fatalf("Free: %v", err)
	}

	// Next allocate should reuse id1.
	id3, err := pf.Allocate()
	if err != nil {
		t.Fatalf("Allocate after free: %v", err)
	}
	if id3 != id1 {
		t.Fatalf("expected reuse of %d, got %d", id1, id3)
	}
	_ = id2
}

func TestFreeListPersists(t *testing.T) {
	path := tmpPath(t)
	pf, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	id1, _ := pf.Allocate()
	id2, _ := pf.Allocate()
	_ = id2
	pf.Free(id1)
	pf.Close()

	// Re-open — free list must survive.
	pf2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer pf2.Close()
	id3, err := pf2.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if id3 != id1 {
		t.Fatalf("free list not persisted: got %d want %d", id3, id1)
	}
}

func TestFreeSuperblockError(t *testing.T) {
	path := tmpPath(t)
	pf, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer pf.Close()
	if err := pf.Free(SuperPage); err == nil {
		t.Fatal("expected error freeing superblock, got nil")
	}
}

func TestReadWritePersistence(t *testing.T) {
	path := tmpPath(t)
	pf, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := pf.Allocate()
	var p Page
	p[0] = 0xDE
	p[PageSize-1] = 0xAD
	pf.WritePage(id, &p)
	pf.Close()

	pf2, _ := Open(path)
	defer pf2.Close()
	var got Page
	pf2.ReadPage(id, &got)
	if got[0] != 0xDE || got[PageSize-1] != 0xAD {
		t.Fatal("page data did not persist across close/open")
	}
}

// -------------------------------------------------------------------------
// BufferPool tests
// -------------------------------------------------------------------------

func newTestPool(t *testing.T, capacity int) (*PageFile, *BufferPool) {
	t.Helper()
	path := tmpPath(t)
	pf, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pf.Close() })
	return pf, NewBufferPool(pf, capacity)
}

func TestBufferPoolFetchPin(t *testing.T) {
	pf, bp := newTestPool(t, 4)

	// Allocate a page directly and write it.
	id, _ := pf.Allocate()
	var p Page
	p[0] = 42
	pf.WritePage(id, &p)

	// Fetch via buffer pool.
	pg, err := bp.FetchPin(id)
	if err != nil {
		t.Fatalf("FetchPin: %v", err)
	}
	if pg[0] != 42 {
		t.Fatalf("FetchPin: pg[0]=%d want 42", pg[0])
	}
	if err := bp.Unpin(id, false); err != nil {
		t.Fatalf("Unpin: %v", err)
	}
}

func TestBufferPoolNewPin(t *testing.T) {
	_, bp := newTestPool(t, 4)

	bp.mu.Lock()
	id, pg, err := bp.NewPin()
	bp.mu.Unlock()
	if err != nil {
		t.Fatalf("NewPin: %v", err)
	}
	pg[7] = 99
	if err := bp.Unpin(id, true); err != nil {
		t.Fatalf("Unpin: %v", err)
	}
	if err := bp.FlushPage(id); err != nil {
		t.Fatalf("FlushPage: %v", err)
	}
}

func TestBufferPoolDirtyWriteback(t *testing.T) {
	pf, bp := newTestPool(t, 2) // tiny pool to force eviction

	// Allocate 3 pages to trigger eviction of the first.
	var ids [3]PageID
	for i := range ids {
		bp.mu.Lock()
		id, pg, err := bp.NewPin()
		bp.mu.Unlock()
		if err != nil {
			t.Fatalf("NewPin %d: %v", i, err)
		}
		pg[0] = byte(i + 1)
		ids[i] = id
		bp.Unpin(id, true)
	}

	// The first page was evicted and written back dirty — re-fetch it.
	pg0, err := bp.FetchPin(ids[0])
	if err != nil {
		t.Fatalf("FetchPin after eviction: %v", err)
	}
	if pg0[0] != 1 {
		t.Fatalf("evicted+written-back page[0]=%d want 1", pg0[0])
	}
	bp.Unpin(ids[0], false)

	// Verify directly from disk too.
	if err := bp.FlushAll(); err != nil {
		t.Fatal(err)
	}
	var disk Page
	pf.ReadPage(ids[0], &disk)
	if disk[0] != 1 {
		t.Fatalf("disk page[0]=%d want 1 after flush", disk[0])
	}
}

func TestBufferPoolAllPinnedError(t *testing.T) {
	_, bp := newTestPool(t, 2)

	// Pin both frames.
	bp.mu.Lock()
	_, _, _ = bp.NewPin()
	_, _, _ = bp.NewPin()
	// Third NewPin should fail (all frames pinned).
	_, _, err := bp.NewPin()
	bp.mu.Unlock()
	if err == nil {
		t.Fatal("expected error when all frames pinned, got nil")
	}
}

func TestBufferPoolFlushAll(t *testing.T) {
	pf, bp := newTestPool(t, 8)

	var ids [5]PageID
	for i := range ids {
		bp.mu.Lock()
		id, pg, _ := bp.NewPin()
		bp.mu.Unlock()
		pg[0] = byte(i + 10)
		ids[i] = id
		bp.Unpin(id, true)
	}
	if err := bp.FlushAll(); err != nil {
		t.Fatalf("FlushAll: %v", err)
	}

	// Verify all pages hit disk.
	for i, id := range ids {
		var p Page
		pf.ReadPage(id, &p)
		if p[0] != byte(i+10) {
			t.Fatalf("page %d: disk[0]=%d want %d", id, p[0], i+10)
		}
	}
}

func TestBufferPoolStats(t *testing.T) {
	_, bp := newTestPool(t, 4)
	s := bp.Stats()
	if s.Capacity != 4 {
		t.Fatalf("capacity=%d want 4", s.Capacity)
	}
	if s.Pinned != 0 || s.Dirty != 0 {
		t.Fatalf("fresh pool: pinned=%d dirty=%d", s.Pinned, s.Dirty)
	}

	bp.mu.Lock()
	id, _, _ := bp.NewPin()
	bp.mu.Unlock()
	s = bp.Stats()
	if s.Pinned != 1 || s.Dirty != 1 {
		t.Fatalf("after NewPin: pinned=%d dirty=%d", s.Pinned, s.Dirty)
	}
	bp.Unpin(id, false)
	s = bp.Stats()
	if s.Pinned != 0 {
		t.Fatalf("after Unpin: pinned=%d", s.Pinned)
	}
}

func TestBufferPoolMarkDirty(t *testing.T) {
	pf, bp := newTestPool(t, 4)

	id, _ := pf.Allocate()
	pg, _ := bp.FetchPin(id)
	pg[0] = 55
	// Mark dirty without dirty=true in Unpin.
	bp.MarkDirty(id)
	bp.Unpin(id, false)
	bp.FlushAll()

	var disk Page
	pf.ReadPage(id, &disk)
	if disk[0] != 55 {
		t.Fatalf("MarkDirty: disk[0]=%d want 55", disk[0])
	}
}

func TestBufferPoolUnpinErrors(t *testing.T) {
	_, bp := newTestPool(t, 4)
	// Unpin non-existent page.
	if err := bp.Unpin(PageID(99), false); err == nil {
		t.Fatal("expected error unpinning non-existent page")
	}
}

func TestBufferPoolLRUOrder(t *testing.T) {
	// Capacity 3: insert 4 pages, verify the right page was evicted (LRU).
	pf, bp := newTestPool(t, 3)

	// Fill all frames.
	var ids [3]PageID
	var vals = [3]byte{10, 20, 30}
	for i := range ids {
		bp.mu.Lock()
		id, pg, _ := bp.NewPin()
		bp.mu.Unlock()
		pg[0] = vals[i]
		ids[i] = id
		bp.Unpin(id, true)
	}

	// Access ids[0] to make it MRU.
	pg0, _ := bp.FetchPin(ids[0])
	bp.Unpin(ids[0], false)
	_ = pg0

	// Now allocate a 4th page — should evict ids[1] (LRU).
	bp.mu.Lock()
	_, _, _ = bp.NewPin()
	bp.mu.Unlock()

	// ids[1] should have been written back to disk.
	var disk Page
	pf.ReadPage(ids[1], &disk)
	if disk[0] != 20 {
		t.Fatalf("LRU eviction: expected ids[1] evicted with val 20, disk[0]=%d", disk[0])
	}
}

func TestPageFileCloseFlushError(t *testing.T) {
	// Close on a valid file should not error.
	path := tmpPath(t)
	pf, _ := Create(path)
	if err := pf.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestReadPagePartialFile(t *testing.T) {
	// Verify ReadPage on a newly created page (sparse file zero-read) succeeds.
	path := tmpPath(t)
	pf, _ := Create(path)
	defer pf.Close()
	id, _ := pf.Allocate()
	var p Page
	if err := pf.ReadPage(id, &p); err != nil {
		t.Fatalf("ReadPage on fresh page: %v", err)
	}
}

func TestFreeThenAllocateChain(t *testing.T) {
	// Free multiple pages and verify LIFO reuse order.
	path := tmpPath(t)
	pf, _ := Create(path)
	defer pf.Close()
	ids := make([]PageID, 5)
	for i := range ids {
		ids[i], _ = pf.Allocate()
	}
	for _, id := range ids {
		pf.Free(id)
	}
	// Reallocate — should reuse in LIFO order.
	for i := len(ids) - 1; i >= 0; i-- {
		got, err := pf.Allocate()
		if err != nil {
			t.Fatal(err)
		}
		if got != ids[i] {
			t.Fatalf("reuse[%d]: got %d want %d", i, got, ids[i])
		}
	}
}
