package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/pkg/output"
	db "github.com/sthadka/jai/pkg/store"
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

		app := cliFrom(cmd)
		issueKey, text := args[0], args[1]

		status := "synced"
		var commentID string

		if commentQueue {
			if err := app.engine.Store.EnsurePendingChangesTable(); err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]string{"body": text})
			if err := app.engine.Store.InsertPendingChange(issueKey, "add_comment", string(payload)); err != nil {
				return err
			}
			commentID = fmt.Sprintf("local_%d", time.Now().UnixNano())
			status = "queued"
		} else {
			id, err := app.engine.Client.AddComment(cmd.Context(), issueKey, text)
			if err != nil {
				msg := fmt.Sprintf("adding comment to %s: %v", issueKey, err)
				if app.jsonOut {
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
			Author:   app.engine.Config.Me,
			Body:     text,
			Created:  now,
			Updated:  now,
		}
		_ = app.engine.Store.UpsertComment(localComment)
		_ = app.engine.Store.UpdateIssueCommentsText(issueKey)

		if app.jsonOut {
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
