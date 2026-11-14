package metrics

import (
	"testing"
	"time"

	"github.com/sthadka/jai/internal/config"
)

func tm(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func tptr(s string) *time.Time { t := tm(s); return &t }

func fcov(s float64) *float64 { return &s }

func coveragePolicy() *Policy {
	return CompilePolicy(&config.MetricsConfig{
		StatusStages: map[string][]string{
			"todo":   {"Backlog"},
			"active": {"In Progress"},
			"done":   {"Closed"},
		},
		WIPStages:     []string{"active"},
		TrackedFields: []string{"status", "priority", "Rank"},
	})
}

func findProject(r *CoverageReport, p string) *ProjectCoverage {
	for i := range r.Projects {
		if r.Projects[i].Project == p {
			return &r.Projects[i]
		}
	}
	return nil
}

// Each history state yields a distinct, correct count per project.
func TestComputeCoverage_HistoryStates(t *testing.T) {
	cutoff := tm("2024-05-01T00:00:00Z")
	in := CoverageInput{
		StaleCutoff: cutoff,
		Issues: []IssueCoverageRow{
			// ALPHA: never-checked (no changelog)
			{Key: "A-1", Project: "ALPHA", Status: "Backlog", Updated: tm("2024-06-01T00:00:00Z"), ChangelogSyncedAt: nil, HasChangelog: false},
			// ALPHA: updated-after-check -> pending
			{Key: "A-2", Project: "ALPHA", Status: "In Progress", Updated: tm("2024-06-10T00:00:00Z"), ChangelogSyncedAt: tptr("2024-06-05T00:00:00Z"), HasChangelog: true},
			// ALPHA: old-but-current (checked before cutoff, updated <= synced) -> NOT pending, but stale check
			{Key: "A-3", Project: "ALPHA", Status: "Closed", Updated: tm("2024-01-01T00:00:00Z"), ChangelogSyncedAt: tptr("2024-02-01T00:00:00Z"), HasChangelog: true, StoryPoints: fcov(3)},
			// ALPHA: checked-current recent (after cutoff) -> not pending, not stale
			{Key: "A-4", Project: "ALPHA", Status: "In Progress", Updated: tm("2024-06-01T00:00:00Z"), ChangelogSyncedAt: tptr("2024-06-02T00:00:00Z"), HasChangelog: true},
			// BETA: zero-history excluded cohort, unmapped status
			{Key: "B-1", Project: "BETA", Status: "Escalated", Updated: tm("2024-06-01T00:00:00Z"), ChangelogSyncedAt: tptr("2024-06-02T00:00:00Z"), HasChangelog: false},
		},
	}
	r := ComputeCoverage(in, coveragePolicy())

	alpha := findProject(r, "ALPHA")
	if alpha == nil {
		t.Fatal("no ALPHA")
	}
	checks := map[string]int{
		"Issues":                alpha.Issues,
		"NeverChecked":          alpha.NeverChecked,
		"UpdatedAfterCheck":     alpha.UpdatedAfterCheck,
		"CheckedCurrent":        alpha.CheckedCurrent,
		"StaleCheck":            alpha.StaleCheck,
		"PendingHistoryBacklog": alpha.PendingHistoryBacklog,
		"ZeroHistory":           alpha.ZeroHistory,
		"WithChangelog":         alpha.WithChangelog,
		"StoryPointsPresent":    alpha.StoryPointsPresent,
	}
	want := map[string]int{
		"Issues":                4,
		"NeverChecked":          1, // A-1
		"UpdatedAfterCheck":     1, // A-2
		"CheckedCurrent":        2, // A-3, A-4
		"StaleCheck":            1, // A-3 (checked before cutoff)
		"PendingHistoryBacklog": 2, // A-1 + A-2
		"ZeroHistory":           1, // A-1
		"WithChangelog":         3, // A-2,A-3,A-4
		"StoryPointsPresent":    1, // A-3
	}
	for k, v := range want {
		if checks[k] != v {
			t.Errorf("ALPHA %s = %d, want %d", k, checks[k], v)
		}
	}
	if alpha.AnyHistoryCoveragePct != 75 {
		t.Errorf("ALPHA coverage pct = %v, want 75", alpha.AnyHistoryCoveragePct)
	}

	beta := findProject(r, "BETA")
	if beta.ZeroHistory != 1 || beta.WithChangelog != 0 {
		t.Errorf("BETA history wrong: %+v", beta)
	}
	if len(beta.UnmappedStatuses) != 1 || beta.UnmappedStatuses[0] != "Escalated" {
		t.Errorf("BETA unmapped statuses = %v, want [Escalated]", beta.UnmappedStatuses)
	}
}

// Coverage must never claim completeness; caveats are always present.
func TestComputeCoverage_CaveatsAlwaysPresent(t *testing.T) {
	r := ComputeCoverage(CoverageInput{}, coveragePolicy())
	if len(r.Caveats) == 0 {
		t.Fatal("expected standing caveats")
	}
	var mentionsHistory bool
	for _, c := range r.Caveats {
		if contains(c, "prove complete history") {
			mentionsHistory = true
		}
	}
	if !mentionsHistory {
		t.Error("caveats must state coverage does not prove complete history")
	}
}

func TestComputeCoverage_EmptyCohortVisible(t *testing.T) {
	in := CoverageInput{
		Freshness: map[string]SourceFreshness{
			"GAMMA": {LastSyncTime: tptr("2024-06-01T00:00:00Z")},
		},
	}
	r := ComputeCoverage(in, coveragePolicy())
	g := findProject(r, "GAMMA")
	if g == nil || !g.Empty || !g.FreshnessKnown {
		t.Fatalf("empty cohort with freshness should stay visible: %+v", g)
	}
}

func TestComputeCoverage_AbsentTrackedFields(t *testing.T) {
	in := CoverageInput{
		ChangelogFields: map[string]bool{"status": true, "priority": true}, // Rank absent
	}
	r := ComputeCoverage(in, coveragePolicy())
	if len(r.AbsentTrackedFields) != 1 || r.AbsentTrackedFields[0] != "Rank" {
		t.Errorf("absent tracked fields = %v, want [Rank]", r.AbsentTrackedFields)
	}
}

// Sprint/dev-info/story-point gating reported independently.
func TestComputeCoverage_IndependentGating(t *testing.T) {
	tests := []struct {
		name            string
		in              CoverageInput
		wantSprintGated bool
		wantDevGated    bool
		wantSPGated     bool
	}{
		{
			name: "all gated by config",
			in: CoverageInput{
				Issues:        []IssueCoverageRow{{Key: "A-1", Project: "ALPHA", Status: "Backlog", Updated: tm("2024-06-01T00:00:00Z")}},
				SprintRows:    0,
				DevLinkRows:   0,
				SyncSprints:   false,
				SyncDevInfo:   false,
				SourcesAllJQL: true,
			},
			wantSprintGated: true, wantDevGated: true, wantSPGated: true,
		},
		{
			name: "data present, nothing gated",
			in: CoverageInput{
				Issues:      []IssueCoverageRow{{Key: "A-1", Project: "ALPHA", Status: "Backlog", Updated: tm("2024-06-01T00:00:00Z"), StoryPoints: fcov(5)}},
				SprintRows:  10,
				DevLinkRows: 4,
				SyncSprints: true,
				SyncDevInfo: true,
			},
			wantSprintGated: false, wantDevGated: false, wantSPGated: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := ComputeCoverage(tc.in, coveragePolicy())
			if r.Sprint.Gated != tc.wantSprintGated {
				t.Errorf("sprint gated = %v (%s), want %v", r.Sprint.Gated, r.Sprint.Reason, tc.wantSprintGated)
			}
			if r.DevInfo.Gated != tc.wantDevGated {
				t.Errorf("devinfo gated = %v, want %v", r.DevInfo.Gated, tc.wantDevGated)
			}
			if r.StoryPointsGap.Gated != tc.wantSPGated {
				t.Errorf("storypoints gated = %v, want %v", r.StoryPointsGap.Gated, tc.wantSPGated)
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
