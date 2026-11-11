// Package metrics implements the portable, tenant-neutral flow-metrics core: config
// normalization, per-project coverage diagnostics, deterministic status-interval replay,
// and reconciliation of reconstructed current state against the live issues snapshot.
//
// All taxonomy (stages, classes, resolutions) is loaded from config.MetricsConfig; no
// tenant status/type/resolution literal appears in this package. The only platform
// constants used are Jira's fixed statusCategory enum values, which seed the overridable
// default stage mapping.
package metrics

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/sthadka/jai/internal/config"
)

// Canonical default stage names. These are this package's own vocabulary, not tenant
// data; a tenant may define any stage names in status_stages.
const (
	StageTodo    = "todo"
	StageActive  = "active"
	StageWaiting = "waiting"
	StageDone    = "done"
	StageUnknown = "unknown"
)

// Provenance records how a status was mapped to a stage.
const (
	ProvID       = "id"       // via status_stage_ids (authoritative)
	ProvName     = "name"     // via status_stages name list
	ProvCategory = "category" // via Jira statusCategory default
	ProvUnknown  = "unknown"  // no mapping available
)

// Metric identifiers used for fail-closed, per-metric rejection.
const (
	MetricStatusStages = "status_stages"
	MetricWIPStages    = "wip_stages"
	MetricTypeClasses  = "type_classes"
	MetricRatio        = "ratio"
	MetricSLA          = "sla"
)

// categoryDefaultStage maps Jira's fixed statusCategory names (platform enum, not tenant
// data) to default stages. Applied only where a current statusCategory is available.
var categoryDefaultStage = map[string]string{
	"to do":         StageTodo,
	"new":           StageTodo,
	"in progress":   StageActive,
	"indeterminate": StageActive,
	"done":          StageDone,
}

// StageMapping is the resolved stage for a status plus the precedence tier that produced it.
type StageMapping struct {
	Stage      string
	Provenance string
}

// RatioPolicy is a validated numerator/denominator ratio over type classes.
type RatioPolicy struct {
	Numerator   string
	Denominator string
	Cohort      string // created | completed
}

// Policy is the normalized, validated flow-metrics policy compiled from config. Rejected
// (structurally invalid) metrics are recorded in rejections and excluded from use; the
// rest of the policy stays usable so unrelated metrics and commands keep working.
type Policy struct {
	statusStageIDs   map[string]string // status id -> stage
	statusStageNames map[string]string // trimmed status name -> stage
	definedStages    map[string]bool
	wipStages        map[string]bool

	doneResolutions    map[string]bool
	doneResolutionsSet bool

	typeClasses map[string]string // trimmed type -> class
	ratio       *RatioPolicy

	trackedFields    []string
	blockerLinkTypes []string

	rejections  map[string][]string
	fingerprint string
}

// CompilePolicy normalizes and structurally validates a metrics config. It never returns
// an error: structural problems fail closed on the affected metric (recorded in the
// returned Policy's rejections) rather than aborting the whole config. A nil config
// yields a usable default policy (category-derived mapping, WIP = [active]). Call Err()
// to surface rejections as an actionable configuration error.
func CompilePolicy(cfg *config.MetricsConfig) *Policy {
	p := &Policy{
		statusStageIDs:   map[string]string{},
		statusStageNames: map[string]string{},
		definedStages:    map[string]bool{StageTodo: true, StageActive: true, StageWaiting: true, StageDone: true},
		wipStages:        map[string]bool{},
		doneResolutions:  map[string]bool{},
		typeClasses:      map[string]string{},
		rejections:       map[string][]string{},
	}
	if cfg == nil {
		p.wipStages[StageActive] = true
		p.fingerprint = fingerprint(cfg)
		return p
	}

	p.compileStages(cfg)
	p.compileWIP(cfg)
	p.compileDoneResolutions(cfg)
	p.compileTypeClasses(cfg)
	p.compileRatio(cfg)
	p.validateSLA(cfg)

	p.trackedFields = dedupeStrings(cfg.TrackedFields)
	p.blockerLinkTypes = dedupeStrings(cfg.BlockerLinkTypes)
	p.fingerprint = fingerprint(cfg)
	return p
}

func (p *Policy) reject(metric, reason string) {
	p.rejections[metric] = append(p.rejections[metric], reason)
}

func (p *Policy) compileStages(cfg *config.MetricsConfig) {
	// status_stage_ids are authoritative; their stage values are valid stage names.
	for id, stage := range cfg.StatusStageIDs {
		id = strings.TrimSpace(id)
		stage = strings.TrimSpace(stage)
		if id == "" || stage == "" {
			continue
		}
		p.statusStageIDs[id] = stage
		p.definedStages[stage] = true
	}
	// status_stages: name -> stage, detecting duplicate name mappings.
	ok := true
	// deterministic iteration for stable first-wins behaviour and error text.
	stages := sortedKeys(cfg.StatusStages)
	for _, stage := range stages {
		st := strings.TrimSpace(stage)
		if st == "" {
			continue
		}
		p.definedStages[st] = true
		for _, name := range cfg.StatusStages[stage] {
			n := strings.TrimSpace(name)
			if n == "" {
				continue
			}
			if existing, dup := p.statusStageNames[n]; dup && existing != st {
				p.reject(MetricStatusStages, fmt.Sprintf("status %q mapped to both %q and %q", n, existing, st))
				ok = false
				continue
			}
			p.statusStageNames[n] = st
		}
	}
	if !ok {
		// Fail closed: discard the ambiguous name map entirely so no misleading output.
		p.statusStageNames = map[string]string{}
	}
}

func (p *Policy) compileWIP(cfg *config.MetricsConfig) {
	wip := dedupeStrings(cfg.WIPStages)
	if len(wip) == 0 {
		p.wipStages[StageActive] = true
		return
	}
	ok := true
	for _, s := range wip {
		if !p.definedStages[s] {
			p.reject(MetricWIPStages, fmt.Sprintf("wip_stages references undefined stage %q", s))
			ok = false
			continue
		}
		p.wipStages[s] = true
	}
	if !ok {
		p.wipStages = map[string]bool{}
	}
}

func (p *Policy) compileDoneResolutions(cfg *config.MetricsConfig) {
	res := dedupeStrings(cfg.DoneResolutions)
	if len(res) == 0 {
		return // omitted: genuine variant disabled, delivery metrics run over all resolved.
	}
	p.doneResolutionsSet = true
	for _, r := range res {
		p.doneResolutions[r] = true
	}
}

func (p *Policy) compileTypeClasses(cfg *config.MetricsConfig) {
	ok := true
	classes := sortedKeys(cfg.TypeClasses)
	for _, class := range classes {
		cl := strings.TrimSpace(class)
		if cl == "" {
			continue
		}
		for _, t := range cfg.TypeClasses[class] {
			tt := strings.TrimSpace(t)
			if tt == "" {
				continue
			}
			if existing, dup := p.typeClasses[tt]; dup && existing != cl {
				p.reject(MetricTypeClasses, fmt.Sprintf("type %q mapped to both %q and %q", tt, existing, cl))
				ok = false
				continue
			}
			p.typeClasses[tt] = cl
		}
	}
	if !ok {
		p.typeClasses = map[string]string{}
	}
}

func (p *Policy) definedClasses() map[string]bool {
	set := map[string]bool{}
	for _, cl := range p.typeClasses {
		set[cl] = true
	}
	return set
}

func (p *Policy) compileRatio(cfg *config.MetricsConfig) {
	if cfg.Ratio == nil {
		return
	}
	r := cfg.Ratio
	ok := true
	classes := p.definedClasses()
	num := strings.TrimSpace(r.Numerator)
	den := strings.TrimSpace(r.Denominator)
	if num == "" || !classes[num] {
		p.reject(MetricRatio, fmt.Sprintf("ratio numerator %q is not a defined type class", r.Numerator))
		ok = false
	}
	if den == "" || !classes[den] {
		p.reject(MetricRatio, fmt.Sprintf("ratio denominator %q is not a defined type class", r.Denominator))
		ok = false
	}
	cohort := strings.TrimSpace(r.Cohort)
	if cohort == "" {
		cohort = "completed"
	}
	if cohort != "created" && cohort != "completed" {
		p.reject(MetricRatio, fmt.Sprintf("ratio cohort %q must be \"created\" or \"completed\"", r.Cohort))
		ok = false
	}
	if ok {
		p.ratio = &RatioPolicy{Numerator: num, Denominator: den, Cohort: cohort}
	}
}

func (p *Policy) validateSLA(cfg *config.MetricsConfig) {
	if cfg.SLA == nil || cfg.SLA.Security == nil {
		return
	}
	s := cfg.SLA.Security
	hasField := strings.TrimSpace(s.SeverityField) != "" || len(s.DeadlinesDays) > 0
	hasBands := len(s.SeverityBands) > 0
	if hasField == hasBands {
		p.reject(MetricSLA, "sla.security must set exactly one severity source: severity_field+deadlines_days OR severity_bands")
	}
	if hasField {
		if strings.TrimSpace(s.SeverityField) == "" {
			p.reject(MetricSLA, "sla.security.severity_field is required with deadlines_days")
		}
		if len(s.DeadlinesDays) == 0 {
			p.reject(MetricSLA, "sla.security.deadlines_days is required with severity_field")
		}
		for label, days := range s.DeadlinesDays {
			if days <= 0 {
				p.reject(MetricSLA, fmt.Sprintf("sla.security.deadlines_days[%s] must be positive", label))
			}
		}
	}
	if hasBands {
		prev := false
		var prevGe float64
		for _, b := range s.SeverityBands {
			if strings.TrimSpace(b.Label) == "" {
				p.reject(MetricSLA, "sla.security.severity_bands entry missing label")
			}
			if prev && b.Ge >= prevGe {
				p.reject(MetricSLA, "sla.security.severity_bands must be strictly descending by ge")
			}
			prev = true
			prevGe = b.Ge
		}
	}
	switch om := strings.TrimSpace(s.OnMissingSeverity); {
	case om == "" || om == "exclude":
	case strings.HasPrefix(om, "treat_as:") && strings.TrimSpace(strings.TrimPrefix(om, "treat_as:")) != "":
	default:
		p.reject(MetricSLA, fmt.Sprintf("sla.security.on_missing_severity %q must be \"exclude\" or \"treat_as:<label>\"", s.OnMissingSeverity))
	}
}

// StageFor resolves a status to a stage using precedence: status_stage_ids (by id) ->
// status_stages (by name) -> statusCategory default (only when a category is given) ->
// unknown. statusCategory is the live snapshot's category; historical changelog rows
// carry no category, so callers pass "" there and unmapped historical statuses stay
// unknown.
func (p *Policy) StageFor(statusID, statusName, statusCategory string) StageMapping {
	if id := strings.TrimSpace(statusID); id != "" {
		if stage, ok := p.statusStageIDs[id]; ok {
			return StageMapping{Stage: stage, Provenance: ProvID}
		}
	}
	if name := strings.TrimSpace(statusName); name != "" {
		if stage, ok := p.statusStageNames[name]; ok {
			return StageMapping{Stage: stage, Provenance: ProvName}
		}
	}
	if cat := strings.ToLower(strings.TrimSpace(statusCategory)); cat != "" {
		if stage, ok := categoryDefaultStage[cat]; ok {
			return StageMapping{Stage: stage, Provenance: ProvCategory}
		}
	}
	return StageMapping{Stage: StageUnknown, Provenance: ProvUnknown}
}

// IsWIPStage reports whether a stage counts as work-in-progress / cycle-time active.
func (p *Policy) IsWIPStage(stage string) bool { return p.wipStages[stage] }

// IsDoneResolution reports whether a resolution counts as genuine delivery. Only
// meaningful when DoneResolutionsConfigured is true.
func (p *Policy) IsDoneResolution(res string) bool { return p.doneResolutions[strings.TrimSpace(res)] }

// DoneResolutionsConfigured reports whether done_resolutions was set. When false, the
// genuine-completion variant is disabled and delivery metrics run over all resolved
// issues (labelled "includes non-delivery resolutions").
func (p *Policy) DoneResolutionsConfigured() bool { return p.doneResolutionsSet }

// ClassForType maps an issue type to its configured class, or "other" if unmapped.
func (p *Policy) ClassForType(issueType string) string {
	if cl, ok := p.typeClasses[strings.TrimSpace(issueType)]; ok {
		return cl
	}
	return "other"
}

// Ratio returns the validated ratio policy, or (nil, false) if unset or rejected.
func (p *Policy) Ratio() (*RatioPolicy, bool) { return p.ratio, p.ratio != nil }

// TrackedFields returns the changelog fields configured for interval replay.
func (p *Policy) TrackedFields() []string { return append([]string(nil), p.trackedFields...) }

// BlockerLinkTypes returns the configured blocker link types.
func (p *Policy) BlockerLinkTypes() []string { return append([]string(nil), p.blockerLinkTypes...) }

// Fingerprint is a stable hash of the normalized config; a change restates reconstructed
// history and (in later phases) invalidates fact generations.
func (p *Policy) Fingerprint() string { return p.fingerprint }

// Enabled reports whether a metric passed structural validation.
func (p *Policy) Enabled(metric string) bool { return len(p.rejections[metric]) == 0 }

// Rejections returns the per-metric structural rejection reasons (fail-closed).
func (p *Policy) Rejections() map[string][]string {
	out := make(map[string][]string, len(p.rejections))
	for k, v := range p.rejections {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// Err returns a single actionable error aggregating all rejected metrics, or nil if the
// config is structurally clean. Config loading ignores this (metrics are optional); the
// `jai metrics` entry points surface it.
func (p *Policy) Err() error {
	if len(p.rejections) == 0 {
		return nil
	}
	metrics := make([]string, 0, len(p.rejections))
	for m := range p.rejections {
		metrics = append(metrics, m)
	}
	sort.Strings(metrics)
	var b strings.Builder
	b.WriteString("invalid metrics configuration (affected metrics disabled):")
	for _, m := range metrics {
		for _, reason := range p.rejections[m] {
			fmt.Fprintf(&b, "\n  %s: %s", m, reason)
		}
	}
	return fmt.Errorf("%s", b.String())
}

func fingerprint(cfg *config.MetricsConfig) string {
	h := sha256.New()
	if cfg == nil {
		fmt.Fprint(h, "nil")
		return hex.EncodeToString(h.Sum(nil))[:16]
	}
	writeMap(h, "status_stage_ids", cfg.StatusStageIDs)
	writeMultiMap(h, "status_stages", cfg.StatusStages)
	writeList(h, "wip_stages", cfg.WIPStages)
	writeList(h, "done_resolutions", cfg.DoneResolutions)
	writeMultiMap(h, "type_classes", cfg.TypeClasses)
	if cfg.Ratio != nil {
		fmt.Fprintf(h, "ratio=%s/%s/%s;", cfg.Ratio.Numerator, cfg.Ratio.Denominator, cfg.Ratio.Cohort)
	}
	writeList(h, "tracked_fields", cfg.TrackedFields)
	writeList(h, "blocker_link_types", cfg.BlockerLinkTypes)
	if cfg.SLA != nil && cfg.SLA.Security != nil {
		s := cfg.SLA.Security
		fmt.Fprintf(h, "sla=%s/%s/%s;", s.SeverityField, s.ClockStart, s.OnMissingSeverity)
		for _, k := range sortedIntMapKeys(s.DeadlinesDays) {
			fmt.Fprintf(h, "%s=%d,", k, s.DeadlinesDays[k])
		}
		for _, b := range s.SeverityBands {
			fmt.Fprintf(h, "%g:%s,", b.Ge, b.Label)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func writeMap(h interface{ Write([]byte) (int, error) }, prefix string, m map[string]string) {
	fmt.Fprintf(h, "%s{", prefix)
	for _, k := range sortedStrMapKeys(m) {
		fmt.Fprintf(h, "%s=%s,", k, m[k])
	}
	fmt.Fprint(h, "};")
}

func writeMultiMap(h interface{ Write([]byte) (int, error) }, prefix string, m map[string][]string) {
	fmt.Fprintf(h, "%s{", prefix)
	for _, k := range sortedKeys(m) {
		vals := append([]string(nil), m[k]...)
		sort.Strings(vals)
		fmt.Fprintf(h, "%s=%s,", k, strings.Join(vals, "|"))
	}
	fmt.Fprint(h, "};")
}

func writeList(h interface{ Write([]byte) (int, error) }, prefix string, l []string) {
	vals := append([]string(nil), l...)
	sort.Strings(vals)
	fmt.Fprintf(h, "%s=%s;", prefix, strings.Join(vals, "|"))
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedStrMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedIntMapKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
