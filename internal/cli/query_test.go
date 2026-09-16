package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sthadka/jai/internal/jira"
)

// TestJQLIssueToRow_DescriptionPreservesLinks guards the JQL path against the
// smart-link drop bug: a description column requested via --fields must render
// the ADF to markdown (preserving URLs), not return null.
func TestJQLIssueToRow_DescriptionPreservesLinks(t *testing.T) {
	const docURL = "https://docs.google.com/document/d/1gXyX/edit"
	fields := json.RawMessage(`{
		"summary": "Project with linked doc",
		"description": {"type":"doc","version":1,"content":[
			{"type":"paragraph","content":[{"type":"text","text":"See project document:"}]},
			{"type":"paragraph","content":[{"type":"inlineCard","attrs":{"url":"` + docURL + `"}}]}
		]}
	}`)
	issue := &jira.Issue{Key: "ROX-36990", Fields: fields}

	row, err := jqlIssueToRow(issue, []string{"key", "description"})
	if err != nil {
		t.Fatalf("jqlIssueToRow: %v", err)
	}
	desc, _ := row[1].(string)
	if !strings.Contains(desc, docURL) {
		t.Errorf("expected description column to contain %q, got %q", docURL, desc)
	}
}

// TestJQLColumnsToAPIFields verifies requested columns map to the API field IDs
// needed to populate them (so description/reporter aren't silently dropped).
func TestJQLColumnsToAPIFields(t *testing.T) {
	got := jqlColumnsToAPIFields([]string{"key", "description", "reporter", "type"})
	want := map[string]bool{"description": true, "reporter": true, "issuetype": true}
	if len(got) != len(want) {
		t.Fatalf("expected %d fields, got %v", len(want), got)
	}
	for _, f := range got {
		if !want[f] {
			t.Errorf("unexpected API field %q in %v", f, got)
		}
	}
}
