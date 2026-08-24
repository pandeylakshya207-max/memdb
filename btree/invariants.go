package btree

import (
	"cmp"
	"fmt"
	"strings"
)

// CheckInvariants validates all B-Tree structural invariants and returns a
// non-nil error describing the first violation found.
//
// Invariants checked:
//  1. Every non-root node has at least t-1 keys.
//  2. Every node has at most 2t-1 keys.
//  3. Every internal node with k keys has exactly k+1 children.
//  4. All leaves are at the same depth.
//  5. Keys within each node are in strictly ascending order.
//  6. Tree is globally sorted (keys in each node are within the bounds implied
//     by ancestor separators).
//  7. bt.size matches the actual count of items in the tree.
func CheckInvariants[K cmp.Ordered, V any](bt *BTree[K, V]) error {
	if bt == nil {
		return fmt.Errorf("btree is nil")
	}
	leafDepth := -1
	count := 0
	if err := checkNode(bt, bt.root, nil, nil, 0, &leafDepth, &count, true); err != nil {
		return err
	}
	if count != bt.size {
		return fmt.Errorf("size mismatch: bt.size=%d but counted %d items", bt.size, count)
	}
	return nil
}

func checkNode[K cmp.Ordered, V any](
	bt *BTree[K, V],
	n *node[K, V],
	lo, hi *K,
	depth int,
	leafDepth *int,
	count *int,
	isRoot bool,
) error {
	if n == nil {
		return fmt.Errorf("nil node at depth %d", depth)
	}

	nk := n.nKeys()

	if !isRoot && nk < bt.minKeys() {
		return fmt.Errorf("depth=%d: node has %d keys, minimum is %d", depth, nk, bt.minKeys())
	}
	if nk > bt.maxKeys() {
		return fmt.Errorf("depth=%d: node has %d keys, maximum is %d", depth, nk, bt.maxKeys())
	}
	if isRoot && !n.leaf && nk < 1 {
		return fmt.Errorf("root internal node has 0 keys")
	}

	if n.leaf {
		if len(n.children) != 0 {
			return fmt.Errorf("depth=%d: leaf has %d children (expected 0)", depth, len(n.children))
		}
	} else {
		if len(n.children) != nk+1 {
			return fmt.Errorf("depth=%d: internal node has %d keys but %d children (expected %d)",
				depth, nk, len(n.children), nk+1)
		}
	}

	if n.leaf {
		if *leafDepth == -1 {
			*leafDepth = depth
		} else if *leafDepth != depth {
			return fmt.Errorf("leaf depth mismatch: expected %d got %d", *leafDepth, depth)
		}
	}

	for i := 0; i < nk; i++ {
		k := n.keys[i].Key
		if i > 0 && n.keys[i-1].Key >= k {
			return fmt.Errorf("depth=%d: keys not strictly ascending at index %d: %v >= %v",
				depth, i, n.keys[i-1].Key, k)
		}
		if lo != nil && k <= *lo {
			return fmt.Errorf("depth=%d key[%d]=%v violates lower bound >%v", depth, i, k, *lo)
		}
		if hi != nil && k >= *hi {
			return fmt.Errorf("depth=%d key[%d]=%v violates upper bound <%v", depth, i, k, *hi)
		}
		*count++
	}

	if !n.leaf {
		for i := 0; i <= nk; i++ {
			var childLo, childHi *K
			if i > 0 {
				v := n.keys[i-1].Key
				childLo = &v
			} else {
				childLo = lo
			}
			if i < nk {
				v := n.keys[i].Key
				childHi = &v
			} else {
				childHi = hi
			}
			if err := checkNode(bt, n.children[i], childLo, childHi, depth+1, leafDepth, count, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// DebugString returns a multi-line ASCII dump of the tree.
func DebugString[K cmp.Ordered, V any](bt *BTree[K, V]) string {
	if bt == nil {
		return "<nil btree>"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "BTree(t=%d, size=%d)\n", bt.t, bt.size)
	debugNode(&sb, bt.root, 0)
	return sb.String()
}

func debugNode[K cmp.Ordered, V any](sb *strings.Builder, n *node[K, V], depth int) {
	indent := strings.Repeat("  ", depth)
	kind := "internal"
	if n.leaf {
		kind = "leaf"
	}
	fmt.Fprintf(sb, "%s[%s] keys=%v\n", indent, kind, n.keys)
	for _, c := range n.children {
		debugNode(sb, c, depth+1)
	}
}
