package metrics

import (
	"sort"
	"strings"

	"github.com/sthadka/jai/internal/config"
)

// Generated fragment names. These are the shared SQL fragments (`jai snippet`)
// derived from the `metrics:` config so the status→stage taxonomy, the WIP set,
// and the genuine-delivery filter live in exactly one place. Every view and
// consumer references them by name instead of re-embedding tenant literals.
const (
	FragmentStatusStageMap = "status_stage_map"
	FragmentActiveStages   = "active_stages"
	FragmentGenuineDone    = "genuine_done"
)

// GenerateFragments compiles the shared SQL fragments from a validated Policy.
// The fragments operate on the `issues` snapshot (status name + status_category;
// the issues table carries no status id, so the id override tier of StageFor is
// not representable here and the name→category precedence is used, matching the
// issues-only metric providers).
//
// Fail closed: a fragment is emitted only when the policy metric it depends on
// passed structural validation. A malformed stage map (duplicate/undefined
// mapping) therefore yields no apparently-valid status_stage_map/active_stages
// fragment rather than a misleading one. Fragments are regenerated on every
// command from the current policy, so a config change never leaves a stale
// fragment behind. A nil policy yields no fragments.
func GenerateFragments(pol *Policy) map[string]string {
	out := map[string]string{}
	if pol == nil {
		return out
	}
	if pol.Enabled(MetricStatusStages) {
		out[FragmentStatusStageMap] = buildStatusStageMap(pol)
		// active_stages wraps the stage map, so it also needs a valid WIP set.
		if pol.Enabled(MetricWIPStages) && len(pol.wipStages) > 0 {
			out[FragmentActiveStages] = buildActiveStages(pol)
		}
	}
	out[FragmentGenuineDone] = buildGenuineDone(pol)
	return out
}

// GenerateViews returns the simple-metric views expressed directly over `issues`
// using the generated fragments. These are the `jai view` surface for metrics
// that need no fact generation; they reference the shared fragments so there is
// no second copy of the taxonomy or completion SQL. A nil policy yields none.
func GenerateViews(pol *Policy) []config.ViewConfig {
	if pol == nil {
		return nil
	}
	frags := GenerateFragments(pol)
	var views []config.ViewConfig
	if _, ok := frags[FragmentStatusStageMap]; ok {
		views = append(views, config.ViewConfig{
			Name:  "metrics-stage-breakdown",
			Title: "Flow stage breakdown (generated)",
			Query: "SELECT {{status_stage_map}} AS stage, COUNT(*) AS count " +
				"FROM issues GROUP BY stage ORDER BY stage",
		})
	}
	if _, ok := frags[FragmentActiveStages]; ok {
		views = append(views, config.ViewConfig{
			Name:  "metrics-wip",
			Title: "Work-in-progress item count (generated)",
			Query: "SELECT COUNT(*) AS wip FROM issues WHERE {{active_stages}}",
		})
	}
	views = append(views,
		config.ViewConfig{
			Name:  "metrics-delivered",
			Title: "Genuinely delivered item count (generated)",
			Query: "SELECT COUNT(*) AS delivered FROM issues WHERE {{genuine_done}}",
		},
		config.ViewConfig{
			Name:  "metrics-resolution-mix",
			Title: "Resolution mix of resolved issues (generated)",
			Query: "SELECT CASE WHEN TRIM(COALESCE(resolution,'')) = '' THEN '(none)' ELSE resolution END AS resolution, " +
				"COUNT(*) AS count FROM issues WHERE resolved IS NOT NULL AND TRIM(resolved) <> '' GROUP BY resolution ORDER BY count DESC, resolution",
		},
	)
	return views
}

// buildStatusStageMap renders the status→stage CASE expression. Precedence
// matches StageFor over the issues snapshot: explicit status-name mapping first,
// then the statusCategory default, else the surfaced `unknown` bucket.
func buildStatusStageMap(pol *Policy) string {
	var b strings.Builder
	b.WriteString("CASE")

	for _, stage := range groupByStage(pol.statusStageNames) {
		b.WriteString(" WHEN status IN (")
		b.WriteString(sqlInList(stage.values))
		b.WriteString(") THEN ")
		b.WriteString(sqlQuote(stage.stage))
	}

	// statusCategory default tier (Jira's fixed enum, not tenant data).
	for _, stage := range groupByStage(categoryDefaultStage) {
		b.WriteString(" WHEN LOWER(status_category) IN (")
		b.WriteString(sqlInList(stage.values))
		b.WriteString(") THEN ")
		b.WriteString(sqlQuote(stage.stage))
	}

	b.WriteString(" ELSE ")
	b.WriteString(sqlQuote(StageUnknown))
	b.WriteString(" END")
	return b.String()
}

// buildActiveStages selects issues whose current stage is a WIP stage, reusing
// the generated stage map so there is one definition of "active".
func buildActiveStages(pol *Policy) string {
	stages := make([]string, 0, len(pol.wipStages))
	for s := range pol.wipStages {
		stages = append(stages, s)
	}
	sort.Strings(stages)
	return "(" + buildStatusStageMap(pol) + ") IN (" + sqlInList(stages) + ")"
}

// buildGenuineDone renders the shared completion filter. When done_resolutions
// is configured, only those resolutions count as delivery; when unset, every
// resolved issue is included (the genuine variant is disabled, never guessed).
func buildGenuineDone(pol *Policy) string {
	// The issues table stores an unresolved issue as NULL or an empty string;
	// both must be treated as not-resolved so this matches the service, which
	// parses a blank timestamp to nil.
	const resolvedPred = "resolved IS NOT NULL AND TRIM(resolved) <> ''"
	if !pol.doneResolutionsSet {
		return "(" + resolvedPred + ")"
	}
	res := make([]string, 0, len(pol.doneResolutions))
	for r := range pol.doneResolutions {
		res = append(res, r)
	}
	sort.Strings(res)
	return "(" + resolvedPred + " AND resolution IN (" + sqlInList(res) + "))"
}

type stageGroup struct {
	stage  string
	values []string
}

// groupByStage inverts a value→stage map into stage-keyed, deterministically
// ordered groups (stages sorted, values sorted within each stage).
func groupByStage(m map[string]string) []stageGroup {
	byStage := map[string][]string{}
	for value, stage := range m {
		byStage[stage] = append(byStage[stage], value)
	}
	stages := make([]string, 0, len(byStage))
	for s := range byStage {
		stages = append(stages, s)
	}
	sort.Strings(stages)
	out := make([]stageGroup, 0, len(stages))
	for _, s := range stages {
		vals := byStage[s]
		sort.Strings(vals)
		out = append(out, stageGroup{stage: s, values: vals})
	}
	return out
}

// sqlQuote single-quotes a string literal, doubling embedded quotes so a
// tenant value containing an apostrophe (e.g. "Won't Do") cannot break or
// inject SQL.
func sqlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func sqlInList(vals []string) string {
	quoted := make([]string, len(vals))
	for i, v := range vals {
		quoted[i] = sqlQuote(v)
	}
	return strings.Join(quoted, ", ")
}
