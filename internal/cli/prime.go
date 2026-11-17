package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/v6/pkg/mcp"
	"github.com/sthadka/jai/v6/pkg/output"
	db "github.com/sthadka/jai/v6/pkg/store"
)

// primeCmd emits a single self-contained context document for an AI agent — the
// jai analogue of `bd prime`. An agent runs `jai prime` after a new session or
// compaction and immediately learns what data is mirrored, how fresh it is, how
// to query it, which fields/views/metrics/MCP tools exist, and runnable example
// queries, without probing status/schema/view/config separately. All of it is
// derived from the live config + DB, so there are no hardcoded project names.
var primeCmd = &cobra.Command{
	Use:   "prime",
	Short: "Emit full agent context (status, synced scope, query surface, MCP tools, examples)",
	Long: `prime prints one self-contained markdown document an AI agent can read after a
new session or compaction to learn what jai mirrors, how fresh it is, how to
query it, and which fields/views/metrics/MCP tools are available.

Everything is derived from the live config and local DB. Use --json for a
structured variant. prime never triggers an auto-sync: it reports freshness, it
does not change it.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		app := cliFrom(cmd)

		// Surface generated metric views/snippets in memory so they appear in
		// the view list exactly as `jai view` would show them.
		applyGeneratedMetrics(app.engine.Config)

		p := gatherPrime(cmd.Context(), app)

		if app.jsonOut {
			fmt.Println(string(output.OK(p.jsonData())))
			return nil
		}
		fmt.Print(p.markdown())
		return nil
	},
}

// primeSource is one sync source plus its freshness metadata.
type primeSource struct {
	Name        string
	JQL         string
	Projects    []string
	LastSync    string // RFC3339 or ""
	LastFull    string // RFC3339 or ""
	DataThrough string // YYYY-MM-DD or ""
	Synced      int64
	Warnings    []string
}

// primeView is a named view (name + title) for the view list.
type primeView struct {
	Name  string
	Title string
}

// primeContext is the single source of truth behind both the markdown and JSON
// renderings, so the two can never drift.
type primeContext struct {
	Me      string
	JiraURL string

	AuthOK    bool
	AuthUser  string
	AuthEmail string
	AuthErr   string

	DBPath          string
	DBExists        bool
	DBSize          int64
	TotalIssues     int
	IssuesByProject map[string]int
	Pending         int

	Sources []primeSource

	FieldsTotal    int
	FieldsSystem   int
	FieldsCustom   int
	FieldsPromoted int

	Views []primeView

	MetricsOn       bool
	Stages          []string
	DoneResolutions []string
	TypeClasses     []string
	Ratio           string
	TrackedFields   []string

	MCPTools []string
}

// gatherPrime pulls everything prime reports from the engine. It degrades
// gracefully: a missing/empty DB yields zero counts (the config-derived scope
// and "how to sync" guidance still print), and a failed auth check is reported
// rather than fatal.
func gatherPrime(ctx context.Context, app *CLI) *primeContext {
	cfg := app.engine.Config
	st := app.engine.Store

	p := &primeContext{
		Me:      cfg.Me,
		JiraURL: cfg.Jira.URL,
		DBPath:  cfg.DB.Path,
	}

	switch {
	case app.engine.Client == nil:
		p.AuthErr = "not checked (no Jira client)"
	default:
		if me, err := app.engine.Client.MySelf(ctx); err != nil {
			p.AuthErr = err.Error()
		} else {
			p.AuthOK = true
			p.AuthUser = me.DisplayName
			p.AuthEmail = me.EmailAddress
		}
	}

	if info, err := os.Stat(cfg.DB.Path); err == nil {
		p.DBExists = true
		p.DBSize = info.Size()
	}

	p.TotalIssues, _ = st.TotalIssueCount()
	p.IssuesByProject, _ = st.IssueCountByProject()
	p.Pending, _ = st.CountPendingChanges()

	// Sources: config definitions first (so scope shows even on an empty DB),
	// then any synced-but-unconfigured source (stale/removed) so the agent sees
	// data that exists but is no longer in scope.
	metas, _ := st.AllSyncMeta()
	metaByName := make(map[string]*db.SyncMeta, len(metas))
	for _, m := range metas {
		metaByName[m.Project] = m
	}
	seen := map[string]bool{}
	for _, s := range cfg.SyncSources {
		ps := primeSource{Name: s.Name, JQL: s.JQL, Projects: s.Projects}
		fillSourceMeta(&ps, metaByName[s.Name])
		p.Sources = append(p.Sources, ps)
		seen[s.Name] = true
	}
	for _, m := range metas {
		if seen[m.Project] {
			continue
		}
		ps := primeSource{Name: m.Project}
		fillSourceMeta(&ps, m)
		p.Sources = append(p.Sources, ps)
	}

	if fms, err := st.AllFieldMappings(); err == nil {
		p.FieldsTotal = len(fms)
		for _, f := range fms {
			if f.IsCustom {
				p.FieldsCustom++
				if f.IsColumn {
					p.FieldsPromoted++
				}
			} else {
				p.FieldsSystem++
			}
		}
	}

	for _, v := range cfg.Views {
		p.Views = append(p.Views, primeView{Name: v.Name, Title: v.Title})
	}

	if m := cfg.Metrics; m != nil {
		p.MetricsOn = true
		p.Stages = sortedKeys(m.StatusStages)
		p.DoneResolutions = m.DoneResolutions
		p.TypeClasses = sortedKeys(m.TypeClasses)
		p.TrackedFields = m.TrackedFields
		if r := m.Ratio; r != nil {
			p.Ratio = fmt.Sprintf("%s / %s (cohort: %s)", r.Numerator, r.Denominator, r.Cohort)
		}
	}

	reg := mcp.NewToolsetRegistry(cfg.MCP.Toolsets, cfg.MCP.ReadOnly)
	tools := reg.ListEnabledTools()
	sort.Strings(tools)
	p.MCPTools = tools

	return p
}

// fillSourceMeta copies freshness fields from sync metadata when present.
func fillSourceMeta(ps *primeSource, m *db.SyncMeta) {
	if m == nil {
		return
	}
	if m.LastSyncTime.Valid {
		ps.LastSync = m.LastSyncTime.String
	}
	if m.LastFullSync.Valid {
		ps.LastFull = m.LastFullSync.String
	}
	if m.LastIssueUpdated.Valid && len(m.LastIssueUpdated.String) >= 10 {
		ps.DataThrough = m.LastIssueUpdated.String[:10]
	}
	if m.IssuesSynced.Valid {
		ps.Synced = m.IssuesSynced.Int64
	}
	ps.Warnings = syncWarnings(m)
}

// sortedKeys returns the map keys in stable order for deterministic output.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// meQuery returns the identifier to use in example queries: the configured `me`
// if set, otherwise a clearly-fake placeholder so examples never imply a real
// account.
func (p *primeContext) meQuery() string {
	if p.Me != "" {
		return p.Me
	}
	return "<your-account>"
}

// markdown renders the full agent-context document.
func (p *primeContext) markdown() string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	w("# jai agent context\n\n")
	w("jai mirrors Jira Cloud into a local SQLite database you query with SQL or JQL. ")
	w("This document is self-contained: it states what is mirrored, how fresh it is, how to query it, and the tools available.\n\n")

	// 1. Status & freshness.
	w("## Status & freshness\n\n")
	w("- Me: %s\n", orUnset(p.Me))
	w("- Jira: %s\n", orUnset(p.JiraURL))
	if p.AuthOK {
		w("- Auth: ✓ %s (%s)\n", p.AuthUser, p.AuthEmail)
	} else {
		w("- Auth: ✗ %s\n", p.AuthErr)
	}
	if p.DBExists {
		w("- DB: `%s` (%s)\n", p.DBPath, humanBytes(p.DBSize))
		w("- Issues: %d total%s\n", p.TotalIssues, projectBreakdown(p.IssuesByProject))
	} else {
		w("- DB: `%s` — not created yet; run `jai sync` to populate it\n", p.DBPath)
	}
	w("- Pending local changes: %d\n\n", p.Pending)

	// 2. Synced scope.
	w("## Synced sources (scope)\n\n")
	if len(p.Sources) == 0 {
		w("No sync sources configured. Add `sync_sources` to your config (or run `jai init`), then `jai sync`.\n\n")
	} else {
		for _, s := range p.Sources {
			w("- **%s**", s.Name)
			switch {
			case s.JQL != "":
				w(" — JQL: `%s`", s.JQL)
			case len(s.Projects) > 0:
				w(" — projects: %s", strings.Join(s.Projects, ", "))
			}
			w("\n")
			w("  - %s", freshness(s))
			w("\n")
			for _, warn := range s.Warnings {
				w("  - ⚠ %s\n", warn)
			}
		}
		w("\nOnly issues matching these sources are mirrored. Issues that moved out of scope (e.g. reassigned project, changed to no longer match a JQL source) may be stale or absent — treat the local snapshot as scoped, not global.\n\n")
	}

	// 3. Query surface.
	w("## Query surface\n\n")
	w("- **Local SQL** (fast, offline, as of last sync): `jai query \"SELECT ... FROM issues ...\"`. Restricted to `SELECT`/`WITH`. MCP: `jai_query sql=...`.\n")
	w("- **Local JQL** (translated to SQL over the local snapshot): `jai query \"status = 'In Progress'\"` (a bare arg that is not SELECT/WITH is auto-detected as JQL; or force with `--local-jql`). Only synced projects/fields are visible; history operators (WAS/CHANGED) are rejected.\n")
	w("- **Live JQL** (real-time against the Jira API): `jai query --jql \"...\"` (CLI) or `jai_query jql=...` (MCP). Use for non-synced projects; slower.\n")
	w("- **Field resolution:** a field name resolves through `field_map` to a core column, a `json_extract(custom_fields, ...)` on sparse JSON, or a promoted column — results are identical regardless of where the value is stored.\n")
	w("- **Text search:** `jai search <text>` (FTS5) or `summary ~ 'term'` in JQL.\n")
	w("- JQL datetime literals are interpreted in your Jira timezone.\n\n")

	// 4. Field discovery.
	w("## Fields\n\n")
	if p.FieldsTotal > 0 {
		w("%d fields mapped (%d system, %d custom, %d promoted to columns). ", p.FieldsTotal, p.FieldsSystem, p.FieldsCustom, p.FieldsPromoted)
	}
	w("Discover fields with `jai schema db` (issues table shape), `jai fields` (all fields), or `jai fields --stats` (population counts). Don't enumerate every field — look them up.\n\n")
	w("Commonly useful core fields: `key`, `summary`, `status`, `assignee`, `priority`, `type`, `created`, `updated`, `resolution`, `resolutiondate`, `labels`, `components`, `parent`.\n\n")

	// 5. Named views.
	w("## Named views\n\n")
	if len(p.Views) == 0 {
		w("No named views configured.\n\n")
	} else {
		for _, v := range p.Views {
			if v.Title != "" {
				w("- `jai view %s` — %s\n", v.Name, v.Title)
			} else {
				w("- `jai view %s`\n", v.Name)
			}
		}
		w("\n")
	}

	// 6. Metrics.
	w("## Flow metrics\n\n")
	if p.MetricsOn {
		w("Flow metrics are configured. Run `jai metrics` (CLI) or `jai_metrics` (MCP); facts are rebuilt from the changelog.\n")
		w("%s", bullet("Stages", p.Stages))
		w("%s", bullet("Done resolutions", p.DoneResolutions))
		w("%s", bullet("Type classes", p.TypeClasses))
		if p.Ratio != "" {
			w("- Ratio: %s\n", p.Ratio)
		}
		w("%s", bullet("Tracked fields", p.TrackedFields))
		w("\n")
	} else {
		w("Flow metrics are not configured (no `metrics:` block). Add one to enable `jai metrics`.\n\n")
	}

	// 7. MCP tools.
	w("## MCP tools\n\n")
	if len(p.MCPTools) == 0 {
		w("No MCP tools enabled.\n\n")
	} else {
		w("Available when embedded as an MCP server (`jai serve`): %s.\n\n", codeList(p.MCPTools))
	}

	// 8. Example queries.
	w("## Example queries\n\n")
	w("```bash\n")
	w("# My open work\n")
	w("jai query \"SELECT key, summary, status FROM issues WHERE assignee = '%s' AND resolution IS NULL ORDER BY updated DESC\"\n\n", p.meQuery())
	w("# Recently updated\n")
	w("jai query \"SELECT key, summary, updated FROM issues ORDER BY updated DESC LIMIT 20\"\n\n")
	w("# Count by status\n")
	w("jai query \"SELECT status, COUNT(*) AS n FROM issues GROUP BY status ORDER BY n DESC\"\n")
	if len(p.Views) > 0 {
		w("\n# Named view\n")
		w("jai view %s\n", p.Views[0].Name)
	}
	w("\n# Live JQL against Jira\n")
	w("jai query --jql \"assignee = currentUser() AND resolution = Unresolved\"\n")
	w("```\n")

	return b.String()
}

// jsonData returns the same content as a structured map for --json consumers.
func (p *primeContext) jsonData() map[string]any {
	auth := map[string]any{"ok": p.AuthOK}
	if p.AuthOK {
		auth["user"] = p.AuthUser
		auth["email"] = p.AuthEmail
	} else {
		auth["error"] = p.AuthErr
	}

	sources := make([]map[string]any, len(p.Sources))
	for i, s := range p.Sources {
		sources[i] = map[string]any{
			"name":           s.Name,
			"jql":            s.JQL,
			"projects":       s.Projects,
			"last_sync_time": s.LastSync,
			"last_full_sync": s.LastFull,
			"data_through":   s.DataThrough,
			"issues_synced":  s.Synced,
			"warnings":       s.Warnings,
			"healthy":        len(s.Warnings) == 0,
		}
	}

	views := make([]map[string]string, len(p.Views))
	for i, v := range p.Views {
		views[i] = map[string]string{"name": v.Name, "title": v.Title}
	}

	data := map[string]any{
		"me":       p.Me,
		"jira_url": p.JiraURL,
		"auth":     auth,
		"db": map[string]any{
			"path":              p.DBPath,
			"exists":            p.DBExists,
			"size_bytes":        p.DBSize,
			"total_issues":      p.TotalIssues,
			"issues_by_project": p.IssuesByProject,
			"pending_changes":   p.Pending,
		},
		"sources": sources,
		"fields": map[string]any{
			"total":    p.FieldsTotal,
			"system":   p.FieldsSystem,
			"custom":   p.FieldsCustom,
			"promoted": p.FieldsPromoted,
		},
		"views": views,
		"metrics": map[string]any{
			"enabled":          p.MetricsOn,
			"stages":           p.Stages,
			"done_resolutions": p.DoneResolutions,
			"type_classes":     p.TypeClasses,
			"ratio":            p.Ratio,
			"tracked_fields":   p.TrackedFields,
		},
		"mcp_tools": p.MCPTools,
		"query_surface": map[string]any{
			"sql_guard":        "local SQL restricted to SELECT/WITH",
			"local_jql":        "bare non-SELECT arg translated to SQL over the local snapshot",
			"live_jql":         "jai query --jql / jai_query jql= runs against the live Jira API",
			"field_resolution": "field name -> core column | json_extract(custom_fields) | promoted column via field_map",
			"text_search":      "jai search <text> (FTS5) or summary ~ 'term' in JQL",
		},
	}
	return data
}

func orUnset(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}

func projectBreakdown(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	keys := sortedKeys(m)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s: %d", k, m[k])
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func freshness(s primeSource) string {
	last := "never synced"
	if s.LastSync != "" {
		if t, err := time.Parse(time.RFC3339, s.LastSync); err == nil {
			last = "last sync " + humanDuration(time.Since(t)) + " ago"
		}
	}
	if s.DataThrough != "" {
		last += ", data through " + s.DataThrough
	}
	if s.Synced > 0 {
		last += fmt.Sprintf(" (%d synced)", s.Synced)
	}
	return last
}

func bullet(label string, items []string) string {
	if len(items) == 0 {
		return ""
	}
	return fmt.Sprintf("- %s: %s\n", label, strings.Join(items, ", "))
}

func codeList(items []string) string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = "`" + it + "`"
	}
	return strings.Join(out, ", ")
}

func init() {
	rootCmd.AddCommand(primeCmd)
}
