package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sthadka/jai/v7/pkg/config"
	"github.com/sthadka/jai/v7/pkg/jira"
	db "github.com/sthadka/jai/v7/pkg/store"
)

// ReconcileProgress reports the state of a rank/field reconcile pass for a
// single sync source.
type ReconcileProgress struct {
	Source    string
	Fields    []string // column names being reconciled
	Scanned   int      // issues scanned in the cheap key+fields pass
	Changed   int      // issues whose reconciled fields drifted
	Refetched int      // issues corrected: full re-fetch, or direct value apply above the cap
	// CadenceSkipped is set when the source was skipped because it was
	// reconciled within Sync.ReconcileInterval. No scan ran.
	CadenceSkipped bool
	Err            error
	Done           bool
}

// maxReconcileRefetch caps how many drifted issues the reconcile pass will
// re-fetch in FULL in one run. At or below the cap, drifted issues are
// re-fetched whole (also refreshing changelog, links, and comments). Above it
// — e.g. a LexoRank rebalance that re-ranks tens of thousands of issues at
// once — a full re-fetch would defeat the point of the cheap pass, so the
// fresh reconcile-field values captured during the scan are applied directly
// instead (see applyReconcileValues). A var (not const) so tests can exercise
// the cap without seeding 500 issues.
var maxReconcileRefetch = 500

// ReconcileFields re-checks the configured fields for every issue in scope,
// bypassing the "updated" high-water-mark gate that normal incremental sync
// relies on. Jira does not bump "updated" for some changes (rank/LexoRank
// reorders most notably), so those changes are otherwise invisible until the
// issue is touched for another reason.
//
// The pass is two-phase: a cheap scan fetches only key + the reconcile fields
// and diffs each against the stored value; only issues that actually drifted
// are corrected. Change sets at or below maxReconcileRefetch are re-fetched in
// full (refreshing all columns, changelog, links, and comments); larger sets
// apply the scanned field values directly (see applyReconcileValues). fields
// is the list of column names to reconcile; an
// empty list is a no-op.
func (e *Engine) ReconcileFields(ctx context.Context, sourceFilter string, fields []string) (<-chan ReconcileProgress, error) {
	if len(fields) == 0 {
		ch := make(chan ReconcileProgress)
		close(ch)
		return ch, nil
	}

	sources, err := effectiveSources(e.cfg, sourceFilter)
	if err != nil {
		return nil, err
	}

	resolved, fieldMap, err := e.resolveReconcileFields(fields)
	if err != nil {
		return nil, err
	}

	ch := make(chan ReconcileProgress, 8)
	go func() {
		defer close(ch)
		if len(resolved) == 0 {
			return
		}
		for _, src := range sources {
			e.reconcileSource(ctx, src, resolved, fieldMap, ch)
		}
	}()
	return ch, nil
}

// ReconcileScoped runs a rank/field reconcile over an arbitrary JQL subset
// (e.g. the issues on a single board, via its filter JQL) rather than a
// configured sync source. It is the cheap, on-demand path for refreshing just
// the ordering a user is currently viewing instead of scanning the whole
// project: rank is a global LexoRank, so refreshing a subset yields a correct
// ordering for that subset without touching — or rescanning — the rest.
//
// freshnessKey namespaces this scope's last_reconcile stamp (e.g. "board:123")
// so repeated opens are rate-limited by interval WITHOUT ever marking a full
// source reconciled — the source still needs its own pass to cover off-scope
// issues. interval is the minimum time between passes for the key ("" or "0"
// runs every call). An empty fields list is a no-op.
func (e *Engine) ReconcileScoped(ctx context.Context, freshnessKey, jql, interval string, fields []string) (<-chan ReconcileProgress, error) {
	if len(fields) == 0 {
		ch := make(chan ReconcileProgress)
		close(ch)
		return ch, nil
	}
	if strings.TrimSpace(freshnessKey) == "" {
		return nil, fmt.Errorf("reconcile: freshnessKey is required")
	}
	if strings.TrimSpace(jql) == "" {
		return nil, fmt.Errorf("reconcile: jql is required")
	}

	resolved, fieldMap, err := e.resolveReconcileFields(fields)
	if err != nil {
		return nil, err
	}

	ch := make(chan ReconcileProgress, 8)
	go func() {
		defer close(ch)
		if len(resolved) == 0 {
			return
		}
		e.reconcileScan(ctx, reconcileScope{key: freshnessKey, baseJQL: jql, interval: interval}, resolved, fieldMap, ch)
	}()
	return ch, nil
}

// BoardReconcileKey is the per-board freshness key used by ReconcileBoard. It is
// deliberately distinct from any sync source name so a board-scoped refresh
// never marks a full source reconciled.
func BoardReconcileKey(boardID int) string {
	return fmt.Sprintf("board:%d", boardID)
}

// ReconcileBoard refreshes ranks/fields for just the issues on one board. It
// resolves the board's filter JQL (GET board configuration → filter → JQL) and
// reconciles that subset on demand, tracked under BoardReconcileKey so repeated
// opens are rate-limited by interval without rescanning the whole project. This
// is the entry a board view calls when its local rank snapshot may be stale.
func (e *Engine) ReconcileBoard(ctx context.Context, boardID int, interval string, fields []string) (<-chan ReconcileProgress, error) {
	jql, err := e.client.GetBoardFilterJQL(ctx, boardID)
	if err != nil {
		return nil, fmt.Errorf("resolving board %d filter: %w", boardID, err)
	}
	return e.ReconcileScoped(ctx, BoardReconcileKey(boardID), jql, interval, fields)
}

// resolveReconcileFields loads the Jira field map and resolves requested column
// names to Jira field IDs. Unknown fields are warned about and skipped rather
// than failing the whole pass. Shared by ReconcileFields and ReconcileScoped.
func (e *Engine) resolveReconcileFields(fields []string) ([]reconcileField, map[string]*db.FieldMapping, error) {
	fieldMap, err := e.db.FieldMapByJiraID()
	if err != nil {
		return nil, nil, fmt.Errorf("loading field map: %w", err)
	}
	colToJiraID := map[string]string{}
	for _, f := range fieldMap {
		colToJiraID[f.Name] = f.JiraID
	}
	var resolved []reconcileField
	for _, col := range fields {
		jiraID, ok := colToJiraID[col]
		if !ok {
			e.warnf("  ⚠ reconcile: unknown field %q (not in field_map), skipping\n", col)
			continue
		}
		resolved = append(resolved, reconcileField{col: col, jiraID: jiraID})
	}
	return resolved, fieldMap, nil
}

type reconcileField struct {
	col    string
	jiraID string
}

// reconcileScope describes one reconcile-pass target: the freshness key under
// which its last_reconcile is tracked, the base JQL to scan (before the shared
// ReconcileScope clause is ANDed on), and the minimum interval between passes
// for that key ("" / "0" = always run). A configured sync source and an
// on-demand board scope differ only by these three values.
type reconcileScope struct {
	key      string
	baseJQL  string
	interval string
}

// reconcileSource reconciles one configured sync source, tracked under its own
// name and gated by the global ReconcileInterval.
func (e *Engine) reconcileSource(ctx context.Context, src config.SyncSource, resolved []reconcileField, fieldMap map[string]*db.FieldMapping, ch chan<- ReconcileProgress) {
	e.reconcileScan(ctx, reconcileScope{
		key:      src.Name,
		baseJQL:  sourceJQL(src),
		interval: e.cfg.Sync.ReconcileInterval,
	}, resolved, fieldMap, ch)
}

// reconcileScan runs the two-phase reconcile (cheap key+fields scan, then
// correct only drifted issues) over sc.baseJQL, tracking freshness under sc.key.
func (e *Engine) reconcileScan(ctx context.Context, sc reconcileScope, resolved []reconcileField, fieldMap map[string]*db.FieldMapping, ch chan<- ReconcileProgress) {
	cols := make([]string, len(resolved))
	scanFields := []string{"key"}
	for i, rf := range resolved {
		cols[i] = rf.col
		scanFields = append(scanFields, rf.jiraID)
	}

	// Cadence gate: the pass enumerates every in-scope issue (same request count
	// as a full scan), so skip a scope reconciled within its interval.
	// "0"/empty/unparseable means run every time.
	if d, err := time.ParseDuration(sc.interval); err == nil && d > 0 {
		last, err := e.db.GetLastReconcile(sc.key)
		if err != nil {
			ch <- ReconcileProgress{Source: sc.key, Fields: cols, Err: err, Done: true}
			return
		}
		if t, perr := time.Parse(time.RFC3339, last); perr == nil && time.Since(t) < d {
			ch <- ReconcileProgress{Source: sc.key, Fields: cols, CadenceSkipped: true, Done: true}
			return
		}
	}

	jql := sc.baseJQL // no "updated >=" filter — that gate is exactly what misses rank changes
	// Scope the scan to issues that can actually drift silently (default: not
	// Done). Rank reorders never touch closed issues, so excluding them cuts the
	// scan without missing corrections.
	if scope := strings.TrimSpace(e.cfg.Sync.ReconcileScope); scope != "" {
		jql = fmt.Sprintf("(%s) AND (%s)", jql, scope)
	}
	var scanned int
	var changedKeys []string
	// Fresh reconcile-field values captured during the scan, keyed by issue key
	// then Jira field id. Retained so the direct-apply path can write them
	// without a second network round-trip when the change set exceeds the cap.
	changedVals := make(map[string]map[string]json.RawMessage)

	for page, err := range e.client.SearchAll(ctx, jql, scanFields) {
		if err != nil {
			ch <- ReconcileProgress{Source: sc.key, Fields: cols, Scanned: scanned, Err: err, Done: true}
			return
		}

		// Pull stored raw JSON for this page's keys in one query, then diff the
		// reconcile fields per issue.
		keys := make([]string, 0, len(page))
		fresh := make(map[string]map[string]json.RawMessage, len(page))
		for _, apiIssue := range page {
			scanned++
			keys = append(keys, apiIssue.Key)
			var ff map[string]json.RawMessage
			_ = json.Unmarshal(apiIssue.Fields, &ff)
			fresh[apiIssue.Key] = ff
		}

		stored, err := e.db.GetIssuesRawJSON(keys)
		if err != nil {
			ch <- ReconcileProgress{Source: sc.key, Fields: cols, Scanned: scanned, Err: err, Done: true}
			return
		}

		for _, key := range keys {
			storedFields := parseIssueFields(stored[key])
			var drifted bool
			for _, rf := range resolved {
				if !rawFieldEqual(fresh[key][rf.jiraID], storedFields[rf.jiraID]) {
					drifted = true
					break
				}
			}
			if !drifted {
				continue
			}
			changedKeys = append(changedKeys, key)
			vals := make(map[string]json.RawMessage, len(resolved))
			for _, rf := range resolved {
				vals[rf.jiraID] = fresh[key][rf.jiraID]
			}
			changedVals[key] = vals
		}

		ch <- ReconcileProgress{Source: sc.key, Fields: cols, Scanned: scanned, Changed: len(changedKeys)}
	}

	if len(changedKeys) == 0 {
		// Scan completed cleanly — record the pass so the cadence gate can skip
		// this scope until its interval elapses.
		_ = e.db.UpdateReconcileMeta(sc.key)
		ch <- ReconcileProgress{Source: sc.key, Fields: cols, Scanned: scanned, Done: true}
		return
	}

	// At or below the cap, re-fetch drifted issues in full (also refreshes
	// changelog, links, and comments in case they changed too). Above the cap —
	// e.g. a whole-project LexoRank rebalance re-ranks tens of thousands of
	// issues at once — a full re-fetch would defeat the cheap pass, so apply the
	// fresh reconcile-field values captured during the scan directly. No extra
	// API calls, and a silent reconcile-field change (rank) does not touch
	// changelog/links/comments.
	var applied int
	var err error
	if len(changedKeys) > maxReconcileRefetch {
		applied, err = e.applyReconcileValues(changedKeys, changedVals, fieldMap)
	} else {
		applied, err = e.refetchAndUpsert(ctx, changedKeys, fieldMap)
	}
	if err == nil {
		// Only stamp the pass as complete when every drifted issue was
		// corrected; a partial failure leaves the cadence open so the next pass
		// retries instead of waiting out the interval.
		_ = e.db.UpdateReconcileMeta(sc.key)
	}
	ch <- ReconcileProgress{Source: sc.key, Fields: cols, Scanned: scanned, Changed: len(changedKeys), Refetched: applied, Err: err, Done: true}
}

// refetchAndUpsert re-fetches the given issues in full and upserts them,
// refreshing every column plus changelog, links, and comments. It runs in two
// phases so the ordering snapshot is atomic: first fetch every drifted issue
// (network only, no writes), then upsert all issue rows in a single
// transaction. A mid-fetch failure or a failed transaction therefore leaves the
// prior (last-known-good) ranks intact instead of a half-updated order. Returns
// the number of issues upserted and the first error encountered.
func (e *Engine) refetchAndUpsert(ctx context.Context, keys []string, fieldMap map[string]*db.FieldMapping) (int, error) {
	fields := e.expandFields(fieldMap)
	var firstErr error

	// Phase 1: fetch all drifted issues via POST /rest/api/3/issue/bulkfetch
	// (bypasses JQL index lag and query-length limits; auto-chunks at 100/1000).
	bulkResp, err := e.client.BulkFetchIssues(ctx, keys, jira.BulkFetchIssuesOptions{
		Fields: fields,
	})
	if err != nil {
		return 0, fmt.Errorf("re-fetching drifted issues: %w", err)
	}
	apiIssues := bulkResp.Issues
	if len(apiIssues) == 0 {
		return 0, firstErr
	}

	// Phase 2: denormalize (pure) then upsert every issue row in one transaction.
	items := make([]*db.IssueUpsert, 0, len(apiIssues))
	raws := make([][]byte, 0, len(apiIssues))
	kept := make([]*jira.Issue, 0, len(apiIssues))
	for _, apiIssue := range apiIssues {
		rawBytes, err := json.Marshal(apiIssue)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		issue, extra, err := Denormalize(rawBytes, fieldMap)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		items = append(items, &db.IssueUpsert{Issue: issue, Extra: extra})
		raws = append(raws, rawBytes)
		kept = append(kept, apiIssue)
	}
	if len(items) == 0 {
		return 0, firstErr
	}
	if err := e.db.UpsertIssuesTx(items); err != nil {
		if firstErr == nil {
			firstErr = err
		}
		return 0, firstErr // rolled back: prior snapshot preserved
	}

	// Phase 3: non-ordering side effects (comments, links, attachments) and
	// changelog. These are idempotent and outside the snapshot invariant, so
	// they run best-effort after the atomic issue-row commit.
	upserted := make([]string, 0, len(kept))
	for i, apiIssue := range kept {
		e.applyIssueSideEffects(apiIssue.Key, raws[i])
		upserted = append(upserted, apiIssue.Key)
	}
	e.syncChangelogsForKeys(ctx, upserted)
	return len(items), firstErr
}

// applyReconcileValues writes the fresh reconcile-field values captured during
// the scan directly into each drifted issue's raw_json and columns, without a
// full API re-fetch. It handles change sets larger than maxReconcileRefetch —
// most notably a whole-project LexoRank rebalance, which re-ranks tens of
// thousands of issues at once. Re-fetching them all would defeat the cheap
// pass; since the scan already carries the new values, patch them into the
// stored raw_json and re-denormalize so the affected columns and raw_json
// refresh. Changelog, links, and comments are intentionally left untouched: a
// silent reconcile-field change does not affect them.
//
// All rows are patched in memory first, then upserted in a single transaction,
// so an interrupted or failed apply never exposes a half-updated ordering — on
// error the transaction rolls back and the prior snapshot stays intact. Returns
// the number of issues updated and the first error encountered.
func (e *Engine) applyReconcileValues(keys []string, vals map[string]map[string]json.RawMessage, fieldMap map[string]*db.FieldMapping) (int, error) {
	var items []*db.IssueUpsert
	var firstErr error
	const batchSize = 500
	for i := 0; i < len(keys); i += batchSize {
		end := i + batchSize
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[i:end]
		stored, err := e.db.GetIssuesRawJSON(batch)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("loading stored issues for reconcile apply: %w", err)
			}
			continue
		}
		for _, key := range batch {
			raw, ok := stored[key]
			if !ok || raw == "" {
				continue // nothing stored to patch; a full sync will create it
			}
			patched, err := patchRawFields(raw, vals[key])
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			issue, extra, err := Denormalize([]byte(patched), fieldMap)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			items = append(items, &db.IssueUpsert{Issue: issue, Extra: extra})
		}
	}
	if len(items) == 0 {
		return 0, firstErr
	}
	if err := e.db.UpsertIssuesTx(items); err != nil {
		if firstErr == nil {
			firstErr = err
		}
		return 0, firstErr // rolled back: prior snapshot preserved
	}
	return len(items), firstErr
}

// patchRawFields returns rawJSON with each fields[id] replaced by the given raw
// value; a nil/absent/JSON-null value removes the field (Jira cleared it).
// Every other field is preserved untouched so re-denormalizing reproduces the
// stored row with only the reconciled fields changed.
func patchRawFields(rawJSON string, updates map[string]json.RawMessage) (string, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rawJSON), &envelope); err != nil {
		return "", fmt.Errorf("parsing stored raw_json: %w", err)
	}
	var fields map[string]json.RawMessage
	if raw, ok := envelope["fields"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &fields); err != nil {
			return "", fmt.Errorf("parsing stored fields: %w", err)
		}
	}
	if fields == nil {
		fields = make(map[string]json.RawMessage, len(updates))
	}
	for id, val := range updates {
		if len(normalizeRaw(val)) == 0 { // absent or JSON null → field cleared
			delete(fields, id)
		} else {
			fields[id] = val
		}
	}
	fieldsBytes, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	envelope["fields"] = fieldsBytes
	out, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// applyIssueSideEffects writes the non-ordering, idempotent extras extracted
// from a re-fetched issue's raw JSON — comments, links, and attachments. These
// are outside the rank-snapshot invariant, so they run best-effort after the
// atomic issue-row commit; individual failures are ignored.
func (e *Engine) applyIssueSideEffects(key string, rawBytes []byte) {
	if comments, err := ExtractComments(key, rawBytes); err == nil {
		for _, c := range comments {
			_ = e.db.UpsertComment(c)
		}
		if len(comments) > 0 {
			_ = e.db.UpdateIssueCommentsText(key)
		}
	}

	links := ExtractIssueLinks(key, rawBytes)
	_ = e.db.UpsertIssueLinks(key, links)

	if attachments, err := ExtractAttachments(key, rawBytes); err == nil && attachments != nil {
		_ = e.db.UpsertAttachments(key, attachments)
	}
}

// parseIssueFields extracts the "fields" object from a stored issue raw_json
// blob as a map of field-id → raw value. Returns an empty map on any error.
func parseIssueFields(rawJSON string) map[string]json.RawMessage {
	if rawJSON == "" {
		return nil
	}
	var envelope struct {
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &envelope); err != nil {
		return nil
	}
	return envelope.Fields
}

// rawFieldEqual compares two raw JSON field values for semantic equality,
// treating missing and JSON null as equivalent to absent.
func rawFieldEqual(a, b json.RawMessage) bool {
	an := normalizeRaw(a)
	bn := normalizeRaw(b)
	return bytes.Equal(an, bn)
}

// normalizeRaw canonicalizes a raw JSON value so that semantically-equal values
// compare equal: missing and JSON null both collapse to nil, and object keys are
// sorted (Go's json.Marshal emits map keys in sorted order). Canonicalizing key
// order matters because reconcile is reused for arbitrary object-valued fields
// (e.g. user/option fields), where Jira may serialize keys in a different order
// between responses without the value actually changing.
func normalizeRaw(r json.RawMessage) []byte {
	if len(r) == 0 {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal(r, &v); err != nil {
		// Not valid JSON on its own — fall back to a compacted byte compare.
		var buf bytes.Buffer
		if err := json.Compact(&buf, r); err != nil {
			return r
		}
		return buf.Bytes()
	}
	if v == nil { // JSON null
		return nil
	}
	canonical, err := json.Marshal(v)
	if err != nil {
		return r
	}
	return canonical
}
