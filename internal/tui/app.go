package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/query"
	synce "github.com/sthadka/jai/internal/sync"
)

// Mode represents the current UI mode.
type Mode int

const (
	ModeTable       Mode = iota
	ModeFilter           // filter input active
	ModeDetail           // detail pane open
	ModeDetailLoad       // loading detail data
	ModeFieldPicker      // field name picker modal (comma key)
	ModeFieldValue       // value input for chosen field
)

// App is the root bubbletea model.
type App struct {
	cfg        *config.Config
	queryEng   *query.Engine
	syncEngine *synce.Engine
	database   *db.DB

	views      []config.ViewConfig
	activeView int
	tables     []*TableModel

	mode        Mode
	filterInput textinput.Model
	detail      *DetailPane

	// Field picker state (ModeFieldPicker / ModeFieldValue).
	fieldPickerInput    textinput.Model
	fieldPickerAll      []*db.FieldMapping
	fieldPickerFiltered []*db.FieldMapping
	fieldPickerCursor   int
	fieldPickerChosen   *db.FieldMapping
	fieldValueInput     textinput.Model
	fieldValueCurrent   string

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
func New(cfg *config.Config, queryEng *query.Engine, syncEng *synce.Engine, database *db.DB) *App {
	ti := textinput.New()
	ti.Placeholder = "filter..."
	ti.CharLimit = 100

	fpi := textinput.New()
	fpi.Placeholder = "field name..."
	fpi.CharLimit = 60

	fvi := textinput.New()
	fvi.Placeholder = "new value..."
	fvi.CharLimit = 256

	a := &App{
		cfg:             cfg,
		queryEng:        queryEng,
		syncEngine:      syncEng,
		database:        database,
		keys:            DefaultKeys(),
		filterInput:     ti,
		fieldPickerInput: fpi,
		fieldValueInput: fvi,
		syncTick:        15 * time.Minute,
	}

	if d, err := time.ParseDuration(cfg.Sync.Interval); err == nil {
		a.syncTick = d
	}

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

// detailLoadedMsg carries the result of an async detail data load.
type detailLoadedMsg struct {
	data        *DetailData
	err         error
}

// loadDetailCmd asynchronously fetches all data needed for the detail pane.
func (a *App) loadDetailCmd(issueKey string) tea.Cmd {
	return func() tea.Msg {
		issue, err := a.database.GetIssue(issueKey)
		if err != nil || issue == nil {
			return detailLoadedMsg{err: fmt.Errorf("issue %s not found", issueKey)}
		}

		projectKey := issueStr(issue, "project")
		projectName := a.database.GetProjectName(projectKey)
		chain := BuildParentChain(a.database, issue)
		comments, _ := a.database.GetComments(issueKey)
		fieldMap, _ := a.database.AllFieldMappings()

		return detailLoadedMsg{
			data: &DetailData{
				IssueKey:     issueKey,
				ProjectName:  projectName,
				Chain:        chain,
				Issue:        issue,
				Comments:     comments,
				FieldMap:     fieldMap,
				SidebarExtra: a.cfg.Detail.SidebarFields,
			},
		}
	}
}

// loadFieldMapCmd fetches field mappings for the field picker.
func (a *App) loadFieldMapCmd() tea.Cmd {
	return func() tea.Msg {
		fields, _ := a.database.AllFieldMappings()
		return fieldMapLoadedMsg{fields: fields}
	}
}

type fieldMapLoadedMsg struct {
	fields []*db.FieldMapping
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

	case detailLoadedMsg:
		if msg.err != nil {
			a.err = msg.err.Error()
			a.mode = ModeTable
		} else {
			a.detail = NewDetailPane(msg.data, a.cfg.Jira.URL, a.width)
			a.mode = ModeDetail
			a.err = ""
		}
		return a, nil

	case fieldMapLoadedMsg:
		a.fieldPickerAll = msg.fields
		a.applyFieldFilter()
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
		cmds = append(cmds, a.loadView(a.activeView))

	case tea.KeyMsg:
		return a.handleKey(msg, cmds)
	}

	// Pass to active text input.
	switch a.mode {
	case ModeFilter:
		var cmd tea.Cmd
		a.filterInput, cmd = a.filterInput.Update(msg)
		if a.tables[a.activeView] != nil {
			a.tables[a.activeView].SetFilter(a.filterInput.Value())
		}
		cmds = append(cmds, cmd)

	case ModeFieldPicker:
		var cmd tea.Cmd
		a.fieldPickerInput, cmd = a.fieldPickerInput.Update(msg)
		a.applyFieldFilter()
		cmds = append(cmds, cmd)

	case ModeFieldValue:
		var cmd tea.Cmd
		a.fieldValueInput, cmd = a.fieldValueInput.Update(msg)
		cmds = append(cmds, cmd)
	}

	return a, tea.Batch(cmds...)
}

func (a *App) handleKey(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	t := a.tables[a.activeView]

	// ── Filter mode ──────────────────────────────────────────────────────────
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

	// ── Detail mode ──────────────────────────────────────────────────────────
	if a.mode == ModeDetail {
		switch {
		case key.Matches(msg, a.keys.Back):
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
		case key.Matches(msg, a.keys.HalfUp):
			for i := 0; i < a.tableHeight()/2; i++ {
				if a.detail != nil {
					a.detail.ScrollUp()
				}
			}
		case key.Matches(msg, a.keys.HalfDown):
			for i := 0; i < a.tableHeight()/2; i++ {
				if a.detail != nil {
					a.detail.ScrollDown()
				}
			}
		case key.Matches(msg, a.keys.Open):
			if a.detail != nil {
				cmds = append(cmds, openBrowser(a.detail.IssueKey(), a.cfg.Jira.URL))
			}
		case key.Matches(msg, a.keys.FieldPicker):
			// Open the field picker modal.
			a.fieldPickerInput.SetValue("")
			a.fieldPickerInput.Focus()
			a.fieldPickerCursor = 0
			a.mode = ModeFieldPicker
			cmds = append(cmds, a.loadFieldMapCmd())
		}
		return a, tea.Batch(cmds...)
	}

	// ── Loading detail ───────────────────────────────────────────────────────
	if a.mode == ModeDetailLoad {
		if key.Matches(msg, a.keys.Back) {
			a.mode = ModeTable
		}
		return a, tea.Batch(cmds...)
	}

	// ── Field picker mode ────────────────────────────────────────────────────
	if a.mode == ModeFieldPicker {
		switch {
		case key.Matches(msg, a.keys.Back):
			a.mode = ModeDetail
			a.fieldPickerInput.Blur()
		case key.Matches(msg, a.keys.Up):
			if a.fieldPickerCursor > 0 {
				a.fieldPickerCursor--
			}
		case key.Matches(msg, a.keys.Down):
			if a.fieldPickerCursor < len(a.fieldPickerFiltered)-1 {
				a.fieldPickerCursor++
			}
		case key.Matches(msg, a.keys.Select):
			if a.fieldPickerCursor < len(a.fieldPickerFiltered) {
				chosen := a.fieldPickerFiltered[a.fieldPickerCursor]
				a.fieldPickerChosen = chosen
				a.fieldPickerInput.Blur()
				// Find current value from detail data.
				a.fieldValueCurrent = ""
				if a.detail != nil && a.detail.data != nil {
					a.fieldValueCurrent = issueStr(a.detail.data.Issue, chosen.Name)
				}
				a.fieldValueInput.SetValue("")
				a.fieldValueInput.Focus()
				a.mode = ModeFieldValue
			}
		}
		return a, tea.Batch(cmds...)
	}

	// ── Field value mode ─────────────────────────────────────────────────────
	if a.mode == ModeFieldValue {
		switch {
		case key.Matches(msg, a.keys.Back):
			a.mode = ModeFieldPicker
			a.fieldValueInput.Blur()
			a.fieldPickerInput.Focus()
		case key.Matches(msg, a.keys.Select):
			if a.fieldPickerChosen != nil && a.detail != nil {
				newVal := a.fieldValueInput.Value()
				issueKey := a.detail.IssueKey()
				fieldName := a.fieldPickerChosen.JiraID
				payload := marshalSetPayload(fieldName, newVal)
				_ = a.database.InsertPendingChange(issueKey, "set_field", payload)
			}
			a.fieldValueInput.Blur()
			a.mode = ModeDetail
		}
		return a, tea.Batch(cmds...)
	}

	// ── Normal table mode ────────────────────────────────────────────────────
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
			next := (t.sortCol + 1) % len(t.columns)
			t.SortByColumn(next)
		}

	case key.Matches(msg, a.keys.Select):
		if t != nil {
			row := t.Selected()
			if row != nil {
				issueKey := ""
				for i, col := range t.columns {
					if col == "key" && i < len(row) {
						issueKey = row[i]
					}
				}
				if issueKey != "" {
					a.mode = ModeDetailLoad
					cmds = append(cmds, a.loadDetailCmd(issueKey))
				}
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

// applyFieldFilter filters a.fieldPickerAll by the current input text.
func (a *App) applyFieldFilter() {
	filter := strings.ToLower(a.fieldPickerInput.Value())
	if filter == "" {
		a.fieldPickerFiltered = a.fieldPickerAll
		return
	}
	var out []*db.FieldMapping
	for _, f := range a.fieldPickerAll {
		if strings.Contains(strings.ToLower(f.Name), filter) ||
			strings.Contains(strings.ToLower(f.JiraName), filter) {
			out = append(out, f)
		}
	}
	a.fieldPickerFiltered = out
	if a.fieldPickerCursor >= len(out) {
		a.fieldPickerCursor = 0
	}
}

// tableHeight returns the usable table height.
func (a *App) tableHeight() int {
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
	} else if a.mode == ModeDetailLoad {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorTab).Render("Loading..."))
		sb.WriteString("\n")
	} else if (a.mode == ModeDetail || a.mode == ModeFieldPicker || a.mode == ModeFieldValue) && a.detail != nil {
		detailContent := a.detail.Render(a.width, a.tableHeight())
		sb.WriteString(detailContent)
		if a.mode == ModeFieldPicker {
			sb.WriteString("\n" + a.renderFieldPickerModal())
		} else if a.mode == ModeFieldValue {
			sb.WriteString("\n" + a.renderFieldValueModal())
		}
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
	if t != nil && (a.mode == ModeTable || a.mode == ModeFilter) {
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

	switch a.mode {
	case ModeDetail:
		parts = append(parts, "q:quit  esc:back  o:browser  ,:edit  ↑↓:scroll")
	case ModeFieldPicker:
		parts = append(parts, "↑↓:select  enter:choose  esc:back")
	case ModeFieldValue:
		parts = append(parts, "enter:save  esc:back")
	default:
		parts = append(parts, "q:quit  /:filter  s:sort  enter:detail  r:refresh")
	}

	return lipgloss.NewStyle().Foreground(colorStatusBar).Render(strings.Join(parts, "  |  "))
}

// renderFieldPickerModal renders the field picker modal overlay.
func (a *App) renderFieldPickerModal() string {
	w := 50
	if w > a.width-4 {
		w = a.width - 4
	}
	maxVisible := 8

	borderStyle := lipgloss.NewStyle().Foreground(colorHeader)
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorActiveTab)
	selectedStyle := lipgloss.NewStyle().Background(colorSelected).Foreground(colorActiveTab)
	normalStyle := lipgloss.NewStyle().Foreground(colorTab)

	var sb strings.Builder
	inner := w - 4

	line := func(content string) {
		sb.WriteString(borderStyle.Render("│") + " " + content + " " + borderStyle.Render("│") + "\n")
	}
	hline := func(l, r string) {
		sb.WriteString(borderStyle.Render(l+strings.Repeat("─", w-2)+r) + "\n")
	}

	hline("╭", "╮")
	line(titleStyle.Render(fmt.Sprintf("%-*s", inner, "Edit field  "+a.detail.IssueKey())))
	line(lipgloss.NewStyle().Foreground(colorStatusBar).Render(strings.Repeat("─", inner)))
	line(a.fieldPickerInput.View())

	// Visible window of filtered results.
	start := 0
	if a.fieldPickerCursor >= maxVisible {
		start = a.fieldPickerCursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(a.fieldPickerFiltered) {
		end = len(a.fieldPickerFiltered)
	}

	for i := start; i < end; i++ {
		f := a.fieldPickerFiltered[i]
		label := fmt.Sprintf("%-*s", inner, f.JiraName+" ("+f.Name+")")
		if len(label) > inner {
			label = label[:inner]
		}
		if i == a.fieldPickerCursor {
			line(selectedStyle.Render(label))
		} else {
			line(normalStyle.Render(label))
		}
	}
	if len(a.fieldPickerFiltered) == 0 {
		line(normalStyle.Render(fmt.Sprintf("%-*s", inner, "(no matches)")))
	}

	hline("╰", "╯")
	return sb.String()
}

// renderFieldValueModal renders the value input modal overlay.
func (a *App) renderFieldValueModal() string {
	if a.fieldPickerChosen == nil {
		return ""
	}
	w := 50
	if w > a.width-4 {
		w = a.width - 4
	}

	borderStyle := lipgloss.NewStyle().Foreground(colorHeader)
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorActiveTab)
	labelStyle := lipgloss.NewStyle().Foreground(colorTab)

	var sb strings.Builder
	inner := w - 4

	line := func(content string) {
		sb.WriteString(borderStyle.Render("│") + " " + content + " " + borderStyle.Render("│") + "\n")
	}
	hline := func(l, r string) {
		sb.WriteString(borderStyle.Render(l+strings.Repeat("─", w-2)+r) + "\n")
	}

	field := a.fieldPickerChosen
	hline("╭", "╮")
	line(titleStyle.Render(fmt.Sprintf("%-*s", inner, "Edit: "+field.JiraName)))
	line(lipgloss.NewStyle().Foreground(colorStatusBar).Render(strings.Repeat("─", inner)))

	cur := a.fieldValueCurrent
	if cur == "" {
		cur = "(empty)"
	}
	line(labelStyle.Render(fmt.Sprintf("%-*s", inner, "Current: "+cur)))
	line("")
	line(labelStyle.Render(fmt.Sprintf("%-*s", inner, "New value:")))
	line(a.fieldValueInput.View())

	hline("╰", "╯")
	return sb.String()
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

// marshalSetPayload builds the JSON payload for a set_field pending change.
func marshalSetPayload(fieldID, value string) string {
	payload := map[string]string{"field": fieldID, "value": value}
	b, _ := json.Marshal(payload)
	return string(b)
}
