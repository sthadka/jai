package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// StatusSummary counts rows by status/status_category for summary bar display.
func StatusSummary(columns []string, rows [][]string) string {
	// Find status_category or status column.
	statusCol := -1
	for i, col := range columns {
		if col == "status_category" {
			statusCol = i
			break
		}
	}
	if statusCol == -1 {
		for i, col := range columns {
			if col == "status" {
				statusCol = i
				break
			}
		}
	}
	if statusCol == -1 {
		return ""
	}

	counts := make(map[string]int)
	for _, row := range rows {
		if statusCol < len(row) {
			val := row[statusCol]
			if val != "" {
				counts[val]++
			}
		}
	}

	if len(counts) == 0 {
		return ""
	}

	// Order: To Do, In Progress, Done, then rest.
	order := []string{"To Do", "In Progress", "Done"}
	seen := make(map[string]bool)
	var parts []string

	for _, s := range order {
		if n, ok := counts[s]; ok {
			color := colorTab
			switch s {
			case "Done":
				color = colorDone
			case "In Progress":
				color = colorInProgress
			}
			parts = append(parts, lipgloss.NewStyle().Foreground(color).Render(fmt.Sprintf("%s: %d", s, n)))
			seen[s] = true
		}
	}
	for s, n := range counts {
		if !seen[s] {
			parts = append(parts, fmt.Sprintf("%s: %d", s, n))
		}
	}

	return strings.Join(parts, "  |  ")
}
