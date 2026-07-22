package cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/output"
)

// CommandSchema describes a command's parameters and flags.
type CommandSchema struct {
	Name        string                     `json:"command"`
	Description string                     `json:"description"`
	Params      map[string]ParamSchema     `json:"params,omitempty"`
	Flags       map[string]ParamSchema     `json:"flags,omitempty"`
}

// ParamSchema describes a single parameter.
type ParamSchema struct {
	Type        string `json:"type"`
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description,omitempty"`
}

var commandSchemas = []CommandSchema{
	{
		Name:        "get",
		Description: "Fetch a single issue from the local database",
		Params: map[string]ParamSchema{
			"key": {Type: "string", Required: true, Description: "Issue key (e.g. ROX-123)"},
		},
		Flags: map[string]ParamSchema{
			"json":   {Type: "bool", Description: "Output as JSON"},
			"fields": {Type: "string", Description: "Comma-separated field names to include"},
		},
	},
	{
		Name:        "query",
		Description: "Execute a SQL query against the local database",
		Params: map[string]ParamSchema{
			"sql": {Type: "string", Required: true, Description: "SQL query to execute"},
		},
		Flags: map[string]ParamSchema{
			"json":   {Type: "bool", Description: "Output as JSON"},
			"fields": {Type: "string", Description: "Filter output columns"},
		},
	},
	{
		Name:        "search",
		Description: "Full-text search across issues",
		Params: map[string]ParamSchema{
			"text": {Type: "string", Required: true, Description: "Search text"},
		},
		Flags: map[string]ParamSchema{
			"json":   {Type: "bool", Description: "Output as JSON"},
			"fields": {Type: "string", Description: "Filter output columns"},
			"limit":  {Type: "int", Description: "Max results (default: 20)"},
		},
	},
	{
		Name:        "sync",
		Description: "Sync Jira issues to local database",
		Flags: map[string]ParamSchema{
			"full": {Type: "bool", Description: "Full resync (delete + re-fetch)"},
		},
	},
	{
		Name:        "set",
		Description: "Set a field value on a Jira issue (queued locally)",
		Params: map[string]ParamSchema{
			"key":   {Type: "string", Required: true, Description: "Issue key"},
			"field": {Type: "string", Required: true, Description: "Field name"},
			"value": {Type: "string", Required: true, Description: "New value"},
		},
	},
	{
		Name:        "comment",
		Description: "Add a comment to a Jira issue (queued locally)",
		Params: map[string]ParamSchema{
			"key":  {Type: "string", Required: true, Description: "Issue key"},
			"text": {Type: "string", Required: true, Description: "Comment text"},
		},
	},
	{
		Name:        "push",
		Description: "Push pending changes to Jira",
	},
	{
		Name:        "fields",
		Description: "List available fields and their Jira mappings",
		Flags: map[string]ParamSchema{
			"json":    {Type: "bool", Description: "Output as JSON"},
			"filter":  {Type: "string", Description: "Filter by name pattern"},
			"stats":   {Type: "bool", Description: "Show population counts per field"},
			"project": {Type: "string", Description: "Scope --stats to a specific project"},
		},
	},
	{
		Name:        "status",
		Description: "Show sync and queue status",
		Flags: map[string]ParamSchema{
			"json": {Type: "bool", Description: "Output as JSON"},
		},
	},
	{
		Name:        "schema",
		Description: "Show command parameter schema (for AI agents)",
		Params: map[string]ParamSchema{
			"command": {Type: "string", Description: "Command name (omit to list all)"},
		},
	},
	{
		Name:        "db",
		Description: "Manage the local database: 'db reset' (delete and recreate), 'db path' (print file path), 'db info' (show stats)",
		Flags: map[string]ParamSchema{
			"json":  {Type: "bool", Description: "Output as JSON (db info only)"},
			"force": {Type: "bool", Description: "Skip confirmation prompt (db reset only)"},
		},
	},
}

var schemaCmd = &cobra.Command{
	Use:   "schema [command]",
	Short: "Show command schema (for AI agents)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			// List all commands.
			list := make([]map[string]string, len(commandSchemas))
			for i, s := range commandSchemas {
				list[i] = map[string]string{
					"command":     s.Name,
					"description": s.Description,
				}
			}
			fmt.Println(string(output.OK(map[string]interface{}{"commands": list})))
			return nil
		}

		name := args[0]
		for _, s := range commandSchemas {
			if s.Name == name {
				fmt.Println(string(output.OK(s)))
				return nil
			}
		}

		fmt.Println(string(output.Err("NotFoundError", fmt.Sprintf("unknown command: %s", name))))
		return nil
	},
}

// schemaDBCmd returns a compact representation of the issues table for AI agents.
var schemaDBCmd = &cobra.Command{
	Use:   "db",
	Short: "Show database schema (for AI agents)",
	RunE: func(cmd *cobra.Command, args []string) error {
		rows, err := g.db.Query("PRAGMA table_info(issues)")
		if err != nil {
			fmt.Println(string(output.Err("QueryError", err.Error())))
			return nil
		}
		defer rows.Close()

		type col struct {
			Name     string `json:"name"`
			Type     string `json:"type"`
			Custom   bool   `json:"custom,omitempty"`
			JiraName string `json:"jira_name,omitempty"`
		}

		var columns []col
		for rows.Next() {
			var cid int
			var name, colType string
			var notNull int
			var dflt interface{}
			var pk int
			if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
				continue
			}
			if name == "raw_json" || name == "comments_text" || name == "synced_at" {
				continue
			}
			columns = append(columns, col{Name: name, Type: strings.ToLower(colType)})
		}
		rows.Close()

		// Enrich columns with jira_name and custom flag from field_map.
		metaRows, err := g.db.Query("SELECT name, jira_name, is_custom FROM field_map WHERE is_column = 1")
		if err == nil {
			type fieldMeta struct {
				jiraName string
				isCustom bool
			}
			metas := map[string]fieldMeta{}
			for metaRows.Next() {
				var n, jn string
				var custom bool
				if err := metaRows.Scan(&n, &jn, &custom); err == nil {
					metas[n] = fieldMeta{jiraName: jn, isCustom: custom}
				}
			}
			metaRows.Close()
			for i, c := range columns {
				if fm, ok := metas[c.Name]; ok {
					columns[i].Custom = fm.isCustom
					columns[i].JiraName = fm.jiraName
				}
			}
		}

		fmt.Println(string(output.OK(map[string]interface{}{
			"table":   "issues",
			"columns": columns,
			"hint":    "Use 'jai schema values <column>' to see distinct values for any column",
		})))
		return nil
	},
}

// safeColumnRe matches valid SQLite column names.
var safeColumnRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// schemaValuesCmd returns distinct values for a column in the issues table.
var schemaValuesCmd = &cobra.Command{
	Use:   "values <column>",
	Short: "List distinct values for a column (for AI agents)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		col := args[0]
		if !safeColumnRe.MatchString(col) {
			fmt.Println(string(output.Err("InvalidColumn", "column name contains invalid characters")))
			return nil
		}

		sql := fmt.Sprintf(`SELECT DISTINCT "%s" FROM issues WHERE "%s" IS NOT NULL AND "%s" != '' ORDER BY "%s" LIMIT 200`, col, col, col, col)
		rows, err := g.db.Query(sql)
		if err != nil {
			fmt.Println(string(output.Err("QueryError", err.Error())))
			return nil
		}
		defer rows.Close()

		var values []interface{}
		for rows.Next() {
			var v interface{}
			if err := rows.Scan(&v); err == nil {
				values = append(values, v)
			}
		}

		fmt.Println(string(output.OK(map[string]interface{}{
			"column": col,
			"values": values,
			"count":  len(values),
		})))
		return nil
	},
}

func init() {
	schemaCmd.AddCommand(schemaDBCmd)
	schemaCmd.AddCommand(schemaValuesCmd)
	rootCmd.AddCommand(schemaCmd)
}
