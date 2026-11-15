// Package jql implements a dialect-agnostic translator front-end for the subset
// of Jira Query Language (JQL) that jai can evaluate locally against the
// denormalized `issues` table.
//
// The package is intentionally self-contained: it MUST NOT import internal/db
// or internal/metrics. Everything it needs from the rest of jai (the field →
// column map, the current user, entity id → display-name lookups) is injected
// through [ResolveOptions]. This keeps the parser/resolver reusable by both the
// CLI (SQLite) and dashboard (DuckDB) code paths, with the dialect-specific SQL
// emission living entirely in the dependent emitter package.
//
// The pipeline is:
//
//	JQL string → Parse → *Query (raw AST)
//	           → Resolve → *ResolvedQuery (field/function/id resolved IR)
//	           → (emitter package) → SQL
//
// This file defines the raw AST produced by [Parse]. The resolved IR consumed
// by emitters lives in resolver.go.
package jql

import "fmt"

// Query is a parsed, but not yet resolved, JQL query. It is the output of
// [Parse]. Field names, functions and entity ids are still in their original
// textual form; call [Resolve] to turn them into the typed IR the emitters
// consume.
type Query struct {
	// Where is the boolean expression tree, or nil when the query is a bare
	// `ORDER BY` with no filter clauses.
	Where Expr
	// OrderBy holds the optional `ORDER BY` tail, in declaration order.
	OrderBy []OrderField
	// Raw is the original query text, retained for diagnostics.
	Raw string
}

// OrderField is a single `ORDER BY` term.
type OrderField struct {
	// Field is the JQL field name as written (unresolved).
	Field string
	// Desc is true for `DESC`; false for `ASC` or an omitted direction.
	Desc bool
	// Offset is the byte offset of the field token in the source, for errors.
	Offset int
}

// BoolOp identifies a binary boolean connective.
type BoolOp int

const (
	// OpAnd is the `AND` connective.
	OpAnd BoolOp = iota
	// OpOr is the `OR` connective.
	OpOr
)

// String renders the connective as its JQL keyword.
func (b BoolOp) String() string {
	switch b {
	case OpAnd:
		return "AND"
	case OpOr:
		return "OR"
	default:
		return fmt.Sprintf("BoolOp(%d)", int(b))
	}
}

// Expr is a node in the raw boolean tree: a [*Clause], [*BinaryExpr] or
// [*NotExpr].
type Expr interface {
	exprNode()
}

// BinaryExpr joins two sub-expressions with AND or OR.
type BinaryExpr struct {
	Op    BoolOp
	Left  Expr
	Right Expr
}

func (*BinaryExpr) exprNode() {}

// NotExpr negates a sub-expression (`NOT <expr>`).
type NotExpr struct {
	X Expr
	// Offset is the byte offset of the `NOT` keyword.
	Offset int
}

func (*NotExpr) exprNode() {}

// Operator identifies the comparison used in a [Clause].
type Operator int

const (
	// OpEq is `=`.
	OpEq Operator = iota
	// OpNeq is `!=`.
	OpNeq
	// OpGt is `>`.
	OpGt
	// OpGte is `>=`.
	OpGte
	// OpLt is `<`.
	OpLt
	// OpLte is `<=`.
	OpLte
	// OpContains is `~` (substring / text match).
	OpContains
	// OpNotContains is `!~`.
	OpNotContains
	// OpIn is `IN`.
	OpIn
	// OpNotIn is `NOT IN`.
	OpNotIn
	// OpIsEmpty is `IS EMPTY` / `IS NULL`.
	OpIsEmpty
	// OpIsNotEmpty is `IS NOT EMPTY` / `IS NOT NULL`.
	OpIsNotEmpty

	// The history operators below are recognized by the parser only so they
	// can be rejected with a clear message by the resolver (v1 non-goal, see
	// design §8.7). They never reach an emitter.

	// OpWas covers `WAS` / `WAS IN` / `WAS NOT` / `WAS NOT IN`.
	OpWas
	// OpChanged covers `CHANGED` (with any FROM/TO/BY/DURING/BEFORE/AFTER/ON
	// qualifiers).
	OpChanged
)

// String renders the operator as its canonical JQL spelling.
func (o Operator) String() string {
	switch o {
	case OpEq:
		return "="
	case OpNeq:
		return "!="
	case OpGt:
		return ">"
	case OpGte:
		return ">="
	case OpLt:
		return "<"
	case OpLte:
		return "<="
	case OpContains:
		return "~"
	case OpNotContains:
		return "!~"
	case OpIn:
		return "IN"
	case OpNotIn:
		return "NOT IN"
	case OpIsEmpty:
		return "IS EMPTY"
	case OpIsNotEmpty:
		return "IS NOT EMPTY"
	case OpWas:
		return "WAS"
	case OpChanged:
		return "CHANGED"
	default:
		return fmt.Sprintf("Operator(%d)", int(o))
	}
}

// isHistory reports whether the operator is a v1-unsupported history operator.
func (o Operator) isHistory() bool {
	return o == OpWas || o == OpChanged
}

// Clause is a single `field OP value` comparison.
type Clause struct {
	// Field is the JQL field name exactly as written (unquoted).
	Field string
	// FieldOffset is the byte offset of the field token.
	FieldOffset int
	// Op is the comparison operator.
	Op Operator
	// OpOffset is the byte offset of the operator token.
	OpOffset int
	// Value is the right-hand side: nil for the empty operators
	// ([OpIsEmpty] / [OpIsNotEmpty]) and for the history operators.
	Value Value
}

func (*Clause) exprNode() {}

// Value is the right-hand side of a [Clause]: a [*StringValue], [*NumberValue],
// [*ListValue], [*RelativeDate] or [*FuncCall].
type Value interface {
	valueNode()
	// pos returns the byte offset of the value for diagnostics.
	pos() int
}

// StringValue is a bare word (`Bug`, `ROX`, a GUID) or a quoted string
// (`"To Do"`). Quoted records whether the source used quotes, which the
// resolver does not care about but is kept for faithful round-tripping.
type StringValue struct {
	Val    string
	Quoted bool
	Offset int
}

func (*StringValue) valueNode() {}
func (v *StringValue) pos() int { return v.Offset }

// NumberValue is a numeric literal such as `10000` or `3.5`.
type NumberValue struct {
	Val    float64
	Raw    string
	Offset int
}

func (*NumberValue) valueNode() {}
func (v *NumberValue) pos() int { return v.Offset }

// ListValue is a parenthesized list, e.g. `(Critical, Blocker)`.
type ListValue struct {
	Items  []Value
	Offset int
}

func (*ListValue) valueNode() {}
func (v *ListValue) pos() int { return v.Offset }

// RelativeDate is a relative-date literal such as `-90d`, `-2w`, `-3M`, `-1y`.
// Sign is -1 or +1, Amount is the magnitude, and Unit is one of
// s, m, h, d, w, M, y (minutes `m` vs months `M` are case-sensitive, matching
// Jira). The resolver turns this into a typed date node; no SQL is produced
// here.
type RelativeDate struct {
	Sign   int
	Amount int
	Unit   string
	Raw    string
	Offset int
}

func (*RelativeDate) valueNode() {}
func (v *RelativeDate) pos() int { return v.Offset }

// FuncCall is a function invocation such as `now()`, `startOfDay()`,
// `currentUser()`, `standardIssueTypes()` or `membersOf("group")`.
type FuncCall struct {
	Name   string
	Args   []Value
	Offset int
}

func (*FuncCall) valueNode() {}
func (v *FuncCall) pos() int { return v.Offset }
