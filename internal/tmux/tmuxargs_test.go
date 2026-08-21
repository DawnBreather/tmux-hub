package tmux

// Target.TmuxArgs is docs/design.md §9's socket override. It was a hostset field for
// a whole branch with no reader anywhere in production — parsed, written, round-trip
// tested, and connected to nothing — so these tests are about the WIRE, not the value:
// what must be true is that the args reach tmux, in the one position where tmux reads
// them, and that carrying them changes nothing else about the seam.
//
// This file deliberately does NOT name a forbidden format, so it stays under
// guard_test.go's tree ban like any ordinary file. The arm that needs the literal
// lives in run_test.go, which is exempt because naming them is its job.

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The position, asserted as a whole argv in BOTH branches. A substring would pass on
// `tmux list-panes -a -L work`, which tmux rejects outright, and on
// `tmux -L work -S /sock …`, which silently uses the socket and not the label — so
// where the args sit is the entire property and only an exact argv states it.
func TestTheSocketOverrideLandsAfterTheSocketAndBeforeTheVerb(t *testing.T) {
	r := NewExec(time.Second).(*execRunner)

	local := Target{Label: "local", Socket: "/tmp/tmux-1000/default", TmuxArgs: []string{"-L", "work"}}
	got, err := r.build(local, []string{"list-panes", "-a", "-F", "#{pane_id}"})
	if err != nil {
		t.Fatalf("build(local with an override): %v", err)
	}
	// After -S, not before, and that is measured rather than chosen: on tmux 3.7b the
	// LAST -S wins (`-S /a -S /b` uses /b), so appending is what lets an -S override
	// override. -S beats -L in either order, which Target.TmuxArgs records.
	assertArgv(t, got, "tmux", "-u", "-S", "/tmp/tmux-1000/default", "-L", "work",
		"list-panes", "-a", "-F", "#{pane_id}")

	remote := Target{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/cm-nuc", TmuxArgs: []string{"-L", "work"}}
	got, err = r.build(remote, []string{"list-panes", "-a", "-F", "#{pane_id}"})
	if err != nil {
		t.Fatalf("build(remote with an override): %v", err)
	}
	// Inside the payload, every ARGUMENT quoted and the program name BARE. The name used to be
	// quoted too, and `'tmux' …` is legal POSIX but a parse error in a shell that is not POSIX —
	// measured against a live macOS host running Nushell, which answered
	// `Error: nu::parser::parse_mismatch` at rc=0, so the hub saw a poll that succeeded with no
	// panes in it and the host never left `connecting`. See ShellJoinCommand.
	assertArgv(t, got, "ssh", "-o", "BatchMode=yes", "-o", "ProxyCommand=false",
		"-S", "/run/cm-nuc", "nuc", `tmux '-u' '-L' 'work' 'list-panes' '-a' '-F' '#{pane_id}'`)
}

// A target with no override is byte-for-byte what it was before it could have one. An
// empty slice must contribute NOTHING — not an empty argument, which tmux would read
// as a command name and refuse.
func TestATargetWithNoOverrideIsUnchanged(t *testing.T) {
	r := NewExec(time.Second).(*execRunner)

	got, err := r.build(Target{Label: "local", Socket: "/tmp/s"}, []string{"list-panes", "-a"})
	if err != nil {
		t.Fatal(err)
	}
	assertArgv(t, got, "tmux", "-u", "-S", "/tmp/s", "list-panes", "-a")

	// Explicitly EMPTY rather than nil, because the two arrive from different places:
	// nil is a host with no tmux_args line, and an empty slice is what
	// `tmux_args = []` parses to.
	got, err = r.build(Target{Label: "local", Socket: "/tmp/s", TmuxArgs: []string{}}, []string{"list-panes", "-a"})
	if err != nil {
		t.Fatal(err)
	}
	assertArgv(t, got, "tmux", "-u", "-S", "/tmp/s", "list-panes", "-a")
}

// The discriminating pair, and the reason the override is validated in its OWN call.
//
// Validate reads the -t shape out of args[0] (shapeFor), so folding the override in
// front of the verb — the obvious simplification, one call instead of two — makes
// shapeFor see `-L` where the verb belongs and every verb collapse to the pane-only
// default. That breaks the seam in BOTH directions at once, which is why both are
// asserted here: a legitimate `new-window -t $0` starts being REFUSED, and a
// `kill-window -t %3` — a window verb handed a pane id — starts being ACCEPTED.
// One direction alone cannot see it.
func TestAnOverrideDoesNotMoveTheVerbTheShapeCheckReads(t *testing.T) {
	r := NewExec(time.Second).(*execRunner)
	tgt := Target{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/cm-nuc", TmuxArgs: []string{"-L", "work"}}

	if _, err := r.build(tgt, []string{"new-window", "-t", "$0", "-n", "nuc"}); err != nil {
		t.Errorf("new-window -t $0 must still be accepted with an override in play: %v", err)
	}
	if _, err := r.build(tgt, []string{"kill-window", "-t", "%3"}); !errors.Is(err, ErrBadTarget) {
		t.Errorf("kill-window -t %%3 must still be refused with an override in play: got %v, "+
			"want ErrBadTarget", err)
	}
}

// The override is user input, so it is validated. It comes out of a hand-edited
// hosts.toml, which is a stranger path into this seam than any hub call site.
//
// The two arms are the two rules, and the % one is over-broad BY CHOICE: a socket path
// containing a literal % is legal on the filesystem, and the % ban exists for
// `display -p`'s strftime rather than for anything an option value does. Refusing it
// keeps ONE validation path over everything that reaches tmux instead of a carve-out
// that can drift, and the refusal names the fix. Loosen it here and the
// forbidden-format scan — the one that segfaults a whole 3.2a server — is what the
// carve-out would have to be trusted not to skip.
func TestTheOverrideIsValidatedBecauseItComesFromAConfigFile(t *testing.T) {
	r := NewExec(time.Second).(*execRunner)
	base := Target{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/cm-nuc"}

	for _, tc := range []struct {
		name string
		args []string
		want error
	}{
		{"a target of the wrong shape", []string{"-t", "mysession"}, ErrBadTarget},
		{"a dangling -t", []string{"-L", "work", "-t"}, ErrBadTarget},
		{"a literal percent", []string{"-S", "/tmp/sock%2"}, ErrPercentInArg},
	} {
		tgt := base
		tgt.TmuxArgs = tc.args
		_, err := r.build(tgt, []string{"list-panes", "-a"})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: build with tmux_args %q = %v, want %v", tc.name, tc.args, err, tc.want)
		}
	}
}

// The refusal above must be readable by whoever wrote the file: it is their config
// that is wrong, and §16's rule is that a status with no remedy is a bug report sent
// to the wrong person. So the message names the host, shows the args, and says which
// line to edit.
func TestARefusedOverrideSaysWhichLineToFix(t *testing.T) {
	r := NewExec(time.Second).(*execRunner)
	tgt := Target{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/cm-nuc", TmuxArgs: []string{"-t", "mysession"}}
	_, err := r.build(tgt, []string{"list-panes", "-a"})
	if err == nil {
		t.Fatal("want a refusal")
	}
	for _, want := range []string{"nuc", "tmux_args", "hosts.toml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q so the reader can act on it, got: %v", want, err)
		}
	}
}
