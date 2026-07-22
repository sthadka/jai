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
		{name: "two args", args: []string{"ROX-1", "ROX-2"}, wantErr: false},
		{name: "three args", args: []string{"ROX-1", "ROX-2", "ROX-3"}, wantErr: true},
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
