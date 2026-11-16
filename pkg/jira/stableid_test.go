package jira

import (
	"encoding/json"
	"testing"
)

// TestIssueIDStringOrNumber verifies that an issue's "id" parses whether Jira
// serialises it as a quoted string (REST search/issue) or a bare number (agile /
// webhook payloads), so move/rename detection never loses the stable identity.
func TestIssueIDStringOrNumber(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"string id", `{"id":"10001","key":"ABC-1","fields":{}}`, "10001"},
		{"number id", `{"id":10001,"key":"ABC-1","fields":{}}`, "10001"},
		{"null id", `{"id":null,"key":"ABC-1","fields":{}}`, ""},
		{"missing id", `{"key":"ABC-1","fields":{}}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var issue Issue
			if err := json.Unmarshal([]byte(c.raw), &issue); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if issue.ID != c.want {
				t.Errorf("ID = %q, want %q", issue.ID, c.want)
			}
			if issue.Key != "ABC-1" {
				t.Errorf("Key = %q, want ABC-1 (other fields must still decode)", issue.Key)
			}
		})
	}
}

// TestSearchResponseNumericID ensures the custom unmarshaler works through the
// slice-of-pointer decode path the sync engine actually uses.
func TestSearchResponseNumericID(t *testing.T) {
	var resp SearchResponse
	if err := json.Unmarshal([]byte(`{"issues":[{"id":42,"key":"X-1","fields":{}}]}`), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Issues) != 1 || resp.Issues[0].ID != "42" {
		t.Fatalf("got %+v, want one issue with ID 42", resp.Issues)
	}
}
