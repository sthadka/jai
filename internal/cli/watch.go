package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/output"
)

var watchCmd = &cobra.Command{
	Use:   "watch <issue-key> [user]",
	Short: "Add a watcher to a Jira issue (pushed immediately)",
	Long: `Add a watcher to a Jira issue directly via the Jira API.

If no user is specified, adds yourself (from config "me") as a watcher.

Examples:
  jai watch PROJ-123                    # watch as yourself
  jai watch PROJ-123 user@example.com   # add specific user as watcher`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		issueKey := strings.ToUpper(args[0])

		user := g.cfg.Me
		if len(args) > 1 {
			user = args[1]
		}

		if user == "" {
			msg := "no user specified and 'me' not set in config"
			if g.jsonOut {
				fmt.Println(string(output.Err("ValidationError", msg)))
				return nil
			}
			return fmt.Errorf("%s", msg)
		}

		accountID, err := g.jira.ResolveAccountID(cmd.Context(), user)
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("JiraError", err.Error())))
				return nil
			}
			return fmt.Errorf("resolving user: %w", err)
		}

		if err := g.jira.AddWatcher(cmd.Context(), issueKey, accountID); err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("JiraError", err.Error())))
				return nil
			}
			return fmt.Errorf("adding watcher: %w", err)
		}

		if g.jsonOut {
			fmt.Println(string(output.OK(map[string]string{
				"message":   fmt.Sprintf("added %s as watcher on %s", user, issueKey),
				"issue_key": issueKey,
				"user":      user,
			})))
			return nil
		}

		fmt.Printf("%s: added %s as watcher\n", issueKey, user)
		return nil
	},
}

var unwatchCmd = &cobra.Command{
	Use:   "unwatch <issue-key>",
	Short: "Remove yourself as a watcher from a Jira issue (pushed immediately)",
	Long: `Remove yourself as a watcher from a Jira issue directly via the Jira API.

Always removes the configured "me" user.

Examples:
  jai unwatch PROJ-123`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		issueKey := strings.ToUpper(args[0])

		user := g.cfg.Me
		if user == "" {
			msg := "'me' not set in config — cannot determine which user to remove"
			if g.jsonOut {
				fmt.Println(string(output.Err("ValidationError", msg)))
				return nil
			}
			return fmt.Errorf("%s", msg)
		}

		accountID, err := g.jira.ResolveAccountID(cmd.Context(), user)
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("JiraError", err.Error())))
				return nil
			}
			return fmt.Errorf("resolving user: %w", err)
		}

		if err := g.jira.RemoveWatcher(cmd.Context(), issueKey, accountID); err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("JiraError", err.Error())))
				return nil
			}
			return fmt.Errorf("removing watcher: %w", err)
		}

		if g.jsonOut {
			fmt.Println(string(output.OK(map[string]string{
				"message":   fmt.Sprintf("removed %s as watcher from %s", user, issueKey),
				"issue_key": issueKey,
				"user":      user,
			})))
			return nil
		}

		fmt.Printf("%s: removed %s as watcher\n", issueKey, user)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(watchCmd)
	rootCmd.AddCommand(unwatchCmd)
}
