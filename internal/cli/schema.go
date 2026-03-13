package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/syethadk/jai/internal/output"
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
			"json":   {Type: "bool", Description: "Output as JSON"},
			"filter": {Type: "string", Description: "Filter by name pattern"},
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

func init() {
	rootCmd.AddCommand(schemaCmd)
}
