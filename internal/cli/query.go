package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var queryCmd = &cobra.Command{
	Use:   "query <sql>",
	Short: "Execute a SQL query against the local database",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		results, err := g.query.Execute(args[0])
		if err != nil {
			if g.jsonOut {
				jsonError("QueryError", err.Error())
			}
			return err
		}

		if g.jsonOut {
			data, err := results.JSONBytes()
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}

		fmt.Print(results.Table())
		return nil
	},
}

func init() {
	rootCmd.AddCommand(queryCmd)
	_ = os.Stderr // ensure os is used
}
