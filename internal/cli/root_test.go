package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestRootPersistentPreRunAssemblesEngine drives the REAL command-assembly path
// (bindRoot -> PersistentPreRunE -> configloader.Load -> core.New -> a RunE that
// uses the engine), which the field-level tests bypass. It is the regression
// guard for the class of bug where the engine is never assembled and the first
// command dereferences a nil engine. If PersistentPreRunE stops wiring the
// engine, Execute panics/errors here instead of shipping.
func TestRootPersistentPreRunAssemblesEngine(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := "" +
		"jira:\n" +
		"  url: https://example.atlassian.net\n" +
		"  email: me@example.com\n" +
		"  token: dummy\n" +
		"db:\n" +
		"  path: " + filepath.Join(dir, "jai.db") + "\n" +
		"sync_sources:\n" +
		"  - name: proj\n" +
		"    jql: project = PROJ\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// A fresh root bound to a CLI we control, with the real query command.
	app := &CLI{}
	root := &cobra.Command{Use: "jai"}
	bindRoot(root, app)
	root.AddCommand(queryCmd)
	root.SetArgs([]string{"--config", cfgPath, "--no-sync", "query", "SELECT 1 AS n"})

	var runErr error
	out := captureStdout(t, func() { runErr = root.Execute() })
	if runErr != nil {
		t.Fatalf("Execute through real assembly path: %v", runErr)
	}

	// The engine must have been assembled by PersistentPreRunE (not nil), and the
	// store opened — a nil engine would have panicked in the query RunE above.
	if app.engine == nil || app.engine.Store == nil || app.engine.Query == nil {
		t.Fatalf("PersistentPreRunE did not assemble the engine: %+v", app.engine)
	}
	if !strings.Contains(out, "1") {
		t.Errorf("expected query result in output, got: %q", out)
	}
}

// TestCommandGroupsCoverEveryCommand guards the grouped `jai --help` layout: a
// new top-level command added without placing it in a group would otherwise fall
// into "Additional Commands" unnoticed. It also catches a command listed in two
// groups, a duplicate group id, and a stale name in commandGroups.
func TestCommandGroupsCoverEveryCommand(t *testing.T) {
	grouped := map[string]bool{}
	groupIDs := map[string]bool{}
	for _, g := range commandGroups {
		if groupIDs[g.ID] {
			t.Errorf("duplicate group id %q", g.ID)
		}
		groupIDs[g.ID] = true
		for _, name := range g.Cmds {
			if grouped[name] {
				t.Errorf("command %q assigned to more than one group", name)
			}
			grouped[name] = true
		}
	}

	// cobra-managed commands intentionally stay in "Additional Commands".
	ungrouped := map[string]bool{"help": true, "completion": true}
	real := map[string]bool{}
	for _, c := range rootCmd.Commands() {
		name := c.Name()
		real[name] = true
		if ungrouped[name] {
			continue
		}
		if !grouped[name] {
			t.Errorf("command %q is not in any help group — add it to commandGroups in root.go", name)
		}
	}
	for name := range grouped {
		if !real[name] {
			t.Errorf("commandGroups references %q, which is not a registered command", name)
		}
	}
}
