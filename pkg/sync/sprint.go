package sync

import (
	"context"
	"fmt"
	"os"

	"github.com/sthadka/jai/internal/config"
)

// SyncSprints fetches boards and sprints from the Jira Agile API and stores them in the DB.
// For each project in the source, it:
// 1. Fetches all boards
// 2. Upserts boards into the boards table
// 3. For each board, fetches all sprints
// 4. Upserts sprints into the sprints table
// 5. For the active sprint, fetches issue membership
// 6. Updates sprint_id and sprint_name on matching issues
//
// Sprint sync is non-fatal — errors are logged to stderr and don't abort the sync.
func (e *Engine) SyncSprints(ctx context.Context, src config.SyncSource) {
	// Only sync sprints for project-based sources, not arbitrary JQL queries.
	if src.JQL != "" || len(src.Projects) == 0 {
		return
	}

	for _, projectKey := range src.Projects {
		if err := e.syncProjectSprints(ctx, projectKey); err != nil {
			fmt.Fprintf(os.Stderr, "warning: syncing sprints for %s: %v\n", projectKey, err)
		}
	}
}

func (e *Engine) syncProjectSprints(ctx context.Context, projectKey string) error {
	// Fetch boards for this project.
	boards, err := e.client.GetBoards(ctx, projectKey)
	if err != nil {
		return fmt.Errorf("fetching boards: %w", err)
	}

	for _, board := range boards {
		// Upsert board metadata.
		boardProjectKey := projectKey
		if board.Location != nil && board.Location.ProjectKey != "" {
			boardProjectKey = board.Location.ProjectKey
		}
		if err := e.db.UpsertBoard(board.ID, board.Name, board.Type, boardProjectKey); err != nil {
			fmt.Fprintf(os.Stderr, "warning: storing board %d: %v\n", board.ID, err)
			continue
		}

		// Fetch sprints for this board.
		sprints, err := e.client.GetSprints(ctx, board.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: fetching sprints for board %d: %v\n", board.ID, err)
			continue
		}

		for _, sprint := range sprints {
			// Upsert sprint metadata.
			if err := e.db.UpsertSprint(sprint.ID, board.ID, sprint.Name, sprint.State, sprint.StartDate, sprint.EndDate, sprint.CompleteDate, sprint.Goal); err != nil {
				fmt.Fprintf(os.Stderr, "warning: storing sprint %d: %v\n", sprint.ID, err)
				continue
			}

			// For active sprints, fetch issue membership and update issues table.
			if sprint.State == "active" {
				if err := e.syncSprintIssues(ctx, sprint.ID, sprint.Name); err != nil {
					fmt.Fprintf(os.Stderr, "warning: syncing issues for sprint %d: %v\n", sprint.ID, err)
				}
			}
		}
	}

	return nil
}

func (e *Engine) syncSprintIssues(ctx context.Context, sprintID int, sprintName string) error {
	keys, err := e.client.GetSprintIssues(ctx, sprintID)
	if err != nil {
		return fmt.Errorf("fetching sprint issues: %w", err)
	}

	for _, key := range keys {
		if err := e.db.UpdateIssueSprintInfo(key, sprintID, sprintName); err != nil {
			// Non-fatal: the issue might not exist locally yet.
			continue
		}
	}

	return nil
}
