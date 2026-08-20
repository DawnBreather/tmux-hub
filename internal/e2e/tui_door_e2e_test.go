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

// The door, driven end to end: a pane-less BACKGROUND row on the screen, `a`, and a real tmux session
// on a real socket running the verb.
//
// The `claude` here is a SCRIPT, and §22.3 asks for it by name (test rule 5). Nothing else can carry
// the accept pole: a fresh `CLAUDE_CONFIG_DIR` has no daemon and no roster, so all a real `claude`
// can do inside a test is refuse — and a refusal-only suite is green against a tree with no door.
// The script is also what makes the case safe, and that rule was paid for: the measurement behind
// §22.3's corrected cost sentence was taken against the operator's own live fleet and left one of
// their rows reading `failed` where it had read `done` (§22.10).

// doorFixture is a private tmux server with no panes of its own, a fake `claude` on PATH that reports
// one background session, and the hub watching it.
type doorFixture struct {
	bin    string
	ui     string // the socket the hub is DISPLAYED on
	target string // the socket it WATCHES, and where the door will make its session
	work   string
	claude string // the fake's log, so a case can see which verbs ran
}

// doorAgentID and the rest are the fake listing's own values. The id is what `claude attach` must be
// called with, and the name is what the created session must be named after.
const (
	doorAgentID   = "e2e00001"
	doorAgentName = "e2e-door"
	doorSessionID = "e2e00001-f68c-4baf-98fd-68d4fd1c3da4"
)

// doorFleet builds the binary and writes the fake. `word` is the listing state the fake reports,
// which is what decides whether `a` asks first.
func doorFleet(t *testing.T, word string) *doorFixture {
	t.Helper()
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	f := &doorFixture{bin: buildBinary(t), work: t.TempDir()}
	f.ui = filepath.Join(f.work, "ui.sock")
	f.target = filepath.Join(f.work, "target.sock")
	f.claude = filepath.Join(f.work, "claude-calls.log")

	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", f.target, "kill-server").Run()
		_ = exec.Command("tmux", "-S", f.ui, "kill-server").Run()
	})

	if err := os.WriteFile(filepath.Join(f.work, "hosts.toml"),
		[]byte("[[host]]\nalias = \"nothing\"\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(f.work, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// The real tmux, by resolved path: HOME stays the operator's own, because an isolated HOME makes
	// `tmux` on the PATH inside a pane a mise shim that cannot resolve — measured, the watched host
	// then reads `down` and stays down.
	if err := os.Symlink(tmuxPath, filepath.Join(bin, "tmux")); err != nil {
		t.Fatal(err)
	}

	// The fake. `agents` answers a bare JSON array, which is the shape agents.Parse takes; `attach`
	// prints a line the test can see and then holds the pane, because the door's whole point is that
	// the operator ends up standing in something.
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %q
case "$1" in
agents)
  printf '%%s' '[{"id":"%s","sessionId":"%s","name":"%s","kind":"background","state":"%s","cwd":"%s"}]'
  ;;
attach)
  echo "WOKE-$2"
  exec sleep 300
  ;;
*)
  echo "the fake claude was asked for $1, which no case expects" >&2
  exit 1
  ;;
esac
`, f.claude, doorAgentID, doorSessionID, doorAgentName, word, f.work)
	claudePath := filepath.Join(bin, "claude")
	if err := os.WriteFile(claudePath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// NOW the server, and its ENVIRONMENT is the load-bearing part of this fixture.
	//
	// The pane the door makes is created by the WATCHED SERVER, not by the hub, so it inherits that
	// server's environment — never the hub's PATH. Measured the hard way, twice:
	//
	//   - with the fake on the hub's PATH only, the created pane ran the OPERATOR's real `claude`,
	//     which answered `No job matching 'e2e00001'` — so the case was watching a log the door
	//     could never write.
	//   - `tmux set-environment -g PATH …` after the fact does NOT reach a new pane's process. Also
	//     measured: with it set, and with `default-shell /bin/sh` as well, the pane still found the
	//     real `claude`. The server's environment is fixed when the server STARTS, so it has to be
	//     given here.
	//
	// The other half of what made this hard to see: `remain-on-exit` is off by design, so a payload
	// that exits takes pane, window and session with it inside 200 ms — every observation then says
	// "nothing was ever created" about a door that created something.
	env := []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + os.Getenv("HOME"), // the real one: an empty HOME makes tmux itself a broken shim
		"TERM=xterm-256color",
	}
	// The hub needs this host `up` — a `down` host is refused before any dial, which is its own case
	// — and one `cat` pane is the cheapest server that answers.
	server := exec.Command("tmux", "-S", f.target, "-f", "/dev/null", "new-session", "-d",
		"-s", "watched", "-c", f.work, "cat")
	server.Env = env
	if out, err := server.CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}

	// `remain-on-exit on`, for the OBSERVER's sake and not the product's. The hub relies on it being
	// off — that is what makes a finished door clean up after itself — and nothing here asserts that.
	// What it buys is that the payload's fate stays on the screen: without it, a payload that exits
	// destroys pane, window and session inside 200 ms, and then `capture-pane` answers `can't find
	// pane` while a list taken a moment earlier still shows the session. Every early version of this
	// case read that as "the door created nothing".
	// Two server options, each earned by a measurement.
	//
	// `remain-on-exit on` is for the OBSERVER, not the product: the hub relies on it being off — that
	// is what makes a finished door clean up after itself — and nothing here asserts that. What it
	// buys is that the payload's fate stays on screen. Without it a payload that exits destroys pane,
	// window and session inside 200 ms, and `capture-pane` then answers `can't find pane` while a
	// list taken a moment earlier still shows the session.
	//
	// `default-shell /bin/sh` is what makes the FAKE win. A created pane inherits the client's PATH
	// (the hub's), but then its own shell runs: with the default shell here being zsh, `~/.zshenv`
	// prepends the operator's real bin directories and the pane ran the OPERATOR's `claude` —
	// measured, its screen read `No job matching 'e2e00001'` while the fake's log stayed empty. A
	// shell that reads no rc files leaves the PATH the hub handed it alone.
	for _, args := range [][]string{
		{"set", "-g", "remain-on-exit", "on"},
		{"set", "-g", "default-shell", "/bin/sh"},
	} {
		c := exec.Command("tmux", append([]string{"-S", f.target, "-f", "/dev/null"}, args...)...)
		c.Env = env
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
	}
	return f
}

// start runs the hub and waits for the AGENT ROW, which is the signal every case here is about.
//
// Not the header and not the fleet: §16 paints a usable screen before any poll, and the pane list
// arrives on the first tick while agent rows come from a separate producer 0.5-2.8 s later. Waiting
// for the wrong one is how a working product reads as broken.
func (f *doorFixture) start(t *testing.T, cols, rows int) {
	t.Helper()
	// §22.3's test rule: a §22 case FAILS rather than skips if the config dir it would point a real
	// `claude` at is not inside the test's own temp dir. The fake ignores it, and that is exactly
	// why the assertion is worth making — the day this fixture stops using a fake, nothing here
	// must be able to reach the operator's own store.
	cfg := filepath.Join(f.work, "claude-home")
	if !strings.HasPrefix(cfg, os.TempDir()) && !strings.HasPrefix(cfg, "/tmp/") {
		t.Fatalf("CLAUDE_CONFIG_DIR would be %q, which is not inside this test's own directory", cfg)
	}
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}

	// The hub's PATH is the CREATED PANE's PATH, and that is measured rather than assumed: a pane
	// tmux makes inherits the environment of the CLIENT that asked for it, never the server's.
	//
	//	env -i PATH=/SERVER-PATH … tmux new-session -d -s watched cat     # the server
	//	env PATH=/CLIENT-PATH tmux new-session -d "sh -c 'echo $PATH'"    # the client
	//	→ PANE_PATH=/CLIENT-PATH:/usr/bin:/bin
	//
	// `-e PATH=…` on new-session did not override it on 3.7b either. So the fake must be on the
	// HUB's PATH — and `/usr/bin:/bin` must be there too, because the payload runs `sh`: with only
	// the fixture's own bin dir, the created pane exited **127 with an empty screen** and, being
	// dead, took its window and session with it inside 200 ms. That is the whole reason this line has
	// three entries instead of one.
	cmd := fmt.Sprintf("PATH=%s:/usr/bin:/bin CLAUDE_CONFIG_DIR=%s %s --hosts %s --no-local "+
		"--host scratch=%s,local --hidden %s --view=flat --no-history; echo EXITED-rc=$?; sleep 60",
		filepath.Join(f.work, "bin"), cfg, f.bin, filepath.Join(f.work, "hosts.toml"),
		f.target, filepath.Join(f.work, "hidden.json"))
	if out, err := exec.Command("tmux", "-S", f.ui, "-f", "/dev/null", "new-session", "-d",
		"-s", "ui", "-x", fmt.Sprint(cols), "-y", fmt.Sprint(rows), "-c", f.work,
		cmd).CombinedOutput(); err != nil {
		t.Fatalf("start the hub: %v: %s", err, out)
	}
	waitUntil(t, "the agent row to reach the screen", 30*time.Second, func() bool {
		s, err := paneScreen(t, f.ui, "ui")
		return err == nil && strings.Contains(s, doorAgentName)
	})
}

// sessions is what exists on the WATCHED server, which is where the door makes things. It is real
// state on a real socket rather than a string off a screen.
func (f *doorFixture) sessions(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("tmux", "-S", f.target, "-f", "/dev/null",
		"list-sessions", "-F", "#{session_name}").CombinedOutput()
	if err != nil {
		return nil
	}
	var names []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names
}

// verbs are the argv lines the fake recorded, so a case can say WHICH verb ran rather than inferring
// it from a screen.
func (f *doorFixture) verbs(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(f.claude)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// diag is what to print when a wait times out: the hub's own note, the sessions that exist, the
// panes with their exit status, and the verbs the fake saw. Assembled in one place because every
// failure in this file is answered by the same four facts — and because the first version of this
// case reported "nothing was created" about a door that had created something and lost it.
func (f *doorFixture) diag(t *testing.T) string {
	t.Helper()
	note := ""
	if s, err := paneScreen(t, f.ui, "ui"); err == nil {
		for _, ln := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
			if strings.TrimSpace(ln) != "" {
				note = strings.TrimSpace(ln)
			}
		}
	}
	panes, _ := exec.Command("tmux", "-S", f.target, "-f", "/dev/null", "list-panes", "-a",
		"-F", "#{session_name}|#{pane_id}|dead=#{pane_dead}|status=#{pane_dead_status}").CombinedOutput()
	made, _ := exec.Command("tmux", "-S", f.target, "-f", "/dev/null", "capture-pane", "-p",
		"-t", doorAgentName+"-"+doorAgentID).CombinedOutput()
	cmdline, _ := exec.Command("tmux", "-S", f.target, "-f", "/dev/null", "list-panes", "-a",
		"-F", "#{session_name}|#{pane_current_command}|#{pane_start_command}").CombinedOutput()
	ls, _ := exec.Command("tmux", "-S", f.target, "-f", "/dev/null", "list-sessions").CombinedOutput()
	return fmt.Sprintf("  the hub's last line: %q\n  sessions: %v\n  list-sessions raw:\n%s"+
		"  panes:\n%s  commands:\n%s  the made pane's screen:\n%s\n  verbs: %v",
		note, f.sessions(t), ls, panes, cmdline, made, f.verbs(t))
}

// waitOr polls until fn holds, and on timeout prints the diagnosis at the moment of FAILURE.
//
// It exists because `waitUntil(t, "..."+f.diag(t), ...)` builds its message EAGERLY — so the four
// facts it prints were captured milliseconds after the keystroke, before anything could have
// happened, and every failure in this file read as "the door created nothing" no matter what it had
// created. A diagnosis has to be taken when the answer is wrong, not when the question is asked.
func (f *doorFixture) waitOr(t *testing.T, what string, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s\n%s", timeout, what, f.diag(t))
}

func (f *doorFixture) waitForSession(t *testing.T, name string) {
	t.Helper()
	f.waitOr(t, "the session "+name+" to exist on the watched server", 20*time.Second, func() bool {
		for _, s := range f.sessions(t) {
			if s == name {
				return true
			}
		}
		return false
	})
}

// THE ACCEPT POLE. A pane-less background row the listing calls `blocked` costs nothing to wake, so
// `a` acts at once: a session appears on the watched server, named after the row and its id, running
// the verb with the SHORT id.
func TestE2EUIDoorAOnABackgroundRowMakesTheSessionAndRunsTheVerb(t *testing.T) {
	f := doorFleet(t, "blocked")
	f.start(t, 120, 40)

	// The row is the only agent row on the screen, and the cursor starts at the top of the list —
	// but `needs` sorts above everything, so walk to it rather than assuming.
	walkTo(t, f.ui, doorAgentName)
	send(t, f.ui, "a")

	want := doorAgentName + "-" + doorAgentID

	// The VERB RAN, and that evidence is asserted first because it is the durable half: the fake
	// records every call to a file, while the session itself only lives as long as its payload —
	// `remain-on-exit` is off by design, so a payload that exits takes pane, window and session with
	// it (§22.3), and a test that looked only for the session would call a working door broken.
	var attached string
	f.waitOr(t, "the fake claude to record an attach", 20*time.Second, func() bool {
		for _, v := range f.verbs(t) {
			if strings.HasPrefix(v, "attach ") {
				attached = v
				return true
			}
		}
		return false
	})
	// The short id and nothing else. Measured against the real CLI: `--debug-file` is rejected with
	// `unknown option '--debug-file'`, so a payload carrying it would exit 1 before the daemon.
	if attached != "attach "+doorAgentID {
		t.Errorf("the verb ran as %q, want the short id and nothing else", attached)
	}
	f.waitForSession(t, want)

	// And the pane the door made is running it: the fake's own line is on that pane's screen.
	waitUntil(t, "the woken pane to show the verb's output", 15*time.Second, func() bool {
		s, err := paneScreen(t, f.target, want)
		return err == nil && strings.Contains(s, "WOKE-"+doorAgentID)
	})
}

// The operator ENDS UP INSIDE the session the door made, and a second keystroke makes no second one.
//
// This is the case that shows the door delivering rather than merely creating, and what it measures
// was a surprise worth writing down: after `a`, the hub's own pane no longer shows the hub. It shows
//
//	WOKE-e2e00001
//	[e2e-door-e2e00001] 0:sh*   "cachyos" 08:39
//
// — the woken session's screen with its own status line, because possession put the client there. The
// second `a` in this case is therefore typed INTO that session, which is why the assertion is about
// the watched server's session list and not about the hub's note: keys stop reaching the hub the
// moment it hands the client over, and a case that asserted a note here would be asserting that the
// door had FAILED to deliver.
//
// Find-or-create is tested where it can be seen: against tmux's own `duplicate session:` at the unit
// level. It is also not what runs on a second press in a live hub — the created pane is Adopted with
// the row's session uuid, so the next agents poll folds the pane-less row into that pane and `a`
// becomes an ordinary attach.
func TestE2EUIDoorTheOperatorEndsUpInsideAndASecondPressMakesNoSecondSession(t *testing.T) {
	f := doorFleet(t, "blocked")
	f.start(t, 120, 40)
	walkTo(t, f.ui, doorAgentName)

	want := doorAgentName + "-" + doorAgentID
	send(t, f.ui, "a")
	f.waitForSession(t, want)

	// The verb's own output is on the screen the operator is now looking at.
	f.waitOr(t, "the hub's client to be inside the woken session", 20*time.Second, func() bool {
		s, err := paneScreen(t, f.ui, "ui")
		return err == nil && strings.Contains(s, "WOKE-"+doorAgentID)
	})

	send(t, f.ui, "a")
	time.Sleep(2 * time.Second) // long enough for a second create to have landed
	var made int
	for _, s := range f.sessions(t) {
		if strings.Contains(s, doorAgentID) {
			made++
		}
	}
	if made != 1 {
		t.Errorf("%d sessions carry the row's id after two presses: %v", made, f.sessions(t))
	}
}

// A row the listing calls `failed` costs something to wake, so `a` ASKS — and a key that is not enter
// leaves it alone, with nothing created.
func TestE2EUIDoorAReapedRowAsksFirstAndCancellingMakesNothing(t *testing.T) {
	f := doorFleet(t, "failed")
	f.start(t, 120, 40)
	walkTo(t, f.ui, doorAgentName)
	send(t, f.ui, "a")

	screenHas(t, f.ui, "wake "+doorAgentName+" on scratch?",
		"a wake that can cost something must ask before it acts")
	screenHas(t, f.ui, "the listing says failed",
		"and the dialog must quote the listing's own word, unfolded")
	screenHas(t, f.ui, "spend tokens",
		"the cost measured on 2026-08-17 — a replay can make the session pick the work up again")
	screenHas(t, f.ui, "any other key leaves it alone",
		"and it must say how to say no")

	send(t, f.ui, "x")
	waitUntil(t, "the hub to say it left the row alone", 15*time.Second, func() bool {
		s, err := paneScreen(t, f.ui, "ui")
		return err == nil && strings.Contains(s, "left "+doorAgentName+" alone")
	})
	for _, s := range f.sessions(t) {
		if strings.Contains(s, doorAgentID) {
			t.Errorf("a cancelled wake made %q anyway", s)
		}
	}
	for _, v := range f.verbs(t) {
		if strings.HasPrefix(v, "attach") {
			t.Errorf("a cancelled wake ran the verb: %q", v)
		}
	}
}

// And enter on that same dialog does wake it.
func TestE2EUIDoorEnterOnTheDialogWakesIt(t *testing.T) {
	f := doorFleet(t, "failed")
	f.start(t, 120, 40)
	walkTo(t, f.ui, doorAgentName)
	send(t, f.ui, "a")
	screenHas(t, f.ui, "wake "+doorAgentName+" on scratch?", "the dialog must be up")

	send(t, f.ui, "Enter")
	f.waitForSession(t, doorAgentName+"-"+doorAgentID)
}
