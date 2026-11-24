package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/sthadka/jai/v6/pkg/clock"
	"github.com/sthadka/jai/v6/pkg/config"
	"github.com/sthadka/jai/v6/pkg/jira"
	db "github.com/sthadka/jai/v6/pkg/store"
)

// Progress reports sync progress for a project.
// Done=false means an intermediate update; Done=true means the project finished.
// ResumedFrom is non-empty on the first event of a resumed sync, holding the
// cursor timestamp the sync started from.
// JQL is set on the first event for each source, containing the effective JQL query.
type Progress struct {
	Project     string
	New         int
	Updated     int
	Total       int
	Error       error
	Done        bool
	ResumedFrom string
	JQL         string
}

// Engine orchestrates Jira sync operations.
type Engine struct {
	db         db.Store
	client     jira.JiraClient
	cfg        *config.Config
	clk        clock.Clock
	jiraTZMu   sync.RWMutex
	jiraTZ     *time.Location
	verifiedAt time.Time
	mw         *metricWriter
	warnf      Warnf
	onChange   ChangeHook
}

// New creates a new sync Engine with the default system clock.
func New(database db.Store, client jira.JiraClient, cfg *config.Config) *Engine {
	mw := newMetricWriter(database, cfg)
	e := &Engine{db: database, client: client, cfg: cfg, clk: clock.System{}, mw: mw, warnf: defaultWarnf}
	if mw != nil {
		mw.clk = e.clk
	}
	return e
}

// WithClock overrides the clock used for sync timestamps and metric-build
// as-of resolution. It returns the engine for chaining and is primarily a test
// seam for deterministic time.
func (e *Engine) WithClock(c clock.Clock) *Engine {
	if c == nil {
		c = clock.System{}
	}
	e.clk = c
	if e.mw != nil {
		e.mw.clk = c
	}
	return e
}

// WithWarnf overrides where recoverable warnings go (default: stderr). Returns
// the engine for chaining; embedders use it to capture or silence engine
// warnings. A nil sink restores the stderr default.
func (e *Engine) WithWarnf(w Warnf) *Engine {
	if w == nil {
		w = defaultWarnf
	}
	e.warnf = w
	return e
}

// now returns the engine's current time.
func (e *Engine) now() time.Time { return e.clk.Now() }

// concurrency returns the configured parallel-fetch worker count, defaulting to
// 8 when unset. The client's shared rate limiter still caps total request rate.
func (e *Engine) concurrency() int {
	if e.cfg.Sync.Concurrency < 1 {
		return 8
	}
	return e.cfg.Sync.Concurrency
}

// VerifyAuth confirms the configured credentials actually authenticate against
// Jira and caches the user's timezone for the next sync. The /myself endpoint
// requires authentication; Jira Cloud can return HTTP 200 with
// anonymous — and completely different or empty — results for search and field
// endpoints when credentials are missing or invalid, so an empty/odd sync could
// otherwise silently overwrite the local database with the wrong data. On
// failure the returned error wraps *jira.AuthError, which callers detect with
// errors.As to fail fast instead of proceeding.
func (e *Engine) VerifyAuth(ctx context.Context) error {
	me, err := e.client.MySelf(ctx)
	if err != nil {
		return fmt.Errorf("jira authentication check failed: %w", err)
	}

	loc := time.UTC
	if me.TimeZone == "" {
		e.warnf("warning: Jira user timezone is unavailable; falling back to UTC\n")
	} else {
		resolved, err := time.LoadLocation(me.TimeZone)
		if err != nil {
			e.warnf("warning: could not resolve Jira timezone %q; falling back to UTC: %v\n", me.TimeZone, err)
		} else {
			loc = resolved
		}
	}

	e.jiraTZMu.Lock()
	e.jiraTZ = loc
	e.verifiedAt = e.now()
	e.jiraTZMu.Unlock()
	return nil
}

// SetTimezone initializes the Jira user timezone directly without calling
// /rest/api/3/myself (e.g. when the caller already verified credentials and
// knows the authenticated user's timezone). A nil location defaults to UTC.
func (e *Engine) SetTimezone(loc *time.Location) {
	if loc == nil {
		loc = time.UTC
	}
	e.jiraTZMu.Lock()
	e.jiraTZ = loc
	e.verifiedAt = e.now()
	e.jiraTZMu.Unlock()
}

// InvalidateAuth clears the cached VerifyAuth timestamp so the next
// VerifyAuthIfStale call re-authenticates against /rest/api/3/myself.
func (e *Engine) InvalidateAuth() {
	e.jiraTZMu.Lock()
	e.verifiedAt = time.Time{}
	e.jiraTZMu.Unlock()
}

// VerifyAuthIfStale runs VerifyAuth only if the engine has not been verified
// within ttl (or has never been verified). A zero or negative ttl always
// verifies. Use this between frequent delta/background ticks to avoid redundant
// /rest/api/3/myself round-trips while keeping fresh VerifyAuth for full syncs
// and deletion scans.
func (e *Engine) VerifyAuthIfStale(ctx context.Context, ttl time.Duration) error {
	if ttl > 0 {
		e.jiraTZMu.RLock()
		fresh := e.jiraTZ != nil && !e.verifiedAt.IsZero() && e.now().Sub(e.verifiedAt) < ttl
		e.jiraTZMu.RUnlock()
		if fresh {
			return nil
		}
	}
	return e.VerifyAuth(ctx)
}

func (e *Engine) jiraTimezone() *time.Location {
	e.jiraTZMu.RLock()
	defer e.jiraTZMu.RUnlock()
	return e.jiraTZ
}

// SyncProjects fetches display names for every distinct project key in the
// issues table and stores them in the projects table. Batches up to 50 keys
// per GET /rest/api/3/project/search call (falling back to GetProject if the
// search endpoint fails). Non-fatal: failures are logged via warnf.
func (e *Engine) SyncProjects(ctx context.Context) {
	rows, err := e.db.ReadDB().Query(`SELECT DISTINCT project FROM issues WHERE project != ''`)
	if err != nil {
		return
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err == nil && k != "" {
			keys = append(keys, k)
		}
	}
	rows.Close()
	if len(keys) == 0 {
		return
	}

	const batchSize = 50
	for i := 0; i < len(keys); i += batchSize {
		end := i + batchSize
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[i:end]

		resp, err := e.client.SearchProjects(ctx, jira.ProjectSearchOptions{
			Keys:       batch,
			MaxResults: batchSize,
		})
		if err != nil {
			// Fallback to per-project GET if /project/search is unavailable.
			for _, k := range batch {
				p, perr := e.client.GetProject(ctx, k)
				if perr != nil {
					e.warnf("warning: fetching project %s: %v\n", k, perr)
					continue
				}
				if uerr := e.db.UpsertProject(p.Key, p.Name); uerr != nil {
					e.warnf("warning: storing project %s: %v\n", k, uerr)
				}
			}
			continue
		}
		for _, p := range resp.Values {
			if uerr := e.db.UpsertProject(p.Key, p.Name); uerr != nil {
				e.warnf("warning: storing project %s: %v\n", p.Key, uerr)
			}
		}
	}
}

// DiscoverFields fetches field metadata from Jira and populates field_map.
func (e *Engine) DiscoverFields(ctx context.Context, overrides map[string]string) error {
	fields, err := e.client.Fields(ctx)
	if err != nil {
		return fmt.Errorf("fetching fields: %w", err)
	}

	// Pre-load existing name→jiraID mapping so we can detect true collisions
	// (same name owned by a different field) and disambiguate rather than skip.
	existing, _ := e.db.FieldMapByJiraID()
	takenNames := make(map[string]string, len(existing)) // name → jiraID
	for id, f := range existing {
		takenNames[f.Name] = id
	}

	for _, f := range fields {
		if f.Schema == nil {
			continue
		}

		// If this field already has a stored name, reuse it to avoid
		// re-triggering collision warnings on every sync.
		name := ""
		if em, ok := existing[f.ID]; ok {
			name = em.Name
		}
		if overrideName, ok := overrides[f.ID]; ok {
			name = overrideName
		}
		if name == "" {
			name = inferColumnName(f, overrides)
		}
		fieldType := jiraSchemaToType(f.Schema)

		// If this name is already owned by a different field, append a suffix
		// derived from the jira_id to make it unique.
		if ownerID, taken := takenNames[name]; taken && ownerID != f.ID {
			suffix := f.ID
			suffix = strings.TrimPrefix(suffix, "customfield_")
			e.warnf("warning: field %q (%s) renamed to %s_%s (name collision with %s)\n",
				f.Name, f.ID, name, suffix, ownerID)
			name = name + "_" + suffix
		}
		takenNames[name] = f.ID

		mapping := &db.FieldMapping{
			JiraID:     f.ID,
			JiraName:   f.Name,
			Name:       name,
			Type:       fieldType,
			ItemType:   jiraSchemaItemType(f.Schema),
			IsCustom:   f.Custom,
			IsColumn:   false,
			Searchable: fieldType == "text" || fieldType == "array",
		}
		if _, overridden := overrides[f.ID]; overridden {
			mapping.UserOverride = true
		}

		if err := e.db.UpsertFieldMapping(mapping); err != nil {
			// Non-fatal: log and continue so one bad field doesn't abort discovery.
			e.warnf("warning: skipping field %s (%s): %v\n", f.ID, name, err)
			continue
		}
	}
	return nil
}

// effectiveSources returns the sync sources to run, optionally filtered to a
// single source by name.
func effectiveSources(cfg *config.Config, filter string) ([]config.SyncSource, error) {
	sources := cfg.SyncSources
	if filter == "" {
		return sources, nil
	}
	for _, s := range sources {
		if s.Name == filter {
			return []config.SyncSource{s}, nil
		}
	}
	return nil, fmt.Errorf("sync source %q not found", filter)
}

// FullSyncOverdue returns the names of sources whose last full sync is missing
// or older than sync.full_sync_warning_age (default 24h). An empty result means
// no source is overdue. sourceFilter, when non-empty, restricts the check to
// that single source. The reconcile pass only refreshes configured fields, so a
// periodic full sync is what reconciles everything else — this drives the
// reminder shown by both the CLI and MCP.
func (e *Engine) FullSyncOverdue(sourceFilter string) []string {
	age := 24 * time.Hour
	if e.cfg.Sync.FullSyncWarningAge != "" {
		if d, err := time.ParseDuration(e.cfg.Sync.FullSyncWarningAge); err == nil {
			age = d
		}
	}

	// Only currently-configured sources can be "overdue". A source removed from
	// config leaves its sync_metadata row behind; that orphan must never keep
	// warning forever (run 'jai sync prune' to delete it).
	configured := make(map[string]bool, len(e.cfg.SyncSources))
	for _, s := range e.cfg.SyncSources {
		configured[s.Name] = true
	}

	metas, err := e.db.AllSyncMeta()
	if err != nil {
		return nil
	}
	cutoff := e.now().Add(-age)
	var stale []string
	for _, m := range metas {
		if !configured[m.Project] {
			continue
		}
		if sourceFilter != "" && m.Project != sourceFilter {
			continue
		}
		if !m.LastFullSync.Valid || m.LastFullSync.String == "" {
			stale = append(stale, m.Project)
			continue
		}
		t, err := time.Parse(time.RFC3339, m.LastFullSync.String)
		if err != nil || t.Before(cutoff) {
			stale = append(stale, m.Project)
		}
	}
	return stale
}

// sourceJQL builds the base JQL for a SyncSource.
func sourceJQL(s config.SyncSource) string {
	if s.JQL != "" {
		return s.JQL
	}
	quoted := make([]string, len(s.Projects))
	for i, p := range s.Projects {
		quoted[i] = `"` + p + `"`
	}
	return `project in (` + strings.Join(quoted, ", ") + `)`
}

// Sync runs a sync for all configured sources (or a single named one).
// It sends intermediate Progress updates (Done=false) as pages arrive,
// and a final Progress (Done=true) when each source finishes.
// The channel is closed when all sources are done.
// resume is only meaningful when full=true: if true, the sync continues from
// the last saved cursor instead of starting over.
// VerifyAuth must succeed before Sync is called.
func (e *Engine) Sync(ctx context.Context, full, resume bool, sourceFilter string) (<-chan Progress, error) {
	if resume && !full {
		return nil, fmt.Errorf("--resume requires --full")
	}

	sources, err := effectiveSources(e.cfg, sourceFilter)
	if err != nil {
		return nil, err
	}

	jiraTZ := e.jiraTimezone()
	if jiraTZ == nil {
		return nil, fmt.Errorf("jira timezone is not initialized; call VerifyAuth before Sync")
	}

	ch := make(chan Progress, 64)
	go func() {
		defer close(ch)
		if err := e.ensurePromotedFields(); err != nil {
			ch <- Progress{Error: fmt.Errorf("promoting configured fields: %w", err), Done: true}
			return
		}
		// Sources are independent (separate cursors, separate JQL); run them
		// concurrently, bounded by the configured worker count. The client's
		// shared rate limiter still caps total request rate, and DB writes
		// serialize on the single connection.
		sem := make(chan struct{}, e.concurrency())
		var wg sync.WaitGroup
		for _, src := range sources {
			wg.Add(1)
			sem <- struct{}{}
			go func(src config.SyncSource) {
				defer wg.Done()
				defer func() { <-sem }()
				e.syncSource(ctx, src, full, resume, jiraTZ, ch)
			}(src)
		}
		wg.Wait()
		if e.db != nil {
			_ = e.db.Optimize()
		}
	}()
	return ch, nil
}

// ensurePromotedFields promotes the custom fields named in sync.promote_fields and
// metrics.tracked_fields from the custom_fields JSON into typed columns, so they
// are queried/sorted efficiently. Promotion is idempotent and backfills the column
// from existing JSON; unknown, system (already columned), or already-promoted
// fields are skipped. Runs once per sync before any source denormalizes, so the
// in-run field map sees is_column and writes the column directly.
func (e *Engine) ensurePromotedFields() error {
	names := append([]string(nil), e.cfg.Sync.PromoteFields...)
	if e.cfg.Metrics != nil {
		names = append(names, e.cfg.Metrics.TrackedFields...)
	}
	if len(names) == 0 {
		return nil
	}
	mappings, err := e.db.AllFieldMappings()
	if err != nil {
		return err
	}
	byName := make(map[string]*db.FieldMapping, len(mappings)*2)
	for _, m := range mappings {
		byName[strings.ToLower(m.Name)] = m
		if m.JiraName != "" {
			byName[strings.ToLower(m.JiraName)] = m
		}
	}
	for _, n := range names {
		m, ok := byName[strings.ToLower(strings.TrimSpace(n))]
		if !ok || !m.IsCustom || m.IsColumn {
			continue
		}
		if err := e.db.PromoteField(m.JiraID, m.Name, sqliteColumnType(m.Type)); err != nil {
			return fmt.Errorf("promoting field %s: %w", m.Name, err)
		}
	}
	return nil
}

// sqliteColumnType maps a field_map type to the SQLite column type for a promoted
// custom-field column.
func sqliteColumnType(t string) string {
	switch strings.ToLower(t) {
	case "number":
		return "REAL"
	case "date", "datetime":
		return "DATETIME"
	default:
		return "TEXT"
	}
}

func (e *Engine) syncSource(ctx context.Context, src config.SyncSource, full, resume bool, jiraTZ *time.Location, ch chan<- Progress) {
	start := e.now()

	fieldMap, err := e.db.FieldMapByJiraID()
	if err != nil {
		ch <- Progress{Project: src.Name, Error: fmt.Errorf("loading field map: %w", err), Done: true}
		return
	}
	base := sourceJQL(src)
	var jql string
	var resumedFrom string

	if full {
		if resume {
			cursor, err := e.db.GetResumeCursor(src.Name)
			if err == nil && cursor != "" {
				resumedFrom = cursor
				jqlTime := cursorToJQL(cursor, jiraTZ)
				jql = fmt.Sprintf(`(%s) AND updated >= "%s" ORDER BY updated ASC`, base, jqlTime)
			} else {
				jql = base + ` ORDER BY updated ASC`
			}
		} else {
			// Fresh full sync: discard any stale cursor.
			_ = e.db.ClearResumeCursor(src.Name)
			jql = base + ` ORDER BY updated ASC`
		}
	} else {
		meta, err := e.db.GetSyncMeta(src.Name)
		if err != nil {
			ch <- Progress{Project: src.Name, Error: fmt.Errorf("loading sync meta: %w", err), Done: true}
			return
		}
		// Prefer the high-water mark (max updated of synced issues) over last_sync_time.
		// last_sync_time records when the sync ran, not the freshness of the data.
		hwm := meta.LastIssueUpdated.String
		if !meta.LastIssueUpdated.Valid || hwm == "" {
			hwm = meta.LastSyncTime.String
		}
		if hwm != "" {
			// Incremental sync with an existing high-water mark: run the two-stage
			// cheap delta (Stage 1: fields=["updated"] maxResults=5000 → batch
			// SQLite diff → Stage 2: BulkFetchIssues for changed IDs only).
			lookback := parseLookbackDuration(e.cfg.Sync.LookbackWindow)
			fields := e.expandFields(fieldMap)
			res, derr := e.DeltaSync(ctx, DeltaOptions{
				ScopeName: src.Name,
				JQL:       base,
				Lookback:  lookback,
				Since:     hwm,
				Fields:    fields,
			})
			if res != nil && res.JQL != "" {
				ch <- Progress{Project: src.Name, JQL: res.JQL}
			}
			if derr != nil {
				probed := 0
				if res != nil {
					probed = res.Probed
				}
				ch <- Progress{Project: src.Name, Total: probed, Error: derr, Done: true}
				return
			}
			ch <- Progress{
				Project: src.Name,
				New:     res.New,
				Updated: res.Updated,
				Total:   res.Probed,
				Done:    true,
			}
			return
		}
		jql = base + ` ORDER BY updated DESC`
	}

	fields := e.expandFields(fieldMap)
	var newCount, updatedCount, total int
	var lastUpdated string   // max updated timestamp seen this run (for cursor)
	var touchedKeys []string // issue keys upserted this run, for incremental metric refresh

	// Emit an initial event so the display knows we're resuming and carries the JQL.
	ch <- Progress{Project: src.Name, ResumedFrom: resumedFrom, JQL: jql}

	for page, err := range e.client.SearchAll(ctx, jql, fields) {
		if err != nil {
			// Save cursor so the next --resume can pick up here.
			if full && lastUpdated != "" {
				_ = e.db.SetResumeCursor(src.Name, lastUpdated)
			}
			// Only a full sync advances the high-water mark on failure: it walks
			// updated ASC, so lastUpdated is a true floor — everything below it
			// has been seen. An incremental sync walks DESC, so lastUpdated is
			// the newest issue, not a floor; advancing to it would strand every
			// older unsynced issue in the window. Leave the mark put and let the
			// next run re-scan.
			hwm := ""
			if full {
				hwm = lastUpdated
			}
			elapsed := time.Since(start).Seconds()
			_ = e.db.UpdateSyncMeta(src.Name, elapsed, total, newCount+updatedCount, err.Error(), hwm)
			ch <- Progress{Project: src.Name, New: newCount, Updated: updatedCount, Total: total, Error: err, Done: true}
			return
		}

		pageKeys := make([]string, 0, len(page))
		for _, apiIssue := range page {
			if apiIssue.Key != "" {
				pageKeys = append(pageKeys, apiIssue.Key)
			}
		}
		storedByKey, _ := e.db.GetIssuesUpdatedByKey(pageKeys)

		bundles := make([]*db.IssueBundle, 0, len(page))
		for _, apiIssue := range page {
			total++
			rawBytes, err := json.Marshal(apiIssue)
			if err != nil {
				continue
			}

			issue, extra, err := Denormalize(rawBytes, fieldMap)
			if err != nil {
				continue
			}

			// Track max updated for resume cursor regardless of skip.
			if issue.Updated > lastUpdated {
				lastUpdated = issue.Updated
			}

			existingUpdated := storedByKey[apiIssue.Key]
			if !full {
				if existingUpdated == "" {
					newCount++
				} else if existingUpdated == issue.Updated {
					continue
				} else {
					updatedCount++
				}
			} else {
				// A full sync must NOT skip unchanged-"updated" issues: Jira does not
				// bump "updated" for silent changes like LexoRank rebalances.
				if existingUpdated == "" {
					newCount++
				} else if existingUpdated != issue.Updated {
					updatedCount++
				}
			}

			b := &db.IssueBundle{
				Issue:       issue,
				Extra:       extra,
				Links:       ExtractIssueLinks(apiIssue.Key, rawBytes),
				UpdateLinks: true,
			}
			if comments, err := ExtractComments(apiIssue.Key, rawBytes); err == nil {
				b.Comments = comments
				b.UpdateCommentsText = len(comments) > 0
			}
			if attachments, err := ExtractAttachments(apiIssue.Key, rawBytes); err == nil && attachments != nil {
				b.Attachments = attachments
				b.UpdateAttachments = true
			}
			bundles = append(bundles, b)
		}

		if len(bundles) > 0 {
			if err := e.db.UpsertIssueBundleTx(bundles); err != nil {
				// Fall back to per-bundle commits so one malformed row never drops a page.
				for _, b := range bundles {
					if berr := e.db.UpsertIssueBundleTx([]*db.IssueBundle{b}); berr == nil {
						touchedKeys = append(touchedKeys, b.Issue.Key)
					}
				}
			} else {
				for _, b := range bundles {
					touchedKeys = append(touchedKeys, b.Issue.Key)
				}
			}
		}
		// Save cursor after each completed page so a restart can skip ahead.
		if full && lastUpdated != "" {
			_ = e.db.SetResumeCursor(src.Name, lastUpdated)
		}

		// Emit an intermediate update after each page.
		ch <- Progress{Project: src.Name, New: newCount, Updated: updatedCount, Total: total}
	}

	// Sync completed successfully — clear the resume cursor.
	if full {
		_ = e.db.ClearResumeCursor(src.Name)
	}

	// Run deletion detection on full sync for project-keyed sources only.
	// JQL sources have no reliable scope to diff against. Deleted keys join the
	// touched set so their derived facts are purged in the same metric refresh.
	if full && src.JQL == "" {
		for _, project := range src.Projects {
			deleted, err := DetectDeletedKeys(ctx, e.db, e.client, project)
			if err != nil {
				_ = err // non-fatal
			}
			touchedKeys = append(touchedKeys, deleted...)
		}
	}

	// Record the full-sync timestamp for every source (JQL sources included) so
	// the "full sync overdue" reminder can reason about staleness. Previously
	// this only ran for project-keyed sources, leaving last_full_sync NULL for
	// JQL-based configs.
	if full {
		_ = e.db.UpdateFullSyncMeta(src.Name)
	}

	// Sync project names so the TUI breadcrumb can show display names.
	if full {
		e.SyncProjects(ctx)
	}

	// Sync sprint and board data if configured.
	if e.cfg.Sync.Sprints && full {
		e.SyncSprints(ctx, src)
	}

	// Sync development info if configured.
	if e.cfg.Sync.DevInfo && full {
		e.SyncDevInfo(ctx, src)
	}

	// Advance the flow-metric facts for the issues this source touched (and any
	// detected as deleted), holding the cross-process lock and keeping facts and
	// rollup slices in one transaction. Incremental by default; a config/mapping
	// change forces a full rebuild. Metric publication is separate from source-sync
	// success: a build failure is logged but does not mark the source sync failed,
	// so a rejected metric never makes source-only sync unusable, and a prior valid
	// generation stays published with honest freshness. No-op when metrics are
	// unconfigured.
	if err := e.mw.refreshTouched(touchedKeys); err != nil {
		e.warnf("warning: metric refresh for %s failed: %v\n", src.Name, err)
	}

	elapsed := time.Since(start).Seconds()
	_ = e.db.UpdateSyncMeta(src.Name, elapsed, total, newCount+updatedCount, "", lastUpdated)

	e.emitChange(ChangeEvent{Source: src.Name, New: newCount, Updated: updatedCount, Total: total})
	ch <- Progress{Project: src.Name, New: newCount, Updated: updatedCount, Total: total, Done: true}
}

// ChangelogProgress reports changelog sync progress.
type ChangelogProgress struct {
	Total   int
	Synced  int
	Skipped int
	Error   error
	Done    bool
}

// ChangelogSyncOptions configures Engine.SyncChangelogsWithOptions.
type ChangelogSyncOptions struct {
	// FieldIDs optionally restricts bulk changelog fetch to at most 10 Jira
	// field IDs. Empty fetches complete unfiltered changelog history and stamps
	// both changelog_synced_at and changelog_full_synced_at.
	FieldIDs []string

	// Force resets all changelog_synced_at / changelog_full_synced_at timestamps
	// before fetching.
	Force bool
}

// SyncChangelogs fetches changelogs for issues that need them using
// cfg.Sync.ChangelogFields (empty = unfiltered by default).
// Uses the bulk changelog API (up to 1,000 issues per request, maxResults=10000)
// with automatic fallback to per-issue fetching if the bulk endpoint is unavailable.
// If force is true, resets all changelog sync timestamps first to trigger a full re-sync.
func (e *Engine) SyncChangelogs(ctx context.Context, sourceFilter string, force bool) (<-chan ChangelogProgress, error) {
	var fieldIDs []string
	if e.cfg != nil {
		fieldIDs = e.cfg.Sync.ChangelogFields
	}
	return e.SyncChangelogsWithOptions(ctx, sourceFilter, ChangelogSyncOptions{
		FieldIDs: fieldIDs,
		Force:    force,
	})
}

// SyncChangelogsWithOptions fetches changelogs for issues needing sync with
// explicit control over fieldIds filtering and force reset.
func (e *Engine) SyncChangelogsWithOptions(ctx context.Context, sourceFilter string, opts ChangelogSyncOptions) (<-chan ChangelogProgress, error) {
	if len(opts.FieldIDs) > jira.BulkChangelogMaxFieldIDs {
		return nil, fmt.Errorf("sync changelogs: at most %d fieldIds allowed, got %d", jira.BulkChangelogMaxFieldIDs, len(opts.FieldIDs))
	}
	if opts.Force {
		if _, err := e.db.ResetChangelogSyncTimestamps(); err != nil {
			return nil, fmt.Errorf("resetting changelog timestamps: %w", err)
		}
	}

	var projectFilter []string
	if sourceFilter != "" {
		sources, err := effectiveSources(e.cfg, sourceFilter)
		if err != nil {
			return nil, err
		}
		if len(sources) == 1 {
			projectFilter = sources[0].Projects
		}
	}

	candidates, err := e.db.GetChangelogSyncCandidates(projectFilter)
	if err != nil {
		return nil, fmt.Errorf("getting changelog candidates: %w", err)
	}

	bulkOpts := jira.BulkChangelogOptions{FieldIDs: opts.FieldIDs}
	isFullHistory := len(opts.FieldIDs) == 0

	ch := make(chan ChangelogProgress, 64)
	go func() {
		defer close(ch)

		total := len(candidates)
		synced := 0
		skipped := 0

		ch <- ChangelogProgress{Total: total}

		if total == 0 {
			ch <- ChangelogProgress{Total: 0, Done: true}
			return
		}

		idToKey, err := e.db.GetIssueIDToKeyMap()
		if err != nil {
			idToKey = nil
		}

		// Split candidates: those with known IDs use bulk, the rest use per-issue.
		var bulkKeys, fallbackKeys []string
		keyToID := make(map[string]bool)
		for _, key := range idToKey {
			keyToID[key] = true
		}
		for _, key := range candidates {
			if keyToID[key] {
				bulkKeys = append(bulkKeys, key)
			} else {
				fallbackKeys = append(fallbackKeys, key)
			}
		}

		// Bulk fetch in batches of up to 1,000 issues (maxResults=10,000),
		// parallelized across workers. The client's shared rate limiter still
		// bounds total API throughput.
		bulkFailed := false
		const batchSize = jira.BulkChangelogMaxIssues
		if len(bulkKeys) > 0 {
			var batches [][]string
			for i := 0; i < len(bulkKeys); i += batchSize {
				end := i + batchSize
				if end > len(bulkKeys) {
					end = len(bulkKeys)
				}
				batches = append(batches, bulkKeys[i:end])
			}

			var mu sync.Mutex
			emit := func() { ch <- ChangelogProgress{Total: total, Synced: synced, Skipped: skipped} }
			store := func(batch []string, entries []jira.BulkChangelogEntry) {
				dbEntries := ExtractBulkChangelog(entries, idToKey)
				if err := e.db.InsertChangelogBatch(dbEntries); err != nil {
					mu.Lock()
					skipped += len(batch)
					emit()
					mu.Unlock()
					return
				}
				if isFullHistory {
					_ = e.db.MarkChangelogFullSynced(batch)
				} else {
					_ = e.db.MarkChangelogSynced(batch)
				}
				mu.Lock()
				synced += len(batch)
				emit()
				mu.Unlock()
			}
			process := func(batch []string) {
				entries, err := e.client.BulkFetchChangelogsWithOptions(ctx, batch, bulkOpts)
				if err != nil {
					mu.Lock()
					skipped += len(batch)
					emit()
					mu.Unlock()
					return
				}
				store(batch, entries)
			}

			// Fetch the first batch synchronously: a failure here means the bulk
			// endpoint is unavailable, so fall back to per-issue for everything
			// rather than firing every batch at a doomed call.
			entries, err := e.client.BulkFetchChangelogsWithOptions(ctx, batches[0], bulkOpts)
			if err != nil {
				bulkFailed = true
				fallbackKeys = append(fallbackKeys, bulkKeys...)
			} else {
				store(batches[0], entries)
				// Fan the remaining batches across a bounded worker pool.
				jobs := make(chan []string)
				var wg sync.WaitGroup
				for range e.concurrency() {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for batch := range jobs {
							process(batch)
						}
					}()
				}
			feed:
				for _, batch := range batches[1:] {
					select {
					case <-ctx.Done():
						break feed
					case jobs <- batch:
					}
				}
				close(jobs)
				wg.Wait()
			}
		}

		if ctx.Err() != nil {
			ch <- ChangelogProgress{Total: total, Synced: synced, Skipped: skipped, Error: ctx.Err(), Done: true}
			return
		}

		if bulkFailed {
			e.warnf("  ⚠ bulk changelog API unavailable, falling back to per-issue fetch\n")
		}

		// Per-issue fallback for issues without ID mapping or if bulk failed.
		for _, key := range fallbackKeys {
			select {
			case <-ctx.Done():
				ch <- ChangelogProgress{Total: total, Synced: synced, Skipped: skipped, Error: ctx.Err(), Done: true}
				return
			default:
			}

			resp, err := e.client.GetIssueChangelog(ctx, key)
			if err != nil {
				skipped++
				ch <- ChangelogProgress{Total: total, Synced: synced, Skipped: skipped}
				continue
			}

			dbEntries := ExtractChangelog(key, resp)
			for _, entry := range dbEntries {
				_ = e.db.InsertChangelog(entry)
			}
			_ = e.db.MarkChangelogFullSynced([]string{key})
			synced++

			if synced%10 == 0 {
				ch <- ChangelogProgress{Total: total, Synced: synced, Skipped: skipped}
			}
		}

		ch <- ChangelogProgress{Total: total, Synced: synced, Skipped: skipped, Done: true}
	}()

	return ch, nil
}

// syncChangelogsForKeys fetches and stores changelogs for the given issue keys.
// Called inline during issue sync for newly upserted issues. Errors are non-fatal.
func (e *Engine) syncChangelogsForKeys(ctx context.Context, keys []string) {
	var fieldIDs []string
	if e.cfg != nil {
		fieldIDs = e.cfg.Sync.ChangelogFields
	}
	e.syncChangelogsForKeysWithOptions(ctx, keys, fieldIDs)
}

// syncChangelogsForKeysWithOptions fetches and stores changelogs for the given
// issue keys using bulk batches of up to 1,000 issues and optional fieldIds.
// Errors are non-fatal and fall back to per-issue fetch.
func (e *Engine) syncChangelogsForKeysWithOptions(ctx context.Context, keys []string, fieldIDs []string) {
	if len(keys) == 0 {
		return
	}

	idToKey, err := e.db.GetIssueIDToKeyMapForKeys(keys)
	if err != nil || len(idToKey) == 0 {
		e.syncChangelogsPerIssue(ctx, keys)
		return
	}

	bulkOpts := jira.BulkChangelogOptions{FieldIDs: fieldIDs}
	isFullHistory := len(fieldIDs) == 0

	for i := 0; i < len(keys); i += jira.BulkChangelogMaxIssues {
		end := i + jira.BulkChangelogMaxIssues
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[i:end]
		entries, err := e.client.BulkFetchChangelogsWithOptions(ctx, batch, bulkOpts)
		if err != nil {
			e.syncChangelogsPerIssue(ctx, batch)
			continue
		}
		dbEntries := ExtractBulkChangelog(entries, idToKey)
		_ = e.db.InsertChangelogBatch(dbEntries)
		if isFullHistory {
			_ = e.db.MarkChangelogFullSynced(batch)
		} else {
			_ = e.db.MarkChangelogSynced(batch)
		}
	}
}

// syncChangelogsPerIssue fetches changelogs one issue at a time, used as a
// fallback when bulk fetch is unavailable or an issue has no ID mapping yet.
func (e *Engine) syncChangelogsPerIssue(ctx context.Context, keys []string) {
	var synced []string
	for _, key := range keys {
		resp, err := e.client.GetIssueChangelog(ctx, key)
		if err != nil {
			continue
		}
		entries := ExtractChangelog(key, resp)
		if err := e.db.InsertChangelogBatch(entries); err != nil {
			continue
		}
		synced = append(synced, key)
	}
	if len(synced) > 0 {
		_ = e.db.MarkChangelogFullSynced(synced)
	}
}

// cursorToJQL converts an RFC3339 timestamp to the format Jira JQL expects,
// expressed in the given timezone. Jira Cloud interprets JQL datetime literals
// in the authenticated user's timezone, so we must convert the UTC cursor to
// that timezone — otherwise the query threshold silently shifts by the UTC
// offset, creating a blind window where updated issues are missed.
// Jira JQL does not support seconds — they cause a silent 0-result response.
// We truncate to minute granularity, which may re-fetch up to 60 seconds of
// already-synced issues (harmless upserts).
func cursorToJQL(cursor string, loc *time.Location) string {
	t, err := time.Parse(time.RFC3339, cursor)
	if err != nil {
		return cursor
	}
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02 15:04")
}

// defaultLookbackWindow is the incremental re-scan overlap applied below the
// high-water mark when none is configured. It covers Jira's "updated" search
// eventual-consistency lag so a change landing just under the mark is re-scanned
// rather than orphaned.
const defaultLookbackWindow = time.Hour

// applyLookback shifts an RFC3339 high-water mark earlier by the configured
// lookback window, widening the incremental query so boundary changes are
// re-scanned. window is a Go duration string (e.g. "1h", "24h"); empty or
// unparseable falls back to defaultLookbackWindow rather than dropping the
// overlap. Returns the shifted RFC3339 timestamp, or the input unchanged if it
// cannot be parsed.
func parseLookbackDuration(window string) time.Duration {
	if window != "" {
		if parsed, err := time.ParseDuration(window); err == nil && parsed >= 0 {
			return parsed
		}
	}
	return defaultLookbackWindow
}

func applyLookback(hwm, window string) string {
	d := parseLookbackDuration(window)
	t, err := time.Parse(time.RFC3339, hwm)
	if err != nil {
		return hwm
	}
	return t.Add(-d).UTC().Format(time.RFC3339)
}

func (e *Engine) expandFields(fieldMap map[string]*db.FieldMapping) []string {
	if e.cfg != nil && strings.EqualFold(e.cfg.Sync.FieldProjection, "slim") {
		return ExpandFieldsSlim(fieldMap, e.configuredCustomFieldIDs(fieldMap))
	}
	return ExpandFieldsFull(fieldMap)
}

// inferColumnName determines the DB column name for a Jira field.
func inferColumnName(f *jira.Field, overrides map[string]string) string {
	if name, ok := overrides[f.ID]; ok {
		return name
	}
	// Slugify: "Custom Team Field" → "custom_team_field"
	name := strings.ToLower(f.Name)
	re := regexp.MustCompile(`[^a-z0-9]+`)
	name = re.ReplaceAllString(name, "_")
	name = strings.Trim(name, "_")
	if name == "" {
		name = strings.ToLower(f.ID)
	}
	// SQLite identifiers cannot start with a digit.
	if len(name) > 0 && name[0] >= '0' && name[0] <= '9' {
		name = "_" + name
	}
	return name
}

// jiraSchemaToType maps Jira field schema to our internal type name.
func jiraSchemaToType(schema *jira.FieldSchema) string {
	if jira.SchemaIsADF(schema) {
		return "richtext"
	}
	switch schema.Type {
	case "number":
		return "number"
	case "date":
		return "date"
	case "datetime":
		return "datetime"
	case "array":
		return "array"
	case "option":
		return "option"
	case "user":
		return "user"
	default:
		return "text"
	}
}

// jiraSchemaItemType returns the array element sub-type (schema.items) for array
// fields, e.g. "user" for Contributors. It is empty for non-array fields. The
// write path (jai set / jai_set) uses this to wrap array elements in the object
// shape Jira requires (user → {"accountId":…}, component/version → {"name":…}).
func jiraSchemaItemType(schema *jira.FieldSchema) string {
	if schema == nil || schema.Type != "array" {
		return ""
	}
	return schema.Items
}
