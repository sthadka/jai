package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type migration struct {
	version     int
	description string
	up          func(*sql.Tx) error
}

var migrations = []migration{
	{
		version:     1,
		description: "initial schema",
		up:          createSchema,
	},
	{
		version:     2,
		description: "add projects table",
		up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS projects (
				key        TEXT PRIMARY KEY,
				name       TEXT NOT NULL,
				synced_at  DATETIME NOT NULL DEFAULT (datetime('now'))
			)`)
			return err
		},
	},
	{
		version:     3,
		description: "add resume_cursor to sync_metadata",
		up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`ALTER TABLE sync_metadata ADD COLUMN resume_cursor TEXT`)
			return err
		},
	},
	{
		version:     4,
		description: "add issue_links table and new issue columns (resolution, due_date, time tracking, subtask_keys)",
		up: func(tx *sql.Tx) error {
			stmts := []string{
				`CREATE TABLE IF NOT EXISTS issue_links (
					id          TEXT PRIMARY KEY,
					issue_key   TEXT NOT NULL,
					link_type   TEXT NOT NULL,
					direction   TEXT NOT NULL,
					linked_key  TEXT NOT NULL
				)`,
				`CREATE INDEX IF NOT EXISTS idx_issue_links_issue  ON issue_links(issue_key)`,
				`CREATE INDEX IF NOT EXISTS idx_issue_links_linked ON issue_links(linked_key)`,
				`ALTER TABLE issues ADD COLUMN resolution         TEXT`,
				`ALTER TABLE issues ADD COLUMN due_date           DATETIME`,
				`ALTER TABLE issues ADD COLUMN original_estimate  INTEGER`,
				`ALTER TABLE issues ADD COLUMN time_spent         INTEGER`,
				`ALTER TABLE issues ADD COLUMN remaining_estimate INTEGER`,
				`ALTER TABLE issues ADD COLUMN subtask_keys       TEXT`,
			}
			for _, s := range stmts {
				if _, err := tx.Exec(s); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version:     5,
		description: "add last_issue_updated high-water mark to sync_metadata",
		up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`ALTER TABLE sync_metadata ADD COLUMN last_issue_updated DATETIME`)
			return err
		},
	},
	{
		version:     6,
		description: "convert comma-separated array columns to JSON arrays",
		up: func(tx *sql.Tx) error {
			// Drop FTS triggers to avoid O(rows×columns) FTS rebuilds during bulk update.
			for _, name := range []string{"issues_fts_insert", "issues_fts_update", "issues_fts_delete"} {
				if _, err := tx.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s", name)); err != nil {
					return fmt.Errorf("dropping trigger %s: %w", name, err)
				}
			}

			builtinArrayCols := []string{"labels", "components", "fix_version", "subtask_keys"}
			for _, col := range builtinArrayCols {
				if err := convertCSVToJSON(tx, col); err != nil {
					return fmt.Errorf("converting %s: %w", col, err)
				}
			}

			rows, err := tx.Query(`SELECT name FROM field_map WHERE type = 'array' AND is_column = 1`)
			if err != nil {
				return err
			}
			var customCols []string
			for rows.Next() {
				var n string
				if rows.Scan(&n) == nil {
					customCols = append(customCols, n)
				}
			}
			rows.Close()

			for _, col := range customCols {
				if err := convertCSVToJSON(tx, col); err != nil {
					return fmt.Errorf("converting custom column %s: %w", col, err)
				}
			}

			// Recreate FTS triggers.
			triggers := []string{
				`CREATE TRIGGER IF NOT EXISTS issues_fts_insert AFTER INSERT ON issues BEGIN
					INSERT INTO issues_fts(rowid, key, summary, description, comments_text, labels)
					VALUES (new.rowid, new.key, new.summary, new.description, new.comments_text, new.labels);
				END`,
				`CREATE TRIGGER IF NOT EXISTS issues_fts_update
				AFTER UPDATE OF key, summary, description, comments_text, labels ON issues
				WHEN old.key           IS NOT new.key
				  OR old.summary       IS NOT new.summary
				  OR old.description   IS NOT new.description
				  OR old.comments_text IS NOT new.comments_text
				  OR old.labels        IS NOT new.labels
				BEGIN
					INSERT INTO issues_fts(issues_fts, rowid, key, summary, description, comments_text, labels)
					VALUES ('delete', old.rowid, old.key, old.summary, old.description, old.comments_text, old.labels);
					INSERT INTO issues_fts(rowid, key, summary, description, comments_text, labels)
					VALUES (new.rowid, new.key, new.summary, new.description, new.comments_text, new.labels);
				END`,
				`CREATE TRIGGER IF NOT EXISTS issues_fts_delete AFTER DELETE ON issues BEGIN
					INSERT INTO issues_fts(issues_fts, rowid, key, summary, description, comments_text, labels)
					VALUES ('delete', old.rowid, old.key, old.summary, old.description, old.comments_text, old.labels);
				END`,
			}
			for _, t := range triggers {
				if _, err := tx.Exec(t); err != nil {
					return fmt.Errorf("recreating FTS trigger: %w", err)
				}
			}

			// Rebuild FTS index once to pick up the new labels format.
			if _, err := tx.Exec(`INSERT INTO issues_fts(issues_fts) VALUES('rebuild')`); err != nil {
				return fmt.Errorf("rebuilding FTS index: %w", err)
			}

			return nil
		},
	},
	{
		version:     7,
		description: "add changelog table for status transition history",
		up: func(tx *sql.Tx) error {
			stmts := []string{
				`CREATE TABLE IF NOT EXISTS changelog (
					id          TEXT PRIMARY KEY,
					issue_key   TEXT NOT NULL,
					author      TEXT,
					field       TEXT,
					field_type  TEXT,
					from_value  TEXT,
					from_string TEXT,
					to_value    TEXT,
					to_string   TEXT,
					changed_at  DATETIME
				)`,
				`CREATE INDEX IF NOT EXISTS idx_changelog_issue ON changelog(issue_key)`,
				`CREATE INDEX IF NOT EXISTS idx_changelog_time ON changelog(changed_at)`,
				`CREATE INDEX IF NOT EXISTS idx_changelog_field_to ON changelog(field, to_string)`,
			}
			for _, s := range stmts {
				if _, err := tx.Exec(s); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version:     8,
		description: "add id column to issues for Jira numeric issue ID",
		up: func(tx *sql.Tx) error {
			// Column may already exist as a dynamic custom-field column.
			var exists bool
			row := tx.QueryRow(`SELECT count(*) > 0 FROM pragma_table_info('issues') WHERE name = 'id'`)
			if err := row.Scan(&exists); err != nil {
				return err
			}
			if !exists {
				if _, err := tx.Exec(`ALTER TABLE issues ADD COLUMN id TEXT`); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version:     9,
		description: "add changelog_synced_at to issues for incremental changelog sync",
		up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`ALTER TABLE issues ADD COLUMN changelog_synced_at DATETIME`)
			return err
		},
	},
	{
		version:     10,
		description: "add sprint, attachment, dev_links tables and issue columns for agent integration",
		up: func(tx *sql.Tx) error {
			stmts := []string{
				// boards table
				`CREATE TABLE IF NOT EXISTS boards (
					id          INTEGER PRIMARY KEY,
					name        TEXT NOT NULL,
					type        TEXT,
					project_key TEXT,
					synced_at   DATETIME NOT NULL DEFAULT (datetime('now'))
				)`,
				// sprints table
				`CREATE TABLE IF NOT EXISTS sprints (
					id          INTEGER PRIMARY KEY,
					board_id    INTEGER NOT NULL REFERENCES boards(id),
					name        TEXT NOT NULL,
					state       TEXT,
					start_date  DATETIME,
					end_date    DATETIME,
					complete_date DATETIME,
					goal        TEXT,
					synced_at   DATETIME NOT NULL DEFAULT (datetime('now'))
				)`,
				// dev_links table
				`CREATE TABLE IF NOT EXISTS dev_links (
					id          INTEGER PRIMARY KEY AUTOINCREMENT,
					issue_key   TEXT NOT NULL,
					type        TEXT NOT NULL,
					url         TEXT,
					title       TEXT,
					status      TEXT,
					repo        TEXT,
					author      TEXT,
					created     DATETIME,
					synced_at   DATETIME NOT NULL DEFAULT (datetime('now'))
				)`,
				// attachments table
				`CREATE TABLE IF NOT EXISTS attachments (
					id          INTEGER PRIMARY KEY,
					issue_key   TEXT NOT NULL,
					filename    TEXT NOT NULL,
					size        INTEGER,
					mime_type   TEXT,
					author      TEXT,
					created     DATETIME,
					url         TEXT,
					synced_at   DATETIME NOT NULL DEFAULT (datetime('now'))
				)`,
			}
			for _, s := range stmts {
				if _, err := tx.Exec(s); err != nil {
					return err
				}
			}

			// Add columns to issue_links table if they don't exist
			issueLinksCols := map[string]string{
				"linked_summary": "TEXT",
				"linked_status":  "TEXT",
				"linked_project": "TEXT",
			}
			for col, typ := range issueLinksCols {
				if err := addColumnIfNotExists(tx, "issue_links", col, typ); err != nil {
					return err
				}
			}

			// Add columns to issues table if they don't exist
			issuesCols := map[string]string{
				"sprint_id":        "INTEGER",
				"sprint_name":      "TEXT",
				"attachment_count": "INTEGER DEFAULT 0",
				"pr_count":         "INTEGER DEFAULT 0",
				"has_open_pr":      "INTEGER DEFAULT 0",
				"branch_name":      "TEXT",
			}
			for col, typ := range issuesCols {
				if err := addColumnIfNotExists(tx, "issues", col, typ); err != nil {
					return err
				}
			}

			// Create indexes
			indexes := []string{
				`CREATE INDEX IF NOT EXISTS idx_issues_sprint ON issues(sprint_id)`,
				`CREATE INDEX IF NOT EXISTS idx_boards_project ON boards(project_key)`,
				`CREATE INDEX IF NOT EXISTS idx_sprints_board ON sprints(board_id)`,
				`CREATE INDEX IF NOT EXISTS idx_sprints_state ON sprints(state)`,
				`CREATE INDEX IF NOT EXISTS idx_dev_links_issue ON dev_links(issue_key)`,
				`CREATE INDEX IF NOT EXISTS idx_dev_links_type ON dev_links(type)`,
				`CREATE INDEX IF NOT EXISTS idx_attachments_issue ON attachments(issue_key)`,
			}
			for _, idx := range indexes {
				if _, err := tx.Exec(idx); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version:     11,
		description: "type description/environment fields as richtext for ADF writes",
		up: func(tx *sql.Tx) error {
			_, err := tx.Exec(
				`UPDATE field_map SET type = 'richtext'
				 WHERE jira_id IN ('description', 'environment') AND type = 'text'`,
			)
			return err
		},
	},
	{
		version:     12,
		description: "add item_type (array element sub-type) to field_map and backfill built-ins",
		up: func(tx *sql.Tx) error {
			if err := addColumnIfNotExists(tx, "field_map", "item_type", "TEXT"); err != nil {
				return err
			}
			// Backfill built-in array fields. Custom user arrays (e.g.
			// Contributors) are populated on the next schema sync, which reads
			// schema.items from the Jira field API.
			backfill := map[string]string{
				"components":  "component",
				"fixVersions": "version",
				"labels":      "string",
			}
			for jiraID, itemType := range backfill {
				if _, err := tx.Exec(
					`UPDATE field_map SET item_type = ? WHERE jira_id = ? AND (item_type IS NULL OR item_type = '')`,
					itemType, jiraID,
				); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version:     13,
		description: "add last_reconcile to sync_metadata",
		up: func(tx *sql.Tx) error {
			return addColumnIfNotExists(tx, "sync_metadata", "last_reconcile", "DATETIME")
		},
	},
	{
		version:     14,
		description: "add flow-metrics fact store (generations, intervals, flow rollup)",
		up:          createMetricsSchema,
	},
	{
		version:     15,
		description: "add partial changelog index for tracked-field count/MIN metrics (idx_cl_tracked)",
		up: func(tx *sql.Tx) error {
			// Count-based signals (priority changes, rank volatility, reassignment
			// counts, triage/first-response latency) are COUNT/MIN over the changelog
			// and are queried directly — no materialized fact_field_event copy. A
			// partial index over the tracked fields keeps those per-(field,issue)
			// scans index-only; the leading `field` column lets each metric seek its
			// field, then range-scan (issue_key, changed_at). The WHERE clause mirrors
			// the spec's tracked-field set so the index stays small.
			_, err := tx.Exec(
				`CREATE INDEX IF NOT EXISTS idx_cl_tracked
				   ON changelog(field, issue_key, changed_at)
				   WHERE field IN ('status','priority','assignee','Team','Rank')`)
			return err
		},
	},
	{
		version:     16,
		description: "add fact_issue_membership table (event-time set-valued dimension facts)",
		up:          createMembershipSchema,
	},
	{
		version:     17,
		description: "add flow_occupancy + estimate columns to fact_issue_flow (persisted occupancy proxy / Effort measure)",
		up: func(tx *sql.Tx) error {
			// flow_occupancy: precomputed active-status occupancy proxy (active ÷ lead,
			// bounded [0,1]) so BI/notebook users query it without a per-row windowed
			// active-seconds aggregation. NULL for issues the provider excludes
			// (non-delivery or undefined lead). estimate: the issue's story-point value
			// where present (NULL when absent), enabling the Effort (Σ estimate) measure.
			if err := addColumnIfNotExists(tx, "fact_issue_flow", "flow_occupancy", "REAL"); err != nil {
				return err
			}
			return addColumnIfNotExists(tx, "fact_issue_flow", "estimate", "REAL")
		},
	},
	{
		version:     18,
		description: "stable id identity: dedup duplicate ids, unique id index, key-rename cascade + alias table",
		up: func(tx *sql.Tx) error {
			// Historical-key alias: keys retired by a move or project rename
			// resolve to the stable numeric id so old keys and URLs still work.
			if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS issue_key_alias (
				old_key    TEXT PRIMARY KEY,
				id         TEXT NOT NULL,
				changed_at DATETIME NOT NULL
			)`); err != nil {
				return err
			}
			if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_issue_key_alias_id ON issue_key_alias(id)`); err != nil {
				return err
			}

			// Collapse pre-existing duplicate-id rows (cross-project moves the old
			// key-keyed upsert never detected) so id can carry a UNIQUE index.
			if err := dedupIssuesByID(tx); err != nil {
				return fmt.Errorf("deduplicating issues by id: %w", err)
			}

			// id becomes the upsert conflict target. Partial (id <> '') so id-less
			// rows stay distinct — an empty id is not a shared identity.
			if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_issues_id ON issues(id) WHERE id <> ''`); err != nil {
				return err
			}

			// Cascade a key change to every child relation and alias the old key.
			if _, err := tx.Exec(issuesKeyRenameTrigger); err != nil {
				return err
			}
			return nil
		},
	},
	{
		version:     19,
		description: "hybrid custom-field storage: add sparse custom_fields JSON column",
		up: func(tx *sql.Tx) error {
			// Non-promoted custom fields live here as a sparse JSON object keyed by
			// Jira field id (customfield_NNNNN). New custom fields require no ALTER
			// TABLE; a field is promoted to a typed column only on demand.
			return addColumnIfNotExists(tx, "issues", "custom_fields", "TEXT")
		},
	},
	{
		version:     20,
		description: "add typed columns for additional Jira system fields",
		up: func(tx *sql.Tx) error {
			cols := []struct{ name, typ string }{
				{"creator", "TEXT"},
				{"environment", "TEXT"},
				{"security_level", "TEXT"},
				{"votes", "INTEGER"},
				{"watchers", "INTEGER"},
				{"work_ratio", "REAL"},
				{"status_category_changed", "DATETIME"},
				{"affects_versions", "TEXT"},
			}
			for _, c := range cols {
				if err := addColumnIfNotExists(tx, "issues", c.name, c.typ); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version:     21,
		description: "add row-level issue detail and full changelog hydration markers",
		up: func(tx *sql.Tx) error {
			if err := addColumnIfNotExists(tx, "issues", "details_synced_at", "DATETIME"); err != nil {
				return err
			}
			return addColumnIfNotExists(tx, "issues", "changelog_full_synced_at", "DATETIME")
		},
	},
	{
		version:     22,
		description: "make idx_issues_id unconditional by storing absent ids as NULL",
		up: func(tx *sql.Tx) error {
			// Normalize legacy empty-string ids to NULL so SQLite allows multiple
			// id-less rows under an unconditional UNIQUE(id) index (NULLs are
			// distinct in UNIQUE indexes) while queries filtering/joining on
			// issues.id can use idx_issues_id without repeating `AND id <> ''`.
			if _, err := tx.Exec(`UPDATE issues SET id = NULL WHERE id = ''`); err != nil {
				return err
			}
			if _, err := tx.Exec(`DROP INDEX IF EXISTS idx_issues_id`); err != nil {
				return err
			}
			_, err := tx.Exec(`CREATE UNIQUE INDEX idx_issues_id ON issues(id)`)
			return err
		},
	},
	{
		version:     23,
		description: "add case-insensitive and composite indexes for JQL hot paths",
		up: func(tx *sql.Tx) error {
			// pkg/jql emits LOWER(col) equality/IN predicates and default
			// ORDER BY updated DESC. Expression + composite indexes keep these
			// hot paths off full table scans and temp B-tree sorts at scale.
			indexes := []string{
				`CREATE INDEX IF NOT EXISTS idx_issues_project_lower_updated ON issues(LOWER(project), updated DESC)`,
				`CREATE INDEX IF NOT EXISTS idx_issues_assignee_lower_updated ON issues(LOWER(assignee), updated DESC)`,
				`CREATE INDEX IF NOT EXISTS idx_issues_reporter_lower ON issues(LOWER(reporter))`,
				`CREATE INDEX IF NOT EXISTS idx_issues_status_lower ON issues(LOWER(status))`,
				`CREATE INDEX IF NOT EXISTS idx_issues_status_cat_lower ON issues(LOWER(status_category))`,
			}
			for _, idx := range indexes {
				if _, err := tx.Exec(idx); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version:     24,
		description: "scope issues_fts_update trigger and add composite, covering, and expression indexes",
		up: func(tx *sql.Tx) error {
			stmts := []string{
				`DROP TRIGGER IF EXISTS issues_fts_update`,
				`CREATE TRIGGER issues_fts_update
				AFTER UPDATE OF key, summary, description, comments_text, labels ON issues
				WHEN old.key           IS NOT new.key
				  OR old.summary       IS NOT new.summary
				  OR old.description   IS NOT new.description
				  OR old.comments_text IS NOT new.comments_text
				  OR old.labels        IS NOT new.labels
				BEGIN
					INSERT INTO issues_fts(issues_fts, rowid, key, summary, description, comments_text, labels)
					VALUES ('delete', old.rowid, old.key, old.summary, old.description, old.comments_text, old.labels);
					INSERT INTO issues_fts(rowid, key, summary, description, comments_text, labels)
					VALUES (new.rowid, new.key, new.summary, new.description, new.comments_text, new.labels);
				END`,

				// JQL case-insensitive equality on type & priority
				`CREATE INDEX IF NOT EXISTS idx_issues_type_lower_prio ON issues(LOWER(type), LOWER(priority), updated DESC)`,
				// Project-scoped grouped/sorted lists
				`CREATE INDEX IF NOT EXISTS idx_issues_project_status_updated ON issues(LOWER(project), status, updated DESC, id)`,
				// Changelog sync candidate selection
				`CREATE INDEX IF NOT EXISTS idx_issues_proj_cl_sync ON issues(project, changelog_synced_at, updated)`,
				// Unconditional (field, issue_key, changed_at) index replacing narrow partial idx_cl_tracked
				`CREATE INDEX IF NOT EXISTS idx_changelog_field_issue_time ON changelog(field, issue_key, changed_at)`,
				`DROP INDEX IF EXISTS idx_cl_tracked`,
				// Per-issue activity timelines (eliminates TEMP B-TREE sort)
				`CREATE INDEX IF NOT EXISTS idx_changelog_issue_time_desc ON changelog(issue_key, changed_at DESC, id DESC)`,
				`CREATE INDEX IF NOT EXISTS idx_comments_issue_created_desc ON comments(issue_key, created DESC, id DESC)`,
				// Metrics fact tables: project-scoped reads
				`CREATE INDEX IF NOT EXISTS idx_ffi_gen_field_proj ON fact_field_interval(generation, field, project, issue_key, seq)`,
				`CREATE INDEX IF NOT EXISTS idx_fim_gen_dim_proj ON fact_issue_membership(generation, dimension, project, issue_key, seq)`,
				// Blocker metrics & key-rename trigger cascade
				`CREATE INDEX IF NOT EXISTS idx_issue_links_type ON issue_links(link_type, issue_key, linked_key)`,
				`CREATE INDEX IF NOT EXISTS idx_pending_changes_issue_key ON pending_changes(issue_key)`,
			}
			for _, stmt := range stmts {
				if _, err := tx.Exec(stmt); err != nil {
					return err
				}
			}
			return nil
		},
	},
}

// issuesKeyRenameTrigger cascades a key change (issue moved project, or project
// renamed) to every child relation keyed by issue_key and records the retired key
// as an alias. It fires from UpsertIssue's ON CONFLICT(id) DO UPDATE, the only
// place a live row's key changes. The child UPDATEs run before SQLite's
// end-of-statement foreign-key check, so comments(issue_key) REFERENCES
// issues(key) stays satisfied without ON UPDATE CASCADE.
const issuesKeyRenameTrigger = `CREATE TRIGGER IF NOT EXISTS issues_key_rename
AFTER UPDATE OF key ON issues
WHEN old.key <> new.key
BEGIN
	UPDATE comments        SET issue_key = new.key WHERE issue_key = old.key;
	UPDATE changelog       SET issue_key = new.key WHERE issue_key = old.key;
	UPDATE issue_links     SET issue_key = new.key WHERE issue_key = old.key;
	UPDATE dev_links       SET issue_key = new.key WHERE issue_key = old.key;
	UPDATE attachments     SET issue_key = new.key WHERE issue_key = old.key;
	UPDATE pending_changes SET issue_key = new.key WHERE issue_key = old.key;
	INSERT OR REPLACE INTO issue_key_alias(old_key, id, changed_at)
		VALUES (old.key, new.id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));
END`

// dedupIssuesByID collapses rows that share a Jira numeric id — the duplicates
// left behind when an issue moved project under the old key-keyed upsert. For each
// id it keeps the most recently synced row (the live key) and folds every stale
// duplicate into it: child relations are repointed to the live key (colliding rows
// dropped), the stale key is aliased to the id, and the stale issues row removed.
// Must run before the UNIQUE(id) index is created.
//
// It resolves survivors and losers in a SINGLE ordered scan of issues. No id index
// exists yet at this point, so a per-id `WHERE id = ?` lookup would full-scan the
// wide issues table once per duplicate — O(duplicates × table) and minutes on a
// multi-GB cache. The child/issues mutations below touch only the (few) duplicate
// keys and hit the existing issue_key / key indexes.
func dedupIssuesByID(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT id, key FROM issues
		WHERE id IS NOT NULL AND id <> ''
		ORDER BY id, synced_at DESC, rowid DESC`)
	if err != nil {
		return err
	}
	// First row seen for an id is its survivor (most recent sync); the rest lose.
	survivors := make(map[string]string)
	losers := make(map[string][]string)
	for rows.Next() {
		var id, key string
		if err := rows.Scan(&id, &key); err != nil {
			rows.Close()
			return err
		}
		if _, seen := survivors[id]; !seen {
			survivors[id] = key
			continue
		}
		losers[id] = append(losers[id], key)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	childTables := []string{"comments", "changelog", "issue_links", "dev_links", "attachments", "pending_changes"}
	now := time.Now().UTC().Format(time.RFC3339)

	for id, keys := range losers {
		survivor := survivors[id]
		for _, loser := range keys {
			for _, t := range childTables {
				if _, err := tx.Exec(fmt.Sprintf(`UPDATE OR IGNORE %s SET issue_key = ? WHERE issue_key = ?`, t), survivor, loser); err != nil {
					return err
				}
				if _, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s WHERE issue_key = ?`, t), loser); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(
				`INSERT OR REPLACE INTO issue_key_alias(old_key, id, changed_at) VALUES (?, ?, ?)`,
				loser, id, now,
			); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM issues WHERE key = ?`, loser); err != nil {
				return err
			}
		}
	}
	return nil
}

func addColumnIfNotExists(tx *sql.Tx, table, column, columnType string) error {
	var exists bool
	query := fmt.Sprintf(`SELECT count(*) > 0 FROM pragma_table_info('%s') WHERE name = '%s'`, table, column)
	row := tx.QueryRow(query)
	if err := row.Scan(&exists); err != nil {
		return err
	}
	if !exists {
		alterStmt := fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, columnType)
		if _, err := tx.Exec(alterStmt); err != nil {
			return err
		}
	}
	return nil
}

func convertCSVToJSON(tx *sql.Tx, column string) error {
	if !safeColumnRe.MatchString(column) {
		return fmt.Errorf("invalid column name: %s", column)
	}

	q := fmt.Sprintf(
		`SELECT rowid, "%s" FROM issues WHERE "%s" IS NOT NULL AND "%s" != '' AND "%s" NOT LIKE '[%%'`,
		column, column, column, column,
	)
	rows, err := tx.Query(q)
	if err != nil {
		return err
	}

	type row struct {
		rowid int64
		val   string
	}
	var updates []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.rowid, &r.val) == nil {
			updates = append(updates, r)
		}
	}
	rows.Close()

	if len(updates) == 0 {
		return nil
	}

	stmt, err := tx.Prepare(fmt.Sprintf(`UPDATE issues SET "%s" = ? WHERE rowid = ?`, column))
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, u := range updates {
		parts := strings.Split(u.val, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		b, err := json.Marshal(parts)
		if err != nil {
			continue
		}
		if _, err := stmt.Exec(string(b), u.rowid); err != nil {
			return err
		}
	}
	return nil
}

// Migrate applies any pending schema migrations in order.
func (db *DB) Migrate() error {
	// Ensure schema_version table exists before querying it.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
		version     INTEGER PRIMARY KEY,
		applied_at  DATETIME NOT NULL DEFAULT (datetime('now')),
		description TEXT
	)`); err != nil {
		return fmt.Errorf("creating schema_version: %w", err)
	}

	current, err := db.currentVersion()
	if err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("beginning migration %d: %w", m.version, err)
		}

		if err := m.up(tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d (%s): %w", m.version, m.description, err)
		}

		if _, err := tx.Exec(
			`INSERT INTO schema_version (version, applied_at, description) VALUES (?, ?, ?)`,
			m.version, time.Now().UTC().Format(time.RFC3339), m.description,
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("recording migration %d: %w", m.version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %d: %w", m.version, err)
		}
	}

	return nil
}

func (db *DB) currentVersion() (int, error) {
	var version int
	err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version)
	return version, err
}
