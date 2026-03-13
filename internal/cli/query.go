package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/output"
)

var queryCmd = &cobra.Command{
	Use:   "query <sql>",
	Short: "Execute a SQL query against the local database",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		results, err := g.query.Execute(args[0])
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
	rootCmd.AddCommand(queryCmd)
}
