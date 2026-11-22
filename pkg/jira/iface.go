package jira

import "context"

// JiraClient is the Jira REST surface the engine depends on. *Client is the
// production implementation; engine packages (sync, mcp) accept this interface
// so an embedding application can supply a mock or an alternative transport.
// The auth mechanism is orthogonal: see New (Basic) and NewWithClient (custom).
type JiraClient interface {
	AddComment(ctx context.Context, issueKey, body string) (string, error)
	AddWatcher(ctx context.Context, issueKey, accountID string) error
	BulkDeleteIssues(ctx context.Context, issueIDsOrKeys []string, sendNotification bool) (*BulkTaskResponse, error)
	BulkEditFields(ctx context.Context, req BulkEditFieldsRequest) (*BulkTaskResponse, error)
	BulkFetchChangelogs(ctx context.Context, keys []string) ([]BulkChangelogEntry, error)
	BulkFetchChangelogsWithOptions(ctx context.Context, keys []string, opts BulkChangelogOptions) ([]BulkChangelogEntry, error)
	BulkFetchIssues(ctx context.Context, idsOrKeys []string, opts BulkFetchIssuesOptions) (*BulkFetchIssuesResult, error)
	BulkGetUsers(ctx context.Context, accountIDs []string) ([]User, error)
	BulkTransitionIssues(ctx context.Context, req BulkTransitionRequest) (*BulkTaskResponse, error)
	BulkUnwatchIssues(ctx context.Context, issueIDsOrKeys []string) (*BulkTaskResponse, error)
	BulkWatchIssues(ctx context.Context, issueIDsOrKeys []string) (*BulkTaskResponse, error)
	CreateIssue(ctx context.Context, fields map[string]interface{}) (*CreateIssueResponse, error)
	CreateLink(ctx context.Context, linkType, inwardKey, outwardKey string) error
	CreateRemoteLink(ctx context.Context, issueKey, url, title string) error
	ExecuteTransition(ctx context.Context, issueKey, transitionID string) error
	Fields(ctx context.Context) ([]*Field, error)
	GetBoardFilterJQL(ctx context.Context, boardID int) (string, error)
	GetBoards(ctx context.Context, projectKey string) ([]Board, error)
	GetBulkEditableFields(ctx context.Context, issueIDsOrKeys []string, opts BulkFieldsQueryOptions) (*BulkEditableFieldsResponse, error)
	GetBulkTask(ctx context.Context, taskID string) (*BulkTaskStatus, error)
	GetBulkTransitions(ctx context.Context, issueIDsOrKeys []string, startingAfter string) (*BulkTransitionsResponse, error)
	GetDevInfo(ctx context.Context, issueID string) (*DevInfoResponse, error)
	GetIssue(ctx context.Context, key string) (*Issue, error)
	GetIssueChangelog(ctx context.Context, key string) (*ChangelogResponse, error)
	GetLinkTypes(ctx context.Context) ([]LinkType, error)
	GetProject(ctx context.Context, key string) (*ProjectInfo, error)
	GetRecentProjects(ctx context.Context) ([]ProjectInfo, error)
	GetSprintIssues(ctx context.Context, sprintID int) ([]string, error)
	GetSprints(ctx context.Context, boardID int) ([]Sprint, error)
	GetTransitions(ctx context.Context, issueKey string) ([]*Transition, error)
	MySelf(ctx context.Context) (*MySelf, error)
	RemoveWatcher(ctx context.Context, issueKey, accountID string) error
	ResolveAccountID(ctx context.Context, identifier string) (string, error)
	SearchAll(ctx context.Context, jql string, fields []string) func(yield func([]*Issue, error) bool)
	SearchAllWithOptions(ctx context.Context, jql string, fields []string, opts SearchOptions) func(yield func([]*Issue, error) bool)
	SearchApproximateCount(ctx context.Context, jql string) (int, error)
	SearchBoards(ctx context.Context, opts BoardSearchOptions) (*BoardSearchResponse, error)
	SearchCount(ctx context.Context, jql string) (int, error)
	SearchProjects(ctx context.Context, opts ProjectSearchOptions) (*ProjectSearchResponse, error)
	UpdateField(ctx context.Context, issueKey, fieldID string, value interface{}) error
	UpdateFieldOp(ctx context.Context, issueKey, fieldID, op string, value interface{}) error
}

// Ensure *Client satisfies JiraClient.
var _ JiraClient = (*Client)(nil)
