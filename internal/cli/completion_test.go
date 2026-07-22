package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestCompletionBash(t *testing.T) {
	root := newRootCmd()
	root.AddCommand(completionCmd)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"completion", "bash"})

	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if out == "" {
		t.Fatal("expected non-empty bash completion output")
	}
	if !strings.Contains(out, "bash") {
		t.Error("bash completion output does not reference bash")
	}
}

func TestCompletionZsh(t *testing.T) {
	root := newRootCmd()
	root.AddCommand(completionCmd)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"completion", "zsh"})

	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if out == "" {
		t.Fatal("expected non-empty zsh completion output")
	}
}

func TestCompletionFish(t *testing.T) {
	root := newRootCmd()
	root.AddCommand(completionCmd)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"completion", "fish"})

	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if out == "" {
		t.Fatal("expected non-empty fish completion output")
	}
	if !strings.Contains(out, "fish") {
		t.Error("fish completion output does not reference fish")
	}
}

func TestCompletionPowershell(t *testing.T) {
	root := newRootCmd()
	root.AddCommand(completionCmd)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"completion", "powershell"})

	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if out == "" {
		t.Fatal("expected non-empty powershell completion output")
	}
}

func TestCompletionInvalidShell(t *testing.T) {
	root := newRootCmd()
	root.AddCommand(completionCmd)

	var buf bytes.Buffer
	root.SetErr(&buf)
	root.SetArgs([]string{"completion", "invalid"})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for invalid shell type")
	}
}
