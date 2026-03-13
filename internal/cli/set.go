package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/output"
)

var setCmd = &cobra.Command{
	Use:   "set <key> <field> <value>",
	Short: "Set a field value on a Jira issue (queued locally until 'jai push')",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		issueKey, fieldName, value := args[0], args[1], args[2]

		// Ensure pending_changes table exists.
		if err := g.db.EnsurePendingChangesTable(); err != nil {
			return err
		}

		// Look up the field to get its jira_id.
		fieldMap, err := g.db.FieldMapByJiraID()
		if err != nil {
			return err
		}

		var jiraID string
		for id, f := range fieldMap {
			if f.Name == fieldName {
				jiraID = id
				break
			}
		}
		if jiraID == "" {
			msg := fmt.Sprintf("unknown field: %s (run 'jai fields' to see available fields)", fieldName)
			if g.jsonOut {
				fmt.Println(string(output.Err("ValidationError", msg)))
				return nil
			}
			return fmt.Errorf("%s", msg)
		}

		// Queue the change.
		payload, _ := json.Marshal(map[string]string{"field": jiraID, "value": value})
		if err := g.db.InsertPendingChange(issueKey, "set_field", string(payload)); err != nil {
			return err
		}

		// Optimistic local update.
		_, err = g.db.Exec(
			fmt.Sprintf("UPDATE issues SET %s = ?, synced_at = datetime('now') WHERE key = ?", fieldName),
			value, issueKey,
		)
		if err != nil {
			// Non-fatal: the pending change is queued.
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: local update failed: %v\n", err)
		}

		msg := fmt.Sprintf("%s: %s → %q (pending sync)", issueKey, fieldName, value)
		if g.jsonOut {
			fmt.Println(string(output.OK(map[string]string{
				"issue_key": issueKey,
				"field":     fieldName,
				"value":     value,
				"status":    "pending",
			})))
			return nil
		}
		fmt.Println(msg)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(setCmd)
}
