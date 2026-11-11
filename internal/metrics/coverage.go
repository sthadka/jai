package metrics

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
)

// IssueCoverageRow is the per-issue snapshot the coverage diagnostics operate on. It is
// deliberately decoupled from db internals so coverage logic is deterministic and
// table-testable.
type IssueCoverageRow struct {
	Key               string
	Project           string
	Status            string
	StatusCategory    string
	Type              string
	Updated           time.Time
	ChangelogSyncedAt *time.Time // nil = never checked
	HasChangelog      bool       // has at least one changelog row
	StoryPoints       *float64   // nil = absent
}

// SourceFreshness is a project's local sync freshness. It is snapshot freshness (when the
// local DB last pulled from Jira), NOT a claim of live-Jira completeness.
type SourceFreshness struct {
	LastSyncTime *time.Time
	LastFullSync *time.Time
}

// CoverageInput bundles everything ComputeCoverage needs.
type CoverageInput struct {
	Issues          []IssueCoverageRow
	Freshness       map[string]SourceFreshness // keyed by project
	ChangelogFields map[string]bool            // fields observed in changelog
	SprintRows      int
	DevLinkRows     int
	SyncSprints     bool // sync.sprints config flag
	SyncDevInfo     bool // sync.dev_info config flag
	SourcesAllJQL   bool // every sync source is JQL-only (no projects: list)
	// AsOfStaleHistory, if non-zero, flags CheckedCurrent issues whose per-issue check is
	// older than this cutoff as "stale check" — informational only, never counted as
	// pending history.
	StaleCutoff time.Time
}

// ProjectCoverage holds the distinct per-project figures. "Any-history coverage" and a
// zero pending backlog are reported separately and never equated with complete history.
type ProjectCoverage struct {
	Project string
	Issues  int

	// Any-history coverage: issue has at least one changelog row.
	WithChangelog         int
	ZeroHistory           int // excluded cohort: no changelog rows at all
	AnyHistoryCoveragePct float64

	// Pending-history backlog uses the real sync predicate
	// (changelog_synced_at IS NULL OR updated > changelog_synced_at).
	NeverChecked          int // changelog_synced_at nil
	UpdatedAfterCheck     int // checked, but updated after the check
	CheckedCurrent        int // checked and up to date (old-but-current counts here, NOT pending)
	StaleCheck            int // subset of CheckedCurrent whose check predates StaleCutoff
	PendingHistoryBacklog int // NeverChecked + UpdatedAfterCheck

	// Source-data coverage.
	StoryPointsPresent int

	// Data-dependent diagnostics.
	UnmappedStatuses []string // distinct current statuses mapping to unknown
	Empty            bool     // no issues in cohort

	Freshness      SourceFreshness
	FreshnessKnown bool
}

// GatingDiagnostic reports why an optional data source is empty (config gating vs missing
// feature), as independent facts.
type GatingDiagnostic struct {
	Rows    int
	Enabled bool
	Gated   bool
	Reason  string
}

// CoverageReport is the full read-only diagnostic.
type CoverageReport struct {
	Projects            []ProjectCoverage // sorted by project
	AbsentTrackedFields []string          // tracked_fields not present in changelog
	Sprint              GatingDiagnostic
	DevInfo             GatingDiagnostic
	StoryPointsGap      GatingDiagnostic // story-point sparsity, independent of sprint dates
	Caveats             []string
}

// ComputeCoverage derives the diagnostic report from in-memory inputs. Pure and
// deterministic.
func ComputeCoverage(in CoverageInput, pol *Policy) *CoverageReport {
	byProject := map[string]*ProjectCoverage{}
	// stable project order
	order := []string{}
	ensure := func(p string) *ProjectCoverage {
		pc, ok := byProject[p]
		if !ok {
			pc = &ProjectCoverage{Project: p}
			byProject[p] = pc
			order = append(order, p)
		}
		return pc
	}

	unmapped := map[string]map[string]bool{} // project -> set of unmapped status names
	var totalIssues, totalSP int

	for _, row := range in.Issues {
		pc := ensure(row.Project)
		pc.Issues++
		totalIssues++

		if row.HasChangelog {
			pc.WithChangelog++
		} else {
			pc.ZeroHistory++
		}

		switch {
		case row.ChangelogSyncedAt == nil:
			pc.NeverChecked++
		case row.Updated.After(*row.ChangelogSyncedAt):
			pc.UpdatedAfterCheck++
		default:
			pc.CheckedCurrent++
			if !in.StaleCutoff.IsZero() && row.ChangelogSyncedAt.Before(in.StaleCutoff) {
				pc.StaleCheck++
			}
		}

		if row.StoryPoints != nil {
			pc.StoryPointsPresent++
			totalSP++
		}

		if m := pol.StageFor("", row.Status, row.StatusCategory); m.Stage == StageUnknown && strings.TrimSpace(row.Status) != "" {
			set := unmapped[row.Project]
			if set == nil {
				set = map[string]bool{}
				unmapped[row.Project] = set
			}
			set[row.Status] = true
		}
	}

	// include projects that only have freshness (no issues) so empty cohorts stay visible
	for p := range in.Freshness {
		ensure(p)
	}

	for _, p := range order {
		pc := byProject[p]
		pc.PendingHistoryBacklog = pc.NeverChecked + pc.UpdatedAfterCheck
		pc.Empty = pc.Issues == 0
		if pc.Issues > 0 {
			pc.AnyHistoryCoveragePct = round1(100 * float64(pc.WithChangelog) / float64(pc.Issues))
		}
		if set := unmapped[p]; len(set) > 0 {
			pc.UnmappedStatuses = sortedSet(set)
		}
		if f, ok := in.Freshness[p]; ok {
			pc.Freshness = f
			pc.FreshnessKnown = true
		}
	}

	sort.Strings(order)
	report := &CoverageReport{}
	for _, p := range order {
		report.Projects = append(report.Projects, *byProject[p])
	}

	// Absent tracked fields: configured but never seen in changelog.
	for _, f := range pol.TrackedFields() {
		if !in.ChangelogFields[f] {
			report.AbsentTrackedFields = append(report.AbsentTrackedFields, f)
		}
	}
	sort.Strings(report.AbsentTrackedFields)

	report.Sprint = sprintDiagnostic(in)
	report.DevInfo = devInfoDiagnostic(in)
	report.StoryPointsGap = storyPointsDiagnostic(totalIssues, totalSP)

	report.Caveats = []string{
		"any-history coverage counts issues with some changelog rows, not every captured transition",
		"a zero pending-history backlog does not prove complete history: Jira can omit very old or permission-filtered changes",
		"source freshness is local snapshot freshness (when jai last synced), not live-Jira completeness",
		"an old per-issue changelog_synced_at on an unchanged issue is not missing history",
	}
	return report
}

func sprintDiagnostic(in CoverageInput) GatingDiagnostic {
	d := GatingDiagnostic{Rows: in.SprintRows, Enabled: in.SyncSprints}
	if in.SprintRows > 0 {
		d.Reason = "sprint data present"
		return d
	}
	d.Gated = true
	switch {
	case !in.SyncSprints && in.SourcesAllJQL:
		d.Reason = "config: sync.sprints disabled and all sources are JQL-only (SyncSprints returns early for JQL sources); enable sync.sprints and use project-keyed sources"
	case !in.SyncSprints:
		d.Reason = "config: sync.sprints disabled; enable it to sync sprint/board data"
	case in.SourcesAllJQL:
		d.Reason = "config: all sources are JQL-only; SyncSprints resolves projects from project-keyed sources"
	default:
		d.Reason = "sprint data empty despite sync.sprints enabled with project-keyed sources; verify API permissions (watch sync stderr warnings before concluding the endpoint is unsupported)"
	}
	return d
}

func devInfoDiagnostic(in CoverageInput) GatingDiagnostic {
	d := GatingDiagnostic{Rows: in.DevLinkRows, Enabled: in.SyncDevInfo}
	if in.DevLinkRows > 0 {
		d.Reason = "dev-info data present"
		return d
	}
	d.Gated = true
	if !in.SyncDevInfo {
		d.Reason = "config: sync.dev_info disabled; enable it (requires extra API permissions) to sync development links"
	} else {
		d.Reason = "dev-info empty despite sync.dev_info enabled; verify API permissions"
	}
	return d
}

func storyPointsDiagnostic(totalIssues, withSP int) GatingDiagnostic {
	d := GatingDiagnostic{Rows: withSP}
	if totalIssues == 0 {
		d.Reason = "no issues in scope"
		return d
	}
	pct := round1(100 * float64(withSP) / float64(totalIssues))
	if withSP == 0 {
		d.Gated = true
		d.Reason = "story points absent across the cohort (source-side sparsity, independent of sprint dates); prefer count-based throughput"
	} else {
		d.Reason = fmt.Sprintf("story points present on %d/%d issues (%.1f%%)", withSP, totalIssues, pct)
	}
	return d
}

// ReportCoverage builds the diagnostic directly from the local DB. Read-only: it never
// enables sync, edits config, or makes API calls. projects empty means all projects.
func ReportCoverage(database *db.DB, projects []string, pol *Policy, cfg *config.Config) (*CoverageReport, error) {
	in := CoverageInput{Freshness: map[string]SourceFreshness{}, ChangelogFields: map[string]bool{}}

	// Issues snapshot with any-history flag.
	q := `
		SELECT i.key, i.project, i.status, i.status_category, i.type, i.updated,
		       i.changelog_synced_at, i.story_points,
		       EXISTS(SELECT 1 FROM changelog c WHERE c.issue_key = i.key) AS has_cl
		FROM issues i`
	var args []any
	if len(projects) > 0 {
		ph := make([]string, len(projects))
		for i, p := range projects {
			ph[i] = "?"
			args = append(args, p)
		}
		q += " WHERE i.project IN (" + strings.Join(ph, ", ") + ")"
	}
	rows, err := database.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("coverage issues query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			key, project            string
			status, category, itype sql.NullString
			updated, syncedAt       sql.NullString
			storyPoints             sql.NullFloat64
			hasCL                   bool
		)
		if err := rows.Scan(&key, &project, &status, &category, &itype, &updated, &syncedAt, &storyPoints, &hasCL); err != nil {
			return nil, err
		}
		row := IssueCoverageRow{
			Key:            key,
			Project:        project,
			Status:         status.String,
			StatusCategory: category.String,
			Type:           itype.String,
			HasChangelog:   hasCL,
		}
		if updated.Valid {
			row.Updated = parseDBTime(updated.String)
		}
		if syncedAt.Valid && strings.TrimSpace(syncedAt.String) != "" {
			t := parseDBTime(syncedAt.String)
			row.ChangelogSyncedAt = &t
		}
		if storyPoints.Valid {
			sp := storyPoints.Float64
			row.StoryPoints = &sp
		}
		in.Issues = append(in.Issues, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Observed changelog fields.
	fieldRows, err := database.Query(`SELECT DISTINCT field FROM changelog WHERE field IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("coverage fields query: %w", err)
	}
	defer fieldRows.Close()
	for fieldRows.Next() {
		var f string
		if fieldRows.Scan(&f) == nil {
			in.ChangelogFields[f] = true
		}
	}

	// Source freshness from sync_metadata.
	fq := `SELECT project, last_sync_time, last_full_sync FROM sync_metadata`
	if len(projects) > 0 {
		ph := make([]string, len(projects))
		fargs := make([]any, len(projects))
		for i, p := range projects {
			ph[i] = "?"
			fargs[i] = p
		}
		fq += " WHERE project IN (" + strings.Join(ph, ", ") + ")"
		fRows, err := database.Query(fq, fargs...)
		if err != nil {
			return nil, err
		}
		scanFreshness(fRows, in.Freshness)
	} else {
		fRows, err := database.Query(fq)
		if err != nil {
			return nil, err
		}
		scanFreshness(fRows, in.Freshness)
	}

	in.SprintRows = countRows(database, "sprints")
	in.DevLinkRows = countRows(database, "dev_links")

	if cfg != nil {
		in.SyncSprints = cfg.Sync.Sprints
		in.SyncDevInfo = cfg.Sync.DevInfo
		in.SourcesAllJQL = sourcesAllJQL(cfg.SyncSources)
	}

	return ComputeCoverage(in, pol), nil
}

func scanFreshness(rows *sql.Rows, out map[string]SourceFreshness) {
	defer rows.Close()
	for rows.Next() {
		var project string
		var lastSync, lastFull sql.NullString
		if rows.Scan(&project, &lastSync, &lastFull) != nil {
			continue
		}
		f := SourceFreshness{}
		if lastSync.Valid && strings.TrimSpace(lastSync.String) != "" {
			t := parseDBTime(lastSync.String)
			f.LastSyncTime = &t
		}
		if lastFull.Valid && strings.TrimSpace(lastFull.String) != "" {
			t := parseDBTime(lastFull.String)
			f.LastFullSync = &t
		}
		out[project] = f
	}
}

func countRows(database *db.DB, table string) int {
	var n int
	// table is a fixed literal, not user input.
	if err := database.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		return 0
	}
	return n
}

func sourcesAllJQL(sources []config.SyncSource) bool {
	if len(sources) == 0 {
		return false
	}
	for _, s := range sources {
		if len(s.Projects) > 0 {
			return false
		}
		if strings.TrimSpace(s.JQL) == "" {
			return false
		}
	}
	return true
}

func parseDBTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
