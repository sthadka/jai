package sync

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/jira"
)

const maxRetries = 5

// PushResult summarizes a single pending change push attempt.
type PushResult struct {
	IssueKey  string
	Operation string
	Success   bool
	Error     error
}

// Writer processes the pending_changes queue.
type Writer struct {
	db     *db.DB
	client *jira.Client
}

// NewWriter creates a Writer.
func NewWriter(database *db.DB, client *jira.Client) *Writer {
	return &Writer{db: database, client: client}
}

// ProcessQueue processes all pending (unsynced) changes.
func (w *Writer) ProcessQueue(ctx context.Context) ([]PushResult, error) {
	changes, err := w.db.ListPendingChanges()
	if err != nil {
		return nil, fmt.Errorf("listing pending changes: %w", err)
	}

	var results []PushResult
	for _, c := range changes {
		if c.RetryCount >= maxRetries {
			results = append(results, PushResult{
				IssueKey:  c.IssueKey,
				Operation: c.Operation,
				Success:   false,
				Error:     fmt.Errorf("max retries (%d) exceeded: %s", maxRetries, c.LastError.String),
			})
			continue
		}

		err := w.process(ctx, c)
		if err != nil {
			_ = w.db.RecordRetryError(c.ID, err.Error())
			results = append(results, PushResult{
				IssueKey:  c.IssueKey,
				Operation: c.Operation,
				Success:   false,
				Error:     err,
			})
			continue
		}

		_ = w.db.MarkSynced(c.ID)
		results = append(results, PushResult{
			IssueKey:  c.IssueKey,
			Operation: c.Operation,
			Success:   true,
		})
	}

	return results, nil
}

func (w *Writer) process(ctx context.Context, c *db.PendingChange) error {
	switch c.Operation {
	case "set_field":
		return w.processSetField(ctx, c)
	case "add_comment":
		return w.processAddComment(ctx, c)
	case "transition":
		return w.processTransition(ctx, c)
	default:
		return fmt.Errorf("unknown operation: %s", c.Operation)
	}
}

func (w *Writer) processSetField(ctx context.Context, c *db.PendingChange) error {
	var payload struct {
		Field string      `json:"field"`
		Value interface{} `json:"value"`
	}
	if err := json.Unmarshal([]byte(c.Payload), &payload); err != nil {
		return fmt.Errorf("parsing set_field payload: %w", err)
	}
	return w.client.UpdateField(ctx, c.IssueKey, payload.Field, payload.Value)
}

func (w *Writer) processAddComment(ctx context.Context, c *db.PendingChange) error {
	var payload struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(c.Payload), &payload); err != nil {
		return fmt.Errorf("parsing add_comment payload: %w", err)
	}
	return w.client.AddComment(ctx, c.IssueKey, payload.Body)
}

func (w *Writer) processTransition(ctx context.Context, c *db.PendingChange) error {
	var payload struct {
		TransitionID string `json:"transition_id"`
	}
	if err := json.Unmarshal([]byte(c.Payload), &payload); err != nil {
		return fmt.Errorf("parsing transition payload: %w", err)
	}
	return w.client.ExecuteTransition(ctx, c.IssueKey, payload.TransitionID)
}
