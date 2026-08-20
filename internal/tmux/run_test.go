package tmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestValidateRejectsForbiddenFormats(t *testing.T) {
	cases := []string{
		"#{client_activity}",
		"#{q:client_activity}",
		"#{?client_activity,y,n}",
		"prefix#{client_created}suffix",
	}
	for _, c := range cases {
		if err := Validate([]string{"display", "-p", c}); !errors.Is(err, ErrForbiddenFormat) {
			t.Errorf("Validate(%q) = %v, want ErrForbiddenFormat", c, err)
		}
	}
}

// The one arm of the socket-override tests that has to NAME a forbidden format, which
// is why it is here: this file is exempt from guard_test.go's tree ban, and
// tmuxargs_test.go holds the rest so that it stays under the ban like any other file.
//
// A crash-format in a user's `tmux_args` reaches the same server a crash-format in a
// hub call site would, and it arrives from further away — nobody reviewed hosts.toml.
// So the override goes through Validate, and this is the arm that says so with the
// literal spelled out rather than read back out of forbiddenVars, which would go green
// if that list were ever emptied.
func TestAForbiddenFormatInTheSocketOverrideIsRefused(t *testing.T) {
	r := NewExec(time.Second).(*execRunner)
	for _, args := range [][]string{
		{"-L", "#{client_activity}"},
		{"-S", "/tmp/#{client_created}"},
	} {
		tgt := Target{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/cm-nuc", TmuxArgs: args}
		if _, err := r.build(tgt, []string{"list-panes", "-a"}); !errors.Is(err, ErrForbiddenFormat) {
			t.Errorf("build with tmux_args %q = %v, want ErrForbiddenFormat — this format "+
				"segfaults a whole 3.2a server, and a config file is not a trusted caller", args, err)
		}
	}
}

func TestValidateRejectsLiteralPercent(t *testing.T) {
	bad := []string{"CONFIRM-%2", "OK %s", "100%", "%"}
	for _, c := range bad {
		if err := Validate([]string{"display", "-p", c}); !errors.Is(err, ErrPercentInArg) {
			t.Errorf("Validate(%q) = %v, want ErrPercentInArg", c, err)
		}
	}
}

func TestValidateAllowsPaneIDs(t *testing.T) {
	ok := [][]string{
		{"capture-pane", "-p", "-e", "-t", "%0", "-S", "18", "-E", "23"},
		{"display", "-p", "-t", "%12", "#{pane_id} #{pane_height}"},
		{"list-panes", "-a", "-F", "#{pane_id}|#{window_activity}"},
	}
	for _, args := range ok {
		if err := Validate(args); err != nil {
			t.Errorf("Validate(%v) = %v, want nil", args, err)
		}
	}
}

func TestRunRefusesEmptySocket(t *testing.T) {
	r := NewExec(2 * time.Second)
	_, err := r.Run(context.Background(), Target{Label: "local"}, "list-panes")
	if !errors.Is(err, ErrNoSocket) {
		t.Fatalf("Run with empty socket = %v, want ErrNoSocket", err)
	}
}

// A real server on a private socket. Never the default socket, never live1.
func testServer(t *testing.T) Target {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := filepath.Join(t.TempDir(), "tmux.sock")
	cmd := exec.Command("tmux", "-S", sock, "-f", "/dev/null",
		"new-session", "-d", "-s", "one", "-x", "80", "-y", "24")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", sock, "kill-server").Run()
	})
	return Target{Label: "test", Socket: sock}
}

func TestRunAgainstRealServer(t *testing.T) {
	tgt := testServer(t)
	r := NewExec(5 * time.Second)
	res, err := r.Run(context.Background(), tgt, "list-panes", "-a", "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("Run: %v (stderr=%q)", err, res.Stderr)
	}
	if res.RC != 0 {
		t.Fatalf("RC = %d, stderr = %q", res.RC, res.Stderr)
	}
	if !strings.HasPrefix(strings.TrimSpace(res.Stdout), "%") {
		t.Fatalf("Stdout = %q, want a pane id", res.Stdout)
	}
}

func TestRunReportsNonZeroWithoutError(t *testing.T) {
	tgt := testServer(t)
	r := NewExec(5 * time.Second)
	res, err := r.Run(context.Background(), tgt, "capture-pane", "-p", "-t", "%999")
	if err != nil {
		t.Fatalf("a tmux failure must come back as RC, not err: %v", err)
	}
	if res.RC == 0 {
		t.Fatal("RC = 0, want non-zero for a missing pane")
	}
	if !strings.Contains(res.Stderr, "find pane") {
		t.Fatalf("Stderr = %q, want it to mention the missing pane", res.Stderr)
	}
}

func TestRunEnforcesDeadline(t *testing.T) {
	r := NewExec(300 * time.Millisecond)
	start := time.Now()
	// A tmux command against a socket with no listener fails fast, so the deadline
	// is exercised through the same code path with a command that blocks. RunRaw is
	// reached by assertion rather than being part of the Runner interface, so that
	// callers cannot bypass Validate by using it.
	rr, ok := r.(interface {
		RunRaw(context.Context, string, ...string) (Result, error)
	})
	if !ok {
		t.Fatal("the exec runner must expose RunRaw so the deadline is testable")
	}
	res, err := rr.RunRaw(context.Background(), "sleep", "10")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("want a deadline error, got RC=%d", res.RC)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("deadline not enforced: took %v", elapsed)
	}
}

func TestRunValidatesBeforeExecuting(t *testing.T) {
	tgt := testServer(t)
	r := NewExec(5 * time.Second)
	_, err := r.Run(context.Background(), tgt, "display", "-p", "#{client_activity}")
	if !errors.Is(err, ErrForbiddenFormat) {
		t.Fatalf("Run must reject before executing, got %v", err)
	}
}

// The guarded send is ONE argument that contains `-t %12`. The original rule —
// "% is legal only in an argument that is exactly a pane id" — rejects it, so the
// entire write path could not go through the seam.
func TestValidateAcceptsAPaneIDInsideASubCommand(t *testing.T) {
	sub := "paste-buffer -d -p -r -b tmux-hub-ab12-1 -t %12 ; " +
		"display -p -t %12 'OK #{pane_id} #{@hub_ab12}'"
	if err := Validate([]string{"if", "-F", "-t", "%12", "#{==:#{@hub_ab12},u}", sub}); err != nil {
		t.Fatalf("the guarded send must be expressible: %v", err)
	}
}

// ...and the strftime hole must stay closed. `display -p 'OK %2'` returns an empty
// string at rc=0, so a %NN that is NOT a -t value is exactly the measured bug.
func TestValidateStillRefusesAPercentInATemplate(t *testing.T) {
	for _, args := range [][]string{
		{"display", "-p", "-t", "%2", "OK %2"},
		{"display", "-p", "CONFIRM-%2"},
		{"display", "-p", "-t", "%2", "activity %Y"},
		{"if", "-F", "-t", "%1", "cond", "display -p -t %1 'OK %1'"},
	} {
		if err := Validate(args); !errors.Is(err, ErrPercentInArg) {
			t.Errorf("Validate(%q) = %v, want ErrPercentInArg", args, err)
		}
	}
}

// An empty -t fails OPEN: measured, a send-keys whose -t value is the empty
// string returns rc=0 and
// delivers to the server's current pane — the pane the user last touched. A stale
// %999 fails closed, which is safe. So only the open direction needs refusing, and
// it can only ever be a hub defect.
func TestValidateRefusesATargetThatIsNotAPaneID(t *testing.T) {
	for _, args := range [][]string{
		{"send-keys", "-t", "", "-l", "X"},
		{"paste-buffer", "-t", "mysession", "-b", "b"},
		{"display", "-p", "-t", "%", "x"},
		{"if", "-F", "-t", "%1", "cond", "paste-buffer -t  -b b"},
	} {
		if err := Validate(args); !errors.Is(err, ErrBadTarget) {
			t.Errorf("Validate(%q) = %v, want ErrBadTarget", args, err)
		}
	}
}

// A dangling -t must be refused at BOTH levels. Measured before this was fixed:
// Validate(["display","-p","-t"]) returned nil while the same dangling -t inside a
// sub-command string was refused — and argv is the less guarded of the two paths,
// so the gap was on the side that passes.
func TestValidateRefusesADanglingTargetFlagInArgv(t *testing.T) {
	for _, args := range [][]string{
		{"display", "-p", "-t"},
		{"send-keys", "-l", "X", "-t"},
		{"if", "-F", "-t", "%1", "cond", "paste-buffer -b b -t"},
	} {
		if err := Validate(args); !errors.Is(err, ErrBadTarget) {
			t.Errorf("Validate(%q) = %v, want ErrBadTarget", args, err)
		}
	}
	// A -t that is not last still needs its value checked, and a command whose last
	// argument merely CONTAINS -t is not a dangling flag.
	if err := Validate([]string{"display", "-p", "-t", "%3", "#{pane_id}"}); err != nil {
		t.Errorf("a well-formed -t was refused: %v", err)
	}
}

// A -t that names a real pane is fine at both levels.
func TestValidateAcceptsWellFormedTargets(t *testing.T) {
	for _, args := range [][]string{
		{"capture-pane", "-p", "-t", "%0"},
		{"display", "-p", "-t", "%137", "#{pane_id} #{window_activity}"},
		{"list-panes", "-a", "-F", "#{pane_id}|#{session_id}"},
	} {
		if err := Validate(args); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", args, err)
		}
	}
}

// The payload never touches argv, so the seam must carry it on stdin.
func TestRunInputFeedsStdin(t *testing.T) {
	tgt := testServer(t) // the helper this package already has
	r := NewExec(10 * time.Second).(*execRunner)

	payload := []byte("first line;\nsecond line with % and ; inside\n")
	if _, err := r.RunInput(context.Background(), tgt, payload,
		"load-buffer", "-b", "probe", "-"); err != nil {
		t.Fatalf("RunInput: %v", err)
	}
	res, err := r.Run(context.Background(), tgt, "show-buffer", "-b", "probe")
	if err != nil {
		t.Fatalf("show-buffer: %v", err)
	}
	if res.Stdout != string(payload) {
		t.Errorf("buffer round-trip changed the text:\n got %q\nwant %q", res.Stdout, payload)
	}
}

// The payload is not argv, so it is not validated — a prompt containing a literal
// % or a trailing ; is ordinary text and must survive untouched. Only the ARGS are
// checked.
func TestRunInputValidatesArgsButNotThePayload(t *testing.T) {
	tgt := testServer(t) // the helper this package already has
	r := NewExec(10 * time.Second).(*execRunner)

	if _, err := r.RunInput(context.Background(), tgt, []byte("100% done; really"),
		"load-buffer", "-b", "pct", "-"); err != nil {
		t.Fatalf("a payload with %% and ; must be accepted: %v", err)
	}
	if _, err := r.RunInput(context.Background(), tgt, []byte("x"),
		"load-buffer", "-b", "OK %2", "-"); !errors.Is(err, ErrPercentInArg) {
		t.Errorf("args must still be validated, got %v", err)
	}
}

func TestRunInputRefusesAnEmptySocket(t *testing.T) {
	r := NewExec(time.Second).(*execRunner)
	if _, err := r.RunInput(context.Background(), Target{Label: "x"}, []byte("y"),
		"load-buffer", "-"); !errors.Is(err, ErrNoSocket) {
		t.Fatalf("got %v, want ErrNoSocket", err)
	}
}

func TestValidateAcceptsTheLIFECYCLETargets(t *testing.T) {
	// Verified against real tmux 3.7b: every one of these forms works.
	//   new-window -t $0            → created %1 in that session
	//   set -w -t @1 remain-on-exit → rc=0, value reads back `on`
	//   kill-window -t @1           → rc=0
	//   kill-session -t $1          → rc=0
	for _, argv := range [][]string{
		{"new-window", "-t", "$0", "-c", "/srv/api", "-P", "-F", "#{pane_id}", "claude --session-id abc"},
		{"kill-window", "-t", "@4"},
		{"kill-session", "-t", "$1"},
		{"set", "-w", "-t", "@4", "remain-on-exit", "on"},
		{"new-session", "-d", "-s", "proj", "-c", "/srv", "-P", "-F", "#{pane_id}", "claude"},
		{"respawn-pane", "-k", "-t", "%3", "claude --resume abc"},
	} {
		if err := Validate(argv); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", argv, err)
		}
	}
}

func TestTheWRITEVerbsStillDemandAPaneID(t *testing.T) {
	// The guard the loosening must not touch. A send addressed to a WINDOW
	// lands in that window's active pane — not the pane the user verified —
	// which is precisely the write-into-the-wrong-agent this branch exists to
	// make impossible.
	for _, argv := range [][]string{
		{"send-keys", "-t", "@4", "-l", "hello"},
		{"send-keys", "-t", "$1", "-l", "hello"},
		{"paste-buffer", "-d", "-p", "-r", "-b", "buf", "-t", "@4"},
		{"capture-pane", "-p", "-t", "@4"},
		{"display", "-p", "-t", "$1", "#{pane_id}"},
		{"respawn-pane", "-k", "-t", "@4"},
	} {
		if err := Validate(argv); !errors.Is(err, ErrBadTarget) {
			t.Errorf("Validate(%q) = %v, want ErrBadTarget", argv, err)
		}
	}
}

func TestSetPStillDemandAPaneIDWhileSetWDemandsAWindowID(t *testing.T) {
	// `set` is the one verb used at two scopes: the stamper writes `set -p`
	// on a PANE (broadcast/stamp.go), lifecycle writes `set -w` on a WINDOW.
	// Keying the shape on the verb alone would let `set -p -t @4` through, and a
	// pane-scoped option written against a window target is the stamp landing
	// somewhere nobody checked.
	if err := Validate([]string{"set", "-p", "-t", "@4", "@hub_x", "1"}); !errors.Is(err, ErrBadTarget) {
		t.Errorf("set -p with a window target must be refused, got %v", err)
	}
	if err := Validate([]string{"set", "-p", "-t", "%3", "@hub_x", "1"}); err != nil {
		t.Errorf("set -p with a pane id must still pass: %v", err)
	}
	if err := Validate([]string{"set", "-w", "-t", "%3", "remain-on-exit", "on"}); !errors.Is(err, ErrBadTarget) {
		t.Errorf("set -w with a pane target must be refused, got %v", err)
	}
}

func TestAnEmptyTargetIsRefusedForEVERYVerb(t *testing.T) {
	// The original fail-open, and it must stay closed at every shape: an empty
	// -t means "whatever is current", so `kill-window -t ''` destroys the
	// window the user is looking at.
	for _, argv := range [][]string{
		{"new-window", "-t", ""},
		{"kill-window", "-t", ""},
		{"kill-session", "-t", ""},
		{"set", "-w", "-t", "", "remain-on-exit", "on"},
		{"send-keys", "-t", "", "-l", "x"},
	} {
		if err := Validate(argv); !errors.Is(err, ErrBadTarget) {
			t.Errorf("Validate(%q) = %v, want ErrBadTarget", argv, err)
		}
	}
}

func TestASessionNAMEIsRefusedWhereAnIDIsRequired(t *testing.T) {
	// Names are refused everywhere, deliberately: an id cannot resolve to "the
	// current one", and the registry already carries SessionID ($N) and
	// WindowID (@N) for every pane, so the hub never needs to pass a name.
	if err := Validate([]string{"new-window", "-t", "api"}); !errors.Is(err, ErrBadTarget) {
		t.Errorf("a session NAME must be refused, got %v", err)
	}
}

func TestTheErrorSaysWHICHSHAPEWasExpected(t *testing.T) {
	// An error that carries its fix. `-t value is not a pane id: "@4"` is
	// actively misleading for kill-window, where a pane id is the wrong answer.
	err := Validate([]string{"kill-window", "-t", "%3"})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "window id") {
		t.Fatalf("the message must name the expected shape, got %q", err)
	}
}

// The container §20 puts the remote attach in. Validate refused it twice over
// before opaqueArg: on ssh's own -t (a tty request, not a tmux target) and on the
// remote tmux's -t $3. A ControlPath spelled ~/.ssh/cm-%h-%p-%r was refused a
// third time, as a literal % outside a pane id.
func TestValidateAcceptsThePossessionContainer(t *testing.T) {
	for _, argv := range [][]string{
		{"new-window", "-t", "$0", "-n", "nuc", "ssh -S /home/dev/.ssh/cm-nuc -t nuc tmux attach -t $3"},
		{"new-window", "-t", "$0", "-n", "nuc", "ssh -S /home/dev/.ssh/cm-%h-%p-%r -t nuc tmux attach -t $3"},
		// The shape the hub actually sends: every element shell-quoted, because tmux
		// hands this one argument to `$SHELL -c` and an unquoted `$3` is eaten there.
		// Quoting must not cost the exemption — it is still ONE trailing argument
		// whose predecessor does not start with `-`, which is all opaqueArg keys on.
		{"new-window", "-t", "$0", "-n", "nuc", `'ssh' '-S' '/home/dev/.ssh/cm-nuc' '-t' 'nuc' 'tmux' 'attach' '-t' '$3'`},
	} {
		if err := Validate(argv); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", argv, err)
		}
	}
}

// The exemption is for the SHELL payload only. A forbidden format hidden inside
// it must still be refused, because Validate scans every argument for those
// before anything else — that is the invariant which may never be scoped.
func TestTheOpaqueArgumentIsStillScannedForForbiddenFormats(t *testing.T) {
	argv := []string{"new-window", "-t", "$0", "-n", "x", "sh -c 'tmux display -p \"#{client_activity}\"'"}
	if err := Validate(argv); !errors.Is(err, ErrForbiddenFormat) {
		t.Errorf("Validate(%q) = %v, want ErrForbiddenFormat", argv, err)
	}
}

// The guard that makes the exemption safe. `respawn-pane -k -t @4` has a command
// verb as its outer verb AND -t as its second-to-last argument, so it has no
// shell payload at all: the last argument IS the target value, and exempting it
// would open the write path to a window target. Keyed on args[len-2] being a
// flag, it stays refused.
func TestACommandVerbWithNoPayloadIsNotExempted(t *testing.T) {
	for _, argv := range [][]string{
		{"respawn-pane", "-k", "-t", "@4"},
		{"new-window", "-t", "@4"},
		{"new-session", "-d", "-s", "proj", "-t", "$1"},
	} {
		if err := Validate(argv); !errors.Is(err, ErrBadTarget) {
			t.Errorf("Validate(%q) = %v, want ErrBadTarget", argv, err)
		}
	}
}

// switch-client addresses a SESSION and select-window a WINDOW. Both are new
// verbs in the seam; without a shape they inherited the pane-only default and
// were refused. `-t 0` is the trap $TMUX sets: its third field is a bare number,
// and a session target needs the $ sigil, so 0 would name a session CALLED 0.
// The raw door must be reachable from the PUBLIC api, and that was the gap: `Exec` is
// Runner+InputRunner, `RunRaw` is in neither, and it is a method on the unexported
// *execRunner — so master.go's four entry points each took an interface nothing a
// caller could supply, and a hand-wired main did not build.
//
// Compile-time, and the method set is spelled out rather than imported, because
// internal/tmux must not depend on internal/hub: this is byte-for-byte the interface
// Ensure, Stop, ReconcileMasters and StopAllMasters each declare inline.
var _ interface {
	RunRaw(context.Context, string, ...string) (Result, error)
} = NewRawRunner(time.Second)

// ...and it must stay a SEPARATE door. This is the type-level counterpart of
// guard_test.go's RunRaw ban: the ban stops a production file CALLING it, and these
// stop the capability arriving in a caller's hands without being asked for.
func TestTheTwoDoorsStaySeparate(t *testing.T) {
	execT := reflect.TypeOf((*Exec)(nil)).Elem()
	rawT := reflect.TypeOf((*RawRunner)(nil)).Elem()
	// Asked of the interface TYPE, never of a value. NewExec's dynamic type is
	// *execRunner, which does have RunRaw, so `NewExec(…).(RawRunner)` succeeds and
	// says nothing about the API — it is the same assertion the deadline test makes on
	// purpose. What must hold is that RunRaw is not in Exec's METHOD SET.
	if execT.Implements(rawT) {
		t.Error("Exec has gained RunRaw: a caller holding an Exec can now reach the " +
			"unvalidated door without asking for it")
	}
	// And the reverse, so master work cannot quietly become tmux work: whoever holds
	// the raw door holds only that.
	for name, door := range map[string]reflect.Type{
		"Runner":      reflect.TypeOf((*Runner)(nil)).Elem(),
		"InputRunner": reflect.TypeOf((*InputRunner)(nil)).Elem(),
	} {
		if rawT.Implements(door) {
			t.Errorf("RawRunner has gained %s: the unvalidated door now also carries the validated ones", name)
		}
	}
	// The positive half of "two constructors over one implementation": both doors are
	// the same *execRunner under the same deadline, so a caller that asks for both
	// configures one process behaviour, not two.
	if _, ok := NewRawRunner(time.Second).(Exec); !ok {
		t.Error("NewRawRunner must return the same implementation that serves Exec")
	}
}

// shimPrintsArgv names the argv the seam really spawned, one bracketed element per
// argument, terminated by a newline. printf reuses its format once per argument, so
// the output is the far side's own account of what it received.
const shimPrintsArgv = "printf '[%s] ' \"$@\"\nprintf '\\n'\n"

// sshShim puts an `ssh` on PATH whose body is the given script, so the remote argv can
// be asserted against a real process without a live master.
//
// PATH is set with t.Setenv rather than on the command, because exec.Command resolves
// argv[0] through the PROCESS environment at construction time — a cmd.Env would not
// change which binary is found.
func sshShim(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// The safety property, and the reason the order is fixed: Validate enforces the -t
// shape over argv, and an ssh payload is ONE opaque string. Wrapping first would
// hide `send-keys -t @4` inside a payload the shape check cannot read, which is the
// write-into-the-wrong-pane this seam exists to prevent.
//
// Calibrated against the wrong order, and the calibration is why this test has the
// arms it has: `send-keys -t @4` ALONE cannot fail, because ShellJoin joins with
// spaces and validateArg reads a multi-token argument as a sub-command chain — so
// the wrapped payload is still re-split and `-t @4` still refused, incidentally.
// What the wrapping really destroys is the VERB: inside a payload shapeFor reads
// toks[0] as `'tmux'`, so every verb-specific shape collapses to the pane-only
// default. Hence the arms below, in BOTH directions.
func TestARemoteTargetValidatesTheTmuxArgvBeforeWrapping(t *testing.T) {
	rt := Target{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/cm-nuc"}
	r := NewExec(time.Second).(*execRunner)

	// Direction one: what Validate refuses, build must refuse. The last two are the
	// discriminating pair — a pane id passes the wrapped scan's default, so a check
	// read over the ssh argv waves through a window-shaped verb given a pane.
	for _, args := range [][]string{
		{"send-keys", "-t", "@4", "-l", "x"},
		{"kill-window", "-t", "%3"},
		{"set", "-w", "-t", "%3", "remain-on-exit", "on"},
	} {
		if err := Validate(args); err == nil {
			t.Fatalf("the tmux argv must still be refused: %q", args)
		}
		if _, err := r.build(rt, args); err == nil {
			t.Errorf("build accepted an argv Validate refuses: %q", args)
		}
	}

	// Direction two, and it is the half a one-directional test cannot see: what
	// Validate ACCEPTS must survive build. The shell-payload exemption is a property
	// of the tmux argv — its outer verb is `new-window` — and a check read over the
	// ssh argv sees `ssh` as the verb, loses the exemption, and refuses the very
	// container §20 attaches with.
	container := []string{"new-window", "-t", "$0", "-n", "nuc",
		"ssh -S /run/cm-nuc -t nuc tmux attach -t $3"}
	if err := Validate(container); err != nil {
		t.Fatalf("the container must be expressible: %v", err)
	}
	if _, err := r.build(rt, container); err != nil {
		t.Errorf("build refused an argv Validate accepts: %v", err)
	}

	// And the local half of the same property: there is no payload at all here, so a
	// check read over the built argv sees `tmux` as the verb and loses the shape the
	// same way.
	lt := Target{Label: "local", Socket: "/tmp/tmux-1000/default"}
	if _, err := r.build(lt, []string{"kill-window", "-t", "@4"}); err != nil {
		t.Errorf("build refused a well-formed local lifecycle argv: %v", err)
	}
}

// The shape of what actually runs. The tmux argv becomes ONE argument to ssh, so a
// whole poll cycle is one round trip — measured 327 ms against 698 ms for the same
// cycle as two invocations through a forwarded socket.
func TestARemoteTargetBuildsOneSSHInvocation(t *testing.T) {
	rt := Target{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/cm-nuc"}
	got, err := NewExec(time.Second).(*execRunner).build(rt, []string{"list-panes", "-a", "-F", "#{pane_id}"})
	if err != nil {
		t.Fatal(err)
	}
	assertArgv(t, got, "ssh", "-o", "BatchMode=yes", "-o", "ProxyCommand=false",
		"-S", "/run/cm-nuc", "nuc", `'tmux' 'list-panes' '-a' '-F' '#{pane_id}'`)
	// Said again as two properties rather than as one literal, because these are the
	// two halves that a wrong wrapping breaks separately: the payload must be a
	// quoted tmux command line, and the format must survive quoting intact — an
	// unquoted `#{pane_id}` would be globbed by the far shell.
	payload := got[len(got)-1]
	if !strings.HasPrefix(payload, "'tmux'") {
		t.Errorf("the payload must be a quoted tmux command line, got %q", payload)
	}
	if !strings.Contains(payload, `'#{pane_id}'`) {
		t.Errorf("the format must survive quoting intact, got %q", payload)
	}
}

// A local target is unchanged, and that is load-bearing: exec runs it with no shell
// at all, so quoting it would make tmux look for a session literally named `'$0'`.
func TestALocalTargetIsStillBareTmux(t *testing.T) {
	lt := Target{Label: "local", Socket: "/tmp/tmux-1000/default"}
	got, err := NewExec(time.Second).(*execRunner).build(lt, []string{"list-panes", "-a"})
	if err != nil {
		t.Fatal(err)
	}
	assertArgv(t, got, "tmux", "-S", "/tmp/tmux-1000/default", "list-panes", "-a")
}

// A remote target with no control path is a hub defect, not a runtime condition:
// the supervisor spawns the master before any host is polled.
func TestARemoteTargetWithoutAControlPathIsRefused(t *testing.T) {
	rt := Target{Label: "nuc", SSHDest: "nuc"}
	if _, err := NewExec(time.Second).(*execRunner).build(rt, []string{"list-panes", "-a"}); err == nil {
		t.Error("a remote target with no control path must be refused, not run bare")
	}
}

// RunInput must go through the same wrapping, or the write path keeps its guard and
// loses its payload transport: the whole reason the payload travels on stdin is that
// argv quoting truncates it, and a remote send is a `load-buffer -` at the far end.
//
// Asked of a real process rather than of build's return value, with an `ssh` on PATH
// that prints its own argv and then cats its stdin. That is what a far tmux would
// receive, and it is the one arm that can tell "ssh was spawned with the payload
// forwarded" from "ssh was spawned".
func TestARemoteRunInputCarriesThePayloadOnStdin(t *testing.T) {
	sshShim(t, shimPrintsArgv+"cat\n")

	// No Socket at all, which is also the discriminator: a remote target is reached
	// with no local socket file in existence.
	rt := Target{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/cm-nuc"}
	payload := "first line;\nsecond line with % and ; inside\n"
	res, err := NewExec(5*time.Second).(*execRunner).RunInput(context.Background(), rt,
		[]byte(payload), "load-buffer", "-b", "probe", "-")
	if err != nil {
		t.Fatalf("RunInput: %v (stderr=%q)", err, res.Stderr)
	}
	argvLine, stdinSeen, ok := strings.Cut(res.Stdout, "\n")
	if !ok {
		t.Fatalf("the shim printed nothing recognisable: %q", res.Stdout)
	}
	const wantArgv = `[-o] [BatchMode=yes] [-o] [ProxyCommand=false] [-S] [/run/cm-nuc] [nuc] ` +
		`['tmux' 'load-buffer' '-b' 'probe' '-'] `
	if argvLine != wantArgv {
		t.Errorf("ssh received a different argv:\n got %q\nwant %q", argvLine, wantArgv)
	}
	if stdinSeen != payload {
		t.Errorf("the payload did not reach the far command intact:\n got %q\nwant %q", stdinSeen, payload)
	}
	// The negative half: the payload must NOT be in argv, which is the defect the
	// stdin transport exists to make impossible.
	if strings.Contains(argvLine, "first line") {
		t.Errorf("the payload travelled as an argument: %q", argvLine)
	}
}

// The READ path needs the same arm, and it is the one the suite was missing: `Run` is
// four invocations per host per tick, the most-used code in the product, and reverting
// it to `tmux -S <socket> …` left all 74 tests green. A remote Run in that state polls
// the LOCAL server and reports its panes under a remote label.
//
// Same discriminator as the write path: the target has NO Socket, so nothing can pass
// unless Run really routes through build.
func TestARemoteRunSpawnsSSHAndNotLocalTmux(t *testing.T) {
	sshShim(t, shimPrintsArgv)

	rt := Target{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/cm-nuc"}
	res, err := NewExec(5*time.Second).(*execRunner).Run(context.Background(), rt,
		"list-panes", "-a", "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("Run: %v (stderr=%q)", err, res.Stderr)
	}
	const wantArgv = `[-o] [BatchMode=yes] [-o] [ProxyCommand=false] [-S] [/run/cm-nuc] [nuc] ` +
		`['tmux' 'list-panes' '-a' '-F' '#{pane_id}'] ` + "\n"
	if res.Stdout != wantArgv {
		t.Errorf("the poll path spawned a different process:\n got %q\nwant %q", res.Stdout, wantArgv)
	}
	// The negative half, and it is the failure this test exists for: a Run that fell
	// back to the local branch would run `tmux -S ""`, which the shim never sees.
	if strings.Contains(res.Stdout, "-S] [] ") {
		t.Errorf("the remote poll ran against an empty local socket: %q", res.Stdout)
	}
}

// A dead master must fail CLOSED and say what to do about it. Without
// ProxyCommand=false ssh silently opens its own connection and returns rc=0 with the
// right answer (§5, measured), so the host keeps looking Up while a tick budgeted at
// 1.3 s takes 8 s — the state hardest to debug. With the flag it is rc=255 and
// `Connection closed by UNKNOWN port 65535`, which is true and addressed to nobody.
//
// The shim IS the dead master here: it reproduces exactly that pair.
func TestADeadMasterFailsClosedWithARemedy(t *testing.T) {
	sshShim(t, "printf 'Connection closed by UNKNOWN port 65535\\n' >&2\nexit 255\n")

	rt := Target{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/cm-nuc"}
	res, err := NewExec(5*time.Second).(*execRunner).Run(context.Background(), rt, "list-panes", "-a")
	// A transport failure is an RC, not an error: host-state classification is a pure
	// function of Result, so turning this into an err would hide it from the taxonomy.
	if err != nil {
		t.Fatalf("a dead master must come back as an RC, not err: %v", err)
	}
	if res.RC != 255 {
		t.Fatalf("RC = %d, want 255 — ssh must fail rather than open its own connection", res.RC)
	}
	// Every part the operator needs in order to act: which host, which control path,
	// and the command that fixes it.
	for _, want := range []string{`"nuc"`, "/run/cm-nuc", "ssh -N -M", "retry"} {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("the message does not name %q, so it carries no remedy: %q", want, res.Stderr)
		}
	}
	// ssh's own words are kept, after the remedy rather than instead of it: a reader
	// debugging a ProxyJump needs them.
	if !strings.Contains(res.Stderr, "UNKNOWN port 65535") {
		t.Errorf("ssh's own text was discarded: %q", res.Stderr)
	}
	// And a LOCAL target's rc=255 is not rewritten — there is no master to blame.
	lt := Target{Label: "local", Socket: "/tmp/tmux-1000/default"}
	if got := explainTransport(lt, Result{RC: 255, Stderr: "tmux: bad"}); got.Stderr != "tmux: bad" {
		t.Errorf("a local failure was explained as a transport failure: %q", got.Stderr)
	}
}

func TestValidateAcceptsThePossessionTargets(t *testing.T) {
	for _, argv := range [][]string{
		{"switch-client", "-t", "$0"},
		{"select-window", "-t", "@12"},
	} {
		if err := Validate(argv); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", argv, err)
		}
	}
	for _, argv := range [][]string{
		{"switch-client", "-t", "0"},
		{"switch-client", "-t", "hub"},
		{"switch-client", "-t", "@4"},
		{"select-window", "-t", "api"},
		{"select-window", "-t", "$0"},
		{"select-window", "-t", ""},
	} {
		if err := Validate(argv); !errors.Is(err, ErrBadTarget) {
			t.Errorf("Validate(%q) = %v, want ErrBadTarget", argv, err)
		}
	}
}

// `set` with NO scope flag is SESSION-scoped, which is what tmux means by it and what the status
// line the hub writes needs (docs/design.md §12).
//
// The shape used to default to a PANE id, so a session-scoped write was refused at the seam. Two
// halves had to be got right, and the second one nearly shipped broken:
//
//   - a session target needs the `$` sigil, exactly as `switch-client` does, so forgetting it
//     cannot silently address a session NAMED `3`;
//   - the scope flags may be CLUSTERED. `broadcast/stamp.go` unstamps with `set -pu`, and a scan
//     that looks for the literal `-p` does not see it — it passed only because the default happened
//     to be a pane id, so moving the default without reading the clusters would have refused a live
//     write path.
func TestSetWithNoScopeFlagIsSessionScoped(t *testing.T) {
	for _, argv := range [][]string{
		{"set", "-t", "$3", "@hub_alias", "billing-cicd"},
		{"set", "-t", "$3", "status-left", "[#{?@hub_alias,#{@hub_alias} (#{session_name}),#{session_name}}]"},
		{"set", "-u", "-t", "$3", "@hub_alias"},
		{"set", "-g", "remain-on-exit", "on"},
		// The clustered pane flags, which must keep reaching a pane id.
		{"set", "-pu", "-t", "%3", "@hub_x"},
		{"set", "-up", "-t", "%3", "@hub_x"},
	} {
		if err := Validate(argv); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", argv, err)
		}
	}
	for _, c := range []struct {
		what string
		argv []string
	}{
		{"a session-scoped write against a pane id", []string{"set", "-t", "%3", "@hub_alias", "x"}},
		{"a session-scoped write against a window id", []string{"set", "-t", "@4", "@hub_alias", "x"}},
		{"a bare number, which would address a session NAMED 3",
			[]string{"set", "-t", "3", "@hub_alias", "x"}},
		{"the clustered pane flag against a window id", []string{"set", "-pu", "-t", "@4", "@hub_x"}},
	} {
		if err := Validate(c.argv); !errors.Is(err, ErrBadTarget) {
			t.Errorf("%s must be refused, got %v", c.what, err)
		}
	}
}

// The scope scan must stop at the option NAME, because a VALUE may look like a flag cluster.
//
// Measured on tmux 3.7b: `set -t p @hub_alias -wip` is rc=0 and the option reads back as `-wip`, so
// the server takes it as the value — while a scan of the whole argv read the `w`, demanded a window
// target and refused an alias tmux would have accepted. (`--` is not the escape: `set … -- -wip`
// answers rc=1 `too many arguments (need at most 2)`.) The rows that must still hold are the
// CLUSTERED scope flags before the name, which is what the scan exists for: `broadcast/stamp.go`
// unstamps with `set -pu`.
func TestTheScopeScanStopsAtTheOptionName(t *testing.T) {
	for _, c := range []struct {
		name string
		argv []string
		ok   bool
	}{
		{"a value that looks like a window cluster", []string{"set", "-t", "$3", "@hub_alias", "-wip"}, true},
		{"a value that looks like a pane cluster", []string{"set", "-t", "$3", "@hub_alias", "-prod"}, true},
		{"a value that is only dashes", []string{"set", "-t", "$3", "@hub_alias", "--"}, true},
		// The scan's whole reason: clustered scope flags BEFORE the name still decide the shape.
		{"clustered -pu still wants a pane", []string{"set", "-pu", "-t", "$3", "@hub_stamp"}, false},
		{"clustered -pu with a pane target", []string{"set", "-pu", "-t", "%3", "@hub_stamp"}, true},
		{"-w still wants a window", []string{"set", "-w", "-t", "$3", "@hub_x", "v"}, false},
		{"-w with a window target", []string{"set", "-w", "-t", "@3", "@hub_x", "v"}, true},
		// And -t's own value is never read as a cluster, whatever it looks like.
		{"scope-less set is session-scoped", []string{"set", "-t", "$3", "@hub_alias", "v"}, true},
		{"scope-less set refuses a pane target", []string{"set", "-t", "%3", "@hub_alias", "v"}, false},
	} {
		err := Validate(c.argv)
		if c.ok && err != nil {
			t.Errorf("%s: refused a legal argv: %v\n  %q", c.name, err, c.argv)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: accepted an argv it must refuse:\n  %q", c.name, c.argv)
		}
	}
}
