package cli

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/output"
)

var openFlags struct {
	urlOnly bool
}

// browserOpener is the function used to open a URL in the browser.
// Override in tests.
var browserOpener = openBrowser

var openCmd = &cobra.Command{
	Use:   "open <issue-key>",
	Short: "Open an issue in the default browser",
	Long: `Open a Jira issue in the default web browser, or print its URL.

Examples:
  jai open PROJ-123              # open in browser
  jai open PROJ-123 --url-only   # print URL only
  jai open PROJ-123 --json       # output JSON with URL`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := strings.ToUpper(args[0])

		baseURL := strings.TrimRight(g.cfg.Jira.URL, "/")
		issueURL := baseURL + "/browse/" + key

		if g.jsonOut {
			fmt.Fprintln(cmd.OutOrStdout(), string(output.OK(map[string]string{
				"url": issueURL,
			})))
			return nil
		}

		if openFlags.urlOnly {
			fmt.Fprintln(cmd.OutOrStdout(), issueURL)
			return nil
		}

		if err := browserOpener(issueURL); err != nil {
			return fmt.Errorf("opening browser: %w", err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Opened %s in browser\n", key)
		return nil
	},
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default: // linux, freebsd, etc.
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func init() {
	openCmd.Flags().BoolVar(&openFlags.urlOnly, "url-only", false, "print URL without opening browser")
	rootCmd.AddCommand(openCmd)
}
