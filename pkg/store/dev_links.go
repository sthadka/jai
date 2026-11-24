package store

import (
	"database/sql"
	"time"
)

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

// UpsertDevLinks replaces all dev links for an issue and updates pr_count,
// has_open_pr, and branch_name atomically in a single transaction.
func (db *DB) UpsertDevLinks(issueKey string, links []*DevLink) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := UpsertDevLinksTx(tx, issueKey, links); err != nil {
		return err
	}
	return tx.Commit()
}

// UpsertDevLinksTx replaces all dev links for an issue within an existing transaction.
func UpsertDevLinksTx(tx *sql.Tx, issueKey string, links []*DevLink) error {
	if _, err := tx.Exec(`DELETE FROM dev_links WHERE issue_key = ?`, issueKey); err != nil {
		return err
	}

	if len(links) > 0 {
		stmt, err := tx.Prepare(
			`INSERT INTO dev_links (issue_key, type, url, title, status, repo, author, created, synced_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		)
		if err != nil {
			return err
		}
		defer stmt.Close()
		now := time.Now().UTC().Format(time.RFC3339)
		for _, link := range links {
			if _, err := stmt.Exec(link.IssueKey, link.Type, link.URL, link.Title, link.Status, link.Repo, link.Author, link.Created, now); err != nil {
				return err
			}
		}
	}

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

	_, err := tx.Exec(
		`UPDATE issues SET pr_count = ?, has_open_pr = ?, branch_name = ? WHERE key = ?`,
		prCount, hasOpenPR, branchName, issueKey,
	)
	return err
}
