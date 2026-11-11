package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	if s == "" {
		return time.Time{}
	}
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad time %q: %v", s, err)
	}
	return tt.UTC()
}

// wantInterval is the expected shape of one scalar interval.
type wantInterval struct {
	valueID    string
	value      string
	known      bool
	provenance string
	entered    string
	exited     string // "" => open
}

func TestReplayScalar_Table(t *testing.T) {
	pol := CompilePolicy(nil)
	asOf := mustTime(t, "2024-03-01T00:00:00Z")

	cases := []struct {
		name         string
		field        string
		created      string
		complete     bool
		currentKnown bool
		currentID    string
		currentVal   string
		events       []ScalarEvent
		want         []wantInterval
		neverChanged bool
		seeded       bool
		ambiguous    bool
		hasUnknown   bool
	}{
		{
			name: "never-changed-complete", field: "priority",
			created: "2024-01-01T00:00:00Z", complete: true,
			currentKnown: true, currentID: "", currentVal: "High",
			want: []wantInterval{
				{value: "High", known: true, provenance: ProvSnapshot, entered: "2024-01-01T00:00:00Z"},
			},
			neverChanged: true,
		},
		{
			name: "never-changed-incomplete-is-unknown", field: "priority",
			created: "2024-01-01T00:00:00Z", complete: false,
			currentKnown: true, currentVal: "High",
			want: []wantInterval{
				{known: false, provenance: ProvUnknown, entered: "2024-01-01T00:00:00Z"},
			},
			hasUnknown: true,
		},
		{
			name: "never-changed-absent-column-is-unknown", field: "Team",
			created: "2024-01-01T00:00:00Z", complete: true,
			currentKnown: false, // column absent: cannot assert a value
			want: []wantInterval{
				{known: false, provenance: ProvUnknown, entered: "2024-01-01T00:00:00Z"},
			},
			neverChanged: true, hasUnknown: true,
		},
		{
			name: "priority-change-complete-seeds-leading", field: "priority",
			created: "2024-01-01T00:00:00Z", complete: true,
			events: []ScalarEvent{
				{ID: "1", ChangedAt: mustTime(t, "2024-01-10T00:00:00Z"), FromValue: "2", FromString: "Low", ToValue: "1", ToString: "High"},
			},
			want: []wantInterval{
				{valueID: "2", value: "Low", known: true, provenance: ProvEvent, entered: "2024-01-01T00:00:00Z", exited: "2024-01-10T00:00:00Z"},
				{valueID: "1", value: "High", known: true, provenance: ProvEvent, entered: "2024-01-10T00:00:00Z"},
			},
			seeded: true,
		},
		{
			name: "priority-change-incomplete-leading-unknown", field: "priority",
			created: "2024-01-01T00:00:00Z", complete: false,
			events: []ScalarEvent{
				{ID: "1", ChangedAt: mustTime(t, "2024-01-10T00:00:00Z"), FromValue: "2", FromString: "Low", ToValue: "1", ToString: "High"},
			},
			want: []wantInterval{
				{known: false, provenance: ProvUnknown, entered: "2024-01-01T00:00:00Z", exited: "2024-01-10T00:00:00Z"},
				{valueID: "1", value: "High", known: true, provenance: ProvEvent, entered: "2024-01-10T00:00:00Z"},
			},
			hasUnknown: true,
		},
		{
			name: "reassignment-chain", field: "assignee",
			created: "2024-01-01T00:00:00Z", complete: true,
			events: []ScalarEvent{
				{ID: "a", ChangedAt: mustTime(t, "2024-01-05T00:00:00Z"), FromValue: "accA", FromString: "Alice", ToValue: "accB", ToString: "Bob"},
				{ID: "b", ChangedAt: mustTime(t, "2024-01-09T00:00:00Z"), FromValue: "accB", FromString: "Bob", ToValue: "accC", ToString: "Carol"},
			},
			want: []wantInterval{
				{valueID: "accA", value: "Alice", known: true, provenance: ProvEvent, entered: "2024-01-01T00:00:00Z", exited: "2024-01-05T00:00:00Z"},
				{valueID: "accB", value: "Bob", known: true, provenance: ProvEvent, entered: "2024-01-05T00:00:00Z", exited: "2024-01-09T00:00:00Z"},
				{valueID: "accC", value: "Carol", known: true, provenance: ProvEvent, entered: "2024-01-09T00:00:00Z"},
			},
			seeded: true,
		},
		{
			name: "empty-sentinel-assignee-seeded-known", field: "assignee",
			created: "2024-01-01T00:00:00Z", complete: true,
			events: []ScalarEvent{
				// Created unassigned (empty from), later assigned. Complete history makes the
				// leading empty value a KNOWN "unassigned" interval, not an unknown gap.
				{ID: "a", ChangedAt: mustTime(t, "2024-01-06T00:00:00Z"), FromValue: "", FromString: "", ToValue: "accB", ToString: "Bob"},
			},
			want: []wantInterval{
				{valueID: "", value: "", known: true, provenance: ProvEvent, entered: "2024-01-01T00:00:00Z", exited: "2024-01-06T00:00:00Z"},
				{valueID: "accB", value: "Bob", known: true, provenance: ProvEvent, entered: "2024-01-06T00:00:00Z"},
			},
			seeded: true,
		},
		{
			name: "display-rename-keeps-stable-id", field: "assignee",
			created: "2024-01-01T00:00:00Z", complete: true,
			events: []ScalarEvent{
				{ID: "a", ChangedAt: mustTime(t, "2024-01-05T00:00:00Z"), FromValue: "accA", FromString: "Alice", ToValue: "accB", ToString: "Bob Smith"},
				// Same account id, display only renamed: boundary recorded, id stays stable.
				{ID: "b", ChangedAt: mustTime(t, "2024-01-08T00:00:00Z"), FromValue: "accB", FromString: "Bob Smith", ToValue: "accB", ToString: "Robert Smith"},
			},
			want: []wantInterval{
				{valueID: "accA", value: "Alice", known: true, provenance: ProvEvent, entered: "2024-01-01T00:00:00Z", exited: "2024-01-05T00:00:00Z"},
				{valueID: "accB", value: "Bob Smith", known: true, provenance: ProvEvent, entered: "2024-01-05T00:00:00Z", exited: "2024-01-08T00:00:00Z"},
				{valueID: "accB", value: "Robert Smith", known: true, provenance: ProvEvent, entered: "2024-01-08T00:00:00Z"},
			},
			seeded: true,
		},
		{
			name: "noop-change-dropped", field: "priority",
			created: "2024-01-01T00:00:00Z", complete: true,
			events: []ScalarEvent{
				// Pure no-op (id and string unchanged) is suppressed before interval building.
				{ID: "x", ChangedAt: mustTime(t, "2024-01-04T00:00:00Z"), FromValue: "1", FromString: "High", ToValue: "1", ToString: "High"},
				{ID: "y", ChangedAt: mustTime(t, "2024-01-07T00:00:00Z"), FromValue: "1", FromString: "High", ToValue: "2", ToString: "Low"},
			},
			want: []wantInterval{
				{valueID: "1", value: "High", known: true, provenance: ProvEvent, entered: "2024-01-01T00:00:00Z", exited: "2024-01-07T00:00:00Z"},
				{valueID: "2", value: "Low", known: true, provenance: ProvEvent, entered: "2024-01-07T00:00:00Z"},
			},
			seeded: true,
		},
		{
			name: "equal-timestamp-tie-by-id", field: "priority",
			created: "2024-01-01T00:00:00Z", complete: true,
			events: []ScalarEvent{
				// Reversed input order; equal timestamps must resolve by stable id (1 before 2).
				{ID: "2", ChangedAt: mustTime(t, "2024-01-05T00:00:00Z"), FromValue: "b", FromString: "Mid", ToValue: "c", ToString: "High"},
				{ID: "1", ChangedAt: mustTime(t, "2024-01-05T00:00:00Z"), FromValue: "a", FromString: "Low", ToValue: "b", ToString: "Mid"},
			},
			want: []wantInterval{
				{valueID: "a", value: "Low", known: true, provenance: ProvEvent, entered: "2024-01-01T00:00:00Z", exited: "2024-01-05T00:00:00Z"},
				{valueID: "b", value: "Mid", known: true, provenance: ProvEvent, entered: "2024-01-05T00:00:00Z", exited: "2024-01-05T00:00:00Z"},
				{valueID: "c", value: "High", known: true, provenance: ProvEvent, entered: "2024-01-05T00:00:00Z"},
			},
			seeded: true, ambiguous: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hist := ScalarHistory{
				Key: "K-1", Project: "P", Field: tc.field,
				CreatedAt: mustTime(t, tc.created), HistoryComplete: tc.complete,
				CurrentValueID: tc.currentID, CurrentValue: tc.currentVal, CurrentKnown: tc.currentKnown,
				Events: tc.events,
			}
			res, err := ReplayScalar(hist, pol, asOf)
			if err != nil {
				t.Fatalf("ReplayScalar: %v", err)
			}
			if len(res.Intervals) != len(tc.want) {
				t.Fatalf("interval count = %d, want %d\n got %+v", len(res.Intervals), len(tc.want), res.Intervals)
			}
			for i, w := range tc.want {
				got := res.Intervals[i]
				if got.ValueID != w.valueID || got.Value != w.value {
					t.Errorf("iv %d value = (%q,%q), want (%q,%q)", i, got.ValueID, got.Value, w.valueID, w.value)
				}
				if got.Known != w.known || got.Provenance != w.provenance {
					t.Errorf("iv %d known/prov = (%v,%q), want (%v,%q)", i, got.Known, got.Provenance, w.known, w.provenance)
				}
				if !got.EnteredAt.Equal(mustTime(t, w.entered)) {
					t.Errorf("iv %d entered = %v, want %v", i, got.EnteredAt, w.entered)
				}
				if w.exited == "" {
					if !got.Open || got.ExitedAt != nil {
						t.Errorf("iv %d expected open, got open=%v exited=%v", i, got.Open, got.ExitedAt)
					}
				} else {
					if got.Open || got.ExitedAt == nil || !got.ExitedAt.Equal(mustTime(t, w.exited)) {
						t.Errorf("iv %d exited = %v, want %v", i, got.ExitedAt, w.exited)
					}
				}
				if got.Duration(asOf) < 0 {
					t.Errorf("iv %d negative duration", i)
				}
			}
			if res.NeverChanged != tc.neverChanged {
				t.Errorf("neverChanged = %v, want %v", res.NeverChanged, tc.neverChanged)
			}
			if res.Seeded != tc.seeded {
				t.Errorf("seeded = %v, want %v", res.Seeded, tc.seeded)
			}
			if res.Ambiguous != tc.ambiguous {
				t.Errorf("ambiguous = %v, want %v", res.Ambiguous, tc.ambiguous)
			}
			if res.HasUnknown != tc.hasUnknown {
				t.Errorf("hasUnknown = %v, want %v", res.HasUnknown, tc.hasUnknown)
			}
		})
	}
}

// Set-valued membership fields must be refused, never coerced into scalar intervals.
func TestReplayScalar_RefusesSetValued(t *testing.T) {
	pol := CompilePolicy(nil)
	for _, f := range []string{"Sprint", "sprint", "labels", "components", "Component/s"} {
		hist := ScalarHistory{Key: "K", Field: f, CreatedAt: mustTime(t, "2024-01-01T00:00:00Z"), HistoryComplete: true}
		if _, err := ReplayScalar(hist, pol, time.Now()); !errors.Is(err, ErrSetValuedField) {
			t.Errorf("field %q: err = %v, want ErrSetValuedField", f, err)
		}
	}
}

// Replay is deterministic and idempotent regardless of input event order.
func TestReplayScalar_DeterministicUnderShuffle(t *testing.T) {
	pol := CompilePolicy(nil)
	asOf := mustTime(t, "2024-03-01T00:00:00Z")
	base := ScalarHistory{
		Key: "K", Field: "assignee", CreatedAt: mustTime(t, "2024-01-01T00:00:00Z"), HistoryComplete: true,
		Events: []ScalarEvent{
			{ID: "1", ChangedAt: mustTime(t, "2024-01-05T00:00:00Z"), FromValue: "a", FromString: "A", ToValue: "b", ToString: "B"},
			{ID: "2", ChangedAt: mustTime(t, "2024-01-09T00:00:00Z"), FromValue: "b", FromString: "B", ToValue: "c", ToString: "C"},
			{ID: "3", ChangedAt: mustTime(t, "2024-01-12T00:00:00Z"), FromValue: "c", FromString: "C", ToValue: "d", ToString: "D"},
		},
	}
	a, _ := ReplayScalar(base, pol, asOf)
	rev := base
	rev.Events = []ScalarEvent{base.Events[2], base.Events[0], base.Events[1]}
	b, _ := ReplayScalar(rev, pol, asOf)
	if len(a.Intervals) != len(b.Intervals) {
		t.Fatalf("interval count differs under shuffle")
	}
	for i := range a.Intervals {
		if a.Intervals[i].ValueID != b.Intervals[i].ValueID || !a.Intervals[i].EnteredAt.Equal(b.Intervals[i].EnteredAt) {
			t.Fatalf("iv %d differs under shuffle", i)
		}
	}
}

func TestScalarTrackedFields_ExcludesStatusAndSetValued(t *testing.T) {
	pol := CompilePolicy(&config.MetricsConfig{
		TrackedFields: []string{"status", "priority", "assignee", "Sprint", "labels", "components", "Rank"},
	})
	got := ScalarTrackedFields(pol)
	want := []string{"priority", "assignee", "Rank"}
	if len(got) != len(want) {
		t.Fatalf("scalar fields = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scalar fields = %v, want %v", got, want)
		}
	}
}

// --- builder wiring: scalar intervals are persisted; set-valued are not scalar-replayed ---

func scalarFactPolicy(tracked []string) *Policy {
	return CompilePolicy(&config.MetricsConfig{
		StatusStages: map[string][]string{
			"todo":   {"Backlog"},
			"active": {"In Progress"},
			"done":   {"Closed"},
		},
		WIPStages:     []string{"active"},
		TrackedFields: tracked,
	})
}

// clField inserts a changelog row for an arbitrary field.
func clField(t *testing.T, database *db.DB, id, key, field, fromV, fromS, toV, toS, at string) {
	t.Helper()
	if err := database.InsertChangelog(&db.ChangelogEntry{
		ID: id, IssueKey: key, Field: field,
		FromValue: fromV, FromString: fromS, ToValue: toV, ToString: toS, ChangedAt: at,
	}); err != nil {
		t.Fatalf("InsertChangelog %s: %v", id, err)
	}
}

func TestBuildAll_ScalarIntervalsPersisted(t *testing.T) {
	database, store := newFactStore(t)
	pol := scalarFactPolicy([]string{"status", "priority", "assignee"})

	upIssue(t, database, "S-1", "PX", "Story", "In Progress", "In Progress",
		"2024-01-01T00:00:00Z", "", "", true)
	// priority Low -> High.
	clField(t, database, "p1", "S-1", "priority", "2", "Low", "1", "High", "2024-01-05T00:00:00Z")
	// assignee unassigned -> Bob.
	clField(t, database, "a1", "S-1", "assignee", "", "", "accB", "Bob", "2024-01-07T00:00:00Z")

	b := NewBuilder(store, pol)
	if _, err := b.BuildAll(asOf, nil); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}

	prio, err := store.Intervals(nil, "priority")
	if err != nil {
		t.Fatalf("Intervals priority: %v", err)
	}
	if len(prio) != 2 {
		t.Fatalf("priority intervals = %d, want 2: %+v", len(prio), prio)
	}
	if prio[0].Value != "Low" || prio[1].Value != "High" {
		t.Errorf("priority values = %q,%q want Low,High", prio[0].Value, prio[1].Value)
	}
	if prio[0].Stage != "" || prio[1].Stage != "" {
		t.Errorf("scalar intervals must have empty stage, got %q,%q", prio[0].Stage, prio[1].Stage)
	}
	if !prio[1].Open {
		t.Errorf("last priority interval should be open")
	}

	asg, err := store.Intervals(nil, "assignee")
	if err != nil {
		t.Fatalf("Intervals assignee: %v", err)
	}
	if len(asg) != 2 {
		t.Fatalf("assignee intervals = %d, want 2: %+v", len(asg), asg)
	}
	// Leading interval is a KNOWN unassigned value under complete history.
	if asg[0].Value != "" || asg[0].Provenance != ProvEvent {
		t.Errorf("assignee[0] = (%q,%q), want empty known (event)", asg[0].Value, asg[0].Provenance)
	}
	if asg[1].Value != "Bob" {
		t.Errorf("assignee[1] value = %q, want Bob", asg[1].Value)
	}

	// status intervals remain and carry stage semantics.
	stat, err := store.Intervals(nil, "status")
	if err != nil {
		t.Fatalf("Intervals status: %v", err)
	}
	if len(stat) == 0 || stat[len(stat)-1].Stage == "" {
		t.Errorf("status intervals must retain stage semantics: %+v", stat)
	}
}

// A set-valued field in tracked_fields must never be published as scalar intervals.
func TestBuildAll_SetValuedNotScalarReplayed(t *testing.T) {
	database, store := newFactStore(t)
	pol := scalarFactPolicy([]string{"status", "Sprint"})

	upIssue(t, database, "S-2", "PX", "Story", "In Progress", "In Progress",
		"2024-01-01T00:00:00Z", "", "", true)
	clField(t, database, "sp1", "S-2", "Sprint", "1", "Sprint 1", "1, 2", "Sprint 1, Sprint 2", "2024-01-05T00:00:00Z")

	b := NewBuilder(store, pol)
	if _, err := b.BuildAll(asOf, nil); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}
	sprintIvs, err := store.Intervals(nil, "Sprint")
	if err != nil {
		t.Fatalf("Intervals Sprint: %v", err)
	}
	if len(sprintIvs) != 0 {
		t.Fatalf("Sprint must not be scalar-replayed, got %d intervals: %+v", len(sprintIvs), sprintIvs)
	}
}

// A history-only update replaces exactly the affected issue's scalar intervals.
func TestIncremental_ReplacesScalarIntervals(t *testing.T) {
	database, store := newFactStore(t)
	pol := scalarFactPolicy([]string{"status", "priority"})

	upIssue(t, database, "S-3", "PX", "Story", "In Progress", "In Progress",
		"2024-01-01T00:00:00Z", "", "", true)
	clField(t, database, "p1", "S-3", "priority", "2", "Low", "1", "High", "2024-01-05T00:00:00Z")

	b := NewBuilder(store, pol)
	if _, err := b.BuildAll(asOf, nil); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}
	if ivs, _ := store.Intervals(nil, "priority"); len(ivs) != 2 {
		t.Fatalf("initial priority intervals = %d, want 2", len(ivs))
	}

	// New priority change arrives; incremental must replace, not duplicate.
	clField(t, database, "p2", "S-3", "priority", "1", "High", "3", "Highest", "2024-01-09T00:00:00Z")
	if _, err := b.Incremental(asOf, []string{"S-3"}); err != nil {
		t.Fatalf("Incremental: %v", err)
	}
	ivs, _ := store.Intervals(nil, "priority")
	if len(ivs) != 3 {
		t.Fatalf("after incremental priority intervals = %d, want 3: %+v", len(ivs), ivs)
	}
	if ivs[2].Value != "Highest" || !ivs[2].Open {
		t.Errorf("latest priority interval = %+v, want open Highest", ivs[2])
	}
}
