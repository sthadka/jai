package metrics

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sthadka/jai/v6/pkg/clock"
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
	clk   clock.Clock

	// afterRead, if set, runs inside BuildAll after the source snapshot is read but
	// before the staging transaction opens. It is a test-only seam for exercising the
	// cross-process read -> activate window (lost-update regression); production leaves
	// it nil.
	afterRead func()
}

// NewBuilder binds a Builder to a store and a compiled policy, using the default
// system clock for the generation build timestamp.
func NewBuilder(store *Store, pol *Policy) *Builder {
	return &Builder{store: store, pol: pol, clk: clock.System{}}
}

// WithClock overrides the clock used to stamp built generations. Returns the
// builder for chaining; a test seam for deterministic build timestamps.
func (b *Builder) WithClock(c clock.Clock) *Builder {
	if c == nil {
		c = clock.System{}
	}
	b.clk = c
	return b
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
	estimate   *float64
	scalar     map[string]*ScalarHistory
	membership map[string]*MembershipHistory
}

// maxFactBatch bounds rows written per staging transaction in BuildAll so
// concurrent UI/API writes never stall behind multi-second write locks.
const maxFactBatch = 5000

type stagedInterval struct {
	issueKey   string
	project    string
	field      string
	seq        int
	valueID    string
	value      string
	stage      any
	provenance string
	enteredAt  time.Time
	exitedAt   *time.Time
	open       bool
}

type stagedMembership struct {
	issueKey   string
	project    string
	dimension  string
	seq        int
	memberID   string
	memberName string
	enteredAt  time.Time
	exitedAt   *time.Time
	open       bool
}

type stagedFacts struct {
	intervals   []stagedInterval
	memberships []stagedMembership
	flows       []FlowRow
}

// BuildAll performs a full restatement: it reads every in-scope issue's history
// on ReadDB, replays intervals in Go before acquiring the writer lock, stages
// the new generation in short batched transactions, flips the active-generation
// pointer atomically, and cleans up obsolete generations in a post-commit tx.
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

	// Perform all replay computation in Go before opening any write transaction.
	staged, err := b.replaySources(sources, asOf)
	if err != nil {
		return nil, err
	}

	builtAt := b.clk.Now().UTC()

	// Step 1: register the staged generation row (invisible until activated).
	genTx, err := b.store.db.Begin()
	if err != nil {
		return nil, err
	}
	res, err := genTx.Exec(
		`INSERT INTO metrics_generation (fingerprint, scope, built_at, source_watermark, issue_count)
		 VALUES (?, ?, ?, ?, ?)`,
		b.pol.Fingerprint(), strings.Join(projects, ","), dbTime(builtAt), dbTimePtr(watermark), len(sources),
	)
	if err != nil {
		_ = genTx.Rollback()
		return nil, fmt.Errorf("insert generation: %w", err)
	}
	gen, err := res.LastInsertId()
	if err != nil {
		_ = genTx.Rollback()
		return nil, err
	}
	if err := genTx.Commit(); err != nil {
		return nil, fmt.Errorf("commit generation header: %w", err)
	}

	cleanupStaged := func() {
		if ctxTx, cerr := b.store.db.Begin(); cerr == nil {
			_, _ = ctxTx.Exec(`DELETE FROM fact_field_interval WHERE generation = ?`, gen)
			_, _ = ctxTx.Exec(`DELETE FROM fact_issue_flow WHERE generation = ?`, gen)
			_, _ = ctxTx.Exec(`DELETE FROM fact_issue_membership WHERE generation = ?`, gen)
			_, _ = ctxTx.Exec(`DELETE FROM metrics_generation WHERE generation = ?`, gen)
			_ = ctxTx.Commit()
		}
	}

	// Step 2: insert staged facts in short chunked transactions so writers never
	// hold an exclusive WAL write lock across tens of thousands of rows.
	if err := b.writeStagedChunked(gen, staged); err != nil {
		cleanupStaged()
		return nil, err
	}

	// Step 3: atomic publication via active-generation pointer swap.
	actTx, err := b.store.db.Begin()
	if err != nil {
		cleanupStaged()
		return nil, err
	}
	if _, err := actTx.Exec(`UPDATE metrics_active_generation SET generation = ? WHERE id = 1`, gen); err != nil {
		_ = actTx.Rollback()
		cleanupStaged()
		return nil, fmt.Errorf("activate generation: %w", err)
	}
	if err := actTx.Commit(); err != nil {
		cleanupStaged()
		return nil, fmt.Errorf("commit generation activation: %w", err)
	}

	// Step 4: drop obsolete generations in a separate post-commit transaction.
	if cleanTx, cerr := b.store.db.Begin(); cerr == nil {
		_, e1 := cleanTx.Exec(`DELETE FROM fact_field_interval WHERE generation != ?`, gen)
		_, e2 := cleanTx.Exec(`DELETE FROM fact_issue_flow WHERE generation != ?`, gen)
		_, e3 := cleanTx.Exec(`DELETE FROM fact_issue_membership WHERE generation != ?`, gen)
		_, e4 := cleanTx.Exec(`DELETE FROM metrics_generation WHERE generation != ?`, gen)
		if e1 == nil && e2 == nil && e3 == nil && e4 == nil {
			_ = cleanTx.Commit()
		} else {
			_ = cleanTx.Rollback()
		}
	}

	return &BuildResult{
		Generation:      gen,
		IssueCount:      len(sources),
		IntervalCount:   len(staged.intervals),
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
	keys = dedupStrings(keys)
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
	staged, err := b.replaySources(sources, asOf)
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
	for _, chunk := range chunkStrings(keys, maxKeyBindChunk) {
		kargs, in := keyFilter(chunk, nil)
		args := make([]any, 0, 1+len(kargs))
		args = append(args, gen)
		args = append(args, kargs...)
		if _, err := tx.Exec(`DELETE FROM fact_field_interval WHERE generation = ? AND issue_key IN `+in, args...); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`DELETE FROM fact_issue_flow WHERE generation = ? AND issue_key IN `+in, args...); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`DELETE FROM fact_issue_membership WHERE generation = ? AND issue_key IN `+in, args...); err != nil {
			return nil, err
		}
	}
	if err := writeStagedInTx(tx, gen, staged); err != nil {
		return nil, err
	}
	intervalCount := len(staged.intervals)

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

// replaySources performs all status, scalar, membership, and flow replays in Go
// memory so no SQLite write lock is held during computation.
func (b *Builder) replaySources(sources []issueSource, asOf time.Time) (*stagedFacts, error) {
	scalarFields := ScalarTrackedFields(b.pol)
	membershipFields := b.pol.MembershipFields()
	staged := &stagedFacts{
		intervals:   make([]stagedInterval, 0, len(sources)*2),
		memberships: make([]stagedMembership, 0, len(sources)),
		flows:       make([]FlowRow, 0, len(sources)),
	}
	for _, src := range sources {
		result := ReplayStatus(src.hist, b.pol, asOf)
		for seq, iv := range result.Intervals {
			staged.intervals = append(staged.intervals, stagedInterval{
				issueKey:   src.hist.Key,
				project:    src.hist.Project,
				field:      "status",
				seq:        seq,
				valueID:    iv.ValueID,
				value:      iv.Value,
				stage:      nullStr(iv.Stage),
				provenance: iv.Provenance,
				enteredAt:  iv.EnteredAt,
				exitedAt:   iv.ExitedAt,
				open:       iv.Open,
			})
		}

		for _, field := range scalarFields {
			sh := src.scalar[field]
			if sh == nil {
				continue
			}
			sr, err := ReplayScalar(*sh, b.pol, asOf)
			if err != nil {
				return nil, fmt.Errorf("replay scalar %q for %s: %w", field, src.hist.Key, err)
			}
			for seq, iv := range sr.Intervals {
				staged.intervals = append(staged.intervals, stagedInterval{
					issueKey:   src.hist.Key,
					project:    src.hist.Project,
					field:      field,
					seq:        seq,
					valueID:    iv.ValueID,
					value:      iv.Value,
					stage:      nil,
					provenance: iv.Provenance,
					enteredAt:  iv.EnteredAt,
					exitedAt:   iv.ExitedAt,
					open:       iv.Open,
				})
			}
		}

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
				staged.memberships = append(staged.memberships, stagedMembership{
					issueKey:   src.hist.Key,
					project:    src.hist.Project,
					dimension:  dim,
					seq:        seq,
					memberID:   iv.Member.ID,
					memberName: iv.Member.Name,
					enteredAt:  iv.EnteredAt,
					exitedAt:   iv.ExitedAt,
					open:       iv.Open,
				})
			}
		}

		staged.flows = append(staged.flows, deriveFlow(result, src, b.pol))
	}
	return staged, nil
}

const (
	insertIntervalSQL = `INSERT INTO fact_field_interval
		(generation, issue_key, project, field, seq, value_id, value, stage, provenance, entered_at, exited_at, is_open)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	insertFlowSQL = `INSERT INTO fact_issue_flow
		(generation, issue_key, project, type, created, first_active_at, resolved, resolution,
		 lead_seconds, cycle_seconds, reopen_count, current_stage, has_active, has_unknown,
		 flow_occupancy, estimate)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	insertMembershipSQL = `INSERT INTO fact_issue_membership
		(generation, issue_key, project, dimension, seq, member_id, member_name, entered_at, exited_at, is_open)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
)

func writeStagedInTx(tx *sql.Tx, gen int64, staged *stagedFacts) error {
	if len(staged.intervals) > 0 {
		stmt, err := tx.Prepare(insertIntervalSQL)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, iv := range staged.intervals {
			if _, err := stmt.Exec(
				gen, iv.issueKey, iv.project, iv.field, iv.seq,
				nullStr(iv.valueID), nullStr(iv.value), iv.stage, nullStr(iv.provenance),
				dbTime(iv.enteredAt), dbTimePtr(iv.exitedAt), iv.open,
			); err != nil {
				return err
			}
		}
	}
	if len(staged.memberships) > 0 {
		stmt, err := tx.Prepare(insertMembershipSQL)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, m := range staged.memberships {
			if _, err := stmt.Exec(
				gen, m.issueKey, m.project, m.dimension, m.seq,
				nullStr(m.memberID), nullStr(m.memberName),
				dbTime(m.enteredAt), dbTimePtr(m.exitedAt), m.open,
			); err != nil {
				return err
			}
		}
	}
	if len(staged.flows) > 0 {
		stmt, err := tx.Prepare(insertFlowSQL)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, flow := range staged.flows {
			if _, err := stmt.Exec(
				gen, flow.IssueKey, flow.Project, nullStr(flow.Type),
				dbTimePtr(flow.Created), dbTimePtr(flow.FirstActiveAt), dbTimePtr(flow.Resolved), nullStr(flow.Resolution),
				nullInt64(flow.LeadSeconds), nullInt64(flow.CycleSeconds), flow.ReopenCount,
				nullStr(flow.CurrentStage), flow.HasActive, flow.HasUnknown,
				nullFloat64(flow.FlowOccupancy), nullFloat64(flow.Estimate),
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *Builder) writeStagedChunked(gen int64, staged *stagedFacts) error {
	for i := 0; i < len(staged.intervals); i += maxFactBatch {
		end := min(i+maxFactBatch, len(staged.intervals))
		tx, err := b.store.db.Begin()
		if err != nil {
			return err
		}
		if err := writeStagedInTx(tx, gen, &stagedFacts{intervals: staged.intervals[i:end]}); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	for i := 0; i < len(staged.memberships); i += maxFactBatch {
		end := min(i+maxFactBatch, len(staged.memberships))
		tx, err := b.store.db.Begin()
		if err != nil {
			return err
		}
		if err := writeStagedInTx(tx, gen, &stagedFacts{memberships: staged.memberships[i:end]}); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	for i := 0; i < len(staged.flows); i += maxFactBatch {
		end := min(i+maxFactBatch, len(staged.flows))
		tx, err := b.store.db.Begin()
		if err != nil {
			return err
		}
		if err := writeStagedInTx(tx, gen, &stagedFacts{flows: staged.flows[i:end]}); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
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

	// estimate: the issue's story-point value where present (NULL when absent), enabling
	// the Effort (sum of estimate) measure. Absent across ROX; that is fine.
	fr.Estimate = src.estimate

	// flow_occupancy: persist the active-status occupancy proxy (active ÷ lead) using the
	// SAME definition as flowOccupancyProvider — delivered cohort, defined lead (resolved
	// strictly after created), active WIP seconds clipped to [created, resolved], bounded
	// to [0,1]. Left NULL (never a fabricated occupancy) for issues the provider excludes:
	// not delivered, or undefined lead. Persisting it lets BI/notebook users query
	// occupancy without a per-row windowed active-seconds aggregation.
	delivered := fr.Resolved != nil && (!pol.DoneResolutionsConfigured() || pol.IsDoneResolution(fr.Resolution))
	if delivered && fr.Created != nil && fr.Resolved.After(*fr.Created) {
		total := fr.Resolved.Sub(*fr.Created).Seconds()
		var active float64
		for i := range result.Intervals {
			iv := result.Intervals[i]
			active += wipSeconds(iv.Stage, iv.EnteredAt, iv.ExitedAt, *fr.Created, *fr.Resolved, pol)
		}
		ratio, _ := occupancyRatio(active, total)
		fr.FlowOccupancy = &ratio
	}
	return fr
}

// loadSources reads issue snapshots and their status changelog into replay inputs. Scope
// by projects or by explicit keys (mutually exclusive; both empty = all issues). It also
// returns the source watermark: the latest issue-update / changelog time observed.
func (b *Builder) loadSources(projects, keys []string) ([]issueSource, *time.Time, error) {
	index := map[string]int{}
	var sources []issueSource
	var watermark time.Time
	scalarFields := ScalarTrackedFields(b.pol)
	membershipFields := b.pol.MembershipFields()

	var scalarFieldOrder []string
	var scalarSelCols []string
	if len(scalarFields) > 0 {
		cols, err := b.scalarColumns(scalarFields)
		if err != nil {
			return nil, nil, err
		}
		for _, f := range scalarFields {
			if col, ok := cols[f]; ok {
				scalarFieldOrder = append(scalarFieldOrder, f)
				scalarSelCols = append(scalarSelCols, `"`+col+`"`)
			}
		}
	}

	// Single-pass SELECT over issues covering base flow fields, membership
	// snapshot fields (sprint_id/sprint_name/components/labels), and all
	// configured scalar columns.
	selCols := []string{
		"key", "project", "type", "created", "status", "status_category",
		"changelog_synced_at", "updated", "resolved", "resolution", "story_points",
	}
	hasMembership := len(membershipFields) > 0
	if hasMembership {
		selCols = append(selCols, "sprint_id", "sprint_name", "components", "labels")
	}
	selCols = append(selCols, scalarSelCols...)
	baseQ := "SELECT " + strings.Join(selCols, ", ") + " FROM issues"

	for _, keyChunk := range keyBatches(keys, maxKeyBindChunk) {
		iq := baseQ
		var iargs []any
		switch {
		case len(keyChunk) > 0:
			iargs, _ = keyFilter(keyChunk, &iq)
		case len(projects) > 0:
			ph := make([]string, len(projects))
			for i, p := range projects {
				ph[i] = "?"
				iargs = append(iargs, p)
			}
			iq += " WHERE project IN (" + strings.Join(ph, ", ") + ")"
		}

		rows, err := b.store.db.ReadDB().Query(iq, iargs...)
		if err != nil {
			return nil, nil, fmt.Errorf("load issues: %w", err)
		}
		for rows.Next() {
			var (
				key, project                      string
				itype, created, status, category  sql.NullString
				synced, updated, resolved, resltn sql.NullString
				storyPoints                       sql.NullFloat64
				sprintID                          sql.NullInt64
				sprintName, comps, labels         sql.NullString
			)
			scalarVals := make([]sql.NullString, len(scalarFieldOrder))
			dest := make([]any, 0, 15+len(scalarFieldOrder))
			dest = append(dest, &key, &project, &itype, &created, &status, &category, &synced, &updated, &resolved, &resltn, &storyPoints)
			if hasMembership {
				dest = append(dest, &sprintID, &sprintName, &comps, &labels)
			}
			for j := range scalarVals {
				dest = append(dest, &scalarVals[j])
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
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
			if storyPoints.Valid {
				v := storyPoints.Float64
				src.estimate = &v
			}
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
				for j, f := range scalarFieldOrder {
					if sh := src.scalar[f]; sh != nil {
						sh.CurrentKnown = true
						sh.CurrentValue = scalarVals[j].String
					}
				}
			}
			if hasMembership {
				src.membership = make(map[string]*MembershipHistory, len(membershipFields))
				for _, f := range membershipFields {
					mh := &MembershipHistory{
						Key:             key,
						Project:         project,
						Field:           f,
						CreatedAt:       src.hist.CreatedAt,
						HistoryComplete: src.hist.HistoryComplete,
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
					src.membership[f] = mh
				}
			}
			index[key] = len(sources)
			sources = append(sources, src)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, nil, err
		}
		rows.Close()
	}

	allFields := make([]string, 0, 1+len(scalarFields)+len(membershipFields))
	allFields = append(allFields, "status")
	allFields = append(allFields, scalarFields...)
	allFields = append(allFields, membershipFields...)
	allFields = dedupStrings(allFields)

	if err := b.loadAllEvents(projects, keys, allFields, index, sources, &watermark); err != nil {
		return nil, nil, err
	}

	var wm *time.Time
	if !watermark.IsZero() {
		wm = &watermark
	}
	return sources, wm, nil
}

// loadAllEvents reads status, scalar, and membership changelog transitions in a
// single SELECT pass over changelog on ReadDB, dispatching each row into the
// matching replay history on sources.
func (b *Builder) loadAllEvents(projects, keys, fields []string, index map[string]int, sources []issueSource, watermark *time.Time) error {
	fph := make([]string, len(fields))
	fargs := make([]any, len(fields))
	for i, f := range fields {
		fph[i] = "?"
		fargs[i] = f
	}
	inFields := strings.Join(fph, ", ")
	chunkSize := max(1, maxKeyBindChunk-len(fields))
	for _, keyChunk := range keyBatches(keys, chunkSize) {
		cargs := make([]any, 0, len(fields)+len(keyChunk)+len(projects))
		cargs = append(cargs, fargs...)
		cq := `SELECT id, issue_key, field, from_value, from_string, to_value, to_string, changed_at
	         FROM changelog WHERE field IN (` + inFields + `)`
		switch {
		case len(keyChunk) > 0:
			args, in := keyFilter(keyChunk, nil)
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
		cq += " ORDER BY issue_key, changed_at"

		crows, err := b.store.db.ReadDB().Query(cq, cargs...)
		if err != nil {
			return fmt.Errorf("load changelog events: %w", err)
		}
		for crows.Next() {
			var (
				id, issueKey, field             string
				fromV, fromS, toV, toS, changed sql.NullString
			)
			if err := crows.Scan(&id, &issueKey, &field, &fromV, &fromS, &toV, &toS, &changed); err != nil {
				crows.Close()
				return err
			}
			i, ok := index[issueKey]
			if !ok {
				continue
			}
			var changedAt time.Time
			if changed.Valid && strings.TrimSpace(changed.String) != "" {
				changedAt = parseDBTime(changed.String)
				if changedAt.After(*watermark) {
					*watermark = changedAt
				}
			}
			if field == "status" {
				sources[i].hist.Events = append(sources[i].hist.Events, StatusEvent{
					ID:         id,
					ChangedAt:  changedAt,
					FromValue:  fromV.String,
					FromString: fromS.String,
					ToValue:    toV.String,
					ToString:   toS.String,
				})
			}
			if sh := sources[i].scalar[field]; sh != nil {
				sh.Events = append(sh.Events, ScalarEvent{
					ID:         id,
					ChangedAt:  changedAt,
					FromValue:  fromV.String,
					FromString: fromS.String,
					ToValue:    toV.String,
					ToString:   toS.String,
				})
			}
			if mh := sources[i].membership[field]; mh != nil {
				mh.Events = append(mh.Events, MembershipEvent{
					ID:         id,
					ChangedAt:  changedAt,
					FromValue:  fromV.String,
					FromString: fromS.String,
					ToValue:    toV.String,
					ToString:   toS.String,
				})
			}
		}
		if err := crows.Err(); err != nil {
			crows.Close()
			return err
		}
		crows.Close()
	}
	return nil
}

// parseJSONArray decodes a denormalized array column (the issues table's components
// and labels, which sync stores as a JSON array of strings) into its member names.
// A null, empty, or non-array value yields nil so callers treat it as an empty set.
func parseJSONArray(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var names []string
	if err := json.Unmarshal([]byte(s), &names); err != nil {
		return nil
	}
	return names
}

// scalarColumns resolves each scalar tracked field to the issues column holding its current
// value, limited to fields whose column actually exists. Resolution is via field_map
// (matching the changelog field name against name or jira_name), falling back to a
// same-named column.
func (b *Builder) scalarColumns(fields []string) (map[string]string, error) {
	existing := map[string]bool{}
	crows, err := b.store.db.ReadDB().Query(`PRAGMA table_info(issues)`)
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
	frows, err := b.store.db.ReadDB().Query(`SELECT name, jira_name FROM field_map WHERE is_column = 1`)
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

const maxKeyBindChunk = 500

// chunkStrings splits ss into non-empty slices of at most size elements.
func chunkStrings(ss []string, size int) [][]string {
	if len(ss) == 0 {
		return nil
	}
	if size <= 0 {
		size = maxKeyBindChunk
	}
	out := make([][]string, 0, (len(ss)+size-1)/size)
	for i := 0; i < len(ss); i += size {
		end := i + size
		if end > len(ss) {
			end = len(ss)
		}
		out = append(out, ss[i:end])
	}
	return out
}

// keyBatches returns chunked slices when keys is non-empty, or a single nil
// sentinel batch when keys is empty so unfiltered/project-scoped callers execute once.
func keyBatches(keys []string, size int) [][]string {
	if len(keys) == 0 {
		return [][]string{nil}
	}
	return chunkStrings(keys, size)
}

// dedupStrings returns unique elements preserving first-seen order.
func dedupStrings(ss []string) []string {
	if len(ss) <= 1 {
		return ss
	}
	seen := make(map[string]struct{}, len(ss))
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
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

func nullFloat64(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}
