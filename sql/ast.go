package sql

// -------------------------------------------------------------------------
// Statement nodes
// -------------------------------------------------------------------------

// Stmt is the top-level interface for all SQL statements.
type Stmt interface{ stmtNode() }

// SelectStmt represents a SELECT query.
type SelectStmt struct {
	Distinct bool
	Columns  []SelectColumn // projection list; nil means SELECT *
	From     string         // table name
	Join     *JoinClause
	Where    Expr
	GroupBy  []string
	Having   Expr
	OrderBy  []OrderByElem
	Limit    *int64
	Offset   *int64
}

func (*SelectStmt) stmtNode() {}

// SelectColumn is one item in the SELECT list.
type SelectColumn struct {
	Expr  Expr   // nil = SELECT *
	Alias string // empty = use expr's natural name
	Star  bool   // true for SELECT *
}

// JoinClause holds a single join.
type JoinClause struct {
	Kind  JoinKind
	Table string
	On    Expr
}

// JoinKind is INNER or LEFT (outer).
type JoinKind int

const (
	JoinInner JoinKind = iota
	JoinLeft
)

// OrderByElem is one ORDER BY term.
type OrderByElem struct {
	Expr Expr
	Desc bool
}

// InsertStmt represents INSERT INTO ... VALUES (...).
type InsertStmt struct {
	Table   string
	Columns []string
	Values  []Expr
}

func (*InsertStmt) stmtNode() {}

// UpdateStmt represents UPDATE ... SET ... WHERE ...
type UpdateStmt struct {
	Table string
	Sets  []SetClause
	Where Expr
}

func (*UpdateStmt) stmtNode() {}

// SetClause is one col = expr assignment.
type SetClause struct {
	Column string
	Value  Expr
}

// DeleteStmt represents DELETE FROM ... WHERE ...
type DeleteStmt struct {
	Table string
	Where Expr
}

func (*DeleteStmt) stmtNode() {}

// CreateTableStmt represents CREATE TABLE.
type CreateTableStmt struct {
	Table      string
	Columns    []ColumnDef
	PrimaryKey []string
}

func (*CreateTableStmt) stmtNode() {}

// ColumnDef is one column in CREATE TABLE.
type ColumnDef struct {
	Name       string
	Type       ColType
	PrimaryKey bool
}

// ColType is the SQL column type.
type ColType int

const (
	ColTypeInt ColType = iota
	ColTypeFloat
	ColTypeText
	ColTypeBool
)

// DropTableStmt represents DROP TABLE.
type DropTableStmt struct {
	Table string
}

func (*DropTableStmt) stmtNode() {}

// -------------------------------------------------------------------------
// Expression nodes
// -------------------------------------------------------------------------

// Expr is the interface for all SQL expression nodes.
type Expr interface{ exprNode() }

// ColumnExpr references a column, optionally table-qualified.
type ColumnExpr struct {
	Table string // empty if unqualified
	Name  string
}

func (*ColumnExpr) exprNode() {}

// LitExpr is a literal value.
type LitExpr struct {
	Kind    LitKind
	IntVal  int64
	FltVal  float64
	StrVal  string
	BoolVal bool
}

func (*LitExpr) exprNode() {}

// LitKind identifies the literal type.
type LitKind int

const (
	LitNull LitKind = iota
	LitInt
	LitFloat
	LitStr
	LitBool
)

// BinaryExpr is a binary operator expression.
type BinaryExpr struct {
	Op    BinOp
	Left  Expr
	Right Expr
}

func (*BinaryExpr) exprNode() {}

// BinOp is a binary operator.
type BinOp int

const (
	BinOpAdd BinOp = iota
	BinOpSub
	BinOpMul
	BinOpDiv
	BinOpEq
	BinOpNeq
	BinOpLt
	BinOpLte
	BinOpGt
	BinOpGte
	BinOpAnd
	BinOpOr
)

// UnaryExpr is a unary operator (NOT, unary minus).
type UnaryExpr struct {
	Op   UnaryOp
	Expr Expr
}

func (*UnaryExpr) exprNode() {}

// UnaryOp is a unary operator.
type UnaryOp int

const (
	UnaryNot UnaryOp = iota
	UnaryNeg
)

// AggExpr is an aggregate function call.
type AggExpr struct {
	Func     AggFunc
	Arg      Expr // nil for COUNT(*)
	Distinct bool
}

func (*AggExpr) exprNode() {}

// AggFunc identifies the aggregate function.
type AggFunc int

const (
	AggFuncCount AggFunc = iota
	AggFuncSum
	AggFuncMin
	AggFuncMax
	AggFuncAvg
)

// IsNullExpr checks IS NULL / IS NOT NULL.
type IsNullExpr struct {
	Expr   Expr
	IsNull bool // true=IS NULL, false=IS NOT NULL
}

func (*IsNullExpr) exprNode() {}
