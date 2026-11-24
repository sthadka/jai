package sync

import (
	"context"
	"fmt"

	"github.com/sthadka/jai/v7/pkg/jira"
	db "github.com/sthadka/jai/v7/pkg/store"
)

// DetectDeletions identifies issues that exist locally but not in Jira and
// removes them, returning the number deleted. It is a thin wrapper over
// DetectDeletedKeys for callers that only need the count. Runs during full sync.
func DetectDeletions(ctx context.Context, database db.Store, client jira.JiraClient, project string) (int, error) {
	keys, err := DetectDeletedKeys(ctx, database, client, project)
	return len(keys), err
}

// DetectDeletedKeys identifies issues that exist locally but not in Jira, removes
// them, and returns the deleted issue keys so the caller can purge their derived
// facts (metric rollups) within the active generation. Runs during full sync.
func DetectDeletedKeys(ctx context.Context, database db.Store, client jira.JiraClient, project string) ([]string, error) {
	// Fetch all issue IDs/keys from Jira using maxResults=5000 (1 HTTP call per 5k issues).
	jql := fmt.Sprintf(`project = "%s" ORDER BY key ASC`, project)
	remoteIDs := make(map[string]bool)
	remoteKeys := make(map[string]bool)

	for page, err := range client.SearchAllWithOptions(ctx, jql, []string{"key"}, jira.SearchOptions{MaxResults: jira.SearchNarrowMaxResults}) {
		if err != nil {
			return nil, fmt.Errorf("fetching remote keys: %w", err)
		}
		for _, issue := range page {
			if issue.ID != "" {
				remoteIDs[issue.ID] = true
			}
			if issue.Key != "" {
				remoteKeys[issue.Key] = true
			}
		}
	}

	// Fetch all local issues (stable numeric id + key) for this project.
	rows, err := database.ReadDB().Query(`SELECT COALESCE(id, ''), key FROM issues WHERE project = ?`, project)
	if err != nil {
		return nil, fmt.Errorf("fetching local keys: %w", err)
	}
	defer rows.Close()

	type localRow struct {
		id  string
		key string
	}
	var localRows []localRow
	for rows.Next() {
		var lr localRow
		if err := rows.Scan(&lr.id, &lr.key); err != nil {
			return nil, err
		}
		localRows = append(localRows, lr)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Diff by stable Jira numeric ID when available (so moved/renamed issues
	// survive without false deletion); fall back to key for id-less rows or
	// servers that omit id.
	hasRemoteIDs := len(remoteIDs) > 0
	var deleted []string
	for _, lr := range localRows {
		present := false
		if lr.id != "" && hasRemoteIDs {
			present = remoteIDs[lr.id]
		} else {
			present = remoteKeys[lr.key]
		}
		if !present {
			deleted = append(deleted, lr.key)
		}
	}

	if len(deleted) > 0 {
		if _, err := database.DeleteIssuesTx(deleted); err != nil {
			return nil, fmt.Errorf("deleting %d issues: %w", len(deleted), err)
		}
	}

	return deleted, nil
}
