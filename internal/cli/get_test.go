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
	g.format = ""
	t.Cleanup(func() {
		g.db = nil
		g.query = nil
		g.jsonOut = false
		g.fields = ""
		g.format = ""
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

	if strings.Contains(out, "summary:") {
		t.Errorf("expected summary to be filtered out, got:\n%s", out)
	}
	if strings.Contains(out, "key:") {
		t.Errorf("expected key to be filtered out, got:\n%s", out)
	}
	if !strings.Contains(out, "In Progress") {
		t.Errorf("expected status value 'In Progress' in output, got:\n%s", out)
	}
}

// TestGetFieldsHumanOutput_KeySummary verifies that key and summary appear
// in front matter when included in --fields.
func TestGetFieldsHumanOutput_KeySummary(t *testing.T) {
	setupTestGlobals(t)
	g.fields = "key,summary"

	out := captureStdout(t, func() {
		if err := getCmd.RunE(getCmd, []string{"TEST-1"}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	if !strings.Contains(out, "key: TEST-1") {
		t.Errorf("expected 'key: TEST-1' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "summary:") {
		t.Errorf("expected 'summary:' in output, got:\n%s", out)
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

	if !strings.Contains(out, "key: TEST-1") {
		t.Errorf("expected 'key: TEST-1' in full output, got:\n%s", out)
	}
	if !strings.Contains(out, "summary:") {
		t.Errorf("expected 'summary:' in full output, got:\n%s", out)
	}
	if !strings.Contains(out, "---") {
		t.Errorf("expected YAML front matter delimiter in full output, got:\n%s", out)
	}
}

// TestGetFormatMarkdown is the regression test for the bug where --format was
// ignored by `get` and it always printed the front-matter document.
func TestGetFormatMarkdown(t *testing.T) {
	setupTestGlobals(t)
	g.format = "markdown"

	out := captureStdout(t, func() {
		if err := getCmd.RunE(getCmd, []string{"TEST-1"}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	if !strings.HasPrefix(out, "| ") {
		t.Errorf("expected a markdown pipe table, got:\n%s", out)
	}
	if strings.Contains(out, "---\nkey:") {
		t.Errorf("expected markdown format, not front-matter document, got:\n%s", out)
	}
	if !strings.Contains(out, "TEST-1") {
		t.Errorf("expected TEST-1 in output, got:\n%s", out)
	}
}

// TestGetFormatCSV verifies --format csv renders a single-row CSV.
func TestGetFormatCSV(t *testing.T) {
	setupTestGlobals(t)
	g.format = "csv"

	out := captureStdout(t, func() {
		if err := getCmd.RunE(getCmd, []string{"TEST-1"}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	if !strings.Contains(out, "Key") || !strings.Contains(out, "TEST-1") {
		t.Errorf("expected CSV header and value, got:\n%s", out)
	}
}

// TestGetFormatsUseSameCuratedFieldsByDefault is the regression test for the
// bug where table/json used the curated field set but csv/tsv/markdown
// dumped every column (including verbose ones like raw_json).
func TestGetFormatsUseSameCuratedFieldsByDefault(t *testing.T) {
	setupTestGlobals(t)

	for _, format := range []string{"table", "json", "csv", "tsv", "markdown"} {
		g.format = format
		out := captureStdout(t, func() {
			if err := getCmd.RunE(getCmd, []string{"TEST-1"}); err != nil {
				t.Fatalf("format %s: RunE: %v", format, err)
			}
		})
		if strings.Contains(out, "raw_json") {
			t.Errorf("format %s: expected raw_json to be excluded from the default curated view, got:\n%s", format, out)
		}
		if !strings.Contains(out, "TEST-1") {
			t.Errorf("format %s: expected TEST-1 in output, got:\n%s", format, out)
		}
	}
}

// TestGetFieldsAll verifies --fields all bypasses curation and returns every
// column, across every --format. json keeps the raw column name (machine
// consumption); csv/tsv/markdown show the title-cased header instead.
func TestGetFieldsAll(t *testing.T) {
	setupTestGlobals(t)
	g.fields = "all"

	wantByFormat := map[string]string{
		"json":     "raw_json",
		"csv":      "Raw Json",
		"tsv":      "Raw Json",
		"markdown": "Raw Json",
	}
	for format, want := range wantByFormat {
		g.format = format
		out := captureStdout(t, func() {
			if err := getCmd.RunE(getCmd, []string{"TEST-1"}); err != nil {
				t.Fatalf("format %s: RunE: %v", format, err)
			}
		})
		if !strings.Contains(out, want) {
			t.Errorf("format %s: expected %q with --fields all, got:\n%s", format, want, out)
		}
	}
}

// TestGetFormatMarkdownUsesReadableLabels is the regression test for the bug
// where csv/tsv/markdown headers showed raw DB column names (e.g. parent_key)
// instead of human-readable labels. A field_map entry (as populated by real
// syncs from Jira's field discovery) takes priority; unmapped columns fall
// back to a generic snake_case -> Title Case conversion.
func TestGetFormatMarkdownUsesReadableLabels(t *testing.T) {
	setupTestGlobals(t)

	database := g.db
	if err := database.UpsertFieldMapping(&db.FieldMapping{
		JiraID: "parent", JiraName: "Parent", Name: "parent_key", Type: "issuelink",
	}); err != nil {
		t.Fatalf("UpsertFieldMapping: %v", err)
	}
	issue := &db.Issue{
		Key: "TEST-2", Project: "TEST", Summary: "Has a parent and due date",
		ParentKey: "TEST-1", DueDate: "2026-01-01", RawJSON: "{}",
	}
	if err := database.UpsertIssue(issue, nil); err != nil {
		t.Fatalf("upsert issue: %v", err)
	}

	g.format = "markdown"
	out := captureStdout(t, func() {
		if err := getCmd.RunE(getCmd, []string{"TEST-2"}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	if strings.Contains(out, "parent_key") {
		t.Errorf("expected raw column name 'parent_key' to be relabeled, got:\n%s", out)
	}
	if !strings.Contains(out, "| Parent ") && !strings.Contains(out, "| Parent |") {
		t.Errorf("expected field_map label 'Parent' in header, got:\n%s", out)
	}
	if strings.Contains(out, "due_date") {
		t.Errorf("expected raw column name 'due_date' to be relabeled, got:\n%s", out)
	}
	if !strings.Contains(out, "Due Date") {
		t.Errorf("expected generic title-case fallback 'Due Date' for an unmapped column, got:\n%s", out)
	}
}

// TestGetFormatJSON verifies --format json behaves the same as --json.
func TestGetFormatJSON(t *testing.T) {
	setupTestGlobals(t)
	g.format = "json"

	out := captureStdout(t, func() {
		if err := getCmd.RunE(getCmd, []string{"TEST-1"}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &result); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
}

// TestGetFormatDefaultUnchanged verifies the default ("table"/unset) format
// keeps the original front-matter document behavior.
func TestGetFormatDefaultUnchanged(t *testing.T) {
	setupTestGlobals(t)
	g.format = "table"

	out := captureStdout(t, func() {
		if err := getCmd.RunE(getCmd, []string{"TEST-1"}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	if !strings.Contains(out, "key: TEST-1") {
		t.Errorf("expected front-matter document for --format table, got:\n%s", out)
	}
}
