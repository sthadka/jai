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

	// Grouping: groupVals is parallel to rows; filtGroupVals parallel to filtered.
	groupBy       int      // column index in rows used for group headers, -1 = none
	groupVals     []string // group value per row (parallel to rows)
	filtGroupVals []string // group value per filtered row

	colorRules interface{} // []config.ColorRule — stored as interface to avoid import cycle
}

// NewTableModel creates a new table model.
func NewTableModel(columns []string, rows [][]string) *TableModel {
	t := &TableModel{
		columns:  columns,
		rows:     rows,
		filtered: rows,
		sortCol:  -1,
		groupBy:  -1,
	}
	return t
}

// SetGroupBy configures the table to show group headers when the value in
// groupCol (index into each row) changes. vals is a parallel slice to rows
// containing the group label for each row. Rows are assumed to already be
// ordered so that equal group values are adjacent.
func (t *TableModel) SetGroupBy(groupCol int, vals []string) {
	t.groupBy = groupCol
	t.groupVals = vals
	t.filtGroupVals = vals // recomputed when filter is applied
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
		t.filtGroupVals = t.groupVals
		t.cursor = 0
		t.offset = 0
		return
	}

	lower := strings.ToLower(text)
	filtered := make([][]string, 0, len(t.rows))
	var filtGV []string
	for i, row := range t.rows {
		for _, cell := range row {
			if strings.Contains(strings.ToLower(cell), lower) {
				filtered = append(filtered, row)
				if t.groupVals != nil && i < len(t.groupVals) {
					filtGV = append(filtGV, t.groupVals[i])
				}
				break
			}
		}
	}
	t.filtered = filtered
	t.filtGroupVals = filtGV
	t.cursor = 0
	t.offset = 0
}

// ClearFilter removes the current filter.
func (t *TableModel) ClearFilter() {
	t.filterText = ""
	t.filtered = t.rows
	t.filtGroupVals = t.groupVals
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
		t.filtGroupVals = t.groupVals
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

	// When grouping is active, account for group header rows in the viewport.
	grouped := t.groupBy >= 0 && len(t.filtGroupVals) > 0
	groupStyle := lipgloss.NewStyle().Foreground(colorGroupHeader).Bold(true)

	// Seed lastGroup from the row just before the viewport so we don't emit a
	// spurious header when the viewport starts mid-group (group member above fold).
	linesUsed := 0
	lastGroup := "\x00"
	if grouped && t.offset > 0 && t.offset-1 < len(t.filtGroupVals) {
		lastGroup = t.filtGroupVals[t.offset-1]
	}
	for i := t.offset; i < len(t.filtered) && linesUsed < dataHeight; i++ {
		row := t.filtered[i]
		selected := i == t.cursor

		// Group header.
		if grouped {
			gv := ""
			if i < len(t.filtGroupVals) {
				gv = t.filtGroupVals[i]
			}
			if gv != lastGroup {
				if linesUsed >= dataHeight {
					break
				}
				label := gv
				if label == "" {
					label = "(no parent)"
				}
				// Add ↑ hint when a group starts mid-viewport (its first member is
				// above the fold), so the user knows to scroll up for the parent row.
				midGroup := i > 0 && i == t.offset && t.offset > 0
				if midGroup {
					label = "↑ " + label
				}
				// Render full-width group header.
				totalW := 0
				for k, w := range widths {
					totalW += w
					if k < len(widths)-1 {
						totalW += 2
					}
				}
				header := fmt.Sprintf("── %s ", label)
				if len(header) < totalW {
					header += strings.Repeat("─", totalW-len(header))
				}
				sb.WriteString(groupStyle.Render(header))
				sb.WriteString("\n")
				linesUsed++
				lastGroup = gv
			}
		}

		if linesUsed >= dataHeight {
			break
		}

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
		linesUsed++
	}

	return sb.String()
}

// computeWidths calculates column widths. Fixed columns are capped; the first
// "expand" column (summary or description) gets all remaining terminal width.
func (t *TableModel) computeWidths() []int {
	n := len(t.columns)
	widths := make([]int, n)
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

	// Find the column that should expand to fill available space.
	expandCol := -1
	for i, colName := range t.columns {
		if colName == "summary" || colName == "description" {
			expandCol = i
			break
		}
	}

	// Fixed caps for non-expand columns.
	fixedCaps := map[string]int{"key": 12}
	defaultCap := 25

	naturalExpand := 0
	if expandCol >= 0 {
		naturalExpand = widths[expandCol]
	}

	for i, colName := range t.columns {
		if i == expandCol {
			continue
		}
		cap := defaultCap
		if c, ok := fixedCaps[colName]; ok {
			cap = c
		}
		if widths[i] > cap {
			widths[i] = cap
		}
	}

	if expandCol >= 0 && t.width > 0 {
		// Space used by fixed columns + 2-char separators between all columns.
		used := (n - 1) * 2
		for i, w := range widths {
			if i != expandCol {
				used += w
			}
		}
		available := t.width - used
		if available < 10 {
			available = 10
		}
		// Don't pad beyond natural content width.
		if naturalExpand < available {
			widths[expandCol] = naturalExpand
		} else {
			widths[expandCol] = available
		}
	} else if expandCol >= 0 {
		// No terminal width info — fall back to a reasonable cap.
		if widths[expandCol] > 60 {
			widths[expandCol] = 60
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
