// Package clock provides a tiny time seam so engine packages can be driven by a
// deterministic clock in tests and by an application-supplied clock when
// embedded. Production code uses System; tests use Fixed.
package clock

import "time"

// Clock reports the current time. It is the injection point for any engine
// component whose behavior depends on "now" (sync watermarks, metric build
// timestamps, default as-of resolution).
type Clock interface {
	Now() time.Time
}

// System is the production Clock backed by time.Now.
type System struct{}

// Now returns the current wall-clock time.
func (System) Now() time.Time { return time.Now() }

// Fixed is a Clock that always returns the same instant; use it in tests to make
// time-dependent logic deterministic.
type Fixed struct {
	Time time.Time
}

// Now returns the fixed instant.
func (f Fixed) Now() time.Time { return f.Time }

// New returns the default production clock.
func New() Clock { return System{} }
