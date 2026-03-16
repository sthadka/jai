package db

// UpsertProject stores a project's display name.
func (db *DB) UpsertProject(key, name string) error {
	_, err := db.Exec(
		`INSERT INTO projects (key, name) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET name = excluded.name`,
		key, name,
	)
	return err
}

// GetProjectName returns the display name for a project key, or the key itself if not found.
func (db *DB) GetProjectName(key string) string {
	var name string
	if err := db.QueryRow(`SELECT name FROM projects WHERE key = ?`, key).Scan(&name); err != nil || name == "" {
		return key
	}
	return name
}
