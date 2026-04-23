package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// CreateIssueResponse is the response from POST /rest/api/3/issue.
type CreateIssueResponse struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Self string `json:"self"`
}

// CreateIssue creates a new Jira issue and returns the created issue's key.
func (c *Client) CreateIssue(ctx context.Context, fields map[string]interface{}) (*CreateIssueResponse, error) {
	payload := map[string]interface{}{
		"fields": fields,
	}
	var resp CreateIssueResponse
	if err := c.postDecode(ctx, "/rest/api/3/issue", payload, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// UpdateField updates a single field on a Jira issue.
func (c *Client) UpdateField(ctx context.Context, issueKey, fieldID string, value interface{}) error {
	payload := map[string]interface{}{
		"fields": map[string]interface{}{
			fieldID: value,
		},
	}
	return c.put(ctx, fmt.Sprintf("/rest/api/3/issue/%s", issueKey), payload)
}

// AddComment adds a comment to a Jira issue.
func (c *Client) AddComment(ctx context.Context, issueKey, body string) error {
	payload := map[string]interface{}{
		"body": map[string]interface{}{
			"type":    "doc",
			"version": 1,
			"content": []map[string]interface{}{
				{
					"type": "paragraph",
					"content": []map[string]interface{}{
						{"type": "text", "text": body},
					},
				},
			},
		},
	}
	return c.post(ctx, fmt.Sprintf("/rest/api/3/issue/%s/comment", issueKey), payload)
}

// GetTransitions returns available transitions for an issue.
func (c *Client) GetTransitions(ctx context.Context, issueKey string) ([]*Transition, error) {
	var resp TransitionsResponse
	if err := c.get(ctx, fmt.Sprintf("/rest/api/3/issue/%s/transitions", issueKey), &resp); err != nil {
		return nil, err
	}
	return resp.Transitions, nil
}

// ExecuteTransition executes a workflow transition on an issue.
func (c *Client) ExecuteTransition(ctx context.Context, issueKey, transitionID string) error {
	payload := map[string]interface{}{
		"transition": map[string]string{"id": transitionID},
	}
	return c.post(ctx, fmt.Sprintf("/rest/api/3/issue/%s/transitions", issueKey), payload)
}

func (c *Client) put(ctx context.Context, path string, body interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := c.doRequest(ctx, http.MethodPut, path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("jira PUT %s: %d %s", path, resp.StatusCode, string(b))
	}
	return nil
}

func (c *Client) post(ctx context.Context, path string, body interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := c.doRequest(ctx, http.MethodPost, path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("jira POST %s: %d %s", path, resp.StatusCode, string(b))
	}
	return nil
}
