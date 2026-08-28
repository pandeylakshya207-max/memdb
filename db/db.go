// Package db provides a durable, file-backed database that wires together
// the WAL logging and SQL layers.
//
// Each database lives in a directory:
//   - catalog.json  — table schemas and primary key definitions
//   - <table>.wal   — append-only row operation log per table
//
// On Open, the catalog is read and each table's WAL is replayed to
// reconstruct in-memory B-Tree state. Every write is WAL-logged and
// fsynced before returning, guaranteeing crash durability.
package db

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pandeylakshya207-max/memdb/query"
	dbsql "github.com/pandeylakshya207-max/memdb/sql"
)

// -------------------------------------------------------------------------
// Catalog — persisted table metadata
// -------------------------------------------------------------------------

type catalogEntry struct {
	Name    string   `json:"name"`
	Columns []colDef `json:"columns"`
	PKCols  []string `json:"pk_cols"`
}

type colDef struct {
	Name string `json:"name"`
	Type int    `json:"type"`
}

// -------------------------------------------------------------------------
// Row WAL record types
// -------------------------------------------------------------------------

const (
	rowOpInsert byte = 1
	rowOpDelete byte = 2
)

// -------------------------------------------------------------------------
// DB
// -------------------------------------------------------------------------

// DB is a file-backed, durable database. All writes survive restarts.
type DB struct {
	mu       sync.Mutex
	dir      string
	sqlDB    *dbsql.Database
	catalog  map[string]catalogEntry // lowercase name → entry
	walFiles map[string]*os.File     // lowercase name → open WAL file
}

// Open opens (or creates) a database rooted at dir.
func Open(dir string) (*DB, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("db.Open mkdir: %w", err)
	}
	d := &DB{
		dir:      dir,
		sqlDB:    dbsql.NewDatabase(),
		catalog:  make(map[string]catalogEntry),
		walFiles: make(map[string]*os.File),
	}
	if err := d.loadCatalog(); err != nil {
		return nil, fmt.Errorf("db.Open load catalog: %w", err)
	}
	// Write catalog on first open so the file always exists.
	if _, err := os.Stat(d.catalogPath()); os.IsNotExist(err) {
		if err := d.saveCatalog(); err != nil {
			return nil, fmt.Errorf("db.Open init catalog: %w", err)
		}
	}
	return d, nil
}

// Close flushes all WAL files and closes the database.
func (d *DB) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var firstErr error
	for _, f := range d.walFiles {
		if err := f.Sync(); err != nil && firstErr == nil {
			firstErr = err
		}
		f.Close()
	}
	d.walFiles = nil
	return firstErr
}

// Exec parses and executes a SQL statement.
// DDL updates the on-disk catalog; DML is WAL-logged before returning.
func (d *DB) Exec(rawSQL string) (*dbsql.ExecResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	stmt, err := dbsql.Parse(rawSQL)
	if err != nil {
		return nil, err
	}
	switch s := stmt.(type) {
	case *dbsql.CreateTableStmt:
		return d.execCreate(rawSQL, s)
	case *dbsql.DropTableStmt:
		return d.execDrop(rawSQL, s)
	case *dbsql.InsertStmt:
		return d.execInsert(rawSQL, s)
	case *dbsql.UpdateStmt:
		return d.execUpdate(rawSQL, s)
	case *dbsql.DeleteStmt:
		return d.execDelete(rawSQL, s)
	default:
		// SELECT and other read-only statements: pass straight through.
		return dbsql.Exec(d.sqlDB, rawSQL)
	}
}

// -------------------------------------------------------------------------
// DDL
// -------------------------------------------------------------------------

func (d *DB) execCreate(rawSQL string, s *dbsql.CreateTableStmt) (*dbsql.ExecResult, error) {
	res, err := dbsql.Exec(d.sqlDB, rawSQL)
	if err != nil {
		return nil, err
	}
	// Build catalog entry.
	cols := make([]colDef, len(s.Columns))
	for i, c := range s.Columns {
		cols[i] = colDef{Name: c.Name, Type: int(sqlColTypeToValueType(c.Type))}
	}
	name := strings.ToLower(s.Table)
	entry := catalogEntry{Name: s.Table, Columns: cols, PKCols: s.PrimaryKey}
	d.catalog[name] = entry
	if err := d.saveCatalog(); err != nil {
		return nil, fmt.Errorf("CREATE TABLE save catalog: %w", err)
	}
	if err := d.openWAL(name); err != nil {
		return nil, err
	}
	return res, nil
}

func (d *DB) execDrop(rawSQL string, s *dbsql.DropTableStmt) (*dbsql.ExecResult, error) {
	name := strings.ToLower(s.Table)
	if f, ok := d.walFiles[name]; ok {
		f.Close()
		delete(d.walFiles, name)
		os.Remove(d.walPath(name))
	}
	delete(d.catalog, name)
	res, err := dbsql.Exec(d.sqlDB, rawSQL)
	if err != nil {
		return nil, err
	}
	return res, d.saveCatalog()
}

// -------------------------------------------------------------------------
// DML — log-then-apply
// -------------------------------------------------------------------------

func (d *DB) execInsert(rawSQL string, s *dbsql.InsertStmt) (*dbsql.ExecResult, error) {
	tbl, ok := d.sqlDB.Table(s.Table)
	if !ok {
		return nil, fmt.Errorf("table %q does not exist", s.Table)
	}
	schema := tbl.Schema()
	row := make(query.Row, schema.Width())
	for i := range row {
		row[i] = query.Null()
	}
	for i, colName := range s.Columns {
		idx := schema.Index(colName)
		if idx >= 0 && i < len(s.Values) {
			row[idx] = litToValue(s.Values[i])
		}
	}
	// WAL first (fsync), then apply in-memory.
	if err := d.walAppend(strings.ToLower(s.Table), rowOpInsert, row); err != nil {
		return nil, fmt.Errorf("INSERT WAL: %w", err)
	}
	return dbsql.Exec(d.sqlDB, rawSQL)
}

func (d *DB) execUpdate(rawSQL string, s *dbsql.UpdateStmt) (*dbsql.ExecResult, error) {
	tbl, ok := d.sqlDB.Table(s.Table)
	if !ok {
		return nil, fmt.Errorf("table %q does not exist", s.Table)
	}

	// Snapshot PKs of rows that will be affected BEFORE the update,
	// so we log only the changed rows (not the whole table).
	var matchedPKs [][]query.Value
	if s.Where != nil {
		whereSQL := "SELECT * FROM " + s.Table + " WHERE " + dbsql.PrintExpr(s.Where)
		if pre, err := dbsql.Exec(d.sqlDB, whereSQL); err == nil {
			for _, row := range pre.Rows {
				matchedPKs = append(matchedPKs, extractPKVal(tbl, row, tbl.Schema()))
			}
		}
	} else {
		// No WHERE — all rows will be updated.
		rows, _ := query.Collect(query.NewTableScan(tbl))
		for _, row := range rows {
			matchedPKs = append(matchedPKs, extractPKVal(tbl, row, tbl.Schema()))
		}
	}

	// Apply the update.
	res, err := dbsql.Exec(d.sqlDB, rawSQL)
	if err != nil {
		return nil, err
	}

	// WAL only the updated rows (their new state) by re-fetching each by PK.
	name := strings.ToLower(s.Table)
	for _, pkVals := range matchedPKs {
		newRow, found := tbl.Get(pkVals...)
		if !found {
			continue
		}
		if err := d.walAppend(name, rowOpInsert, newRow); err != nil {
			return nil, fmt.Errorf("UPDATE WAL: %w", err)
		}
	}
	return res, nil
}

func (d *DB) execDelete(rawSQL string, s *dbsql.DeleteStmt) (*dbsql.ExecResult, error) {
	name := strings.ToLower(s.Table)
	var preRows []query.Row
	if s.Where != nil {
		whereSQL := "SELECT * FROM " + s.Table + " WHERE " + dbsql.PrintExpr(s.Where)
		if pre, err := dbsql.Exec(d.sqlDB, whereSQL); err == nil {
			preRows = pre.Rows
		}
	} else {
		tbl, ok := d.sqlDB.Table(s.Table)
		if ok {
			preRows, _ = query.Collect(query.NewTableScan(tbl))
		}
	}

	res, err := dbsql.Exec(d.sqlDB, rawSQL)
	if err != nil {
		return nil, err
	}
	for _, row := range preRows {
		if err := d.walAppend(name, rowOpDelete, row); err != nil {
			return nil, fmt.Errorf("DELETE WAL: %w", err)
		}
	}
	return res, nil
}

// -------------------------------------------------------------------------
// WAL serialisation
// -------------------------------------------------------------------------

func (d *DB) walAppend(table string, op byte, row query.Row) error {
	f, ok := d.walFiles[table]
	if !ok {
		return fmt.Errorf("walAppend: no WAL file for %q", table)
	}
	buf := marshalRow(op, row)
	if _, err := f.Write(buf); err != nil {
		return err
	}
	return f.Sync()
}

// Record format: [op:1][ncols:4][col...] where col = [type:1][len:4][data:N]
func marshalRow(op byte, row query.Row) []byte {
	hdr := make([]byte, 5)
	hdr[0] = op
	binary.LittleEndian.PutUint32(hdr[1:], uint32(len(row)))
	var out []byte
	out = append(out, hdr...)
	for _, v := range row {
		out = append(out, marshalValue(v)...)
	}
	return out
}

func marshalValue(v query.Value) []byte {
	switch v.Type() {
	case query.TypeNull:
		return []byte{byte(query.TypeNull), 0, 0, 0, 0}
	case query.TypeInt:
		b := make([]byte, 13)
		b[0] = byte(query.TypeInt)
		binary.LittleEndian.PutUint32(b[1:], 8)
		binary.LittleEndian.PutUint64(b[5:], uint64(v.AsInt()))
		return b
	case query.TypeFloat:
		b := make([]byte, 13)
		b[0] = byte(query.TypeFloat)
		binary.LittleEndian.PutUint32(b[1:], 8)
		binary.LittleEndian.PutUint64(b[5:], math.Float64bits(v.AsFloat()))
		return b
	case query.TypeText:
		s := []byte(v.AsText())
		b := make([]byte, 5+len(s))
		b[0] = byte(query.TypeText)
		binary.LittleEndian.PutUint32(b[1:], uint32(len(s)))
		copy(b[5:], s)
		return b
	case query.TypeBool:
		b := []byte{byte(query.TypeBool), 1, 0, 0, 0, 0}
		if v.AsBool() {
			b[5] = 1
		}
		return b
	}
	return []byte{byte(query.TypeNull), 0, 0, 0, 0}
}

func unmarshalValue(r io.Reader) (query.Value, error) {
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return query.Null(), err
	}
	typ := query.ValueType(hdr[0])
	dataLen := int(binary.LittleEndian.Uint32(hdr[1:]))
	data := make([]byte, dataLen)
	if dataLen > 0 {
		if _, err := io.ReadFull(r, data); err != nil {
			return query.Null(), err
		}
	}
	switch typ {
	case query.TypeNull:
		return query.Null(), nil
	case query.TypeInt:
		if len(data) < 8 {
			return query.Null(), nil
		}
		return query.Int(int64(binary.LittleEndian.Uint64(data))), nil
	case query.TypeFloat:
		if len(data) < 8 {
			return query.Null(), nil
		}
		return query.Float(math.Float64frombits(binary.LittleEndian.Uint64(data))), nil
	case query.TypeText:
		return query.Text(string(data)), nil
	case query.TypeBool:
		if len(data) < 1 {
			return query.Null(), nil
		}
		return query.Bool(data[0] == 1), nil
	}
	return query.Null(), nil
}

// -------------------------------------------------------------------------
// WAL replay
// -------------------------------------------------------------------------

func (d *DB) replayWAL(tbl *query.Table, walPath string) error {
	f, err := os.Open(walPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	hdr := make([]byte, 5)
	for {
		if _, err := io.ReadFull(f, hdr); err != nil {
			return nil // EOF or truncated tail — stop cleanly
		}
		op := hdr[0]
		ncols := int(binary.LittleEndian.Uint32(hdr[1:]))
		row := make(query.Row, ncols)
		ok := true
		for i := 0; i < ncols; i++ {
			v, err := unmarshalValue(f)
			if err != nil {
				ok = false
				break
			}
			row[i] = v
		}
		if !ok {
			return nil
		}
		switch op {
		case rowOpInsert:
			tbl.Upsert(row)
		case rowOpDelete:
			pkCols := tbl.PKCols()
			schema := tbl.Schema()
			pkVals := make([]query.Value, len(pkCols))
			for i, name := range pkCols {
				idx := schema.Index(name)
				if idx >= 0 {
					pkVals[i] = row[idx]
				}
			}
			tbl.Delete(pkVals...)
		}
	}
}

// -------------------------------------------------------------------------
// Catalog persistence
// -------------------------------------------------------------------------

func (d *DB) catalogPath() string { return filepath.Join(d.dir, "catalog.json") }
func (d *DB) walPath(table string) string {
	return filepath.Join(d.dir, table+".wal")
}

func (d *DB) saveCatalog() error {
	entries := make([]catalogEntry, 0, len(d.catalog))
	for _, e := range d.catalog {
		entries = append(entries, e)
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := d.catalogPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, d.catalogPath())
}

func (d *DB) loadCatalog() error {
	data, err := os.ReadFile(d.catalogPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var entries []catalogEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("corrupt catalog: %w", err)
	}
	for _, entry := range entries {
		cols := make([]query.Column, len(entry.Columns))
		for i, c := range entry.Columns {
			cols[i] = query.Column{Name: c.Name, Type: query.ValueType(c.Type)}
		}
		schema := query.Schema{Columns: cols}
		tbl, err := query.NewTable(schema, entry.PKCols...)
		if err != nil {
			return fmt.Errorf("reconstruct table %q: %w", entry.Name, err)
		}
		name := strings.ToLower(entry.Name)
		if err := d.replayWAL(tbl, d.walPath(name)); err != nil {
			return fmt.Errorf("replay WAL %q: %w", entry.Name, err)
		}
		if err := d.sqlDB.CreateTable(entry.Name, tbl); err != nil {
			return fmt.Errorf("register table %q: %w", entry.Name, err)
		}
		d.catalog[name] = entry
		if err := d.openWAL(name); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) openWAL(name string) error {
	f, err := os.OpenFile(d.walPath(name), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("openWAL %q: %w", name, err)
	}
	d.walFiles[name] = f
	return nil
}

// -------------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------------

func sqlColTypeToValueType(ct dbsql.ColType) query.ValueType {
	switch ct {
	case dbsql.ColTypeInt:
		return query.TypeInt
	case dbsql.ColTypeFloat:
		return query.TypeFloat
	case dbsql.ColTypeText:
		return query.TypeText
	case dbsql.ColTypeBool:
		return query.TypeBool
	}
	return query.TypeNull
}

func litToValue(e dbsql.Expr) query.Value {
	lit, ok := e.(*dbsql.LitExpr)
	if !ok {
		return query.Null()
	}
	switch lit.Kind {
	case dbsql.LitNull:
		return query.Null()
	case dbsql.LitInt:
		return query.Int(lit.IntVal)
	case dbsql.LitFloat:
		return query.Float(lit.FltVal)
	case dbsql.LitStr:
		return query.Text(lit.StrVal)
	case dbsql.LitBool:
		return query.Bool(lit.BoolVal)
	}
	return query.Null()
}

// -------------------------------------------------------------------------
// WAL compaction
// -------------------------------------------------------------------------

// Compact rewrites the WAL for table as a clean snapshot of the current
// in-memory state, discarding all historical records.
// After compaction the WAL contains exactly one INSERT record per live row.
// This keeps WAL size proportional to the number of rows, not the number
// of writes.
//
// Compact is safe to call at any time. It is a no-op if the table does not
// exist. For best results call it periodically or after bulk operations.
func (d *DB) Compact(table string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.compactLocked(strings.ToLower(table))
}

// CompactAll compacts all tables.
func (d *DB) CompactAll() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for name := range d.catalog {
		if err := d.compactLocked(name); err != nil {
			return fmt.Errorf("CompactAll %q: %w", name, err)
		}
	}
	return nil
}

func (d *DB) compactLocked(name string) error {
	tbl, ok := d.sqlDB.Table(name)
	if !ok {
		return nil
	}

	// Collect all live rows.
	rows, err := query.Collect(query.NewTableScan(tbl))
	if err != nil {
		return fmt.Errorf("compact scan: %w", err)
	}

	// Close the old WAL file.
	if f, ok := d.walFiles[name]; ok {
		if err := f.Sync(); err != nil {
			return err
		}
		f.Close()
		delete(d.walFiles, name)
	}

	// Truncate the WAL file (rewrite from scratch).
	wp := d.walPath(name)
	f, err := os.OpenFile(wp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("compact open: %w", err)
	}

	// Write one INSERT per live row.
	for _, row := range rows {
		buf := marshalRow(rowOpInsert, row)
		if _, err := f.Write(buf); err != nil {
			f.Close()
			return fmt.Errorf("compact write: %w", err)
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}

	// Reopen for appending.
	f.Close()
	newF, err := os.OpenFile(wp, os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("compact reopen: %w", err)
	}
	d.walFiles[name] = newF
	return nil
}

// WALSize returns the current WAL file size in bytes for table.
// Returns 0 if the table does not exist or has no WAL.
func (d *DB) WALSize(table string) int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	wp := d.walPath(strings.ToLower(table))
	info, err := os.Stat(wp)
	if err != nil {
		return 0
	}
	return info.Size()
}

// extractPKVal extracts primary key column values from a row using the
// table's PK column list.
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
