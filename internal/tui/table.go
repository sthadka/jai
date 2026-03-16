package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// TableModel renders a sortable, scrollable, filterable table.
type TableModel struct {
	columns    []string
	rows       [][]string   // all rows
	filtered   [][]string   // filtered rows (subset of rows)
	cursor     int
	offset     int
	height     int
	width      int
	sortCol    int
	sortDesc   bool
	filterText string

	// Grouping.
	groupBy    int // column index, -1 = no grouping
	collapsed  map[string]bool

	colorRules interface{} // []config.ColorRule — stored as interface to avoid import cycle
}

// NewTableModel creates a new table model.
func NewTableModel(columns []string, rows [][]string) *TableModel {
	t := &TableModel{
		columns:   columns,
		rows:      rows,
		filtered:  rows,
		sortCol:   -1,
		groupBy:   -1,
		collapsed: make(map[string]bool),
	}
	return t
}

// SetSize updates the display dimensions.
func (t *TableModel) SetSize(w, h int) {
	t.width = w
	t.height = h
}

// MoveUp moves the cursor up by n rows.
func (t *TableModel) MoveUp(n int) {
	t.cursor -= n
	if t.cursor < 0 {
		t.cursor = 0
	}
	t.clampOffset()
}

// MoveDown moves the cursor down by n rows.
func (t *TableModel) MoveDown(n int) {
	t.cursor += n
	if t.cursor >= len(t.filtered) {
		t.cursor = len(t.filtered) - 1
	}
	if t.cursor < 0 {
		t.cursor = 0
	}
	t.clampOffset()
}

// GoTop moves the cursor to the first row.
func (t *TableModel) GoTop() {
	t.cursor = 0
	t.offset = 0
}

// GoBottom moves the cursor to the last row.
func (t *TableModel) GoBottom() {
	t.cursor = len(t.filtered) - 1
	t.clampOffset()
}

// Selected returns the currently selected row, or nil.
func (t *TableModel) Selected() []string {
	if t.cursor < 0 || t.cursor >= len(t.filtered) {
		return nil
	}
	return t.filtered[t.cursor]
}

// SelectedIndex returns the cursor position.
func (t *TableModel) SelectedIndex() int {
	return t.cursor
}

// SetFilter applies a filter to the rows.
func (t *TableModel) SetFilter(text string) {
	t.filterText = text
	if text == "" {
		t.filtered = t.rows
		t.cursor = 0
		t.offset = 0
		return
	}

	lower := strings.ToLower(text)
	filtered := make([][]string, 0, len(t.rows))
	for _, row := range t.rows {
		for _, cell := range row {
			if strings.Contains(strings.ToLower(cell), lower) {
				filtered = append(filtered, row)
				break
			}
		}
	}
	t.filtered = filtered
	t.cursor = 0
	t.offset = 0
}

// ClearFilter removes the current filter.
func (t *TableModel) ClearFilter() {
	t.filterText = ""
	t.filtered = t.rows
	t.cursor = 0
	t.offset = 0
}

// SortByColumn sorts by the given column index, toggling direction if same column.
func (t *TableModel) SortByColumn(col int) {
	if t.sortCol == col {
		t.sortDesc = !t.sortDesc
	} else {
		t.sortCol = col
		t.sortDesc = false
	}
	t.applySort()
}

func (t *TableModel) applySort() {
	if t.sortCol < 0 {
		return
	}
	col := t.sortCol
	desc := t.sortDesc
	sort.SliceStable(t.rows, func(i, j int) bool {
		a, b := "", ""
		if col < len(t.rows[i]) {
			a = t.rows[i][col]
		}
		if col < len(t.rows[j]) {
			b = t.rows[j][col]
		}
		if desc {
			return a > b
		}
		return a < b
	})
	// Re-apply filter after sort.
	if t.filterText != "" {
		t.SetFilter(t.filterText)
	} else {
		t.filtered = t.rows
	}
}

// UpdateRows replaces the row data (e.g. after a sync refresh).
func (t *TableModel) UpdateRows(rows [][]string) {
	t.rows = rows
	t.applySort()
	if t.filterText != "" {
		t.SetFilter(t.filterText)
	} else {
		t.filtered = rows
	}
	if t.cursor >= len(t.filtered) {
		t.cursor = len(t.filtered) - 1
	}
	if t.cursor < 0 {
		t.cursor = 0
	}
}

// Render returns the table as a string for the given height.
func (t *TableModel) Render(height int) string {
	if len(t.columns) == 0 {
		return ""
	}

	widths := t.computeWidths()
	var sb strings.Builder

	// Header.
	headerStyle := lipgloss.NewStyle().Foreground(colorHeader).Bold(true)
	for i, col := range t.columns {
		label := strings.ToUpper(col)
		if t.sortCol == i {
			if t.sortDesc {
				label += " ▼"
			} else {
				label += " ▲"
			}
		}
		cell := fmt.Sprintf("%-*s", widths[i], label)
		if len(cell) > widths[i]+2 {
			cell = cell[:widths[i]]
		}
		sb.WriteString(headerStyle.Render(cell))
		if i < len(t.columns)-1 {
			sb.WriteString("  ")
		}
	}
	sb.WriteString("\n")

	// Separator.
	sepStyle := lipgloss.NewStyle().Foreground(colorStatusBar)
	for i, w := range widths {
		sb.WriteString(sepStyle.Render(strings.Repeat("─", w)))
		if i < len(widths)-1 {
			sb.WriteString("  ")
		}
	}
	sb.WriteString("\n")

	// Data rows.
	dataHeight := height - 2 // header + separator
	if dataHeight < 1 {
		dataHeight = 1
	}

	end := t.offset + dataHeight
	if end > len(t.filtered) {
		end = len(t.filtered)
	}

	for i := t.offset; i < end; i++ {
		row := t.filtered[i]
		selected := i == t.cursor

		rowStyle := lipgloss.NewStyle()
		if selected {
			rowStyle = rowStyle.Background(colorSelected).Bold(true)
		}

		for j := range t.columns {
			cell := ""
			if j < len(row) {
				cell = row[j]
			}
			if len(cell) > widths[j] {
				cell = cell[:widths[j]-3] + "..."
			}
			rendered := fmt.Sprintf("%-*s", widths[j], cell)
			sb.WriteString(rowStyle.Render(rendered))
			if j < len(t.columns)-1 {
				sb.WriteString("  ")
			}
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// computeWidths calculates column widths based on content, capped at reasonable sizes.
func (t *TableModel) computeWidths() []int {
	widths := make([]int, len(t.columns))
	for i, col := range t.columns {
		widths[i] = len(col)
	}

	sampleSize := 200
	if len(t.filtered) < sampleSize {
		sampleSize = len(t.filtered)
	}
	for _, row := range t.filtered[:sampleSize] {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	// Cap: key=10, summary=60, others=20
	caps := map[string]int{
		"key": 12, "summary": 55, "description": 60,
	}
	defaultCap := 25

	for i, colName := range t.columns {
		cap := defaultCap
		if c, ok := caps[colName]; ok {
			cap = c
		}
		if widths[i] > cap {
			widths[i] = cap
		}
	}

	return widths
}

func (t *TableModel) clampOffset() {
	visibleRows := t.height - 3 // header + separator + status
	if visibleRows < 1 {
		visibleRows = 1
	}
	if t.cursor < t.offset {
		t.offset = t.cursor
	}
	if t.cursor >= t.offset+visibleRows {
		t.offset = t.cursor - visibleRows + 1
	}
	if t.offset < 0 {
		t.offset = 0
	}
}

// RowCount returns the number of filtered rows.
func (t *TableModel) RowCount() int {
	return len(t.filtered)
}

// FilterText returns the current filter string.
func (t *TableModel) FilterText() string {
	return t.filterText
}
