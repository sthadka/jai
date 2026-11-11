package metrics

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sthadka/jai/internal/db"
)

// The advisory lock is mutually exclusive: while one handle holds it, a non-blocking
// acquire on the same path fails, and a blocking acquire succeeds only after release.
func TestBuildLock_MutualExclusion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jai.db")

	l1, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}

	_, ok, err := TryAcquireLock(path)
	if err != nil {
		t.Fatalf("TryAcquireLock: %v", err)
	}
	if ok {
		t.Fatal("TryAcquireLock succeeded while lock held")
	}

	if err := l1.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	l2, ok, err := TryAcquireLock(path)
	if err != nil || !ok {
		t.Fatalf("TryAcquireLock after release: ok=%v err=%v", ok, err)
	}
	l2.Release()

	// Release is idempotent.
	if err := l2.Release(); err != nil {
		t.Errorf("second Release should be nil, got %v", err)
	}
}

// envChildDB / envChildRole drive the re-executed child process that stands in for a
// second OS process (a concurrent full rebuild) in the lost-update regression test.
const (
	envChildDB     = "JAI_METRICS_LOCKTEST_DB"
	envChildReady  = "JAI_METRICS_LOCKTEST_READY"
	envChildGo     = "JAI_METRICS_LOCKTEST_GO"
	envChildRunTag = "JAI_METRICS_LOCKTEST_CHILD"
)

// TestMain lets this test binary re-exec itself as the "rebuilder" child process so the
// lost-update invariant is exercised across two genuine OS processes, not goroutines.
func TestMain(m *testing.M) {
	if os.Getenv(envChildRunTag) == "1" {
		runRebuilderChild()
		return
	}
	os.Exit(m.Run())
}

// runRebuilderChild performs a full rebuild that reads the source snapshot, signals the
// parent it has read, waits for the parent's go-ahead, then activates — all while holding
// the cross-process advisory lock. It exits 0 on success, 1 on failure.
func runRebuilderChild() {
	path := os.Getenv(envChildDB)
	readyFile := os.Getenv(envChildReady)
	goFile := os.Getenv(envChildGo)

	database, err := db.Open(path)
	if err != nil {
		os.Exit(1)
	}
	defer database.Close()
	store := NewStore(database, path)
	b := NewBuilder(store, factPolicy())
	// Between read and activation: tell the parent we have read the (stale) snapshot and
	// block until it has performed its source mutation, maximizing the lost-update window.
	b.afterRead = func() {
		os.WriteFile(readyFile, []byte("read"), 0o600)
		for range 2000 {
			if _, err := os.Stat(goFile); err == nil {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	if _, err := b.BuildAll(asOf, nil); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// A full rebuild in a separate process and an incremental update in this process must not
// interleave: the advisory lock serializes them so activation cannot overwrite the newer
// incremental facts. Without the lock, the child's activation (built from the stale read)
// would clobber the parent's "In Progress" incremental and the final stage would be "todo".
func TestLostUpdate_TwoProcesses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jai.db")
	readyFile := filepath.Join(dir, "ready")
	goFile := filepath.Join(dir, "go")

	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	store := NewStore(database, path)

	// Seed one issue in the backlog and publish an initial generation.
	upIssue(t, database, "L-1", "PL", "Story", "Backlog", "To Do", "2024-01-01T00:00:00Z", "", "", true)
	if _, err := NewBuilder(store, factPolicy()).BuildAll(asOf, nil); err != nil {
		t.Fatalf("seed build: %v", err)
	}

	// Launch the child rebuilder. It reads the current (Backlog/"todo") snapshot and then
	// waits, holding the lock, for our go-ahead before activating.
	child := exec.Command(os.Args[0], "-test.run=TestMain")
	child.Env = append(os.Environ(),
		envChildRunTag+"=1",
		envChildDB+"="+path,
		envChildReady+"="+readyFile,
		envChildGo+"="+goFile,
	)
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}

	// Wait for the child to have read the stale snapshot (it now holds the lock).
	waitForFile(t, readyFile, 10*time.Second)

	// The newer source state: L-1 moves to In Progress. This is the update the rebuilder's
	// activation must not clobber.
	upIssue(t, database, "L-1", "PL", "Story", "In Progress", "In Progress", "2024-01-01T00:00:00Z", "", "", true)
	cl(t, database, "lc1", "L-1", "Backlog", "In Progress", "2024-01-03T00:00:00Z")

	// Release the child so it finishes its (stale) activation, then run our incremental.
	// Incremental blocks on the lock until the child releases it, so the newer facts win.
	if err := os.WriteFile(goFile, []byte("go"), 0o600); err != nil {
		t.Fatalf("write go file: %v", err)
	}

	b := NewBuilder(store, factPolicy())
	var incErr error
	for {
		_, incErr = b.Incremental(asOf, []string{"L-1"})
		if incErr != ErrFullRebuildRequired {
			break
		}
		// The child's activation may not have landed yet; retry briefly.
		time.Sleep(5 * time.Millisecond)
	}
	if incErr != nil {
		t.Fatalf("incremental: %v", incErr)
	}

	if err := child.Wait(); err != nil {
		t.Fatalf("child exited with error: %v", err)
	}
	database.Close()

	// Reopen and assert the final published state reflects the newer incremental.
	database2, err := db.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer database2.Close()
	flows, err := NewStore(database2, path).FlowRows(nil)
	if err != nil {
		t.Fatalf("FlowRows: %v", err)
	}
	if len(flows) != 1 {
		t.Fatalf("flow rows = %d, want 1", len(flows))
	}
	if flows[0].CurrentStage != "active" {
		t.Fatalf("lost update: final stage = %q, want \"active\" (incremental clobbered by stale rebuild)", flows[0].CurrentStage)
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
