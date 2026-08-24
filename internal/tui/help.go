package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderHelp renders a full-screen keybinding reference overlay.
func (a *App) renderHelp() string {
	width := a.width
	if width < 60 {
		width = 60
	}
	if width > 80 {
		width = 80
	}

	borderStyle := lipgloss.NewStyle().Foreground(colorHeader)
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorActiveTab)
	headingStyle := lipgloss.NewStyle().Bold(true).Foreground(colorActiveTab)
	keyStyle := lipgloss.NewStyle().Foreground(colorTab)
	descStyle := lipgloss.NewStyle().Foreground(colorStatusBar)

	var sb strings.Builder
	inner := width - 4

	line := func(content string) {
		padded := content
		if len(content) < inner {
			padded = content + strings.Repeat(" ", inner-len(content))
		}
		sb.WriteString(borderStyle.Render("│") + " " + padded + " " + borderStyle.Render("│") + "\n")
	}

	hline := func(l, r string) {
		sb.WriteString(borderStyle.Render(l+strings.Repeat("─", width-2)+r) + "\n")
	}

	emptyLine := func() {
		line("")
	}

	keyLine := func(key, desc string) {
		keyPart := keyStyle.Render(key)
		// Pad key to 12 characters
		padding := 12 - lipgloss.Width(key)
		if padding < 0 {
			padding = 0
		}
		keyPart = keyPart + strings.Repeat(" ", padding)
		descPart := descStyle.Render(desc)
		line(keyPart + descPart)
	}

	// Header
	hline("┌", "┐")
	title := "jai Help (? to close)"
	padding := (inner - len(title)) / 2
	if padding < 0 {
		padding = 0
	}
	line(strings.Repeat(" ", padding) + titleStyle.Render(title))
	emptyLine()

	// Navigation section
	line(headingStyle.Render("Navigation"))
	line(strings.Repeat("─", inner))
	keyLine("j/↓", "down")
	keyLine("k/↑", "up")
	keyLine("Ctrl+D", "half page down")
	keyLine("Ctrl+U", "half page up")
	keyLine("PgDn", "page down")
	keyLine("PgUp", "page up")
	keyLine("g/Home", "top")
	keyLine("G/End", "bottom")
	emptyLine()

	// Views section
	line(headingStyle.Render("Views"))
	line(strings.Repeat("─", inner))
	keyLine("Tab", "next view")
	keyLine("Shift+Tab", "prev view")
	keyLine("1-9", "jump to view")
	emptyLine()

	// Actions section
	line(headingStyle.Render("Actions"))
	line(strings.Repeat("─", inner))
	keyLine("Enter", "open detail")
	keyLine("/", "filter")
	keyLine("s", "sort")
	keyLine("o", "open in browser")
	keyLine("r", "refresh")
	keyLine("q", "quit")
	emptyLine()

	// Detail View section
	line(headingStyle.Render("Detail View"))
	line(strings.Repeat("─", inner))
	keyLine("Esc", "back")
	keyLine("o", "open in browser")
	keyLine(",", "edit field")
	keyLine("↑↓", "scroll")
	emptyLine()

	// Footer
	hline("└", "┘")

	// Center the help overlay
	lines := strings.Split(sb.String(), "\n")
	height := len(lines)
	topPadding := (a.height - height) / 2
	if topPadding < 0 {
		topPadding = 0
	}

	result := strings.Repeat("\n", topPadding) + sb.String()
	return result
}
