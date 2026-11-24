package sync

import (
	"path/filepath"
	"testing"

	"github.com/sthadka/jai/v7/pkg/config"
	db "github.com/sthadka/jai/v7/pkg/store"
)

// TestEnsurePromotedFields verifies config-driven promotion: a custom field named
// in sync.promote_fields (or metrics.tracked_fields) is promoted from the
// custom_fields JSON to a typed column, backfilled, and recorded is_column.
func TestEnsurePromotedFields(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	// A JSON-stored custom field and an issue carrying it.
	if err := database.UpsertFieldMapping(&db.FieldMapping{
		JiraID: "customfield_10019", Name: "rank", JiraName: "Rank", Type: "text",
		IsCustom: true, IsColumn: false, Searchable: true,
	}); err != nil {
		t.Fatalf("UpsertFieldMapping: %v", err)
	}
	if err := database.UpsertIssue(
		&db.Issue{ID: "1", Key: "R-1", Project: "R", Summary: "x", RawJSON: "{}"},
		map[string]interface{}{"custom_fields": `{"customfield_10019":"1|hzzzzz:"}`},
	); err != nil {
		t.Fatalf("UpsertIssue: %v", err)
	}

	cfg := &config.Config{Sync: config.SyncConfig{PromoteFields: []string{"Rank"}}}
	e := New(database, nil, cfg)

	if err := e.ensurePromotedFields(); err != nil {
		t.Fatalf("ensurePromotedFields: %v", err)
	}

	var isCol bool
	if err := database.QueryRow(`SELECT is_column FROM field_map WHERE jira_id='customfield_10019'`).Scan(&isCol); err != nil {
		t.Fatal(err)
	}
	if !isCol {
		t.Error("rank not marked is_column after config-driven promotion")
	}
	var rank string
	if err := database.QueryRow(`SELECT "rank" FROM issues WHERE key='R-1'`).Scan(&rank); err != nil {
		t.Fatalf("scan rank column: %v", err)
	}
	if rank != "1|hzzzzz:" {
		t.Errorf("backfilled rank = %q, want 1|hzzzzz:", rank)
	}
}

// TestEnsurePromotedFields_TrackedFields verifies metrics.tracked_fields also
// drive promotion, and that unknown/system fields are safely ignored.
func TestEnsurePromotedFields_TrackedFields(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	if err := database.UpsertFieldMapping(&db.FieldMapping{
		JiraID: "customfield_20001", Name: "team", JiraName: "Team", Type: "text",
		IsCustom: true, IsColumn: false, Searchable: true,
	}); err != nil {
		t.Fatalf("UpsertFieldMapping: %v", err)
	}

	cfg := &config.Config{Metrics: &config.MetricsConfig{
		TrackedFields: []string{"status", "priority", "Team", "does_not_exist"},
	}}
	e := New(database, nil, cfg)
	if err := e.ensurePromotedFields(); err != nil {
		t.Fatalf("ensurePromotedFields: %v", err)
	}

	var isCol bool
	if err := database.QueryRow(`SELECT is_column FROM field_map WHERE jira_id='customfield_20001'`).Scan(&isCol); err != nil {
		t.Fatal(err)
	}
	if !isCol {
		t.Error("team (a metrics tracked field) not promoted")
	}
}
