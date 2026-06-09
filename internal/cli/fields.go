package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/output"
)

var (
	fieldsFilter  string
	fieldsStats   bool
	fieldsProject string
)

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

		// Optionally gather population stats.
		var stats map[string]*db.FieldStats
		if fieldsStats {
			var colNames []string
			for _, m := range mappings {
				if m.IsColumn {
					colNames = append(colNames, m.Name)
				}
			}
			stats, _ = g.db.FieldPopulationStats(colNames, fieldsProject)
		}

		if g.jsonOut {
			fields := make([]map[string]interface{}, len(mappings))
			for i, m := range mappings {
				f := map[string]interface{}{
					"name":       m.Name,
					"jira_id":    m.JiraID,
					"jira_name":  m.JiraName,
					"type":       m.Type,
					"is_custom":  m.IsCustom,
					"searchable": m.Searchable,
				}
				if s, ok := stats[m.Name]; ok {
					f["populated"] = s.NonNull
					f["total"] = s.Total
					if s.Sample != "" {
						f["sample"] = s.Sample
					}
				}
				fields[i] = f
			}
			fmt.Println(string(output.OK(map[string]interface{}{
				"fields": fields,
				"count":  len(fields),
			})))
			return nil
		}

		// Human output.
		cols := []string{"name", "jira_name", "jira_id", "type", "fts"}
		if fieldsStats {
			cols = append(cols, "populated")
		}
		rows := make([][]interface{}, len(mappings))
		for i, m := range mappings {
			fts := ""
			if m.Searchable {
				fts = "*"
			}
			row := []interface{}{m.Name, m.JiraName, m.JiraID, m.Type, fts}
			if fieldsStats {
				if s, ok := stats[m.Name]; ok {
					pct := 0.0
					if s.Total > 0 {
						pct = float64(s.NonNull) / float64(s.Total) * 100
					}
					row = append(row, fmt.Sprintf("%d/%d (%.1f%%)", s.NonNull, s.Total, pct))
				} else {
					row = append(row, "N/A")
				}
			}
			rows[i] = row
		}
		fmt.Print(output.Table(cols, rows))
		return nil
	},
}

func init() {
	fieldsCmd.Flags().StringVar(&fieldsFilter, "filter", "", "filter by name pattern")
	fieldsCmd.Flags().BoolVar(&fieldsStats, "stats", false, "show population counts per field")
	fieldsCmd.Flags().StringVar(&fieldsProject, "project", "", "scope --stats to a specific project")
	rootCmd.AddCommand(fieldsCmd)
}
