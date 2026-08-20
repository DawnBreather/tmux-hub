//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// SELECTION at real widths: `space`, `A`, `C`, and the viewport, pressed as keys in a real
// terminal and read off the screen an operator would be looking at.
//
// What is covered elsewhere and deliberately not repeated: internal/ui/selection_test.go drives
// the Selection type, and internal/e2e/scale_test.go calls ui.VisiblePanes and ui.RenderInbox in
// process and compares the two numbers. Neither of them presses a key, so the ARITHMETIC of
// "what is on screen" was pinned and the KEYS were not — `A` could have been wired to
// visibleRows, to the whole registry or to nothing at all and both suites stay green. These
// cases press the key and read the footer's own count.

// ── the fixture ────────────────────────────────────────────────────────────────────────────────

// uiselHub starts the hub over a private server of `panes` cat panes and returns once the whole
// fleet has painted.
//
// It is a private launcher rather than a call to hubWith because every assertion in this file is
// a COUNT, and the harness's two existing shapes each hand the count to a producer this file
// does not control:
//
//   - hubWith keeps the operator's real HOME, so the fleet carries their own Claude sessions as
//     pane-less rows. Measured on this machine: 28 of them, arriving 0.5-2.8 s after the tmux
//     tick and sorting ABOVE the pane rows, so THEY would decide how many cat panes fit on
//     screen and the number would differ between two runs a minute apart.
//   - hubWithHome(…, true) isolates HOME, and that is worse, because `claude` under a HOME it
//     has never seen does not fail — it HANGS. Measured: a `claude agents` child sits there
//     forever, the hub's own transport tick never completes, and the dashboard paints
//     `tmux-hub  0 sessions` with `scratch down` for as long as you watch it. The first run of
//     this file spent sixty seconds on that screen, which reads exactly like a broken product.
//
// So the fixture isolates HOME *and* takes `claude` off the PATH, which turns the agents listing
// from an uncontrolled producer into a stated absence: the host then reads `scratch up (agents:
// claude is not installed here)` and the fleet is exactly the panes this function created. The
// header's own session count is the positive assertion that it arrived, and it is EXACT — an
// agent row leaking in would make the header say a number this wait never matches.
func uiselHub(t *testing.T, cols, rows, panes int) (ui, target string, paneIDs []string, work string) {
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

	// A hosts file that has DECIDED something, because a file that has decided nothing opens
	// the picker at startup and the picker would swallow every key these cases send.
	hosts := filepath.Join(work, "hosts.toml")
	if err := os.WriteFile(hosts,
		[]byte("[[host]]\nalias = \"nothing\"\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(work, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	launch := fmt.Sprintf("HOME=%s PATH=%s %s --hosts %s --no-local --host scratch=%s,local "+
		"--hidden %s --view=flat --no-history; echo EXITED-rc=$?; sleep 60",
		home, uiselPathWithoutClaude(t), bin, hosts, target,
		filepath.Join(work, "hidden.json"))
	if out, err := run(ui, "new-session", "-d", "-s", "ui", "-x", fmt.Sprint(cols),
		"-y", fmt.Sprint(rows), "-c", work, launch); err != nil {
		t.Fatalf("start the hub: %v: %s", err, out)
	}

	uiselWaitFleet(t, ui, panes)
	return ui, target, paneIDs, work
}

// uiselWaitFleet waits for the header to report exactly n rows.
//
// The header, not a pane id: with more panes than fit, any given pane may legitimately be below
// the fold, and waiting for one that is times out and reads as a broken product. §16 promises a
// painted screen BEFORE any poll completes and keeps that promise, so waiting for the header
// alone would race every assertion about content — it is the header's COUNT that says the fleet
// arrived.
func uiselWaitFleet(t *testing.T, ui string, n int) {
	t.Helper()
	word := "sessions"
	if n == 1 {
		word = "session"
	}
	want := fmt.Sprintf("tmux-hub  %d %s", n, word)
	waitUntil(t, fmt.Sprintf("the header to report exactly %d rows (%q)", n, want),
		60*time.Second, func() bool {
			s, err := paneScreen(t, ui, "ui")
			return err == nil && strings.Contains(s, want)
		})
}

// uiselPathWithoutClaude is PATH with every directory holding a `claude` executable dropped.
//
// The dropped count is checked against the operator's own machine: if `claude` is on the PATH and
// this filter removes nothing, the fixture is not the one the doc comment above describes and
// every count below would be measuring somebody's Claude sessions. A machine without `claude`
// removes nothing and that is correct, so the floor is conditional on finding it.
func uiselPathWithoutClaude(t *testing.T) string {
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
		t.Fatalf("claude is on the PATH and this filter dropped none of its %d directories — "+
			"the fleet would carry the operator's own agent rows and every count in this file "+
			"would be measuring them", len(dirs))
	}
	if len(keep) == 0 {
		t.Fatal("filtering claude out of PATH left nothing, so the hub could not find tmux")
	}
	return strings.Join(keep, ":")
}

// ── reading the screen ─────────────────────────────────────────────────────────────────────────

// uiselFooter is the LAST non-empty line, which is where the footer lives.
//
// It is a per-surface read rather than a Contains over the whole screen, because the screen holds
// the count's ingredients twice: the inbox draws a `◆` per marked row and the footer says how
// many there are. A whole-screen match cannot tell a footer that counts from a footer that has
// silently dropped the count while the rows still show their marks — and dropping a tail part is
// something lines.Fit is allowed to do at 80 columns, so that is a real state and not a
// hypothetical one.
func uiselFooter(t *testing.T, ui string) string {
	t.Helper()
	rows := strings.Split(capturePane(t, ui, "ui"), "\n")
	for i := len(rows) - 1; i >= 0; i-- {
		if strings.TrimSpace(rows[i]) != "" {
			return strings.TrimRight(rows[i], " ")
		}
	}
	return ""
}

// uiselWaitFooter waits for the footer — not the screen — to say something.
func uiselWaitFooter(t *testing.T, ui, want, why string) {
	t.Helper()
	var last string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		last = uiselFooter(t, ui)
		if strings.Contains(last, want) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the footer never said %q — %s\nfooter: %q\nscreen:\n%s",
		want, why, last, capturePane(t, ui, "ui"))
}

var uiselMarkedRe = regexp.MustCompile(`→ (\d+) marked`)

// uiselMarkedCount reads the footer's own count. The bool distinguishes "the footer makes no
// claim about a selection" from "the footer says zero" — different facts, and only one of them is
// a footer that dropped its count.
func uiselMarkedCount(footer string) (int, bool) {
	m := uiselMarkedRe.FindStringSubmatch(footer)
	if m == nil {
		return 0, false
	}
	n := 0
	for _, r := range m[1] {
		n = n*10 + int(r-'0')
	}
	return n, true
}

// uiselMarkGlyph is the mark the inbox draws between the cursor marker and the state glyph.
const uiselMarkGlyph = "◆"

// uiselMarkedRows are the lines carrying a mark. Every case here runs below 100 columns, where
// the layout is inbox-only: there are no tiles, so each occurrence of the glyph is one row's mark
// and counting the glyph counts rows.
func uiselMarkedRows(screen string) []string {
	var out []string
	for _, l := range strings.Split(screen, "\n") {
		if strings.Contains(l, uiselMarkGlyph) {
			out = append(out, strings.TrimRight(l, " "))
		}
	}
	return out
}

// uiselPaneRows are the pane rows the inbox is drawing. Both halves of the test matter: `cat` is
// the command column of every pane this fixture creates, and `%` is a tmux pane id, which no
// other line of the screen carries at these widths.
func uiselPaneRows(screen string) []string {
	var out []string
	for _, l := range strings.Split(screen, "\n") {
		if strings.Contains(l, "cat") && strings.Contains(l, "%") {
			out = append(out, strings.TrimRight(l, " "))
		}
	}
	return out
}

var uiselPaneIDRe = regexp.MustCompile(`%\d+`)

// uiselPaneIDIn is the pane id a row names — the row's IDENTITY. Every assertion here about "the
// same row" is about this string and never about a line number, because the list re-sorts on
// every tick and a position names a different row a second later. This repo has already shipped
// the other shape.
func uiselPaneIDIn(row string) string { return uiselPaneIDRe.FindString(row) }

// uiselPaneOrder is the pane ids top to bottom. Two of these taken a moment apart are how a case
// PROVES the list re-sorted instead of assuming it did.
func uiselPaneOrder(screen string) []string {
	var out []string
	for _, r := range uiselPaneRows(screen) {
		if id := uiselPaneIDIn(r); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// ── space: one row, one mark, and the same key takes it back ───────────────────────────────────

// `space` marks the row UNDER THE CURSOR, the row shows the mark, and pressing it again unmarks.
//
// The row's IDENTITY is asserted, not its position: the pane id is read before the keystroke and
// the marked row afterwards must carry the same one. "The third row now has a mark" would pass
// against a key that marks the third row of a list it re-sorted in between.
func TestE2EUISelectionSpaceMarksTheRowUnderTheCursor(t *testing.T) {
	ui, _, _, _ := uiselHub(t, 80, 24, 4)

	// Off the top row first, so a key that marks "the first row" rather than "the cursor's row"
	// is caught. With the cursor at 0 those two are indistinguishable.
	send(t, ui, "j")
	time.Sleep(400 * time.Millisecond)
	want := uiselPaneIDIn(cursorRow(t, ui))
	if want == "" {
		t.Fatalf("the cursor is not on a pane row, so this case cannot say what space marked:\n%s",
			capturePane(t, ui, "ui"))
	}

	send(t, ui, "space")
	uiselWaitFooter(t, ui, "→ 1 marked",
		"the footer is the only place an operator can read how many rows the next send would "+
			"reach; without it enter is a surprise")

	screen := capturePane(t, ui, "ui")
	marked := uiselMarkedRows(screen)
	if len(marked) != 1 {
		t.Fatalf("one press of space produced %d marked rows, want exactly 1 — the operator "+
			"cannot see which row a send would reach:\n%s", len(marked), screen)
	}
	if got := uiselPaneIDIn(marked[0]); got != want {
		t.Errorf("space marked %s while the cursor was on %s — a mark on a row nobody chose is "+
			"a send into a pane nobody chose:\n%s", got, want, screen)
	}
	// The mark sits in its own column BESIDE the cursor marker rather than replacing it: one
	// glyph for both would leave the operator unable to tell "this row is selected" from "this
	// row is where I am", which are the two facts the next keystroke depends on.
	if cur := cursorRow(t, ui); !strings.HasPrefix(cur, ">"+uiselMarkGlyph) {
		t.Errorf("the cursor row does not carry the cursor marker and the mark side by side: %q",
			cur)
	}

	// The same key takes it back.
	send(t, ui, "space")
	waitUntil(t, "the mark to leave the row", 15*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && len(uiselMarkedRows(s)) == 0
	})
	if f := uiselFooter(t, ui); strings.Contains(f, "marked") {
		t.Errorf("the footer still claims a selection after space unmarked the only row: %q — "+
			"an operator who trusts it sends to a row they had just deselected", f)
	}
	// And the send path agrees: nothing is selected, so the composer has nothing to open for.
	send(t, ui, "i")
	screenHas(t, ui, "select a pane with space first",
		"a second space must leave NO target — if the composer opens, the mark survived the "+
			"unmark and the screen was the thing that lied")
}

// ── A: every row on screen, and never one below the fold ───────────────────────────────────────

// `A` selects what is ON SCREEN. The fixture is deliberately bigger than the screen — 40 panes on
// a 24-row terminal, where the inbox holds about 22 — because with a fleet that fits,
// "everything visible" and "everything" are the same set and the case could not fail.
//
// Four assertions, each ruling out a different wrong wiring: a no-op, a key wired to the whole
// registry, a key that overshoots the drawn rows, and a count that disagrees with the marks the
// screen is showing.
func TestE2EUISelectionASelectsOnlyTheRowsOnScreen(t *testing.T) {
	const panes = 40
	const rows = 24
	ui, _, _, _ := uiselHub(t, 80, rows, panes)

	// The ceiling is MEASURED off the screen, not assumed: bodyHeight is height-2 today, and a
	// second footer row tomorrow would make a hard-coded 22 accuse a correct product.
	drawn := len(uiselPaneRows(capturePane(t, ui, "ui")))
	if drawn == 0 {
		t.Fatalf("no pane rows on screen, so there is nothing for A to select:\n%s",
			capturePane(t, ui, "ui"))
	}
	if drawn >= panes {
		t.Fatalf("all %d panes fit on a %d-row screen (%d drawn), so this case cannot tell "+
			"'everything visible' from 'everything' — the fixture needs more panes",
			panes, rows, drawn)
	}

	send(t, ui, "A")
	uiselWaitFooter(t, ui, "marked", "`A` must say how many rows it took")

	// ONE capture for both numbers: the list re-sorts on every tick, so a footer read from one
	// frame against marks counted in another can disagree for a reason that is not a defect.
	screen := capturePane(t, ui, "ui")
	footer := uiselFooter(t, ui)
	n, ok := uiselMarkedCount(footer)
	if !ok {
		t.Fatalf("the footer reports no count after `A`: %q\n%s", footer, screen)
	}
	if n == 0 {
		t.Errorf("`A` selected nothing: %q — a key that answers nothing reads as broken", footer)
	}
	if n >= panes {
		t.Errorf("`A` selected %d of a %d-pane fleet on a screen drawing %d rows: it reached "+
			"below the fold, and §7's whole point is that nobody sends into a pane they are not "+
			"looking at:\n%s", n, panes, drawn, screen)
	}
	if n > drawn {
		t.Errorf("`A` selected %d rows while the screen was drawing %d — the extra targets are "+
			"invisible to the operator who has to confirm the send:\n%s", n, drawn, screen)
	}
	if got := len(uiselMarkedRows(screen)); got != n {
		t.Errorf("the footer says %d marked and %d marks are on screen — either a target is off "+
			"screen or the count is wrong, and the operator cannot tell which:\n%s",
			n, got, screen)
	}
	// The three numbers, logged so a reader of a PASS can see the case was discriminating: with
	// `drawn` equal to `panes` the equality above is vacuous, and that is the one way this case
	// can pass while proving nothing. The guard for it is a Fatalf above; the record is here.
	t.Logf("`A` at 80x%d over %d panes: %d rows drawn, footer says %d marked, %d marks on screen",
		rows, panes, drawn, n, len(uiselMarkedRows(screen)))
}

// `A` follows the VIEWPORT, not the top of the list.
//
// This is a different property from the one above and no amount of testing at cursor 0 reaches it:
// with the cursor on the first row, "the rows on screen" and "the first rows of the list" are the
// same set, so a key wired to `rows[:22]` passes the previous case completely. Here the cursor is
// walked to the bottom, the drawn window is proven to have MOVED, and the marks must land in the
// window the operator is looking at.
func TestE2EUISelectionAFollowsTheViewportAfterScrolling(t *testing.T) {
	const panes = 40
	ui, _, _, _ := uiselHub(t, 80, 24, panes)

	first := uiselPaneOrder(capturePane(t, ui, "ui"))
	if len(first) == 0 || len(first) >= panes {
		t.Fatalf("the first window holds %d of %d panes, so there is nothing to scroll past",
			len(first), panes)
	}

	// Walk to the bottom. One press per pane is enough whether or not the cursor clamps at the
	// end, and pressing past the end is itself a thing an operator does.
	for i := 0; i < panes; i++ {
		send(t, ui, "j")
		time.Sleep(50 * time.Millisecond)
	}

	// The CONTROL: the window really moved. Without this the case can pass on a screen that never
	// scrolled, where the assertion below is the previous case again.
	var last []string
	waitUntil(t, "the drawn window to move away from the top of the list", 15*time.Second,
		func() bool {
			s, err := paneScreen(t, ui, "ui")
			if err != nil {
				return false
			}
			last = uiselPaneOrder(s)
			return len(last) > 0 && strings.Join(last, ",") != strings.Join(first, ",")
		})

	send(t, ui, "A")
	uiselWaitFooter(t, ui, "marked", "`A` must say how many rows it took")

	screen := capturePane(t, ui, "ui")
	drawn := uiselPaneOrder(screen)
	onScreen := map[string]bool{}
	for _, id := range drawn {
		onScreen[id] = true
	}
	inFirst := map[string]bool{}
	for _, id := range first {
		inFirst[id] = true
	}

	markedIDs := map[string]bool{}
	for _, r := range uiselMarkedRows(screen) {
		id := uiselPaneIDIn(r)
		markedIDs[id] = true
		if !onScreen[id] {
			t.Errorf("a mark is drawn for %s, which is not a row on this screen:\n%s", id, screen)
		}
	}
	n, ok := uiselMarkedCount(uiselFooter(t, ui))
	if !ok {
		t.Fatalf("the footer reports no count after `A`:\n%s", screen)
	}
	if n != len(markedIDs) {
		t.Errorf("the footer says %d marked and the screen shows %d marks — after scrolling, one "+
			"of the two is describing a window the other is not:\n%s", n, len(markedIDs), screen)
	}
	if n > len(drawn) {
		t.Errorf("`A` selected %d rows while the scrolled screen was drawing %d — it reached "+
			"outside the viewport:\n%s", n, len(drawn), screen)
	}
	// The discriminator: at least one selected row is one the FIRST window did not hold. A key
	// wired to the head of the list marks only rows that were on the unscrolled screen, and every
	// other assertion here would still pass.
	reached := 0
	for id := range markedIDs {
		if !inFirst[id] {
			reached++
		}
	}
	if reached == 0 {
		t.Errorf("`A` marked %d rows and not one of them is outside the window the screen showed "+
			"before scrolling — it is selecting the top of the list rather than what the operator "+
			"is looking at:\n%s", n, screen)
	}
	t.Logf("`A` after scrolling: %d rows drawn, %d marked, %d of them below the first window",
		len(drawn), n, reached)
}

// ── C: the selection goes away, and says so ────────────────────────────────────────────────────

// `C` clears every mark, tells the operator it did, and leaves the send path with no target.
//
// The last of those is the one that matters. A footer that stops counting while the selection
// survives is worse than one that keeps counting, because the next enter then reaches a pane
// nobody can see — so `i` is pressed afterwards and must refuse for want of a target.
func TestE2EUISelectionCClearsEveryMark(t *testing.T) {
	ui, _, _, _ := uiselHub(t, 80, 24, 4)

	// Two marks as `space j space` rather than by walking to two named rows: the cursor starts
	// at the top, so "this row and the one below it" is order-independent, while walking to a
	// row that sorts ABOVE the cursor never arrives and the walk fails for the wrong reason.
	send(t, ui, "space")
	time.Sleep(300 * time.Millisecond)
	send(t, ui, "j")
	time.Sleep(300 * time.Millisecond)
	send(t, ui, "space")
	uiselWaitFooter(t, ui, "→ 2 marked", "two presses of space must produce two targets")

	send(t, ui, "C")
	uiselWaitFooter(t, ui, "selection cleared",
		"`C` must say the selection went away: a key that silently empties the operator's "+
			"stated subject is indistinguishable from a key that did nothing")

	screen := capturePane(t, ui, "ui")
	if got := uiselMarkedRows(screen); len(got) != 0 {
		t.Errorf("`C` left %d marks on the screen: %v", len(got), got)
	}
	if f := uiselFooter(t, ui); strings.Contains(f, "marked") {
		t.Errorf("`C` cleared the marks and the footer still counts them: %q", f)
	}
	send(t, ui, "i")
	screenHas(t, ui, "select a pane with space first",
		"after `C` the composer must refuse for want of a target — if it opens, the selection "+
			"survived the clear and the footer was the thing that lied")
}

// ── the count survives the list moving under it ────────────────────────────────────────────────

// The footer keeps saying how many are marked while the list re-sorts, and the mark stays on the
// ROW it was taken on rather than on the position that row happened to occupy.
//
// The re-sort is PROVEN rather than assumed: the case records the pane ids the screen is drawing,
// changes the fleet until that sequence differs, and fails if it never does. Without that control
// the case passes on a list that never moved, which is the one state in which it proves nothing —
// and a stored index into a list somebody else re-sorts is a defect this repo has already shipped
// and written up.
func TestE2EUISelectionTheFooterKeepsTheCountWhileTheListResorts(t *testing.T) {
	ui, target, _, work := uiselHub(t, 80, 24, 6)

	send(t, ui, "j")
	time.Sleep(400 * time.Millisecond)
	want := uiselPaneIDIn(cursorRow(t, ui))
	if want == "" {
		t.Fatalf("the cursor is not on a pane row:\n%s", capturePane(t, ui, "ui"))
	}
	send(t, ui, "space")
	uiselWaitFooter(t, ui, "→ 1 marked", "space must produce the target this case then keeps")

	before := strings.Join(uiselPaneOrder(capturePane(t, ui, "ui")), ",")
	moved := false
	deadline := time.Now().Add(40 * time.Second)
	for !moved && time.Now().Before(deadline) {
		// Two kinds of movement, because the list moves in two ways in real use: a new pane
		// changes what the sort runs over, and writing into a pane changes its state, which is
		// the key the sort orders by.
		if out, err := exec.Command("tmux", "-S", target, "new-window", "-t", "watched",
			"-c", work, "cat").CombinedOutput(); err != nil {
			t.Fatalf("add a pane to move the list: %v: %s", err, out)
		}
		_ = exec.Command("tmux", "-S", target, "send-keys", "-t", "watched", "-l", "churn\n").Run()

		// Checked on EVERY frame of the window, not once at the end: a footer that dropped the
		// count for one tick and recovered is exactly the failure an operator would hit, and a
		// single look afterwards cannot see it.
		for i := 0; i < 12; i++ {
			screen := capturePane(t, ui, "ui")
			footer := uiselFooter(t, ui)
			if n, ok := uiselMarkedCount(footer); !ok || n != 1 {
				t.Fatalf("the footer stopped saying one row is marked while the list moved: %q "+
					"— the operator loses the only statement of what the next enter would "+
					"reach\n%s", footer, screen)
			}
			// A marked row that has scrolled past the fold is legitimate and is not asserted
			// against; a mark that has landed on a DIFFERENT row is the defect.
			for _, r := range uiselMarkedRows(screen) {
				if got := uiselPaneIDIn(r); got != want {
					t.Fatalf("the mark moved from %s to %s as the list re-sorted — a send would "+
						"reach a pane the operator never chose:\n%s", want, got, screen)
				}
			}
			if strings.Join(uiselPaneOrder(screen), ",") != before {
				moved = true
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	if !moved {
		t.Fatalf("the list never moved in 40s, so this case did not test what it claims\n%s",
			capturePane(t, ui, "ui"))
	}
}

// ── a mark on a row that leaves the screen ─────────────────────────────────────────────────────

// A marked row that is HIDDEN must stop being a target, and the proof is the send path refusing
// rather than the footer's count going away.
//
// The count going away is necessary and not sufficient: a selection that survived while the
// footer stopped counting it is strictly worse than one where both survive, because `i` then
// opens a composer whose enter writes into a pane the screen is not drawing. So this presses `i`.
func TestE2EUISelectionHidingAMarkedRowStopsItBeingATarget(t *testing.T) {
	ui, _, _, _ := uiselHub(t, 80, 24, 4)

	send(t, ui, "j")
	time.Sleep(400 * time.Millisecond)
	id := uiselPaneIDIn(cursorRow(t, ui))
	if id == "" {
		t.Fatalf("the cursor is not on a pane row:\n%s", capturePane(t, ui, "ui"))
	}
	send(t, ui, "space")
	uiselWaitFooter(t, ui, "→ 1 marked", "space must produce the target this case then hides")

	send(t, ui, "x")
	waitUntil(t, "the hidden row to leave the screen", 15*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && !strings.Contains(s, id)
	})
	uiselWaitFooter(t, ui, "hidden", "the footer must account for the row it stopped drawing")

	screen := capturePane(t, ui, "ui")
	if got := uiselMarkedRows(screen); len(got) != 0 {
		t.Errorf("a mark survives on screen after its row was hidden: %v\n%s", got, screen)
	}
	if f := uiselFooter(t, ui); strings.Contains(f, "marked") {
		t.Errorf("the footer still counts a mark whose row is hidden: %q — the row is off screen "+
			"and the count says a send still has a target", f)
	}
	send(t, ui, "i")
	screenHas(t, ui, "select a pane with space first",
		"a hidden row must not survive as a target: the composer must refuse, because its enter "+
			"would write into a pane the screen is no longer drawing")
}

// A mark on a row the ACTIVE PROJECT excludes is the other way a target leaves the screen, and
// this one must NOT be dropped: §21.5 keeps it on purpose, because the filter decides what is
// SHOWN and not what exists, and pruning here would silently lose a mark mid-compose whenever a
// pane cd'd out of the group. So the footer has to say so instead, and this is that sentence.
//
// Asserted at 80 columns, the size §16 commits to. A footer claimant that only survives on a wide
// terminal is a claimant the committed screen does not have — and the whole footer priority list
// exists because that had already happened to the host that was down.
func TestE2EUISelectionAMarkOutsideTheActiveProjectIsNamedInTheFooter(t *testing.T) {
	ui, target, _, work := uiselHub(t, 80, 24, 1)

	// Two more panes in two more DIRECTORIES. A derived project is keyed on the last path
	// segment, so distinct directory names are what make distinct groups: two panes under one
	// directory would be one project and the filter would have nothing to exclude. (Getting
	// exactly this wrong — writing `/x/st/one` and `/x/st/two` for "one group, two rows" — is
	// in this repo's own journal.)
	newPane := func(dir string) string {
		t.Helper()
		full := filepath.Join(work, dir)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("tmux", "-S", target, "new-window", "-t", "watched",
			"-c", full, "-P", "-F", "#{pane_id}", "cat").Output()
		if err != nil {
			t.Fatalf("new-window in %s: %v", full, err)
		}
		return strings.TrimSpace(string(out))
	}
	alpha := newPane("alpha")
	newPane("beta")
	uiselWaitFleet(t, ui, 3)

	// Mark the pane living in `alpha`, then narrow to `beta`.
	walkTo(t, ui, alpha)
	send(t, ui, "space")
	uiselWaitFooter(t, ui, "→ 1 marked", "space must produce the mark this case then filters away")

	send(t, ui, "P")
	screenHas(t, ui, "enter narrows", "`P` must open the project list")
	uiselWalkProjectTo(t, ui, "beta")
	send(t, ui, "Enter")
	screenHas(t, ui, "tmux-hub", "enter on a project must return to the dashboard, narrowed")

	// The marked row must really be gone, or the sentence has nothing to be about.
	waitUntil(t, "the marked row to leave the narrowed screen", 15*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && !strings.Contains(s, alpha)
	})
	uiselWaitFooter(t, ui, "not in this project",
		"a mark the screen cannot show still receives the send, so the footer must name it — "+
			"otherwise the operator reads the visible rows as the whole target list")
	uiselWaitFooter(t, ui, "enter still sends to it",
		"and the sentence must say what HAPPENS rather than merely that something is elsewhere: "+
			"the consequence is the actionable half")
}

// uiselWalkProjectTo moves the project list's cursor with `j` until its row matches want.
//
// The project list draws its own cursor as `❯ ` where the dashboard draws `>`, so cursorRow
// cannot read it — which is why this is a second walker rather than a call to the first.
func uiselWalkProjectTo(t *testing.T, ui, want string) {
	t.Helper()
	for i := 0; i < 30; i++ {
		row := ""
		for _, l := range strings.Split(capturePane(t, ui, "ui"), "\n") {
			if strings.HasPrefix(l, "❯") {
				row = l
				break
			}
		}
		if row == "" {
			t.Fatalf("the project list is drawing no cursor:\n%s", capturePane(t, ui, "ui"))
		}
		if strings.Contains(strings.ToLower(row), strings.ToLower(want)) {
			return
		}
		send(t, ui, "j")
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("the project list's cursor never reached a row containing %q in 30 presses\n%s",
		want, capturePane(t, ui, "ui"))
}
