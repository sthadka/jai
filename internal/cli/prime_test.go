package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sthadka/jai/v6/pkg/config"
	db "github.com/sthadka/jai/v6/pkg/store"
)

// primeTestConfig returns a config with a sync source, a named view, and a
// metrics block, so prime has real scope/view/metrics content to render.
func primeTestConfig(dbPath string) *config.Config {
	return &config.Config{
		Me:   "alice@example.com",
		Jira: config.JiraConfig{URL: "https://acme.atlassian.net"},
		DB:   config.DBConfig{Path: dbPath},
		SyncSources: []config.SyncSource{
			{Name: "myteam", JQL: "project = ENG AND assignee = currentUser()"},
		},
		Views: []config.ViewConfig{
			{Name: "open-bugs", Title: "Open bugs"},
		},
		Metrics: &config.MetricsConfig{
			StatusStages:    map[string][]string{"active": {"In Progress"}, "done": {"Done"}},
			DoneResolutions: []string{"Done", "Fixed"},
			TypeClasses:     map[string][]string{"bug": {"Bug"}},
			TrackedFields:   []string{"status"},
		},
	}
}

func setupPrime(t *testing.T, withIssue bool) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "prime.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	if withIssue {
		if err := database.UpsertIssue(&db.Issue{
			Key: "ENG-1", Project: "ENG", Summary: "s", Status: "In Progress", RawJSON: "{}",
		}, nil); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	g.engine.Store = database
	g.engine.Config = primeTestConfig(dbPath)
	g.engine.Client = nil // offline: prime must degrade gracefully
	g.jsonOut = false
	t.Cleanup(func() {
		g.engine.Store = nil
		g.engine.Config = nil
		g.engine.Client = nil
		g.jsonOut = false
	})
	return dbPath
}

func TestPrimeMarkdownPopulated(t *testing.T) {
	setupPrime(t, true)

	out := captureStdout(t, func() {
		if err := callRunE(primeCmd, nil); err != nil {
			t.Fatalf("prime: %v", err)
		}
	})

	// Every acceptance section must be present and grounded in live config/DB.
	for _, want := range []string{
		"# jai agent context",
		"## Status & freshness",
		"alice@example.com",
		"https://acme.atlassian.net",
		"Issues: 1 total",
		"ENG: 1",
		"## Synced sources (scope)",
		"project = ENG AND assignee = currentUser()", // source JQL, not a hardcoded project name
		"## Query surface",
		"SELECT`/`WITH", // read-only guard described
		"json_extract",  // field_map resolution
		"## Fields",
		"## Named views",
		"jai view open-bugs",
		"## Flow metrics",
		"Done, Fixed", // done_resolutions
		"## MCP tools",
		"jai_query", // default toolsets include read
		"## Example queries",
		"assignee = 'alice@example.com'", // example grounded in configured `me`
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prime output missing %q\n---\n%s", want, out)
		}
	}
}

func TestPrimeEmptyDBDegrades(t *testing.T) {
	setupPrime(t, false)

	out := captureStdout(t, func() {
		if err := callRunE(primeCmd, nil); err != nil {
			t.Fatalf("prime: %v", err)
		}
	})

	// With no issues, prime still prints the configured scope and a sync hint,
	// and must not claim issues it does not have.
	if !strings.Contains(out, "Issues: 0 total") {
		t.Errorf("expected zero-issue count, got:\n%s", out)
	}
	if !strings.Contains(out, "never synced") {
		t.Errorf("expected 'never synced' for unsynced source, got:\n%s", out)
	}
	if !strings.Contains(out, "myteam") {
		t.Errorf("expected configured source name, got:\n%s", out)
	}
}

func TestPrimeJSON(t *testing.T) {
	setupPrime(t, true)
	g.jsonOut = true

	out := captureStdout(t, func() {
		if err := callRunE(primeCmd, nil); err != nil {
			t.Fatalf("prime: %v", err)
		}
	})

	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Me string `json:"me"`
			DB struct {
				TotalIssues int `json:"total_issues"`
			} `json:"db"`
			Sources []struct {
				Name string `json:"name"`
				JQL  string `json:"jql"`
			} `json:"sources"`
			Metrics struct {
				Enabled         bool     `json:"enabled"`
				DoneResolutions []string `json:"done_resolutions"`
			} `json:"metrics"`
			MCPTools []string `json:"mcp_tools"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if !env.OK {
		t.Errorf("expected ok=true")
	}
	if env.Data.Me != "alice@example.com" {
		t.Errorf("me = %q", env.Data.Me)
	}
	if env.Data.DB.TotalIssues != 1 {
		t.Errorf("total_issues = %d", env.Data.DB.TotalIssues)
	}
	if len(env.Data.Sources) != 1 || env.Data.Sources[0].Name != "myteam" {
		t.Errorf("sources = %+v", env.Data.Sources)
	}
	if !env.Data.Metrics.Enabled || len(env.Data.Metrics.DoneResolutions) != 2 {
		t.Errorf("metrics = %+v", env.Data.Metrics)
	}
	if len(env.Data.MCPTools) == 0 {
		t.Errorf("expected enabled MCP tools")
	}
}
