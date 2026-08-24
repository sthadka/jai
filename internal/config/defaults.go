package config

// defaultTemplates provides built-in issue description templates.
var defaultTemplates = map[string]string{
	"bug": "## Steps to Reproduce\n1. \n\n## Expected Behavior\n\n\n## Actual Behavior\n\n\n## Environment\n",
	"story": "## User Story\nAs a [role], I want [feature] so that [benefit].\n\n## Acceptance Criteria\n- [ ] \n\n## Notes\n",
	"task": "## Description\n\n\n## Steps\n- [ ] \n\n## Done When\n",
	"spike": "## Question\n\n\n## Research Areas\n- \n\n## Time Box\n\n## Output\n",
	"epic": "## Goal\n\n\n## Success Metrics\n- \n\n## Scope\n\n### In Scope\n- \n\n### Out of Scope\n- \n",
}

// defaultSnippets provides built-in SQL query snippets.
var defaultSnippets = map[string]string{
	"stale_issues": `status NOT IN ('Done','Closed','Resolved')
AND updated < '{{month_ago}}'`,
	"assignment_balance": `SELECT assignee, COUNT(*) as count,
  SUM(COALESCE(story_points, 0)) as points
FROM issues
WHERE status NOT IN ('Done','Closed','Resolved')
AND assignee IS NOT NULL
GROUP BY assignee ORDER BY count DESC`,
	"recently_created": `SELECT issue_type, COUNT(*) as count
FROM issues
WHERE created >= '{{week_ago}}'
GROUP BY issue_type ORDER BY count DESC`,
	"cross_project_blockers": `SELECT l.issue_key as source, l.linked_key as target,
  l.linked_summary, l.linked_status, l.linked_project
FROM issue_links l
JOIN issues i ON l.issue_key = i.key
WHERE l.link_type LIKE '%lock%'
  AND l.linked_project != i.project
  AND l.linked_status NOT IN ('Done','Closed')
ORDER BY i.project, l.linked_project`,
	"cycle_time": `SELECT key, summary,
  CAST(julianday(
    (SELECT MIN(c.changed_at) FROM changelog c WHERE c.issue_key = issues.key AND c.field = 'status' AND c.to_string IN ('Done','Closed'))
  ) - julianday(
    (SELECT MIN(c.changed_at) FROM changelog c WHERE c.issue_key = issues.key AND c.field = 'status' AND c.to_string = 'In Progress')
  ) AS INTEGER) as cycle_days
FROM issues
WHERE status IN ('Done','Closed')`,
	"time_in_status": `SELECT c1.issue_key, c1.to_string as status,
  CAST(julianday(COALESCE(c2.changed_at, datetime('now'))) - julianday(c1.changed_at) AS REAL) as days
FROM changelog c1
LEFT JOIN changelog c2 ON c1.issue_key = c2.issue_key
  AND c2.field = 'status' AND c2.changed_at > c1.changed_at
  AND NOT EXISTS (
    SELECT 1 FROM changelog cx WHERE cx.issue_key = c1.issue_key
    AND cx.field = 'status' AND cx.changed_at > c1.changed_at AND cx.changed_at < c2.changed_at
  )
WHERE c1.field = 'status'`,
	"reassignment_count": `SELECT issue_key, COUNT(*) as reassignments
FROM changelog
WHERE field = 'assignee'
GROUP BY issue_key
HAVING reassignments > 1
ORDER BY reassignments DESC`,
	"reopened_issues": `SELECT issue_key, COUNT(*) as reopen_count
FROM changelog
WHERE field = 'status' AND from_string IN ('Done','Closed','Resolved')
  AND to_string NOT IN ('Done','Closed','Resolved')
GROUP BY issue_key
ORDER BY reopen_count DESC`,
}

// DefaultViews returns starter views generated for a project/user.
func DefaultViews(project, me, team string) []ViewConfig {
	return []ViewConfig{
		{
			Name:  "my-work",
			Title: "My Work",
			Query: `SELECT key, summary, status, priority, updated
FROM issues
WHERE assignee_email = '{{me}}'
AND status_category != 'Done'
ORDER BY priority DESC, updated DESC`,
			Columns:       []string{"key", "summary", "status", "priority"},
			StatusSummary: true,
			ColorRules: []ColorRule{
				{Field: "priority", Condition: "equals", Value: "Blocker", Color: "#dd4444"},
				{Field: "priority", Condition: "equals", Value: "Critical", Color: "#ff8800"},
			},
		},
		{
			Name:  "recent-updates",
			Title: "Recent Updates",
			Query: `SELECT key, summary, status, assignee, updated
FROM issues
ORDER BY updated DESC
LIMIT 100`,
			Columns: []string{"key", "summary", "status", "assignee", "updated"},
		},
		{
			Name:  "team-board",
			Title: "Team Board",
			Query: `SELECT key, summary, status, assignee, priority
FROM issues
WHERE status_category != 'Done'
ORDER BY status, priority DESC`,
			Columns:       []string{"key", "summary", "status", "assignee"},
			GroupBy:       "status",
			StatusSummary: true,
		},
		{
			Name:  "stale-issues",
			Title: "Stale Issues",
			Query: `SELECT key, summary, status, assignee, updated
FROM issues
WHERE status_category = 'In Progress'
AND updated < datetime('now', '-28 days')
ORDER BY updated ASC`,
			Columns: []string{"key", "summary", "status", "assignee", "updated"},
			ColorRules: []ColorRule{
				{Field: "updated", Condition: "older_than", Value: "28d", Color: "#dd4444"},
			},
		},
	}
}
