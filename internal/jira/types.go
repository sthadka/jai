package jira

import (
	"encoding/json"
	"strings"
)

// SearchResponse is the Jira /rest/api/3/search/jql response.
type SearchResponse struct {
	Issues        []*Issue `json:"issues"`
	NextPageToken string   `json:"nextPageToken"`
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
	Type    string             `json:"type"`
	Text    string             `json:"text"`
	Content []adfNode          `json:"content"`
	Attrs   map[string]json.RawMessage `json:"attrs"`
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
