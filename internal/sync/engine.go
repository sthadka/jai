package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/jira"
)

// Progress reports sync progress for a project.
// Done=false means an intermediate update; Done=true means the project finished.
// ResumedFrom is non-empty on the first event of a resumed sync, holding the
// cursor timestamp the sync started from.
// JQL is set on the first event for each source, containing the effective JQL query.
type Progress struct {
	Project     string
	New         int
	Updated     int
	Total       int
	Error       error
	Done        bool
	ResumedFrom string
	JQL         string
}

// Engine orchestrates Jira sync operations.
type Engine struct {
	db     *db.DB
	client *jira.Client
	cfg    *config.Config
}

// New creates a new sync Engine.
func New(database *db.DB, client *jira.Client, cfg *config.Config) *Engine {
	return &Engine{db: database, client: client, cfg: cfg}
}

// SyncProjects fetches the display name for every distinct project key in the issues
// table and stores it in the projects table. Non-fatal: failures are logged to stderr.
func (e *Engine) SyncProjects(ctx context.Context) {
	rows, err := e.db.Query(`SELECT DISTINCT project FROM issues WHERE project != ''`)
	if err != nil {
		return
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err == nil && k != "" {
			keys = append(keys, k)
		}
	}
	rows.Close()

	for _, k := range keys {
		p, err := e.client.GetProject(ctx, k)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: fetching project %s: %v\n", k, err)
			continue
		}
		if err := e.db.UpsertProject(p.Key, p.Name); err != nil {
			fmt.Fprintf(os.Stderr, "warning: storing project %s: %v\n", k, err)
		}
	}
}

// DiscoverFields fetches field metadata from Jira and populates field_map.
func (e *Engine) DiscoverFields(ctx context.Context, overrides map[string]string) error {
	fields, err := e.client.Fields(ctx)
	if err != nil {
		return fmt.Errorf("fetching fields: %w", err)
	}

	// Pre-load existing name→jiraID mapping so we can detect true collisions
	// (same name owned by a different field) and disambiguate rather than skip.
	existing, _ := e.db.FieldMapByJiraID()
	takenNames := make(map[string]string, len(existing)) // name → jiraID
	for id, f := range existing {
		takenNames[f.Name] = id
	}

	for _, f := range fields {
		if f.Schema == nil {
			continue
		}
		name := inferColumnName(f, overrides)
		fieldType := jiraSchemaToType(f.Schema)

		// If this name is already owned by a different field, append a suffix
		// derived from the jira_id to make it unique.
		if ownerID, taken := takenNames[name]; taken && ownerID != f.ID {
			suffix := f.ID
			suffix = strings.TrimPrefix(suffix, "customfield_")
			fmt.Fprintf(os.Stderr, "warning: field %q (%s) renamed to %s_%s (name collision with %s)\n",
				f.Name, f.ID, name, suffix, ownerID)
			name = name + "_" + suffix
		}
		takenNames[name] = f.ID

		mapping := &db.FieldMapping{
			JiraID:     f.ID,
			JiraName:   f.Name,
			Name:       name,
			Type:       fieldType,
			IsCustom:   f.Custom,
			IsColumn:   false,
			Searchable: fieldType == "text" || fieldType == "array",
		}
		if _, overridden := overrides[f.ID]; overridden {
			mapping.UserOverride = true
		}

		if err := e.db.UpsertFieldMapping(mapping); err != nil {
			// Non-fatal: log and continue so one bad field doesn't abort discovery.
			fmt.Fprintf(os.Stderr, "warning: skipping field %s (%s): %v\n", f.ID, name, err)
			continue
		}
	}
	return nil
}

// effectiveSources returns the sync sources to run, optionally filtered to a
// single source by name.
func effectiveSources(cfg *config.Config, filter string) ([]config.SyncSource, error) {
	sources := cfg.SyncSources
	if filter == "" {
		return sources, nil
	}
	for _, s := range sources {
		if s.Name == filter {
			return []config.SyncSource{s}, nil
		}
	}
	return nil, fmt.Errorf("sync source %q not found", filter)
}

// sourceJQL builds the base JQL for a SyncSource.
func sourceJQL(s config.SyncSource) string {
	if s.JQL != "" {
		return s.JQL
	}
	quoted := make([]string, len(s.Projects))
	for i, p := range s.Projects {
		quoted[i] = `"` + p + `"`
	}
	return `project in (` + strings.Join(quoted, ", ") + `)`
}

// Sync runs a sync for all configured sources (or a single named one).
// It sends intermediate Progress updates (Done=false) as pages arrive,
// and a final Progress (Done=true) when each source finishes.
// The channel is closed when all sources are done.
// resume is only meaningful when full=true: if true, the sync continues from
// the last saved cursor instead of starting over.
func (e *Engine) Sync(ctx context.Context, full, resume bool, sourceFilter string) (<-chan Progress, error) {
	if resume && !full {
		return nil, fmt.Errorf("--resume requires --full")
	}

	sources, err := effectiveSources(e.cfg, sourceFilter)
	if err != nil {
		return nil, err
	}

	ch := make(chan Progress, 64)
	go func() {
		defer close(ch)
		for _, src := range sources {
			e.syncSource(ctx, src, full, resume, ch)
		}
	}()
	return ch, nil
}

func (e *Engine) syncSource(ctx context.Context, src config.SyncSource, full, resume bool, ch chan<- Progress) {
	start := time.Now()

	fieldMap, err := e.db.FieldMapByJiraID()
	if err != nil {
		ch <- Progress{Project: src.Name, Error: fmt.Errorf("loading field map: %w", err), Done: true}
		return
	}

	// Ensure custom field columns exist.
	if err := e.ensureCustomColumns(fieldMap); err != nil {
		ch <- Progress{Project: src.Name, Error: err, Done: true}
		return
	}

	base := sourceJQL(src)
	var jql string
	var resumedFrom string

	if full {
		if resume {
			cursor, err := e.db.GetResumeCursor(src.Name)
			if err == nil && cursor != "" {
				resumedFrom = cursor
				jqlTime := cursorToJQL(cursor)
				jql = fmt.Sprintf(`(%s) AND updated >= "%s" ORDER BY updated ASC`, base, jqlTime)
			} else {
				jql = base + ` ORDER BY updated ASC`
			}
		} else {
			// Fresh full sync: discard any stale cursor.
			_ = e.db.ClearResumeCursor(src.Name)
			jql = base + ` ORDER BY updated ASC`
		}
	} else {
		meta, err := e.db.GetSyncMeta(src.Name)
		if err != nil {
			ch <- Progress{Project: src.Name, Error: fmt.Errorf("loading sync meta: %w", err), Done: true}
			return
		}
		// Prefer the high-water mark (max updated of synced issues) over last_sync_time.
		// last_sync_time records when the sync ran, not the freshness of the data.
		hwm := meta.LastIssueUpdated.String
		if !meta.LastIssueUpdated.Valid || hwm == "" {
			hwm = meta.LastSyncTime.String
		}
		if hwm != "" {
			jqlTime := cursorToJQL(hwm)
			jql = fmt.Sprintf(`(%s) AND updated >= "%s" ORDER BY updated ASC`, base, jqlTime)
		} else {
			jql = base + ` ORDER BY updated ASC`
		}
	}

	fields := e.expandFields(fieldMap)
	var newCount, updatedCount, total int
	var lastUpdated string // max updated timestamp seen this run (for cursor)
	var pageUpsertedKeys []string

	// Emit an initial event so the display knows we're resuming and carries the JQL.
	ch <- Progress{Project: src.Name, ResumedFrom: resumedFrom, JQL: jql}

	for page, err := range e.client.SearchAll(ctx, jql, fields) {
		if err != nil {
			// Save cursor so the next --resume can pick up here.
			if full && lastUpdated != "" {
				_ = e.db.SetResumeCursor(src.Name, lastUpdated)
			}
			elapsed := time.Since(start).Seconds()
			_ = e.db.UpdateSyncMeta(src.Name, elapsed, total, newCount+updatedCount, err.Error(), lastUpdated)
			ch <- Progress{Project: src.Name, New: newCount, Updated: updatedCount, Total: total, Error: err, Done: true}
			return
		}

		for _, apiIssue := range page {
			total++
			rawBytes, err := json.Marshal(apiIssue)
			if err != nil {
				continue
			}

			issue, extra, err := Denormalize(rawBytes, fieldMap)
			if err != nil {
				continue
			}

			// Track max updated for resume cursor regardless of skip.
			if issue.Updated > lastUpdated {
				lastUpdated = issue.Updated
			}

			existingUpdated, _ := e.db.GetIssueUpdated(apiIssue.Key)
			if existingUpdated == "" {
				newCount++
			} else if existingUpdated == issue.Updated {
				// Issue unchanged since last sync — skip upsert.
				continue
			} else {
				updatedCount++
			}

			if err := e.db.UpsertIssue(issue, extra); err != nil {
				continue
			}
			pageUpsertedKeys = append(pageUpsertedKeys, apiIssue.Key)

			comments, err := ExtractComments(apiIssue.Key, rawBytes)
			if err == nil {
				for _, c := range comments {
					_ = e.db.UpsertComment(c)
				}
				if len(comments) > 0 {
					_ = e.db.UpdateIssueCommentsText(apiIssue.Key)
				}
			}

			links := ExtractIssueLinks(apiIssue.Key, rawBytes)
			_ = e.db.UpsertIssueLinks(apiIssue.Key, links)
		}

		// Sync changelogs for the issues we just upserted in this page.
		if len(pageUpsertedKeys) > 0 {
			e.syncChangelogsForKeys(ctx, pageUpsertedKeys)
			pageUpsertedKeys = pageUpsertedKeys[:0]
		}

		// Save cursor after each completed page so a restart can skip ahead.
		if full && lastUpdated != "" {
			_ = e.db.SetResumeCursor(src.Name, lastUpdated)
		}

		// Emit an intermediate update after each page.
		ch <- Progress{Project: src.Name, New: newCount, Updated: updatedCount, Total: total}
	}

	// Sync completed successfully — clear the resume cursor.
	if full {
		_ = e.db.ClearResumeCursor(src.Name)
	}

	// Run deletion detection on full sync for project-keyed sources only.
	// JQL sources have no reliable scope to diff against.
	if full && src.JQL == "" {
		for _, project := range src.Projects {
			if _, err := DetectDeletions(ctx, e.db, e.client, project); err != nil {
				_ = err // non-fatal
			}
		}
		_ = e.db.UpdateFullSyncMeta(src.Name)
	}

	// Sync project names so the TUI breadcrumb can show display names.
	if full {
		e.SyncProjects(ctx)
	}

	elapsed := time.Since(start).Seconds()
	_ = e.db.UpdateSyncMeta(src.Name, elapsed, total, newCount+updatedCount, "", lastUpdated)

	ch <- Progress{Project: src.Name, New: newCount, Updated: updatedCount, Total: total, Done: true}
}

// ChangelogProgress reports changelog sync progress.
type ChangelogProgress struct {
	Total     int
	Synced    int
	Skipped   int
	Error     error
	Done      bool
}

// SyncChangelogs fetches changelogs for issues that need them.
// Uses the bulk changelog API (up to 100 issues per request) with automatic
// fallback to per-issue fetching if the bulk endpoint is unavailable.
func (e *Engine) SyncChangelogs(ctx context.Context, sourceFilter string) (<-chan ChangelogProgress, error) {
	var projectFilter []string
	if sourceFilter != "" {
		sources, err := effectiveSources(e.cfg, sourceFilter)
		if err != nil {
			return nil, err
		}
		if len(sources) == 1 {
			projectFilter = sources[0].Projects
		}
	}

	candidates, err := e.db.GetChangelogSyncCandidates(projectFilter)
	if err != nil {
		return nil, fmt.Errorf("getting changelog candidates: %w", err)
	}

	ch := make(chan ChangelogProgress, 64)
	go func() {
		defer close(ch)

		total := len(candidates)
		synced := 0
		skipped := 0

		ch <- ChangelogProgress{Total: total}

		if total == 0 {
			ch <- ChangelogProgress{Total: 0, Done: true}
			return
		}

		idToKey, err := e.db.GetIssueIDToKeyMap()
		if err != nil {
			idToKey = nil
		}

		// Split candidates: those with known IDs use bulk, the rest use per-issue.
		var bulkKeys, fallbackKeys []string
		keyToID := make(map[string]bool)
		for _, key := range idToKey {
			keyToID[key] = true
		}
		for _, key := range candidates {
			if keyToID[key] {
				bulkKeys = append(bulkKeys, key)
			} else {
				fallbackKeys = append(fallbackKeys, key)
			}
		}

		// Bulk fetch in batches of 100.
		bulkFailed := false
		const batchSize = 100
		for i := 0; i < len(bulkKeys); i += batchSize {
			select {
			case <-ctx.Done():
				ch <- ChangelogProgress{Total: total, Synced: synced, Skipped: skipped, Error: ctx.Err(), Done: true}
				return
			default:
			}

			end := i + batchSize
			if end > len(bulkKeys) {
				end = len(bulkKeys)
			}
			batch := bulkKeys[i:end]

			entries, err := e.client.BulkFetchChangelogs(ctx, batch)
			if err != nil {
				if i == 0 {
					bulkFailed = true
					fallbackKeys = append(fallbackKeys, bulkKeys...)
					break
				}
				skipped += len(batch)
				ch <- ChangelogProgress{Total: total, Synced: synced, Skipped: skipped}
				continue
			}

			dbEntries := ExtractBulkChangelog(entries, idToKey)
			if err := e.db.InsertChangelogBatch(dbEntries); err != nil {
				skipped += len(batch)
				ch <- ChangelogProgress{Total: total, Synced: synced, Skipped: skipped}
				continue
			}

			// Mark these issues as changelog-synced (even if they had 0 entries).
			_ = e.db.MarkChangelogSynced(batch)

			synced += len(batch)
			ch <- ChangelogProgress{Total: total, Synced: synced, Skipped: skipped}
		}

		if bulkFailed {
			fmt.Fprintf(os.Stderr, "  ⚠ bulk changelog API unavailable, falling back to per-issue fetch\n")
		}

		// Per-issue fallback for issues without ID mapping or if bulk failed.
		for _, key := range fallbackKeys {
			select {
			case <-ctx.Done():
				ch <- ChangelogProgress{Total: total, Synced: synced, Skipped: skipped, Error: ctx.Err(), Done: true}
				return
			default:
			}

			resp, err := e.client.GetIssueChangelog(ctx, key)
			if err != nil {
				skipped++
				ch <- ChangelogProgress{Total: total, Synced: synced, Skipped: skipped}
				continue
			}

			dbEntries := ExtractChangelog(key, resp)
			for _, entry := range dbEntries {
				_ = e.db.InsertChangelog(entry)
			}
			_ = e.db.MarkChangelogSynced([]string{key})
			synced++

			if synced%10 == 0 {
				ch <- ChangelogProgress{Total: total, Synced: synced, Skipped: skipped}
			}
		}

		ch <- ChangelogProgress{Total: total, Synced: synced, Skipped: skipped, Done: true}
	}()

	return ch, nil
}

// syncChangelogsForKeys fetches and stores changelogs for the given issue keys.
// Called inline during issue sync for newly upserted issues. Errors are non-fatal.
func (e *Engine) syncChangelogsForKeys(ctx context.Context, keys []string) {
	if len(keys) == 0 {
		return
	}

	// Build id→key map for these keys. We can get the IDs from the DB since
	// we just upserted them with IDs.
	idToKey, err := e.db.GetIssueIDToKeyMapForKeys(keys)
	if err != nil || len(idToKey) == 0 {
		e.syncChangelogsPerIssue(ctx, keys)
		return
	}

	// Bulk fetch (keys are ≤100, fits in one API call).
	entries, err := e.client.BulkFetchChangelogs(ctx, keys)
	if err != nil {
		// Fallback to per-issue on bulk failure.
		e.syncChangelogsPerIssue(ctx, keys)
		return
	}

	dbEntries := ExtractBulkChangelog(entries, idToKey)
	_ = e.db.InsertChangelogBatch(dbEntries)
	_ = e.db.MarkChangelogSynced(keys)
}

// syncChangelogsPerIssue fetches changelogs one issue at a time, used as a
// fallback when bulk fetch is unavailable or an issue has no ID mapping yet.
func (e *Engine) syncChangelogsPerIssue(ctx context.Context, keys []string) {
	for _, key := range keys {
		resp, err := e.client.GetIssueChangelog(ctx, key)
		if err != nil {
			continue
		}
		entries := ExtractChangelog(key, resp)
		for _, entry := range entries {
			_ = e.db.InsertChangelog(entry)
		}
	}
	_ = e.db.MarkChangelogSynced(keys)
}

// cursorToJQL converts an RFC3339 timestamp to the format Jira JQL expects.
// Jira JQL does not support seconds in datetime strings — they cause a silent
// 0-result response. We truncate to minute granularity, which may re-fetch
// up to 60 seconds of already-synced issues (harmless upserts).
func cursorToJQL(cursor string) string {
	t, err := time.Parse(time.RFC3339, cursor)
	if err != nil {
		return cursor
	}
	return t.UTC().Format("2006-01-02 15:04")
}

// ensureCustomColumns creates columns in the issues table for custom fields that don't have one yet.
// It also updates the in-memory fieldMap so Denormalize sees is_column=true immediately on the
// same sync run (otherwise the first sync after column creation would skip the field).
func (e *Engine) ensureCustomColumns(fieldMap map[string]*db.FieldMapping) error {
	for _, f := range fieldMap {
		if !f.IsCustom || f.IsColumn {
			continue
		}
		colType := sqliteType(f.Type)
		if err := e.db.EnsureColumn(f.Name, colType); err != nil {
			return fmt.Errorf("adding column %s: %w", f.Name, err)
		}
		if err := e.db.MarkFieldAsColumn(f.JiraID); err != nil {
			return err
		}
		f.IsColumn = true // update in-memory map so Denormalize uses it this run
	}
	return nil
}

func (e *Engine) expandFields(fieldMap map[string]*db.FieldMapping) []string {
	// Always include standard fields + comment.
	fields := []string{
		"summary", "description", "status", "priority", "assignee", "reporter",
		"created", "updated", "resolutiondate", "labels", "components", "fixVersions",
		"parent", "issuetype", "project", "comment",
		"issuelinks", "resolution", "duedate",
		"timeoriginalestimate", "timespent", "timeestimate",
		"subtasks",
	}
	// Add custom fields.
	for jiraID, f := range fieldMap {
		if f.IsCustom {
			fields = append(fields, jiraID)
		}
	}
	return fields
}

// inferColumnName determines the DB column name for a Jira field.
func inferColumnName(f *jira.Field, overrides map[string]string) string {
	if name, ok := overrides[f.ID]; ok {
		return name
	}
	// Slugify: "Custom Team Field" → "custom_team_field"
	name := strings.ToLower(f.Name)
	re := regexp.MustCompile(`[^a-z0-9]+`)
	name = re.ReplaceAllString(name, "_")
	name = strings.Trim(name, "_")
	if name == "" {
		name = strings.ToLower(f.ID)
	}
	// SQLite identifiers cannot start with a digit.
	if len(name) > 0 && name[0] >= '0' && name[0] <= '9' {
		name = "_" + name
	}
	return name
}

// jiraSchemaToType maps Jira field schema to our internal type name.
func jiraSchemaToType(schema *jira.FieldSchema) string {
	switch schema.Type {
	case "number":
		return "number"
	case "date":
		return "date"
	case "datetime":
		return "datetime"
	case "array":
		return "array"
	case "option":
		return "option"
	case "user":
		return "user"
	default:
		return "text"
	}
}

// sqliteType maps our type name to an SQLite column type.
func sqliteType(t string) string {
	switch t {
	case "number":
		return "REAL"
	case "date", "datetime":
		return "DATETIME"
	default:
		return "TEXT"
	}
}
