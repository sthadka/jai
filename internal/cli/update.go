package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/pkg/fieldwrap"
	"github.com/sthadka/jai/pkg/jira"
	"github.com/sthadka/jai/pkg/output"
	db "github.com/sthadka/jai/pkg/store"
	synce "github.com/sthadka/jai/pkg/sync"
)

var (
	updateSetValues  []string
	updateTransition string
	updateComment    string
	updateQueue      bool
)

var updateCmd = &cobra.Command{
	Use:   "update <key>",
	Short: "Composite write operation: set fields, transition, and comment in one command",
	Long: `Update a Jira issue with multiple operations in a single command.

By default, changes are pushed to Jira immediately. Use --queue to
defer the push until 'jai push'.

Examples:
  jai update ROX-123 --set priority=High --set assignee=alice --transition "In Progress" --comment "Starting work"
  jai update ROX-456 --set priority=Major --comment "Escalating"
  jai update ROX-789 --transition Done --queue`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {

		app := cliFrom(cmd)
		issueKey := args[0]

		if len(updateSetValues) == 0 && updateTransition == "" && updateComment == "" {
			msg := "at least one of --set, --transition, or --comment is required"
			if app.jsonOut {
				fmt.Println(string(output.Err("ValidationError", msg)))
				return nil
			}
			return fmt.Errorf("%s", msg)
		}

		if updateQueue {
			if err := app.engine.Store.EnsurePendingChangesTable(); err != nil {
				return err
			}
		}

		result := &updateResult{
			IssueKey:     issueKey,
			FieldsSet:    0,
			FieldsFailed: make([]string, 0),
			Status:       "synced",
		}

		if updateQueue {
			result.Status = "queued"
		}

		// Phase 1: Set fields
		if len(updateSetValues) > 0 {
			if err := app.updateSetFields(cmd, issueKey, result); err != nil {
				return err
			}
		}

		// Phase 2: Execute transition
		if updateTransition != "" {
			if err := app.updateExecuteTransition(cmd, issueKey, updateTransition, result); err != nil {
				return err
			}
		}

		// Phase 3: Add comment
		if updateComment != "" {
			if err := app.updateAddComment(cmd, issueKey, updateComment, result); err != nil {
				return err
			}
		}

		// Output results
		if app.jsonOut {
			fmt.Println(string(output.OK(result)))
			return nil
		}

		// Human-readable output
		fmt.Printf("%s update:\n", issueKey)
		if len(updateSetValues) > 0 {
			if result.FieldsSet > 0 {
				statusMark := "✓"
				if updateQueue {
					statusMark = "(queued)"
				}
				fmt.Printf("  Fields set: %d %s\n", result.FieldsSet, statusMark)
			}
			if len(result.FieldsFailed) > 0 {
				fmt.Printf("  Fields failed: %v\n", result.FieldsFailed)
			}
		}
		if updateTransition != "" {
			switch result.TransitionStatus {
			case "ok":
				statusMark := "✓"
				if updateQueue {
					statusMark = "(queued)"
				}
				fmt.Printf("  Transition: %s %s\n", result.TransitionName, statusMark)
			case "failed":
				fmt.Printf("  Transition: failed (%s)\n", result.TransitionError)
			}
		}
		if updateComment != "" {
			switch result.CommentStatus {
			case "ok":
				statusMark := "✓"
				if updateQueue {
					statusMark = "(queued)"
				}
				fmt.Printf("  Comment: added %s\n", statusMark)
			case "failed":
				fmt.Printf("  Comment: failed (%s)\n", result.CommentError)
			}
		}

		return nil
	},
}

type updateResult struct {
	IssueKey         string   `json:"issue_key"`
	FieldsSet        int      `json:"fields_set"`
	FieldsFailed     []string `json:"fields_failed,omitempty"`
	TransitionName   string   `json:"transition,omitempty"`
	TransitionStatus string   `json:"transition_status,omitempty"`
	TransitionError  string   `json:"transition_error,omitempty"`
	CommentStatus    string   `json:"comment_status,omitempty"`
	CommentError     string   `json:"comment_error,omitempty"`
	Status           string   `json:"status"`
}

func (app *CLI) updateSetFields(cmd *cobra.Command, issueKey string, result *updateResult) error {
	fieldMap, err := app.engine.Store.FieldMapByJiraID()
	if err != nil {
		return err
	}

	for _, setExpr := range updateSetValues {
		parts := strings.SplitN(setExpr, "=", 2)
		if len(parts) != 2 {
			result.FieldsFailed = append(result.FieldsFailed, fmt.Sprintf("%s (invalid format)", setExpr))
			continue
		}
		fieldName := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		var jiraID string
		var fieldType string
		var itemType string
		for id, f := range fieldMap {
			if f.Name == fieldName {
				jiraID = id
				fieldType = f.Type
				itemType = f.ItemType
				break
			}
		}
		if jiraID == "" {
			result.FieldsFailed = append(result.FieldsFailed, fmt.Sprintf("%s (unknown field)", fieldName))
			continue
		}

		var payloadVal interface{} = value
		localVal := value

		if fieldType == "array" {
			resolveAccountID := func(v string) (string, error) {
				return app.engine.Client.ResolveAccountID(cmd.Context(), v)
			}
			items := fieldwrap.ParseArrayInput(value)
			wrapped, wrapErr := fieldwrap.WrapArrayItems(jiraID, itemType, items, resolveAccountID)
			if wrapErr != nil {
				result.FieldsFailed = append(result.FieldsFailed, fmt.Sprintf("%s (%v)", fieldName, wrapErr))
				continue
			}
			payloadVal = wrapped
			j, _ := json.Marshal(items)
			localVal = string(j)
		} else {
			resolveAccountID := func(v string) (string, error) {
				return app.engine.Client.ResolveAccountID(cmd.Context(), v)
			}
			if wrapped, ok, wrapErr := fieldwrap.WrapScalarFieldValue(jiraID, value, resolveAccountID); ok {
				if wrapErr != nil {
					result.FieldsFailed = append(result.FieldsFailed, fmt.Sprintf("%s (%v)", fieldName, wrapErr))
					continue
				}
				payloadVal = wrapped
			} else if jira.IsADFField(jiraID, fieldType) {
				payloadVal = jira.MarkdownToADF(value)
			}
		}

		if updateQueue {
			payload, _ := json.Marshal(map[string]interface{}{"field": jiraID, "value": payloadVal})
			if err := app.engine.Store.InsertPendingChange(issueKey, "set_field", string(payload)); err != nil {
				result.FieldsFailed = append(result.FieldsFailed, fmt.Sprintf("%s (%v)", fieldName, err))
				continue
			}
		} else {
			if err := app.engine.Client.UpdateField(cmd.Context(), issueKey, jiraID, payloadVal); err != nil {
				result.FieldsFailed = append(result.FieldsFailed, fmt.Sprintf("%s (%v)", fieldName, err))
				continue
			}
		}

		_, err := app.engine.Store.Exec(
			fmt.Sprintf("UPDATE issues SET %s = ?, synced_at = datetime('now') WHERE key = ?", fieldName),
			localVal, issueKey,
		)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: local update failed for %s: %v\n", fieldName, err)
		}

		result.FieldsSet++
	}

	return nil
}

func (app *CLI) updateExecuteTransition(cmd *cobra.Command, issueKey, targetName string, result *updateResult) error {
	transitions, err := app.engine.Client.GetTransitions(cmd.Context(), issueKey)
	if err != nil {
		result.TransitionStatus = "failed"
		result.TransitionError = fmt.Sprintf("fetching transitions: %v", err)
		return nil
	}

	match, ambiguous := resolveTransition(targetName, transitions)

	if match == nil && ambiguous != nil {
		result.TransitionStatus = "failed"
		result.TransitionError = fmt.Sprintf("ambiguous transition %q", targetName)
		return nil
	}

	if match == nil {
		result.TransitionStatus = "failed"
		result.TransitionError = fmt.Sprintf("unknown transition %q", targetName)
		return nil
	}

	result.TransitionName = match.Name

	if updateQueue {
		payload, _ := json.Marshal(map[string]string{"transition_id": match.ID})
		if err := app.engine.Store.InsertPendingChange(issueKey, "transition", string(payload)); err != nil {
			result.TransitionStatus = "failed"
			result.TransitionError = err.Error()
			return nil
		}
		result.TransitionStatus = "ok"
	} else {
		if err := app.engine.Client.ExecuteTransition(cmd.Context(), issueKey, match.ID); err != nil {
			result.TransitionStatus = "failed"
			result.TransitionError = err.Error()
			return nil
		}

		// Refresh the local DB so status is immediately queryable.
		if apiIssue, fetchErr := app.engine.Client.GetIssue(cmd.Context(), issueKey); fetchErr == nil {
			rawJSON, _ := json.Marshal(apiIssue)
			if fieldMapRefresh, fmErr := app.engine.Store.FieldMapByJiraID(); fmErr == nil {
				if dbIssue, extra, denormErr := synce.Denormalize(rawJSON, fieldMapRefresh); denormErr == nil {
					_ = app.engine.Store.UpsertIssue(dbIssue, extra)
				}
			}
		}

		result.TransitionStatus = "ok"
	}

	return nil
}

func (app *CLI) updateAddComment(cmd *cobra.Command, issueKey, text string, result *updateResult) error {
	var commentID string

	if updateQueue {
		payload, _ := json.Marshal(map[string]string{"body": text})
		if err := app.engine.Store.InsertPendingChange(issueKey, "add_comment", string(payload)); err != nil {
			result.CommentStatus = "failed"
			result.CommentError = err.Error()
			return nil
		}
		commentID = fmt.Sprintf("local_%d", time.Now().UnixNano())
		result.CommentStatus = "ok"
	} else {
		id, err := app.engine.Client.AddComment(cmd.Context(), issueKey, text)
		if err != nil {
			result.CommentStatus = "failed"
			result.CommentError = err.Error()
			return nil
		}
		commentID = id
		result.CommentStatus = "ok"
	}

	now := time.Now().UTC().Format(time.RFC3339)
	localComment := &db.Comment{
		ID:       commentID,
		IssueKey: issueKey,
		Author:   app.engine.Config.Me,
		Body:     text,
		Created:  now,
		Updated:  now,
	}
	_ = app.engine.Store.UpsertComment(localComment)
	_ = app.engine.Store.UpdateIssueCommentsText(issueKey)

	return nil
}

func init() {
	updateCmd.Flags().StringArrayVar(&updateSetValues, "set", nil, "Set a field value (format: field=value, repeatable)")
	updateCmd.Flags().StringVar(&updateTransition, "transition", "", "Transition to status")
	updateCmd.Flags().StringVar(&updateComment, "comment", "", "Add a comment")
	updateCmd.Flags().BoolVarP(&updateQueue, "queue", "q", false, "Queue changes locally instead of pushing to Jira immediately")
	rootCmd.AddCommand(updateCmd)
}
