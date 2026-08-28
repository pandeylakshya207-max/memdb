package query

import (
	"fmt"
	"strings"
	"sync"

	"github.com/pandeylakshya207-max/memdb/btree"
)

// -------------------------------------------------------------------------
// Table — ordered in-memory table backed by a B-Tree
// -------------------------------------------------------------------------

// Table is a schema-bound, ordered in-memory table.
// Rows are keyed by their primary key column(s); the B-Tree keeps them sorted.
// All methods are safe for concurrent reads; writes take a write lock.
type Table struct {
	mu       sync.RWMutex
	schema   Schema
	pkCols   []string // primary key column names (in order)
	tree     *btree.BTree[string, Row]
	rowCount int
}

// NewTable creates a new empty table with the given schema and primary key columns.
// pkCols must reference valid column names in schema.
func NewTable(schema Schema, pkCols ...string) (*Table, error) {
	for _, pk := range pkCols {
		if schema.Index(pk) < 0 {
			return nil, fmt.Errorf("NewTable: primary key column %q not in schema", pk)
		}
	}
	if len(pkCols) == 0 {
		return nil, fmt.Errorf("NewTable: at least one primary key column required")
	}
	return &Table{
		schema: schema,
		pkCols: pkCols,
		tree:   btree.New[string, Row](4),
	}, nil
}

// Schema returns the table's schema.
func (t *Table) Schema() Schema {
	return t.schema
}

// Len returns the number of rows currently stored.
func (t *Table) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.rowCount
}

// Insert inserts a new row.  Returns an error if the row violates the schema
// width or if a row with the same primary key already exists.
func (t *Table) Insert(row Row) error {
	if len(row) != t.schema.Width() {
		return fmt.Errorf("Insert: row width %d != schema width %d", len(row), t.schema.Width())
	}
	key := t.pkKey(row)

	t.mu.Lock()
	defer t.mu.Unlock()

	if _, exists := t.tree.Get(key); exists {
		return fmt.Errorf("Insert: duplicate primary key %s", key)
	}
	t.tree.Set(key, row.Clone())
	t.rowCount++
	return nil
}

// Upsert inserts or replaces the row (last-write-wins on duplicate PK).
func (t *Table) Upsert(row Row) error {
	if len(row) != t.schema.Width() {
		return fmt.Errorf("Upsert: row width %d != schema width %d", len(row), t.schema.Width())
	}
	key := t.pkKey(row)
	t.mu.Lock()
	defer t.mu.Unlock()
	inserted := t.tree.Set(key, row.Clone())
	if inserted {
		t.rowCount++
	}
	return nil
}

// Delete removes the row with the given primary key values.
// Returns true if a row was deleted.
func (t *Table) Delete(pkVals ...Value) bool {
	key := pkValKey(pkVals)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tree.Delete(key) {
		t.rowCount--
		return true
	}
	return false
}

// Get retrieves a single row by primary key.
func (t *Table) Get(pkVals ...Value) (Row, bool) {
	key := pkValKey(pkVals)
	t.mu.RLock()
	defer t.mu.RUnlock()
	row, ok := t.tree.Get(key)
	if !ok {
		return nil, false
	}
	return row.Clone(), true
}

// pkKey builds the B-Tree key string from a row's primary key columns.
func (t *Table) pkKey(row Row) string {
	vals := make([]Value, len(t.pkCols))
	for i, name := range t.pkCols {
		idx := t.schema.Index(name)
		vals[i] = row[idx]
	}
	return pkValKey(vals)
}

// pkValKey serialises primary key values into a lexicographically sort-stable
// string key.  For integers we use a sign-prefix + zero-padded 20-digit form
// so that numeric order matches string order.  Other types use their natural
// string representation (text is already lex-ordered; floats use %020.6f with
// sign prefix).
func pkValKey(vals []Value) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = sortableKey(v)
	}
	return strings.Join(parts, "\x00")
}

// sortableKey converts a Value into a string that sorts lexicographically in
// the same order as the value's natural order.
func sortableKey(v Value) string {
	switch v.typ {
	case TypeInt:
		// Encode as sign-char + 20-digit zero-padded absolute value.
		// Negative: flip all digits so that -10 < -5 sorts correctly.
		n := v.intVal
		if n >= 0 {
			return fmt.Sprintf("+%020d", n)
		}
		// For negatives: use '-' prefix with the complement so larger
		// magnitude = smaller string.
		// math.MinInt64 safe: use uint64 complement.
		return fmt.Sprintf("-%020d", ^uint64(^n)) // effectively 10^20 - |n|
	case TypeFloat:
		f := v.fltVal
		if f >= 0 {
			return fmt.Sprintf("+%030.10f", f)
		}
		// Negative floats: need complement so -10 < -5 lexicographically.
		// Simplest correct approach: encode as sign + padded, then for
		// negatives invert digit by digit.
		s := fmt.Sprintf("-%030.10f", -f) // e.g. "-0000000010.0000000000"
		b := []byte(s)
		for i := 1; i < len(b); i++ {
			if b[i] >= '0' && b[i] <= '9' {
				b[i] = '9' - (b[i] - '0') // complement digit
			}
		}
		return string(b)
	default:
		return v.String()
	}
}

// -------------------------------------------------------------------------
// TableScan — full ordered scan operator
// -------------------------------------------------------------------------

// TableScan scans all rows of a Table in primary-key order.
type TableScan struct {
	table  *Table
	rows   []Row // snapshot taken at Open time
	cursor int
}

// NewTableScan creates a scan over table.
func NewTableScan(table *Table) *TableScan {
	return &TableScan{table: table}
}

func (s *TableScan) Schema() Schema { return s.table.schema }

func (s *TableScan) Open() error {
	s.table.mu.RLock()
	defer s.table.mu.RUnlock()
	s.rows = nil
	s.cursor = 0
	s.table.tree.Scan(func(_ string, row Row) bool {
		s.rows = append(s.rows, row.Clone())
		return true
	})
	return nil
}

func (s *TableScan) Next() (Row, bool) {
	if s.cursor >= len(s.rows) {
		return nil, false
	}
	row := s.rows[s.cursor]
	s.cursor++
	return row, true
}

func (s *TableScan) Close() error {
	s.rows = nil
	return nil
}

// -------------------------------------------------------------------------
// RangeScan — scan rows within a PK range
// -------------------------------------------------------------------------

// RangeScan scans rows whose primary key falls in [lo, hi] (inclusive).
// Works only for single-column primary keys.
type RangeScan struct {
	table  *Table
	lo, hi string
	rows   []Row
	cursor int
}

// NewRangeScan creates a scan between lo and hi primary key values (inclusive).
func NewRangeScan(table *Table, lo, hi Value) *RangeScan {
	return &RangeScan{
		table: table,
		lo:    pkValKey([]Value{lo}),
		hi:    pkValKey([]Value{hi}),
	}
}

func (s *RangeScan) Schema() Schema { return s.table.schema }

func (s *RangeScan) Open() error {
	s.table.mu.RLock()
	defer s.table.mu.RUnlock()
	s.rows = nil
	s.cursor = 0
	s.table.tree.Range(s.lo, s.hi, func(_ string, row Row) bool {
		s.rows = append(s.rows, row.Clone())
		return true
	})
	return nil
}

func (s *RangeScan) Next() (Row, bool) {
	if s.cursor >= len(s.rows) {
		return nil, false
	}
	row := s.rows[s.cursor]
	s.cursor++
	return row, true
}

func (s *RangeScan) Close() error {
	s.rows = nil
	return nil
}

// PKCols returns the primary key column names in order.
func (t *Table) PKCols() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	cp := make([]string, len(t.pkCols))
	copy(cp, t.pkCols)
	return cp
}
