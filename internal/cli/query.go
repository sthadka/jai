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
			return runJQLQuery(queryJQL)
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

	if g.jsonOut {
		fmt.Println(string(output.OKQuery(cols, rows, len(rows))))
		return nil
	}

	fmt.Print(output.Table(cols, rows))
	return nil
}

// jqlColumns defines the default columns returned for a live JQL query.
var jqlColumns = []string{"key", "summary", "status", "priority", "assignee", "updated"}

// jqlAPIFields are the Jira field IDs requested from the API.
var jqlAPIFields = []string{"summary", "status", "priority", "assignee", "updated"}

func runJQLQuery(jql string) error {
	cols := jqlColumns
	if g.fields != "" {
		cols = output.ParseFields(g.fields)
	}

	var rows [][]interface{}
	for page, err := range g.jira.SearchAll(context.Background(), jql, jqlAPIFields) {
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

	if g.jsonOut {
		fmt.Println(string(output.OKQuery(cols, rows, len(rows))))
		return nil
	}
	fmt.Print(output.Table(cols, rows))
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
