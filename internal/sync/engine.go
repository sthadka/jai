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
type Progress struct {
	Project string
	New     int
	Updated int
	Total   int
	Error   error
	Done    bool
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
			if strings.HasPrefix(suffix, "customfield_") {
				suffix = suffix[len("customfield_"):]
			}
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
// single source by name. If no sync_sources are configured, one source per
// entry in jira.projects is generated for backward compatibility.
func effectiveSources(cfg *config.Config, filter string) ([]config.SyncSource, error) {
	sources := cfg.SyncSources
	if len(sources) == 0 {
		for _, p := range cfg.Jira.Projects {
			sources = append(sources, config.SyncSource{
				Name:     p,
				Projects: []string{p},
			})
		}
	}
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
func (e *Engine) Sync(ctx context.Context, full bool, sourceFilter string) (<-chan Progress, error) {
	sources, err := effectiveSources(e.cfg, sourceFilter)
	if err != nil {
		return nil, err
	}

	ch := make(chan Progress, 64)
	go func() {
		defer close(ch)
		for _, src := range sources {
			e.syncSource(ctx, src, full, ch)
		}
	}()
	return ch, nil
}

func (e *Engine) syncSource(ctx context.Context, src config.SyncSource, full bool, ch chan<- Progress) {
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
	if full {
		jql = base + ` ORDER BY updated ASC`
	} else {
		meta, err := e.db.GetSyncMeta(src.Name)
		if err != nil {
			ch <- Progress{Project: src.Name, Error: fmt.Errorf("loading sync meta: %w", err), Done: true}
			return
		}
		if meta.LastSyncTime.Valid && meta.LastSyncTime.String != "" {
			jql = fmt.Sprintf(`(%s) AND updated >= "%s" ORDER BY updated ASC`, base, meta.LastSyncTime.String)
		} else {
			jql = base + ` ORDER BY updated ASC`
		}
	}

	fields := e.expandFields(fieldMap)
	var newCount, updatedCount, total int

	for page, err := range e.client.SearchAll(ctx, jql, fields) {
		if err != nil {
			elapsed := time.Since(start).Seconds()
			_ = e.db.UpdateSyncMeta(src.Name, elapsed, total, newCount+updatedCount, err.Error())
			ch <- Progress{Project: src.Name, New: newCount, Updated: updatedCount, Total: total, Error: err, Done: true}
			return
		}

		for _, apiIssue := range page {
			total++
			rawBytes, err := json.Marshal(apiIssue)
			if err != nil {
				continue
			}

			existing, _ := e.db.GetIssue(apiIssue.Key)
			if existing == nil {
				newCount++
			} else {
				updatedCount++
			}

			issue, extra, err := Denormalize(rawBytes, fieldMap)
			if err != nil {
				continue
			}

			if err := e.db.UpsertIssue(issue, extra); err != nil {
				continue
			}

			comments, err := ExtractComments(apiIssue.Key, rawBytes)
			if err == nil {
				for _, c := range comments {
					_ = e.db.UpsertComment(c)
				}
				if len(comments) > 0 {
					_ = e.db.UpdateIssueCommentsText(apiIssue.Key)
				}
			}
		}

		// Emit an intermediate update after each page.
		ch <- Progress{Project: src.Name, New: newCount, Updated: updatedCount, Total: total}
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

	elapsed := time.Since(start).Seconds()
	_ = e.db.UpdateSyncMeta(src.Name, elapsed, total, newCount+updatedCount, "")

	ch <- Progress{Project: src.Name, New: newCount, Updated: updatedCount, Total: total, Done: true}
}

// ensureCustomColumns creates columns in the issues table for custom fields that don't have one yet.
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
	}
	return nil
}

func (e *Engine) expandFields(fieldMap map[string]*db.FieldMapping) []string {
	// Always include standard fields + comment.
	fields := []string{
		"summary", "description", "status", "priority", "assignee", "reporter",
		"created", "updated", "resolutiondate", "labels", "components", "fixVersions",
		"parent", "issuetype", "project", "comment",
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
