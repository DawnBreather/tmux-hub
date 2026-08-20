//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EVERY LAUNCHER IN THIS SUITE STATES WHICH VIEW IT WANTS, and this reads the suite's own source to
// make sure of it.
//
// The hub opens on the filesystem view (docs/design.md §23). Two hundred cases here were written
// against the attention-ordered list, so each launcher passes `--view=flat` — and the day the default
// flipped, ONE launcher was missed: `historyHub` passes `--history <path>` where the others pass
// `--no-history`, so an edit keyed on the neighbouring flag walked straight past it. The cost was
// sixteen failures whose screens were all CORRECT: `h` is a dashboard key, the hub was on the tree, and
// every message read like a broken product.
//
// A launcher with no `--view=` is not merely fragile, it is a case whose subject is decided elsewhere.
// This check is mechanical, so the next launcher cannot be added without saying which screen it means.
func TestEveryLauncherStatesItsView(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	launchers := 0
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(src), "\n")
		for i, ln := range lines {
			// `--hosts` is what every launcher of the TUI carries, and nothing else does. The
			// comment is CUT rather than the line skipped: several of this suite's doc comments quote
			// the argv they explain, and one struct field's trailing comment names the flag itself
			// (`hostsPath string // the --hosts file`). Both are prose, not a launch.
			code := ln
			if at := strings.Index(code, "//"); at >= 0 {
				code = code[:at]
			}
			if !strings.Contains(code, "--hosts") {
				continue
			}
			launchers++
			// The window is this line and the twelve after it. Twelve because one launcher builds
			// its argv as a SLICE and appends the flag seven lines later, after a loop that adds a
			// host per absent socket — a five-line window called that a miss.
			window := strings.Join(lines[i:min(i+13, len(lines))], "\n")
			if !strings.Contains(window, "--view=") && !strings.Contains(window, "viewFlag(") {
				t.Errorf("%s:%d launches the hub without saying which view it wants — pass "+
					"`--view=flat` for a case about the attention-ordered list, or `--view=tree` for "+
					"one about the filesystem view:\n%s",
					filepath.Base(f), i+1, window)
			}
		}
	}
	// A FLOOR, because a matcher that found nothing would pass silently and this whole check would
	// then be a green line saying "I looked at nothing". Measured: 21 launchers across 19 files the
	// day this was written, and the number only grows.
	if launchers < 15 {
		t.Errorf("this check found only %d launchers, which means it is no longer matching them — "+
			"a suite of this size has one per fixture", launchers)
	}
}
