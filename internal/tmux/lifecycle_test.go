package tmux

import (
	"context"
	"strings"
	"testing"
)

// fakeRunner records the last argv and returns canned output.
type fakeRunner struct {
	stdout string
	stderr string
	rc     int
	last   []string
	// calls is how many times tmux was RUN, which is a claim no argv assertion can
	// make: a batched write is one invocation and an empty write list is none.
	calls int
}

func (r *fakeRunner) Run(ctx context.Context, t Target, args ...string) (Result, error) {
	r.last = args
	r.calls++
	return Result{Stdout: r.stdout, Stderr: r.stderr, RC: r.rc}, nil
}

// target returns a minimal target for unit tests.
func target() Target {
	return Target{Label: "test", Socket: "/tmp/test.sock"}
}

// assertArgv checks that the recorded argv matches exactly.
func assertArgv(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("argv length = %d, want %d\ngot:  %q\nwant: %q", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q\nfull: %q", i, got[i], want[i], got)
		}
	}
}

// argvOf runs fn against a recording runner and returns the argv it captured.
func argvOf(t *testing.T, fn func(Runner)) []string {
	t.Helper()
	r := &fakeRunner{stdout: "%1\n"}
	fn(r)
	return r.last
}

func TestNewWindowReturnsTheNewPaneID(t *testing.T) {
	// -P -F is what makes a created pane knowable without a search. The whole
	// identity-at-birth argument (docs/design.md §19) rests on this one string.
	r := &fakeRunner{stdout: "%7\n"}
	id, err := NewWindow(context.Background(), r, target(), "$0", "/srv/api", "claude --session-id abc")
	if err != nil {
		t.Fatal(err)
	}
	if id != "%7" {
		t.Fatalf("paneID = %q, want %q", id, "%7")
	}
	assertArgv(t, r.last, "new-window", "-t", "$0", "-c", "/srv/api", "-P", "-F", "#{pane_id}", "claude --session-id abc")
}

func TestNewWindowKeepsCWDAsItsOwnArgvElement(t *testing.T) {
	// Measured: `-c "/a/dir with space"` works because it is one argv element.
	// Joining it into the command string would need quoting and would be a
	// class of bug rather than an instance.
	r := &fakeRunner{stdout: "%1\n"}
	if _, err := NewWindow(context.Background(), r, target(), "$0", "/a/dir with space", "claude"); err != nil {
		t.Fatal(err)
	}
	for i, a := range r.last {
		if a == "-c" {
			if got := r.last[i+1]; got != "/a/dir with space" {
				t.Fatalf("cwd argv element = %q", got)
			}
			return
		}
	}
	t.Fatal("no -c in argv")
}

func TestNewWindowOmitsCWDWhenEmpty(t *testing.T) {
	// `-c ""` is not "no directory" to tmux. Omit the flag instead.
	r := &fakeRunner{stdout: "%2\n"}
	if _, err := NewWindow(context.Background(), r, target(), "$0", "", "sh"); err != nil {
		t.Fatal(err)
	}
	for _, a := range r.last {
		if a == "-c" {
			t.Fatal("-c must be omitted when cwd is empty")
		}
	}
}

func TestPaneAliveReadsEMPTINESSNotAnError(t *testing.T) {
	// Measured: display -p -t %999 '#{pane_id}' returns rc=0 and an EMPTY
	// string — tmux does not error with no client attached. So a check that
	// waits for a non-zero exit code concludes every pane is alive, forever.
	dead := &fakeRunner{stdout: "\n"}
	alive, err := PaneAlive(context.Background(), dead, target(), "%999")
	if err != nil {
		t.Fatal(err)
	}
	if alive {
		t.Fatal("an empty display result means the pane is GONE")
	}

	live := &fakeRunner{stdout: "%3\n"}
	alive, err = PaneAlive(context.Background(), live, target(), "%3")
	if err != nil {
		t.Fatal(err)
	}
	if !alive {
		t.Fatal("a pane that echoes its own id is alive")
	}
}

func TestPaneAliveRejectsAnEchoOfADifferentPane(t *testing.T) {
	// If tmux ever falls back to another pane, "non-empty" is not enough.
	r := &fakeRunner{stdout: "%4\n"}
	alive, err := PaneAlive(context.Background(), r, target(), "%3")
	if err != nil {
		t.Fatal(err)
	}
	if alive {
		t.Fatal("%4 is not evidence about %3")
	}
}

func TestANONZEROTmuxExitIsAFailureCarryingTMUXOWNMESSAGE(t *testing.T) {
	// THE contract of this package, and it is a trap: execRunner.Run returns a
	// NIL error when tmux exits non-zero (run.go:201 — `res.RC = ee.ExitCode();
	// return res, nil`). Every existing caller checks res.RC by hand
	// (assert.go:54, labels.go:54, delta.go:132, capture.go:129,
	// stamp.go:51). A verb that checks only `err` therefore treats a REFUSED
	// new-window as a success and then fails on the empty pane id, hiding the
	// one message the user needs — `can't find directory /nope`.
	r := &fakeRunner{rc: 1, stderr: "can't find directory /nope"}
	_, err := NewWindow(context.Background(), r, target(), "$0", "/nope", "claude")
	if err == nil {
		t.Fatal("a non-zero tmux exit must be an error")
	}
	if !strings.Contains(err.Error(), "can't find directory /nope") {
		t.Fatalf("the error must carry tmux's own message, got %q", err)
	}
}

func TestEveryVerbBuildsArgvTheSEAMACCEPTS(t *testing.T) {
	// NOT "the verbs call Validate" — they cannot be tested for that here,
	// because Validate lives INSIDE execRunner.Run (run.go:174) and a fake
	// runner bypasses it by construction. The real hazard is the opposite
	// direction, and this project has already been bitten by it: a tightened
	// Validate once rejected every command that targeted a pane, including
	// seven working call sites. So assert that each verb's argv is ACCEPTED.
	cases := map[string][]string{
		"new-window": argvOf(t, func(r Runner) {
			NewWindow(context.Background(), r, target(), "$0", "/srv/api", "claude --session-id abc")
		}),
		"new-session":   argvOf(t, func(r Runner) { NewSession(context.Background(), r, target(), "proj", "/srv", "claude") }),
		"respawn-pane":  argvOf(t, func(r Runner) { RespawnPane(context.Background(), r, target(), "%3", "claude --resume abc") }),
		"kill-window":   argvOf(t, func(r Runner) { KillWindow(context.Background(), r, target(), "@4") }),
		"kill-session":  argvOf(t, func(r Runner) { KillSession(context.Background(), r, target(), "$1") }),
		"set -w":        argvOf(t, func(r Runner) { SetWindowOption(context.Background(), r, target(), "@4", "remain-on-exit", "on") }),
		"display alive": argvOf(t, func(r Runner) { PaneAlive(context.Background(), r, target(), "%3") }),
	}
	for name, argv := range cases {
		if err := Validate(argv); err != nil {
			t.Errorf("%s builds argv the seam refuses: %v\nargv: %q", name, err, argv)
		}
	}
}

// NewSession carries the duplicate sentinel, so a caller can tell "the name is taken" from every
// other rc=1 — a missing far tmux, a dead ssh master, a wrong socket path.
//
// It did not, and CreateSession has since the door needed it, so the LAUNCH could only hand tmux's
// sentence back to an operator who never typed the name. Both creates answer the same question now.
func TestNewSessionNamesTheDuplicateAsSuch(t *testing.T) {
	dup := &fakeRunner{rc: 1, stderr: "duplicate session: st"}
	_, err := NewSession(context.Background(), dup, target(), "st", "/w/x", "claude")
	if !IsDuplicateSession(err) {
		t.Errorf("err = %v, want the duplicate sentinel — without it the caller cannot retry with "+
			"another name and every launch into that directory fails", err)
	}
	// And tmux's own words survive the wrapping, because they are what a reader can act on.
	if err == nil || !strings.Contains(err.Error(), "duplicate session: st") {
		t.Errorf("err = %v, want tmux's own sentence inside it", err)
	}

	// Every OTHER rc=1 is itself, never a duplicate: retrying under a second name would hide a dead
	// transport behind a name change.
	other := &fakeRunner{rc: 1, stderr: "no server running on /tmp/tmux-1000/nope"}
	_, err = NewSession(context.Background(), other, target(), "st", "/w/x", "claude")
	if IsDuplicateSession(err) {
		t.Errorf("a dead server was reported as a taken name: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "no server running") {
		t.Errorf("err = %v, want tmux's own sentence", err)
	}
}
