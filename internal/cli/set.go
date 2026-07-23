package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/output"
)

var (
	setAddValues    []string
	setRemoveValues []string
	setQuery        string
)

var setCmd = &cobra.Command{
	Use:   "set [key] <field> [value]",
	Short: "Set a field value on one or more Jira issues (queued locally until 'jai push')",
	Long: `Set a field value on one or more Jira issues (queued locally until 'jai push').

For scalar fields:
  jai set ROX-123 priority High

For array fields (labels, components, fixVersions):
  jai set ROX-123 labels --add rit-escalated
  jai set ROX-123 labels --remove old-label

Bulk operations with comma-separated keys:
  jai set ROX-1,ROX-2,ROX-3 priority Major

Bulk operations with a SQL query:
  jai set --query "SELECT key FROM issues WHERE type='Bug'" priority Major`,
	Args: func(cmd *cobra.Command, args []string) error {
		if setQuery != "" {
			if len(args) < 1 || len(args) > 2 {
				return fmt.Errorf("with --query, provide <field> [value] (got %d args)", len(args))
			}
			return nil
		}
		return cobra.RangeArgs(2, 3)(cmd, args)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		var keys []string
		var fieldName string
		var scalarValue string
		hasAdd := len(setAddValues) > 0
		hasRemove := len(setRemoveValues) > 0

		if setQuery != "" {
			fieldName = args[0]
			if len(args) == 2 {
				scalarValue = args[1]
			}
			results, err := g.query.Execute(setQuery)
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
			fieldName = args[1]
			if len(args) == 3 {
				scalarValue = args[2]
			}
		}

		hasScalarValue := scalarValue != ""

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

		if len(keys) > 1 {
			return setBulk(cmd, keys, fieldName, jiraID, scalarValue, fieldType)
		}

		issueKey := keys[0]
		if hasAdd || hasRemove {
			return setArrayField(cmd, issueKey, fieldName, jiraID)
		}
		return setScalarField(cmd, issueKey, fieldName, jiraID, scalarValue, fieldType)
	},
}

// wrapScalarFieldValue converts a scalar string value into the object shape
// Jira's write API requires for reference fields (priority, assignee, reporter).
// ok is false for fields that accept a bare string/number/date as-is, in which
// case the caller should use the raw value. resolveAccountID resolves an
// assignee/reporter identifier to an account ID; pass nil to use the raw value
// unresolved (e.g. in tests).
func wrapScalarFieldValue(jiraID, value string, resolveAccountID func(string) (string, error)) (result interface{}, ok bool, err error) {
	switch jiraID {
	case "priority":
		return map[string]string{"name": value}, true, nil
	case "assignee", "reporter":
		accountID := value
		if resolveAccountID != nil {
			resolved, rerr := resolveAccountID(value)
			if rerr != nil {
				return nil, true, rerr
			}
			accountID = resolved
		}
		return map[string]string{"accountId": accountID}, true, nil
	}
	return nil, false, nil
}

// wrapArrayItemValue converts a single array-item string into the object shape
// Jira's write API requires for reference-array fields (components, fixVersions).
// ok is false for plain string arrays (e.g. labels), where the raw string is used.
func wrapArrayItemValue(jiraID, value string) (result interface{}, ok bool) {
	switch jiraID {
	case "components", "fixVersions":
		return map[string]string{"name": value}, true
	}
	return nil, false
}

func setScalarField(cmd *cobra.Command, issueKey, fieldName, jiraID, value, fieldType string) error {
	var payloadVal interface{} = value
	localVal := value

	if fieldType == "array" {
		items := parseArrayValue(value)
		wrapped := make([]interface{}, len(items))
		for i, item := range items {
			if w, ok := wrapArrayItemValue(jiraID, item); ok {
				wrapped[i] = w
			} else {
				wrapped[i] = item
			}
		}
		payloadVal = wrapped
		j, _ := json.Marshal(items)
		localVal = string(j)
	} else {
		resolveAccountID := func(v string) (string, error) {
			return g.jira.ResolveAccountID(cmd.Context(), v)
		}
		if wrapped, ok, err := wrapScalarFieldValue(jiraID, value, resolveAccountID); ok {
			if err != nil {
				msg := fmt.Sprintf("resolving %s: %v", fieldName, err)
				if g.jsonOut {
					fmt.Println(string(output.Err("JiraError", msg)))
					return nil
				}
				return fmt.Errorf("%s", msg)
			}
			payloadVal = wrapped
		}
	}

	payload, _ := json.Marshal(map[string]interface{}{"field": jiraID, "value": payloadVal})
	if err := g.db.InsertPendingChange(issueKey, "set_field", string(payload)); err != nil {
		return err
	}

	_, err := g.db.Exec(
		fmt.Sprintf("UPDATE issues SET %s = ?, synced_at = datetime('now') WHERE key = ?", fieldName),
		localVal, issueKey,
	)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: local update failed: %v\n", err)
	}

	if g.jsonOut {
		fmt.Println(string(output.OK(map[string]interface{}{
			"issue_key": issueKey,
			"field":     fieldName,
			"value":     payloadVal,
			"status":    "pending",
		})))
		return nil
	}
	fmt.Printf("%s: %s → %q (pending sync)\n", issueKey, fieldName, localVal)
	return nil
}

func parseArrayValue(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			result = append(result, v)
		}
	}
	return result
}

func expandKeys(keyArg string) []string {
	parts := strings.Split(keyArg, ",")
	keys := make([]string, 0, len(parts))
	for _, p := range parts {
		if k := strings.TrimSpace(p); k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}

func extractKeys(columns []string, rows [][]interface{}) ([]string, error) {
	keyCol := -1
	for i, col := range columns {
		if strings.EqualFold(col, "key") {
			keyCol = i
			break
		}
	}
	if keyCol == -1 {
		return nil, fmt.Errorf("query must return a 'key' column")
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		if keyCol < len(row) && row[keyCol] != nil {
			keys = append(keys, fmt.Sprint(row[keyCol]))
		}
	}
	return keys, nil
}

func setBulk(cmd *cobra.Command, keys []string, fieldName, jiraID, value, fieldType string) error {
	hasAdd := len(setAddValues) > 0
	hasRemove := len(setRemoveValues) > 0

	var scalarPayloadVal interface{}
	var scalarLocalVal string
	if !hasAdd && !hasRemove {
		scalarPayloadVal = value
		scalarLocalVal = value
		if fieldType == "array" {
			items := parseArrayValue(value)
			wrapped := make([]interface{}, len(items))
			for i, item := range items {
				if w, ok := wrapArrayItemValue(jiraID, item); ok {
					wrapped[i] = w
				} else {
					wrapped[i] = item
				}
			}
			scalarPayloadVal = wrapped
			j, _ := json.Marshal(items)
			scalarLocalVal = string(j)
		} else {
			resolveAccountID := func(v string) (string, error) {
				return g.jira.ResolveAccountID(cmd.Context(), v)
			}
			if wrapped, ok, err := wrapScalarFieldValue(jiraID, value, resolveAccountID); ok {
				if err != nil {
					msg := fmt.Sprintf("resolving %s: %v", fieldName, err)
					if g.jsonOut {
						fmt.Println(string(output.Err("JiraError", msg)))
						return nil
					}
					return fmt.Errorf("%s", msg)
				}
				scalarPayloadVal = wrapped
			}
		}
	}

	for _, key := range keys {
		if hasAdd || hasRemove {
			for _, v := range setAddValues {
				var val interface{} = v
				if w, ok := wrapArrayItemValue(jiraID, v); ok {
					val = w
				}
				payload, _ := json.Marshal(map[string]interface{}{"field": jiraID, "op": "add", "value": val})
				if err := g.db.InsertPendingChange(key, "update_field", string(payload)); err != nil {
					return err
				}
			}
			for _, v := range setRemoveValues {
				var val interface{} = v
				if w, ok := wrapArrayItemValue(jiraID, v); ok {
					val = w
				}
				payload, _ := json.Marshal(map[string]interface{}{"field": jiraID, "op": "remove", "value": val})
				if err := g.db.InsertPendingChange(key, "update_field", string(payload)); err != nil {
					return err
				}
			}

			current := readCurrentArray(key, fieldName)
			updated := applyArrayOps(current, setAddValues, setRemoveValues)
			var keyLocalVal string
			if len(updated) > 0 {
				b, _ := json.Marshal(updated)
				keyLocalVal = string(b)
			}
			if _, err := g.db.Exec(
				fmt.Sprintf("UPDATE issues SET %s = ?, synced_at = datetime('now') WHERE key = ?", fieldName),
				keyLocalVal, key,
			); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: local update failed for %s: %v\n", key, err)
			}
		} else {
			payload, _ := json.Marshal(map[string]interface{}{"field": jiraID, "value": scalarPayloadVal})
			if err := g.db.InsertPendingChange(key, "set_field", string(payload)); err != nil {
				return err
			}
			if _, err := g.db.Exec(
				fmt.Sprintf("UPDATE issues SET %s = ?, synced_at = datetime('now') WHERE key = ?", fieldName),
				scalarLocalVal, key,
			); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: local update failed for %s: %v\n", key, err)
			}
		}
	}

	if g.jsonOut {
		fmt.Println(string(output.OK(map[string]interface{}{
			"count": len(keys),
			"keys":  keys,
		})))
		return nil
	}
	fmt.Printf("queued %d changes (pending sync)\n", len(keys))
	return nil
}

func setArrayField(cmd *cobra.Command, issueKey, fieldName, jiraID string) error {
	for _, v := range setAddValues {
		var val interface{} = v
		if w, ok := wrapArrayItemValue(jiraID, v); ok {
			val = w
		}
		payload, _ := json.Marshal(map[string]interface{}{"field": jiraID, "op": "add", "value": val})
		if err := g.db.InsertPendingChange(issueKey, "update_field", string(payload)); err != nil {
			return err
		}
	}
	for _, v := range setRemoveValues {
		var val interface{} = v
		if w, ok := wrapArrayItemValue(jiraID, v); ok {
			val = w
		}
		payload, _ := json.Marshal(map[string]interface{}{"field": jiraID, "op": "remove", "value": val})
		if err := g.db.InsertPendingChange(issueKey, "update_field", string(payload)); err != nil {
			return err
		}
	}

	current := readCurrentArray(issueKey, fieldName)
	updated := applyArrayOps(current, setAddValues, setRemoveValues)
	var localVal string
	if len(updated) > 0 {
		b, _ := json.Marshal(updated)
		localVal = string(b)
	}
	_, err := g.db.Exec(
		fmt.Sprintf("UPDATE issues SET %s = ?, synced_at = datetime('now') WHERE key = ?", fieldName),
		localVal, issueKey,
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
	setCmd.Flags().StringVar(&setQuery, "query", "", "SQL query returning a 'key' column to bulk-set")
	rootCmd.AddCommand(setCmd)
}
