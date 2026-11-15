package metrics

import "sort"

// Provider computes one named metric from the shared Service. Providers register
// themselves from their own source files (delivery.go, snapshot.go, ...) via Register in
// an init function, so adding a metric never requires editing one central registry file
// and never produces competing edits to a shared switch.
type Provider interface {
	// Metric is the stable metric name callers request.
	Metric() string
	// Compute runs the metric. The service has already validated the basis and filled
	// common envelope fields on the returned Result via newResult; the provider fills the
	// metric-specific data, eligible/excluded cohorts, and cohort label.
	Compute(s *Service, q Query) (*Result, error)
}

// registry holds every registered provider by metric name. It is populated at init time
// (single-threaded) and read-only thereafter.
var registry = map[string]Provider{}

// Register adds a provider. It panics on a duplicate name so a collision is caught at
// program start, not silently shadowed.
func Register(p Provider) {
	name := p.Metric()
	if _, dup := registry[name]; dup {
		panic("metrics: duplicate provider registration: " + name)
	}
	registry[name] = p
}

// lookup returns the provider for a metric name.
func lookup(name string) (Provider, bool) {
	p, ok := registry[name]
	return p, ok
}

// Metrics returns every registered metric name, sorted. Consumers (CLI/MCP/TUI) use it to
// enumerate the available metrics from the single shared definition.
func Metrics() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
