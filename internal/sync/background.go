package sync

import (
	"context"
	"sync"
	"time"
)

// BackgroundWorker runs Engine.Sync() on a ticker interval in the background.
// Extracted from internal/tui/sync.go goroutine pattern.
type BackgroundWorker struct {
	engine   *Engine
	interval time.Duration
	mu       sync.Mutex
	lastSync time.Time
	syncing  bool
	cancel   context.CancelFunc
}

// NewBackgroundWorker creates a new background sync worker.
func NewBackgroundWorker(engine *Engine, interval time.Duration) *BackgroundWorker {
	return &BackgroundWorker{engine: engine, interval: interval}
}

// Start begins the background sync loop.
func (w *BackgroundWorker) Start(ctx context.Context) {
	ctx, w.cancel = context.WithCancel(ctx)
	go w.loop(ctx)
}

// Stop stops the background sync loop.
func (w *BackgroundWorker) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
}

// SyncAge returns the duration since the last successful sync.
// Returns -1 if never synced.
func (w *BackgroundWorker) SyncAge() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lastSync.IsZero() {
		return -1
	}
	return time.Since(w.lastSync)
}

// IsSyncing returns true if a sync is currently in progress.
func (w *BackgroundWorker) IsSyncing() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.syncing
}

func (w *BackgroundWorker) loop(ctx context.Context) {
	// Run immediately on start
	w.runSync(ctx)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runSync(ctx)
		}
	}
}

func (w *BackgroundWorker) runSync(ctx context.Context) {
	w.mu.Lock()
	w.syncing = true
	w.mu.Unlock()

	ch, err := w.engine.Sync(ctx, false, false, "")
	if err == nil {
		for range ch {
		} // drain
	}

	w.mu.Lock()
	w.syncing = false
	w.lastSync = time.Now()
	w.mu.Unlock()
}
