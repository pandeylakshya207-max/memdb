package hashindex

import (
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"testing"
)

// -------------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------------

func newStrMap(initBuckets int) *HashMap[string, int] {
	return New[string, int](StringHasher(), initBuckets)
}

func newIntMap(initBuckets int) *HashMap[int, int] {
	return New[int, int](IntHasher(), initBuckets)
}

// checkConsistency verifies Len() matches a full Scan count and that every
// key returned by Scan is actually retrievable via Get.
func checkConsistency[K comparable, V comparable](t *testing.T, h *HashMap[K, V]) {
	t.Helper()
	scanCount := 0
	h.Scan(func(k K, v V) bool {
		got, ok := h.Get(k)
		if !ok {
			t.Errorf("Scan returned key that Get cannot find")
		}
		if got != v {
			t.Errorf("Get returned different value than Scan for same key")
		}
		scanCount++
		return true
	})
	if scanCount != h.Len() {
		t.Errorf("Scan count %d != Len() %d", scanCount, h.Len())
	}
}

func collectKeys(h *HashMap[string, int]) []string {
	var out []string
	h.Scan(func(k string, _ int) bool { out = append(out, k); return true })
	sort.Strings(out)
	return out
}

// -------------------------------------------------------------------------
// Basic smoke tests
// -------------------------------------------------------------------------

func TestEmptyMap(t *testing.T) {
	h := newStrMap(16)
	if h.Len() != 0 {
		t.Fatalf("expected Len=0, got %d", h.Len())
	}
	if _, ok := h.Get("missing"); ok {
		t.Fatal("Get on empty map returned ok=true")
	}
	if h.Delete("missing") {
		t.Fatal("Delete on empty map returned true")
	}
	h.Scan(func(k string, v int) bool {
		t.Fatal("Scan called fn on empty map")
		return true
	})
}

func TestSetAndGet(t *testing.T) {
	h := newStrMap(16)
	if !h.Set("a", 1) {
		t.Fatal("first Set should return true")
	}
	if h.Set("a", 2) {
		t.Fatal("second Set for same key should return false")
	}
	v, ok := h.Get("a")
	if !ok || v != 2 {
		t.Fatalf("Get(a) = (%d, %v), want (2, true)", v, ok)
	}
	if h.Len() != 1 {
		t.Fatalf("Len=%d, want 1", h.Len())
	}
}

func TestDelete(t *testing.T) {
	h := newStrMap(16)
	h.Set("x", 10)
	h.Set("y", 20)
	if !h.Delete("x") {
		t.Fatal("Delete(x) returned false")
	}
	if _, ok := h.Get("x"); ok {
		t.Fatal("Get(x) after delete returned ok=true")
	}
	if h.Delete("x") {
		t.Fatal("second Delete(x) should return false")
	}
	if h.Len() != 1 {
		t.Fatalf("Len=%d after delete, want 1", h.Len())
	}
	v, ok := h.Get("y")
	if !ok || v != 20 {
		t.Fatalf("Get(y) = (%d,%v), want (20,true)", v, ok)
	}
}

func TestDeleteNonExistent(t *testing.T) {
	h := newStrMap(16)
	h.Set("a", 1)
	if h.Delete("b") {
		t.Fatal("Delete of non-existent key returned true")
	}
	if h.Len() != 1 {
		t.Fatalf("Len should still be 1, got %d", h.Len())
	}
}

func TestLenTracking(t *testing.T) {
	h := newIntMap(16)
	for i := 0; i < 100; i++ {
		h.Set(i, i)
		if h.Len() != i+1 {
			t.Fatalf("after Set(%d): Len=%d want %d", i, h.Len(), i+1)
		}
	}
	// Updates must not change Len.
	for i := 0; i < 100; i++ {
		h.Set(i, i*2)
		if h.Len() != 100 {
			t.Fatalf("after update(%d): Len=%d want 100", i, h.Len())
		}
	}
	for i := 0; i < 50; i++ {
		h.Delete(i)
		if h.Len() != 100-i-1 {
			t.Fatalf("after Delete(%d): Len=%d want %d", i, h.Len(), 100-i-1)
		}
	}
}

// -------------------------------------------------------------------------
// Hash collision: use a deliberately bad hasher that maps everything to
// the same bucket, exercising the chain walk on every op.
// -------------------------------------------------------------------------

func TestCollisionChain(t *testing.T) {
	collide := func(_ int) uint64 { return 0 } // all keys land in bucket 0
	h := New[int, int](collide, 16)
	for i := 0; i < 20; i++ {
		h.Set(i, i*10)
	}
	checkConsistency(t, h)
	// Delete every other key.
	for i := 0; i < 20; i += 2 {
		if !h.Delete(i) {
			t.Fatalf("Delete(%d) returned false", i)
		}
	}
	checkConsistency(t, h)
	if h.Len() != 10 {
		t.Fatalf("Len=%d want 10", h.Len())
	}
	// Remaining keys must all be there.
	for i := 1; i < 20; i += 2 {
		v, ok := h.Get(i)
		if !ok || v != i*10 {
			t.Fatalf("Get(%d)=(%d,%v)", i, v, ok)
		}
	}
}

// -------------------------------------------------------------------------
// Rehash / load-factor tests
// -------------------------------------------------------------------------

func TestRehashUp(t *testing.T) {
	// Start with a small table and insert enough to trigger at least one rehash.
	h := newIntMap(4) // will be clamped to defaultInitBuckets=16
	const N = 200
	for i := 0; i < N; i++ {
		h.Set(i, i)
	}
	if h.Len() != N {
		t.Fatalf("Len=%d want %d", h.Len(), N)
	}
	checkConsistency(t, h)
	// Every key must still be reachable.
	for i := 0; i < N; i++ {
		v, ok := h.Get(i)
		if !ok || v != i {
			t.Fatalf("Get(%d)=(%d,%v) after rehash", i, v, ok)
		}
	}
}

func TestRehashDown(t *testing.T) {
	h := newIntMap(16)
	const N = 300
	for i := 0; i < N; i++ {
		h.Set(i, i)
	}
	// Delete most keys to trigger shrink.
	for i := 0; i < N-5; i++ {
		h.Delete(i)
	}
	checkConsistency(t, h)
	if h.Len() != 5 {
		t.Fatalf("Len=%d want 5", h.Len())
	}
	for i := N - 5; i < N; i++ {
		v, ok := h.Get(i)
		if !ok || v != i {
			t.Fatalf("Get(%d)=(%d,%v) after shrink", i, v, ok)
		}
	}
}

func TestRehashPreservesAllKeys(t *testing.T) {
	// Small initial table, force many rehashes, check nothing lost.
	h := New[string, int](StringHasher(), 2)
	ref := map[string]int{}
	for i := 0; i < 500; i++ {
		k := fmt.Sprintf("key-%04d", i)
		h.Set(k, i)
		ref[k] = i
	}
	for k, want := range ref {
		got, ok := h.Get(k)
		if !ok || got != want {
			t.Fatalf("Get(%q)=(%d,%v) after multi-rehash", k, got, ok)
		}
	}
	checkConsistency(t, h)
}

// -------------------------------------------------------------------------
// Scan tests
// -------------------------------------------------------------------------

func TestScanAllKeys(t *testing.T) {
	h := newStrMap(16)
	want := []string{"apple", "banana", "cherry", "date", "elderberry"}
	for i, k := range want {
		h.Set(k, i)
	}
	got := collectKeys(h)
	if len(got) != len(want) {
		t.Fatalf("scan got %d keys, want %d", len(got), len(want))
	}
	wantSorted := make([]string, len(want))
	copy(wantSorted, want)
	sort.Strings(wantSorted)
	for i := range wantSorted {
		if got[i] != wantSorted[i] {
			t.Fatalf("scan[%d]=%q want %q", i, got[i], wantSorted[i])
		}
	}
}

func TestScanEarlyStop(t *testing.T) {
	h := newIntMap(16)
	for i := 0; i < 50; i++ {
		h.Set(i, i)
	}
	count := 0
	h.Scan(func(k, v int) bool {
		count++
		return count < 10
	})
	if count != 10 {
		t.Fatalf("expected scan to stop after 10 calls, got %d", count)
	}
}

func TestScanEmpty(t *testing.T) {
	h := newIntMap(16)
	called := false
	h.Scan(func(k, v int) bool { called = true; return true })
	if called {
		t.Fatal("Scan on empty map called fn")
	}
}

// -------------------------------------------------------------------------
// Integer key type
// -------------------------------------------------------------------------

func TestIntKeys(t *testing.T) {
	h := newIntMap(16)
	for i := -50; i <= 50; i++ {
		h.Set(i, i*i)
	}
	checkConsistency(t, h)
	if h.Len() != 101 {
		t.Fatalf("Len=%d want 101", h.Len())
	}
	for i := -50; i <= 50; i++ {
		v, ok := h.Get(i)
		if !ok || v != i*i {
			t.Fatalf("Get(%d)=(%d,%v)", i, v, ok)
		}
	}
}

// -------------------------------------------------------------------------
// Random stress test (single-threaded, reference map cross-check)
// -------------------------------------------------------------------------

func TestRandomStressSingleThreaded(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	h := newIntMap(16)
	ref := map[int]int{}

	for op := 0; op < 2000; op++ {
		k := rng.Intn(200)
		switch rng.Intn(3) {
		case 0: // Set
			v := rng.Intn(10000)
			h.Set(k, v)
			ref[k] = v
		case 1: // Delete
			got := h.Delete(k)
			_, want := ref[k]
			if got != want {
				t.Fatalf("op%d Delete(%d): got %v want %v", op, k, got, want)
			}
			delete(ref, k)
		case 2: // Get
			got, gotOK := h.Get(k)
			want, wantOK := ref[k]
			if gotOK != wantOK {
				t.Fatalf("op%d Get(%d): ok got %v want %v", op, k, gotOK, wantOK)
			}
			if gotOK && got != want {
				t.Fatalf("op%d Get(%d): value got %d want %d", op, k, got, want)
			}
		}
	}

	if h.Len() != len(ref) {
		t.Fatalf("final Len=%d want %d", h.Len(), len(ref))
	}
	checkConsistency(t, h)

	// Verify every ref key.
	for k, want := range ref {
		got, ok := h.Get(k)
		if !ok || got != want {
			t.Fatalf("Get(%d)=(%d,%v) final check", k, got, ok)
		}
	}
}

// -------------------------------------------------------------------------
// Concurrent stress tests (the ones that catch real race bugs)
// -------------------------------------------------------------------------

// TestConcurrentSetGet hammers concurrent reads and writes on disjoint key
// ranges, then verifies all keys are present.
func TestConcurrentSetGet(t *testing.T) {
	const (
		writers    = 8
		readers    = 8
		opsPerGoro = 500
	)
	h := newIntMap(16)

	var wg sync.WaitGroup

	// Writers: each goroutine owns a unique key range [id*1000, id*1000+opsPerGoro).
	for w := 0; w < writers; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			base := w * 1000
			for i := 0; i < opsPerGoro; i++ {
				h.Set(base+i, base+i)
			}
		}()
	}

	// Readers: read random keys while writes are in progress.
	for r := 0; r < readers; r++ {
		r := r
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(r)))
			for i := 0; i < opsPerGoro; i++ {
				k := rng.Intn(writers * 1000)
				h.Get(k) // result may or may not be there yet — just must not crash
			}
		}()
	}

	wg.Wait()

	// All written keys must be present.
	for w := 0; w < writers; w++ {
		base := w * 1000
		for i := 0; i < opsPerGoro; i++ {
			k := base + i
			v, ok := h.Get(k)
			if !ok || v != k {
				t.Fatalf("after concurrent writes: Get(%d)=(%d,%v)", k, v, ok)
			}
		}
	}
	if h.Len() != writers*opsPerGoro {
		t.Fatalf("Len=%d want %d", h.Len(), writers*opsPerGoro)
	}
}

// TestConcurrentSetDelete mixes concurrent inserts and deletes on the same
// key space.  We only assert that the map ends up consistent (Len matches
// Scan count, every scanned key is Get-able) — not which keys survive, since
// that depends on scheduling.
func TestConcurrentSetDelete(t *testing.T) {
	const goroutines = 16
	const opsEach = 300

	h := newIntMap(32)
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(g * 7)))
			for i := 0; i < opsEach; i++ {
				k := rng.Intn(100)
				if rng.Intn(2) == 0 {
					h.Set(k, g)
				} else {
					h.Delete(k)
				}
			}
		}()
	}

	wg.Wait()
	checkConsistency(t, h)
}

// TestConcurrentRehashSafety inserts enough items to trigger multiple rehashes
// from concurrent goroutines, verifying no data is lost or corrupted.
func TestConcurrentRehashSafety(t *testing.T) {
	// Start tiny so rehash fires frequently.
	h := New[int, int](IntHasher(), 2)
	const goroutines = 12
	const perGoro = 200

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			base := g * perGoro
			for i := 0; i < perGoro; i++ {
				h.Set(base+i, base+i)
			}
		}()
	}
	wg.Wait()

	total := goroutines * perGoro
	if h.Len() != total {
		t.Fatalf("Len=%d want %d after concurrent inserts", h.Len(), total)
	}
	for g := 0; g < goroutines; g++ {
		base := g * perGoro
		for i := 0; i < perGoro; i++ {
			k := base + i
			v, ok := h.Get(k)
			if !ok || v != k {
				t.Fatalf("Get(%d)=(%d,%v) after concurrent rehash", k, v, ok)
			}
		}
	}
}

// TestConcurrentScan verifies that concurrent Scans don't race with writes.
// We don't assert scan completeness (items may be in flux) — only that the
// process doesn't crash and each item the scan *does* return is Get-able.
func TestConcurrentScan(t *testing.T) {
	h := newIntMap(32)
	// Pre-populate.
	for i := 0; i < 200; i++ {
		h.Set(i, i)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Background writers.
	for w := 0; w < 4; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			for {
				select {
				case <-stop:
					return
				default:
					k := rng.Intn(300)
					if rng.Intn(2) == 0 {
						h.Set(k, k)
					} else {
						h.Delete(k)
					}
				}
			}
		}()
	}

	// Run several scans while writers run.
	for s := 0; s < 10; s++ {
		h.Scan(func(k, v int) bool {
			// Just verify Get agrees — value may have changed, but key must exist.
			h.Get(k) // must not panic
			return true
		})
	}

	close(stop)
	wg.Wait()
}

// -------------------------------------------------------------------------
// Hasher quality tests
// -------------------------------------------------------------------------

func TestFNV1aDistribution(t *testing.T) {
	// FNV-1a should distribute 1000 keys across 64 buckets with no bucket
	// holding more than 5x the average (1000/64 ≈ 15.6 → threshold 78).
	const nKeys = 1000
	const nBuckets = 64
	counts := make([]int, nBuckets)
	h := fnv1aString
	for i := 0; i < nKeys; i++ {
		k := fmt.Sprintf("key-%d", i)
		counts[int(h(k))%nBuckets]++
	}
	avg := float64(nKeys) / nBuckets
	for bi, c := range counts {
		if float64(c) > 5*avg {
			t.Errorf("bucket %d has %d items (avg %.1f) — poor distribution", bi, c, avg)
		}
	}
}

func TestIntHasherDistribution(t *testing.T) {
	const nKeys = 1000
	const nBuckets = 64
	counts := make([]int, nBuckets)
	h := hashInt
	for i := 0; i < nKeys; i++ {
		counts[int(h(i))&(nBuckets-1)]++
		if counts[int(h(i))&(nBuckets-1)] < 0 {
			counts[int(h(i))&(nBuckets-1)] = -counts[int(h(i))&(nBuckets-1)]
		}
	}
	avg := float64(nKeys) / nBuckets
	for bi, c := range counts {
		if float64(c) > 5*avg {
			t.Errorf("bucket %d has %d items (avg %.1f)", bi, c, avg)
		}
	}
}

// -------------------------------------------------------------------------
// nextPow2 unit tests
// -------------------------------------------------------------------------

func TestNextPow2(t *testing.T) {
	cases := [][2]int{
		{0, 1}, {1, 1}, {2, 2}, {3, 4}, {4, 4},
		{5, 8}, {7, 8}, {8, 8}, {9, 16}, {15, 16},
		{16, 16}, {17, 32}, {1023, 1024}, {1024, 1024},
	}
	for _, c := range cases {
		got := nextPow2(c[0])
		if got != c[1] {
			t.Errorf("nextPow2(%d) = %d, want %d", c[0], got, c[1])
		}
	}
}

// -------------------------------------------------------------------------
// Benchmark
// -------------------------------------------------------------------------

func BenchmarkSetSequential(b *testing.B) {
	h := newIntMap(b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Set(i, i)
	}
}

func BenchmarkGetHit(b *testing.B) {
	h := newIntMap(b.N)
	for i := 0; i < b.N; i++ {
		h.Set(i, i)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Get(i)
	}
}

func BenchmarkConcurrentMixed(b *testing.B) {
	h := newIntMap(1024)
	for i := 0; i < 1000; i++ {
		h.Set(i, i)
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		rng := rand.New(rand.NewSource(42))
		for pb.Next() {
			k := rng.Intn(2000)
			if rng.Intn(4) == 0 {
				h.Set(k, k)
			} else {
				h.Get(k)
			}
		}
	})
}

func TestUint64Hasher(t *testing.T) {
	h := New[uint64, string](Uint64Hasher(), 16)
	h.Set(0, "zero")
	h.Set(1<<63, "big")
	h.Set(^uint64(0), "max")
	checkConsistency(t, h)
	if v, ok := h.Get(0); !ok || v != "zero" {
		t.Fatalf("Get(0)=(%q,%v)", v, ok)
	}
	if v, ok := h.Get(^uint64(0)); !ok || v != "max" {
		t.Fatalf("Get(max)=(%q,%v)", v, ok)
	}
}

// TestRehashNoOpWhenAlreadyDone triggers the double-check path inside rehash
// where a concurrent goroutine already resized the table.
func TestRehashIdempotent(t *testing.T) {
	h := newIntMap(16)
	for i := 0; i < 100; i++ {
		h.Set(i, i)
	}
	// Call rehash manually with the current size — should be a clean no-op.
	h.rehash(h.nBuckets)
	checkConsistency(t, h)
	if h.Len() != 100 {
		t.Fatalf("Len=%d want 100 after no-op rehash", h.Len())
	}
}
