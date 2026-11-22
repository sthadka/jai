package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// BulkTaskResponse is returned by asynchronous /rest/api/3/bulk/issues/* write
// endpoints. Poll GetBulkTask with TaskID to track completion.
type BulkTaskResponse struct {
	TaskID string `json:"taskId"`
}

// BulkTaskStatus is the response from GET /rest/api/3/bulk/queue/{taskId}.
type BulkTaskStatus struct {
	TaskID                          string              `json:"taskId"`
	Status                          string              `json:"status"`
	ProgressPercent                 int                 `json:"progressPercent"`
	SubmittedTime                   int64               `json:"submittedTime"`
	StartedTime                     int64               `json:"startedTime"`
	UpdatedTime                     int64               `json:"updatedTime"`
	TotalIssueCount                 int                 `json:"totalIssueCount"`
	InvalidOrInaccessibleIssueCount int                 `json:"invalidOrInaccessibleIssueCount"`
	ProcessedAccessibleIssues       []int64             `json:"processedAccessibleIssues"`
	FailedAccessibleIssues          map[string][]string `json:"failedAccessibleIssues"`
}

// BulkFieldsQueryOptions configures GET /rest/api/3/bulk/issues/fields.
type BulkFieldsQueryOptions struct {
	SearchText    string
	StartingAfter string
	EndingBefore  string
}

// BulkEditableField describes a single field eligible for bulk edit.
type BulkEditableField struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	Type                 string `json:"type"`
	Description          string `json:"description,omitempty"`
	IsRequired           bool   `json:"isRequired"`
	UnavailableMessage   string `json:"unavailableMessage,omitempty"`
	FieldOptions         json.RawMessage `json:"fieldOptions,omitempty"`
	MultiSelectFieldOpts []string `json:"multiSelectFieldOptions,omitempty"`
}

// BulkEditableFieldsResponse is the response from GET /rest/api/3/bulk/issues/fields.
type BulkEditableFieldsResponse struct {
	Fields        []BulkEditableField `json:"fields"`
	StartingAfter string              `json:"startingAfter,omitempty"`
	EndingBefore  string              `json:"endingBefore,omitempty"`
}

// BulkEditFieldsRequest is the request body for POST /rest/api/3/bulk/issues/fields.
type BulkEditFieldsRequest struct {
	SelectedIssueIDsOrKeys []string       `json:"selectedIssueIdsOrKeys"`
	EditedFieldsInput      map[string]any `json:"editedFieldsInput"`
	SendBulkNotification   *bool          `json:"sendBulkNotification,omitempty"`
}

// BulkTransitionInput specifies one transition and the issues to apply it to.
type BulkTransitionInput struct {
	SelectedIssueIDsOrKeys []string `json:"selectedIssueIdsOrKeys"`
	TransitionID           string   `json:"transitionId"`
}

// BulkTransitionRequest is the request body for POST /rest/api/3/bulk/issues/transition.
type BulkTransitionRequest struct {
	BulkTransitionInputs []BulkTransitionInput `json:"bulkTransitionInputs"`
	SendBulkNotification *bool                 `json:"sendBulkNotification,omitempty"`
}

// BulkTransitionsResponse is the response from GET /rest/api/3/bulk/issues/transition.
type BulkTransitionsResponse struct {
	AvailableTransitions []json.RawMessage `json:"availableTransitions"`
	StartingAfter        string            `json:"startingAfter,omitempty"`
	EndingBefore         string            `json:"endingBefore,omitempty"`
}

// GetBulkEditableFields retrieves fields that can be bulk-edited for the given
// issues via GET /rest/api/3/bulk/issues/fields.
func (c *Client) GetBulkEditableFields(ctx context.Context, issueIDsOrKeys []string, opts BulkFieldsQueryOptions) (*BulkEditableFieldsResponse, error) {
	if len(issueIDsOrKeys) == 0 {
		return nil, fmt.Errorf("bulk editable fields: at least one issue ID or key is required")
	}
	q := url.Values{}
	q.Set("issueIdsOrKeys", strings.Join(issueIDsOrKeys, ","))
	if opts.SearchText != "" {
		q.Set("searchText", opts.SearchText)
	}
	if opts.StartingAfter != "" {
		q.Set("startingAfter", opts.StartingAfter)
	}
	if opts.EndingBefore != "" {
		q.Set("endingBefore", opts.EndingBefore)
	}
	var resp BulkEditableFieldsResponse
	if err := c.get(ctx, "/rest/api/3/bulk/issues/fields?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BulkEditFields submits an asynchronous bulk field edit for up to 1,000 issues
// via POST /rest/api/3/bulk/issues/fields. Returns *AuthError on HTTP 403 when
// the caller lacks Global Bulk Change permission.
func (c *Client) BulkEditFields(ctx context.Context, req BulkEditFieldsRequest) (*BulkTaskResponse, error) {
	if len(req.SelectedIssueIDsOrKeys) == 0 {
		return nil, fmt.Errorf("bulk edit fields: at least one issue ID or key is required")
	}
	var resp BulkTaskResponse
	if err := c.postDecode(ctx, "/rest/api/3/bulk/issues/fields", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetBulkTransitions retrieves available workflow transitions for the given
// issues via GET /rest/api/3/bulk/issues/transition.
func (c *Client) GetBulkTransitions(ctx context.Context, issueIDsOrKeys []string, startingAfter string) (*BulkTransitionsResponse, error) {
	if len(issueIDsOrKeys) == 0 {
		return nil, fmt.Errorf("bulk transitions: at least one issue ID or key is required")
	}
	q := url.Values{}
	q.Set("issueIdsOrKeys", strings.Join(issueIDsOrKeys, ","))
	if startingAfter != "" {
		q.Set("startingAfter", startingAfter)
	}
	var resp BulkTransitionsResponse
	if err := c.get(ctx, "/rest/api/3/bulk/issues/transition?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BulkTransitionIssues submits an asynchronous bulk transition for up to 1,000
// issues via POST /rest/api/3/bulk/issues/transition.
func (c *Client) BulkTransitionIssues(ctx context.Context, req BulkTransitionRequest) (*BulkTaskResponse, error) {
	if len(req.BulkTransitionInputs) == 0 {
		return nil, fmt.Errorf("bulk transition: at least one transition input is required")
	}
	var resp BulkTaskResponse
	if err := c.postDecode(ctx, "/rest/api/3/bulk/issues/transition", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BulkDeleteIssues submits an asynchronous bulk delete for up to 1,000 issues
// via POST /rest/api/3/bulk/issues/delete.
func (c *Client) BulkDeleteIssues(ctx context.Context, issueIDsOrKeys []string, sendNotification bool) (*BulkTaskResponse, error) {
	if len(issueIDsOrKeys) == 0 {
		return nil, fmt.Errorf("bulk delete: at least one issue ID or key is required")
	}
	req := map[string]any{
		"selectedIssueIdsOrKeys": issueIDsOrKeys,
		"sendBulkNotification":   sendNotification,
	}
	var resp BulkTaskResponse
	if err := c.postDecode(ctx, "/rest/api/3/bulk/issues/delete", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BulkWatchIssues submits an asynchronous bulk watch for up to 1,000 issues
// via POST /rest/api/3/bulk/issues/watch.
func (c *Client) BulkWatchIssues(ctx context.Context, issueIDsOrKeys []string) (*BulkTaskResponse, error) {
	if len(issueIDsOrKeys) == 0 {
		return nil, fmt.Errorf("bulk watch: at least one issue ID or key is required")
	}
	req := map[string]any{
		"selectedIssueIdsOrKeys": issueIDsOrKeys,
	}
	var resp BulkTaskResponse
	if err := c.postDecode(ctx, "/rest/api/3/bulk/issues/watch", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BulkUnwatchIssues submits an asynchronous bulk unwatch for up to 1,000 issues
// via POST /rest/api/3/bulk/issues/unwatch.
func (c *Client) BulkUnwatchIssues(ctx context.Context, issueIDsOrKeys []string) (*BulkTaskResponse, error) {
	if len(issueIDsOrKeys) == 0 {
		return nil, fmt.Errorf("bulk unwatch: at least one issue ID or key is required")
	}
	req := map[string]any{
		"selectedIssueIdsOrKeys": issueIDsOrKeys,
	}
	var resp BulkTaskResponse
	if err := c.postDecode(ctx, "/rest/api/3/bulk/issues/unwatch", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetBulkTask fetches the progress and outcome of an asynchronous bulk
// operation submitted by any Bulk* write endpoint via
// GET /rest/api/3/bulk/queue/{taskId}.
func (c *Client) GetBulkTask(ctx context.Context, taskID string) (*BulkTaskStatus, error) {
	if strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("bulk task: taskID is required")
	}
	var resp BulkTaskStatus
	if err := c.get(ctx, "/rest/api/3/bulk/queue/"+url.PathEscape(taskID), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
