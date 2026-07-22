package cli

import (
	"testing"
)

func TestWatchCmd_ArgsValidation(t *testing.T) {
	cmd := watchCmd

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "zero args", args: []string{}, wantErr: true},
		{name: "one arg (issue key)", args: []string{"PROJ-123"}, wantErr: false},
		{name: "two args (issue key + user)", args: []string{"PROJ-123", "user@example.com"}, wantErr: false},
		{name: "three args", args: []string{"PROJ-123", "user@example.com", "extra"}, wantErr: true},
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

func TestUnwatchCmd_ArgsValidation(t *testing.T) {
	cmd := unwatchCmd

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "zero args", args: []string{}, wantErr: true},
		{name: "one arg (issue key)", args: []string{"PROJ-123"}, wantErr: false},
		{name: "two args", args: []string{"PROJ-123", "extra"}, wantErr: true},
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

func TestWatchCmd_Registration(t *testing.T) {
	found := false
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "watch" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'watch' command to be registered on rootCmd")
	}
}

func TestUnwatchCmd_Registration(t *testing.T) {
	found := false
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "unwatch" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'unwatch' command to be registered on rootCmd")
	}
}

func TestWatchCmd_UseString(t *testing.T) {
	if watchCmd.Use != "watch <issue-key> [user]" {
		t.Errorf("unexpected Use string: %s", watchCmd.Use)
	}
}

func TestUnwatchCmd_UseString(t *testing.T) {
	if unwatchCmd.Use != "unwatch <issue-key>" {
		t.Errorf("unexpected Use string: %s", unwatchCmd.Use)
	}
}
