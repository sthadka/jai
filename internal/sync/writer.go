package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/sthadka/jai/internal/config"
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
	mw     *metricWriter
}

// NewWriter creates a Writer with no metric wiring. Pushing changes still mutates
// Jira and the optimistic local snapshot; use NewWriterWithMetrics to also refresh
// flow-metric facts for the affected issues.
func NewWriter(database *db.DB, client *jira.Client) *Writer {
	return &Writer{db: database, client: client}
}

// NewWriterWithMetrics creates a Writer that refreshes flow-metric facts for the
// issues whose queued changes it successfully pushes. Metric refresh is a no-op
// when metrics are unconfigured (nil metrics: block).
func NewWriterWithMetrics(database *db.DB, client *jira.Client, cfg *config.Config) *Writer {
	return &Writer{db: database, client: client, mw: newMetricWriter(database, cfg)}
}

// ProcessQueue processes all pending (unsynced) changes.
func (w *Writer) ProcessQueue(ctx context.Context) ([]PushResult, error) {
	changes, err := w.db.ListPendingChanges()
	if err != nil {
		return nil, fmt.Errorf("listing pending changes: %w", err)
	}

	var results []PushResult
	var pushed []string // issue keys whose changes pushed successfully (dedup below)
	seen := map[string]bool{}
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
		if !seen[c.IssueKey] {
			seen[c.IssueKey] = true
			pushed = append(pushed, c.IssueKey)
		}
		results = append(results, PushResult{
			IssueKey:  c.IssueKey,
			Operation: c.Operation,
			Success:   true,
		})
	}

	// A pushed change also updated the optimistic local snapshot (issues table), so
	// refresh the flow-metric facts for the affected issues within the active
	// generation. No-op when metrics are unconfigured; logged, not fatal, on failure
	// so a metric build problem never fails an otherwise-successful push.
	if err := w.mw.refreshTouched(pushed); err != nil {
		fmt.Fprintf(os.Stderr, "warning: metric refresh after push failed: %v\n", err)
	}

	return results, nil
}

func (w *Writer) process(ctx context.Context, c *db.PendingChange) error {
	switch c.Operation {
	case "set_field":
		return w.processSetField(ctx, c)
	case "update_field":
		return w.processUpdateField(ctx, c)
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

func (w *Writer) processUpdateField(ctx context.Context, c *db.PendingChange) error {
	var payload struct {
		Field string      `json:"field"`
		Op    string      `json:"op"`
		Value interface{} `json:"value"`
	}
	if err := json.Unmarshal([]byte(c.Payload), &payload); err != nil {
		return fmt.Errorf("parsing update_field payload: %w", err)
	}
	return w.client.UpdateFieldOp(ctx, c.IssueKey, payload.Field, payload.Op, payload.Value)
}

func (w *Writer) processAddComment(ctx context.Context, c *db.PendingChange) error {
	var payload struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(c.Payload), &payload); err != nil {
		return fmt.Errorf("parsing add_comment payload: %w", err)
	}
	_, err := w.client.AddComment(ctx, c.IssueKey, payload.Body)
	return err
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
