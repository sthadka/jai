package metrics

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockFileName is the sidecar advisory-lock file kept next to the SQLite database.
// The lock serializes every flow-metric writer (full rebuilder and incremental
// writers) across processes: jai runs as multiple processes (serve/CLI/TUI) against
// one SQLite file, so a process-local sync.Mutex cannot guard the multi-transaction
// read -> activate window. An OS advisory lock (flock) releases automatically on
// process exit, avoiding lease expiry/renewal/fencing complexity.
const lockFileName = ".metrics-build.lock"

// LockPath returns the canonical advisory-lock sidecar path for a database file. It
// is derived purely from the DB path so every process computes the same path.
func LockPath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), lockFileName)
}

// BuildLock is an acquired exclusive OS advisory lock held for the duration of a
// metric write (rebuild or incremental). Release it (usually via defer) to let the
// next writer proceed; process exit releases it automatically.
type BuildLock struct {
	f      *os.File
	closed bool
}

// AcquireLock takes the exclusive advisory lock for dbPath, blocking until it is
// available. Every flow-metric writer MUST hold this lock across its whole
// read/build/activation (or incremental replacement) so activation can never publish
// stale facts over a newer incremental update.
func AcquireLock(dbPath string) (*BuildLock, error) {
	return acquire(dbPath, syscall.LOCK_EX)
}

// TryAcquireLock attempts a non-blocking exclusive acquire. ok is false (with a nil
// error and nil lock) when another process currently holds the lock.
func TryAcquireLock(dbPath string) (lock *BuildLock, ok bool, err error) {
	l, err := acquire(dbPath, syscall.LOCK_EX|syscall.LOCK_NB)
	if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return l, true, nil
}

func acquire(dbPath string, how int) (*BuildLock, error) {
	path := LockPath(dbPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("metrics lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open metrics lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		return nil, err
	}
	return &BuildLock{f: f}, nil
}

// Release drops the advisory lock and closes the sidecar handle. It is idempotent.
func (l *BuildLock) Release() error {
	if l == nil || l.closed {
		return nil
	}
	l.closed = true
	flockErr := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	closeErr := l.f.Close()
	if flockErr != nil {
		return flockErr
	}
	return closeErr
}
