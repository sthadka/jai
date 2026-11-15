package jira

import (
	"encoding/json"
	"testing"
)

// findNodes walks an ADF document and returns all nodes of the given type.
func findNodes(doc map[string]interface{}, nodeType string) []map[string]interface{} {
	raw, _ := json.Marshal(doc)
	var root map[string]interface{}
	_ = json.Unmarshal(raw, &root)
	var found []map[string]interface{}
	var walk func(n map[string]interface{})
	walk = func(n map[string]interface{}) {
		if n["type"] == nodeType {
			found = append(found, n)
		}
		if content, ok := n["content"].([]interface{}); ok {
			for _, c := range content {
				if cm, ok := c.(map[string]interface{}); ok {
					walk(cm)
				}
			}
		}
	}
	walk(root)
	return found
}

// firstMarkTypes returns the mark types on the first text node whose text matches.
func markTypesFor(doc map[string]interface{}, text string) []string {
	for _, n := range findNodes(doc, "text") {
		if n["text"] == text {
			var out []string
			if marks, ok := n["marks"].([]interface{}); ok {
				for _, m := range marks {
					if mm, ok := m.(map[string]interface{}); ok {
						out = append(out, mm["type"].(string))
					}
				}
			}
			return out
		}
	}
	return nil
}

func TestMarkdownToADF_TopLevelShape(t *testing.T) {
	doc := MarkdownToADF("hello")
	if doc["type"] != "doc" || doc["version"] != 1 {
		t.Fatalf("bad top-level shape: %v", doc)
	}
	if len(findNodes(doc, "paragraph")) != 1 {
		t.Errorf("expected 1 paragraph")
	}
}

func TestMarkdownToADF_Empty(t *testing.T) {
	doc := MarkdownToADF("")
	paras := findNodes(doc, "paragraph")
	if len(paras) != 1 {
		t.Fatalf("expected 1 empty paragraph, got %d", len(paras))
	}
	if _, hasContent := paras[0]["content"]; hasContent {
		t.Errorf("empty paragraph should carry no content array")
	}
}

func TestMarkdownToADF_Headings(t *testing.T) {
	doc := MarkdownToADF("# Title\n\n## Subtitle")
	headings := findNodes(doc, "heading")
	if len(headings) != 2 {
		t.Fatalf("expected 2 headings, got %d", len(headings))
	}
	attrs := headings[0]["attrs"].(map[string]interface{})
	if attrs["level"].(float64) != 1 {
		t.Errorf("expected first heading level 1, got %v", attrs["level"])
	}
}

func TestMarkdownToADF_InlineMarks(t *testing.T) {
	doc := MarkdownToADF("This is **bold** and *italic* and `code`.")
	if got := markTypesFor(doc, "bold"); len(got) != 1 || got[0] != "strong" {
		t.Errorf("bold marks = %v, want [strong]", got)
	}
	if got := markTypesFor(doc, "italic"); len(got) != 1 || got[0] != "em" {
		t.Errorf("italic marks = %v, want [em]", got)
	}
	if got := markTypesFor(doc, "code"); len(got) != 1 || got[0] != "code" {
		t.Errorf("code marks = %v, want [code]", got)
	}
}

func TestMarkdownToADF_Link(t *testing.T) {
	doc := MarkdownToADF("See [the docs](https://example.com/x).")
	for _, n := range findNodes(doc, "text") {
		if n["text"] != "the docs" {
			continue
		}
		marks := n["marks"].([]interface{})
		m := marks[0].(map[string]interface{})
		if m["type"] != "link" {
			t.Fatalf("expected link mark, got %v", m["type"])
		}
		attrs := m["attrs"].(map[string]interface{})
		if attrs["href"] != "https://example.com/x" {
			t.Errorf("href = %v", attrs["href"])
		}
		return
	}
	t.Fatal("link text node not found")
}

func TestMarkdownToADF_BulletList(t *testing.T) {
	doc := MarkdownToADF("- one\n- two\n- three")
	if len(findNodes(doc, "bulletList")) != 1 {
		t.Errorf("expected 1 bulletList")
	}
	if n := len(findNodes(doc, "listItem")); n != 3 {
		t.Errorf("expected 3 listItems, got %d", n)
	}
}

func TestMarkdownToADF_OrderedList(t *testing.T) {
	doc := MarkdownToADF("1. one\n2. two")
	if len(findNodes(doc, "orderedList")) != 1 {
		t.Errorf("expected 1 orderedList")
	}
}

func TestMarkdownToADF_TaskList(t *testing.T) {
	doc := MarkdownToADF("- [ ] todo item\n- [x] done item")
	if len(findNodes(doc, "taskList")) != 1 {
		t.Fatalf("expected 1 taskList")
	}
	items := findNodes(doc, "taskItem")
	if len(items) != 2 {
		t.Fatalf("expected 2 taskItems, got %d", len(items))
	}
	if items[0]["attrs"].(map[string]interface{})["state"] != "TODO" {
		t.Errorf("first item should be TODO")
	}
	if items[1]["attrs"].(map[string]interface{})["state"] != "DONE" {
		t.Errorf("second item should be DONE")
	}
	// localId is required by Jira on task lists/items.
	if items[0]["attrs"].(map[string]interface{})["localId"] == "" {
		t.Errorf("taskItem requires a localId")
	}
}

func TestMarkdownToADF_Table(t *testing.T) {
	md := "| A | B |\n|---|---|\n| 1 | 2 |"
	doc := MarkdownToADF(md)
	if len(findNodes(doc, "table")) != 1 {
		t.Fatalf("expected 1 table")
	}
	if n := len(findNodes(doc, "tableHeader")); n != 2 {
		t.Errorf("expected 2 header cells, got %d", n)
	}
	if n := len(findNodes(doc, "tableCell")); n != 2 {
		t.Errorf("expected 2 body cells, got %d", n)
	}
	if n := len(findNodes(doc, "tableRow")); n != 2 {
		t.Errorf("expected 2 rows, got %d", n)
	}
}

func TestMarkdownToADF_CodeBlock(t *testing.T) {
	md := "```go\nfmt.Println(\"hi\")\n```"
	doc := MarkdownToADF(md)
	blocks := findNodes(doc, "codeBlock")
	if len(blocks) != 1 {
		t.Fatalf("expected 1 codeBlock, got %d", len(blocks))
	}
	attrs := blocks[0]["attrs"].(map[string]interface{})
	if attrs["language"] != "go" {
		t.Errorf("language = %v, want go", attrs["language"])
	}
	content := blocks[0]["content"].([]interface{})
	textNode := content[0].(map[string]interface{})
	if textNode["text"] != "fmt.Println(\"hi\")" {
		t.Errorf("code text = %q", textNode["text"])
	}
}

func TestMarkdownToADF_ValidJSON(t *testing.T) {
	// A representative brief exercising most node types must marshal cleanly
	// and round-trip to non-empty plaintext.
	md := "# Brief\n\nSome **intro** text with a [link](https://x.io).\n\n" +
		"## Acceptance criteria\n\n- [ ] first\n- [x] second\n\n" +
		"| Field | Value |\n|---|---|\n| a | b |\n\n```\ncode\n```"
	doc := MarkdownToADF(md)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := ADFToPlaintext(raw); got == "" {
		t.Error("expected non-empty plaintext round-trip")
	}
}
