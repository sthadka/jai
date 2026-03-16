package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// adfNode mirrors the Atlassian Document Format node structure used in rich-text rendering.
type adfNode struct {
	Type    string                     `json:"type"`
	Text    string                     `json:"text"`
	Content []adfNode                  `json:"content"`
	Marks   []adfMark                  `json:"marks"`
	Attrs   map[string]json.RawMessage `json:"attrs"`
}

type adfMark struct {
	Type  string                     `json:"type"`
	Attrs map[string]json.RawMessage `json:"attrs"`
}

// span is a run of text with optional styling.
type span struct {
	text      string
	bold      bool
	italic    bool
	code      bool
	underline bool
	color     string
}

func (s span) render() string {
	style := lipgloss.NewStyle()
	if s.bold {
		style = style.Bold(true)
	}
	if s.italic {
		style = style.Italic(true)
	}
	if s.code {
		style = style.Background(lipgloss.Color("#2a2a2a")).Foreground(lipgloss.Color("#d4d4d4"))
	}
	if s.underline {
		style = style.Underline(true)
	}
	if s.color != "" && !s.code {
		style = style.Foreground(lipgloss.Color(s.color))
	}
	return style.Render(s.text)
}

// RenderADF converts an ADF JSON blob to a list of display lines at the given width.
func RenderADF(raw json.RawMessage, width int) []string {
	if len(raw) == 0 {
		return nil
	}
	if width < 10 {
		width = 10
	}

	// Handle plain strings (older Jira or simple values).
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return wrapPlain(s, width)
	}

	var root adfNode
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	return renderBlock(&root, width, 0)
}

// renderBlock renders a block-level ADF node into display lines.
func renderBlock(n *adfNode, width, indent int) []string {
	var lines []string
	usable := width - indent

	switch n.Type {
	case "doc":
		for i := range n.Content {
			lines = append(lines, renderBlock(&n.Content[i], width, indent)...)
		}

	case "paragraph":
		spans := collectSpans(n, nil)
		if len(spans) > 0 {
			for _, l := range wrapSpans(spans, usable) {
				lines = append(lines, strings.Repeat(" ", indent)+l)
			}
		}
		lines = append(lines, "") // blank after paragraph

	case "heading":
		level := 1
		if raw, ok := n.Attrs["level"]; ok {
			var l int
			if err := json.Unmarshal(raw, &l); err == nil {
				level = l
			}
		}
		text := collectPlainText(n)
		prefix := strings.Repeat("#", level) + " "
		var style lipgloss.Style
		switch {
		case level <= 1:
			style = lipgloss.NewStyle().Bold(true).Underline(true).Foreground(colorActiveTab)
		case level == 2:
			style = lipgloss.NewStyle().Bold(true).Foreground(colorActiveTab)
		default:
			style = lipgloss.NewStyle().Bold(true).Foreground(colorHeader)
		}
		lines = append(lines, style.Render(prefix+text))
		lines = append(lines, "")

	case "bulletList":
		for i := range n.Content {
			lines = append(lines, renderListItem(&n.Content[i], usable, indent, "• "))
		}
		lines = append(lines, "")

	case "orderedList":
		for i := range n.Content {
			lines = append(lines, renderListItem(&n.Content[i], usable, indent, fmt.Sprintf("%d. ", i+1)))
		}
		lines = append(lines, "")

	case "blockquote":
		quoteStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
		for i := range n.Content {
			for _, l := range renderBlock(&n.Content[i], usable-2, 0) {
				if l == "" {
					lines = append(lines, quoteStyle.Render("│"))
				} else {
					lines = append(lines, quoteStyle.Render("│ ")+l)
				}
			}
		}

	case "codeBlock":
		lang := ""
		if raw, ok := n.Attrs["language"]; ok {
			json.Unmarshal(raw, &lang) //nolint
		}
		codeStyle := lipgloss.NewStyle().Background(lipgloss.Color("#1e1e1e")).Foreground(lipgloss.Color("#d4d4d4"))
		if lang != "" {
			lines = append(lines, lipgloss.NewStyle().Foreground(colorTab).Render("  ╭─ "+lang))
		}
		text := collectPlainText(n)
		for _, l := range strings.Split(text, "\n") {
			// Pad to block width for consistent background.
			if len(l) > usable-4 {
				l = l[:usable-4]
			}
			padded := fmt.Sprintf("  %-*s  ", usable-4, l)
			lines = append(lines, codeStyle.Render(padded))
		}
		lines = append(lines, "")

	case "rule":
		lines = append(lines, lipgloss.NewStyle().Foreground(colorStatusBar).Render(strings.Repeat("─", usable)))

	case "table":
		lines = append(lines, renderADFTable(n, usable)...)
		lines = append(lines, "")

	case "mediaSingle", "media":
		lines = append(lines, lipgloss.NewStyle().Foreground(colorTab).Render("  [attachment]"))
		lines = append(lines, "")

	case "inlineCard", "embedCard":
		url := ""
		if raw, ok := n.Attrs["url"]; ok {
			json.Unmarshal(raw, &url) //nolint
		}
		if url != "" {
			style := lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color("#5c8fc9"))
			lines = append(lines, "  "+style.Render(url))
			lines = append(lines, "")
		}

	case "listItem":
		for i := range n.Content {
			lines = append(lines, renderBlock(&n.Content[i], width, indent)...)
		}

	case "hardBreak":
		lines = append(lines, "")
	}

	return lines
}

// renderListItem renders a single list item with a given prefix.
func renderListItem(item *adfNode, width, indent int, prefix string) string {
	prefixW := len(prefix)
	prefixStyle := lipgloss.NewStyle().Foreground(colorSync)
	var parts []string

	for i := range item.Content {
		spans := collectSpans(&item.Content[i], nil)
		if len(spans) == 0 {
			continue
		}
		wrapped := wrapSpans(spans, width-prefixW)
		for j, l := range wrapped {
			if i == 0 && j == 0 {
				parts = append(parts, strings.Repeat(" ", indent)+prefixStyle.Render(prefix)+l)
			} else {
				parts = append(parts, strings.Repeat(" ", indent+prefixW)+l)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// collectSpans flattens inline ADF content into a slice of styled spans.
func collectSpans(n *adfNode, inherited []adfMark) []span {
	if n.Type == "text" && n.Text != "" {
		allMarks := append(inherited, n.Marks...)
		return []span{applyMarks(n.Text, allMarks)}
	}
	if n.Type == "hardBreak" {
		return []span{{text: "\n"}}
	}
	if n.Type == "mention" {
		name := collectPlainText(n)
		return []span{{text: "@" + name, color: "#5c8fc9"}}
	}
	if n.Type == "emoji" {
		text := ""
		if raw, ok := n.Attrs["text"]; ok {
			json.Unmarshal(raw, &text) //nolint
		}
		return []span{{text: text}}
	}

	var spans []span
	allMarks := append(inherited, n.Marks...)
	for i := range n.Content {
		spans = append(spans, collectSpans(&n.Content[i], allMarks)...)
	}
	return spans
}

func applyMarks(text string, marks []adfMark) span {
	s := span{text: text}
	for _, m := range marks {
		switch m.Type {
		case "strong":
			s.bold = true
		case "em":
			s.italic = true
		case "code":
			s.code = true
		case "link":
			s.underline = true
			s.color = "#5c8fc9"
		case "textColor":
			if raw, ok := m.Attrs["color"]; ok {
				json.Unmarshal(raw, &s.color) //nolint
			}
		case "strike":
			s.italic = true // best approximation in terminal
		case "subsup":
			// ignore
		}
	}
	return s
}

// wrapSpans word-wraps a list of styled spans at the given visual width.
// Returns one rendered string per line.
func wrapSpans(spans []span, width int) []string {
	if width < 1 {
		width = 80
	}

	type word struct {
		rendered string
		vlen     int // visual (plain text) length
		newline  bool
	}

	var words []word
	for _, s := range spans {
		// Split on explicit newlines first.
		parts := strings.Split(s.text, "\n")
		for pi, part := range parts {
			if pi > 0 {
				words = append(words, word{newline: true})
			}
			if part == "" {
				continue
			}
			for _, w := range strings.Fields(part) {
				sc := s
				sc.text = w
				words = append(words, word{rendered: sc.render(), vlen: len(w)})
			}
		}
	}

	var lines []string
	var cur []string
	curLen := 0

	flush := func() {
		lines = append(lines, strings.Join(cur, " "))
		cur = nil
		curLen = 0
	}

	for _, w := range words {
		if w.newline {
			flush()
			continue
		}
		if curLen > 0 && curLen+1+w.vlen > width {
			flush()
		}
		cur = append(cur, w.rendered)
		if curLen == 0 {
			curLen = w.vlen
		} else {
			curLen += 1 + w.vlen
		}
	}
	if len(cur) > 0 {
		flush()
	}

	return lines
}

// collectPlainText recursively extracts all plain text from an ADF node.
func collectPlainText(n *adfNode) string {
	if n.Type == "text" {
		return n.Text
	}
	var sb strings.Builder
	for i := range n.Content {
		sb.WriteString(collectPlainText(&n.Content[i]))
	}
	return sb.String()
}

// wrapPlain wraps plain text (no ADF) at the given width.
func wrapPlain(text string, width int) []string {
	var out []string
	for _, para := range strings.Split(text, "\n") {
		wrapped := wrapSpans([]span{{text: para}}, width)
		out = append(out, wrapped...)
		out = append(out, "")
	}
	return out
}

// renderADFTable renders an ADF table node as ASCII box-drawing characters.
func renderADFTable(n *adfNode, width int) []string {
	type row struct {
		cells  []string
		header bool
	}
	var rows []row

	for i := range n.Content {
		rn := &n.Content[i]
		if rn.Type != "tableRow" {
			continue
		}
		var cells []string
		isHeader := false
		for j := range rn.Content {
			cell := &rn.Content[j]
			if cell.Type == "tableHeader" {
				isHeader = true
			}
			cells = append(cells, strings.TrimSpace(collectPlainText(cell)))
		}
		rows = append(rows, row{cells: cells, header: isHeader})
	}

	if len(rows) == 0 {
		return nil
	}

	// Compute column count and widths.
	numCols := 0
	for _, r := range rows {
		if len(r.cells) > numCols {
			numCols = len(r.cells)
		}
	}
	if numCols == 0 {
		return nil
	}

	colW := make([]int, numCols)
	for _, r := range rows {
		for i, cell := range r.cells {
			if i < numCols && len(cell) > colW[i] {
				colW[i] = len(cell)
			}
		}
	}

	// Cap total width to available space.
	totalW := numCols + 1
	for _, w := range colW {
		totalW += w + 2
	}
	if totalW > width {
		scale := float64(width-numCols-1) / float64(totalW-numCols-1)
		for i := range colW {
			colW[i] = max(3, int(float64(colW[i])*scale))
		}
	}

	border := func(left, mid, right, horiz string) string {
		var sb strings.Builder
		sb.WriteString(left)
		for i, w := range colW {
			sb.WriteString(strings.Repeat(horiz, w+2))
			if i < len(colW)-1 {
				sb.WriteString(mid)
			}
		}
		sb.WriteString(right)
		return sb.String()
	}

	renderRow := func(r row) string {
		var sb strings.Builder
		sb.WriteString("│")
		for i := 0; i < numCols; i++ {
			cell := ""
			if i < len(r.cells) {
				cell = r.cells[i]
			}
			if len(cell) > colW[i] {
				cell = cell[:colW[i]-2] + ".."
			}
			content := fmt.Sprintf(" %-*s ", colW[i], cell)
			if r.header {
				content = lipgloss.NewStyle().Bold(true).Render(content)
			}
			sb.WriteString(content + "│")
		}
		return sb.String()
	}

	var lines []string
	lines = append(lines, border("┌", "┬", "┐", "─"))
	for i, r := range rows {
		lines = append(lines, renderRow(r))
		if i < len(rows)-1 {
			if rows[i].header {
				lines = append(lines, border("╞", "╪", "╡", "═"))
			} else {
				lines = append(lines, border("├", "┼", "┤", "─"))
			}
		}
	}
	lines = append(lines, border("└", "┴", "┘", "─"))
	return lines
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
