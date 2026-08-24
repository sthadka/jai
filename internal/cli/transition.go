package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/jira"
	"github.com/sthadka/jai/internal/output"
	synce "github.com/sthadka/jai/internal/sync"
)

var (
	transitionQueue bool
	transitionQuery string
)

func resolveTransition(name string, transitions []*jira.Transition) (match *jira.Transition, ambiguous []*jira.Transition) {
	lower := strings.ToLower(name)
	var matches []*jira.Transition
	for _, t := range transitions {
		if strings.ToLower(t.Name) == lower {
			matches = append(matches, t)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return nil, matches
	}
	return nil, nil
}

func formatTransitionNames(transitions []*jira.Transition) string {
	names := make([]string, len(transitions))
	for i, t := range transitions {
		names[i] = fmt.Sprintf("  - %s (id: %s)", t.Name, t.ID)
	}
	return strings.Join(names, "\n")
}

var transitionCmd = &cobra.Command{
	Use:   "transition [key] [status]",
	Short: "Transition one or more Jira issues to a new status",
	Long: `Move one or more Jira issues through their workflow.

By default, the transition is pushed to Jira immediately. Use --queue to
defer the push until 'jai push'.

Bulk operations with comma-separated keys:
  jai transition ROX-1,ROX-2,ROX-3 "Done"

Bulk operations with a SQL query:
  jai transition --query "SELECT key FROM issues WHERE type='Bug'" "Done"`,
	Args: func(cmd *cobra.Command, args []string) error {
		if transitionQuery != "" {
			if len(args) < 1 || len(args) > 2 {
				return fmt.Errorf("with --query, provide [status] (got %d args)", len(args))
			}
			return nil
		}
		return cobra.RangeArgs(1, 2)(cmd, args)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		var keys []string
		var targetName string
		listFlag, _ := cmd.Flags().GetBool("list")

		if transitionQuery != "" {
			if len(args) >= 1 {
				targetName = args[0]
			}
			results, err := g.query.Execute(transitionQuery)
			if err != nil {
				if g.jsonOut {
					fmt.Println(string(output.Err("QueryError", err.Error())))
					return nil
				}
				return fmt.Errorf("query: %w", err)
			}
			keys, err = extractKeys(results.Columns, results.Rows)
			if err != nil {
				if g.jsonOut {
					fmt.Println(string(output.Err("QueryError", err.Error())))
					return nil
				}
				return err
			}
			if len(keys) == 0 {
				msg := "query returned 0 rows"
				if g.jsonOut {
					fmt.Println(string(output.Err("QueryError", msg)))
					return nil
				}
				return fmt.Errorf("%s", msg)
			}
		} else {
			keys = expandKeys(args[0])
			if len(args) >= 2 {
				targetName = args[1]
			}
		}

		// Handle bulk transitions
		if len(keys) > 1 {
			if listFlag {
				msg := "--list flag cannot be used with bulk operations"
				if g.jsonOut {
					fmt.Println(string(output.Err("ValidationError", msg)))
					return nil
				}
				return fmt.Errorf("%s", msg)
			}
			if targetName == "" {
				msg := "status argument required for bulk transitions"
				if g.jsonOut {
					fmt.Println(string(output.Err("ValidationError", msg)))
					return nil
				}
				return fmt.Errorf("%s", msg)
			}
			return transitionBulk(cmd, keys, targetName)
		}

		// Single key logic (existing)
		issueKey := keys[0]

		transitions, err := g.jira.GetTransitions(cmd.Context(), issueKey)
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("JiraError", fmt.Sprintf("fetching transitions: %v", err))))
				return nil
			}
			return fmt.Errorf("fetching transitions for %s: %w", issueKey, err)
		}

		if listFlag || targetName == "" {
			type transitionInfo struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			items := make([]transitionInfo, len(transitions))
			for i, t := range transitions {
				items[i] = transitionInfo{ID: t.ID, Name: t.Name}
			}
			if g.jsonOut {
				fmt.Println(string(output.OK(map[string]interface{}{
					"issue_key":   issueKey,
					"transitions": items,
				})))
				return nil
			}
			if len(transitions) == 0 {
				fmt.Printf("%s: no transitions available\n", issueKey)
				return nil
			}
			fmt.Printf("Available transitions for %s:\n%s\n", issueKey, formatTransitionNames(transitions))
			return nil
		}

		match, ambiguous := resolveTransition(targetName, transitions)

		if match == nil && ambiguous != nil {
			msg := fmt.Sprintf("ambiguous transition %q matches multiple options:\n%s", targetName, formatTransitionNames(ambiguous))
			if g.jsonOut {
				fmt.Println(string(output.Err("ValidationError", msg)))
				return nil
			}
			return fmt.Errorf("%s", msg)
		}

		if match == nil {
			msg := fmt.Sprintf("unknown transition %q for %s", targetName, issueKey)
			if len(transitions) > 0 {
				msg += fmt.Sprintf("\nAvailable transitions:\n%s", formatTransitionNames(transitions))
			}
			if g.jsonOut {
				fmt.Println(string(output.Err("ValidationError", msg)))
				return nil
			}
			return fmt.Errorf("%s", msg)
		}

		status := "synced"
		if transitionQueue {
			if err := g.db.EnsurePendingChangesTable(); err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]string{"transition_id": match.ID})
			if err := g.db.InsertPendingChange(issueKey, "transition", string(payload)); err != nil {
				return err
			}
			status = "queued"
		} else {
			if err := g.jira.ExecuteTransition(cmd.Context(), issueKey, match.ID); err != nil {
				msg := fmt.Sprintf("transition failed: %v", err)
				if g.jsonOut {
					fmt.Println(string(output.Err("JiraError", msg)))
					return nil
				}
				return fmt.Errorf("%s", msg)
			}

			// Refresh the local DB so status is immediately queryable.
			if apiIssue, fetchErr := g.jira.GetIssue(cmd.Context(), issueKey); fetchErr == nil {
				rawJSON, _ := json.Marshal(apiIssue)
				if fieldMap, fmErr := g.db.FieldMapByJiraID(); fmErr == nil {
					if dbIssue, extra, denormErr := synce.Denormalize(rawJSON, fieldMap); denormErr == nil {
						_ = g.db.UpsertIssue(dbIssue, extra)
					}
				}
			}
		}

		if g.jsonOut {
			fmt.Println(string(output.OK(map[string]string{
				"issue_key":     issueKey,
				"transition":    match.Name,
				"transition_id": match.ID,
				"status":        status,
			})))
			return nil
		}

		if status == "queued" {
			fmt.Printf("%s: transition to %q (queued)\n", issueKey, match.Name)
		} else {
			fmt.Printf("%s: transitioned to %q \u2713\n", issueKey, match.Name)
		}
		return nil
	},
}

func transitionBulk(cmd *cobra.Command, keys []string, targetName string) error {
	type result struct {
		Key        string `json:"key"`
		Status     string `json:"status"`
		Transition string `json:"transition,omitempty"`
		Error      string `json:"error,omitempty"`
	}

	var succeeded, failed int
	var results []result

	for _, issueKey := range keys {
		transitions, err := g.jira.GetTransitions(cmd.Context(), issueKey)
		if err != nil {
			failed++
			res := result{
				Key:    issueKey,
				Status: "failed",
				Error:  fmt.Sprintf("fetching transitions: %v", err),
			}
			results = append(results, res)
			if !g.jsonOut {
				fmt.Fprintf(cmd.ErrOrStderr(), "  ✗ %s: %v\n", issueKey, err)
			}
			continue
		}

		match, ambiguous := resolveTransition(targetName, transitions)

		if match == nil && ambiguous != nil {
			failed++
			res := result{
				Key:    issueKey,
				Status: "failed",
				Error:  fmt.Sprintf("ambiguous transition %q", targetName),
			}
			results = append(results, res)
			if !g.jsonOut {
				fmt.Fprintf(cmd.ErrOrStderr(), "  ✗ %s: ambiguous transition %q\n", issueKey, targetName)
			}
			continue
		}

		if match == nil {
			failed++
			res := result{
				Key:    issueKey,
				Status: "failed",
				Error:  fmt.Sprintf("unknown transition %q", targetName),
			}
			results = append(results, res)
			if !g.jsonOut {
				fmt.Fprintf(cmd.ErrOrStderr(), "  ✗ %s: unknown transition %q\n", issueKey, targetName)
			}
			continue
		}

		if transitionQueue {
			if err := g.db.EnsurePendingChangesTable(); err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]string{"transition_id": match.ID})
			if err := g.db.InsertPendingChange(issueKey, "transition", string(payload)); err != nil {
				failed++
				res := result{
					Key:    issueKey,
					Status: "failed",
					Error:  fmt.Sprintf("queueing: %v", err),
				}
				results = append(results, res)
				if !g.jsonOut {
					fmt.Fprintf(cmd.ErrOrStderr(), "  ✗ %s: %v\n", issueKey, err)
				}
				continue
			}
		} else {
			if err := g.jira.ExecuteTransition(cmd.Context(), issueKey, match.ID); err != nil {
				failed++
				res := result{
					Key:    issueKey,
					Status: "failed",
					Error:  fmt.Sprintf("transition failed: %v", err),
				}
				results = append(results, res)
				if !g.jsonOut {
					fmt.Fprintf(cmd.ErrOrStderr(), "  ✗ %s: %v\n", issueKey, err)
				}
				continue
			}

			// Refresh the local DB so status is immediately queryable.
			if apiIssue, fetchErr := g.jira.GetIssue(cmd.Context(), issueKey); fetchErr == nil {
				rawJSON, _ := json.Marshal(apiIssue)
				if fieldMap, fmErr := g.db.FieldMapByJiraID(); fmErr == nil {
					if dbIssue, extra, denormErr := synce.Denormalize(rawJSON, fieldMap); denormErr == nil {
						_ = g.db.UpsertIssue(dbIssue, extra)
					}
				}
			}
		}

		succeeded++
		res := result{
			Key:        issueKey,
			Status:     "ok",
			Transition: match.Name,
		}
		results = append(results, res)
		if !g.jsonOut {
			fmt.Fprintf(cmd.OutOrStdout(), "  ✓ %s: transitioned to %q\n", issueKey, match.Name)
		}
	}

	if g.jsonOut {
		fmt.Println(string(output.OK(map[string]interface{}{
			"count":     len(keys),
			"succeeded": succeeded,
			"failed":    failed,
			"results":   results,
		})))
		return nil
	}

	fmt.Printf("%d succeeded, %d failed\n", succeeded, failed)
	return nil
}

func init() {
	transitionCmd.Flags().Bool("list", false, "list available transitions")
	transitionCmd.Flags().BoolVarP(&transitionQueue, "queue", "q", false, "Queue change locally instead of pushing to Jira immediately")
	transitionCmd.Flags().StringVar(&transitionQuery, "query", "", "SQL query returning a 'key' column to bulk-transition")
	rootCmd.AddCommand(transitionCmd)
}
