package metrics

import (
	"sort"
	"strings"
	"time"
)

// Set-valued membership modeling (Sprint, labels, components).
//
// These fields are multi-valued and time-varying: one changelog event carries the whole
// before/after *set*, not a single scalar transition, so they cannot be replayed by
// ReplayScalar (which would silently collapse a set into a misleading single-value dwell).
// Membership is reconstructed on demand from the changelog — no second persistence schema —
// and exposes two semantics consumed downstream (M15 historical cohorts, M18 eventual
// resolution, X03 sprint metrics):
//
//   - membership-at-time: MembershipResult.MembersAt(t) — the set at an event time, never
//     copied from the current issue snapshot.
//   - ever-members: MembershipResult.EverMembers() — every member the issue was ever in,
//     deduplicated so repeated add/remove/re-add does not inflate counts.
//
// Counting / allocation policy (avoids join fan-out): membership is keyed per distinct
// (issue, member) pair. An issue added and removed from the same sprint many times is ONE
// ever-member; an issue in several components contributes to each component's count without
// inflating any single-issue total. EverMemberCounts applies this policy across issues.
//
// Identity: Sprint and components are keyed by their stable ID (names churn and need not be
// unique — two different sprint IDs may share a name and MUST stay distinct). When an ID is
// unrecoverable (missing/malformed), the member is surfaced as unknown (Resolved == false)
// and never merged with equal-named members. Labels have no ID; the label text is the
// identity.

// Member is one element of a set-valued field's membership. ID is the stable identity
// (sprint id, component id, or the label text); Name is the display value. Resolved is true
// when a stable ID is known; an unresolved member (Resolved == false) had no recoverable ID
// and is reported as unknown rather than merged with equal-named members.
type Member struct {
	ID       string
	Name     string
	Resolved bool
}

// Key is the stable dedup/identity key for a member. Resolved members key by ID (so two
// sprints sharing a name stay distinct); unresolved members key by name under a distinct
// namespace so they never collide with a resolved member of the same name.
func (m Member) Key() string {
	if m.Resolved {
		return "id:" + m.ID
	}
	return "name?:" + m.Name
}

// MembershipEvent is one changelog transition of a set-valued field. From/To carry the full
// membership set before/after the change; *Value holds stable ids, *String the display
// names (comma-separated for Sprint/components, space-separated for labels). ID is the
// stable changelog row identity used as the equal-timestamp tie-breaker.
type MembershipEvent struct {
	ID         string
	ChangedAt  time.Time
	FromValue  string
	FromString string
	ToValue    string
	ToString   string
}

// MembershipHistory is the replay input for one issue's single set-valued field.
// CurrentValue/CurrentString hold the current snapshot set, used only when there are no
// changelog events (never-changed); historical membership always comes from events.
type MembershipHistory struct {
	Key             string
	Project         string
	Field           string
	CreatedAt       time.Time
	Events          []MembershipEvent
	HistoryComplete bool

	CurrentValue  string
	CurrentString string
}

// MembershipInterval is one half-open span [EnteredAt, ExitedAt) during which the issue was
// a member of Member. Open == true (ExitedAt == nil) means currently a member. EnteredKnown
// is false when the entry time is not provable (incomplete history before the first recorded
// event) — the membership is real but its start is a lower bound, not an asserted instant.
type MembershipInterval struct {
	IssueKey     string
	Field        string
	Member       Member
	EnteredAt    time.Time
	ExitedAt     *time.Time
	Open         bool
	EnteredKnown bool
}

// MembershipResult is the reconstructed membership-interval series for one issue/field.
// InitialUnknown is true when history is incomplete, so membership before the first recorded
// event cannot be proven — the initial state is unknown, NOT an empty set.
type MembershipResult struct {
	IssueKey  string
	Project   string
	Field     string
	Intervals []MembershipInterval

	InitialUnknown bool
	Ambiguous      bool // an equal-timestamp tie was resolved by changelog id
}

// ReplayMembership reconstructs the deterministic membership-interval series for one issue's
// set-valued field. Equal timestamps order by stable changelog id, no-op events (set
// unchanged) are dropped, removals and re-additions are retained as separate intervals, and
// the initial set is taken from the first event's from-set (known only with complete
// history). It never copies the current snapshot onto historical intervals.
func ReplayMembership(hist MembershipHistory) MembershipResult {
	res := MembershipResult{IssueKey: hist.Key, Project: hist.Project, Field: hist.Field}

	events := append([]MembershipEvent(nil), hist.Events...)
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].ChangedAt.Equal(events[j].ChangedAt) {
			return events[i].ChangedAt.Before(events[j].ChangedAt)
		}
		return events[i].ID < events[j].ID
	})
	events = dropMembershipNoOps(hist.Field, events)
	res.Ambiguous = hasEqualMembershipTimestamps(events)

	active := map[string]*MembershipInterval{}
	var out []*MembershipInterval

	open := func(m Member, at time.Time, known bool) {
		iv := &MembershipInterval{
			IssueKey: hist.Key, Field: hist.Field, Member: m,
			EnteredAt: at, Open: true, EnteredKnown: known,
		}
		active[m.Key()] = iv
		out = append(out, iv)
	}
	closeM := func(key string, at time.Time) {
		if iv, ok := active[key]; ok {
			t := at
			iv.ExitedAt = &t
			iv.Open = false
			delete(active, key)
		}
	}

	// Initial membership (creation -> first event, or whole history when no events).
	if len(events) > 0 {
		for _, m := range parseMembers(hist.Field, events[0].FromValue, events[0].FromString) {
			open(m, hist.CreatedAt, hist.HistoryComplete)
		}
		if !hist.HistoryComplete {
			res.InitialUnknown = true
		}
	} else {
		for _, m := range parseMembers(hist.Field, hist.CurrentValue, hist.CurrentString) {
			open(m, hist.CreatedAt, hist.HistoryComplete)
		}
		if !hist.HistoryComplete {
			res.InitialUnknown = true
		}
	}

	for _, e := range events {
		to := parseMembers(hist.Field, e.ToValue, e.ToString)
		toKeys := make(map[string]bool, len(to))
		for _, m := range to {
			toKeys[m.Key()] = true
		}
		// Removals: active members no longer in the to-set.
		for key := range active {
			if !toKeys[key] {
				closeM(key, e.ChangedAt)
			}
		}
		// Additions / re-additions: to-set members not currently active.
		for _, m := range to {
			if _, ok := active[m.Key()]; !ok {
				open(m, e.ChangedAt, true)
			}
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].EnteredAt.Equal(out[j].EnteredAt) {
			return out[i].EnteredAt.Before(out[j].EnteredAt)
		}
		if out[i].Member.Key() != out[j].Member.Key() {
			return out[i].Member.Key() < out[j].Member.Key()
		}
		return exitBefore(out[i].ExitedAt, out[j].ExitedAt)
	})
	res.Intervals = make([]MembershipInterval, len(out))
	for i, iv := range out {
		res.Intervals[i] = *iv
	}
	return res
}

// ReplayMembershipAll replays a batch deterministically (input order preserved).
func ReplayMembershipAll(hists []MembershipHistory) []MembershipResult {
	out := make([]MembershipResult, 0, len(hists))
	for _, h := range hists {
		out = append(out, ReplayMembership(h))
	}
	return out
}

// MembersAt returns the distinct members the issue belonged to at instant t, using
// half-open [entered, exited) semantics (a membership that ended exactly at t is excluded).
func (r MembershipResult) MembersAt(t time.Time) []Member {
	var out []Member
	seen := map[string]bool{}
	for _, iv := range r.Intervals {
		if iv.EnteredAt.After(t) {
			continue
		}
		if iv.ExitedAt != nil && !iv.ExitedAt.After(t) {
			continue
		}
		if !seen[iv.Member.Key()] {
			seen[iv.Member.Key()] = true
			out = append(out, iv.Member)
		}
	}
	sortMembers(out)
	return out
}

// EverMembers returns every member the issue was ever in, deduplicated by stable key so an
// add/remove/re-add cycle counts once (duplicate-membership policy).
func (r MembershipResult) EverMembers() []Member {
	var out []Member
	seen := map[string]bool{}
	for _, iv := range r.Intervals {
		if !seen[iv.Member.Key()] {
			seen[iv.Member.Key()] = true
			out = append(out, iv.Member)
		}
	}
	sortMembers(out)
	return out
}

// MemberTally is a per-member issue count under the no-fan-out counting policy.
type MemberTally struct {
	Member Member
	Issues int
}

// EverMemberCounts aggregates, across issues, the number of distinct issues that were ever a
// member of each member key. Each issue is counted at most once per member regardless of
// repeated membership, and an issue in several members contributes to each member's count
// without inflating any single-issue total — i.e. the membership join never fans out issue
// totals. Keyed by Member.Key().
func EverMemberCounts(results []MembershipResult) map[string]MemberTally {
	tally := map[string]MemberTally{}
	for _, r := range results {
		for _, m := range r.EverMembers() { // EverMembers is already per-issue deduped
			k := m.Key()
			t := tally[k]
			if t.Issues == 0 {
				t.Member = m
			}
			t.Issues++
			tally[k] = t
		}
	}
	return tally
}

// --- parsing ---

// parseMembers parses a membership set from a changelog value/display pair for the given
// field. Labels are a space-separated token list where the text is its own identity;
// Sprint/components (and any other set-valued field) are comma-separated, zipping ids from
// value with names from display.
func parseMembers(field, value, display string) []Member {
	if strings.EqualFold(strings.TrimSpace(field), "labels") {
		return parseTokenMembers(firstNonEmpty(display, value))
	}
	return parseIDNameMembers(value, display)
}

// parseTokenMembers parses a whitespace-separated token list (labels). Each token is a
// resolved member whose id and name are the token itself.
func parseTokenMembers(s string) []Member {
	var out []Member
	seen := map[string]bool{}
	for _, tok := range strings.Fields(s) {
		m := Member{ID: tok, Name: tok, Resolved: true}
		if seen[m.Key()] {
			continue
		}
		seen[m.Key()] = true
		out = append(out, m)
	}
	return out
}

// parseIDNameMembers parses comma-separated ids (value) zipped with comma-separated names
// (display). A member with a present id is resolved; an entry with only a name is unresolved
// (unknown id) and never merged with equal-named resolved members.
func parseIDNameMembers(value, display string) []Member {
	ids := splitList(value)
	names := splitList(display)
	n := len(ids)
	if len(names) > n {
		n = len(names)
	}
	var out []Member
	seen := map[string]bool{}
	for i := range n {
		var id, name string
		if i < len(ids) {
			id = ids[i]
		}
		if i < len(names) {
			name = names[i]
		}
		if id == "" && name == "" {
			continue
		}
		m := Member{ID: id, Name: name, Resolved: id != ""}
		if seen[m.Key()] {
			continue
		}
		seen[m.Key()] = true
		out = append(out, m)
	}
	return out
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func dropMembershipNoOps(field string, events []MembershipEvent) []MembershipEvent {
	out := make([]MembershipEvent, 0, len(events))
	for _, e := range events {
		from := keySet(parseMembers(field, e.FromValue, e.FromString))
		to := keySet(parseMembers(field, e.ToValue, e.ToString))
		if sameKeySet(from, to) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func hasEqualMembershipTimestamps(events []MembershipEvent) bool {
	for i := 1; i < len(events); i++ {
		if events[i].ChangedAt.Equal(events[i-1].ChangedAt) {
			return true
		}
	}
	return false
}

func keySet(members []Member) map[string]bool {
	set := make(map[string]bool, len(members))
	for _, m := range members {
		set[m.Key()] = true
	}
	return set
}

func sameKeySet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func sortMembers(ms []Member) {
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].Key() < ms[j].Key() })
}

// exitBefore orders exit times with open (nil) intervals last.
func exitBefore(a, b *time.Time) bool {
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}
	return a.Before(*b)
}
