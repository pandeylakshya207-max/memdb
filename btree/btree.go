// Package btree implements an in-memory B-Tree with generic keys and values.
// It supports insert, delete, point search, and ordered range scans.
// The tree uses a minimum degree t, meaning:
//   - every non-root node has at least t-1 keys
//   - every node has at most 2t-1 keys
//   - every non-leaf has at most 2t children
//
// All public methods are safe to call on a nil *BTree (returns zero value / false).
package btree

import "cmp"

// Item holds a single key-value pair stored in the tree.
type Item[K, V any] struct {
	Key   K
	Value V
}

// node is an internal B-Tree node.
type node[K, V any] struct {
	keys     []Item[K, V]
	children []*node[K, V]
	leaf     bool
}

func newNode[K, V any](leaf bool) *node[K, V] {
	return &node[K, V]{leaf: leaf}
}

// nKeys returns the number of keys stored in this node.
func (n *node[K, V]) nKeys() int { return len(n.keys) }

// BTree is an ordered in-memory B-Tree parameterised over comparable key and
// value types.  The key type K must be orderable via cmp.Ordered.
//
// Minimum degree t (default 3, giving max 4 keys / 5 children per node) can
// be set with New.  A value of 1 is invalid and is silently promoted to 2.
type BTree[K cmp.Ordered, V any] struct {
	root *node[K, V]
	t    int // minimum degree
	size int // number of items stored
}

// New creates a BTree with the given minimum degree t.
// t=2 is the classic 2-3-4 tree.  t=3 is a good general default.
// Values below 2 are clamped to 2.
func New[K cmp.Ordered, V any](t int) *BTree[K, V] {
	if t < 2 {
		t = 2
	}
	return &BTree[K, V]{
		root: newNode[K, V](true),
		t:    t,
	}
}

// Len returns the number of items currently in the tree.
func (bt *BTree[K, V]) Len() int {
	if bt == nil {
		return 0
	}
	return bt.size
}

// maxKeys is 2t-1.
func (bt *BTree[K, V]) maxKeys() int { return 2*bt.t - 1 }

// minKeys is t-1 (never applies to the root).
func (bt *BTree[K, V]) minKeys() int { return bt.t - 1 }

// -------------------------------------------------------------------------
// Search
// -------------------------------------------------------------------------

// Get returns the value associated with key and true if found, or the zero
// value and false if the key is not present.
func (bt *BTree[K, V]) Get(key K) (V, bool) {
	if bt == nil {
		var zero V
		return zero, false
	}
	return search(bt.root, key)
}

func search[K cmp.Ordered, V any](n *node[K, V], key K) (V, bool) {
	if n == nil {
		var zero V
		return zero, false
	}
	i := lowerBound(n.keys, key)
	if i < n.nKeys() && n.keys[i].Key == key {
		return n.keys[i].Value, true
	}
	if n.leaf {
		var zero V
		return zero, false
	}
	return search(n.children[i], key)
}

// lowerBound returns the first index i such that keys[i].Key >= key.
func lowerBound[K cmp.Ordered, V any](keys []Item[K, V], key K) int {
	lo, hi := 0, len(keys)
	for lo < hi {
		mid := (lo + hi) >> 1
		if keys[mid].Key < key {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// -------------------------------------------------------------------------
// Insert
// -------------------------------------------------------------------------

// Set inserts or updates the key-value pair.  If the key already exists its
// value is replaced.  Returns true if a new key was inserted (false = update).
func (bt *BTree[K, V]) Set(key K, value V) bool {
	inserted := insertOrUpdate(bt, key, value)
	if inserted {
		bt.size++
	}
	return inserted
}

func insertOrUpdate[K cmp.Ordered, V any](bt *BTree[K, V], key K, value V) bool {
	root := bt.root
	if root.nKeys() == bt.maxKeys() {
		// Root is full — split it and create a new root.
		newRoot := newNode[K, V](false)
		newRoot.children = append(newRoot.children, root)
		splitChild(bt, newRoot, 0)
		bt.root = newRoot
		return insertNonFull(bt, newRoot, key, value)
	}
	return insertNonFull(bt, root, key, value)
}

// splitChild splits newRoot.children[i] (which must be full) into two nodes
// and promotes the median key up into newRoot.
func splitChild[K cmp.Ordered, V any](bt *BTree[K, V], parent *node[K, V], i int) {
	t := bt.t
	full := parent.children[i]
	mid := t - 1 // index of median key in full

	// Right sibling gets the upper half of full's keys.
	right := newNode[K, V](full.leaf)
	right.keys = append(right.keys, full.keys[mid+1:]...)
	if !full.leaf {
		right.children = append(right.children, full.children[mid+1:]...)
	}

	// Promote the median key into the parent.
	medianItem := full.keys[mid]

	// Truncate full to the lower half (keys before mid).
	full.keys = full.keys[:mid]
	if !full.leaf {
		full.children = full.children[:mid+1]
	}

	// Insert medianItem into parent.keys at position i.
	parent.keys = append(parent.keys, Item[K, V]{}) // grow
	copy(parent.keys[i+1:], parent.keys[i:])
	parent.keys[i] = medianItem

	// Insert right into parent.children at position i+1.
	parent.children = append(parent.children, nil) // grow
	copy(parent.children[i+2:], parent.children[i+1:])
	parent.children[i+1] = right
}

// insertNonFull inserts key into a node that is guaranteed to be non-full.
// Returns true if this was a new insertion (false = update in place).
func insertNonFull[K cmp.Ordered, V any](bt *BTree[K, V], n *node[K, V], key K, value V) bool {
	i := lowerBound(n.keys, key)

	// Key already exists — update in place.
	if i < n.nKeys() && n.keys[i].Key == key {
		n.keys[i].Value = value
		return false
	}

	if n.leaf {
		// Insert at position i.
		n.keys = append(n.keys, Item[K, V]{})
		copy(n.keys[i+1:], n.keys[i:])
		n.keys[i] = Item[K, V]{Key: key, Value: value}
		return true
	}

	// Descend to child[i]; split first if full.
	child := n.children[i]
	if child.nKeys() == bt.maxKeys() {
		splitChild(bt, n, i)
		// After split, the promoted key is at n.keys[i].
		// Re-check which side to go.
		if key > n.keys[i].Key {
			i++
		} else if key == n.keys[i].Key {
			n.keys[i].Value = value
			return false
		}
	}
	return insertNonFull(bt, n.children[i], key, value)
}

// -------------------------------------------------------------------------
// Delete
// -------------------------------------------------------------------------

// Delete removes the key from the tree.  Returns true if the key was found
// and deleted, false if the key was not present.
func (bt *BTree[K, V]) Delete(key K) bool {
	if bt == nil || bt.root == nil {
		return false
	}
	deleted := deleteKey(bt, bt.root, key)
	if deleted {
		bt.size--
		// If root is now empty and has a child, shrink the tree height.
		if bt.root.nKeys() == 0 && !bt.root.leaf {
			bt.root = bt.root.children[0]
		}
	}
	return deleted
}

// deleteKey deletes key from the subtree rooted at n.
// It maintains the invariant that n has at least t keys when called (unless
// n is the root).
func deleteKey[K cmp.Ordered, V any](bt *BTree[K, V], n *node[K, V], key K) bool {
	t := bt.t
	i := lowerBound(n.keys, key)

	if n.leaf {
		// Case 1: key is in this leaf.
		if i < n.nKeys() && n.keys[i].Key == key {
			n.keys = append(n.keys[:i], n.keys[i+1:]...)
			return true
		}
		return false // not found
	}

	// Case 2: key is in this internal node.
	if i < n.nKeys() && n.keys[i].Key == key {
		return deleteFromInternal(bt, n, i)
	}

	// Case 3: key might be in subtree rooted at children[i].
	// Ensure children[i] has at least t keys before descending.
	child := n.children[i]
	if child.nKeys() < t {
		// fill may borrow from a sibling (no structural change to n's key
		// count) or merge two children (removes one key and one child from n).
		// When fill calls mergeChildren(n, i-1) — the last-child case — the
		// merged node lands at n.children[i-1], so we must decrement i.
		fill(bt, n, i)
		// Detect the last-child merge: fill chose i-1 because i == n.nKeys()+1
		// after the merge (i.e. i was the last child and a merge happened).
		// A simpler, always-correct guard: if i > n.nKeys(), the child that
		// was at position i no longer exists; the merged node is at i-1.
		if i > n.nKeys() {
			i--
		}
		return deleteKey(bt, n.children[i], key)
	}
	return deleteKey(bt, child, key)
}

// deleteFromInternal handles deleting a key that lives in an internal node n
// at position i.
func deleteFromInternal[K cmp.Ordered, V any](bt *BTree[K, V], n *node[K, V], i int) bool {
	t := bt.t
	key := n.keys[i]
	_ = key

	leftChild := n.children[i]
	rightChild := n.children[i+1]

	if leftChild.nKeys() >= t {
		// Case 2a: predecessor is in left subtree.
		pred := getPredecessor(leftChild)
		n.keys[i] = pred
		return deleteKey(bt, leftChild, pred.Key)
	}

	if rightChild.nKeys() >= t {
		// Case 2b: successor is in right subtree.
		succ := getSuccessor(rightChild)
		n.keys[i] = succ
		return deleteKey(bt, rightChild, succ.Key)
	}

	// Case 2c: both children have t-1 keys — merge them.
	// The separator key (n.keys[i]) is pulled down into the merged child
	// during mergeChildren, and n.keys[i] is removed from n.  We must
	// delete the *original* key (captured before the merge) from the now-
	// merged child at n.children[i].
	origKey := key.Key
	mergeChildren(bt, n, i)
	return deleteKey(bt, n.children[i], origKey)
}

// getPredecessor returns the largest item in the subtree rooted at n.
func getPredecessor[K cmp.Ordered, V any](n *node[K, V]) Item[K, V] {
	for !n.leaf {
		n = n.children[n.nKeys()]
	}
	return n.keys[n.nKeys()-1]
}

// getSuccessor returns the smallest item in the subtree rooted at n.
func getSuccessor[K cmp.Ordered, V any](n *node[K, V]) Item[K, V] {
	for !n.leaf {
		n = n.children[0]
	}
	return n.keys[0]
}

// fill ensures that n.children[i] has at least t keys, by borrowing from a
// sibling or merging with one.
func fill[K cmp.Ordered, V any](bt *BTree[K, V], n *node[K, V], i int) {
	t := bt.t
	if i > 0 && n.children[i-1].nKeys() >= t {
		borrowFromLeft(n, i)
	} else if i < n.nKeys() && n.children[i+1].nKeys() >= t {
		borrowFromRight(n, i)
	} else {
		// Merge with a sibling.
		if i < n.nKeys() {
			mergeChildren(bt, n, i)
		} else {
			mergeChildren(bt, n, i-1)
		}
	}
}

// borrowFromLeft rotates the last key of children[i-1] through the parent
// into children[i].
func borrowFromLeft[K cmp.Ordered, V any](n *node[K, V], i int) {
	child := n.children[i]
	sibling := n.children[i-1]

	// Shift child's keys right to make room.
	child.keys = append(child.keys, Item[K, V]{})
	copy(child.keys[1:], child.keys)
	child.keys[0] = n.keys[i-1]

	// If internal, shift children right and take sibling's last child.
	if !child.leaf {
		child.children = append(child.children, nil)
		copy(child.children[1:], child.children)
		child.children[0] = sibling.children[sibling.nKeys()]
		sibling.children = sibling.children[:sibling.nKeys()]
	}

	// Promote sibling's last key to parent.
	n.keys[i-1] = sibling.keys[sibling.nKeys()-1]
	sibling.keys = sibling.keys[:sibling.nKeys()-1]
}

// borrowFromRight rotates the first key of children[i+1] through the parent
// into children[i].
func borrowFromRight[K cmp.Ordered, V any](n *node[K, V], i int) {
	child := n.children[i]
	sibling := n.children[i+1]

	// Append parent's separator key to child.
	child.keys = append(child.keys, n.keys[i])

	// If internal, take sibling's first child.
	if !child.leaf {
		child.children = append(child.children, sibling.children[0])
		sibling.children = sibling.children[1:]
	}

	// Promote sibling's first key to parent.
	n.keys[i] = sibling.keys[0]
	sibling.keys = sibling.keys[1:]
}

// mergeChildren merges n.children[i] and n.children[i+1] together with
// n.keys[i] as the separator, then removes the separator from n.
// After the merge, n.children[i] has 2t-1 keys.
func mergeChildren[K cmp.Ordered, V any](bt *BTree[K, V], n *node[K, V], i int) {
	left := n.children[i]
	right := n.children[i+1]

	// Pull the separator key down into left.
	left.keys = append(left.keys, n.keys[i])
	// Append all of right's keys and children into left.
	left.keys = append(left.keys, right.keys...)
	if !left.leaf {
		left.children = append(left.children, right.children...)
	}

	// Remove n.keys[i] and n.children[i+1].
	n.keys = append(n.keys[:i], n.keys[i+1:]...)
	n.children = append(n.children[:i+1], n.children[i+2:]...)
}

// -------------------------------------------------------------------------
// Range Scan
// -------------------------------------------------------------------------

// Range calls fn for every key k in [lo, hi] (inclusive) in ascending order.
// Iteration stops early if fn returns false.
// Returns true if the full range was visited, false if fn stopped it early.
func (bt *BTree[K, V]) Range(lo, hi K, fn func(key K, value V) bool) bool {
	if bt == nil {
		return true
	}
	if lo > hi {
		return true
	}
	return rangeQuery(bt.root, lo, hi, fn)
}

func rangeQuery[K cmp.Ordered, V any](
	n *node[K, V],
	lo, hi K,
	fn func(K, V) bool,
) bool {
	if n == nil {
		return true
	}
	// Find the first key >= lo.
	start := lowerBound(n.keys, lo)

	for i := start; i < n.nKeys(); i++ {
		// Descend into left child of n.keys[i] before visiting it.
		if !n.leaf {
			if !rangeQuery(n.children[i], lo, hi, fn) {
				return false
			}
		}
		if n.keys[i].Key > hi {
			return true
		}
		if !fn(n.keys[i].Key, n.keys[i].Value) {
			return false
		}
	}
	// Descend into the rightmost child that falls in range.
	if !n.leaf {
		return rangeQuery(n.children[n.nKeys()], lo, hi, fn)
	}
	return true
}

// -------------------------------------------------------------------------
// Scan (full ordered iteration)
// -------------------------------------------------------------------------

// Scan calls fn for every key-value pair in the tree in ascending order.
// Stops early if fn returns false.
func (bt *BTree[K, V]) Scan(fn func(key K, value V) bool) bool {
	if bt == nil {
		return true
	}
	return scan(bt.root, fn)
}

func scan[K cmp.Ordered, V any](n *node[K, V], fn func(K, V) bool) bool {
	if n == nil {
		return true
	}
	for i := 0; i < n.nKeys(); i++ {
		if !n.leaf {
			if !scan(n.children[i], fn) {
				return false
			}
		}
		if !fn(n.keys[i].Key, n.keys[i].Value) {
			return false
		}
	}
	if !n.leaf {
		return scan(n.children[n.nKeys()], fn)
	}
	return true
}

// -------------------------------------------------------------------------
// Min / Max convenience
// -------------------------------------------------------------------------

// Min returns the smallest key and its value.  ok is false for an empty tree.
func (bt *BTree[K, V]) Min() (key K, value V, ok bool) {
	if bt == nil || bt.size == 0 {
		return
	}
	n := bt.root
	for !n.leaf {
		n = n.children[0]
	}
	return n.keys[0].Key, n.keys[0].Value, true
}

// Max returns the largest key and its value.  ok is false for an empty tree.
func (bt *BTree[K, V]) Max() (key K, value V, ok bool) {
	if bt == nil || bt.size == 0 {
		return
	}
	n := bt.root
	for !n.leaf {
		n = n.children[n.nKeys()]
	}
	last := n.nKeys() - 1
	return n.keys[last].Key, n.keys[last].Value, true
}
