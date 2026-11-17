package cli

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// helpHeaderRE matches the standalone header lines in cobra's help output: the
// grouped command sections (e.g. "Querying & Reading:") and the built-in
// sections ("Usage:", "Flags:", "Global Flags:", "Additional Commands:"). A
// header is a line that is just a Capitalized title made of letters, spaces and
// "&", ending in ":".
var helpHeaderRE = regexp.MustCompile(`(?m)^([A-Z][A-Za-z &]*:)\s*$`)

// colorizedHelpFunc renders cobra's standard help, then tints the section and
// group headers with an accent color so the grouped layout is easy to scan —
// the same visual treatment bd uses. Color is suppressed when stdout is not a
// terminal or when NO_COLOR / CLICOLOR=0 is set (CLICOLOR_FORCE overrides).
func colorizedHelpFunc(cmd *cobra.Command, _ []string) {
	var out strings.Builder
	switch {
	case cmd.Long != "":
		out.WriteString(cmd.Long + "\n\n")
	case cmd.Short != "":
		out.WriteString(cmd.Short + "\n\n")
	}
	out.WriteString(cmd.UsageString())

	text := out.String()
	if helpColorEnabled() {
		text = helpHeaderRE.ReplaceAllString(text, ansiBold+ansiCyan+"$1"+ansiReset)
	}
	fmt.Fprint(cmd.OutOrStdout(), text)
}

// helpColorEnabled reports whether help headers should be colorized, following
// the de-facto conventions: NO_COLOR (any value) and CLICOLOR=0 disable color,
// CLICOLOR_FORCE forces it, otherwise color is on only when stdout is a TTY.
func helpColorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("CLICOLOR") == "0" {
		return false
	}
	if os.Getenv("CLICOLOR_FORCE") != "" {
		return true
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
