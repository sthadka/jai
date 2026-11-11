package metrics

import (
	"errors"
	"strings"
	"testing"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
)

func slaService(t *testing.T, mc *config.MetricsConfig) (*db.DB, *Service) {
	t.Helper()
	database, store := newFactStore(t)
	pol := CompilePolicy(mc)
	cfg := &config.Config{Metrics: mc}
	return database, NewService(database, store, pol, cfg)
}

// upSec inserts a security-cohort issue with a priority (categorical severity source).
func upSec(t *testing.T, database *db.DB, key, itype, priority, created, resolved string) {
	t.Helper()
	cat := "In Progress"
	res := ""
	if resolved != "" {
		cat = "Done"
		res = "Done"
	}
	iss := &db.Issue{
		Key: key, Project: "SEC", Type: itype, Summary: key,
		Status: cat, StatusCategory: cat, Priority: priority,
		Created: created, Resolved: resolved, Resolution: res,
	}
	if err := database.UpsertIssue(iss, nil); err != nil {
		t.Fatalf("UpsertIssue %s: %v", key, err)
	}
}

func slaCategoricalConfig(onMissing string) *config.MetricsConfig {
	return &config.MetricsConfig{
		TypeClasses: map[string][]string{"security": {"Vulnerability"}, "bug": {"Bug"}},
		SLA: &config.SLAConfig{Security: &config.SLASecurityConfig{
			SeverityField:     "priority",
			DeadlinesDays:     map[string]int{"Critical": 7, "Major": 30},
			ClockStart:        "created",
			OnMissingSeverity: onMissing,
		}},
	}
}

// With no sla.security policy the metric fails closed (unavailable), never guessing a
// severity source or calling a bare resolution duration an SLA.
func TestSLA_FailClosedUnconfigured(t *testing.T) {
	database, svc := slaService(t, &config.MetricsConfig{
		TypeClasses: map[string][]string{"security": {"Vulnerability"}},
	})
	upSec(t, database, "V-1", "Vulnerability", "Critical", day(1), day(5))

	_, err := svc.Compute(Query{Metric: MetricSecuritySLA})
	if !errors.Is(err, ErrSLANotConfigured) {
		t.Fatalf("got err %v, want ErrSLANotConfigured", err)
	}
}

// A complete categorical policy judges each issue: on-time resolved = compliant, late
// resolved = breach, open-past-deadline = breach (open_overdue), open-within = open_within.
// Missing severity under on_missing=exclude is surfaced as an exclusion, never dropped.
func TestSLA_CategoricalCompliance(t *testing.T) {
	database, svc := slaService(t, slaCategoricalConfig("exclude"))
	upSec(t, database, "V-1", "Vulnerability", "Critical", day(1), day(5))  // 4d <= 7 compliant
	upSec(t, database, "V-2", "Vulnerability", "Critical", day(1), day(10)) // 9d > 7 breach
	upSec(t, database, "V-3", "Vulnerability", "Major", day(1), "")         // open, deadline day31; overdue
	upSec(t, database, "V-4", "Vulnerability", "Major", day(35), "")        // open, deadline day65; within
	upSec(t, database, "V-5", "Vulnerability", "", day(1), day(3))          // missing severity -> excluded
	upSec(t, database, "N-1", "Bug", "Critical", day(1), day(2))            // non-security -> ignored

	r, err := svc.Compute(Query{Metric: MetricSecuritySLA, AsOf: tm("2024-02-09T00:00:00Z")})
	if err != nil {
		t.Fatalf("security_sla: %v", err)
	}
	if r.Eligible != 5 {
		t.Errorf("Eligible = %d, want 5 (security cohort; Bug excluded)", r.Eligible)
	}
	want := map[string]int{
		"sla:compliant":    1,
		"sla:breached":     2,
		"sla:open_overdue": 1,
		"sla:open_within":  1,
	}
	for k, v := range want {
		if r.Counts[k] != v {
			t.Errorf("Counts[%q] = %d, want %d", k, r.Counts[k], v)
		}
	}
	if r.Excluded["missing_severity"] != 1 {
		t.Errorf("Excluded[missing_severity] = %d, want 1", r.Excluded["missing_severity"])
	}
	// Remediation timing covers every resolved security issue (V-1, V-2, V-5), independent
	// of severity resolution.
	if r.Distribution == nil || r.Distribution.Count != 3 {
		t.Fatalf("remediation distribution = %+v, want Count 3", r.Distribution)
	}
	if r.Counts["remediation_resolved"] != 3 {
		t.Errorf("remediation_resolved = %d, want 3", r.Counts["remediation_resolved"])
	}
	if r.Policies["sla_compliance"] != "evaluated" {
		t.Errorf("sla_compliance = %q, want evaluated", r.Policies["sla_compliance"])
	}
}

// on_missing=treat_as:<label> uses that label's deadline instead of excluding, and the
// treated cohort stays visible.
func TestSLA_OnMissingTreatAs(t *testing.T) {
	database, svc := slaService(t, slaCategoricalConfig("treat_as:Major"))
	upSec(t, database, "V-5", "Vulnerability", "", day(1), day(5)) // missing -> treated Major(30), 4d compliant

	r, err := svc.Compute(Query{Metric: MetricSecuritySLA, AsOf: tm("2024-02-09T00:00:00Z")})
	if err != nil {
		t.Fatalf("security_sla: %v", err)
	}
	if r.Counts["sla:compliant"] != 1 {
		t.Errorf("sla:compliant = %d, want 1 (missing severity treated as Major)", r.Counts["sla:compliant"])
	}
	if r.Excluded["missing_severity"] != 0 {
		t.Errorf("Excluded[missing_severity] = %d, want 0 (treat_as, not exclude)", r.Excluded["missing_severity"])
	}
	if r.Counts["missing_severity_treated_as:Major"] != 1 {
		t.Errorf("missing_severity_treated_as:Major = %d, want 1", r.Counts["missing_severity_treated_as:Major"])
	}
}

// Numeric severity_bands classify severity but carry no per-severity deadline, so the
// metric reports remediation timing and explicitly withholds any compliance verdict.
func TestSLA_NumericBandsNoComplianceClaim(t *testing.T) {
	mc := &config.MetricsConfig{
		TypeClasses: map[string][]string{"security": {"Vulnerability"}},
		SLA: &config.SLAConfig{Security: &config.SLASecurityConfig{
			SeverityBands: []config.SeverityBand{{Ge: 9, Label: "Critical"}, {Ge: 7, Label: "Major"}, {Ge: 0, Label: "Minor"}},
			ClockStart:    "created",
		}},
	}
	if !CompilePolicy(mc).Enabled(MetricSLA) {
		t.Fatalf("test setup: bands-only policy should be structurally valid")
	}
	database, svc := slaService(t, mc)
	upSec(t, database, "V-1", "Vulnerability", "", day(1), day(5))

	r, err := svc.Compute(Query{Metric: MetricSecuritySLA, AsOf: tm("2024-02-09T00:00:00Z")})
	if err != nil {
		t.Fatalf("security_sla: %v", err)
	}
	if _, ok := r.Counts["sla:compliant"]; ok {
		t.Errorf("numeric bands must not produce a compliance verdict, got %v", r.Counts)
	}
	if r.Policies["sla_compliance"] != "unavailable" {
		t.Errorf("sla_compliance = %q, want unavailable", r.Policies["sla_compliance"])
	}
	if r.Distribution == nil || r.Distribution.Count != 1 {
		t.Errorf("remediation distribution = %+v, want Count 1", r.Distribution)
	}
	if !hasWarn(r, "no compliance verdict is claimed") {
		t.Errorf("expected a 'no compliance verdict' warning, got %v", r.Warnings)
	}
}

// A structurally-invalid policy (both severity sources) fails closed: no compliance, only
// remediation timing with a diagnostic.
func TestSLA_StructurallyInvalidFailsClosed(t *testing.T) {
	mc := &config.MetricsConfig{
		TypeClasses: map[string][]string{"security": {"Vulnerability"}},
		SLA: &config.SLAConfig{Security: &config.SLASecurityConfig{
			SeverityField: "priority",
			DeadlinesDays: map[string]int{"Critical": 7},
			SeverityBands: []config.SeverityBand{{Ge: 9, Label: "Critical"}},
			ClockStart:    "created",
		}},
	}
	if CompilePolicy(mc).Enabled(MetricSLA) {
		t.Fatalf("test setup: both-sources policy should be rejected")
	}
	database, svc := slaService(t, mc)
	upSec(t, database, "V-1", "Vulnerability", "Critical", day(1), day(5))

	r, err := svc.Compute(Query{Metric: MetricSecuritySLA, AsOf: tm("2024-02-09T00:00:00Z")})
	if err != nil {
		t.Fatalf("security_sla: %v", err)
	}
	if _, ok := r.Counts["sla:compliant"]; ok {
		t.Errorf("rejected policy must not produce a compliance verdict, got %v", r.Counts)
	}
	if !hasWarn(r, "structurally invalid") {
		t.Errorf("expected a structural-invalidity warning, got %v", r.Warnings)
	}
}

// bandLabel maps a score to the highest band whose floor it meets; sub-floor scores are
// unresolved. Boundary values (== ge) are inclusive.
func TestBandLabel_Boundaries(t *testing.T) {
	bands := []config.SeverityBand{{Ge: 9, Label: "Critical"}, {Ge: 7, Label: "Major"}, {Ge: 0, Label: "Minor"}}
	cases := []struct {
		score float64
		label string
		ok    bool
	}{
		{9.0, "Critical", true},
		{9.5, "Critical", true},
		{8.9, "Major", true},
		{7.0, "Major", true},
		{6.9, "Minor", true},
		{0.0, "Minor", true},
		{-0.1, "", false},
	}
	for _, c := range cases {
		got, ok := bandLabel(bands, c.score)
		if got != c.label || ok != c.ok {
			t.Errorf("bandLabel(%v) = (%q,%v), want (%q,%v)", c.score, got, ok, c.label, c.ok)
		}
	}
}

func hasWarn(r *Result, sub string) bool {
	for _, w := range r.Warnings {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}
