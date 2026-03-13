package tui

import (
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// DetailPane renders a single issue's fields.
type DetailPane struct {
	IssueKey string
	data     map[string]string
	jiraURL  string
	offset   int
	lines    []string
}

// NewDetailPane creates a DetailPane for a row.
func NewDetailPane(issueKey string, data map[string]string, jiraURL string) *DetailPane {
	d := &DetailPane{
		IssueKey: issueKey,
		data:     data,
		jiraURL:  jiraURL,
	}
	d.buildLines()
	return d
}

func (d *DetailPane) buildLines() {
	var lines []string

	// Key and summary first.
	if v, ok := d.data["key"]; ok && v != "" {
		lines = append(lines, lipgloss.NewStyle().Bold(true).Render(v))
	}
	if v, ok := d.data["summary"]; ok && v != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(colorActiveTab).Render(v))
	}
	lines = append(lines, strings.Repeat("─", 60))

	// All other fields in alphabetical order.
	skip := map[string]bool{"key": true, "summary": true, "raw_json": true, "comments_text": true}
	keys := make([]string, 0, len(d.data))
	for k := range d.data {
		if !skip[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	for _, k := range keys {
		v := d.data[k]
		if v == "" {
			continue
		}
		label := lipgloss.NewStyle().Foreground(colorTab).Render(fmt.Sprintf("%-18s", k+":"))
		lines = append(lines, label+" "+v)
	}

	d.lines = lines
}

// ScrollUp scrolls the detail pane up.
func (d *DetailPane) ScrollUp() {
	if d.offset > 0 {
		d.offset--
	}
}

// ScrollDown scrolls the detail pane down.
func (d *DetailPane) ScrollDown() {
	d.offset++
}

// Render renders the detail pane.
func (d *DetailPane) Render(width, height int) string {
	var sb strings.Builder

	end := d.offset + height
	if end > len(d.lines) {
		end = len(d.lines)
		d.offset = end - height
		if d.offset < 0 {
			d.offset = 0
		}
	}

	for i := d.offset; i < end && i < len(d.lines); i++ {
		line := d.lines[i]
		if len(line) > width {
			line = line[:width-3] + "..."
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	hint := lipgloss.NewStyle().Foreground(colorTab).Render(
		"esc:back  o:open in browser  ↑↓:scroll",
	)
	sb.WriteString(hint)

	return sb.String()
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
