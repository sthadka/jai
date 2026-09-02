package mcp

import (
	"encoding/json"
	"reflect"

	"github.com/sthadka/jai/internal/jira"
)

// excludedColumns defines columns that should never appear in MCP responses
// unless explicitly requested.
var excludedColumns = map[string]bool{
	"raw_json":      true,
	"comments_text": true,
	"synced_at":     true,
	"id":            true,
}

// defaultGetFields defines the default field set when fields parameter is omitted
// on jai_get. These are curated to match the CLI's frontMatterEntries.
var defaultGetFields = []string{
	"key", "summary", "status", "status_category", "issue_type", "priority",
	"assignee", "assignee_email", "reporter", "labels", "components",
	"fix_versions", "parent_key", "created", "updated", "resolution",
	"story_points", "sprint_name", "project", "description",
}

// stripNulls removes any key-value pair where the value is nil, empty string,
// or a zero-value for the type.
func stripNulls(data map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range data {
		if v == nil {
			continue
		}

		// Check for empty string
		if s, ok := v.(string); ok && s == "" {
			continue
		}

		// Check for zero values using reflection
		rv := reflect.ValueOf(v)
		if rv.IsValid() && rv.IsZero() {
			continue
		}

		result[k] = v
	}
	return result
}

// filterExcluded removes excluded columns from a response map.
func filterExcluded(data map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range data {
		if !excludedColumns[k] {
			result[k] = v
		}
	}
	return result
}

// applyResponseFilters applies the full filter chain for MCP responses.
// - If requestedFields is empty, apply defaultGetFields
// - If requestedFields is "all", keep everything except excluded columns
// - If requestedFields is specific fields, use those
// - Always strip nulls
// - Always filter excluded columns (unless "all" is requested)
func applyResponseFilters(data map[string]interface{}, requestedFields string) map[string]interface{} {
	var result map[string]interface{}

	// Determine which fields to keep
	switch requestedFields {
	case "":
		// Default: use curated field set
		result = make(map[string]interface{})
		for _, field := range defaultGetFields {
			if val, ok := data[field]; ok {
				result[field] = val
			}
		}
	case "all":
		// All fields except excluded columns
		result = filterExcluded(data)
	default:
		// Specific fields requested - parse and filter
		fields := parseFieldList(requestedFields)
		result = make(map[string]interface{})
		for _, field := range fields {
			// Allow explicitly requested excluded columns
			if val, ok := data[field]; ok {
				result[field] = val
			}
		}
	}

	// Always strip nulls from the result
	return stripNulls(result)
}

// parseFieldList parses a comma-separated field list into a slice.
func parseFieldList(fields string) []string {
	if fields == "" {
		return nil
	}

	result := []string{}
	current := ""
	for _, ch := range fields {
		if ch == ',' {
			if current != "" {
				result = append(result, current)
				current = ""
			}
		} else if ch != ' ' && ch != '\t' {
			current += string(ch)
		}
	}
	if current != "" {
		result = append(result, current)
	}

	return result
}

// issueFieldsToMap converts a Jira API IssueFields struct to a map,
// extracting standard fields. This is used when falling back to the API
// in jai_get when an issue isn't in the local database.
func issueFieldsToMap(key string, fields json.RawMessage) map[string]interface{} {
	m := map[string]interface{}{"key": key}

	// Parse the raw JSON fields into IssueFields struct
	var f struct {
		Summary     string          `json:"summary"`
		Description json.RawMessage `json:"description"`
		Status      *struct {
			Name           string `json:"name"`
			StatusCategory *struct {
				Name string `json:"name"`
			} `json:"statusCategory"`
		} `json:"status"`
		Priority *struct {
			Name string `json:"name"`
		} `json:"priority"`
		Assignee *struct {
			DisplayName  string `json:"displayName"`
			EmailAddress string `json:"emailAddress"`
		} `json:"assignee"`
		Reporter *struct {
			DisplayName  string `json:"displayName"`
			EmailAddress string `json:"emailAddress"`
		} `json:"reporter"`
		Created        string   `json:"created"`
		Updated        string   `json:"updated"`
		ResolutionDate string   `json:"resolutiondate"`
		Labels         []string `json:"labels"`
		Components     []struct {
			Name string `json:"name"`
		} `json:"components"`
		FixVersions []struct {
			Name string `json:"name"`
		} `json:"fixVersions"`
		Parent *struct {
			Key string `json:"key"`
		} `json:"parent"`
		IssueType *struct {
			Name string `json:"name"`
		} `json:"issuetype"`
		Project *struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"project"`
		Resolution *struct {
			Name string `json:"name"`
		} `json:"resolution"`
		DueDate string `json:"duedate"`
	}

	if err := json.Unmarshal(fields, &f); err != nil {
		// If parsing fails, return just the key
		return m
	}

	// Extract fields
	m["summary"] = f.Summary

	if f.Status != nil {
		m["status"] = f.Status.Name
		if f.Status.StatusCategory != nil {
			m["status_category"] = f.Status.StatusCategory.Name
		}
	}

	if f.Priority != nil {
		m["priority"] = f.Priority.Name
	}

	if f.Assignee != nil {
		m["assignee"] = f.Assignee.DisplayName
		m["assignee_email"] = f.Assignee.EmailAddress
	}

	if f.Reporter != nil {
		m["reporter"] = f.Reporter.DisplayName
	}

	if f.IssueType != nil {
		m["issue_type"] = f.IssueType.Name
	}

	if f.Project != nil {
		m["project"] = f.Project.Key
	}

	if f.Parent != nil {
		m["parent_key"] = f.Parent.Key
	}

	if f.Resolution != nil {
		m["resolution"] = f.Resolution.Name
	}

	m["created"] = f.Created
	m["updated"] = f.Updated
	m["due_date"] = f.DueDate
	m["resolution_date"] = f.ResolutionDate

	if len(f.Labels) > 0 {
		// Convert to comma-separated string to match DB format
		labelStr := ""
		for i, label := range f.Labels {
			if i > 0 {
				labelStr += ", "
			}
			labelStr += label
		}
		m["labels"] = labelStr
	}

	if len(f.Components) > 0 {
		compStr := ""
		for i, comp := range f.Components {
			if i > 0 {
				compStr += ", "
			}
			compStr += comp.Name
		}
		m["components"] = compStr
	}

	if len(f.FixVersions) > 0 {
		versionStr := ""
		for i, ver := range f.FixVersions {
			if i > 0 {
				versionStr += ", "
			}
			versionStr += ver.Name
		}
		m["fix_versions"] = versionStr
	}

	// Convert ADF description to markdown if present
	if len(f.Description) > 0 {
		if md := jira.ADFToMarkdown(f.Description); md != "" {
			m["description"] = md
		}
	}

	return m
}
