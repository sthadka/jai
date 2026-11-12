package metrics

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/sthadka/jai/internal/db"
)

// activeGenSub is the scalar subquery selecting the active generation id. Every reader
// filters on it so a staged, interrupted, or obsolete generation is never visible; a
// NULL pointer (no active generation yet) matches no rows.
const activeGenSub = `(SELECT generation FROM metrics_active_generation WHERE id = 1)`

// Store reads and persists status facts under atomically-published generations. It owns
// the database handle plus the canonical advisory-lock path derived from dbPath; every
// writer takes that lock across its whole read/build/activation window.
type Store struct {
	db     *db.DB
	dbPath string
}

// NewStore binds a Store to a database and the on-disk path of that database. dbPath is
// required to derive the cross-process advisory-lock sidecar path; pass the same path
// used to open the DB.
func NewStore(database *db.DB, dbPath string) *Store {
	return &Store{db: database, dbPath: dbPath}
}

// GenerationMeta describes one build. BuiltAt is the build watermark and SourceWatermark
// the latest source change the build observed; neither is a query as_of (open intervals
// age against an explicit as_of at read time, never a frozen build now).
type GenerationMeta struct {
	Generation      int64
	Fingerprint     string
	Scope           []string
	BuiltAt         time.Time
	SourceWatermark *time.Time
	IssueCount      int
}

// FlowRow is the one-row-per-issue Phase-1 snapshot flow fact. Lead/Cycle are nil when
// their inputs are missing or timestamps are reversed; the delivery service classifies
// such issues as published exclusions rather than fabricating a value.
type FlowRow struct {
	IssueKey      string
	Project       string
	Type          string
	Created       *time.Time
	FirstActiveAt *time.Time
	Resolved      *time.Time
	Resolution    string
	LeadSeconds   *int64
	CycleSeconds  *int64
	ReopenCount   int
	CurrentStage  string
	HasActive     bool
	HasUnknown    bool
	// FlowOccupancy is the persisted active-status occupancy proxy (active ÷ lead,
	// bounded [0,1]); nil for issues the flow_occupancy provider excludes (non-delivery
	// or undefined lead). Estimate is the issue's story-point value; nil when absent.
	FlowOccupancy *float64
	Estimate      *float64
}

// IntervalRow is a persisted status dwell. Open intervals have ExitedAt == nil and
// Open == true and are aged against an explicit as_of by the reader.
type IntervalRow struct {
	IssueKey   string
	Project    string
	Field      string
	Seq        int
	ValueID    string
	Value      string
	Stage      string
	Provenance string
	EnteredAt  time.Time
	ExitedAt   *time.Time
	Open       bool
}

// MembershipIntervalRow is a persisted event-time membership interval for a set-valued
// dimension (component, label, sprint). Open intervals have ExitedAt == nil and Open ==
// true. One row per (issue, member, interval): a GROUP BY member_name over a temporal join
// never double-counts a single issue's measure. MemberID is empty for members with no
// stable id (e.g. labels, or a components snapshot that carries names only).
type MembershipIntervalRow struct {
	IssueKey   string
	Project    string
	Dimension  string
	Seq        int
	MemberID   string
	MemberName string
	EnteredAt  time.Time
	ExitedAt   *time.Time
	Open       bool
}

// LockPath returns the advisory-lock sidecar path this store serializes writers on.
func (s *Store) LockPath() string { return LockPath(s.dbPath) }

// ActiveGeneration returns the currently active generation's metadata, or (nil, nil)
// when no generation has been activated yet.
func (s *Store) ActiveGeneration() (*GenerationMeta, error) {
	var genID sql.NullInt64
	if err := s.db.QueryRow(`SELECT generation FROM metrics_active_generation WHERE id = 1`).Scan(&genID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if !genID.Valid {
		return nil, nil
	}
	return s.generation(genID.Int64)
}

func (s *Store) generation(gen int64) (*GenerationMeta, error) {
	var (
		fingerprint string
		scope       sql.NullString
		builtAt     string
		watermark   sql.NullString
		issueCount  int
	)
	err := s.db.QueryRow(
		`SELECT fingerprint, scope, built_at, source_watermark, issue_count
		   FROM metrics_generation WHERE generation = ?`, gen,
	).Scan(&fingerprint, &scope, &builtAt, &watermark, &issueCount)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	meta := &GenerationMeta{
		Generation:  gen,
		Fingerprint: fingerprint,
		BuiltAt:     parseDBTime(builtAt),
		IssueCount:  issueCount,
	}
	if scope.Valid && strings.TrimSpace(scope.String) != "" {
		meta.Scope = strings.Split(scope.String, ",")
	}
	if watermark.Valid && strings.TrimSpace(watermark.String) != "" {
		t := parseDBTime(watermark.String)
		meta.SourceWatermark = &t
	}
	return meta, nil
}

// FlowRows returns the active generation's flow facts, optionally scoped to projects
// (empty = all). Results are ordered by (project, issue_key) for deterministic output.
func (s *Store) FlowRows(projects []string) ([]FlowRow, error) {
	q := `SELECT issue_key, project, type, created, first_active_at, resolved, resolution,
	             lead_seconds, cycle_seconds, reopen_count, current_stage, has_active, has_unknown,
	             flow_occupancy, estimate
	        FROM fact_issue_flow
	       WHERE generation = ` + activeGenSub
	args, where := projectFilter(projects, "project")
	q += where + " ORDER BY project, issue_key"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("flow rows query: %w", err)
	}
	defer rows.Close()

	var out []FlowRow
	for rows.Next() {
		var (
			r                     FlowRow
			itype, resolution     sql.NullString
			created, firstActive  sql.NullString
			resolved              sql.NullString
			leadSec, cycleSec     sql.NullInt64
			currentStage          sql.NullString
			hasActive, hasUnknown bool
			occupancy, estimate   sql.NullFloat64
		)
		if err := rows.Scan(&r.IssueKey, &r.Project, &itype, &created, &firstActive, &resolved,
			&resolution, &leadSec, &cycleSec, &r.ReopenCount, &currentStage, &hasActive, &hasUnknown,
			&occupancy, &estimate); err != nil {
			return nil, err
		}
		r.Type = itype.String
		r.Resolution = resolution.String
		r.CurrentStage = currentStage.String
		r.HasActive = hasActive
		r.HasUnknown = hasUnknown
		r.Created = nullTime(created)
		r.FirstActiveAt = nullTime(firstActive)
		r.Resolved = nullTime(resolved)
		if leadSec.Valid {
			v := leadSec.Int64
			r.LeadSeconds = &v
		}
		if cycleSec.Valid {
			v := cycleSec.Int64
			r.CycleSeconds = &v
		}
		if occupancy.Valid {
			v := occupancy.Float64
			r.FlowOccupancy = &v
		}
		if estimate.Valid {
			v := estimate.Float64
			r.Estimate = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Intervals returns the active generation's interval facts for a field (Phase 1:
// "status"), optionally scoped to projects. Ordered by (issue_key, seq).
func (s *Store) Intervals(projects []string, field string) ([]IntervalRow, error) {
	q := `SELECT issue_key, project, field, seq, value_id, value, stage, provenance,
	             entered_at, exited_at, is_open
	        FROM fact_field_interval
	       WHERE generation = ` + activeGenSub + ` AND field = ?`
	args := []any{field}
	more, where := projectFilter(projects, "project")
	q += where + " ORDER BY issue_key, seq"
	args = append(args, more...)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("interval rows query: %w", err)
	}
	defer rows.Close()

	var out []IntervalRow
	for rows.Next() {
		var (
			r                 IntervalRow
			valueID, value    sql.NullString
			stage, provenance sql.NullString
			enteredAt         string
			exitedAt          sql.NullString
			isOpen            bool
		)
		if err := rows.Scan(&r.IssueKey, &r.Project, &r.Field, &r.Seq, &valueID, &value,
			&stage, &provenance, &enteredAt, &exitedAt, &isOpen); err != nil {
			return nil, err
		}
		r.ValueID = valueID.String
		r.Value = value.String
		r.Stage = stage.String
		r.Provenance = provenance.String
		r.EnteredAt = parseDBTime(enteredAt)
		r.ExitedAt = nullTime(exitedAt)
		r.Open = isOpen
		out = append(out, r)
	}
	return out, rows.Err()
}

// MembershipIntervals returns the active generation's membership interval facts for a
// dimension (component, label, sprint), optionally scoped to projects. Ordered by
// (issue_key, seq). Mirrors Intervals; the half-open temporal join
// `entered_at <= t AND (exited_at IS NULL OR exited_at > t)` yields event-time members.
func (s *Store) MembershipIntervals(projects []string, dimension string) ([]MembershipIntervalRow, error) {
	q := `SELECT issue_key, project, dimension, seq, member_id, member_name,
	             entered_at, exited_at, is_open
	        FROM fact_issue_membership
	       WHERE generation = ` + activeGenSub + ` AND dimension = ?`
	args := []any{dimension}
	more, where := projectFilter(projects, "project")
	q += where + " ORDER BY issue_key, seq"
	args = append(args, more...)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("membership interval rows query: %w", err)
	}
	defer rows.Close()

	var out []MembershipIntervalRow
	for rows.Next() {
		var (
			r                    MembershipIntervalRow
			memberID, memberName sql.NullString
			enteredAt            string
			exitedAt             sql.NullString
			isOpen               bool
		)
		if err := rows.Scan(&r.IssueKey, &r.Project, &r.Dimension, &r.Seq, &memberID, &memberName,
			&enteredAt, &exitedAt, &isOpen); err != nil {
			return nil, err
		}
		r.MemberID = memberID.String
		r.MemberName = memberName.String
		r.EnteredAt = parseDBTime(enteredAt)
		r.ExitedAt = nullTime(exitedAt)
		r.Open = isOpen
		out = append(out, r)
	}
	return out, rows.Err()
}

// projectFilter builds an "AND col IN (?, ...)" clause (empty when no projects) plus the
// matching args, so scoped and unscoped reads share one code path.
func projectFilter(projects []string, col string) ([]any, string) {
	if len(projects) == 0 {
		return nil, ""
	}
	ph := make([]string, len(projects))
	args := make([]any, len(projects))
	for i, p := range projects {
		ph[i] = "?"
		args[i] = p
	}
	return args, " AND " + col + " IN (" + strings.Join(ph, ", ") + ")"
}

// nullTime converts a scanned DATETIME string to a *time.Time, returning nil when absent.
// The go-sqlite3 driver auto-parses DATETIME columns, so an empty/unset value surfaces as
// the zero time ("0001-01-01T00:00:00Z"); that is treated as absent, not a real instant.
func nullTime(s sql.NullString) *time.Time {
	if !s.Valid || strings.TrimSpace(s.String) == "" {
		return nil
	}
	t := parseDBTime(s.String)
	if t.IsZero() {
		return nil
	}
	return &t
}

func dbTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func dbTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return dbTime(*t)
}
