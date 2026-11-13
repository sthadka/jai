package jql

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseError describes a syntax error with the byte offset at which it was
// detected, so callers can surface the precise location (design §9.2).
type ParseError struct {
	Offset int
	Msg    string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("offset %d: %s", e.Offset, e.Msg)
}

// Parse lexes and parses a JQL string into a raw [*Query]. It performs no field,
// function or entity resolution; call [Resolve] for that. Operator precedence is
// NOT > AND > OR, with parentheses overriding. History operators
// (WAS/CHANGED/…) are recognized syntactically so that [Resolve] can reject them
// with a clear message rather than silently dropping them.
func Parse(input string) (*Query, error) {
	toks, err := lex(input)
	if err != nil {
		if le, ok := err.(*lexError); ok {
			return nil, &ParseError{Offset: le.Offset, Msg: le.Msg}
		}
		return nil, err
	}
	p := &parser{toks: toks, src: input}
	q, err := p.parseQuery()
	if err != nil {
		return nil, err
	}
	return q, nil
}

// parser is a single-pass recursive-descent parser over the token slice.
type parser struct {
	toks []token
	pos  int
	src  string
}

// cur returns the current token.
func (p *parser) cur() token { return p.toks[p.pos] }

// peek returns the token n positions ahead (clamped to the trailing tEOF).
func (p *parser) peek(n int) token {
	i := p.pos + n
	if i >= len(p.toks) {
		return p.toks[len(p.toks)-1]
	}
	return p.toks[i]
}

// advance consumes and returns the current token.
func (p *parser) advance() token {
	t := p.toks[p.pos]
	if p.pos < len(p.toks)-1 {
		p.pos++
	}
	return t
}

// errf builds a ParseError anchored at the current token.
func (p *parser) errf(format string, args ...any) error {
	return &ParseError{Offset: p.cur().off, Msg: fmt.Sprintf(format, args...)}
}

// parseQuery parses an optional boolean expression followed by an optional
// `ORDER BY` tail, then requires end-of-input.
func (p *parser) parseQuery() (*Query, error) {
	q := &Query{Raw: p.src}

	if p.cur().kind == tEOF {
		return nil, p.errf("empty query")
	}

	if p.cur().kind != tOrder {
		where, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		q.Where = where
	}

	if p.cur().kind == tOrder {
		order, err := p.parseOrderBy()
		if err != nil {
			return nil, err
		}
		q.OrderBy = order
	}

	if p.cur().kind != tEOF {
		return nil, p.errf("unexpected token %q after query", p.cur().text)
	}
	return q, nil
}

// parseOr handles the lowest-precedence connective.
func (p *parser) parseOr() (Expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.cur().kind == tOr {
		p.advance()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Op: OpOr, Left: left, Right: right}
	}
	return left, nil
}

// parseAnd handles AND, which binds tighter than OR.
func (p *parser) parseAnd() (Expr, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.cur().kind == tAnd {
		p.advance()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Op: OpAnd, Left: left, Right: right}
	}
	return left, nil
}

// parseNot handles the unary NOT, which binds tighter than AND.
func (p *parser) parseNot() (Expr, error) {
	if p.cur().kind == tNot {
		off := p.cur().off
		p.advance()
		x, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &NotExpr{X: x, Offset: off}, nil
	}
	return p.parsePrimary()
}

// parsePrimary handles a parenthesized sub-expression or a single clause.
func (p *parser) parsePrimary() (Expr, error) {
	if p.cur().kind == tLParen {
		p.advance()
		e, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.cur().kind != tRParen {
			return nil, p.errf("expected ')'")
		}
		p.advance()
		return e, nil
	}
	return p.parseClause()
}

// parseClause parses a single `field OP value` comparison.
func (p *parser) parseClause() (Expr, error) {
	ft := p.cur()
	if ft.kind != tIdent && ft.kind != tString {
		return nil, p.errf("expected a field name, got %q", ft.text)
	}
	p.advance()
	c := &Clause{Field: ft.text, FieldOffset: ft.off}

	op := p.cur()
	c.OpOffset = op.off
	switch op.kind {
	case tEq, tNeq, tGt, tGte, tLt, tLte, tContains, tNContains:
		p.advance()
		c.Op = scalarOp(op.kind)
		v, err := p.parseScalarValue()
		if err != nil {
			return nil, err
		}
		c.Value = v
	case tIn:
		p.advance()
		c.Op = OpIn
		v, err := p.parseInValue()
		if err != nil {
			return nil, err
		}
		c.Value = v
	case tNot:
		p.advance()
		if p.cur().kind != tIn {
			return nil, p.errf("expected IN after NOT, got %q", p.cur().text)
		}
		p.advance()
		c.Op = OpNotIn
		v, err := p.parseInValue()
		if err != nil {
			return nil, err
		}
		c.Value = v
	case tIs:
		p.advance()
		o, err := p.parseIsTail()
		if err != nil {
			return nil, err
		}
		c.Op = o
	case tWas:
		p.advance()
		c.Op = OpWas
		p.skipHistoryTail()
	case tChanged:
		p.advance()
		c.Op = OpChanged
		p.skipHistoryTail()
	default:
		return nil, p.errf("expected an operator after %q, got %q", ft.text, op.text)
	}
	return c, nil
}

// scalarOp maps a comparison token kind to its [Operator].
func scalarOp(k tokenKind) Operator {
	switch k {
	case tEq:
		return OpEq
	case tNeq:
		return OpNeq
	case tGt:
		return OpGt
	case tGte:
		return OpGte
	case tLt:
		return OpLt
	case tLte:
		return OpLte
	case tContains:
		return OpContains
	case tNContains:
		return OpNotContains
	default:
		panic("scalarOp: not a scalar operator")
	}
}

// parseIsTail parses the tail of an `IS` clause: `EMPTY`/`NULL` or
// `NOT EMPTY`/`NOT NULL`.
func (p *parser) parseIsTail() (Operator, error) {
	switch p.cur().kind {
	case tEmpty, tNull:
		p.advance()
		return OpIsEmpty, nil
	case tNot:
		p.advance()
		if p.cur().kind != tEmpty && p.cur().kind != tNull {
			return 0, p.errf("expected EMPTY or NULL after IS NOT, got %q", p.cur().text)
		}
		p.advance()
		return OpIsNotEmpty, nil
	default:
		return 0, p.errf("expected EMPTY, NULL or NOT after IS, got %q", p.cur().text)
	}
}

// skipHistoryTail consumes the remainder of a history clause (operands and any
// FROM/TO/BY/DURING/BEFORE/AFTER/ON qualifiers) up to the next clause boundary,
// balancing parentheses. The tokens are discarded because [Resolve] rejects the
// whole clause; the parser only needs to recognize the shape so parsing of the
// surrounding query continues correctly.
func (p *parser) skipHistoryTail() {
	depth := 0
	for {
		t := p.cur()
		if t.kind == tEOF {
			return
		}
		if depth == 0 {
			switch t.kind {
			case tAnd, tOr, tRParen, tOrder:
				return
			}
		}
		switch t.kind {
		case tLParen:
			depth++
		case tRParen:
			depth--
		}
		p.advance()
	}
}

// parseScalarValue parses the right-hand side of a scalar operator: a string,
// number, relative date, function call or bare word.
func (p *parser) parseScalarValue() (Value, error) {
	t := p.cur()
	switch t.kind {
	case tString:
		p.advance()
		return &StringValue{Val: t.text, Quoted: true, Offset: t.off}, nil
	case tNumber:
		p.advance()
		f, err := strconv.ParseFloat(t.text, 64)
		if err != nil {
			return nil, &ParseError{Offset: t.off, Msg: fmt.Sprintf("invalid number %q", t.text)}
		}
		return &NumberValue{Val: f, Raw: t.text, Offset: t.off}, nil
	case tRelDate:
		p.advance()
		return parseRelativeDate(t)
	case tIdent:
		if p.peek(1).kind == tLParen {
			return p.parseFuncCall()
		}
		p.advance()
		return &StringValue{Val: t.text, Quoted: false, Offset: t.off}, nil
	default:
		return nil, p.errf("expected a value, got %q", t.text)
	}
}

// parseInValue parses the right-hand side of IN / NOT IN: either a parenthesized
// list `(a, b, c)` or a function call that yields a list (e.g.
// `standardIssueTypes()`).
func (p *parser) parseInValue() (Value, error) {
	switch {
	case p.cur().kind == tLParen:
		off := p.cur().off
		p.advance()
		list := &ListValue{Offset: off}
		if p.cur().kind == tRParen {
			return nil, p.errf("empty IN list")
		}
		for {
			v, err := p.parseScalarValue()
			if err != nil {
				return nil, err
			}
			list.Items = append(list.Items, v)
			if p.cur().kind == tComma {
				p.advance()
				continue
			}
			break
		}
		if p.cur().kind != tRParen {
			return nil, p.errf("expected ',' or ')' in IN list, got %q", p.cur().text)
		}
		p.advance()
		return list, nil
	case p.cur().kind == tIdent && p.peek(1).kind == tLParen:
		return p.parseFuncCall()
	default:
		return nil, p.errf("expected '(' or a function after IN, got %q", p.cur().text)
	}
}

// parseFuncCall parses `name(args…)`; the current token must be the function
// name identifier and be followed by `(`.
func (p *parser) parseFuncCall() (Value, error) {
	name := p.advance() // identifier
	p.advance()         // '('
	fc := &FuncCall{Name: name.text, Offset: name.off}
	if p.cur().kind == tRParen {
		p.advance()
		return fc, nil
	}
	for {
		v, err := p.parseScalarValue()
		if err != nil {
			return nil, err
		}
		fc.Args = append(fc.Args, v)
		if p.cur().kind == tComma {
			p.advance()
			continue
		}
		break
	}
	if p.cur().kind != tRParen {
		return nil, p.errf("expected ',' or ')' in function arguments, got %q", p.cur().text)
	}
	p.advance()
	return fc, nil
}

// parseRelativeDate converts a tRelDate token into a [*RelativeDate] node.
func parseRelativeDate(t token) (Value, error) {
	text := t.text
	sign := 1
	i := 0
	switch text[0] {
	case '+':
		i = 1
	case '-':
		sign = -1
		i = 1
	}
	digitsStart := i
	for i < len(text) && text[i] >= '0' && text[i] <= '9' {
		i++
	}
	amount, err := strconv.Atoi(text[digitsStart:i])
	if err != nil {
		return nil, &ParseError{Offset: t.off, Msg: fmt.Sprintf("invalid relative date %q", text)}
	}
	unit := text[i:]
	return &RelativeDate{Sign: sign, Amount: amount, Unit: unit, Raw: text, Offset: t.off}, nil
}

// parseOrderBy parses the `ORDER BY field [ASC|DESC] [, …]` tail. The current
// token must be `ORDER`.
func (p *parser) parseOrderBy() ([]OrderField, error) {
	p.advance() // ORDER
	if p.cur().kind != tBy {
		return nil, p.errf("expected BY after ORDER, got %q", p.cur().text)
	}
	p.advance() // BY
	var fields []OrderField
	for {
		ft := p.cur()
		if ft.kind != tIdent && ft.kind != tString {
			return nil, p.errf("expected a field name in ORDER BY, got %q", ft.text)
		}
		p.advance()
		of := OrderField{Field: ft.text, Offset: ft.off}
		switch p.cur().kind {
		case tAsc:
			p.advance()
		case tDesc:
			of.Desc = true
			p.advance()
		}
		fields = append(fields, of)
		if p.cur().kind == tComma {
			p.advance()
			continue
		}
		break
	}
	return fields, nil
}

// lowerName lowercases a field or function name for case-insensitive matching.
func lowerName(s string) string { return strings.ToLower(s) }
