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

// SELECTION GAPS: the cases other files do not cover.
//
// tui_selection_e2e_test.go has seven cases over space/A/C/hiding/filtering. This file closes
// measured gaps: `A` over single-pane sessions (the defect that shipped), marks that scroll OFF
// SCREEN but survive in the footer, and `X` changing the footer SENTENCE rather than silently
// toggling a mode nobody can read.

// ── A over single-pane sessions: the shipped defect ───────────────────────────────────────────

// `A` on a fleet of single-pane sessions marks exactly the rows the renderer drew, and the
// footer agrees with the marks on screen.
//
// The defect this pins, measured on the operator's fleet before the fix: 45 single-pane sessions
// at 40 rows, the renderer drew 25 rows and `A` marked 12 — the operator selected half of what
// they could see and sent to it. The defect was SPECIFIC to single-pane sessions because §16's
// group-of-one rule makes them headerless, and the viewport arithmetic (`inboxWindow`) had been
// written for the grouped shape where a session header costs a row. So this case is deliberately
// a fleet of single-pane sessions, and counts three numbers: how many the renderer drew, how
// many `A` marked, and what the footer says — all three must agree.
func TestE2EGAPSelectionAOnSinglePaneSessionsMarksExactlyWhatIsDrawn(t *testing.T) {
	// 30 single-pane sessions on a 24-row terminal: the inbox holds about 22, so some are below
	// the fold. With fewer panes than fit, "everything on screen" and "everything" are the same
	// set and the shipped defect would pass.
	const panes = 30
	const rows = 24
	ui, _, _, _ := selBuildFleet(t, 80, rows, panes)

	screen := capturePane(t, ui, "ui")
	drawn := len(uiselPaneRows(screen))
	if drawn == 0 {
		t.Fatalf("no pane rows on screen, so there is nothing for A to mark:\n%s", screen)
	}
	if drawn >= panes {
		t.Fatalf("all %d panes fit on a %d-row screen (%d drawn), so this case cannot discriminate "+
			"— the fixture needs more panes or a narrower terminal", panes, rows, drawn)
	}

	send(t, ui, "A")
	uiselWaitFooter(t, ui, "marked", "`A` must report how many rows it took")

	// One capture for all three numbers: the list re-sorts on every tick, so a footer read from
	// one frame against marks counted in another can disagree for reasons unrelated to the
	// defect. The shipped failure was all three disagreeing in one frame.
	screen = capturePane(t, ui, "ui")
	footer := uiselFooter(t, ui)
	marked := uiselMarkedRows(screen)
	footerCount, ok := uiselMarkedCount(footer)
	if !ok {
		t.Fatalf("the footer reports no count after `A`: %q\n%s", footer, screen)
	}

	// The defect: drew 25, marked 12. These must all be equal.
	if len(marked) != drawn {
		t.Errorf("`A` marked %d rows while the screen drew %d — the operator selected a number "+
			"they cannot see, and §16's fix was written for exactly this:\n%s",
			len(marked), drawn, screen)
	}
	if footerCount != drawn {
		t.Errorf("the footer says %d marked while the screen drew %d rows — one of the two is "+
			"describing a window the other is not:\n%s", footerCount, drawn, screen)
	}
	if footerCount != len(marked) {
		t.Errorf("the footer says %d marked and %d marks are on screen — either a target is "+
			"invisible or the count is wrong:\n%s", footerCount, len(marked), screen)
	}

	t.Logf("`A` over %d single-pane sessions at 80x%d: drew %d, marked %d, footer says %d",
		panes, rows, drawn, len(marked), footerCount)
}

// ── marks that scroll off screen ──────────────────────────────────────────────────────────────

// A mark on a row that scrolls OFF SCREEN still counts in the footer, and the mark is still on
// the row when it scrolls back.
//
// The existing resorting test (TestE2EUISelectionTheFooterKeepsTheCountWhileTheListResorts)
// proves the mark stays on the ROW by identity, which is the structural property. This proves
// the VIEWPORT property: an operator who marks a row, scrolls away, and looks at the footer
// must see the mark counted — and scrolling back must show the mark still there. Without the
// first half, a mark off screen is indistinguishable from one that was silently dropped; without
// the second, the count could be a stale number describing nothing.
func TestE2EGAPSelectionAMarkSurvivesScrollingOffScreenAndTheFooterCounts(t *testing.T) {
	const panes = 40
	ui, _, paneIDs, _ := selBuildFleet(t, 80, 24, panes)

	// Mark the first row, then scroll down. With 40 panes and ~14 drawn at a time, the first row
	// will be off screen when we're at the bottom.
	want := paneIDs[0]
	send(t, ui, "space")
	uiselWaitFooter(t, ui, "→ 1 marked", "space must mark the row")

	// Walk to a row near the end. With ~14 drawn and 40 panes, row 30 should put the viewport
	// well past row 0.
	selWalkTo(t, ui, paneIDs[30])

	// The mark is no longer on screen, which is the control: if it never left, this case cannot
	// tell "the mark survived scrolling away" from "the mark never left".
	var offScreen bool
	waitUntil(t, "the marked row to scroll off screen", 10*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		if err != nil {
			return false
		}
		// Check that the marked pane ID is not in the list of currently drawn pane rows.
		drawnIDs := make(map[string]bool)
		for _, row := range uiselPaneRows(s) {
			if id := uiselPaneIDIn(row); id != "" {
				drawnIDs[id] = true
			}
		}
		offScreen = !drawnIDs[want]
		return offScreen
	})
	if !offScreen {
		t.Fatalf("the marked row never left the screen after scrolling to row 30, so this "+
			"case cannot prove the mark survives being off screen:\n%s", capturePane(t, ui, "ui"))
	}

	// The mark is off screen, and the footer must still count it — that is the only statement of
	// how many targets a send would reach.
	footer := uiselFooter(t, ui)
	n, ok := uiselMarkedCount(footer)
	if !ok || n != 1 {
		t.Errorf("the footer stopped counting the mark when it scrolled off screen (%q) — an "+
			"operator who looks at the footer before pressing enter has no way to know the mark "+
			"survived:\n%s", footer, capturePane(t, ui, "ui"))
	}

	// Scroll back to the marked row. The mark must still be on it, which is the second half: a
	// footer that kept counting a mark that had been silently dropped would pass the check above
	// and fail here.
	selWalkTo(t, ui, want)
	// Wait for the id to be drawn IN A ROW, not anywhere on the screen. The details TILE prints the
	// same id (`┌─ host session %N`), so `Contains(screen, want)` is satisfied while the row itself is
	// still off screen — and the assertion below then counts zero marks on a product that is working.
	// This suite has the same lesson written down for a different helper; under the full suite's load
	// it went red once in 216 cases and passed three times in isolation, which is what a wait on the
	// wrong surface looks like.
	waitUntil(t, "the marked ROW to scroll back on screen (the tile prints the same id, so the row is "+
		"the only surface that answers)", 15*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		if err != nil {
			return false
		}
		for _, row := range uiselPaneRows(s) {
			if uiselPaneIDIn(row) == want {
				return true
			}
		}
		return false
	})

	marked := uiselMarkedRows(capturePane(t, ui, "ui"))
	if len(marked) != 1 {
		t.Errorf("scrolling back showed %d marks, want 1 — the mark did not survive the round "+
			"trip off screen:\n%s", len(marked), capturePane(t, ui, "ui"))
	}
	if len(marked) > 0 && !strings.Contains(marked[0], want) {
		t.Errorf("the mark is on %s, want it on %s — it moved to a different row:\n%s",
			uiselPaneIDIn(marked[0]), want, capturePane(t, ui, "ui"))
	}
}

// ── X and the footer sentence ──────────────────────────────────────────────────────────────────

// `X` changes the footer SENTENCE between the filtered view and the unfiltered view, and the
// sentence names the key.
//
// The gap this closes: TestE2EUIHidePersistXShowsHiddenRowsWithoutClearingTheMark asserts that X
// shows hidden rows and does not rewrite the file, but does NOT assert that the footer SENTENCE
// differs between the two views — and with the count suppressed while X is on, the only thing
// that says the view is unfiltered is the sentence. An operator who presses X to check what they
// hid last week needs the screen to say "you are looking at everything, including the rows you
// marked [x]", not the same footer they see every other day.
func TestE2EGAPSelectionXChangesTheFooterSentenceBetweenFilteredAndUnfiltered(t *testing.T) {
	ui, target, paneIDs, work := selBuildFleet(t, 80, 24, 3)
	hidden := filepath.Join(work, "hidden.json")

	// Hide one row so the fleet HAS a hidden row to toggle.
	selWalkTo(t, ui, paneIDs[1])
	send(t, ui, "x")
	uiselWaitFooter(t, ui, "1 hidden", "`x` must hide the row and say so")

	filteredFooter := uiselFooter(t, ui)
	if !strings.Contains(filteredFooter, "1 hidden") {
		t.Fatalf("the filtered footer does not say a row is hidden: %q", filteredFooter)
	}

	// Toggle: the hidden row is now on screen.
	send(t, ui, "X")
	waitUntil(t, "the hidden row to appear", 15*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && strings.Contains(s, paneIDs[1])
	})

	unfilteredFooter := uiselFooter(t, ui)
	// The two sentences must differ. An identical footer would mean the operator has no way to
	// know the view changed, which is the whole failure mode this case is about.
	if unfilteredFooter == filteredFooter {
		t.Errorf("`X` left the footer unchanged (%q) — an operator who toggles to check what they "+
			"hid has no way to tell the view is unfiltered, and an operator who leaves it toggled "+
			"cannot tell their screen from one with nothing hidden",
			unfilteredFooter)
	}
	// And the unfiltered sentence must NOT say "N hidden", because that is a claim about what is
	// being kept OFF the screen — false when every row is on it.
	if strings.Contains(unfilteredFooter, "1 hidden") {
		t.Errorf("the footer claims a row is hidden while it is on screen: %q — the number "+
			"describes nothing the operator can see", unfilteredFooter)
	}
	// The unfiltered sentence should name the rows that WOULD be hidden. Exactly what it says is
	// not asserted here — that is a frame test — but it must say SOMETHING different, and it
	// should mention "hidden" or the mark "[x]" so the operator knows what they are looking at.
	if !strings.Contains(strings.ToLower(unfilteredFooter), "hidden") &&
		!strings.Contains(unfilteredFooter, "[x]") {
		t.Logf("note: the unfiltered footer mentions neither 'hidden' nor '[x]': %q — an operator "+
			"may not recognise the view as unfiltered", unfilteredFooter)
	}

	// Toggle back: the sentence must return to the original.
	send(t, ui, "X")
	uiselWaitFooter(t, ui, "1 hidden", "`X` must toggle back to the filtered view")
	backFooter := uiselFooter(t, ui)
	if backFooter != filteredFooter {
		t.Errorf("`X` toggled back and the footer differs from the first filtered view:\nfirst:  %q\n"+
			"second: %q", filteredFooter, backFooter)
	}

	t.Logf("filtered footer: %q", filteredFooter)
	t.Logf("unfiltered footer: %q", unfilteredFooter)

	// The file must not have been touched by either toggle, which is the other half of "X is a
	// view". Checked here rather than in hidepersist because this case already has the fixture.
	beforeBytes, err := os.ReadFile(hidden)
	if err != nil {
		t.Fatalf("read %s: %v", hidden, err)
	}
	send(t, ui, "X")
	time.Sleep(800 * time.Millisecond)
	send(t, ui, "X")
	time.Sleep(800 * time.Millisecond)
	afterBytes, err := os.ReadFile(hidden)
	if err != nil {
		t.Fatalf("read %s after two toggles: %v", hidden, err)
	}
	if string(beforeBytes) != string(afterBytes) {
		t.Errorf("`X` rewrote the hidden set — it is a way of LOOKING and must not change what is "+
			"hidden:\nbefore: %s\nafter:  %s", beforeBytes, afterBytes)
	}

	_ = target // used above
}

// ── multiple marks ─────────────────────────────────────────────────────────────────────────────

// Marking multiple rows with space, and the footer counts them all.
//
// The existing tests all mark one row or use `A`. This proves that marking three rows with space
// produces three targets and the footer says so — because a send with no confirmation lands in
// every target, and the count is the only statement of what "every" means.
func TestE2EGAPSelectionMarkingMultipleRowsWithSpaceCountsThemAll(t *testing.T) {
	ui, _, paneIDs, _ := selBuildFleet(t, 80, 24, 5)

	// Three marks as three separate keystrokes, walking between them. The walk is necessary
	// because space toggles: pressing it three times on the same row marks, unmarks, marks.
	for i, id := range paneIDs[:3] {
		selWalkTo(t, ui, id)
		send(t, ui, "space")
		uiselWaitFooter(t, ui, fmt.Sprintf("→ %d marked", i+1),
			"space must increment the count with each mark")
	}

	screen := capturePane(t, ui, "ui")
	marked := uiselMarkedRows(screen)
	if len(marked) != 3 {
		t.Errorf("three presses of space on three rows produced %d marks, want 3 — the operator "+
			"cannot see which rows a send would reach:\n%s", len(marked), screen)
	}

	footer := uiselFooter(t, ui)
	n, ok := uiselMarkedCount(footer)
	if !ok || n != 3 {
		t.Errorf("the footer says %q after marking three rows, want it to say 3 marked — that is "+
			"the only statement of how many targets the next enter will send to", footer)
	}

	// And all three ids are on marked rows.
	gotIDs := map[string]bool{}
	for _, row := range marked {
		if id := uiselPaneIDIn(row); id != "" {
			gotIDs[id] = true
		}
	}
	for _, want := range paneIDs[:3] {
		if !gotIDs[want] {
			t.Errorf("marked rows do not include %s, one of the three rows space was pressed on: "+
				"%v", want, marked)
		}
	}
}

// ── helpers ────────────────────────────────────────────────────────────────────────────────────

// selBuildFleet is the fixture for this file: a hub over a private server of `panes` cat panes,
// with no agent rows. It waits for the fleet to paint before returning. Returns paneIDs in
// creation order (index 0 is window 0).
//
// It is a copy of uiselHub from tui_selection_e2e_test.go, extended to return paneIDs and work.
// The duplication is deliberate: extracting it to helpers_test.go would mean this file cannot be
// modified without coordinating with another agent.
func selBuildFleet(t *testing.T, cols, rows, panes int) (ui, target string, paneIDs []string, work string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	bin := buildBinary(t)
	work = t.TempDir()
	ui = filepath.Join(work, "ui.sock")
	target = filepath.Join(work, "target.sock")

	run := func(socket string, args ...string) (string, error) {
		full := append([]string{"-S", socket, "-f", "/dev/null"}, args...)
		out, err := exec.Command("tmux", full...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", target, "kill-server").Run()
		_ = exec.Command("tmux", "-S", ui, "kill-server").Run()
	})

	id, err := run(target, "new-session", "-d", "-s", "watched", "-c", work,
		"-P", "-F", "#{pane_id}", "cat")
	if err != nil {
		t.Fatalf("new-session: %v: %s", err, id)
	}
	paneIDs = append(paneIDs, id)
	for i := 1; i < panes; i++ {
		id, err := run(target, "new-window", "-t", "watched", "-c", work,
			"-P", "-F", "#{pane_id}", "cat")
		if err != nil {
			t.Fatalf("new-window %d: %v: %s", i, err, id)
		}
		paneIDs = append(paneIDs, id)
	}

	hosts := filepath.Join(work, "hosts.toml")
	if err := os.WriteFile(hosts,
		[]byte("[[host]]\nalias = \"nothing\"\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(work, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	pathWithoutClaude := selPathWithoutClaude(t)
	launch := fmt.Sprintf("HOME=%s PATH=%s %s --hosts %s --no-local --host scratch=%s,local "+
		"--hidden %s --view=flat --no-history; echo EXITED-rc=$?; sleep 60",
		home, pathWithoutClaude, bin, hosts, target, filepath.Join(work, "hidden.json"))
	if out, err := run(ui, "new-session", "-d", "-s", "ui", "-x", fmt.Sprint(cols),
		"-y", fmt.Sprint(rows), "-c", work, launch); err != nil {
		t.Fatalf("start the hub: %v: %s", err, out)
	}

	selWaitFleet(t, ui, panes)
	return ui, target, paneIDs, work
}

// selWaitFleet waits for the header to report exactly n rows.
func selWaitFleet(t *testing.T, ui string, n int) {
	t.Helper()
	word := "sessions"
	if n == 1 {
		word = "session"
	}
	want := fmt.Sprintf("tmux-hub  %d %s", n, word)
	waitUntil(t, fmt.Sprintf("the header to report exactly %d rows", n), 60*time.Second,
		func() bool {
			s, err := paneScreen(t, ui, "ui")
			return err == nil && strings.Contains(s, want)
		})
}

// selPathWithoutClaude is PATH with every directory holding `claude` dropped, so the fleet is
// exactly the panes this fixture creates.
func selPathWithoutClaude(t *testing.T) string {
	t.Helper()
	_, lookErr := exec.LookPath("claude")
	dirs := filepath.SplitList(os.Getenv("PATH"))
	var keep []string
	dropped := 0
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if st, err := os.Stat(filepath.Join(dir, "claude")); err == nil && !st.IsDir() {
			dropped++
			continue
		}
		keep = append(keep, dir)
	}
	if lookErr == nil && dropped == 0 {
		t.Fatalf("claude is on PATH and this filter dropped none of %d dirs", len(dirs))
	}
	if len(keep) == 0 {
		t.Fatal("filtering claude out of PATH left nothing")
	}
	return strings.Join(keep, ":")
}

// selWalkTo moves the cursor to the row containing id. Walks up first, then down, so a row that
// sorts to the top can be reached.
func selWalkTo(t *testing.T, ui, id string) {
	t.Helper()
	for i := 0; i < 30; i++ {
		send(t, ui, "k")
		time.Sleep(40 * time.Millisecond)
	}
	for i := 0; i < 50; i++ {
		if strings.Contains(cursorRow(t, ui), id) {
			return
		}
		send(t, ui, "j")
		time.Sleep(60 * time.Millisecond)
	}
	t.Fatalf("the cursor never reached %s in 50 presses:\n%s", id, capturePane(t, ui, "ui"))
}
