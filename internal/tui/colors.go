package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/syethadk/jai/internal/config"
)

var (
	colorDone        = lipgloss.Color("#6cc644")
	colorInProgress  = lipgloss.Color("#e1c233")
	colorBlocked     = lipgloss.Color("#dd4444")
	colorSelected    = lipgloss.Color("#333333")
	colorHeader      = lipgloss.Color("#5c8fc9")
	colorTab         = lipgloss.Color("#888888")
	colorActiveTab   = lipgloss.Color("#ffffff")
	colorGroupHeader = lipgloss.Color("#9b9b9b")
	colorStatusBar   = lipgloss.Color("#444444")
	colorSync        = lipgloss.Color("#5c8fc9")
)

// RowStyle returns the lipgloss style for a row given color rules.
func RowStyle(row map[string]string, rules []config.ColorRule, selected bool) lipgloss.Style {
	base := lipgloss.NewStyle()
	if selected {
		base = base.Background(colorSelected)
	}

	for _, rule := range rules {
		if matchesRule(row, rule) {
			return base.Foreground(lipgloss.Color(rule.Color))
		}
	}

	// Default coloring by status.
	if status, ok := row["status"]; ok {
		lower := strings.ToLower(status)
		switch {
		case lower == "done" || lower == "closed" || lower == "resolved":
			return base.Foreground(colorDone)
		case strings.Contains(lower, "progress"):
			return base.Foreground(colorInProgress)
		case strings.Contains(lower, "block"):
			return base.Foreground(colorBlocked)
		}
	}

	return base
}

func matchesRule(row map[string]string, rule config.ColorRule) bool {
	val, ok := row[rule.Field]
	if !ok {
		return false
	}

	switch rule.Condition {
	case "equals":
		return strings.EqualFold(val, rule.Value)
	case "not_equals":
		return !strings.EqualFold(val, rule.Value)
	case "contains":
		return strings.Contains(strings.ToLower(val), strings.ToLower(rule.Value))
	case "older_than":
		d, err := parseAgeDuration(rule.Value)
		if err != nil {
			return false
		}
		t, err := time.Parse(time.RFC3339, val)
		if err != nil {
			return false
		}
		return time.Since(t) > d
	case "in":
		for _, v := range strings.Split(rule.Value, ",") {
			if strings.EqualFold(strings.TrimSpace(v), val) {
				return true
			}
		}
	}
	return false
}

// parseAgeDuration parses "28d", "2h", "30m" etc.
func parseAgeDuration(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		n := 0
		for _, c := range s[:len(s)-1] {
			if c >= '0' && c <= '9' {
				n = n*10 + int(c-'0')
			}
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}
