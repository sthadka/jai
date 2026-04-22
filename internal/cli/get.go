package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
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
			printIssueMap(fields)
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
		printIssueMap(data)
		if getShowComments {
			printCommentsSection(dbComments)
		}
		return nil
	},
}

func printIssueMap(fields map[string]interface{}) {
	if v := output.ValueStr(fields["key"]); v != "" {
		fmt.Printf("  %-22s %s\n", "Key:", v)
	}
	if v := output.ValueStr(fields["summary"]); v != "" {
		fmt.Printf("  %-22s %s\n", "Summary:", v)
	}
	fmt.Println()

	skip := map[string]bool{"key": true, "summary": true, "raw_json": true, "comments_text": true}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		if !skip[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := fields[k]
		if v == nil || output.ValueStr(v) == "" {
			continue
		}
		fmt.Print(output.KV(toTitle(k), v))
	}
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
	if desc := jira.ADFToPlaintext(f.Description); desc != "" {
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
			"body":    jira.ADFToPlaintext(c.Body),
		}
	}
	return out
}

func printCommentsSection(comments []map[string]interface{}) {
	fmt.Printf("\n  Comments (%d)\n", len(comments))
	if len(comments) == 0 {
		fmt.Println("  (none)")
		return
	}
	sep := strings.Repeat("─", 60)
	for _, c := range comments {
		fmt.Printf("  %s\n", sep)
		fmt.Printf("  %-14s %s  |  %s\n", "Author:", output.ValueStr(c["author"]), output.ValueStr(c["created"]))
		if body := output.ValueStr(c["body"]); body != "" {
			for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
				fmt.Printf("  %s\n", line)
			}
		}
	}
	fmt.Printf("  %s\n", sep)
}

func toTitle(s string) string {
	result := make([]byte, 0, len(s))
	capitalize := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' {
			result = append(result, ' ')
			capitalize = true
			continue
		}
		if capitalize {
			if c >= 'a' && c <= 'z' {
				c -= 32
			}
			capitalize = false
		}
		result = append(result, c)
	}
	return string(result)
}

func init() {
	rootCmd.AddCommand(getCmd)
	getCmd.Flags().BoolVar(&getShowComments, "comments", false, "include comments in output")
}
