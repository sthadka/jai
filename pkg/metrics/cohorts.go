package metrics

import (
	"sort"
	"strings"
	"time"
)

// Event-time cohort machinery (shared by M15 workload, M16 WIP/CFD/dwell, and consumed
// downstream by M18/M19 sprint and priority/triage cohorts).
//
// The single most important invariant here is event-time vs current dimensions: an
// event-time cut reads a dimension's value from the interval state in effect at a reference
// instant and NEVER copies today's assignee/team/type onto an old interval. Current-
// attribute cuts read the dimension straight off the `issues` snapshot and are explicitly
// labelled as such. Both are offered so a caller can select the one whose time basis is
// declared and recoverable for the dimension in question.

// CohortCut names the population/time axis a cohort is anchored on. Created anchors each
// issue by its creation instant; Completed anchors by its completion instant. The cut
// decides which issues enter the cohort and against which timestamp event-time dimensions
// are read.
type CohortCut string

const (
	CutCreated   CohortCut = "created"
	CutCompleted CohortCut = "completed"
)

// Dimension is a cohort split dimension. A dimension may only be used for a metric when its
// time basis is declared and recoverable; the caller picks the attribution mode.
type Dimension string

const (
	DimProject   Dimension = "project"
	DimType      Dimension = "type"
	DimPriority  Dimension = "priority"
	DimAssignee  Dimension = "assignee"
	DimComponent Dimension = "component"
	DimLabel     Dimension = "label"
	DimTeam      Dimension = "team"
)

// AttributionMode selects whether a dimension's value is read from the current issue
// snapshot (current-attribute cut, off `issues`) or from the event-time interval state at a
// reference instant (event-time cut). Event-time cuts NEVER copy today's dimension value
// onto historical intervals.
type AttributionMode string

const (
	AttrCurrent AttributionMode = "current"
	AttrEvent   AttributionMode = "event"
)

// unknownOwnerBucket is the explicit bucket for a completion/interval whose event-time
// owner could not be recovered (no assignee history covering the instant, or an unknown
// gap). It is surfaced, never silently dropped and never back-filled from today's owner.
const unknownOwnerBucket = "(unknown owner)"

// NormalizeOwner collapses empty/sentinel assignee values into the explicit Unassigned
// bucket and otherwise prefers a human-readable display name, falling back to the stable
// account id when the display name is missing (display names churn; ids are stable). An
// explicit "unassigned" sentinel is treated as unassigned regardless of a stored id.
func NormalizeOwner(accountID, display string) string {
	d := strings.TrimSpace(display)
	switch {
	case d == "":
		if id := strings.TrimSpace(accountID); id != "" {
			return id
		}
		return unassigned
	case strings.EqualFold(d, "unassigned"):
		return unassigned
	default:
		return d
	}
}

// IntervalIndex is an in-memory temporal join over interval facts (status or any scalar
// tracked field). It answers "what value/stage was in effect for this issue at instant t"
// without copying the current snapshot onto old intervals — the event-time join M15 and M16
// build on. Intervals are half-open [EnteredAt, ExitedAt); an open interval (ExitedAt ==
// nil) extends to +infinity so it ages against the caller's as_of.
type IntervalIndex struct {
	byKey map[string][]IntervalRow
}

// NewIntervalIndex groups interval rows by issue key in seq order.
func NewIntervalIndex(rows []IntervalRow) *IntervalIndex {
	ix := &IntervalIndex{byKey: make(map[string][]IntervalRow)}
	for _, r := range rows {
		ix.byKey[r.IssueKey] = append(ix.byKey[r.IssueKey], r)
	}
	for k := range ix.byKey {
		rs := ix.byKey[k]
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].Seq < rs[j].Seq })
	}
	return ix
}

// Keys returns the indexed issue keys, sorted for deterministic iteration.
func (ix *IntervalIndex) Keys() []string {
	keys := make([]string, 0, len(ix.byKey))
	for k := range ix.byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Intervals returns the (seq-ordered) intervals recorded for an issue key.
func (ix *IntervalIndex) Intervals(key string) []IntervalRow { return ix.byKey[key] }

// At returns the interval in effect for issueKey at instant t and true, or a zero row and
// false when no interval covers t (before creation, or a gap). Membership is half-open —
// EnteredAt <= t < ExitedAt — so a transition instant belongs to exactly one interval and
// is never double counted.
func (ix *IntervalIndex) At(key string, t time.Time) (IntervalRow, bool) {
	for _, r := range ix.byKey[key] {
		if t.Before(r.EnteredAt) {
			continue
		}
		if r.ExitedAt == nil || t.Before(*r.ExitedAt) {
			return r, true
		}
	}
	return IntervalRow{}, false
}

// StageAt returns the stage in effect at instant t and whether a covering interval exists.
// A covered-but-unmapped status yields StageUnknown — a real surfaced bucket, never dropped.
func (ix *IntervalIndex) StageAt(key string, t time.Time) (stage string, covered bool) {
	r, ok := ix.At(key, t)
	if !ok {
		return "", false
	}
	if strings.TrimSpace(r.Stage) == "" {
		return StageUnknown, true
	}
	return r.Stage, true
}

// ValueAt returns the scalar value and its stable value id in effect at instant t, plus
// whether that value is KNOWN (not an unknown-history gap). An unknown gap returns known ==
// false; callers bucket it explicitly rather than dropping it.
func (ix *IntervalIndex) ValueAt(key string, t time.Time) (value, valueID string, known bool) {
	r, ok := ix.At(key, t)
	if !ok {
		return "", "", false
	}
	if r.Provenance == ProvUnknown {
		return r.Value, r.ValueID, false
	}
	return r.Value, r.ValueID, true
}

// OwnerAt returns the normalized event-time owner in effect at instant t and whether it was
// recoverable (a covering, known assignee interval). It never falls back to the current
// snapshot owner.
func (ix *IntervalIndex) OwnerAt(key string, t time.Time) (owner string, known bool) {
	val, id, ok := ix.ValueAt(key, t)
	if !ok {
		return unknownOwnerBucket, false
	}
	return NormalizeOwner(id, val), true
}

// DailySamples returns the UTC-midnight sample instants spanning [from, to] inclusive of
// both endpoints' dates, fixing the timezone and daily sampling boundary so a transition is
// counted on exactly one day. An empty or reversed range yields no samples.
func DailySamples(from, to time.Time) []time.Time {
	if from.IsZero() || to.IsZero() || to.Before(from) {
		return nil
	}
	start := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	end := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	var out []time.Time
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		out = append(out, d)
	}
	return out
}

// DayKey formats a sample instant as its UTC date (YYYY-MM-DD).
func DayKey(t time.Time) string { return t.UTC().Format("2006-01-02") }

// currentDimension extracts a current-attribute cohort bucket from an issue snapshot for
// the dimensions carried on the snapshot (project, type, assignee). Other dimensions need
// event-time intervals or membership and return ("", false).
func currentDimension(it issueSnap, dim Dimension) (string, bool) {
	switch dim {
	case DimProject:
		if strings.TrimSpace(it.project) == "" {
			return "(unknown project)", true
		}
		return it.project, true
	case DimType:
		if strings.TrimSpace(it.itype) == "" {
			return "(unknown type)", true
		}
		return it.itype, true
	case DimAssignee:
		return NormalizeOwner("", it.assignee), true
	default:
		return "", false
	}
}
