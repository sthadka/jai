package cli

import (
	"testing"
)

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
