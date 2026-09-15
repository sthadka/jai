package jira

import (
	"encoding/json"
	"testing"
)

// roundTrip converts text to ADF and back to plaintext to verify the document
// is well-formed and preserves content.
func roundTrip(t *testing.T, text string) string {
	t.Helper()
	doc := TextToADF(text)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal ADF: %v", err)
	}
	return ADFToPlaintext(raw)
}

func TestTextToADF_TopLevelShape(t *testing.T) {
	doc := TextToADF("hello")
	if doc["type"] != "doc" {
		t.Errorf("expected type=doc, got %v", doc["type"])
	}
	if doc["version"] != 1 {
		t.Errorf("expected version=1, got %v", doc["version"])
	}
	content, ok := doc["content"].([]map[string]interface{})
	if !ok || len(content) == 0 {
		t.Fatalf("expected non-empty content, got %v", doc["content"])
	}
}

func TestTextToADF_SingleParagraph(t *testing.T) {
	if got := roundTrip(t, "Hello, world!"); got != "Hello, world!" {
		t.Errorf("expected 'Hello, world!', got %q", got)
	}
}

func TestTextToADF_MultiParagraph(t *testing.T) {
	doc := TextToADF("First para\n\nSecond para")
	content := doc["content"].([]map[string]interface{})
	if len(content) != 2 {
		t.Fatalf("expected 2 paragraphs, got %d", len(content))
	}
	got := roundTrip(t, "First para\n\nSecond para")
	if got == "" {
		t.Fatal("expected non-empty round-trip result")
	}
}

func TestTextToADF_Empty(t *testing.T) {
	doc := TextToADF("")
	content := doc["content"].([]map[string]interface{})
	if len(content) != 1 {
		t.Fatalf("expected 1 empty paragraph, got %d", len(content))
	}
	// An empty paragraph must have no (empty-string) text node, which is invalid ADF.
	if _, hasContent := content[0]["content"]; hasContent {
		t.Errorf("empty paragraph should not carry a content array")
	}
}

func TestTextToADF_NoEmptyTextNodes(t *testing.T) {
	// Lines that are empty must not produce text nodes with empty strings.
	doc := TextToADF("line1\n\nline3")
	raw, _ := json.Marshal(doc)
	var parsed map[string]interface{}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var walk func(node interface{})
	walk = func(node interface{}) {
		m, ok := node.(map[string]interface{})
		if !ok {
			return
		}
		if m["type"] == "text" {
			if s, _ := m["text"].(string); s == "" {
				t.Errorf("found empty text node")
			}
		}
		if c, ok := m["content"].([]interface{}); ok {
			for _, child := range c {
				walk(child)
			}
		}
	}
	walk(parsed)
}

func TestTextToADF_CRLF(t *testing.T) {
	doc := TextToADF("a\r\n\r\nb")
	content := doc["content"].([]map[string]interface{})
	if len(content) != 2 {
		t.Fatalf("expected CRLF to split into 2 paragraphs, got %d", len(content))
	}
}

func TestIsADFField(t *testing.T) {
	cases := []struct {
		jiraID    string
		fieldType string
		want      bool
	}{
		{"description", "text", true},     // seeded type predates richtext
		{"description", "richtext", true}, // after re-sync/migration
		{"environment", "text", true},
		{"customfield_10050", "richtext", true},
		{"summary", "text", false}, // plain single-line text
		{"priority", "option", false},
		{"customfield_10010", "text", false},
	}
	for _, c := range cases {
		if got := IsADFField(c.jiraID, c.fieldType); got != c.want {
			t.Errorf("IsADFField(%q, %q) = %v, want %v", c.jiraID, c.fieldType, got, c.want)
		}
	}
}

func TestSchemaIsADF(t *testing.T) {
	cases := []struct {
		name   string
		schema *FieldSchema
		want   bool
	}{
		{"nil", nil, false},
		{"description", &FieldSchema{Type: "string", System: "description"}, true},
		{"environment", &FieldSchema{Type: "string", System: "environment"}, true},
		{"summary", &FieldSchema{Type: "string", System: "summary"}, false},
		{"textarea custom", &FieldSchema{Type: "string", Custom: "com.atlassian.jira.plugin.system.customfieldtypes:textarea"}, true},
		{"textfield custom", &FieldSchema{Type: "string", Custom: "com.atlassian.jira.plugin.system.customfieldtypes:textfield"}, false},
	}
	for _, c := range cases {
		if got := SchemaIsADF(c.schema); got != c.want {
			t.Errorf("SchemaIsADF(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}
