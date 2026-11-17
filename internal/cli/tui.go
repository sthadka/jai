package cli

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/sthadka/jai/v6/internal/tui"
)

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Launch the interactive full-screen TUI",
	RunE: func(cmd *cobra.Command, args []string) error {
		app := cliFrom(cmd)
		model := tui.New(app.engine.Config, app.engine.Query, app.engine.Sync, app.engine.Store, app.engine.Client)
		p := tea.NewProgram(model, tea.WithAltScreen())
		if _, err := p.Run(); err != nil {
			return fmt.Errorf("TUI error: %w", err)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}
