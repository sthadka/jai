package sync

import (
	"fmt"
	"testing"
)

// WithWarnf must route engine warnings to the supplied sink, and a nil sink must
// restore the default (non-nil) rather than panic.
func TestEngine_WithWarnf(t *testing.T) {
	var got string
	e := New(nil, nil, nil).WithWarnf(func(format string, args ...any) {
		got = fmt.Sprintf(format, args...)
	})
	e.warnf("warning: %s %d\n", "x", 7)
	if got != "warning: x 7\n" {
		t.Fatalf("sink got %q", got)
	}

	// nil restores the default sink (must not be nil / must not panic).
	e.WithWarnf(nil)
	if e.warnf == nil {
		t.Fatal("nil sink should restore default, not clear it")
	}
	e.warnf("ignored\n") // exercises default stderr sink without panicking
}
