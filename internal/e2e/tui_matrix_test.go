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

// The interface's own test cases, chosen for what an IN-PROCESS test structurally cannot see.
//
// `internal/ui`'s 12 test files drive `m.Update(msg)` and read `View()`. That covers the model and
// the renderer completely, and it cannot cover: the real terminal's geometry (bubbletea is told a
// size by the terminal, not by a fixture), the key ROUTING (a `tea.KeyMsg` handed to Update has
// already been decoded — an escape sequence has not), a key whose meaning depends on the mode it
// arrives in, the alt-screen and the quit path, and the full write chain into a pane that a real
// process is reading.
//
// Each case below is one of those. Where a case could be written in process, it is not here.

// hubWith starts the hub over a watched server whose panes run `cmd`, and returns the sockets plus
// the watched pane ids. `cat` is the repo's own choice for a pane that can be written to and read
// back: it echoes a paste, so "did the send arrive" has an answer that does not need an agent.
func hubWith(t *testing.T, cols, rows, panes int, cmd string) (ui, target string, paneIDs []string, work string) {
	return hubWithHome(t, cols, rows, panes, cmd, false, "flat")
}

// hubWithView is hubWith with the opening screen SAID rather than fixed.
//
// Two hundred cases in this suite are about the attention-ordered list, so hubWith pins `--view=flat`
// for them. An empty view here passes NO flag at all, which is the only way a case can tell what the
// DEFAULT is — a flag that is tested while the default is not is a claim nothing asserts.
func hubWithView(t *testing.T, cols, rows, panes int, cmd, view string) (ui, target string,
	paneIDs []string, work string) {
	return hubWithHome(t, cols, rows, panes, cmd, false, view)
}

// hubWithHome is hubWith with a choice about HOME. An ISOLATED home is what the picker cases need:
// `p` probes every `Host` in `~/.ssh/config` with `tmux -V` over ssh, so against the operator's real
// config a test would reach out to eighteen of their machines and take fifteen seconds doing it.
// With an empty home the picker opens on its own specified screen — "nothing to show yet" — which is
// the shape those cases are about anyway.
//
// The other cases keep the real home on purpose: the agent rows they look for come from the
// operator's own Claude sessions, and inventing them would test the fixture.
func hubWithHome(t *testing.T, cols, rows, panes int, cmd string, isolateHome bool,
	view string) (ui, target string, paneIDs []string, work string) {
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
		"-P", "-F", "#{pane_id}", cmd)
	if err != nil {
		t.Fatalf("new-session: %v: %s", err, id)
	}
	paneIDs = append(paneIDs, id)
	for i := 1; i < panes; i++ {
		id, err := run(target, "new-window", "-t", "watched", "-c", work,
			"-P", "-F", "#{pane_id}", cmd)
		if err != nil {
			t.Fatalf("new-window: %v: %s", err, id)
		}
		paneIDs = append(paneIDs, id)
	}

	hosts := filepath.Join(work, "hosts.toml")
	if err := os.WriteFile(hosts,
		[]byte("[[host]]\nalias = \"nothing\"\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := ""
	if isolateHome {
		home := filepath.Join(work, "home")
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		env = "HOME=" + home + " "
	}
	launch := fmt.Sprintf("%s%s --hosts %s --no-local --host scratch=%s,local --hidden %s %s--no-history; "+
		"echo EXITED-rc=$?; sleep 60",
		env, bin, hosts, target, filepath.Join(work, "hidden.json"), viewFlag(view))
	if out, err := run(ui, "new-session", "-d", "-s", "ui", "-x", fmt.Sprint(cols),
		"-y", fmt.Sprint(rows), "-c", work, launch); err != nil {
		t.Fatalf("start the hub: %v: %s", err, out)
	}

	// Wait for the FLEET, not the header: §16 paints a usable screen before any poll completes, so
	// waiting for the header races every assertion about content.
	//
	// With an ISOLATED home there is no fleet at all, so those cases wait for the header instead.
	// Re-measured on this machine at 120x40, and the mechanism is not the one this comment used to
	// give: `tmux` on the PATH inside the pane is a mise shim that resolves the real binary through
	// $HOME/.local/share/mise, so with HOME pointed at an empty directory the watched host is DOWN —
	// `0 sessions` with `scratch down (list-panes rc=1: mise ERROR tmux is not a valid shim…)`,
	// identical at 4 s and at 16 s. The pane is therefore never shown, not shown LATE, and the
	// reason has nothing to do with the agent listing reading `~/.claude`. (tui_hidepersist's fixture
	// measured this first and pins PATH at the resolved binary to keep a real home; this comment was
	// what it contradicted.)
	// What the ROW shows, not the pane id: with one pane in `watched` the hub draws no id (rowPaneID),
	// and the id then survives only in the TILE — which a 40×6 terminal has no room for, so the wait
	// timed out on a screen that was correct.
	want := hubRowNeedle(paneIDs[0], paneIDs)
	if isolateHome {
		want = "tmux-hub"
	}
	waitUntil(t, "the first frame with "+want, 30*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && strings.Contains(s, want)
	})
	return ui, target, paneIDs, work
}

// cursorRow is the line the `>` marker is on, which is the only way to know from OUTSIDE which row
// the hub thinks is under the cursor.
func cursorRow(t *testing.T, ui string) string {
	t.Helper()
	for _, line := range strings.Split(capturePane(t, ui, "ui"), "\n") {
		if strings.HasPrefix(line, ">") {
			return strings.TrimRight(line, " ")
		}
	}
	return ""
}

// hubRowNeedle is what the ROW of one of hubWith's panes shows, which is not always its pane id.
//
// hubWith puts every pane in ONE session (`watched`), so a single-pane fixture is a session that
// puts one row on the screen and the hub draws no id for it (rowPaneID: an id that distinguishes
// nothing is a column the operator cannot read). With two or more panes the ids ARE what tells the
// rows apart, so they are drawn and the id is the right needle. Keying a walk on the id was correct
// until the id stopped being drawn, and then the walk pressed `j` sixty times against a screen that
// was right — and the id is still on the TILE, so a `Contains` over the whole screen keeps passing
// while the walk fails, which is what made this look like a product defect.
func hubRowNeedle(paneID string, ids []string) string {
	return rowNeedleIn(hubSession, paneID, ids)
}

// hubSession is the tmux session hubWith and historyHub both create their panes in.
const hubSession = "watched"

// rowNeedleIn is hubRowNeedle for a fixture that names its session something else.
func rowNeedleIn(session, paneID string, ids []string) string {
	if len(ids) > 1 {
		return paneID
	}
	return session
}

// cursorIsPaneLess asks whether the cursor is on a row with no pane, and it asks the TILE.
//
// The ROW can no longer answer: it used to be "a state word and no `%N`", and now no lone pane row
// carries an id either, so that shape matches both kinds and the walk stopped on the wrong one. §17
// gives the pane-less tile its own shape — `state / kind / started / id` instead of a capture — so
// `kind:` appears for exactly the rows this asks about. One tile, which holds below 160 columns; a
// grid could show a pane-less row's tile beside the cursor's, and no caller of this runs that wide.
func cursorIsPaneLess(t *testing.T, ui string) bool {
	t.Helper()
	s, err := paneScreen(t, ui, "ui")
	return err == nil && strings.Contains(s, "kind:")
}

// findPaneLessRow walks until the cursor is on a pane-less row, and reports whether it got there.
// ONE loop for the three callers that each had their own: two of them recognised the target by the
// row's shape and would now stop on a pane row, and a helper that lands on the wrong row is worse
// than one that gives up, because the case then asserts a refusal that never came.
func findPaneLessRow(t *testing.T, ui string, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for i := 0; i < 60 && time.Now().Before(deadline); i++ {
		if cursorIsPaneLess(t, ui) {
			return true
		}
		send(t, ui, "j")
		time.Sleep(120 * time.Millisecond)
	}
	return cursorIsPaneLess(t, ui)
}

// walkTo moves the cursor with `j` until its row matches want, and says so when it cannot.
func walkTo(t *testing.T, ui, want string) {
	t.Helper()
	// 60 presses, because the pane rows sort BELOW every agent row that wants attention and a
	// machine with a busy fleet has dozens. A walk that gives up early does not fail — it acts on
	// whatever row it stopped on, which is how the first version of the send case came to send to
	// an agent row it had not chosen.
	for i := 0; i < 60; i++ {
		if strings.Contains(cursorRow(t, ui), want) {
			return
		}
		send(t, ui, "j")
		time.Sleep(80 * time.Millisecond)
	}
	t.Fatalf("the cursor never reached a row containing %q in 60 presses\n%s",
		want, capturePane(t, ui, "ui"))
}

// ── geometry: the size the project COMMITS to, and the sizes it does not ──────────────────────────

// §16 commits to 80×24 and calls it "the size to hold, not a degraded case". A fixture cannot check
// it, because a fixture chooses its own width; a terminal imposes one.
func TestE2ETheTUIHoldsTheCommittedSize(t *testing.T) {
	ui, _, ids, _ := hubWith(t, 80, 24, 2, "cat")

	screen := capturePane(t, ui, "ui")
	lines := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	if len(lines) > 24 {
		t.Errorf("the screen is %d rows on a 24-row terminal", len(lines))
	}
	for i, l := range lines {
		if n := len([]rune(l)); n > 80 {
			t.Errorf("line %d is %d columns wide on an 80-column terminal: %q", i+1, n, l)
		}
	}
	if !strings.Contains(screen, "tmux-hub") {
		t.Errorf("no header at 80 columns:\n%s", screen)
	}
	// The footer's positive statement about the fleet has to survive the narrow band — this is the
	// class of defect §16 and the footer's own history are about.
	if !strings.Contains(screen, "scratch") {
		t.Errorf("the footer lost the host at 80 columns:\n%s", screen)
	}
	// Both watched panes are reachable on screen at the committed size.
	for _, id := range ids {
		if !strings.Contains(screen, id) {
			t.Errorf("pane %s is not on an 80×24 screen:\n%s", id, screen)
		}
	}
}

// A terminal smaller than the chrome is a real thing an operator does. It must not panic and must not
// draw outside itself; what it shows is allowed to be almost nothing.
func TestE2EATerminalSmallerThanTheChromeDoesNotPanic(t *testing.T) {
	ui, _, _, _ := hubWith(t, 40, 6, 1, "cat")

	screen := capturePane(t, ui, "ui")
	for i, l := range strings.Split(strings.TrimRight(screen, "\n"), "\n") {
		if n := len([]rune(l)); n > 40 {
			t.Errorf("line %d is %d columns on a 40-column terminal: %q", i+1, n, l)
		}
	}
	if strings.Contains(screen, "panic") || strings.Contains(screen, "goroutine") {
		t.Fatalf("the hub panicked at 40×6:\n%s", screen)
	}
	send(t, ui, "q")
	screenHas(t, ui, "EXITED-rc=0", "the hub must quit cleanly from a tiny terminal too")
}

// ── key routing: what an in-process test never decodes ────────────────────────────────────────────

// `ctrl+c` quits from EVERY screen, and this is the assertion that needed a terminal: the key arrives
// as a byte the runtime decodes, and the hub answers it before the mode dispatch on purpose. In
// process, a test hands Update a `tea.KeyMsg` that is already decoded.
func TestE2ECtrlCQuitsFromEveryScreen(t *testing.T) {
	for _, c := range []struct {
		name string
		open []string // the keys that get to that screen
		mark string   // something that screen shows, so we know we got there
	}{
		{"the dashboard", nil, "tmux-hub"},
		{"the composer", []string{"space", "i"}, "enter: send"},
		{"the launch form", []string{"n"}, "enter: create"},
		{"the project list", []string{"P"}, "project"},
		{"the host picker", []string{"p"}, "Hosts —"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The picker probes ~/.ssh/config over ssh, so that row gets an isolated home.
			ui, _, _, _ := hubWithHome(t, 120, 40, 1, "cat", c.name == "the host picker", "flat")
			for _, k := range c.open {
				send(t, ui, k)
				time.Sleep(250 * time.Millisecond)
			}
			screenHas(t, ui, c.mark, "the keys that open "+c.name+" must open it")

			send(t, ui, "C-c")
			screenHas(t, ui, "EXITED-rc=0",
				"ctrl+c must quit from "+c.name+" — it is answered before the mode dispatch")
		})
	}
}

// `q` means QUIT on the dashboard and the letter `q` inside a text box. A key whose meaning depends on
// the mode is exactly what a decoded-message test cannot distinguish, because both arrive the same.
func TestE2EQTypesItselfInsideATextBoxAndQuitsOutside(t *testing.T) {
	ui, _, _, _ := hubWith(t, 120, 40, 1, "cat")

	send(t, ui, "space", "i")
	screenHas(t, ui, "enter: send", "`i` must open the composer")

	// The `q` ALONE first, as its own keystroke. That is the whole assertion: a run of characters
	// injected in one call arrives as one key message and would prove nothing about `q`.
	typeOneAtATime(t, ui, "q")
	screenHas(t, ui, "enter: send",
		"`q` inside the composer must not quit — the composer is still open")
	typeOneAtATime(t, ui, "uit the server")
	screenHas(t, ui, "quit the server",
		"`q` inside the composer must be TEXT — a dashboard that quits when someone types "+
			"\"quit the server\" is worse than one with modes")

	// Leaving keeps the draft, which the README promises.
	send(t, ui, "Escape")
	time.Sleep(400 * time.Millisecond)
	send(t, ui, "i")
	screenHas(t, ui, "quit the server", "esc from the composer must keep the draft")

	send(t, ui, "Escape")
	time.Sleep(400 * time.Millisecond)
	send(t, ui, "q")
	screenHas(t, ui, "EXITED-rc=0", "`q` on the dashboard must quit")
}

// ── the write path, end to end through the interface ──────────────────────────────────────────────

// A send to a pane the hub cannot vouch for must ASK, and the dialog must name what it is about to do.
// Then any key that is not enter cancels — and says so, rather than leaving the operator guessing.
//
// The pane here runs `cat`, so it is plainly not an agent: the confirmation is the product working.
func TestE2EASendAsksBeforeWritingAndAnyOtherKeyCancels(t *testing.T) {
	ui, _, ids, _ := hubWith(t, 120, 40, 1, "cat")

	walkTo(t, ui, hubRowNeedle(ids[0], ids))
	send(t, ui, "space")
	time.Sleep(300 * time.Millisecond)
	send(t, ui, "i")
	screenHas(t, ui, "enter: send", "`i` must open the composer")
	sendLiteral(t, ui, "hello from the interface test")
	send(t, ui, "Enter")

	// The dialog names the ACT and the PAYLOAD, which is what makes confirming a decision rather
	// than a reflex.
	screenHas(t, ui, "send", "the dialog must name the act")
	screenHas(t, ui, "hello from the interface test", "the dialog must show the payload")

	// Any other key cancels. `x` is deliberately a key that DOES something on the dashboard, so a
	// dialog that leaked it would hide a row instead of cancelling.
	send(t, ui, "x")
	screenHas(t, ui, "cancelled", "a key that is not enter must cancel and say so")
	if s := capturePane(t, ui, "ui"); strings.Contains(s, "1 hidden") {
		t.Errorf("the cancelling key leaked to the dashboard and hid a row:\n%s", s)
	}
}

// A send the operator confirms anyway is still REFUSED when the hub cannot vouch for the target, and
// the refusal is a NON-WRITE rather than a message: the payload must not reach the pane.
//
// This is §7's promise and the reason the whole stamp mechanism exists. A pane-less row can never
// carry a token, so `enter: send anyway` cannot turn into a write however firmly it is pressed.
//
// Measured while writing this case, which is why it replaced the one that came first: the hub answers
// `sent to 1 target: 1 refused` and the dialog names both reasons — `this pane cannot be identified as
// an agent` and `this pane does not accept pasted text`. What is NOT automated here is the other half,
// a send that ARRIVES: that needs a pane the process walk identifies as an agent, which means a real
// Claude session and a real prompt typed into it, and a prompt costs the operator tokens. The unit
// suite covers the arriving path against a fake; this file covers the refusing path against the
// product.
func TestE2EASendAnywayIsStillRefusedForAnUnvouchedTarget(t *testing.T) {
	ui, target, _, _ := hubWith(t, 120, 40, 1, "cat")
	if !waitForAgentRow(t, ui) {
		t.Skip("no pane-less Claude rows appeared, so there is no unvouchable target to try")
	}
	walkToAgentRow(t, ui)

	send(t, ui, "space")
	time.Sleep(300 * time.Millisecond)
	send(t, ui, "i")
	screenHas(t, ui, "enter: send", "`i` must open the composer")
	const payload = "must-never-arrive"
	sendLiteral(t, ui, payload)
	send(t, ui, "Enter")

	// The dialog names WHY, and both reasons matter: one is about identity, the other about how the
	// pane would read the bytes.
	screenHas(t, ui, "cannot be identified as an agent", "the dialog must say why it is asking")
	screenHas(t, ui, "send anyway", "the dialog must offer the decision explicitly")

	send(t, ui, "Enter")
	screenHas(t, ui, "refused",
		"a target the hub cannot vouch for must be refused at the write path, not merely questioned")

	// And nothing was written. The watched pane is a `cat`, so anything that reached it would be on
	// its screen — this is the assertion that separates a refusal from a message about one.
	time.Sleep(1500 * time.Millisecond)
	for _, id := range []string{"%0"} {
		s, err := paneScreen(t, target, id)
		if err == nil && strings.Contains(s, payload) {
			t.Errorf("the payload reached pane %s despite the refusal:\n%s", id, s)
		}
	}
}

// ── refusals the operator has to be able to read ──────────────────────────────────────────────────

// `a` on a pane-less row is NOT tested here any more, and the reason is worth the space.
//
// The case that used to sit here pressed `a` on whatever pane-less row the OPERATOR's own `claude`
// reported, and asserted the refusal `not a tmux pane`. Both halves are now wrong. §22.3's door means
// a pane-less BACKGROUND row is opened rather than refused — and the refusal only ever applied to
// rows of another kind — so the assertion was testing a product that no longer exists.
//
// Worse, and this is the rule: with the door in place, that keystroke ACTED on the operator's real
// fleet. Measured on the run that caught it — the case woke a real session
// (`20260809--рендеринг-карты`), which then had a live worker where the roster had held none. A test
// must not do that. So every case that presses `a` on an agent row lives in tui_door_e2e_test.go,
// against a fake `claude` on a temporary PATH whose rows the test itself invented; `hubWith`, which
// keeps the operator's real home so that real agent rows appear at all, is for cases that only LOOK.
//
// What the deleted case asserted is covered twice over: the refusal by
// `ui.TestTheRefusalReachesTheScreenAtTheCommittedSize` (a frame at 80 columns, which also caught the
// sentence being cut), and the accept side by
// `TestE2EUIDoorAOnABackgroundRowMakesTheSessionAndRunsTheVerb`.

// `R` needs a selection and says so when there is none — a key that answers nothing is this repo's
// recurring defect.
func TestE2ERestartWithNoSelectionSaysSo(t *testing.T) {
	ui, _, _, _ := hubWith(t, 120, 40, 1, "cat")
	send(t, ui, "R")
	time.Sleep(600 * time.Millisecond)
	s := capturePane(t, ui, "ui")
	if !strings.Contains(s, "select") {
		t.Errorf("`R` with nothing selected said nothing about needing a selection:\n%s", s)
	}
}

// ── hiding, through the interface ─────────────────────────────────────────────────────────────────

// `x` hides the row under the cursor and `X` brings it back, and the footer counts what is hidden.
func TestE2EHidingAndUnhidingAPaneThroughTheScreen(t *testing.T) {
	ui, _, ids, _ := hubWith(t, 120, 40, 2, "cat")

	walkTo(t, ui, ids[1])
	send(t, ui, "x")
	waitUntil(t, "the hidden pane to leave the screen", 15*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && !strings.Contains(s, ids[1])
	})
	if s := capturePane(t, ui, "ui"); !strings.Contains(s, "hidden") {
		t.Errorf("the footer does not say a row is hidden:\n%s", s)
	}
	if s := capturePane(t, ui, "ui"); !strings.Contains(s, hubSession) {
		t.Errorf("hiding one pane took the other with it:\n%s", s)
	}

	send(t, ui, "X")
	waitUntil(t, "the hidden pane to come back", 15*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && strings.Contains(s, ids[1])
	})
}

// ── the picker, which is the screen a first run meets ─────────────────────────────────────────────

// With no hosts file the picker opens at startup — the first screen a new operator sees, and one no
// in-process test reaches, because it is a startup DECISION rather than a keystroke.
func TestE2EThePickerOpensOnAFirstRunAndEscLeavesIt(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	bin := buildBinary(t)
	work := t.TempDir()
	ui := filepath.Join(work, "ui.sock")
	target := filepath.Join(work, "target.sock")
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", target, "kill-server").Run()
		_ = exec.Command("tmux", "-S", ui, "kill-server").Run()
	})
	if out, err := exec.Command("tmux", "-S", target, "-f", "/dev/null",
		"new-session", "-d", "-s", "w", "cat").CombinedOutput(); err != nil {
		t.Fatalf("target: %v: %s", err, out)
	}
	// NO hosts file: the file has decided nothing, which is what opens the picker.
	missing := filepath.Join(work, "hosts.toml")
	home := filepath.Join(work, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	// An isolated HOME so the picker's probe cannot reach the operator's own machines: with an empty
	// ssh config it opens on its own specified screen instead, which is what this case asserts.
	cmd := fmt.Sprintf("HOME=%s %s --hosts %s --no-local --host scratch=%s,local --hidden %s --view=flat --no-history; "+
		"echo EXITED-rc=$?; sleep 60", home, bin, missing, target, filepath.Join(work, "hidden.json"))
	if out, err := exec.Command("tmux", "-S", ui, "-f", "/dev/null", "new-session", "-d", "-s", "ui",
		"-x", "120", "-y", "40", "-c", work, cmd).CombinedOutput(); err != nil {
		t.Fatalf("start: %v: %s", err, out)
	}

	screenHas(t, ui, "Hosts —",
		"a run with no hosts file must open the picker, which is the first screen a new operator meets")
	send(t, ui, "Escape")
	time.Sleep(600 * time.Millisecond)
	if s := capturePane(t, ui, "ui"); !strings.Contains(s, "tmux-hub") {
		t.Errorf("esc did not leave the picker for the dashboard:\n%s", s)
	}
	send(t, ui, "q")
	screenHas(t, ui, "EXITED-rc=0", "the hub must quit cleanly after the picker")
}

// waitForAgentRow polls for a pane-less Claude row, because the agents listing is a slower producer
// than the tmux tick and a single look reads the frame before it arrives.
func waitForAgentRow(t *testing.T, ui string) bool {
	t.Helper()
	// It WALKS now, where it used to scan the lines for a pane-less shape. The shape stopped
	// discriminating (see cursorIsPaneLess), and the callers all want the cursor on such a row
	// anyway — so answering the question and leaving the cursor there is one action, not two.
	return findPaneLessRow(t, ui, 20*time.Second)
}

// walkToAgentRow moves the cursor until it is on a PANE-LESS row, judged by the row's own shape
// (looksPaneLess). A t.Skip here reports PASS, so a helper that cannot recognise its target silently
// removes every case that calls it.
func walkToAgentRow(t *testing.T, ui string) {
	t.Helper()
	if findPaneLessRow(t, ui, 20*time.Second) {
		return
	}
	t.Skip("could not put the cursor on a pane-less row within 60 presses")
}

// THE DEFAULT PATH, which is the one an operator takes: press `n`, type a directory, press enter.
// No arrow keys, so the destination stays at `a new window`.
//
// This case exists because it was REPORTED from real use — "I could not create a session through the
// TUI at all" — and the harness reproduced it in one run while every existing test passed. The tests
// that came first all pressed Right to choose "a new session", so the default was uncovered, and the
// default hard-coded `$0` as the session to put the window in. On any server whose first session has
// ever been killed — which is every long-lived server — the launch answered
// `launch failed: create pane: new-window: can't find session: $0` and created nothing.
//
// The fixture is that server: two sessions created, the first killed, so the ids start at `$1`.
func TestE2ETheDefaultLaunchPathCreatesAWindowWhereTheCursorIs(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed, so a launch cannot produce a pane to look at")
	}
	ui, target, _, work := hubWith(t, 120, 40, 1, "cat")

	// Advance the session ids past $0 the way a long-lived server does.
	laterPane, err := exec.Command("tmux", "-S", target, "new-session", "-d", "-s", "later",
		"-c", work, "-P", "-F", "#{pane_id}", "sleep", "300").Output()
	if err != nil {
		t.Fatalf("second session: %v", err)
	}
	survivor := strings.TrimSpace(string(laterPane))
	if out, err := exec.Command("tmux", "-S", target, "kill-session", "-t", "watched").
		CombinedOutput(); err != nil {
		t.Fatalf("kill the first session: %v: %s", err, out)
	}
	ids, err := exec.Command("tmux", "-S", target, "list-sessions", "-F", "#{session_id}").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ids), "$0") {
		t.Fatalf("the fixture still has $0, so it cannot see the defect: %q", ids)
	}

	dir := filepath.Join(work, "st")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Wait for the surviving session's pane to reach the screen, so the cursor has somewhere to be.
	// By the session NAME and case-insensitively, because that is the only form that holds in BOTH
	// shapes: a group header upper-cases the name, and a headerless row spells it as the source did.
	// This used to match the pane ID for exactly that reason — and the id is no longer drawn for a
	// session that puts one row on the screen (rowPaneID), so it matched nothing and the wait timed
	// out against a correct screen.
	waitUntil(t, "the surviving session's pane to reach the screen", 20*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && (strings.Contains(strings.ToLower(s), "later") ||
			strings.Contains(s, survivor))
	})

	send(t, ui, "n")
	screenHas(t, ui, "enter: create", "`n` must open the launch form")
	// ctrl+u FIRST, because the directory field now opens holding the cwd of the row the cursor was
	// on. An operator who wants a different directory clears it; a test that types without clearing
	// appends to a real path and launches into nonsense.
	send(t, ui, "C-u")
	sendLiteral(t, ui, dir)
	screenHas(t, ui, "a new window", "the DEFAULT destination is a new window; this case is about it")
	send(t, ui, "Enter")

	// The launch must succeed. `launch failed` is the exact string the defect produced.
	screenHas(t, ui, "launched:",
		"the default path must create a pane — it hard-coded $0 and failed on any server whose "+
			"first session had been killed")
	if s := capturePane(t, ui, "ui"); strings.Contains(s, "launch failed") {
		t.Errorf("the default launch path failed:\n%s", s)
	}

	// And the window really is in a session that exists, running claude.
	waitUntil(t, "the claude pane to exist on the watched server", 20*time.Second, func() bool {
		out, err := exec.Command("tmux", "-S", target, "list-panes", "-a", "-F",
			"#{session_name} #{pane_current_command}").Output()
		return err == nil && strings.Contains(string(out), "claude")
	})
}

// A host with NO tmux server has nowhere to put a window, and the refusal has to name the way out.
//
// This is reachable from the fleet an operator really has: a host that answers ssh but runs no tmux
// reads `up-empty`, and choosing it in the form with the default destination asks for a window in a
// session that does not exist. Before the fix it answered tmux's own `can't find session: $0`, which
// names an id the operator never typed; now it says which host and what to choose instead.
func TestE2EALaunchIntoAHostWithNoSessionsSaysWhatToDoInstead(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	bin := buildBinary(t)
	work := t.TempDir()
	ui := filepath.Join(work, "ui.sock")
	// A socket path with NO server behind it: the host is reachable and has no tmux.
	empty := filepath.Join(work, "nothing.sock")
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", ui, "kill-server").Run() })

	hosts := filepath.Join(work, "hosts.toml")
	if err := os.WriteFile(hosts,
		[]byte("[[host]]\nalias = \"nothing\"\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := fmt.Sprintf("%s --hosts %s --no-local --host bare=%s,local --hidden %s --view=flat --no-history; "+
		"echo EXITED-rc=$?; sleep 60", bin, hosts, empty, filepath.Join(work, "hidden.json"))
	if out, err := exec.Command("tmux", "-S", ui, "-f", "/dev/null", "new-session", "-d", "-s", "ui",
		"-x", "120", "-y", "40", "-c", work, cmd).CombinedOutput(); err != nil {
		t.Fatalf("start: %v: %s", err, out)
	}
	screenHas(t, ui, "tmux-hub", "the hub must paint even with a fleet that has no server")

	dir := filepath.Join(work, "st")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	send(t, ui, "n")
	screenHas(t, ui, "enter: create", "`n` must open the launch form")
	// ctrl+u FIRST, because the directory field now opens holding the cwd of the row the cursor was
	// on. An operator who wants a different directory clears it; a test that types without clearing
	// appends to a real path and launches into nonsense.
	send(t, ui, "C-u")
	sendLiteral(t, ui, dir)
	send(t, ui, "Enter")

	screenHas(t, ui, "no tmux session",
		"the refusal must say the host has no session, not quote a tmux id the operator never typed")
	screenHas(t, ui, "a new session",
		"and it must name the way out, which is the other destination the form already offers")
}

// viewFlag turns a view name into the flag, or into nothing at all: an empty name means "pass no flag
// and let the binary choose", which is the only way to test what the binary chooses.
func viewFlag(view string) string {
	if view == "" {
		return ""
	}
	return "--view=" + view + " "
}
