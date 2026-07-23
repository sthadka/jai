package cli

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/sthadka/jai/internal/db"
)

func TestExtractCloneFields(t *testing.T) {
	rawJSON := `{
		"key": "PROJ-123",
		"fields": {
			"project": {"key": "PROJ"},
			"summary": "Original summary",
			"issuetype": {"name": "Bug"},
			"priority": {"name": "High"},
			"labels": ["backend", "urgent"],
			"components": [{"name": "API"}, {"name": "DB"}],
			"assignee": {"accountId": "abc123", "displayName": "Alice"},
			"parent": {"key": "PROJ-100"},
			"fixVersions": [{"name": "1.0"}, {"name": "2.0"}],
			"description": {
				"type": "doc",
				"version": 1,
				"content": [{"type": "paragraph", "content": [{"type": "text", "text": "Hello world"}]}]
			},
			"customfield_10001": "custom value",
			"customfield_10002": null,
			"status": {"name": "Open"},
			"reporter": {"accountId": "xyz"}
		}
	}`

	fields, project, err := extractCloneFields(rawJSON, nil)
	if err != nil {
		t.Fatalf("extractCloneFields: %v", err)
	}

	if project != "PROJ" {
		t.Errorf("project = %q, want %q", project, "PROJ")
	}

	// Check required fields.
	if got := fields["summary"]; got != "Original summary" {
		t.Errorf("summary = %v, want %q", got, "Original summary")
	}

	if got, ok := fields["issuetype"].(map[string]string); !ok || got["name"] != "Bug" {
		t.Errorf("issuetype = %v, want {name: Bug}", fields["issuetype"])
	}

	if got, ok := fields["project"].(map[string]string); !ok || got["key"] != "PROJ" {
		t.Errorf("project = %v, want {key: PROJ}", fields["project"])
	}

	// Priority.
	if got, ok := fields["priority"].(map[string]string); !ok || got["name"] != "High" {
		t.Errorf("priority = %v, want {name: High}", fields["priority"])
	}

	// Labels.
	if got, ok := fields["labels"].([]string); !ok || !reflect.DeepEqual(got, []string{"backend", "urgent"}) {
		t.Errorf("labels = %v, want [backend, urgent]", fields["labels"])
	}

	// Components.
	comps, ok := fields["components"].([]map[string]string)
	if !ok || len(comps) != 2 || comps[0]["name"] != "API" || comps[1]["name"] != "DB" {
		t.Errorf("components = %v, want [{name:API}, {name:DB}]", fields["components"])
	}

	// Assignee.
	if got, ok := fields["assignee"].(map[string]string); !ok || got["accountId"] != "abc123" {
		t.Errorf("assignee = %v, want {accountId: abc123}", fields["assignee"])
	}

	// Parent.
	if got, ok := fields["parent"].(map[string]string); !ok || got["key"] != "PROJ-100" {
		t.Errorf("parent = %v, want {key: PROJ-100}", fields["parent"])
	}

	// Fix versions.
	fv, ok := fields["fixVersions"].([]map[string]string)
	if !ok || len(fv) != 2 || fv[0]["name"] != "1.0" || fv[1]["name"] != "2.0" {
		t.Errorf("fixVersions = %v, want [{name:1.0}, {name:2.0}]", fields["fixVersions"])
	}

	// Description (ADF).
	if _, ok := fields["description"].(map[string]interface{}); !ok {
		t.Errorf("description should be a map (ADF doc), got %T", fields["description"])
	}

	// Custom field (non-null should be copied).
	if got := fields["customfield_10001"]; got != "custom value" {
		t.Errorf("customfield_10001 = %v, want %q", got, "custom value")
	}

	// Null custom field should not be copied.
	if _, ok := fields["customfield_10002"]; ok {
		t.Error("customfield_10002 should not be in fields (was null)")
	}

	// Non-cloneable fields should not be copied.
	if _, ok := fields["status"]; ok {
		t.Error("status should not be in cloned fields")
	}
	if _, ok := fields["reporter"]; ok {
		t.Error("reporter should not be in cloned fields")
	}
}

func TestExtractCloneFieldsMinimal(t *testing.T) {
	rawJSON := `{
		"key": "MIN-1",
		"fields": {
			"project": {"key": "MIN"},
			"summary": "Minimal issue",
			"issuetype": {"name": "Task"}
		}
	}`

	fields, project, err := extractCloneFields(rawJSON, nil)
	if err != nil {
		t.Fatalf("extractCloneFields: %v", err)
	}
	if project != "MIN" {
		t.Errorf("project = %q, want %q", project, "MIN")
	}
	if got := fields["summary"]; got != "Minimal issue" {
		t.Errorf("summary = %v, want %q", got, "Minimal issue")
	}
	if _, ok := fields["labels"]; ok {
		t.Error("labels should not be set for minimal issue")
	}
}

func TestExtractCloneFieldsSkipsRank(t *testing.T) {
	rawJSON := `{
		"key": "PROJ-123",
		"fields": {
			"project": {"key": "PROJ"},
			"summary": "Has a rank",
			"issuetype": {"name": "Task"},
			"customfield_10019": "0|iq7psn:",
			"customfield_10001": "custom value"
		}
	}`

	fieldMap := map[string]*db.FieldMapping{
		"customfield_10019": {JiraID: "customfield_10019", JiraName: "Rank", Type: "text"},
	}

	fields, _, err := extractCloneFields(rawJSON, fieldMap)
	if err != nil {
		t.Fatalf("extractCloneFields: %v", err)
	}

	if _, ok := fields["customfield_10019"]; ok {
		t.Error("customfield_10019 (Rank) should not be copied — Jira's create API rejects its GET representation")
	}
	if got := fields["customfield_10001"]; got != "custom value" {
		t.Errorf("customfield_10001 = %v, want %q", got, "custom value")
	}
}

func TestApplyFieldOverride(t *testing.T) {
	fieldMap := map[string]*db.FieldMapping{
		"customfield_10001": {JiraID: "customfield_10001", Name: "story_points", JiraName: "Story Points", Type: "number"},
	}

	tests := []struct {
		name      string
		fieldName string
		value     string
		checkKey  string
		checkVal  interface{}
	}{
		{
			name:      "summary",
			fieldName: "summary",
			value:     "New Summary",
			checkKey:  "summary",
			checkVal:  "New Summary",
		},
		{
			name:      "priority",
			fieldName: "priority",
			value:     "Low",
			checkKey:  "priority",
			checkVal:  map[string]string{"name": "Low"},
		},
		{
			name:      "assignee",
			fieldName: "assignee",
			value:     "user123",
			checkKey:  "assignee",
			checkVal:  map[string]string{"accountId": "user123"},
		},
		{
			name:      "labels",
			fieldName: "labels",
			value:     "bug,urgent",
			checkKey:  "labels",
			checkVal:  []string{"bug", "urgent"},
		},
		{
			name:      "parent",
			fieldName: "parent",
			value:     "PROJ-99",
			checkKey:  "parent",
			checkVal:  map[string]string{"key": "PROJ-99"},
		},
		{
			name:      "fix-version",
			fieldName: "fix-version",
			value:     "3.0",
			checkKey:  "fixVersions",
			checkVal:  []map[string]string{{"name": "3.0"}},
		},
		{
			name:      "type",
			fieldName: "type",
			value:     "Story",
			checkKey:  "issuetype",
			checkVal:  map[string]string{"name": "Story"},
		},
		{
			name:      "custom field by name",
			fieldName: "story_points",
			value:     "5",
			checkKey:  "customfield_10001",
			checkVal:  float64(5), // "5" is valid JSON, parsed as number
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := make(map[string]interface{})
			err := applyFieldOverride(fields, fieldMap, tt.fieldName, tt.value, nil)
			if err != nil {
				t.Fatalf("applyFieldOverride: %v", err)
			}

			got := fields[tt.checkKey]
			// JSON round-trip for comparison since map types differ.
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(tt.checkVal)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("fields[%q] = %s, want %s", tt.checkKey, gotJSON, wantJSON)
			}
		})
	}
}

func TestApplyFieldOverrideUnknown(t *testing.T) {
	fieldMap := map[string]*db.FieldMapping{}
	fields := make(map[string]interface{})
	err := applyFieldOverride(fields, fieldMap, "nonexistent", "value", nil)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	if got := err.Error(); got != "unknown field: nonexistent (run 'jai fields' to see available fields)" {
		t.Errorf("error = %q, want unknown field message", got)
	}
}

func TestApplyFieldOverrideAssigneeResolvesAccountID(t *testing.T) {
	fields := make(map[string]interface{})
	resolveAccountID := func(v string) (string, error) {
		if v != "user@example.com" {
			t.Fatalf("resolveAccountID called with %q, want user@example.com", v)
		}
		return "resolved-account-id", nil
	}
	err := applyFieldOverride(fields, nil, "assignee", "user@example.com", resolveAccountID)
	if err != nil {
		t.Fatalf("applyFieldOverride: %v", err)
	}
	if got, ok := fields["assignee"].(map[string]string); !ok || got["accountId"] != "resolved-account-id" {
		t.Errorf("assignee = %v, want {accountId: resolved-account-id}", fields["assignee"])
	}
}

func TestApplyFieldOverrideAssigneeResolveError(t *testing.T) {
	fields := make(map[string]interface{})
	resolveAccountID := func(v string) (string, error) {
		return "", fmt.Errorf("no Jira user found matching %q", v)
	}
	err := applyFieldOverride(fields, nil, "assignee", "nobody@example.com", resolveAccountID)
	if err == nil {
		t.Fatal("expected error when resolveAccountID fails")
	}
}

func TestApplyFieldOverrideJSONValue(t *testing.T) {
	fieldMap := map[string]*db.FieldMapping{
		"customfield_10010": {JiraID: "customfield_10010", Name: "config", Type: "string"},
	}
	fields := make(map[string]interface{})
	err := applyFieldOverride(fields, fieldMap, "config", `{"nested": true}`, nil)
	if err != nil {
		t.Fatalf("applyFieldOverride: %v", err)
	}
	got, ok := fields["customfield_10010"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T", fields["customfield_10010"])
	}
	if got["nested"] != true {
		t.Errorf("nested = %v, want true", got["nested"])
	}
}

func TestParseReplace(t *testing.T) {
	tests := []struct {
		input   string
		find    string
		replace string
		ok      bool
	}{
		{"old:new", "old", "new", true},
		{"foo:bar:baz", "foo", "bar:baz", true},
		{":empty_find", "", "empty_find", true},
		{"no_colon", "", "", false},
	}

	for _, tt := range tests {
		find, replace, ok := parseReplace(tt.input)
		if ok != tt.ok {
			t.Errorf("parseReplace(%q) ok = %v, want %v", tt.input, ok, tt.ok)
			continue
		}
		if ok {
			if find != tt.find {
				t.Errorf("parseReplace(%q) find = %q, want %q", tt.input, find, tt.find)
			}
			if replace != tt.replace {
				t.Errorf("parseReplace(%q) replace = %q, want %q", tt.input, replace, tt.replace)
			}
		}
	}
}

func TestReplaceInADF(t *testing.T) {
	adf := map[string]interface{}{
		"type":    "doc",
		"version": float64(1),
		"content": []interface{}{
			map[string]interface{}{
				"type": "paragraph",
				"content": []interface{}{
					map[string]interface{}{
						"type": "text",
						"text": "Hello old world, old friend",
					},
				},
			},
			map[string]interface{}{
				"type": "paragraph",
				"content": []interface{}{
					map[string]interface{}{
						"type": "text",
						"text": "Another old paragraph",
					},
				},
			},
		},
	}

	replaceInADF(adf, "old", "new")

	// Check the replacements.
	content := adf["content"].([]interface{})

	para1 := content[0].(map[string]interface{})
	text1 := para1["content"].([]interface{})[0].(map[string]interface{})["text"]
	if text1 != "Hello new world, new friend" {
		t.Errorf("text1 = %q, want %q", text1, "Hello new world, new friend")
	}

	para2 := content[1].(map[string]interface{})
	text2 := para2["content"].([]interface{})[0].(map[string]interface{})["text"]
	if text2 != "Another new paragraph" {
		t.Errorf("text2 = %q, want %q", text2, "Another new paragraph")
	}
}

func TestApplyFieldOverrideComponents(t *testing.T) {
	fieldMap := map[string]*db.FieldMapping{}
	fields := make(map[string]interface{})

	err := applyFieldOverride(fields, fieldMap, "components", "API,DB", nil)
	if err != nil {
		t.Fatalf("applyFieldOverride: %v", err)
	}

	comps, ok := fields["components"].([]map[string]string)
	if !ok {
		t.Fatalf("expected []map[string]string, got %T", fields["components"])
	}
	if len(comps) != 2 || comps[0]["name"] != "API" || comps[1]["name"] != "DB" {
		t.Errorf("components = %v, want [{name:API}, {name:DB}]", comps)
	}
}
