package metrics

import (
	"sort"
	"strings"
	"time"
)

// StatusEvent is a single `status`-field changelog transition. ID is the stable changelog
// row identity used as the deterministic tie-breaker for equal timestamps.
type StatusEvent struct {
	ID         string
	ChangedAt  time.Time
	FromValue  string // Jira status ID before
	FromString string // status name before
	ToValue    string // Jira status ID after
	ToString   string // status name after
}

// IssueHistory is the replay input for one issue: its creation time, status changelog,
// whether its history is provably complete, and its current snapshot (used for the open
// interval's category fallback and never-changed seeding).
type IssueHistory struct {
	Key             string
	Project         string
	CreatedAt       time.Time
	Events          []StatusEvent
	HistoryComplete bool

	CurrentStatusID       string
	CurrentStatus         string
	CurrentStatusCategory string
}

// Interval is one half-open status dwell [EnteredAt, ExitedAt). An open interval has
// ExitedAt == nil and Open == true; its duration is computed against an explicit as_of.
type Interval struct {
	IssueKey   string
	ValueID    string // status ID (stable identity; empty when unseeded/unknown history)
	Value      string // status name (display)
	Stage      string
	Provenance string
	EnteredAt  time.Time
	ExitedAt   *time.Time
	Open       bool
}

// Duration returns the dwell length. Open intervals age against asOf (never a sync-frozen
// now); a zero-or-negative span clamps to zero so no negative duration is ever produced.
func (iv Interval) Duration(asOf time.Time) time.Duration {
	end := asOf
	if iv.ExitedAt != nil {
		end = *iv.ExitedAt
	}
	d := end.Sub(iv.EnteredAt)
	if d < 0 {
		return 0
	}
	return d
}

// ReplayResult is the reconstructed interval series and summary flags for one issue.
type ReplayResult struct {
	IssueKey  string
	Project   string
	Intervals []Interval

	CurrentStage   string // stage of the open (final) interval
	CurrentValue   string
	CurrentValueID string

	KnownCount   int
	UnknownCount int
	HasUnknown   bool

	Ambiguous         bool // an equal-timestamp tie was resolved by changelog id
	Seeded            bool // the created->first-change interval was seeded as a known status
	NeverChanged      bool // provably never changed (complete history, no events)
	HasActiveInterval bool // at least one interval is in a WIP stage (cycle-time evidence)
}

// ReplayStatus reconstructs the deterministic, idempotent status-interval series for one
// issue under the given policy and as_of. Equal timestamps are ordered by stable changelog
// id, no-op transitions are suppressed, the leading created->first-change interval is
// seeded only when history is complete (otherwise it is an explicit unknown period), and
// the final interval is left open for as_of ageing.
func ReplayStatus(issue IssueHistory, pol *Policy, asOf time.Time) ReplayResult {
	res := ReplayResult{IssueKey: issue.Key, Project: issue.Project}

	events := make([]StatusEvent, 0, len(issue.Events))
	for _, e := range issue.Events {
		if isNoOp(e) {
			continue // suppress genuine no-ops before building intervals
		}
		events = append(events, e)
	}
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].ChangedAt.Equal(events[j].ChangedAt) {
			return events[i].ChangedAt.Before(events[j].ChangedAt)
		}
		return events[i].ID < events[j].ID
	})
	res.Ambiguous = hasEqualTimestamps(events)

	if len(events) == 0 {
		res.Intervals = append(res.Intervals, noEventInterval(issue, pol))
		if issue.HistoryComplete {
			res.NeverChanged = true
		}
		finalize(&res, asOf, pol)
		return res
	}

	// Leading created -> first change interval.
	first := events[0]
	if issue.CreatedAt.Before(first.ChangedAt) {
		res.Intervals = append(res.Intervals, leadingInterval(issue, first, pol, &res))
	}

	for i := range events {
		e := events[i]
		iv := Interval{
			IssueKey:  issue.Key,
			ValueID:   e.ToValue,
			Value:     e.ToString,
			EnteredAt: e.ChangedAt,
		}
		isLast := i == len(events)-1
		if isLast {
			iv.Open = true
			iv.ExitedAt = nil
			m := pol.StageFor(e.ToValue, e.ToString, openCategory(issue, e))
			iv.Stage, iv.Provenance = m.Stage, m.Provenance
		} else {
			next := events[i+1].ChangedAt
			iv.ExitedAt = &next
			m := pol.StageFor(e.ToValue, e.ToString, "") // historical: no category
			iv.Stage, iv.Provenance = m.Stage, m.Provenance
		}
		res.Intervals = append(res.Intervals, iv)
	}

	finalize(&res, asOf, pol)
	return res
}

// ReplayStatusAll replays a batch deterministically (input order preserved).
func ReplayStatusAll(issues []IssueHistory, pol *Policy, asOf time.Time) []ReplayResult {
	out := make([]ReplayResult, 0, len(issues))
	for _, iss := range issues {
		out = append(out, ReplayStatus(iss, pol, asOf))
	}
	return out
}

func isNoOp(e StatusEvent) bool {
	return e.FromValue == e.ToValue && e.FromString == e.ToString
}

func hasEqualTimestamps(events []StatusEvent) bool {
	for i := 1; i < len(events); i++ {
		if events[i].ChangedAt.Equal(events[i-1].ChangedAt) {
			return true
		}
	}
	return false
}

// noEventInterval builds the single open interval for an issue with no status changes.
func noEventInterval(issue IssueHistory, pol *Policy) Interval {
	iv := Interval{IssueKey: issue.Key, EnteredAt: issue.CreatedAt, Open: true}
	if issue.HistoryComplete {
		// Provably never changed: current status has held since creation.
		iv.ValueID = issue.CurrentStatusID
		iv.Value = issue.CurrentStatus
		m := pol.StageFor(issue.CurrentStatusID, issue.CurrentStatus, issue.CurrentStatusCategory)
		iv.Stage, iv.Provenance = m.Stage, m.Provenance
	} else {
		// Unavailable history: cannot assert the status since creation.
		iv.Stage = StageUnknown
		iv.Provenance = ProvUnknown
	}
	return iv
}

// leadingInterval builds the created->first-change interval, seeding it as a known status
// only when history is complete.
func leadingInterval(issue IssueHistory, first StatusEvent, pol *Policy, res *ReplayResult) Interval {
	iv := Interval{
		IssueKey:  issue.Key,
		EnteredAt: issue.CreatedAt,
		ExitedAt:  cloneTime(first.ChangedAt),
	}
	if issue.HistoryComplete && (strings.TrimSpace(first.FromString) != "" || strings.TrimSpace(first.FromValue) != "") {
		iv.ValueID = first.FromValue
		iv.Value = first.FromString
		m := pol.StageFor(first.FromValue, first.FromString, "")
		iv.Stage, iv.Provenance = m.Stage, m.Provenance
		res.Seeded = true
	} else {
		// Partial/stale history: never extend unverified history as known.
		iv.Stage = StageUnknown
		iv.Provenance = ProvUnknown
	}
	return iv
}

// openCategory returns the current statusCategory for the open interval's category
// fallback, but only when the final changelog status agrees with the current snapshot —
// otherwise applying today's category to a stale status would mis-attribute the stage.
func openCategory(issue IssueHistory, last StatusEvent) string {
	curID := strings.TrimSpace(issue.CurrentStatusID)
	curName := strings.TrimSpace(issue.CurrentStatus)
	if curID != "" && curID == strings.TrimSpace(last.ToValue) {
		return issue.CurrentStatusCategory
	}
	if curName != "" && curName == strings.TrimSpace(last.ToString) {
		return issue.CurrentStatusCategory
	}
	return ""
}

func finalize(res *ReplayResult, asOf time.Time, pol *Policy) {
	for _, iv := range res.Intervals {
		if iv.Stage == StageUnknown {
			res.UnknownCount++
		} else {
			res.KnownCount++
		}
		if pol.IsWIPStage(iv.Stage) {
			res.HasActiveInterval = true
		}
	}
	res.HasUnknown = res.UnknownCount > 0
	if n := len(res.Intervals); n > 0 {
		open := res.Intervals[n-1]
		res.CurrentStage = open.Stage
		res.CurrentValue = open.Value
		res.CurrentValueID = open.ValueID
	}
	_ = asOf // as_of drives Interval.Duration; nothing to clip at replay time.
}

func cloneTime(t time.Time) *time.Time { return &t }
