# memdb

An in-memory database engine in Go, built incrementally with deep testing and real bugs found at each stage.

Module: `github.com/pandeylakshya207-max/memdb`  
Language: Go 1.22+ · No external dependencies

---

## Week 1 — B-Tree

A generic, ordered B-Tree (`btree` package) with insert, delete, point search, and range scan.

### Design

- **Generic** over `cmp.Ordered` keys and any value type (`BTree[K, V]`)
- **Minimum degree `t`** configurable at construction (default suggestion: `t=3`, giving max 4 keys / 5 children per node; `t=2` is a classic 2-3-4 tree)
- **Insert** — top-down split-on-descent, so no second pass needed
- **Delete** — top-down fill-on-descent (borrow-left, borrow-right, or merge), with correct predecessor/successor replacement for internal-node keys
- **Range scan** — in-order traversal bounded to `[lo, hi]`, short-circuits as soon as `key > hi` or the callback returns `false`
- **Structural invariant checker** (`CheckInvariants`) used by every test after every mutation

### API

```go
bt := btree.New[int, string](3)          // minimum degree 3

bt.Set(42, "hello")                       // insert (returns true) or update (returns false)
v, ok := bt.Get(42)                       // point lookup
bt.Delete(42)                             // returns true if key was present

bt.Range(10, 50, func(k int, v string) bool {
    fmt.Println(k, v)
    return true   // return false to stop early
})

bt.Scan(func(k int, v string) bool { ... })  // full ordered scan

k, v, ok := bt.Min()
k, v, ok = bt.Max()
n := bt.Len()
```

### Tests

31 tests, 93.0% statement coverage, `-race` clean.

| Test | What it covers |
|---|---|
| `TestEmptyTree` | Zero-value behaviour, no-op operations |
| `TestSingleItem` | Insert, Get, Min, Max, Delete of a single key |
| `TestUpdate` | Set returns false on duplicate; value is replaced; Len unchanged |
| `TestInsertSequentialSmall/t={2,3,4,5}` | Ascending inserts across all degrees |
| `TestInsertReverseSequential` | Descending inserts; scan order verified |
| `TestInsertAlternating` | Interleaved low/high keys; stress split paths |
| `TestRootSplit` | Root full → split → new internal root |
| `TestDeleteFromLeaf` | Leaf key removal |
| `TestDeleteAllAscending/Descending` | Full delete in both orders (50 keys) |
| `TestDeleteNonExistent` | Delete returns false; tree unchanged |
| `TestDeleteFromInternalNode` | Separator keys deleted via pred/succ replacement |
| `TestDeleteTriggersTreeHeightReduction` | Tree shrinks height when root empties |
| `TestRangeFullTree` | Range covers entire tree |
| `TestRangeSubset` | Bounded subrange |
| `TestRangeSingleKey` | `[k, k]` returns exactly one item |
| `TestRangeNoMatch` | Range outside all keys → empty |
| `TestRangeInvertedBounds` | `lo > hi` → empty, no panic |
| `TestRangeEarlyStop` | Callback returning false stops iteration |
| `TestRangeGapKeys` | Sparse keys, range hits only the right ones |
| `TestMinMax` | Min/Max on multi-key tree |
| `TestStringKeys` | Generic type parameter with `string` keys |
| `TestRandomInsertDeleteStress/t={2,3,5,8}` | 500 random inserts + random deletes; invariant check after every op |
| `TestRandomRangeQuery` | 100 random range queries cross-validated against reference sorted slice |
| `TestDeleteBorrowFromLeftSibling` | Explicit borrow-left path |
| `TestDeleteBorrowFromRightSibling` | Explicit borrow-right path |
| `TestDeleteMergeSiblings` | Explicit sibling merge path |
| `TestScanEarlyStop` | Scan stops when callback returns false |
| `TestDegree2Exhaustive` | All 720 permutations of [1..6] inserted and fully deleted with `t=2`; invariant checked after every single operation |
| `TestLenTracking` | Len correct through 100 inserts, 100 updates, 50 deletes |
| `TestNilReceiver` | All methods safe on nil `*BTree` |
| `TestDegreeClamp` / `TestDebugString` / `TestCheckInvariantsNil` | Edge cases |

```
go test ./btree/... -race -cover
ok  github.com/pandeylakshya207-max/memdb/btree  1.7s  coverage: 93.0%
```

### Bugs found during testing

**Bug 1 — `deleteFromInternal` case 2c: wrong key deleted after merge**

When both children of an internal-node key had exactly `t-1` keys, we merge them and then call `deleteKey(bt, n.children[i], n.keys[i].Key)`. But after `mergeChildren(bt, n, i)`, the separator at `n.keys[i]` has been pulled _down_ into the merged child and removed from `n.keys` — so `n.keys[i]` is now a _different_ key (the old `n.keys[i+1]`). The fix: capture the original key before the merge and delete that.

```go
// Before (wrong):
mergeChildren(bt, n, i)
return deleteKey(bt, n.children[i], n.keys[i].Key)  // n.keys[i] is now a different key!

// After (correct):
origKey := key.Key   // key captured before mergeChildren
mergeChildren(bt, n, i)
return deleteKey(bt, n.children[i], origKey)
```

Caught by `TestDeleteFromInternalNode`, `TestDegree2Exhaustive`, and the random stress tests.

**Bug 2 — `deleteKey` case 3: wrong child index after last-child merge**

When descending into a child that needs filling and the child is the _last_ child of its parent, `fill` calls `mergeChildren(bt, n, i-1)`. This merges the child into its left sibling at position `i-1` and removes the old `n.children[i]` slot entirely. The old code then called `deleteKey(bt, n.children[i], key)` — but `n.children[i]` no longer exists (or is a completely different child). The fix: after `fill`, if `i > n.nKeys()` the merge happened and the merged node is at `i-1`.

```go
// Before (wrong):
fill(bt, n, i)
if i >= n.nKeys()+1 { i = n.nKeys() }    // still wrong — n.nKeys() is now i-1
return deleteKey(bt, n.children[i], key)

// After (correct):
fill(bt, n, i)
if i > n.nKeys() { i-- }                   // last-child merge: merged node is at i-1
return deleteKey(bt, n.children[i], key)
```

Caught by `TestRandomInsertDeleteStress` (all 4 degrees) and `TestDegree2Exhaustive`.

---

## Planned (Weeks 2–4)

- **Week 2** — Hash index (chained hash table, dynamic rehashing) + concurrent-safe wrapper
- **Week 3** — Storage layer: WAL + page cache (buffer pool) + crash recovery
- **Week 4** — SQL-subset query engine: scan, filter, project, join, simple aggregates
