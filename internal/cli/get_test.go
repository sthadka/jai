package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/query"
)

func setupTestGlobals(t *testing.T) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	issue := &db.Issue{
		Key:     "TEST-1",
		Project: "TEST",
		Summary: "Test summary",
		Status:  "In Progress",
		RawJSON: "{}",
	}
	if err := database.UpsertIssue(issue, nil); err != nil {
		t.Fatalf("upsert issue: %v", err)
	}

	g.db = database
	g.query = query.New(database, &config.Config{})
	g.jsonOut = false
	g.fields = ""
	t.Cleanup(func() {
		g.db = nil
		g.query = nil
		g.jsonOut = false
		g.fields = ""
	})
}

// captureStdout runs fn and returns everything written to os.Stdout during the call.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = orig
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return string(out)
}

// TestGetFieldsHumanOutput is the regression test for the bug where --fields
// was ignored in the human text output path.
func TestGetFieldsHumanOutput(t *testing.T) {
	setupTestGlobals(t)
	g.fields = "status"

	out := captureStdout(t, func() {
		if err := getCmd.RunE(getCmd, []string{"TEST-1"}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	if strings.Contains(out, "Summary:") {
		t.Errorf("expected Summary to be filtered out, got:\n%s", out)
	}
	if strings.Contains(out, "Key:") {
		t.Errorf("expected Key to be filtered out, got:\n%s", out)
	}
	if !strings.Contains(out, "In Progress") {
		t.Errorf("expected status value 'In Progress' in output, got:\n%s", out)
	}
}

// TestGetFieldsHumanOutput_KeySummary verifies that key and summary appear
// in the header when included in --fields.
func TestGetFieldsHumanOutput_KeySummary(t *testing.T) {
	setupTestGlobals(t)
	g.fields = "key,summary"

	out := captureStdout(t, func() {
		if err := getCmd.RunE(getCmd, []string{"TEST-1"}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	if !strings.Contains(out, "Key:") {
		t.Errorf("expected Key: in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Summary:") {
		t.Errorf("expected Summary: in output, got:\n%s", out)
	}
	if !strings.Contains(out, "TEST-1") {
		t.Errorf("expected TEST-1 in output, got:\n%s", out)
	}
	if strings.Contains(out, "In Progress") {
		t.Errorf("expected status to be filtered out, got:\n%s", out)
	}
}

// TestGetFieldsJSONOutput verifies that --fields is applied in JSON mode.
func TestGetFieldsJSONOutput(t *testing.T) {
	setupTestGlobals(t)
	g.jsonOut = true
	g.fields = "summary,status"

	out := captureStdout(t, func() {
		if err := getCmd.RunE(getCmd, []string{"TEST-1"}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &result); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
	data, ok := result["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data object, got: %v", result["data"])
	}
	if _, ok := data["key"]; ok {
		t.Errorf("key should have been filtered out: %v", data)
	}
	if _, ok := data["summary"]; !ok {
		t.Errorf("summary should be present: %v", data)
	}
	if _, ok := data["status"]; !ok {
		t.Errorf("status should be present: %v", data)
	}
}

// TestGetNoFields verifies full output when --fields is not set.
func TestGetNoFields(t *testing.T) {
	setupTestGlobals(t)

	out := captureStdout(t, func() {
		if err := getCmd.RunE(getCmd, []string{"TEST-1"}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	if !strings.Contains(out, "Key:") {
		t.Errorf("expected Key: in full output, got:\n%s", out)
	}
	if !strings.Contains(out, "Summary:") {
		t.Errorf("expected Summary: in full output, got:\n%s", out)
	}
}
