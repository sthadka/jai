package cli

import (
	"testing"

	"github.com/sthadka/jai/v7/pkg/config"
	"github.com/sthadka/jai/v7/pkg/metrics"
)

// TestDashboardCapabilitiesNilMetricsConfig is a regression test: dashboardCapabilities
// must not panic when the config has no metrics section (nil *config.MetricsConfig).
// `jai metrics dashboard` SIGSEGV'd at metrics_dashboard.go:126 in that state.
func TestDashboardCapabilitiesNilMetricsConfig(t *testing.T) {
	setupMetricsGlobals(t, nil, nil)

	pol := metrics.CompilePolicy(g.engine.Config.Metrics)
	caps := g.dashboardCapabilities(pol)

	if caps["security_sla"] {
		t.Fatalf("security_sla should be absent with nil metrics config, got %v", caps)
	}
	if caps["blocker_link_types"] {
		t.Fatalf("blocker_link_types should be absent with empty policy, got %v", caps)
	}
}

func TestDashboardCapabilitiesSecuritySLAConfigured(t *testing.T) {
	setupMetricsGlobals(t, nil, &config.MetricsConfig{
		SLA: &config.SLAConfig{Security: &config.SLASecurityConfig{}},
	})

	pol := metrics.CompilePolicy(g.engine.Config.Metrics)
	caps := g.dashboardCapabilities(pol)

	if !caps["security_sla"] {
		t.Fatalf("security_sla should be set when a security SLA is configured, got %v", caps)
	}
}
