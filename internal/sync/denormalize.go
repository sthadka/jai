package sync

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/jira"
)

// jiraDateFormats lists the timestamp formats Jira Cloud uses.
// The colon-less timezone offset (+0000) is not RFC3339, so go-sqlite3
// fails to parse it back from DATETIME columns, returning a zero time.Time.
// We normalise to RFC3339 at write time so SQLite date functions also work.
var jiraDateFormats = []string{
	"2006-01-02T15:04:05.999-0700",
	"2006-01-02T15:04:05.999Z07:00",
	"2006-01-02T15:04:05-0700",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02",
}

func normalizeDate(s string) string {
	if s == "" {
		return ""
	}
	for _, layout := range jiraDateFormats {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return s // unrecognised format — store as-is
}

// Denormalize extracts column values from a raw Jira issue JSON blob using the field map.
// Returns: (fixed IssueFields, extra dynamic columns map).
func Denormalize(raw []byte, fieldMap map[string]*db.FieldMapping) (*db.Issue, map[string]interface{}, error) {
	var apiIssue jira.Issue
	if err := json.Unmarshal(raw, &apiIssue); err != nil {
		return nil, nil, err
	}

	var fields jira.IssueFields
	if err := json.Unmarshal(apiIssue.Fields, &fields); err != nil {
		return nil, nil, err
	}

	issue := &db.Issue{
		Key:     apiIssue.Key,
		Summary: fields.Summary,
		RawJSON: string(raw),
	}

	if fields.Project != nil {
		issue.Project = fields.Project.Key
	}
	if fields.IssueType != nil {
		issue.Type = fields.IssueType.Name
	}
	if fields.Description != nil {
		issue.Description = jira.ADFToPlaintext(fields.Description)
	}
	if fields.Status != nil {
		issue.Status = fields.Status.Name
		if fields.Status.StatusCategory != nil {
			issue.StatusCategory = fields.Status.StatusCategory.Name
		}
	}
	if fields.Priority != nil {
		issue.Priority = fields.Priority.Name
	}
	if fields.Assignee != nil {
		issue.Assignee = fields.Assignee.DisplayName
		issue.AssigneeEmail = fields.Assignee.EmailAddress
	}
	if fields.Reporter != nil {
		issue.Reporter = fields.Reporter.DisplayName
	}
	issue.Created = normalizeDate(fields.Created)
	issue.Updated = normalizeDate(fields.Updated)
	issue.Resolved = normalizeDate(fields.ResolutionDate)

	if len(fields.Labels) > 0 {
		issue.Labels = strings.Join(fields.Labels, ",")
	}
	if len(fields.Components) > 0 {
		names := make([]string, len(fields.Components))
		for i, c := range fields.Components {
			names[i] = c.Name
		}
		issue.Components = strings.Join(names, ",")
	}
	if len(fields.FixVersions) > 0 {
		names := make([]string, len(fields.FixVersions))
		for i, v := range fields.FixVersions {
			names[i] = v.Name
		}
		issue.FixVersion = strings.Join(names, ",")
	}
	if fields.Parent != nil {
		issue.ParentKey = fields.Parent.Key
	}
	if fields.Resolution != nil {
		issue.Resolution = fields.Resolution.Name
	}
	issue.DueDate = normalizeDate(fields.DueDate)
	if fields.TimeOriginalEstimate != nil {
		issue.OriginalEstimate = sql.NullInt64{Int64: *fields.TimeOriginalEstimate, Valid: true}
	}
	if fields.TimeSpent != nil {
		issue.TimeSpent = sql.NullInt64{Int64: *fields.TimeSpent, Valid: true}
	}
	if fields.TimeEstimate != nil {
		issue.RemainingEstimate = sql.NullInt64{Int64: *fields.TimeEstimate, Valid: true}
	}
	if len(fields.Subtasks) > 0 {
		keys := make([]string, len(fields.Subtasks))
		for i, s := range fields.Subtasks {
			keys[i] = s.Key
		}
		issue.SubtaskKeys = strings.Join(keys, ",")
	}

	// Extract custom fields as dynamic columns.
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(apiIssue.Fields, &rawFields); err != nil {
		return issue, nil, nil
	}

	extra := make(map[string]interface{})
	for jiraID, mapping := range fieldMap {
		if !mapping.IsCustom || !mapping.IsColumn {
			continue
		}
		rawVal, ok := rawFields[jiraID]
		if !ok || string(rawVal) == "null" {
			continue
		}
		val := extractFieldValue(rawVal, mapping.Type)
		if val != nil {
			extra[mapping.Name] = val
		}
	}

	return issue, extra, nil
}

// extractFieldValue converts a raw JSON field value to a Go value based on Jira field type.
func extractFieldValue(raw json.RawMessage, fieldType string) interface{} {
	switch fieldType {
	case "text", "string":
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		if v := jira.ADFToPlaintext(raw); v != "" {
			return v
		}
		// Some Jira field types (e.g. Team) return objects with a name property.
		var obj struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &obj); err == nil && obj.Name != "" {
			return obj.Name
		}

	case "number":
		var f float64
		if err := json.Unmarshal(raw, &f); err == nil {
			return f
		}

	case "date", "datetime":
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}

	case "option":
		var obj struct {
			Value string `json:"value"`
		}
		if err := json.Unmarshal(raw, &obj); err == nil && obj.Value != "" {
			return obj.Value
		}

	case "user":
		var obj struct {
			DisplayName string `json:"displayName"`
		}
		if err := json.Unmarshal(raw, &obj); err == nil && obj.DisplayName != "" {
			return obj.DisplayName
		}

	case "array":
		// Try array of options.
		var opts []struct {
			Value string `json:"value"`
			Name  string `json:"name"`
		}
		if err := json.Unmarshal(raw, &opts); err == nil {
			names := make([]string, 0, len(opts))
			for _, o := range opts {
				if o.Value != "" {
					names = append(names, o.Value)
				} else if o.Name != "" {
					names = append(names, o.Name)
				}
			}
			if len(names) > 0 {
				return strings.Join(names, ",")
			}
		}
		// Try array of strings.
		var strs []string
		if err := json.Unmarshal(raw, &strs); err == nil {
			return strings.Join(strs, ",")
		}
	}
	return nil
}

// ExtractIssueLinks extracts formal Jira issue links from a raw issue JSON.
// Each link produces two rows (inward + outward perspective) so both sides are queryable.
func ExtractIssueLinks(issueKey string, raw []byte) []db.IssueLink {
	var apiIssue jira.Issue
	if err := json.Unmarshal(raw, &apiIssue); err != nil {
		return nil
	}
	var fields jira.IssueFields
	if err := json.Unmarshal(apiIssue.Fields, &fields); err != nil {
		return nil
	}

	var links []db.IssueLink
	for _, l := range fields.IssueLinks {
		if l.InwardIssue != nil {
			links = append(links, db.IssueLink{
				ID:        l.ID + "_in",
				IssueKey:  issueKey,
				LinkType:  l.Type.Inward,
				Direction: "inward",
				LinkedKey: l.InwardIssue.Key,
			})
		}
		if l.OutwardIssue != nil {
			links = append(links, db.IssueLink{
				ID:        l.ID + "_out",
				IssueKey:  issueKey,
				LinkType:  l.Type.Outward,
				Direction: "outward",
				LinkedKey: l.OutwardIssue.Key,
			})
		}
	}
	return links
}

// ExtractComments extracts Jira comments from a raw issue JSON.
func ExtractComments(issueKey string, raw []byte) ([]*db.Comment, error) {
	var apiIssue jira.Issue
	if err := json.Unmarshal(raw, &apiIssue); err != nil {
		return nil, err
	}

	var fields jira.IssueFields
	if err := json.Unmarshal(apiIssue.Fields, &fields); err != nil {
		return nil, err
	}

	if fields.Comment == nil {
		return nil, nil
	}

	comments := make([]*db.Comment, 0, len(fields.Comment.Comments))
	for _, c := range fields.Comment.Comments {
		dbC := &db.Comment{
			ID:       c.ID,
			IssueKey: issueKey,
			Body:     jira.ADFToPlaintext(c.Body),
			Created:  normalizeDate(c.Created),
			Updated:  normalizeDate(c.Updated),
		}
		if c.Author != nil {
			dbC.Author = c.Author.DisplayName
			dbC.AuthorEmail = c.Author.EmailAddress
		}
		comments = append(comments, dbC)
	}
	return comments, nil
}
