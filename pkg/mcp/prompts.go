package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerPrompts registers all MCP prompt templates with the server.
func (s *Server) registerPrompts(srv *server.MCPServer) {
	// standup-report prompt
	srv.AddPrompt(
		mcp.Prompt{
			Name:        "standup-report",
			Description: "Generate a standup report for the current user or team",
			Arguments: []mcp.PromptArgument{
				{
					Name:        "scope",
					Description: "Who to report on: 'me' or 'team'",
					Required:    false,
				},
			},
		},
		s.handleStandupReportPrompt,
	)

	// sprint-review prompt
	srv.AddPrompt(
		mcp.Prompt{
			Name:        "sprint-review",
			Description: "Analyze the current sprint's progress and risks",
			Arguments: []mcp.PromptArgument{
				{
					Name:        "sprint_name",
					Description: "Sprint name or 'current'",
					Required:    false,
				},
			},
		},
		s.handleSprintReviewPrompt,
	)

	// bug-triage prompt
	srv.AddPrompt(
		mcp.Prompt{
			Name:        "bug-triage",
			Description: "Triage a new bug: check for duplicates, assess severity, create or link",
			Arguments: []mcp.PromptArgument{
				{
					Name:        "summary",
					Description: "Bug summary to triage",
					Required:    true,
				},
				{
					Name:        "project",
					Description: "Project key",
					Required:    true,
				},
			},
		},
		s.handleBugTriagePrompt,
	)

	// release-check prompt
	srv.AddPrompt(
		mcp.Prompt{
			Name:        "release-check",
			Description: "Check release readiness: open issues, blockers, bug count, completion %",
			Arguments: []mcp.PromptArgument{
				{
					Name:        "version",
					Description: "Fix version name to check",
					Required:    true,
				},
			},
		},
		s.handleReleaseCheckPrompt,
	)

	// workload-balance prompt
	srv.AddPrompt(
		mcp.Prompt{
			Name:        "workload-balance",
			Description: "Analyze team workload distribution and identify imbalances",
			Arguments: []mcp.PromptArgument{
				{
					Name:        "project",
					Description: "Project key (optional, all projects if omitted)",
					Required:    false,
				},
			},
		},
		s.handleWorkloadBalancePrompt,
	)

	// spec-to-tickets prompt
	srv.AddPrompt(
		mcp.Prompt{
			Name:        "spec-to-tickets",
			Description: "Break down a specification into an epic with child stories/tasks",
			Arguments: []mcp.PromptArgument{
				{
					Name:        "project",
					Description: "Project key",
					Required:    true,
				},
				{
					Name:        "spec",
					Description: "The specification text to break down",
					Required:    true,
				},
			},
		},
		s.handleSpecToTicketsPrompt,
	)
}

// handleStandupReportPrompt returns the standup report workflow.
func (s *Server) handleStandupReportPrompt(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	scope := "me"
	if val, ok := req.Params.Arguments["scope"]; ok && val != "" {
		scope = val
	}

	workflow := `## Standup Report Workflow

Use the following jai tools to gather standup data:

### Step 1: Yesterday's completions
` + "```" + `
jai_query({
  "sql": "SELECT key, summary, assignee, status FROM issues WHERE status IN ('Done', 'Closed') AND updated >= datetime('now', '-1 day') ORDER BY assignee"
})
` + "```" + `

### Step 2: In-progress work
` + "```" + `
jai_query({
  "sql": "SELECT key, summary, assignee, status, updated FROM issues WHERE status IN ('In Progress', 'In Review', 'In QA') ORDER BY assignee, updated DESC"
})
` + "```" + `

### Step 3: Blockers (stale in-progress)
` + "```" + `
jai_query({
  "sql": "SELECT key, summary, assignee, status, updated, CAST(julianday('now') - julianday(updated) AS INTEGER) as days_stale FROM issues WHERE status = 'In Progress' AND updated < datetime('now', '-7 days') ORDER BY updated ASC"
})
` + "```" + `

### Step 4: Newly created (incoming work)
` + "```" + `
jai_query({
  "sql": "SELECT key, summary, priority, reporter, created FROM issues WHERE created >= datetime('now', '-1 day') ORDER BY priority, created DESC"
})
` + "```" + `

After gathering this data:
1. Format as a standup-style report
2. Highlight risks and stale items
3. Summarize key metrics (completions, WIP, blockers, new work)`

	if scope == "team" {
		workflow = strings.ReplaceAll(workflow, "ORDER BY assignee", "WHERE assignee IS NOT NULL ORDER BY assignee")
	}

	return &mcp.GetPromptResult{
		Description: fmt.Sprintf("Standup report for %s", scope),
		Messages: []mcp.PromptMessage{
			{
				Role: "user",
				Content: mcp.TextContent{
					Type: "text",
					Text: workflow,
				},
			},
		},
	}, nil
}

// handleSprintReviewPrompt returns the sprint review workflow.
func (s *Server) handleSprintReviewPrompt(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	sprintName := "current"
	if val, ok := req.Params.Arguments["sprint_name"]; ok && val != "" {
		sprintName = val
	}

	sprintFilter := "sprint IS NOT NULL"
	if sprintName != "current" {
		sprintFilter = fmt.Sprintf("sprint LIKE '%%%s%%'", sprintName)
	}

	workflow := fmt.Sprintf(`## Sprint Planning Workflow

Use the following jai tools to analyze sprint state:

### Step 1: Current sprint state
`+"```"+`
jai_query({
  "sql": "SELECT key, summary, status, priority, assignee, story_points FROM issues WHERE %s AND status NOT IN ('Done', 'Closed') ORDER BY priority, story_points DESC"
})
`+"```"+`

### Step 2: Capacity check
`+"```"+`
jai_query({
  "sql": "SELECT assignee, COUNT(*) as issues, SUM(story_points) as points FROM issues WHERE %s AND status NOT IN ('Done', 'Closed') GROUP BY assignee ORDER BY points DESC"
})
`+"```"+`

### Step 3: Unestimated items
`+"```"+`
jai_query({
  "sql": "SELECT key, summary, issue_type, priority FROM issues WHERE %s AND (story_points IS NULL OR story_points = 0) AND status NOT IN ('Done', 'Closed')"
})
`+"```"+`

### Step 4: Backlog candidates (ready, unassigned, prioritized)
`+"```"+`
jai_query({
  "sql": "SELECT key, summary, priority, story_points, issue_type FROM issues WHERE sprint IS NULL AND status IN ('To Do', 'Open', 'Backlog') AND priority IN ('Highest', 'High', 'Medium') ORDER BY CASE priority WHEN 'Highest' THEN 1 WHEN 'High' THEN 2 WHEN 'Medium' THEN 3 ELSE 4 END, created ASC LIMIT 20"
})
`+"```"+`

After gathering this data:
1. Analyze sprint completion progress
2. Identify capacity issues
3. Flag unestimated work
4. Suggest backlog items to pull in`, sprintFilter, sprintFilter, sprintFilter)

	return &mcp.GetPromptResult{
		Description: fmt.Sprintf("Sprint review for %s", sprintName),
		Messages: []mcp.PromptMessage{
			{
				Role: "user",
				Content: mcp.TextContent{
					Type: "text",
					Text: workflow,
				},
			},
		},
	}, nil
}

// handleBugTriagePrompt returns the bug triage workflow.
func (s *Server) handleBugTriagePrompt(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	summary := req.Params.Arguments["summary"]
	project := req.Params.Arguments["project"]

	if summary == "" || project == "" {
		return nil, fmt.Errorf("summary and project are required")
	}

	// Extract keywords from summary for search
	keywords := extractKeywords(summary)

	workflow := fmt.Sprintf(`## Bug Triage Workflow

Bug to triage: "%s"
Project: %s

### Step 1: Search for potential duplicates
`+"```"+`
jai_search({
  "text": "%s",
  "limit": 10
})
`+"```"+`

### Step 2: If FTS finds candidates, check details
`+"```"+`
jai_query({
  "sql": "SELECT key, summary, status, resolution, description FROM issues WHERE project = '%s' AND issue_type = 'Bug' ORDER BY created DESC LIMIT 20"
})
`+"```"+`

### Step 3: Get open bugs by component/area
`+"```"+`
jai_query({
  "sql": "SELECT key, summary, status, priority, components, labels FROM issues WHERE project = '%s' AND issue_type = 'Bug' AND status NOT IN ('Done', 'Closed', 'Resolved') ORDER BY priority, created DESC"
})
`+"```"+`

### Step 4: If not a duplicate, create the bug
`+"```"+`
jai_create({
  "project": "%s",
  "type": "Bug",
  "summary": "%s",
  "description": "<provide detailed description>",
  "priority": "High"
})
`+"```"+`

### Step 5: If duplicate found, link and comment
`+"```"+`
jai_link({
  "issue": "<new-key>",
  "linked_issue": "<existing-key>",
  "link_type": "Duplicate"
})
jai_comment({
  "issue": "<existing-key>",
  "comment": "Duplicate report received. Additional context: ..."
})
`+"```"+`

After gathering this data:
1. Determine if bug is a duplicate
2. Assess severity and priority
3. Either create new bug or link to existing
4. Add appropriate labels and components`, summary, project, keywords, project, project, project, summary)

	return &mcp.GetPromptResult{
		Description: fmt.Sprintf("Bug triage for: %s", summary),
		Messages: []mcp.PromptMessage{
			{
				Role: "user",
				Content: mcp.TextContent{
					Type: "text",
					Text: workflow,
				},
			},
		},
	}, nil
}

// handleReleaseCheckPrompt returns the release readiness workflow.
func (s *Server) handleReleaseCheckPrompt(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	version := req.Params.Arguments["version"]

	if version == "" {
		return nil, fmt.Errorf("version is required")
	}

	workflow := fmt.Sprintf(`## Release Readiness Check

Release version: %s

### Step 1: Issues targeted for release version
`+"```"+`
jai_query({
  "sql": "SELECT key, summary, status, priority, assignee, issue_type FROM issues WHERE fix_versions LIKE '%%%s%%' ORDER BY CASE status WHEN 'Done' THEN 3 WHEN 'In Progress' THEN 2 ELSE 1 END, priority"
})
`+"```"+`

### Step 2: Completion summary
`+"```"+`
jai_query({
  "sql": "SELECT status, COUNT(*) as count FROM issues WHERE fix_versions LIKE '%%%s%%' GROUP BY status ORDER BY count DESC"
})
`+"```"+`

### Step 3: Open blockers for the release
`+"```"+`
jai_query({
  "sql": "SELECT key, summary, priority, assignee, status FROM issues WHERE fix_versions LIKE '%%%s%%' AND status NOT IN ('Done', 'Closed') AND priority IN ('Highest', 'High', 'Blocker') ORDER BY priority"
})
`+"```"+`

### Step 4: Unresolved bugs in release scope
`+"```"+`
jai_query({
  "sql": "SELECT key, summary, priority, assignee, status FROM issues WHERE fix_versions LIKE '%%%s%%' AND issue_type = 'Bug' AND status NOT IN ('Done', 'Closed', 'Resolved') ORDER BY priority"
})
`+"```"+`

After gathering this data:
1. Calculate completion percentage
2. Identify critical blockers
3. Assess bug count and severity
4. Provide go/no-go recommendation`, version, version, version, version, version)

	return &mcp.GetPromptResult{
		Description: fmt.Sprintf("Release readiness check for %s", version),
		Messages: []mcp.PromptMessage{
			{
				Role: "user",
				Content: mcp.TextContent{
					Type: "text",
					Text: workflow,
				},
			},
		},
	}, nil
}

// handleWorkloadBalancePrompt returns the workload analysis workflow.
func (s *Server) handleWorkloadBalancePrompt(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	project := ""
	if val, ok := req.Params.Arguments["project"]; ok && val != "" {
		project = val
	}

	projectFilter := ""
	if project != "" {
		projectFilter = fmt.Sprintf("AND project = '%s'", project)
	}

	workflow := fmt.Sprintf(`## Workload Analysis

Project: %s

### Step 1: Current load by person
`+"```"+`
jai_query({
  "sql": "SELECT assignee, COUNT(*) as total, SUM(CASE WHEN status = 'In Progress' THEN 1 ELSE 0 END) as in_progress, SUM(CASE WHEN priority IN ('Highest', 'High') THEN 1 ELSE 0 END) as high_pri, SUM(COALESCE(story_points, 0)) as total_points FROM issues WHERE status NOT IN ('Done', 'Closed', 'Resolved') AND assignee IS NOT NULL %s GROUP BY assignee ORDER BY total DESC"
})
`+"```"+`

### Step 2: Overloaded (more than 5 in-progress items)
`+"```"+`
jai_query({
  "sql": "SELECT assignee, COUNT(*) as wip FROM issues WHERE status = 'In Progress' AND assignee IS NOT NULL %s GROUP BY assignee HAVING wip > 5 ORDER BY wip DESC"
})
`+"```"+`

### Step 3: Completion velocity (last 2 weeks)
`+"```"+`
jai_query({
  "sql": "SELECT assignee, COUNT(*) as completed, SUM(COALESCE(story_points, 0)) as points_completed FROM issues WHERE status IN ('Done', 'Closed') AND updated >= datetime('now', '-14 days') AND assignee IS NOT NULL %s GROUP BY assignee ORDER BY completed DESC"
})
`+"```"+`

After gathering this data:
1. Identify overloaded team members
2. Find underutilized capacity
3. Compare velocity across team
4. Suggest rebalancing actions`, project, projectFilter, projectFilter, projectFilter)

	if project == "" {
		workflow = strings.ReplaceAll(workflow, "Project: \n", "Project: All\n")
	}

	return &mcp.GetPromptResult{
		Description: "Team workload balance analysis",
		Messages: []mcp.PromptMessage{
			{
				Role: "user",
				Content: mcp.TextContent{
					Type: "text",
					Text: workflow,
				},
			},
		},
	}, nil
}

// handleSpecToTicketsPrompt returns the spec-to-tickets workflow.
func (s *Server) handleSpecToTicketsPrompt(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	project := req.Params.Arguments["project"]
	spec := req.Params.Arguments["spec"]

	if project == "" || spec == "" {
		return nil, fmt.Errorf("project and spec are required")
	}

	workflow := fmt.Sprintf(`## Spec-to-Tickets Workflow

Project: %s
Spec length: %d characters

### Step 1: Discover project conventions
`+"```"+`
jai_schema_values({
  "column": "issue_type"
})
jai_schema_values({
  "column": "priority"
})
jai_fields({})
`+"```"+`

### Step 2: Check for existing epic/feature
`+"```"+`
jai_search({
  "text": "<extract feature name from spec>",
  "limit": 10
})
jai_query({
  "sql": "SELECT key, summary, status FROM issues WHERE project = '%s' AND issue_type = 'Epic' ORDER BY created DESC LIMIT 10"
})
`+"```"+`

### Step 3: Create the epic (if not exists)
`+"```"+`
jai_create({
  "project": "%s",
  "type": "Epic",
  "summary": "<epic title from spec>",
  "description": "<spec summary>"
})
`+"```"+`

### Step 4: Create child tickets (one per spec section/requirement)
`+"```"+`
jai_create({
  "project": "%s",
  "type": "Story",
  "summary": "<story 1 title>",
  "description": "<story 1 details>",
  "parent": "<EPIC-KEY>"
})
# Repeat for each story
`+"```"+`

### Step 5: Add dependencies between tickets
`+"```"+`
jai_link({
  "issue": "<STORY-2>",
  "linked_issue": "<STORY-1>",
  "link_type": "Blocks"
})
`+"```"+`

### Step 6: Verify the structure
`+"```"+`
jai_query({
  "sql": "SELECT key, summary, issue_type, status, parent_key FROM issues WHERE parent_key = '<EPIC-KEY>' ORDER BY key"
})
`+"```"+`

Specification to break down:
%s

After completing the workflow:
1. Break spec into logical epic + stories
2. Create hierarchical ticket structure
3. Add dependencies where needed
4. Return created ticket keys`, project, len(spec), project, project, project, spec)

	return &mcp.GetPromptResult{
		Description: "Break down specification into tickets",
		Messages: []mcp.PromptMessage{
			{
				Role: "user",
				Content: mcp.TextContent{
					Type: "text",
					Text: workflow,
				},
			},
		},
	}, nil
}

// extractKeywords extracts simple keywords from a summary for search.
func extractKeywords(summary string) string {
	// Simple keyword extraction: remove common words and take first few meaningful terms
	words := strings.Fields(summary)
	var keywords []string
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "and": true, "or": true,
		"but": true, "in": true, "on": true, "at": true, "to": true,
		"for": true, "of": true, "with": true, "is": true, "are": true,
	}

	for _, word := range words {
		lower := strings.ToLower(strings.Trim(word, ".,!?;:"))
		if len(lower) > 2 && !stopWords[lower] {
			keywords = append(keywords, lower)
			if len(keywords) >= 5 {
				break
			}
		}
	}

	return strings.Join(keywords, " ")
}
