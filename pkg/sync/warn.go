package sync

import (
	"fmt"
	"os"
)

// Warnf reports a recoverable, non-fatal warning (timezone fallback, a single
// project/sprint that failed to sync, a field-name collision, etc.). The engine
// calls it instead of writing to stderr directly, so an embedding application can
// capture, route, or silence these messages. The default writes to os.Stderr,
// preserving the CLI's behavior.
type Warnf func(format string, args ...any)

// defaultWarnf writes warnings to stderr (the CLI default).
func defaultWarnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format, args...)
}
