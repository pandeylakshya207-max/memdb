package query

import "fmt"

// -------------------------------------------------------------------------
// NestedLoopJoin — inner / left-outer join
// -------------------------------------------------------------------------

// JoinType specifies the join variant.
type JoinType uint8

const (
	InnerJoin JoinType = iota
	LeftOuterJoin
)

// NestedLoopJoin implements inner and left-outer join.
// For each left row it scans all right rows looking for matches.
// The right side is fully buffered at Open time (once), so it is suitable
// when the right-hand side fits in memory.
type NestedLoopJoin struct {
	left, right Iterator
	pred        Expr // nil = cross join / always-true
	joinType    JoinType
	outSchema   Schema

	leftSchema   Schema
	rightSchema  Schema
	rightRows    []Row // snapshot of right side, built once at Open
	leftRow      Row
	rightCur     int
	leftDone     bool
	leftMatched  bool
	needEmitNull bool // for LEFT OUTER: emit null-padded row
}

// NewJoin creates a join operator.
func NewJoin(left, right Iterator, pred Expr, jt JoinType) *NestedLoopJoin {
	return &NestedLoopJoin{left: left, right: right, pred: pred, joinType: jt}
}

func (j *NestedLoopJoin) Schema() Schema { return j.outSchema }

func (j *NestedLoopJoin) Open() error {
	ls := j.left.Schema()
	rs := j.right.Schema()
	cols := make([]Column, 0, ls.Width()+rs.Width())
	cols = append(cols, ls.Columns...)
	cols = append(cols, rs.Columns...)
	j.outSchema = Schema{Columns: cols}
	j.leftSchema = ls
	j.rightSchema = rs

	if err := j.left.Open(); err != nil {
		return err
	}
	if err := j.right.Open(); err != nil {
		return err
	}
	j.rightRows = nil
	for {
		row, ok := j.right.Next()
		if !ok {
			break
		}
		j.rightRows = append(j.rightRows, row.Clone())
	}
	j.right.Close()

	j.leftRow = nil
	j.leftDone = false
	j.rightCur = len(j.rightRows) // force left-advance on first Next
	j.leftMatched = false
	j.needEmitNull = false
	return nil
}

func (j *NestedLoopJoin) Next() (Row, bool) {
	for {
		// Emit a null-padded left row for LEFT OUTER JOIN when unmatched.
		if j.needEmitNull {
			j.needEmitNull = false
			out := make(Row, j.leftSchema.Width()+j.rightSchema.Width())
			copy(out, j.leftRow)
			for i := j.leftSchema.Width(); i < len(out); i++ {
				out[i] = Null()
			}
			j.leftRow = nil
			return out, true
		}

		// Scan remaining right rows for the current left row.
		for j.rightCur < len(j.rightRows) {
			rRow := j.rightRows[j.rightCur]
			j.rightCur++

			combined := make(Row, j.leftSchema.Width()+j.rightSchema.Width())
			copy(combined, j.leftRow)
			copy(combined[j.leftSchema.Width():], rRow)

			if j.pred != nil {
				if !isTruthy(j.pred.Eval(combined, j.outSchema)) {
					continue
				}
			}
			j.leftMatched = true
			return combined, true
		}

		// Right side exhausted for this left row.
		if j.leftRow != nil && j.joinType == LeftOuterJoin && !j.leftMatched {
			j.needEmitNull = true
			continue
		}

		// Advance to next left row.
		if j.leftDone {
			return nil, false
		}
		row, ok := j.left.Next()
		if !ok {
			j.leftDone = true
			return nil, false
		}
		j.leftRow = row.Clone()
		j.rightCur = 0
		j.leftMatched = false
	}
}

func (j *NestedLoopJoin) Close() error {
	j.rightRows = nil
	j.leftRow = nil
	return j.left.Close()
}

// -------------------------------------------------------------------------
// HashAggregate — GROUP BY + aggregate functions
// -------------------------------------------------------------------------

// groupBucket holds the running state for one group.
type groupBucket struct {
	repRow []Value // representative GROUP BY column values
	states []*aggState
}

// HashAggregate groups rows by groupBy columns and computes aggregates.
// If groupBy is empty it produces a single global aggregate row.
type HashAggregate struct {
	child     Iterator
	groupBy   []string
	aggs      []AggSpec
	outSchema Schema

	inSchema    Schema
	buckets     map[string]*groupBucket
	orderedKeys []string
	cursor      int
}

// NewHashAggregate creates an aggregate operator.
func NewHashAggregate(child Iterator, groupBy []string, aggs ...AggSpec) *HashAggregate {
	return &HashAggregate{child: child, groupBy: groupBy, aggs: aggs}
}

func (h *HashAggregate) Schema() Schema { return h.outSchema }

func (h *HashAggregate) Open() error {
	if err := h.child.Open(); err != nil {
		return err
	}
	h.inSchema = h.child.Schema()

	// Build output schema: GROUP BY columns first, then aggregate columns.
	cols := make([]Column, 0, len(h.groupBy)+len(h.aggs))
	for _, name := range h.groupBy {
		idx := h.inSchema.Index(name)
		if idx < 0 {
			return fmt.Errorf("HashAggregate: GROUP BY column %q not in schema", name)
		}
		cols = append(cols, h.inSchema.Columns[idx])
	}
	for _, spec := range h.aggs {
		cols = append(cols, Column{Name: spec.Output, Type: TypeNull})
	}
	h.outSchema = Schema{Columns: cols}

	// Consume all input and build groups.
	h.buckets = make(map[string]*groupBucket)
	h.orderedKeys = nil

	for {
		row, ok := h.child.Next()
		if !ok {
			break
		}
		key := rowKey(row, h.inSchema, h.groupBy)
		b, exists := h.buckets[key]
		if !exists {
			// Capture the GROUP BY column values from this first row.
			repRow := make([]Value, len(h.groupBy))
			for i, name := range h.groupBy {
				idx := h.inSchema.Index(name)
				if idx >= 0 && idx < len(row) {
					repRow[i] = row[idx]
				}
			}
			states := make([]*aggState, len(h.aggs))
			for i, spec := range h.aggs {
				states[i] = newAggState(spec)
			}
			b = &groupBucket{repRow: repRow, states: states}
			h.buckets[key] = b
			h.orderedKeys = append(h.orderedKeys, key)
		}
		for _, s := range b.states {
			s.feed(row, h.inSchema)
		}
	}

	// Empty input with no GROUP BY → emit one zero-row.
	if len(h.buckets) == 0 && len(h.groupBy) == 0 {
		states := make([]*aggState, len(h.aggs))
		for i, spec := range h.aggs {
			states[i] = newAggState(spec)
		}
		h.buckets[""] = &groupBucket{repRow: nil, states: states}
		h.orderedKeys = []string{""}
	}

	h.cursor = 0
	return h.child.Close()
}

func (h *HashAggregate) Next() (Row, bool) {
	if h.cursor >= len(h.orderedKeys) {
		return nil, false
	}
	b := h.buckets[h.orderedKeys[h.cursor]]
	h.cursor++

	out := make(Row, len(h.groupBy)+len(h.aggs))
	copy(out, b.repRow)
	for i, s := range b.states {
		out[len(h.groupBy)+i] = s.result()
	}
	return out, true
}

func (h *HashAggregate) Close() error {
	h.buckets = nil
	h.orderedKeys = nil
	return nil
}
