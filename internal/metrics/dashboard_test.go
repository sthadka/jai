package metrics

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/sthadka/jai/internal/config"
)

// dashboardPolicy combines the fact stages (todo/active/done) with type classes and a ratio
// so every curated dashboard section has a configured path to exercise.
func dashboardPolicy() *Policy {
	return CompilePolicy(&config.MetricsConfig{
		StatusStages: map[string][]string{
			"todo":   {"Backlog"},
			"active": {"In Progress"},
			"done":   {"Closed"},
		},
		WIPStages:       []string{"active"},
		DoneResolutions: []string{"Done"},
		TypeClasses: map[string][]string{
			"bug":     {"Bug"},
			"feature": {"Story", "Feature"},
		},
		Ratio: &config.RatioConfig{Numerator: "bug", Denominator: "feature", Cohort: "completed"},
	})
}

// extractData pulls the embedded JSON out of the dashboard HTML and decodes it. The embedded
// block is HTML-escaped (\u003c etc.), which json.Unmarshal reverses, so this also proves the
// embedded data round-trips.
func extractData(t *testing.T, html string) map[string]interface{} {
	t.Helper()
	const open = `<script id="dashboard-data" type="application/json">`
	i := strings.Index(html, open)
	if i < 0 {
		t.Fatalf("no embedded dashboard-data block")
	}
	rest := html[i+len(open):]
	j := strings.Index(rest, "</script>")
	if j < 0 {
		t.Fatalf("unterminated dashboard-data block")
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(rest[:j]), &m); err != nil {
		t.Fatalf("embedded data is not valid JSON: %v", err)
	}
	return m
}

func sectionsByID(m map[string]interface{}) map[string]map[string]interface{} {
	out := map[string]map[string]interface{}{}
	secs, _ := m["sections"].([]interface{})
	for _, s := range secs {
		if sm, ok := s.(map[string]interface{}); ok {
			if id, ok := sm["id"].(string); ok {
				out[id] = sm
			}
		}
	}
	return out
}

// seedBug creates a resolved genuine-done bug (for the ratio/resolution/throughput cohorts).
func seedBug(t *testing.T, store *Store, key string, resolvedDay int) {
	t.Helper()
	database := store.db
	upIssue(t, database, key, "PC", "Bug", "Closed", "Done", day(1), day(resolvedDay), "Done", true)
	cl(t, database, key+"-a", key, "Backlog", "In Progress", day(2))
	cl(t, database, key+"-b", key, "In Progress", "Closed", day(resolvedDay))
}

// TestDashboard_SelfContainedOffline is the core offline invariant: the exported HTML embeds
// both the computed data and the viz renderer, and carries ZERO external/CDN references and
// NO runtime file-fetch. This is the property a reviewer would otherwise verify by opening the
// file in a browser with networking disabled.
func TestDashboard_SelfContainedOffline(t *testing.T) {
	store, svc := buildService(t, dashboardPolicy())
	for k := 1; k <= 3; k++ {
		seedCycle(t, store, "C-"+itoa(k), 2, 5+k)
	}
	seedBug(t, store, "B-1", 7)
	// A hostile, Jira-controlled string that MUST be rendered inert (via the workload assignee
	// bucket). If it leaked unescaped it would be a stored-XSS payload in the export.
	upIssueA(t, store.db, "H-1", "PC", "Story", "In Progress", "In Progress", day(3), "", "",
		`</script><img src=x onerror=alert(1)>`)
	rebuild(t, store, dashboardPolicy())

	out, err := ExportDashboard(svc, DashboardOptions{Projects: []string{"PC"}, AsOf: asOf})
	if err != nil {
		t.Fatalf("ExportDashboard: %v", err)
	}
	html := string(out)

	// 1. No external/CDN references and no runtime fetch primitives anywhere in the artifact.
	// The SVG namespace URI is an inert XML identifier (never fetched by any browser), so it is
	// allow-listed before scanning for bare protocols.
	scan := strings.ReplaceAll(html, "http://www.w3.org/2000/svg", "")
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`https?://`),
		regexp.MustCompile(`(?i)src\s*=\s*["']\s*//`), // protocol-relative src
		regexp.MustCompile(`(?i)(src|href)\s*=\s*["']\s*https?:`),
		regexp.MustCompile(`(?i)@import`),
		regexp.MustCompile(`(?i)url\(\s*["']?https?:`),
		regexp.MustCompile(`(?i)\bfetch\s*\(`),
		regexp.MustCompile(`XMLHttpRequest`),
		regexp.MustCompile(`(?i)\bimport\s*\(`), // dynamic ES module import
		regexp.MustCompile(`(?i)cdn\.`),
		regexp.MustCompile(`(?i)integrity\s*=`), // only used with external subresources
	}
	for _, re := range forbidden {
		if loc := re.FindString(scan); loc != "" {
			t.Errorf("dashboard contains forbidden external/fetch reference %q (pattern %s)", loc, re)
		}
	}

	// 2. Self-contained: the data block AND the inlined renderer/stylesheet are present.
	for _, marker := range []string{
		`<script id="dashboard-data" type="application/json">`,
		`"use strict"`,                     // from chart.js (inlined renderer)
		`getElementById("dashboard-data")`, // renderer reads embedded data, never fetches
		`--panel`,                          // from style.css (inlined)
	} {
		if !strings.Contains(html, marker) {
			t.Errorf("dashboard missing self-contained marker %q", marker)
		}
	}

	// 3. Hostile string must be embedded ESCAPED (not as live markup).
	if strings.Contains(html, `<img src=x onerror`) || strings.Contains(html, `</script><img`) {
		t.Errorf("hostile Jira string leaked unescaped into the export")
	}
	if !strings.Contains(html, `\u003c/script\u003e\u003cimg src=x onerror=alert(1)\u003e`) {
		t.Errorf("hostile string not found in the expected HTML-escaped form (data not embedded or escaping changed)")
	}

	// 4. Disclosure about data sensitivity is embedded.
	if !strings.Contains(html, "authorized") {
		t.Errorf("data-sharing disclosure missing from export")
	}
}

// TestDashboard_ValuesMatchService asserts the embedded chart values are the SAME numbers the
// shared service returns — the dashboard is fed by Compute, never a forked computation.
func TestDashboard_ValuesMatchService(t *testing.T) {
	store, svc := buildService(t, dashboardPolicy())
	for k := 1; k <= 3; k++ {
		seedCycle(t, store, "C-"+itoa(k), 2, 5+k) // all resolved in 2024-01
	}
	rebuild(t, store, dashboardPolicy())

	out, err := ExportDashboard(svc, DashboardOptions{Projects: []string{"PC"}, AsOf: asOf})
	if err != nil {
		t.Fatalf("ExportDashboard: %v", err)
	}
	data := extractData(t, string(out))
	secs := sectionsByID(data)

	// Throughput: compare the embedded bars to a direct service compute over the same window.
	tp := secs["throughput"]
	if tp == nil || tp["available"] != true {
		t.Fatalf("throughput section missing or unavailable: %v", tp)
	}
	trend := &TimeRange{From: asOf.AddDate(0, -6, 0), To: asOf}
	r, err := svc.Compute(Query{Metric: MetricThroughput, Projects: []string{"PC"}, AsOf: asOf,
		Period: "month", Range: trend})
	if err != nil {
		t.Fatalf("compute throughput: %v", err)
	}
	want := map[string]int{}
	for _, p := range r.Series {
		want[p.Period] = p.Count
	}
	tdata, _ := tp["data"].(map[string]interface{})
	labels, _ := tdata["labels"].([]interface{})
	values, _ := tdata["values"].([]interface{})
	if len(labels) != len(want) {
		t.Fatalf("throughput periods = %d, service = %d", len(labels), len(want))
	}
	for i, lab := range labels {
		ls := lab.(string)
		got := int(values[i].(float64))
		if got != want[ls] {
			t.Errorf("throughput[%s] embedded = %d, service = %d", ls, got, want[ls])
		}
	}

	// CFD must be available and span all stages consistently (todo/active/done at least).
	cfd := secs["cfd"]
	if cfd == nil || cfd["available"] != true {
		t.Fatalf("cfd section missing or unavailable: %v", cfd)
	}
	cdata, _ := cfd["data"].(map[string]interface{})
	stages, _ := cdata["stages"].([]interface{})
	if len(stages) == 0 {
		t.Errorf("cfd has no stages")
	}
}

// TestDashboard_EmptyAndUnconfigured covers the boundary case: no issues, no fact generation,
// no ratio config. The export must still succeed, stay self-contained, and render explicit
// "unavailable" cards instead of fabricating charts or panicking.
func TestDashboard_EmptyAndUnconfigured(t *testing.T) {
	database, store := newFactStore(t)
	svc := NewService(database, store, factPolicy(), nil) // factPolicy has no type_classes/ratio

	out, err := ExportDashboard(svc, DashboardOptions{AsOf: asOf})
	if err != nil {
		t.Fatalf("ExportDashboard on empty DB: %v", err)
	}
	html := string(out)
	if !strings.Contains(html, `<script id="dashboard-data" type="application/json">`) {
		t.Fatalf("empty export is not self-contained")
	}
	scan := strings.ReplaceAll(html, "http://www.w3.org/2000/svg", "") // inert SVG namespace
	if strings.Contains(scan, "http://") || strings.Contains(scan, "https://") {
		t.Errorf("empty export contains an external reference")
	}

	data := extractData(t, html)
	secs := sectionsByID(data)

	// No fact generation was built: the no-generation note is surfaced.
	if _, ok := secs["no-generation"]; !ok {
		t.Errorf("missing no-generation note on a DB with no fact store")
	}
	// Ratio is unconfigured here: the section must be unavailable, not a fabricated chart.
	if ratio := secs["ratio"]; ratio == nil || ratio["available"] == true {
		t.Errorf("ratio should be unavailable when unconfigured, got %v", ratio)
	}
	// Cycle-time distribution over an empty cohort is unavailable, not a zeroed chart.
	if ct := secs["cycle-time-dist"]; ct == nil || ct["available"] == true {
		t.Errorf("cycle-time distribution should be unavailable on empty data, got %v", ct)
	}
}
