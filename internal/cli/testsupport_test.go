package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/sthadka/jai/pkg/core"
)

// g is a test-only CLI handle. Production code no longer has a global; tests
// build one engine and drive commands through it. Each setup resets it.
var g = &CLI{engine: &core.Engine{}}

// callRunE binds g into the command context (as the root PersistentPreRunE would
// at runtime) and invokes the command's RunE, so tests can call commands
// directly without wiring the whole cobra tree.
func callRunE(cmd *cobra.Command, args []string) error {
	cmd.SetContext(context.WithValue(context.Background(), cliCtxKey{}, g))
	return cmd.RunE(cmd, args)
}

// newRootCmd builds a fresh root command bound to a throwaway CLI, for tests
// that execute the cobra tree in isolation from the package-global rootCmd.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "jai",
		Short: "Query Jira with SQL",
		Long:  "jai syncs Jira Cloud data to a local SQLite database and lets you query it with SQL.",
	}
	bindRoot(root, &CLI{engine: &core.Engine{}})
	return root
}
