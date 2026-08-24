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
	fieldsSearch  string
	fieldsSuggest string
)

// handleFieldsSearch performs a LIKE search on field_map.name and field_map.jira_name.
func handleFieldsSearch(keyword string) error {
	sql := `SELECT name, jira_id, jira_name, type, is_custom, searchable, is_column
			FROM field_map
			WHERE name LIKE ? OR jira_name LIKE ?
			ORDER BY is_column DESC, name ASC`
	pattern := "%" + keyword + "%"
	rows, err := g.db.Query(sql, pattern, pattern)
	if err != nil {
		if g.jsonOut {
			fmt.Println(string(output.Err("QueryError", err.Error())))
			return nil
		}
		return err
	}
	defer rows.Close()

	var mappings []db.FieldMapping
	for rows.Next() {
		var m db.FieldMapping
		if err := rows.Scan(&m.Name, &m.JiraID, &m.JiraName, &m.Type, &m.IsCustom, &m.Searchable, &m.IsColumn); err != nil {
			continue
		}
		mappings = append(mappings, m)
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

	cols := []string{"name", "jira_name", "jira_id", "type", "fts"}
	rows2 := make([][]interface{}, len(mappings))
	for i, m := range mappings {
		fts := ""
		if m.Searchable {
			fts = "*"
		}
		rows2[i] = []interface{}{m.Name, m.JiraName, m.JiraID, m.Type, fts}
	}

	switch g.format {
	case "json":
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
	case "csv":
		fmt.Print(output.CSV(cols, rows2))
	case "tsv":
		fmt.Print(output.TSV(cols, rows2))
	case "markdown":
		fmt.Print(output.Markdown(cols, rows2))
	default:
		fmt.Print(output.Table(cols, rows2))
	}

	return nil
}

// handleFieldsSuggest shows which custom fields have non-null values for a given issue key.
func handleFieldsSuggest(key string) error {
	// Get all custom column fields from field_map.
	sql := `SELECT name, jira_name, type FROM field_map WHERE is_column = 1 AND is_custom = 1`
	rows, err := g.db.Query(sql)
	if err != nil {
		if g.jsonOut {
			fmt.Println(string(output.Err("QueryError", err.Error())))
			return nil
		}
		return err
	}
	defer rows.Close()

	type customField struct {
		Name     string
		JiraName string
		Type     string
	}
	var customFields []customField
	for rows.Next() {
		var cf customField
		if err := rows.Scan(&cf.Name, &cf.JiraName, &cf.Type); err != nil {
			continue
		}
		customFields = append(customFields, cf)
	}
	rows.Close()

	// Check which fields have non-null values for the given issue.
	var populated []map[string]interface{}
	for _, cf := range customFields {
		// Use dynamic SQL to check if the field is non-null for this issue.
		checkSQL := fmt.Sprintf(`SELECT 1 FROM issues WHERE key = ? AND "%s" IS NOT NULL AND "%s" != '' LIMIT 1`, cf.Name, cf.Name)
		checkRows, err := g.db.Query(checkSQL, key)
		if err != nil {
			continue
		}
		hasValue := checkRows.Next()
		checkRows.Close()

		if hasValue {
			populated = append(populated, map[string]interface{}{
				"name":      cf.Name,
				"jira_name": cf.JiraName,
				"type":      cf.Type,
			})
		}
	}

	if g.jsonOut {
		fmt.Println(string(output.OK(map[string]interface{}{
			"issue":  key,
			"fields": populated,
			"count":  len(populated),
		})))
		return nil
	}

	cols := []string{"name", "jira_name", "type"}
	rows2 := make([][]interface{}, len(populated))
	for i, f := range populated {
		rows2[i] = []interface{}{f["name"], f["jira_name"], f["type"]}
	}

	switch g.format {
	case "json":
		fmt.Println(string(output.OK(map[string]interface{}{
			"issue":  key,
			"fields": populated,
			"count":  len(populated),
		})))
	case "csv":
		fmt.Print(output.CSV(cols, rows2))
	case "tsv":
		fmt.Print(output.TSV(cols, rows2))
	case "markdown":
		fmt.Print(output.Markdown(cols, rows2))
	default:
		fmt.Print(output.Table(cols, rows2))
	}

	return nil
}

var fieldsCmd = &cobra.Command{
	Use:   "fields",
	Short: "List available fields and their Jira mappings",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Handle --suggest flag.
		if fieldsSuggest != "" {
			return handleFieldsSuggest(fieldsSuggest)
		}

		// Handle --search flag.
		if fieldsSearch != "" {
			return handleFieldsSearch(fieldsSearch)
		}

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

		// Use --format flag (--json already handled above).
		switch g.format {
		case "json":
			// Already handled above with the full JSON envelope.
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
	},
}

func init() {
	fieldsCmd.Flags().StringVar(&fieldsFilter, "filter", "", "filter by name pattern")
	fieldsCmd.Flags().BoolVar(&fieldsStats, "stats", false, "show population counts per field")
	fieldsCmd.Flags().StringVar(&fieldsProject, "project", "", "scope --stats to a specific project")
	fieldsCmd.Flags().StringVar(&fieldsSearch, "search", "", "LIKE match on field name or jira_name")
	fieldsCmd.Flags().StringVar(&fieldsSuggest, "suggest", "", "given issue key, show custom fields with non-null values")
	rootCmd.AddCommand(fieldsCmd)
}
