package jql

import (
	"strings"
	"testing"
)

// testOpts builds ResolveOptions covering the §8.9 example: a custom numeric
// field, a current user, and an id resolver that maps the example GUID to a team
// display name.
func testOpts() ResolveOptions {
	return ResolveOptions{
		Fields: FieldMap{
			"px_impact_score": {Column: "px_impact_score", Kind: KindNumber},
			"PX_Impact_Score": {Column: "px_impact_score", Kind: KindNumber},
		},
		CurrentUser: "alice@example.com",
		ResolveID: func(field, id string) (string, bool, error) {
			if field == "Team" && id == "ec74d716-af36-4b3c-950f-f79213d08f71-1819" {
				return "ACS Docs", true, nil
			}
			return "", false, nil
		},
	}
}

func mustParse(t *testing.T, src string) *Query {
	t.Helper()
	q, err := Parse(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return q
}

func TestResolveExample89(t *testing.T) {
	q := mustParse(t, example89)
	rq, err := Resolve(q, testOpts())
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	clauses := collectResolved(rq.Where)

	// standardIssueTypes() dropped (redundant with issuetype = Bug) → 8 clauses.
	if len(clauses) != 8 {
		t.Fatalf("got %d resolved clauses, want 8: %+v", len(clauses), clauses)
	}

	byCol := map[string]*ResolvedClause{}
	for _, c := range clauses {
		byCol[c.Column] = c
	}

	// project = ROX
	if c := byCol["project"]; c == nil || c.Op != OpEq || c.Values[0].Str != "ROX" {
		t.Errorf("project clause = %+v", c)
	}
	// issuetype = Bug (standardIssueTypes() dropped)
	if c := byCol["type"]; c == nil || c.Op != OpEq || c.Values[0].Str != "Bug" {
		t.Errorf("type clause = %+v", c)
	}
	// Team != <id> resolves to display name via IDResolver.
	if c := byCol["team"]; c == nil || c.Op != OpNeq {
		t.Fatalf("team clause = %+v", c)
	} else {
		v := c.Values[0]
		if !v.ResolvedFromID || v.Str != "ACS Docs" {
			t.Errorf("team value = %+v, want resolved 'ACS Docs'", v)
		}
	}
	// PX_Impact_Score >= 10000 (custom numeric)
	if c := byCol["px_impact_score"]; c == nil || c.Kind != KindNumber || c.Op != OpGte {
		t.Fatalf("px clause = %+v", c)
	} else if c.Values[0].Kind != RVNumber || c.Values[0].Num != 10000 {
		t.Errorf("px value = %+v, want number 10000", c.Values[0])
	}
	// created < -90d resolves to a relative-date node.
	if c := byCol["created"]; c == nil || c.Op != OpLt {
		t.Fatalf("created clause = %+v", c)
	} else {
		v := c.Values[0]
		if v.Kind != RVDate || v.Date == nil || v.Date.Kind != DateRelative ||
			v.Date.Sign != -1 || v.Date.Amount != 90 || v.Date.Unit != "d" {
			t.Errorf("created value = %+v, want relative -90d", v)
		}
	}
	// resolved IS EMPTY has no values.
	if c := byCol["resolved"]; c == nil || c.Op != OpIsEmpty || len(c.Values) != 0 {
		t.Errorf("resolved clause = %+v", c)
	}
	// status IN (...) with 4 values.
	if c := byCol["status"]; c == nil || c.Op != OpIn || len(c.Values) != 4 {
		t.Errorf("status clause = %+v", c)
	}
}

// TestInjectedFieldMapCannotDisableEntity is the §8.9 regression: consumers
// (dashboard 14.6 / CLI 14.7) build their injected FieldMap from the DB
// field_map, where team is not a user-typed field and so arrives with
// Entity:false. That injected false must NOT be able to turn OFF the built-in
// entity flag; otherwise `Team != <GUID>` skips id → display-name resolution and
// the raw GUID leaks into the SQL (never excluding the resolved "ACS Docs" rows).
func TestInjectedFieldMapCannotDisableEntity(t *testing.T) {
	const guid = "ec74d716-af36-4b3c-950f-f79213d08f71-1819"
	resolveID := func(field, id string) (string, bool, error) {
		if field == "Team" && id == guid {
			return "ACS Docs", true, nil
		}
		return "", false, nil
	}

	// Inject team with Entity:false, mimicking the DB-sourced consumer FieldMap.
	q := mustParse(t, "Team != "+guid)
	rq, err := Resolve(q, ResolveOptions{
		Fields:    FieldMap{"team": {Column: "team", Kind: KindString, Entity: false}},
		ResolveID: resolveID,
	})
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	clauses := collectResolved(rq.Where)
	if len(clauses) != 1 {
		t.Fatalf("got %d clauses, want 1: %+v", len(clauses), clauses)
	}
	c := clauses[0]
	if c.Column != "team" || c.Op != OpNeq || len(c.Values) != 1 {
		t.Fatalf("team clause = %+v", c)
	}
	v := c.Values[0]
	if !v.ResolvedFromID || v.Str != "ACS Docs" {
		t.Fatalf("team value = %+v, want resolved 'ACS Docs' (ResolvedFromID true)", v)
	}

	// End-to-end: the emitted SQL must use the resolved name, not the raw GUID.
	r := mustTranslate(t, "Team != "+guid, Options{
		Dialect:    SQLite,
		FieldMap:   FieldMap{"team": {Column: "team", Kind: KindString, Entity: false}},
		IDResolver: resolveID,
	})
	wantFragments(t, r.SQL, "lower(COALESCE(team,'')) NOT IN ('', lower('ACS Docs'))")
	if strings.Contains(r.SQL, guid) {
		t.Errorf("SQL leaked raw GUID %q: %s", guid, r.SQL)
	}
}

func TestResolveUnknownFieldNamed(t *testing.T) {
	q := mustParse(t, `bogusField = 1`)
	_, err := Resolve(q, ResolveOptions{})
	if err == nil {
		t.Fatal("expected unknown field error")
	}
	if !strings.Contains(err.Error(), "bogusField") {
		t.Errorf("error %q does not name the field", err)
	}
}

func TestResolveMembersOfRejected(t *testing.T) {
	q := mustParse(t, `assignee in membersOf("devs")`)
	_, err := Resolve(q, ResolveOptions{})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "membersof") {
		t.Fatalf("expected membersOf rejection, got %v", err)
	}
}

func TestResolveHistoryOpRejected(t *testing.T) {
	q := mustParse(t, `status WAS "Done"`)
	_, err := Resolve(q, ResolveOptions{})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "historical operator") {
		t.Fatalf("expected historical operator rejection, got %v", err)
	}
}

func TestResolveStandardIssueTypesDroppedWhenRedundant(t *testing.T) {
	q := mustParse(t, `issuetype in standardIssueTypes() and issuetype = Bug`)
	rq, err := Resolve(q, ResolveOptions{})
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	clauses := collectResolved(rq.Where)
	if len(clauses) != 1 || clauses[0].Values[0].Str != "Bug" {
		t.Fatalf("expected only issuetype=Bug after drop, got %+v", clauses)
	}
}

func TestResolveStandardIssueTypesRejectedWhenNotRedundant(t *testing.T) {
	q := mustParse(t, `issuetype in standardIssueTypes() and project = ROX`)
	_, err := Resolve(q, ResolveOptions{})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "standardissuetypes") {
		t.Fatalf("expected standardIssueTypes rejection, got %v", err)
	}
}

func TestResolveStandardIssueTypesNotRedundantAcrossOr(t *testing.T) {
	// The concrete issuetype filter is in an OR branch, so it does NOT make the
	// standardIssueTypes() clause redundant.
	q := mustParse(t, `issuetype in standardIssueTypes() and (issuetype = Bug or project = ROX)`)
	_, err := Resolve(q, ResolveOptions{})
	if err == nil {
		t.Fatal("expected rejection: concrete filter across OR must not count as redundant")
	}
}

func TestResolveRelativeDateAndDateFunctions(t *testing.T) {
	cases := []struct {
		src  string
		kind DateSpecKind
	}{
		{`created > now()`, DateNow},
		{`created > startOfDay()`, DateStartOfDay},
		{`created > startOfWeek()`, DateStartOfWeek},
		{`created > startOfMonth()`, DateStartOfMonth},
		{`created > -2w`, DateRelative},
	}
	for _, tc := range cases {
		q := mustParse(t, tc.src)
		rq, err := Resolve(q, ResolveOptions{})
		if err != nil {
			t.Fatalf("resolve %q: %v", tc.src, err)
		}
		c := collectResolved(rq.Where)[0]
		v := c.Values[0]
		if v.Kind != RVDate || v.Date == nil || v.Date.Kind != tc.kind {
			t.Errorf("%q: value = %+v, want date kind %v", tc.src, v, tc.kind)
		}
	}
}

func TestResolveCurrentUser(t *testing.T) {
	q := mustParse(t, `assignee = currentUser()`)

	// With a configured user, resolves to it.
	rq, err := Resolve(q, ResolveOptions{CurrentUser: "bob"})
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	if v := collectResolved(rq.Where)[0].Values[0]; v.Str != "bob" {
		t.Errorf("currentUser resolved to %q, want bob", v.Str)
	}

	// Without one, rejected.
	if _, err := Resolve(q, ResolveOptions{}); err == nil {
		t.Fatal("expected currentUser() rejection without configured user")
	}
}

func TestResolveUnresolvedEntityID(t *testing.T) {
	q := mustParse(t, `Team = ec74d716-af36-4b3c-950f-f79213d08f71-1819`)
	// No ResolveID provided → reject with display-name guidance.
	_, err := Resolve(q, ResolveOptions{})
	if err == nil || !strings.Contains(err.Error(), "use the display name") {
		t.Fatalf("expected id rejection with guidance, got %v", err)
	}
}

func TestResolveArrayAndFieldKinds(t *testing.T) {
	q := mustParse(t, `labels in (x, y) and components = Backend`)
	rq, err := Resolve(q, ResolveOptions{})
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	for _, c := range collectResolved(rq.Where) {
		if c.Kind != KindArray {
			t.Errorf("%s: kind = %v, want array", c.Column, c.Kind)
		}
	}
}

func TestResolveOrderByColumns(t *testing.T) {
	q := mustParse(t, `project = ROX ORDER BY issuetype DESC`)
	rq, err := Resolve(q, ResolveOptions{})
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	if len(rq.OrderBy) != 1 || rq.OrderBy[0].Column != "type" || !rq.OrderBy[0].Desc {
		t.Fatalf("order = %+v, want type DESC", rq.OrderBy)
	}
}

func TestResolveFilterRejected(t *testing.T) {
	q := mustParse(t, `filter = "My Saved Filter"`)
	_, err := Resolve(q, ResolveOptions{})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "filter") {
		t.Fatalf("expected filter rejection, got %v", err)
	}
}

// collectResolved flattens all resolved clauses in a resolved tree.
func collectResolved(e ResolvedExpr) []*ResolvedClause {
	switch n := e.(type) {
	case *ResolvedClause:
		return []*ResolvedClause{n}
	case *ResolvedNot:
		return collectResolved(n.X)
	case *ResolvedBinary:
		return append(collectResolved(n.Left), collectResolved(n.Right)...)
	default:
		return nil
	}
}
