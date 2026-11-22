package jira

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

const (
	// BulkFetchFullLimit is Jira Cloud's per-call cap for POST /rest/api/3/issue/bulkfetch
	// when fetching full fields, wildcards, multi-valued fields (comment,
	// worklog, attachment), or expansions.
	BulkFetchFullLimit = 100

	// BulkFetchScalarLimit is Jira Cloud's expanded per-call cap for
	// POST /rest/api/3/issue/bulkfetch when requesting an explicit list of <=100
	// scalar fields without multi-valued fields or expansions.
	BulkFetchScalarLimit = 1000
)

// BulkFetchIssuesOptions configures POST /rest/api/3/issue/bulkfetch.
type BulkFetchIssuesOptions struct {
	// Fields restricts which issue fields are returned. An explicit list of <=100
	// scalar fields (excluding comment, worklog, attachment, and wildcards)
	// qualifies for 1,000 issues/call; otherwise batching uses 100 issues/call.
	Fields []string

	// Expand requests additional issue representations (e.g. "names"). Note that
	// expanding changelog, editmeta, operations, renderedFields, transitions, or
	// versionedRepresentations drops the batch cap to 100 issues/call.
	Expand []string

	// FieldsByKeys interprets Fields entries as field keys rather than field IDs.
	FieldsByKeys bool
}

// BulkFetchIssuesResult holds the aggregated response from BulkFetchIssues.
type BulkFetchIssuesResult struct {
	Issues      []*Issue          `json:"issues"`
	IssueErrors []json.RawMessage `json:"issueErrors,omitempty"`
}

// BulkFetchBatchLimit returns Jira Cloud's maximum issueIdsOrKeys batch size
// (1000 or 100) for POST /rest/api/3/issue/bulkfetch given fields and expand.
//
// Jira allows 1,000 issues per call only when ALL four conditions hold:
//  1. fields explicitly names at least one field (no *all, *navigable, or exclusions-only),
//  2. at most 100 fields are requested,
//  3. no multi-valued fields (comment, worklog, attachment) are requested,
//  4. expand omits changelog, editmeta, operations, renderedFields, transitions,
//     and versionedRepresentations.
func BulkFetchBatchLimit(fields, expand []string) int {
	if len(fields) == 0 || len(fields) > 100 {
		return BulkFetchFullLimit
	}
	included := 0
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if strings.HasPrefix(f, "-") {
			continue
		}
		lower := strings.ToLower(f)
		switch lower {
		case "*all", "*navigable", "comment", "worklog", "attachment":
			return BulkFetchFullLimit
		}
		included++
	}
	if included == 0 {
		return BulkFetchFullLimit
	}
	for _, e := range expand {
		switch strings.ToLower(strings.TrimSpace(e)) {
		case "changelog", "editmeta", "operations", "renderedfields", "transitions", "versionedrepresentations":
			return BulkFetchFullLimit
		}
	}
	return BulkFetchScalarLimit
}

// BulkFetchIssues fetches issues by numeric ID or key via
// POST /rest/api/3/issue/bulkfetch, bypassing JQL eventual-consistency index lag
// and resolving moved/renamed keys automatically. Input slices larger than
// BulkFetchBatchLimit(opts.Fields, opts.Expand) are transparently chunked.
func (c *Client) BulkFetchIssues(ctx context.Context, idsOrKeys []string, opts BulkFetchIssuesOptions) (*BulkFetchIssuesResult, error) {
	result := &BulkFetchIssuesResult{}
	if len(idsOrKeys) == 0 {
		return result, nil
	}

	batchSize := BulkFetchBatchLimit(opts.Fields, opts.Expand)
	for i := 0; i < len(idsOrKeys); i += batchSize {
		end := i + batchSize
		if end > len(idsOrKeys) {
			end = len(idsOrKeys)
		}

		reqBody := map[string]any{
			"issueIdsOrKeys": idsOrKeys[i:end],
		}
		if len(opts.Fields) > 0 {
			reqBody["fields"] = opts.Fields
		}
		if len(opts.Expand) > 0 {
			reqBody["expand"] = opts.Expand
		}
		if opts.FieldsByKeys {
			reqBody["fieldsByKeys"] = true
		}

		var page BulkFetchIssuesResult
		if err := c.postDecode(ctx, "/rest/api/3/issue/bulkfetch", reqBody, &page); err != nil {
			return nil, err
		}
		result.Issues = append(result.Issues, page.Issues...)
		result.IssueErrors = append(result.IssueErrors, page.IssueErrors...)
	}

	return result, nil
}

// User represents a Jira Cloud user returned by GET /rest/api/3/user/bulk.
type User struct {
	AccountID    string            `json:"accountId"`
	AccountType  string            `json:"accountType,omitempty"`
	DisplayName  string            `json:"displayName"`
	EmailAddress string            `json:"emailAddress,omitempty"`
	Active       bool              `json:"active"`
	TimeZone     string            `json:"timeZone,omitempty"`
	AvatarURLs   map[string]string `json:"avatarUrls,omitempty"`
}

type bulkUsersPage struct {
	Values     []User `json:"values"`
	StartAt    int    `json:"startAt"`
	MaxResults int    `json:"maxResults"`
	Total      int    `json:"total"`
	IsLast     bool   `json:"isLast"`
}

// BulkGetUsers fetches user details for the given Jira Cloud account IDs via
// GET /rest/api/3/user/bulk. Duplicate and empty account IDs are skipped, and
// large slices are chunked into batches of 100 to stay well within Jira's
// per-request query-parameter and URL-length limits.
func (c *Client) BulkGetUsers(ctx context.Context, accountIDs []string) ([]User, error) {
	seen := make(map[string]struct{}, len(accountIDs))
	unique := make([]string, 0, len(accountIDs))
	for _, id := range accountIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return nil, nil
	}

	const batchSize = 100
	var all []User
	for i := 0; i < len(unique); i += batchSize {
		end := i + batchSize
		if end > len(unique) {
			end = len(unique)
		}
		batch := unique[i:end]

		startAt := 0
		for {
			q := url.Values{}
			for _, id := range batch {
				q.Add("accountId", id)
			}
			q.Set("startAt", strconv.Itoa(startAt))
			q.Set("maxResults", strconv.Itoa(batchSize))

			var page bulkUsersPage
			if err := c.get(ctx, "/rest/api/3/user/bulk?"+q.Encode(), &page); err != nil {
				return nil, err
			}
			all = append(all, page.Values...)
			if page.IsLast || len(page.Values) == 0 || startAt+len(page.Values) >= page.Total {
				break
			}
			startAt += len(page.Values)
		}
	}
	return all, nil
}
