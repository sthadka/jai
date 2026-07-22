package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/output"
)

var (
	setAddValues    []string
	setRemoveValues []string
)

var setCmd = &cobra.Command{
	Use:   "set <key> <field> [value]",
	Short: "Set a field value on a Jira issue (queued locally until 'jai push')",
	Long: `Set a field value on a Jira issue (queued locally until 'jai push').

For scalar fields:
  jai set ROX-123 priority High

For array fields (labels, components, fixVersions):
  jai set ROX-123 --add labels rit-escalated
  jai set ROX-123 --remove labels old-label`,
	Args: cobra.RangeArgs(2, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		issueKey, fieldName := args[0], args[1]
		hasAdd := len(setAddValues) > 0
		hasRemove := len(setRemoveValues) > 0
		hasScalarValue := len(args) == 3

		if (hasAdd || hasRemove) && hasScalarValue {
			msg := "cannot combine --add/--remove with a positional value"
			if g.jsonOut {
				fmt.Println(string(output.Err("ValidationError", msg)))
				return nil
			}
			return fmt.Errorf("%s", msg)
		}
		if !hasAdd && !hasRemove && !hasScalarValue {
			msg := "provide a value or use --add/--remove for array fields"
			if g.jsonOut {
				fmt.Println(string(output.Err("ValidationError", msg)))
				return nil
			}
			return fmt.Errorf("%s", msg)
		}

		if err := g.db.EnsurePendingChangesTable(); err != nil {
			return err
		}

		fieldMap, err := g.db.FieldMapByJiraID()
		if err != nil {
			return err
		}

		var jiraID string
		var fieldType string
		for id, f := range fieldMap {
			if f.Name == fieldName {
				jiraID = id
				fieldType = f.Type
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

		if (hasAdd || hasRemove) && fieldType != "array" {
			msg := fmt.Sprintf("%s is not an array field", fieldName)
			if g.jsonOut {
				fmt.Println(string(output.Err("ValidationError", msg)))
				return nil
			}
			return fmt.Errorf("%s", msg)
		}

		if hasAdd || hasRemove {
			return setArrayField(cmd, issueKey, fieldName, jiraID)
		}
		return setScalarField(cmd, issueKey, fieldName, jiraID, args[2])
	},
}

func setScalarField(cmd *cobra.Command, issueKey, fieldName, jiraID, value string) error {
	payload, _ := json.Marshal(map[string]string{"field": jiraID, "value": value})
	if err := g.db.InsertPendingChange(issueKey, "set_field", string(payload)); err != nil {
		return err
	}

	_, err := g.db.Exec(
		fmt.Sprintf("UPDATE issues SET %s = ?, synced_at = datetime('now') WHERE key = ?", fieldName),
		value, issueKey,
	)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: local update failed: %v\n", err)
	}

	if g.jsonOut {
		fmt.Println(string(output.OK(map[string]string{
			"issue_key": issueKey,
			"field":     fieldName,
			"value":     value,
			"status":    "pending",
		})))
		return nil
	}
	fmt.Printf("%s: %s → %q (pending sync)\n", issueKey, fieldName, value)
	return nil
}

func setArrayField(cmd *cobra.Command, issueKey, fieldName, jiraID string) error {
	for _, v := range setAddValues {
		payload, _ := json.Marshal(map[string]string{"field": jiraID, "op": "add", "value": v})
		if err := g.db.InsertPendingChange(issueKey, "update_field", string(payload)); err != nil {
			return err
		}
	}
	for _, v := range setRemoveValues {
		payload, _ := json.Marshal(map[string]string{"field": jiraID, "op": "remove", "value": v})
		if err := g.db.InsertPendingChange(issueKey, "update_field", string(payload)); err != nil {
			return err
		}
	}

	current := readCurrentArray(issueKey, fieldName)
	updated := applyArrayOps(current, setAddValues, setRemoveValues)
	newJSON, _ := json.Marshal(updated)
	_, err := g.db.Exec(
		fmt.Sprintf("UPDATE issues SET %s = ?, synced_at = datetime('now') WHERE key = ?", fieldName),
		string(newJSON), issueKey,
	)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: local update failed: %v\n", err)
	}

	if g.jsonOut {
		fmt.Println(string(output.OK(map[string]interface{}{
			"issue_key": issueKey,
			"field":     fieldName,
			"added":     setAddValues,
			"removed":   setRemoveValues,
			"status":    "pending",
		})))
		return nil
	}
	if len(setAddValues) > 0 {
		fmt.Printf("%s: %s += %v (pending sync)\n", issueKey, fieldName, setAddValues)
	}
	if len(setRemoveValues) > 0 {
		fmt.Printf("%s: %s -= %v (pending sync)\n", issueKey, fieldName, setRemoveValues)
	}
	return nil
}

func readCurrentArray(issueKey, fieldName string) []string {
	var raw sql.NullString
	_ = g.db.QueryRow(
		fmt.Sprintf("SELECT %s FROM issues WHERE key = ?", fieldName),
		issueKey,
	).Scan(&raw)
	if !raw.Valid || raw.String == "" {
		return nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(raw.String), &arr); err != nil {
		return []string{raw.String}
	}
	return arr
}

func applyArrayOps(current, adds, removes []string) []string {
	removeSet := make(map[string]bool, len(removes))
	for _, v := range removes {
		removeSet[v] = true
	}
	var result []string
	for _, v := range current {
		if !removeSet[v] {
			result = append(result, v)
		}
	}
	existSet := make(map[string]bool, len(result))
	for _, v := range result {
		existSet[v] = true
	}
	for _, v := range adds {
		if !existSet[v] {
			result = append(result, v)
			existSet[v] = true
		}
	}
	return result
}

func init() {
	setCmd.Flags().StringArrayVar(&setAddValues, "add", nil, "Add a value to an array field (repeatable)")
	setCmd.Flags().StringArrayVar(&setRemoveValues, "remove", nil, "Remove a value from an array field (repeatable)")
	rootCmd.AddCommand(setCmd)
}
