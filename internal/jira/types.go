package jira

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SearchResponse is the Jira /rest/api/3/search/jql response.
type SearchResponse struct {
	Issues        []*Issue `json:"issues"`
	NextPageToken string   `json:"nextPageToken"`
	ErrorMessages []string `json:"errorMessages"`
	WarningMessages []string `json:"warningMessages"`
}

// Issue is a Jira issue from the API.
type Issue struct {
	Key    string          `json:"key"`
	Fields json.RawMessage `json:"fields"`
}

// IssueFields contains the commonly-accessed fields from an issue.
type IssueFields struct {
	Summary     string          `json:"summary"`
	Description json.RawMessage `json:"description"` // ADF or string
	Status      *struct {
		Name           string `json:"name"`
		StatusCategory *struct {
			Name string `json:"name"`
		} `json:"statusCategory"`
	} `json:"status"`
	Priority *struct {
		Name string `json:"name"`
	} `json:"priority"`
	Assignee *struct {
		DisplayName  string `json:"displayName"`
		EmailAddress string `json:"emailAddress"`
	} `json:"assignee"`
	Reporter *struct {
		DisplayName  string `json:"displayName"`
		EmailAddress string `json:"emailAddress"`
	} `json:"reporter"`
	Created        string `json:"created"`
	Updated        string `json:"updated"`
	ResolutionDate string `json:"resolutiondate"`
	Labels         []string `json:"labels"`
	Components     []struct {
		Name string `json:"name"`
	} `json:"components"`
	FixVersions []struct {
		Name string `json:"name"`
	} `json:"fixVersions"`
	Parent *struct {
		Key string `json:"key"`
	} `json:"parent"`
	IssueType *struct {
		Name string `json:"name"`
	} `json:"issuetype"`
	Project *struct {
		Key string `json:"key"`
	} `json:"project"`
	Comment *struct {
		Comments []*Comment `json:"comments"`
	} `json:"comment"`
	Resolution *struct {
		Name string `json:"name"`
	} `json:"resolution"`
	DueDate              string `json:"duedate"`
	TimeOriginalEstimate *int64 `json:"timeoriginalestimate"`
	TimeSpent            *int64 `json:"timespent"`
	TimeEstimate         *int64 `json:"timeestimate"`
	Subtasks             []struct {
		Key string `json:"key"`
	} `json:"subtasks"`
	IssueLinks []struct {
		ID   string `json:"id"`
		Type struct {
			Name    string `json:"name"`
			Inward  string `json:"inward"`
			Outward string `json:"outward"`
		} `json:"type"`
		InwardIssue  *struct{ Key string `json:"key"` } `json:"inwardIssue"`
		OutwardIssue *struct{ Key string `json:"key"` } `json:"outwardIssue"`
	} `json:"issuelinks"`
}

// Comment is a Jira comment from the API.
type Comment struct {
	ID     string `json:"id"`
	Author *struct {
		DisplayName  string `json:"displayName"`
		EmailAddress string `json:"emailAddress"`
	} `json:"author"`
	Body    json.RawMessage `json:"body"` // ADF or string
	Created string          `json:"created"`
	Updated string          `json:"updated"`
}

// ChangelogResponse is the response from GET /rest/api/3/issue/{key}?expand=changelog.
type ChangelogResponse struct {
	Key       string     `json:"key"`
	Changelog *Changelog `json:"changelog"`
}

// Changelog contains the history of changes to an issue.
type Changelog struct {
	Histories []ChangelogHistory `json:"histories"`
}

// ChangelogHistory is a single changelog entry (one user action at one timestamp).
type ChangelogHistory struct {
	ID      string `json:"id"`
	Author  *struct {
		DisplayName string `json:"displayName"`
	} `json:"author"`
	Created string          `json:"created"`
	Items   []ChangelogItem `json:"items"`
}

// ChangelogItem is a single field change within a changelog history.
type ChangelogItem struct {
	Field      string `json:"field"`
	FieldType  string `json:"fieldtype"`
	From       string `json:"from"`
	FromString string `json:"fromString"`
	To         string `json:"to"`
	ToString   string `json:"toString"`
}

// Field is a Jira field from /rest/api/3/field.
type Field struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Custom bool         `json:"custom"`
	Schema *FieldSchema `json:"schema"`
}

// FieldSchema describes the type of a Jira field.
type FieldSchema struct {
	Type   string `json:"type"`
	Items  string `json:"items"` // for array types
	System string `json:"system"`
	Custom string `json:"custom"`
}

// ProjectInfo is a Jira project returned by /rest/api/3/project/{key}.
type ProjectInfo struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// MySelf is the /rest/api/3/myself response.
type MySelf struct {
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
}

// TransitionsResponse is the Jira transitions response.
type TransitionsResponse struct {
	Transitions []*Transition `json:"transitions"`
}

// Transition is a single workflow transition.
type Transition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ADFToPlaintext converts an Atlassian Document Format JSON blob to plain text.
func ADFToPlaintext(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}

	// Handle plain string (older Jira versions or simple fields).
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	var node adfNode
	if err := json.Unmarshal(raw, &node); err != nil {
		return ""
	}
	return strings.TrimSpace(node.plaintext())
}

type adfNode struct {
	Type    string                     `json:"type"`
	Text    string                     `json:"text"`
	Content []adfNode                  `json:"content"`
	Marks   []adfMark                  `json:"marks"`
	Attrs   map[string]json.RawMessage `json:"attrs"`
}

type adfMark struct {
	Type  string                     `json:"type"`
	Attrs map[string]json.RawMessage `json:"attrs"`
}

func (n *adfNode) plaintext() string {
	switch n.Type {
	case "text":
		return n.Text
	case "hardBreak", "rule":
		return "\n"
	}

	var sb strings.Builder
	for i, child := range n.Content {
		sb.WriteString(child.plaintext())
		if n.Type == "doc" || n.Type == "bulletList" || n.Type == "orderedList" {
			if i < len(n.Content)-1 {
				sb.WriteString("\n")
			}
		}
	}

	switch n.Type {
	case "paragraph", "heading", "listItem":
		return sb.String() + "\n"
	}
	return sb.String()
}

// ADFToMarkdown converts an Atlassian Document Format JSON blob to markdown text.
func ADFToMarkdown(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var node adfNode
	if err := json.Unmarshal(raw, &node); err != nil {
		return ""
	}
	return strings.TrimSpace(node.markdown(0))
}

func (n *adfNode) markdown(depth int) string {
	switch n.Type {
	case "text":
		return adfApplyMarks(n.Text, n.Marks)
	case "hardBreak":
		return "\n"
	case "rule":
		return "---\n\n"
	case "mention":
		return "@" + n.collectText()
	case "emoji":
		if raw, ok := n.Attrs["text"]; ok {
			var t string
			json.Unmarshal(raw, &t) //nolint
			return t
		}
		return ""
	case "inlineCard", "embedCard":
		if raw, ok := n.Attrs["url"]; ok {
			var u string
			json.Unmarshal(raw, &u) //nolint
			return u
		}
		return ""
	case "doc":
		var sb strings.Builder
		for i := range n.Content {
			sb.WriteString(n.Content[i].markdown(depth))
		}
		return sb.String()
	case "paragraph":
		var sb strings.Builder
		for i := range n.Content {
			sb.WriteString(n.Content[i].markdown(depth))
		}
		return strings.TrimRight(sb.String(), "\n") + "\n\n"
	case "heading":
		level := 1
		if raw, ok := n.Attrs["level"]; ok {
			var l int
			json.Unmarshal(raw, &l) //nolint
			level = l
		}
		return strings.Repeat("#", level) + " " + n.collectText() + "\n\n"
	case "bulletList":
		var sb strings.Builder
		for i := range n.Content {
			sb.WriteString(n.Content[i].markdownListItem(depth, "- "))
		}
		if depth == 0 {
			sb.WriteString("\n")
		}
		return sb.String()
	case "orderedList":
		var sb strings.Builder
		for i := range n.Content {
			sb.WriteString(n.Content[i].markdownListItem(depth, fmt.Sprintf("%d. ", i+1)))
		}
		if depth == 0 {
			sb.WriteString("\n")
		}
		return sb.String()
	case "codeBlock":
		lang := ""
		if raw, ok := n.Attrs["language"]; ok {
			json.Unmarshal(raw, &lang) //nolint
		}
		return "```" + lang + "\n" + n.collectText() + "\n```\n\n"
	case "blockquote":
		var sb strings.Builder
		for i := range n.Content {
			inner := n.Content[i].markdown(depth)
			for _, line := range strings.Split(strings.TrimRight(inner, "\n"), "\n") {
				sb.WriteString("> " + line + "\n")
			}
		}
		sb.WriteString("\n")
		return sb.String()
	case "table":
		return n.markdownTable() + "\n"
	case "mediaSingle", "media":
		return "[attachment]\n\n"
	}
	var sb strings.Builder
	for i := range n.Content {
		sb.WriteString(n.Content[i].markdown(depth))
	}
	return sb.String()
}

func (n *adfNode) markdownListItem(depth int, prefix string) string {
	indent := strings.Repeat("  ", depth)
	var sb strings.Builder
	first := true
	for i := range n.Content {
		child := &n.Content[i]
		if child.Type == "bulletList" || child.Type == "orderedList" {
			sb.WriteString(child.markdown(depth + 1))
			continue
		}
		var text string
		if child.Type == "paragraph" {
			var isb strings.Builder
			for j := range child.Content {
				isb.WriteString(child.Content[j].markdown(depth))
			}
			text = strings.TrimRight(isb.String(), "\n")
		} else {
			text = strings.TrimRight(child.markdown(depth), "\n")
		}
		if first {
			sb.WriteString(indent + prefix + text + "\n")
			first = false
		} else {
			sb.WriteString(indent + strings.Repeat(" ", len(prefix)) + text + "\n")
		}
	}
	return sb.String()
}

func (n *adfNode) collectText() string {
	if n.Type == "text" {
		return n.Text
	}
	var sb strings.Builder
	for i := range n.Content {
		sb.WriteString(n.Content[i].collectText())
	}
	return sb.String()
}

func (n *adfNode) markdownTable() string {
	type row struct {
		cells  []string
		header bool
	}
	var rows []row
	for i := range n.Content {
		rn := &n.Content[i]
		if rn.Type != "tableRow" {
			continue
		}
		var cells []string
		isHeader := false
		for j := range rn.Content {
			cell := &rn.Content[j]
			if cell.Type == "tableHeader" {
				isHeader = true
			}
			cells = append(cells, strings.TrimSpace(cell.collectText()))
		}
		rows = append(rows, row{cells: cells, header: isHeader})
	}
	if len(rows) == 0 {
		return ""
	}
	numCols := 0
	for _, r := range rows {
		if len(r.cells) > numCols {
			numCols = len(r.cells)
		}
	}
	var sb strings.Builder
	for i, r := range rows {
		sb.WriteString("|")
		for j := 0; j < numCols; j++ {
			cell := ""
			if j < len(r.cells) {
				cell = r.cells[j]
			}
			sb.WriteString(" " + cell + " |")
		}
		sb.WriteString("\n")
		if i == 0 {
			sb.WriteString("|")
			for j := 0; j < numCols; j++ {
				sb.WriteString(" --- |")
			}
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func adfApplyMarks(text string, marks []adfMark) string {
	bold, italic, code, strike := false, false, false, false
	linkURL := ""
	for _, m := range marks {
		switch m.Type {
		case "strong":
			bold = true
		case "em":
			italic = true
		case "code":
			code = true
		case "strike":
			strike = true
		case "link":
			if raw, ok := m.Attrs["href"]; ok {
				json.Unmarshal(raw, &linkURL) //nolint
			}
		}
	}
	s := text
	if code {
		return "`" + s + "`"
	}
	if bold {
		s = "**" + s + "**"
	}
	if italic {
		s = "*" + s + "*"
	}
	if strike {
		s = "~~" + s + "~~"
	}
	if linkURL != "" {
		s = "[" + s + "](" + linkURL + ")"
	}
	return s
}
