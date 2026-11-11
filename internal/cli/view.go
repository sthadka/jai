package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/metrics"
	"github.com/sthadka/jai/internal/output"
)

// applyGeneratedMetrics merges the config-generated metric fragments (snippets)
// and simple-metric views into cfg in memory, so `jai view` and `jai snippet`
// expose them without a second copy of the SQL and without overwriting any
// user-defined entry. It is a no-op when no `metrics:` block is configured, so
// users who have not opted into flow metrics see no injected entries. Returns
// the generated snippet/view names and the user collisions that were skipped.
func applyGeneratedMetrics(cfg *config.Config) (snippets, views, collisions []string) {
	if cfg == nil || cfg.Metrics == nil {
		return nil, nil, nil
	}
	pol := metrics.CompilePolicy(cfg.Metrics)
	sAdded, sColl := cfg.MergeGeneratedSnippets(metrics.GenerateFragments(pol))
	vAdded, vColl := cfg.MergeGeneratedViews(metrics.GenerateViews(pol))
	return sAdded, vAdded, append(sColl, vColl...)
}

var viewCmd = &cobra.Command{
	Use:   "view [name]",
	Short: "Execute a named view query",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Surface generated metric fragments/views (in-memory; never persisted).
		if _, _, collisions := applyGeneratedMetrics(g.cfg); len(collisions) > 0 {
			fmt.Fprintf(os.Stderr, "note: generated metric names overridden by user config: %v\n", collisions)
		}

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

		// --json flag takes precedence over --format for backward compatibility.
		if g.jsonOut {
			fmt.Println(string(output.OKQuery(cols, rows, len(rows))))
			return nil
		}

		if view.Title != "" && (g.format == "table" || g.format == "") {
			fmt.Printf("=== %s ===\n", view.Title)
		}

		// Use --format flag.
		switch g.format {
		case "json":
			fmt.Println(string(output.OKQuery(cols, rows, len(rows))))
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
	rootCmd.AddCommand(viewCmd)
}
