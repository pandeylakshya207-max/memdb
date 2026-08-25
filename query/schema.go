// Package query implements a simple relational query engine over memdb's
// storage primitives.
//
// Data model:
//   - A Schema describes column names and types.
//   - A Row is an ordered slice of Values, one per schema column.
//   - A Value is a tagged union: NULL, INT (int64), FLOAT (float64), TEXT (string), BOOL.
//
// Query execution uses the Volcano/iterator model: every operator implements
// the Iterator interface (Open → repeated Next → Close).  Operators compose
// by wrapping each other, so the planner just chains them.
//
// Operators implemented:
//   - TableScan     — full ordered scan of a Table
//   - Filter        — predicate pushdown
//   - Project       — column selection / rename / computed expressions
//   - NestedLoopJoin — inner join (hash join would need more infra)
//   - HashAggregate — GROUP BY + aggregate functions (COUNT, SUM, MIN, MAX, AVG)
//   - Limit / Offset
//   - OrderBy       — in-memory sort
package query

import (
	"fmt"
	"math"
	"strings"
)

// -------------------------------------------------------------------------
// Value — tagged union
// -------------------------------------------------------------------------

// ValueType identifies the kind of a Value.
type ValueType uint8

const (
	TypeNull  ValueType = iota
	TypeInt             // int64
	TypeFloat           // float64
	TypeText            // string
	TypeBool            // bool
)

// Value is a single cell in a Row.
type Value struct {
	typ     ValueType
	intVal  int64
	fltVal  float64
	txtVal  string
	boolVal bool
}

// Null returns a NULL Value.
func Null() Value { return Value{typ: TypeNull} }

// Int returns an integer Value.
func Int(v int64) Value { return Value{typ: TypeInt, intVal: v} }

// Float returns a float Value.
func Float(v float64) Value { return Value{typ: TypeFloat, fltVal: v} }

// Text returns a text Value.
func Text(v string) Value { return Value{typ: TypeText, txtVal: v} }

// Bool returns a boolean Value.
func Bool(v bool) Value { return Value{typ: TypeBool, boolVal: v} }

// Type returns the ValueType.
func (v Value) Type() ValueType { return v.typ }

// IsNull returns true if the value is NULL.
func (v Value) IsNull() bool { return v.typ == TypeNull }

// AsInt returns the int64 value. Panics if not TypeInt.
func (v Value) AsInt() int64 {
	if v.typ != TypeInt {
		panic(fmt.Sprintf("Value.AsInt: type is %v", v.typ))
	}
	return v.intVal
}

// AsFloat returns the float64 value. Panics if not TypeFloat.
func (v Value) AsFloat() float64 {
	if v.typ != TypeFloat {
		panic(fmt.Sprintf("Value.AsFloat: type is %v", v.typ))
	}
	return v.fltVal
}

// AsText returns the string value. Panics if not TypeText.
func (v Value) AsText() string {
	if v.typ != TypeText {
		panic(fmt.Sprintf("Value.AsText: type is %v", v.typ))
	}
	return v.txtVal
}

// AsBool returns the bool value. Panics if not TypeBool.
func (v Value) AsBool() bool {
	if v.typ != TypeBool {
		panic(fmt.Sprintf("Value.AsBool: type is %v", v.typ))
	}
	return v.boolVal
}

// ToFloat coerces Int or Float to float64. Returns (0, false) for other types.
func (v Value) ToFloat() (float64, bool) {
	switch v.typ {
	case TypeInt:
		return float64(v.intVal), true
	case TypeFloat:
		return v.fltVal, true
	}
	return 0, false
}

// String returns a human-readable representation.
func (v Value) String() string {
	switch v.typ {
	case TypeNull:
		return "NULL"
	case TypeInt:
		return fmt.Sprintf("%d", v.intVal)
	case TypeFloat:
		return fmt.Sprintf("%g", v.fltVal)
	case TypeText:
		return fmt.Sprintf("%q", v.txtVal)
	case TypeBool:
		if v.boolVal {
			return "true"
		}
		return "false"
	}
	return "?"
}

// Equal returns true if two Values are equal (NULL != NULL, matching SQL semantics
// except we return false for NULL comparisons rather than NULL).
func (v Value) Equal(o Value) bool {
	if v.typ == TypeNull || o.typ == TypeNull {
		return false
	}
	if v.typ != o.typ {
		return false
	}
	switch v.typ {
	case TypeInt:
		return v.intVal == o.intVal
	case TypeFloat:
		return v.fltVal == o.fltVal
	case TypeText:
		return v.txtVal == o.txtVal
	case TypeBool:
		return v.boolVal == o.boolVal
	}
	return false
}

// Compare returns -1, 0, or 1 for ordered types (Int, Float, Text).
// Returns (0, false) when values are incomparable (NULL or mismatched types).
func (v Value) Compare(o Value) (int, bool) {
	if v.typ == TypeNull || o.typ == TypeNull {
		return 0, false
	}
	// Allow Int↔Float comparison.
	if (v.typ == TypeInt || v.typ == TypeFloat) && (o.typ == TypeInt || o.typ == TypeFloat) {
		a, _ := v.ToFloat()
		b, _ := o.ToFloat()
		switch {
		case a < b:
			return -1, true
		case a > b:
			return 1, true
		default:
			return 0, true
		}
	}
	if v.typ != o.typ {
		return 0, false
	}
	switch v.typ {
	case TypeText:
		return strings.Compare(v.txtVal, o.txtVal), true
	case TypeBool:
		if v.boolVal == o.boolVal {
			return 0, true
		}
		if !v.boolVal {
			return -1, true
		}
		return 1, true
	}
	return 0, false
}

// -------------------------------------------------------------------------
// Schema — column definitions
// -------------------------------------------------------------------------

// Column describes one column in a schema.
type Column struct {
	Name string
	Type ValueType
}

// Schema is an ordered list of columns.
type Schema struct {
	Columns []Column
}

// NewSchema constructs a schema from alternating name/type pairs.
// Panics on odd count.
func NewSchema(pairs ...interface{}) Schema {
	if len(pairs)%2 != 0 {
		panic("NewSchema: need alternating name, type pairs")
	}
	s := Schema{Columns: make([]Column, len(pairs)/2)}
	for i := 0; i < len(pairs); i += 2 {
		name, ok := pairs[i].(string)
		if !ok {
			panic(fmt.Sprintf("NewSchema: name at index %d must be string", i))
		}
		typ, ok := pairs[i+1].(ValueType)
		if !ok {
			panic(fmt.Sprintf("NewSchema: type at index %d must be ValueType", i+1))
		}
		s.Columns[i/2] = Column{Name: name, Type: typ}
	}
	return s
}

// Index returns the column index for name, or -1 if not found.
func (s Schema) Index(name string) int {
	for i, c := range s.Columns {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// Width returns the number of columns.
func (s Schema) Width() int { return len(s.Columns) }

// -------------------------------------------------------------------------
// Row
// -------------------------------------------------------------------------

// Row is an ordered list of Values corresponding to a Schema.
type Row []Value

// Clone returns a deep copy of the row.
func (r Row) Clone() Row {
	c := make(Row, len(r))
	copy(c, r)
	return c
}

// String formats the row as (v1, v2, ...).
func (r Row) String() string {
	parts := make([]string, len(r))
	for i, v := range r {
		parts[i] = v.String()
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// -------------------------------------------------------------------------
// Iterator — the Volcano model interface
// -------------------------------------------------------------------------

// Iterator is the core interface every query operator implements.
// Usage: Open → { Next → process row } until Next returns (nil, false) → Close.
type Iterator interface {
	// Open initialises the operator, opens child iterators.
	Open() error
	// Next returns the next row and true, or (nil, false) at end-of-stream.
	Next() (Row, bool)
	// Close releases resources.
	Close() error
	// Schema returns the output schema of this operator.
	Schema() Schema
}

// Collect drains an iterator into a slice and closes it.
// Returns an error if Open or Close fail.
func Collect(it Iterator) ([]Row, error) {
	if err := it.Open(); err != nil {
		return nil, err
	}
	var rows []Row
	for {
		row, ok := it.Next()
		if !ok {
			break
		}
		rows = append(rows, row.Clone())
	}
	return rows, it.Close()
}

// -------------------------------------------------------------------------
// Expression — used by Filter, Project, OrderBy
// -------------------------------------------------------------------------

// Expr evaluates to a Value given a Row and its Schema.
type Expr interface {
	Eval(row Row, schema Schema) Value
	// ResultType returns the expected output type (TypeNull = unknown/any).
	ResultType() ValueType
}

// ColRef references a column by name.
type ColRef struct{ Name string }

func (c ColRef) Eval(row Row, schema Schema) Value {
	i := schema.Index(c.Name)
	if i < 0 || i >= len(row) {
		return Null()
	}
	return row[i]
}
func (c ColRef) ResultType() ValueType { return TypeNull } // unknown without schema

// Literal is a constant value.
type Literal struct{ Val Value }

func (l Literal) Eval(_ Row, _ Schema) Value { return l.Val }
func (l Literal) ResultType() ValueType      { return l.Val.Type() }

// BinOp is a binary operator expression.
type BinOpKind uint8

const (
	OpAdd BinOpKind = iota
	OpSub
	OpMul
	OpDiv
	OpEq
	OpNeq
	OpLt
	OpLte
	OpGt
	OpGte
	OpAnd
	OpOr
)

// BinOp evaluates Left op Right.
type BinOp struct {
	Left, Right Expr
	Op          BinOpKind
}

func (b BinOp) ResultType() ValueType {
	switch b.Op {
	case OpEq, OpNeq, OpLt, OpLte, OpGt, OpGte, OpAnd, OpOr:
		return TypeBool
	}
	return TypeNull
}

func (b BinOp) Eval(row Row, schema Schema) Value {
	lv := b.Left.Eval(row, schema)
	rv := b.Right.Eval(row, schema)

	switch b.Op {
	case OpAnd:
		if lv.typ == TypeBool && rv.typ == TypeBool {
			return Bool(lv.boolVal && rv.boolVal)
		}
		return Null()
	case OpOr:
		if lv.typ == TypeBool && rv.typ == TypeBool {
			return Bool(lv.boolVal || rv.boolVal)
		}
		return Null()
	case OpEq:
		return Bool(lv.Equal(rv))
	case OpNeq:
		return Bool(!lv.Equal(rv))
	}

	// Comparison operators.
	if b.Op >= OpLt && b.Op <= OpGte {
		cmp, ok := lv.Compare(rv)
		if !ok {
			return Null()
		}
		switch b.Op {
		case OpLt:
			return Bool(cmp < 0)
		case OpLte:
			return Bool(cmp <= 0)
		case OpGt:
			return Bool(cmp > 0)
		case OpGte:
			return Bool(cmp >= 0)
		}
	}

	// Arithmetic operators — promote to float if either side is float.
	la, lok := lv.ToFloat()
	ra, rok := rv.ToFloat()
	if !lok || !rok {
		return Null()
	}
	switch b.Op {
	case OpAdd:
		return numResult(lv, rv, la+ra)
	case OpSub:
		return numResult(lv, rv, la-ra)
	case OpMul:
		return numResult(lv, rv, la*ra)
	case OpDiv:
		if ra == 0 {
			return Null() // division by zero → NULL
		}
		return numResult(lv, rv, la/ra)
	}
	return Null()
}

// numResult returns Int if both operands were Int, otherwise Float.
func numResult(l, r Value, result float64) Value {
	if l.typ == TypeInt && r.typ == TypeInt {
		return Int(int64(result))
	}
	return Float(result)
}

// NotExpr negates a boolean expression.
type NotExpr struct{ Inner Expr }

func (n NotExpr) ResultType() ValueType { return TypeBool }
func (n NotExpr) Eval(row Row, schema Schema) Value {
	v := n.Inner.Eval(row, schema)
	if v.typ != TypeBool {
		return Null()
	}
	return Bool(!v.boolVal)
}

// IsNullExpr checks for NULL.
type IsNullExpr struct{ Inner Expr }

func (e IsNullExpr) ResultType() ValueType { return TypeBool }
func (e IsNullExpr) Eval(row Row, schema Schema) Value {
	return Bool(e.Inner.Eval(row, schema).IsNull())
}

// -------------------------------------------------------------------------
// Aggregate functions
// -------------------------------------------------------------------------

// AggFunc is the kind of aggregate.
type AggFunc uint8

const (
	AggCount AggFunc = iota
	AggSum
	AggMin
	AggMax
	AggAvg
)

// AggSpec describes one output aggregate column.
type AggSpec struct {
	Func   AggFunc
	Input  Expr   // nil → COUNT(*)
	Output string // output column name
}

// aggState holds running state for one aggregate.
type aggState struct {
	spec  AggSpec
	count int64
	sum   float64
	min   Value
	max   Value
	isInt bool // track whether sum/min/max are pure-integer
}

func newAggState(spec AggSpec) *aggState {
	return &aggState{spec: spec, min: Null(), max: Null(), isInt: true}
}

func (a *aggState) feed(row Row, schema Schema) {
	a.count++
	if a.spec.Input == nil {
		return // COUNT(*)
	}
	v := a.spec.Input.Eval(row, schema)
	if v.IsNull() {
		return
	}
	if v.typ == TypeFloat {
		a.isInt = false
	}
	fv, ok := v.ToFloat()
	if !ok {
		return
	}
	a.sum += fv
	if a.min.IsNull() {
		a.min = v
		a.max = v
		return
	}
	if cmp, ok := v.Compare(a.min); ok && cmp < 0 {
		a.min = v
	}
	if cmp, ok := v.Compare(a.max); ok && cmp > 0 {
		a.max = v
	}
}

func (a *aggState) result() Value {
	switch a.spec.Func {
	case AggCount:
		return Int(a.count)
	case AggSum:
		if a.isInt {
			return Int(int64(a.sum))
		}
		return Float(a.sum)
	case AggMin:
		return a.min
	case AggMax:
		return a.max
	case AggAvg:
		if a.count == 0 {
			return Null()
		}
		return Float(a.sum / float64(a.count))
	}
	return Null()
}

// -------------------------------------------------------------------------
// Helpers used by multiple operators
// -------------------------------------------------------------------------

func isTruthy(v Value) bool {
	return v.typ == TypeBool && v.boolVal
}

// rowKey builds a string key from a set of column values (used for GROUP BY
// and hash join).
func rowKey(row Row, schema Schema, cols []string) string {
	var sb strings.Builder
	for i, name := range cols {
		if i > 0 {
			sb.WriteByte('|')
		}
		idx := schema.Index(name)
		if idx >= 0 && idx < len(row) {
			sb.WriteString(row[idx].String())
		} else {
			sb.WriteString("NULL")
		}
	}
	return sb.String()
}

// sentinel for math usage
var _ = math.MaxInt64
