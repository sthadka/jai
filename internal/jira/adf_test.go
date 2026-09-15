package jira

import "testing"

func TestIsADFField(t *testing.T) {
	cases := []struct {
		jiraID    string
		fieldType string
		want      bool
	}{
		{"description", "text", true},     // seeded type predates richtext
		{"description", "richtext", true}, // after re-sync/migration
		{"environment", "text", true},
		{"customfield_10050", "richtext", true},
		{"summary", "text", false}, // plain single-line text
		{"priority", "option", false},
		{"customfield_10010", "text", false},
	}
	for _, c := range cases {
		if got := IsADFField(c.jiraID, c.fieldType); got != c.want {
			t.Errorf("IsADFField(%q, %q) = %v, want %v", c.jiraID, c.fieldType, got, c.want)
		}
	}
}

func TestSchemaIsADF(t *testing.T) {
	cases := []struct {
		name   string
		schema *FieldSchema
		want   bool
	}{
		{"nil", nil, false},
		{"description", &FieldSchema{Type: "string", System: "description"}, true},
		{"environment", &FieldSchema{Type: "string", System: "environment"}, true},
		{"summary", &FieldSchema{Type: "string", System: "summary"}, false},
		{"textarea custom", &FieldSchema{Type: "string", Custom: "com.atlassian.jira.plugin.system.customfieldtypes:textarea"}, true},
		{"textfield custom", &FieldSchema{Type: "string", Custom: "com.atlassian.jira.plugin.system.customfieldtypes:textfield"}, false},
	}
	for _, c := range cases {
		if got := SchemaIsADF(c.schema); got != c.want {
			t.Errorf("SchemaIsADF(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}
