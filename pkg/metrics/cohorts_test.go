package metrics

import (
	"testing"
	"time"
)

func ts(y, m, d int) time.Time {
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
}

func cp(t time.Time) *time.Time { return &t }

func TestNormalizeOwner(t *testing.T) {
	cases := []struct {
		name            string
		accountID, disp string
		want            string
	}{
		{"display wins", "accA", "Alice", "Alice"},
		{"empty display falls back to id", "accA", "", "accA"},
		{"empty both -> unassigned", "", "", unassigned},
		{"sentinel -> unassigned even with id", "accA", "unassigned", unassigned},
		{"trim", "", "  Bob  ", "Bob"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeOwner(c.accountID, c.disp); got != c.want {
				t.Errorf("NormalizeOwner(%q,%q) = %q, want %q", c.accountID, c.disp, got, c.want)
			}
		})
	}
}

// IntervalIndex performs an event-time join: the value at an old instant is the historical
// value, never the current one; an open interval extends to the query instant; and a point
// before creation or inside an unknown gap is surfaced, not silently attributed.
func TestIntervalIndex_EventTimeJoin(t *testing.T) {
	rows := []IntervalRow{
		// Unknown leading gap (incomplete history) for I-1.
		{IssueKey: "I-1", Field: "assignee", Seq: 0, Provenance: ProvUnknown, EnteredAt: ts(2024, 1, 1), ExitedAt: cp(ts(2024, 1, 5))},
		{IssueKey: "I-1", Field: "assignee", Seq: 1, Value: "Alice", ValueID: "accA", Provenance: ProvEvent, EnteredAt: ts(2024, 1, 5), ExitedAt: cp(ts(2024, 1, 20))},
		{IssueKey: "I-1", Field: "assignee", Seq: 2, Value: "Bob", ValueID: "accB", Provenance: ProvEvent, EnteredAt: ts(2024, 1, 20), Open: true}, // open
	}
	ix := NewIntervalIndex(rows)

	// Within the unknown gap: covered but not known.
	if _, _, known := ix.ValueAt("I-1", ts(2024, 1, 3)); known {
		t.Errorf("value in unknown gap must be known=false")
	}
	if owner, known := ix.OwnerAt("I-1", ts(2024, 1, 3)); known || owner != unknownOwnerBucket {
		t.Errorf("owner in unknown gap = (%q,%v), want (%q,false)", owner, known, unknownOwnerBucket)
	}

	// Historical instant: Alice, not the current (Bob).
	if owner, known := ix.OwnerAt("I-1", ts(2024, 1, 10)); !known || owner != "Alice" {
		t.Errorf("owner at 2024-01-10 = (%q,%v), want Alice — never current-copied", owner, known)
	}
	// Boundary instant belongs to exactly one (half-open) interval: 2024-01-20 is Bob.
	if owner, _ := ix.OwnerAt("I-1", ts(2024, 1, 20)); owner != "Bob" {
		t.Errorf("boundary 2024-01-20 owner = %q, want Bob (half-open)", owner)
	}
	// Open interval extends to a far-future as_of.
	if owner, known := ix.OwnerAt("I-1", ts(2025, 6, 1)); !known || owner != "Bob" {
		t.Errorf("open interval owner at 2025-06-01 = (%q,%v), want Bob", owner, known)
	}
	// Before creation: no covering interval.
	if _, ok := ix.At("I-1", ts(2023, 12, 1)); ok {
		t.Errorf("instant before creation must have no covering interval")
	}
}

func TestIntervalIndex_StageAt(t *testing.T) {
	rows := []IntervalRow{
		{IssueKey: "S-1", Field: "status", Seq: 0, Stage: StageTodo, EnteredAt: ts(2024, 1, 1), ExitedAt: cp(ts(2024, 1, 3))},
		{IssueKey: "S-1", Field: "status", Seq: 1, Stage: StageActive, EnteredAt: ts(2024, 1, 3), ExitedAt: cp(ts(2024, 1, 8))},
		{IssueKey: "S-1", Field: "status", Seq: 2, Stage: "", EnteredAt: ts(2024, 1, 8), Open: true}, // empty stage -> unknown bucket
	}
	ix := NewIntervalIndex(rows)
	if st, _ := ix.StageAt("S-1", ts(2024, 1, 5)); st != StageActive {
		t.Errorf("stage at 2024-01-05 = %q, want active", st)
	}
	if st, covered := ix.StageAt("S-1", ts(2024, 1, 10)); !covered || st != StageUnknown {
		t.Errorf("empty stage must surface as unknown bucket, got (%q,%v)", st, covered)
	}
}

func TestDailySamples(t *testing.T) {
	got := DailySamples(ts(2024, 1, 1).Add(5*time.Hour), ts(2024, 1, 3))
	if len(got) != 3 {
		t.Fatalf("samples = %d, want 3 (inclusive, midnight-truncated)", len(got))
	}
	if DayKey(got[0]) != "2024-01-01" || DayKey(got[2]) != "2024-01-03" {
		t.Errorf("sample bounds = %s..%s, want 2024-01-01..2024-01-03", DayKey(got[0]), DayKey(got[2]))
	}
	if got[0].Hour() != 0 {
		t.Errorf("samples must be midnight-aligned, got hour %d", got[0].Hour())
	}
	if DailySamples(ts(2024, 1, 3), ts(2024, 1, 1)) != nil {
		t.Errorf("reversed range must yield no samples")
	}
}

func TestCurrentDimension(t *testing.T) {
	it := issueSnap{project: "PW", itype: "Bug", assignee: ""}
	if v, _ := currentDimension(it, DimProject); v != "PW" {
		t.Errorf("project cut = %q, want PW", v)
	}
	if v, _ := currentDimension(it, DimType); v != "Bug" {
		t.Errorf("type cut = %q, want Bug", v)
	}
	if v, _ := currentDimension(it, DimAssignee); v != unassigned {
		t.Errorf("assignee cut = %q, want (Unassigned)", v)
	}
	if _, ok := currentDimension(it, DimPriority); ok {
		t.Errorf("priority has no current-attribute cut off the snapshot")
	}
}
