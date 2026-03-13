package jira

import (
	"encoding/json"
	"testing"
)

func TestADFToPlaintext_Text(t *testing.T) {
	adf := json.RawMessage(`{
		"type": "doc",
		"version": 1,
		"content": [
			{
				"type": "paragraph",
				"content": [
					{"type": "text", "text": "Hello, world!"}
				]
			}
		]
	}`)

	got := ADFToPlaintext(adf)
	if got != "Hello, world!" {
		t.Errorf("expected 'Hello, world!', got %q", got)
	}
}

func TestADFToPlaintext_PlainString(t *testing.T) {
	raw := json.RawMessage(`"plain text"`)
	got := ADFToPlaintext(raw)
	if got != "plain text" {
		t.Errorf("expected 'plain text', got %q", got)
	}
}

func TestADFToPlaintext_Nil(t *testing.T) {
	got := ADFToPlaintext(nil)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestADFToPlaintext_MultiParagraph(t *testing.T) {
	adf := json.RawMessage(`{
		"type": "doc",
		"version": 1,
		"content": [
			{
				"type": "paragraph",
				"content": [{"type": "text", "text": "First"}]
			},
			{
				"type": "paragraph",
				"content": [{"type": "text", "text": "Second"}]
			}
		]
	}`)

	got := ADFToPlaintext(adf)
	if got == "" {
		t.Error("expected non-empty result")
	}
	// Should contain both paragraphs.
	if len(got) < 11 {
		t.Errorf("expected at least 'First\\nSecond', got %q", got)
	}
}
