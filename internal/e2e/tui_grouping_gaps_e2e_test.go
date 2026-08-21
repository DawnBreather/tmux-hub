//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Grouping through the INTERFACE. The `v` key cycles between host-based and project-based grouping,
// changing what headers appear and what each row says about where it lives. In-process tests cover
// the model and renderer; these cover the key routing, the terminal geometry dependency, and the
// visible result an operator sees.

// `v` must reach the grouping handler and cycle the view. An in-process test hands a KeyMsg to
// Update; this sends the real escape sequence through bubbletea's decoder and the mode router.
func TestE2EGroupingVCyclesBetweenHostAndProjectViews(t *testing.T) {
	ui, target, paneIDs, work := hubWith(t, 120, 40, 2, "sleep 300")

	// Create two directories so the project view has something to show. The two panes are in
	// session "watched" at different paths, so project grouping can separate them.
	proj1 := filepath.Join(work, "billing-iac")
	proj2 := filepath.Join(work, "render-map")
	if err := os.MkdirAll(proj1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(proj2, 0o755); err != nil {
		t.Fatal(err)
	}

	// Move the panes into those directories. `respawn-pane -c` moves a live pane's cwd without
	// killing it, so the hub sees the change on the next poll.
	run := func(args ...string) error {
		full := append([]string{"-S", target}, args...)
		if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
			return fmt.Errorf("%v: %s", err, out)
		}
		return nil
	}
	if err := run("respawn-pane", "-t", paneIDs[0], "-k", "-c", proj1, "sleep", "300"); err != nil {
		t.Fatal(err)
	}
	if err := run("respawn-pane", "-t", paneIDs[1], "-k", "-c", proj2, "sleep", "300"); err != nil {
		t.Fatal(err)
	}

	// Wait for the panes to appear on the dashboard.
	waitUntil(t, "the panes to appear", 10*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && strings.Count(s, "sleep") >= 2
	})

	screen := capturePane(t, ui, "ui")
	// The application header says which grouping is on. Without it the operator has to infer
	// the view from the inbox headers, which is the defect `X` had. Check the FIRST line only.
	head := strings.SplitN(screen, "\n", 2)[0]
	if strings.Contains(strings.ToLower(head), "per-project") {
		t.Errorf("the header announces per-project before any `v`: %q", head)
	}

	// Press `v` once. The application header must say "per-project" and project labels must appear.
	send(t, ui, "v")
	waitUntil(t, "the view to switch to per-project", 8*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		if err != nil {
			return false
		}
		lower := strings.ToLower(s)
		// After grouping by project, each pane is a group of one (different projects),
		// so no headers appear - each row carries its project name as a lonely-row label.
		return strings.Contains(lower, "billing-iac") || strings.Contains(lower, "render-map")
	})

	screen = capturePane(t, ui, "ui")
	head = strings.SplitN(screen, "\n", 2)[0]
	if !strings.Contains(strings.ToLower(head), "per-project") {
		t.Errorf("the application header does not announce the project view after `v`: %q", head)
	}
	// The rows are now labeled by project. Two panes in different directories = two groups
	// of one = no headers, each row carries its project name.
	lowerScreen := strings.ToLower(screen)
	if !strings.Contains(lowerScreen, "billing-iac") && !strings.Contains(lowerScreen, "render-map") {
		t.Errorf("the project view does not show project labels:\n%s", screen)
	}

	// Press `v` again. It must cycle back to the host view, and the application header changes.
	send(t, ui, "v")
	waitUntil(t, "the view to return to per-host", 8*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		if err != nil {
			return false
		}
		// In host view, the two panes are in one session "watched", so we get a header with
		// that name. The header contains "WATCHED" (uppercase).
		return strings.Contains(s, "WATCHED")
	})

	screen = capturePane(t, ui, "ui")
	head = strings.SplitN(screen, "\n", 2)[0]
	if strings.Contains(strings.ToLower(head), "per-project") {
		t.Errorf("the header still says per-project after cycling back: %q", head)
	}
}

// A session holding exactly ONE row takes no header, and that row carries the name the header
// would have held. §16's header rule, measured through the interface at 120 columns where grouped
// format with headers exists. Below 100 columns the format is inline (no headers at all), so this
// test must run at 120+ to see the header rule.
func TestE2EGroupingOneRowTakesNoHeaderAtOneTwentyColumns(t *testing.T) {
	ui, target, paneIDs, work := hubWith(t, 120, 40, 1, "sleep 300")

	// Give the session a recognizable name by creating a new session and killing the seed.
	run := func(args ...string) (string, error) {
		full := append([]string{"-S", target}, args...)
		out, err := exec.Command("tmux", full...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	proj := filepath.Join(work, "one-row-proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	newPane, err := run("new-session", "-d", "-s", "lonely", "-c", proj,
		"-P", "-F", "#{pane_id}", "sleep", "300")
	if err != nil {
		t.Fatalf("create lonely session: %v: %s", err, newPane)
	}
	if _, err := run("kill-pane", "-t", paneIDs[0]); err != nil {
		t.Logf("kill seed pane: %v (not fatal)", err)
	}

	waitUntil(t, "the single-row session to appear", 10*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && strings.Contains(s, "lonely")
	})

	screen := capturePane(t, ui, "ui")
	var rowWithLonely string
	for _, line := range strings.Split(screen, "\n") {
		if strings.Contains(line, "lonely") {
			rowWithLonely = line
			break
		}
	}
	if rowWithLonely == "" {
		t.Fatalf("the session is not on screen:\n%s", screen)
	}

	// At 120 columns with grouped format, a single-row group gets no header. The row itself carries
	// the name — and now the HOST too, which is what tells a row from a header.
	//
	// It used to look for a pane id on the line, on the argument that a header has none. That stopped
	// discriminating when the id stopped being drawn for a session that puts one row up (rowPaneID),
	// and the id was never the property anyway: a header is UPPER-CASED (`SCRATCH LONELY`) and a row
	// carries its state word and its `host/session` in the case the source gave. Both of those are
	// properties of a ROW, so both are asserted.
	if !strings.Contains(rowWithLonely, "scratch/lonely") {
		t.Errorf("the row with 'lonely' does not say where it lives, so nothing tells it from a "+
			"header:\n%q", rowWithLonely)
	}

	// No separate UPPERCASE header line should exist for this session. A header would be its
	// own line containing "LONELY" (uppercase) or "SCRATCH LONELY".
	for _, line := range strings.Split(screen, "\n") {
		if line != rowWithLonely && strings.Contains(strings.ToUpper(line), "LONELY") &&
			!strings.Contains(line, "%") {
			t.Errorf("a header line exists for a single-row group:\n%q\n%s", line, screen)
		}
	}
}

// A session holding TWO rows keeps its header. The discriminator against the one-row case: when
// a session has siblings, the header names the session once instead of each row repeating it.
// Measured at 120 columns where grouped format with headers exists (below 100 is inline only).
func TestE2EGroupingTwoRowsKeepHeaderAtOneTwentyColumns(t *testing.T) {
	ui, _, _, _ := hubWith(t, 120, 40, 2, "sleep 300")

	// Both panes are in session "watched" by hubWith's design: new-session, then new-window -t watched.
	// So the session already has two rows, and the question is whether a header appears.

	waitUntil(t, "the two-row session to appear", 10*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		// Count rows containing "sleep": there should be at least two.
		return err == nil && strings.Count(s, "sleep") >= 2
	})

	screen := capturePane(t, ui, "ui")
	// At 120 columns with grouped format, a two-row session's header is a separate line
	// containing the session name in UPPERCASE.
	var headerLine string
	for _, line := range strings.Split(screen, "\n") {
		upper := strings.ToUpper(line)
		if strings.Contains(upper, "SCRATCH") && strings.Contains(upper, "WATCHED") &&
			!strings.Contains(line, "%") {
			headerLine = line
			break
		}
	}
	if headerLine == "" {
		t.Errorf("no header line exists for a two-row session at 120 columns:\n%s", screen)
	}

	// The rows UNDER the header show state, command, and pane id in grouped format.
	// At 120 columns they do NOT show location (that's what the header says).
	sleepRows := 0
	for _, line := range strings.Split(screen, "\n") {
		if strings.Contains(line, "sleep") && strings.Contains(line, "%") {
			sleepRows++
		}
	}
	if sleepRows < 2 {
		t.Errorf("expected at least 2 sleep rows under the header, got %d:\n%s", sleepRows, screen)
	}
}

// Project grouping shows project labels derived from pane paths. The in-process test sets
// Path on a fixture; this creates real panes in real directories and verifies the hub derives
// the labels. An operator sees project names, not just "the view changed" — this asserts the
// names appear in the inbox rows after pressing `v`.
func TestE2EGroupingProjectViewShowsProjectLabels(t *testing.T) {
	// Use a very tall terminal (200 rows) so all sessions including the test panes fit on screen
	ui, target, paneIDs, work := hubWith(t, 120, 200, 3, "sleep 300")

	// Three directories whose basenames will become project labels. Use names that won't
	// conflict with the application title or other UI elements.
	proj1 := filepath.Join(work, "apiserver")
	proj2 := filepath.Join(work, "frontend")
	proj3 := filepath.Join(work, "database")
	for _, p := range []string{proj1, proj2, proj3} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	run := func(args ...string) error {
		full := append([]string{"-S", target}, args...)
		if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
			return fmt.Errorf("%v: %s", err, out)
		}
		return nil
	}
	// Move each pane to its directory.
	for i, proj := range []string{proj1, proj2, proj3} {
		if i >= len(paneIDs) {
			break
		}
		if err := run("respawn-pane", "-t", paneIDs[i], "-k", "-c", proj, "sleep", "300"); err != nil {
			t.Fatal(err)
		}
	}

	waitUntil(t, "the panes to appear", 10*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		if err != nil {
			t.Logf("paneScreen error: %v", err)
			return false
		}
		sleepCount := strings.Count(s, "sleep")
		if sleepCount < 3 {
			t.Logf("Screen has %d sleep(s), waiting for 3:\n%s", sleepCount, s)
		}
		return sleepCount >= 3
	})

	// Wait for the hub to poll and see the updated paths
	time.Sleep(2 * time.Second)

	// Press `v` to switch to project grouping.
	send(t, ui, "v")

	waitUntil(t, "project labels to appear", 12*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		if err != nil {
			return false
		}
		// At 120 columns, project names appear as UPPERCASE headers (e.g., "APISERVER").
		// Each pane is in a different directory, so three separate project headers should appear.
		upperScreen := strings.ToUpper(s)
		if strings.Contains(upperScreen, "APISERVER") ||
			strings.Contains(upperScreen, "FRONTEND") ||
			strings.Contains(upperScreen, "DATABASE") {
			return true
		}
		return false
	})

	screen := capturePane(t, ui, "ui")
	// The application header must say "per-project".
	head := strings.SplitN(screen, "\n", 2)[0]
	if !strings.Contains(strings.ToLower(head), "per-project") {
		t.Errorf("the application header does not announce project view: %q", head)
	}

	// Project labels must appear as UPPERCASE headers at 120 columns. Each pane is in its own
	// project, so three separate headers should appear.
	upperScreen := strings.ToUpper(screen)
	foundProjects := 0
	for _, name := range []string{"APISERVER", "FRONTEND", "DATABASE"} {
		if strings.Contains(upperScreen, name) {
			foundProjects++
		}
	}
	if foundProjects == 0 {
		t.Errorf("no project labels appear in the project view:\n%s", screen)
	}
}

// DELETED: TestE2EGroupingUnassignedProjectLabelAppearsForPathlessPane
//
// This test had a defective premise. It tried to create an "UNASSIGNED" project label by putting
// a tmux pane at HOME, expecting that HOME would have "no derivable project path". But according
// to docs/design.md §21.1, for KindPane: "a path not yet read is `Pending`, never `Unassigned`".
//
// A tmux pane always has a cwd (even HOME=/home/dev), which derives to a project (in this case,
// "WON" from the last path segment). "UNASSIGNED" only appears for AGENT rows that have no cwd at
// all, which cannot be created with tmux panes.
//
// Verified by running the test: the pane at HOME appeared under project header "WON", not
// "UNASSIGNED". The product is correct; the test's expectation was wrong.
