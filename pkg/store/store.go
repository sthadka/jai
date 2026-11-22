package store

import (
	"context"
	"database/sql"
)

// Store is the persistence seam for the jai engine: the full public surface of
// the SQLite-backed *DB. Engine packages (sync, metrics, query, mcp) depend on
// this interface rather than the concrete type, so an embedding application can
// supply an alternative implementation or a test double. *DB is the production
// implementation returned by Open.
type Store interface {
	// Raw SQL access (promoted from the embedded *sql.DB).
	Query(query string, args ...any) (*sql.Rows, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
	Begin() (*sql.Tx, error)
	Prepare(query string) (*sql.Stmt, error)
	Close() error
	ReadDB() *sql.DB

	// Typed jai persistence methods.
	AllFieldMappings() ([]*FieldMapping, error)
	AllSyncMeta() ([]*SyncMeta, error)
	ClearResumeCursor(source string) error
	CountPendingChanges() (int, error)
	DeleteSyncMetadata(sources []string) (int64, error)
	EnsureColumn(name, colType string) error
	EnsurePendingChangesTable() error
	FieldMapByJiraID() (map[string]*FieldMapping, error)
	FieldPopulationStats(columns []string, project string) (map[string]*FieldStats, error)
	GetChangelogSyncCandidates(projectFilter []string) ([]string, error)
	GetComments(issueKey string) ([]*Comment, error)
	GetIssue(key string) (map[string]interface{}, error)
	GetIssueHydrationState(key string) (*IssueHydrationState, error)
	GetIssueIDToKeyMap() (map[string]string, error)
	GetIssueIDToKeyMapForKeys(keys []string) (map[string]string, error)
	GetIssueUpdated(key string) (string, error)
	GetIssuesRawJSON(keys []string) (map[string]string, error)
	GetIssuesUpdatedByID(ids []string) (map[string]string, error)
	GetIssuesUpdatedByKey(keys []string) (map[string]string, error)
	GetLastReconcile(source string) (string, error)
	GetProjectName(key string) string
	GetResumeCursor(source string) (string, error)
	GetSyncMeta(project string) (*SyncMeta, error)
	InsertChangelog(e *ChangelogEntry) error
	InsertChangelogBatch(entries []*ChangelogEntry) error
	InsertPendingChange(issueKey, operation, payload string) error
	IssueCountByProject() (map[string]int, error)
	ListPendingChanges() ([]*PendingChange, error)
	MarkChangelogFullSynced(keys []string) error
	MarkChangelogSynced(keys []string) error
	MarkFieldAsColumn(jiraID string) error
	MarkIssueDetailsSynced(keys []string) error
	MarkSynced(id int64) error
	Migrate() error
	PromoteField(jiraID, name, colType string) error
	PruneProjects(projects []string) (int64, error)
	RebuildFTS() error
	RecordRetryError(id int64, errMsg string) error
	ResetChangelogSyncTimestamps() (int64, error)
	SetResumeCursor(source, cursor string) error
	TotalIssueCount() (int, error)
	UpdateFullSyncMeta(project string) error
	UpdateIssueCommentsText(issueKey string) error
	UpdateIssueSprintInfo(issueKey string, sprintID int, sprintName string) error
	UpdateReconcileMeta(source string) error
	UpdateSyncMeta(project string, duration float64, total, synced int, syncErr, hwm string) error
	UpsertAttachments(issueKey string, attachments []*Attachment) error
	UpsertBoard(id int, name, boardType, projectKey string) error
	UpsertComment(c *Comment) error
	UpsertDevLinks(issueKey string, links []*DevLink) error
	UpsertFieldMapping(f *FieldMapping) error
	UpsertIssue(issue *Issue, extra map[string]interface{}) error
	UpsertIssuesTx(items []*IssueUpsert) error
	UpsertIssueLinks(issueKey string, links []IssueLink) error
	UpsertProject(key, name string) error
	UpsertSprint(id, boardID int, name, state, startDate, endDate, completeDate, goal string) error
}

// Ensure *DB satisfies Store.
var _ Store = (*DB)(nil)
