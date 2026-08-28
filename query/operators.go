package query

import "sort"

// -------------------------------------------------------------------------
// Filter — predicate evaluation
// -------------------------------------------------------------------------

// Filter passes only rows for which pred evaluates to true.
type Filter struct {
	child  Iterator
	pred   Expr
	schema Schema
}

// NewFilter wraps child with a predicate filter.
func NewFilter(child Iterator, pred Expr) *Filter {
	return &Filter{child: child, pred: pred}
}

func (f *Filter) Schema() Schema { return f.child.Schema() }

func (f *Filter) Open() error {
	if err := f.child.Open(); err != nil {
		return err
	}
	f.schema = f.child.Schema()
	return nil
}

func (f *Filter) Next() (Row, bool) {
	for {
		row, ok := f.child.Next()
		if !ok {
			return nil, false
		}
		v := f.pred.Eval(row, f.schema)
		if isTruthy(v) {
			return row, true
		}
	}
}

func (f *Filter) Close() error { return f.child.Close() }

// -------------------------------------------------------------------------
// Project — column selection and computed expressions
// -------------------------------------------------------------------------

// Projection defines one output column: an expression and an output name.
type Projection struct {
	Expr Expr
	Name string
}

// Project evaluates a list of expressions, producing a new schema.
type Project struct {
	child       Iterator
	projections []Projection
	outSchema   Schema
	inSchema    Schema
}

// NewProject wraps child with the given column projections.
func NewProject(child Iterator, projections ...Projection) *Project {
	cols := make([]Column, len(projections))
	for i, p := range projections {
		cols[i] = Column{Name: p.Name, Type: p.Expr.ResultType()}
	}
	return &Project{
		child:       child,
		projections: projections,
		outSchema:   Schema{Columns: cols},
	}
}

func (p *Project) Schema() Schema { return p.outSchema }

func (p *Project) Open() error {
	if err := p.child.Open(); err != nil {
		return err
	}
	p.inSchema = p.child.Schema()
	return nil
}

func (p *Project) Next() (Row, bool) {
	row, ok := p.child.Next()
	if !ok {
		return nil, false
	}
	out := make(Row, len(p.projections))
	for i, proj := range p.projections {
		out[i] = proj.Expr.Eval(row, p.inSchema)
	}
	return out, true
}

func (p *Project) Close() error { return p.child.Close() }

// -------------------------------------------------------------------------
// Limit / Offset
// -------------------------------------------------------------------------

// Limit passes at most n rows from child.
type Limit struct {
	child   Iterator
	n       int
	emitted int
}

func NewLimit(child Iterator, n int) *Limit {
	return &Limit{child: child, n: n}
}

func (l *Limit) Schema() Schema { return l.child.Schema() }
func (l *Limit) Open() error    { l.emitted = 0; return l.child.Open() }
func (l *Limit) Close() error   { return l.child.Close() }

func (l *Limit) Next() (Row, bool) {
	if l.emitted >= l.n {
		return nil, false
	}
	row, ok := l.child.Next()
	if !ok {
		return nil, false
	}
	l.emitted++
	return row, true
}

// Offset skips the first n rows from child.
type Offset struct {
	child   Iterator
	n       int
	skipped bool
}

func NewOffset(child Iterator, n int) *Offset {
	return &Offset{child: child, n: n}
}

func (o *Offset) Schema() Schema { return o.child.Schema() }
func (o *Offset) Close() error   { return o.child.Close() }

func (o *Offset) Open() error {
	o.skipped = false
	return o.child.Open()
}

func (o *Offset) Next() (Row, bool) {
	if !o.skipped {
		for i := 0; i < o.n; i++ {
			if _, ok := o.child.Next(); !ok {
				o.skipped = true
				return nil, false
			}
		}
		o.skipped = true
	}
	return o.child.Next()
}

// -------------------------------------------------------------------------
// OrderBy — in-memory sort
// -------------------------------------------------------------------------

// SortKey specifies one column sort key.
type SortKey struct {
	ColName string
	Desc    bool
}

// OrderBy buffers all child rows then sorts them.
type OrderBy struct {
	child  Iterator
	keys   []SortKey
	rows   []Row
	cursor int
	schema Schema
}

func NewOrderBy(child Iterator, keys ...SortKey) *OrderBy {
	return &OrderBy{child: child, keys: keys}
}

func (o *OrderBy) Schema() Schema { return o.child.Schema() }
func (o *OrderBy) Close() error   { o.rows = nil; return o.child.Close() }

func (o *OrderBy) Open() error {
	if err := o.child.Open(); err != nil {
		return err
	}
	o.schema = o.child.Schema()
	o.rows = nil
	o.cursor = 0
	for {
		row, ok := o.child.Next()
		if !ok {
			break
		}
		o.rows = append(o.rows, row.Clone())
	}
	sort.SliceStable(o.rows, func(i, j int) bool {
		for _, k := range o.keys {
			idx := o.schema.Index(k.ColName)
			if idx < 0 {
				continue
			}
			cmp, ok := o.rows[i][idx].Compare(o.rows[j][idx])
			if !ok || cmp == 0 {
				continue
			}
			if k.Desc {
				return cmp > 0
			}
			return cmp < 0
		}
		return false
	})
	return nil
}

func (o *OrderBy) Next() (Row, bool) {
	if o.cursor >= len(o.rows) {
		return nil, false
	}
	row := o.rows[o.cursor]
	o.cursor++
	return row, true
}

// -------------------------------------------------------------------------
// Distinct — removes duplicate rows (based on all column values)
// -------------------------------------------------------------------------

// Distinct filters out duplicate rows from its child iterator.
// It buffers the full output in memory and uses a string-key set for dedup.
type Distinct struct {
	child  Iterator
	seen   map[string]struct{}
	schema Schema
}

func NewDistinct(child Iterator) *Distinct {
	return &Distinct{child: child}
}

func (d *Distinct) Schema() Schema { return d.child.Schema() }
func (d *Distinct) Close() error   { d.seen = nil; return d.child.Close() }

func (d *Distinct) Open() error {
	if err := d.child.Open(); err != nil {
		return err
	}
	d.schema = d.child.Schema()
	d.seen = make(map[string]struct{})
	return nil
}

func (d *Distinct) Next() (Row, bool) {
	for {
		row, ok := d.child.Next()
		if !ok {
			return nil, false
		}
		key := row.String()
		if _, dup := d.seen[key]; dup {
			continue
		}
		d.seen[key] = struct{}{}
		return row, true
	}
}
