package sync

import (
	"context"
	"fmt"

	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/jira"
)

// DetectDeletions identifies issues that exist locally but not in Jira
// and marks them with is_deleted. Runs during full sync.
func DetectDeletions(ctx context.Context, database *db.DB, client *jira.Client, project string) (int, error) {
	// Fetch all issue keys from Jira.
	jql := fmt.Sprintf(`project = "%s" ORDER BY key ASC`, project)
	remoteKeys := make(map[string]bool)

	for page, err := range client.SearchAll(ctx, jql, []string{"key"}) {
		if err != nil {
			return 0, fmt.Errorf("fetching remote keys: %w", err)
		}
		for _, issue := range page {
			remoteKeys[issue.Key] = true
		}
	}

	// Fetch all local keys for this project.
	rows, err := database.Query(`SELECT key FROM issues WHERE project = ?`, project)
	if err != nil {
		return 0, fmt.Errorf("fetching local keys: %w", err)
	}
	defer rows.Close()

	var localKeys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return 0, err
		}
		localKeys = append(localKeys, key)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Find deleted keys (local but not remote).
	var deleted int
	for _, key := range localKeys {
		if !remoteKeys[key] {
			// Mark as deleted by removing from DB (or could add is_deleted column).
			if _, err := database.Exec(`DELETE FROM issues WHERE key = ?`, key); err != nil {
				return deleted, fmt.Errorf("deleting %s: %w", key, err)
			}
			deleted++
		}
	}

	return deleted, nil
}
