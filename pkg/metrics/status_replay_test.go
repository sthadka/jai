package metrics

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/sthadka/jai/internal/config"
)

// --- shared fixture loader (consumed by replay and reconcile tests) ---

type fixtureDoc struct {
	Config           fixtureConfig           `json:"config"`
	AsOf             string                  `json:"as_of"`
	Cohorts          fixtureCohorts          `json:"cohorts"`
	ReconcileSummary fixtureReconcileSummary `json:"reconcile_summary"`
	Cases            []fixtureCase           `json:"cases"`
}

type fixtureConfig struct {
	StatusStageIDs  map[string]string   `json:"status_stage_ids"`
	StatusStages    map[string][]string `json:"status_stages"`
	WIPStages       []string            `json:"wip_stages"`
	DoneResolutions []string            `json:"done_resolutions"`
	TypeClasses     map[string][]string `json:"type_classes"`
	Ratio           *struct {
		Numerator   string `json:"numerator"`
		Denominator string `json:"denominator"`
		Cohort      string `json:"cohort"`
	} `json:"ratio"`
}

type fixtureCohorts struct {
	TotalIssues                   int `json:"total_issues"`
	GenuineDone                   int `json:"genuine_done"`
	GenuineDoneWithActiveInterval int `json:"genuine_done_with_active_interval"`
	GenuineDoneUnknownCycle       int `json:"genuine_done_unknown_cycle"`
}

type fixtureReconcileSummary struct {
	Match     int                        `json:"match"`
	Mismatch  int                        `json:"mismatch"`
	Unknown   int                        `json:"unknown"`
	Trusted   bool                       `json:"trusted"`
	ByProject map[string]fixtureRecounts `json:"by_project"`
}

type fixtureRecounts struct {
	Match    int `json:"match"`
	Mismatch int `json:"mismatch"`
	Unknown  int `json:"unknown"`
}

type fixtureEvent struct {
	ID         string `json:"id"`
	ChangedAt  string `json:"changed_at"`
	FromValue  string `json:"from_value"`
	FromString string `json:"from_string"`
	ToValue    string `json:"to_value"`
	ToString   string `json:"to_string"`
}

type fixtureCurrent struct {
	StatusID       string `json:"status_id"`
	Status         string `json:"status"`
	StatusCategory string `json:"status_category"`
	Resolution     string `json:"resolution"`
	Resolved       string `json:"resolved"`
}

type fixtureInterval struct {
	Value      string  `json:"value"`
	ValueID    string  `json:"value_id"`
	Stage      string  `json:"stage"`
	Provenance string  `json:"provenance"`
	EnteredAt  string  `json:"entered_at"`
	ExitedAt   *string `json:"exited_at"`
	Open       bool    `json:"open"`
}

type fixtureExpected struct {
	Intervals         []fixtureInterval `json:"intervals"`
	CurrentStage      string            `json:"current_stage"`
	KnownCount        int               `json:"known_count"`
	UnknownCount      int               `json:"unknown_count"`
	WIP               bool              `json:"wip"`
	NeverChanged      bool              `json:"never_changed"`
	Ambiguous         bool              `json:"ambiguous"`
	Seeded            bool              `json:"seeded"`
	HasActiveInterval bool              `json:"has_active_interval"`
	Reconcile         struct {
		Outcome string `json:"outcome"`
		Reason  string `json:"reason"`
	} `json:"reconcile"`
}

type fixtureCase struct {
	Name            string          `json:"name"`
	Project         string          `json:"project"`
	Type            string          `json:"type"`
	CreatedAt       string          `json:"created_at"`
	HistoryComplete bool            `json:"history_complete"`
	Events          []fixtureEvent  `json:"events"`
	Current         fixtureCurrent  `json:"current"`
	Expected        fixtureExpected `json:"expected"`
}

func loadFlowCases(t *testing.T) *fixtureDoc {
	t.Helper()
	data, err := os.ReadFile("testdata/flow_cases.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var doc fixtureDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}
	return &doc
}

func ftime(t *testing.T, s string) time.Time {
	t.Helper()
	if s == "" {
		return time.Time{}
	}
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad time %q: %v", s, err)
	}
	return tt
}

func policyFromFixture(fc fixtureConfig) *Policy {
	mc := &config.MetricsConfig{
		StatusStageIDs:  fc.StatusStageIDs,
		StatusStages:    fc.StatusStages,
		WIPStages:       fc.WIPStages,
		DoneResolutions: fc.DoneResolutions,
		TypeClasses:     fc.TypeClasses,
	}
	if fc.Ratio != nil {
		mc.Ratio = &config.RatioConfig{Numerator: fc.Ratio.Numerator, Denominator: fc.Ratio.Denominator, Cohort: fc.Ratio.Cohort}
	}
	return CompilePolicy(mc)
}

func issueFromCase(t *testing.T, c fixtureCase) IssueHistory {
	t.Helper()
	ih := IssueHistory{
		Key:                   c.Name,
		Project:               c.Project,
		CreatedAt:             ftime(t, c.CreatedAt),
		HistoryComplete:       c.HistoryComplete,
		CurrentStatusID:       c.Current.StatusID,
		CurrentStatus:         c.Current.Status,
		CurrentStatusCategory: c.Current.StatusCategory,
	}
	for _, e := range c.Events {
		ih.Events = append(ih.Events, StatusEvent{
			ID:         e.ID,
			ChangedAt:  ftime(t, e.ChangedAt),
			FromValue:  e.FromValue,
			FromString: e.FromString,
			ToValue:    e.ToValue,
			ToString:   e.ToString,
		})
	}
	return ih
}

// --- replay tests ---

func TestReplay_Fixtures(t *testing.T) {
	doc := loadFlowCases(t)
	pol := policyFromFixture(doc.Config)
	if err := pol.Err(); err != nil {
		t.Fatalf("fixture config rejected: %v", err)
	}
	asOf := ftime(t, doc.AsOf)

	for _, c := range doc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			res := ReplayStatus(issueFromCase(t, c), pol, asOf)
			exp := c.Expected

			if len(res.Intervals) != len(exp.Intervals) {
				t.Fatalf("interval count = %d, want %d\n got: %+v", len(res.Intervals), len(exp.Intervals), res.Intervals)
			}
			for i, want := range exp.Intervals {
				got := res.Intervals[i]
				if got.Value != want.Value || got.ValueID != want.ValueID {
					t.Errorf("interval %d value = (%q,%q), want (%q,%q)", i, got.Value, got.ValueID, want.Value, want.ValueID)
				}
				if got.Stage != want.Stage || got.Provenance != want.Provenance {
					t.Errorf("interval %d stage/prov = (%q,%q), want (%q,%q)", i, got.Stage, got.Provenance, want.Stage, want.Provenance)
				}
				if !got.EnteredAt.Equal(ftime(t, want.EnteredAt)) {
					t.Errorf("interval %d entered = %v, want %v", i, got.EnteredAt, want.EnteredAt)
				}
				if want.Open {
					if !got.Open || got.ExitedAt != nil {
						t.Errorf("interval %d expected open, got open=%v exited=%v", i, got.Open, got.ExitedAt)
					}
				} else {
					if got.Open || got.ExitedAt == nil || !got.ExitedAt.Equal(ftime(t, *want.ExitedAt)) {
						t.Errorf("interval %d exited = %v, want %v", i, got.ExitedAt, deref(want.ExitedAt))
					}
				}
				// No negative duration ever.
				if got.Duration(asOf) < 0 {
					t.Errorf("interval %d negative duration", i)
				}
			}

			if res.CurrentStage != exp.CurrentStage {
				t.Errorf("current stage = %q, want %q", res.CurrentStage, exp.CurrentStage)
			}
			if res.KnownCount != exp.KnownCount || res.UnknownCount != exp.UnknownCount {
				t.Errorf("counts = (known %d, unknown %d), want (%d, %d)", res.KnownCount, res.UnknownCount, exp.KnownCount, exp.UnknownCount)
			}
			if res.NeverChanged != exp.NeverChanged {
				t.Errorf("never_changed = %v, want %v", res.NeverChanged, exp.NeverChanged)
			}
			if res.Ambiguous != exp.Ambiguous {
				t.Errorf("ambiguous = %v, want %v", res.Ambiguous, exp.Ambiguous)
			}
			if res.Seeded != exp.Seeded {
				t.Errorf("seeded = %v, want %v", res.Seeded, exp.Seeded)
			}
			if res.HasActiveInterval != exp.HasActiveInterval {
				t.Errorf("has_active_interval = %v, want %v", res.HasActiveInterval, exp.HasActiveInterval)
			}
			// WIP: current open interval is a WIP stage.
			if gotWIP := pol.IsWIPStage(res.CurrentStage); gotWIP != exp.WIP {
				t.Errorf("wip = %v, want %v", gotWIP, exp.WIP)
			}
		})
	}
}

// Unknown periods must not be treated as zero WIP: they are a distinct unknown bucket.
func TestReplay_UnknownNotZeroWIP(t *testing.T) {
	doc := loadFlowCases(t)
	pol := policyFromFixture(doc.Config)
	asOf := ftime(t, doc.AsOf)
	for _, c := range doc.Cases {
		res := ReplayStatus(issueFromCase(t, c), pol, asOf)
		for _, iv := range res.Intervals {
			if iv.Stage == StageUnknown && pol.IsWIPStage(iv.Stage) {
				t.Fatalf("%s: unknown stage must never be a WIP stage", c.Name)
			}
		}
	}
}

// Replay is deterministic and idempotent, including under shuffled input order.
func TestReplay_DeterministicAndIdempotent(t *testing.T) {
	doc := loadFlowCases(t)
	pol := policyFromFixture(doc.Config)
	asOf := ftime(t, doc.AsOf)
	for _, c := range doc.Cases {
		ih := issueFromCase(t, c)
		a := ReplayStatus(ih, pol, asOf)
		b := ReplayStatus(ih, pol, asOf)
		if !sameIntervals(a.Intervals, b.Intervals) {
			t.Fatalf("%s: replay not idempotent", c.Name)
		}
		// Reverse event order -> same reconstructed series.
		rev := ih
		rev.Events = append([]StatusEvent(nil), ih.Events...)
		for i, j := 0, len(rev.Events)-1; i < j; i, j = i+1, j-1 {
			rev.Events[i], rev.Events[j] = rev.Events[j], rev.Events[i]
		}
		cR := ReplayStatus(rev, pol, asOf)
		if !sameIntervals(a.Intervals, cR.Intervals) {
			t.Fatalf("%s: replay order-dependent", c.Name)
		}
	}
}

// Open intervals age as as_of advances with no new sync.
func TestReplay_OpenIntervalAges(t *testing.T) {
	doc := loadFlowCases(t)
	pol := policyFromFixture(doc.Config)
	asOf := ftime(t, doc.AsOf)
	var c fixtureCase
	for _, cc := range doc.Cases {
		if cc.Name == "initially-active" {
			c = cc
		}
	}
	res := ReplayStatus(issueFromCase(t, c), pol, asOf)
	open := res.Intervals[len(res.Intervals)-1]
	if !open.Open {
		t.Fatal("expected open final interval")
	}
	later := asOf.Add(10 * 24 * time.Hour)
	if open.Duration(later) <= open.Duration(asOf) {
		t.Error("open interval did not age with a later as_of")
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func sameIntervals(a, b []Interval) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Value != b[i].Value || a[i].ValueID != b[i].ValueID || a[i].Stage != b[i].Stage ||
			a[i].Provenance != b[i].Provenance || !a[i].EnteredAt.Equal(b[i].EnteredAt) || a[i].Open != b[i].Open {
			return false
		}
		if (a[i].ExitedAt == nil) != (b[i].ExitedAt == nil) {
			return false
		}
		if a[i].ExitedAt != nil && !a[i].ExitedAt.Equal(*b[i].ExitedAt) {
			return false
		}
	}
	return true
}
