package server

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pandeylakshya207-max/memdb/db"
)

func startTestServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(dir)
	if err != nil {
		t.Fatalf("Open db: %v", err)
	}
	srv := New(database, "127.0.0.1:0")
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go srv.Serve()
	t.Cleanup(func() {
		srv.Close()
		database.Close()
	})
	return srv, database
}

func connect(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// sendSQL sends one SQL statement and collects all response lines until OK or ERR.
func sendSQL(t *testing.T, conn net.Conn, sql string) []string {
	t.Helper()
	fmt.Fprintf(conn, "%s\n", sql)
	var lines []string
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()
		lines = append(lines, line)
		if strings.HasPrefix(line, "OK") || strings.HasPrefix(line, "ERR") {
			break
		}
	}
	return lines
}

func assertOK(t *testing.T, lines []string) {
	t.Helper()
	if len(lines) == 0 {
		t.Fatal("no response lines")
	}
	if !strings.HasPrefix(lines[len(lines)-1], "OK") {
		t.Fatalf("expected OK, got: %v", lines)
	}
}

func assertERR(t *testing.T, lines []string) {
	t.Helper()
	if len(lines) == 0 {
		t.Fatal("no response lines")
	}
	if !strings.HasPrefix(lines[0], "ERR") {
		t.Fatalf("expected ERR, got: %v", lines)
	}
}

func rowLines(lines []string) []string {
	var rows []string
	for _, l := range lines {
		if strings.HasPrefix(l, "ROW") {
			rows = append(rows, l)
		}
	}
	return rows
}

func TestServerCreateAndInsert(t *testing.T) {
	srv, _ := startTestServer(t)
	conn := connect(t, srv.Addr())

	assertOK(t, sendSQL(t, conn, "CREATE TABLE t (id INT PRIMARY KEY, val TEXT)"))
	assertOK(t, sendSQL(t, conn, "INSERT INTO t (id, val) VALUES (1, 'hello')"))

	lines := sendSQL(t, conn, "SELECT * FROM t")
	assertOK(t, lines)
	if len(rowLines(lines)) != 1 {
		t.Fatalf("rows=%d want 1", len(rowLines(lines)))
	}
}

func TestServerSelectReturnsData(t *testing.T) {
	srv, _ := startTestServer(t)
	conn := connect(t, srv.Addr())

	sendSQL(t, conn, "CREATE TABLE emp (id INT PRIMARY KEY, name TEXT, salary FLOAT)")
	sendSQL(t, conn, "INSERT INTO emp (id, name, salary) VALUES (1, 'Alice', 90000.0)")
	sendSQL(t, conn, "INSERT INTO emp (id, name, salary) VALUES (2, 'Bob', 80000.0)")

	lines := sendSQL(t, conn, "SELECT * FROM emp ORDER BY id")
	assertOK(t, lines)
	rows := rowLines(lines)
	if len(rows) != 2 {
		t.Fatalf("rows=%d want 2", len(rows))
	}
	if !strings.Contains(rows[0], "Alice") {
		t.Fatalf("first row should contain Alice: %q", rows[0])
	}
}

func TestServerErrorResponse(t *testing.T) {
	srv, _ := startTestServer(t)
	conn := connect(t, srv.Addr())
	assertERR(t, sendSQL(t, conn, "SELECT * FROM nonexistent"))
}

func TestServerMultipleClients(t *testing.T) {
	srv, _ := startTestServer(t)

	conn1 := connect(t, srv.Addr())
	sendSQL(t, conn1, "CREATE TABLE t (id INT PRIMARY KEY, v INT)")
	sendSQL(t, conn1, "INSERT INTO t (id, v) VALUES (1, 100)")

	conn2 := connect(t, srv.Addr())
	lines := sendSQL(t, conn2, "SELECT * FROM t")
	assertOK(t, lines)
	if len(rowLines(lines)) != 1 {
		t.Fatal("second client should see row from first")
	}
}

func TestServerDelete(t *testing.T) {
	srv, _ := startTestServer(t)
	conn := connect(t, srv.Addr())

	sendSQL(t, conn, "CREATE TABLE t (id INT PRIMARY KEY, v TEXT)")
	sendSQL(t, conn, "INSERT INTO t (id, v) VALUES (1, 'a')")
	sendSQL(t, conn, "INSERT INTO t (id, v) VALUES (2, 'b')")
	sendSQL(t, conn, "DELETE FROM t WHERE id = 1")

	lines := sendSQL(t, conn, "SELECT * FROM t")
	if len(rowLines(lines)) != 1 {
		t.Fatalf("rows after delete=%d want 1", len(rowLines(lines)))
	}
}

func TestServerSchemaHeader(t *testing.T) {
	srv, _ := startTestServer(t)
	conn := connect(t, srv.Addr())

	sendSQL(t, conn, "CREATE TABLE t (id INT PRIMARY KEY, name TEXT)")
	sendSQL(t, conn, "INSERT INTO t (id, name) VALUES (1, 'x')")

	lines := sendSQL(t, conn, "SELECT * FROM t")
	hasData := false
	for _, l := range lines {
		if strings.HasPrefix(l, "DATA") {
			hasData = true
			if !strings.Contains(l, "id") || !strings.Contains(l, "name") {
				t.Fatalf("DATA missing columns: %q", l)
			}
		}
	}
	if !hasData {
		t.Fatal("missing DATA header")
	}
}

func TestServerEmptySelect(t *testing.T) {
	srv, _ := startTestServer(t)
	conn := connect(t, srv.Addr())

	sendSQL(t, conn, "CREATE TABLE t (id INT PRIMARY KEY, v TEXT)")
	lines := sendSQL(t, conn, "SELECT * FROM t")
	assertOK(t, lines)
	if len(rowLines(lines)) != 0 {
		t.Fatal("empty table should return 0 rows")
	}
}

func TestServerUpdate(t *testing.T) {
	srv, _ := startTestServer(t)
	conn := connect(t, srv.Addr())

	sendSQL(t, conn, "CREATE TABLE t (id INT PRIMARY KEY, score INT)")
	sendSQL(t, conn, "INSERT INTO t (id, score) VALUES (1, 50)")
	sendSQL(t, conn, "UPDATE t SET score = 99 WHERE id = 1")

	lines := sendSQL(t, conn, "SELECT score FROM t WHERE id = 1")
	rows := rowLines(lines)
	if len(rows) != 1 || !strings.Contains(rows[0], "99") {
		t.Fatalf("updated score not reflected: %v", rows)
	}
}

func TestServerConcurrentConnections(t *testing.T) {
	srv, _ := startTestServer(t)
	conn := connect(t, srv.Addr())
	sendSQL(t, conn, "CREATE TABLE t (id INT PRIMARY KEY, v INT)")
	for i := 1; i <= 10; i++ {
		sendSQL(t, conn, fmt.Sprintf("INSERT INTO t (id, v) VALUES (%d, %d)", i, i*10))
	}

	// Open 5 concurrent reader connections.
	done := make(chan bool, 5)
	for i := 0; i < 5; i++ {
		go func() {
			c := connect(t, srv.Addr())
			lines := sendSQL(t, c, "SELECT * FROM t")
			if len(rowLines(lines)) != 10 {
				t.Errorf("concurrent reader: got %d rows want 10", len(rowLines(lines)))
			}
			done <- true
		}()
	}
	for i := 0; i < 5; i++ {
		<-done
	}
}

func TestServerGroupBy(t *testing.T) {
	srv, _ := startTestServer(t)
	conn := connect(t, srv.Addr())

	sendSQL(t, conn, "CREATE TABLE emp (id INT PRIMARY KEY, dept TEXT, salary FLOAT)")
	sendSQL(t, conn, "INSERT INTO emp (id, dept, salary) VALUES (1, 'Eng', 90000.0)")
	sendSQL(t, conn, "INSERT INTO emp (id, dept, salary) VALUES (2, 'HR', 70000.0)")
	sendSQL(t, conn, "INSERT INTO emp (id, dept, salary) VALUES (3, 'Eng', 85000.0)")

	lines := sendSQL(t, conn, "SELECT dept, COUNT(*) AS cnt FROM emp GROUP BY dept ORDER BY dept")
	assertOK(t, lines)
	rows := rowLines(lines)
	if len(rows) != 2 {
		t.Fatalf("groups=%d want 2: %v", len(rows), rows)
	}
}
