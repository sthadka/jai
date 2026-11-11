package metrics

import (
	"strings"
	"testing"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/query"
)

// TestGenerateFragments_QuoteSafety verifies tenant values containing single
// quotes are doubled, so a value like "Won't Do" cannot break or inject SQL.
func TestGenerateFragments_QuoteSafety(t *testing.T) {
	pol := CompilePolicy(&config.MetricsConfig{
		StatusStages:    map[string][]string{"active": {"In O'Brien Review"}},
		WIPStages:       []string{"active"},
		DoneResolutions: []string{"Won't Do", "Done"},
	})
	frags := GenerateFragments(pol)

	gd := frags[FragmentGenuineDone]
	if !strings.Contains(gd, "'Won''t Do'") {
		t.Fatalf("genuine_done did not escape apostrophe: %s", gd)
	}
	// An un-doubled apostrophe would leave an odd number of quote characters.
	if strings.Count(gd, "'")%2 != 0 {
		t.Fatalf("genuine_done has unbalanced quotes (injection risk): %s", gd)
	}

	ssm := frags[FragmentStatusStageMap]
	if !strings.Contains(ssm, "'In O''Brien Review'") {
		t.Fatalf("status_stage_map did not escape apostrophe: %s", ssm)
	}
	if strings.Count(ssm, "'")%2 != 0 {
		t.Fatalf("status_stage_map has unbalanced quotes: %s", ssm)
	}
}

// TestGenerateFragments_TenantNeutral verifies fragments track config changes
// (no tenant literal is baked into the generator): a renamed stage status
// appears in the output, and done_resolutions unset degrades to all-resolved.
func TestGenerateFragments_TenantNeutral(t *testing.T) {
	withDone := GenerateFragments(CompilePolicy(&config.MetricsConfig{
		DoneResolutions: []string{"Shipped"},
	}))
	if g := withDone[FragmentGenuineDone]; !strings.Contains(g, "'Shipped'") {
		t.Fatalf("genuine_done ignored configured resolution: %s", g)
	}

	noDone := GenerateFragments(CompilePolicy(&config.MetricsConfig{}))
	if g := noDone[FragmentGenuineDone]; g != "(resolved IS NOT NULL AND TRIM(resolved) <> '')" {
		t.Fatalf("unset done_resolutions should be all-resolved, got: %s", g)
	}

	remapped := GenerateFragments(CompilePolicy(&config.MetricsConfig{
		StatusStages: map[string][]string{"active": {"Coding"}},
		WIPStages:    []string{"active"},
	}))
	if g := remapped[FragmentStatusStageMap]; !strings.Contains(g, "'Coding'") {
		t.Fatalf("status_stage_map ignored configured status name: %s", g)
	}
}

// TestGenerateFragments_FailClosed verifies a structurally-invalid stage map
// (a duplicate status mapping) emits no apparently-valid status-based fragment,
// while the independent genuine_done fragment stays available.
func TestGenerateFragments_FailClosed(t *testing.T) {
	pol := CompilePolicy(&config.MetricsConfig{
		// "Review" mapped to two stages -> rejects MetricStatusStages (fail closed).
		StatusStages: map[string][]string{
			"active": {"Review"},
			"todo":   {"Review"},
		},
		WIPStages: []string{"active"},
	})
	if pol.Enabled(MetricStatusStages) {
		t.Fatalf("test setup: expected status_stages to be rejected")
	}
	frags := GenerateFragments(pol)
	if _, ok := frags[FragmentStatusStageMap]; ok {
		t.Fatalf("status_stage_map must not be emitted when the stage map is rejected")
	}
	if _, ok := frags[FragmentActiveStages]; ok {
		t.Fatalf("active_stages must not be emitted when the stage map is rejected")
	}
	if _, ok := frags[FragmentGenuineDone]; !ok {
		t.Fatalf("genuine_done should remain available independent of the stage map")
	}
}

// TestGeneratedViews_EqualMetricCounts verifies a generated view and the shared
// metric service produce equal counts for the same scope (resolution mix), and
// that the fragment-backed delivered/stage views reflect the completion policy
// and surface the unknown stage bucket.
func TestGeneratedViews_EqualMetricCounts(t *testing.T) {
	database, _ := newFactStore(t)
	// Delivered (genuine) resolved issue.
	upIssue(t, database, "S-1", "PS", "Story", "Closed", "Done", day(1), day(10), "Done", false)
	// Resolved but non-delivery resolution.
	upIssue(t, database, "S-2", "PS", "Bug", "Closed", "Done", day(2), day(11), "Won't Do", false)
	// Open, active WIP.
	upIssue(t, database, "S-3", "PS", "Story", "In Progress", "In Progress", day(3), "", "", false)
	// Open, status that maps to no stage and has no category -> unknown.
	upIssue(t, database, "S-4", "PS", "Story", "Mystery", "", day(4), "", "", false)

	mc := &config.MetricsConfig{
		StatusStages:    map[string][]string{"active": {"In Progress"}, "done": {"Closed"}},
		WIPStages:       []string{"active"},
		DoneResolutions: []string{"Done"},
	}
	pol := CompilePolicy(mc)
	svc := NewService(database, NewStore(database, ""), pol, nil)

	cfg := &config.Config{Metrics: mc}
	cfg.MergeGeneratedSnippets(GenerateFragments(pol))
	cfg.MergeGeneratedViews(GenerateViews(pol))
	eng := query.New(database, cfg)

	// resolution_mix: view counts must equal the metric counts exactly.
	metricRes, err := svc.Compute(Query{Metric: MetricResolutionMix})
	if err != nil {
		t.Fatalf("resolution_mix compute: %v", err)
	}
	viewCounts := runViewCounts(t, eng, cfg, "metrics-resolution-mix", "resolution", "count")
	if !equalCounts(viewCounts, metricRes.Counts) {
		t.Fatalf("view counts %v != metric counts %v", viewCounts, metricRes.Counts)
	}

	// metrics-delivered: genuine_done filter counts only the delivered issue.
	if n := runViewScalar(t, eng, cfg, "metrics-delivered"); n != 1 {
		t.Fatalf("metrics-delivered = %d, want 1 (genuine-done excludes Won't Do and open)", n)
	}

	// metrics-wip: active_stages counts only the active WIP issue.
	if n := runViewScalar(t, eng, cfg, "metrics-wip"); n != 1 {
		t.Fatalf("metrics-wip = %d, want 1", n)
	}

	// stage breakdown must surface the unknown bucket (never dropped).
	stages := runViewCounts(t, eng, cfg, "metrics-stage-breakdown", "stage", "count")
	if stages[StageUnknown] != 1 {
		t.Fatalf("stage breakdown missing unknown bucket: %v", stages)
	}
	if stages[StageActive] != 1 || stages[StageDone] != 2 {
		t.Fatalf("stage breakdown counts wrong: %v", stages)
	}
}

func runViewCounts(t *testing.T, eng *query.Engine, cfg *config.Config, view, keyCol, valCol string) map[string]int {
	t.Helper()
	v := cfg.ViewByName(view)
	if v == nil {
		t.Fatalf("generated view %q missing", view)
	}
	res, err := eng.Execute(v.Query)
	if err != nil {
		t.Fatalf("execute %q: %v", view, err)
	}
	ki, vi := colIndex(res.Columns, keyCol), colIndex(res.Columns, valCol)
	if ki < 0 || vi < 0 {
		t.Fatalf("view %q columns %v missing %q/%q", view, res.Columns, keyCol, valCol)
	}
	out := map[string]int{}
	for _, row := range res.Rows {
		out[toString(row[ki])] = toInt(row[vi])
	}
	return out
}

func runViewScalar(t *testing.T, eng *query.Engine, cfg *config.Config, view string) int {
	t.Helper()
	v := cfg.ViewByName(view)
	if v == nil {
		t.Fatalf("generated view %q missing", view)
	}
	res, err := eng.Execute(v.Query)
	if err != nil {
		t.Fatalf("execute %q: %v", view, err)
	}
	if len(res.Rows) != 1 || len(res.Rows[0]) != 1 {
		t.Fatalf("view %q did not return a scalar: cols=%v rows=%v", view, res.Columns, res.Rows)
	}
	return toInt(res.Rows[0][0])
}

func colIndex(cols []string, name string) int {
	for i, c := range cols {
		if c == name {
			return i
		}
	}
	return -1
}

func equalCounts(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func toString(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	default:
		return ""
	}
}

func toInt(v interface{}) int {
	switch x := v.(type) {
	case int64:
		return int(x)
	case int:
		return x
	case float64:
		return int(x)
	default:
		return 0
	}
}
