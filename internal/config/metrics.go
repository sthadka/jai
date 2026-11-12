package config

// MetricsConfig is the raw, tenant-neutral `metrics:` YAML block. All flow-metric
// taxonomy (stage mapping, completion policy, type classes, tracked fields, blocker
// links, SLA) lives here so a user retargets jai at their own Jira by editing YAML,
// never by patching Go or SQL. It is optional: a config without a `metrics:` block
// loads unchanged and unrelated commands keep working.
//
// This type only carries the literal YAML shape. Normalization, precedence, and
// structural (fail-closed) validation live in internal/metrics, which compiles this
// into a validated Policy. Keeping the raw block here (alongside views/snippets/
// fields) mirrors the existing Config conventions without importing internal/metrics.
type MetricsConfig struct {
	// StatusStageIDs maps a Jira status ID to a flow stage. Authoritative: an ID
	// mapping wins over a name mapping and survives status renames. Optional.
	StatusStageIDs map[string]string `yaml:"status_stage_ids"`

	// StatusStages maps a flow stage name to the list of Jira status names in it.
	// Names not listed fall through to the statusCategory-derived default, then to
	// `unknown`. A status name appearing under two stages is a duplicate mapping and
	// rejects the stage metric (fail closed).
	StatusStages map[string][]string `yaml:"status_stages"`

	// WIPStages lists the stages counted as work-in-progress / cycle-time active.
	// Each must be a defined stage; an undefined reference rejects the WIP metric.
	// Empty means the compiler applies the default [active].
	WIPStages []string `yaml:"wip_stages"`

	// DoneResolutions is the shared genuine-completion filter (cycle/lead/throughput/
	// ratio). REQUIRED for delivery metrics. If omitted, delivery metrics run over all
	// resolved issues labelled "includes non-delivery resolutions" and the genuine
	// variant is disabled — never guessed.
	DoneResolutions []string `yaml:"done_resolutions"`

	// TypeClasses maps a class name (e.g. bug, feature) to issue types. Types not
	// listed fall into `other`. A type under two classes rejects the type-class metric.
	TypeClasses map[string][]string `yaml:"type_classes"`

	// Ratio names a numerator/denominator class and a cohort. Undefined class
	// references or an invalid cohort reject the ratio metric. Optional.
	Ratio *RatioConfig `yaml:"ratio"`

	// TrackedFields lists changelog fields replayed into interval facts. Fields absent
	// from the changelog are a run-time coverage diagnostic, not a structural error.
	TrackedFields []string `yaml:"tracked_fields"`

	// MembershipFields lists the set-valued changelog fields (Sprint, labels,
	// components) persisted as event-time membership intervals in
	// fact_issue_membership. Each must be a set-valued field (coercing a scalar
	// here rejects the membership facts, fail closed). Empty applies the default
	// [components, labels, Sprint].
	MembershipFields []string `yaml:"membership_fields"`

	// BlockerLinkTypes lists issue-link types treated as blockers (direction
	// normalized at build time).
	BlockerLinkTypes []string `yaml:"blocker_link_types"`

	// SLA drives optional security/SLA metrics. Optional.
	SLA *SLAConfig `yaml:"sla"`
}

// RatioConfig names a numerator/denominator ratio over type classes.
type RatioConfig struct {
	Numerator   string `yaml:"numerator"`
	Denominator string `yaml:"denominator"`
	Cohort      string `yaml:"cohort"` // created | completed
}

// SLAConfig holds SLA policies. Only security is modelled in Phase 0.
type SLAConfig struct {
	Security *SLASecurityConfig `yaml:"security"`
}

// SLASecurityConfig defines a security SLA. Exactly one severity source must be set:
// a categorical SeverityField with per-value DeadlinesDays, or numeric SeverityBands
// over a score. Setting both or neither rejects the SLA metric.
type SLASecurityConfig struct {
	SeverityField     string         `yaml:"severity_field"`
	DeadlinesDays     map[string]int `yaml:"deadlines_days"`
	SeverityBands     []SeverityBand `yaml:"severity_bands"`
	ClockStart        string         `yaml:"clock_start"`
	OnMissingSeverity string         `yaml:"on_missing_severity"` // exclude | treat_as:<label>
}

// SeverityBand maps a numeric score floor to a severity label. Bands must be strictly
// descending by Ge; a non-monotonic set rejects the SLA metric.
type SeverityBand struct {
	Ge    float64 `yaml:"ge"`
	Label string  `yaml:"label"`
}
