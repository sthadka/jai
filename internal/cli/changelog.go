package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/output"
)

var changelogFieldFilter string

var changelogCmd = &cobra.Command{
	Use:   "changelog <key>",
	Short: "Show changelog history for an issue",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := strings.ToUpper(args[0])

		// Build query with optional field filter.
		query := "SELECT changed_at, author, field, from_string, to_string FROM changelog WHERE issue_key = ?"
		params := []interface{}{key}

		if changelogFieldFilter != "" {
			query += " AND field = ?"
			params = append(params, changelogFieldFilter)
		}

		query += " ORDER BY changed_at ASC"

		rows, err := g.db.Query(query, params...)
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("QueryError", err.Error())))
				return nil
			}
			return fmt.Errorf("querying changelog: %w", err)
		}
		defer rows.Close()

		type entry struct {
			ChangedAt  string `json:"changed_at"`
			Author     string `json:"author"`
			Field      string `json:"field"`
			FromString string `json:"from,omitempty"`
			ToString   string `json:"to"`
		}

		var entries []entry
		for rows.Next() {
			var e entry
			if err := rows.Scan(&e.ChangedAt, &e.Author, &e.Field, &e.FromString, &e.ToString); err != nil {
				continue
			}
			entries = append(entries, e)
		}

		if g.jsonOut {
			fmt.Println(string(output.OK(map[string]interface{}{
				"issue_key": key,
				"entries":   entries,
			})))
			return nil
		}

		// Human-readable output.
		if len(entries) == 0 {
			fmt.Printf("%s Changelog\n", key)
			fmt.Println(strings.Repeat("─", len(key)+10))
			fmt.Println("(no changelog entries)")
			return nil
		}

		fmt.Printf("%s Changelog\n", key)
		fmt.Println(strings.Repeat("─", len(key)+10))

		for _, e := range entries {
			// Format: 2026-08-20 10:30  alice    status      To Do → In Progress
			timestamp := e.ChangedAt
			if len(timestamp) > 16 {
				timestamp = timestamp[:16] // Trim to "2026-08-20 10:30"
			}

			author := e.Author
			if len(author) > 12 {
				author = author[:12]
			}

			field := e.Field
			if len(field) > 12 {
				field = field[:12]
			}

			change := fmt.Sprintf("%s → %s", e.FromString, e.ToString)

			fmt.Printf("%-16s  %-12s  %-12s  %s\n", timestamp, author, field, change)
		}

		return nil
	},
}

func init() {
	changelogCmd.Flags().StringVar(&changelogFieldFilter, "field", "", "filter by field name")
	rootCmd.AddCommand(changelogCmd)
}
