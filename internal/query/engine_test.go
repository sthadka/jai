package query

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open DB: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestExecute_Basic(t *testing.T) {
	database := openTestDB(t)
	cfg := &config.Config{}

	// Insert test data.
	issue := &db.Issue{Key: "TEST-1", Project: "TEST", Summary: "Test issue", RawJSON: "{}"}
	if err := database.UpsertIssue(issue, nil); err != nil {
		t.Fatalf("UpsertIssue: %v", err)
	}

	engine := New(database, cfg)
	results, err := engine.Execute("SELECT key, summary FROM issues")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if results.Count != 1 {
		t.Errorf("expected 1 row, got %d", results.Count)
	}
	if results.Columns[0] != "key" {
		t.Errorf("expected first column 'key', got %s", results.Columns[0])
	}
}

func TestExecute_TemplateVars(t *testing.T) {
	database := openTestDB(t)
	cfg := &config.Config{Me: "me@example.com"}

	engine := New(database, cfg)
	// Just verify template substitution doesn't crash with SQL using {{me}}.
	_, err := engine.Execute("SELECT '{{me}}' as me")
	if err != nil {
		t.Fatalf("Execute with template: %v", err)
	}
}

func TestResolveTemplates_BuiltinTimeVars(t *testing.T) {
	// Use a known Wednesday: 2024-07-17 (Wednesday)
	now := time.Date(2024, 7, 17, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{
		Me:   "user@example.com",
		Team: "my-team",
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"today", "{{today}}", "2024-07-17"},
		{"yesterday", "{{yesterday}}", "2024-07-16"},
		{"week_ago", "{{week_ago}}", "2024-07-10"},
		{"month_ago", "{{month_ago}}", "2024-06-17"},
		{"quarter_ago", "{{quarter_ago}}", "2024-04-18"},
		{"this_week is Monday", "{{this_week}}", "2024-07-15"},
		{"this_month", "{{this_month}}", "2024-07-01"},
		{"this_quarter Jul->Jul1", "{{this_quarter}}", "2024-07-01"},
		{"me", "{{me}}", "user@example.com"},
		{"team", "{{team}}", "my-team"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveTemplatesAt(tc.input, now, cfg)
			if err != nil {
				t.Fatalf("resolveTemplatesAt(%q) error: %v", tc.input, err)
			}
			if got != tc.expected {
				t.Errorf("resolveTemplatesAt(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestResolveTemplates_ThisWeekAlwaysMonday(t *testing.T) {
	cfg := &config.Config{}
	// Check every day of a week (Mon 2024-07-15 .. Sun 2024-07-21)
	for d := 15; d <= 21; d++ {
		now := time.Date(2024, 7, d, 12, 0, 0, 0, time.UTC)
		got, err := resolveTemplatesAt("{{this_week}}", now, cfg)
		if err != nil {
			t.Fatalf("day=%d: error: %v", d, err)
		}
		if got != "2024-07-15" {
			t.Errorf("day=%d (%s): this_week = %q, want 2024-07-15", d, now.Weekday(), got)
		}
	}
}

func TestResolveTemplates_ThisQuarterBoundaries(t *testing.T) {
	cfg := &config.Config{}
	tests := []struct {
		date     time.Time
		expected string
	}{
		{time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), "2024-01-01"},
		{time.Date(2024, 2, 15, 0, 0, 0, 0, time.UTC), "2024-01-01"},
		{time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC), "2024-01-01"},
		{time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC), "2024-04-01"},
		{time.Date(2024, 6, 30, 0, 0, 0, 0, time.UTC), "2024-04-01"},
		{time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC), "2024-07-01"},
		{time.Date(2024, 9, 30, 0, 0, 0, 0, time.UTC), "2024-07-01"},
		{time.Date(2024, 10, 1, 0, 0, 0, 0, time.UTC), "2024-10-01"},
		{time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC), "2024-10-01"},
	}
	for _, tc := range tests {
		t.Run(tc.date.Format("2006-01-02"), func(t *testing.T) {
			got, err := resolveTemplatesAt("{{this_quarter}}", tc.date, cfg)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if got != tc.expected {
				t.Errorf("this_quarter on %s = %q, want %q", tc.date.Format("2006-01-02"), got, tc.expected)
			}
		})
	}
}

func TestResolveTemplates_ParameterizedVars(t *testing.T) {
	now := time.Date(2024, 7, 17, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"days_ago:7 == week_ago", "{{days_ago:7}}", "2024-07-10"},
		{"days_ago:14", "{{days_ago:14}}", "2024-07-03"},
		{"days_ago:1 == yesterday", "{{days_ago:1}}", "2024-07-16"},
		{"weeks_ago:1", "{{weeks_ago:1}}", "2024-07-10"},
		{"weeks_ago:4", "{{weeks_ago:4}}", "2024-06-19"},
		{"months_ago:1", "{{months_ago:1}}", "2024-06-17"},
		{"months_ago:3", "{{months_ago:3}}", "2024-04-17"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveTemplatesAt(tc.input, now, cfg)
			if err != nil {
				t.Fatalf("resolveTemplatesAt(%q) error: %v", tc.input, err)
			}
			if got != tc.expected {
				t.Errorf("resolveTemplatesAt(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestResolveTemplates_DaysAgo7EqualsWeekAgo(t *testing.T) {
	now := time.Date(2024, 7, 17, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{}

	daysAgo, err := resolveTemplatesAt("{{days_ago:7}}", now, cfg)
	if err != nil {
		t.Fatalf("days_ago error: %v", err)
	}
	weekAgo, err := resolveTemplatesAt("{{week_ago}}", now, cfg)
	if err != nil {
		t.Fatalf("week_ago error: %v", err)
	}
	if daysAgo != weekAgo {
		t.Errorf("days_ago:7 (%s) != week_ago (%s)", daysAgo, weekAgo)
	}
}

func TestResolveTemplates_MonthsAgo1EqualsMonthAgo(t *testing.T) {
	// months_ago:1 uses AddDate(0,-1,0), month_ago uses AddDate(0,0,-30).
	// They match when the month has exactly 30 days. Use June 30 (30 days).
	// Actually per spec: month_ago is 30 days ago, months_ago:1 is calendar month.
	// Let's test that both resolve without error and are plausible dates.
	now := time.Date(2024, 7, 30, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{}

	monthsAgo, err := resolveTemplatesAt("{{months_ago:1}}", now, cfg)
	if err != nil {
		t.Fatalf("months_ago error: %v", err)
	}
	monthAgo, err := resolveTemplatesAt("{{month_ago}}", now, cfg)
	if err != nil {
		t.Fatalf("month_ago error: %v", err)
	}

	// months_ago:1 on July 30 → June 30
	if monthsAgo != "2024-06-30" {
		t.Errorf("months_ago:1 = %q, want 2024-06-30", monthsAgo)
	}
	// month_ago on July 30 → 30 days ago → June 30
	if monthAgo != "2024-06-30" {
		t.Errorf("month_ago = %q, want 2024-06-30", monthAgo)
	}
}

func TestResolveTemplates_InvalidParameterizedLeftAsIs(t *testing.T) {
	now := time.Date(2024, 7, 17, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{}

	tests := []struct {
		name  string
		input string
	}{
		{"unknown_var", "{{unknown_ago:5}}"},
		{"no_number", "{{days_ago:abc}}"},
		{"wrong_format", "{{days_ago}}"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveTemplatesAt(tc.input, now, cfg)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if got != tc.input {
				t.Errorf("expected %q to be left as-is, got %q", tc.input, got)
			}
		})
	}
}

func TestResolveTemplates_Projects(t *testing.T) {
	now := time.Date(2024, 7, 17, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		cfg      *config.Config
		expected string
	}{
		{
			"single project",
			&config.Config{
				SyncSources: []config.SyncSource{
					{Projects: []string{"PROJ1"}},
				},
			},
			"'PROJ1'",
		},
		{
			"multiple projects across sources",
			&config.Config{
				SyncSources: []config.SyncSource{
					{Projects: []string{"PROJ1", "PROJ2"}},
					{Projects: []string{"PROJ3"}},
				},
			},
			"'PROJ1','PROJ2','PROJ3'",
		},
		{
			"deduplicates projects",
			&config.Config{
				SyncSources: []config.SyncSource{
					{Projects: []string{"PROJ1", "PROJ2"}},
					{Projects: []string{"PROJ2", "PROJ3"}},
				},
			},
			"'PROJ1','PROJ2','PROJ3'",
		},
		{
			"no sync sources",
			&config.Config{},
			"",
		},
		{
			"jql source with simple equality",
			&config.Config{
				SyncSources: []config.SyncSource{
					{JQL: "PROJECT = ROX"},
				},
			},
			"'ROX'",
		},
		{
			"jql source with IN clause",
			&config.Config{
				SyncSources: []config.SyncSource{
					{JQL: "project in (ROX, OTHER)"},
				},
			},
			"'ROX','OTHER'",
		},
		{
			"jql source with trailing clause",
			&config.Config{
				SyncSources: []config.SyncSource{
					{JQL: "PROJECT = ROX AND status != Done"},
				},
			},
			"'ROX'",
		},
		{
			"explicit projects list takes precedence over jql",
			&config.Config{
				SyncSources: []config.SyncSource{
					{JQL: "PROJECT = ROX", Projects: []string{"OVERRIDE"}},
				},
			},
			"'OVERRIDE'",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveTemplatesAt("{{projects}}", now, tc.cfg)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if got != tc.expected {
				t.Errorf("projects = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestResolveTemplates_MultipleVarsInQuery(t *testing.T) {
	now := time.Date(2024, 7, 17, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{
		Me: "user@example.com",
		SyncSources: []config.SyncSource{
			{Projects: []string{"PROJ1"}},
		},
	}

	input := "SELECT * FROM issues WHERE assignee = '{{me}}' AND updated >= '{{days_ago:14}}' AND project IN ({{projects}})"
	expected := "SELECT * FROM issues WHERE assignee = 'user@example.com' AND updated >= '2024-07-03' AND project IN ('PROJ1')"
	got, err := resolveTemplatesAt(input, now, cfg)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if got != expected {
		t.Errorf("multi-var query:\ngot:  %s\nwant: %s", got, expected)
	}
}

func TestTable_Empty(t *testing.T) {
	r := &Results{Columns: []string{"key"}, Rows: nil, Count: 0}
	out := r.Table()
	if out != "(no results)\n" {
		t.Errorf("expected '(no results)\\n', got %q", out)
	}
}

func TestTable_Format(t *testing.T) {
	r := &Results{
		Columns: []string{"key", "summary"},
		Rows:    [][]interface{}{{"TEST-1", "My issue"}},
		Count:   1,
	}
	out := r.Table()
	if out == "" {
		t.Error("expected non-empty table output")
	}
}

func TestResolveTemplates_Snippets(t *testing.T) {
	now := time.Date(2024, 7, 17, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		input    string
		cfg      *config.Config
		expected string
		wantErr  bool
		errMsg   string
	}{
		{
			name:  "simple snippet",
			input: "SELECT * FROM issues WHERE {{active}}",
			cfg: &config.Config{
				Snippets: map[string]string{
					"active": "status NOT IN ('Done', 'Closed')",
				},
			},
			expected: "SELECT * FROM issues WHERE status NOT IN ('Done', 'Closed')",
		},
		{
			name:  "snippet referencing built-in variable",
			input: "SELECT * FROM issues WHERE {{my_open}}",
			cfg: &config.Config{
				Me: "user@example.com",
				Snippets: map[string]string{
					"my_open": "assignee = 'user@example.com' AND status != 'Done'",
				},
			},
			expected: "SELECT * FROM issues WHERE assignee = 'user@example.com' AND status != 'Done'",
		},
		{
			name:  "recursive snippet — snippet references another snippet",
			input: "SELECT * FROM issues WHERE {{my_open}}",
			cfg: &config.Config{
				Me: "user@example.com",
				Snippets: map[string]string{
					"active":  "status NOT IN ('Done', 'Closed')",
					"my_open": "assignee = 'user@example.com' AND {{active}}",
				},
			},
			expected: "SELECT * FROM issues WHERE assignee = 'user@example.com' AND status NOT IN ('Done', 'Closed')",
		},
		{
			name:  "snippet with built-in var inside",
			input: "SELECT * FROM issues WHERE {{my_tasks}}",
			cfg: &config.Config{
				Me: "user@example.com",
				Snippets: map[string]string{
					"my_tasks": "assignee = '{{me}}'",
				},
			},
			// {{me}} is resolved first by resolveBuiltins, then snippet expands
			expected: "SELECT * FROM issues WHERE assignee = 'user@example.com'",
		},
		{
			name:  "circular reference A→B→A",
			input: "{{a}}",
			cfg: &config.Config{
				Snippets: map[string]string{
					"a": "{{b}}",
					"b": "{{a}}",
				},
			},
			wantErr: true,
			errMsg:  "circular snippet reference: a",
		},
		{
			name:  "self-referencing snippet",
			input: "{{loop}}",
			cfg: &config.Config{
				Snippets: map[string]string{
					"loop": "{{loop}}",
				},
			},
			wantErr: true,
			errMsg:  "circular snippet reference: loop",
		},
		{
			name:  "unknown snippet left as-is",
			input: "SELECT * FROM issues WHERE {{unknown_snippet}}",
			cfg: &config.Config{
				Snippets: map[string]string{
					"active": "status != 'Done'",
				},
			},
			expected: "SELECT * FROM issues WHERE {{unknown_snippet}}",
		},
		{
			name:  "empty snippets map",
			input: "SELECT * FROM issues WHERE {{active}}",
			cfg:   &config.Config{},
			// No snippets defined — left as-is
			expected: "SELECT * FROM issues WHERE {{active}}",
		},
		{
			name:  "multiple snippets in one query",
			input: "SELECT * FROM issues WHERE {{active}} AND {{high_pri}}",
			cfg: &config.Config{
				Snippets: map[string]string{
					"active":   "status != 'Done'",
					"high_pri": "priority IN ('Highest', 'High')",
				},
			},
			expected: "SELECT * FROM issues WHERE status != 'Done' AND priority IN ('Highest', 'High')",
		},
		{
			name:  "deep recursive chain within limit",
			input: "{{s1}}",
			cfg: &config.Config{
				Snippets: map[string]string{
					"s1": "{{s2}}",
					"s2": "{{s3}}",
					"s3": "resolved",
				},
			},
			expected: "resolved",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveTemplatesAt(tc.input, now, tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errMsg)
				}
				if !strings.Contains(err.Error(), tc.errMsg) {
					t.Errorf("error = %q, want it to contain %q", err.Error(), tc.errMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.expected {
				t.Errorf("got %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestExpandSnippet(t *testing.T) {
	now := time.Date(2024, 7, 17, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{
		Me: "user@example.com",
		Snippets: map[string]string{
			"active":  "status NOT IN ('Done', 'Closed')",
			"my_open": "assignee = '{{me}}' AND {{active}}",
		},
	}

	// Test expanding my_open, which references {{me}} (built-in) and {{active}} (snippet).
	got, err := ExpandSnippet(cfg.Snippets["my_open"], now, cfg)
	if err != nil {
		t.Fatalf("ExpandSnippet error: %v", err)
	}
	expected := "assignee = 'user@example.com' AND status NOT IN ('Done', 'Closed')"
	if got != expected {
		t.Errorf("ExpandSnippet = %q, want %q", got, expected)
	}
}

func TestJSONBytes(t *testing.T) {
	r := &Results{
		Columns: []string{"key"},
		Rows:    [][]interface{}{{"TEST-1"}},
		Count:   1,
	}
	data, err := r.JSONBytes()
	if err != nil {
		t.Fatalf("JSONBytes: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty JSON")
	}
}
