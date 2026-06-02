package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/output"
)

var searchLimit int

var searchCmd = &cobra.Command{
	Use:   "search <text>",
	Short: "Full-text search across issues",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		text := args[0]

		sql := fmt.Sprintf(`
			SELECT i.key, i.summary, i.status, i.assignee, highlight(issues_fts, 1, '[', ']') AS match
			FROM issues_fts
			JOIN issues i ON i.key = issues_fts.key
			WHERE issues_fts MATCH ?
			ORDER BY issues_fts.rank
			LIMIT %d`, searchLimit)

		results, err := g.query.Execute(sql, text)
		if err != nil && strings.Contains(err.Error(), "fts5: missing row") {
			fmt.Fprintln(cmd.ErrOrStderr(), "FTS index out of sync, rebuilding...")
			if rbErr := g.db.RebuildFTS(); rbErr != nil {
				return fmt.Errorf("rebuilding FTS index: %w", rbErr)
			}
			results, err = g.query.Execute(sql, text)
		}
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
	},
}

func init() {
	searchCmd.Flags().IntVar(&searchLimit, "limit", 20, "maximum number of results")
	rootCmd.AddCommand(searchCmd)
}
