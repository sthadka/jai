package sync

import (
	"fmt"
	"testing"

	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/jira"
)

func TestDenormalize_Basic(t *testing.T) {
	raw := []byte(`{
		"key": "TEST-1",
		"fields": {
			"summary": "Test issue",
			"issuetype": {"name": "Story"},
			"project": {"key": "TEST"},
			"status": {
				"name": "In Progress",
				"statusCategory": {"name": "In Progress"}
			},
			"priority": {"name": "High"},
			"assignee": {"displayName": "Jane Doe", "emailAddress": "jane@example.com"},
			"reporter": {"displayName": "Bob Smith"},
			"created": "2026-01-01T00:00:00.000+0000",
			"updated": "2026-01-02T00:00:00.000+0000",
			"labels": ["security", "auth"],
			"components": [{"name": "Backend"}],
			"fixVersions": [{"name": "v1.0"}]
		}
	}`)

	issue, _, err := Denormalize(raw, map[string]*db.FieldMapping{})
	if err != nil {
		t.Fatalf("Denormalize: %v", err)
	}

	if issue.Key != "TEST-1" {
		t.Errorf("expected key TEST-1, got %s", issue.Key)
	}
	if issue.Summary != "Test issue" {
		t.Errorf("expected summary 'Test issue', got %s", issue.Summary)
	}
	if issue.Status != "In Progress" {
		t.Errorf("expected status 'In Progress', got %s", issue.Status)
	}
	if issue.Priority != "High" {
		t.Errorf("expected priority 'High', got %s", issue.Priority)
	}
	if issue.Assignee != "Jane Doe" {
		t.Errorf("expected assignee 'Jane Doe', got %s", issue.Assignee)
	}
	if issue.Labels != "security,auth" {
		t.Errorf("expected labels 'security,auth', got %s", issue.Labels)
	}
	if issue.Components != "Backend" {
		t.Errorf("expected components 'Backend', got %s", issue.Components)
	}
	if issue.FixVersion != "v1.0" {
		t.Errorf("expected fix_version 'v1.0', got %s", issue.FixVersion)
	}
}

func TestDenormalize_CustomFields(t *testing.T) {
	raw := []byte(`{
		"key": "TEST-2",
		"fields": {
			"summary": "Custom fields test",
			"project": {"key": "TEST"},
			"customfield_12345": {"value": "ACS Scanner"},
			"customfield_67890": 8.0
		}
	}`)

	fieldMap := map[string]*db.FieldMapping{
		"customfield_12345": {JiraID: "customfield_12345", Name: "team", Type: "option", IsCustom: true, IsColumn: true},
		"customfield_67890": {JiraID: "customfield_67890", Name: "story_points", Type: "number", IsCustom: true, IsColumn: true},
	}

	_, extra, err := Denormalize(raw, fieldMap)
	if err != nil {
		t.Fatalf("Denormalize: %v", err)
	}

	if extra["team"] != "ACS Scanner" {
		t.Errorf("expected team 'ACS Scanner', got %v", extra["team"])
	}
	if extra["story_points"] != 8.0 {
		t.Errorf("expected story_points 8.0, got %v", extra["story_points"])
	}
}

func TestDenormalize_ObjectFieldFallback(t *testing.T) {
	tests := []struct {
		name     string
		fieldType string
		rawValue string
		want     string
	}{
		{"name key (Team-type)", "text", `{"id":"abc","name":"ACS Cloud Service","isShared":true}`, "ACS Cloud Service"},
		{"value key", "text", `{"id":"xyz","value":"High Impact"}`, "High Impact"},
		{"displayName key", "text", `{"self":"http://...","displayName":"Jane Doe"}`, "Jane Doe"},
		{"title key", "text", `{"id":"t1","title":"Sprint 42"}`, "Sprint 42"},
		{"name key on option type", "option", `{"id":"o1","name":"Fallback Name"}`, "Fallback Name"},
		{"priority order: name wins over title", "text", `{"name":"Preferred","title":"Alt"}`, "Preferred"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{
				"key": "TEST-FB",
				"fields": {
					"summary": "Fallback test",
					"project": {"key": "TEST"},
					"customfield_99999": %s
				}
			}`, tt.rawValue))

			fieldMap := map[string]*db.FieldMapping{
				"customfield_99999": {JiraID: "customfield_99999", Name: "test_field", Type: tt.fieldType, IsCustom: true, IsColumn: true},
			}

			_, extra, err := Denormalize(raw, fieldMap)
			if err != nil {
				t.Fatalf("Denormalize: %v", err)
			}

			if extra["test_field"] != tt.want {
				t.Errorf("expected %q, got %v", tt.want, extra["test_field"])
			}
		})
	}
}

func TestExtractComments(t *testing.T) {
	raw := []byte(`{
		"key": "TEST-1",
		"fields": {
			"summary": "Test",
			"project": {"key": "TEST"},
			"comment": {
				"comments": [
					{
						"id": "c1",
						"author": {"displayName": "Alice", "emailAddress": "alice@example.com"},
						"body": "First comment",
						"created": "2026-01-01T00:00:00.000+0000",
						"updated": "2026-01-01T00:00:00.000+0000"
					}
				]
			}
		}
	}`)

	comments, err := ExtractComments("TEST-1", raw)
	if err != nil {
		t.Fatalf("ExtractComments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if comments[0].Author != "Alice" {
		t.Errorf("expected author 'Alice', got %s", comments[0].Author)
	}
}

func TestInferColumnName(t *testing.T) {
	tests := []struct {
		name     string
		expected string
	}{
		{"Custom Team Field", "custom_team_field"},
		{"Story Points", "story_points"},
		{"sprint", "sprint"},
	}

	for _, tt := range tests {
		f := &jira.Field{ID: "customfield_test", Name: tt.name, Custom: true}
		got := inferColumnName(f, nil)
		if got != tt.expected {
			t.Errorf("inferColumnName(%q) = %q, want %q", tt.name, got, tt.expected)
		}
	}
}
