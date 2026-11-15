package sync

// ChangeEvent describes a cache update produced by a completed sync of one
// source. It is intentionally coarse (per-source, emitted once the source's
// issues and derived facts have been committed) so a reactive consumer can
// refresh views without the engine taking on per-row observation cost.
type ChangeEvent struct {
	// Source is the configured sync source (sync_sources[].name) that updated.
	Source string
	// New, Updated, and Total report the counts for this source's sync.
	New     int
	Updated int
	Total   int
}

// ChangeHook receives ChangeEvents. Implementations must be fast and
// non-blocking; the sync engine calls the hook synchronously on its own
// goroutine after a source commits.
type ChangeHook func(ChangeEvent)

// OnChange registers a hook invoked once per source after its sync commits.
// It is optional: the CLI never sets one, so event delivery is a no-op there.
// A reactive frontend (GUI/service) can subscribe to refresh its views. Passing
// nil clears the hook. Set it before starting a sync; it is not safe to change
// concurrently with a running sync.
func (e *Engine) OnChange(hook ChangeHook) { e.onChange = hook }

// emitChange delivers an event to the registered hook, if any.
func (e *Engine) emitChange(ev ChangeEvent) {
	if e.onChange != nil {
		e.onChange(ev)
	}
}
