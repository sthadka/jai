package output

import (
	"fmt"
	"strings"
	"time"
)

// Table renders rows as a plain-text aligned table.
func Table(columns []string, rows [][]interface{}) string {
	if len(rows) == 0 {
		return "(no results)\n"
	}

	widths := make([]int, len(columns))
	for i, col := range columns {
		widths[i] = len(col)
	}
	strRows := make([][]string, len(rows))
	for r, row := range rows {
		strRows[r] = make([]string, len(columns))
		for c, val := range row {
			s := ValueStr(val)
			strRows[r][c] = s
			if len(s) > widths[c] {
				widths[c] = len(s)
			}
		}
	}
	// Cap columns at 80 chars.
	for i := range widths {
		if widths[i] > 80 {
			widths[i] = 80
		}
	}

	var sb strings.Builder

	// Header.
	for i, col := range columns {
		fmt.Fprintf(&sb, "%-*s", widths[i], strings.ToUpper(col))
		if i < len(columns)-1 {
			sb.WriteString("  ")
		}
	}
	sb.WriteString("\n")

	// Separator.
	for i, w := range widths {
		sb.WriteString(strings.Repeat("-", w))
		if i < len(widths)-1 {
			sb.WriteString("  ")
		}
	}
	sb.WriteString("\n")

	// Rows.
	for _, row := range strRows {
		for i, s := range row {
			if len(s) > widths[i] {
				s = s[:widths[i]-3] + "..."
			}
			fmt.Fprintf(&sb, "%-*s", widths[i], s)
			if i < len(row)-1 {
				sb.WriteString("  ")
			}
		}
		sb.WriteString("\n")
	}

	n := len(rows)
	if n == 1 {
		sb.WriteString("(1 row)\n")
	} else {
		fmt.Fprintf(&sb, "(%d rows)\n", n)
	}

	return sb.String()
}

// ValueStr converts an arbitrary database value to a display string.
func ValueStr(v interface{}) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case []byte:
		return string(t)
	case string:
		return t
	case time.Time:
		if t.IsZero() {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// KV renders a key-value pair for single-item display.
func KV(label string, val interface{}) string {
	s := ValueStr(val)
	if s == "" {
		return ""
	}
	return fmt.Sprintf("  %-22s %s\n", label+":", s)
}
