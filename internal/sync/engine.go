package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/syethadk/jai/internal/config"
	"github.com/syethadk/jai/internal/db"
	"github.com/syethadk/jai/internal/jira"
)

// Progress reports sync progress for a project.
type Progress struct {
	Project string
	New     int
	Updated int
	Total   int
	Error   error
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

	for _, f := range fields {
		if f.Schema == nil {
			continue
		}
		name := inferColumnName(f, overrides)
		fieldType := jiraSchemaToType(f.Schema)

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
			return fmt.Errorf("upserting field %s: %w", f.ID, err)
		}
	}
	return nil
}

// Sync runs an incremental sync for all configured projects.
// It sends progress updates over the returned channel (closed when done).
func (e *Engine) Sync(ctx context.Context, full bool) (<-chan Progress, error) {
	ch := make(chan Progress, len(e.cfg.Jira.Projects))

	go func() {
		defer close(ch)
		for _, project := range e.cfg.Jira.Projects {
			p := e.syncProject(ctx, project, full)
			ch <- p
		}
	}()

	return ch, nil
}

func (e *Engine) syncProject(ctx context.Context, project string, full bool) Progress {
	start := time.Now()

	fieldMap, err := e.db.FieldMapByJiraID()
	if err != nil {
		return Progress{Project: project, Error: fmt.Errorf("loading field map: %w", err)}
	}

	// Ensure custom field columns exist.
	if err := e.ensureCustomColumns(fieldMap); err != nil {
		return Progress{Project: project, Error: err}
	}

	var jql string
	if full {
		jql = fmt.Sprintf(`project = "%s" ORDER BY updated ASC`, project)
	} else {
		meta, err := e.db.GetSyncMeta(project)
		if err != nil {
			return Progress{Project: project, Error: fmt.Errorf("loading sync meta: %w", err)}
		}

		if meta.LastSyncTime.Valid && meta.LastSyncTime.String != "" {
			jql = fmt.Sprintf(`project = "%s" AND updated >= "%s" ORDER BY updated ASC`, project, meta.LastSyncTime.String)
		} else {
			// First sync: fetch all.
			jql = fmt.Sprintf(`project = "%s" ORDER BY updated ASC`, project)
		}
	}

	fields := e.expandFields(fieldMap)

	var newCount, updatedCount, total int

	for page, err := range e.client.SearchAll(ctx, jql, fields) {
		if err != nil {
			elapsed := time.Since(start).Seconds()
			_ = e.db.UpdateSyncMeta(project, elapsed, total, newCount+updatedCount, err.Error())
			return Progress{Project: project, New: newCount, Updated: updatedCount, Total: total, Error: err}
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

			// Extract and store comments.
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
	}

	// Run deletion detection on full sync.
	if full {
		deleted, err := DetectDeletions(ctx, e.db, e.client, project)
		if err != nil {
			// Non-fatal: log and continue.
			_ = err
		} else {
			_ = deleted
		}
		_ = e.db.UpdateFullSyncMeta(project)
	}

	elapsed := time.Since(start).Seconds()
	_ = e.db.UpdateSyncMeta(project, elapsed, total, newCount+updatedCount, "")

	return Progress{Project: project, New: newCount, Updated: updatedCount, Total: total}
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
