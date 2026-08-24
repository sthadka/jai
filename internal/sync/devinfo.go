package sync

import (
	"context"
	"fmt"
	"os"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/jira"
)

// SyncDevInfo fetches development information (PRs, branches, commits) for issues
// and updates the dev_links table. Non-fatal: errors are logged to stderr.
func (e *Engine) SyncDevInfo(ctx context.Context, src config.SyncSource) {
	// Get all issues for this sync source that have numeric IDs.
	query := `SELECT key, id FROM issues WHERE id != '' AND id IS NOT NULL`

	// Filter by project if this is a project-keyed source.
	if src.JQL == "" && len(src.Projects) > 0 {
		placeholders := ""
		for i := range src.Projects {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
		}
		query += ` AND project IN (` + placeholders + `)`
	}

	stmt, err := e.db.Prepare(query)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: dev info sync prepare failed: %v\n", err)
		return
	}
	defer stmt.Close()

	var args []interface{}
	if src.JQL == "" && len(src.Projects) > 0 {
		for _, p := range src.Projects {
			args = append(args, p)
		}
	}

	rows, err := stmt.Query(args...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: dev info sync query failed: %v\n", err)
		return
	}
	defer rows.Close()

	type issueInfo struct {
		key string
		id  string
	}
	var issues []issueInfo
	for rows.Next() {
		var info issueInfo
		if err := rows.Scan(&info.key, &info.id); err == nil {
			issues = append(issues, info)
		}
	}

	// Batch fetch dev info for issues.
	for _, issue := range issues {
		devInfo, err := e.client.GetDevInfo(ctx, issue.id)
		if err != nil {
			// Non-fatal: skip this issue and continue.
			continue
		}

		links := extractDevLinks(issue.key, devInfo)
		if len(links) > 0 {
			_ = e.db.UpsertDevLinks(issue.key, links)
		}
	}
}

// extractDevLinks converts dev info API response to db.DevLink structs.
func extractDevLinks(issueKey string, devInfo *jira.DevInfoResponse) []*db.DevLink {
	var links []*db.DevLink

	for _, detail := range devInfo.Detail {
		for _, repo := range detail.Repositories {
			// Extract pull requests.
			for _, pr := range repo.PullRequests {
				author := ""
				if pr.Author != nil {
					author = pr.Author.Name
				}
				links = append(links, &db.DevLink{
					IssueKey: issueKey,
					Type:     "pullrequest",
					URL:      pr.URL,
					Title:    pr.Title,
					Status:   pr.Status,
					Repo:     repo.Name,
					Author:   author,
					Created:  normalizeDate(pr.LastUpdate),
				})
			}

			// Extract branches.
			for _, branch := range repo.Branches {
				links = append(links, &db.DevLink{
					IssueKey: issueKey,
					Type:     "branch",
					URL:      branch.URL,
					Title:    branch.Name,
					Status:   "",
					Repo:     repo.Name,
					Author:   "",
					Created:  "",
				})
			}

			// Extract commits.
			for _, commit := range repo.Commits {
				author := ""
				if commit.Author != nil {
					author = commit.Author.Name
				}
				links = append(links, &db.DevLink{
					IssueKey: issueKey,
					Type:     "commit",
					URL:      commit.URL,
					Title:    commit.Message,
					Status:   "",
					Repo:     repo.Name,
					Author:   author,
					Created:  normalizeDate(commit.Timestamp),
				})
			}
		}
	}

	return links
}
