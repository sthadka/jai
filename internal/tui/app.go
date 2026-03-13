package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/syethadk/jai/internal/config"
	"github.com/syethadk/jai/internal/query"
	synce "github.com/syethadk/jai/internal/sync"
)

// Mode represents the current UI mode.
type Mode int

const (
	ModeTable   Mode = iota
	ModeFilter       // filter input active
	ModeDetail       // detail pane open
	ModeSortPicker   // sort column picker
)

// App is the root bubbletea model.
type App struct {
	cfg        *config.Config
	queryEng   *query.Engine
	syncEngine *synce.Engine

	views      []config.ViewConfig
	activeView int
	tables     []*TableModel

	mode        Mode
	filterInput textinput.Model
	detail      *DetailPane

	width  int
	height int
	keys   KeyMap

	syncing    bool
	syncStatus string
	syncTick   time.Duration
	lastSync   time.Time

	err string
}

// New creates a new App model.
func New(cfg *config.Config, queryEng *query.Engine, syncEng *synce.Engine) *App {
	ti := textinput.New()
	ti.Placeholder = "filter..."
	ti.CharLimit = 100

	a := &App{
		cfg:        cfg,
		queryEng:   queryEng,
		syncEngine: syncEng,
		keys:       DefaultKeys(),
		filterInput: ti,
		syncTick:   15 * time.Minute,
	}

	// Parse sync interval from config.
	if d, err := time.ParseDuration(cfg.Sync.Interval); err == nil {
		a.syncTick = d
	}

	// Set up views from config. Fall back to a default if none configured.
	a.views = cfg.Views
	if len(a.views) == 0 {
		a.views = []config.ViewConfig{
			{
				Name:  "all",
				Title: "All Issues",
				Query: "SELECT key, summary, status, priority, assignee FROM issues ORDER BY updated DESC LIMIT 500",
			},
		}
	}

	a.tables = make([]*TableModel, len(a.views))
	return a
}

// Init initializes the model and loads the first view.
func (a *App) Init() tea.Cmd {
	return tea.Batch(
		a.loadView(0),
		syncCmd(a.syncEngine, a.syncTick),
	)
}

// loadView returns a command that queries and loads data for view i.
func (a *App) loadView(i int) tea.Cmd {
	return func() tea.Msg {
		v := a.views[i]
		results, err := a.queryEng.Execute(v.Query)
		if err != nil {
			return viewLoadedMsg{index: i, err: err}
		}

		rows := make([][]string, len(results.Rows))
		for r, row := range results.Rows {
			cells := make([]string, len(row))
			for c, val := range row {
				cells[c] = fmtCell(val)
			}
			rows[r] = cells
		}

		cols := results.Columns
		if len(v.Columns) > 0 {
			// Filter to configured columns.
			colIdx := make(map[string]int, len(cols))
			for i, c := range cols {
				colIdx[c] = i
			}
			filtCols := make([]string, 0, len(v.Columns))
			filtIdxs := make([]int, 0, len(v.Columns))
			for _, vc := range v.Columns {
				if idx, ok := colIdx[vc]; ok {
					filtCols = append(filtCols, vc)
					filtIdxs = append(filtIdxs, idx)
				}
			}
			if len(filtCols) > 0 {
				filtRows := make([][]string, len(rows))
				for r, row := range rows {
					filtRow := make([]string, len(filtIdxs))
					for j, idx := range filtIdxs {
						if idx < len(row) {
							filtRow[j] = row[idx]
						}
					}
					filtRows[r] = filtRow
				}
				cols = filtCols
				rows = filtRows
			}
		}

		return viewLoadedMsg{index: i, columns: cols, rows: rows}
	}
}

type viewLoadedMsg struct {
	index   int
	columns []string
	rows    [][]string
	err     error
}

// Update handles messages and key events.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		if a.tables[a.activeView] != nil {
			a.tables[a.activeView].SetSize(a.width, a.tableHeight())
		}
		return a, nil

	case viewLoadedMsg:
		if msg.err != nil {
			a.err = msg.err.Error()
		} else {
			a.err = ""
			t := NewTableModel(msg.columns, msg.rows)
			t.SetSize(a.width, a.tableHeight())
			a.tables[msg.index] = t
		}
		return a, nil

	case SyncTickMsg:
		a.syncing = true
		a.syncStatus = "syncing..."
		cmds = append(cmds, doSync(a.syncEngine), syncCmd(a.syncEngine, a.syncTick))

	case SyncMsg:
		a.syncing = false
		if msg.Err != nil {
			a.syncStatus = "sync error"
		} else {
			a.syncStatus = "synced " + time.Now().Format("15:04")
		}
		a.lastSync = time.Now()
		// Refresh current view.
		cmds = append(cmds, a.loadView(a.activeView))

	case tea.KeyMsg:
		return a.handleKey(msg, cmds)
	}

	// Pass to filter input if active.
	if a.mode == ModeFilter {
		var cmd tea.Cmd
		a.filterInput, cmd = a.filterInput.Update(msg)
		if a.tables[a.activeView] != nil {
			a.tables[a.activeView].SetFilter(a.filterInput.Value())
		}
		cmds = append(cmds, cmd)
	}

	return a, tea.Batch(cmds...)
}

func (a *App) handleKey(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	t := a.tables[a.activeView]

	// Filter mode: most keys go to the input.
	if a.mode == ModeFilter {
		switch {
		case key.Matches(msg, a.keys.Back):
			a.mode = ModeTable
			a.filterInput.Blur()
			if t != nil {
				t.ClearFilter()
			}
		case key.Matches(msg, a.keys.Select):
			a.mode = ModeTable
			a.filterInput.Blur()
		}
		return a, tea.Batch(cmds...)
	}

	// Detail mode.
	if a.mode == ModeDetail {
		switch {
		case key.Matches(msg, a.keys.Back), key.Matches(msg, a.keys.Select):
			a.mode = ModeTable
			a.detail = nil
		case key.Matches(msg, a.keys.Up):
			if a.detail != nil {
				a.detail.ScrollUp()
			}
		case key.Matches(msg, a.keys.Down):
			if a.detail != nil {
				a.detail.ScrollDown()
			}
		case key.Matches(msg, a.keys.Open):
			if a.detail != nil {
				cmds = append(cmds, openBrowser(a.detail.IssueKey, a.cfg.Jira.URL))
			}
		}
		return a, tea.Batch(cmds...)
	}

	// Normal table mode.
	switch {
	case key.Matches(msg, a.keys.Quit):
		return a, tea.Quit

	case key.Matches(msg, a.keys.Up):
		if t != nil {
			t.MoveUp(1)
		}
	case key.Matches(msg, a.keys.Down):
		if t != nil {
			t.MoveDown(1)
		}
	case key.Matches(msg, a.keys.HalfUp):
		if t != nil {
			t.MoveUp(a.tableHeight() / 2)
		}
	case key.Matches(msg, a.keys.HalfDown):
		if t != nil {
			t.MoveDown(a.tableHeight() / 2)
		}
	case key.Matches(msg, a.keys.PageUp):
		if t != nil {
			t.MoveUp(a.tableHeight())
		}
	case key.Matches(msg, a.keys.PageDown):
		if t != nil {
			t.MoveDown(a.tableHeight())
		}
	case key.Matches(msg, a.keys.GoTop):
		if t != nil {
			t.GoTop()
		}
	case key.Matches(msg, a.keys.GoBottom):
		if t != nil {
			t.GoBottom()
		}

	case key.Matches(msg, a.keys.TabNext):
		a.switchView((a.activeView + 1) % len(a.views))
		cmds = append(cmds, a.loadViewIfNeeded(a.activeView))

	case key.Matches(msg, a.keys.TabPrev):
		a.switchView((a.activeView - 1 + len(a.views)) % len(a.views))
		cmds = append(cmds, a.loadViewIfNeeded(a.activeView))

	case key.Matches(msg, a.keys.Tab1):
		a.switchAndLoad(0)
	case key.Matches(msg, a.keys.Tab2):
		a.switchAndLoad(1)
	case key.Matches(msg, a.keys.Tab3):
		a.switchAndLoad(2)
	case key.Matches(msg, a.keys.Tab4):
		a.switchAndLoad(3)
	case key.Matches(msg, a.keys.Tab5):
		a.switchAndLoad(4)

	case key.Matches(msg, a.keys.Filter):
		a.mode = ModeFilter
		a.filterInput.SetValue("")
		a.filterInput.Focus()

	case key.Matches(msg, a.keys.Back):
		if t != nil && t.FilterText() != "" {
			t.ClearFilter()
		}

	case key.Matches(msg, a.keys.Sort):
		if t != nil && len(t.columns) > 0 {
			// Cycle to next column.
			next := (t.sortCol + 1) % len(t.columns)
			t.SortByColumn(next)
		}

	case key.Matches(msg, a.keys.Select):
		if t != nil {
			row := t.Selected()
			if row != nil && len(t.columns) > 0 {
				a.openDetail(t.columns, row)
			}
		}

	case key.Matches(msg, a.keys.Refresh):
		cmds = append(cmds, a.loadView(a.activeView))

	case key.Matches(msg, a.keys.Open):
		if t != nil {
			row := t.Selected()
			if row != nil {
				for i, col := range t.columns {
					if col == "key" && i < len(row) {
						cmds = append(cmds, openBrowser(row[i], a.cfg.Jira.URL))
					}
				}
			}
		}
	}

	return a, tea.Batch(cmds...)
}

func (a *App) switchView(i int) {
	if i < 0 || i >= len(a.views) {
		return
	}
	a.activeView = i
}

func (a *App) switchAndLoad(i int) {
	if i < 0 || i >= len(a.views) {
		return
	}
	a.activeView = i
}

func (a *App) loadViewIfNeeded(i int) tea.Cmd {
	if a.tables[i] == nil {
		return a.loadView(i)
	}
	return nil
}

func (a *App) openDetail(columns []string, row []string) {
	data := make(map[string]string, len(columns))
	for i, col := range columns {
		if i < len(row) {
			data[col] = row[i]
		}
	}
	issueKey := data["key"]
	a.detail = NewDetailPane(issueKey, data, a.cfg.Jira.URL)
	a.mode = ModeDetail
}

// tableHeight returns the usable table height.
func (a *App) tableHeight() int {
	// Reserve: 1 tab bar + 1 status bar + 1 filter bar
	reserved := 3
	h := a.height - reserved
	if h < 5 {
		h = 5
	}
	return h
}

// View renders the entire TUI.
func (a *App) View() string {
	if a.width == 0 {
		return "Loading..."
	}

	var sb strings.Builder

	// Tab bar.
	sb.WriteString(a.renderTabBar())
	sb.WriteString("\n")

	// Main content.
	if a.err != "" {
		errStyle := lipgloss.NewStyle().Foreground(colorBlocked)
		sb.WriteString(errStyle.Render("Error: " + a.err))
		sb.WriteString("\n")
	} else if a.mode == ModeDetail && a.detail != nil {
		sb.WriteString(a.detail.Render(a.width, a.tableHeight()))
	} else {
		t := a.tables[a.activeView]
		if t == nil {
			sb.WriteString("Loading view...\n")
		} else {
			sb.WriteString(t.Render(a.tableHeight()))
		}
	}

	// Status bar.
	sb.WriteString(a.renderStatusBar())

	return sb.String()
}

func (a *App) renderTabBar() string {
	var tabs []string
	for i, v := range a.views {
		label := v.Title
		if label == "" {
			label = v.Name
		}
		if i < 9 {
			label = fmt.Sprintf("%d:%s", i+1, label)
		}
		if i == a.activeView {
			s := lipgloss.NewStyle().Foreground(colorActiveTab).Bold(true).
				Underline(true).Render(label)
			tabs = append(tabs, s)
		} else {
			s := lipgloss.NewStyle().Foreground(colorTab).Render(label)
			tabs = append(tabs, s)
		}
	}
	return strings.Join(tabs, "  ")
}

func (a *App) renderStatusBar() string {
	var parts []string

	t := a.tables[a.activeView]
	if t != nil {
		count := fmt.Sprintf("%d rows", t.RowCount())
		if t.FilterText() != "" {
			count += fmt.Sprintf(" (filtered: %q)", t.FilterText())
		}
		parts = append(parts, count)
	}

	if a.mode == ModeFilter {
		filterBar := "Filter: " + a.filterInput.View()
		parts = append([]string{filterBar}, parts...)
	}

	if a.syncing {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorSync).Render("⟳ syncing"))
	} else if a.syncStatus != "" {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorTab).Render(a.syncStatus))
	}

	parts = append(parts, "q:quit  /:filter  s:sort  enter:detail  r:refresh")

	return lipgloss.NewStyle().Foreground(colorStatusBar).Render(strings.Join(parts, "  |  "))
}

func fmtCell(v interface{}) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case []byte:
		return string(t)
	case string:
		return t
	default:
		return fmt.Sprintf("%v", t)
	}
}
