package sync

import (
	"context"
	"fmt"

	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/jira"
)

// DetectDeletions identifies issues that exist locally but not in Jira and
// removes them, returning the number deleted. It is a thin wrapper over
// DetectDeletedKeys for callers that only need the count. Runs during full sync.
func DetectDeletions(ctx context.Context, database *db.DB, client *jira.Client, project string) (int, error) {
	keys, err := DetectDeletedKeys(ctx, database, client, project)
	return len(keys), err
}

// DetectDeletedKeys identifies issues that exist locally but not in Jira, removes
// them, and returns the deleted issue keys so the caller can purge their derived
// facts (metric rollups) within the active generation. Runs during full sync.
func DetectDeletedKeys(ctx context.Context, database *db.DB, client *jira.Client, project string) ([]string, error) {
	// Fetch all issue keys from Jira.
	jql := fmt.Sprintf(`project = "%s" ORDER BY key ASC`, project)
	remoteKeys := make(map[string]bool)

	for page, err := range client.SearchAll(ctx, jql, []string{"key"}) {
		if err != nil {
			return nil, fmt.Errorf("fetching remote keys: %w", err)
		}
		for _, issue := range page {
			remoteKeys[issue.Key] = true
		}
	}

	// Fetch all local keys for this project.
	rows, err := database.Query(`SELECT key FROM issues WHERE project = ?`, project)
	if err != nil {
		return nil, fmt.Errorf("fetching local keys: %w", err)
	}
	defer rows.Close()

	var localKeys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		localKeys = append(localKeys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Find deleted keys (local but not remote) and remove them, returning the keys
	// so the caller can purge their derived facts.
	var deleted []string
	for _, key := range localKeys {
		if !remoteKeys[key] {
			if _, err := database.Exec(`DELETE FROM issues WHERE key = ?`, key); err != nil {
				return deleted, fmt.Errorf("deleting %s: %w", key, err)
			}
			deleted = append(deleted, key)
		}
	}

	return deleted, nil
}
