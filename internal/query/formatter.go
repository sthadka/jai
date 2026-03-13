package query

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Table formats results as a plain text table.
func (r *Results) Table() string {
	if len(r.Rows) == 0 {
		return "(no results)\n"
	}

	// Compute column widths.
	widths := make([]int, len(r.Columns))
	for i, col := range r.Columns {
		widths[i] = len(col)
	}
	for _, row := range r.Rows {
		for i, val := range row {
			s := formatValue(val)
			if len(s) > widths[i] {
				widths[i] = len(s)
			}
		}
		// Cap column width at 80 for readability.
		for i := range widths {
			if widths[i] > 80 {
				widths[i] = 80
			}
		}
	}

	var sb strings.Builder

	// Header row.
	for i, col := range r.Columns {
		fmt.Fprintf(&sb, "%-*s", widths[i], strings.ToUpper(col))
		if i < len(r.Columns)-1 {
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

	// Data rows.
	for _, row := range r.Rows {
		for i, val := range row {
			s := formatValue(val)
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

	fmt.Fprintf(&sb, "(%d row", r.Count)
	if r.Count != 1 {
		sb.WriteString("s")
	}
	sb.WriteString(")\n")

	return sb.String()
}

// JSONBytes returns the results as a compact JSON envelope.
func (r *Results) JSONBytes() ([]byte, error) {
	rows := r.Rows
	if rows == nil {
		rows = [][]interface{}{}
	}
	return json.Marshal(map[string]interface{}{
		"ok":      true,
		"columns": r.Columns,
		"rows":    rows,
		"count":   r.Count,
	})
}

// SingleJSON returns a single-item JSON envelope from the first row.
func (r *Results) SingleJSON() ([]byte, error) {
	if len(r.Rows) == 0 {
		return json.Marshal(map[string]interface{}{
			"ok":   false,
			"error": map[string]string{"type": "NotFoundError", "message": "not found"},
		})
	}
	data := make(map[string]interface{}, len(r.Columns))
	for i, col := range r.Columns {
		data[col] = r.Rows[0][i]
	}
	return json.Marshal(map[string]interface{}{
		"ok":   true,
		"data": data,
	})
}

func formatValue(v interface{}) string {
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
