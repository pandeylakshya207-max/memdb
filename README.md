# memdb

A from-scratch relational database engine written in Go. No external dependencies.

**github.com/pandeylakshya207-max/memdb**

---

## Architecture

```
┌─────────────────────────────────────────────┐
│              cmd/memdb-server               │  ← runnable TCP server binary
├─────────────────────────────────────────────┤
│                   server                    │  ← TCP server, line-based protocol
├─────────────────────────────────────────────┤
│                     db                      │  ← durable DB: WAL-logged, catalog
├──────────────┬──────────────────────────────┤
│     sql      │          query               │  ← SQL parser/planner + iterator engine
├──────────────┴──────────────────────────────┤
│        page + wal                           │  ← buffer pool + write-ahead log
├──────────────┬──────────────────────────────┤
│    btree     │       hashindex              │  ← ordered B-Tree + concurrent HashMap
└──────────────┴──────────────────────────────┘
```

---

## Packages

### `btree` — Ordered B-Tree index
Generic B-Tree with configurable minimum degree `t`. Used as the primary storage structure for table rows (keyed by primary key).

- Top-down split-on-descent insert; top-down fill-on-descent delete (borrow-left, borrow-right, merge)
- Ordered range scan with early-stop callback
- Full structural invariant checker used after every mutation in tests

### `hashindex` — Concurrent hash table
Generic, dynamically-resizing hash map with stripe-sharded locking.

- 16-stripe `sync.RWMutex` sharding — reads on different key ranges never contend
- Dynamic rehash: doubles at load > 2.0, halves at load < 0.25
- FNV-1a for strings, Murmur3-mix for integers
- `atomic.Int64` for O(1) `Len()` without any lock

### `page` — Page file + buffer pool
Fixed-size (4096 byte) page manager over an OS file, plus an LRU buffer pool.

- Superblock at page 0: magic number, page count, free-list head
- LIFO free-list persisted across close/open
- Buffer pool: FetchPin/Unpin/MarkDirty, LRU eviction with dirty write-back
- All-pinned returns a clear error

### `wal` — Write-ahead log
Append-only WAL with CRC32 record checksums and fsync-on-commit.

- Wire format: `[LSN:8][Type:1][PageID:8][DataLen:4][Data:N][CRC32:4]`
- `AppendWrite` / `AppendCommit` (fsync) / `AppendCheckpoint` (fsync)
- REDO recovery: two-pass — find last checkpoint, replay only committed transactions, discard uncommitted tail writes
- CRC mismatch or short read = clean stop (crash-safe tail truncation)

### `query` — Volcano-model query engine
Composable relational operators using the iterator model (Open → Next* → Close).

| Operator | Description |
|---|---|
| `TableScan` | Full ordered scan of a Table (backed by B-Tree) |
| `RangeScan` | B-Tree range scan [lo, hi] on PK column |
| `Filter` | Predicate pushdown; any `Expr` |
| `Project` | Column selection, rename, computed expressions |
| `Limit` / `Offset` | Pagination |
| `OrderBy` | In-memory sort, multi-key, ASC/DESC per key |
| `Distinct` | Deduplicates rows by all column values |
| `NestedLoopJoin` | INNER and LEFT OUTER join; right side buffered once |
| `HashAggregate` | GROUP BY + COUNT / SUM / MIN / MAX / AVG |

Expression system: `ColRef`, `Literal`, `BinOp` (arithmetic + comparison + logical), `NotExpr`, `IsNullExpr`, `IsNullExpr`. Int/Float cross-type comparison. NULL-safe semantics.

### `sql` — SQL parser + planner
Recursive-descent parser from string to AST, and a planner that wires AST nodes to `query` iterators.

**Supported syntax:**
```sql
SELECT [DISTINCT] col [AS alias], ... FROM table
  [JOIN table ON expr]
  [WHERE expr]
  [GROUP BY col, ...]
  [HAVING expr]
  [ORDER BY col [ASC|DESC], ...]
  [LIMIT n] [OFFSET n]

INSERT INTO table (col, ...) VALUES (val, ...)
UPDATE table SET col = expr [, ...] [WHERE expr]
DELETE FROM table [WHERE expr]
CREATE TABLE table (col type PRIMARY KEY [, ...])
DROP TABLE table
```

**Types:** `INT`, `FLOAT`, `TEXT`, `BOOL`

### `db` — Durable database
Wires the SQL layer to a persistent, WAL-backed store. Every write is fsynced before returning.

- Row-level WAL: each INSERT/UPDATE/DELETE appends a serialised row record
- Catalog (`catalog.json`): table schemas + PK definitions, updated atomically on DDL
- Crash recovery: on `Open`, catalog is read → each table's WAL is replayed → in-memory B-Tree is reconstructed
- `db.Exec(sql)` is the single entry point for all operations

### `server` — TCP server
Line-based SQL server over TCP.

**Protocol:**
```
Client →  <SQL statement>\n
Server →  DATA\t<col1>\t<col2>\t...\n   (SELECT: schema header)
          ROW\t<v1>\t<v2>\t...\n         (SELECT: one per row)
          OK\t<rows_affected>\n           (always last)
       or ERR\t<message>\n               (on error)
```

### `cmd/memdb-server` — Server binary
```sh
go build ./cmd/memdb-server
./memdb-server -dir ./mydb -addr 0.0.0.0:5432
```

---

## Quick start

```sh
git clone https://github.com/pandeylakshya207-max/memdb
cd memdb
go test ./...          # run all tests
go build ./...         # build everything

# Start the server
go run ./cmd/memdb-server -dir /tmp/mydb -addr localhost:5432

# In another terminal — connect with netcat
nc localhost 5432
CREATE TABLE users (id INT PRIMARY KEY, name TEXT, age INT)
INSERT INTO users (id, name, age) VALUES (1, 'Alice', 30)
INSERT INTO users (id, name, age) VALUES (2, 'Bob', 25)
SELECT name, age FROM users WHERE age > 20 ORDER BY age DESC
SELECT COUNT(*) AS total FROM users
DROP TABLE users
quit
```

Or use the Go API directly:
```go
import (
    "github.com/pandeylakshya207-max/memdb/db"
    "github.com/pandeylakshya207-max/memdb/sql"
)

// In-memory only (no persistence)
database := sql.NewDatabase()
sql.Exec(database, "CREATE TABLE t (id INT PRIMARY KEY, val TEXT)")
sql.Exec(database, "INSERT INTO t (id, val) VALUES (1, 'hello')")
res, _ := sql.Exec(database, "SELECT * FROM t")

// Durable (WAL-backed)
durable, _ := db.Open("/path/to/data")
defer durable.Close()
durable.Exec("CREATE TABLE t (id INT PRIMARY KEY, val TEXT)")
durable.Exec("INSERT INTO t (id, val) VALUES (1, 'hello')")
// Data survives process restart.
```

---

## Test suite

| Package | Tests | Coverage | Notes |
|---|---|---|---|
| `btree` | 34 | 93.0% | Exhaustive permutation tests, random stress |
| `hashindex` | 23 | 95.7% | Concurrent stress under `-race` |
| `page` | 20 | 86.8% | Free-list, LRU eviction, dirty write-back |
| `wal` | 18 | 88.8% | Crash simulation, CRC corruption, checkpoint recovery |
| `query` | 61 | 83.9% | All operators, pipeline composition, large table |
| `sql` | 54 | 77.2% | Parser fuzzing, end-to-end Exec tests |
| `db` | 13 | 81.3% | Crash recovery, multi-session, NULL persistence |
| `server` | 10 | 90.2% | Real TCP connections, concurrent clients |

All packages pass `go test ./... -race`. No external dependencies.

```sh
go test ./... -race    # full suite under race detector
go vet ./...           # static analysis
```

---

## Real bugs found during development

Every bug was caught by the test suite, not discovered in production.

| # | Package | Bug | Caught by |
|---|---|---|---|
| 1 | `btree` | `deleteFromInternal` case 2c read `n.keys[i]` after `mergeChildren` already removed it — wrong key deleted | `TestDegree2Exhaustive` (720 permutations × invariant-check-per-op) |
| 2 | `btree` | Last-child merge left `i` pointing past the merged node — off-by-one in child index after fill | `TestRandomInsertDeleteStress` (4 degrees × 500 ops) |
| 3 | `query` | `RangeScan` returned wrong rows for integer PK ranges — `pkValKey` used `Value.String()` making `"100" < "2"` lexicographically | `TestRangeScanEmpty` |
| 4 | `query` | `Filter.Open` and `Project.Open` captured child schema before opening child — `HashAggregate.Schema()` returns empty until `Open()`, so GROUP BY queries produced all-NULL rows silently | `TestExecSelectGroupBy` |
| 5 | `sql` | `postAggSchema` built from `base.Schema()` before `Open()` — planner couldn't reference GROUP BY columns for ORDER BY / PROJECT | `TestExecSelectGroupBy`, `TestExecSelectAggNoGroupBy` |
| 6 | `sql` | ORDER BY placed after PROJECT in the plan — sort column dropped by projection | `TestExecSelectOrderBy` |
| 7 | `sql` | `eatIdent()` rejected keyword tokens used as column aliases (`AS avg`, `AS count`) | `TestExecSelectAggNoGroupBy` |
| 8 | `sql` | `DELETE` with composite PK used only `row[0]` — first column always deleted regardless of WHERE | `TestExecDeleteCompositePK` |
| 9 | `db` | All DML used `PrintStmt(s)` to re-serialise the AST — `PrintStmt` produces human-readable text not valid SQL, causing parse errors on every write | `TestCreateTablePersists` |
| 10 | `db` | `DISTINCT` parsed but ignored — no operator wired into the plan | `TestExecDistinct` |

---

## Design decisions

**Why a row-level WAL in `db` instead of using the page-level WAL from `wal`?**
The page-level WAL is correct for a buffer-pool-based storage engine where the B-Tree lives on disk pages. Since memdb's B-Tree is in-memory (Weeks 1–4), the right recovery log is at the row level: replay INSERTs and DELETEs to reconstruct the B-Tree. This is simpler and produces a smaller, more readable WAL. A future persistent B-Tree would use the page-level WAL directly.

**Why nested-loop join instead of hash join?**
Nested-loop join is O(N×M) but correct and simple to implement. Hash join would require hashing one side into memory, which adds complexity without changing the correctness story. For the query engine's scope (in-memory tables, no query optimiser) NLJ is appropriate.

**Why a custom line protocol instead of PostgreSQL wire?**
The PostgreSQL wire protocol is 50+ pages of spec. The goal here is to demonstrate end-to-end connectivity, not protocol compatibility. The line protocol is readable with plain `nc`, easy to test, and implements the same fundamental request/response pattern.
