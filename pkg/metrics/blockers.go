package metrics

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// MetricBlockerAgeing is the blocker-ageing metric. It is deliberately NOT a single
// "blocked duration" number: Jira has no clean blocked state and issue_links carry no
// add/remove history, so this metric publishes two separate, explicitly-labelled proxy
// signals instead of fabricating one (spec §Blocker ageing):
//
//  1. relationship-age — the age of issues that are *currently* dependency-linked via a
//     configured blocker_link_type. The link shows a relationship that exists now, not when
//     blocking began, so this is the age of the blocked issue, never a blocked-duration.
//  2. waiting-dwell — per-issue time spent in the `waiting` stage (status intervals),
//     a proxy for queueing/blockage, never proven blocked time.
const MetricBlockerAgeing = "blocker_ageing"

func init() { Register(blockerAgeingProvider{}) }

// blockerLink is one blocker relationship: the blocked issue (issue_key) depends on /
// is blocked by linkedKey. Direction is normalized by matching the configured directional
// link-type phrases (which name the blocked side), so the blocked issue is always
// issue_key and the inverse "blocks" row — a different phrase — never matches.
type blockerLink struct {
	blockedKey   string
	blockedCreat *time.Time
	linkedKey    string
	linkedSnap   string // captured linked_status snapshot string (may be stale)
	linkedCur    string // blocker's current status from the local issues row (fresh)
	linkedCurCat string // blocker's current status category (fresh), for stale-link detection
	linkedLocal  bool   // true when the blocker exists as a local issue (fresh state available)
}

// loadBlockerLinks returns every blocker relationship whose link_type is a configured
// blocker type, scoped to the blocked issue's project. The blocked issue's created date and
// the blocker's fresh local state are joined in so the caller can prefer fresh state over
// the captured snapshot without a second query.
func (s *Service) loadBlockerLinks(projects, linkTypes []string) ([]blockerLink, error) {
	if len(linkTypes) == 0 {
		return nil, nil
	}
	ph := make([]string, len(linkTypes))
	args := make([]any, 0, len(linkTypes)+len(projects))
	for i, lt := range linkTypes {
		ph[i] = "?"
		args = append(args, lt)
	}
	q := `SELECT il.issue_key, il.linked_key, il.linked_status,
	             bi.created,
	             li.status, li.status_category, li.key
	        FROM issue_links il
	        JOIN issues bi ON bi.key = il.issue_key
	        LEFT JOIN issues li ON li.key = il.linked_key
	       WHERE il.link_type IN (` + strings.Join(ph, ", ") + `)`
	if len(projects) > 0 {
		pph := make([]string, len(projects))
		for i, p := range projects {
			pph[i] = "?"
			args = append(args, p)
		}
		q += " AND bi.project IN (" + strings.Join(pph, ", ") + ")"
	}
	q += " ORDER BY il.issue_key, il.linked_key"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("load blocker links: %w", err)
	}
	defer rows.Close()

	var out []blockerLink
	for rows.Next() {
		var (
			blockedKey, linkedKey       string
			linkedSnap, created         sql.NullString
			linkedCur, linkedCat, liKey sql.NullString
		)
		if err := rows.Scan(&blockedKey, &linkedKey, &linkedSnap, &created, &linkedCur, &linkedCat, &liKey); err != nil {
			return nil, err
		}
		bl := blockerLink{
			blockedKey:   blockedKey,
			blockedCreat: nullTime(created),
			linkedKey:    linkedKey,
			linkedSnap:   strings.TrimSpace(linkedSnap.String),
			linkedCur:    strings.TrimSpace(linkedCur.String),
			linkedCurCat: strings.TrimSpace(linkedCat.String),
			linkedLocal:  liKey.Valid,
		}
		out = append(out, bl)
	}
	return out, rows.Err()
}

// blockerAgeingProvider computes the two proxy signals. The relationship-age histogram runs
// off issue_links + issues (no fact store needed); the waiting-dwell distribution reads the
// active status-interval generation. Both are labelled proxies in the result.
type blockerAgeingProvider struct{}

func (blockerAgeingProvider) Metric() string { return MetricBlockerAgeing }

func (blockerAgeingProvider) Compute(s *Service, q Query) (*Result, error) {
	asOf := s.asOf(q)
	r := s.newResult(q)
	r.Cohort = "currently dependency-linked issues (relationship-age proxy) + waiting-stage dwell proxy"
	r.Counts = map[string]int{}
	for _, b := range agedBuckets {
		r.Counts["age:"+b.label] = 0
	}
	r.warn("relationship-age is a PROXY: issue_links show a dependency that exists now, not when blocking began; no link add/remove history exists, so this is not blocked-duration")
	r.warn("waiting-dwell is a PROXY for blockage (time in the waiting stage), not proven blocked time")

	// --- Signal 1: relationship-age of currently dependency-linked issues. ---
	linkTypes := s.pol.BlockerLinkTypes()
	if len(linkTypes) == 0 {
		r.warn("blocker_link_types is unset; the relationship-age signal is unavailable (no blocker link set is guessed)")
	} else {
		links, err := s.loadBlockerLinks(q.Projects, linkTypes)
		if err != nil {
			return nil, err
		}
		r.Counts["blocker_links_total"] = len(links)
		// Deduplicate blocked issues: an issue with multiple blockers, or captured from the
		// inverse direction, is one blocked issue, counted once in the age histogram.
		type blocked struct {
			created *time.Time
		}
		dedup := map[string]blocked{}
		var fresh, snapshot, missing, resolvedBlockers int
		for _, l := range links {
			dedup[l.blockedKey] = blocked{created: l.blockedCreat}
			// Prefer the blocker's fresh local state over the captured link snapshot;
			// keep missing/untrusted state visible rather than dropping it.
			switch {
			case l.linkedLocal:
				fresh++
				if strings.EqualFold(l.linkedCurCat, "done") {
					resolvedBlockers++ // blocker is resolved but still linked: likely-stale link
				}
			case l.linkedSnap != "":
				snapshot++
			default:
				missing++
			}
		}
		var unknownAge int
		for _, b := range dedup {
			if b.created == nil {
				unknownAge++
				continue
			}
			ageDays := int(asOf.Sub(*b.created).Hours() / 24)
			if ageDays < 0 {
				ageDays = 0
			}
			r.Counts["age:"+bucketFor(ageDays)]++
		}
		r.Eligible = len(dedup)
		r.exclude("unknown_age", unknownAge)
		r.Counts["blocker_state:fresh"] = fresh
		r.Counts["blocker_state:stale_snapshot"] = snapshot
		r.Counts["blocker_state:missing"] = missing
		r.Counts["blocker_resolved"] = resolvedBlockers
	}

	// --- Signal 2: waiting-stage dwell proxy (per issue, against as_of). ---
	if err := s.attachBuild(r); err != nil {
		return nil, err
	}
	intervals, err := s.store.Intervals(q.Projects, "status")
	if err != nil {
		return nil, err
	}
	dwellDays := map[string]float64{}
	for _, iv := range intervals {
		if iv.Stage != StageWaiting {
			continue
		}
		dwellDays[iv.IssueKey] += intervalDurationDays(iv, asOf)
	}
	samples := make([]float64, 0, len(dwellDays))
	for _, d := range dwellDays {
		samples = append(samples, d)
	}
	r.Distribution = distribution(samples)
	r.Counts["waiting_dwell_issues"] = len(samples)
	if len(samples) == 0 {
		r.warn("no waiting-stage dwell found (no waiting intervals in the active generation, or no fact store built)")
	}
	return r, nil
}

// intervalDurationDays is an interval's dwell in days: exited-entered for a closed
// interval, or as_of-entered for an open one. Reversed/negative spans clamp to zero.
func intervalDurationDays(iv IntervalRow, asOf time.Time) float64 {
	end := asOf
	if iv.ExitedAt != nil {
		end = *iv.ExitedAt
	}
	d := end.Sub(iv.EnteredAt)
	if d < 0 {
		return 0
	}
	return d.Hours() / 24
}
