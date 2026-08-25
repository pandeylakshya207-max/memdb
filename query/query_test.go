package query

import (
	"fmt"
	"math"
	"sort"
	"testing"
)

// -------------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------------

// employeeSchema: id INT, name TEXT, dept TEXT, salary FLOAT, active BOOL
var employeeSchema = NewSchema(
	"id", TypeInt,
	"name", TypeText,
	"dept", TypeText,
	"salary", TypeFloat,
	"active", TypeBool,
)

func mustTable(t *testing.T, schema Schema, pk ...string) *Table {
	t.Helper()
	tbl, err := NewTable(schema, pk...)
	if err != nil {
		t.Fatalf("NewTable: %v", err)
	}
	return tbl
}

func mustInsert(t *testing.T, tbl *Table, row Row) {
	t.Helper()
	if err := tbl.Insert(row); err != nil {
		t.Fatalf("Insert: %v", err)
	}
}

func mustUpsert(t *testing.T, tbl *Table, row Row) {
	t.Helper()
	if err := tbl.Upsert(row); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
}

func mustCollect(t *testing.T, it Iterator) []Row {
	t.Helper()
	rows, err := Collect(it)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return rows
}

func assertRowCount(t *testing.T, rows []Row, want int) {
	t.Helper()
	if len(rows) != want {
		t.Fatalf("expected %d rows, got %d: %v", want, len(rows), rows)
	}
}

func assertIntCol(t *testing.T, rows []Row, colIdx int, want []int64) {
	t.Helper()
	if len(rows) != len(want) {
		t.Fatalf("row count %d != %d", len(rows), len(want))
	}
	for i, row := range rows {
		if row[colIdx].AsInt() != want[i] {
			t.Fatalf("row[%d][%d]=%v want %d", i, colIdx, row[colIdx], want[i])
		}
	}
}

func assertTextCol(t *testing.T, rows []Row, colIdx int, want []string) {
	t.Helper()
	if len(rows) != len(want) {
		t.Fatalf("row count %d != %d", len(rows), len(want))
	}
	for i, row := range rows {
		if row[colIdx].AsText() != want[i] {
			t.Fatalf("row[%d][%d]=%v want %q", i, colIdx, row[colIdx], want[i])
		}
	}
}

// buildEmployeeTable returns a table with 6 employees.
func buildEmployeeTable(t *testing.T) *Table {
	t.Helper()
	tbl := mustTable(t, employeeSchema, "id")
	rows := []Row{
		{Int(1), Text("Alice"), Text("Eng"), Float(90000), Bool(true)},
		{Int(2), Text("Bob"), Text("Eng"), Float(80000), Bool(true)},
		{Int(3), Text("Carol"), Text("HR"), Float(70000), Bool(true)},
		{Int(4), Text("Dave"), Text("HR"), Float(65000), Bool(false)},
		{Int(5), Text("Eve"), Text("Eng"), Float(95000), Bool(true)},
		{Int(6), Text("Frank"), Text("Mgmt"), Float(120000), Bool(true)},
	}
	for _, row := range rows {
		mustInsert(t, tbl, row)
	}
	return tbl
}

// -------------------------------------------------------------------------
// Value tests
// -------------------------------------------------------------------------

func TestValueTypes(t *testing.T) {
	cases := []struct {
		v   Value
		typ ValueType
		str string
	}{
		{Null(), TypeNull, "NULL"},
		{Int(42), TypeInt, "42"},
		{Int(-7), TypeInt, "-7"},
		{Float(3.14), TypeFloat, "3.14"},
		{Text("hello"), TypeText, `"hello"`},
		{Bool(true), TypeBool, "true"},
		{Bool(false), TypeBool, "false"},
	}
	for _, c := range cases {
		if c.v.Type() != c.typ {
			t.Errorf("type: got %v want %v", c.v.Type(), c.typ)
		}
		if c.v.String() != c.str {
			t.Errorf("string: got %q want %q", c.v.String(), c.str)
		}
	}
}

func TestValueEqual(t *testing.T) {
	if !Int(5).Equal(Int(5)) {
		t.Fatal("5 == 5 should be true")
	}
	if Int(5).Equal(Int(6)) {
		t.Fatal("5 == 6 should be false")
	}
	if Null().Equal(Null()) {
		t.Fatal("NULL == NULL should be false (SQL semantics)")
	}
	if !Text("a").Equal(Text("a")) {
		t.Fatal("text equal failed")
	}
	if Text("a").Equal(Text("b")) {
		t.Fatal("text neq should be false")
	}
}

func TestValueCompare(t *testing.T) {
	cmp := func(a, b Value) int {
		c, ok := a.Compare(b)
		if !ok {
			t.Fatalf("Compare(%v, %v) returned ok=false", a, b)
		}
		return c
	}
	if cmp(Int(1), Int(2)) >= 0 {
		t.Fatal("1 < 2 failed")
	}
	if cmp(Int(2), Int(1)) <= 0 {
		t.Fatal("2 > 1 failed")
	}
	if cmp(Int(3), Int(3)) != 0 {
		t.Fatal("3 == 3 failed")
	}
	// Int vs Float cross-type.
	if cmp(Int(1), Float(1.5)) >= 0 {
		t.Fatal("1 < 1.5 failed")
	}
	// Text.
	if cmp(Text("a"), Text("b")) >= 0 {
		t.Fatal("a < b failed")
	}
	// NULL is incomparable.
	if _, ok := Null().Compare(Int(1)); ok {
		t.Fatal("NULL.Compare should return ok=false")
	}
}

func TestValuePanics(t *testing.T) {
	panicWith := func(fn func()) (panicked bool) {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		fn()
		return false
	}
	if !panicWith(func() { Null().AsInt() }) {
		t.Fatal("AsInt on NULL should panic")
	}
	if !panicWith(func() { Int(1).AsFloat() }) {
		t.Fatal("AsFloat on Int should panic")
	}
	if !panicWith(func() { Text("x").AsBool() }) {
		t.Fatal("AsBool on Text should panic")
	}
}

func TestValueToFloat(t *testing.T) {
	f, ok := Int(7).ToFloat()
	if !ok || f != 7.0 {
		t.Fatalf("Int(7).ToFloat()=(%v,%v)", f, ok)
	}
	f, ok = Float(2.5).ToFloat()
	if !ok || f != 2.5 {
		t.Fatalf("Float(2.5).ToFloat()=(%v,%v)", f, ok)
	}
	_, ok = Text("x").ToFloat()
	if ok {
		t.Fatal("Text.ToFloat should return ok=false")
	}
}

// -------------------------------------------------------------------------
// Expression tests
// -------------------------------------------------------------------------

func TestBinOpArithmetic(t *testing.T) {
	schema := NewSchema("a", TypeInt, "b", TypeFloat)
	row := Row{Int(10), Float(3.0)}

	cases := []struct {
		expr Expr
		want Value
	}{
		{BinOp{ColRef{"a"}, Literal{Int(5)}, OpAdd}, Int(15)},
		{BinOp{ColRef{"a"}, Literal{Int(3)}, OpSub}, Int(7)},
		{BinOp{ColRef{"a"}, Literal{Int(2)}, OpMul}, Int(20)},
		{BinOp{ColRef{"a"}, Literal{Int(4)}, OpDiv}, Int(2)},
		{BinOp{ColRef{"a"}, ColRef{"b"}, OpAdd}, Float(13.0)},
		{BinOp{ColRef{"a"}, Literal{Int(0)}, OpDiv}, Null()}, // div by zero
	}
	for _, c := range cases {
		got := c.expr.Eval(row, schema)
		if !got.Equal(c.want) {
			// Handle Null comparison separately.
			if c.want.IsNull() && got.IsNull() {
				continue
			}
			t.Errorf("expr=%v: got %v want %v", c.expr, got, c.want)
		}
	}
}

func TestBinOpComparison(t *testing.T) {
	schema := NewSchema("x", TypeInt)
	row := Row{Int(5)}

	cases := []struct {
		expr Expr
		want bool
	}{
		{BinOp{ColRef{"x"}, Literal{Int(5)}, OpEq}, true},
		{BinOp{ColRef{"x"}, Literal{Int(6)}, OpEq}, false},
		{BinOp{ColRef{"x"}, Literal{Int(6)}, OpNeq}, true},
		{BinOp{ColRef{"x"}, Literal{Int(6)}, OpLt}, true},
		{BinOp{ColRef{"x"}, Literal{Int(5)}, OpLte}, true},
		{BinOp{ColRef{"x"}, Literal{Int(4)}, OpGt}, true},
		{BinOp{ColRef{"x"}, Literal{Int(5)}, OpGte}, true},
	}
	for _, c := range cases {
		got := c.expr.Eval(row, schema)
		if got.typ != TypeBool || got.boolVal != c.want {
			t.Errorf("got %v want %v", got, c.want)
		}
	}
}

func TestBinOpLogical(t *testing.T) {
	schema := NewSchema("a", TypeBool, "b", TypeBool)
	tt := Row{Bool(true), Bool(true)}
	tf := Row{Bool(true), Bool(false)}

	and := BinOp{ColRef{"a"}, ColRef{"b"}, OpAnd}
	or := BinOp{ColRef{"a"}, ColRef{"b"}, OpOr}

	if !and.Eval(tt, schema).AsBool() {
		t.Fatal("true AND true should be true")
	}
	if and.Eval(tf, schema).AsBool() {
		t.Fatal("true AND false should be false")
	}
	if !or.Eval(tf, schema).AsBool() {
		t.Fatal("true OR false should be true")
	}
}

func TestNotExpr(t *testing.T) {
	schema := NewSchema("x", TypeBool)
	row := Row{Bool(true)}
	got := NotExpr{ColRef{"x"}}.Eval(row, schema)
	if got.AsBool() {
		t.Fatal("NOT true should be false")
	}
}

func TestIsNullExpr(t *testing.T) {
	schema := NewSchema("x", TypeInt)
	row := Row{Null()}
	expr := IsNullExpr{ColRef{"x"}}
	if !expr.Eval(row, schema).AsBool() {
		t.Fatal("IS NULL should be true for NULL value")
	}
	row2 := Row{Int(1)}
	if expr.Eval(row2, schema).AsBool() {
		t.Fatal("IS NULL should be false for non-NULL")
	}
}

func TestColRefMissing(t *testing.T) {
	schema := NewSchema("x", TypeInt)
	row := Row{Int(1)}
	v := ColRef{"missing"}.Eval(row, schema)
	if !v.IsNull() {
		t.Fatal("ColRef for missing column should return NULL")
	}
}

// -------------------------------------------------------------------------
// Table tests
// -------------------------------------------------------------------------

func TestTableInsertAndGet(t *testing.T) {
	tbl := buildEmployeeTable(t)
	if tbl.Len() != 6 {
		t.Fatalf("Len=%d want 6", tbl.Len())
	}
	row, ok := tbl.Get(Int(3))
	if !ok {
		t.Fatal("Get(3) returned false")
	}
	if row[1].AsText() != "Carol" {
		t.Fatalf("name=%q want Carol", row[1].AsText())
	}
}

func TestTableDuplicatePKError(t *testing.T) {
	tbl := mustTable(t, employeeSchema, "id")
	mustInsert(t, tbl, Row{Int(1), Text("A"), Text("X"), Float(1), Bool(true)})
	err := tbl.Insert(Row{Int(1), Text("B"), Text("Y"), Float(2), Bool(false)})
	if err == nil {
		t.Fatal("expected duplicate PK error, got nil")
	}
}

func TestTableUpsert(t *testing.T) {
	tbl := mustTable(t, employeeSchema, "id")
	mustInsert(t, tbl, Row{Int(1), Text("A"), Text("X"), Float(1000), Bool(true)})
	mustUpsert(t, tbl, Row{Int(1), Text("A-updated"), Text("X"), Float(2000), Bool(true)})
	row, ok := tbl.Get(Int(1))
	if !ok || row[1].AsText() != "A-updated" {
		t.Fatalf("upsert: got %v", row)
	}
	if tbl.Len() != 1 {
		t.Fatalf("Len=%d want 1 after upsert", tbl.Len())
	}
}

func TestTableDelete(t *testing.T) {
	tbl := buildEmployeeTable(t)
	if !tbl.Delete(Int(3)) {
		t.Fatal("Delete(3) returned false")
	}
	if tbl.Len() != 5 {
		t.Fatalf("Len=%d want 5", tbl.Len())
	}
	if _, ok := tbl.Get(Int(3)); ok {
		t.Fatal("Get(3) after delete should return false")
	}
	if tbl.Delete(Int(99)) {
		t.Fatal("Delete non-existent returned true")
	}
}

func TestTableSchemaMismatch(t *testing.T) {
	tbl := mustTable(t, employeeSchema, "id")
	err := tbl.Insert(Row{Int(1), Text("X")}) // too few columns
	if err == nil {
		t.Fatal("expected schema mismatch error")
	}
}

func TestNewTableInvalidPK(t *testing.T) {
	_, err := NewTable(employeeSchema, "nonexistent")
	if err == nil {
		t.Fatal("expected error for invalid PK column")
	}
	_, err = NewTable(employeeSchema) // no PK
	if err == nil {
		t.Fatal("expected error for empty PK")
	}
}

func TestTableCompositePK(t *testing.T) {
	schema := NewSchema("dept", TypeText, "seq", TypeInt, "val", TypeText)
	tbl := mustTable(t, schema, "dept", "seq")
	mustInsert(t, tbl, Row{Text("Eng"), Int(1), Text("a")})
	mustInsert(t, tbl, Row{Text("Eng"), Int(2), Text("b")})
	mustInsert(t, tbl, Row{Text("HR"), Int(1), Text("c")})
	if tbl.Len() != 3 {
		t.Fatalf("Len=%d want 3", tbl.Len())
	}
	// Duplicate composite PK should error.
	if err := tbl.Insert(Row{Text("Eng"), Int(1), Text("dup")}); err == nil {
		t.Fatal("expected composite PK duplicate error")
	}
}

// -------------------------------------------------------------------------
// TableScan / RangeScan
// -------------------------------------------------------------------------

func TestTableScanOrder(t *testing.T) {
	tbl := buildEmployeeTable(t)
	rows := mustCollect(t, NewTableScan(tbl))
	assertRowCount(t, rows, 6)
	// Rows must come out in PK (id) order.
	assertIntCol(t, rows, 0, []int64{1, 2, 3, 4, 5, 6})
}

func TestTableScanEmpty(t *testing.T) {
	tbl := mustTable(t, employeeSchema, "id")
	rows := mustCollect(t, NewTableScan(tbl))
	assertRowCount(t, rows, 0)
}

func TestRangeScan(t *testing.T) {
	tbl := buildEmployeeTable(t)
	rows := mustCollect(t, NewRangeScan(tbl, Int(2), Int(4)))
	assertRowCount(t, rows, 3)
	assertIntCol(t, rows, 0, []int64{2, 3, 4})
}

func TestRangeScanSingleRow(t *testing.T) {
	tbl := buildEmployeeTable(t)
	rows := mustCollect(t, NewRangeScan(tbl, Int(3), Int(3)))
	assertRowCount(t, rows, 1)
	if rows[0][1].AsText() != "Carol" {
		t.Fatalf("got %v", rows[0])
	}
}

func TestRangeScanEmpty(t *testing.T) {
	tbl := buildEmployeeTable(t)
	rows := mustCollect(t, NewRangeScan(tbl, Int(100), Int(200)))
	assertRowCount(t, rows, 0)
}

// -------------------------------------------------------------------------
// Filter
// -------------------------------------------------------------------------

func TestFilterBasic(t *testing.T) {
	tbl := buildEmployeeTable(t)
	// dept = "Eng"
	pred := BinOp{ColRef{"dept"}, Literal{Text("Eng")}, OpEq}
	rows := mustCollect(t, NewFilter(NewTableScan(tbl), pred))
	assertRowCount(t, rows, 3)
	for _, row := range rows {
		if row[2].AsText() != "Eng" {
			t.Fatalf("filter leaked non-Eng row: %v", row)
		}
	}
}

func TestFilterCompound(t *testing.T) {
	tbl := buildEmployeeTable(t)
	// dept = "Eng" AND active = true
	pred := BinOp{
		BinOp{ColRef{"dept"}, Literal{Text("Eng")}, OpEq},
		BinOp{ColRef{"active"}, Literal{Bool(true)}, OpEq},
		OpAnd,
	}
	rows := mustCollect(t, NewFilter(NewTableScan(tbl), pred))
	assertRowCount(t, rows, 3) // Alice, Bob, Eve
}

func TestFilterSalaryRange(t *testing.T) {
	tbl := buildEmployeeTable(t)
	// salary >= 80000 AND salary <= 95000
	pred := BinOp{
		BinOp{ColRef{"salary"}, Literal{Float(80000)}, OpGte},
		BinOp{ColRef{"salary"}, Literal{Float(95000)}, OpLte},
		OpAnd,
	}
	rows := mustCollect(t, NewFilter(NewTableScan(tbl), pred))
	assertRowCount(t, rows, 3) // Alice(90k), Bob(80k), Eve(95k)
}

func TestFilterNoMatch(t *testing.T) {
	tbl := buildEmployeeTable(t)
	pred := BinOp{ColRef{"dept"}, Literal{Text("Finance")}, OpEq}
	rows := mustCollect(t, NewFilter(NewTableScan(tbl), pred))
	assertRowCount(t, rows, 0)
}

func TestFilterAllMatch(t *testing.T) {
	tbl := buildEmployeeTable(t)
	pred := BinOp{ColRef{"id"}, Literal{Int(0)}, OpGt}
	rows := mustCollect(t, NewFilter(NewTableScan(tbl), pred))
	assertRowCount(t, rows, 6)
}

func TestFilterNotActive(t *testing.T) {
	tbl := buildEmployeeTable(t)
	pred := BinOp{ColRef{"active"}, Literal{Bool(false)}, OpEq}
	rows := mustCollect(t, NewFilter(NewTableScan(tbl), pred))
	assertRowCount(t, rows, 1) // Dave
	if rows[0][1].AsText() != "Dave" {
		t.Fatalf("expected Dave, got %v", rows[0][1])
	}
}

// -------------------------------------------------------------------------
// Project
// -------------------------------------------------------------------------

func TestProjectColumns(t *testing.T) {
	tbl := buildEmployeeTable(t)
	proj := NewProject(NewTableScan(tbl),
		Projection{ColRef{"id"}, "id"},
		Projection{ColRef{"name"}, "name"},
	)
	rows := mustCollect(t, proj)
	assertRowCount(t, rows, 6)
	if proj.Schema().Width() != 2 {
		t.Fatalf("schema width=%d want 2", proj.Schema().Width())
	}
	assertIntCol(t, rows, 0, []int64{1, 2, 3, 4, 5, 6})
}

func TestProjectComputed(t *testing.T) {
	tbl := buildEmployeeTable(t)
	// Output: id, salary * 1.1 AS raised
	proj := NewProject(NewTableScan(tbl),
		Projection{ColRef{"id"}, "id"},
		Projection{BinOp{ColRef{"salary"}, Literal{Float(1.1)}, OpMul}, "raised"},
	)
	rows := mustCollect(t, proj)
	assertRowCount(t, rows, 6)
	// Alice: 90000 * 1.1 = 99000
	raised := rows[0][1].AsFloat()
	if math.Abs(raised-99000) > 0.01 {
		t.Fatalf("raised salary for Alice: got %v want 99000", raised)
	}
}

func TestProjectRename(t *testing.T) {
	tbl := buildEmployeeTable(t)
	proj := NewProject(NewTableScan(tbl),
		Projection{ColRef{"name"}, "employee_name"},
	)
	rows := mustCollect(t, proj)
	if proj.Schema().Columns[0].Name != "employee_name" {
		t.Fatal("rename failed")
	}
	assertTextCol(t, rows, 0, []string{"Alice", "Bob", "Carol", "Dave", "Eve", "Frank"})
}

// -------------------------------------------------------------------------
// Limit / Offset
// -------------------------------------------------------------------------

func TestLimit(t *testing.T) {
	tbl := buildEmployeeTable(t)
	rows := mustCollect(t, NewLimit(NewTableScan(tbl), 3))
	assertRowCount(t, rows, 3)
	assertIntCol(t, rows, 0, []int64{1, 2, 3})
}

func TestLimitBeyondSize(t *testing.T) {
	tbl := buildEmployeeTable(t)
	rows := mustCollect(t, NewLimit(NewTableScan(tbl), 100))
	assertRowCount(t, rows, 6)
}

func TestLimitZero(t *testing.T) {
	tbl := buildEmployeeTable(t)
	rows := mustCollect(t, NewLimit(NewTableScan(tbl), 0))
	assertRowCount(t, rows, 0)
}

func TestOffset(t *testing.T) {
	tbl := buildEmployeeTable(t)
	rows := mustCollect(t, NewOffset(NewTableScan(tbl), 3))
	assertRowCount(t, rows, 3)
	assertIntCol(t, rows, 0, []int64{4, 5, 6})
}

func TestOffsetBeyondSize(t *testing.T) {
	tbl := buildEmployeeTable(t)
	rows := mustCollect(t, NewOffset(NewTableScan(tbl), 10))
	assertRowCount(t, rows, 0)
}

func TestLimitOffset(t *testing.T) {
	tbl := buildEmployeeTable(t)
	// OFFSET 2 LIMIT 3 → rows 3,4,5
	rows := mustCollect(t, NewLimit(NewOffset(NewTableScan(tbl), 2), 3))
	assertRowCount(t, rows, 3)
	assertIntCol(t, rows, 0, []int64{3, 4, 5})
}

// -------------------------------------------------------------------------
// OrderBy
// -------------------------------------------------------------------------

func TestOrderByAsc(t *testing.T) {
	tbl := buildEmployeeTable(t)
	rows := mustCollect(t, NewOrderBy(NewTableScan(tbl), SortKey{"salary", false}))
	assertRowCount(t, rows, 6)
	// Salaries ascending: Dave(65k), Carol(70k), Bob(80k), Alice(90k), Eve(95k), Frank(120k)
	assertTextCol(t, rows, 1, []string{"Dave", "Carol", "Bob", "Alice", "Eve", "Frank"})
}

func TestOrderByDesc(t *testing.T) {
	tbl := buildEmployeeTable(t)
	rows := mustCollect(t, NewOrderBy(NewTableScan(tbl), SortKey{"salary", true}))
	assertTextCol(t, rows, 1, []string{"Frank", "Eve", "Alice", "Bob", "Carol", "Dave"})
}

func TestOrderByMultiKey(t *testing.T) {
	tbl := buildEmployeeTable(t)
	// ORDER BY dept ASC, salary DESC
	rows := mustCollect(t, NewOrderBy(NewTableScan(tbl),
		SortKey{"dept", false},
		SortKey{"salary", true},
	))
	assertRowCount(t, rows, 6)
	// Eng: Eve(95k), Alice(90k), Bob(80k); HR: Carol(70k), Dave(65k); Mgmt: Frank(120k)
	assertTextCol(t, rows, 1, []string{"Eve", "Alice", "Bob", "Carol", "Dave", "Frank"})
}

func TestOrderByText(t *testing.T) {
	tbl := buildEmployeeTable(t)
	rows := mustCollect(t, NewOrderBy(NewTableScan(tbl), SortKey{"name", false}))
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r[1].AsText()
	}
	sorted := make([]string, len(names))
	copy(sorted, names)
	sort.Strings(sorted)
	for i := range sorted {
		if names[i] != sorted[i] {
			t.Fatalf("order[%d]=%q want %q", i, names[i], sorted[i])
		}
	}
}

// -------------------------------------------------------------------------
// Join
// -------------------------------------------------------------------------

func buildDeptTable(t *testing.T) *Table {
	t.Helper()
	schema := NewSchema("dept_name", TypeText, "budget", TypeInt)
	tbl := mustTable(t, schema, "dept_name")
	mustInsert(t, tbl, Row{Text("Eng"), Int(500000)})
	mustInsert(t, tbl, Row{Text("HR"), Int(200000)})
	mustInsert(t, tbl, Row{Text("Mgmt"), Int(300000)})
	return tbl
}

func TestInnerJoin(t *testing.T) {
	emp := buildEmployeeTable(t)
	dept := buildDeptTable(t)
	pred := BinOp{ColRef{"dept"}, ColRef{"dept_name"}, OpEq}
	rows := mustCollect(t, NewJoin(NewTableScan(emp), NewTableScan(dept), pred, InnerJoin))
	assertRowCount(t, rows, 6) // all 6 employees have matching depts
	// Each row should have 7 columns (5 emp + 2 dept).
	if len(rows[0]) != 7 {
		t.Fatalf("joined row width=%d want 7", len(rows[0]))
	}
}

func TestInnerJoinFiltered(t *testing.T) {
	emp := buildEmployeeTable(t)
	dept := buildDeptTable(t)
	// Join dept="Eng" employees with dept table.
	empScan := NewFilter(NewTableScan(emp), BinOp{ColRef{"dept"}, Literal{Text("Eng")}, OpEq})
	pred := BinOp{ColRef{"dept"}, ColRef{"dept_name"}, OpEq}
	rows := mustCollect(t, NewJoin(empScan, NewTableScan(dept), pred, InnerJoin))
	assertRowCount(t, rows, 3)
}

func TestCrossJoin(t *testing.T) {
	// nil predicate = cross join
	schema := NewSchema("x", TypeInt)
	a := mustTable(t, schema, "x")
	b := mustTable(t, schema, "x")
	for i := 1; i <= 3; i++ {
		mustInsert(t, a, Row{Int(int64(i))})
		mustInsert(t, b, Row{Int(int64(i))})
	}
	rows := mustCollect(t, NewJoin(NewTableScan(a), NewTableScan(b), nil, InnerJoin))
	assertRowCount(t, rows, 9) // 3 × 3
}

func TestLeftOuterJoin(t *testing.T) {
	emp := buildEmployeeTable(t)
	// Dept table missing "Eng" — so Eng employees get NULLs on right side.
	schema := NewSchema("dept_name", TypeText, "budget", TypeInt)
	deptTbl := mustTable(t, schema, "dept_name")
	mustInsert(t, deptTbl, Row{Text("HR"), Int(200000)})
	mustInsert(t, deptTbl, Row{Text("Mgmt"), Int(300000)})

	pred := BinOp{ColRef{"dept"}, ColRef{"dept_name"}, OpEq}
	rows := mustCollect(t, NewJoin(NewTableScan(emp), NewTableScan(deptTbl), pred, LeftOuterJoin))
	assertRowCount(t, rows, 6) // all 6 employees, Eng ones with NULL budget

	// Find an Eng employee row — budget (col 6) should be NULL.
	for _, row := range rows {
		if row[2].AsText() == "Eng" {
			if !row[6].IsNull() {
				t.Fatalf("Eng employee should have NULL budget in LEFT JOIN, got %v", row[6])
			}
		}
	}
}

func TestJoinNoMatches(t *testing.T) {
	emp := buildEmployeeTable(t)
	schema := NewSchema("dept_name", TypeText, "budget", TypeInt)
	emptyDept := mustTable(t, schema, "dept_name")
	pred := BinOp{ColRef{"dept"}, ColRef{"dept_name"}, OpEq}
	rows := mustCollect(t, NewJoin(NewTableScan(emp), NewTableScan(emptyDept), pred, InnerJoin))
	assertRowCount(t, rows, 0)
}

// -------------------------------------------------------------------------
// HashAggregate
// -------------------------------------------------------------------------

func TestAggCountStar(t *testing.T) {
	tbl := buildEmployeeTable(t)
	agg := NewHashAggregate(
		NewTableScan(tbl),
		nil,
		AggSpec{AggCount, nil, "count"},
	)
	rows := mustCollect(t, agg)
	assertRowCount(t, rows, 1)
	if rows[0][0].AsInt() != 6 {
		t.Fatalf("COUNT(*)=%v want 6", rows[0][0])
	}
}

func TestAggSumAvg(t *testing.T) {
	tbl := buildEmployeeTable(t)
	agg := NewHashAggregate(
		NewTableScan(tbl),
		nil,
		AggSpec{AggSum, ColRef{"salary"}, "total"},
		AggSpec{AggAvg, ColRef{"salary"}, "avg"},
	)
	rows := mustCollect(t, agg)
	assertRowCount(t, rows, 1)
	// Total: 90k+80k+70k+65k+95k+120k = 520000
	total, _ := rows[0][0].ToFloat()
	if math.Abs(total-520000) > 0.01 {
		t.Fatalf("SUM(salary)=%v want 520000", total)
	}
	avg, _ := rows[0][1].ToFloat()
	if math.Abs(avg-520000.0/6) > 0.01 {
		t.Fatalf("AVG(salary)=%v want %v", avg, 520000.0/6)
	}
}

func TestAggMinMax(t *testing.T) {
	tbl := buildEmployeeTable(t)
	agg := NewHashAggregate(
		NewTableScan(tbl),
		nil,
		AggSpec{AggMin, ColRef{"salary"}, "min_sal"},
		AggSpec{AggMax, ColRef{"salary"}, "max_sal"},
	)
	rows := mustCollect(t, agg)
	minSal, _ := rows[0][0].ToFloat()
	maxSal, _ := rows[0][1].ToFloat()
	if math.Abs(minSal-65000) > 0.01 {
		t.Fatalf("MIN=%v want 65000", minSal)
	}
	if math.Abs(maxSal-120000) > 0.01 {
		t.Fatalf("MAX=%v want 120000", maxSal)
	}
}

func TestAggGroupBy(t *testing.T) {
	tbl := buildEmployeeTable(t)
	agg := NewHashAggregate(
		NewTableScan(tbl),
		[]string{"dept"},
		AggSpec{AggCount, nil, "cnt"},
		AggSpec{AggSum, ColRef{"salary"}, "total"},
	)
	rows := mustCollect(t, agg)
	assertRowCount(t, rows, 3) // Eng, HR, Mgmt

	// Build a map dept → row for easy assertion.
	byDept := map[string]Row{}
	for _, r := range rows {
		byDept[r[0].AsText()] = r
	}
	if byDept["Eng"][1].AsInt() != 3 {
		t.Fatalf("Eng count=%v want 3", byDept["Eng"][1])
	}
	engTotal, _ := byDept["Eng"][2].ToFloat()
	if math.Abs(engTotal-265000) > 0.01 { // Alice+Bob+Eve
		t.Fatalf("Eng total=%v want 265000", engTotal)
	}
	if byDept["HR"][1].AsInt() != 2 {
		t.Fatalf("HR count=%v want 2", byDept["HR"][1])
	}
}

func TestAggGroupByInvalidColumn(t *testing.T) {
	tbl := buildEmployeeTable(t)
	agg := NewHashAggregate(
		NewTableScan(tbl),
		[]string{"nonexistent"},
		AggSpec{AggCount, nil, "cnt"},
	)
	if err := agg.Open(); err == nil {
		t.Fatal("expected error for invalid GROUP BY column")
	}
	agg.Close()
}

func TestAggEmptyInput(t *testing.T) {
	tbl := mustTable(t, employeeSchema, "id")
	agg := NewHashAggregate(
		NewTableScan(tbl),
		nil,
		AggSpec{AggCount, nil, "cnt"},
	)
	rows := mustCollect(t, agg)
	assertRowCount(t, rows, 1)
	if rows[0][0].AsInt() != 0 {
		t.Fatalf("COUNT(*) on empty table=%v want 0", rows[0][0])
	}
}

func TestAggEmptyInputGroupBy(t *testing.T) {
	tbl := mustTable(t, employeeSchema, "id")
	agg := NewHashAggregate(
		NewTableScan(tbl),
		[]string{"dept"},
		AggSpec{AggCount, nil, "cnt"},
	)
	rows := mustCollect(t, agg)
	assertRowCount(t, rows, 0) // no groups from empty input
}

// -------------------------------------------------------------------------
// Pipeline composition tests
// -------------------------------------------------------------------------

func TestFilterProjectPipeline(t *testing.T) {
	tbl := buildEmployeeTable(t)
	// SELECT name, salary FROM employees WHERE dept='Eng' ORDER BY salary DESC
	it := NewOrderBy(
		NewProject(
			NewFilter(
				NewTableScan(tbl),
				BinOp{ColRef{"dept"}, Literal{Text("Eng")}, OpEq},
			),
			Projection{ColRef{"name"}, "name"},
			Projection{ColRef{"salary"}, "salary"},
		),
		SortKey{"salary", true},
	)
	rows := mustCollect(t, it)
	assertRowCount(t, rows, 3)
	assertTextCol(t, rows, 0, []string{"Eve", "Alice", "Bob"})
}

func TestJoinFilterAggPipeline(t *testing.T) {
	emp := buildEmployeeTable(t)
	dept := buildDeptTable(t)
	// SELECT dept, COUNT(*) FROM emp JOIN dept ON dept=dept_name
	// WHERE active=true GROUP BY dept ORDER BY dept ASC
	joined := NewJoin(
		NewFilter(NewTableScan(emp), BinOp{ColRef{"active"}, Literal{Bool(true)}, OpEq}),
		NewTableScan(dept),
		BinOp{ColRef{"dept"}, ColRef{"dept_name"}, OpEq},
		InnerJoin,
	)
	agg := NewHashAggregate(joined, []string{"dept"}, AggSpec{AggCount, nil, "cnt"})
	sorted := NewOrderBy(agg, SortKey{"dept", false})
	rows := mustCollect(t, sorted)
	assertRowCount(t, rows, 3)
	// Eng: Alice, Bob, Eve (all active); HR: Carol (Dave inactive); Mgmt: Frank
	byDept := map[string]int64{}
	for _, r := range rows {
		byDept[r[0].AsText()] = r[1].AsInt()
	}
	if byDept["Eng"] != 3 {
		t.Fatalf("Eng count=%d want 3", byDept["Eng"])
	}
	if byDept["HR"] != 1 {
		t.Fatalf("HR count=%d want 1", byDept["HR"])
	}
}

func TestLimitOffsetOrderPipeline(t *testing.T) {
	tbl := buildEmployeeTable(t)
	// SELECT id FROM employees ORDER BY salary DESC LIMIT 3 OFFSET 1
	// → skip Frank(120k), take Eve(95k), Alice(90k), Bob(80k)
	it := NewLimit(
		NewOffset(
			NewProject(
				NewOrderBy(NewTableScan(tbl), SortKey{"salary", true}),
				Projection{ColRef{"id"}, "id"},
				Projection{ColRef{"name"}, "name"},
			),
			1,
		),
		3,
	)
	rows := mustCollect(t, it)
	assertRowCount(t, rows, 3)
	assertTextCol(t, rows, 1, []string{"Eve", "Alice", "Bob"})
}

// -------------------------------------------------------------------------
// Schema helpers
// -------------------------------------------------------------------------

func TestSchemaIndex(t *testing.T) {
	s := NewSchema("a", TypeInt, "b", TypeText, "c", TypeFloat)
	if s.Index("b") != 1 {
		t.Fatalf("Index(b)=%d want 1", s.Index("b"))
	}
	if s.Index("missing") != -1 {
		t.Fatal("Index(missing) should return -1")
	}
}

func TestNewSchemaPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("NewSchema with odd args should panic")
		}
	}()
	NewSchema("a") // odd count → panic
}

// -------------------------------------------------------------------------
// Collect helper
// -------------------------------------------------------------------------

func TestCollectClosesIterator(t *testing.T) {
	tbl := buildEmployeeTable(t)
	rows, err := Collect(NewTableScan(tbl))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 {
		t.Fatalf("Collect returned %d rows", len(rows))
	}
}

// -------------------------------------------------------------------------
// Large table stress test
// -------------------------------------------------------------------------

func TestLargeTablePipeline(t *testing.T) {
	schema := NewSchema("id", TypeInt, "group", TypeText, "val", TypeFloat)
	tbl := mustTable(t, schema, "id")

	const N = 1000
	for i := 0; i < N; i++ {
		grp := fmt.Sprintf("g%d", i%10)
		mustInsert(t, tbl, Row{Int(int64(i)), Text(grp), Float(float64(i))})
	}

	// SELECT group, COUNT(*), AVG(val) GROUP BY group ORDER BY group
	agg := NewHashAggregate(
		NewTableScan(tbl),
		[]string{"group"},
		AggSpec{AggCount, nil, "cnt"},
		AggSpec{AggAvg, ColRef{"val"}, "avg_val"},
	)
	rows := mustCollect(t, NewOrderBy(agg, SortKey{"group", false}))
	if len(rows) != 10 {
		t.Fatalf("expected 10 groups, got %d", len(rows))
	}
	for _, r := range rows {
		if r[1].AsInt() != 100 {
			t.Fatalf("group %v count=%v want 100", r[0], r[1])
		}
	}
}
