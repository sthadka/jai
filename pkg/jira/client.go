package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// AuthError indicates Jira rejected the request's credentials, or the
// authenticated user lacks permission for the resource (HTTP 401/403). It is
// returned instead of a generic API error so callers can detect an
// unauthenticated state via errors.As and fail fast. This matters because Jira
// Cloud otherwise returns wildly different — often empty — results depending on
// whether the request is authenticated, which is easy to mistake for "no data"
// and silently sync/serve the wrong thing.
type AuthError struct {
	StatusCode int
	Body       string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("jira authentication failed (HTTP %d): check jira.email and jira.token", e.StatusCode)
}

// authError returns an *AuthError for 401/403 responses so callers can detect
// an unauthenticated state; it returns nil for every other status, leaving the
// caller to format its own error for other non-2xx codes.
func authError(statusCode int, body []byte) error {
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		return &AuthError{StatusCode: statusCode, Body: string(body)}
	}
	return nil
}

// RateLimiter throttles outbound Jira requests. *golang.org/x/time/rate.Limiter
// satisfies it; embedders may supply their own (e.g. a shared cross-client
// limiter) via NewWithClient's options.
type RateLimiter interface {
	Wait(ctx context.Context) error
}

// BasicAuthTransport is an http.RoundTripper that injects HTTP Basic
// Authentication (Jira email + API token) into every outgoing request. It is
// the default auth mechanism; an embedder can substitute any other
// RoundTripper (for example an OAuth 2.0 token source) by constructing the
// client with NewWithClient and a custom *http.Client.
type BasicAuthTransport struct {
	Email    string
	APIToken string
	// Base is the underlying transport; nil uses http.DefaultTransport.
	Base http.RoundTripper
}

// RoundTrip adds the Authorization header to a clone of req and forwards it to
// the base transport, leaving the caller's request untouched.
func (t *BasicAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.SetBasicAuth(t.Email, t.APIToken)
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

// Client is a Jira Cloud HTTP client. Auth is carried by its *http.Client's
// transport, so the authentication mechanism is pluggable (Basic by default).
type Client struct {
	baseURL    string
	httpClient *http.Client
	limiter    RateLimiter
	rlObserver RateLimitObserver
}

// New creates a Jira client authenticated with HTTP Basic auth (email + API
// token), the default for Jira Cloud. For a different auth mechanism (e.g.
// OAuth 2.0), use NewWithClient with a custom *http.Client.
func New(baseURL, email, token string, ratePerSec float64, opts ...ClientOption) *Client {
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &BasicAuthTransport{
			Email:    email,
			APIToken: token,
		},
	}
	return NewWithClient(baseURL, httpClient, ratePerSec, opts...)
}

// NewWithClient creates a Jira client using a caller-supplied *http.Client,
// which is the seam for alternative authentication: set the client's Transport
// to any http.RoundTripper (BasicAuthTransport, an OAuth token source, etc.).
// A nil httpClient defaults to an unauthenticated 30s-timeout client.
// Optional ClientOptions configure shared rate limiters and header observers.
func NewWithClient(baseURL string, httpClient *http.Client, ratePerSec float64, opts ...ClientOption) *Client {
	if ratePerSec <= 0 {
		ratePerSec = 10
	}
	// Burst must be at least 1: int() truncation of a sub-1 rate (e.g. 0.5)
	// would yield a zero burst, which blocks every request forever. Round up so
	// the burst never starves and fractional rates still admit one request.
	burst := int(math.Ceil(ratePerSec))
	if burst < 1 {
		burst = 1
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
		limiter:    rate.NewLimiter(rate.Limit(ratePerSec), burst),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

// doRequest performs an authenticated request with rate limiting and retry.
// body is passed as a reusable byte slice so retries (429, 5xx, transport errors)
// always transmit the full payload instead of an already-drained io.Reader.
func (c *Client) doRequest(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	reqURL := c.baseURL + path
	const maxRetries = 3

	var lastErr error
	skipBackoff := false
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 && !skipBackoff {
			// Exponential backoff with jitter: 1s, 2s, 4s ± 20%.
			base := time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
			jitter := time.Duration(rand.Float64()*0.4*float64(base) - 0.2*float64(base))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(base + jitter):
			}
		}
		skipBackoff = false

		// Wait for rate limiter.
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, err
		}

		var bodyReader io.Reader
		if len(body) > 0 {
			bodyReader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		info := c.observeRateLimit(resp, method, path)

		// Retry on 429 (respect Retry-After without stacking exponential backoff) and 5xx.
		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			lastErr = fmt.Errorf("rate limited (429)")
			if attempt == maxRetries {
				break
			}
			wait := 2 * time.Second
			if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
				if d, perr := parseRetryAfter(ra); perr == nil {
					wait = d
				}
			} else if info.ResetAfter > 0 {
				wait = info.ResetAfter
			}
			if wait > 0 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(wait):
				}
			}
			skipBackoff = true
			continue
		}

		if resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("server error: %d", resp.StatusCode)
			continue
		}

		return resp, nil
	}

	return nil, fmt.Errorf("max retries exceeded: %w", lastErr)
}

func (c *Client) observeRateLimit(resp *http.Response, method, path string) RateLimitInfo {
	if c.rlObserver == nil && resp.StatusCode != http.StatusTooManyRequests {
		return RateLimitInfo{Remaining: -1, Quota: -1}
	}
	info := ParseRateLimitHeaders(resp.Header)
	info.StatusCode = resp.StatusCode
	info.Method = method
	info.Path = path
	info.Timestamp = time.Now().UTC()
	if c.rlObserver != nil {
		c.rlObserver(info)
	}
	return info
}

// get performs a GET request and decodes the JSON response.
func (c *Client) get(ctx context.Context, path string, out interface{}) error {
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		if authErr := authError(resp.StatusCode, body); authErr != nil {
			return authErr
		}
		return fmt.Errorf("jira API error %d: %s", resp.StatusCode, string(body))
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

// postDecode performs a POST request with a JSON body and decodes the JSON response into out.
func (c *Client) postDecode(ctx context.Context, path string, in, out interface{}) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	resp, err := c.doRequest(ctx, http.MethodPost, path, b)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		if authErr := authError(resp.StatusCode, body); authErr != nil {
			return authErr
		}
		return fmt.Errorf("jira API error %d: %s", resp.StatusCode, string(body))
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

// MySelf calls /rest/api/3/myself to test the connection.
func (c *Client) MySelf(ctx context.Context) (*MySelf, error) {
	var m MySelf
	if err := c.get(ctx, "/rest/api/3/myself", &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// resolvedUser is a single result from the Jira Cloud user search endpoint.
type resolvedUser struct {
	AccountID string `json:"accountId"`
}

// ResolveAccountID resolves an email address to a Jira Cloud account ID via the
// user search API. Jira Cloud write endpoints (watchers, assignee, etc.) require
// an account ID and reject emails/usernames directly (GDPR changes removed that
// lookup). If identifier doesn't look like an email, it's assumed to already be
// an account ID and is returned unchanged.
func (c *Client) ResolveAccountID(ctx context.Context, identifier string) (string, error) {
	if !strings.Contains(identifier, "@") {
		return identifier, nil
	}
	var users []resolvedUser
	path := "/rest/api/3/user/search?query=" + url.QueryEscape(identifier)
	if err := c.get(ctx, path, &users); err != nil {
		return "", fmt.Errorf("resolving user %q: %w", identifier, err)
	}
	if len(users) == 0 {
		return "", fmt.Errorf("no Jira user found matching %q", identifier)
	}
	return users[0].AccountID, nil
}

// GetProject fetches project metadata (name, key).
func (c *Client) GetProject(ctx context.Context, key string) (*ProjectInfo, error) {
	var p ProjectInfo
	if err := c.get(ctx, "/rest/api/3/project/"+key, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// GetIssue fetches a single issue by key from the Jira API.
func (c *Client) GetIssue(ctx context.Context, key string) (*Issue, error) {
	var issue Issue
	if err := c.get(ctx, "/rest/api/3/issue/"+key, &issue); err != nil {
		return nil, err
	}
	return &issue, nil
}

// GetIssueChangelog fetches a single issue with its full changelog.
func (c *Client) GetIssueChangelog(ctx context.Context, key string) (*ChangelogResponse, error) {
	var resp ChangelogResponse
	if err := c.get(ctx, "/rest/api/3/issue/"+key+"?expand=changelog&fields=key", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

const (
	// BulkChangelogMaxIssues is Jira Cloud's maximum issueIdsOrKeys per
	// POST /rest/api/3/changelog/bulkfetch request.
	BulkChangelogMaxIssues = 1000

	// BulkChangelogMaxResults is Jira Cloud's maximum histories per page for
	// POST /rest/api/3/changelog/bulkfetch (API default when omitted is 1,000).
	BulkChangelogMaxResults = 10000

	// BulkChangelogMaxFieldIDs is Jira Cloud's maximum fieldIds filter count.
	BulkChangelogMaxFieldIDs = 10
)

// BulkChangelogOptions configures POST /rest/api/3/changelog/bulkfetch.
type BulkChangelogOptions struct {
	// FieldIDs optionally restricts returned changelog items to at most 10 Jira
	// field IDs (e.g. "status", "assignee", "priority", "customfield_10020").
	// Empty returns complete unfiltered history.
	FieldIDs []string

	// MaxResults sets the page size (0 defaults to BulkChangelogMaxResults = 10000;
	// values above 10000 are clamped to 10000).
	MaxResults int
}

// BulkFetchChangelogs fetches complete unfiltered changelogs for the given
// issues via POST /rest/api/3/changelog/bulkfetch using maxResults=10000 and
// batches up to 1,000 issues.
func (c *Client) BulkFetchChangelogs(ctx context.Context, keys []string) ([]BulkChangelogEntry, error) {
	return c.BulkFetchChangelogsWithOptions(ctx, keys, BulkChangelogOptions{})
}

// BulkFetchChangelogsWithOptions fetches changelogs for the given issue IDs or
// keys via POST /rest/api/3/changelog/bulkfetch, supporting optional fieldIds
// filtering (<=10 fields), maxResults up to 10,000, and automatic chunking for
// slices exceeding 1,000 issues.
func (c *Client) BulkFetchChangelogsWithOptions(ctx context.Context, keys []string, opts BulkChangelogOptions) ([]BulkChangelogEntry, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	if len(opts.FieldIDs) > BulkChangelogMaxFieldIDs {
		return nil, fmt.Errorf("bulkfetch changelogs: at most %d fieldIds allowed, got %d", BulkChangelogMaxFieldIDs, len(opts.FieldIDs))
	}

	maxResults := opts.MaxResults
	if maxResults <= 0 || maxResults > BulkChangelogMaxResults {
		maxResults = BulkChangelogMaxResults
	}

	var all []BulkChangelogEntry
	for i := 0; i < len(keys); i += BulkChangelogMaxIssues {
		end := i + BulkChangelogMaxIssues
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[i:end]
		nextPageToken := ""

		for {
			req := BulkChangelogRequest{
				IssueIdsOrKeys: batch,
				FieldIDs:       opts.FieldIDs,
				MaxResults:     maxResults,
				NextPageToken:  nextPageToken,
			}

			var resp BulkChangelogResponse
			if err := c.postDecode(ctx, "/rest/api/3/changelog/bulkfetch", req, &resp); err != nil {
				return nil, err
			}

			for _, icl := range resp.IssueChangeLogs {
				for _, h := range icl.ChangeHistories {
					all = append(all, BulkChangelogEntry{
						ID:      h.ID,
						IssueID: icl.IssueID,
						Author:  h.Author,
						Created: epochMillisToRFC3339(h.Created),
						Items:   h.Items,
					})
				}
			}

			if resp.NextPageToken == "" {
				break
			}
			nextPageToken = resp.NextPageToken
		}
	}

	return all, nil
}

// epochMillisToRFC3339 converts Jira's epoch-millisecond changelog timestamps to
// an RFC3339 UTC string. Returns "" for a zero/absent value.
func epochMillisToRFC3339(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.Unix(0, ms*int64(time.Millisecond)).UTC().Format(time.RFC3339)
}

// Fields fetches all field definitions.
func (c *Client) Fields(ctx context.Context) ([]*Field, error) {
	var fields []*Field
	if err := c.get(ctx, "/rest/api/3/field", &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

const (
	// SearchDefaultMaxResults is the page size for broad/full-field JQL queries.
	SearchDefaultMaxResults = 100

	// SearchNarrowMaxResults is Jira Cloud's maximum page size for
	// POST /rest/api/3/search/jql when requesting narrow scalar-only fields
	// (e.g. ["id"], ["key"], ["updated"], or ["key", rankField]).
	SearchNarrowMaxResults = 5000

	// SearchMaxReconcileIssues is Jira Cloud's maximum reconcileIssues count.
	SearchMaxReconcileIssues = 50
)

// SearchOptions configures POST /rest/api/3/search/jql.
type SearchOptions struct {
	// MaxResults sets the requested page size. Zero auto-sizes to 5,000 for
	// narrow scalar-only field projections and 100 otherwise. Values above 5,000
	// are clamped to 5,000.
	MaxResults int

	// ReconcileIssues specifies up to 50 numeric issue IDs that Jira Cloud should
	// strongly reconcile against the search index before returning results.
	ReconcileIssues []int64

	// Expand is an optional Jira expand expression.
	Expand string
}

// isNarrowSearchFields reports whether fields is a small explicit scalar-only
// projection safe to request with maxResults=5000 on POST /rest/api/3/search/jql.
func isNarrowSearchFields(fields []string) bool {
	if len(fields) == 0 || len(fields) > 5 {
		return false
	}
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" || strings.HasPrefix(f, "-") {
			return false
		}
		switch strings.ToLower(f) {
		case "*all", "*navigable", "comment", "attachment", "worklog", "description", "renderedfields", "changelog":
			return false
		}
	}
	return true
}

// SearchAll returns a paginated iterator over issues matching the JQL query.
// It yields each page of issues. The caller iterates with a for range loop.
// Uses POST /rest/api/3/search/jql with cursor-based pagination and automatic
// page-size selection (5,000 for narrow scalar-only fields, 100 otherwise).
func (c *Client) SearchAll(ctx context.Context, jql string, fields []string) func(yield func([]*Issue, error) bool) {
	return c.SearchAllWithOptions(ctx, jql, fields, SearchOptions{})
}

// SearchAllWithOptions returns a paginated iterator over issues matching jql
// via POST /rest/api/3/search/jql with explicit control over maxResults,
// reconcileIssues, and expand.
func (c *Client) SearchAllWithOptions(ctx context.Context, jql string, fields []string, opts SearchOptions) func(yield func([]*Issue, error) bool) {
	return func(yield func([]*Issue, error) bool) {
		if len(opts.ReconcileIssues) > SearchMaxReconcileIssues {
			yield(nil, fmt.Errorf("search/jql: at most %d reconcileIssues allowed, got %d", SearchMaxReconcileIssues, len(opts.ReconcileIssues)))
			return
		}

		maxResults := opts.MaxResults
		if maxResults <= 0 {
			if isNarrowSearchFields(fields) {
				maxResults = SearchNarrowMaxResults
			} else {
				maxResults = SearchDefaultMaxResults
			}
		} else if maxResults > SearchNarrowMaxResults {
			maxResults = SearchNarrowMaxResults
		}

		var nextPageToken string

		for {
			reqBody := map[string]interface{}{
				"jql":        jql,
				"maxResults": maxResults,
				"fields":     fields,
			}
			if nextPageToken != "" {
				reqBody["nextPageToken"] = nextPageToken
			}
			if len(opts.ReconcileIssues) > 0 {
				reqBody["reconcileIssues"] = opts.ReconcileIssues
			}
			if opts.Expand != "" {
				reqBody["expand"] = opts.Expand
			}

			var resp SearchResponse
			if err := c.postDecode(ctx, "/rest/api/3/search/jql", reqBody, &resp); err != nil {
				yield(nil, err)
				return
			}

			if len(resp.ErrorMessages) > 0 {
				yield(nil, fmt.Errorf("jira JQL error: %s", strings.Join(resp.ErrorMessages, "; ")))
				return
			}

			if len(resp.Issues) == 0 {
				return
			}

			if !yield(resp.Issues, nil) {
				return
			}

			if resp.NextPageToken == "" {
				return
			}
			nextPageToken = resp.NextPageToken
		}
	}
}

// SearchApproximateCount returns the approximate number of issues matching jql
// in a single API call via POST /rest/api/3/search/approximate-count.
func (c *Client) SearchApproximateCount(ctx context.Context, jql string) (int, error) {
	var resp struct {
		Count int `json:"count"`
	}
	if err := c.postDecode(ctx, "/rest/api/3/search/approximate-count", map[string]string{"jql": jql}, &resp); err != nil {
		return 0, err
	}
	return resp.Count, nil
}

// SearchCount returns the number of issues matching a JQL query using
// POST /rest/api/3/search/approximate-count (a single call instead of paging
// every issue key).
func (c *Client) SearchCount(ctx context.Context, jql string) (int, error) {
	return c.SearchApproximateCount(ctx, jql)
}

// GetBoards fetches all boards (Scrum, Kanban, Simple) for a project from the
// Agile API, paginating across all pages.
func (c *Client) GetBoards(ctx context.Context, projectKey string) ([]Board, error) {
	var all []Board
	startAt := 0
	for {
		resp, err := c.SearchBoards(ctx, BoardSearchOptions{
			ProjectKeyOrID: projectKey,
			StartAt:        startAt,
			MaxResults:     50,
		})
		if err != nil {
			return nil, err
		}
		all = append(all, resp.Values...)
		if resp.IsLast || len(resp.Values) == 0 || (resp.Total > 0 && startAt+len(resp.Values) >= resp.Total) {
			break
		}
		if len(resp.Values) < 50 && resp.Total == 0 {
			break
		}
		startAt += len(resp.Values)
	}
	return all, nil
}

// GetBoardFilterJQL returns the JQL of the saved filter backing a board, i.e.
// the query that defines which issues appear on it. It resolves the board's
// filter id via the board configuration endpoint, then fetches that filter's
// JQL. This is the scope used to refresh ranks for just one board without
// scanning the whole project — the global LexoRank values of the board's issue
// subset fully determine their on-board ordering.
func (c *Client) GetBoardFilterJQL(ctx context.Context, boardID int) (string, error) {
	var cfg BoardConfiguration
	if err := c.get(ctx, fmt.Sprintf("/rest/agile/1.0/board/%d/configuration", boardID), &cfg); err != nil {
		return "", fmt.Errorf("fetching board %d configuration: %w", boardID, err)
	}
	if cfg.Filter.ID == "" {
		return "", fmt.Errorf("board %d has no backing filter", boardID)
	}
	var f Filter
	if err := c.get(ctx, fmt.Sprintf("/rest/api/3/filter/%s", cfg.Filter.ID), &f); err != nil {
		return "", fmt.Errorf("fetching filter %s for board %d: %w", cfg.Filter.ID, boardID, err)
	}
	if strings.TrimSpace(f.JQL) == "" {
		return "", fmt.Errorf("board %d filter %s has empty JQL", boardID, cfg.Filter.ID)
	}
	return f.JQL, nil
}

// GetSprints fetches all sprints for a board from the Agile API.
func (c *Client) GetSprints(ctx context.Context, boardID int) ([]Sprint, error) {
	path := fmt.Sprintf("/rest/agile/1.0/board/%d/sprint?state=active,closed,future", boardID)
	var resp SprintResponse
	if err := c.get(ctx, path, &resp); err != nil {
		return nil, err
	}
	return resp.Values, nil
}

// GetSprintIssues fetches all issue keys in a sprint from the Agile API.
func (c *Client) GetSprintIssues(ctx context.Context, sprintID int) ([]string, error) {
	path := fmt.Sprintf("/rest/agile/1.0/sprint/%d/issue?fields=key", sprintID)
	var resp SprintIssuesResponse
	if err := c.get(ctx, path, &resp); err != nil {
		return nil, err
	}
	keys := make([]string, len(resp.Issues))
	for i, issue := range resp.Issues {
		keys[i] = issue.Key
	}
	return keys, nil
}

// GetDevInfo fetches development information for an issue from the dev-status API.
// Requires the issue's numeric ID (not key). Returns pull requests, branches, and commits.
func (c *Client) GetDevInfo(ctx context.Context, issueID string) (*DevInfoResponse, error) {
	path := fmt.Sprintf("/rest/dev-status/latest/issue/detail?issueId=%s&applicationType=GitHub&dataType=pullrequest", issueID)
	var resp DevInfoResponse
	if err := c.get(ctx, path, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
