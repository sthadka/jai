package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sthadka/jai/internal/config"
)

func TestOpenURLConstruction(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		key     string
		wantURL string
	}{
		{
			name:    "standard URL",
			baseURL: "https://mycompany.atlassian.net",
			key:     "PROJ-123",
			wantURL: "https://mycompany.atlassian.net/browse/PROJ-123",
		},
		{
			name:    "URL with trailing slash",
			baseURL: "https://mycompany.atlassian.net/",
			key:     "PROJ-456",
			wantURL: "https://mycompany.atlassian.net/browse/PROJ-456",
		},
		{
			name:    "lowercase key uppercased",
			baseURL: "https://jira.example.com",
			key:     "proj-789",
			wantURL: "https://jira.example.com/browse/PROJ-789",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origCfg := g.cfg
			origJSON := g.jsonOut
			origOpener := browserOpener
			defer func() {
				g.cfg = origCfg
				g.jsonOut = origJSON
				browserOpener = origOpener
			}()

			g.cfg = &config.Config{
				Jira: config.JiraConfig{URL: tt.baseURL},
			}
			g.jsonOut = false

			var opened string
			browserOpener = func(url string) error {
				opened = url
				return nil
			}

			// Bypass PersistentPreRunE by calling RunE directly.
			openCmd.SetOut(&bytes.Buffer{})
			if err := openCmd.RunE(openCmd, []string{tt.key}); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if opened != tt.wantURL {
				t.Errorf("opened URL = %q, want %q", opened, tt.wantURL)
			}
		})
	}
}

func TestOpenURLOnly(t *testing.T) {
	origCfg := g.cfg
	origJSON := g.jsonOut
	origOpener := browserOpener
	origURLOnly := openFlags.urlOnly
	defer func() {
		g.cfg = origCfg
		g.jsonOut = origJSON
		browserOpener = origOpener
		openFlags.urlOnly = origURLOnly
	}()

	g.cfg = &config.Config{
		Jira: config.JiraConfig{URL: "https://mycompany.atlassian.net"},
	}
	g.jsonOut = false
	openFlags.urlOnly = true

	browserCalled := false
	browserOpener = func(url string) error {
		browserCalled = true
		return nil
	}

	var buf bytes.Buffer
	openCmd.SetOut(&buf)

	if err := openCmd.RunE(openCmd, []string{"PROJ-123"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if browserCalled {
		t.Error("browser should not be opened with --url-only")
	}

	got := strings.TrimSpace(buf.String())
	want := "https://mycompany.atlassian.net/browse/PROJ-123"
	if got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestOpenJSON(t *testing.T) {
	origCfg := g.cfg
	origJSON := g.jsonOut
	origOpener := browserOpener
	defer func() {
		g.cfg = origCfg
		g.jsonOut = origJSON
		browserOpener = origOpener
	}()

	g.cfg = &config.Config{
		Jira: config.JiraConfig{URL: "https://mycompany.atlassian.net"},
	}
	g.jsonOut = true

	browserCalled := false
	browserOpener = func(url string) error {
		browserCalled = true
		return nil
	}

	var buf bytes.Buffer
	openCmd.SetOut(&buf)

	if err := openCmd.RunE(openCmd, []string{"PROJ-123"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if browserCalled {
		t.Error("browser should not be opened with --json")
	}

	var resp struct {
		OK   bool              `json:"ok"`
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON output: %v\nraw: %s", err, buf.String())
	}
	if !resp.OK {
		t.Error("expected ok=true")
	}
	want := "https://mycompany.atlassian.net/browse/PROJ-123"
	if resp.Data["url"] != want {
		t.Errorf("url = %q, want %q", resp.Data["url"], want)
	}
}

func TestOpenNoArgs(t *testing.T) {
	root := newRootCmd()
	root.AddCommand(openCmd)

	var buf bytes.Buffer
	root.SetErr(&buf)
	root.SetArgs([]string{"open"})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error when no issue key provided")
	}
}
