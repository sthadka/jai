package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/jira"
	"github.com/sthadka/jai/internal/output"
	synce "github.com/sthadka/jai/internal/sync"
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
	Use:   "transition <key> [status]",
	Short: "Transition a Jira issue to a new status (pushed immediately)",
	Long:  "Move a Jira issue through its workflow. Transitions are pushed immediately, unlike field edits.",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		issueKey := args[0]
		listFlag, _ := cmd.Flags().GetBool("list")

		transitions, err := g.jira.GetTransitions(cmd.Context(), issueKey)
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("JiraError", fmt.Sprintf("fetching transitions: %v", err))))
				return nil
			}
			return fmt.Errorf("fetching transitions for %s: %w", issueKey, err)
		}

		if listFlag || len(args) == 1 {
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

		targetName := args[1]
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

		if err := g.db.EnsurePendingChangesTable(); err != nil {
			return err
		}

		payload, _ := json.Marshal(map[string]string{"transition_id": match.ID})
		if err := g.db.InsertPendingChange(issueKey, "transition", string(payload)); err != nil {
			return err
		}

		writer := synce.NewWriter(g.db, g.jira)
		results, err := writer.ProcessQueue(context.Background())
		if err != nil {
			return fmt.Errorf("pushing transition: %w", err)
		}

		for _, r := range results {
			if r.IssueKey == issueKey && r.Operation == "transition" && !r.Success {
				msg := fmt.Sprintf("transition failed: %v", r.Error)
				if g.jsonOut {
					fmt.Println(string(output.Err("JiraError", msg)))
					return nil
				}
				return fmt.Errorf("%s", msg)
			}
		}

		// Refresh the local DB from Jira so status (and any workflow side effects,
		// e.g. resolution) are immediately queryable instead of stale until next sync.
		if apiIssue, fetchErr := g.jira.GetIssue(cmd.Context(), issueKey); fetchErr == nil {
			rawJSON, _ := json.Marshal(apiIssue)
			if fieldMap, fmErr := g.db.FieldMapByJiraID(); fmErr == nil {
				if dbIssue, extra, denormErr := synce.Denormalize(rawJSON, fieldMap); denormErr == nil {
					_ = g.db.UpsertIssue(dbIssue, extra)
				}
			}
		}

		if g.jsonOut {
			fmt.Println(string(output.OK(map[string]string{
				"issue_key":     issueKey,
				"transition":    match.Name,
				"transition_id": match.ID,
				"status":        "pushed",
			})))
			return nil
		}

		fmt.Printf("%s: transitioned to %q\n", issueKey, match.Name)
		return nil
	},
}

func init() {
	transitionCmd.Flags().Bool("list", false, "list available transitions")
	rootCmd.AddCommand(transitionCmd)
}
