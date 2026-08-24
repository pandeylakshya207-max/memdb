package page

import (
	"container/list"
	"fmt"
	"sync"
)

// BufferPool is an in-memory cache of pages backed by a PageFile.
//
// Design:
//   - Fixed capacity (maxFrames pages).
//   - LRU eviction: the least-recently-used unpinned clean page is evicted first;
//     dirty pages are written back before eviction.
//   - Pin/Unpin: callers pin a page before use and unpin when done.  Pinned
//     pages cannot be evicted.  MarkDirty flags a page for write-back.
//   - A frame table maps PageID → frame slot; each slot holds the page bytes,
//     pin count, dirty flag, and LRU list element.
//
// All exported methods are safe for concurrent use.
type BufferPool struct {
	mu       sync.Mutex
	pf       *PageFile
	capacity int

	frames   []frame
	pageMap  map[PageID]int // PageID → frame index
	lru      *list.List     // *lruEntry, front = MRU, back = LRU
	freeList []int          // indices of unused frames
}

type frame struct {
	data     Page
	pageID   PageID
	pinCount int
	dirty    bool
	lruElem  *list.Element // nil if frame is unused
}

type lruEntry struct {
	frameIdx int
}

// NewBufferPool creates a buffer pool with the given capacity (number of page frames).
func NewBufferPool(pf *PageFile, capacity int) *BufferPool {
	if capacity < 1 {
		capacity = 1
	}
	bp := &BufferPool{
		pf:       pf,
		capacity: capacity,
		frames:   make([]frame, capacity),
		pageMap:  make(map[PageID]int, capacity),
		lru:      list.New(),
		freeList: make([]int, capacity),
	}
	for i := range bp.frames {
		bp.frames[i].pageID = InvalidPage
		bp.freeList[i] = i
	}
	return bp
}

// FetchPin loads page id into a frame, pins it, and returns a pointer to
// the frame's data.  The caller must call Unpin when finished.
// The returned *Page is valid only while the frame is pinned.
func (bp *BufferPool) FetchPin(id PageID) (*Page, error) {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	// Already in cache?
	if fi, ok := bp.pageMap[id]; ok {
		f := &bp.frames[fi]
		f.pinCount++
		// Move to MRU position.
		if f.lruElem != nil {
			bp.lru.MoveToFront(f.lruElem)
		}
		return &f.data, nil
	}

	// Need a free frame.
	fi, err := bp.evict()
	if err != nil {
		return nil, err
	}

	f := &bp.frames[fi]
	f.pageID = id
	f.pinCount = 1
	f.dirty = false

	// Read from disk.
	if err := bp.pf.readPageLocked(id, &f.data); err != nil {
		// Return frame to free list on failure.
		f.pageID = InvalidPage
		bp.freeList = append(bp.freeList, fi)
		return nil, fmt.Errorf("BufferPool.FetchPin(%d): %w", id, err)
	}

	bp.pageMap[id] = fi
	f.lruElem = bp.lru.PushFront(&lruEntry{fi})
	return &f.data, nil
}

// NewPin allocates a new page on disk, loads it into a frame, pins it, and
// returns the new PageID and a pointer to the frame data.
func (bp *BufferPool) NewPin() (PageID, *Page, error) {
	// Allocate outside the lock (PageFile has its own lock).
	bp.mu.Unlock()
	id, err := bp.pf.Allocate()
	bp.mu.Lock()
	if err != nil {
		return InvalidPage, nil, err
	}

	// Bring into buffer pool (already zero from Allocate).
	fi, err := bp.evict()
	if err != nil {
		return InvalidPage, nil, err
	}
	f := &bp.frames[fi]
	f.pageID = id
	f.pinCount = 1
	f.dirty = true // mark dirty so it gets written on evict
	f.data = Page{}
	bp.pageMap[id] = fi
	f.lruElem = bp.lru.PushFront(&lruEntry{fi})
	return id, &f.data, nil
}

// Unpin decrements the pin count for page id.  If dirty is true, the page is
// marked for write-back on eviction.
func (bp *BufferPool) Unpin(id PageID, dirty bool) error {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	fi, ok := bp.pageMap[id]
	if !ok {
		return fmt.Errorf("BufferPool.Unpin(%d): page not in pool", id)
	}
	f := &bp.frames[fi]
	if f.pinCount == 0 {
		return fmt.Errorf("BufferPool.Unpin(%d): pin count already 0", id)
	}
	f.pinCount--
	if dirty {
		f.dirty = true
	}
	return nil
}

// MarkDirty marks a pinned page dirty without unpinning it.
func (bp *BufferPool) MarkDirty(id PageID) error {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	fi, ok := bp.pageMap[id]
	if !ok {
		return fmt.Errorf("BufferPool.MarkDirty(%d): page not in pool", id)
	}
	bp.frames[fi].dirty = true
	return nil
}

// FlushAll writes all dirty pages back to disk.
func (bp *BufferPool) FlushAll() error {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	for i := range bp.frames {
		f := &bp.frames[i]
		if f.pageID != InvalidPage && f.dirty {
			if err := bp.pf.writePageLocked(f.pageID, &f.data); err != nil {
				return err
			}
			f.dirty = false
		}
	}
	return nil
}

// FlushPage writes a specific dirty page to disk immediately.
func (bp *BufferPool) FlushPage(id PageID) error {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	fi, ok := bp.pageMap[id]
	if !ok {
		return nil // not in pool — already on disk
	}
	f := &bp.frames[fi]
	if f.dirty {
		if err := bp.pf.writePageLocked(f.pageID, &f.data); err != nil {
			return err
		}
		f.dirty = false
	}
	return nil
}

// Stats returns cache hit/miss counters for observability.
func (bp *BufferPool) Stats() PoolStats {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	pinned, dirty := 0, 0
	for i := range bp.frames {
		f := &bp.frames[i]
		if f.pageID != InvalidPage {
			if f.pinCount > 0 {
				pinned++
			}
			if f.dirty {
				dirty++
			}
		}
	}
	return PoolStats{Capacity: bp.capacity, Pinned: pinned, Dirty: dirty}
}

// PoolStats holds observable metrics about the buffer pool.
type PoolStats struct {
	Capacity int
	Pinned   int
	Dirty    int
}

// -------------------------------------------------------------------------
// Internal helpers
// -------------------------------------------------------------------------

// evict finds a free frame or evicts the LRU unpinned page.
// Must be called with bp.mu held.
// Returns the frame index ready for use (lruElem is nil, pageMap entry cleared).
func (bp *BufferPool) evict() (int, error) {
	// Use a free frame first.
	if len(bp.freeList) > 0 {
		fi := bp.freeList[len(bp.freeList)-1]
		bp.freeList = bp.freeList[:len(bp.freeList)-1]
		return fi, nil
	}

	// Walk LRU from back (least recently used) looking for an unpinned frame.
	for elem := bp.lru.Back(); elem != nil; elem = elem.Prev() {
		entry := elem.Value.(*lruEntry)
		fi := entry.frameIdx
		f := &bp.frames[fi]
		if f.pinCount > 0 {
			continue // still in use
		}
		// Write back if dirty.
		if f.dirty {
			if err := bp.pf.writePageLocked(f.pageID, &f.data); err != nil {
				return 0, fmt.Errorf("evict write-back page %d: %w", f.pageID, err)
			}
			f.dirty = false
		}
		// Evict.
		delete(bp.pageMap, f.pageID)
		bp.lru.Remove(elem)
		f.lruElem = nil
		f.pageID = InvalidPage
		return fi, nil
	}

	return 0, fmt.Errorf("BufferPool: all %d frames are pinned — cannot evict", bp.capacity)
}
