package db

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func tmpDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "testdb")
}

func mustExec(t *testing.T, d *DB, sql string) {
	t.Helper()
	if _, err := d.Exec(sql); err != nil {
		t.Fatalf("Exec(%q): %v", sql, err)
	}
}

func rowCount(t *testing.T, d *DB, table string) int {
	t.Helper()
	res, err := d.Exec("SELECT * FROM " + table)
	if err != nil {
		t.Fatalf("SELECT * FROM %s: %v", table, err)
	}
	return len(res.Rows)
}

func TestOpenFreshDB(t *testing.T) {
	dir := tmpDir(t)
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "catalog.json")); os.IsNotExist(err) {
		t.Fatal("catalog.json not created")
	}
}

func TestCreateTablePersists(t *testing.T) {
	dir := tmpDir(t)

	db, _ := Open(dir)
	mustExec(t, db, "CREATE TABLE users (id INT PRIMARY KEY, name TEXT)")
	mustExec(t, db, "INSERT INTO users (id, name) VALUES (1, 'Alice')")
	mustExec(t, db, "INSERT INTO users (id, name) VALUES (2, 'Bob')")
	db.Close()

	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	if n := rowCount(t, db2, "users"); n != 2 {
		t.Fatalf("after reopen: %d rows want 2", n)
	}
}

func TestRowValuesPersist(t *testing.T) {
	dir := tmpDir(t)

	db, _ := Open(dir)
	mustExec(t, db, "CREATE TABLE t (id INT PRIMARY KEY, name TEXT, score FLOAT, active BOOL)")
	mustExec(t, db, "INSERT INTO t (id, name, score, active) VALUES (1, 'Alice', 98.5, TRUE)")
	db.Close()

	db2, _ := Open(dir)
	defer db2.Close()
	res, err := db2.Exec("SELECT * FROM t WHERE id = 1")
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows=%d want 1", len(res.Rows))
	}
	row := res.Rows[0]
	if row[0].AsInt() != 1 {
		t.Fatalf("id=%v want 1", row[0])
	}
	if row[1].AsText() != "Alice" {
		t.Fatalf("name=%v want Alice", row[1])
	}
	if math.Abs(row[2].AsFloat()-98.5) > 0.001 {
		t.Fatalf("score=%v want 98.5", row[2])
	}
	if !row[3].AsBool() {
		t.Fatalf("active=%v want true", row[3])
	}
}

func TestDeletePersists(t *testing.T) {
	dir := tmpDir(t)

	db, _ := Open(dir)
	mustExec(t, db, "CREATE TABLE t (id INT PRIMARY KEY, val TEXT)")
	mustExec(t, db, "INSERT INTO t (id, val) VALUES (1, 'a')")
	mustExec(t, db, "INSERT INTO t (id, val) VALUES (2, 'b')")
	mustExec(t, db, "INSERT INTO t (id, val) VALUES (3, 'c')")
	mustExec(t, db, "DELETE FROM t WHERE id = 2")
	db.Close()

	db2, _ := Open(dir)
	defer db2.Close()
	if n := rowCount(t, db2, "t"); n != 2 {
		t.Fatalf("after delete+reopen: %d rows want 2", n)
	}
	res, _ := db2.Exec("SELECT * FROM t WHERE id = 2")
	if len(res.Rows) != 0 {
		t.Fatalf("deleted row survived reopen: %v", res.Rows)
	}
}

func TestUpdatePersists(t *testing.T) {
	dir := tmpDir(t)

	db, _ := Open(dir)
	mustExec(t, db, "CREATE TABLE t (id INT PRIMARY KEY, score FLOAT)")
	mustExec(t, db, "INSERT INTO t (id, score) VALUES (1, 50.0)")
	mustExec(t, db, "UPDATE t SET score = 99.0 WHERE id = 1")
	db.Close()

	db2, _ := Open(dir)
	defer db2.Close()
	res, _ := db2.Exec("SELECT score FROM t WHERE id = 1")
	if len(res.Rows) != 1 {
		t.Fatalf("rows=%d want 1", len(res.Rows))
	}
	if math.Abs(res.Rows[0][0].AsFloat()-99.0) > 0.001 {
		t.Fatalf("score=%v want 99.0", res.Rows[0][0])
	}
}

func TestDropTablePersists(t *testing.T) {
	dir := tmpDir(t)

	db, _ := Open(dir)
	mustExec(t, db, "CREATE TABLE t (id INT PRIMARY KEY, val TEXT)")
	mustExec(t, db, "INSERT INTO t (id, val) VALUES (1, 'x')")
	mustExec(t, db, "DROP TABLE t")
	db.Close()

	db2, _ := Open(dir)
	defer db2.Close()
	if _, err := db2.Exec("SELECT * FROM t"); err == nil {
		t.Fatal("dropped table accessible after reopen")
	}
}

func TestMultipleTablesPersist(t *testing.T) {
	dir := tmpDir(t)

	db, _ := Open(dir)
	mustExec(t, db, "CREATE TABLE users (id INT PRIMARY KEY, name TEXT)")
	mustExec(t, db, "CREATE TABLE products (id INT PRIMARY KEY, title TEXT, price FLOAT)")
	for i := 1; i <= 5; i++ {
		mustExec(t, db, fmt.Sprintf("INSERT INTO users (id, name) VALUES (%d, 'user%d')", i, i))
		mustExec(t, db, fmt.Sprintf("INSERT INTO products (id, title, price) VALUES (%d, 'prod%d', %g)", i, i, float64(i)*10.0))
	}
	db.Close()

	db2, _ := Open(dir)
	defer db2.Close()
	if n := rowCount(t, db2, "users"); n != 5 {
		t.Fatalf("users: %d rows want 5", n)
	}
	if n := rowCount(t, db2, "products"); n != 5 {
		t.Fatalf("products: %d rows want 5", n)
	}
}

func TestCrashRecovery(t *testing.T) {
	dir := tmpDir(t)

	db, _ := Open(dir)
	mustExec(t, db, "CREATE TABLE t (id INT PRIMARY KEY, val TEXT)")
	mustExec(t, db, "INSERT INTO t (id, val) VALUES (1, 'committed')")
	mustExec(t, db, "INSERT INTO t (id, val) VALUES (2, 'also committed')")
	// Simulate crash: sync WAL files but don't call Close().
	for _, f := range db.walFiles {
		f.Sync()
	}
	// Leave db open (simulated crash).

	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("recovery Open: %v", err)
	}
	defer db2.Close()
	if n := rowCount(t, db2, "t"); n != 2 {
		t.Fatalf("crash recovery: %d rows want 2", n)
	}
}

func TestSelectQueryAfterReopen(t *testing.T) {
	dir := tmpDir(t)

	db, _ := Open(dir)
	mustExec(t, db, "CREATE TABLE emp (id INT PRIMARY KEY, dept TEXT, salary FLOAT)")
	mustExec(t, db, "INSERT INTO emp (id, dept, salary) VALUES (1, 'Eng', 90000.0)")
	mustExec(t, db, "INSERT INTO emp (id, dept, salary) VALUES (2, 'HR', 70000.0)")
	mustExec(t, db, "INSERT INTO emp (id, dept, salary) VALUES (3, 'Eng', 85000.0)")
	db.Close()

	db2, _ := Open(dir)
	defer db2.Close()
	res, err := db2.Exec("SELECT dept, COUNT(*) AS cnt FROM emp GROUP BY dept ORDER BY dept")
	if err != nil {
		t.Fatalf("GROUP BY after reopen: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("groups=%d want 2", len(res.Rows))
	}
	byDept := map[string]int64{}
	for _, r := range res.Rows {
		byDept[r[0].AsText()] = r[1].AsInt()
	}
	if byDept["Eng"] != 2 || byDept["HR"] != 1 {
		t.Fatalf("group counts: %v", byDept)
	}
}

func TestNullValuePersists(t *testing.T) {
	dir := tmpDir(t)

	db, _ := Open(dir)
	mustExec(t, db, "CREATE TABLE t (id INT PRIMARY KEY, val TEXT)")
	mustExec(t, db, "INSERT INTO t (id, val) VALUES (1, NULL)")
	db.Close()

	db2, _ := Open(dir)
	defer db2.Close()
	res, _ := db2.Exec("SELECT val FROM t WHERE id = 1")
	if len(res.Rows) != 1 {
		t.Fatalf("rows=%d want 1", len(res.Rows))
	}
	if !res.Rows[0][0].IsNull() {
		t.Fatalf("NULL not preserved: %v", res.Rows[0][0])
	}
}

func TestLargeDatasetPersists(t *testing.T) {
	dir := tmpDir(t)

	db, _ := Open(dir)
	mustExec(t, db, "CREATE TABLE nums (id INT PRIMARY KEY, val INT)")
	for i := 0; i < 200; i++ {
		mustExec(t, db, fmt.Sprintf("INSERT INTO nums (id, val) VALUES (%d, %d)", i, i*i))
	}
	db.Close()

	db2, _ := Open(dir)
	defer db2.Close()
	if n := rowCount(t, db2, "nums"); n != 200 {
		t.Fatalf("large: %d rows want 200", n)
	}
	res, _ := db2.Exec("SELECT val FROM nums WHERE id = 10")
	if len(res.Rows) != 1 || res.Rows[0][0].AsInt() != 100 {
		t.Fatalf("id=10 val=%v want 100", res.Rows[0][0])
	}
}

func TestReopenMultipleTimes(t *testing.T) {
	dir := tmpDir(t)

	db, _ := Open(dir)
	mustExec(t, db, "CREATE TABLE t (id INT PRIMARY KEY, n INT)")

	for round := 0; round < 3; round++ {
		mustExec(t, db, fmt.Sprintf("INSERT INTO t (id, n) VALUES (%d, %d)", round+1, round))
		db.Close()
		var err error
		db, err = Open(dir)
		if err != nil {
			t.Fatalf("round %d reopen: %v", round, err)
		}
	}
	defer db.Close()
	if n := rowCount(t, db, "t"); n != 3 {
		t.Fatalf("after 3 rounds: %d rows want 3", n)
	}
}

func TestDeleteAllAndReopen(t *testing.T) {
	dir := tmpDir(t)

	db, _ := Open(dir)
	mustExec(t, db, "CREATE TABLE t (id INT PRIMARY KEY, val TEXT)")
	for i := 1; i <= 5; i++ {
		mustExec(t, db, fmt.Sprintf("INSERT INTO t (id, val) VALUES (%d, 'v%d')", i, i))
	}
	mustExec(t, db, "DELETE FROM t WHERE id > 0")
	db.Close()

	db2, _ := Open(dir)
	defer db2.Close()
	if n := rowCount(t, db2, "t"); n != 0 {
		t.Fatalf("after delete-all+reopen: %d rows want 0", n)
	}
}

// -------------------------------------------------------------------------
// Gap fix tests
// -------------------------------------------------------------------------

func TestUpdateWALOnlyChangedRows(t *testing.T) {
	dir := tmpDir(t)
	db, _ := Open(dir)
	mustExec(t, db, "CREATE TABLE t (id INT PRIMARY KEY, val INT)")
	for i := 1; i <= 10; i++ {
		mustExec(t, db, fmt.Sprintf("INSERT INTO t (id, val) VALUES (%d, %d)", i, i*10))
	}

	// Record WAL size before update.
	sizeBefore := db.WALSize("t")

	// Update only 1 row out of 10.
	mustExec(t, db, "UPDATE t SET val = 999 WHERE id = 5")
	sizeAfter := db.WALSize("t")

	// The WAL growth should be ~1 record, not 10.
	// Each row record ≈ 5 + 3*13 bytes ≈ 44 bytes. One row ≈ 44, ten rows ≈ 440.
	growth := sizeAfter - sizeBefore
	if growth > 200 {
		t.Fatalf("UPDATE WAL wrote too much: grew by %d bytes (want ~44 for 1 row)", growth)
	}
	db.Close()

	// Verify the update persisted correctly.
	db2, _ := Open(dir)
	defer db2.Close()
	res, _ := db2.Exec("SELECT val FROM t WHERE id = 5")
	if len(res.Rows) != 1 || res.Rows[0][0].AsInt() != 999 {
		t.Fatalf("update not persisted: %v", res.Rows)
	}
	// Other rows unchanged.
	res2, _ := db2.Exec("SELECT val FROM t WHERE id = 1")
	if len(res2.Rows) != 1 || res2.Rows[0][0].AsInt() != 10 {
		t.Fatalf("untouched row changed: %v", res2.Rows)
	}
}

func TestWALCompaction(t *testing.T) {
	dir := tmpDir(t)
	db, _ := Open(dir)
	mustExec(t, db, "CREATE TABLE t (id INT PRIMARY KEY, val INT)")

	// Insert 100 rows, update each once → WAL has 200 records.
	for i := 1; i <= 100; i++ {
		mustExec(t, db, fmt.Sprintf("INSERT INTO t (id, val) VALUES (%d, %d)", i, i))
	}
	for i := 1; i <= 100; i++ {
		mustExec(t, db, fmt.Sprintf("UPDATE t SET val = %d WHERE id = %d", i*100, i))
	}

	sizeBefore := db.WALSize("t")
	if sizeBefore == 0 {
		t.Fatal("WAL size should be > 0 before compaction")
	}

	// Compact — WAL should shrink to exactly 100 records (one per live row).
	if err := db.Compact("t"); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	sizeAfter := db.WALSize("t")
	if sizeAfter >= sizeBefore {
		t.Fatalf("Compact didn't reduce WAL: before=%d after=%d", sizeBefore, sizeAfter)
	}

	db.Close()

	// Data must still be correct after compaction + reopen.
	db2, _ := Open(dir)
	defer db2.Close()
	if n := rowCount(t, db2, "t"); n != 100 {
		t.Fatalf("after compact+reopen: %d rows want 100", n)
	}
	res, _ := db2.Exec("SELECT val FROM t WHERE id = 50")
	if len(res.Rows) != 1 || res.Rows[0][0].AsInt() != 5000 {
		t.Fatalf("val for id=50: %v want 5000", res.Rows)
	}
}

func TestCompactAll(t *testing.T) {
	dir := tmpDir(t)
	db, _ := Open(dir)
	mustExec(t, db, "CREATE TABLE a (id INT PRIMARY KEY, v INT)")
	mustExec(t, db, "CREATE TABLE b (id INT PRIMARY KEY, v TEXT)")
	for i := 1; i <= 20; i++ {
		mustExec(t, db, fmt.Sprintf("INSERT INTO a (id, v) VALUES (%d, %d)", i, i))
		mustExec(t, db, fmt.Sprintf("INSERT INTO b (id, v) VALUES (%d, 'x%d')", i, i))
	}
	// Update all — bloats WAL.
	for i := 1; i <= 20; i++ {
		mustExec(t, db, fmt.Sprintf("UPDATE a SET v = %d WHERE id = %d", i*10, i))
	}
	if err := db.CompactAll(); err != nil {
		t.Fatalf("CompactAll: %v", err)
	}
	db.Close()

	db2, _ := Open(dir)
	defer db2.Close()
	if rowCount(t, db2, "a") != 20 || rowCount(t, db2, "b") != 20 {
		t.Fatal("wrong row count after CompactAll+reopen")
	}
}
