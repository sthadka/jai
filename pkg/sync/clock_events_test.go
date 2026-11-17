package sync

import (
	"testing"
	"time"

	"github.com/sthadka/jai/v6/pkg/clock"
)

// WithClock must make the engine's notion of "now" deterministic.
func TestEngine_WithClock(t *testing.T) {
	fixed := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	e := New(nil, nil, nil).WithClock(clock.Fixed{Time: fixed})
	if got := e.now(); !got.Equal(fixed) {
		t.Fatalf("now() = %v, want %v", got, fixed)
	}
	// nil clock falls back to the system clock without panicking.
	if New(nil, nil, nil).WithClock(nil).now().IsZero() {
		t.Fatal("system clock returned zero time")
	}
}

// The change-event hook must receive emitted events, and an engine with no hook
// (the CLI default) must treat emission as a safe no-op.
func TestEngine_OnChange(t *testing.T) {
	var got []ChangeEvent
	e := New(nil, nil, nil)
	e.OnChange(func(ev ChangeEvent) { got = append(got, ev) })
	e.emitChange(ChangeEvent{Source: "proj", New: 2, Updated: 1, Total: 3})

	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].Source != "proj" || got[0].New != 2 || got[0].Updated != 1 || got[0].Total != 3 {
		t.Errorf("unexpected event: %+v", got[0])
	}

	// No hook registered: must not panic.
	New(nil, nil, nil).emitChange(ChangeEvent{Source: "x"})

	// Clearing the hook stops delivery.
	e.OnChange(nil)
	e.emitChange(ChangeEvent{Source: "proj2"})
	if len(got) != 1 {
		t.Errorf("event delivered after clearing hook: %+v", got)
	}
}
