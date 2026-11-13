package jql

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Dialect selects which SQL flavor the emitters produce. The translator front-end
// (lexer/parser/resolver) is dialect-agnostic; only the leaf spellings emitted by
// emit_sqlite.go and emit_duckdb.go differ (design §16.3).
type Dialect int

const (
	// SQLite targets the embedded SQLite engine used by the `jai query` CLI
	// (no DuckDB dependency). The issues table is referenced unqualified.
	SQLite Dialect = iota
	// DuckDB targets the dashboard's analytical engine, where the SQLite file is
	// ATTACHed under the `jai` alias, so the issues table is `jai.issues`.
	DuckDB
)

// String renders the dialect name for diagnostics.
func (d Dialect) String() string {
	switch d {
	case SQLite:
		return "sqlite"
	case DuckDB:
		return "duckdb"
	default:
		return fmt.Sprintf("Dialect(%d)", int(d))
	}
}

// Options configures a single [Translate] call. It carries everything both
// consumers (the DuckDB dashboard and the SQLite CLI) need to turn a JQL string
// into a parameter-safe local SQL query. Everything the resolver depends on
// (FieldMap, IDResolver, CurrentUser) is injected here so the package never
// imports internal/db or internal/metrics.
type Options struct {
	// Dialect selects the SQL flavor to emit.
	Dialect Dialect
	// FieldMap augments/overrides the built-in JQL field → column alias table.
	FieldMap FieldMap
	// IDResolver resolves entity ids (team/assignee GUIDs) to display names; nil
	// means entity ids cannot be resolved and are rejected by the resolver.
	IDResolver IDResolver
	// CurrentUser backs the currentUser() function; empty rejects it.
	CurrentUser string
	// Now is the wall clock used to resolve the now()/startOfDay/Week/Month()
	// anchors to concrete timestamps. Injected for deterministic tests; the zero
	// value falls back to time.Now(). (Bare relative dates such as -90d compile
	// to engine-side date math and do not consult Now.)
	Now time.Time
	// SelectColumns overrides the projected columns. When empty, the default
	// issue-list projection is used, augmented with any columns referenced by the
	// WHERE predicates or ORDER BY so the emitted SQL is self-consistent.
	SelectColumns []string
}

// Result is the output of [Translate]: the compiled SQL statement and the
// ordered list of projected columns (so callers can label result rows without
// re-parsing the SELECT).
type Result struct {
	// SQL is the full `SELECT … FROM … [WHERE …] [ORDER BY …]` statement.
	SQL string
	// Columns is the ordered projection, matching the SELECT list.
	Columns []string
}

// defaultColumns is the sensible issue-list projection used when the caller does
// not supply [Options.SelectColumns] (design §8.1 / §16.4).
var defaultColumns = []string{"key", "type", "priority", "status", "team", "created", "summary"}

// Translate runs the full pipeline Parse → Resolve → emit, producing a
// parameter-safe local SQL query for the requested [Dialect]. Every literal is
// emitted as escaped data (single quotes doubled) and every column comes from the
// resolved FieldMap whitelist, never from raw user text (design §8.8, §10.3).
func Translate(jql string, opts Options) (Result, error) {
	q, err := Parse(jql)
	if err != nil {
		return Result{}, err
	}
	rq, err := Resolve(q, ResolveOptions{
		Fields:      opts.FieldMap,
		CurrentUser: opts.CurrentUser,
		ResolveID:   opts.IDResolver,
	})
	if err != nil {
		return Result{}, err
	}
	return emit(rq, opts)
}

// emitter supplies the dialect-specific leaf spellings consumed by the shared
// WHERE compiler. SQLite and DuckDB differ only in these primitives (design
// §16.3); everything else (boolean tree, SELECT/ORDER BY assembly, literal
// escaping, EMPTY/`!=` handling) is dialect-agnostic.
type emitter interface {
	// tableName returns the issues table reference (dialect-qualified).
	tableName() string
	// numericCompare renders an ordered numeric comparison against a cast column.
	numericCompare(col, op, num string) string
	// dateCompareRelative renders a timestamp comparison against engine-side
	// relative-date math.
	dateCompareRelative(col, op string, ds *DateSpec) string
	// dateCompareLiteral renders a timestamp comparison against a concrete
	// timestamp literal.
	dateCompareLiteral(col, op, ts string) string
	// textContains renders a case-insensitive substring match (negated when set).
	textContains(col, pat string, negate bool) string
	// arrayContains renders a JSON-array membership test.
	arrayContains(col, val string) string
}

// emit turns a [*ResolvedQuery] into dialect-specific SQL. It is the shared
// assembly layer; the per-dialect leaf spellings live in the [emitter]
// implementations.
func emit(rq *ResolvedQuery, opts Options) (Result, error) {
	e := emitterFor(opts.Dialect)
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	cp := &compiler{e: e, now: now}

	cols := selectColumns(opts, rq)

	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(strings.Join(cols, ", "))
	b.WriteString(" FROM ")
	b.WriteString(e.tableName())

	if rq.Where != nil {
		where, err := cp.expr(rq.Where)
		if err != nil {
			return Result{}, err
		}
		b.WriteString(" WHERE ")
		b.WriteString(where)
	}

	if len(rq.OrderBy) > 0 {
		b.WriteString(" ORDER BY ")
		b.WriteString(orderBy(rq.OrderBy))
	}

	return Result{SQL: b.String(), Columns: cols}, nil
}

// emitterFor returns the leaf-spelling emitter for a dialect.
func emitterFor(d Dialect) emitter {
	if d == DuckDB {
		return duckdbEmitter{}
	}
	return sqliteEmitter{}
}

// selectColumns computes the projection: the caller override verbatim, otherwise
// the default issue-list projection augmented (first-seen order) with any columns
// referenced by the predicates or ORDER BY so the statement is self-consistent.
func selectColumns(opts Options, rq *ResolvedQuery) []string {
	if len(opts.SelectColumns) > 0 {
		return append([]string(nil), opts.SelectColumns...)
	}
	cols := append([]string(nil), defaultColumns...)
	seen := make(map[string]bool, len(cols))
	for _, c := range cols {
		seen[c] = true
	}
	for _, c := range referencedColumns(rq) {
		if !seen[c] {
			cols = append(cols, c)
			seen[c] = true
		}
	}
	return cols
}

// referencedColumns returns, in first-seen order, every physical column named by
// the WHERE tree or the ORDER BY tail.
func referencedColumns(rq *ResolvedQuery) []string {
	var out []string
	seen := map[string]bool{}
	add := func(c string) {
		if c != "" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	var walk func(e ResolvedExpr)
	walk = func(e ResolvedExpr) {
		switch n := e.(type) {
		case *ResolvedClause:
			add(n.Column)
		case *ResolvedNot:
			walk(n.X)
		case *ResolvedBinary:
			walk(n.Left)
			walk(n.Right)
		}
	}
	walk(rq.Where)
	for _, o := range rq.OrderBy {
		add(o.Column)
	}
	return out
}

// orderBy renders the resolved ORDER BY terms. ASC (the default) is omitted.
func orderBy(terms []ResolvedOrder) string {
	parts := make([]string, len(terms))
	for i, t := range terms {
		if t.Desc {
			parts[i] = t.Column + " DESC"
		} else {
			parts[i] = t.Column
		}
	}
	return strings.Join(parts, ", ")
}

// compiler walks the resolved boolean tree, delegating the dialect-specific leaf
// spellings to its [emitter].
type compiler struct {
	e   emitter
	now time.Time
}

// expr compiles a resolved boolean node into a SQL fragment.
func (cp *compiler) expr(e ResolvedExpr) (string, error) {
	switch n := e.(type) {
	case *ResolvedClause:
		return cp.clause(n)
	case *ResolvedNot:
		s, err := cp.expr(n.X)
		if err != nil {
			return "", err
		}
		return "NOT (" + s + ")", nil
	case *ResolvedBinary:
		left, err := cp.child(n.Left, n.Op)
		if err != nil {
			return "", err
		}
		right, err := cp.child(n.Right, n.Op)
		if err != nil {
			return "", err
		}
		return left + " " + n.Op.String() + " " + right, nil
	default:
		return "", fmt.Errorf("jql: unexpected resolved node %T", e)
	}
}

// child compiles a sub-expression, parenthesizing a binary node only when its
// connective differs from the parent's (AND/OR precedence). Equal connectives are
// associative and need no parens; clauses and NOT nodes are already self-contained.
func (cp *compiler) child(e ResolvedExpr, parent BoolOp) (string, error) {
	s, err := cp.expr(e)
	if err != nil {
		return "", err
	}
	if b, ok := e.(*ResolvedBinary); ok && b.Op != parent {
		return "(" + s + ")", nil
	}
	return s, nil
}

// clause compiles a single resolved comparison into a SQL fragment, dispatching
// on the operator and the column's storage kind (design §8.3).
func (cp *compiler) clause(c *ResolvedClause) (string, error) {
	col := c.Column
	switch c.Op {
	case OpIsEmpty:
		return emptyExpr(col), nil
	case OpIsNotEmpty:
		return "NOT " + emptyExpr(col), nil
	case OpContains:
		return cp.e.textContains(col, c.Values[0].Str, false), nil
	case OpNotContains:
		return cp.e.textContains(col, c.Values[0].Str, true), nil
	case OpGt, OpGte, OpLt, OpLte:
		return cp.compare(c)
	case OpEq:
		if c.Kind == KindArray {
			return cp.e.arrayContains(col, c.Values[0].Str), nil
		}
		return col + " = " + scalarLiteral(c.Values[0]), nil
	case OpNeq:
		if c.Kind == KindArray {
			return "NOT " + cp.e.arrayContains(col, c.Values[0].Str), nil
		}
		// JQL `!=` excludes EMPTY, so an empty/NULL column never matches.
		return "COALESCE(" + col + ",'') NOT IN ('', " + scalarLiteral(c.Values[0]) + ")", nil
	case OpIn:
		if c.Kind == KindArray {
			return cp.arrayIn(col, c.Values, false), nil
		}
		return col + " IN (" + literalList(c.Values) + ")", nil
	case OpNotIn:
		if c.Kind == KindArray {
			return cp.arrayIn(col, c.Values, true), nil
		}
		return "COALESCE(" + col + ",'') NOT IN ('', " + literalList(c.Values) + ")", nil
	default:
		return "", fmt.Errorf("jql: unsupported operator %s on %q", c.Op, c.Field)
	}
}

// compare compiles the ordered comparison operators (> >= < <=) for date and
// numeric columns.
func (cp *compiler) compare(c *ResolvedClause) (string, error) {
	op := c.Op.String()
	v := c.Values[0]
	col := c.Column

	if c.Kind == KindDate {
		if v.Kind != RVDate {
			return "", fmt.Errorf("jql: non-date value for date field %q", c.Field)
		}
		if v.Date != nil && v.Date.Kind == DateRelative {
			return cp.e.dateCompareRelative(col, op, v.Date), nil
		}
		var ts string
		if v.Date != nil {
			ts = cp.anchorTime(v.Date).Format("2006-01-02 15:04:05")
		} else {
			// nil Date => Str is an absolute date literal.
			ts = v.Str
		}
		return cp.e.dateCompareLiteral(col, op, ts), nil
	}

	// Numeric comparison; the value is validated numeric by the resolver.
	if v.Kind != RVNumber {
		return "", fmt.Errorf("jql: non-numeric value for comparison on %q", c.Field)
	}
	return cp.e.numericCompare(col, op, formatNum(v.Num)), nil
}

// arrayIn compiles IN / NOT IN over a JSON-array column as an OR of containment
// tests.
func (cp *compiler) arrayIn(col string, values []ResolvedValue, negate bool) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = cp.e.arrayContains(col, v.Str)
	}
	expr := "(" + strings.Join(parts, " OR ") + ")"
	if negate {
		return "NOT " + expr
	}
	return expr
}

// anchorTime resolves a now()/startOf* date anchor to a concrete timestamp using
// the injected clock, so emitted SQL is deterministic.
func (cp *compiler) anchorTime(ds *DateSpec) time.Time {
	switch ds.Kind {
	case DateStartOfDay:
		return startOfDay(cp.now)
	case DateStartOfWeek:
		return startOfWeek(cp.now)
	case DateStartOfMonth:
		return startOfMonth(cp.now)
	default: // DateNow
		return cp.now
	}
}

// emptyExpr is the dialect-agnostic EMPTY/NULL test (design §8.3).
func emptyExpr(col string) string {
	return "(" + col + " IS NULL OR " + col + " = '')"
}

// scalarLiteral renders a single resolved value as a SQL literal: quoted/escaped
// text, or a bare numeric literal for numbers.
func scalarLiteral(v ResolvedValue) string {
	if v.Kind == RVNumber {
		return formatNum(v.Num)
	}
	return quote(v.Str)
}

// literalList renders a comma-joined list of scalar literals for IN / NOT IN.
func literalList(values []ResolvedValue) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = scalarLiteral(v)
	}
	return strings.Join(parts, ",")
}

// quote renders s as a single-quoted SQL string literal with embedded quotes
// doubled. This is the sole string-literal escaper; all literals are data, never
// identifiers (design §8.8, §10.3).
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// formatNum renders a float64 as its shortest exact decimal literal.
func formatNum(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// intervalUnit maps a relative-date unit to its DuckDB and SQLite spellings,
// converting weeks to days (SQLite has no week modifier, and days keep both
// dialects semantically identical). The amount is adjusted accordingly.
func intervalUnit(unit string, amount int) (amt int, duck, sqlite string) {
	switch unit {
	case "s":
		return amount, "SECOND", "second"
	case "m":
		return amount, "MINUTE", "minute"
	case "h":
		return amount, "HOUR", "hour"
	case "d":
		return amount, "DAY", "day"
	case "w":
		return amount * 7, "DAY", "day"
	case "M":
		return amount, "MONTH", "month"
	case "y":
		return amount, "YEAR", "year"
	default:
		return amount, "DAY", "day"
	}
}

// intervalSign renders the arithmetic sign for a relative-date spec.
func intervalSign(ds *DateSpec) string {
	if ds.Sign < 0 {
		return "-"
	}
	return "+"
}

// startOfDay truncates t to midnight in its own location.
func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// startOfWeek returns midnight on the Monday of t's week.
func startOfWeek(t time.Time) time.Time {
	d := startOfDay(t)
	// Go's Sunday==0; shift so Monday is the first day of the week.
	offset := (int(d.Weekday()) + 6) % 7
	return d.AddDate(0, 0, -offset)
}

// startOfMonth returns midnight on the first day of t's month.
func startOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}
