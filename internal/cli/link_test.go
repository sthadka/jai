package cli

import (
	"testing"
)

func TestLinkCmd_DefaultType(t *testing.T) {
	if linkFlags.linkType != "" {
		t.Skip("flags already initialized")
	}

	cmd := linkCmd
	flag := cmd.Flags().Lookup("type")
	if flag == nil {
		t.Fatal("expected --type flag to exist")
	}
	if flag.DefValue != "Relates" {
		t.Errorf("expected default link type %q, got %q", "Relates", flag.DefValue)
	}
}

func TestLinkCmd_ListTypesFlag(t *testing.T) {
	cmd := linkCmd
	flag := cmd.Flags().Lookup("list-types")
	if flag == nil {
		t.Fatal("expected --list-types flag to exist")
	}
	if flag.DefValue != "false" {
		t.Errorf("expected default list-types %q, got %q", "false", flag.DefValue)
	}
}

func TestLinkCmd_ArgsValidation(t *testing.T) {
	cmd := linkCmd
	if cmd.Args == nil {
		t.Fatal("expected Args validator to be set")
	}

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "zero args", args: []string{}, wantErr: false},
		{name: "one arg", args: []string{"ROX-1"}, wantErr: false},
		{name: "two args (issue link)", args: []string{"ROX-1", "ROX-2"}, wantErr: false},
		{name: "two args (remote link)", args: []string{"ROX-1", "https://example.com"}, wantErr: false},
		{name: "three args (remote link with title)", args: []string{"ROX-1", "https://example.com", "My Link"}, wantErr: false},
		{name: "four args", args: []string{"ROX-1", "ROX-2", "ROX-3", "ROX-4"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := cmd.Args(cmd, tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("Args(%v) error = %v, wantErr %v", tt.args, err, tt.wantErr)
			}
		})
	}
}

func TestIsURL(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{input: "https://example.com", want: true},
		{input: "http://example.com", want: true},
		{input: "https://github.com/org/repo/pull/42", want: true},
		{input: "PROJ-123", want: false},
		{input: "ROX-1", want: false},
		{input: "ftp://example.com", want: false},
		{input: "", want: false},
		{input: "httpsnot-a-url", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isURL(tt.input)
			if got != tt.want {
				t.Errorf("isURL(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestLinkCmd_UseStringUpdated(t *testing.T) {
	if linkCmd.Use != "link <issue-key> <target> [title]" {
		t.Errorf("unexpected Use string: %s", linkCmd.Use)
	}
}
