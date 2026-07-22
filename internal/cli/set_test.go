package cli

import (
	"encoding/json"
	"testing"
)

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
