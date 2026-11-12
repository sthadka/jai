// Package live implements the live, interactive flow-metrics dashboard: a
// localhost-only HTTP server backed by one persistent DuckDB process that has
// the live jai.db ATTACHed READ_ONLY. The browser SPA (vendored Vega-Lite, no
// CDN) calls the read-only, SELECT-only, parameterized /q endpoint, which builds
// every query from a FIXED SQL template plus whitelisted identifiers — never raw
// client SQL (see query.go).
package live

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// eoqMarker is the sentinel column name emitted after every query so the reader
// knows where one query's JSON output ends on the shared stdout stream.
const eoqMarker = "__jai_eoq__"

// markerLine is the exact single-line JSON the sentinel query prints; the reader
// treats it as the end-of-query delimiter.
const markerLine = `[{"` + eoqMarker + `":"` + eoqMarker + `"}]`

// queryTimeout bounds a single /q round-trip against the persistent process so a
// pathological query can never hang the server forever.
const queryTimeout = 60 * time.Second

// ErrDuckDBNotFound is returned by NewEngine when no `duckdb` binary is on PATH.
// The CLI turns this into an actionable install hint and exits cleanly.
var ErrDuckDBNotFound = errors.New("duckdb binary not found on PATH")

// EngineConfig configures the persistent DuckDB process.
type EngineConfig struct {
	// DBPath is the on-disk path of the live jai.db SQLite file (ATTACHed READ_ONLY).
	DBPath string
	// DoneResolutions is the genuine-delivery resolution set from the metrics
	// config (no tenant literal in code); used to bind the `done_res` DuckDB
	// variable for the delivered cohort. May be empty.
	DoneResolutions []string
	// BlockerLinkTypes is the configured blocker link-type set from the metrics
	// config (metrics.blocker_link_types — no tenant literal in code); used to bind
	// the `blocker_links` DuckDB variable for the blocker_ageing measure. When empty
	// the measure matches nothing (the dashboard renders it as an explicit
	// unavailable card), never fabricated data.
	BlockerLinkTypes []string
}

// Engine owns one long-lived `duckdb` process in JSON output mode with jai.db
// ATTACHed READ_ONLY. Queries are serialized over a single stdin/stdout pipe;
// holding one process is lower latency than a subprocess per query.
type Engine struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *bufio.Reader
	stderr *strings.Builder
	mu     sync.Mutex
	closed bool
}

// NewEngine locates the duckdb binary, starts it, and runs the ATTACH + setup
// boilerplate (mirrors docs/attach.sql). It returns ErrDuckDBNotFound when no
// duckdb binary is on PATH so the caller can print an install hint instead of
// crashing.
func NewEngine(cfg EngineConfig) (*Engine, error) {
	bin, err := exec.LookPath("duckdb")
	if err != nil {
		return nil, ErrDuckDBNotFound
	}

	// `:memory:` is the DuckDB catalog the ATTACH lives in; the READ_ONLY guarantee
	// is on the SQLite ATTACH below, which keeps jai.db untouched (see docs/attach.sql).
	cmd := exec.Command(bin, ":memory:")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("duckdb stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("duckdb stdout: %w", err)
	}
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start duckdb: %w", err)
	}

	e := &Engine{
		cmd:    cmd,
		stdin:  stdin,
		out:    bufio.NewReaderSize(stdout, 1<<20),
		stderr: &stderrBuf,
	}

	if err := e.setup(cfg); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

// setup pins JSON output mode, attaches jai.db READ_ONLY, and binds the active
// generation and delivered-resolution variables the SQL template reads.
func (e *Engine) setup(cfg EngineConfig) error {
	var b strings.Builder
	b.WriteString(".mode json\n")
	b.WriteString("INSTALL sqlite;\n")
	b.WriteString("LOAD sqlite;\n")
	fmt.Fprintf(&b, "ATTACH '%s' AS jai (TYPE sqlite, READ_ONLY);\n", sqlEscape(cfg.DBPath))
	// Active generation: every fact query filters on it so staged/obsolete
	// generations are never visible.
	b.WriteString("SET VARIABLE g = (SELECT generation FROM jai.metrics_active_generation WHERE id = 1);\n")
	b.WriteString("SET VARIABLE done_res = " + sqlStringList(cfg.DoneResolutions) + ";\n")
	b.WriteString("SET VARIABLE blocker_links = " + sqlStringList(cfg.BlockerLinkTypes) + ";\n")
	if _, err := io.WriteString(e.stdin, b.String()); err != nil {
		return fmt.Errorf("duckdb setup write: %w", err)
	}

	// Probe: confirm the attach + variable bind succeeded and the pipe is live.
	rows, err := e.query("SELECT getvariable('g') AS g")
	if err != nil {
		return fmt.Errorf("duckdb attach failed (%s): %w", strings.TrimSpace(e.stderr.String()), err)
	}
	if len(rows) == 0 || rows[0]["g"] == nil {
		return fmt.Errorf("no active metrics generation in %s — run `jai metrics rebuild` first", cfg.DBPath)
	}
	return nil
}

// Query runs a single SELECT against the persistent process and returns the
// decoded JSON rows. It is safe for concurrent callers (serialized internally).
func (e *Engine) Query(sql string) ([]map[string]any, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, errors.New("duckdb engine closed")
	}
	return e.query(sql)
}

// query writes a statement plus the sentinel and reads rows until the sentinel.
// Callers must hold e.mu (setup runs before any concurrent use).
func (e *Engine) query(sql string) ([]map[string]any, error) {
	stmt := strings.TrimRight(strings.TrimSpace(sql), ";")
	payload := stmt + ";\nSELECT '" + eoqMarker + "' AS " + eoqMarker + ";\n"
	if _, err := io.WriteString(e.stdin, payload); err != nil {
		return nil, fmt.Errorf("duckdb write: %w", err)
	}

	type readResult struct {
		rows []map[string]any
		err  error
	}
	done := make(chan readResult, 1)
	go func() {
		rows, err := e.readUntilMarker()
		done <- readResult{rows, err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			return nil, fmt.Errorf("duckdb query (%s): %w", strings.TrimSpace(e.stderr.String()), r.err)
		}
		return r.rows, nil
	case <-time.After(queryTimeout):
		return nil, errors.New("duckdb query timed out")
	}
}

// readUntilMarker accumulates DuckDB's JSON output up to the sentinel line and
// returns the decoded rows of the single preceding result array.
func (e *Engine) readUntilMarker() ([]map[string]any, error) {
	// DuckDB's JSON mode prints a result array that may span multiple lines; the
	// sentinel query always prints on its own single line. Accumulate every line
	// up to the marker line, then decode the joined buffer as one JSON array.
	var buf strings.Builder
	for {
		line, err := e.out.ReadString('\n')
		if line != "" {
			if strings.TrimSpace(line) == markerLine {
				trimmed := strings.TrimSpace(buf.String())
				if trimmed == "" {
					return []map[string]any{}, nil
				}
				return decodeRows(trimmed)
			}
			buf.WriteString(line)
		}
		if err != nil {
			return nil, fmt.Errorf("duckdb read (%s): %w", strings.TrimSpace(buf.String()), err)
		}
	}
}

// Close terminates the persistent process.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	if e.stdin != nil {
		_, _ = io.WriteString(e.stdin, ".quit\n")
		_ = e.stdin.Close()
	}
	if e.cmd != nil && e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
		_ = e.cmd.Wait()
	}
	return nil
}
