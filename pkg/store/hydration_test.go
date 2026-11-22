package store

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestGetIssuesUpdatedByIDAndKey_LargeBatch(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Exceed SQLite's 500-item chunk boundary to prove chunked batch lookups work.
	const total = 620
	items := make([]*IssueUpsert, total)
	ids := make([]string, total+1)
	keys := make([]string, total+1)
	for i := range total {
		id := fmt.Sprintf("%d", 10000+i)
		key := fmt.Sprintf("ENG-%d", i+1)
		updated := fmt.Sprintf("2026-06-01T00:%02d:%02dZ", i/60, i%60)
		items[i] = &IssueUpsert{
			Issue: &Issue{ID: id, Key: key, Project: "ENG", Summary: "S", Updated: updated, RawJSON: "{}"},
		}
		ids[i] = id
		keys[i] = key
	}
	ids[total] = "999999"
	keys[total] = "ENG-MISSING"

	if err := db.UpsertIssuesTx(items); err != nil {
		t.Fatalf("UpsertIssuesTx: %v", err)
	}

	byID, err := db.GetIssuesUpdatedByID(ids)
	if err != nil {
		t.Fatalf("GetIssuesUpdatedByID: %v", err)
	}
	if len(byID) != total {
		t.Errorf("GetIssuesUpdatedByID len = %d, want %d", len(byID), total)
	}
	if _, ok := byID["999999"]; ok {
		t.Error("missing ID should be omitted")
	}

	byKey, err := db.GetIssuesUpdatedByKey(keys)
	if err != nil {
		t.Fatalf("GetIssuesUpdatedByKey: %v", err)
	}
	if len(byKey) != total {
		t.Errorf("GetIssuesUpdatedByKey len = %d, want %d", len(byKey), total)
	}

	// Also verify GetIssuesRawJSON handles >500 keys (needed for maxResults=5000 reconcile scans).
	raws, err := db.GetIssuesRawJSON(keys)
	if err != nil {
		t.Fatalf("GetIssuesRawJSON: %v", err)
	}
	if len(raws) != total {
		t.Errorf("GetIssuesRawJSON len = %d, want %d", len(raws), total)
	}
}

func TestHydrationMarkers_PreservedAcrossUpsertAndRename(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	orig := &Issue{ID: "10042", Key: "OLD-1", Project: "OLD", Summary: "Initial", Updated: "2026-06-01T00:00:00Z", RawJSON: "{}"}
	if err := db.UpsertIssue(orig, nil); err != nil {
		t.Fatalf("UpsertIssue: %v", err)
	}

	if err := db.MarkIssueDetailsSynced([]string{"OLD-1"}); err != nil {
		t.Fatalf("MarkIssueDetailsSynced: %v", err)
	}
	if err := db.MarkChangelogFullSynced([]string{"OLD-1"}); err != nil {
		t.Fatalf("MarkChangelogFullSynced: %v", err)
	}

	st, err := db.GetIssueHydrationState("OLD-1")
	if err != nil || st == nil {
		t.Fatalf("GetIssueHydrationState = (%+v, %v)", st, err)
	}
	if st.DetailsSyncedAt == "" || st.ChangelogSyncedAt == "" || st.ChangelogFullSyncedAt == "" {
		t.Fatalf("expected all hydration timestamps populated, got %+v", st)
	}

	// Upserting the same issue (including moving its key) must preserve hydration markers.
	moved := &Issue{ID: "10042", Key: "NEW-1", Project: "NEW", Summary: "Moved", Updated: "2026-06-02T00:00:00Z", RawJSON: "{}"}
	if err := db.UpsertIssue(moved, nil); err != nil {
		t.Fatalf("UpsertIssue moved: %v", err)
	}

	// Lookup via old aliased key resolves to NEW-1 and preserves hydration markers.
	stAfter, err := db.GetIssueHydrationState("OLD-1")
	if err != nil || stAfter == nil {
		t.Fatalf("GetIssueHydrationState via alias = (%+v, %v)", stAfter, err)
	}
	if stAfter.DetailsSyncedAt != st.DetailsSyncedAt {
		t.Errorf("details_synced_at changed on upsert: got %q, want %q", stAfter.DetailsSyncedAt, st.DetailsSyncedAt)
	}
	if stAfter.ChangelogFullSyncedAt != st.ChangelogFullSyncedAt {
		t.Errorf("changelog_full_synced_at changed on upsert: got %q, want %q", stAfter.ChangelogFullSyncedAt, st.ChangelogFullSyncedAt)
	}

	// ResetChangelogSyncTimestamps clears both changelog markers while leaving details_synced_at intact.
	if _, err := db.ResetChangelogSyncTimestamps(); err != nil {
		t.Fatalf("ResetChangelogSyncTimestamps: %v", err)
	}
	stReset, err := db.GetIssueHydrationState("NEW-1")
	if err != nil || stReset == nil {
		t.Fatalf("GetIssueHydrationState after reset = (%+v, %v)", stReset, err)
	}
	if stReset.ChangelogSyncedAt != "" || stReset.ChangelogFullSyncedAt != "" {
		t.Errorf("expected changelog markers cleared, got %+v", stReset)
	}
	if stReset.DetailsSyncedAt != st.DetailsSyncedAt {
		t.Errorf("details_synced_at should survive changelog reset, got %q", stReset.DetailsSyncedAt)
	}
}
