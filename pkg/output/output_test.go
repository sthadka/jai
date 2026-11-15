package output

import (
	"encoding/json"
	"testing"
)

func TestOK(t *testing.T) {
	data := map[string]string{"key": "TEST-1"}
	b := OK(data)

	var result map[string]interface{}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["ok"] != true {
		t.Errorf("expected ok=true, got %v", result["ok"])
	}
}

func TestErr(t *testing.T) {
	b := Err("QueryError", "no such table")

	var result map[string]interface{}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["ok"] != false {
		t.Errorf("expected ok=false, got %v", result["ok"])
	}
}

func TestOKQuery(t *testing.T) {
	b := OKQuery([]string{"key", "summary"}, [][]interface{}{{"TEST-1", "My issue"}}, 1)

	var result map[string]interface{}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["count"].(float64) != 1 {
		t.Errorf("expected count=1, got %v", result["count"])
	}
}

func TestTable(t *testing.T) {
	out := Table([]string{"key", "summary"}, [][]interface{}{
		{"TEST-1", "My issue"},
		{"TEST-2", "Another issue"},
	})
	if out == "" {
		t.Error("expected non-empty table")
	}
	if out == "(no results)\n" {
		t.Error("expected data rows")
	}
}

func TestTable_Empty(t *testing.T) {
	out := Table([]string{"key"}, nil)
	if out != "(no results)\n" {
		t.Errorf("expected '(no results)\\n', got %q", out)
	}
}

func TestParseFields(t *testing.T) {
	fields := ParseFields("key,summary, status")
	if len(fields) != 3 {
		t.Errorf("expected 3 fields, got %d: %v", len(fields), fields)
	}
	if fields[2] != "status" {
		t.Errorf("expected 'status', got %q", fields[2])
	}
}

func TestParseFields_Empty(t *testing.T) {
	fields := ParseFields("")
	if fields != nil {
		t.Errorf("expected nil, got %v", fields)
	}
}

func TestFilterFields(t *testing.T) {
	data := map[string]interface{}{
		"key":     "TEST-1",
		"summary": "My issue",
		"status":  "In Progress",
	}
	filtered := FilterFields(data, []string{"key", "summary"})
	if len(filtered) != 2 {
		t.Errorf("expected 2 fields, got %d", len(filtered))
	}
	if _, ok := filtered["status"]; ok {
		t.Error("status should have been filtered out")
	}
}

func TestFilterColumns(t *testing.T) {
	cols := []string{"key", "summary", "status"}
	rows := [][]interface{}{
		{"TEST-1", "Issue 1", "In Progress"},
	}

	filtCols, filtRows := FilterColumns(cols, rows, []string{"key", "status"})
	if len(filtCols) != 2 {
		t.Errorf("expected 2 cols, got %d", len(filtCols))
	}
	if filtRows[0][0] != "TEST-1" {
		t.Errorf("expected TEST-1, got %v", filtRows[0][0])
	}
	if filtRows[0][1] != "In Progress" {
		t.Errorf("expected In Progress, got %v", filtRows[0][1])
	}
}
