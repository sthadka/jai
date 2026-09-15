package jira

import (
	"fmt"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	xast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// MarkdownToADF parses GitHub-flavored Markdown into an Atlassian Document
// Format (ADF) document, the structured shape Jira Cloud's REST v3 API requires
// for rich-text fields (description, environment, multi-line text customs).
//
// Supported constructs: headings, paragraphs, GFM tables, bullet/ordered lists,
// task lists (- [ ] / - [x]), blockquotes, thematic breaks, fenced/indented code
// blocks, and the strong/em/strikethrough/code/link inline marks. Plain text is a
// valid subset and converts to simple paragraphs.
func MarkdownToADF(src string) map[string]interface{} {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	source := []byte(src)

	md := goldmark.New(goldmark.WithExtensions(extension.GFM))
	doc := md.Parser().Parse(text.NewReader(source))

	r := &adfRenderer{source: source}
	content := r.renderBlocks(doc)
	if len(content) == 0 {
		content = []interface{}{map[string]interface{}{"type": "paragraph"}}
	}

	return map[string]interface{}{
		"type":    "doc",
		"version": 1,
		"content": content,
	}
}

// adfRenderer walks a goldmark AST and emits ADF JSON. It carries the source
// bytes (goldmark stores text as offsets into the source) and a counter used to
// mint the localId values ADF requires on task lists/items.
type adfRenderer struct {
	source  []byte
	localID int
}

func (r *adfRenderer) nextLocalID() string {
	r.localID++
	return fmt.Sprintf("md-%d", r.localID)
}

// renderBlocks renders the block-level children of a node into ADF nodes.
func (r *adfRenderer) renderBlocks(n ast.Node) []interface{} {
	var out []interface{}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if b := r.renderBlock(c); b != nil {
			out = append(out, b)
		}
	}
	return out
}

func (r *adfRenderer) renderBlock(n ast.Node) interface{} {
	switch v := n.(type) {
	case *ast.Heading:
		return map[string]interface{}{
			"type":    "heading",
			"attrs":   map[string]interface{}{"level": v.Level},
			"content": r.renderInlineChildren(v, nil),
		}
	case *ast.Paragraph:
		return paragraph(r.renderInlineChildren(v, nil))
	case *ast.TextBlock:
		// TextBlock appears inside tight list items and table cells.
		return paragraph(r.renderInlineChildren(v, nil))
	case *ast.FencedCodeBlock:
		return codeBlock(string(v.Language(r.source)), r.collectLines(v))
	case *ast.CodeBlock:
		return codeBlock("", r.collectLines(v))
	case *ast.Blockquote:
		content := r.renderBlocks(v)
		if len(content) == 0 {
			content = []interface{}{map[string]interface{}{"type": "paragraph"}}
		}
		return map[string]interface{}{"type": "blockquote", "content": content}
	case *ast.ThematicBreak:
		return map[string]interface{}{"type": "rule"}
	case *ast.List:
		return r.renderList(v)
	case *xast.Table:
		return r.renderTable(v)
	default:
		return nil
	}
}

func (r *adfRenderer) renderList(v *ast.List) interface{} {
	if r.listHasTask(v) {
		var items []interface{}
		for li := v.FirstChild(); li != nil; li = li.NextSibling() {
			state := "TODO"
			if checked, found := taskItemChecked(li); found && checked {
				state = "DONE"
			}
			items = append(items, map[string]interface{}{
				"type":    "taskItem",
				"attrs":   map[string]interface{}{"localId": r.nextLocalID(), "state": state},
				"content": r.taskItemInlines(li),
			})
		}
		return map[string]interface{}{
			"type":    "taskList",
			"attrs":   map[string]interface{}{"localId": r.nextLocalID()},
			"content": items,
		}
	}

	listType := "bulletList"
	if v.IsOrdered() {
		listType = "orderedList"
	}
	var items []interface{}
	for li := v.FirstChild(); li != nil; li = li.NextSibling() {
		content := r.renderBlocks(li)
		if len(content) == 0 {
			content = []interface{}{map[string]interface{}{"type": "paragraph"}}
		}
		items = append(items, map[string]interface{}{"type": "listItem", "content": content})
	}
	return map[string]interface{}{"type": listType, "content": items}
}

func (r *adfRenderer) renderTable(t *xast.Table) interface{} {
	var rows []interface{}
	for row := t.FirstChild(); row != nil; row = row.NextSibling() {
		_, isHeader := row.(*xast.TableHeader)
		cellType := "tableCell"
		if isHeader {
			cellType = "tableHeader"
		}
		var cells []interface{}
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			cells = append(cells, map[string]interface{}{
				"type":    cellType,
				"content": []interface{}{paragraph(r.renderInlineChildren(cell, nil))},
			})
		}
		rows = append(rows, map[string]interface{}{"type": "tableRow", "content": cells})
	}
	return map[string]interface{}{"type": "table", "content": rows}
}

// renderInlineChildren renders the inline children of a node, threading the
// active set of ADF marks (strong, em, code, link, strike) down the tree.
func (r *adfRenderer) renderInlineChildren(n ast.Node, marks []interface{}) []interface{} {
	var out []interface{}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		out = append(out, r.renderInline(c, marks)...)
	}
	return out
}

func (r *adfRenderer) renderInline(n ast.Node, marks []interface{}) []interface{} {
	switch v := n.(type) {
	case *ast.Text:
		var out []interface{}
		if s := string(v.Segment.Value(r.source)); s != "" {
			out = append(out, textNode(s, marks))
		}
		if v.HardLineBreak() {
			out = append(out, map[string]interface{}{"type": "hardBreak"})
		} else if v.SoftLineBreak() {
			out = append(out, textNode(" ", marks))
		}
		return out
	case *ast.String:
		if s := string(v.Value); s != "" {
			return []interface{}{textNode(s, marks)}
		}
		return nil
	case *ast.CodeSpan:
		return []interface{}{textNode(r.nodeText(v), addMark(marks, mark("code")))}
	case *ast.Emphasis:
		m := "em"
		if v.Level >= 2 {
			m = "strong"
		}
		return r.renderInlineChildren(v, addMark(marks, mark(m)))
	case *xast.Strikethrough:
		return r.renderInlineChildren(v, addMark(marks, mark("strike")))
	case *ast.Link:
		linkMark := map[string]interface{}{
			"type":  "link",
			"attrs": map[string]interface{}{"href": string(v.Destination)},
		}
		return r.renderInlineChildren(v, addMark(marks, linkMark))
	case *ast.AutoLink:
		url := string(v.URL(r.source))
		linkMark := map[string]interface{}{
			"type":  "link",
			"attrs": map[string]interface{}{"href": url},
		}
		return []interface{}{textNode(url, addMark(marks, linkMark))}
	case *ast.RawHTML:
		if s := r.rawHTMLText(v); s != "" {
			return []interface{}{textNode(s, marks)}
		}
		return nil
	case *xast.TaskCheckBox:
		// Rendered as taskItem state at the list level; emit nothing inline.
		return nil
	default:
		return r.renderInlineChildren(n, marks)
	}
}

// taskItemInlines returns the inline content of a task-list item, stripping the
// leading checkbox marker.
func (r *adfRenderer) taskItemInlines(li ast.Node) []interface{} {
	block := li.FirstChild()
	if block == nil {
		return nil
	}
	var out []interface{}
	for c := block.FirstChild(); c != nil; c = c.NextSibling() {
		if _, ok := c.(*xast.TaskCheckBox); ok {
			continue
		}
		out = append(out, r.renderInline(c, nil)...)
	}
	return out
}

func (r *adfRenderer) listHasTask(v *ast.List) bool {
	for li := v.FirstChild(); li != nil; li = li.NextSibling() {
		if _, found := taskItemChecked(li); found {
			return true
		}
	}
	return false
}

func taskItemChecked(li ast.Node) (checked bool, found bool) {
	block := li.FirstChild()
	if block == nil {
		return false, false
	}
	for c := block.FirstChild(); c != nil; c = c.NextSibling() {
		if cb, ok := c.(*xast.TaskCheckBox); ok {
			return cb.IsChecked, true
		}
	}
	return false, false
}

// collectLines concatenates the raw source lines of a code block.
func (r *adfRenderer) collectLines(n ast.Node) string {
	var b strings.Builder
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		b.Write(seg.Value(r.source))
	}
	return strings.TrimRight(b.String(), "\n")
}

// nodeText concatenates the text of a node's Text descendants (used for code
// spans, whose content should be emitted verbatim without nested marks).
func (r *adfRenderer) nodeText(n ast.Node) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*ast.Text); ok {
			b.Write(t.Segment.Value(r.source))
		} else {
			b.WriteString(r.nodeText(c))
		}
	}
	return b.String()
}

func (r *adfRenderer) rawHTMLText(v *ast.RawHTML) string {
	var b strings.Builder
	for i := 0; i < v.Segments.Len(); i++ {
		seg := v.Segments.At(i)
		b.Write(seg.Value(r.source))
	}
	return b.String()
}

// --- ADF node helpers ---

func paragraph(content []interface{}) map[string]interface{} {
	p := map[string]interface{}{"type": "paragraph"}
	if len(content) > 0 {
		p["content"] = content
	}
	return p
}

func codeBlock(language, code string) map[string]interface{} {
	node := map[string]interface{}{"type": "codeBlock"}
	if language != "" {
		node["attrs"] = map[string]interface{}{"language": language}
	}
	if code != "" {
		node["content"] = []interface{}{map[string]interface{}{"type": "text", "text": code}}
	}
	return node
}

func textNode(s string, marks []interface{}) map[string]interface{} {
	n := map[string]interface{}{"type": "text", "text": s}
	if len(marks) > 0 {
		n["marks"] = marks
	}
	return n
}

func mark(t string) map[string]interface{} {
	return map[string]interface{}{"type": t}
}

// addMark returns a new slice with m appended, without mutating the input (marks
// are shared across sibling inline nodes as the tree is walked).
func addMark(marks []interface{}, m interface{}) []interface{} {
	out := make([]interface{}, len(marks), len(marks)+1)
	copy(out, marks)
	return append(out, m)
}
