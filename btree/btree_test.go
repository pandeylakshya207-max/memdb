package btree

import (
	"cmp"
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

// -------------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------------

func mustCheck[K cmp.Ordered, V any](t *testing.T, bt *BTree[K, V]) {
	t.Helper()
	if err := CheckInvariants(bt); err != nil {
		t.Fatalf("invariant violation: %v\n%s", err, DebugString(bt))
	}
}

func newIntTree(degree int) *BTree[int, int] { return New[int, int](degree) }

func insertAll(t *testing.T, bt *BTree[int, int], keys []int) {
	t.Helper()
	for _, k := range keys {
		bt.Set(k, k*10)
		mustCheck(t, bt)
	}
}

func deleteAll(t *testing.T, bt *BTree[int, int], keys []int) {
	t.Helper()
	for _, k := range keys {
		if !bt.Delete(k) {
			t.Fatalf("Delete(%d) returned false but key should exist", k)
		}
		mustCheck(t, bt)
	}
}

func collectScan(bt *BTree[int, int]) []int {
	var out []int
	bt.Scan(func(k, _ int) bool { out = append(out, k); return true })
	return out
}

func collectRange(bt *BTree[int, int], lo, hi int) []int {
	var out []int
	bt.Range(lo, hi, func(k, _ int) bool { out = append(out, k); return true })
	return out
}

func sortedUniq(s []int) []int {
	m := map[int]struct{}{}
	for _, v := range s {
		m[v] = struct{}{}
	}
	out := make([]int, 0, len(m))
	for v := range m {
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}

func treeHeight[K cmp.Ordered, V any](n *node[K, V]) int {
	if n == nil || n.leaf {
		return 1
	}
	return 1 + treeHeight(n.children[0])
}

// -------------------------------------------------------------------------
// Basic smoke tests
// -------------------------------------------------------------------------

func TestEmptyTree(t *testing.T) {
	bt := newIntTree(3)
	mustCheck(t, bt)
	if bt.Len() != 0 {
		t.Fatalf("expected len=0, got %d", bt.Len())
	}
	if _, ok := bt.Get(42); ok {
		t.Fatal("Get on empty tree returned ok=true")
	}
	if bt.Delete(1) {
		t.Fatal("Delete on empty tree returned true")
	}
	if k, _, ok := bt.Min(); ok {
		t.Fatalf("Min on empty returned ok=true, k=%v", k)
	}
	if k, _, ok := bt.Max(); ok {
		t.Fatalf("Max on empty returned ok=true, k=%v", k)
	}
	bt.Scan(func(k, v int) bool { t.Fatalf("Scan called on empty"); return true })
	bt.Range(0, 100, func(k, v int) bool { t.Fatalf("Range called on empty"); return true })
}

func TestSingleItem(t *testing.T) {
	bt := newIntTree(2)
	bt.Set(7, 77)
	mustCheck(t, bt)
	v, ok := bt.Get(7)
	if !ok || v != 77 {
		t.Fatalf("Get(7): got (%d, %v), want (77, true)", v, ok)
	}
	k, val, ok := bt.Min()
	if !ok || k != 7 || val != 77 {
		t.Fatalf("Min: got (%v,%v,%v), want (7,77,true)", k, val, ok)
	}
	k, val, ok = bt.Max()
	if !ok || k != 7 || val != 77 {
		t.Fatalf("Max: got (%v,%v,%v), want (7,77,true)", k, val, ok)
	}
	if !bt.Delete(7) {
		t.Fatal("Delete(7) returned false")
	}
	mustCheck(t, bt)
	if bt.Len() != 0 {
		t.Fatalf("after delete, len=%d", bt.Len())
	}
}

func TestUpdate(t *testing.T) {
	bt := newIntTree(3)
	if !bt.Set(5, 50) {
		t.Fatal("first Set should return true (insert)")
	}
	if bt.Set(5, 99) {
		t.Fatal("second Set for same key should return false (update)")
	}
	v, ok := bt.Get(5)
	if !ok || v != 99 {
		t.Fatalf("after update: Get(5) = (%d,%v), want (99,true)", v, ok)
	}
	if bt.Len() != 1 {
		t.Fatalf("len should be 1 after insert+update, got %d", bt.Len())
	}
	mustCheck(t, bt)
}

// -------------------------------------------------------------------------
// Insert tests
// -------------------------------------------------------------------------

func TestInsertSequentialSmall(t *testing.T) {
	for _, deg := range []int{2, 3, 4, 5} {
		t.Run(fmt.Sprintf("t=%d", deg), func(t *testing.T) {
			bt := newIntTree(deg)
			keys := make([]int, 20)
			for i := range keys {
				keys[i] = i + 1
			}
			insertAll(t, bt, keys)
			if bt.Len() != len(keys) {
				t.Fatalf("len=%d, want %d", bt.Len(), len(keys))
			}
			for _, k := range keys {
				v, ok := bt.Get(k)
				if !ok || v != k*10 {
					t.Fatalf("Get(%d) = (%d,%v)", k, v, ok)
				}
			}
		})
	}
}

func TestInsertReverseSequential(t *testing.T) {
	bt := newIntTree(3)
	for i := 100; i >= 1; i-- {
		bt.Set(i, i)
		mustCheck(t, bt)
	}
	got := collectScan(bt)
	for i, k := range got {
		if k != i+1 {
			t.Fatalf("scan order wrong at index %d: got %d", i, k)
		}
	}
}

func TestInsertAlternating(t *testing.T) {
	bt := newIntTree(2)
	vals := []int{50, 1, 99, 25, 75, 10, 90, 40, 60}
	insertAll(t, bt, vals)
	got := collectScan(bt)
	want := sortedUniq(vals)
	if len(got) != len(want) {
		t.Fatalf("scan len %d != %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scan[%d]: got %d want %d", i, got[i], want[i])
		}
	}
}

func TestRootSplit(t *testing.T) {
	bt := newIntTree(2)
	for _, k := range []int{10, 20, 30} {
		bt.Set(k, k)
	}
	bt.Set(5, 5)
	mustCheck(t, bt)
	if bt.root.leaf {
		t.Fatal("root should be internal after split")
	}
}

// -------------------------------------------------------------------------
// Delete tests
// -------------------------------------------------------------------------

func TestDeleteFromLeaf(t *testing.T) {
	bt := newIntTree(3)
	insertAll(t, bt, []int{1, 2, 3, 4, 5})
	deleteAll(t, bt, []int{3, 1, 5})
	got := collectScan(bt)
	if len(got) != 2 || got[0] != 2 || got[1] != 4 {
		t.Fatalf("after delete, scan = %v, want [2 4]", got)
	}
}

func TestDeleteAllAscending(t *testing.T) {
	bt := newIntTree(3)
	n := 50
	keys := make([]int, n)
	for i := range keys {
		keys[i] = i + 1
	}
	insertAll(t, bt, keys)
	deleteAll(t, bt, keys)
	mustCheck(t, bt)
	if bt.Len() != 0 {
		t.Fatalf("tree should be empty, len=%d", bt.Len())
	}
}

func TestDeleteAllDescending(t *testing.T) {
	bt := newIntTree(3)
	n := 50
	keys := make([]int, n)
	for i := range keys {
		keys[i] = i + 1
	}
	insertAll(t, bt, keys)
	rev := make([]int, n)
	for i, k := range keys {
		rev[n-1-i] = k
	}
	deleteAll(t, bt, rev)
	mustCheck(t, bt)
	if bt.Len() != 0 {
		t.Fatalf("tree should be empty, len=%d", bt.Len())
	}
}

func TestDeleteNonExistent(t *testing.T) {
	bt := newIntTree(3)
	insertAll(t, bt, []int{10, 20, 30})
	if bt.Delete(99) {
		t.Fatal("Delete(99) returned true but 99 was never inserted")
	}
	mustCheck(t, bt)
	if bt.Len() != 3 {
		t.Fatalf("len should still be 3, got %d", bt.Len())
	}
}

func TestDeleteFromInternalNode(t *testing.T) {
	bt := newIntTree(2)
	keys := []int{10, 20, 30, 40, 50, 60, 70}
	insertAll(t, bt, keys)
	for _, k := range []int{20, 40, 60} {
		bt.Delete(k)
		mustCheck(t, bt)
	}
	got := collectScan(bt)
	want := []int{10, 30, 50, 70}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scan[%d]: got %d want %d", i, got[i], want[i])
		}
	}
}

func TestDeleteTriggersTreeHeightReduction(t *testing.T) {
	bt := newIntTree(2)
	for i := 1; i <= 15; i++ {
		bt.Set(i, i)
	}
	depth := treeHeight(bt.root)
	if depth < 2 {
		t.Fatalf("expected height >= 2 after 15 inserts with t=2, got %d", depth)
	}
	for i := 2; i <= 15; i++ {
		bt.Delete(i)
		mustCheck(t, bt)
	}
	if !bt.root.leaf {
		t.Fatal("root should be a leaf after deleting down to 1 element")
	}
	if bt.Len() != 1 {
		t.Fatalf("len=%d, want 1", bt.Len())
	}
}

// -------------------------------------------------------------------------
// Range scan tests
// -------------------------------------------------------------------------

func TestRangeFullTree(t *testing.T) {
	bt := newIntTree(3)
	for i := 1; i <= 30; i++ {
		bt.Set(i, i*10)
	}
	got := collectRange(bt, 1, 30)
	if len(got) != 30 {
		t.Fatalf("expected 30 results, got %d: %v", len(got), got)
	}
	for i, k := range got {
		if k != i+1 {
			t.Fatalf("range[%d]=%d, want %d", i, k, i+1)
		}
	}
}

func TestRangeSubset(t *testing.T) {
	bt := newIntTree(3)
	for i := 1; i <= 20; i++ {
		bt.Set(i, i)
	}
	got := collectRange(bt, 5, 15)
	want := []int{5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("range[%d]=%d, want %d", i, got[i], want[i])
		}
	}
}

func TestRangeSingleKey(t *testing.T) {
	bt := newIntTree(3)
	for i := 1; i <= 10; i++ {
		bt.Set(i, i)
	}
	got := collectRange(bt, 7, 7)
	if len(got) != 1 || got[0] != 7 {
		t.Fatalf("range [7,7] = %v, want [7]", got)
	}
}

func TestRangeNoMatch(t *testing.T) {
	bt := newIntTree(3)
	for i := 1; i <= 10; i++ {
		bt.Set(i, i)
	}
	got := collectRange(bt, 50, 100)
	if len(got) != 0 {
		t.Fatalf("expected empty range, got %v", got)
	}
}

func TestRangeInvertedBounds(t *testing.T) {
	bt := newIntTree(3)
	for i := 1; i <= 10; i++ {
		bt.Set(i, i)
	}
	got := collectRange(bt, 8, 3)
	if len(got) != 0 {
		t.Fatalf("inverted range should be empty, got %v", got)
	}
}

func TestRangeEarlyStop(t *testing.T) {
	bt := newIntTree(3)
	for i := 1; i <= 20; i++ {
		bt.Set(i, i)
	}
	var got []int
	bt.Range(1, 20, func(k, v int) bool {
		got = append(got, k)
		return k < 5
	})
	if len(got) != 5 {
		t.Fatalf("expected 5 items before early stop, got %d: %v", len(got), got)
	}
	if got[4] != 5 {
		t.Fatalf("last item should be 5, got %d", got[4])
	}
}

func TestRangeGapKeys(t *testing.T) {
	bt := newIntTree(3)
	for _, k := range []int{2, 5, 8, 11, 14, 17, 20} {
		bt.Set(k, k)
	}
	got := collectRange(bt, 3, 15)
	want := []int{5, 8, 11, 14}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("[%d] got %d want %d", i, got[i], want[i])
		}
	}
}

// -------------------------------------------------------------------------
// Min / Max
// -------------------------------------------------------------------------

func TestMinMax(t *testing.T) {
	bt := newIntTree(3)
	insertAll(t, bt, []int{5, 3, 8, 1, 9, 4, 7, 2, 6})
	if k, _, ok := bt.Min(); !ok || k != 1 {
		t.Fatalf("Min = (%v,%v), want 1", k, ok)
	}
	if k, _, ok := bt.Max(); !ok || k != 9 {
		t.Fatalf("Max = (%v,%v), want 9", k, ok)
	}
}

// -------------------------------------------------------------------------
// String keys
// -------------------------------------------------------------------------

func TestStringKeys(t *testing.T) {
	bt := New[string, int](3)
	words := []string{"banana", "apple", "cherry", "date", "elderberry", "fig", "grape"}
	for i, w := range words {
		bt.Set(w, i)
	}
	if err := CheckInvariants(bt); err != nil {
		t.Fatalf("invariant: %v", err)
	}
	var got []string
	bt.Scan(func(k string, _ int) bool { got = append(got, k); return true })
	sorted := make([]string, len(words))
	copy(sorted, words)
	sort.Strings(sorted)
	for i := range sorted {
		if got[i] != sorted[i] {
			t.Fatalf("scan[%d]=%q want %q", i, got[i], sorted[i])
		}
	}
}

// -------------------------------------------------------------------------
// Large random stress test
// -------------------------------------------------------------------------

func TestRandomInsertDeleteStress(t *testing.T) {
	const N = 500
	for _, deg := range []int{2, 3, 5, 8} {
		t.Run(fmt.Sprintf("t=%d", deg), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(deg) * 42))
			bt := newIntTree(deg)
			present := map[int]bool{}

			for i := 0; i < N; i++ {
				k := rng.Intn(N / 2)
				bt.Set(k, k*10)
				present[k] = true
				mustCheck(t, bt)
			}

			for k := range present {
				v, ok := bt.Get(k)
				if !ok || v != k*10 {
					t.Fatalf("Get(%d) = (%d,%v) after insert phase", k, v, ok)
				}
			}
			if bt.Len() != len(present) {
				t.Fatalf("len=%d, want %d", bt.Len(), len(present))
			}

			keys := make([]int, 0, len(present))
			for k := range present {
				keys = append(keys, k)
			}
			rng.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
			for _, k := range keys[:len(keys)/2] {
				if !bt.Delete(k) {
					t.Fatalf("Delete(%d) returned false", k)
				}
				delete(present, k)
				mustCheck(t, bt)
			}

			for k := range present {
				if _, ok := bt.Get(k); !ok {
					t.Fatalf("key %d lost after delete phase", k)
				}
			}
			got := collectScan(bt)
			want := sortedUniq(func() []int {
				out := make([]int, 0, len(present))
				for k := range present {
					out = append(out, k)
				}
				return out
			}())
			if len(got) != len(want) {
				t.Fatalf("scan len %d != %d", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("scan[%d] got %d want %d", i, got[i], want[i])
				}
			}
		})
	}
}

// -------------------------------------------------------------------------
// Range scan cross-validated against reference sorted slice
// -------------------------------------------------------------------------

func TestRandomRangeQuery(t *testing.T) {
	rng := rand.New(rand.NewSource(777))
	bt := newIntTree(3)
	ref := map[int]bool{}

	for i := 0; i < 200; i++ {
		k := rng.Intn(300)
		bt.Set(k, k)
		ref[k] = true
	}
	mustCheck(t, bt)

	sorted := sortedUniq(func() []int {
		out := make([]int, 0, len(ref))
		for k := range ref {
			out = append(out, k)
		}
		return out
	}())

	for trial := 0; trial < 100; trial++ {
		lo := rng.Intn(300)
		hi := lo + rng.Intn(100)
		got := collectRange(bt, lo, hi)
		var want []int
		for _, k := range sorted {
			if k >= lo && k <= hi {
				want = append(want, k)
			}
		}
		if len(got) != len(want) {
			t.Fatalf("trial %d Range(%d,%d): got %v, want %v", trial, lo, hi, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("trial %d Range(%d,%d): got[%d]=%d want %d", trial, lo, hi, i, got[i], want[i])
			}
		}
	}
}

// -------------------------------------------------------------------------
// Specific delete path coverage
// -------------------------------------------------------------------------

func TestDeleteBorrowFromLeftSibling(t *testing.T) {
	bt := newIntTree(2)
	for _, k := range []int{10, 20, 30, 40, 50, 60, 70, 80} {
		bt.Set(k, k)
	}
	mustCheck(t, bt)
	for _, k := range []int{70, 80} {
		bt.Delete(k)
		mustCheck(t, bt)
	}
}

func TestDeleteBorrowFromRightSibling(t *testing.T) {
	bt := newIntTree(2)
	for _, k := range []int{10, 20, 30, 40, 50, 60, 70, 80} {
		bt.Set(k, k)
	}
	mustCheck(t, bt)
	for _, k := range []int{10, 20} {
		bt.Delete(k)
		mustCheck(t, bt)
	}
}

func TestDeleteMergeSiblings(t *testing.T) {
	bt := newIntTree(2)
	for _, k := range []int{10, 20, 30, 40, 50, 60, 70} {
		bt.Set(k, k)
	}
	mustCheck(t, bt)
	for _, k := range []int{10, 20, 30} {
		bt.Delete(k)
		mustCheck(t, bt)
	}
	got := collectScan(bt)
	want := []int{40, 50, 60, 70}
	if len(got) != len(want) {
		t.Fatalf("after merges: got %v want %v", got, want)
	}
}

// -------------------------------------------------------------------------
// Scan early stop
// -------------------------------------------------------------------------

func TestScanEarlyStop(t *testing.T) {
	bt := newIntTree(3)
	for i := 1; i <= 10; i++ {
		bt.Set(i, i)
	}
	count := 0
	bt.Scan(func(k, v int) bool {
		count++
		return count < 3
	})
	if count != 3 {
		t.Fatalf("expected scan to stop after 3 calls, got %d", count)
	}
}

// -------------------------------------------------------------------------
// Exhaustive permutation test (small N, all insert orders)
// -------------------------------------------------------------------------

func TestDegree2Exhaustive(t *testing.T) {
	const N = 6
	perm := make([]int, N)
	for i := range perm {
		perm[i] = i + 1
	}
	count := 0
	for p := range permutations(perm) {
		bt := newIntTree(2)
		for _, k := range p {
			bt.Set(k, k*100)
			mustCheck(t, bt)
		}
		for k := 1; k <= N; k++ {
			v, ok := bt.Get(k)
			if !ok || v != k*100 {
				t.Fatalf("perm=%v Get(%d)=(%d,%v)", p, k, v, ok)
			}
		}
		for _, k := range p {
			if !bt.Delete(k) {
				t.Fatalf("perm=%v Delete(%d) returned false", p, k)
			}
			mustCheck(t, bt)
		}
		if bt.Len() != 0 {
			t.Fatalf("perm=%v len=%d after full delete", p, bt.Len())
		}
		count++
	}
	t.Logf("exhaustive: verified %d permutations of [1..%d]", count, N)
}

func permutations(arr []int) chan []int {
	ch := make(chan []int)
	go func() {
		a := make([]int, len(arr))
		copy(a, arr)
		generate(len(a), a, ch)
		close(ch)
	}()
	return ch
}

func generate(k int, a []int, ch chan []int) {
	if k == 1 {
		tmp := make([]int, len(a))
		copy(tmp, a)
		ch <- tmp
		return
	}
	for i := 0; i < k; i++ {
		generate(k-1, a, ch)
		if k%2 == 0 {
			a[i], a[k-1] = a[k-1], a[i]
		} else {
			a[0], a[k-1] = a[k-1], a[0]
		}
	}
}

// -------------------------------------------------------------------------
// Len tracking
// -------------------------------------------------------------------------

func TestLenTracking(t *testing.T) {
	bt := newIntTree(3)
	for i := 0; i < 100; i++ {
		bt.Set(i, i)
		if bt.Len() != i+1 {
			t.Fatalf("after Set(%d): len=%d, want %d", i, bt.Len(), i+1)
		}
	}
	for i := 0; i < 100; i++ {
		bt.Set(i, i*2)
		if bt.Len() != 100 {
			t.Fatalf("after update(%d): len=%d, want 100", i, bt.Len())
		}
	}
	for i := 0; i < 50; i++ {
		bt.Delete(i)
		if bt.Len() != 100-i-1 {
			t.Fatalf("after Delete(%d): len=%d, want %d", i, bt.Len(), 100-i-1)
		}
	}
}

// -------------------------------------------------------------------------
// Nil-receiver and edge-case coverage
// -------------------------------------------------------------------------

func TestNilReceiver(t *testing.T) {
	var bt *BTree[int, int]
	if bt.Len() != 0 {
		t.Fatal("nil Len should be 0")
	}
	if _, ok := bt.Get(1); ok {
		t.Fatal("nil Get should return false")
	}
	if bt.Delete(1) {
		t.Fatal("nil Delete should return false")
	}
	if _, _, ok := bt.Min(); ok {
		t.Fatal("nil Min should return false")
	}
	if _, _, ok := bt.Max(); ok {
		t.Fatal("nil Max should return false")
	}
	if !bt.Scan(func(k, v int) bool { return true }) {
		t.Fatal("nil Scan should return true")
	}
	if !bt.Range(0, 10, func(k, v int) bool { return true }) {
		t.Fatal("nil Range should return true")
	}
}

func TestDegreeClamp(t *testing.T) {
	// Degrees below 2 are clamped to 2.
	bt := New[int, int](0)
	bt.Set(1, 1)
	bt.Set(2, 2)
	bt.Set(3, 3)
	mustCheck(t, bt)
	bt2 := New[int, int](1)
	for i := 1; i <= 10; i++ {
		bt2.Set(i, i)
		mustCheck(t, bt2)
	}
}

func TestDebugString(t *testing.T) {
	bt := newIntTree(3)
	for i := 1; i <= 5; i++ {
		bt.Set(i, i)
	}
	s := DebugString(bt)
	if s == "" {
		t.Fatal("DebugString returned empty string")
	}
	// Must mention the root.
	if len(s) < 10 {
		t.Fatalf("DebugString suspiciously short: %q", s)
	}
	// Nil tree.
	ns := DebugString[int, int](nil)
	if ns == "" {
		t.Fatal("DebugString(nil) returned empty")
	}
}

func TestCheckInvariantsNil(t *testing.T) {
	if err := CheckInvariants[int, int](nil); err == nil {
		t.Fatal("CheckInvariants(nil) should return error")
	}
}
