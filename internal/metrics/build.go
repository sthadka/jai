package metrics

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
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
// (resolution/type live on issues, not in the replay history). scalar holds one replay
// input per configured scalar tracked field (keyed by field name); status is replayed
// separately via hist.
type issueSource struct {
	hist       IssueHistory
	resolved   *time.Time
	resolution string
	itype      string
	scalar     map[string]*ScalarHistory
	membership map[string]*MembershipHistory
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
	if _, err := tx.Exec(`DELETE FROM fact_issue_membership WHERE generation != ?`, gen); err != nil {
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
		if _, err := tx.Exec(`DELETE FROM fact_issue_membership WHERE generation = ? AND issue_key = ?`, gen, k); err != nil {
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
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
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
	memStmt, err := tx.Prepare(
		`INSERT INTO fact_issue_membership
		   (generation, issue_key, project, dimension, seq, member_id, member_name, entered_at, exited_at, is_open)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer memStmt.Close()

	scalarFields := ScalarTrackedFields(b.pol)
	membershipFields := b.pol.MembershipFields()
	intervalCount := 0
	for _, src := range sources {
		result := ReplayStatus(src.hist, b.pol, asOf)
		for seq, iv := range result.Intervals {
			if _, err := ivStmt.Exec(
				gen, src.hist.Key, src.hist.Project, "status", seq,
				nullStr(iv.ValueID), nullStr(iv.Value), nullStr(iv.Stage), nullStr(iv.Provenance),
				dbTime(iv.EnteredAt), dbTimePtr(iv.ExitedAt), iv.Open,
			); err != nil {
				return 0, err
			}
			intervalCount++
		}

		// Scalar tracked fields (priority, assignee, Team, Rank, ...). Set-valued fields
		// are excluded by ScalarTrackedFields and modeled as membership intervals instead;
		// scalar replay never has a stage, so the stage column is NULL.
		for _, field := range scalarFields {
			sh := src.scalar[field]
			if sh == nil {
				continue
			}
			sr, err := ReplayScalar(*sh, b.pol, asOf)
			if err != nil {
				return 0, fmt.Errorf("replay scalar %q for %s: %w", field, src.hist.Key, err)
			}
			for seq, iv := range sr.Intervals {
				if _, err := ivStmt.Exec(
					gen, src.hist.Key, src.hist.Project, field, seq,
					nullStr(iv.ValueID), nullStr(iv.Value), nil, nullStr(iv.Provenance),
					dbTime(iv.EnteredAt), dbTimePtr(iv.ExitedAt), iv.Open,
				); err != nil {
					return 0, err
				}
				intervalCount++
			}
		}

		// Set-valued membership fields (components, labels, Sprint). Each resulting
		// interval is one event-time row per (issue, member, interval); exited_at stays
		// NULL for open memberships. Membership is never scalar-replayed.
		for _, field := range membershipFields {
			mh := src.membership[field]
			if mh == nil {
				continue
			}
			dim, ok := MembershipDimension(field)
			if !ok {
				continue
			}
			mr := ReplayMembership(*mh)
			for seq, iv := range mr.Intervals {
				if _, err := memStmt.Exec(
					gen, src.hist.Key, src.hist.Project, dim, seq,
					nullStr(iv.Member.ID), nullStr(iv.Member.Name),
					dbTime(iv.EnteredAt), dbTimePtr(iv.ExitedAt), iv.Open,
				); err != nil {
					return 0, err
				}
			}
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
	scalarFields := ScalarTrackedFields(b.pol)
	membershipFields := b.pol.MembershipFields()
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
				HistoryComplete:       nullTime(synced) != nil,
				CurrentStatus:         status.String,
				CurrentStatusCategory: category.String,
			},
			resolution: resltn.String,
			itype:      itype.String,
		}
		if t := nullTime(created); t != nil {
			src.hist.CreatedAt = *t
		}
		src.resolved = nullTime(resolved)
		if t := nullTime(updated); t != nil && t.After(watermark) {
			watermark = *t
		}
		if len(scalarFields) > 0 {
			src.scalar = make(map[string]*ScalarHistory, len(scalarFields))
			for _, f := range scalarFields {
				src.scalar[f] = &ScalarHistory{
					Key:             key,
					Project:         project,
					Field:           f,
					CreatedAt:       src.hist.CreatedAt,
					HistoryComplete: src.hist.HistoryComplete,
				}
			}
		}
		if len(membershipFields) > 0 {
			src.membership = make(map[string]*MembershipHistory, len(membershipFields))
			for _, f := range membershipFields {
				src.membership[f] = &MembershipHistory{
					Key:             key,
					Project:         project,
					Field:           f,
					CreatedAt:       src.hist.CreatedAt,
					HistoryComplete: src.hist.HistoryComplete,
				}
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

	if len(scalarFields) > 0 {
		if err := b.loadScalarCurrent(projects, keys, scalarFields, index, sources); err != nil {
			return nil, nil, err
		}
		if err := b.loadScalarEvents(projects, keys, scalarFields, index, sources, &watermark); err != nil {
			return nil, nil, err
		}
	}

	if len(membershipFields) > 0 {
		if err := b.loadMembershipCurrent(projects, keys, membershipFields, index, sources); err != nil {
			return nil, nil, err
		}
		if err := b.loadMembershipEvents(projects, keys, membershipFields, index, sources, &watermark); err != nil {
			return nil, nil, err
		}
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

// loadScalarEvents appends changelog transitions for the scalar tracked fields to the
// per-field replay inputs already attached to each source. One query covers all scalar
// fields; rows are grouped by (issue, field).
func (b *Builder) loadScalarEvents(projects, keys, fields []string, index map[string]int, sources []issueSource, watermark *time.Time) error {
	fph := make([]string, len(fields))
	cargs := make([]any, 0, len(fields)+len(keys)+len(projects))
	for i, f := range fields {
		fph[i] = "?"
		cargs = append(cargs, f)
	}
	cq := `SELECT id, issue_key, field, from_value, from_string, to_value, to_string, changed_at
	         FROM changelog WHERE field IN (` + strings.Join(fph, ", ") + `)`
	switch {
	case len(keys) > 0:
		args, in := keyFilter(keys, nil)
		cq += " AND issue_key IN " + in
		cargs = append(cargs, args...)
	case len(projects) > 0:
		ph := make([]string, len(projects))
		for i, p := range projects {
			ph[i] = "?"
			cargs = append(cargs, p)
		}
		cq += " AND issue_key IN (SELECT key FROM issues WHERE project IN (" + strings.Join(ph, ", ") + "))"
	}
	cq += " ORDER BY issue_key, field, changed_at"

	crows, err := b.store.db.Query(cq, cargs...)
	if err != nil {
		return fmt.Errorf("load scalar events: %w", err)
	}
	defer crows.Close()
	for crows.Next() {
		var (
			id, issueKey, field             string
			fromV, fromS, toV, toS, changed sql.NullString
		)
		if err := crows.Scan(&id, &issueKey, &field, &fromV, &fromS, &toV, &toS, &changed); err != nil {
			return err
		}
		i, ok := index[issueKey]
		if !ok {
			continue
		}
		sh := sources[i].scalar[field]
		if sh == nil {
			continue
		}
		ev := ScalarEvent{
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
		sh.Events = append(sh.Events, ev)
	}
	return crows.Err()
}

// loadMembershipCurrent fills each set-valued field's current snapshot set from the issues
// row (sprint_id/sprint_name, components, labels). It is used only for the never-changed
// seeding path (no changelog events): historical membership always comes from events, never
// from today's snapshot. The snapshot carries names only for components/labels (no stable
// ids), so those seed name-keyed members; sprint carries its id.
func (b *Builder) loadMembershipCurrent(projects, keys, fields []string, index map[string]int, sources []issueSource) error {
	q := `SELECT key, sprint_id, sprint_name, components, labels FROM issues`
	var args []any
	switch {
	case len(keys) > 0:
		args, _ = keyFilter(keys, &q)
	case len(projects) > 0:
		ph := make([]string, len(projects))
		for i, p := range projects {
			ph[i] = "?"
			args = append(args, p)
		}
		q += " WHERE project IN (" + strings.Join(ph, ", ") + ")"
	}
	rows, err := b.store.db.Query(q, args...)
	if err != nil {
		return fmt.Errorf("load membership current values: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			key                       string
			sprintID                  sql.NullInt64
			sprintName, comps, labels sql.NullString
		)
		if err := rows.Scan(&key, &sprintID, &sprintName, &comps, &labels); err != nil {
			return err
		}
		i, ok := index[key]
		if !ok {
			continue
		}
		for _, f := range fields {
			mh := sources[i].membership[f]
			if mh == nil {
				continue
			}
			dim, _ := MembershipDimension(f)
			switch dim {
			case "sprint":
				if sprintID.Valid {
					mh.CurrentValue = strconv.FormatInt(sprintID.Int64, 10)
				}
				if sprintName.Valid {
					mh.CurrentString = sprintName.String
				}
			case "component":
				if names := parseJSONArray(comps.String); len(names) > 0 {
					mh.CurrentString = strings.Join(names, ",")
				}
			case "label":
				if names := parseJSONArray(labels.String); len(names) > 0 {
					mh.CurrentString = strings.Join(names, " ")
				}
			}
		}
	}
	return rows.Err()
}

// loadMembershipEvents appends the set-valued changelog transitions for the membership
// fields to the per-field membership histories already attached to each source. One query
// covers all membership fields; rows are grouped by (issue, field).
func (b *Builder) loadMembershipEvents(projects, keys, fields []string, index map[string]int, sources []issueSource, watermark *time.Time) error {
	fph := make([]string, len(fields))
	cargs := make([]any, 0, len(fields)+len(keys)+len(projects))
	for i, f := range fields {
		fph[i] = "?"
		cargs = append(cargs, f)
	}
	cq := `SELECT id, issue_key, field, from_value, from_string, to_value, to_string, changed_at
	         FROM changelog WHERE field IN (` + strings.Join(fph, ", ") + `)`
	switch {
	case len(keys) > 0:
		args, in := keyFilter(keys, nil)
		cq += " AND issue_key IN " + in
		cargs = append(cargs, args...)
	case len(projects) > 0:
		ph := make([]string, len(projects))
		for i, p := range projects {
			ph[i] = "?"
			cargs = append(cargs, p)
		}
		cq += " AND issue_key IN (SELECT key FROM issues WHERE project IN (" + strings.Join(ph, ", ") + "))"
	}
	cq += " ORDER BY issue_key, field, changed_at"

	crows, err := b.store.db.Query(cq, cargs...)
	if err != nil {
		return fmt.Errorf("load membership events: %w", err)
	}
	defer crows.Close()
	for crows.Next() {
		var (
			id, issueKey, field             string
			fromV, fromS, toV, toS, changed sql.NullString
		)
		if err := crows.Scan(&id, &issueKey, &field, &fromV, &fromS, &toV, &toS, &changed); err != nil {
			return err
		}
		i, ok := index[issueKey]
		if !ok {
			continue
		}
		mh := sources[i].membership[field]
		if mh == nil {
			continue
		}
		ev := MembershipEvent{
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
		mh.Events = append(mh.Events, ev)
	}
	return crows.Err()
}

// loadScalarCurrent fills each scalar field's current snapshot value from the issues row,
// but only for fields that map to an existing issues column. A field with no column keeps
// CurrentKnown == false, so a never-changed issue surfaces as an explicit unknown period
// rather than fabricating a value (field availability is a coverage diagnostic).
func (b *Builder) loadScalarCurrent(projects, keys, fields []string, index map[string]int, sources []issueSource) error {
	cols, err := b.scalarColumns(fields)
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return nil
	}
	// Deterministic column order following the configured field order.
	fieldOrder := make([]string, 0, len(cols))
	selCols := make([]string, 0, len(cols))
	for _, f := range fields {
		if col, ok := cols[f]; ok {
			fieldOrder = append(fieldOrder, f)
			selCols = append(selCols, `"`+col+`"`)
		}
	}
	q := `SELECT key, ` + strings.Join(selCols, ", ") + ` FROM issues`
	var args []any
	switch {
	case len(keys) > 0:
		args, _ = keyFilter(keys, &q)
	case len(projects) > 0:
		ph := make([]string, len(projects))
		for i, p := range projects {
			ph[i] = "?"
			args = append(args, p)
		}
		q += " WHERE project IN (" + strings.Join(ph, ", ") + ")"
	}
	rows, err := b.store.db.Query(q, args...)
	if err != nil {
		return fmt.Errorf("load scalar current values: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		vals := make([]sql.NullString, len(fieldOrder))
		dest := make([]any, 0, len(fieldOrder)+1)
		dest = append(dest, &key)
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		i, ok := index[key]
		if !ok {
			continue
		}
		for j, f := range fieldOrder {
			if sh := sources[i].scalar[f]; sh != nil {
				sh.CurrentKnown = true
				sh.CurrentValue = vals[j].String
			}
		}
	}
	return rows.Err()
}

// scalarColumns resolves each scalar tracked field to the issues column holding its current
// value, limited to fields whose column actually exists. Resolution is via field_map
// (matching the changelog field name against name or jira_name), falling back to a
// same-named column.
func (b *Builder) scalarColumns(fields []string) (map[string]string, error) {
	existing := map[string]bool{}
	crows, err := b.store.db.Query(`PRAGMA table_info(issues)`)
	if err != nil {
		return nil, fmt.Errorf("inspect issues columns: %w", err)
	}
	for crows.Next() {
		var (
			cid, notnull, pk int
			name             string
			ctype, dflt      sql.NullString
		)
		if err := crows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			crows.Close()
			return nil, err
		}
		existing[name] = true
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return nil, err
	}

	lookup := map[string]string{}
	frows, err := b.store.db.Query(`SELECT name, jira_name FROM field_map WHERE is_column = 1`)
	if err != nil {
		return nil, fmt.Errorf("load field map: %w", err)
	}
	for frows.Next() {
		var name string
		var jiraName sql.NullString
		if err := frows.Scan(&name, &jiraName); err != nil {
			frows.Close()
			return nil, err
		}
		lookup[strings.ToLower(name)] = name
		if jn := strings.TrimSpace(jiraName.String); jn != "" {
			lookup[strings.ToLower(jn)] = name
		}
	}
	frows.Close()
	if err := frows.Err(); err != nil {
		return nil, err
	}

	out := map[string]string{}
	for _, f := range fields {
		col, ok := lookup[strings.ToLower(strings.TrimSpace(f))]
		if !ok && existing[f] {
			col, ok = f, true
		}
		if ok && existing[col] {
			out[f] = col
		}
	}
	return out, nil
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
