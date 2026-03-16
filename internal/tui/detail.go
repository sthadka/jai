package tui

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sthadka/jai/internal/db"
)

// ChainItem is one step in the breadcrumb parent chain.
type ChainItem struct {
	Key     string
	Summary string
}

// DetailData holds all data needed to render the issue detail pane.
type DetailData struct {
	IssueKey    string
	ProjectName string // display name, e.g. "Rox Platform"
	Chain       []ChainItem
	Issue       map[string]interface{} // full row from issues table
	Comments    []*db.Comment
	FieldMap    []*db.FieldMapping
	SidebarExtra []string // jira_name values from config.Detail.SidebarFields
}

// DetailPane renders a rich two-panel issue view.
type DetailPane struct {
	data        *DetailData
	jiraURL     string
	scrollOffset int
	leftLines   []string  // pre-rendered left panel lines
	rightLines  []string  // pre-rendered right panel lines (sidebar, static)
}

// NewDetailPane creates a DetailPane from loaded data.
func NewDetailPane(data *DetailData, jiraURL string, width int) *DetailPane {
	d := &DetailPane{data: data, jiraURL: jiraURL}
	d.rebuild(width)
	return d
}

// IssueKey returns the issue key for this detail pane.
func (d *DetailPane) IssueKey() string {
	if d.data == nil {
		return ""
	}
	return d.data.IssueKey
}

func (d *DetailPane) rebuild(width int) {
	leftW, rightW := panelWidths(width)
	d.leftLines = d.buildLeft(leftW)
	d.rightLines = d.buildRight(rightW)
}

// ScrollUp scrolls the left panel up.
func (d *DetailPane) ScrollUp() {
	if d.scrollOffset > 0 {
		d.scrollOffset--
	}
}

// ScrollDown scrolls the left panel down.
func (d *DetailPane) ScrollDown() {
	d.scrollOffset++
}

// Render renders the two-panel detail view into the given dimensions.
func (d *DetailPane) Render(width, height int) string {
	leftW, rightW := panelWidths(width)

	// Re-build if width changed.
	if len(d.leftLines) == 0 || (len(d.leftLines) > 0 && lipgloss.Width(d.leftLines[0]) != leftW) {
		d.rebuild(width)
	}

	contentH := height - 1 // reserve one line for the hint bar

	// Clamp scroll.
	maxScroll := len(d.leftLines) - contentH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if d.scrollOffset > maxScroll {
		d.scrollOffset = maxScroll
	}

	leftStyle := lipgloss.NewStyle().Width(leftW)
	rightStyle := lipgloss.NewStyle().Width(rightW)
	sepStyle := lipgloss.NewStyle().Foreground(colorStatusBar)

	var sb strings.Builder

	for i := 0; i < contentH; i++ {
		leftIdx := d.scrollOffset + i

		var leftLine, rightLine string
		if leftIdx < len(d.leftLines) {
			leftLine = d.leftLines[leftIdx]
		}
		if i < len(d.rightLines) {
			rightLine = d.rightLines[i]
		}

		leftRendered := leftStyle.Render(leftLine)
		rightRendered := rightStyle.Render(rightLine)
		sb.WriteString(leftRendered + sepStyle.Render("│") + rightRendered + "\n")
	}

	// Hint bar.
	scrollPct := ""
	if len(d.leftLines) > contentH {
		pct := 100 * (d.scrollOffset + contentH) / len(d.leftLines)
		if pct > 100 {
			pct = 100
		}
		scrollPct = fmt.Sprintf(" %d%%", pct)
	}
	hint := lipgloss.NewStyle().Foreground(colorTab).Render(
		"esc:back  o:browser  ,:edit field  ↑↓/ctrl+d/ctrl+u:scroll" + scrollPct,
	)
	sb.WriteString(hint)

	return sb.String()
}

// buildLeft constructs the left panel content lines.
func (d *DetailPane) buildLeft(width int) []string {
	var lines []string
	data := d.data
	if data == nil {
		return []string{"Loading..."}
	}

	strVal := func(key string) string { return issueStr(data.Issue, key) }

	// ── Breadcrumb ──────────────────────────────────────────────────────────
	breadcrumb := buildBreadcrumb(data.ProjectName, data.Chain, data.IssueKey)
	lines = append(lines, lipgloss.NewStyle().Foreground(colorTab).Render(breadcrumb))
	lines = append(lines, "")

	// ── Summary ─────────────────────────────────────────────────────────────
	summary := strVal("summary")
	if summary != "" {
		summaryStyle := lipgloss.NewStyle().Bold(true).Foreground(colorActiveTab)
		for _, l := range wrapSpans([]span{{text: summary, bold: true}}, width) {
			lines = append(lines, summaryStyle.Render(l))
		}
	}
	lines = append(lines, "")
	lines = append(lines, lipgloss.NewStyle().Foreground(colorStatusBar).Render(strings.Repeat("─", width)))
	lines = append(lines, "")

	// ── Description ─────────────────────────────────────────────────────────
	lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(colorHeader).Render("Description"))
	lines = append(lines, "")

	rawJSON := strVal("raw_json")
	descLines := extractAndRenderDesc(rawJSON, width-2)
	if len(descLines) == 0 {
		// Fall back to stored plaintext.
		plain := strVal("description")
		if plain == "" {
			descLines = []string{lipgloss.NewStyle().Foreground(colorTab).Render("  (no description)")}
		} else {
			for _, l := range wrapPlain(plain, width-2) {
				descLines = append(descLines, "  "+l)
			}
		}
	} else {
		// Indent description lines.
		for i, l := range descLines {
			if l != "" {
				descLines[i] = "  " + l
			}
		}
	}
	lines = append(lines, descLines...)
	lines = append(lines, "")
	lines = append(lines, lipgloss.NewStyle().Foreground(colorStatusBar).Render(strings.Repeat("─", width)))
	lines = append(lines, "")

	// ── Comments ────────────────────────────────────────────────────────────
	n := len(data.Comments)
	commentHeader := "Comments"
	if n == 1 {
		commentHeader = "Comments (1)"
	} else if n > 1 {
		commentHeader = fmt.Sprintf("Comments (%d)", n)
	}
	lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(colorHeader).Render(commentHeader))
	lines = append(lines, "")

	if n == 0 {
		lines = append(lines, lipgloss.NewStyle().Foreground(colorTab).Render("  (no comments)"))
	}

	for _, c := range data.Comments {
		// Comment header: author · date
		date := formatDateShort(c.Created)
		authorStyle := lipgloss.NewStyle().Bold(true).Foreground(colorActiveTab)
		dateStyle := lipgloss.NewStyle().Foreground(colorTab)
		lines = append(lines, "  "+authorStyle.Render(c.Author)+"  "+dateStyle.Render("· "+date))
		lines = append(lines, "  "+lipgloss.NewStyle().Foreground(colorStatusBar).Render(strings.Repeat("┄", width-4)))
		// Comment body — word-wrap.
		for _, l := range wrapPlain(c.Body, width-4) {
			if l == "" {
				lines = append(lines, "")
			} else {
				lines = append(lines, "  "+l)
			}
		}
		lines = append(lines, "")
	}

	return lines
}

// buildRight constructs the right sidebar content lines.
func (d *DetailPane) buildRight(width int) []string {
	var lines []string
	data := d.data
	if data == nil {
		return nil
	}

	strVal := func(key string) string { return issueStr(data.Issue, key) }

	labelStyle := lipgloss.NewStyle().Foreground(colorTab)
	valStyle := lipgloss.NewStyle().Foreground(colorActiveTab)

	addRow := func(label, value string) {
		if value == "" {
			return
		}
		l := fmt.Sprintf("%-10s", label)
		for i, line := range wrapPlain(value, width-11) {
			if i == 0 {
				lines = append(lines, " "+labelStyle.Render(l)+" "+valStyle.Render(line))
			} else {
				lines = append(lines, " "+strings.Repeat(" ", 11)+valStyle.Render(line))
			}
		}
	}

	// ── Type & Priority ─────────────────────────────────────────────────────
	addRow("Type", strVal("type"))
	addRow("Priority", strVal("priority"))
	lines = append(lines, "")

	// ── People ──────────────────────────────────────────────────────────────
	addRow("Assignee", strVal("assignee"))
	addRow("Reporter", strVal("reporter"))
	// Labels (comma-separated → one per line)
	if labels := strVal("labels"); labels != "" {
		for i, label := range strings.Split(labels, ",") {
			label = strings.TrimSpace(label)
			if label == "" {
				continue
			}
			if i == 0 {
				lines = append(lines, " "+labelStyle.Render(fmt.Sprintf("%-10s", "Labels"))+" "+valStyle.Render(label))
			} else {
				lines = append(lines, " "+strings.Repeat(" ", 11)+valStyle.Render(label))
			}
		}
	}

	// Extra configured fields (looked up via field_map jira_name).
	for _, jiraName := range data.SidebarExtra {
		col := findFieldColumn(data.FieldMap, jiraName)
		if col == "" {
			continue
		}
		val := issueStr(data.Issue, col)
		if val != "" {
			label := truncate(jiraName, 10)
			addRow(label, val)
		}
	}
	lines = append(lines, "")

	// ── Status ──────────────────────────────────────────────────────────────
	status := strVal("status")
	if status != "" {
		dot := statusDot(strings.ToLower(status))
		lines = append(lines, " "+labelStyle.Render("Status"))
		lines = append(lines, " "+dot+" "+valStyle.Render(status))
	}
	lines = append(lines, "")

	// ── Dates ───────────────────────────────────────────────────────────────
	lines = append(lines, " "+labelStyle.Render("Dates"))
	addDateRow := func(label, val string) {
		if val == "" {
			return
		}
		formatted := formatDateShort(val)
		lines = append(lines, " "+labelStyle.Render(fmt.Sprintf("  %-8s", label))+" "+valStyle.Render(formatted))
	}
	addDateRow("Created", strVal("created"))
	addDateRow("Updated", strVal("updated"))
	addDateRow("Resolved", strVal("resolved"))

	return lines
}

// buildBreadcrumb formats the breadcrumb navigation string.
func buildBreadcrumb(projectName string, chain []ChainItem, currentKey string) string {
	parts := []string{projectName}
	for _, c := range chain {
		parts = append(parts, c.Key)
	}
	sep := lipgloss.NewStyle().Foreground(lipgloss.Color("#555555")).Render(" › ")
	var rendered []string
	for i, p := range parts {
		rendered = append(rendered, lipgloss.NewStyle().Foreground(lipgloss.Color("#666666")).Render(p))
		_ = i
	}
	return strings.Join(rendered, sep) + sep + lipgloss.NewStyle().Bold(true).Foreground(colorActiveTab).Render(currentKey)
}

// extractAndRenderDesc parses raw_json and renders the ADF description.
func extractAndRenderDesc(rawJSON string, width int) []string {
	if rawJSON == "" {
		return nil
	}
	var wrapper struct {
		Fields struct {
			Description json.RawMessage `json:"description"`
		} `json:"fields"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &wrapper); err != nil {
		return nil
	}
	if len(wrapper.Fields.Description) == 0 {
		return nil
	}
	return RenderADF(wrapper.Fields.Description, width)
}

// panelWidths returns (leftWidth, rightWidth) for the two-panel layout.
func panelWidths(totalWidth int) (int, int) {
	rightW := totalWidth / 4
	if rightW < 22 {
		rightW = 22
	}
	if rightW > 35 {
		rightW = 35
	}
	leftW := totalWidth - rightW - 1 // -1 for separator
	if leftW < 20 {
		leftW = 20
	}
	return leftW, rightW
}

// issueStr extracts a string value from the issue map.
func issueStr(issue map[string]interface{}, key string) string {
	if issue == nil {
		return ""
	}
	switch v := issue[key].(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case int64:
		return fmt.Sprintf("%d", v)
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%.0f", v)
		}
		return fmt.Sprintf("%.2f", v)
	}
	return ""
}

// findFieldColumn returns the DB column name for a field with the given jira_name (case-insensitive).
func findFieldColumn(fieldMap []*db.FieldMapping, jiraName string) string {
	lower := strings.ToLower(jiraName)
	for _, f := range fieldMap {
		if strings.ToLower(f.JiraName) == lower {
			return f.Name
		}
	}
	return ""
}

// statusDot returns a colored bullet for the status.
func statusDot(status string) string {
	var color lipgloss.Color
	switch {
	case status == "done" || status == "closed" || status == "resolved":
		color = colorDone
	case strings.Contains(status, "progress"):
		color = colorInProgress
	case strings.Contains(status, "block"):
		color = colorBlocked
	default:
		color = colorTab
	}
	return lipgloss.NewStyle().Foreground(color).Render("●")
}

// formatDateShort formats a date string as a short human-readable string.
// Handles RFC3339, Jira Cloud's "+0000" timezone format, and date-only strings.
func formatDateShort(s string) string {
	if s == "" {
		return ""
	}
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05.999-0700", // Jira Cloud: millis + no-colon TZ
		"2006-01-02T15:04:05.999Z07:00",
		"2006-01-02T15:04:05-0700",
		"2006-01-02",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			if t.IsZero() {
				return ""
			}
			return t.Format("Jan 02, 2006")
		}
	}
	return s
}

// truncate shortens a string to at most n characters.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// openBrowser returns a tea.Cmd that opens the issue in the browser.
func openBrowser(issueKey, baseURL string) tea.Cmd {
	return func() tea.Msg {
		url := strings.TrimRight(baseURL, "/") + "/browse/" + issueKey
		var err error
		switch runtime.GOOS {
		case "darwin":
			err = exec.Command("open", url).Start()
		case "linux":
			err = exec.Command("xdg-open", url).Start()
		default:
			err = exec.Command("cmd", "/c", "start", url).Start()
		}
		_ = err
		return nil
	}
}

// BuildParentChain walks the parent_key links from an issue upward and returns the chain.
func BuildParentChain(database *db.DB, issueData map[string]interface{}) []ChainItem {
	var chain []ChainItem
	seen := map[string]bool{}
	current := issueStr(issueData, "parent_key")

	for current != "" && !seen[current] {
		seen[current] = true
		data, err := database.GetIssue(current)
		if err != nil || data == nil {
			break
		}
		chain = append([]ChainItem{{
			Key:     issueStr(data, "key"),
			Summary: issueStr(data, "summary"),
		}}, chain...)
		current = issueStr(data, "parent_key")
		if len(chain) >= 5 {
			break
		}
	}
	return chain
}
