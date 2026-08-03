package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/output"
)

var commentQueue bool

var commentCmd = &cobra.Command{
	Use:   "comment <key> <text>",
	Short: "Add a comment to a Jira issue",
	Long: `Add a comment to a Jira issue.

By default, the comment is pushed to Jira immediately. Use --queue to
defer the push until 'jai push'.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		issueKey, text := args[0], args[1]

		status := "synced"
		var commentID string

		if commentQueue {
			if err := g.db.EnsurePendingChangesTable(); err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]string{"body": text})
			if err := g.db.InsertPendingChange(issueKey, "add_comment", string(payload)); err != nil {
				return err
			}
			commentID = fmt.Sprintf("local_%d", time.Now().UnixNano())
			status = "queued"
		} else {
			id, err := g.jira.AddComment(cmd.Context(), issueKey, text)
			if err != nil {
				msg := fmt.Sprintf("adding comment to %s: %v", issueKey, err)
				if g.jsonOut {
					fmt.Println(string(output.Err("JiraError", msg)))
					return nil
				}
				return fmt.Errorf("%s", msg)
			}
			commentID = id
		}

		now := time.Now().UTC().Format(time.RFC3339)
		localComment := &db.Comment{
			ID:       commentID,
			IssueKey: issueKey,
			Author:   g.cfg.Me,
			Body:     text,
			Created:  now,
			Updated:  now,
		}
		_ = g.db.UpsertComment(localComment)
		_ = g.db.UpdateIssueCommentsText(issueKey)

		if g.jsonOut {
			fmt.Println(string(output.OK(map[string]string{
				"issue_key": issueKey,
				"status":    status,
			})))
			return nil
		}

		if status == "queued" {
			fmt.Printf("%s: comment added (queued)\n", issueKey)
		} else {
			fmt.Printf("%s: comment added ✓\n", issueKey)
		}
		return nil
	},
}

func init() {
	commentCmd.Flags().BoolVarP(&commentQueue, "queue", "q", false, "Queue change locally instead of pushing to Jira immediately")
	rootCmd.AddCommand(commentCmd)
}
