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

// The first tests that drive the REAL BINARY in a REAL TERMINAL.
//
// Measured before they existed: 47 e2e cases built the binary and ran it only as `status` /
// `--status`, the non-interactive report path, and all 12 ui test files drove `m.Update(...)` and
// `View()` in process. So the model and the renderer were covered and the INTERFACE was not — no
// bubbletea runtime, no pty, no real keystroke, no terminal size, no alt-screen, no quit path. The
// project journal records a manual pty run as the way that gap was covered ("found by running the
// branch in a pty"), which is a way of saying nothing automated did.
//
// The harness is tmux itself, which is the one this repo already trusts everywhere else: run the hub
// in a pane on a PRIVATE socket, `send-keys` to drive it, `capture-pane` to read what an operator
// would see. Every server here is private and killed by its own Cleanup; nothing touches the
// operator's own tmux, their hosts.toml, their hidden set or their history.
//
// What the first run of this harness found, which is why it is worth its weight: a launch into "a
// new session" created a session with an EMPTY NAME, because the form never filled SessionName. The
// row drew as the pane id twice and `tmux attach -t <name>` had nothing to take. Unit tests could
// not see it — they assert on the spec and the screen, and both were correct; what was wrong was the
// argv the spec produced, three layers down, and only a real launch reaches it.

// hubUI starts the hub in a pane on a private tmux server and returns the two sockets: the one the
// hub is DISPLAYED on, and the one it is told to WATCH.
//
// The watched host is marked `local` so the hub may write to it, and `--no-local` keeps the
// operator's own server out of the fleet. The hosts file exists and holds a DECISION (one disabled
// entry), because the picker opens at startup when the file has decided nothing — a first-run screen
// that would swallow every keystroke this test sends.
func hubUI(t *testing.T, cols, rows int) (ui, target, work string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	bin := buildBinary(t)
	work = t.TempDir()
	ui = filepath.Join(work, "ui.sock")
	target = filepath.Join(work, "target.sock")

	must := func(socket string, args ...string) {
		t.Helper()
		full := append([]string{"-S", socket, "-f", "/dev/null"}, args...)
		if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", target, "kill-server").Run()
		_ = exec.Command("tmux", "-S", ui, "kill-server").Run()
	})

	// One pane on the watched server, so the fleet is not empty.
	must(target, "new-session", "-d", "-s", "work", "-c", work, "sleep", "300")

	hosts := filepath.Join(work, "hosts.toml")
	if err := os.WriteFile(hosts,
		[]byte("[[host]]\nalias = \"nothing\"\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The wrapper's `echo` is how the quit path is asserted: a clean exit prints rc=0, and a panic
	// prints something else in the same place. `sleep` afterwards keeps the pane alive to be read.
	cmd := fmt.Sprintf("%s --hosts %s --no-local --host scratch=%s,local --hidden %s --view=flat --no-history; "+
		"echo EXITED-rc=$?; sleep 60",
		bin, hosts, target, filepath.Join(work, "hidden.json"))
	must(ui, "new-session", "-d", "-s", "ui", "-x", fmt.Sprint(cols), "-y", fmt.Sprint(rows),
		"-c", work, cmd)

	// Wait for the FLEET, not for the header. §16 promises a usable screen before any poll
	// completes, and it keeps that promise — the header and `0 sessions` paint immediately — so a
	// harness that waits for the header races every assertion about content and reads the pre-poll
	// frame. Measured: the first version of this helper did exactly that and read `0 sessions`.
	waitUntil(t, "the watched server's pane to reach the screen", 30*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && strings.Contains(s, "sleep")
	})
	return ui, target, work
}

// send drives the hub the way an operator does. Named keys go through tmux's own vocabulary; literal
// text goes through `-l` so a directory containing a word like `Enter` cannot be read as a key.
func send(t *testing.T, ui string, keys ...string) {
	t.Helper()
	args := append([]string{"-S", ui, "send-keys", "-t", "ui"}, keys...)
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		t.Fatalf("send-keys %v: %v: %s", keys, err, out)
	}
}

func sendLiteral(t *testing.T, ui, text string) {
	t.Helper()
	if out, err := exec.Command("tmux", "-S", ui, "send-keys", "-t", "ui", "-l", text).
		CombinedOutput(); err != nil {
		t.Fatalf("send-keys -l %q: %v: %s", text, err, out)
	}
}

// typeOneAtATime sends each character as its own keystroke.
//
// It exists because `send-keys -l "some text"` injects the whole run at once and bubbletea coalesces
// it into ONE key message whose String() is the entire string — so a per-character rule is not
// exercised at all. Measured: a mutant that made `q` quit from inside the composer SURVIVED a test
// that typed `quit the server` in one call, and dies against a test that types the `q` alone. Any
// case about what a SINGLE key means must type it singly.
func typeOneAtATime(t *testing.T, ui, text string) {
	t.Helper()
	for _, r := range text {
		sendLiteral(t, ui, string(r))
		time.Sleep(60 * time.Millisecond)
	}
}

// screenHas waits for the screen to contain what a keystroke was supposed to produce. A fixed sleep
// would either be flaky or slow; this is neither, and its failure prints the screen.
func screenHas(t *testing.T, ui, want, why string) {
	t.Helper()
	var last string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		s, err := paneScreen(t, ui, "ui")
		if err == nil {
			last = s
			if strings.Contains(s, want) {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the screen never showed %q — %s\n%s", want, why, last)
}

// The interface exists at all: it starts, paints the fleet, and quits cleanly.
//
// Every assertion here would have been satisfied by a stub before this file existed, which is the
// point: nothing was asserting them.
func TestE2ETheTUIPaintsAndQuitsCleanly(t *testing.T) {
	ui, _, _ := hubUI(t, 120, 40)

	screen := capturePane(t, ui, "ui")
	for _, want := range []string{"tmux-hub", "scratch"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the first paint does not name %q:\n%s", want, screen)
		}
	}
	// The watched pane is on the screen, so the fleet really was polled through the real transport.
	if !strings.Contains(screen, "sleep") {
		t.Errorf("the watched server's pane is not on the screen:\n%s", screen)
	}

	send(t, ui, "q")
	screenHas(t, ui, "EXITED-rc=0",
		"`q` must quit with a zero status; anything else here is a panic or a refused exit")
}

// The flow an operator actually performs, and the one no test could reach before: press `n`, fill the
// form, create a session on another server, and find it by NAME.
//
// The name is the assertion that matters. It was empty until the form was wired, and the first run of
// this harness is what found that — `tmux new-session -d -s ""` succeeds on both fleet versions, so
// the session existed, drew as its pane id twice, and could not be attached to by name.
func TestE2ELaunchingFromTheFormCreatesAFindableSession(t *testing.T) {
	// The launch runs the real `claude`, and it has to: the defect this test pins is in the argv
	// three layers below the spec, so a substituted command would test the substitution. Starting
	// a Claude REPL costs nothing — it waits for input and spends nothing until a prompt is typed —
	// and the session dies with the private server this test kills.
	//
	// Without `claude` the pane exits at once, the window closes with it and the session is gone
	// before it can be read, so the case is skipped rather than left to flake.
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed, so a launch cannot produce a session to look at")
	}
	ui, target, work := hubUI(t, 120, 40)

	dir := filepath.Join(work, "st")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	send(t, ui, "n")
	screenHas(t, ui, "enter: create", "`n` must open the launch form")

	// The directory field is focused on open — it is the only mandatory one — and it now opens
	// HOLDING the cwd of the row the cursor was on, so a fresh path starts with ctrl+u. Typing
	// without clearing appends to a real directory and launches into nonsense.
	send(t, ui, "C-u")
	sendLiteral(t, ui, dir)
	screenHas(t, ui, dir, "what was typed must appear in the field")

	// dir -> model -> mode -> where, then change it to a new session.
	send(t, ui, "Tab", "Tab", "Tab")
	send(t, ui, "Right")
	screenHas(t, ui, "a new session", "the destination must be switchable with the arrows")

	send(t, ui, "Enter")
	screenHas(t, ui, "launched:", "a launch must report the pane it created")

	// The session is NAMED, and the name is addressable — the two are different claims, because
	// tmux accepts a name its own target syntax cannot then resolve.
	waitUntil(t, "the named session to exist on the watched server", 20*time.Second, func() bool {
		out, err := exec.Command("tmux", "-S", target, "has-session", "-t", "st").CombinedOutput()
		_ = out
		return err == nil
	})
	out, err := exec.Command("tmux", "-S", target, "list-sessions", "-F",
		"#{session_name}|#{session_id}").Output()
	if err != nil {
		t.Fatalf("list-sessions: %v", err)
	}
	if !strings.Contains(string(out), "st|") {
		t.Errorf("the launched session is not named after its directory:\n%s", out)
	}
	// An empty name is what shipped before, and it is a REAL name rather than an absent one, so
	// this is the assertion that pins the fix.
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(line, "|") {
			t.Errorf("a session was created with an empty name: %q — the row draws as its pane "+
				"id twice and `tmux attach -t <name>` has nothing to take", line)
		}
	}
	// And the operator can see the name on the dashboard, which is the other half of "findable".
	//
	// It is on the ROW, beside the command, and that is a per-surface assertion rather than a
	// `Contains` over the whole screen: a launched session holds ONE pane, and a session holding one
	// row takes no header, so the row carries the name the header used to. This used to assert the
	// header `SCRATCH ST` — upper-cased, because a header upper-cases a derived name — and the
	// comment here said a lowercase match "would have failed against a correct screen". Both halves
	// were true of the layout at the time and neither is a property of the name.
	screenHas(t, ui, "claude", "the launched pane must show what it is running")
	var row string
	for _, l := range strings.Split(capturePane(t, ui, "ui"), "\n") {
		if strings.Contains(l, "claude") {
			row = l
			break
		}
	}
	if !strings.Contains(row, "st") {
		t.Errorf("the launched session's name is not on the row that shows what it is running, "+
			"so nothing on the screen says WHICH session just started: %q",
			strings.TrimRight(row, " "))
	}

	send(t, ui, "q")
	screenHas(t, ui, "EXITED-rc=0", "the hub must still quit cleanly after a launch")
}

// `x` on a row with no pane refuses, and it refuses THROUGH THE INTERFACE. The unit tests drive
// `hideSubject` and assert the note; this asserts the sentence an operator reads, at the width they
// read it, from the binary they run.
//
// The fleet here has pane-less rows only if the machine running the test has Claude sessions, so the
// case is skipped rather than faked when it does not: a fabricated row would test the fixture.
func TestE2ETheHideRefusalReachesTheScreen(t *testing.T) {
	ui, _, _ := hubUI(t, 120, 40)

	// WAIT for a pane-less row, do not look once. The agents listing is a separate, slower producer
	// than the tmux tick — `claude agents --json --all` takes 0.5-2.8 s — and hubUI returns as soon
	// as the tmux fleet has painted. The first version of this test read the screen at that moment,
	// found no agent row and SKIPPED, which in this repo reports PASS: a skip that fires because the
	// harness looked too early is indistinguishable from a machine with nothing to test.
	seen := false
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && !seen {
		s, err := paneScreen(t, ui, "ui")
		if err == nil && (strings.Contains(s, "background") || strings.Contains(s, "interactive")) {
			seen = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !seen {
		t.Skip("no pane-less Claude rows appeared in 20s, so there is nothing to refuse")
	}

	// Walk to the first pane-less row through the SHARED loop. This had its own copy, which
	// recognised the target by the row's shape ("a state word and no `%N`") — a shape that stopped
	// discriminating once a lone pane row also carried no id, at which point the copy would have
	// stopped on a pane row and this case would have asserted a refusal that never came. The tile
	// is the surface that answers the question (§17), and findPaneLessRow is the one place that
	// asks it.
	found := findPaneLessRow(t, ui, 20*time.Second)
	if !found {
		t.Skip("could not put the cursor on a pane-less row within 40 presses")
	}

	send(t, ui, "x")
	screenHas(t, ui, "nothing hidden",
		"`x` on a pane-less row must refuse, and say so where the operator is looking")
	screenHas(t, ui, "never expire", "the refusal must carry its reason")

	send(t, ui, "q")
	screenHas(t, ui, "EXITED-rc=0", "the hub must quit cleanly after a refusal")
}
