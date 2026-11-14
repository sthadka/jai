package metrics

import (
	"testing"
)

func mev(t *testing.T, id, at, fromV, fromS, toV, toS string) MembershipEvent {
	t.Helper()
	return MembershipEvent{ID: id, ChangedAt: mustTime(t, at), FromValue: fromV, FromString: fromS, ToValue: toV, ToString: toS}
}

// memberKeys returns the sorted stable keys of a member slice for comparison.
func memberKeys(ms []Member) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Key()
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Two sprint IDs sharing a name must stay distinct; a name-only (missing id) membership is
// reported as unknown, never merged with a resolved member of the same name.
func TestMembership_SharedNamesAndMissingIDs(t *testing.T) {
	hist := MembershipHistory{
		Key: "K-1", Field: "Sprint", CreatedAt: mustTime(t, "2024-01-01T00:00:00Z"), HistoryComplete: true,
		Events: []MembershipEvent{
			// Enter two sprints with the same display name but different ids, plus a
			// name-only sprint whose id is unrecoverable.
			mev(t, "1", "2024-01-05T00:00:00Z", "", "", "10, 11", "Alpha, Alpha"),
			mev(t, "2", "2024-01-09T00:00:00Z", "10, 11", "Alpha, Alpha", "10, 11", "Alpha, Alpha, Ghost"),
		},
	}
	res := ReplayMembership(hist)
	ever := memberKeys(res.EverMembers())
	want := []string{"id:10", "id:11", "name?:Ghost"}
	if !eq(ever, want) {
		t.Fatalf("ever members = %v, want %v", ever, want)
	}
	// Ghost is unresolved (unknown id).
	for _, m := range res.EverMembers() {
		if m.Name == "Ghost" && m.Resolved {
			t.Errorf("Ghost should be unresolved (unknown id)")
		}
		if m.ID == "10" && !m.Resolved {
			t.Errorf("id 10 should be resolved")
		}
	}
}

// Multiple simultaneous memberships, and membership evaluated at an event time rather than
// copied from the current snapshot.
func TestMembership_AtTimeNotCurrent(t *testing.T) {
	hist := MembershipHistory{
		Key: "K-2", Field: "Sprint", CreatedAt: mustTime(t, "2024-01-01T00:00:00Z"), HistoryComplete: true,
		// Current snapshot (as of today) would be just sprint 3, but historically the issue
		// was in sprints 1 and 2 simultaneously.
		CurrentValue: "3", CurrentString: "S3",
		Events: []MembershipEvent{
			mev(t, "1", "2024-01-05T00:00:00Z", "", "", "1, 2", "S1, S2"),
			mev(t, "2", "2024-01-15T00:00:00Z", "1, 2", "S1, S2", "3", "S3"),
		},
	}
	res := ReplayMembership(hist)

	at := mustTime(t, "2024-01-10T00:00:00Z")
	got := memberKeys(res.MembersAt(at))
	if want := []string{"id:1", "id:2"}; !eq(got, want) {
		t.Fatalf("members at %v = %v, want %v", at, got, want)
	}
	// Before any membership began.
	if got := res.MembersAt(mustTime(t, "2024-01-02T00:00:00Z")); len(got) != 0 {
		t.Fatalf("members before first membership = %v, want none", got)
	}
	// After the switch, only sprint 3.
	got = memberKeys(res.MembersAt(mustTime(t, "2024-01-20T00:00:00Z")))
	if want := []string{"id:3"}; !eq(got, want) {
		t.Fatalf("members after switch = %v, want %v", got, want)
	}
}

// Removal and re-addition yield separate intervals, but the member is counted once ever.
func TestMembership_RemovalReaddition(t *testing.T) {
	hist := MembershipHistory{
		Key: "K-3", Field: "Sprint", CreatedAt: mustTime(t, "2024-01-01T00:00:00Z"), HistoryComplete: true,
		Events: []MembershipEvent{
			mev(t, "1", "2024-01-05T00:00:00Z", "", "", "1", "S1"), // add
			mev(t, "2", "2024-01-10T00:00:00Z", "1", "S1", "", ""), // remove
			mev(t, "3", "2024-01-20T00:00:00Z", "", "", "1", "S1"), // re-add
		},
	}
	res := ReplayMembership(hist)

	var s1Intervals int
	for _, iv := range res.Intervals {
		if iv.Member.Key() == "id:1" {
			s1Intervals++
		}
	}
	if s1Intervals != 2 {
		t.Fatalf("sprint 1 intervals = %d, want 2 (add + re-add)", s1Intervals)
	}
	// Not a member during the gap.
	if got := res.MembersAt(mustTime(t, "2024-01-15T00:00:00Z")); len(got) != 0 {
		t.Fatalf("members during gap = %v, want none", got)
	}
	// Member again after re-add.
	if got := memberKeys(res.MembersAt(mustTime(t, "2024-01-25T00:00:00Z"))); !eq(got, []string{"id:1"}) {
		t.Fatalf("members after re-add = %v, want [id:1]", got)
	}
	// Ever-members dedup: counted once despite two intervals.
	if ever := res.EverMembers(); len(ever) != 1 {
		t.Fatalf("ever members = %v, want 1", ever)
	}
}

// Partial history: initial membership before the first event is unknown, not an empty set.
func TestMembership_PartialHistoryInitialUnknown(t *testing.T) {
	hist := MembershipHistory{
		Key: "K-4", Field: "Sprint", CreatedAt: mustTime(t, "2024-01-01T00:00:00Z"), HistoryComplete: false,
		Events: []MembershipEvent{
			mev(t, "1", "2024-01-10T00:00:00Z", "1", "S1", "1, 2", "S1, S2"),
		},
	}
	res := ReplayMembership(hist)
	if !res.InitialUnknown {
		t.Fatalf("expected InitialUnknown with incomplete history")
	}
	// Sprint 1 was present before the first event; it must be modeled as a member (not an
	// empty initial set), but its entry time is not provable.
	var s1 *MembershipInterval
	for i := range res.Intervals {
		if res.Intervals[i].Member.Key() == "id:1" && res.Intervals[i].EnteredAt.Equal(hist.CreatedAt) {
			s1 = &res.Intervals[i]
		}
	}
	if s1 == nil {
		t.Fatalf("sprint 1 should be a member from creation (initial set not empty)")
	}
	if s1.EnteredKnown {
		t.Errorf("initial membership entry time must be flagged unknown (EnteredKnown=false)")
	}
}

// Labels are space-separated tokens with the text as identity; evaluated at event time.
func TestMembership_LabelsHistorical(t *testing.T) {
	hist := MembershipHistory{
		Key: "K-5", Field: "labels", CreatedAt: mustTime(t, "2024-01-01T00:00:00Z"), HistoryComplete: true,
		Events: []MembershipEvent{
			mev(t, "1", "2024-01-05T00:00:00Z", "", "backend", "", "backend urgent"),
			mev(t, "2", "2024-01-12T00:00:00Z", "", "backend urgent", "", "backend"),
		},
	}
	res := ReplayMembership(hist)
	// "urgent" was a label only between the two events.
	if got := memberKeys(res.MembersAt(mustTime(t, "2024-01-08T00:00:00Z"))); !eq(got, []string{"id:backend", "id:urgent"}) {
		t.Fatalf("labels mid = %v, want [backend urgent]", got)
	}
	if got := memberKeys(res.MembersAt(mustTime(t, "2024-01-20T00:00:00Z"))); !eq(got, []string{"id:backend"}) {
		t.Fatalf("labels after = %v, want [backend]", got)
	}
}

// No-op membership events (set unchanged) are dropped.
func TestMembership_NoOpDropped(t *testing.T) {
	hist := MembershipHistory{
		Key: "K-6", Field: "components", CreatedAt: mustTime(t, "2024-01-01T00:00:00Z"), HistoryComplete: true,
		Events: []MembershipEvent{
			mev(t, "1", "2024-01-05T00:00:00Z", "100", "API", "100", "API"), // no-op (same set)
			mev(t, "2", "2024-01-09T00:00:00Z", "100", "API", "100, 200", "API, DB"),
		},
	}
	res := ReplayMembership(hist)
	// Component 100 is a member from creation (seeded from the first non-no-op event's
	// from-set), 200 added at the second event.
	c100 := 0
	for _, iv := range res.Intervals {
		if iv.Member.Key() == "id:100" {
			c100++
			if !iv.EnteredAt.Equal(hist.CreatedAt) {
				t.Errorf("component 100 should enter at creation, got %v", iv.EnteredAt)
			}
		}
	}
	if c100 != 1 {
		t.Fatalf("component 100 intervals = %d, want 1 (no-op must not split)", c100)
	}
}

// Deterministic under shuffled input order (equal-timestamp ties by id).
func TestMembership_DeterministicUnderShuffle(t *testing.T) {
	base := MembershipHistory{
		Key: "K-7", Field: "Sprint", CreatedAt: mustTime(t, "2024-01-01T00:00:00Z"), HistoryComplete: true,
		Events: []MembershipEvent{
			mev(t, "1", "2024-01-05T00:00:00Z", "", "", "1", "S1"),
			mev(t, "2", "2024-01-05T00:00:00Z", "1", "S1", "1, 2", "S1, S2"),
			mev(t, "3", "2024-01-10T00:00:00Z", "1, 2", "S1, S2", "2", "S2"),
		},
	}
	a := ReplayMembership(base)
	if !a.Ambiguous {
		t.Errorf("expected Ambiguous (equal timestamps)")
	}
	rev := base
	rev.Events = []MembershipEvent{base.Events[2], base.Events[0], base.Events[1]}
	b := ReplayMembership(rev)
	if len(a.Intervals) != len(b.Intervals) {
		t.Fatalf("interval count differs under shuffle: %d vs %d", len(a.Intervals), len(b.Intervals))
	}
	for i := range a.Intervals {
		if a.Intervals[i].Member.Key() != b.Intervals[i].Member.Key() ||
			!a.Intervals[i].EnteredAt.Equal(b.Intervals[i].EnteredAt) {
			t.Fatalf("interval %d differs under shuffle", i)
		}
	}
}

// Repeated membership and multi-valued joins must not inflate issue totals: each issue is
// counted once per member, and multi-member issues do not multiply any single-issue total.
func TestMembership_CountingNoFanOut(t *testing.T) {
	results := []MembershipResult{
		// Issue A: added and removed from sprint 1 twice (repeated membership).
		ReplayMembership(MembershipHistory{
			Key: "A", Field: "Sprint", CreatedAt: mustTime(t, "2024-01-01T00:00:00Z"), HistoryComplete: true,
			Events: []MembershipEvent{
				mev(t, "1", "2024-01-02T00:00:00Z", "", "", "1", "S1"),
				mev(t, "2", "2024-01-03T00:00:00Z", "1", "S1", "", ""),
				mev(t, "3", "2024-01-04T00:00:00Z", "", "", "1", "S1"),
			},
		}),
		// Issue B: in sprint 1 and sprint 2 simultaneously (multi-valued).
		ReplayMembership(MembershipHistory{
			Key: "B", Field: "Sprint", CreatedAt: mustTime(t, "2024-01-01T00:00:00Z"), HistoryComplete: true,
			Events: []MembershipEvent{
				mev(t, "1", "2024-01-02T00:00:00Z", "", "", "1, 2", "S1, S2"),
			},
		}),
	}
	counts := EverMemberCounts(results)

	// Sprint 1: issues A and B -> 2 (A counted once despite add/remove/re-add).
	if got := counts["id:1"].Issues; got != 2 {
		t.Errorf("sprint 1 ever-members = %d, want 2", got)
	}
	// Sprint 2: issue B only -> 1.
	if got := counts["id:2"].Issues; got != 1 {
		t.Errorf("sprint 2 ever-members = %d, want 1", got)
	}
	// Sanity: total distinct issues across members is not inflated; B contributes to both
	// sprints but is a single issue.
	total := 0
	for _, t := range counts {
		total += t.Issues
	}
	if total != 3 { // A@s1 + B@s1 + B@s2
		t.Errorf("summed membership rows = %d, want 3", total)
	}
}

// Never-changed with complete history takes the current snapshot set; incomplete history is
// flagged unknown rather than asserting an empty set.
func TestMembership_NoEvents(t *testing.T) {
	complete := ReplayMembership(MembershipHistory{
		Key: "K-8", Field: "components", CreatedAt: mustTime(t, "2024-01-01T00:00:00Z"), HistoryComplete: true,
		CurrentValue: "100, 200", CurrentString: "API, DB",
	})
	if got := memberKeys(complete.EverMembers()); !eq(got, []string{"id:100", "id:200"}) {
		t.Fatalf("never-changed complete members = %v, want [100 200]", got)
	}
	if complete.InitialUnknown {
		t.Errorf("complete history must not be InitialUnknown")
	}
	incomplete := ReplayMembership(MembershipHistory{
		Key: "K-9", Field: "components", CreatedAt: mustTime(t, "2024-01-01T00:00:00Z"), HistoryComplete: false,
	})
	if !incomplete.InitialUnknown {
		t.Errorf("incomplete history with no events must be InitialUnknown (not empty set)")
	}
}
