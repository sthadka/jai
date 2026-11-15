package metrics

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Sprint eventual-resolution metric (spec §6 Sprint completion; §Metric coverage).
//
// This metric deliberately reports the ONLY sprint signal this substrate can prove:
// "eventual resolution of ever-members" — of all issues that were EVER a member of a
// sprint (reconstructed from the set-valued `Sprint` changelog membership, M14), what
// share is currently resolved under the active completion policy. It is NOT sprint
// completion, commitment-at-close, carry-over, or velocity: those require membership AND
// status captured at the sprint start/close instants plus baseline estimates, none of
// which are available here (boards/sprints carry no start/close dates or points under the
// current data). Those limitations are surfaced, never silently converted to success.
//
// Identity: sprints are keyed by their stable ID via Member.Key(), so two distinct sprint
// IDs that happen to share a display name stay distinct and an issue added/removed/re-added
// to the same sprint is counted once (EverMembers dedup policy). A membership whose sprint
// ID is unrecoverable is surfaced as an unknown-id member, never merged with equal-named
// resolved sprints. Membership is event-time; resolution is the CURRENT snapshot state.
const MetricSprintResolution = "sprint_resolution"

// sprintField is the changelog/snapshot field name whose set-valued membership models
// sprints. It is a Jira platform field name (like "status"/"assignee"), not tenant taxonomy.
const sprintField = "Sprint"

func init() { Register(sprintResolutionProvider{}) }

// SprintResolution is the per-sprint eventual-resolution tally. EverMembers is the number
// of distinct issues that were ever a member of this sprint (the denominator);
// ResolvedMembers is how many of them are currently resolved under the active completion
// policy (the numerator). Resolved reports whether the sprint's stable ID is known;
// unresolved-id sprints (Resolved == false) are surfaced separately and never merged with
// equal-named sprints.
type SprintResolution struct {
	Key             string // Member.Key() — stable dedup identity
	SprintID        string // stable sprint id ("" when the id is unrecoverable)
	SprintName      string // display name (need not be unique across distinct ids)
	Resolved        bool   // the sprint's stable id is known
	EverMembers     int    // distinct issues ever in this sprint (denominator)
	ResolvedMembers int    // of those, currently resolved under completion policy (numerator)
}

// Label is the stable display label for the sprint, carrying the id so distinct same-name
// sprints stay distinguishable and unknown-id sprints are explicit.
func (sr SprintResolution) Label() string {
	if sr.Resolved {
		return fmt.Sprintf("%s [%s]", sr.SprintName, sr.SprintID)
	}
	name := sr.SprintName
	if strings.TrimSpace(name) == "" {
		name = "(unnamed)"
	}
	return name + " (unknown id)"
}

// SprintDiagnostics carries the coverage/unknown diagnostics that must never be silently
// folded into success.
type SprintDiagnostics struct {
	Sprints               int // distinct sprints reported
	TotalEverMembers      int // total (issue, sprint) ever-memberships (denominator sum)
	TotalResolvedMembers  int // total resolved ever-memberships (numerator sum)
	InitialUnknownIssues  int // issues whose membership before first event is unprovable
	UnresolvedMemberships int // (issue, sprint) memberships with an unrecoverable sprint id
	NeverMemberIssues     int // issues that were never a sprint member
	GenuineDone           bool
}

// sprintResolutionProvider computes per-sprint eventual resolution of ever-members.
type sprintResolutionProvider struct{}

func (sprintResolutionProvider) Metric() string { return MetricSprintResolution }

func (sprintResolutionProvider) Compute(s *Service, q Query) (*Result, error) {
	r := s.newResult(q)
	if err := s.attachBuild(r); err != nil {
		return nil, err
	}

	sprints, diag, err := s.SprintResolutions(q)
	if err != nil {
		return nil, err
	}

	r.Cohort = "sprint eventual resolution of ever-members (event-time membership; current resolution)"
	r.Eligible = len(sprints)
	r.Counts = map[string]int{}
	for _, sr := range sprints {
		label := sr.Label()
		r.Counts[label+" \u2014 ever-members"] = sr.EverMembers
		r.Counts[label+" \u2014 resolved"] = sr.ResolvedMembers
	}

	// Overall share of ever-members currently resolved. Numerator/denominator are the
	// totals; Value is nil when the denominator is zero (undefined, never silently 0).
	ratio := &RatioResult{
		Numerator:   "resolved ever-members",
		Denominator: "ever-members",
		NumCount:    diag.TotalResolvedMembers,
		DenCount:    diag.TotalEverMembers,
	}
	if diag.TotalEverMembers > 0 {
		v := float64(diag.TotalResolvedMembers) / float64(diag.TotalEverMembers)
		ratio.Value = &v
	}
	r.Ratio = ratio

	r.exclude("initial membership unknown (incomplete history)", diag.InitialUnknownIssues)
	r.exclude("unresolved sprint id (membership)", diag.UnresolvedMemberships)
	r.exclude("never a sprint member", diag.NeverMemberIssues)

	if diag.GenuineDone {
		r.Policies["sprint_resolution"] = "genuine-done (done_resolutions filter applied)"
	} else {
		r.Policies["sprint_resolution"] = "includes non-delivery resolutions (done_resolutions unset)"
		r.warn("resolution counts include non-delivery resolutions (done_resolutions unset); the genuine-delivery variant is disabled")
	}

	r.warn("eventual resolution of ever-members — NOT sprint completion, commitment, carry-over, or velocity")
	r.warn("sprint start/close dates and story points are unavailable; commitment-at-close, carry-over, and velocity are not computed")
	if r.BuildGeneration == 0 {
		r.warn("no active fact generation; resolution counts are 0 until facts are built")
	}
	return r, nil
}

// SprintResolutions reconstructs the per-sprint eventual-resolution tallies for the query
// scope. Membership is reconstructed on demand from the `Sprint` changelog (event-time,
// M14) and joined to the CURRENT resolution state (fact_issue_flow + completion policy).
// Results are returned sorted by stable member key for deterministic output. This is the
// typed contract consumed by the downstream sprint consumers (M21/M22/M23, X03).
func (s *Service) SprintResolutions(q Query) ([]SprintResolution, SprintDiagnostics, error) {
	results, err := s.sprintMembership(q.Projects)
	if err != nil {
		return nil, SprintDiagnostics{}, err
	}

	rows, err := s.flowRows(q)
	if err != nil {
		return nil, SprintDiagnostics{}, err
	}
	genuine := s.pol.DoneResolutionsConfigured()
	resolvedSet := make(map[string]bool, len(rows))
	for i := range rows {
		fr := rows[i]
		if fr.Resolved == nil {
			continue
		}
		if genuine && !s.pol.IsDoneResolution(fr.Resolution) {
			continue
		}
		resolvedSet[fr.IssueKey] = true
	}

	agg := map[string]*SprintResolution{}
	diag := SprintDiagnostics{GenuineDone: genuine}
	for _, res := range results {
		if res.InitialUnknown {
			diag.InitialUnknownIssues++
		}
		members := res.EverMembers()
		if len(members) == 0 {
			diag.NeverMemberIssues++
			continue
		}
		resolved := resolvedSet[res.IssueKey]
		for _, m := range members {
			k := m.Key()
			sr := agg[k]
			if sr == nil {
				sr = &SprintResolution{Key: k, SprintID: m.ID, SprintName: m.Name, Resolved: m.Resolved}
				agg[k] = sr
			}
			sr.EverMembers++
			diag.TotalEverMembers++
			if resolved {
				sr.ResolvedMembers++
				diag.TotalResolvedMembers++
			}
			if !m.Resolved {
				diag.UnresolvedMemberships++
			}
		}
	}

	out := make([]SprintResolution, 0, len(agg))
	for _, sr := range agg {
		out = append(out, *sr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	diag.Sprints = len(out)
	return out, diag, nil
}

// sprintMembership reconstructs the `Sprint` membership-interval series for every issue in
// the query scope. It mirrors the build-time changelog loaders but produces on-demand
// membership histories (there is no second persistence schema for set-valued fields). The
// current snapshot (issues.sprint_id/sprint_name) seeds only never-changed issues.
func (s *Service) sprintMembership(projects []string) ([]MembershipResult, error) {
	iq := `SELECT key, project, created, changelog_synced_at, sprint_id, sprint_name FROM issues`
	var iargs []any
	if len(projects) > 0 {
		ph := make([]string, len(projects))
		for i, p := range projects {
			ph[i] = "?"
			iargs = append(iargs, p)
		}
		iq += " WHERE project IN (" + strings.Join(ph, ", ") + ")"
	}
	iq += " ORDER BY key"

	rows, err := s.db.Query(iq, iargs...)
	if err != nil {
		return nil, fmt.Errorf("load sprint issues: %w", err)
	}
	defer rows.Close()

	index := map[string]int{}
	var hists []MembershipHistory
	for rows.Next() {
		var (
			key, project                string
			created, synced, sprintName sql.NullString
			sprintID                    sql.NullInt64
		)
		if err := rows.Scan(&key, &project, &created, &synced, &sprintID, &sprintName); err != nil {
			return nil, err
		}
		h := MembershipHistory{
			Key:             key,
			Project:         project,
			Field:           sprintField,
			HistoryComplete: synced.Valid && strings.TrimSpace(synced.String) != "",
		}
		if t := nullTime(created); t != nil {
			h.CreatedAt = *t
		}
		if sprintID.Valid {
			h.CurrentValue = strconv.FormatInt(sprintID.Int64, 10)
		}
		if sprintName.Valid {
			h.CurrentString = sprintName.String
		}
		index[key] = len(hists)
		hists = append(hists, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := s.loadMembershipEvents(projects, sprintField, index, hists); err != nil {
		return nil, err
	}
	return ReplayMembershipAll(hists), nil
}

// loadMembershipEvents appends the set-valued changelog transitions for field into the
// per-issue membership histories. The query is covered by the idx_cl_tracked / changelog
// field indexes (leading field column).
func (s *Service) loadMembershipEvents(projects []string, field string, index map[string]int, hists []MembershipHistory) error {
	cq := `SELECT id, issue_key, from_value, from_string, to_value, to_string, changed_at
	         FROM changelog WHERE field = ?`
	cargs := []any{field}
	if len(projects) > 0 {
		ph := make([]string, len(projects))
		for i, p := range projects {
			ph[i] = "?"
			cargs = append(cargs, p)
		}
		cq += " AND issue_key IN (SELECT key FROM issues WHERE project IN (" + strings.Join(ph, ", ") + "))"
	}
	cq += " ORDER BY issue_key, changed_at"

	crows, err := s.db.Query(cq, cargs...)
	if err != nil {
		return fmt.Errorf("load membership events: %w", err)
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
		ev := MembershipEvent{
			ID:         id,
			FromValue:  fromV.String,
			FromString: fromS.String,
			ToValue:    toV.String,
			ToString:   toS.String,
		}
		if changed.Valid && strings.TrimSpace(changed.String) != "" {
			ev.ChangedAt = parseDBTime(changed.String)
		}
		hists[i].Events = append(hists[i].Events, ev)
	}
	return crows.Err()
}
