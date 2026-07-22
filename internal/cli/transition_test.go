package cli

import (
	"strings"
	"testing"

	"github.com/sthadka/jai/internal/jira"
)

func TestResolveTransition(t *testing.T) {
	transitions := []*jira.Transition{
		{ID: "11", Name: "To Do"},
		{ID: "21", Name: "In Progress"},
		{ID: "31", Name: "Done"},
	}

	tests := []struct {
		name        string
		input       string
		wantMatch   string
		wantAmbig   int
		wantNone    bool
	}{
		{name: "exact match", input: "Done", wantMatch: "31"},
		{name: "case insensitive", input: "done", wantMatch: "31"},
		{name: "mixed case", input: "in progress", wantMatch: "21"},
		{name: "upper case", input: "IN PROGRESS", wantMatch: "21"},
		{name: "no match", input: "Closed", wantNone: true},
		{name: "empty input", input: "", wantNone: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match, ambiguous := resolveTransition(tt.input, transitions)

			if tt.wantNone {
				if match != nil {
					t.Errorf("expected no match, got %s", match.Name)
				}
				if ambiguous != nil {
					t.Errorf("expected no ambiguous, got %d", len(ambiguous))
				}
				return
			}

			if tt.wantAmbig > 0 {
				if match != nil {
					t.Errorf("expected no match for ambiguous, got %s", match.Name)
				}
				if len(ambiguous) != tt.wantAmbig {
					t.Errorf("expected %d ambiguous, got %d", tt.wantAmbig, len(ambiguous))
				}
				return
			}

			if match == nil {
				t.Fatal("expected match, got nil")
				return
			}
			if match.ID != tt.wantMatch {
				t.Errorf("expected ID %s, got %s", tt.wantMatch, match.ID)
			}
		})
	}
}

func TestResolveTransition_Ambiguous(t *testing.T) {
	transitions := []*jira.Transition{
		{ID: "11", Name: "Done"},
		{ID: "21", Name: "done"},
	}

	match, ambiguous := resolveTransition("done", transitions)
	if match != nil {
		t.Errorf("expected no match for ambiguous, got %s", match.Name)
	}
	if len(ambiguous) != 2 {
		t.Errorf("expected 2 ambiguous, got %d", len(ambiguous))
	}
}

func TestFormatTransitionNames(t *testing.T) {
	transitions := []*jira.Transition{
		{ID: "11", Name: "To Do"},
		{ID: "21", Name: "In Progress"},
	}

	result := formatTransitionNames(transitions)
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	if !strings.Contains(result, "To Do") || !strings.Contains(result, "In Progress") {
		t.Errorf("expected transition names in output, got %s", result)
	}
	if !strings.Contains(result, "11") || !strings.Contains(result, "21") {
		t.Errorf("expected transition IDs in output, got %s", result)
	}
}
