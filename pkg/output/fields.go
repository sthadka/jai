package output

import "strings"

// FilterFields filters a map to only include the specified field names.
// If fields is empty, all keys are returned unchanged.
func FilterFields(data map[string]interface{}, fields []string) map[string]interface{} {
	if len(fields) == 0 {
		return data
	}
	result := make(map[string]interface{}, len(fields))
	for _, f := range fields {
		if v, ok := data[f]; ok {
			result[f] = v
		}
	}
	return result
}

// ParseFields splits a comma-separated field list.
func ParseFields(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// FilterColumns filters query result columns and rows to only include specified fields.
func FilterColumns(columns []string, rows [][]interface{}, fields []string) ([]string, [][]interface{}) {
	if len(fields) == 0 {
		return columns, rows
	}

	// Build index of desired columns.
	want := make(map[string]int, len(fields))
	for _, f := range fields {
		want[f] = -1
	}
	indices := make([]int, 0, len(fields))
	filtCols := make([]string, 0, len(fields))
	for i, col := range columns {
		if _, ok := want[col]; ok {
			indices = append(indices, i)
			filtCols = append(filtCols, col)
		}
	}

	filtRows := make([][]interface{}, len(rows))
	for r, row := range rows {
		filtRow := make([]interface{}, len(indices))
		for j, idx := range indices {
			filtRow[j] = row[idx]
		}
		filtRows[r] = filtRow
	}

	return filtCols, filtRows
}
