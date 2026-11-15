package jira

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestADFToMarkdown_LinksPreserved guards against the smart-link/hyperlink
// drop bug: link marks and card nodes (inlineCard/embedCard/blockCard) must
// keep their URLs when an ADF description is converted to markdown.
func TestADFToMarkdown_LinksPreserved(t *testing.T) {
	adf := json.RawMessage(`{
		"type": "doc",
		"version": 1,
		"content": [
			{
				"type": "paragraph",
				"content": [
					{"type": "text", "text": "See project document:"}
				]
			},
			{
				"type": "paragraph",
				"content": [
					{
						"type": "inlineCard",
						"attrs": {"url": "https://docs.google.com/document/d/1gXyX/edit"}
					}
				]
			},
			{
				"type": "paragraph",
				"content": [
					{
						"type": "text",
						"text": "hyperlink",
						"marks": [
							{"type": "link", "attrs": {"href": "https://example.com/spec"}}
						]
					}
				]
			},
			{
				"type": "blockCard",
				"attrs": {"url": "https://confluence.example.com/page/123"}
			}
		]
	}`)

	got := ADFToMarkdown(adf)

	wantURLs := []string{
		"https://docs.google.com/document/d/1gXyX/edit",
		"https://example.com/spec",
		"https://confluence.example.com/page/123",
	}
	for _, u := range wantURLs {
		if !strings.Contains(got, u) {
			t.Errorf("expected markdown to contain URL %q, got:\n%s", u, got)
		}
	}
	// The link mark should render as a markdown link.
	if !strings.Contains(got, "[hyperlink](https://example.com/spec)") {
		t.Errorf("expected markdown link syntax for link mark, got:\n%s", got)
	}
}

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
