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

var createFlags struct {
	issueType   string
	summary     string
	description string
	parent      string
	labels      []string
	components  []string
	priority    string
	assignee    string
	fixVersion  string
	dueDate     string
	field       []string // key=value pairs for arbitrary fields
}

var createCmd = &cobra.Command{
	Use:   "create <project>",
	Short: "Create a new Jira issue",
	Long: `Create a new Jira issue directly via the Jira API.

The new issue key is returned immediately. The issue is also inserted
into the local database so it is queryable right away.

Examples:
  jai create ROX --type Bug --summary "Login fails"
  jai create ROX --type Story --summary "Add search" --parent ROX-100 --labels backend,urgent
  jai create ROX --type Task --summary "Fix tests" --assignee user@example.com --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		project := strings.ToUpper(args[0])

		if createFlags.summary == "" {
			msg := "summary is required (use --summary)"
			if g.jsonOut {
				fmt.Println(string(output.Err("ValidationError", msg)))
				return nil
			}
			return fmt.Errorf("%s", msg)
		}
		if createFlags.issueType == "" {
			msg := "issue type is required (use --type)"
			if g.jsonOut {
				fmt.Println(string(output.Err("ValidationError", msg)))
				return nil
			}
			return fmt.Errorf("%s", msg)
		}

		fields := map[string]interface{}{
			"project":   map[string]string{"key": project},
			"summary":   createFlags.summary,
			"issuetype": map[string]string{"name": createFlags.issueType},
		}

		if createFlags.description != "" {
			fields["description"] = map[string]interface{}{
				"type":    "doc",
				"version": 1,
				"content": []map[string]interface{}{
					{
						"type": "paragraph",
						"content": []map[string]interface{}{
							{"type": "text", "text": createFlags.description},
						},
					},
				},
			}
		}

		if createFlags.parent != "" {
			fields["parent"] = map[string]string{"key": createFlags.parent}
		}

		if len(createFlags.labels) > 0 {
			expanded := expandCSV(createFlags.labels)
			fields["labels"] = expanded
		}

		if len(createFlags.components) > 0 {
			expanded := expandCSV(createFlags.components)
			comps := make([]map[string]string, len(expanded))
			for i, c := range expanded {
				comps[i] = map[string]string{"name": c}
			}
			fields["components"] = comps
		}

		if createFlags.priority != "" {
			fields["priority"] = map[string]string{"name": createFlags.priority}
		}

		if createFlags.assignee != "" {
			fields["assignee"] = map[string]string{"accountId": createFlags.assignee}
		}

		if createFlags.fixVersion != "" {
			fields["fixVersions"] = []map[string]string{{"name": createFlags.fixVersion}}
		}

		if createFlags.dueDate != "" {
			fields["duedate"] = createFlags.dueDate
		}

		// Apply arbitrary --field key=value pairs via the field map.
		if len(createFlags.field) > 0 {
			fieldMap, err := g.db.FieldMapByJiraID()
			if err != nil {
				return err
			}
			for _, kv := range createFlags.field {
				parts := strings.SplitN(kv, "=", 2)
				if len(parts) != 2 {
					msg := fmt.Sprintf("invalid --field format %q (expected key=value)", kv)
					if g.jsonOut {
						fmt.Println(string(output.Err("ValidationError", msg)))
						return nil
					}
					return fmt.Errorf("%s", msg)
				}
				name, value := parts[0], parts[1]

				jiraID := resolveFieldID(fieldMap, name)
				if jiraID == "" {
					msg := fmt.Sprintf("unknown field: %s (run 'jai fields' to see available fields)", name)
					if g.jsonOut {
						fmt.Println(string(output.Err("ValidationError", msg)))
						return nil
					}
					return fmt.Errorf("%s", msg)
				}

				var parsed interface{}
				if err := json.Unmarshal([]byte(value), &parsed); err != nil {
					parsed = value
				}
				fields[jiraID] = parsed
			}
		}

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
				"project": project,
				"status":  "created",
			})))
			return nil
		}

		fmt.Printf("✓ Created %s: %s\n", resp.Key, createFlags.summary)
		return nil
	},
}

// resolveFieldID maps a display name or jira_id to the canonical jira_id.
func resolveFieldID(fieldMap map[string]*db.FieldMapping, name string) string {
	if _, ok := fieldMap[name]; ok {
		return name
	}
	for id, f := range fieldMap {
		if f.Name == name || f.JiraName == name {
			return id
		}
	}
	return ""
}

// expandCSV splits comma-separated items within each slice element.
func expandCSV(items []string) []string {
	var out []string
	for _, item := range items {
		for _, s := range strings.Split(item, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func init() {
	rootCmd.AddCommand(createCmd)

	createCmd.Flags().StringVar(&createFlags.issueType, "type", "", "issue type (e.g. Bug, Story, Task, Epic)")
	createCmd.Flags().StringVar(&createFlags.summary, "summary", "", "issue summary/title")
	createCmd.Flags().StringVar(&createFlags.description, "description", "", "issue description")
	createCmd.Flags().StringVar(&createFlags.parent, "parent", "", "parent issue key (e.g. ROX-100)")
	createCmd.Flags().StringSliceVar(&createFlags.labels, "labels", nil, "comma-separated labels")
	createCmd.Flags().StringSliceVar(&createFlags.components, "components", nil, "comma-separated component names")
	createCmd.Flags().StringVar(&createFlags.priority, "priority", "", "priority name (e.g. High, Medium, Low)")
	createCmd.Flags().StringVar(&createFlags.assignee, "assignee", "", "assignee account ID or email")
	createCmd.Flags().StringVar(&createFlags.fixVersion, "fix-version", "", "fix version name")
	createCmd.Flags().StringVar(&createFlags.dueDate, "due-date", "", "due date (YYYY-MM-DD)")
	createCmd.Flags().StringArrayVar(&createFlags.field, "field", nil, "arbitrary field as key=value (repeatable)")
}
