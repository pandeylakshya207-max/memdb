package sql

import (
	"math"
	"strings"
	"testing"

	"github.com/pandeylakshya207-max/memdb/query"
)

// -------------------------------------------------------------------------
// Lexer tests
// -------------------------------------------------------------------------

func TestLexerBasic(t *testing.T) {
	tokens, err := Tokenise("SELECT * FROM employees WHERE id = 1")
	if err != nil {
		t.Fatalf("Tokenise: %v", err)
	}
	kinds := make([]TokenKind, 0, len(tokens))
	for _, tok := range tokens {
		kinds = append(kinds, tok.Kind)
	}
	want := []TokenKind{tokSelect, tokStar, tokFrom, tokIdent, tokWhere, tokIdent, tokEq, tokIntLit, tokEOF}
	if len(kinds) != len(want) {
		t.Fatalf("token count %d != %d: %v", len(kinds), len(want), tokens)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("kinds[%d]=%d want %d", i, kinds[i], want[i])
		}
	}
}

func TestLexerStringLiteral(t *testing.T) {
	tokens, err := Tokenise("'hello world'")
	if err != nil {
		t.Fatalf("Tokenise: %v", err)
	}
	if tokens[0].Kind != tokStrLit || tokens[0].Text != "hello world" {
		t.Fatalf("string literal: %v", tokens[0])
	}
}

func TestLexerEscapedQuote(t *testing.T) {
	tokens, err := Tokenise("'it''s'")
	if err != nil {
		t.Fatal(err)
	}
	if tokens[0].Text != "it's" {
		t.Fatalf("escaped quote: got %q want \"it's\"", tokens[0].Text)
	}
}

func TestLexerNumbers(t *testing.T) {
	tokens, err := Tokenise("42 3.14 -7")
	if err != nil {
		t.Fatal(err)
	}
	if tokens[0].Kind != tokIntLit || tokens[0].Text != "42" {
		t.Fatalf("int: %v", tokens[0])
	}
	if tokens[1].Kind != tokFloatLit || tokens[1].Text != "3.14" {
		t.Fatalf("float: %v", tokens[1])
	}
}

func TestLexerOperators(t *testing.T) {
	src := "= != <> < <= > >="
	tokens, err := Tokenise(src)
	if err != nil {
		t.Fatal(err)
	}
	want := []TokenKind{tokEq, tokNeq, tokNeq, tokLt, tokLte, tokGt, tokGte, tokEOF}
	for i, w := range want {
		if tokens[i].Kind != w {
			t.Errorf("op[%d]: got %d want %d", i, tokens[i].Kind, w)
		}
	}
}

func TestLexerKeywordsCaseInsensitive(t *testing.T) {
	tokens, err := Tokenise("select FROM Where GROUP by order BY")
	if err != nil {
		t.Fatal(err)
	}
	want := []TokenKind{tokSelect, tokFrom, tokWhere, tokGroup, tokBy, tokOrder, tokBy, tokEOF}
	for i, w := range want {
		if tokens[i].Kind != w {
			t.Errorf("kw[%d]: got %d want %d", i, tokens[i].Kind, w)
		}
	}
}

func TestLexerLineComment(t *testing.T) {
	tokens, err := Tokenise("SELECT -- this is a comment\n42")
	if err != nil {
		t.Fatal(err)
	}
	if tokens[0].Kind != tokSelect || tokens[1].Kind != tokIntLit {
		t.Fatalf("comment not skipped: %v", tokens)
	}
}

func TestLexerError(t *testing.T) {
	_, err := Tokenise("SELECT @ FROM t")
	if err == nil {
		t.Fatal("expected lexer error for '@'")
	}
}

// -------------------------------------------------------------------------
// Parser tests
// -------------------------------------------------------------------------

func mustParse(t *testing.T, src string) Stmt {
	t.Helper()
	stmt, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	return stmt
}

func mustParseSelect(t *testing.T, src string) *SelectStmt {
	t.Helper()
	stmt := mustParse(t, src)
	s, ok := stmt.(*SelectStmt)
	if !ok {
		t.Fatalf("expected SelectStmt, got %T", stmt)
	}
	return s
}

func TestParseSelectStar(t *testing.T) {
	s := mustParseSelect(t, "SELECT * FROM employees")
	if s.From != "employees" {
		t.Fatalf("From=%q want employees", s.From)
	}
	if len(s.Columns) != 1 || !s.Columns[0].Star {
		t.Fatal("expected SELECT *")
	}
}

func TestParseSelectColumns(t *testing.T) {
	s := mustParseSelect(t, "SELECT id, name AS n, salary FROM emp")
	if len(s.Columns) != 3 {
		t.Fatalf("col count=%d want 3", len(s.Columns))
	}
	if s.Columns[1].Alias != "n" {
		t.Fatalf("alias=%q want n", s.Columns[1].Alias)
	}
}

func TestParseWhere(t *testing.T) {
	s := mustParseSelect(t, "SELECT * FROM t WHERE id > 5 AND active = TRUE")
	if s.Where == nil {
		t.Fatal("WHERE is nil")
	}
	bin, ok := s.Where.(*BinaryExpr)
	if !ok || bin.Op != BinOpAnd {
		t.Fatal("expected AND at root of WHERE")
	}
}

func TestParseGroupByHaving(t *testing.T) {
	s := mustParseSelect(t, "SELECT dept, COUNT(*) FROM emp GROUP BY dept HAVING COUNT(*) > 2")
	if len(s.GroupBy) != 1 || s.GroupBy[0] != "dept" {
		t.Fatalf("GroupBy=%v", s.GroupBy)
	}
	if s.Having == nil {
		t.Fatal("HAVING is nil")
	}
}

func TestParseOrderByLimitOffset(t *testing.T) {
	s := mustParseSelect(t, "SELECT * FROM t ORDER BY salary DESC, name ASC LIMIT 10 OFFSET 5")
	if len(s.OrderBy) != 2 {
		t.Fatalf("OrderBy len=%d want 2", len(s.OrderBy))
	}
	if !s.OrderBy[0].Desc {
		t.Fatal("first ORDER BY should be DESC")
	}
	if s.OrderBy[1].Desc {
		t.Fatal("second ORDER BY should be ASC")
	}
	if s.Limit == nil || *s.Limit != 10 {
		t.Fatalf("Limit=%v want 10", s.Limit)
	}
	if s.Offset == nil || *s.Offset != 5 {
		t.Fatalf("Offset=%v want 5", s.Offset)
	}
}

func TestParseInnerJoin(t *testing.T) {
	s := mustParseSelect(t, "SELECT * FROM emp JOIN dept ON dept = dept_name")
	if s.Join == nil {
		t.Fatal("JOIN is nil")
	}
	if s.Join.Kind != JoinInner {
		t.Fatal("expected INNER join")
	}
	if s.Join.Table != "dept" {
		t.Fatalf("join table=%q want dept", s.Join.Table)
	}
}

func TestParseLeftJoin(t *testing.T) {
	s := mustParseSelect(t, "SELECT * FROM emp LEFT JOIN dept ON dept = dept_name")
	if s.Join == nil || s.Join.Kind != JoinLeft {
		t.Fatal("expected LEFT join")
	}
}

func TestParseDistinct(t *testing.T) {
	s := mustParseSelect(t, "SELECT DISTINCT dept FROM emp")
	if !s.Distinct {
		t.Fatal("expected DISTINCT")
	}
}

func TestParseInsert(t *testing.T) {
	stmt := mustParse(t, "INSERT INTO emp (id, name) VALUES (1, 'Alice')")
	ins, ok := stmt.(*InsertStmt)
	if !ok {
		t.Fatalf("expected InsertStmt, got %T", stmt)
	}
	if ins.Table != "emp" || len(ins.Columns) != 2 {
		t.Fatalf("insert: %v", ins)
	}
}

func TestParseUpdate(t *testing.T) {
	stmt := mustParse(t, "UPDATE emp SET salary = 100000 WHERE id = 1")
	upd, ok := stmt.(*UpdateStmt)
	if !ok {
		t.Fatalf("expected UpdateStmt, got %T", stmt)
	}
	if upd.Table != "emp" || len(upd.Sets) != 1 {
		t.Fatalf("update: %v", upd)
	}
}

func TestParseDelete(t *testing.T) {
	stmt := mustParse(t, "DELETE FROM emp WHERE id = 3")
	del, ok := stmt.(*DeleteStmt)
	if !ok {
		t.Fatalf("expected DeleteStmt, got %T", stmt)
	}
	if del.Table != "emp" {
		t.Fatalf("delete table=%q", del.Table)
	}
}

func TestParseCreateTable(t *testing.T) {
	stmt := mustParse(t, `CREATE TABLE employees (
		id INT PRIMARY KEY,
		name TEXT,
		salary FLOAT,
		active BOOL
	)`)
	ct, ok := stmt.(*CreateTableStmt)
	if !ok {
		t.Fatalf("expected CreateTableStmt, got %T", stmt)
	}
	if ct.Table != "employees" || len(ct.Columns) != 4 {
		t.Fatalf("create: table=%q cols=%d", ct.Table, len(ct.Columns))
	}
	if len(ct.PrimaryKey) != 1 || ct.PrimaryKey[0] != "id" {
		t.Fatalf("primary key=%v", ct.PrimaryKey)
	}
}

func TestParseDropTable(t *testing.T) {
	stmt := mustParse(t, "DROP TABLE employees")
	dt, ok := stmt.(*DropTableStmt)
	if !ok {
		t.Fatalf("expected DropTableStmt, got %T", stmt)
	}
	if dt.Table != "employees" {
		t.Fatalf("drop table=%q", dt.Table)
	}
}

func TestParseExprPrecedence(t *testing.T) {
	// 1 + 2 * 3 should parse as 1 + (2 * 3)
	s := mustParseSelect(t, "SELECT 1 + 2 * 3 FROM t")
	col := s.Columns[0].Expr
	add, ok := col.(*BinaryExpr)
	if !ok || add.Op != BinOpAdd {
		t.Fatalf("expected ADD at root, got %T", col)
	}
	_, ok = add.Right.(*BinaryExpr)
	if !ok {
		t.Fatal("expected MUL on right side of ADD")
	}
}

func TestParseIsNull(t *testing.T) {
	s := mustParseSelect(t, "SELECT * FROM t WHERE name IS NULL")
	isn, ok := s.Where.(*IsNullExpr)
	if !ok || !isn.IsNull {
		t.Fatalf("expected IS NULL, got %T", s.Where)
	}
}

func TestParseIsNotNull(t *testing.T) {
	s := mustParseSelect(t, "SELECT * FROM t WHERE name IS NOT NULL")
	isn, ok := s.Where.(*IsNullExpr)
	if !ok || isn.IsNull {
		t.Fatalf("expected IS NOT NULL, got %T", s.Where)
	}
}

func TestParseAggFunctions(t *testing.T) {
	s := mustParseSelect(t, "SELECT COUNT(*), SUM(salary), AVG(salary), MIN(id), MAX(id) FROM emp")
	if len(s.Columns) != 5 {
		t.Fatalf("col count=%d want 5", len(s.Columns))
	}
	funcs := []AggFunc{AggFuncCount, AggFuncSum, AggFuncAvg, AggFuncMin, AggFuncMax}
	for i, f := range funcs {
		agg, ok := s.Columns[i].Expr.(*AggExpr)
		if !ok || agg.Func != f {
			t.Errorf("col[%d]: expected agg func %d, got %T", i, f, s.Columns[i].Expr)
		}
	}
}

func TestParseSemicolon(t *testing.T) {
	_, err := Parse("SELECT * FROM t;")
	if err != nil {
		t.Fatalf("trailing semicolon should not error: %v", err)
	}
}

func TestParseError(t *testing.T) {
	cases := []string{
		"SELECT FROM t",            // missing column list context (SELECT FROM is actually valid for *... actually an error since no col before FROM)
		"SELECT * FROM",            // missing table name
		"CREATE TABLE t (id INT)",  // missing PRIMARY KEY
		"INSERT INTO t VALUES (1)", // missing column list
	}
	for _, c := range cases {
		_, err := Parse(c)
		if err == nil {
			t.Errorf("expected parse error for %q, got nil", c)
		}
	}
}

func TestPrintExpr(t *testing.T) {
	cases := []struct {
		expr Expr
		want string
	}{
		{&ColumnExpr{Name: "salary"}, "salary"},
		{&ColumnExpr{Table: "emp", Name: "id"}, "emp.id"},
		{&LitExpr{Kind: LitInt, IntVal: 42}, "42"},
		{&LitExpr{Kind: LitStr, StrVal: "hello"}, "'hello'"},
		{&LitExpr{Kind: LitNull}, "NULL"},
		{&LitExpr{Kind: LitBool, BoolVal: true}, "TRUE"},
		{&AggExpr{Func: AggFuncCount}, "COUNT(*)"},
		{&AggExpr{Func: AggFuncSum, Arg: &ColumnExpr{Name: "salary"}}, "SUM(salary)"},
	}
	for _, c := range cases {
		got := PrintExpr(c.expr)
		if got != c.want {
			t.Errorf("PrintExpr: got %q want %q", got, c.want)
		}
	}
}

// -------------------------------------------------------------------------
// End-to-end: Exec tests
// -------------------------------------------------------------------------

func makeTestDB(t *testing.T) *Database {
	t.Helper()
	db := NewDatabase()

	// CREATE TABLE via SQL.
	_, err := Exec(db, `CREATE TABLE employees (
		id INT PRIMARY KEY,
		name TEXT,
		dept TEXT,
		salary FLOAT,
		active BOOL
	)`)
	if err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	// INSERT rows.
	inserts := []string{
		"INSERT INTO employees (id, name, dept, salary, active) VALUES (1, 'Alice', 'Eng', 90000.0, TRUE)",
		"INSERT INTO employees (id, name, dept, salary, active) VALUES (2, 'Bob', 'Eng', 80000.0, TRUE)",
		"INSERT INTO employees (id, name, dept, salary, active) VALUES (3, 'Carol', 'HR', 70000.0, TRUE)",
		"INSERT INTO employees (id, name, dept, salary, active) VALUES (4, 'Dave', 'HR', 65000.0, FALSE)",
		"INSERT INTO employees (id, name, dept, salary, active) VALUES (5, 'Eve', 'Eng', 95000.0, TRUE)",
		"INSERT INTO employees (id, name, dept, salary, active) VALUES (6, 'Frank', 'Mgmt', 120000.0, TRUE)",
	}
	for _, ins := range inserts {
		res, err := Exec(db, ins)
		if err != nil {
			t.Fatalf("INSERT: %v", err)
		}
		if res.RowsAffected != 1 {
			t.Fatalf("INSERT rows affected=%d", res.RowsAffected)
		}
	}
	return db
}

func TestExecSelectStar(t *testing.T) {
	db := makeTestDB(t)
	res, err := Exec(db, "SELECT * FROM employees")
	if err != nil {
		t.Fatalf("SELECT *: %v", err)
	}
	if len(res.Rows) != 6 {
		t.Fatalf("row count=%d want 6", len(res.Rows))
	}
}

func TestExecSelectWhere(t *testing.T) {
	db := makeTestDB(t)
	res, err := Exec(db, "SELECT * FROM employees WHERE dept = 'Eng'")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("Eng employees=%d want 3", len(res.Rows))
	}
}

func TestExecSelectColumns(t *testing.T) {
	db := makeTestDB(t)
	res, err := Exec(db, "SELECT name, salary FROM employees WHERE id = 1")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows=%d want 1", len(res.Rows))
	}
	if res.Rows[0][0].AsText() != "Alice" {
		t.Fatalf("name=%v want Alice", res.Rows[0][0])
	}
	if res.Schema.Width() != 2 {
		t.Fatalf("schema width=%d want 2", res.Schema.Width())
	}
}

func TestExecSelectOrderBy(t *testing.T) {
	db := makeTestDB(t)
	res, err := Exec(db, "SELECT name, salary FROM employees ORDER BY salary DESC")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 6 {
		t.Fatalf("rows=%d", len(res.Rows))
	}
	if res.Rows[0][0].AsText() != "Frank" {
		t.Fatalf("first by salary desc=%v want Frank", res.Rows[0][0])
	}
}

func TestExecSelectLimitOffset(t *testing.T) {
	db := makeTestDB(t)
	res, err := Exec(db, "SELECT name FROM employees ORDER BY salary DESC LIMIT 2 OFFSET 1")
	if err != nil {
		t.Fatal(err)
	}
	// Skip Frank(120k), take Eve(95k) and Alice(90k).
	if len(res.Rows) != 2 {
		t.Fatalf("rows=%d want 2", len(res.Rows))
	}
	if res.Rows[0][0].AsText() != "Eve" {
		t.Fatalf("row[0]=%v want Eve", res.Rows[0][0])
	}
}

func TestExecSelectGroupBy(t *testing.T) {
	db := makeTestDB(t)
	res, err := Exec(db, "SELECT dept, COUNT(*) AS cnt FROM employees GROUP BY dept")
	if err != nil {
		t.Fatalf("GROUP BY: %v", err)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("groups=%d want 3", len(res.Rows))
	}
	// Build map dept→count.
	counts := map[string]int64{}
	for _, row := range res.Rows {
		counts[row[0].AsText()] = row[1].AsInt()
	}
	if counts["Eng"] != 3 {
		t.Fatalf("Eng count=%d want 3", counts["Eng"])
	}
	if counts["HR"] != 2 {
		t.Fatalf("HR count=%d want 2", counts["HR"])
	}
}

func TestExecSelectAggNoGroupBy(t *testing.T) {
	db := makeTestDB(t)
	res, err := Exec(db, "SELECT COUNT(*) AS n, SUM(salary) AS total, AVG(salary) AS avg FROM employees")
	if err != nil {
		t.Fatalf("global agg: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows=%d want 1", len(res.Rows))
	}
	if res.Rows[0][0].AsInt() != 6 {
		t.Fatalf("COUNT(*)=%v want 6", res.Rows[0][0])
	}
	total, _ := res.Rows[0][1].ToFloat()
	if math.Abs(total-520000) > 0.01 {
		t.Fatalf("SUM(salary)=%v want 520000", total)
	}
}

func TestExecSelectJoin(t *testing.T) {
	db := makeTestDB(t)
	_, err := Exec(db, `CREATE TABLE departments (
		dept_name TEXT PRIMARY KEY,
		budget INT
	)`)
	if err != nil {
		t.Fatal(err)
	}
	for _, ins := range []string{
		"INSERT INTO departments (dept_name, budget) VALUES ('Eng', 500000)",
		"INSERT INTO departments (dept_name, budget) VALUES ('HR', 200000)",
		"INSERT INTO departments (dept_name, budget) VALUES ('Mgmt', 300000)",
	} {
		Exec(db, ins)
	}

	res, err := Exec(db, "SELECT name, budget FROM employees JOIN departments ON dept = dept_name")
	if err != nil {
		t.Fatalf("JOIN: %v", err)
	}
	if len(res.Rows) != 6 {
		t.Fatalf("joined rows=%d want 6", len(res.Rows))
	}
}

func TestExecSelectCompoundWhere(t *testing.T) {
	db := makeTestDB(t)
	res, err := Exec(db, "SELECT name FROM employees WHERE dept = 'Eng' AND salary >= 90000.0")
	if err != nil {
		t.Fatal(err)
	}
	// Alice(90k), Eve(95k)
	if len(res.Rows) != 2 {
		t.Fatalf("rows=%d want 2: %v", len(res.Rows), res.Rows)
	}
}

func TestExecInsertDuplicate(t *testing.T) {
	db := makeTestDB(t)
	_, err := Exec(db, "INSERT INTO employees (id, name, dept, salary, active) VALUES (1, 'Dup', 'X', 0.0, FALSE)")
	if err == nil {
		t.Fatal("expected duplicate PK error")
	}
}

func TestExecUpdate(t *testing.T) {
	db := makeTestDB(t)
	res, err := Exec(db, "UPDATE employees SET salary = 100000.0 WHERE id = 1")
	if err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	if res.RowsAffected != 1 {
		t.Fatalf("affected=%d want 1", res.RowsAffected)
	}
	// Verify via SELECT.
	sel, _ := Exec(db, "SELECT salary FROM employees WHERE id = 1")
	sal, _ := sel.Rows[0][0].ToFloat()
	if math.Abs(sal-100000) > 0.01 {
		t.Fatalf("updated salary=%v want 100000", sal)
	}
}

func TestExecDelete(t *testing.T) {
	db := makeTestDB(t)
	res, err := Exec(db, "DELETE FROM employees WHERE active = FALSE")
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if res.RowsAffected != 1 { // only Dave is inactive
		t.Fatalf("affected=%d want 1", res.RowsAffected)
	}
	sel, _ := Exec(db, "SELECT * FROM employees")
	if len(sel.Rows) != 5 {
		t.Fatalf("rows after delete=%d want 5", len(sel.Rows))
	}
}

func TestExecCreateAndDrop(t *testing.T) {
	db := NewDatabase()
	_, err := Exec(db, "CREATE TABLE t (id INT PRIMARY KEY, val TEXT)")
	if err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	// Duplicate create should fail.
	_, err = Exec(db, "CREATE TABLE t (id INT PRIMARY KEY)")
	if err == nil {
		t.Fatal("expected error on duplicate CREATE TABLE")
	}
	_, err = Exec(db, "DROP TABLE t")
	if err != nil {
		t.Fatalf("DROP: %v", err)
	}
	// Drop again should fail.
	_, err = Exec(db, "DROP TABLE t")
	if err == nil {
		t.Fatal("expected error on DROP of non-existent table")
	}
}

func TestExecTableNotFound(t *testing.T) {
	db := NewDatabase()
	_, err := Exec(db, "SELECT * FROM nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown table")
	}
}

func TestExecIsNull(t *testing.T) {
	db := NewDatabase()
	Exec(db, "CREATE TABLE t (id INT PRIMARY KEY, val TEXT)")
	Exec(db, "INSERT INTO t (id, val) VALUES (1, 'hello')")
	Exec(db, "INSERT INTO t (id, val) VALUES (2, NULL)")

	res, err := Exec(db, "SELECT id FROM t WHERE val IS NULL")
	if err != nil {
		t.Fatalf("IS NULL: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0].AsInt() != 2 {
		t.Fatalf("IS NULL result: %v", res.Rows)
	}
}

func TestExecArithmeticProjection(t *testing.T) {
	db := makeTestDB(t)
	res, err := Exec(db, "SELECT name, salary * 1.1 AS raised FROM employees WHERE id = 2")
	if err != nil {
		t.Fatal(err)
	}
	raised, _ := res.Rows[0][1].ToFloat()
	if math.Abs(raised-88000) > 0.01 {
		t.Fatalf("raised salary=%v want 88000", raised)
	}
}

func TestPrintStmt(t *testing.T) {
	stmt := mustParse(t, "SELECT id, name FROM emp WHERE id > 1 ORDER BY name ASC LIMIT 5")
	s := PrintStmt(stmt)
	if !strings.Contains(s, "SELECT") || !strings.Contains(s, "WHERE") {
		t.Fatalf("PrintStmt output: %q", s)
	}
}

func TestExecSelectMinMax(t *testing.T) {
	db := makeTestDB(t)
	res, err := Exec(db, "SELECT MIN(salary) AS min_s, MAX(salary) AS max_s FROM employees")
	if err != nil {
		t.Fatalf("MIN/MAX: %v", err)
	}
	minS, _ := res.Rows[0][0].ToFloat()
	maxS, _ := res.Rows[0][1].ToFloat()
	if math.Abs(minS-65000) > 0.01 {
		t.Fatalf("MIN=%v want 65000", minS)
	}
	if math.Abs(maxS-120000) > 0.01 {
		t.Fatalf("MAX=%v want 120000", maxS)
	}
}

func TestExecNegativeLiteral(t *testing.T) {
	db := NewDatabase()
	Exec(db, "CREATE TABLE t (id INT PRIMARY KEY, val INT)")
	Exec(db, "INSERT INTO t (id, val) VALUES (1, 10)")
	res, err := Exec(db, "SELECT val + -3 AS result FROM t WHERE id = 1")
	if err != nil {
		t.Fatalf("negative literal: %v", err)
	}
	if res.Rows[0][0].AsInt() != 7 {
		t.Fatalf("val + -3 = %v want 7", res.Rows[0][0])
	}
}

func TestExecUpdateMultipleRows(t *testing.T) {
	db := makeTestDB(t)
	res, err := Exec(db, "UPDATE employees SET active = FALSE WHERE dept = 'Eng'")
	if err != nil {
		t.Fatal(err)
	}
	if res.RowsAffected != 3 {
		t.Fatalf("affected=%d want 3", res.RowsAffected)
	}
}

func TestExecDeleteAll(t *testing.T) {
	db := makeTestDB(t)
	res, err := Exec(db, "DELETE FROM employees WHERE id > 0")
	if err != nil {
		t.Fatal(err)
	}
	if res.RowsAffected != 6 {
		t.Fatalf("affected=%d want 6", res.RowsAffected)
	}
	sel, _ := Exec(db, "SELECT * FROM employees")
	if len(sel.Rows) != 0 {
		t.Fatalf("rows after delete all=%d", len(sel.Rows))
	}
}

// -------------------------------------------------------------------------
// Large data stress test
// -------------------------------------------------------------------------

func TestExecLargeTable(t *testing.T) {
	db := NewDatabase()
	Exec(db, "CREATE TABLE nums (id INT PRIMARY KEY, grp TEXT, val INT)")
	for i := 0; i < 500; i++ {
		grp := "even"
		if i%2 != 0 {
			grp = "odd"
		}
		Exec(db, strings.ReplaceAll(
			strings.ReplaceAll(
				"INSERT INTO nums (id, grp, val) VALUES ({i}, '{g}', {i})",
				"{i}", query.Int(int64(i)).String()),
			"{g}", grp))
	}

	res, err := Exec(db, "SELECT grp, COUNT(*) AS cnt FROM nums GROUP BY grp ORDER BY grp")
	if err != nil {
		t.Fatalf("large group by: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("groups=%d want 2", len(res.Rows))
	}
}

// -------------------------------------------------------------------------
// Bug fix tests
// -------------------------------------------------------------------------

func TestExecDeleteCompositePK(t *testing.T) {
	db := NewDatabase()
	Exec(db, `CREATE TABLE orders (
		customer_id INT PRIMARY KEY,
		order_id    INT PRIMARY KEY,
		amount      FLOAT
	)`)
	Exec(db, "INSERT INTO orders (customer_id, order_id, amount) VALUES (1, 100, 50.0)")
	Exec(db, "INSERT INTO orders (customer_id, order_id, amount) VALUES (1, 101, 75.0)")
	Exec(db, "INSERT INTO orders (customer_id, order_id, amount) VALUES (2, 100, 30.0)")

	res, err := Exec(db, "DELETE FROM orders WHERE customer_id = 1 AND order_id = 100")
	if err != nil {
		t.Fatalf("DELETE composite PK: %v", err)
	}
	if res.RowsAffected != 1 {
		t.Fatalf("affected=%d want 1", res.RowsAffected)
	}
	sel, _ := Exec(db, "SELECT * FROM orders")
	if len(sel.Rows) != 2 {
		t.Fatalf("rows after delete=%d want 2: %v", len(sel.Rows), sel.Rows)
	}
}

func TestExecDistinct(t *testing.T) {
	db := makeTestDB(t)
	res, err := Exec(db, "SELECT DISTINCT dept FROM employees ORDER BY dept")
	if err != nil {
		t.Fatalf("DISTINCT: %v", err)
	}
	// Eng, HR, Mgmt — 3 distinct depts
	if len(res.Rows) != 3 {
		t.Fatalf("distinct depts=%d want 3: %v", len(res.Rows), res.Rows)
	}
	if res.Rows[0][0].AsText() != "Eng" {
		t.Fatalf("first dept=%v want Eng", res.Rows[0][0])
	}
}

func TestExecHavingCount(t *testing.T) {
	db := makeTestDB(t)
	// Departments with more than 1 employee
	res, err := Exec(db, "SELECT dept, COUNT(*) AS cnt FROM employees GROUP BY dept HAVING COUNT(*) > 1")
	if err != nil {
		t.Fatalf("HAVING: %v", err)
	}
	// Eng (3) and HR (2) qualify; Mgmt (1) does not
	if len(res.Rows) != 2 {
		t.Fatalf("groups with cnt>1: %d want 2: %v", len(res.Rows), res.Rows)
	}
	for _, row := range res.Rows {
		if row[1].AsInt() <= 1 {
			t.Fatalf("HAVING failed: dept %v has cnt=%v", row[0], row[1])
		}
	}
}

func TestExecDistinctAllSame(t *testing.T) {
	db := NewDatabase()
	Exec(db, "CREATE TABLE t (id INT PRIMARY KEY, val TEXT)")
	Exec(db, "INSERT INTO t (id, val) VALUES (1, 'x')")
	Exec(db, "INSERT INTO t (id, val) VALUES (2, 'x')")
	Exec(db, "INSERT INTO t (id, val) VALUES (3, 'x')")
	res, err := Exec(db, "SELECT DISTINCT val FROM t")
	if err != nil {
		t.Fatalf("DISTINCT all same: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows=%d want 1", len(res.Rows))
	}
}

// -------------------------------------------------------------------------
// Gap fix tests
// -------------------------------------------------------------------------

func TestHavingAggNotInSelect(t *testing.T) {
	db := makeTestDB(t)
	// HAVING uses SUM(salary) which is NOT in the SELECT list.
	res, err := Exec(db, `SELECT dept FROM employees GROUP BY dept HAVING SUM(salary) > 200000.0`)
	if err != nil {
		t.Fatalf("HAVING agg not in SELECT: %v", err)
	}
	// Eng: 90k+80k+95k = 265k > 200k ✓
	// HR: 70k+65k = 135k — no
	// Mgmt: 120k — no
	if len(res.Rows) != 1 {
		t.Fatalf("rows=%d want 1: %v", len(res.Rows), res.Rows)
	}
	if res.Rows[0][0].AsText() != "Eng" {
		t.Fatalf("dept=%v want Eng", res.Rows[0][0])
	}
}

func TestHavingWithCountNotInSelect(t *testing.T) {
	db := makeTestDB(t)
	// HAVING COUNT(*) > 2 but SELECT only has dept.
	res, err := Exec(db, `SELECT dept FROM employees GROUP BY dept HAVING COUNT(*) > 2`)
	if err != nil {
		t.Fatalf("HAVING COUNT(*) not in SELECT: %v", err)
	}
	// Only Eng has 3 employees.
	if len(res.Rows) != 1 || res.Rows[0][0].AsText() != "Eng" {
		t.Fatalf("rows=%v want [Eng]", res.Rows)
	}
}

func TestPrintExprStringEscaping(t *testing.T) {
	// PrintExpr must escape single quotes for SQL round-trip.
	expr := &LitExpr{Kind: LitStr, StrVal: "it's a test"}
	printed := PrintExpr(expr)
	if printed != "'it''s a test'" {
		t.Fatalf("PrintExpr string escaping: got %q want %q", printed, "'it''s a test'")
	}
	// Must parse back correctly.
	stmt, err := Parse("SELECT * FROM t WHERE name = " + printed)
	if err != nil {
		t.Fatalf("round-trip parse: %v", err)
	}
	sel := stmt.(*SelectStmt)
	bin := sel.Where.(*BinaryExpr)
	lit := bin.Right.(*LitExpr)
	if lit.StrVal != "it's a test" {
		t.Fatalf("round-trip value: got %q want %q", lit.StrVal, "it's a test")
	}
}

func TestPrintExprComplexWhere(t *testing.T) {
	// Complex WHERE with AND/OR nesting must round-trip.
	original := "SELECT * FROM t WHERE (a > 1 AND b < 10) OR c = TRUE"
	stmt, err := Parse(original)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sel := stmt.(*SelectStmt)
	printed := PrintExpr(sel.Where)
	// Re-parse the printed WHERE.
	stmt2, err := Parse("SELECT * FROM t WHERE " + printed)
	if err != nil {
		t.Fatalf("re-parse printed WHERE %q: %v", printed, err)
	}
	// Both should produce the same structure.
	sel2 := stmt2.(*SelectStmt)
	if PrintExpr(sel2.Where) != printed {
		t.Fatalf("not idempotent: first=%q second=%q", printed, PrintExpr(sel2.Where))
	}
}

func TestDeleteWithComplexWhere(t *testing.T) {
	db := makeTestDB(t)
	// DELETE with AND expression — exercises PrintExpr round-trip in db.execDelete pre-scan.
	res, err := Exec(db, "DELETE FROM employees WHERE dept = 'HR' AND salary < 70000.0")
	if err != nil {
		t.Fatalf("DELETE complex WHERE: %v", err)
	}
	// Only Dave (HR, 65k) matches.
	if res.RowsAffected != 1 {
		t.Fatalf("affected=%d want 1", res.RowsAffected)
	}
}

func TestDeleteWithStringContainingQuote(t *testing.T) {
	db := NewDatabase()
	Exec(db, "CREATE TABLE t (id INT PRIMARY KEY, name TEXT)")
	Exec(db, "INSERT INTO t (id, name) VALUES (1, 'it''s Alice')")
	Exec(db, "INSERT INTO t (id, name) VALUES (2, 'Bob')")
	res, err := Exec(db, "DELETE FROM t WHERE name = 'it''s Alice'")
	if err != nil {
		t.Fatalf("DELETE with quoted string: %v", err)
	}
	if res.RowsAffected != 1 {
		t.Fatalf("affected=%d want 1", res.RowsAffected)
	}
	sel, _ := Exec(db, "SELECT * FROM t")
	if len(sel.Rows) != 1 || sel.Rows[0][0].AsInt() != 2 {
		t.Fatalf("wrong row survived: %v", sel.Rows)
	}
}
