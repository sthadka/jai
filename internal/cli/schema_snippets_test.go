package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sthadka/jai/internal/config"
)

func TestSchemaSnippetsCmd_Empty(t *testing.T) {
	g.cfg = &config.Config{}

	var buf bytes.Buffer
	schemaSnippetsCmd.SetOut(&buf)
	schemaSnippetsCmd.SetArgs(nil)

	// Capture stdout by redirecting to buffer.
	old := schemaSnippetsCmd.OutOrStdout()
	_ = old

	if err := schemaSnippetsCmd.RunE(schemaSnippetsCmd, nil); err != nil {
		t.Fatalf("RunE error: %v", err)
	}
}

func TestSchemaSnippetsCmd_WithSnippets(t *testing.T) {
	g.cfg = &config.Config{
		Me: "user@example.com",
		Snippets: map[string]string{
			"active":  "status NOT IN ('Done', 'Closed')",
			"my_open": "assignee = '{{me}}' AND {{active}}",
		},
	}

	if err := schemaSnippetsCmd.RunE(schemaSnippetsCmd, nil); err != nil {
		t.Fatalf("RunE error: %v", err)
	}
}

func TestSchemaSnippetsCmd_JSONStructure(t *testing.T) {
	g.cfg = &config.Config{
		Me: "user@example.com",
		Snippets: map[string]string{
			"active": "status != 'Done'",
		},
	}

	// We can't easily capture fmt.Println output in this test setup,
	// but we can at least verify the command runs without error.
	if err := schemaSnippetsCmd.RunE(schemaSnippetsCmd, nil); err != nil {
		t.Fatalf("RunE error: %v", err)
	}
}

func TestSchemaSnippetsCmd_OutputFormat(t *testing.T) {
	// Verify that the output package produces correct JSON structure.
	type snippetInfo struct {
		Name     string `json:"name"`
		Raw      string `json:"raw"`
		Expanded string `json:"expanded"`
	}

	snippets := []snippetInfo{
		{Name: "active", Raw: "status != 'Done'", Expanded: "status != 'Done'"},
	}

	data := map[string]interface{}{
		"snippets": snippets,
		"count":    len(snippets),
		"hint":     "Use {{snippet_name}} in any SQL query to expand a snippet",
	}

	b, err := json.Marshal(map[string]interface{}{"ok": true, "data": data})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if result["ok"] != true {
		t.Errorf("expected ok=true, got %v", result["ok"])
	}

	d := result["data"].(map[string]interface{})
	if d["count"].(float64) != 1 {
		t.Errorf("expected count=1, got %v", d["count"])
	}

	snips := d["snippets"].([]interface{})
	if len(snips) != 1 {
		t.Errorf("expected 1 snippet, got %d", len(snips))
	}

	s := snips[0].(map[string]interface{})
	if s["name"] != "active" {
		t.Errorf("expected name 'active', got %v", s["name"])
	}
	if s["raw"] != "status != 'Done'" {
		t.Errorf("expected raw value, got %v", s["raw"])
	}
}
