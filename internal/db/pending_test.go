package db

import (
	"path/filepath"
	"testing"
)

func TestPendingChanges(t *testing.T) {
	db := openTestDB(t)

	if err := db.EnsurePendingChangesTable(); err != nil {
		t.Fatalf("EnsurePendingChangesTable: %v", err)
	}

	// Insert.
	if err := db.InsertPendingChange("TEST-1", "set_field", `{"field":"status","value":"Done"}`); err != nil {
		t.Fatalf("InsertPendingChange: %v", err)
	}

	// Count.
	count, err := db.CountPendingChanges()
	if err != nil {
		t.Fatalf("CountPendingChanges: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 pending change, got %d", count)
	}

	// List.
	changes, err := db.ListPendingChanges()
	if err != nil {
		t.Fatalf("ListPendingChanges: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].IssueKey != "TEST-1" {
		t.Errorf("expected issue key TEST-1, got %s", changes[0].IssueKey)
	}

	// Mark synced.
	if err := db.MarkSynced(changes[0].ID); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	// Count after syncing.
	count, _ = db.CountPendingChanges()
	if count != 0 {
		t.Errorf("expected 0 pending after sync, got %d", count)
	}
}

func TestRecordRetryError(t *testing.T) {
	db := openTestDB(t)
	_ = db.EnsurePendingChangesTable()

	_ = db.InsertPendingChange("TEST-1", "set_field", `{}`)
	changes, _ := db.ListPendingChanges()

	if err := db.RecordRetryError(changes[0].ID, "connection refused"); err != nil {
		t.Fatalf("RecordRetryError: %v", err)
	}

	changes2, _ := db.ListPendingChanges()
	if changes2[0].RetryCount != 1 {
		t.Errorf("expected retry_count=1, got %d", changes2[0].RetryCount)
	}
	if changes2[0].LastError.String != "connection refused" {
		t.Errorf("expected error 'connection refused', got %q", changes2[0].LastError.String)
	}
}

// helper already defined in db_test.go
var _ = filepath.Join // ensure import used
