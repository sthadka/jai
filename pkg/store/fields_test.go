package store

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

// TestUpsertPreservesIsColumn guards the sync-startup regression where
// DiscoverFields re-upserts every field with IsColumn:false each run. If the
// upsert clobbered is_column, ensurePromotedFields would re-promote (full-table
// backfill) every promoted/tracked field on every sync. Promotion state is
// owned by MarkFieldAsColumn/PromoteField and must survive a later upsert.
func TestUpsertPreservesIsColumn(t *testing.T) {
	db := openTestDB(t)

	// Initial discovery: field is not yet a column.
	if err := db.UpsertFieldMapping(&FieldMapping{
		JiraID:   "customfield_90001",
		JiraName: "Story Points",
		Name:     "regr_sp",
		Type:     "number",
		IsCustom: true,
		IsColumn: false,
	}); err != nil {
		t.Fatalf("initial UpsertFieldMapping: %v", err)
	}

	// Promotion marks it as a column (as ensurePromotedFields does).
	if err := db.MarkFieldAsColumn("customfield_90001"); err != nil {
		t.Fatalf("MarkFieldAsColumn: %v", err)
	}

	// A later sync's DiscoverFields re-upserts with IsColumn:false. The promoted
	// flag must survive so the field is NOT re-backfilled.
	if err := db.UpsertFieldMapping(&FieldMapping{
		JiraID:   "customfield_90001",
		JiraName: "Story Points",
		Name:     "regr_sp",
		Type:     "number",
		IsCustom: true,
		IsColumn: false,
	}); err != nil {
		t.Fatalf("re-discovery UpsertFieldMapping: %v", err)
	}

	m, err := db.FieldMapByJiraID()
	if err != nil {
		t.Fatalf("FieldMapByJiraID: %v", err)
	}
	got, ok := m["customfield_90001"]
	if !ok {
		t.Fatal("field not found after re-upsert")
	}
	if !got.IsColumn {
		t.Fatal("IsColumn was reset to false by re-upsert; promoted columns will be re-backfilled every sync")
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
