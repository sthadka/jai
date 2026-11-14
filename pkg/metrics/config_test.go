package metrics

import (
	"testing"

	"github.com/sthadka/jai/internal/config"
)

// Two distinct tenant taxonomies must produce correct mapping with no code change.
func TestStageFor_TwoTenants(t *testing.T) {
	tenantA := &config.MetricsConfig{
		StatusStages: map[string][]string{
			"todo":   {"Backlog", "To Do"},
			"active": {"In Progress", "Review"},
			"done":   {"Closed"},
		},
		WIPStages: []string{"active"},
	}
	tenantB := &config.MetricsConfig{
		StatusStageIDs: map[string]string{"10001": "shipped"},
		StatusStages: map[string][]string{
			"queued":  {"Open", "Triage"},
			"doing":   {"Coding", "Verifying"},
			"shipped": {"Released"},
		},
		WIPStages: []string{"doing"},
	}

	pa := CompilePolicy(tenantA)
	pb := CompilePolicy(tenantB)
	if err := pa.Err(); err != nil {
		t.Fatalf("tenantA rejected: %v", err)
	}
	if err := pb.Err(); err != nil {
		t.Fatalf("tenantB rejected: %v", err)
	}

	if m := pa.StageFor("", "In Progress", ""); m.Stage != "active" || m.Provenance != ProvName {
		t.Errorf("tenantA In Progress: %+v", m)
	}
	if !pa.IsWIPStage("active") || pa.IsWIPStage("doing") {
		t.Error("tenantA wip stages wrong")
	}
	if m := pb.StageFor("", "Coding", ""); m.Stage != "doing" || m.Provenance != ProvName {
		t.Errorf("tenantB Coding: %+v", m)
	}
	if !pb.IsWIPStage("doing") || pb.IsWIPStage("active") {
		t.Error("tenantB wip stages wrong")
	}
	// Same status name maps differently per tenant with no code literal.
	if pa.StageFor("", "In Progress", "").Stage == pb.StageFor("", "In Progress", "").Stage {
		// tenantB has no "In Progress" -> unknown
		t.Error("expected divergent mapping across tenants")
	}
}

func TestStageFor_Precedence(t *testing.T) {
	cfg := &config.MetricsConfig{
		StatusStageIDs: map[string]string{"10001": "done"},
		StatusStages: map[string][]string{
			"todo":   {"Backlog"},
			"active": {"In Progress"},
			"done":   {"Closed"},
		},
	}
	p := CompilePolicy(cfg)

	tests := []struct {
		name                 string
		id, status, category string
		wantStage, wantProv  string
	}{
		{"id overrides name", "10001", "Backlog", "To Do", "done", ProvID},
		{"name match", "", "In Progress", "", "active", ProvName},
		{"renamed status via id", "10001", "Shipped", "Done", "done", ProvID},
		{"unmapped name, category default todo", "", "Weird", "To Do", "todo", ProvCategory},
		{"unmapped name, category default active", "", "Weird", "In Progress", "active", ProvCategory},
		{"unmapped name, category default done", "", "Weird", "Done", "done", ProvCategory},
		{"unmapped, no category -> unknown", "", "Weird", "", "unknown", ProvUnknown},
		{"historical unmapped (no category) stays unknown", "", "LegacyStatus", "", "unknown", ProvUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := p.StageFor(tc.id, tc.status, tc.category)
			if m.Stage != tc.wantStage || m.Provenance != tc.wantProv {
				t.Errorf("StageFor(%q,%q,%q) = %+v, want stage=%s prov=%s",
					tc.id, tc.status, tc.category, m, tc.wantStage, tc.wantProv)
			}
		})
	}
}

func TestCompile_DuplicateStatusMappingRejected(t *testing.T) {
	cfg := &config.MetricsConfig{
		StatusStages: map[string][]string{
			"todo":   {"Backlog", "In Progress"},
			"active": {"In Progress"}, // duplicate: In Progress under two stages
		},
	}
	p := CompilePolicy(cfg)
	if p.Enabled(MetricStatusStages) {
		t.Fatal("expected status_stages rejected for duplicate mapping")
	}
	if p.Err() == nil {
		t.Fatal("expected aggregated error")
	}
	// Fail closed: ambiguous name map discarded -> unknown, not a misleading guess.
	if m := p.StageFor("", "In Progress", ""); m.Stage != "unknown" {
		t.Errorf("fail-closed expected unknown, got %+v", m)
	}
}

func TestCompile_UndefinedWIPStageRejected(t *testing.T) {
	cfg := &config.MetricsConfig{
		StatusStages: map[string][]string{"active": {"In Progress"}},
		WIPStages:    []string{"nonexistent"},
	}
	p := CompilePolicy(cfg)
	if p.Enabled(MetricWIPStages) {
		t.Fatal("expected wip_stages rejected")
	}
	if p.IsWIPStage("nonexistent") || p.IsWIPStage("active") {
		t.Error("fail-closed: no wip stage should be active")
	}
}

func TestCompile_DuplicateTypeClassRejected(t *testing.T) {
	cfg := &config.MetricsConfig{
		TypeClasses: map[string][]string{
			"bug":     {"Bug", "Defect"},
			"feature": {"Defect"}, // duplicate type
		},
	}
	p := CompilePolicy(cfg)
	if p.Enabled(MetricTypeClasses) {
		t.Fatal("expected type_classes rejected")
	}
	if p.ClassForType("Bug") != "other" {
		t.Error("fail-closed: type classes discarded -> other")
	}
}

func TestCompile_RatioUndefinedClassRejected(t *testing.T) {
	cfg := &config.MetricsConfig{
		TypeClasses: map[string][]string{"bug": {"Bug"}},
		Ratio:       &config.RatioConfig{Numerator: "bug", Denominator: "feature", Cohort: "completed"},
	}
	p := CompilePolicy(cfg)
	if p.Enabled(MetricRatio) {
		t.Fatal("expected ratio rejected for undefined denominator class")
	}
	if _, ok := p.Ratio(); ok {
		t.Error("rejected ratio must not be usable")
	}
}

func TestCompile_RatioInvalidCohortRejected(t *testing.T) {
	cfg := &config.MetricsConfig{
		TypeClasses: map[string][]string{"bug": {"Bug"}, "feature": {"Story"}},
		Ratio:       &config.RatioConfig{Numerator: "bug", Denominator: "feature", Cohort: "sometime"},
	}
	p := CompilePolicy(cfg)
	if p.Enabled(MetricRatio) {
		t.Fatal("expected ratio rejected for invalid cohort")
	}
}

func TestCompile_RatioValid(t *testing.T) {
	cfg := &config.MetricsConfig{
		TypeClasses: map[string][]string{"bug": {"Bug"}, "feature": {"Story"}},
		Ratio:       &config.RatioConfig{Numerator: "bug", Denominator: "feature"},
	}
	p := CompilePolicy(cfg)
	if !p.Enabled(MetricRatio) {
		t.Fatalf("ratio should be valid: %v", p.Err())
	}
	r, ok := p.Ratio()
	if !ok || r.Cohort != "completed" { // default applied
		t.Errorf("ratio = %+v ok=%v, want default cohort completed", r, ok)
	}
}

func TestCompile_OmittedDoneResolutions(t *testing.T) {
	p := CompilePolicy(&config.MetricsConfig{})
	if p.DoneResolutionsConfigured() {
		t.Error("expected done_resolutions unconfigured when omitted")
	}
	with := CompilePolicy(&config.MetricsConfig{DoneResolutions: []string{"Done"}})
	if !with.DoneResolutionsConfigured() || !with.IsDoneResolution("Done") || with.IsDoneResolution("Won't Do") {
		t.Error("done_resolutions filter wrong")
	}
}

func TestCompile_SLA(t *testing.T) {
	tests := []struct {
		name   string
		sec    *config.SLASecurityConfig
		wantOK bool
	}{
		{"valid categorical", &config.SLASecurityConfig{
			SeverityField: "priority", DeadlinesDays: map[string]int{"Critical": 7}, OnMissingSeverity: "exclude",
		}, true},
		{"valid bands", &config.SLASecurityConfig{
			SeverityBands:     []config.SeverityBand{{Ge: 9, Label: "Critical"}, {Ge: 7, Label: "Major"}, {Ge: 0, Label: "Minor"}},
			OnMissingSeverity: "treat_as:Minor",
		}, true},
		{"both sources", &config.SLASecurityConfig{
			SeverityField: "priority", DeadlinesDays: map[string]int{"Critical": 7},
			SeverityBands: []config.SeverityBand{{Ge: 9, Label: "Critical"}},
		}, false},
		{"neither source", &config.SLASecurityConfig{OnMissingSeverity: "exclude"}, false},
		{"non-monotonic bands", &config.SLASecurityConfig{
			SeverityBands: []config.SeverityBand{{Ge: 7, Label: "Major"}, {Ge: 9, Label: "Critical"}},
		}, false},
		{"bad on_missing", &config.SLASecurityConfig{
			SeverityField: "priority", DeadlinesDays: map[string]int{"Critical": 7}, OnMissingSeverity: "drop",
		}, false},
		{"treat_as without label", &config.SLASecurityConfig{
			SeverityField: "priority", DeadlinesDays: map[string]int{"Critical": 7}, OnMissingSeverity: "treat_as:",
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := CompilePolicy(&config.MetricsConfig{SLA: &config.SLAConfig{Security: tc.sec}})
			if got := p.Enabled(MetricSLA); got != tc.wantOK {
				t.Errorf("Enabled(sla) = %v, want %v (rejections: %v)", got, tc.wantOK, p.Rejections()[MetricSLA])
			}
		})
	}
}

// A rejected metric must not disable unrelated metrics.
func TestCompile_RejectionIsScoped(t *testing.T) {
	cfg := &config.MetricsConfig{
		StatusStages: map[string][]string{"active": {"In Progress"}},
		TypeClasses: map[string][]string{
			"bug":     {"Bug", "Defect"},
			"feature": {"Defect"}, // rejects type_classes only
		},
		WIPStages: []string{"active"},
	}
	p := CompilePolicy(cfg)
	if p.Enabled(MetricTypeClasses) {
		t.Fatal("type_classes should be rejected")
	}
	if !p.Enabled(MetricStatusStages) || !p.Enabled(MetricWIPStages) {
		t.Error("unrelated metrics must stay enabled")
	}
	if m := p.StageFor("", "In Progress", ""); m.Stage != "active" {
		t.Errorf("status mapping must still work: %+v", m)
	}
}

func TestFingerprint_StableAndSensitive(t *testing.T) {
	a := &config.MetricsConfig{
		StatusStages: map[string][]string{"active": {"In Progress", "Review"}},
		WIPStages:    []string{"active"},
	}
	// Same content, different map/list element order must hash identically.
	b := &config.MetricsConfig{
		StatusStages: map[string][]string{"active": {"Review", "In Progress"}},
		WIPStages:    []string{"active"},
	}
	if CompilePolicy(a).Fingerprint() != CompilePolicy(b).Fingerprint() {
		t.Error("fingerprint must be order-independent")
	}
	c := &config.MetricsConfig{
		StatusStages: map[string][]string{"active": {"In Progress"}},
		WIPStages:    []string{"active"},
	}
	if CompilePolicy(a).Fingerprint() == CompilePolicy(c).Fingerprint() {
		t.Error("fingerprint must change when mapping changes")
	}
}

func TestCompile_NilConfigUsableDefault(t *testing.T) {
	p := CompilePolicy(nil)
	if err := p.Err(); err != nil {
		t.Fatalf("nil config must not error: %v", err)
	}
	if !p.IsWIPStage("active") {
		t.Error("nil config should default wip=[active]")
	}
	// Category default still works with no explicit mapping.
	if m := p.StageFor("", "Anything", "In Progress"); m.Stage != "active" || m.Provenance != ProvCategory {
		t.Errorf("category default under nil config: %+v", m)
	}
	if m := p.StageFor("", "Anything", ""); m.Stage != "unknown" {
		t.Errorf("no category -> unknown: %+v", m)
	}
}
