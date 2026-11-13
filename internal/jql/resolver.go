package jql

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// FieldKind classifies how a resolved column is stored, so the (separate)
// emitter package can pick the correct SQL spelling without re-deriving type
// information: dates need a timestamp cast, numbers a numeric cast, arrays a
// JSON-containment test, and strings a direct comparison (design §8.8).
type FieldKind int

const (
	// KindString is a plain text column (the default).
	KindString FieldKind = iota
	// KindDate is a timestamp/date column (created, updated, resolved, …).
	KindDate
	// KindNumber is a numeric column (story points, custom numeric fields).
	KindNumber
	// KindArray is a JSON-array column (labels, components, …).
	KindArray
)

// String renders the kind for diagnostics.
func (k FieldKind) String() string {
	switch k {
	case KindString:
		return "string"
	case KindDate:
		return "date"
	case KindNumber:
		return "number"
	case KindArray:
		return "array"
	default:
		return fmt.Sprintf("FieldKind(%d)", int(k))
	}
}

// FieldDef describes the resolution target for a JQL field: the physical
// `issues` column, its storage kind, and whether its values may be entity ids
// (GUIDs / account ids) that need id → display-name resolution.
type FieldDef struct {
	// Column is the physical column name in the `issues` table.
	Column string
	// Kind is the storage classification used by emitters.
	Kind FieldKind
	// Entity marks fields whose values may be written as opaque ids (team,
	// assignee, …) and therefore run through the injected [IDResolver].
	Entity bool
}

// FieldMap maps JQL field names to their [FieldDef]. Lookups are
// case-insensitive; callers may supply keys in any case. This is the injected
// field metadata (sourced from jai's field_map / `jai fields`) that keeps the
// resolver free of any db dependency.
type FieldMap map[string]FieldDef

// IDResolver resolves an entity id to its display name for a given field. It
// returns (name, true, nil) on success, ("", false, nil) when the id is unknown
// locally, and a non-nil error only for lookup failures. It is injected so the
// resolver never touches storage directly (design §8.5).
type IDResolver func(field, id string) (name string, ok bool, err error)

// ResolveOptions carries everything the resolver needs from the host program.
type ResolveOptions struct {
	// Fields augments/overrides the built-in alias table. Entries here take
	// precedence over the built-ins.
	Fields FieldMap
	// CurrentUser is the display name/account used for currentUser(); empty
	// means currentUser() cannot be resolved and is rejected.
	CurrentUser string
	// ResolveID resolves entity ids to display names; nil means ids for entity
	// fields cannot be resolved and are rejected with guidance.
	ResolveID IDResolver
}

// ResolveError is a resolution failure (unknown field, unsupported function,
// unresolvable id, history operator, …). Offset points at the offending token.
type ResolveError struct {
	Offset int
	Msg    string
}

func (e *ResolveError) Error() string {
	return fmt.Sprintf("offset %d: %s", e.Offset, e.Msg)
}

// ResolvedQuery is the typed, fully-resolved IR consumed by the SQL emitters.
// Every field has been mapped to a column + kind, every function to a typed
// value or a rejection, and every entity id to a display name. No SQL text is
// produced here.
type ResolvedQuery struct {
	// Where is the resolved boolean tree, or nil when every clause resolved to
	// a no-op (e.g. a sole redundant standardIssueTypes()).
	Where ResolvedExpr
	// OrderBy holds the resolved `ORDER BY` terms.
	OrderBy []ResolvedOrder
	// Raw is the original query text.
	Raw string
}

// ResolvedOrder is a resolved `ORDER BY` term.
type ResolvedOrder struct {
	Field  string
	Column string
	Kind   FieldKind
	Desc   bool
}

// ResolvedExpr mirrors [Expr] after resolution: a [*ResolvedBinary],
// [*ResolvedNot] or [*ResolvedClause].
type ResolvedExpr interface {
	resolvedNode()
}

// ResolvedBinary is a resolved AND/OR node.
type ResolvedBinary struct {
	Op    BoolOp
	Left  ResolvedExpr
	Right ResolvedExpr
}

func (*ResolvedBinary) resolvedNode() {}

// ResolvedNot is a resolved NOT node.
type ResolvedNot struct {
	X ResolvedExpr
}

func (*ResolvedNot) resolvedNode() {}

// ResolvedClause is a resolved `column OP value(s)` comparison.
type ResolvedClause struct {
	// Field is the original JQL field name (for diagnostics).
	Field string
	// Column is the resolved physical column.
	Column string
	// Kind is the column's storage kind.
	Kind FieldKind
	// Op is the comparison operator.
	Op Operator
	// Values holds the resolved right-hand side: empty for the empty operators,
	// one element for scalar operators, and N elements for IN / NOT IN.
	Values []ResolvedValue
}

func (*ResolvedClause) resolvedNode() {}

// ResolvedValueKind tags the payload carried by a [ResolvedValue].
type ResolvedValueKind int

const (
	// RVString is a literal string (or a display name resolved from an id).
	RVString ResolvedValueKind = iota
	// RVNumber is a numeric literal.
	RVNumber
	// RVDate is a date value; see [ResolvedValue.Date].
	RVDate
)

// ResolvedValue is a single annotated right-hand-side value. The emitter does
// pure spelling from this; it never re-resolves. For RVDate, a non-nil Date
// carries relative/anchor date math while a nil Date means Str is an absolute
// date literal.
type ResolvedValue struct {
	Kind ResolvedValueKind
	// Str is the literal/display-name text for RVString, or the absolute date
	// literal for RVDate with a nil Date.
	Str string
	// Num is the value for RVNumber.
	Num float64
	// Date carries computed date math for RVDate (nil => Str is an absolute
	// literal).
	Date *DateSpec
	// Original is the source text, for diagnostics.
	Original string
	// ResolvedFromID is true when Str was produced by resolving an entity id to
	// a display name.
	ResolvedFromID bool
}

// DateSpecKind enumerates the computed date anchors.
type DateSpecKind int

const (
	// DateRelative is an offset from now, e.g. -90d (Sign/Amount/Unit set).
	DateRelative DateSpecKind = iota
	// DateNow is now().
	DateNow
	// DateStartOfDay is startOfDay().
	DateStartOfDay
	// DateStartOfWeek is startOfWeek().
	DateStartOfWeek
	// DateStartOfMonth is startOfMonth().
	DateStartOfMonth
)

// DateSpec is a storage-agnostic description of a date value. The emitter turns
// it into dialect-specific date math (SQLite datetime() vs DuckDB INTERVAL).
type DateSpec struct {
	Kind DateSpecKind
	// Sign/Amount/Unit are meaningful only for DateRelative.
	Sign   int
	Amount int
	Unit   string
}

// builtinAliases is the canonical JQL-name → column table (design §8.4). The
// injected [FieldMap] overrides any entry here.
var builtinAliases = FieldMap{
	// identity / text
	"key":            {Column: "key", Kind: KindString},
	"issuekey":       {Column: "key", Kind: KindString},
	"issue":          {Column: "key", Kind: KindString},
	"summary":        {Column: "summary", Kind: KindString},
	"description":    {Column: "description", Kind: KindString},
	"project":        {Column: "project", Kind: KindString},
	"status":         {Column: "status", Kind: KindString},
	"priority":       {Column: "priority", Kind: KindString},
	"resolution":     {Column: "resolution", Kind: KindString},
	"issuetype":      {Column: "type", Kind: KindString},
	"type":           {Column: "type", Kind: KindString},
	"statuscategory": {Column: "status_category", Kind: KindString},
	"fixversion":     {Column: "fix_version", Kind: KindString},
	"fixversions":    {Column: "fix_version", Kind: KindString},
	"parent":         {Column: "parent_key", Kind: KindString},
	"epic":           {Column: "epic_key", Kind: KindString},
	"sprint":         {Column: "sprint", Kind: KindString},

	// entity fields (values may be ids)
	"assignee": {Column: "assignee", Kind: KindString, Entity: true},
	"reporter": {Column: "reporter", Kind: KindString, Entity: true},
	"creator":  {Column: "creator", Kind: KindString, Entity: true},
	"team":     {Column: "team", Kind: KindString, Entity: true},

	// JSON-array fields
	"labels":     {Column: "labels", Kind: KindArray},
	"component":  {Column: "components", Kind: KindArray},
	"components": {Column: "components", Kind: KindArray},

	// date fields
	"created":        {Column: "created", Kind: KindDate},
	"updated":        {Column: "updated", Kind: KindDate},
	"resolved":       {Column: "resolved", Kind: KindDate},
	"resolutiondate": {Column: "resolved", Kind: KindDate},
	"due":            {Column: "due_date", Kind: KindDate},
	"duedate":        {Column: "due_date", Kind: KindDate},
}

// entityIDRe matches values that look like entity ids: Atlassian/Jira GUIDs
// (with an optional numeric suffix as in the design §8.9 example), bare 24-hex
// account ids, and `557058:<uuid>` account-id forms.
var entityIDRe = regexp.MustCompile(
	`^(?:[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}(?:-[0-9]+)?|[0-9a-f]{24}|[0-9]{5,}:[0-9a-fA-F-]+)$`,
)

// Resolve turns a parsed [*Query] into a [*ResolvedQuery], applying field
// resolution, the function registry and entity-id resolution. It emits no SQL.
// Any unsupported construct is rejected with a precise, specific [*ResolveError].
func Resolve(q *Query, opts ResolveOptions) (*ResolvedQuery, error) {
	r := &resolver{opts: opts, fields: normalizeFields(opts.Fields), groups: map[*Clause][]*Clause{}}

	var where ResolvedExpr
	if q.Where != nil {
		buildGroups(q.Where, r.groups)
		w, err := r.resolveExpr(q.Where)
		if err != nil {
			return nil, err
		}
		where = w
	}

	order, err := r.resolveOrder(q.OrderBy)
	if err != nil {
		return nil, err
	}

	return &ResolvedQuery{Where: where, OrderBy: order, Raw: q.Raw}, nil
}

// resolver holds per-resolution state.
type resolver struct {
	opts   ResolveOptions
	fields FieldMap
	// groups maps each clause to the set of clauses in its AND-group (used for
	// the standardIssueTypes() redundancy rule).
	groups map[*Clause][]*Clause
}

// normalizeFields lowercases the injected field keys for case-insensitive
// lookup. The returned map is independent of the caller's.
func normalizeFields(in FieldMap) FieldMap {
	out := make(FieldMap, len(in))
	for k, v := range in {
		out[strings.ToLower(k)] = v
	}
	return out
}

// lookupField resolves a JQL field name to its [FieldDef], checking the injected
// map first, then the built-in alias table. Matching is case-insensitive.
func (r *resolver) lookupField(name string) (FieldDef, bool) {
	key := strings.ToLower(name)
	if fd, ok := r.fields[key]; ok {
		return fd, true
	}
	if fd, ok := builtinAliases[key]; ok {
		return fd, true
	}
	return FieldDef{}, false
}

// resolveExpr recursively resolves a boolean node. A nil ResolvedExpr return
// with a nil error means the sub-tree resolved to a no-op (always true), which
// happens when a redundant standardIssueTypes() clause is dropped; callers fold
// that away per boolean algebra.
func (r *resolver) resolveExpr(e Expr) (ResolvedExpr, error) {
	switch n := e.(type) {
	case *BinaryExpr:
		left, err := r.resolveExpr(n.Left)
		if err != nil {
			return nil, err
		}
		right, err := r.resolveExpr(n.Right)
		if err != nil {
			return nil, err
		}
		if n.Op == OpAnd {
			// X AND true == X.
			if left == nil {
				return right, nil
			}
			if right == nil {
				return left, nil
			}
			return &ResolvedBinary{Op: OpAnd, Left: left, Right: right}, nil
		}
		// OR: X OR true == true (represented as nil / no constraint).
		if left == nil || right == nil {
			return nil, nil
		}
		return &ResolvedBinary{Op: OpOr, Left: left, Right: right}, nil
	case *NotExpr:
		x, err := r.resolveExpr(n.X)
		if err != nil {
			return nil, err
		}
		if x == nil {
			// NOT of a dropped (always-true) sub-tree: treat as no-op; this
			// only arises from a redundant standardIssueTypes() under NOT,
			// which carries no local meaning.
			return nil, nil
		}
		return &ResolvedNot{X: x}, nil
	case *Clause:
		return r.resolveClause(n)
	default:
		return nil, fmt.Errorf("jql: unknown expression node %T", e)
	}
}

// resolveClause resolves a single comparison, applying the function registry and
// entity-id resolution, and rejecting unsupported constructs.
func (r *resolver) resolveClause(c *Clause) (ResolvedExpr, error) {
	if c.Op.isHistory() {
		return nil, &ResolveError{Offset: c.OpOffset,
			Msg: fmt.Sprintf("historical operator %q is not supported locally yet", c.Op)}
	}

	// `filter = "saved"` references a saved filter, which is not synced.
	if strings.EqualFold(c.Field, "filter") {
		return nil, &ResolveError{Offset: c.FieldOffset,
			Msg: "filter queries are not supported locally (saved filters are not synced)"}
	}

	fd, ok := r.lookupField(c.Field)
	if !ok {
		return nil, &ResolveError{Offset: c.FieldOffset,
			Msg: fmt.Sprintf("unknown field %q (no column mapping found)", c.Field)}
	}

	// Empty operators have no value payload.
	if c.Op == OpIsEmpty || c.Op == OpIsNotEmpty {
		return &ResolvedClause{Field: c.Field, Column: fd.Column, Kind: fd.Kind, Op: c.Op}, nil
	}

	// standardIssueTypes()/subTaskIssueTypes() special handling: drop when a
	// concrete sibling issuetype filter makes it redundant, else reject.
	if fc, isFunc := c.Value.(*FuncCall); isFunc && isIssueTypeSetFunc(fc.Name) {
		if r.redundantIssueTypeSet(c) {
			return nil, nil // drop as a no-op
		}
		return nil, &ResolveError{Offset: fc.Offset,
			Msg: fmt.Sprintf("%s() is not supported locally (issue-type metadata is not synced); "+
				"add a concrete issuetype =/IN filter to make it redundant", fc.Name)}
	}

	// Gather raw value items (a list expands to its elements).
	var items []Value
	if lv, isList := c.Value.(*ListValue); isList {
		items = lv.Items
	} else {
		items = []Value{c.Value}
	}

	rc := &ResolvedClause{Field: c.Field, Column: fd.Column, Kind: fd.Kind, Op: c.Op}
	for _, item := range items {
		rv, err := r.resolveValue(fd, c.Field, item)
		if err != nil {
			return nil, err
		}
		rc.Values = append(rc.Values, rv)
	}
	return rc, nil
}

// resolveValue resolves a single value node into a typed [ResolvedValue].
func (r *resolver) resolveValue(fd FieldDef, field string, v Value) (ResolvedValue, error) {
	switch n := v.(type) {
	case *FuncCall:
		return r.resolveFunc(fd, field, n)
	case *RelativeDate:
		return ResolvedValue{
			Kind:     RVDate,
			Original: n.Raw,
			Date:     &DateSpec{Kind: DateRelative, Sign: n.Sign, Amount: n.Amount, Unit: n.Unit},
		}, nil
	case *NumberValue:
		return ResolvedValue{Kind: RVNumber, Num: n.Val, Str: n.Raw, Original: n.Raw}, nil
	case *StringValue:
		return r.resolveStringValue(fd, field, n)
	case *ListValue:
		return ResolvedValue{}, &ResolveError{Offset: n.Offset, Msg: "nested list is not allowed"}
	default:
		return ResolvedValue{}, fmt.Errorf("jql: unknown value node %T", v)
	}
}

// resolveStringValue resolves a bare word / quoted string, performing entity-id
// resolution for entity fields and date-literal/number classification by kind.
func (r *resolver) resolveStringValue(fd FieldDef, field string, s *StringValue) (ResolvedValue, error) {
	// Entity id → display name.
	if fd.Entity && !s.Quoted && entityIDRe.MatchString(s.Val) {
		if r.opts.ResolveID == nil {
			return ResolvedValue{}, &ResolveError{Offset: s.Offset,
				Msg: fmt.Sprintf("could not resolve %s id %q locally; use the display name", field, s.Val)}
		}
		name, ok, err := r.opts.ResolveID(field, s.Val)
		if err != nil {
			return ResolvedValue{}, &ResolveError{Offset: s.Offset,
				Msg: fmt.Sprintf("error resolving %s id %q: %v", field, s.Val, err)}
		}
		if !ok {
			return ResolvedValue{}, &ResolveError{Offset: s.Offset,
				Msg: fmt.Sprintf("could not resolve %s id %q locally; use the display name", field, s.Val)}
		}
		return ResolvedValue{Kind: RVString, Str: name, Original: s.Val, ResolvedFromID: true}, nil
	}

	switch fd.Kind {
	case KindDate:
		// A string on a date column is treated as an absolute date literal.
		return ResolvedValue{Kind: RVDate, Str: s.Val, Original: s.Val}, nil
	case KindNumber:
		if f, err := strconv.ParseFloat(s.Val, 64); err == nil {
			return ResolvedValue{Kind: RVNumber, Num: f, Str: s.Val, Original: s.Val}, nil
		}
		return ResolvedValue{Kind: RVString, Str: s.Val, Original: s.Val}, nil
	default:
		return ResolvedValue{Kind: RVString, Str: s.Val, Original: s.Val}, nil
	}
}

// resolveFunc applies the function registry (design §8.6).
func (r *resolver) resolveFunc(fd FieldDef, field string, fc *FuncCall) (ResolvedValue, error) {
	switch lowerName(fc.Name) {
	case "now":
		return ResolvedValue{Kind: RVDate, Original: "now()", Date: &DateSpec{Kind: DateNow}}, nil
	case "startofday":
		return ResolvedValue{Kind: RVDate, Original: "startOfDay()", Date: &DateSpec{Kind: DateStartOfDay}}, nil
	case "startofweek":
		return ResolvedValue{Kind: RVDate, Original: "startOfWeek()", Date: &DateSpec{Kind: DateStartOfWeek}}, nil
	case "startofmonth":
		return ResolvedValue{Kind: RVDate, Original: "startOfMonth()", Date: &DateSpec{Kind: DateStartOfMonth}}, nil
	case "currentuser":
		if r.opts.CurrentUser == "" {
			return ResolvedValue{}, &ResolveError{Offset: fc.Offset,
				Msg: "currentUser() cannot be resolved: no current user is configured"}
		}
		return ResolvedValue{Kind: RVString, Str: r.opts.CurrentUser, Original: "currentUser()"}, nil
	case "standardissuetypes", "subtaskissuetypes":
		// Handled at clause level for =/IN; reaching here means it is used in a
		// position where redundancy cannot apply.
		return ResolvedValue{}, &ResolveError{Offset: fc.Offset,
			Msg: fmt.Sprintf("%s() is not supported locally (issue-type metadata is not synced)", fc.Name)}
	case "membersof":
		return ResolvedValue{}, &ResolveError{Offset: fc.Offset,
			Msg: "membersOf() is not supported locally (group membership is not synced)"}
	case "votedissues":
		return ResolvedValue{}, &ResolveError{Offset: fc.Offset,
			Msg: "votedIssues() is not supported locally (votes are not synced)"}
	case "watchedissues":
		return ResolvedValue{}, &ResolveError{Offset: fc.Offset,
			Msg: "watchedIssues() is not supported locally (watchers are not synced)"}
	case "linkedissues":
		return ResolvedValue{}, &ResolveError{Offset: fc.Offset,
			Msg: "linkedIssues() is not supported locally yet"}
	default:
		return ResolvedValue{}, &ResolveError{Offset: fc.Offset,
			Msg: fmt.Sprintf("unsupported function %s()", fc.Name)}
	}
}

// resolveOrder resolves the ORDER BY fields to columns.
func (r *resolver) resolveOrder(fields []OrderField) ([]ResolvedOrder, error) {
	var out []ResolvedOrder
	for _, f := range fields {
		fd, ok := r.lookupField(f.Field)
		if !ok {
			return nil, &ResolveError{Offset: f.Offset,
				Msg: fmt.Sprintf("unknown field %q in ORDER BY (no column mapping found)", f.Field)}
		}
		out = append(out, ResolvedOrder{Field: f.Field, Column: fd.Column, Kind: fd.Kind, Desc: f.Desc})
	}
	return out, nil
}

// isIssueTypeSetFunc reports whether name is standardIssueTypes/subTaskIssueTypes.
func isIssueTypeSetFunc(name string) bool {
	n := lowerName(name)
	return n == "standardissuetypes" || n == "subtaskissuetypes"
}

// redundantIssueTypeSet reports whether clause c (an issuetype IN/=
// standardIssueTypes()-style clause) is made redundant by a sibling concrete
// issuetype filter in the same AND-group (design §8.6).
func (r *resolver) redundantIssueTypeSet(c *Clause) bool {
	typeFd, ok := r.lookupField("issuetype")
	if !ok {
		return false
	}
	for _, sib := range r.groups[c] {
		if sib == c {
			continue
		}
		if sib.Op != OpEq && sib.Op != OpIn {
			continue
		}
		fd, ok := r.lookupField(sib.Field)
		if !ok || fd.Column != typeFd.Column {
			continue
		}
		// The sibling must itself be a concrete filter, not another
		// standardIssueTypes()/subTaskIssueTypes() function.
		if fc, isFunc := sib.Value.(*FuncCall); isFunc && isIssueTypeSetFunc(fc.Name) {
			continue
		}
		return true
	}
	return false
}

// buildGroups populates m with the AND-group (set of conjoined clauses) for
// every clause in the tree. Clauses separated only by AND edges share a group;
// crossing an OR or NOT starts a fresh group. This supports the
// standardIssueTypes() redundancy rule, which only applies within an AND-group.
func buildGroups(e Expr, m map[*Clause][]*Clause) {
	switch n := e.(type) {
	case *Clause:
		m[n] = []*Clause{n}
	case *NotExpr:
		buildGroups(n.X, m)
	case *BinaryExpr:
		if n.Op == OpOr {
			buildGroups(n.Left, m)
			buildGroups(n.Right, m)
			return
		}
		// AND region: collect all AND-connected clauses into one group, then
		// descend into any OR/NOT sub-nodes to assign their own groups.
		g := collectConjuncts(e)
		for _, c := range g {
			m[c] = g
		}
		descendConjuncts(e, m)
	}
}

// collectConjuncts returns all clauses reachable from e via AND edges only,
// stopping at OR/NOT boundaries.
func collectConjuncts(e Expr) []*Clause {
	switch n := e.(type) {
	case *Clause:
		return []*Clause{n}
	case *BinaryExpr:
		if n.Op == OpAnd {
			return append(collectConjuncts(n.Left), collectConjuncts(n.Right)...)
		}
		return nil
	default:
		return nil
	}
}

// descendConjuncts walks the AND region rooted at e and recurses buildGroups on
// any OR/NOT sub-node so clauses nested inside them get their own groups.
func descendConjuncts(e Expr, m map[*Clause][]*Clause) {
	switch n := e.(type) {
	case *BinaryExpr:
		if n.Op == OpAnd {
			descendConjuncts(n.Left, m)
			descendConjuncts(n.Right, m)
			return
		}
		buildGroups(n, m) // OR sub-node: fresh region
	case *NotExpr:
		buildGroups(n, m)
	}
}
