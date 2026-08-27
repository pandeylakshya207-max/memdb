// Package sql implements a SQL subset parser and query planner for memdb.
//
// Supported syntax:
//
//	SELECT expr [AS alias] [, ...] FROM table
//	  [JOIN table ON expr]
//	  [WHERE expr]
//	  [GROUP BY col [, ...]]
//	  [HAVING expr]
//	  [ORDER BY col [ASC|DESC] [, ...]]
//	  [LIMIT n] [OFFSET n]
//
//	INSERT INTO table (col [, ...]) VALUES (val [, ...])
//	UPDATE table SET col = expr [, ...] [WHERE expr]
//	DELETE FROM table [WHERE expr]
//	CREATE TABLE table (col type [PRIMARY KEY] [, ...])
//	DROP TABLE table
package sql

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// -------------------------------------------------------------------------
// Token kinds
// -------------------------------------------------------------------------

type TokenKind int

const (
	// Literals
	tokEOF TokenKind = iota
	tokIdent
	tokIntLit
	tokFloatLit
	tokStrLit

	// Punctuation
	tokLParen    // (
	tokRParen    // )
	tokComma     // ,
	tokSemicolon // ;
	tokDot       // .
	tokStar      // *

	// Operators
	tokEq    // =
	tokNeq   // != or <>
	tokLt    // <
	tokLte   // <=
	tokGt    // >
	tokGte   // >=
	tokPlus  // +
	tokMinus // -
	tokSlash // /
	tokAnd   // AND
	tokOr    // OR
	tokNot   // NOT

	// Keywords
	tokSelect
	tokFrom
	tokWhere
	tokJoin
	tokInner
	tokLeft
	tokOuter
	tokOn
	tokGroup
	tokBy
	tokHaving
	tokOrder
	tokAsc
	tokDesc
	tokLimit
	tokOffset
	tokAs
	tokInsert
	tokInto
	tokValues
	tokUpdate
	tokSet
	tokDelete
	tokCreate
	tokTable
	tokDrop
	tokPrimary
	tokKey
	tokNull
	tokIs
	tokTrue
	tokFalse
	tokInt   // type keyword
	tokFloat // type keyword
	tokText  // type keyword
	tokBool  // type keyword
	tokCount
	tokSum
	tokMin
	tokMax
	tokAvg
	tokDistinct
)

var keywords = map[string]TokenKind{
	"SELECT":   tokSelect,
	"FROM":     tokFrom,
	"WHERE":    tokWhere,
	"JOIN":     tokJoin,
	"INNER":    tokInner,
	"LEFT":     tokLeft,
	"OUTER":    tokOuter,
	"ON":       tokOn,
	"GROUP":    tokGroup,
	"BY":       tokBy,
	"HAVING":   tokHaving,
	"ORDER":    tokOrder,
	"ASC":      tokAsc,
	"DESC":     tokDesc,
	"LIMIT":    tokLimit,
	"OFFSET":   tokOffset,
	"AS":       tokAs,
	"INSERT":   tokInsert,
	"INTO":     tokInto,
	"VALUES":   tokValues,
	"UPDATE":   tokUpdate,
	"SET":      tokSet,
	"DELETE":   tokDelete,
	"CREATE":   tokCreate,
	"TABLE":    tokTable,
	"DROP":     tokDrop,
	"PRIMARY":  tokPrimary,
	"KEY":      tokKey,
	"NULL":     tokNull,
	"IS":       tokIs,
	"TRUE":     tokTrue,
	"FALSE":    tokFalse,
	"AND":      tokAnd,
	"OR":       tokOr,
	"NOT":      tokNot,
	"INT":      tokInt,
	"INTEGER":  tokInt,
	"FLOAT":    tokFloat,
	"REAL":     tokFloat,
	"TEXT":     tokText,
	"VARCHAR":  tokText,
	"STRING":   tokText,
	"BOOL":     tokBool,
	"BOOLEAN":  tokBool,
	"COUNT":    tokCount,
	"SUM":      tokSum,
	"MIN":      tokMin,
	"MAX":      tokMax,
	"AVG":      tokAvg,
	"DISTINCT": tokDistinct,
}

// Token is a single lexical token.
type Token struct {
	Kind TokenKind
	Text string // original source text
	Pos  int    // byte offset in source
}

func (t Token) String() string {
	return fmt.Sprintf("Token(%d, %q, @%d)", t.Kind, t.Text, t.Pos)
}

// -------------------------------------------------------------------------
// Lexer
// -------------------------------------------------------------------------

type lexer struct {
	src    string
	pos    int
	tokens []Token
}

// Tokenise converts src into a slice of tokens, returning an error on
// unrecognised input.
func Tokenise(src string) ([]Token, error) {
	l := &lexer{src: src}
	if err := l.run(); err != nil {
		return nil, err
	}
	return l.tokens, nil
}

func (l *lexer) run() error {
	for l.pos < len(l.src) {
		r, size := utf8.DecodeRuneInString(l.src[l.pos:])

		// Skip whitespace.
		if unicode.IsSpace(r) {
			l.pos += size
			continue
		}

		// Skip -- line comments.
		if r == '-' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '-' {
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
			continue
		}

		start := l.pos

		switch {
		case r == '\'': // string literal
			l.pos += size
			for l.pos < len(l.src) {
				c, sz := utf8.DecodeRuneInString(l.src[l.pos:])
				l.pos += sz
				if c == '\'' {
					// Doubled quote = escaped quote.
					if l.pos < len(l.src) && l.src[l.pos] == '\'' {
						l.pos++
						continue
					}
					break
				}
			}
			// Store content without surrounding quotes, unescape ''.
			inner := l.src[start+1 : l.pos-1]
			inner = strings.ReplaceAll(inner, "''", "'")
			l.tokens = append(l.tokens, Token{tokStrLit, inner, start})

		case unicode.IsDigit(r): // number
			isFloat := false
			for l.pos < len(l.src) {
				c, sz := utf8.DecodeRuneInString(l.src[l.pos:])
				if c == '.' {
					isFloat = true
					l.pos += sz
					continue
				}
				if !unicode.IsDigit(c) {
					break
				}
				l.pos += sz
			}
			kind := tokIntLit
			if isFloat {
				kind = tokFloatLit
			}
			l.tokens = append(l.tokens, Token{kind, l.src[start:l.pos], start})

		case unicode.IsLetter(r) || r == '_': // identifier or keyword
			for l.pos < len(l.src) {
				c, sz := utf8.DecodeRuneInString(l.src[l.pos:])
				if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' {
					break
				}
				l.pos += sz
			}
			word := l.src[start:l.pos]
			upper := strings.ToUpper(word)
			if kind, ok := keywords[upper]; ok {
				l.tokens = append(l.tokens, Token{kind, word, start})
			} else {
				l.tokens = append(l.tokens, Token{tokIdent, word, start})
			}

		case r == '(':
			l.pos += size
			l.tokens = append(l.tokens, Token{tokLParen, "(", start})
		case r == ')':
			l.pos += size
			l.tokens = append(l.tokens, Token{tokRParen, ")", start})
		case r == ',':
			l.pos += size
			l.tokens = append(l.tokens, Token{tokComma, ",", start})
		case r == ';':
			l.pos += size
			l.tokens = append(l.tokens, Token{tokSemicolon, ";", start})
		case r == '.':
			l.pos += size
			l.tokens = append(l.tokens, Token{tokDot, ".", start})
		case r == '*':
			l.pos += size
			l.tokens = append(l.tokens, Token{tokStar, "*", start})
		case r == '+':
			l.pos += size
			l.tokens = append(l.tokens, Token{tokPlus, "+", start})
		case r == '/':
			l.pos += size
			l.tokens = append(l.tokens, Token{tokSlash, "/", start})
		case r == '=':
			l.pos += size
			l.tokens = append(l.tokens, Token{tokEq, "=", start})
		case r == '!':
			l.pos += size
			if l.pos < len(l.src) && l.src[l.pos] == '=' {
				l.pos++
				l.tokens = append(l.tokens, Token{tokNeq, "!=", start})
			} else {
				return fmt.Errorf("lexer: unexpected '!' at pos %d", start)
			}
		case r == '<':
			l.pos += size
			if l.pos < len(l.src) && l.src[l.pos] == '=' {
				l.pos++
				l.tokens = append(l.tokens, Token{tokLte, "<=", start})
			} else if l.pos < len(l.src) && l.src[l.pos] == '>' {
				l.pos++
				l.tokens = append(l.tokens, Token{tokNeq, "<>", start})
			} else {
				l.tokens = append(l.tokens, Token{tokLt, "<", start})
			}
		case r == '>':
			l.pos += size
			if l.pos < len(l.src) && l.src[l.pos] == '=' {
				l.pos++
				l.tokens = append(l.tokens, Token{tokGte, ">=", start})
			} else {
				l.tokens = append(l.tokens, Token{tokGt, ">", start})
			}
		case r == '-':
			l.pos += size
			l.tokens = append(l.tokens, Token{tokMinus, "-", start})
		default:
			return fmt.Errorf("lexer: unexpected character %q at pos %d", r, start)
		}
	}
	l.tokens = append(l.tokens, Token{tokEOF, "", l.pos})
	return nil
}

// -------------------------------------------------------------------------
// Token stream helper — used by parser
// -------------------------------------------------------------------------

type tokenStream struct {
	tokens []Token
	pos    int
}

func newStream(tokens []Token) *tokenStream {
	return &tokenStream{tokens: tokens}
}

func (s *tokenStream) peek() Token {
	if s.pos >= len(s.tokens) {
		return Token{Kind: tokEOF}
	}
	return s.tokens[s.pos]
}

func (s *tokenStream) peekKind() TokenKind {
	return s.peek().Kind
}

func (s *tokenStream) next() Token {
	t := s.peek()
	if s.pos < len(s.tokens) {
		s.pos++
	}
	return t
}

func (s *tokenStream) eat(kind TokenKind) (Token, error) {
	t := s.next()
	if t.Kind != kind {
		return t, fmt.Errorf("parser: expected token %d but got %q at pos %d", kind, t.Text, t.Pos)
	}
	return t, nil
}

func (s *tokenStream) eatIdent() (string, error) {
	t := s.next()
	// Accept plain identifiers AND keywords that can serve as names (aliases, etc.)
	switch t.Kind {
	case tokIdent,
		tokCount, tokSum, tokMin, tokMax, tokAvg,
		tokInt, tokFloat, tokText, tokBool,
		tokAsc, tokDesc:
		return t.Text, nil
	}
	return "", fmt.Errorf("parser: expected identifier but got %q at pos %d", t.Text, t.Pos)
}

func (s *tokenStream) check(kind TokenKind) bool {
	return s.peekKind() == kind
}

func (s *tokenStream) match(kinds ...TokenKind) bool {
	for _, k := range kinds {
		if s.peekKind() == k {
			s.next()
			return true
		}
	}
	return false
}

// ident returns true if the next token is an identifier or unquoted keyword
// that can be used as a column/table name.
func isIdentOrKeyword(k TokenKind) bool {
	return k == tokIdent || k == tokCount || k == tokSum || k == tokMin ||
		k == tokMax || k == tokAvg
}

// -------------------------------------------------------------------------
// Number parsing helpers
// -------------------------------------------------------------------------

func parseIntLit(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

func parseFloatLit(s string) (float64, error) {
	return strconv.ParseFloat(s, 64)
}
