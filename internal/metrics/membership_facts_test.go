package metrics

import (
	"testing"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
)

// clm inserts a set-valued (membership) changelog transition. value fields hold stable ids
// (comma-separated for components/Sprint), string fields the display names.
func clm(t *testing.T, database *db.DB, id, key, field, fromV, fromS, toV, toS, at string) {
	t.Helper()
	if err := database.InsertChangelog(&db.ChangelogEntry{
		ID: id, IssueKey: key, Field: field,
		FromValue: fromV, FromString: fromS, ToValue: toV, ToString: toS, ChangedAt: at,
	}); err != nil {
		t.Fatalf("InsertChangelog %s: %v", id, err)
	}
}

// BuildAll persists event-time membership intervals for components/labels/Sprint. A temporal
// join at the completion instant returns the event-time members (never the current snapshot),
// and GROUP BY member_name is fan-out-safe: an issue added/removed/re-added to one component
// is counted once.
func TestBuildAll_MembershipFacts(t *testing.T) {
	database, store := newFactStore(t)

	// M-1: created in Alpha; Beta added, then Alpha removed before completion. At the
	// resolution instant the event-time component set is {Beta} only (Alpha had exited).
	upIssue(t, database, "M-1", "PM", "Story", "Closed", "Done", "2024-01-01T00:00:00Z", "2024-01-10T00:00:00Z", "Done", true)
	cl(t, database, "s1", "M-1", "Backlog", "Closed", "2024-01-09T00:00:00Z")
	clm(t, database, "mc1", "M-1", "components", "100", "Alpha", "100,200", "Alpha,Beta", "2024-01-03T00:00:00Z")
	clm(t, database, "mc2", "M-1", "components", "100,200", "Alpha,Beta", "200", "Beta", "2024-01-07T00:00:00Z")
	clm(t, database, "ml1", "M-1", "labels", "", "urgent", "", "urgent backend", "2024-01-04T00:00:00Z")
	clm(t, database, "ms1", "M-1", "Sprint", "", "", "5", "Sprint 5", "2024-01-02T00:00:00Z")

	// M-2: added to Beta, removed, then re-added (two Beta intervals). At completion only the
	// open interval matches, so a GROUP BY member_name counts M-2 once (not twice).
	upIssue(t, database, "M-2", "PM", "Story", "Closed", "Done", "2024-01-04T00:00:00Z", "2024-01-20T00:00:00Z", "Done", true)
	cl(t, database, "s2", "M-2", "Backlog", "Closed", "2024-01-19T00:00:00Z")
	clm(t, database, "mc3", "M-2", "components", "", "", "200", "Beta", "2024-01-05T00:00:00Z")
	clm(t, database, "mc4", "M-2", "components", "200", "Beta", "", "", "2024-01-08T00:00:00Z")
	clm(t, database, "mc5", "M-2", "components", "", "", "200", "Beta", "2024-01-12T00:00:00Z")

	b := NewBuilder(store, factPolicy())
	if _, err := b.BuildAll(asOf, nil); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}

	// Rows were written for every configured dimension.
	for _, dim := range []string{"component", "label", "sprint"} {
		var n int
		if err := database.QueryRow(
			`SELECT COUNT(*) FROM fact_issue_membership WHERE generation = `+activeGenSub+` AND dimension = ?`, dim,
		).Scan(&n); err != nil {
			t.Fatalf("count %s rows: %v", dim, err)
		}
		if n == 0 {
			t.Errorf("no %s membership rows written", dim)
		}
	}

	// M-2 has two Beta intervals persisted (add/remove/re-add), which would double-count
	// under a naive GROUP BY — the temporal join must collapse it to one.
	var betaRows int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM fact_issue_membership
		   WHERE generation = ` + activeGenSub + ` AND dimension = 'component' AND issue_key = 'M-2' AND member_name = 'Beta'`,
	).Scan(&betaRows); err != nil {
		t.Fatalf("count M-2 Beta rows: %v", err)
	}
	if betaRows != 2 {
		t.Fatalf("M-2 Beta interval rows = %d, want 2", betaRows)
	}

	// Event-time temporal join at the completion instant, fan-out-safe GROUP BY member_name.
	type res struct {
		name string
		n    int
	}
	rows, err := database.Query(
		`SELECT m.member_name, COUNT(*) AS n
		   FROM fact_issue_flow f
		   JOIN fact_issue_membership m
		     ON m.generation = f.generation AND m.issue_key = f.issue_key AND m.dimension = 'component'
		    AND m.entered_at <= f.resolved AND (m.exited_at IS NULL OR m.exited_at > f.resolved)
		  WHERE f.generation = ` + activeGenSub + ` AND f.resolved IS NOT NULL
		  GROUP BY m.member_name ORDER BY m.member_name`)
	if err != nil {
		t.Fatalf("temporal join: %v", err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var r res
		if err := rows.Scan(&r.name, &r.n); err != nil {
			t.Fatal(err)
		}
		got[r.name] = r.n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// Alpha had exited both issues before their completion, so it is NOT an event-time member
	// at resolution (proves we never copy today's value onto the completion instant).
	if _, ok := got["Alpha"]; ok {
		t.Errorf("Alpha must not be an event-time member at completion, got count %d", got["Alpha"])
	}
	// Beta is the event-time component of both issues at completion: exactly two distinct
	// issues, despite M-2 carrying two Beta interval rows.
	if got["Beta"] != 2 {
		t.Errorf("component Beta event-time count = %d, want 2 (fan-out-safe)", got["Beta"])
	}

	// Store accessor mirrors the fact rows; M-1 keeps both an exited Alpha and an open Beta.
	ivs, err := store.MembershipIntervals(nil, "component")
	if err != nil {
		t.Fatalf("MembershipIntervals: %v", err)
	}
	var m1Alpha, m1BetaOpen bool
	for _, iv := range ivs {
		if iv.IssueKey == "M-1" && iv.MemberName == "Alpha" {
			m1Alpha = true
			if iv.Open || iv.ExitedAt == nil {
				t.Errorf("M-1 Alpha must be closed with a non-nil exit, open=%v exit=%v", iv.Open, iv.ExitedAt)
			}
		}
		if iv.IssueKey == "M-1" && iv.MemberName == "Beta" && iv.Open {
			m1BetaOpen = true
			if iv.ExitedAt != nil {
				t.Errorf("open membership must store NULL exit, got %v", iv.ExitedAt)
			}
			if iv.MemberID != "200" {
				t.Errorf("M-1 Beta member_id = %q, want 200", iv.MemberID)
			}
		}
	}
	if !m1Alpha || !m1BetaOpen {
		t.Errorf("expected M-1 closed Alpha (%v) and open Beta (%v)", m1Alpha, m1BetaOpen)
	}
}

// Changing membership_fields changes the policy fingerprint, forcing a full restatement.
func TestMembershipFields_Fingerprint(t *testing.T) {
	base := CompilePolicy(&config.MetricsConfig{})                                             // default [components, labels, Sprint]
	narrowed := CompilePolicy(&config.MetricsConfig{MembershipFields: []string{"components"}}) // only component dimension
	if base.Fingerprint() == narrowed.Fingerprint() {
		t.Fatalf("fingerprint unchanged after narrowing membership_fields: %s", base.Fingerprint())
	}

	// Default and an explicit equivalent set yield the same dimension set -> same fingerprint
	// contribution (membership is folded in by dimension token, order-independent).
	explicit := CompilePolicy(&config.MetricsConfig{MembershipFields: []string{"Sprint", "labels", "components"}})
	if base.Fingerprint() != explicit.Fingerprint() {
		t.Errorf("default and explicit-equivalent membership sets differ: %s vs %s", base.Fingerprint(), explicit.Fingerprint())
	}

	// A non-set-valued membership field is rejected (fail closed), not silently accepted.
	bad := CompilePolicy(&config.MetricsConfig{MembershipFields: []string{"priority"}})
	if bad.Enabled(MetricMembership) {
		t.Errorf("membership metric must reject a non-set-valued field")
	}
}
