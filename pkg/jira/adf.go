package jira

import "strings"

// IsADFField reports whether a Jira field must be written as an ADF document
// rather than a plain string. fieldType is jai's internal field type (from the
// field_map table); jiraID is the Jira field id.
//
// The jiraID check covers the well-known system rich-text fields even when the
// local field_map predates richtext typing (e.g. before a re-sync), so writes
// work without requiring a full re-sync first.
func IsADFField(jiraID, fieldType string) bool {
	if fieldType == "richtext" {
		return true
	}
	switch jiraID {
	case "description", "environment":
		return true
	}
	return false
}

// SchemaIsADF reports whether a Jira field schema describes an ADF rich-text
// field: the system description/environment fields, or a custom multi-line text
// (textarea) field.
func SchemaIsADF(schema *FieldSchema) bool {
	if schema == nil {
		return false
	}
	switch schema.System {
	case "description", "environment":
		return true
	}
	return strings.HasSuffix(schema.Custom, ":textarea")
}
