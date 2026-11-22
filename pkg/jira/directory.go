package jira

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

// ProjectSearchOptions configures GET /rest/api/3/project/search.
type ProjectSearchOptions struct {
	// Query filters projects by case-insensitive match on key or name.
	Query string
	// Keys restricts results to specific project keys (Jira accepts up to 50).
	Keys []string
	// Status filters by project status (e.g. "live", "archived", "deleted").
	Status []string
	// OrderBy specifies sort order (e.g. "name", "key", "lastIssueUpdatedTime").
	OrderBy string
	// StartAt is the zero-based pagination offset.
	StartAt int
	// MaxResults is the page size (default 50, max 100).
	MaxResults int
}

// ProjectSearchResponse is a paginated response from GET /rest/api/3/project/search.
type ProjectSearchResponse struct {
	Values     []ProjectInfo `json:"values"`
	StartAt    int           `json:"startAt"`
	MaxResults int           `json:"maxResults"`
	Total      int           `json:"total"`
	IsLast     bool          `json:"isLast"`
}

// GetRecentProjects returns up to 20 projects recently viewed by the
// authenticated user via GET /rest/api/3/project/recent.
func (c *Client) GetRecentProjects(ctx context.Context) ([]ProjectInfo, error) {
	var projects []ProjectInfo
	if err := c.get(ctx, "/rest/api/3/project/recent", &projects); err != nil {
		return nil, err
	}
	return projects, nil
}

// SearchProjects queries Jira projects with pagination and filtering via
// GET /rest/api/3/project/search.
func (c *Client) SearchProjects(ctx context.Context, opts ProjectSearchOptions) (*ProjectSearchResponse, error) {
	q := url.Values{}
	if s := strings.TrimSpace(opts.Query); s != "" {
		q.Set("query", s)
	}
	for _, k := range opts.Keys {
		if k = strings.TrimSpace(k); k != "" {
			q.Add("keys", k)
		}
	}
	for _, st := range opts.Status {
		if st = strings.TrimSpace(st); st != "" {
			q.Add("status", st)
		}
	}
	if s := strings.TrimSpace(opts.OrderBy); s != "" {
		q.Set("orderBy", s)
	}
	if opts.StartAt > 0 {
		q.Set("startAt", strconv.Itoa(opts.StartAt))
	}
	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = 50
	} else if maxResults > 100 {
		maxResults = 100
	}
	q.Set("maxResults", strconv.Itoa(maxResults))

	var resp ProjectSearchResponse
	if err := c.get(ctx, "/rest/api/3/project/search?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BoardSearchOptions configures GET /rest/agile/1.0/board.
type BoardSearchOptions struct {
	// ProjectKeyOrID filters boards belonging to a project.
	ProjectKeyOrID string
	// Name filters boards whose name contains this substring.
	Name string
	// Type filters by board type ("scrum", "kanban", "simple"). Empty returns all types.
	Type string
	// StartAt is the zero-based pagination offset.
	StartAt int
	// MaxResults is the page size (default 50, max 50).
	MaxResults int
}

// BoardSearchResponse is a paginated response from GET /rest/agile/1.0/board.
type BoardSearchResponse struct {
	Values     []Board `json:"values"`
	StartAt    int     `json:"startAt"`
	MaxResults int     `json:"maxResults"`
	Total      int     `json:"total"`
	IsLast     bool    `json:"isLast"`
}

// SearchBoards queries Jira Agile boards with pagination and optional filtering
// via GET /rest/agile/1.0/board. Unlike GetBoards in earlier releases, omitting
// Type returns both Scrum and Kanban boards.
func (c *Client) SearchBoards(ctx context.Context, opts BoardSearchOptions) (*BoardSearchResponse, error) {
	q := url.Values{}
	if s := strings.TrimSpace(opts.ProjectKeyOrID); s != "" {
		q.Set("projectKeyOrId", s)
	}
	if s := strings.TrimSpace(opts.Name); s != "" {
		q.Set("name", s)
	}
	if s := strings.TrimSpace(opts.Type); s != "" {
		q.Set("type", s)
	}
	if opts.StartAt > 0 {
		q.Set("startAt", strconv.Itoa(opts.StartAt))
	}
	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = 50
	} else if maxResults > 50 {
		maxResults = 50
	}
	q.Set("maxResults", strconv.Itoa(maxResults))

	var resp BoardSearchResponse
	if err := c.get(ctx, "/rest/agile/1.0/board?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
