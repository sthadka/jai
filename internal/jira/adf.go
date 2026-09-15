package jira

import "strings"

// TextToADF converts plain text into an Atlassian Document Format (ADF)
// document, the shape Jira Cloud's REST v3 API requires for rich-text fields
// such as description, environment, and multi-line text custom fields.
//
// Blank lines separate paragraphs; single newlines within a paragraph become
// hardBreak nodes so line structure is preserved. The result always contains at
// least one (possibly empty) paragraph so it is a valid ADF document.
func TextToADF(text string) map[string]interface{} {
	// Normalize CRLF so line splitting is consistent across platforms.
	text = strings.ReplaceAll(text, "\r\n", "\n")

	var content []map[string]interface{}
	for _, para := range strings.Split(text, "\n\n") {
		lines := strings.Split(para, "\n")
		var nodes []map[string]interface{}
		for i, line := range lines {
			if i > 0 {
				nodes = append(nodes, map[string]interface{}{"type": "hardBreak"})
			}
			// ADF text nodes must be non-empty; skip empty lines (the
			// preceding hardBreak still records the line break).
			if line != "" {
				nodes = append(nodes, map[string]interface{}{"type": "text", "text": line})
			}
		}
		paragraph := map[string]interface{}{"type": "paragraph"}
		if len(nodes) > 0 {
			paragraph["content"] = nodes
		}
		content = append(content, paragraph)
	}
	if len(content) == 0 {
		content = append(content, map[string]interface{}{"type": "paragraph"})
	}

	return map[string]interface{}{
		"type":    "doc",
		"version": 1,
		"content": content,
	}
}

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
