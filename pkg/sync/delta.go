package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sthadka/jai/v6/pkg/jira"
	db "github.com/sthadka/jai/v6/pkg/store"
)

// defaultDeltaLookback is the default sliding overlap below the high-water mark
// for two-stage DeltaSync probes. Two minutes covers Jira Cloud's typical
// search-index propagation lag while keeping idle delta probes at ~1 API point.
const defaultDeltaLookback = 2 * time.Minute

// coreScalarFields are the standard Jira system fields always requested by both
// slim and full projections. Comments are included in the primary pass so each
// issue costs 1 point total (splitting comments into a second pass would cost
// 2 points/issue under Jira Cloud's per-object points model).
var coreScalarFields = []string{
	"summary", "description", "status", "priority", "assignee", "reporter",
	"created", "updated", "resolutiondate", "labels", "components", "fixVersions",
	"parent", "issuetype", "project", "comment",
	"issuelinks", "resolution", "duedate",
	"timeoriginalestimate", "timespent", "timeestimate",
	"subtasks",
	"creator", "environment", "security", "votes", "watches",
	"workratio", "versions", "statuscategorychangedate",
}

// ExpandFieldsFull returns the full field list: core system fields, comment,
// attachment, and every discovered custom field in fieldMap.
func ExpandFieldsFull(fieldMap map[string]*db.FieldMapping) []string {
	fields := make([]string, 0, len(coreScalarFields)+1+len(fieldMap))
	fields = append(fields, coreScalarFields...)
	fields = append(fields, "attachment")
	var customIDs []string
	for jiraID, f := range fieldMap {
		if f.IsCustom {
			customIDs = append(customIDs, jiraID)
		}
	}
	sort.Strings(customIDs)
	return append(fields, customIDs...)
}

// ExpandFieldsSlim returns the explicit primary field list: core system fields,
// comment, promoted custom fields (f.IsColumn), and any extraCustomIDs.
// Excludes worklog, attachment, and unconfigured custom fields so background
// syncs avoid heavy payload bloat while keeping comments and promoted columns
// fresh in a single pass.
func ExpandFieldsSlim(fieldMap map[string]*db.FieldMapping, extraCustomIDs []string) []string {
	seen := make(map[string]struct{}, len(coreScalarFields)+len(extraCustomIDs)+8)
	fields := make([]string, 0, len(coreScalarFields)+len(extraCustomIDs)+8)
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		fields = append(fields, id)
	}
	for _, f := range coreScalarFields {
		add(f)
	}
	var customIDs []string
	for jiraID, f := range fieldMap {
		if f.IsCustom && f.IsColumn {
			customIDs = append(customIDs, jiraID)
		}
	}
	customIDs = append(customIDs, extraCustomIDs...)
	sort.Strings(customIDs)
	for _, id := range customIDs {
		add(id)
	}
	return fields
}

// DefaultTrackedChangelogFields returns up to 10 load-bearing Jira field IDs
// for filtered changelog/bulkfetch calls: core workflow fields plus Sprint and
// Rank custom field IDs resolved from fieldMap (when present) and any extra
// field names or IDs.
func DefaultTrackedChangelogFields(fieldMap map[string]*db.FieldMapping, extra ...string) []string {
	byName := make(map[string]string, len(fieldMap)*2)
	for id, f := range fieldMap {
		byName[strings.ToLower(f.Name)] = id
		if f.JiraName != "" {
			byName[strings.ToLower(f.JiraName)] = id
		}
		byName[strings.ToLower(id)] = id
	}

	seen := make(map[string]struct{}, jira.BulkChangelogMaxFieldIDs)
	out := make([]string, 0, jira.BulkChangelogMaxFieldIDs)
	add := func(id string) {
		if len(out) >= jira.BulkChangelogMaxFieldIDs {
			return
		}
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}

	// Core workflow & identity fields.
	for _, id := range []string{"status", "assignee", "priority", "resolution", "issuetype", "project", "Key"} {
		add(id)
	}
	// Resolve common Agile custom fields if discovered.
	for _, name := range []string{"sprint", "rank"} {
		if id, ok := byName[name]; ok {
			add(id)
		}
	}
	// Append caller-configured tracked fields (resolving column/display names).
	for _, name := range extra {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		if id, ok := byName[strings.ToLower(trimmed)]; ok {
			add(id)
		} else {
			add(trimmed)
		}
	}
	return out
}

// configuredCustomFieldIDs resolves custom field IDs referenced by PromoteFields,
// Metrics.TrackedFields, and ReconcileFields so slim projections always keep
// configured fields fresh.
func (e *Engine) configuredCustomFieldIDs(fieldMap map[string]*db.FieldMapping) []string {
	if e.cfg == nil {
		return nil
	}
	var names []string
	names = append(names, e.cfg.Sync.PromoteFields...)
	names = append(names, e.cfg.Sync.ReconcileFields...)
	if e.cfg.Metrics != nil {
		names = append(names, e.cfg.Metrics.TrackedFields...)
	}
	if len(names) == 0 {
		return nil
	}
	byName := make(map[string]string, len(fieldMap)*2)
	for id, f := range fieldMap {
		if !f.IsCustom {
			continue
		}
		byName[strings.ToLower(f.Name)] = id
		if f.JiraName != "" {
			byName[strings.ToLower(f.JiraName)] = id
		}
		byName[strings.ToLower(id)] = id
	}
	var ids []string
	for _, n := range names {
		if id, ok := byName[strings.ToLower(strings.TrimSpace(n))]; ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// DeltaOptions configures Engine.DeltaSync, a two-stage minimal-probe delta
// pass: Stage 1 queries POST /rest/api/3/search/jql with fields=["updated"] and
// maxResults=5000, diffs against SQLite in one batch query, and Stage 2
// hydrates only new or changed issues via POST /rest/api/3/issue/bulkfetch.
type DeltaOptions struct {
	// ScopeName identifies the sync_metadata row used to read and advance
	// last_issue_updated. Required unless SkipMeta is true.
	ScopeName string

	// JQL is the base scope query (e.g. `project in ("ENG")` or a custom/personal
	// JQL filter). Required.
	JQL string

	// WatchIDs optionally folds specific numeric issue IDs (`OR id in (...)`)
	// into the Stage-1 delta probe.
	WatchIDs []string

	// Lookback is the overlap duration subtracted from the high-water mark.
	// Zero defaults to 2 minutes. Negative disables the overlap (exact HWM).
	Lookback time.Duration

	// Since overrides the high-water mark (RFC3339). When empty and !SkipMeta,
	// DeltaSync reads sync_metadata.last_issue_updated (falling back to
	// last_sync_time).
	Since string

	// SkipMeta disables reading and writing sync_metadata for this run.
	SkipMeta bool

	// Fields explicitly sets the Stage-2 BulkFetchIssues field list. When empty,
	// SlimFields (or cfg.Sync.FieldProjection=="slim") selects ExpandFieldsSlim;
	// otherwise ExpandFieldsFull is used.
	Fields []string

	// SlimFields forces slim core+comment projection for Stage 2 when Fields is empty.
	SlimFields bool

	// ReconcileIssues passes up to 50 issue IDs to Stage-1 search/jql for
	// read-after-write consistency.
	ReconcileIssues []int64

	// SyncChangelogs fetches changelogs for newly upserted issues in Stage 2.
	SyncChangelogs bool

	// ChangelogFieldIDs optionally limits Stage-2 changelog/bulkfetch to <=10
	// Jira field IDs. Empty fetches complete unfiltered changelogs.
	ChangelogFieldIDs []string
}

// DeltaResult summarizes a completed Engine.DeltaSync run.
type DeltaResult struct {
	Scope         string
	JQL           string
	Probed        int      // issues returned by the Stage-1 probe
	Changed       int      // issues whose updated differed from SQLite (or new)
	New           int      // newly discovered issues
	Updated       int      // existing issues whose updated advanced
	SyncedKeys    []string // keys successfully upserted in Stage 2
	MissingIDs    []string // IDs/keys seen in Stage 1 but absent from Stage 2 (deleted/403)
	HighWaterMark string   // max RFC3339 updated seen across Stage 1
}

type probeUpdatedFields struct {
	Updated string `json:"updated"`
}

func extractProbeUpdated(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var f probeUpdatedFields
	if err := json.Unmarshal(raw, &f); err != nil {
		return ""
	}
	return normalizeDate(f.Updated)
}

// buildDeltaScopeJQL combines baseJQL, optional watchIDs, and an optional
// RFC3339 floor timestamp into a newest-first Stage-1 probe JQL.
func buildDeltaScopeJQL(baseJQL string, watchIDs []string, floor string, loc *time.Location) string {
	scope := "(" + strings.TrimSpace(baseJQL) + ")"
	if len(watchIDs) > 0 {
		cleaned := make([]string, 0, len(watchIDs))
		for _, id := range watchIDs {
			if id = strings.TrimSpace(id); id != "" {
				cleaned = append(cleaned, id)
			}
		}
		if len(cleaned) > 0 {
			scope = fmt.Sprintf("(%s OR id in (%s))", scope, strings.Join(cleaned, ", "))
		}
	}
	if floor != "" {
		return fmt.Sprintf(`%s AND updated >= "%s" ORDER BY updated DESC`, scope, cursorToJQL(floor, loc))
	}
	return scope + ` ORDER BY updated DESC`
}

// DeltaSync executes a two-stage cheap delta sync:
//  1. Probe `POST /rest/api/3/search/jql` with `fields: ["updated"]` and
//     `maxResults: 5000`, diffing against SQLite in batch via GetIssuesUpdatedByID.
//  2. Hydrate only new or changed issues via `POST /rest/api/3/issue/bulkfetch`
//     (and optionally `POST /rest/api/3/changelog/bulkfetch`).
//
// The high-water mark (`sync_metadata.last_issue_updated`) advances ONLY after
// both Stage 1 and Stage 2 succeed; mid-run failure leaves the watermark intact.
func (e *Engine) DeltaSync(ctx context.Context, opts DeltaOptions) (*DeltaResult, error) {
	if strings.TrimSpace(opts.JQL) == "" {
		return nil, fmt.Errorf("delta sync: JQL is required")
	}
	if !opts.SkipMeta && strings.TrimSpace(opts.ScopeName) == "" {
		return nil, fmt.Errorf("delta sync: ScopeName is required when SkipMeta is false")
	}

	jiraTZ := e.jiraTimezone()
	if jiraTZ == nil {
		return nil, fmt.Errorf("jira timezone is not initialized; call VerifyAuth or SetTimezone before DeltaSync")
	}

	if err := e.ensurePromotedFields(); err != nil {
		return nil, fmt.Errorf("promoting configured fields: %w", err)
	}
	fieldMap, err := e.db.FieldMapByJiraID()
	if err != nil {
		return nil, fmt.Errorf("loading field map: %w", err)
	}

	start := e.now()

	// Resolve high-water mark floor.
	hwm := strings.TrimSpace(opts.Since)
	if hwm == "" && !opts.SkipMeta {
		meta, err := e.db.GetSyncMeta(opts.ScopeName)
		if err != nil {
			return nil, fmt.Errorf("loading sync meta: %w", err)
		}
		if meta.LastIssueUpdated.Valid && meta.LastIssueUpdated.String != "" {
			hwm = meta.LastIssueUpdated.String
		} else if meta.LastSyncTime.Valid && meta.LastSyncTime.String != "" {
			hwm = meta.LastSyncTime.String
		}
	}

	floor := ""
	if hwm != "" {
		lookback := opts.Lookback
		if lookback == 0 {
			lookback = defaultDeltaLookback
		}
		if lookback > 0 {
			if t, err := time.Parse(time.RFC3339, hwm); err == nil {
				floor = t.Add(-lookback).UTC().Format(time.RFC3339)
			} else {
				floor = hwm
			}
		} else {
			floor = hwm
		}
	}

	jql := buildDeltaScopeJQL(opts.JQL, opts.WatchIDs, floor, jiraTZ)
	res := &DeltaResult{
		Scope: opts.ScopeName,
		JQL:   jql,
	}

	// Stage 1: minimal-field probe (fields=["updated"], maxResults=5000).
	type changedTarget struct {
		ref   string // numeric ID when available, else key
		id    string
		key   string
		isNew bool
	}
	var targets []changedTarget
	seenRef := make(map[string]struct{})
	var maxUpdated string

	searchOpts := jira.SearchOptions{
		MaxResults:      jira.SearchNarrowMaxResults,
		ReconcileIssues: opts.ReconcileIssues,
	}

	for page, err := range e.client.SearchAllWithOptions(ctx, jql, []string{"updated"}, searchOpts) {
		if err != nil {
			if !opts.SkipMeta {
				elapsed := e.now().Sub(start).Seconds()
				// Watermark safety: do NOT advance HWM on Stage-1 failure.
				_ = e.db.UpdateSyncMeta(opts.ScopeName, elapsed, res.Probed, 0, err.Error(), "")
			}
			return res, err
		}

		res.Probed += len(page)
		ids := make([]string, 0, len(page))
		keys := make([]string, 0, len(page))
		for _, iss := range page {
			if iss.ID != "" {
				ids = append(ids, iss.ID)
			} else if iss.Key != "" {
				keys = append(keys, iss.Key)
			}
		}

		storedByID, err := e.db.GetIssuesUpdatedByID(ids)
		if err != nil {
			return res, fmt.Errorf("batch lookup updated by id: %w", err)
		}
		storedByKey, err := e.db.GetIssuesUpdatedByKey(keys)
		if err != nil {
			return res, fmt.Errorf("batch lookup updated by key: %w", err)
		}

		for _, iss := range page {
			remoteUpdated := extractProbeUpdated(iss.Fields)
			if remoteUpdated > maxUpdated {
				maxUpdated = remoteUpdated
			}

			var storedUpdated string
			ref := iss.ID
			if iss.ID != "" {
				storedUpdated = storedByID[iss.ID]
			} else {
				ref = iss.Key
				storedUpdated = storedByKey[iss.Key]
			}
			if ref == "" {
				continue
			}

			if storedUpdated != "" && storedUpdated == remoteUpdated {
				continue
			}
			if _, dup := seenRef[ref]; dup {
				continue
			}
			seenRef[ref] = struct{}{}

			isNew := storedUpdated == ""
			if isNew {
				res.New++
			} else {
				res.Updated++
			}
			targets = append(targets, changedTarget{
				ref:   ref,
				id:    iss.ID,
				key:   iss.Key,
				isNew: isNew,
			})
		}
	}

	res.Changed = len(targets)
	res.HighWaterMark = maxUpdated

	// Stage 2: targeted hydration via POST /rest/api/3/issue/bulkfetch.
	if len(targets) > 0 {
		refs := make([]string, len(targets))
		for i, t := range targets {
			refs[i] = t.ref
		}

		fields := opts.Fields
		if len(fields) == 0 {
			if opts.SlimFields || (e.cfg != nil && strings.EqualFold(e.cfg.Sync.FieldProjection, "slim")) {
				fields = ExpandFieldsSlim(fieldMap, e.configuredCustomFieldIDs(fieldMap))
			} else {
				fields = ExpandFieldsFull(fieldMap)
			}
		}

		bulkResp, err := e.client.BulkFetchIssues(ctx, refs, jira.BulkFetchIssuesOptions{
			Fields: fields,
		})
		if err != nil {
			if !opts.SkipMeta {
				elapsed := e.now().Sub(start).Seconds()
				// Watermark safety: do NOT advance HWM on Stage-2 failure.
				_ = e.db.UpdateSyncMeta(opts.ScopeName, elapsed, res.Probed, 0, err.Error(), "")
			}
			return res, fmt.Errorf("hydrating changed issues: %w", err)
		}

		returnedIDs := make(map[string]struct{}, len(bulkResp.Issues))
		returnedKeys := make(map[string]struct{}, len(bulkResp.Issues))
		bundles := make([]*db.IssueBundle, 0, len(bulkResp.Issues))
		for _, apiIssue := range bulkResp.Issues {
			if apiIssue.ID != "" {
				returnedIDs[apiIssue.ID] = struct{}{}
			}
			if apiIssue.Key != "" {
				returnedKeys[apiIssue.Key] = struct{}{}
			}
			rawBytes, err := json.Marshal(apiIssue)
			if err != nil {
				continue
			}
			issue, extra, err := Denormalize(rawBytes, fieldMap)
			if err != nil {
				continue
			}
			if issue.Updated > maxUpdated {
				maxUpdated = issue.Updated
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
				for _, b := range bundles {
					if berr := e.db.UpsertIssueBundleTx([]*db.IssueBundle{b}); berr == nil {
						res.SyncedKeys = append(res.SyncedKeys, b.Issue.Key)
					}
				}
			} else {
				for _, b := range bundles {
					res.SyncedKeys = append(res.SyncedKeys, b.Issue.Key)
				}
			}
		}
		for _, t := range targets {
			_, gotID := returnedIDs[t.id]
			_, gotKey := returnedKeys[t.key]
			if !gotID && !gotKey {
				res.MissingIDs = append(res.MissingIDs, t.ref)
			}
		}

		if opts.SyncChangelogs && len(res.SyncedKeys) > 0 {
			clFields := opts.ChangelogFieldIDs
			if len(clFields) == 0 && e.cfg != nil {
				clFields = e.cfg.Sync.ChangelogFields
			}
			e.syncChangelogsForKeysWithOptions(ctx, res.SyncedKeys, clFields)
		}

		if err := e.mw.refreshTouched(res.SyncedKeys); err != nil {
			e.warnf("warning: metric refresh for %s failed: %v\n", opts.ScopeName, err)
		}
	}

	res.HighWaterMark = maxUpdated

	if !opts.SkipMeta {
		elapsed := e.now().Sub(start).Seconds()
		_ = e.db.UpdateSyncMeta(opts.ScopeName, elapsed, res.Probed, len(res.SyncedKeys), "", maxUpdated)
	}

	if len(res.SyncedKeys) > 0 {
		if e.db != nil {
			_ = e.db.Optimize()
		}
		e.emitChange(ChangeEvent{
			Source:  opts.ScopeName,
			New:     res.New,
			Updated: res.Updated,
			Total:   res.Probed,
		})
	}

	return res, nil
}

// HydrateIssueDetails fetches the complete single-issue payload from Jira
// (GET /rest/api/3/issue/{keyOrID} — including worklogs, attachments, and all
// custom fields), upserts the issue and side tables, stamps details_synced_at,
// and refreshes flow metrics for that issue.
func (e *Engine) HydrateIssueDetails(ctx context.Context, keyOrID string) (*db.Issue, error) {
	if strings.TrimSpace(keyOrID) == "" {
		return nil, fmt.Errorf("hydrate issue details: key or ID is required")
	}
	fieldMap, err := e.db.FieldMapByJiraID()
	if err != nil {
		return nil, fmt.Errorf("loading field map: %w", err)
	}
	apiIssue, err := e.client.GetIssue(ctx, keyOrID)
	if err != nil {
		return nil, err
	}
	rawBytes, err := json.Marshal(apiIssue)
	if err != nil {
		return nil, err
	}
	issue, extra, err := Denormalize(rawBytes, fieldMap)
	if err != nil {
		return nil, err
	}
	if err := e.db.UpsertIssue(issue, extra); err != nil {
		return nil, err
	}
	e.applyIssueSideEffects(apiIssue.Key, rawBytes)
	if err := e.db.MarkIssueDetailsSynced([]string{apiIssue.Key}); err != nil {
		return nil, err
	}
	if err := e.mw.refreshTouched([]string{apiIssue.Key}); err != nil {
		e.warnf("warning: metric refresh for %s failed: %v\n", apiIssue.Key, err)
	}
	return issue, nil
}

// SyncIssueFullChangelog fetches complete unfiltered changelog history for a
// single issue via GET /rest/api/3/issue/{key}?expand=changelog, merges entries
// idempotently via INSERT OR IGNORE, and stamps changelog_full_synced_at and
// changelog_synced_at. Returns the number of changelog entries processed.
func (e *Engine) SyncIssueFullChangelog(ctx context.Context, key string) (int, error) {
	if strings.TrimSpace(key) == "" {
		return 0, fmt.Errorf("sync full changelog: key is required")
	}
	resp, err := e.client.GetIssueChangelog(ctx, key)
	if err != nil {
		return 0, err
	}
	entries := ExtractChangelog(key, resp)
	if err := e.db.InsertChangelogBatch(entries); err != nil {
		return 0, err
	}
	if err := e.db.MarkChangelogFullSynced([]string{key}); err != nil {
		return 0, err
	}
	if err := e.mw.refreshTouched([]string{key}); err != nil {
		e.warnf("warning: metric refresh for %s failed: %v\n", key, err)
	}
	return len(entries), nil
}
