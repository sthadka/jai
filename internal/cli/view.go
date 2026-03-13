package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/output"
)

var viewCmd = &cobra.Command{
	Use:   "view [name]",
	Short: "Execute a named view query",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			// List available views.
			views := g.cfg.Views
			if len(views) == 0 {
				fmt.Println("No views configured. Add views to your config file.")
				return nil
			}
			if g.jsonOut {
				list := make([]map[string]string, len(views))
				for i, v := range views {
					list[i] = map[string]string{"name": v.Name, "title": v.Title}
				}
				fmt.Println(string(output.OK(map[string]interface{}{"views": list})))
				return nil
			}
			fmt.Println("Available views:")
			for _, v := range views {
				fmt.Printf("  %-20s %s\n", v.Name, v.Title)
			}
			return nil
		}

		name := args[0]
		view := g.cfg.ViewByName(name)
		if view == nil {
			msg := fmt.Sprintf("unknown view: %s", name)
			if g.jsonOut {
				fmt.Println(string(output.Err("NotFoundError", msg)))
				return nil
			}
			return fmt.Errorf("%s", msg)
		}

		results, err := g.query.Execute(view.Query)
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("QueryError", err.Error())))
				return nil
			}
			return err
		}

		cols, rows := results.Columns, results.Rows

		// Apply view column filter.
		if len(view.Columns) > 0 {
			cols, rows = output.FilterColumns(cols, rows, view.Columns)
		}

		// Apply --fields override.
		if g.fields != "" {
			cols, rows = output.FilterColumns(cols, rows, output.ParseFields(g.fields))
		}

		if g.jsonOut {
			fmt.Println(string(output.OKQuery(cols, rows, len(rows))))
			return nil
		}

		if view.Title != "" {
			fmt.Printf("=== %s ===\n", view.Title)
		}
		fmt.Print(output.Table(cols, rows))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(viewCmd)
}
