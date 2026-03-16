package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	synce "github.com/sthadka/jai/internal/sync"
)

// SyncMsg is sent when a background sync completes.
type SyncMsg struct {
	Results []synce.Progress
	Err     error
}

// SyncTickMsg triggers a sync check.
type SyncTickMsg struct{}

// syncCmd starts a background incremental sync and sends a SyncMsg when done.
func syncCmd(engine *synce.Engine, interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return SyncTickMsg{}
	})
}

// doSync runs an incremental sync and returns a SyncMsg.
func doSync(engine *synce.Engine) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		ch, err := engine.Sync(ctx, false, "")
		if err != nil {
			return SyncMsg{Err: err}
		}

		var results []synce.Progress
		for p := range ch {
			results = append(results, p)
		}
		return SyncMsg{Results: results}
	}
}
