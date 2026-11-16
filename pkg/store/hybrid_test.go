package store

import "testing"

// TestGetIssueMergesCustomFields verifies a non-promoted custom field stored in
// the sparse custom_fields JSON (keyed by Jira field id) surfaces through GetIssue
// under its field name, and the raw custom_fields entry is dropped.
func TestGetIssueMergesCustomFields(t *testing.T) {
	db := openTestDB(t)

	if err := db.UpsertFieldMapping(&FieldMapping{
		JiraID: "customfield_777", Name: "risk_level", Type: "text",
		IsCustom: true, IsColumn: false, Searchable: true,
	}); err != nil {
		t.Fatalf("UpsertFieldMapping: %v", err)
	}

	issue := &Issue{ID: "500", Key: "CF-1", Project: "CF", Summary: "x", RawJSON: "{}"}
	extra := map[string]interface{}{"custom_fields": `{"customfield_777":"high"}`}
	if err := db.UpsertIssue(issue, extra); err != nil {
		t.Fatalf("UpsertIssue: %v", err)
	}

	got, err := db.GetIssue("CF-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got == nil {
		t.Fatal("GetIssue returned nil")
	}
	if got["risk_level"] != "high" {
		t.Errorf("risk_level = %v, want high (merged from JSON under field name)", got["risk_level"])
	}
	if _, raw := got["custom_fields"]; raw {
		t.Error("raw custom_fields key must be dropped from the merged map")
	}
}

// TestGetIssueNoCustomFields guards the empty path: an issue with no custom_fields
// returns cleanly with no leftover custom_fields key.
func TestGetIssueNoCustomFields(t *testing.T) {
	db := openTestDB(t)
	if err := db.UpsertIssue(&Issue{ID: "501", Key: "CF-2", Project: "CF", Summary: "x", RawJSON: "{}"}, nil); err != nil {
		t.Fatalf("UpsertIssue: %v", err)
	}
	got, err := db.GetIssue("CF-2")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got == nil {
		t.Fatal("GetIssue returned nil")
	}
	if _, raw := got["custom_fields"]; raw {
		t.Error("custom_fields key should be absent for an issue with none")
	}
}
