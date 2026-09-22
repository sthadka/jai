package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/jira"
)

// ReconcileProgress reports the state of a rank/field reconcile pass for a
// single sync source.
type ReconcileProgress struct {
	Source    string
	Fields    []string // column names being reconciled
	Scanned   int      // issues scanned in the cheap key+fields pass
	Changed   int      // issues whose reconciled fields drifted
	Refetched int      // issues corrected: full re-fetch, or direct value apply above the cap
	Skipped   bool     // Deprecated: no longer set; large change sets are applied directly now
	Err       error
	Done      bool
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

	fieldMap, err := e.db.FieldMapByJiraID()
	if err != nil {
		return nil, fmt.Errorf("loading field map: %w", err)
	}

	// Ensure custom columns exist: a drifted issue is re-fetched in full and
	// upserted, which writes every custom column. Normal sync ensures these,
	// but reconcile may run standalone (e.g. via MCP).
	if err := e.ensureCustomColumns(fieldMap); err != nil {
		return nil, err
	}

	// Resolve requested column names to Jira field IDs. Unknown fields are
	// warned about and skipped rather than failing the whole pass.
	colToJiraID := map[string]string{}
	for _, f := range fieldMap {
		colToJiraID[f.Name] = f.JiraID
	}
	var resolved []reconcileField
	for _, col := range fields {
		jiraID, ok := colToJiraID[col]
		if !ok {
			fmt.Fprintf(os.Stderr, "  ⚠ reconcile: unknown field %q (not in field_map), skipping\n", col)
			continue
		}
		resolved = append(resolved, reconcileField{col: col, jiraID: jiraID})
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

type reconcileField struct {
	col    string
	jiraID string
}

func (e *Engine) reconcileSource(ctx context.Context, src config.SyncSource, resolved []reconcileField, fieldMap map[string]*db.FieldMapping, ch chan<- ReconcileProgress) {
	cols := make([]string, len(resolved))
	scanFields := []string{"key"}
	for i, rf := range resolved {
		cols[i] = rf.col
		scanFields = append(scanFields, rf.jiraID)
	}

	jql := sourceJQL(src) // no "updated >=" filter — that gate is exactly what misses rank changes
	var scanned int
	var changedKeys []string
	// Fresh reconcile-field values captured during the scan, keyed by issue key
	// then Jira field id. Retained so the direct-apply path can write them
	// without a second network round-trip when the change set exceeds the cap.
	changedVals := make(map[string]map[string]json.RawMessage)

	for page, err := range e.client.SearchAll(ctx, jql, scanFields) {
		if err != nil {
			ch <- ReconcileProgress{Source: src.Name, Fields: cols, Scanned: scanned, Err: err, Done: true}
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
			ch <- ReconcileProgress{Source: src.Name, Fields: cols, Scanned: scanned, Err: err, Done: true}
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

		ch <- ReconcileProgress{Source: src.Name, Fields: cols, Scanned: scanned, Changed: len(changedKeys)}
	}

	if len(changedKeys) == 0 {
		ch <- ReconcileProgress{Source: src.Name, Fields: cols, Scanned: scanned, Done: true}
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
	ch <- ReconcileProgress{Source: src.Name, Fields: cols, Scanned: scanned, Changed: len(changedKeys), Refetched: applied, Err: err, Done: true}
}

// refetchAndUpsert re-fetches the given issues in full and upserts them,
// refreshing every column plus changelog, links, and comments. Returns the
// number of issues successfully upserted, and the first error encountered while
// re-fetching (if any) so the caller can surface partial reconciliation instead
// of silently leaving known-drifted issues uncorrected.
func (e *Engine) refetchAndUpsert(ctx context.Context, keys []string, fieldMap map[string]*db.FieldMapping) (int, error) {
	fields := e.expandFields(fieldMap)
	refetched := 0
	var firstErr error
	const batchSize = 100
	for i := 0; i < len(keys); i += batchSize {
		end := i + batchSize
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[i:end]

		jql := fmt.Sprintf("key in (%s)", strings.Join(quoteKeys(batch), ", "))
		var upserted []string
		for page, err := range e.client.SearchAll(ctx, jql, fields) {
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("re-fetching drifted issues: %w", err)
				}
				break
			}
			for _, apiIssue := range page {
				if e.upsertAPIIssue(apiIssue, fieldMap) {
					upserted = append(upserted, apiIssue.Key)
					refetched++
				}
			}
		}
		if len(upserted) > 0 {
			e.syncChangelogsForKeys(ctx, upserted)
		}
	}
	return refetched, firstErr
}

// applyReconcileValues writes the fresh reconcile-field values captured during
// the scan directly into each drifted issue's raw_json and columns, without a
// full API re-fetch. It handles change sets larger than maxReconcileRefetch —
// most notably a whole-project LexoRank rebalance, which re-ranks tens of
// thousands of issues at once. Re-fetching them all would defeat the cheap
// pass; since the scan already carries the new values, patch them into the
// stored raw_json and re-denormalize so the affected columns and raw_json
// refresh. Changelog, links, and comments are intentionally left untouched: a
// silent reconcile-field change does not affect them. Returns the number of
// issues updated and the first error encountered.
func (e *Engine) applyReconcileValues(keys []string, vals map[string]map[string]json.RawMessage, fieldMap map[string]*db.FieldMapping) (int, error) {
	applied := 0
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
			if err := e.db.UpsertIssue(issue, extra); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			applied++
		}
	}
	return applied, firstErr
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

// upsertAPIIssue denormalizes and upserts a single API issue along with its
// comments, links, and attachments. Returns true on a successful issue upsert.
func (e *Engine) upsertAPIIssue(apiIssue *jira.Issue, fieldMap map[string]*db.FieldMapping) bool {
	rawBytes, err := json.Marshal(apiIssue)
	if err != nil {
		return false
	}
	issue, extra, err := Denormalize(rawBytes, fieldMap)
	if err != nil {
		return false
	}
	if err := e.db.UpsertIssue(issue, extra); err != nil {
		return false
	}

	if comments, err := ExtractComments(apiIssue.Key, rawBytes); err == nil {
		for _, c := range comments {
			_ = e.db.UpsertComment(c)
		}
		if len(comments) > 0 {
			_ = e.db.UpdateIssueCommentsText(apiIssue.Key)
		}
	}

	links := ExtractIssueLinks(apiIssue.Key, rawBytes)
	_ = e.db.UpsertIssueLinks(apiIssue.Key, links)

	if attachments, err := ExtractAttachments(apiIssue.Key, rawBytes); err == nil && attachments != nil {
		_ = e.db.UpsertAttachments(apiIssue.Key, attachments)
	}
	return true
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

func quoteKeys(keys []string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = `"` + k + `"`
	}
	return out
}
