package jql

import "testing"

// example89 is the worked example from design §8.9.
const example89 = `project = ROX and issuetype in standardIssueTypes() and Team != ec74d716-af36-4b3c-950f-f79213d08f71-1819
and issuetype = Bug and (priority in (Critical, Blocker) or PX_Impact_Score >= 10000)
and created < -90d and resolved is EMPTY and status in (New, Backlog, Refinement, "To Do")`

func TestParseExample89(t *testing.T) {
	q, err := Parse(example89)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if q.Where == nil {
		t.Fatal("expected a where tree")
	}
	clauses := collectClauses(q.Where)
	if len(clauses) != 9 {
		t.Fatalf("got %d clauses, want 9", len(clauses))
	}

	// Verify a sampling of clause shapes.
	byField := map[string]*Clause{}
	for _, c := range clauses {
		byField[c.Field] = c
	}
	if c := byField["Team"]; c == nil || c.Op != OpNeq {
		t.Errorf("Team clause = %+v, want != ", c)
	}
	if c := byField["created"]; c == nil {
		t.Fatal("missing created clause")
	} else if _, ok := c.Value.(*RelativeDate); !ok {
		t.Errorf("created value = %T, want *RelativeDate", c.Value)
	}
	if c := byField["resolved"]; c == nil || c.Op != OpIsEmpty {
		t.Errorf("resolved clause = %+v, want IS EMPTY", c)
	}
	if c := byField["status"]; c == nil || c.Op != OpIn {
		t.Errorf("status clause = %+v, want IN", c)
	} else if lv, ok := c.Value.(*ListValue); !ok || len(lv.Items) != 4 {
		t.Errorf("status value = %+v, want 4-item list", c.Value)
	}
	// standardIssueTypes() parsed as a function value on an IN clause.
	var sawFunc bool
	for _, c := range clauses {
		if fc, ok := c.Value.(*FuncCall); ok && lowerName(fc.Name) == "standardissuetypes" {
			sawFunc = true
		}
	}
	if !sawFunc {
		t.Error("standardIssueTypes() function value not parsed")
	}
}

func TestParsePrecedenceNotAndOr(t *testing.T) {
	// NOT a AND b OR c  ==  ((NOT a) AND b) OR c
	q, err := Parse(`NOT a = 1 AND b = 2 OR c = 3`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	top, ok := q.Where.(*BinaryExpr)
	if !ok || top.Op != OpOr {
		t.Fatalf("top = %T %v, want OR", q.Where, q.Where)
	}
	left, ok := top.Left.(*BinaryExpr)
	if !ok || left.Op != OpAnd {
		t.Fatalf("left = %T, want AND", top.Left)
	}
	if _, ok := left.Left.(*NotExpr); !ok {
		t.Fatalf("left.Left = %T, want NOT", left.Left)
	}
}

func TestParseParensOverridePrecedence(t *testing.T) {
	q, err := Parse(`a = 1 AND (b = 2 OR c = 3)`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	top, ok := q.Where.(*BinaryExpr)
	if !ok || top.Op != OpAnd {
		t.Fatalf("top = %T, want AND", q.Where)
	}
	if r, ok := top.Right.(*BinaryExpr); !ok || r.Op != OpOr {
		t.Fatalf("right = %T, want OR", top.Right)
	}
}

func TestParseAllScalarOperators(t *testing.T) {
	cases := map[string]Operator{
		"a = 1":          OpEq,
		"a != 1":         OpNeq,
		"a > 1":          OpGt,
		"a >= 1":         OpGte,
		"a < 1":          OpLt,
		"a <= 1":         OpLte,
		`a ~ "x"`:        OpContains,
		`a !~ "x"`:       OpNotContains,
		"a in (1,2)":     OpIn,
		"a not in (1,2)": OpNotIn,
		"a is empty":     OpIsEmpty,
		"a is not empty": OpIsNotEmpty,
		"a is null":      OpIsEmpty,
		"a is not null":  OpIsNotEmpty,
	}
	for src, want := range cases {
		q, err := Parse(src)
		if err != nil {
			t.Fatalf("parse %q: %v", src, err)
		}
		c := q.Where.(*Clause)
		if c.Op != want {
			t.Errorf("%q: op = %v, want %v", src, c.Op, want)
		}
	}
}

func TestParseQuotedMultiWordValue(t *testing.T) {
	q, err := Parse(`status = "In Progress"`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	c := q.Where.(*Clause)
	sv, ok := c.Value.(*StringValue)
	if !ok || sv.Val != "In Progress" || !sv.Quoted {
		t.Fatalf("value = %+v, want quoted 'In Progress'", c.Value)
	}
}

func TestParseOrderBy(t *testing.T) {
	q, err := Parse(`project = ROX ORDER BY created DESC, priority ASC`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(q.OrderBy) != 2 {
		t.Fatalf("got %d order fields, want 2", len(q.OrderBy))
	}
	if q.OrderBy[0].Field != "created" || !q.OrderBy[0].Desc {
		t.Errorf("order[0] = %+v, want created DESC", q.OrderBy[0])
	}
	if q.OrderBy[1].Field != "priority" || q.OrderBy[1].Desc {
		t.Errorf("order[1] = %+v, want priority ASC", q.OrderBy[1])
	}
}

func TestParseHistoryOpsRecognized(t *testing.T) {
	// The parser must recognize history ops (not error) so the resolver can
	// reject them with a specific message.
	for _, src := range []string{
		`status WAS "Done"`,
		`status WAS IN ("Done", "Closed") AND project = ROX`,
		`status CHANGED FROM "To Do" TO "Done" DURING (-30d, now())`,
		`assignee CHANGED AFTER -7d`,
	} {
		q, err := Parse(src)
		if err != nil {
			t.Fatalf("parse %q: %v", src, err)
		}
		if q.Where == nil {
			t.Fatalf("parse %q: nil where", src)
		}
	}
}

func TestParseErrorsHaveOffset(t *testing.T) {
	cases := []string{
		``,
		`project =`,
		`project = ROX AND`,
		`= ROX`,
		`project ROX`,
		`a in ()`,
		`(a = 1`,
		`a in 1`,
	}
	for _, src := range cases {
		_, err := Parse(src)
		if err == nil {
			t.Errorf("expected error for %q", src)
			continue
		}
		if _, ok := err.(*ParseError); !ok {
			t.Errorf("%q: error type %T, want *ParseError", src, err)
		}
	}
}

// collectClauses flattens all clauses in a raw boolean tree.
func collectClauses(e Expr) []*Clause {
	switch n := e.(type) {
	case *Clause:
		return []*Clause{n}
	case *NotExpr:
		return collectClauses(n.X)
	case *BinaryExpr:
		return append(collectClauses(n.Left), collectClauses(n.Right)...)
	default:
		return nil
	}
}
