package db

import "time"

// DevLink represents a row from the dev_links table.
type DevLink struct {
	IssueKey string
	Type     string // "pullrequest", "branch", "commit"
	URL      string
	Title    string
	Status   string
	Repo     string
	Author   string
	Created  string
}

// UpsertDevLinks replaces all dev links for an issue and updates pr_count, has_open_pr, branch_name.
func (db *DB) UpsertDevLinks(issueKey string, links []*DevLink) error {
	// Delete existing dev links for this issue.
	if _, err := db.Exec(`DELETE FROM dev_links WHERE issue_key = ?`, issueKey); err != nil {
		return err
	}

	// Insert new dev links.
	for _, link := range links {
		_, err := db.Exec(
			`INSERT INTO dev_links (issue_key, type, url, title, status, repo, author, created, synced_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			link.IssueKey, link.Type, link.URL, link.Title, link.Status, link.Repo, link.Author, link.Created,
			time.Now().UTC().Format(time.RFC3339),
		)
		if err != nil {
			return err
		}
	}

	// Update denormalized columns on issues.
	prCount := 0
	hasOpenPR := 0
	branchName := ""

	for _, link := range links {
		if link.Type == "pullrequest" {
			prCount++
			if link.Status == "OPEN" {
				hasOpenPR = 1
			}
		}
		if link.Type == "branch" && branchName == "" {
			branchName = link.Title // For branches, Title contains the branch name
		}
	}

	_, err := db.Exec(
		`UPDATE issues SET pr_count = ?, has_open_pr = ?, branch_name = ? WHERE key = ?`,
		prCount, hasOpenPR, branchName, issueKey,
	)
	return err
}
