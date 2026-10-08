// Package hashindex implements a concurrent, dynamically-resizing hash table
// using chained buckets.
//
// Design decisions:
//   - Separate chaining: each bucket holds a singly-linked list of entries.
//   - Striped locking: the table is divided into NStripes shards, each
//     protected by its own sync.RWMutex. Get takes the shard read-lock; Set
//     and Delete take the shard write-lock. Rehash takes every shard
//     write-lock, in index order, before it replaces the backing array.
//   - Generation check: an operation reads the backing array first and locks
//     its stripe second, so a rehash can slip in between. Each operation
//     therefore re-checks the table generation once it holds the stripe lock
//     and retries if the table was replaced.
//   - Load-factor-triggered rehash: the table doubles when items/buckets
//     exceeds maxLoadFactor and halves when it falls low enough, never below
//     minBuckets.
//   - A pluggable Hasher maps keys to uint64 hashes.
//   - The zero value of HashMap is not usable; construct with New.
package hashindex

import (
	"sync"
	"sync/atomic"
)

const (
	// NStripes is the number of independently-locked shards.
	// Must be a power of two.
	NStripes = 16

	defaultInitBuckets = 16  // must be >= NStripes and a power of two
	maxLoadFactor      = 2.0 // rehash-up threshold: items/buckets
	minLoadFactor      = 0.5 // rehash-down threshold
	minBuckets         = 16  // never shrink below this
)

// entry is one node in a bucket chain.
type entry[K comparable, V any] struct {
	key   K
	value V
	next  *entry[K, V]
}

// Hasher maps a key to a uint64 hash. The table reduces it to a bucket index.
type Hasher[K comparable] func(key K) uint64

// HashMap is a generic concurrent hash table.
type HashMap[K comparable, V any] struct {
	hasher  Hasher[K]
	stripes [NStripes]sync.RWMutex

	// mu protects buckets and nBuckets. Rehash replaces them while holding mu
	// and every stripe write-lock.
	mu       sync.Mutex
	buckets  []*entry[K, V]
	nBuckets int

	// gen counts how many times rehash has replaced the backing array. It only
	// changes while every stripe write-lock is held.
	gen atomic.Uint64

	// count is updated atomically so Len is O(1) without any lock.
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

// stripe returns the stripe index for a given bucket index.
func stripe(bucketIdx, nBuckets int) int {
	return bucketIdx / (nBuckets / NStripes)
}

// locate returns (bucket index, stripe index) for a key in a table of nb buckets.
func (h *HashMap[K, V]) locate(key K, nb int) (int, int) {
	hash := h.hasher(key)
	bi := int(hash) & (nb - 1) // nb is always a power of two
	if bi < 0 {
		bi = -bi
	}
	return bi, stripe(bi, nb)
}

// snapshot returns the current backing array, its size and its generation.
func (h *HashMap[K, V]) snapshot() ([]*entry[K, V], int, uint64) {
	h.mu.Lock()
	buckets, nb, gen := h.buckets, h.nBuckets, h.gen.Load()
	h.mu.Unlock()
	return buckets, nb, gen
}

// acquire locks the stripe that owns key and returns the table it locked
// against, with the bucket and stripe index for key.
//
// A rehash can replace the table between reading it and taking the stripe
// lock. Operating on the old array would lose writes and miss keys, so
// acquire re-checks the generation once the lock is held and retries if it
// changed. Rehash needs every stripe lock to replace the table, so after the
// check passes the table cannot change until the caller releases the stripe.
func (h *HashMap[K, V]) acquire(key K, write bool) ([]*entry[K, V], int, int, int) {
	for {
		buckets, nb, gen := h.snapshot()
		bi, si := h.locate(key, nb)
		if write {
			h.stripes[si].Lock()
		} else {
			h.stripes[si].RLock()
		}
		if h.gen.Load() == gen {
			return buckets, nb, bi, si
		}
		if write {
			h.stripes[si].Unlock()
		} else {
			h.stripes[si].RUnlock()
		}
	}
}

// Get returns the value for key and true if found; zero value and false otherwise.
func (h *HashMap[K, V]) Get(key K) (V, bool) {
	buckets, _, bi, si := h.acquire(key, false)
	defer h.stripes[si].RUnlock()
	for e := buckets[bi]; e != nil; e = e.next {
		if e.key == key {
			return e.value, true
		}
	}
	var zero V
	return zero, false
}

// Set inserts or updates the value for key.
// Returns true if this was a new insertion, false if an existing key was updated.
func (h *HashMap[K, V]) Set(key K, value V) bool {
	buckets, nb, bi, si := h.acquire(key, true)
	for e := buckets[bi]; e != nil; e = e.next {
		if e.key == key {
			e.value = value
			h.stripes[si].Unlock()
			return false
		}
	}
	buckets[bi] = &entry[K, V]{key: key, value: value, next: buckets[bi]}
	n := h.count.Add(1)
	h.stripes[si].Unlock()

	// Rough check without a lock; rehash repeats it under its own locks.
	if float64(n) > maxLoadFactor*float64(nb) {
		h.rehash(nb * 2)
	}
	return true
}

// Delete removes the key from the map.
// Returns true if the key was found and deleted, false if not present.
func (h *HashMap[K, V]) Delete(key K) bool {
	buckets, nb, bi, si := h.acquire(key, true)
	var prev *entry[K, V]
	for e := buckets[bi]; e != nil; prev, e = e, e.next {
		if e.key != key {
			continue
		}
		if prev == nil {
			buckets[bi] = e.next
		} else {
			prev.next = e.next
		}
		n := h.count.Add(-1)
		h.stripes[si].Unlock()
		if nb > minBuckets && float64(n) < minLoadFactor*float64(nb)/2 {
			h.rehash(nb / 2)
		}
		return true
	}
	h.stripes[si].Unlock()
	return false
}

// Scan calls fn for every key-value pair in the map in an unspecified order.
// fn returning false stops iteration early.
//
// Scan copies the contents while holding every stripe read-lock, releases the
// locks, and then calls fn on the copy. It therefore sees a point-in-time
// view of the map, and fn may safely call back into the same map. The cost is
// one temporary copy of the keys and values per Scan.
func (h *HashMap[K, V]) Scan(fn func(key K, value V) bool) bool {
	for si := 0; si < NStripes; si++ {
		h.stripes[si].RLock()
	}
	buckets, _, _ := h.snapshot()
	n := h.Len()
	if n < 0 {
		n = 0
	}
	keys := make([]K, 0, n)
	values := make([]V, 0, n)
	for _, head := range buckets {
		for e := head; e != nil; e = e.next {
			keys = append(keys, e.key)
			values = append(values, e.value)
		}
	}
	for si := NStripes - 1; si >= 0; si-- {
		h.stripes[si].RUnlock()
	}

	for i := range keys {
		if !fn(keys[i], values[i]) {
			return false
		}
	}
	return true
}

// rehash rebuilds the table with newSize buckets.
// It acquires every stripe write-lock, in stripe order, so that no read or
// write can run while the backing array is replaced.
func (h *HashMap[K, V]) rehash(newSize int) {
	newSize = nextPow2(newSize)
	if newSize < minBuckets {
		newSize = minBuckets
	}

	// Cheap check first: another goroutine may already have rehashed.
	h.mu.Lock()
	oldNB := h.nBuckets
	n := int(h.count.Load())
	if newSize > oldNB && float64(n) <= maxLoadFactor*float64(oldNB) {
		h.mu.Unlock()
		return
	}
	if newSize < oldNB && (newSize < minBuckets || float64(n) >= minLoadFactor*float64(newSize)/2) {
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()

	for si := 0; si < NStripes; si++ {
		h.stripes[si].Lock()
	}

	// Check again with every stripe held.
	h.mu.Lock()
	oldBuckets := h.buckets
	oldNB = h.nBuckets
	n = int(h.count.Load())
	alreadyDone := newSize == oldNB
	if newSize > oldNB && float64(n) <= maxLoadFactor*float64(oldNB) {
		alreadyDone = true
	}
	if newSize < oldNB && (newSize < minBuckets || float64(n) >= minLoadFactor*float64(newSize)/2) {
		alreadyDone = true
	}

	if !alreadyDone {
		newBuckets := make([]*entry[K, V], newSize)
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
		h.gen.Add(1)
	}
	h.mu.Unlock()

	for si := NStripes - 1; si >= 0; si-- {
		h.stripes[si].Unlock()
	}
}

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
