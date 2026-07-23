package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/output"
	synce "github.com/sthadka/jai/internal/sync"
)

var cloneFlags struct {
	summary string
	set     []string // key=value pairs
	replace []string // find:replace pairs
}

var cloneCmd = &cobra.Command{
	Use:   "clone <key>",
	Short: "Clone a Jira issue with optional field overrides",
	Long: `Clone a Jira issue by reading it from the local database, applying
optional overrides, and creating a new issue via the Jira API.

The new issue key is returned immediately. The cloned issue is also
inserted into the local database so it is queryable right away.

Examples:
  jai clone PROJ-123
  jai clone PROJ-123 --summary "Copy of original"
  jai clone PROJ-123 --set priority=High --set labels=bug,urgent
  jai clone PROJ-123 --replace "old text:new text"
  jai clone PROJ-123 --summary "New title" --set assignee=user@example.com --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		sourceKey := strings.ToUpper(args[0])

		// Read source issue from local DB.
		issue, err := g.db.GetIssue(sourceKey)
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("QueryError", err.Error())))
				return nil
			}
			return fmt.Errorf("reading issue: %w", err)
		}
		if issue == nil {
			msg := fmt.Sprintf("issue %s not found in local database (try: jai sync)", sourceKey)
			if g.jsonOut {
				fmt.Println(string(output.Err("NotFoundError", msg)))
				return nil
			}
			return fmt.Errorf("%s", msg)
		}

		// Extract fields from raw_json.
		rawJSON, ok := issue["raw_json"].(string)
		if !ok || rawJSON == "" {
			msg := fmt.Sprintf("issue %s has no raw_json data (try: jai sync)", sourceKey)
			if g.jsonOut {
				fmt.Println(string(output.Err("DataError", msg)))
				return nil
			}
			return fmt.Errorf("%s", msg)
		}

		fieldMap, err := g.db.FieldMapByJiraID()
		if err != nil {
			return err
		}

		fields, project, err := extractCloneFields(rawJSON, fieldMap)
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("DataError", err.Error())))
				return nil
			}
			return fmt.Errorf("extracting fields: %w", err)
		}

		// Apply --summary override.
		if cloneFlags.summary != "" {
			fields["summary"] = cloneFlags.summary
		}

		// Apply --replace substitutions to summary and description.
		for _, r := range cloneFlags.replace {
			find, repl, ok := parseReplace(r)
			if !ok {
				msg := fmt.Sprintf("invalid --replace format %q (expected find:replace)", r)
				if g.jsonOut {
					fmt.Println(string(output.Err("ValidationError", msg)))
					return nil
				}
				return fmt.Errorf("%s", msg)
			}
			if s, ok := fields["summary"].(string); ok {
				fields["summary"] = strings.ReplaceAll(s, find, repl)
			}
			if desc, ok := fields["description"].(map[string]interface{}); ok {
				replaceInADF(desc, find, repl)
			}
		}

		// Apply --set field=value overrides (reuse field resolution from create).
		if len(cloneFlags.set) > 0 {
			for _, kv := range cloneFlags.set {
				parts := strings.SplitN(kv, "=", 2)
				if len(parts) != 2 {
					msg := fmt.Sprintf("invalid --set format %q (expected key=value)", kv)
					if g.jsonOut {
						fmt.Println(string(output.Err("ValidationError", msg)))
						return nil
					}
					return fmt.Errorf("%s", msg)
				}
				name, value := parts[0], parts[1]
				resolveAccountID := func(v string) (string, error) {
					return g.jira.ResolveAccountID(cmd.Context(), v)
				}
				if err := applyFieldOverride(fields, fieldMap, name, value, resolveAccountID); err != nil {
					if g.jsonOut {
						fmt.Println(string(output.Err("ValidationError", err.Error())))
						return nil
					}
					return err
				}
			}
		}

		// Create the issue via Jira API.
		resp, err := g.jira.CreateIssue(cmd.Context(), fields)
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("JiraError", err.Error())))
				return nil
			}
			return fmt.Errorf("creating issue: %w", err)
		}

		// Fetch the full issue and insert into local DB.
		apiIssue, fetchErr := g.jira.GetIssue(cmd.Context(), resp.Key)
		if fetchErr == nil {
			rawJSON, _ := json.Marshal(apiIssue)
			fieldMap, fmErr := g.db.FieldMapByJiraID()
			if fmErr == nil {
				dbIssue, extra, denormErr := synce.Denormalize(rawJSON, fieldMap)
				if denormErr == nil {
					_ = g.db.UpsertIssue(dbIssue, extra)
				}
			}
		}

		if g.jsonOut {
			fmt.Println(string(output.OK(map[string]string{
				"key":     resp.Key,
				"id":      resp.ID,
				"source":  sourceKey,
				"project": project,
				"status":  "created",
			})))
			return nil
		}

		summary := fields["summary"]
		fmt.Printf("Created %s (cloned from %s): %s\n", resp.Key, sourceKey, summary)
		return nil
	},
}

// nonClonableFields lists Jira field names that are computed/managed by Jira
// and cannot be round-tripped through issue creation. Their GET representation
// (e.g. Rank's lexoRank string) doesn't match what the create API accepts.
var nonClonableFields = map[string]bool{
	"Rank": true,
}

// extractCloneFields parses raw_json and extracts fields suitable for creating
// a new issue. Returns the fields map and the project key.
func extractCloneFields(rawJSON string, fieldMap map[string]*db.FieldMapping) (map[string]interface{}, string, error) {
	var apiIssue struct {
		Fields json.RawMessage `json:"fields"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &apiIssue); err != nil {
		return nil, "", fmt.Errorf("parsing raw_json: %w", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(apiIssue.Fields, &raw); err != nil {
		return nil, "", fmt.Errorf("parsing fields: %w", err)
	}

	fields := make(map[string]interface{})
	project := ""

	// Project (required).
	if v, ok := raw["project"]; ok {
		var proj map[string]interface{}
		if json.Unmarshal(v, &proj) == nil && proj["key"] != nil {
			project = fmt.Sprint(proj["key"])
			fields["project"] = map[string]string{"key": project}
		}
	}

	// Summary (required).
	if v, ok := raw["summary"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			fields["summary"] = s
		}
	}

	// Issue type (required).
	if v, ok := raw["issuetype"]; ok {
		var it map[string]interface{}
		if json.Unmarshal(v, &it) == nil {
			if name, ok := it["name"]; ok {
				fields["issuetype"] = map[string]string{"name": fmt.Sprint(name)}
			}
		}
	}

	// Description (ADF document, passed through as-is).
	if v, ok := raw["description"]; ok {
		var desc map[string]interface{}
		if json.Unmarshal(v, &desc) == nil {
			fields["description"] = desc
		}
	}

	// Priority.
	if v, ok := raw["priority"]; ok {
		var p map[string]interface{}
		if json.Unmarshal(v, &p) == nil && p["name"] != nil {
			fields["priority"] = map[string]string{"name": fmt.Sprint(p["name"])}
		}
	}

	// Labels.
	if v, ok := raw["labels"]; ok {
		var labels []string
		if json.Unmarshal(v, &labels) == nil && len(labels) > 0 {
			fields["labels"] = labels
		}
	}

	// Components.
	if v, ok := raw["components"]; ok {
		var comps []map[string]interface{}
		if json.Unmarshal(v, &comps) == nil && len(comps) > 0 {
			names := make([]map[string]string, len(comps))
			for i, c := range comps {
				names[i] = map[string]string{"name": fmt.Sprint(c["name"])}
			}
			fields["components"] = names
		}
	}

	// Assignee.
	if v, ok := raw["assignee"]; ok {
		var a map[string]interface{}
		if json.Unmarshal(v, &a) == nil && a["accountId"] != nil {
			fields["assignee"] = map[string]string{"accountId": fmt.Sprint(a["accountId"])}
		}
	}

	// Parent (subtask/child issue).
	if v, ok := raw["parent"]; ok {
		var p map[string]interface{}
		if json.Unmarshal(v, &p) == nil && p["key"] != nil {
			fields["parent"] = map[string]string{"key": fmt.Sprint(p["key"])}
		}
	}

	// Fix versions.
	if v, ok := raw["fixVersions"]; ok {
		var versions []map[string]interface{}
		if json.Unmarshal(v, &versions) == nil && len(versions) > 0 {
			fv := make([]map[string]string, len(versions))
			for i, ver := range versions {
				fv[i] = map[string]string{"name": fmt.Sprint(ver["name"])}
			}
			fields["fixVersions"] = fv
		}
	}

	// Story points (commonly customfield_10016 but handled via custom fields below).
	// Epic link is typically a custom field as well.

	// Custom fields: copy any customfield_* values as-is, skipping fields
	// that Jira manages internally and can't accept back on create (e.g. Rank).
	for key, v := range raw {
		if strings.HasPrefix(key, "customfield_") {
			if fm, ok := fieldMap[key]; ok && nonClonableFields[fm.JiraName] {
				continue
			}
			var val interface{}
			if json.Unmarshal(v, &val) == nil && val != nil {
				fields[key] = val
			}
		}
	}

	return fields, project, nil
}

// applyFieldOverride applies a single key=value override to the fields map,
// resolving the field name through the field map (same as jai create).
// resolveAccountID resolves an assignee identifier (email or account ID) to a
// Jira account ID; pass nil to skip resolution (e.g. in tests) and use the
// value as-is.
func applyFieldOverride(fields map[string]interface{}, fieldMap map[string]*db.FieldMapping, name, value string, resolveAccountID func(string) (string, error)) error {
	// Handle well-known fields by their common names.
	switch strings.ToLower(name) {
	case "summary":
		fields["summary"] = value
		return nil
	case "priority":
		fields["priority"] = map[string]string{"name": value}
		return nil
	case "assignee":
		accountID := value
		if resolveAccountID != nil {
			resolved, err := resolveAccountID(value)
			if err != nil {
				return fmt.Errorf("resolving assignee: %w", err)
			}
			accountID = resolved
		}
		fields["assignee"] = map[string]string{"accountId": accountID}
		return nil
	case "labels":
		fields["labels"] = expandCSV([]string{value})
		return nil
	case "components":
		expanded := expandCSV([]string{value})
		comps := make([]map[string]string, len(expanded))
		for i, c := range expanded {
			comps[i] = map[string]string{"name": c}
		}
		fields["components"] = comps
		return nil
	case "parent":
		fields["parent"] = map[string]string{"key": value}
		return nil
	case "fix-version", "fixversion":
		fields["fixVersions"] = []map[string]string{{"name": value}}
		return nil
	case "type", "issuetype":
		fields["issuetype"] = map[string]string{"name": value}
		return nil
	}

	// Try the field map for custom/dynamic fields.
	jiraID := resolveFieldID(fieldMap, name)
	if jiraID == "" {
		return fmt.Errorf("unknown field: %s (run 'jai fields' to see available fields)", name)
	}

	var parsed interface{}
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		parsed = value
	}
	fields[jiraID] = parsed
	return nil
}

// parseReplace splits a "find:replace" string into its two parts.
func parseReplace(s string) (find, replace string, ok bool) {
	idx := strings.Index(s, ":")
	if idx < 0 {
		return "", "", false
	}
	return s[:idx], s[idx+1:], true
}

// replaceInADF recursively replaces text in an ADF document.
func replaceInADF(node map[string]interface{}, find, replace string) {
	if t, ok := node["type"].(string); ok && t == "text" {
		if text, ok := node["text"].(string); ok {
			node["text"] = strings.ReplaceAll(text, find, replace)
		}
	}
	if content, ok := node["content"].([]interface{}); ok {
		for _, child := range content {
			if childMap, ok := child.(map[string]interface{}); ok {
				replaceInADF(childMap, find, replace)
			}
		}
	}
}

func init() {
	rootCmd.AddCommand(cloneCmd)

	cloneCmd.Flags().StringVar(&cloneFlags.summary, "summary", "", "override the summary/title")
	cloneCmd.Flags().StringArrayVar(&cloneFlags.set, "set", nil, "set a field value as key=value (repeatable)")
	cloneCmd.Flags().StringArrayVar(&cloneFlags.replace, "replace", nil, "find and replace in summary/description as find:replace (repeatable)")
}
