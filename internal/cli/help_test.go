package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func newHelpTestRoot() *cobra.Command {
	root := &cobra.Command{Use: "jai", Short: "Query Jira with SQL"}
	root.PersistentFlags().String("config", "", "config file")
	root.AddGroup(&cobra.Group{ID: "query", Title: "Querying & Reading:"})
	root.AddCommand(&cobra.Command{
		Use:     "get",
		Short:   "Fetch a single issue",
		GroupID: "query",
		Run:     func(*cobra.Command, []string) {},
	})
	return root
}

func TestColorizedHelpColorsHeadersWhenEnabled(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR", "")
	t.Setenv("CLICOLOR_FORCE", "1")

	root := newHelpTestRoot()
	var buf bytes.Buffer
	root.SetOut(&buf)

	colorizedHelpFunc(root, nil)
	got := buf.String()

	want := ansiBold + ansiCyan + "Querying & Reading:" + ansiReset
	if !strings.Contains(got, want) {
		t.Errorf("group header not colorized.\ngot:\n%q", got)
	}
	if !strings.Contains(got, ansiBold+ansiCyan+"Flags:"+ansiReset) {
		t.Errorf("section header 'Flags:' not colorized.\ngot:\n%q", got)
	}
}

func TestColorizedHelpPlainWhenNoColor(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("CLICOLOR", "")
	t.Setenv("NO_COLOR", "1")

	root := newHelpTestRoot()
	var buf bytes.Buffer
	root.SetOut(&buf)

	colorizedHelpFunc(root, nil)
	got := buf.String()

	if strings.Contains(got, "\033[") {
		t.Errorf("expected no ANSI escapes under NO_COLOR, got:\n%q", got)
	}
	if !strings.Contains(got, "Querying & Reading:") {
		t.Errorf("header text missing under NO_COLOR, got:\n%q", got)
	}
}
