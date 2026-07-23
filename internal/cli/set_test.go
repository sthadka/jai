package cli

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestWrapScalarFieldValue(t *testing.T) {
	tests := []struct {
		name    string
		jiraID  string
		value   string
		resolve func(string) (string, error)
		want    interface{}
		wantOK  bool
		wantErr bool
	}{
		{
			name:   "priority wraps as name object",
			jiraID: "priority",
			value:  "Major",
			want:   map[string]string{"name": "Major"},
			wantOK: true,
		},
		{
			name:   "assignee wraps as accountId object without resolver",
			jiraID: "assignee",
			value:  "user123",
			want:   map[string]string{"accountId": "user123"},
			wantOK: true,
		},
		{
			name:   "reporter resolves via callback",
			jiraID: "reporter",
			value:  "user@example.com",
			resolve: func(v string) (string, error) {
				return "resolved-id", nil
			},
			want:   map[string]string{"accountId": "resolved-id"},
			wantOK: true,
		},
		{
			name:   "resolver error propagates",
			jiraID: "assignee",
			value:  "nobody@example.com",
			resolve: func(v string) (string, error) {
				return "", fmt.Errorf("no user found")
			},
			wantOK:  true,
			wantErr: true,
		},
		{
			name:   "plain text field is not wrapped",
			jiraID: "summary",
			value:  "New Summary",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := wrapScalarFieldValue(tt.jiraID, tt.value, tt.resolve)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tt.wantOK {
				return
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(tt.want)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("got %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestWrapArrayItemValue(t *testing.T) {
	tests := []struct {
		name   string
		jiraID string
		value  string
		want   interface{}
		wantOK bool
	}{
		{
			name:   "components wraps as name object",
			jiraID: "components",
			value:  "API",
			want:   map[string]string{"name": "API"},
			wantOK: true,
		},
		{
			name:   "fixVersions wraps as name object",
			jiraID: "fixVersions",
			value:  "1.0",
			want:   map[string]string{"name": "1.0"},
			wantOK: true,
		},
		{
			name:   "labels is not wrapped",
			jiraID: "labels",
			value:  "bug",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := wrapArrayItemValue(tt.jiraID, tt.value)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(tt.want)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("got %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestParseArrayValue(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "single value",
			input: "bug",
			want:  []string{"bug"},
		},
		{
			name:  "comma separated",
			input: "bug,security",
			want:  []string{"bug", "security"},
		},
		{
			name:  "comma separated with spaces",
			input: "bug, security, rit-escalated",
			want:  []string{"bug", "security", "rit-escalated"},
		},
		{
			name:  "empty segments skipped",
			input: "bug,,security",
			want:  []string{"bug", "security"},
		},
		{
			name:  "whitespace only segments skipped",
			input: "bug, ,security",
			want:  []string{"bug", "security"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseArrayValue(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got[%d]=%q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestArrayFieldPayloadSerialization(t *testing.T) {
	arr := parseArrayValue("bug,security")
	payload, err := json.Marshal(map[string]interface{}{"field": "labels", "value": arr})
	if err != nil {
		t.Fatal(err)
	}

	var decoded struct {
		Field string      `json:"field"`
		Value interface{} `json:"value"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}

	values, ok := decoded.Value.([]interface{})
	if !ok {
		t.Fatalf("expected []interface{}, got %T", decoded.Value)
	}
	if len(values) != 2 {
		t.Fatalf("expected 2 values, got %d", len(values))
	}
	if values[0] != "bug" || values[1] != "security" {
		t.Fatalf("expected [bug, security], got %v", values)
	}
}

func TestExpandKeys(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "single key",
			input: "ROX-123",
			want:  []string{"ROX-123"},
		},
		{
			name:  "comma separated",
			input: "ROX-1,ROX-2,ROX-3",
			want:  []string{"ROX-1", "ROX-2", "ROX-3"},
		},
		{
			name:  "with spaces",
			input: "ROX-1, ROX-2, ROX-3",
			want:  []string{"ROX-1", "ROX-2", "ROX-3"},
		},
		{
			name:  "empty segments skipped",
			input: "ROX-1,,ROX-2",
			want:  []string{"ROX-1", "ROX-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandKeys(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got[%d]=%q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestExtractKeys(t *testing.T) {
	tests := []struct {
		name    string
		columns []string
		rows    [][]interface{}
		want    []string
		wantErr bool
	}{
		{
			name:    "extracts key column",
			columns: []string{"key", "summary"},
			rows: [][]interface{}{
				{"ROX-1", "first"},
				{"ROX-2", "second"},
			},
			want: []string{"ROX-1", "ROX-2"},
		},
		{
			name:    "case insensitive column match",
			columns: []string{"KEY", "summary"},
			rows: [][]interface{}{
				{"ROX-1", "first"},
			},
			want: []string{"ROX-1"},
		},
		{
			name:    "no key column errors",
			columns: []string{"summary", "status"},
			rows: [][]interface{}{
				{"first", "Open"},
			},
			wantErr: true,
		},
		{
			name:    "nil values skipped",
			columns: []string{"key"},
			rows: [][]interface{}{
				{"ROX-1"},
				{nil},
				{"ROX-3"},
			},
			want: []string{"ROX-1", "ROX-3"},
		},
		{
			name:    "empty rows",
			columns: []string{"key"},
			rows:    nil,
			want:    []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractKeys(tt.columns, tt.rows)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got[%d]=%q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestApplyArrayOps(t *testing.T) {
	tests := []struct {
		name    string
		current []string
		adds    []string
		removes []string
		want    []string
	}{
		{
			name:    "add to empty",
			current: nil,
			adds:    []string{"bug"},
			want:    []string{"bug"},
		},
		{
			name:    "add to existing",
			current: []string{"bug"},
			adds:    []string{"security"},
			want:    []string{"bug", "security"},
		},
		{
			name:    "add duplicate is idempotent",
			current: []string{"bug", "security"},
			adds:    []string{"bug"},
			want:    []string{"bug", "security"},
		},
		{
			name:    "remove existing",
			current: []string{"bug", "security", "rit"},
			removes: []string{"security"},
			want:    []string{"bug", "rit"},
		},
		{
			name:    "remove non-existing is no-op",
			current: []string{"bug"},
			removes: []string{"nonexistent"},
			want:    []string{"bug"},
		},
		{
			name:    "add and remove simultaneously",
			current: []string{"old-label", "keep"},
			adds:    []string{"new-label"},
			removes: []string{"old-label"},
			want:    []string{"keep", "new-label"},
		},
		{
			name:    "remove all leaves empty",
			current: []string{"a", "b"},
			removes: []string{"a", "b"},
			want:    nil,
		},
		{
			name:    "add multiple",
			current: nil,
			adds:    []string{"a", "b", "c"},
			want:    []string{"a", "b", "c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyArrayOps(tt.current, tt.adds, tt.removes)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got[%d]=%q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
