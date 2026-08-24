package output

import "encoding/json"

// OK returns a compact JSON success envelope for a single item.
func OK(data interface{}) []byte {
	b, _ := json.Marshal(map[string]interface{}{"ok": true, "data": data})
	return b
}

// OKQuery returns a compact JSON success envelope for query results.
func OKQuery(columns []string, rows [][]interface{}, count int) []byte {
	if rows == nil {
		rows = [][]interface{}{}
	}
	b, _ := json.Marshal(map[string]interface{}{
		"ok":      true,
		"columns": columns,
		"rows":    rows,
		"count":   count,
	})
	return b
}

// OKQueryWithMeta returns a JSON success envelope for query results with metadata.
func OKQueryWithMeta(columns []string, rows [][]interface{}, count int, syncAgeSeconds int) []byte {
	if rows == nil {
		rows = [][]interface{}{}
	}
	result := map[string]interface{}{
		"ok":      true,
		"columns": columns,
		"rows":    rows,
		"count":   count,
	}
	if syncAgeSeconds >= 0 {
		result["sync_age_seconds"] = syncAgeSeconds
	}
	b, _ := json.Marshal(result)
	return b
}

// Err returns a compact JSON error envelope.
func Err(errType, msg string) []byte {
	b, _ := json.Marshal(map[string]interface{}{
		"ok": false,
		"error": map[string]string{
			"type":    errType,
			"message": msg,
		},
	})
	return b
}
