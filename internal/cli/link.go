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

var linkCmd = &cobra.Command{
	Use:   "link <from-key> <to-key>",
	Short: "Create a link between two Jira issues",
	Long: `Create a link between two Jira issues directly via the Jira API.

Links are pushed immediately and are idempotent — creating the same
link twice is a no-op.

Examples:
  jai link ROX-1 ROX-2                       # default link type
  jai link ROX-1 ROX-2 --type "Blocks"       # typed link
  jai link --list-types                       # show available link types`,
	Args: cobra.RangeArgs(0, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if linkFlags.listTypes {
			return runListLinkTypes(cmd)
		}

		if len(args) < 2 {
			msg := "requires two issue keys: jai link <from-key> <to-key>"
			if g.jsonOut {
				fmt.Println(string(output.Err("ValidationError", msg)))
				return nil
			}
			return fmt.Errorf("%s", msg)
		}

		fromKey := strings.ToUpper(args[0])
		toKey := strings.ToUpper(args[1])
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

		if err := g.jira.CreateLink(cmd.Context(), linkType, fromKey, toKey); err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("JiraError", err.Error())))
				return nil
			}
			return fmt.Errorf("creating link: %w", err)
		}

		if g.jsonOut {
			fmt.Println(string(output.OK(map[string]string{
				"from_key":  fromKey,
				"to_key":    toKey,
				"link_type": linkType,
				"status":    "created",
			})))
			return nil
		}

		fmt.Printf("%s -> %s: linked (%s)\n", fromKey, toKey, linkType)
		return nil
	},
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
