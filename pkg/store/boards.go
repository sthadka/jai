package db

import "time"

// UpsertBoard stores a board's metadata.
func (db *DB) UpsertBoard(id int, name, boardType, projectKey string) error {
	_, err := db.Exec(
		`INSERT INTO boards (id, name, type, project_key, synced_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET name = excluded.name, type = excluded.type, project_key = excluded.project_key, synced_at = excluded.synced_at`,
		id, name, boardType, projectKey, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

// UpsertSprint stores a sprint's metadata.
func (db *DB) UpsertSprint(id, boardID int, name, state, startDate, endDate, completeDate, goal string) error {
	_, err := db.Exec(
		`INSERT INTO sprints (id, board_id, name, state, start_date, end_date, complete_date, goal, synced_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET board_id = excluded.board_id, name = excluded.name, state = excluded.state,
		   start_date = excluded.start_date, end_date = excluded.end_date, complete_date = excluded.complete_date, goal = excluded.goal, synced_at = excluded.synced_at`,
		id, boardID, name, state, startDate, endDate, completeDate, goal, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

// UpdateIssueSprintInfo updates the sprint_id and sprint_name columns for an issue.
func (db *DB) UpdateIssueSprintInfo(issueKey string, sprintID int, sprintName string) error {
	_, err := db.Exec(
		`UPDATE issues SET sprint_id = ?, sprint_name = ? WHERE key = ?`,
		sprintID, sprintName, issueKey,
	)
	return err
}
