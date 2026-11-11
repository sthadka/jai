package metrics

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrFullRebuildRequired is returned by Incremental when no compatible active
// generation exists to patch — because none has been built yet or because the policy
// fingerprint changed (a config/mapping change restates history). The caller must run a
// full BuildAll instead.
var ErrFullRebuildRequired = errors.New("metrics: full rebuild required (no compatible active generation)")

// Builder constructs and publishes status-fact generations for a Policy. Every public
// method takes the cross-process advisory lock for the whole read/build/activation (or
// incremental replacement) window, so a staged rebuild can never publish stale facts
// over a newer incremental update, cross-process.
type Builder struct {
	store *Store
	pol   *Policy

	// afterRead, if set, runs inside BuildAll after the source snapshot is read but
	// before the staging transaction opens. It is a test-only seam for exercising the
	// cross-process read -> activate window (lost-update regression); production leaves
	// it nil.
	afterRead func()
}

// NewBuilder binds a Builder to a store and a compiled policy.
func NewBuilder(store *Store, pol *Policy) *Builder {
	return &Builder{store: store, pol: pol}
}

// BuildResult summarizes a published generation.
type BuildResult struct {
	Generation      int64
	IssueCount      int
	IntervalCount   int
	Fingerprint     string
	BuiltAt         time.Time
	SourceWatermark *time.Time
}

// issueSource is one issue's replay input plus the snapshot fields the flow rollup needs
// (resolution/type live on issues, not in the replay history).
type issueSource struct {
	hist       IssueHistory
	resolved   *time.Time
	resolution string
	itype      string
}

// BuildAll performs a full restatement: it reads every in-scope issue's status history,
// replays intervals under the policy, stages a brand-new generation, and activates it in
// one transaction (then plain-DELETEs obsolete generations). projects empty = all. asOf
// is recorded as the open-interval reference is applied at read time, not frozen here.
func (b *Builder) BuildAll(asOf time.Time, projects []string) (*BuildResult, error) {
	lock, err := AcquireLock(b.store.dbPath)
	if err != nil {
		return nil, fmt.Errorf("acquire metrics lock: %w", err)
	}
	defer lock.Release()

	sources, watermark, err := b.loadSources(projects, nil)
	if err != nil {
		return nil, err
	}
	if b.afterRead != nil {
		b.afterRead()
	}

	tx, err := b.store.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	builtAt := time.Now().UTC()
	res, err := tx.Exec(
		`INSERT INTO metrics_generation (fingerprint, scope, built_at, source_watermark, issue_count)
		 VALUES (?, ?, ?, ?, 0)`,
		b.pol.Fingerprint(), strings.Join(projects, ","), dbTime(builtAt), dbTimePtr(watermark),
	)
	if err != nil {
		return nil, fmt.Errorf("insert generation: %w", err)
	}
	gen, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	intervalCount, err := b.writeFacts(tx, gen, sources, asOf)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE metrics_generation SET issue_count = ? WHERE generation = ?`, len(sources), gen); err != nil {
		return nil, err
	}

	// Atomic publication: swap the pointer, then drop every obsolete generation. A reader
	// in another process sees either the whole old generation or the whole new one.
	if _, err := tx.Exec(`UPDATE metrics_active_generation SET generation = ? WHERE id = 1`, gen); err != nil {
		return nil, fmt.Errorf("activate generation: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM fact_field_interval WHERE generation != ?`, gen); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM fact_issue_flow WHERE generation != ?`, gen); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM metrics_generation WHERE generation != ?`, gen); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit generation: %w", err)
	}

	return &BuildResult{
		Generation:      gen,
		IssueCount:      len(sources),
		IntervalCount:   intervalCount,
		Fingerprint:     b.pol.Fingerprint(),
		BuiltAt:         builtAt,
		SourceWatermark: watermark,
	}, nil
}

// Incremental replaces the facts for the given issue keys within the active generation,
// in one transaction, holding the advisory lock. Keys no longer present in issues are
// deleted (handles deletions); re-running with the same keys is idempotent (delete then
// re-insert). If the active generation is absent or its fingerprint no longer matches the
// policy, it returns ErrFullRebuildRequired.
func (b *Builder) Incremental(asOf time.Time, keys []string) (*BuildResult, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	lock, err := AcquireLock(b.store.dbPath)
	if err != nil {
		return nil, fmt.Errorf("acquire metrics lock: %w", err)
	}
	defer lock.Release()

	active, err := b.store.ActiveGeneration()
	if err != nil {
		return nil, err
	}
	if active == nil || active.Fingerprint != b.pol.Fingerprint() {
		return nil, ErrFullRebuildRequired
	}
	gen := active.Generation

	sources, watermark, err := b.loadSources(nil, keys)
	if err != nil {
		return nil, err
	}

	tx, err := b.store.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Delete the old facts for every requested key (including ones that no longer exist),
	// then re-insert replayed facts for the keys that still exist.
	for _, k := range keys {
		if _, err := tx.Exec(`DELETE FROM fact_field_interval WHERE generation = ? AND issue_key = ?`, gen, k); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`DELETE FROM fact_issue_flow WHERE generation = ? AND issue_key = ?`, gen, k); err != nil {
			return nil, err
		}
	}
	intervalCount, err := b.writeFacts(tx, gen, sources, asOf)
	if err != nil {
		return nil, err
	}

	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM fact_issue_flow WHERE generation = ?`, gen).Scan(&count); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`UPDATE metrics_generation SET issue_count = ?, source_watermark = COALESCE(?, source_watermark) WHERE generation = ?`,
		count, dbTimePtr(watermark), gen,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &BuildResult{
		Generation:      gen,
		IssueCount:      count,
		IntervalCount:   intervalCount,
		Fingerprint:     active.Fingerprint,
		BuiltAt:         active.BuiltAt,
		SourceWatermark: watermark,
	}, nil
}

// writeFacts replays each source and inserts its interval and flow facts under gen,
// returning the number of interval rows written. asOf ages open intervals only for the
// derived flow fields; exited_at stays NULL on disk.
func (b *Builder) writeFacts(tx *sql.Tx, gen int64, sources []issueSource, asOf time.Time) (int, error) {
	ivStmt, err := tx.Prepare(
		`INSERT INTO fact_field_interval
		   (generation, issue_key, project, field, seq, value_id, value, stage, provenance, entered_at, exited_at, is_open)
		 VALUES (?, ?, ?, 'status', ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer ivStmt.Close()
	flowStmt, err := tx.Prepare(
		`INSERT INTO fact_issue_flow
		   (generation, issue_key, project, type, created, first_active_at, resolved, resolution,
		    lead_seconds, cycle_seconds, reopen_count, current_stage, has_active, has_unknown)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer flowStmt.Close()

	intervalCount := 0
	for _, src := range sources {
		result := ReplayStatus(src.hist, b.pol, asOf)
		for seq, iv := range result.Intervals {
			if _, err := ivStmt.Exec(
				gen, src.hist.Key, src.hist.Project, seq,
				nullStr(iv.ValueID), nullStr(iv.Value), nullStr(iv.Stage), nullStr(iv.Provenance),
				dbTime(iv.EnteredAt), dbTimePtr(iv.ExitedAt), iv.Open,
			); err != nil {
				return 0, err
			}
			intervalCount++
		}

		flow := deriveFlow(result, src, b.pol)
		if _, err := flowStmt.Exec(
			gen, src.hist.Key, src.hist.Project, nullStr(src.itype),
			dbTimePtr(flow.Created), dbTimePtr(flow.FirstActiveAt), dbTimePtr(flow.Resolved), nullStr(flow.Resolution),
			nullInt64(flow.LeadSeconds), nullInt64(flow.CycleSeconds), flow.ReopenCount,
			nullStr(flow.CurrentStage), flow.HasActive, flow.HasUnknown,
		); err != nil {
			return 0, err
		}
	}
	return intervalCount, nil
}

// deriveFlow computes the Phase-1 snapshot flow rollup from the replayed intervals and
// the issue's current snapshot. first_active_at is the first WIP-stage entry; cycle time
// needs that start, lead time needs created, and both require a non-reversed resolved.
func deriveFlow(result ReplayResult, src issueSource, pol *Policy) FlowRow {
	fr := FlowRow{
		IssueKey:     src.hist.Key,
		Project:      src.hist.Project,
		Type:         src.itype,
		Resolution:   src.resolution,
		Resolved:     src.resolved,
		CurrentStage: result.CurrentStage,
		HasActive:    result.HasActiveInterval,
		HasUnknown:   result.HasUnknown,
	}
	if !src.hist.CreatedAt.IsZero() {
		c := src.hist.CreatedAt
		fr.Created = &c
	}

	// First active entry + WIP-episode count (reopen proxy): a WIP episode is a maximal
	// run of consecutive WIP intervals; reopen_count is episodes beyond the first.
	inWIP := false
	episodes := 0
	for i := range result.Intervals {
		iv := result.Intervals[i]
		wip := pol.IsWIPStage(iv.Stage)
		if wip && !inWIP {
			episodes++
			if fr.FirstActiveAt == nil {
				ea := iv.EnteredAt
				fr.FirstActiveAt = &ea
			}
		}
		inWIP = wip
	}
	if episodes > 1 {
		fr.ReopenCount = episodes - 1
	}

	if fr.Created != nil && fr.Resolved != nil && !fr.Resolved.Before(*fr.Created) {
		s := int64(fr.Resolved.Sub(*fr.Created).Seconds())
		fr.LeadSeconds = &s
	}
	if fr.FirstActiveAt != nil && fr.Resolved != nil && !fr.Resolved.Before(*fr.FirstActiveAt) {
		s := int64(fr.Resolved.Sub(*fr.FirstActiveAt).Seconds())
		fr.CycleSeconds = &s
	}
	return fr
}

// loadSources reads issue snapshots and their status changelog into replay inputs. Scope
// by projects or by explicit keys (mutually exclusive; both empty = all issues). It also
// returns the source watermark: the latest issue-update / changelog time observed.
func (b *Builder) loadSources(projects, keys []string) ([]issueSource, *time.Time, error) {
	iq := `SELECT key, project, type, created, status, status_category, changelog_synced_at, updated, resolved, resolution FROM issues`
	var iargs []any
	switch {
	case len(keys) > 0:
		iargs, _ = keyFilter(keys, &iq)
	case len(projects) > 0:
		ph := make([]string, len(projects))
		for i, p := range projects {
			ph[i] = "?"
			iargs = append(iargs, p)
		}
		iq += " WHERE project IN (" + strings.Join(ph, ", ") + ")"
	}

	rows, err := b.store.db.Query(iq, iargs...)
	if err != nil {
		return nil, nil, fmt.Errorf("load issues: %w", err)
	}
	defer rows.Close()

	index := map[string]int{}
	var sources []issueSource
	var watermark time.Time
	for rows.Next() {
		var (
			key, project                      string
			itype, created, status, category  sql.NullString
			synced, updated, resolved, resltn sql.NullString
		)
		if err := rows.Scan(&key, &project, &itype, &created, &status, &category, &synced, &updated, &resolved, &resltn); err != nil {
			return nil, nil, err
		}
		src := issueSource{
			hist: IssueHistory{
				Key:                   key,
				Project:               project,
				HistoryComplete:       synced.Valid && strings.TrimSpace(synced.String) != "",
				CurrentStatus:         status.String,
				CurrentStatusCategory: category.String,
			},
			resolution: resltn.String,
			itype:      itype.String,
		}
		if created.Valid && strings.TrimSpace(created.String) != "" {
			src.hist.CreatedAt = parseDBTime(created.String)
		}
		if resolved.Valid && strings.TrimSpace(resolved.String) != "" {
			t := parseDBTime(resolved.String)
			src.resolved = &t
		}
		if updated.Valid && strings.TrimSpace(updated.String) != "" {
			if t := parseDBTime(updated.String); t.After(watermark) {
				watermark = t
			}
		}
		index[key] = len(sources)
		sources = append(sources, src)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	if err := b.loadStatusEvents(projects, keys, index, sources, &watermark); err != nil {
		return nil, nil, err
	}

	var wm *time.Time
	if !watermark.IsZero() {
		wm = &watermark
	}
	return sources, wm, nil
}

// loadStatusEvents appends status changelog transitions to the already-loaded sources.
func (b *Builder) loadStatusEvents(projects, keys []string, index map[string]int, sources []issueSource, watermark *time.Time) error {
	cq := `SELECT id, issue_key, from_value, from_string, to_value, to_string, changed_at
	         FROM changelog WHERE field = 'status'`
	var cargs []any
	switch {
	case len(keys) > 0:
		args, in := keyFilter(keys, nil)
		cq += " AND issue_key IN " + in
		cargs = args
	case len(projects) > 0:
		ph := make([]string, len(projects))
		for i, p := range projects {
			ph[i] = "?"
			cargs = append(cargs, p)
		}
		cq += " AND issue_key IN (SELECT key FROM issues WHERE project IN (" + strings.Join(ph, ", ") + "))"
	}
	cq += " ORDER BY issue_key, changed_at"

	crows, err := b.store.db.Query(cq, cargs...)
	if err != nil {
		return fmt.Errorf("load status events: %w", err)
	}
	defer crows.Close()
	for crows.Next() {
		var (
			id, issueKey                    string
			fromV, fromS, toV, toS, changed sql.NullString
		)
		if err := crows.Scan(&id, &issueKey, &fromV, &fromS, &toV, &toS, &changed); err != nil {
			return err
		}
		i, ok := index[issueKey]
		if !ok {
			continue
		}
		ev := StatusEvent{
			ID:         id,
			FromValue:  fromV.String,
			FromString: fromS.String,
			ToValue:    toV.String,
			ToString:   toS.String,
		}
		if changed.Valid && strings.TrimSpace(changed.String) != "" {
			ev.ChangedAt = parseDBTime(changed.String)
			if ev.ChangedAt.After(*watermark) {
				*watermark = ev.ChangedAt
			}
		}
		sources[i].hist.Events = append(sources[i].hist.Events, ev)
	}
	return crows.Err()
}

// keyFilter appends/returns an "IN (?, ...)" clause for issue keys. When q is non-nil the
// clause is appended as a WHERE; otherwise only the parenthesized list is returned.
func keyFilter(keys []string, q *string) ([]any, string) {
	ph := make([]string, len(keys))
	args := make([]any, len(keys))
	for i, k := range keys {
		ph[i] = "?"
		args[i] = k
	}
	in := "(" + strings.Join(ph, ", ") + ")"
	if q != nil {
		*q += " WHERE key IN " + in
	}
	return args, in
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
