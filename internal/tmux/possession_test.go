package tmux

import (
	"context"
	"strings"
	"testing"
)

// The pair §20 specifies, verbatim and by id. Measured on tmux 3.7b from inside a
// pane of the same server with NO -c: rc=0 both, and the attached client moves
// (status-left [hub] -> [work]).
func TestSwitchClientAndSelectWindowSendExactlyTheSpecifiedArgv(t *testing.T) {
	r := &fakeRunner{}
	if err := SwitchClient(context.Background(), r, target(), "$1"); err != nil {
		t.Fatal(err)
	}
	assertArgv(t, r.last, "switch-client", "-t", "$1")

	r2 := &fakeRunner{}
	if err := SelectWindow(context.Background(), r2, target(), "@4"); err != nil {
		t.Fatal(err)
	}
	assertArgv(t, r2.last, "select-window", "-t", "@4")
}

// RC is checked separately from err because execRunner returns a nil error for a
// non-zero tmux exit. Without this, a refused switch (the session died between the
// poll and the keypress) reads as a successful jump and the operator is told they
// were moved somewhere they are not.
func TestPossessionVerbsSurfaceANonZeroRC(t *testing.T) {
	r := &fakeRunner{rc: 1, stderr: "can't find session $9"}
	err := SwitchClient(context.Background(), r, target(), "$9")
	if err == nil || !strings.Contains(err.Error(), "can't find session $9") {
		t.Fatalf("err = %v, want tmux's own message", err)
	}
	r2 := &fakeRunner{rc: 1, stderr: "can't find window @9"}
	if err := SelectWindow(context.Background(), r2, target(), "@9"); err == nil {
		t.Fatal("SelectWindow swallowed rc=1")
	}
	r3 := &fakeRunner{rc: 1, stderr: "no space for new window"}
	err = AttachWindow(context.Background(), r3, target(), "$0", "nuc", "ssh x")
	if err == nil {
		t.Fatal("AttachWindow swallowed rc=1")
	}
	if !strings.Contains(err.Error(), "no space for new window") {
		t.Errorf("err = %v, want tmux's own message — the caller turns this into "+
			"\"cannot go there\", which is only honest if it says what tmux said", err)
	}
}

// The remote container: -n so the window list reads as the host rather than as
// `ssh`, and the command as ONE argv element because that is what tmux hands to a
// shell.
//
// The literal has the SHAPE the hub now sends (internal/ui's WindowPayload): the
// attach argv quoted once, that whole script quoted again inside `sh -c`. It is
// abbreviated on purpose — the wrapper's exact text is asserted where it is built,
// and what this file has to prove is that a payload carrying two levels of quoting
// still reaches tmux as ONE argument and still keeps its opaqueArg exemption, which
// is what the Validate call below asks. What a shell then makes of it is asserted in
// internal/ui: TestNoShellCanAlterTheWindowPathPayload.
func TestAttachWindowNamesTheWindowAndPassesTheCommandWhole(t *testing.T) {
	r := &recordingRunner{}
	cmd := `'sh' '-c' ''\''ssh'\'' '\''-S'\'' '\''/home/dev/.ssh/cm-nuc'\'' '\''-t'\'' ` +
		`'\''nuc'\'' '\''tmux'\'' '\''attach'\'' '\''-t'\'' '\''$3'\''; read _'`
	if err := AttachWindow(context.Background(), r, target(), "$0", "nuc", cmd); err != nil {
		t.Fatal(err)
	}
	assertArgv(t, r.calls[0], "new-window", "-t", "$0", "-n", "nuc", cmd)
	for _, argv := range r.calls {
		if err := Validate(argv); err != nil {
			t.Fatalf("the argv AttachWindow builds must survive the seam: %v (%q)", err, argv)
		}
	}
}

// ONE command, and this is a floor rather than a tidiness check: the second command
// this used to send was `set -w remain-on-exit on`, and it LOST a race it could only
// win on time — `new-window` is what starts the payload, so a payload that dies
// first takes the window with it (measured on 3.7b over a private socket: `false`
// survived 6 of 12 trials). The window holds itself open through its payload now
// (internal/ui's WindowPayload, proved against a real server in internal/e2e), so a
// re-added option here would not merely be redundant — it would put a dead pane
// behind the keypress that line asks for.
func TestAttachWindowSendsOnlyTheCreateAndSetsNoOption(t *testing.T) {
	r := &recordingRunner{}
	if err := AttachWindow(context.Background(), r, target(), "$0", "nuc", "ssh x"); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("AttachWindow sent %d commands, want 1 (the create alone): %q",
			len(r.calls), r.calls)
	}
	for _, argv := range r.calls {
		if len(argv) > 0 && argv[0] == "set" {
			t.Errorf("AttachWindow set an option: %q — the payload is what keeps the "+
				"window, and an option set after new-window cannot", argv)
		}
		if hasArg(argv, "-P") || hasArg(argv, "#{window_id}") {
			t.Errorf("AttachWindow reads a window id back: %q — nothing consumes it now "+
				"that no option is set, and an unreadable id was a failure mode of its own", argv)
		}
	}
}

// recordingRunner keeps every invocation, because the assertion that matters is how
// MANY there are and a runner that remembers only the last cannot make it.
type recordingRunner struct {
	calls [][]string
}

func (r *recordingRunner) Run(_ context.Context, _ Target, args ...string) (Result, error) {
	argv := make([]string, len(args))
	copy(argv, args)
	r.calls = append(r.calls, argv)
	return Result{}, nil
}

func hasArg(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}

// Measured: `display -p -F '#{pid}:#{start_time}'` answers rc=0 with NO client
// attached, which is what makes it usable for the locality check — the hub is not
// an attached client of the servers it polls.
func TestServerEpochReadsThePidAndStartTime(t *testing.T) {
	r := &fakeRunner{stdout: "1525344:1786587652\n"}
	got, err := ServerEpoch(context.Background(), r, target())
	if err != nil {
		t.Fatal(err)
	}
	if got != "1525344:1786587652" {
		t.Fatalf("epoch = %q, want %q", got, "1525344:1786587652")
	}
	assertArgv(t, r.last, "display", "-p", "-F", "#{pid}:#{start_time}")
}

// An empty answer at rc=0 is the shape that fails OPEN: a locality check that
// compares "" to "" would call every server the same one, and the hub would run
// switch-client against a remote pane id.
func TestServerEpochRefusesAnEmptyAnswer(t *testing.T) {
	r := &fakeRunner{stdout: "\n"}
	if _, err := ServerEpoch(context.Background(), r, target()); err == nil {
		t.Fatal("an empty epoch must be an error, not an empty string")
	}
}
