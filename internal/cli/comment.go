package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/syethadk/jai/internal/db"
	"github.com/syethadk/jai/internal/output"
)

var commentCmd = &cobra.Command{
	Use:   "comment <key> <text>",
	Short: "Add a comment to a Jira issue (queued locally until 'jai push')",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		issueKey, text := args[0], args[1]

		// Ensure pending_changes table exists.
		if err := g.db.EnsurePendingChangesTable(); err != nil {
			return err
		}

		// Queue the change.
		payload, _ := json.Marshal(map[string]string{"body": text})
		if err := g.db.InsertPendingChange(issueKey, "add_comment", string(payload)); err != nil {
			return err
		}

		// Also insert into local comments table immediately.
		now := time.Now().UTC().Format(time.RFC3339)
		localComment := &db.Comment{
			ID:       fmt.Sprintf("local_%d", time.Now().UnixNano()),
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
				"status":    "pending",
			})))
			return nil
		}

		fmt.Printf("%s: comment added (pending sync)\n", issueKey)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(commentCmd)
}
