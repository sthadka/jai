package metrics

import (
	"testing"
	"time"

	"github.com/sthadka/jai/v5/pkg/clock"
)

// A query that omits as_of must resolve it from the injected clock, so metric
// computation is deterministic under a fixed clock.
func TestService_AsOfUsesClock(t *testing.T) {
	fixed := time.Date(2022, 6, 7, 8, 9, 10, 0, time.UTC)
	s := NewService(nil, nil, nil, nil).WithClock(clock.Fixed{Time: fixed})

	if got := s.asOf(Query{}); !got.Equal(fixed) {
		t.Fatalf("asOf(empty) = %v, want %v", got, fixed)
	}
	// An explicit as_of still wins over the clock.
	explicit := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := s.asOf(Query{AsOf: explicit}); !got.Equal(explicit) {
		t.Errorf("asOf(explicit) = %v, want %v", got, explicit)
	}
}

// WithClock on the builder must be honored (default is the system clock).
func TestBuilder_WithClock(t *testing.T) {
	fixed := time.Date(2023, 2, 3, 4, 5, 6, 0, time.UTC)
	b := NewBuilder(nil, nil).WithClock(clock.Fixed{Time: fixed})
	if !b.clk.Now().Equal(fixed) {
		t.Fatalf("builder clock = %v, want %v", b.clk.Now(), fixed)
	}
}
