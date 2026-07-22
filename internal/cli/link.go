package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/output"
)

var linkFlags struct {
	linkType  string
	listTypes bool
}

// isURL returns true if s looks like an HTTP(S) URL.
func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

var linkCmd = &cobra.Command{
	Use:   "link <issue-key> <target> [title]",
	Short: "Create a link between two Jira issues or add a remote URL link",
	Long: `Create a link between two Jira issues or add a remote (web URL) link
directly via the Jira API.

If the second argument is a URL (starts with http:// or https://), a remote
link is created on the issue. An optional third argument sets the link title
(defaults to the URL itself).

If the second argument is an issue key, a standard issue-to-issue link is
created.

Links are pushed immediately and are idempotent.

Examples:
  jai link ROX-1 ROX-2                                          # default link type
  jai link ROX-1 ROX-2 --type "Blocks"                          # typed link
  jai link ROX-1 https://github.com/org/repo/pull/42 "PR #42"   # remote link
  jai link ROX-1 https://example.com                             # remote link (URL as title)
  jai link --list-types                                          # show available link types`,
	Args: cobra.RangeArgs(0, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if linkFlags.listTypes {
			return runListLinkTypes(cmd)
		}

		if len(args) < 2 {
			msg := "requires at least two arguments: jai link <issue-key> <target>"
			if g.jsonOut {
				fmt.Println(string(output.Err("ValidationError", msg)))
				return nil
			}
			return fmt.Errorf("%s", msg)
		}

		issueKey := strings.ToUpper(args[0])
		target := args[1]

		// Remote link: second arg is a URL
		if isURL(target) {
			return runRemoteLink(cmd, issueKey, target, args)
		}

		// Issue-to-issue link
		if len(args) > 2 {
			msg := "too many arguments for issue link; use --type flag for link type"
			if g.jsonOut {
				fmt.Println(string(output.Err("ValidationError", msg)))
				return nil
			}
			return fmt.Errorf("%s", msg)
		}

		toKey := strings.ToUpper(target)
		linkType := linkFlags.linkType

		resolved, err := resolveLinkType(cmd, linkType)
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("JiraError", err.Error())))
				return nil
			}
			return err
		}
		linkType = resolved

		if err := g.jira.CreateLink(cmd.Context(), linkType, issueKey, toKey); err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("JiraError", err.Error())))
				return nil
			}
			return fmt.Errorf("creating link: %w", err)
		}

		if g.jsonOut {
			fmt.Println(string(output.OK(map[string]string{
				"from_key":  issueKey,
				"to_key":    toKey,
				"link_type": linkType,
				"status":    "created",
			})))
			return nil
		}

		fmt.Printf("%s -> %s: linked (%s)\n", issueKey, toKey, linkType)
		return nil
	},
}

func runRemoteLink(cmd *cobra.Command, issueKey, url string, args []string) error {
	title := url
	if len(args) > 2 {
		title = args[2]
	}

	if err := g.jira.CreateRemoteLink(cmd.Context(), issueKey, url, title); err != nil {
		if g.jsonOut {
			fmt.Println(string(output.Err("JiraError", err.Error())))
			return nil
		}
		return fmt.Errorf("creating remote link: %w", err)
	}

	if g.jsonOut {
		fmt.Println(string(output.OK(map[string]string{
			"issue_key": issueKey,
			"url":       url,
			"title":     title,
			"status":    "created",
		})))
		return nil
	}

	fmt.Printf("%s: remote link created (%s)\n", issueKey, url)
	return nil
}

func runListLinkTypes(cmd *cobra.Command) error {
	linkTypes, err := g.jira.GetLinkTypes(cmd.Context())
	if err != nil {
		if g.jsonOut {
			fmt.Println(string(output.Err("JiraError", err.Error())))
			return nil
		}
		return fmt.Errorf("fetching link types: %w", err)
	}

	if g.jsonOut {
		fmt.Println(string(output.OK(map[string]interface{}{
			"link_types": linkTypes,
		})))
		return nil
	}

	if len(linkTypes) == 0 {
		fmt.Println("No link types available.")
		return nil
	}

	fmt.Println("Available link types:")
	for _, lt := range linkTypes {
		fmt.Printf("  - %s (inward: %q, outward: %q)\n", lt.Name, lt.Inward, lt.Outward)
	}
	return nil
}

func resolveLinkType(cmd *cobra.Command, name string) (string, error) {
	linkTypes, err := g.jira.GetLinkTypes(cmd.Context())
	if err != nil {
		return "", fmt.Errorf("fetching link types: %w", err)
	}
	for _, lt := range linkTypes {
		if strings.EqualFold(lt.Name, name) {
			return lt.Name, nil
		}
	}
	names := make([]string, len(linkTypes))
	for i, lt := range linkTypes {
		names[i] = lt.Name
	}
	return "", fmt.Errorf("unknown link type %q (available: %s)", name, strings.Join(names, ", "))
}

func init() {
	linkCmd.Flags().StringVar(&linkFlags.linkType, "type", "Relates", "link type name (e.g. Relates, Blocks)")
	linkCmd.Flags().BoolVar(&linkFlags.listTypes, "list-types", false, "list available link types")
	rootCmd.AddCommand(linkCmd)
}
