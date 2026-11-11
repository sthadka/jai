package metrics

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Provenance values specific to scalar (non-status) interval replay. The status path uses
// the stage-mapping provenances (ProvID/ProvName/ProvCategory/ProvUnknown); scalar fields
// carry no stage, so their provenance records where the value itself came from.
const (
	ProvEvent    = "event"    // value taken directly from a changelog transition
	ProvSnapshot = "snapshot" // value taken from the current issue snapshot (never-changed)
)

// setValuedFields are the Jira platform fields whose changelog carries a *set* of members
// per event, not a single scalar value. Scalar interval replay is invalid for these; they
// are modeled as membership intervals (see membership.go). These are Jira field names (a
// fixed platform vocabulary like statusCategory), not tenant taxonomy, so matching them
// here does not violate the no-tenant-literal invariant.
var setValuedFields = map[string]bool{
	"sprint":      true,
	"labels":      true,
	"components":  true,
	"component":   true,
	"component/s": true,
}

// IsSetValuedField reports whether a changelog field name denotes a set-valued membership
// field (Sprint, labels, components). Matching is case-insensitive.
func IsSetValuedField(field string) bool {
	return setValuedFields[strings.ToLower(strings.TrimSpace(field))]
}

// ErrSetValuedField is returned by ReplayScalar when asked to replay a set-valued
// membership field. Such fields must be modeled with ReplayMembership (membership.go), not
// coerced into a single-valued interval series that would silently publish misleading
// dwell facts.
var ErrSetValuedField = errors.New("metrics: scalar replay invalid for set-valued field")

// ScalarEvent is a single changelog transition of a scalar tracked field. ID is the stable
// changelog row identity used as the deterministic tie-breaker for equal timestamps.
type ScalarEvent struct {
	ID         string
	ChangedAt  time.Time
	FromValue  string // stable id before (e.g. priority id, assignee accountId)
	FromString string // display value before
	ToValue    string // stable id after
	ToString   string // display value after
}

// ScalarHistory is the replay input for one issue's single scalar tracked field: its
// creation time, the field's changelog, whether history is provably complete, and the
// current snapshot value used for never-changed seeding.
//
// CurrentKnown distinguishes "the current value is genuinely empty" (column present, value
// blank — e.g. a known-unassigned issue) from "we have no current value for this field"
// (column absent — a coverage gap). Only the former can seed a known never-changed
// interval; the latter stays unknown.
type ScalarHistory struct {
	Key             string
	Project         string
	Field           string
	CreatedAt       time.Time
	Events          []ScalarEvent
	HistoryComplete bool

	CurrentValueID string
	CurrentValue   string
	CurrentKnown   bool
}

// ScalarInterval is one half-open dwell [EnteredAt, ExitedAt) during which a scalar field
// held a single value. Known == false marks an unknown gap (incomplete history before the
// first recorded change, or an absent current value); such intervals carry
// Provenance == ProvUnknown and must never be counted as a real value. An empty Value with
// Known == true is a genuine empty/sentinel value (e.g. unassigned), not a gap.
type ScalarInterval struct {
	IssueKey   string
	Field      string
	ValueID    string // stable identity (names churn); empty when unknown
	Value      string // display value
	Known      bool
	Provenance string
	EnteredAt  time.Time
	ExitedAt   *time.Time
	Open       bool
}

// Duration returns the dwell length. Open intervals age against asOf (never a sync-frozen
// now); a zero-or-negative span clamps to zero so no negative duration is ever produced.
func (iv ScalarInterval) Duration(asOf time.Time) time.Duration {
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

// ScalarReplayResult is the reconstructed scalar-interval series and summary flags for one
// issue/field.
type ScalarReplayResult struct {
	IssueKey  string
	Project   string
	Field     string
	Intervals []ScalarInterval

	CurrentValue   string // value of the open (final) interval
	CurrentValueID string

	KnownCount   int
	UnknownCount int
	HasUnknown   bool

	Ambiguous    bool // an equal-timestamp tie was resolved by changelog id
	Seeded       bool // the created->first-change interval was seeded as a known value
	NeverChanged bool // provably never changed (complete history, no events)
}

// ScalarTrackedFields returns the configured tracked fields eligible for scalar interval
// replay: every tracked field except "status" (which carries status-specific stage
// semantics and is replayed by ReplayStatus) and the set-valued membership fields (Sprint,
// labels, components), which are modeled by ReplayMembership. Order follows the configured
// tracked_fields for deterministic builds.
func ScalarTrackedFields(pol *Policy) []string {
	var out []string
	for _, f := range pol.TrackedFields() {
		if strings.EqualFold(strings.TrimSpace(f), "status") {
			continue
		}
		if IsSetValuedField(f) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// ReplayScalar reconstructs the deterministic, idempotent interval series for one scalar
// tracked field of one issue. Equal timestamps are ordered by stable changelog id, no-op
// transitions are suppressed, the leading created->first-change interval is seeded as a
// known value only when history is complete (otherwise it is an explicit unknown period),
// and the final interval is left open for as_of ageing.
//
// It refuses set-valued membership fields with ErrSetValuedField: coercing a set into a
// scalar dwell would silently publish misleading intervals.
func ReplayScalar(hist ScalarHistory, pol *Policy, asOf time.Time) (ScalarReplayResult, error) {
	if IsSetValuedField(hist.Field) {
		return ScalarReplayResult{}, fmt.Errorf("%w: %q", ErrSetValuedField, hist.Field)
	}

	res := ScalarReplayResult{IssueKey: hist.Key, Project: hist.Project, Field: hist.Field}

	events := make([]ScalarEvent, 0, len(hist.Events))
	for _, e := range hist.Events {
		if isNoOpScalar(e) {
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
	res.Ambiguous = hasEqualTimestampsScalar(events)

	if len(events) == 0 {
		res.Intervals = append(res.Intervals, noEventScalarInterval(hist))
		if hist.HistoryComplete {
			res.NeverChanged = true
		}
		finalizeScalar(&res)
		return res, nil
	}

	// Leading created -> first change interval.
	first := events[0]
	if hist.CreatedAt.Before(first.ChangedAt) {
		res.Intervals = append(res.Intervals, leadingScalarInterval(hist, first, &res))
	}

	for i := range events {
		e := events[i]
		iv := ScalarInterval{
			IssueKey:   hist.Key,
			Field:      hist.Field,
			ValueID:    e.ToValue,
			Value:      e.ToString,
			Known:      true,
			Provenance: ProvEvent,
			EnteredAt:  e.ChangedAt,
		}
		if i == len(events)-1 {
			iv.Open = true
		} else {
			next := events[i+1].ChangedAt
			iv.ExitedAt = &next
		}
		res.Intervals = append(res.Intervals, iv)
	}

	finalizeScalar(&res)
	return res, nil
}

// ReplayScalarAll replays a batch deterministically (input order preserved). It stops at
// the first set-valued field, which is a programming error (callers filter via
// ScalarTrackedFields / IsSetValuedField before replay).
func ReplayScalarAll(hists []ScalarHistory, pol *Policy, asOf time.Time) ([]ScalarReplayResult, error) {
	out := make([]ScalarReplayResult, 0, len(hists))
	for _, h := range hists {
		r, err := ReplayScalar(h, pol, asOf)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func isNoOpScalar(e ScalarEvent) bool {
	return e.FromValue == e.ToValue && e.FromString == e.ToString
}

func hasEqualTimestampsScalar(events []ScalarEvent) bool {
	for i := 1; i < len(events); i++ {
		if events[i].ChangedAt.Equal(events[i-1].ChangedAt) {
			return true
		}
	}
	return false
}

// noEventScalarInterval builds the single open interval for an issue whose field never
// changed. With complete history and a known current value it is a known interval held
// since creation; otherwise it is an explicit unknown period.
func noEventScalarInterval(hist ScalarHistory) ScalarInterval {
	iv := ScalarInterval{IssueKey: hist.Key, Field: hist.Field, EnteredAt: hist.CreatedAt, Open: true}
	if hist.HistoryComplete && hist.CurrentKnown {
		iv.ValueID = hist.CurrentValueID
		iv.Value = hist.CurrentValue
		iv.Known = true
		iv.Provenance = ProvSnapshot
	} else {
		iv.Known = false
		iv.Provenance = ProvUnknown
	}
	return iv
}

// leadingScalarInterval builds the created->first-change interval. With complete history
// the pre-change value is provably the from-value (which may legitimately be empty, e.g.
// unassigned); with partial history the period is unknown.
func leadingScalarInterval(hist ScalarHistory, first ScalarEvent, res *ScalarReplayResult) ScalarInterval {
	iv := ScalarInterval{
		IssueKey:  hist.Key,
		Field:     hist.Field,
		EnteredAt: hist.CreatedAt,
		ExitedAt:  cloneTime(first.ChangedAt),
	}
	if hist.HistoryComplete {
		iv.ValueID = first.FromValue
		iv.Value = first.FromString
		iv.Known = true
		iv.Provenance = ProvEvent
		res.Seeded = true
	} else {
		iv.Known = false
		iv.Provenance = ProvUnknown
	}
	return iv
}

func finalizeScalar(res *ScalarReplayResult) {
	for _, iv := range res.Intervals {
		if iv.Known {
			res.KnownCount++
		} else {
			res.UnknownCount++
		}
	}
	res.HasUnknown = res.UnknownCount > 0
	if n := len(res.Intervals); n > 0 {
		open := res.Intervals[n-1]
		res.CurrentValue = open.Value
		res.CurrentValueID = open.ValueID
	}
}
