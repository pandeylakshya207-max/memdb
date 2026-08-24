// Package hashindex implements a concurrent, dynamically-resizing hash table
// using chained buckets.
//
// Design decisions:
//   - Separate chaining: each bucket holds a singly-linked list of entries.
//     Simpler to implement correctly under rehash than open-addressing, and
//     avoids clustering.
//   - Striped locking: the table is divided into NStripes shards, each
//     protected by its own sync.RWMutex.  Reads (Get) take the shard read-lock;
//     writes (Set, Delete) take the shard write-lock.  Rehash takes ALL shard
//     write-locks in index order to avoid deadlock.
//   - Load-factor-triggered rehash: when the global item count exceeds
//     loadFactor * capacity the table doubles.  Shrink triggers at 0.25 * capacity
//     (but never below minBuckets).
//   - FNV-1a hash for string keys; a pluggable Hasher interface for other types.
//   - The zero value of HashMap is not usable; construct with New.
package hashindex

import (
	"sync"
	"sync/atomic"
)

const (
	// NStripes is the number of independently-locked shards.
	// Must be a power of two so shard = bucketIdx & (NStripes-1) works.
	NStripes = 16

	defaultInitBuckets = 16  // must be >= NStripes and a power of two
	maxLoadFactor      = 2.0 // rehash-up threshold: items/buckets
	minLoadFactor      = 0.5 // rehash-down threshold
	minBuckets         = 16  // never shrink below this
)

// entry is one node in a bucket's chain.
type entry[K comparable, V any] struct {
	key   K
	value V
	next  *entry[K, V]
}

// Hasher maps a key to a uint64 hash.  The hash need not be in any particular
// range; the table takes it modulo the bucket count internally.
type Hasher[K comparable] func(key K) uint64

// HashMap is a generic concurrent hash table.
// K must be comparable (Go built-in constraint); V can be anything.
type HashMap[K comparable, V any] struct {
	hasher  Hasher[K]
	stripes [NStripes]sync.RWMutex

	// mu protects the buckets slice pointer and nBuckets during rehash.
	// Normal reads/writes only hold a stripe lock.  Rehash holds mu + all
	// stripe write-locks to swap the backing array atomically.
	mu       sync.Mutex
	buckets  []*entry[K, V]
	nBuckets int

	// count is updated atomically so Len() is O(1) without any lock.
	count atomic.Int64
}

// New returns a new HashMap using the provided hash function.
// initBuckets is a hint for the initial table size; it is rounded up to the
// nearest power of two >= NStripes.
func New[K comparable, V any](hasher Hasher[K], initBuckets int) *HashMap[K, V] {
	nb := nextPow2(initBuckets)
	if nb < defaultInitBuckets {
		nb = defaultInitBuckets
	}
	return &HashMap[K, V]{
		hasher:   hasher,
		buckets:  make([]*entry[K, V], nb),
		nBuckets: nb,
	}
}

// Len returns the number of key-value pairs currently in the map.
func (h *HashMap[K, V]) Len() int {
	return int(h.count.Load())
}

// -------------------------------------------------------------------------
// Shard helpers
// -------------------------------------------------------------------------

// stripe returns the stripe index for a given bucket index.
func stripe(bucketIdx, nBuckets int) int {
	// Distribute evenly: stride = nBuckets / NStripes buckets per shard.
	// Because both nBuckets and NStripes are powers of two, this is exact.
	return bucketIdx / (nBuckets / NStripes)
}

// locate returns (bucket index, stripe index) for a key, given the current
// number of buckets.  Called while holding at least the stripe's read-lock
// (caller already snapped nBuckets).
func (h *HashMap[K, V]) locate(key K, nb int) (int, int) {
	hash := h.hasher(key)
	bi := int(hash) & (nb - 1) // nb is always a power of two
	if bi < 0 {
		bi = -bi
	}
	si := stripe(bi, nb)
	return bi, si
}

// -------------------------------------------------------------------------
// Get
// -------------------------------------------------------------------------

// Get returns the value for key and true if found; zero value and false otherwise.
func (h *HashMap[K, V]) Get(key K) (V, bool) {
	h.mu.Lock()
	buckets := h.buckets
	nb := h.nBuckets
	h.mu.Unlock()

	bi, si := h.locate(key, nb)
	h.stripes[si].RLock()
	e := buckets[bi]
	for e != nil {
		if e.key == key {
			v := e.value
			h.stripes[si].RUnlock()
			return v, true
		}
		e = e.next
	}
	h.stripes[si].RUnlock()
	var zero V
	return zero, false
}

// -------------------------------------------------------------------------
// Set
// -------------------------------------------------------------------------

// Set inserts or updates the value for key.
// Returns true if this was a new insertion, false if an existing key was updated.
func (h *HashMap[K, V]) Set(key K, value V) bool {
	h.mu.Lock()
	buckets := h.buckets
	nb := h.nBuckets
	h.mu.Unlock()

	bi, si := h.locate(key, nb)
	h.stripes[si].Lock()
	e := buckets[bi]
	for e != nil {
		if e.key == key {
			e.value = value
			h.stripes[si].Unlock()
			return false // update
		}
		e = e.next
	}
	// Insert at head of chain.
	buckets[bi] = &entry[K, V]{key: key, value: value, next: buckets[bi]}
	h.stripes[si].Unlock()

	n := h.count.Add(1)
	// Check if rehash is needed (rough check without lock — exact check inside rehash).
	if float64(n) > maxLoadFactor*float64(nb) {
		h.rehash(nb * 2)
	}
	return true
}

// -------------------------------------------------------------------------
// Delete
// -------------------------------------------------------------------------

// Delete removes the key from the map.
// Returns true if the key was found and deleted, false if not present.
func (h *HashMap[K, V]) Delete(key K) bool {
	h.mu.Lock()
	buckets := h.buckets
	nb := h.nBuckets
	h.mu.Unlock()

	bi, si := h.locate(key, nb)
	h.stripes[si].Lock()
	prev := (*entry[K, V])(nil)
	e := buckets[bi]
	for e != nil {
		if e.key == key {
			if prev == nil {
				buckets[bi] = e.next
			} else {
				prev.next = e.next
			}
			h.stripes[si].Unlock()
			n := h.count.Add(-1)
			// Shrink if load falls too low and we're above minBuckets.
			if nb > minBuckets && float64(n) < minLoadFactor*float64(nb)/2 {
				h.rehash(nb / 2)
			}
			return true
		}
		prev = e
		e = e.next
	}
	h.stripes[si].Unlock()
	return false
}

// -------------------------------------------------------------------------
// Scan
// -------------------------------------------------------------------------

// Scan calls fn for every key-value pair in the map in an unspecified order.
// fn returning false stops iteration early.
// Scan takes a consistent snapshot of each stripe (stripe-by-stripe), so it
// is safe under concurrent modifications, but does not provide a
// point-in-time snapshot of the entire map.
func (h *HashMap[K, V]) Scan(fn func(key K, value V) bool) bool {
	h.mu.Lock()
	buckets := h.buckets
	nb := h.nBuckets
	h.mu.Unlock()

	for si := 0; si < NStripes; si++ {
		h.stripes[si].RLock()
		bucketStart := si * (nb / NStripes)
		bucketEnd := bucketStart + (nb / NStripes)
		var stop bool
		for bi := bucketStart; bi < bucketEnd; bi++ {
			for e := buckets[bi]; e != nil; e = e.next {
				if !fn(e.key, e.value) {
					stop = true
					break
				}
			}
			if stop {
				break
			}
		}
		h.stripes[si].RUnlock()
		if stop {
			return false
		}
	}
	return true
}

// -------------------------------------------------------------------------
// Rehash
// -------------------------------------------------------------------------

// rehash rebuilds the table with newSize buckets.
// It acquires all stripe write-locks (in stripe order) to prevent any
// concurrent reads or writes during the swap.
func (h *HashMap[K, V]) rehash(newSize int) {
	newSize = nextPow2(newSize)
	if newSize < minBuckets {
		newSize = minBuckets
	}

	// Acquire the table-level mutex to read and (later) update nBuckets.
	h.mu.Lock()
	oldNB := h.nBuckets
	// Re-check under lock: another goroutine may have already rehashed.
	n := int(h.count.Load())
	if newSize > oldNB && float64(n) <= maxLoadFactor*float64(oldNB) {
		h.mu.Unlock()
		return // no longer needed
	}
	if newSize < oldNB && (newSize < minBuckets || float64(n) >= minLoadFactor*float64(newSize)/2) {
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()

	// Lock all stripes in order (deadlock-free since we always go 0..NStripes-1).
	for si := 0; si < NStripes; si++ {
		h.stripes[si].Lock()
	}

	// Re-read under all locks held.
	h.mu.Lock()
	oldBuckets := h.buckets
	oldNB = h.nBuckets

	// Double-check again — concurrent rehash may have already done it.
	n = int(h.count.Load())
	alreadyDone := false
	if newSize > oldNB && float64(n) <= maxLoadFactor*float64(oldNB) {
		alreadyDone = true
	}
	if newSize < oldNB && (newSize < minBuckets || float64(n) >= minLoadFactor*float64(newSize)/2) {
		alreadyDone = true
	}
	// Also skip if sizes match.
	if newSize == oldNB {
		alreadyDone = true
	}

	if !alreadyDone {
		newBuckets := make([]*entry[K, V], newSize)
		// Redistribute all entries.
		for _, head := range oldBuckets {
			for e := head; e != nil; {
				next := e.next
				bi := int(h.hasher(e.key)) & (newSize - 1)
				if bi < 0 {
					bi = -bi
				}
				e.next = newBuckets[bi]
				newBuckets[bi] = e
				e = next
			}
		}
		h.buckets = newBuckets
		h.nBuckets = newSize
	}
	h.mu.Unlock()

	for si := NStripes - 1; si >= 0; si-- {
		h.stripes[si].Unlock()
	}
}

// -------------------------------------------------------------------------
// Utility
// -------------------------------------------------------------------------

// nextPow2 returns the smallest power of two >= n (minimum 1).
func nextPow2(n int) int {
	if n <= 1 {
		return 1
	}
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	n |= n >> 32
	return n + 1
}
