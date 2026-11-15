package query

import "testing"

func TestLevenshteinDistance(t *testing.T) {
	tests := []struct {
		a, b     string
		expected int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"kitten", "sitting", 3},
		{"status", "statues", 1},
		{"team", "teem", 1},
	}

	for _, tt := range tests {
		got := LevenshteinDistance(tt.a, tt.b)
		if got != tt.expected {
			t.Errorf("LevenshteinDistance(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.expected)
		}
	}
}

func TestSuggestField(t *testing.T) {
	candidates := []string{"status", "summary", "assignee", "priority", "team"}

	tests := []struct {
		input    string
		expected string
	}{
		{"statuss", "status"},
		{"teem", "team"},
		{"pririty", "priority"},
		{"xyz_totally_wrong", ""}, // too far
	}

	for _, tt := range tests {
		got := SuggestField(tt.input, candidates)
		if got != tt.expected {
			t.Errorf("SuggestField(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
