package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/jira"
	"github.com/sthadka/jai/internal/output"
)

var getShowComments bool

var getCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Fetch a single issue from the local database",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]

		results, err := g.query.Execute("SELECT * FROM issues WHERE key = ?", key)
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("QueryError", err.Error())))
				return nil
			}
			return err
		}

		if len(results.Rows) == 0 {
			issue, apiErr := g.jira.GetIssue(cmd.Context(), key)
			if apiErr != nil {
				msg := fmt.Sprintf("issue %s not found in local database (try: jai sync)", key)
				if g.jsonOut {
					fmt.Println(string(output.Err("NotFoundError", msg)))
					return nil
				}
				return fmt.Errorf("%s", msg)
			}
			var f jira.IssueFields
			_ = json.Unmarshal(issue.Fields, &f)
			fields := issueFieldsToMap(issue.Key, &f)
			if g.fields != "" {
				fields = output.FilterFields(fields, output.ParseFields(g.fields))
			}
			var apiComments []map[string]interface{}
			if getShowComments && f.Comment != nil {
				apiComments = apiCommentsToMaps(f.Comment.Comments)
			}
			if g.jsonOut {
				if getShowComments {
					fields["comments"] = apiComments
				}
				fmt.Println(string(output.OK(fields)))
				return nil
			}
			fmt.Fprintln(os.Stderr, "(live from Jira API — not in local database)")
			printMarkdownDoc(fields)
			if getShowComments {
				printCommentsSection(apiComments)
			}
			return nil
		}

		data := make(map[string]interface{}, len(results.Columns))
		for i, col := range results.Columns {
			data[col] = results.Rows[0][i]
		}
		if g.fields != "" {
			data = output.FilterFields(data, output.ParseFields(g.fields))
		}
		var dbComments []map[string]interface{}
		if getShowComments {
			if cs, err := g.db.GetComments(key); err == nil {
				dbComments = dbCommentsToMaps(cs)
			}
		}
		if g.jsonOut {
			if getShowComments {
				data["comments"] = dbComments
			}
			fmt.Println(string(output.OK(data)))
			return nil
		}
		if rawJSON := output.ValueStr(data["raw_json"]); rawJSON != "" {
			var wrapper struct {
				Fields struct {
					Description json.RawMessage `json:"description"`
				} `json:"fields"`
			}
			if err := json.Unmarshal([]byte(rawJSON), &wrapper); err == nil {
				if md := jira.ADFToMarkdown(wrapper.Fields.Description); md != "" {
					data["description"] = md
				}
			}
		}
		printMarkdownDoc(data)
		if getShowComments {
			printCommentsSection(dbComments)
		}
		return nil
	},
}

// frontMatterEntries defines the curated, ordered fields for YAML front matter.
// field is the map key; yamlKey is the name written to the front matter.
var frontMatterEntries = []struct{ field, yamlKey string }{
	{"key", "key"},
	{"summary", "summary"},
	{"status", "status"},
	{"type", "type"},
	{"size", "size"},
	{"priority", "priority"},
	{"assignee", "assignee"},
	{"reporter", "reporter"},
	{"fix_version", "fix_version"},
	{"labels", "labels"},
	{"components", "components"},
	{"parent_key", "parent"},
	{"team", "team"},
	{"product_manager", "product_manager"},
	{"activity_type", "activity_type"},
	{"release_note_text", "release_note"},
	{"release_note_type", "release_note_type"},
	{"release_type", "release_type"},
	{"product_documentation_required", "product_docs"},
	{"due_date", "due"},
	{"target_version", "target_version"},
	{"resolution", "resolution"},
	{"created", "created"},
	{"updated", "updated"},
	{"resolved", "resolved"},
}

func parseJSONOrCSV(s string) []string {
	var items []string
	if json.Unmarshal([]byte(s), &items) == nil {
		return items
	}
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// arrayFMFields holds array values that become YAML arrays.
var arrayFMFields = map[string]bool{
	"labels": true, "components": true, "fix_version": true,
}

// zeroSkipFMFields are omitted when their value is zero/false/none.
var zeroSkipFMFields = map[string]bool{
	"effort": true, "reach": true, "rice_score": true,
	"blocked": true, "blocked_reason": true, "ready": true,
}

func printMarkdownDoc(fields map[string]interface{}) {
	fmt.Println("---")
	for _, e := range frontMatterEntries {
		s := output.ValueStr(fields[e.field])
		if s == "" {
			continue
		}
		if zeroSkipFMFields[e.field] && isZeroOrFalseOrNone(s) {
			continue
		}
		if arrayFMFields[e.field] {
			items := parseJSONOrCSV(s)
			fmt.Printf("%s: [%s]\n", e.yamlKey, strings.Join(items, ", "))
		} else {
			fmt.Printf("%s: %s\n", e.yamlKey, fmQuote(s))
		}
	}
	fmt.Println("---")
	fmt.Println()

	if summary := output.ValueStr(fields["summary"]); summary != "" {
		fmt.Printf("# %s\n\n", summary)
	}
	if desc := output.ValueStr(fields["description"]); desc != "" {
		fmt.Println(strings.TrimRight(desc, "\n"))
		fmt.Println()
	}
	if ss := strings.TrimSpace(output.ValueStr(fields["status_summary"])); ss != "" {
		// Strip leading lone colons left by ADF artifacts.
		ss = strings.TrimLeft(ss, ": \n")
		if ss != "" {
			fmt.Println("## Status Summary")
			fmt.Println()
			fmt.Println(strings.TrimRight(ss, "\n"))
			fmt.Println()
		}
	}
}

// fmQuote formats a scalar value for YAML front matter.
// ISO datetimes are truncated to YYYY-MM-DD; values with special chars are double-quoted.
func fmQuote(s string) string {
	if isISODatetime(s) {
		s = s[:10]
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
			return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
		}
	}
	return s
}

func isISODatetime(s string) bool {
	return len(s) > 10 && s[4] == '-' && s[7] == '-'
}

func isZeroOrFalseOrNone(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "0", "false", "none", "{}":
		return true
	}
	return false
}

func issueFieldsToMap(key string, f *jira.IssueFields) map[string]interface{} {
	m := map[string]interface{}{"key": key}
	m["summary"] = f.Summary
	if f.Status != nil {
		m["status"] = f.Status.Name
	}
	if f.Priority != nil {
		m["priority"] = f.Priority.Name
	}
	if f.Assignee != nil {
		m["assignee"] = f.Assignee.DisplayName
	}
	if f.Reporter != nil {
		m["reporter"] = f.Reporter.DisplayName
	}
	if f.IssueType != nil {
		m["issue_type"] = f.IssueType.Name
	}
	if f.Project != nil {
		m["project_key"] = f.Project.Key
	}
	if f.Parent != nil {
		m["parent_key"] = f.Parent.Key
	}
	if f.Resolution != nil {
		m["resolution"] = f.Resolution.Name
	}
	m["created"] = f.Created
	m["updated"] = f.Updated
	m["due_date"] = f.DueDate
	m["resolution_date"] = f.ResolutionDate
	if len(f.Labels) > 0 {
		m["labels"] = strings.Join(f.Labels, ", ")
	}
	if len(f.Components) > 0 {
		names := make([]string, len(f.Components))
		for i, c := range f.Components {
			names[i] = c.Name
		}
		m["components"] = strings.Join(names, ", ")
	}
	if len(f.FixVersions) > 0 {
		names := make([]string, len(f.FixVersions))
		for i, v := range f.FixVersions {
			names[i] = v.Name
		}
		m["fix_versions"] = strings.Join(names, ", ")
	}
	if len(f.Subtasks) > 0 {
		keys := make([]string, len(f.Subtasks))
		for i, s := range f.Subtasks {
			keys[i] = s.Key
		}
		m["subtasks"] = strings.Join(keys, ", ")
	}
	if desc := jira.ADFToMarkdown(f.Description); desc != "" {
		m["description"] = desc
	}
	return m
}

func dbCommentsToMaps(comments []*db.Comment) []map[string]interface{} {
	out := make([]map[string]interface{}, len(comments))
	for i, c := range comments {
		out[i] = map[string]interface{}{
			"id":      c.ID,
			"author":  c.Author,
			"created": c.Created,
			"body":    c.Body,
		}
	}
	return out
}

func apiCommentsToMaps(comments []*jira.Comment) []map[string]interface{} {
	out := make([]map[string]interface{}, len(comments))
	for i, c := range comments {
		author := ""
		if c.Author != nil {
			author = c.Author.DisplayName
		}
		out[i] = map[string]interface{}{
			"id":      c.ID,
			"author":  author,
			"created": c.Created,
			"body":    jira.ADFToMarkdown(c.Body),
		}
	}
	return out
}

func printCommentsSection(comments []map[string]interface{}) {
	if len(comments) == 0 {
		return
	}
	fmt.Printf("---\n\n## Comments (%d)\n", len(comments))
	for _, c := range comments {
		fmt.Printf("\n**%s** · %s\n\n", output.ValueStr(c["author"]), output.ValueStr(c["created"]))
		if body := output.ValueStr(c["body"]); body != "" {
			fmt.Println(strings.TrimRight(body, "\n"))
		}
		fmt.Println()
	}
}


func init() {
	rootCmd.AddCommand(getCmd)
	getCmd.Flags().BoolVar(&getShowComments, "comments", false, "include comments in output")
}
