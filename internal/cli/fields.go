package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/output"
)

var fieldsFilter string

var fieldsCmd = &cobra.Command{
	Use:   "fields",
	Short: "List available fields and their Jira mappings",
	RunE: func(cmd *cobra.Command, args []string) error {
		mappings, err := g.db.AllFieldMappings()
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("QueryError", err.Error())))
				return nil
			}
			return err
		}

		// Apply filter.
		if fieldsFilter != "" {
			filter := strings.ToLower(fieldsFilter)
			var filtered = mappings[:0]
			for _, m := range mappings {
				if strings.Contains(strings.ToLower(m.Name), filter) ||
					strings.Contains(strings.ToLower(m.JiraName), filter) {
					filtered = append(filtered, m)
				}
			}
			mappings = filtered
		}

		if g.jsonOut {
			fields := make([]map[string]interface{}, len(mappings))
			for i, m := range mappings {
				fields[i] = map[string]interface{}{
					"name":       m.Name,
					"jira_id":    m.JiraID,
					"jira_name":  m.JiraName,
					"type":       m.Type,
					"is_custom":  m.IsCustom,
					"searchable": m.Searchable,
				}
			}
			fmt.Println(string(output.OK(map[string]interface{}{
				"fields": fields,
				"count":  len(fields),
			})))
			return nil
		}

		// Human output.
		cols := []string{"name", "jira_id", "type", "fts"}
		rows := make([][]interface{}, len(mappings))
		for i, m := range mappings {
			fts := ""
			if m.Searchable {
				fts = "*"
			}
			rows[i] = []interface{}{m.Name, m.JiraID, m.Type, fts}
		}
		fmt.Print(output.Table(cols, rows))
		return nil
	},
}

func init() {
	fieldsCmd.Flags().StringVar(&fieldsFilter, "filter", "", "filter by name pattern")
	rootCmd.AddCommand(fieldsCmd)
}
