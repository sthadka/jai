package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

const baseConfig = `jira:
  url: https://example.atlassian.net
  email: me@example.com
  token: secret
sync_sources:
  - name: proj
    jql: project = PROJ
`

// A config with no metrics block must load and validate unchanged.
func TestLoad_NoMetricsBlock(t *testing.T) {
	cfg, err := Load(writeTempConfig(t, baseConfig))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Metrics != nil {
		t.Fatalf("expected nil Metrics, got %+v", cfg.Metrics)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// A config with a metrics block must unmarshal every identifier.
func TestLoad_MetricsBlock(t *testing.T) {
	body := baseConfig + `metrics:
  status_stage_ids:
    "10001": done
  status_stages:
    todo: [New, Backlog]
    active: [In Progress, Review]
    done: [Closed]
  wip_stages: [active]
  done_resolutions: [Done, Done-Errata]
  type_classes:
    bug: [Bug]
    feature: [Story, "Feature Request"]
  ratio:
    numerator: bug
    denominator: feature
    cohort: completed
  tracked_fields: [status, priority]
  blocker_link_types: [is blocked by]
  sla:
    security:
      severity_field: priority
      deadlines_days: { Critical: 7, Major: 30 }
      clock_start: created
      on_missing_severity: exclude
`
	cfg, err := Load(writeTempConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m := cfg.Metrics
	if m == nil {
		t.Fatal("expected Metrics, got nil")
	}
	if m.StatusStageIDs["10001"] != "done" {
		t.Errorf("status_stage_ids not parsed: %v", m.StatusStageIDs)
	}
	if got := m.StatusStages["active"]; len(got) != 2 || got[0] != "In Progress" {
		t.Errorf("status_stages not parsed: %v", m.StatusStages)
	}
	if len(m.WIPStages) != 1 || m.WIPStages[0] != "active" {
		t.Errorf("wip_stages not parsed: %v", m.WIPStages)
	}
	if len(m.DoneResolutions) != 2 {
		t.Errorf("done_resolutions not parsed: %v", m.DoneResolutions)
	}
	if m.TypeClasses["feature"][1] != "Feature Request" {
		t.Errorf("type_classes not parsed: %v", m.TypeClasses)
	}
	if m.Ratio == nil || m.Ratio.Numerator != "bug" || m.Ratio.Cohort != "completed" {
		t.Errorf("ratio not parsed: %+v", m.Ratio)
	}
	if len(m.TrackedFields) != 2 || len(m.BlockerLinkTypes) != 1 {
		t.Errorf("tracked/blocker not parsed: %v %v", m.TrackedFields, m.BlockerLinkTypes)
	}
	if m.SLA == nil || m.SLA.Security == nil || m.SLA.Security.DeadlinesDays["Critical"] != 7 {
		t.Errorf("sla not parsed: %+v", m.SLA)
	}
}

// Malformed YAML is a global parse failure (distinct from a metric-local rejection,
// which is handled by the metrics compiler, not Load).
func TestLoad_MalformedYAML(t *testing.T) {
	body := baseConfig + "metrics:\n  status_stages: [this, is, not, a, map]\n"
	if _, err := Load(writeTempConfig(t, body)); err == nil {
		t.Fatal("expected parse error for malformed metrics YAML")
	}
}
