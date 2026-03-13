package config

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
