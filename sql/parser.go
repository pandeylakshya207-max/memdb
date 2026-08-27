package sql

import (
	"fmt"
	"strings"
)

// Parse parses a single SQL statement from src.
func Parse(src string) (Stmt, error) {
	tokens, err := Tokenise(src)
	if err != nil {
		return nil, err
	}
	p := &parser{s: newStream(tokens)}
	stmt, err := p.parseStmt()
	if err != nil {
		return nil, err
	}
	// Consume optional trailing semicolon.
	p.s.match(tokSemicolon)
	if !p.s.check(tokEOF) {
		return nil, fmt.Errorf("parser: unexpected token %q after statement", p.s.peek().Text)
	}
	return stmt, nil
}

// -------------------------------------------------------------------------
// Parser
// -------------------------------------------------------------------------

type parser struct {
	s *tokenStream
}

func (p *parser) parseStmt() (Stmt, error) {
	switch p.s.peekKind() {
	case tokSelect:
		return p.parseSelect()
	case tokInsert:
		return p.parseInsert()
	case tokUpdate:
		return p.parseUpdate()
	case tokDelete:
		return p.parseDelete()
	case tokCreate:
		return p.parseCreate()
	case tokDrop:
		return p.parseDrop()
	default:
		return nil, fmt.Errorf("parser: unexpected token %q — expected statement keyword", p.s.peek().Text)
	}
}

// -------------------------------------------------------------------------
// SELECT
// -------------------------------------------------------------------------

func (p *parser) parseSelect() (*SelectStmt, error) {
	if _, err := p.s.eat(tokSelect); err != nil {
		return nil, err
	}
	stmt := &SelectStmt{}

	// DISTINCT
	if p.s.match(tokDistinct) {
		stmt.Distinct = true
	}

	// Column list
	cols, err := p.parseSelectColumns()
	if err != nil {
		return nil, err
	}
	stmt.Columns = cols

	// FROM
	if _, err := p.s.eat(tokFrom); err != nil {
		return nil, err
	}
	tbl, err := p.s.eatIdent()
	if err != nil {
		return nil, err
	}
	stmt.From = tbl

	// Optional JOIN
	if p.s.check(tokJoin) || p.s.check(tokInner) || p.s.check(tokLeft) {
		jc, err := p.parseJoin()
		if err != nil {
			return nil, err
		}
		stmt.Join = jc
	}

	// WHERE
	if p.s.match(tokWhere) {
		expr, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		stmt.Where = expr
	}

	// GROUP BY
	if p.s.check(tokGroup) {
		p.s.next()
		if _, err := p.s.eat(tokBy); err != nil {
			return nil, err
		}
		groups, err := p.parseIdentList()
		if err != nil {
			return nil, err
		}
		stmt.GroupBy = groups
	}

	// HAVING
	if p.s.match(tokHaving) {
		expr, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		stmt.Having = expr
	}

	// ORDER BY
	if p.s.check(tokOrder) {
		p.s.next()
		if _, err := p.s.eat(tokBy); err != nil {
			return nil, err
		}
		orderElems, err := p.parseOrderBy()
		if err != nil {
			return nil, err
		}
		stmt.OrderBy = orderElems
	}

	// LIMIT
	if p.s.match(tokLimit) {
		n, err := p.parseInt64()
		if err != nil {
			return nil, err
		}
		stmt.Limit = &n
	}

	// OFFSET
	if p.s.match(tokOffset) {
		n, err := p.parseInt64()
		if err != nil {
			return nil, err
		}
		stmt.Offset = &n
	}

	return stmt, nil
}

func (p *parser) parseSelectColumns() ([]SelectColumn, error) {
	// SELECT *
	if p.s.check(tokStar) {
		p.s.next()
		return []SelectColumn{{Star: true}}, nil
	}

	var cols []SelectColumn
	for {
		col, err := p.parseSelectColumn()
		if err != nil {
			return nil, err
		}
		cols = append(cols, col)
		if !p.s.match(tokComma) {
			break
		}
	}
	return cols, nil
}

func (p *parser) parseSelectColumn() (SelectColumn, error) {
	expr, err := p.parseExpr()
	if err != nil {
		return SelectColumn{}, err
	}
	col := SelectColumn{Expr: expr}
	// Optional AS alias
	if p.s.match(tokAs) {
		alias, err := p.s.eatIdent()
		if err != nil {
			return SelectColumn{}, err
		}
		col.Alias = alias
	} else if p.s.check(tokIdent) {
		// Implicit alias (no AS keyword)
		col.Alias = p.s.next().Text
	}
	return col, nil
}

func (p *parser) parseJoin() (*JoinClause, error) {
	kind := JoinInner
	if p.s.match(tokLeft) {
		kind = JoinLeft
		p.s.match(tokOuter) // optional OUTER keyword
	}
	p.s.match(tokInner) // optional INNER keyword
	if _, err := p.s.eat(tokJoin); err != nil {
		return nil, err
	}
	tbl, err := p.s.eatIdent()
	if err != nil {
		return nil, err
	}
	if _, err := p.s.eat(tokOn); err != nil {
		return nil, err
	}
	on, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	return &JoinClause{Kind: kind, Table: tbl, On: on}, nil
}

func (p *parser) parseIdentList() ([]string, error) {
	var names []string
	for {
		t := p.s.peek()
		if t.Kind != tokIdent {
			return nil, fmt.Errorf("parser: expected identifier in list, got %q", t.Text)
		}
		p.s.next()
		names = append(names, t.Text)
		if !p.s.match(tokComma) {
			break
		}
	}
	return names, nil
}

func (p *parser) parseOrderBy() ([]OrderByElem, error) {
	var elems []OrderByElem
	for {
		expr, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		elem := OrderByElem{Expr: expr}
		if p.s.match(tokDesc) {
			elem.Desc = true
		} else {
			p.s.match(tokAsc) // optional ASC
		}
		elems = append(elems, elem)
		if !p.s.match(tokComma) {
			break
		}
	}
	return elems, nil
}

func (p *parser) parseInt64() (int64, error) {
	t := p.s.next()
	if t.Kind != tokIntLit {
		return 0, fmt.Errorf("parser: expected integer literal, got %q", t.Text)
	}
	return parseIntLit(t.Text)
}

// -------------------------------------------------------------------------
// INSERT
// -------------------------------------------------------------------------

func (p *parser) parseInsert() (*InsertStmt, error) {
	p.s.eat(tokInsert)
	if _, err := p.s.eat(tokInto); err != nil {
		return nil, err
	}
	tbl, err := p.s.eatIdent()
	if err != nil {
		return nil, err
	}
	if _, err := p.s.eat(tokLParen); err != nil {
		return nil, err
	}
	cols, err := p.parseIdentList()
	if err != nil {
		return nil, err
	}
	if _, err := p.s.eat(tokRParen); err != nil {
		return nil, err
	}
	if _, err := p.s.eat(tokValues); err != nil {
		return nil, err
	}
	if _, err := p.s.eat(tokLParen); err != nil {
		return nil, err
	}
	vals, err := p.parseExprList()
	if err != nil {
		return nil, err
	}
	if _, err := p.s.eat(tokRParen); err != nil {
		return nil, err
	}
	return &InsertStmt{Table: tbl, Columns: cols, Values: vals}, nil
}

// -------------------------------------------------------------------------
// UPDATE
// -------------------------------------------------------------------------

func (p *parser) parseUpdate() (*UpdateStmt, error) {
	p.s.eat(tokUpdate)
	tbl, err := p.s.eatIdent()
	if err != nil {
		return nil, err
	}
	if _, err := p.s.eat(tokSet); err != nil {
		return nil, err
	}
	var sets []SetClause
	for {
		col, err := p.s.eatIdent()
		if err != nil {
			return nil, err
		}
		if _, err := p.s.eat(tokEq); err != nil {
			return nil, err
		}
		val, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		sets = append(sets, SetClause{Column: col, Value: val})
		if !p.s.match(tokComma) {
			break
		}
	}
	stmt := &UpdateStmt{Table: tbl, Sets: sets}
	if p.s.match(tokWhere) {
		expr, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		stmt.Where = expr
	}
	return stmt, nil
}

// -------------------------------------------------------------------------
// DELETE
// -------------------------------------------------------------------------

func (p *parser) parseDelete() (*DeleteStmt, error) {
	p.s.eat(tokDelete)
	if _, err := p.s.eat(tokFrom); err != nil {
		return nil, err
	}
	tbl, err := p.s.eatIdent()
	if err != nil {
		return nil, err
	}
	stmt := &DeleteStmt{Table: tbl}
	if p.s.match(tokWhere) {
		expr, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		stmt.Where = expr
	}
	return stmt, nil
}

// -------------------------------------------------------------------------
// CREATE TABLE
// -------------------------------------------------------------------------

func (p *parser) parseCreate() (*CreateTableStmt, error) {
	p.s.eat(tokCreate)
	if _, err := p.s.eat(tokTable); err != nil {
		return nil, err
	}
	tbl, err := p.s.eatIdent()
	if err != nil {
		return nil, err
	}
	if _, err := p.s.eat(tokLParen); err != nil {
		return nil, err
	}
	stmt := &CreateTableStmt{Table: tbl}
	for {
		col, err := p.parseColumnDef()
		if err != nil {
			return nil, err
		}
		if col.PrimaryKey {
			stmt.PrimaryKey = append(stmt.PrimaryKey, col.Name)
		}
		stmt.Columns = append(stmt.Columns, col)
		if !p.s.match(tokComma) {
			break
		}
		if p.s.check(tokRParen) {
			break
		}
	}
	if _, err := p.s.eat(tokRParen); err != nil {
		return nil, err
	}
	if len(stmt.PrimaryKey) == 0 {
		return nil, fmt.Errorf("parser: CREATE TABLE %q requires at least one PRIMARY KEY column", tbl)
	}
	return stmt, nil
}

func (p *parser) parseColumnDef() (ColumnDef, error) {
	name, err := p.s.eatIdent()
	if err != nil {
		return ColumnDef{}, err
	}
	t := p.s.next()
	var ct ColType
	switch t.Kind {
	case tokInt:
		ct = ColTypeInt
	case tokFloat:
		ct = ColTypeFloat
	case tokText:
		ct = ColTypeText
	case tokBool:
		ct = ColTypeBool
	default:
		return ColumnDef{}, fmt.Errorf("parser: expected column type, got %q", t.Text)
	}
	col := ColumnDef{Name: name, Type: ct}
	// Optional PRIMARY KEY
	if p.s.check(tokPrimary) {
		p.s.next()
		if _, err := p.s.eat(tokKey); err != nil {
			return ColumnDef{}, err
		}
		col.PrimaryKey = true
	}
	return col, nil
}

// -------------------------------------------------------------------------
// DROP TABLE
// -------------------------------------------------------------------------

func (p *parser) parseDrop() (*DropTableStmt, error) {
	p.s.eat(tokDrop)
	if _, err := p.s.eat(tokTable); err != nil {
		return nil, err
	}
	tbl, err := p.s.eatIdent()
	if err != nil {
		return nil, err
	}
	return &DropTableStmt{Table: tbl}, nil
}

// -------------------------------------------------------------------------
// Expression parsing — recursive descent with precedence climbing
// -------------------------------------------------------------------------

// Precedence levels (higher = tighter binding):
//   1: OR
//   2: AND
//   3: NOT
//   4: IS NULL, comparison (=, !=, <, <=, >, >=)
//   5: addition / subtraction
//   6: multiplication / division
//   7: unary minus
//   8: primary (literal, column, aggregate, parenthesised expr)

func (p *parser) parseExpr() (Expr, error) {
	return p.parseOr()
}

func (p *parser) parseOr() (Expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.s.check(tokOr) {
		p.s.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Op: BinOpOr, Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) parseAnd() (Expr, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.s.check(tokAnd) {
		p.s.next()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Op: BinOpAnd, Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) parseNot() (Expr, error) {
	if p.s.match(tokNot) {
		inner, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &UnaryExpr{Op: UnaryNot, Expr: inner}, nil
	}
	return p.parseComparison()
}

func (p *parser) parseComparison() (Expr, error) {
	left, err := p.parseAddSub()
	if err != nil {
		return nil, err
	}
	// IS [NOT] NULL
	if p.s.check(tokIs) {
		p.s.next()
		notNull := p.s.match(tokNot)
		if _, err := p.s.eat(tokNull); err != nil {
			return nil, err
		}
		return &IsNullExpr{Expr: left, IsNull: !notNull}, nil
	}
	ops := map[TokenKind]BinOp{
		tokEq:  BinOpEq,
		tokNeq: BinOpNeq,
		tokLt:  BinOpLt,
		tokLte: BinOpLte,
		tokGt:  BinOpGt,
		tokGte: BinOpGte,
	}
	if op, ok := ops[p.s.peekKind()]; ok {
		p.s.next()
		right, err := p.parseAddSub()
		if err != nil {
			return nil, err
		}
		return &BinaryExpr{Op: op, Left: left, Right: right}, nil
	}
	return left, nil
}

func (p *parser) parseAddSub() (Expr, error) {
	left, err := p.parseMulDiv()
	if err != nil {
		return nil, err
	}
	for {
		switch p.s.peekKind() {
		case tokPlus:
			p.s.next()
			right, err := p.parseMulDiv()
			if err != nil {
				return nil, err
			}
			left = &BinaryExpr{Op: BinOpAdd, Left: left, Right: right}
		case tokMinus:
			p.s.next()
			right, err := p.parseMulDiv()
			if err != nil {
				return nil, err
			}
			left = &BinaryExpr{Op: BinOpSub, Left: left, Right: right}
		default:
			return left, nil
		}
	}
}

func (p *parser) parseMulDiv() (Expr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		switch p.s.peekKind() {
		case tokStar:
			p.s.next()
			right, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			left = &BinaryExpr{Op: BinOpMul, Left: left, Right: right}
		case tokSlash:
			p.s.next()
			right, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			left = &BinaryExpr{Op: BinOpDiv, Left: left, Right: right}
		default:
			return left, nil
		}
	}
}

func (p *parser) parseUnary() (Expr, error) {
	if p.s.match(tokMinus) {
		inner, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		return &UnaryExpr{Op: UnaryNeg, Expr: inner}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (Expr, error) {
	t := p.s.peek()
	switch t.Kind {
	case tokIntLit:
		p.s.next()
		v, err := parseIntLit(t.Text)
		if err != nil {
			return nil, err
		}
		return &LitExpr{Kind: LitInt, IntVal: v}, nil

	case tokFloatLit:
		p.s.next()
		v, err := parseFloatLit(t.Text)
		if err != nil {
			return nil, err
		}
		return &LitExpr{Kind: LitFloat, FltVal: v}, nil

	case tokStrLit:
		p.s.next()
		return &LitExpr{Kind: LitStr, StrVal: t.Text}, nil

	case tokNull:
		p.s.next()
		return &LitExpr{Kind: LitNull}, nil

	case tokTrue:
		p.s.next()
		return &LitExpr{Kind: LitBool, BoolVal: true}, nil

	case tokFalse:
		p.s.next()
		return &LitExpr{Kind: LitBool, BoolVal: false}, nil

	case tokLParen:
		p.s.next()
		inner, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.s.eat(tokRParen); err != nil {
			return nil, err
		}
		return inner, nil

	// Aggregate functions
	case tokCount, tokSum, tokMin, tokMax, tokAvg:
		return p.parseAgg()

	case tokIdent:
		p.s.next()
		name := t.Text
		// Table-qualified: name.col
		if p.s.check(tokDot) {
			p.s.next()
			col, err := p.s.eatIdent()
			if err != nil {
				return nil, err
			}
			return &ColumnExpr{Table: name, Name: col}, nil
		}
		return &ColumnExpr{Name: name}, nil

	case tokStar:
		// SELECT * already handled; star in expression context (COUNT(*)) is
		// handled by parseAgg. Hitting * here is an error.
		return nil, fmt.Errorf("parser: unexpected '*' in expression at pos %d", t.Pos)

	default:
		return nil, fmt.Errorf("parser: unexpected token %q in expression at pos %d", t.Text, t.Pos)
	}
}

func (p *parser) parseAgg() (*AggExpr, error) {
	t := p.s.next()
	var fn AggFunc
	switch t.Kind {
	case tokCount:
		fn = AggFuncCount
	case tokSum:
		fn = AggFuncSum
	case tokMin:
		fn = AggFuncMin
	case tokMax:
		fn = AggFuncMax
	case tokAvg:
		fn = AggFuncAvg
	}
	if _, err := p.s.eat(tokLParen); err != nil {
		return nil, err
	}
	agg := &AggExpr{Func: fn}
	if p.s.match(tokDistinct) {
		agg.Distinct = true
	}
	// COUNT(*) special case
	if fn == AggFuncCount && p.s.check(tokStar) {
		p.s.next()
	} else {
		arg, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		agg.Arg = arg
	}
	if _, err := p.s.eat(tokRParen); err != nil {
		return nil, err
	}
	return agg, nil
}

func (p *parser) parseExprList() ([]Expr, error) {
	var exprs []Expr
	for {
		expr, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		exprs = append(exprs, expr)
		if !p.s.match(tokComma) {
			break
		}
	}
	return exprs, nil
}

// -------------------------------------------------------------------------
// AST pretty-printer (for debugging / test output)
// -------------------------------------------------------------------------

// PrintExpr returns a readable string for an Expr.
func PrintExpr(e Expr) string {
	if e == nil {
		return "<nil>"
	}
	switch n := e.(type) {
	case *ColumnExpr:
		if n.Table != "" {
			return n.Table + "." + n.Name
		}
		return n.Name
	case *LitExpr:
		switch n.Kind {
		case LitNull:
			return "NULL"
		case LitInt:
			return fmt.Sprintf("%d", n.IntVal)
		case LitFloat:
			return fmt.Sprintf("%g", n.FltVal)
		case LitStr:
			return fmt.Sprintf("'%s'", n.StrVal)
		case LitBool:
			if n.BoolVal {
				return "TRUE"
			}
			return "FALSE"
		}
	case *BinaryExpr:
		opStr := map[BinOp]string{
			BinOpAdd: "+", BinOpSub: "-", BinOpMul: "*", BinOpDiv: "/",
			BinOpEq: "=", BinOpNeq: "!=", BinOpLt: "<", BinOpLte: "<=",
			BinOpGt: ">", BinOpGte: ">=", BinOpAnd: "AND", BinOpOr: "OR",
		}[n.Op]
		return fmt.Sprintf("(%s %s %s)", PrintExpr(n.Left), opStr, PrintExpr(n.Right))
	case *UnaryExpr:
		if n.Op == UnaryNot {
			return fmt.Sprintf("(NOT %s)", PrintExpr(n.Expr))
		}
		return fmt.Sprintf("(-%s)", PrintExpr(n.Expr))
	case *AggExpr:
		fn := map[AggFunc]string{
			AggFuncCount: "COUNT", AggFuncSum: "SUM", AggFuncMin: "MIN",
			AggFuncMax: "MAX", AggFuncAvg: "AVG",
		}[n.Func]
		if n.Arg == nil {
			return fn + "(*)"
		}
		return fmt.Sprintf("%s(%s)", fn, PrintExpr(n.Arg))
	case *IsNullExpr:
		if n.IsNull {
			return fmt.Sprintf("(%s IS NULL)", PrintExpr(n.Expr))
		}
		return fmt.Sprintf("(%s IS NOT NULL)", PrintExpr(n.Expr))
	}
	return "?"
}

// PrintStmt returns a readable string for a Stmt.
func PrintStmt(s Stmt) string {
	switch n := s.(type) {
	case *SelectStmt:
		var sb strings.Builder
		sb.WriteString("SELECT ")
		if n.Distinct {
			sb.WriteString("DISTINCT ")
		}
		if len(n.Columns) == 1 && n.Columns[0].Star {
			sb.WriteString("*")
		} else {
			for i, c := range n.Columns {
				if i > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString(PrintExpr(c.Expr))
				if c.Alias != "" {
					sb.WriteString(" AS " + c.Alias)
				}
			}
		}
		sb.WriteString(" FROM " + n.From)
		if n.Join != nil {
			if n.Join.Kind == JoinLeft {
				sb.WriteString(" LEFT")
			} else {
				sb.WriteString(" INNER")
			}
			sb.WriteString(" JOIN " + n.Join.Table + " ON " + PrintExpr(n.Join.On))
		}
		if n.Where != nil {
			sb.WriteString(" WHERE " + PrintExpr(n.Where))
		}
		if len(n.GroupBy) > 0 {
			sb.WriteString(" GROUP BY " + strings.Join(n.GroupBy, ", "))
		}
		if n.Having != nil {
			sb.WriteString(" HAVING " + PrintExpr(n.Having))
		}
		if len(n.OrderBy) > 0 {
			sb.WriteString(" ORDER BY ")
			for i, o := range n.OrderBy {
				if i > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString(PrintExpr(o.Expr))
				if o.Desc {
					sb.WriteString(" DESC")
				}
			}
		}
		if n.Limit != nil {
			sb.WriteString(fmt.Sprintf(" LIMIT %d", *n.Limit))
		}
		if n.Offset != nil {
			sb.WriteString(fmt.Sprintf(" OFFSET %d", *n.Offset))
		}
		return sb.String()
	case *InsertStmt:
		return fmt.Sprintf("INSERT INTO %s (%s) VALUES (...)", n.Table, strings.Join(n.Columns, ", "))
	case *UpdateStmt:
		return fmt.Sprintf("UPDATE %s SET ...", n.Table)
	case *DeleteStmt:
		return fmt.Sprintf("DELETE FROM %s", n.Table)
	case *CreateTableStmt:
		return fmt.Sprintf("CREATE TABLE %s (%d columns)", n.Table, len(n.Columns))
	case *DropTableStmt:
		return fmt.Sprintf("DROP TABLE %s", n.Table)
	}
	return "?"
}
