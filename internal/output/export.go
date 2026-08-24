package output

import (
	"encoding/csv"
	"strings"
)

// CSV returns a CSV-formatted string (RFC 4180 compliant) with header and data rows.
func CSV(columns []string, rows [][]interface{}) string {
	var buf strings.Builder
	w := csv.NewWriter(&buf)

	// Write header.
	if err := w.Write(columns); err != nil {
		return ""
	}

	// Write data rows.
	for _, row := range rows {
		strRow := make([]string, len(row))
		for i, val := range row {
			strRow[i] = ValueStr(val)
		}
		if err := w.Write(strRow); err != nil {
			return ""
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return ""
	}

	return buf.String()
}

// TSV returns a tab-separated values string with header and data rows.
// Tabs and newlines within values are replaced with spaces.
func TSV(columns []string, rows [][]interface{}) string {
	var buf strings.Builder

	// Write header.
	for i, col := range columns {
		if i > 0 {
			buf.WriteByte('\t')
		}
		buf.WriteString(escapeTSV(col))
	}
	buf.WriteByte('\n')

	// Write data rows.
	for _, row := range rows {
		for i, val := range row {
			if i > 0 {
				buf.WriteByte('\t')
			}
			buf.WriteString(escapeTSV(ValueStr(val)))
		}
		buf.WriteByte('\n')
	}

	return buf.String()
}

// escapeTSV replaces tabs and newlines with spaces for TSV format.
func escapeTSV(s string) string {
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}

// Markdown returns a GitHub-flavored Markdown pipe table.
func Markdown(columns []string, rows [][]interface{}) string {
	var buf strings.Builder

	// Write header.
	buf.WriteString("| ")
	for i, col := range columns {
		if i > 0 {
			buf.WriteString(" | ")
		}
		buf.WriteString(escapeMD(col))
	}
	buf.WriteString(" |\n")

	// Write separator row.
	buf.WriteString("|")
	for i := 0; i < len(columns); i++ {
		buf.WriteString(" --- |")
	}
	buf.WriteByte('\n')

	// Write data rows.
	for _, row := range rows {
		buf.WriteString("| ")
		for i, val := range row {
			if i > 0 {
				buf.WriteString(" | ")
			}
			buf.WriteString(escapeMD(ValueStr(val)))
		}
		buf.WriteString(" |\n")
	}

	return buf.String()
}

// escapeMD escapes pipes and newlines for Markdown table format.
func escapeMD(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}
