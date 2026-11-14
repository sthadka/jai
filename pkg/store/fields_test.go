package db

import "testing"

func TestFieldMappingItemTypeRoundTrip(t *testing.T) {
	db := openTestDB(t)

	f := &FieldMapping{
		JiraID:   "customfield_10466",
		JiraName: "Contributors",
		Name:     "contributors",
		Type:     "array",
		ItemType: "user",
		IsCustom: true,
		IsColumn: true,
	}
	if err := db.UpsertFieldMapping(f); err != nil {
		t.Fatalf("UpsertFieldMapping: %v", err)
	}

	m, err := db.FieldMapByJiraID()
	if err != nil {
		t.Fatalf("FieldMapByJiraID: %v", err)
	}
	got, ok := m["customfield_10466"]
	if !ok {
		t.Fatal("field not found after upsert")
	}
	if got.ItemType != "user" {
		t.Fatalf("ItemType = %q, want %q", got.ItemType, "user")
	}
}

func TestMigrationBackfillsBuiltinItemTypes(t *testing.T) {
	// openTestDB runs all migrations, including v12 which backfills built-in
	// array fields. Contributors-style custom fields are populated later by
	// schema sync, not by the migration.
	db := openTestDB(t)

	m, err := db.FieldMapByJiraID()
	if err != nil {
		t.Fatalf("FieldMapByJiraID: %v", err)
	}
	want := map[string]string{
		"components":  "component",
		"fixVersions": "version",
		"labels":      "string",
	}
	for jiraID, itemType := range want {
		f, ok := m[jiraID]
		if !ok {
			t.Fatalf("built-in field %q missing", jiraID)
		}
		if f.ItemType != itemType {
			t.Errorf("%s ItemType = %q, want %q", jiraID, f.ItemType, itemType)
		}
	}
}
