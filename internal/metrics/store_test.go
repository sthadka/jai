package metrics

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
)

func factPolicy() *Policy {
	return CompilePolicy(&config.MetricsConfig{
		StatusStages: map[string][]string{
			"todo":   {"Backlog"},
			"active": {"In Progress"},
			"done":   {"Closed"},
		},
		WIPStages:       []string{"active"},
		DoneResolutions: []string{"Done"},
	})
}

func newFactStore(t *testing.T) (*db.DB, *Store) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "jai.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database, NewStore(database, path)
}

// upIssue inserts/updates an issue with the given current fields; synced marks the
// changelog as fetched (HistoryComplete).
func upIssue(t *testing.T, database *db.DB, key, project, itype, status, category, created, resolved, resolution string, synced bool) {
	t.Helper()
	iss := &db.Issue{
		Key: key, Project: project, Type: itype, Summary: key,
		Status: status, StatusCategory: category,
		Created: created, Resolved: resolved, Resolution: resolution,
	}
	var extra map[string]interface{}
	if synced {
		extra = map[string]interface{}{"changelog_synced_at": "2030-01-01T00:00:00Z"}
	}
	if err := database.UpsertIssue(iss, extra); err != nil {
		t.Fatalf("UpsertIssue %s: %v", key, err)
	}
}

func cl(t *testing.T, database *db.DB, id, key, fromS, toS, at string) {
	t.Helper()
	if err := database.InsertChangelog(&db.ChangelogEntry{
		ID: id, IssueKey: key, Field: "status",
		FromString: fromS, ToString: toS, ChangedAt: at,
	}); err != nil {
		t.Fatalf("InsertChangelog %s: %v", id, err)
	}
}

func findFlow(rows []FlowRow, key string) *FlowRow {
	for i := range rows {
		if rows[i].IssueKey == key {
			return &rows[i]
		}
	}
	return nil
}

func genCount(t *testing.T, database *db.DB) int {
	t.Helper()
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM metrics_generation`).Scan(&n); err != nil {
		t.Fatalf("count generations: %v", err)
	}
	return n
}

var asOf = time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)

// A full build publishes an active generation; readers see it, open intervals persist a
// NULL exit, and resolved issues carry lead/cycle seconds.
func TestBuildAll_Publishes(t *testing.T) {
	database, store := newFactStore(t)

	upIssue(t, database, "X-1", "PX", "Story", "In Progress", "In Progress", "2024-01-01T00:00:00Z", "", "", true)
	cl(t, database, "c1", "X-1", "Backlog", "In Progress", "2024-01-02T00:00:00Z")

	upIssue(t, database, "X-2", "PX", "Bug", "Closed", "Done", "2024-01-01T00:00:00Z", "2024-01-05T00:00:00Z", "Done", true)
	cl(t, database, "c2", "X-2", "Backlog", "In Progress", "2024-01-02T00:00:00Z")
	cl(t, database, "c3", "X-2", "In Progress", "Closed", "2024-01-05T00:00:00Z")

	b := NewBuilder(store, factPolicy())
	res, err := b.BuildAll(asOf, nil)
	if err != nil {
		t.Fatalf("BuildAll: %v", err)
	}
	if res.IssueCount != 2 {
		t.Fatalf("issue count = %d, want 2", res.IssueCount)
	}

	active, err := store.ActiveGeneration()
	if err != nil || active == nil {
		t.Fatalf("ActiveGeneration: %v / %v", active, err)
	}
	if active.Generation != res.Generation {
		t.Errorf("active gen %d != built gen %d", active.Generation, res.Generation)
	}

	flows, err := store.FlowRows(nil)
	if err != nil {
		t.Fatalf("FlowRows: %v", err)
	}
	if len(flows) != 2 {
		t.Fatalf("flow rows = %d, want 2", len(flows))
	}

	x1 := findFlow(flows, "X-1")
	if x1.FirstActiveAt == nil || !x1.FirstActiveAt.Equal(tm("2024-01-02T00:00:00Z")) {
		t.Errorf("X-1 first_active_at = %v", x1.FirstActiveAt)
	}
	if x1.LeadSeconds != nil || x1.CycleSeconds != nil {
		t.Errorf("X-1 unresolved should have nil lead/cycle, got %v/%v", x1.LeadSeconds, x1.CycleSeconds)
	}
	if !x1.HasActive {
		t.Errorf("X-1 should have an active interval")
	}

	x2 := findFlow(flows, "X-2")
	if x2.LeadSeconds == nil || *x2.LeadSeconds != int64(4*24*3600) {
		t.Errorf("X-2 lead = %v, want %d", x2.LeadSeconds, 4*24*3600)
	}
	if x2.CycleSeconds == nil || *x2.CycleSeconds != int64(3*24*3600) {
		t.Errorf("X-2 cycle = %v, want %d", x2.CycleSeconds, 3*24*3600)
	}
	if x2.Resolution != "Done" {
		t.Errorf("X-2 resolution = %q", x2.Resolution)
	}

	// Open final interval stored with NULL exit.
	ivs, err := store.Intervals(nil, "status")
	if err != nil {
		t.Fatalf("Intervals: %v", err)
	}
	var openSeen bool
	for _, iv := range ivs {
		if iv.IssueKey == "X-1" && iv.Open {
			openSeen = true
			if iv.ExitedAt != nil {
				t.Errorf("open interval must store NULL exit, got %v", iv.ExitedAt)
			}
		}
	}
	if !openSeen {
		t.Errorf("expected an open interval for X-1")
	}
}

// Readers see nothing before any generation is activated.
func TestReaders_EmptyBeforeBuild(t *testing.T) {
	_, store := newFactStore(t)
	active, err := store.ActiveGeneration()
	if err != nil {
		t.Fatalf("ActiveGeneration: %v", err)
	}
	if active != nil {
		t.Fatalf("expected no active generation, got %+v", active)
	}
	flows, err := store.FlowRows(nil)
	if err != nil || len(flows) != 0 {
		t.Fatalf("FlowRows before build = %v / %v", flows, err)
	}
}

// A second full build atomically swaps the pointer and deletes the obsolete generation;
// readers only ever see the latest generation's rows.
func TestBuildAll_SwapDeletesObsolete(t *testing.T) {
	database, store := newFactStore(t)
	upIssue(t, database, "X-1", "PX", "Story", "Backlog", "To Do", "2024-01-01T00:00:00Z", "", "", true)

	b := NewBuilder(store, factPolicy())
	first, err := b.BuildAll(asOf, nil)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}

	// Change the snapshot and rebuild.
	upIssue(t, database, "X-1", "PX", "Story", "In Progress", "In Progress", "2024-01-01T00:00:00Z", "", "", true)
	second, err := b.BuildAll(asOf, nil)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if second.Generation <= first.Generation {
		t.Errorf("generation must increase: %d -> %d", first.Generation, second.Generation)
	}
	if n := genCount(t, database); n != 1 {
		t.Errorf("obsolete generations not deleted: %d generation rows remain", n)
	}

	flows, _ := store.FlowRows(nil)
	if len(flows) != 1 || flows[0].CurrentStage != "active" {
		t.Errorf("reader should see only new generation; got %+v", flows)
	}

	// No fact rows from the old generation remain.
	var leftover int
	database.QueryRow(`SELECT COUNT(*) FROM fact_issue_flow WHERE generation = ?`, first.Generation).Scan(&leftover)
	if leftover != 0 {
		t.Errorf("old generation facts not deleted: %d", leftover)
	}
}

// Incremental replacement updates only the touched issue within the active generation and
// is idempotent when re-run with the same keys.
func TestIncremental_ReplaceAndIdempotent(t *testing.T) {
	database, store := newFactStore(t)
	upIssue(t, database, "A-1", "PA", "Story", "Backlog", "To Do", "2024-01-01T00:00:00Z", "", "", true)
	upIssue(t, database, "A-2", "PA", "Story", "Backlog", "To Do", "2024-01-01T00:00:00Z", "", "", true)

	b := NewBuilder(store, factPolicy())
	if _, err := b.BuildAll(asOf, nil); err != nil {
		t.Fatalf("build: %v", err)
	}

	// A-1 advances to In Progress; incremental for just A-1.
	upIssue(t, database, "A-1", "PA", "Story", "In Progress", "In Progress", "2024-01-01T00:00:00Z", "", "", true)
	cl(t, database, "a1", "A-1", "Backlog", "In Progress", "2024-01-03T00:00:00Z")
	if _, err := b.Incremental(asOf, []string{"A-1"}); err != nil {
		t.Fatalf("incremental: %v", err)
	}

	flows, _ := store.FlowRows(nil)
	if len(flows) != 2 {
		t.Fatalf("flow rows = %d, want 2", len(flows))
	}
	if findFlow(flows, "A-1").CurrentStage != "active" {
		t.Errorf("A-1 should be active after incremental")
	}
	if findFlow(flows, "A-2").CurrentStage != "todo" {
		t.Errorf("A-2 should be untouched (todo)")
	}

	// Idempotent: re-running the same incremental yields the same row set.
	if _, err := b.Incremental(asOf, []string{"A-1"}); err != nil {
		t.Fatalf("incremental rerun: %v", err)
	}
	flows2, _ := store.FlowRows(nil)
	if len(flows2) != 2 {
		t.Errorf("idempotence broken: %d rows after rerun", len(flows2))
	}
	var ivCount int
	database.QueryRow(`SELECT COUNT(*) FROM fact_field_interval WHERE issue_key = 'A-1'`).Scan(&ivCount)
	if ivCount != 2 { // leading todo + open active
		t.Errorf("A-1 interval rows = %d, want 2 (no duplication)", ivCount)
	}
}

// An issue removed from issues has its facts deleted by an incremental naming its key.
func TestIncremental_Deletion(t *testing.T) {
	database, store := newFactStore(t)
	upIssue(t, database, "D-1", "PD", "Story", "Backlog", "To Do", "2024-01-01T00:00:00Z", "", "", true)
	upIssue(t, database, "D-2", "PD", "Story", "Backlog", "To Do", "2024-01-01T00:00:00Z", "", "", true)

	b := NewBuilder(store, factPolicy())
	if _, err := b.BuildAll(asOf, nil); err != nil {
		t.Fatalf("build: %v", err)
	}

	if _, err := database.Exec(`DELETE FROM issues WHERE key = 'D-1'`); err != nil {
		t.Fatalf("delete issue: %v", err)
	}
	if _, err := b.Incremental(asOf, []string{"D-1"}); err != nil {
		t.Fatalf("incremental deletion: %v", err)
	}

	flows, _ := store.FlowRows(nil)
	if len(flows) != 1 || flows[0].IssueKey != "D-2" {
		t.Errorf("deleted issue facts should be gone; got %+v", flows)
	}
}

// A policy whose fingerprint differs from the active generation forces a full rebuild.
func TestIncremental_FingerprintMismatch(t *testing.T) {
	database, store := newFactStore(t)
	upIssue(t, database, "F-1", "PF", "Story", "Backlog", "To Do", "2024-01-01T00:00:00Z", "", "", true)

	if _, err := NewBuilder(store, factPolicy()).BuildAll(asOf, nil); err != nil {
		t.Fatalf("build: %v", err)
	}

	// Different stage map => different fingerprint.
	altPol := CompilePolicy(&config.MetricsConfig{
		StatusStages: map[string][]string{
			"todo":   {"Backlog", "Open"},
			"active": {"In Progress"},
		},
		WIPStages: []string{"active"},
	})
	if altPol.Fingerprint() == factPolicy().Fingerprint() {
		t.Fatal("test setup: fingerprints should differ")
	}
	_, err := NewBuilder(store, altPol).Incremental(asOf, []string{"F-1"})
	if err != ErrFullRebuildRequired {
		t.Errorf("expected ErrFullRebuildRequired, got %v", err)
	}
}

// Incremental against a never-built store also requires a full rebuild.
func TestIncremental_NoActiveGeneration(t *testing.T) {
	database, store := newFactStore(t)
	upIssue(t, database, "N-1", "PN", "Story", "Backlog", "To Do", "2024-01-01T00:00:00Z", "", "", true)
	_, err := NewBuilder(store, factPolicy()).Incremental(asOf, []string{"N-1"})
	if err != ErrFullRebuildRequired {
		t.Errorf("expected ErrFullRebuildRequired, got %v", err)
	}
}
