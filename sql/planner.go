package sql

import (
	"fmt"
	"strings"
	"sync"

	"github.com/pandeylakshya207-max/memdb/query"
)

// -------------------------------------------------------------------------
// Database — table catalog
// -------------------------------------------------------------------------

// Database is a named collection of Tables, thread-safe.
type Database struct {
	mu     sync.RWMutex
	tables map[string]*query.Table
}

// NewDatabase creates an empty Database.
func NewDatabase() *Database {
	return &Database{tables: make(map[string]*query.Table)}
}

// CreateTable adds a table to the catalog.
func (db *Database) CreateTable(name string, tbl *query.Table) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if _, exists := db.tables[strings.ToLower(name)]; exists {
		return fmt.Errorf("table %q already exists", name)
	}
	db.tables[strings.ToLower(name)] = tbl
	return nil
}

// DropTable removes a table from the catalog.
func (db *Database) DropTable(name string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	key := strings.ToLower(name)
	if _, exists := db.tables[key]; !exists {
		return fmt.Errorf("table %q does not exist", name)
	}
	delete(db.tables, key)
	return nil
}

// Table returns the table by name.
func (db *Database) Table(name string) (*query.Table, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	t, ok := db.tables[strings.ToLower(name)]
	return t, ok
}

// -------------------------------------------------------------------------
// Exec
// -------------------------------------------------------------------------

// ExecResult holds the result of executing a SQL statement.
type ExecResult struct {
	Rows         []query.Row
	Schema       query.Schema
	RowsAffected int
}

// Exec parses, plans, and executes a SQL statement against db.
func Exec(db *Database, sqlStr string) (*ExecResult, error) {
	stmt, err := Parse(sqlStr)
	if err != nil {
		return nil, err
	}
	return execStmt(db, stmt)
}

func execStmt(db *Database, stmt Stmt) (*ExecResult, error) {
	switch s := stmt.(type) {
	case *SelectStmt:
		return execSelect(db, s)
	case *InsertStmt:
		return execInsert(db, s)
	case *UpdateStmt:
		return execUpdate(db, s)
	case *DeleteStmt:
		return execDelete(db, s)
	case *CreateTableStmt:
		return execCreateTable(db, s)
	case *DropTableStmt:
		return execDropTable(db, s)
	}
	return nil, fmt.Errorf("exec: unknown statement type")
}

// -------------------------------------------------------------------------
// SELECT planner
// -------------------------------------------------------------------------

// aggMapping tracks the name assigned to each aggregate in the output schema.
type aggMapping struct {
	sqlExpr *AggExpr
	outName string // name used in HashAggregate output schema
}

func execSelect(db *Database, s *SelectStmt) (*ExecResult, error) {
	tbl, ok := db.Table(s.From)
	if !ok {
		return nil, fmt.Errorf("table %q does not exist", s.From)
	}

	// 1. Base scan.
	var base query.Iterator = query.NewTableScan(tbl)
	baseSchema := tbl.Schema()

	// 2. JOIN — buffer right side, produce merged schema.
	if s.Join != nil {
		joinTbl, ok := db.Table(s.Join.Table)
		if !ok {
			return nil, fmt.Errorf("join table %q does not exist", s.Join.Table)
		}
		merged := mergedSchema(baseSchema, joinTbl.Schema())
		joinPred, err := planExpr(s.Join.On, merged)
		if err != nil {
			return nil, fmt.Errorf("JOIN ON: %w", err)
		}
		jt := query.InnerJoin
		if s.Join.Kind == JoinLeft {
			jt = query.LeftOuterJoin
		}
		base = query.NewJoin(base, query.NewTableScan(joinTbl), joinPred, jt)
		baseSchema = merged
	}

	// 3. WHERE (before aggregation).
	if s.Where != nil {
		pred, err := planExpr(s.Where, baseSchema)
		if err != nil {
			return nil, fmt.Errorf("WHERE: %w", err)
		}
		base = query.NewFilter(base, pred)
	}

	// 4. GROUP BY / aggregation.
	// Determine which SELECT columns are aggregates and which are pass-through.
	// Build a mapping from SQL AggExpr → output column name so the projection
	// step can reference the right name after aggregation.
	var aggMaps []aggMapping
	hasAgg := stmtHasAgg(s)
	postAggSchema := baseSchema

	if hasAgg || len(s.GroupBy) > 0 {
		var aggSpecs []query.AggSpec
		for _, col := range s.Columns {
			agg, ok := col.Expr.(*AggExpr)
			if !ok {
				continue
			}
			name := col.Alias
			if name == "" {
				name = defaultAggName(agg)
			}
			fn, err := mapAggFunc(agg.Func)
			if err != nil {
				return nil, err
			}
			var inputExpr query.Expr
			if agg.Arg != nil {
				inputExpr, err = planExpr(agg.Arg, baseSchema)
				if err != nil {
					return nil, err
				}
			}
			aggSpecs = append(aggSpecs, query.AggSpec{Func: fn, Input: inputExpr, Output: name})
			aggMaps = append(aggMaps, aggMapping{sqlExpr: agg, outName: name})
		}
		// Also extract aggs referenced in HAVING.
		if s.Having != nil {
			havingAggs, err := extractHavingAggSpecs(s.Having, baseSchema, aggMaps)
			if err != nil {
				return nil, fmt.Errorf("HAVING agg: %w", err)
			}
			aggSpecs = append(aggSpecs, havingAggs...)
		}
		base = query.NewHashAggregate(base, s.GroupBy, aggSpecs...)
		// Build postAggSchema eagerly (same logic as HashAggregate.Open) so
		// the planner can reference it before Open() is called.
		postAggSchema = buildPostAggSchema(baseSchema, s.GroupBy, aggSpecs)
	}

	// 5. HAVING (filter on aggregated output).
	if s.Having != nil {
		pred, err := planExprWithAggMap(s.Having, postAggSchema, aggMaps)
		if err != nil {
			return nil, fmt.Errorf("HAVING: %w", err)
		}
		base = query.NewFilter(base, pred)
	}

	// 6. ORDER BY — must happen BEFORE PROJECT so we can sort by any column.
	// We'll project after sorting.
	if len(s.OrderBy) > 0 {
		keys, err := buildSortKeys(s.OrderBy, postAggSchema)
		if err != nil {
			return nil, fmt.Errorf("ORDER BY: %w", err)
		}
		base = query.NewOrderBy(base, keys...)
	}

	// 7. PROJECT (SELECT column list).
	outSchema := postAggSchema
	if !isStar(s.Columns) {
		projs, newSchema, err := buildProjections(s.Columns, postAggSchema, aggMaps)
		if err != nil {
			return nil, fmt.Errorf("SELECT: %w", err)
		}
		base = query.NewProject(base, projs...)
		outSchema = newSchema
	}

	// 8. DISTINCT (after project, before limit/offset).
	if s.Distinct {
		base = query.NewDistinct(base)
	}

	// 9. OFFSET / LIMIT.
	if s.Offset != nil {
		base = query.NewOffset(base, int(*s.Offset))
	}
	if s.Limit != nil {
		base = query.NewLimit(base, int(*s.Limit))
	}

	rows, err := query.Collect(base)
	if err != nil {
		return nil, err
	}
	return &ExecResult{Rows: rows, Schema: outSchema}, nil
}

func isStar(cols []SelectColumn) bool {
	return len(cols) == 1 && cols[0].Star
}

func mergedSchema(l, r query.Schema) query.Schema {
	cols := make([]query.Column, 0, l.Width()+r.Width())
	cols = append(cols, l.Columns...)
	cols = append(cols, r.Columns...)
	return query.Schema{Columns: cols}
}

func stmtHasAgg(s *SelectStmt) bool {
	for _, col := range s.Columns {
		if exprHasAgg(col.Expr) {
			return true
		}
	}
	return exprHasAgg(s.Having)
}

func exprHasAgg(e Expr) bool {
	if e == nil {
		return false
	}
	switch n := e.(type) {
	case *AggExpr:
		return true
	case *BinaryExpr:
		return exprHasAgg(n.Left) || exprHasAgg(n.Right)
	case *UnaryExpr:
		return exprHasAgg(n.Expr)
	case *IsNullExpr:
		return exprHasAgg(n.Expr)
	}
	return false
}

// defaultAggName generates a column name for an aggregate with no alias.
func defaultAggName(a *AggExpr) string {
	fn := map[AggFunc]string{
		AggFuncCount: "count", AggFuncSum: "sum",
		AggFuncMin: "min", AggFuncMax: "max", AggFuncAvg: "avg",
	}[a.Func]
	if a.Arg == nil {
		return fn + "_star"
	}
	col, ok := a.Arg.(*ColumnExpr)
	if ok {
		return fn + "_" + col.Name
	}
	return fn + "_expr"
}

// extractHavingAggSpecs collects agg specs needed for HAVING that aren't
// already in the SELECT list.
func extractHavingAggSpecs(e Expr, schema query.Schema, existing []aggMapping) ([]query.AggSpec, error) {
	var specs []query.AggSpec
	var walk func(Expr) error
	walk = func(e Expr) error {
		if e == nil {
			return nil
		}
		switch n := e.(type) {
		case *AggExpr:
			// Skip if already mapped.
			for _, m := range existing {
				if aggExprsEqual(m.sqlExpr, n) {
					return nil
				}
			}
			fn, err := mapAggFunc(n.Func)
			if err != nil {
				return err
			}
			name := defaultAggName(n)
			var inputExpr query.Expr
			if n.Arg != nil {
				inputExpr, err = planExpr(n.Arg, schema)
				if err != nil {
					return err
				}
			}
			specs = append(specs, query.AggSpec{Func: fn, Input: inputExpr, Output: name})
		case *BinaryExpr:
			walk(n.Left)
			walk(n.Right)
		case *UnaryExpr:
			walk(n.Expr)
		}
		return nil
	}
	return specs, walk(e)
}

func aggExprsEqual(a, b *AggExpr) bool {
	if a.Func != b.Func {
		return false
	}
	if (a.Arg == nil) != (b.Arg == nil) {
		return false
	}
	if a.Arg != nil && b.Arg != nil {
		return PrintExpr(a.Arg) == PrintExpr(b.Arg)
	}
	return true
}

func mapAggFunc(f AggFunc) (query.AggFunc, error) {
	switch f {
	case AggFuncCount:
		return query.AggCount, nil
	case AggFuncSum:
		return query.AggSum, nil
	case AggFuncMin:
		return query.AggMin, nil
	case AggFuncMax:
		return query.AggMax, nil
	case AggFuncAvg:
		return query.AggAvg, nil
	}
	return 0, fmt.Errorf("unknown aggregate function %d", f)
}

// buildProjections converts SELECT column list → query.Projection slice.
// For aggregate columns, it uses the aggMaps to find the correct post-agg
// column name rather than re-evaluating the aggregate.
func buildProjections(cols []SelectColumn, schema query.Schema, aggMaps []aggMapping) ([]query.Projection, query.Schema, error) {
	var projs []query.Projection
	outCols := make([]query.Column, 0, len(cols))

	for _, col := range cols {
		var expr query.Expr
		var name string
		var err error

		if agg, ok := col.Expr.(*AggExpr); ok {
			// Find the post-agg column name from the mapping.
			outName := ""
			for _, m := range aggMaps {
				if aggExprsEqual(m.sqlExpr, agg) {
					outName = m.outName
					break
				}
			}
			if outName == "" {
				outName = defaultAggName(agg)
			}
			if schema.Index(outName) < 0 {
				return nil, query.Schema{}, fmt.Errorf("aggregate %q not found in schema after aggregation", outName)
			}
			expr = query.ColRef{Name: outName}
			name = col.Alias
			if name == "" {
				name = outName
			}
		} else {
			expr, err = planExpr(col.Expr, schema)
			if err != nil {
				return nil, query.Schema{}, err
			}
			name = col.Alias
			if name == "" {
				name = exprName(col.Expr)
			}
		}

		projs = append(projs, query.Projection{Expr: expr, Name: name})
		outCols = append(outCols, query.Column{Name: name, Type: expr.ResultType()})
	}
	return projs, query.Schema{Columns: outCols}, nil
}

func exprName(e Expr) string {
	switch n := e.(type) {
	case *ColumnExpr:
		return n.Name
	case *AggExpr:
		return defaultAggName(n)
	}
	return PrintExpr(e)
}

// buildSortKeys converts OrderByElem list → query.SortKey slice.
// Accepts any column name present in schema (not just the final projected ones).
func buildSortKeys(elems []OrderByElem, schema query.Schema) ([]query.SortKey, error) {
	var keys []query.SortKey
	for _, elem := range elems {
		col, ok := elem.Expr.(*ColumnExpr)
		if !ok {
			return nil, fmt.Errorf("ORDER BY expression must be a column reference (got %T)", elem.Expr)
		}
		// Case-insensitive lookup.
		colName := col.Name
		if schema.Index(colName) < 0 {
			// Try case-insensitive.
			found := false
			for _, c := range schema.Columns {
				if strings.EqualFold(c.Name, colName) {
					colName = c.Name
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("ORDER BY column %q not found in schema", col.Name)
			}
		}
		keys = append(keys, query.SortKey{ColName: colName, Desc: elem.Desc})
	}
	return keys, nil
}

// -------------------------------------------------------------------------
// Expression planner
// -------------------------------------------------------------------------

func planExpr(e Expr, schema query.Schema) (query.Expr, error) {
	return planExprWithAggMap(e, schema, nil)
}

// planExprWithAggMap converts a SQL Expr to a query.Expr.
// aggMaps provides the post-agg column name for AggExpr nodes.
func planExprWithAggMap(e Expr, schema query.Schema, aggMaps []aggMapping) (query.Expr, error) {
	if e == nil {
		return nil, fmt.Errorf("planExpr: nil expression")
	}
	switch n := e.(type) {
	case *ColumnExpr:
		name := n.Name
		if schema.Index(name) < 0 {
			// Case-insensitive fallback.
			for _, col := range schema.Columns {
				if strings.EqualFold(col.Name, name) {
					name = col.Name
					break
				}
			}
			if schema.Index(name) < 0 {
				return nil, fmt.Errorf("column %q not found in schema", n.Name)
			}
		}
		return query.ColRef{Name: name}, nil

	case *LitExpr:
		switch n.Kind {
		case LitNull:
			return query.Literal{Val: query.Null()}, nil
		case LitInt:
			return query.Literal{Val: query.Int(n.IntVal)}, nil
		case LitFloat:
			return query.Literal{Val: query.Float(n.FltVal)}, nil
		case LitStr:
			return query.Literal{Val: query.Text(n.StrVal)}, nil
		case LitBool:
			return query.Literal{Val: query.Bool(n.BoolVal)}, nil
		}

	case *BinaryExpr:
		left, err := planExprWithAggMap(n.Left, schema, aggMaps)
		if err != nil {
			return nil, err
		}
		right, err := planExprWithAggMap(n.Right, schema, aggMaps)
		if err != nil {
			return nil, err
		}
		op, err := mapBinOp(n.Op)
		if err != nil {
			return nil, err
		}
		return query.BinOp{Left: left, Right: right, Op: op}, nil

	case *UnaryExpr:
		inner, err := planExprWithAggMap(n.Expr, schema, aggMaps)
		if err != nil {
			return nil, err
		}
		if n.Op == UnaryNot {
			return query.NotExpr{Inner: inner}, nil
		}
		return query.BinOp{
			Left:  inner,
			Right: query.Literal{Val: query.Int(-1)},
			Op:    query.OpMul,
		}, nil

	case *IsNullExpr:
		inner, err := planExprWithAggMap(n.Expr, schema, aggMaps)
		if err != nil {
			return nil, err
		}
		if n.IsNull {
			return query.IsNullExpr{Inner: inner}, nil
		}
		return query.NotExpr{Inner: query.IsNullExpr{Inner: inner}}, nil

	case *AggExpr:
		// Post-aggregation: the aggregate result is a plain column.
		outName := ""
		for _, m := range aggMaps {
			if aggExprsEqual(m.sqlExpr, n) {
				outName = m.outName
				break
			}
		}
		if outName == "" {
			outName = defaultAggName(n)
		}
		if schema.Index(outName) < 0 {
			return nil, fmt.Errorf("aggregate %q not found in schema after aggregation", outName)
		}
		return query.ColRef{Name: outName}, nil
	}
	return nil, fmt.Errorf("planExpr: unsupported expression type %T", e)
}

func mapBinOp(op BinOp) (query.BinOpKind, error) {
	switch op {
	case BinOpAdd:
		return query.OpAdd, nil
	case BinOpSub:
		return query.OpSub, nil
	case BinOpMul:
		return query.OpMul, nil
	case BinOpDiv:
		return query.OpDiv, nil
	case BinOpEq:
		return query.OpEq, nil
	case BinOpNeq:
		return query.OpNeq, nil
	case BinOpLt:
		return query.OpLt, nil
	case BinOpLte:
		return query.OpLte, nil
	case BinOpGt:
		return query.OpGt, nil
	case BinOpGte:
		return query.OpGte, nil
	case BinOpAnd:
		return query.OpAnd, nil
	case BinOpOr:
		return query.OpOr, nil
	}
	return 0, fmt.Errorf("unknown binary operator %d", op)
}

// -------------------------------------------------------------------------
// INSERT / UPDATE / DELETE / CREATE / DROP
// -------------------------------------------------------------------------

func execInsert(db *Database, s *InsertStmt) (*ExecResult, error) {
	tbl, ok := db.Table(s.Table)
	if !ok {
		return nil, fmt.Errorf("table %q does not exist", s.Table)
	}
	schema := tbl.Schema()
	if len(s.Columns) != len(s.Values) {
		return nil, fmt.Errorf("INSERT: column count %d != value count %d", len(s.Columns), len(s.Values))
	}
	row := make(query.Row, schema.Width())
	for i := range row {
		row[i] = query.Null()
	}
	for i, colName := range s.Columns {
		idx := schema.Index(colName)
		if idx < 0 {
			return nil, fmt.Errorf("INSERT: column %q not in table", colName)
		}
		val, err := evalLit(s.Values[i])
		if err != nil {
			return nil, err
		}
		row[idx] = val
	}
	if err := tbl.Insert(row); err != nil {
		return nil, err
	}
	return &ExecResult{RowsAffected: 1}, nil
}

func execUpdate(db *Database, s *UpdateStmt) (*ExecResult, error) {
	tbl, ok := db.Table(s.Table)
	if !ok {
		return nil, fmt.Errorf("table %q does not exist", s.Table)
	}
	schema := tbl.Schema()

	var scan query.Iterator = query.NewTableScan(tbl)
	if s.Where != nil {
		pred, err := planExpr(s.Where, schema)
		if err != nil {
			return nil, err
		}
		scan = query.NewFilter(scan, pred)
	}
	rows, err := query.Collect(scan)
	if err != nil {
		return nil, err
	}

	affected := 0
	for _, row := range rows {
		newRow := row.Clone()
		for _, sc := range s.Sets {
			idx := schema.Index(sc.Column)
			if idx < 0 {
				return nil, fmt.Errorf("UPDATE: column %q not in table", sc.Column)
			}
			val, err := evalLitOrCol(sc.Value, row, schema)
			if err != nil {
				return nil, err
			}
			newRow[idx] = val
		}
		if err := tbl.Upsert(newRow); err != nil {
			return nil, err
		}
		affected++
	}
	return &ExecResult{RowsAffected: affected}, nil
}

func execDelete(db *Database, s *DeleteStmt) (*ExecResult, error) {
	tbl, ok := db.Table(s.Table)
	if !ok {
		return nil, fmt.Errorf("table %q does not exist", s.Table)
	}
	schema := tbl.Schema()

	var scan query.Iterator = query.NewTableScan(tbl)
	if s.Where != nil {
		pred, err := planExpr(s.Where, schema)
		if err != nil {
			return nil, err
		}
		scan = query.NewFilter(scan, pred)
	}
	rows, err := query.Collect(scan)
	if err != nil {
		return nil, err
	}

	affected := 0
	for _, row := range rows {
		pkVals := extractPKVal(tbl, row, schema)
		if tbl.Delete(pkVals...) {
			affected++
		}
	}
	return &ExecResult{RowsAffected: affected}, nil
}

func execCreateTable(db *Database, s *CreateTableStmt) (*ExecResult, error) {
	cols := make([]query.Column, len(s.Columns))
	for i, c := range s.Columns {
		var vt query.ValueType
		switch c.Type {
		case ColTypeInt:
			vt = query.TypeInt
		case ColTypeFloat:
			vt = query.TypeFloat
		case ColTypeText:
			vt = query.TypeText
		case ColTypeBool:
			vt = query.TypeBool
		}
		cols[i] = query.Column{Name: c.Name, Type: vt}
	}
	schema := query.Schema{Columns: cols}
	tbl, err := query.NewTable(schema, s.PrimaryKey...)
	if err != nil {
		return nil, err
	}
	if err := db.CreateTable(s.Table, tbl); err != nil {
		return nil, err
	}
	return &ExecResult{}, nil
}

func execDropTable(db *Database, s *DropTableStmt) (*ExecResult, error) {
	if err := db.DropTable(s.Table); err != nil {
		return nil, err
	}
	return &ExecResult{}, nil
}

// -------------------------------------------------------------------------
// Evaluation helpers
// -------------------------------------------------------------------------

func evalLit(e Expr) (query.Value, error) {
	lit, ok := e.(*LitExpr)
	if !ok {
		qe, err := planExpr(e, query.Schema{})
		if err != nil {
			return query.Null(), err
		}
		return qe.Eval(nil, query.Schema{}), nil
	}
	switch lit.Kind {
	case LitNull:
		return query.Null(), nil
	case LitInt:
		return query.Int(lit.IntVal), nil
	case LitFloat:
		return query.Float(lit.FltVal), nil
	case LitStr:
		return query.Text(lit.StrVal), nil
	case LitBool:
		return query.Bool(lit.BoolVal), nil
	}
	return query.Null(), fmt.Errorf("unknown literal kind")
}

func evalLitOrCol(e Expr, row query.Row, schema query.Schema) (query.Value, error) {
	qe, err := planExpr(e, schema)
	if err != nil {
		return evalLit(e)
	}
	return qe.Eval(row, schema), nil
}

// extractPKVal extracts primary key values from a row using the table's PK column list.
func extractPKVal(tbl *query.Table, row query.Row, schema query.Schema) []query.Value {
	pkCols := tbl.PKCols()
	vals := make([]query.Value, len(pkCols))
	for i, name := range pkCols {
		idx := schema.Index(name)
		if idx >= 0 && idx < len(row) {
			vals[i] = row[idx]
		} else {
			vals[i] = query.Null()
		}
	}
	return vals
}

// buildPostAggSchema constructs the schema that HashAggregate will produce,
// mirroring HashAggregate.Open() logic so we can reference it before Open().
func buildPostAggSchema(inSchema query.Schema, groupBy []string, aggSpecs []query.AggSpec) query.Schema {
	var cols []query.Column
	for _, name := range groupBy {
		idx := inSchema.Index(name)
		if idx >= 0 {
			cols = append(cols, inSchema.Columns[idx])
		}
	}
	for _, spec := range aggSpecs {
		cols = append(cols, query.Column{Name: spec.Output, Type: query.TypeNull})
	}
	return query.Schema{Columns: cols}
}
