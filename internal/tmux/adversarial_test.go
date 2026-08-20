package tmux

// An adversarial sweep of the exemption `opaqueArg` introduced: it loosens the
// seam for exactly one argument, and these tests are the proof that it loosened
// nothing else. They exist because the whole-branch review never returned a
// verdict, so the loosening's blast radius was measured instead of read.
//
// This file names the forbidden formats deliberately, which is why guard_test.go
// exempts it — the same reason run_test.go is exempt.

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// remoteSeam is a remote target WITH a master, so an argv that gets past the shape
// check is wrapped rather than refused for want of a control path. Without the
// control path every arm below would pass for the wrong reason.
func remoteSeam() Target {
	return Target{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/cm-nuc"}
}

// Every write verb crossed with every non-pane target, plus the command verbs
// crossed with every position a payload could sit in. This is a sweep, not a
// sample: if the exemption opened the write path anywhere, one of these accepts.
//
// Each shape is crossed through BOTH doors, because the write path now has two: the
// predicate `Validate` and the seam's `build`, which wraps a remote command into one
// ssh payload. The predicate being closed says nothing about the seam — that is only
// true while build validates BEFORE it wraps, which is exactly the property a sweep
// over `Validate` alone cannot see (run_test.go holds the order itself).
func TestAdversarialSweepOfTheExemption(t *testing.T) {
	r := NewExec(time.Second).(*execRunner)
	writeVerbs := []string{"send-keys", "paste-buffer", "capture-pane", "display", "if"}
	badTargets := []string{"", "@4", "$1", "mysession", "%", "0", "hub", "%abc", " ", "%1 ; kill-server"}
	accepted := []string{}
	for _, v := range writeVerbs {
		for _, tgt := range badTargets {
			argv := []string{v, "-t", tgt, "-l", "X"}
			if err := Validate(argv); err == nil {
				accepted = append(accepted, v+" -t "+tgt)
			} else if !errors.Is(err, ErrBadTarget) && !errors.Is(err, ErrPercentInArg) {
				t.Errorf("Validate(%q) refused with an unexpected error: %v", argv, err)
			}
			if _, err := r.build(remoteSeam(), argv); err == nil {
				accepted = append(accepted, "REMOTE "+v+" -t "+tgt)
			}
		}
	}
	if len(accepted) != 0 {
		t.Fatalf("a write verb accepted a non-pane target:\n%s", strings.Join(accepted, "\n"))
	}
}

// A command verb must not become a way to smuggle a bad target into the verb's
// OWN -t. Only the trailing shell payload is exempt.
func TestTheExemptionCoversOnlyTheTrailingPayload(t *testing.T) {
	mustRefuse := [][]string{
		{"new-window", "-t", "@4", "-n", "x", "sh -c true"},          // window id where a session belongs
		{"new-window", "-t", "", "-n", "x", "sh -c true"},            // empty target, fails OPEN in tmux
		{"new-window", "-t", "hub", "-n", "x", "sh -c true"},         // a NAME
		{"respawn-pane", "-k", "-t", "@4", "cmd"},                    // pane verb, window target
		{"respawn-pane", "-k", "-t", "", "cmd"},                      // empty
		{"new-session", "-d", "-s", "p", "-t", "@1", "cmd"},          // window id
		{"split-window", "-t", "$1", "cmd"},                          // session id where a pane belongs
		{"run-shell", "-t", "@9", "cmd"},                             // window id
		{"new-window", "-t", "$0", "-n", "CONFIRM-%2", "sh -c true"}, // % in a NON-payload arg

		// A command verb with NO payload, i.e. `-t` as the second-to-last
		// argument. These are the shapes `opaqueArg`'s args[len-2] guard exists
		// for: without it the target value IS the trailing argument and would be
		// exempted, which is a window target reaching a pane-only verb.
		//
		// They are listed separately because the first nine do NOT exercise that
		// guard — every one of them has a real payload after the target, so
		// dropping the guard leaves them all refused. Calibrated: with the guard
		// removed this block goes red and the block above stays green, which is
		// how the hole in this test was found in the first place.
		{"respawn-pane", "-k", "-t", "@4"},
		{"respawn-pane", "-k", "-t", "$1"},
		{"respawn-pane", "-k", "-t", ""},
		{"new-window", "-t", "@4"},
		{"new-window", "-t", "hub"},
		{"split-window", "-t", "$1"},
		{"run-shell", "-t", "@9"},
	}
	r := NewExec(time.Second).(*execRunner)
	for _, argv := range mustRefuse {
		if err := Validate(argv); err == nil {
			t.Errorf("Validate(%q) = nil, want a refusal", argv)
		}
		// And through the remote seam, where the whole argv would become one opaque
		// ssh payload: the refusal must happen before the wrapping, not after.
		if _, err := r.build(remoteSeam(), argv); err == nil {
			t.Errorf("build(remote, %q) = nil, want a refusal", argv)
		}
	}
	mustAccept := [][]string{
		{"new-window", "-t", "$0", "-n", "nuc", "ssh -S /p/cm-%h-%p -t nuc tmux attach -t $3"},
		{"new-window", "-t", "$0", "-c", "/srv", "-P", "-F", "#{pane_id}", "claude --resume x"},
		{"respawn-pane", "-k", "-t", "%3", "claude --resume abc ; echo %done"},
		{"run-shell", "-t", "%1", "printf '%s\\n' hi"},
	}
	for _, argv := range mustAccept {
		if err := Validate(argv); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", argv, err)
		}
	}
}

// A forbidden format must be refused wherever it hides, including inside the
// exempt payload — that scan is the one invariant that may never be scoped.
func TestForbiddenFormatsAreRefusedEverywhereIncludingThePayload(t *testing.T) {
	for _, argv := range [][]string{
		{"new-window", "-t", "$0", "-n", "x", "tmux display -p '#{client_activity}'"},
		{"new-window", "-t", "$0", "-n", "#{client_created}", "sh -c true"},
		{"respawn-pane", "-k", "-t", "%1", "echo #{client_activity}"},
		{"run-shell", "-t", "%1", "x #{client_created} y"},
	} {
		if err := Validate(argv); !errors.Is(err, ErrForbiddenFormat) {
			t.Errorf("Validate(%q) = %v, want ErrForbiddenFormat", argv, err)
		}
	}
}
