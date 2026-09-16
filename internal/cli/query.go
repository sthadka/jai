package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/jira"
	"github.com/sthadka/jai/internal/output"
)

var queryJQL string

var queryCmd = &cobra.Command{
	Use:   "query <sql>",
	Short: "Execute a SQL query against the local database, or pass --jql for a live Jira query",
	// With --jql the positional arg is not required.
	Args: func(cmd *cobra.Command, args []string) error {
		if queryJQL != "" {
			return nil
		}
		return cobra.ExactArgs(1)(cmd, args)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if queryJQL != "" {
			if g.rawOut {
				return runJQLQueryRaw(queryJQL)
			}
			return runJQLQuery(queryJQL)
		}
		if g.rawOut {
			return fmt.Errorf("--raw is only supported with --jql")
		}
		return runSQLQuery(args[0])
	},
}

func runSQLQuery(sql string) error {
	results, err := g.query.Execute(sql)
	if err != nil {
		if g.jsonOut {
			fmt.Println(string(output.Err("QueryError", err.Error())))
			return nil
		}
		return err
	}

	cols, rows := results.Columns, results.Rows
	if g.fields != "" {
		cols, rows = output.FilterColumns(cols, rows, output.ParseFields(g.fields))
	}

	// --json flag takes precedence over --format for backward compatibility.
	if g.jsonOut {
		syncAge := GetSyncAge()
		fmt.Println(string(output.OKQueryWithMeta(cols, rows, len(rows), syncAge)))
		return nil
	}

	// Use --format flag.
	switch g.format {
	case "json":
		syncAge := GetSyncAge()
		fmt.Println(string(output.OKQueryWithMeta(cols, rows, len(rows), syncAge)))
	case "csv":
		fmt.Print(output.CSV(cols, rows))
	case "tsv":
		fmt.Print(output.TSV(cols, rows))
	case "markdown":
		fmt.Print(output.Markdown(cols, rows))
	default: // "table"
		fmt.Print(output.Table(cols, rows))
	}

	return nil
}

// jqlColumns defines the default columns returned for a live JQL query.
var jqlColumns = []string{"key", "summary", "status", "priority", "assignee", "updated"}

// jqlAPIFields are the Jira field IDs requested from the API by default.
var jqlAPIFields = []string{"summary", "status", "priority", "assignee", "updated"}

// jqlColumnsToAPIFields maps requested output columns to the Jira API field IDs
// that must be fetched to populate them. Without this, a --fields selection
// (e.g. "description") would request only the default fields and the column
// would come back null.
func jqlColumnsToAPIFields(cols []string) []string {
	set := make(map[string]bool)
	for _, col := range cols {
		switch col {
		case "key":
			// Always returned by the API.
			continue
		case "summary", "status", "priority", "assignee", "reporter", "created", "updated", "labels", "parent", "description":
			set[col] = true
		case "type", "issuetype":
			set["issuetype"] = true
		case "project":
			set["project"] = true
		case "resolved", "resolution_date":
			set["resolutiondate"] = true
		}
	}
	fields := make([]string, 0, len(set))
	for f := range set {
		fields = append(fields, f)
	}
	if len(fields) == 0 {
		return jqlAPIFields
	}
	return fields
}

func runJQLQuery(jql string) error {
	cols := jqlColumns
	apiFields := jqlAPIFields
	if g.fields != "" {
		cols = output.ParseFields(g.fields)
		apiFields = jqlColumnsToAPIFields(cols)
	}

	var rows [][]interface{}
	for page, err := range g.jira.SearchAll(context.Background(), jql, apiFields) {
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("JQLError", err.Error())))
				return nil
			}
			return fmt.Errorf("jql query: %w", err)
		}
		for _, issue := range page {
			row, err := jqlIssueToRow(issue, cols)
			if err != nil {
				continue
			}
			rows = append(rows, row)
		}
	}

	// --json flag takes precedence over --format for backward compatibility.
	if g.jsonOut {
		syncAge := GetSyncAge()
		fmt.Println(string(output.OKQueryWithMeta(cols, rows, len(rows), syncAge)))
		return nil
	}

	// Use --format flag.
	switch g.format {
	case "json":
		syncAge := GetSyncAge()
		fmt.Println(string(output.OKQueryWithMeta(cols, rows, len(rows), syncAge)))
	case "csv":
		fmt.Print(output.CSV(cols, rows))
	case "tsv":
		fmt.Print(output.TSV(cols, rows))
	case "markdown":
		fmt.Print(output.Markdown(cols, rows))
	default: // "table"
		fmt.Print(output.Table(cols, rows))
	}

	return nil
}

func runJQLQueryRaw(jql string) error {
	var issues []*jira.Issue
	for page, err := range g.jira.SearchAll(context.Background(), jql, []string{"*all"}) {
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("JQLError", err.Error())))
				return nil
			}
			return fmt.Errorf("jql query: %w", err)
		}
		issues = append(issues, page...)
	}

	if g.jsonOut {
		b, _ := json.Marshal(issues)
		result := map[string]interface{}{
			"ok":    true,
			"data":  json.RawMessage(b),
			"count": len(issues),
		}
		out, _ := json.Marshal(result)
		fmt.Println(string(out))
		return nil
	}

	b, _ := json.Marshal(issues)
	fmt.Println(string(b))
	return nil
}

// jqlIssueToRow extracts the requested columns from a live Jira issue.
func jqlIssueToRow(issue *jira.Issue, cols []string) ([]interface{}, error) {
	var fields jira.IssueFields
	if err := json.Unmarshal(issue.Fields, &fields); err != nil {
		return nil, err
	}

	get := func(col string) interface{} {
		switch col {
		case "key":
			return issue.Key
		case "summary":
			return fields.Summary
		case "status":
			if fields.Status != nil {
				return fields.Status.Name
			}
		case "priority":
			if fields.Priority != nil {
				return fields.Priority.Name
			}
		case "assignee":
			if fields.Assignee != nil {
				return fields.Assignee.DisplayName
			}
		case "reporter":
			if fields.Reporter != nil {
				return fields.Reporter.DisplayName
			}
		case "type", "issuetype":
			if fields.IssueType != nil {
				return fields.IssueType.Name
			}
		case "project":
			if fields.Project != nil {
				return fields.Project.Key
			}
		case "created":
			return fields.Created
		case "updated":
			return fields.Updated
		case "resolved":
			return fields.ResolutionDate
		case "labels":
			return strings.Join(fields.Labels, ", ")
		case "parent":
			if fields.Parent != nil {
				return fields.Parent.Key
			}
		case "description":
			if md := jira.ADFToMarkdown(fields.Description); md != "" {
				return md
			}
		}
		return nil
	}

	row := make([]interface{}, len(cols))
	for i, col := range cols {
		row[i] = get(col)
	}
	return row, nil
}

func init() {
	queryCmd.Flags().StringVar(&queryJQL, "jql", "", "run a live JQL query against Jira (bypasses local DB)")
	rootCmd.AddCommand(queryCmd)
}
