package tmux

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// CreateSession reads back everything the caller needs to GO to what it just made, in the same
// invocation, and the epoch is the field that makes it necessary rather than tidy.
//
// docs/design.md §22.3: possession compares the target's `#{pid}:#{start_time}` against the hub's
// own, and an empty epoch on a LOCAL host falls through to the full-screen attach — which takes the
// terminal and blocks the hub. The commonest row the door serves is a background agent on this very
// machine, so a create that returned only a pane id would have produced exactly that.
func TestCreateSessionReadsBackTheFiveFieldsInOneInvocation(t *testing.T) {
	r := &fakeRunner{stdout: "$3|@7|%9|4242|1786450000\n"}
	got, err := CreateSession(context.Background(), r, target(), CreateSpec{Name: "cicd-30f3382b", CWD: "/w/iac", Cmd: "sh -c 'claude attach 30f3382b'"})
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "$3" || got.WindowID != "@7" || got.PaneID != "%9" {
		t.Errorf("ids = %+v", got)
	}
	if got.Epoch != "4242:1786450000" {
		t.Errorf("Epoch = %q, want the server's own pid:start_time — possession compares it and an "+
			"empty one takes the terminal", got.Epoch)
	}
	assertArgv(t, r.last, "new-session", "-d", "-s", "cicd-30f3382b", "-c", "/w/iac",
		"-P", "-F", "#{session_id}|#{window_id}|#{pane_id}|#{pid}|#{start_time}",
		"sh -c 'claude attach 30f3382b'")
}

// The format must survive the guard that keeps two segfaulting variables off the wire, because a
// format this package builds is exactly what that guard exists to check.
func TestCreateSessionsFormatPassesValidate(t *testing.T) {
	if err := Validate([]string{"new-session", "-d", "-s", "x", "-P", "-F", createFormat}); err != nil {
		t.Errorf("the create's own format is refused by Validate: %v", err)
	}
}

// `duplicate session: <name>` is the ONE rc=1 that means "it is already there", and telling it
// apart from every other rc=1 is what lets the door find-or-create without keeping state.
//
// rc=1 alone is not evidence: docs/design.md §22.3 lists three other things that produce it — the
// far tmux missing, the ssh master dying between the poll and the keypress, a wrong socket path —
// and each of those must be reported as itself rather than treated as "already open".
func TestOnlyTmuxsOwnDuplicateWordsMeanTheSessionIsAlreadyThere(t *testing.T) {
	for _, c := range []struct {
		name      string
		stderr    string
		duplicate bool
	}{
		{"tmux's own words", "duplicate session: cicd-30f3382b", true},
		{"a missing server", "no server running on /tmp/tmux-1000/default", false},
		{"a dead ssh master", "no live ssh master at /run/user/1000/cm-x", false},
		{"a wrong socket", "error connecting to /tmp/nope (No such file or directory)", false},
		{"a refused directory", "can't find directory /nope", false},
	} {
		r := &fakeRunner{stderr: c.stderr, rc: 1}
		_, err := CreateSession(context.Background(), r, target(), CreateSpec{Name: "cicd-30f3382b", Cmd: "true"})
		if err == nil {
			t.Fatalf("%s: rc=1 returned no error", c.name)
		}
		if got := errors.Is(err, ErrDuplicateSession); got != c.duplicate {
			t.Errorf("%s: ErrDuplicateSession = %v, want %v (err %v)", c.name, got, c.duplicate, err)
		}
		// Either way tmux's own sentence travels, because an outer reader of an error string can
		// only lose the remedy it carries.
		if !strings.Contains(err.Error(), c.stderr) {
			t.Errorf("%s: tmux's words were dropped: %v", c.name, err)
		}
	}
}

// A create that answers rc=0 with nothing to read is a failure, not a success with empty fields:
// tmux answers an unknown format variable with an EMPTY field at rc=0 (§3), so a version that did
// not know one of these five would otherwise hand the caller a pane id of "".
func TestCreateSessionRefusesAnEmptyAnswerAtRCZero(t *testing.T) {
	for _, c := range []struct{ name, stdout string }{
		{"nothing at all", "\n"},
		{"too few fields", "$3|@7|%9\n"},
		{"an empty pane id", "$3|@7||4242|1786450000\n"},
		{"an empty epoch half", "$3|@7|%9||1786450000\n"},
	} {
		if _, err := CreateSession(context.Background(), &fakeRunner{stdout: c.stdout}, target(),
			CreateSpec{Name: "x", Cmd: "true"}); err == nil {
			t.Errorf("%s: accepted %q at rc=0", c.name, c.stdout)
		}
	}
}

// The find half of find-or-create: the same five fields for a session that already exists, so the
// door reaches the pane the operator already has instead of reporting a duplicate they cannot see.
func TestSessionByNameReadsTheSameFiveFields(t *testing.T) {
	r := &fakeRunner{stdout: "$1|@1|%1|4242|1786450000|other\n" +
		"$3|@7|%9|4242|1786450000|cicd-30f3382b\n"}
	got, err := SessionByName(context.Background(), r, target(), "cicd-30f3382b")
	if err != nil {
		t.Fatal(err)
	}
	if got.PaneID != "%9" || got.SessionID != "$3" || got.Epoch != "4242:1786450000" {
		t.Errorf("got %+v — the wrong session's fields, or none", got)
	}
	// No `-t`: the seam refuses a -t that is not an id of the verb's own shape, and a session NAME
	// is neither. The format is the create's own constant with the name APPENDED, because two copies
	// of a format are two things to keep in step — and because a name may hold a `|`, so it has to be
	// the field nothing is parsed after.
	assertArgv(t, r.last, "list-sessions", "-F", createFormat+"|#{session_name}")
}

// A name that is not there, after tmux has just called it taken, is a contradiction rather than an
// empty answer — the door must not enter a pane id it read out of nothing.
func TestSessionByNameRefusesWhenTheNameIsNotInTheList(t *testing.T) {
	r := &fakeRunner{stdout: "$1|@1|%1|4242|1786450000|other\n"}
	if _, err := SessionByName(context.Background(), r, target(), "cicd-30f3382b"); err == nil {
		t.Error("a name absent from the listing was accepted")
	}
}

// A session name may legally hold a `|`, so the name is the LAST field and the split is bounded at
// five. With the name first, `a|b|$3|@7|%9|4242|1786450000` is ambiguous between a session called
// `a|b` and one called `a` — and the ambiguous read yields a plausible record whose session id is a
// fragment of somebody's name.
func TestSessionByNameSurvivesAPipeInTheName(t *testing.T) {
	r := &fakeRunner{stdout: "$3|@7|%9|4242|1786450000|we|ird\n"}
	got, err := SessionByName(context.Background(), r, target(), "we|ird")
	if err != nil {
		t.Fatalf("a name holding a pipe was not found: %v", err)
	}
	if got.PaneID != "%9" || got.SessionID != "$3" {
		t.Errorf("got %+v", got)
	}
	// And the ambiguous neighbour is NOT mistaken for it.
	if _, err := SessionByName(context.Background(),
		&fakeRunner{stdout: "$3|@7|%9|4242|1786450000|we|ird\n"}, target(), "we"); err == nil {
		t.Error("a session called `we|ird` answered to the name `we`")
	}
}

// A name tmux cannot address is a session nobody can reach, so the door's naming rule has to
// produce one that `-t` resolves.
//
// Measured: tmux ACCEPTS `.` and `:` in a session name, and then `has-session -t my.app` answers
// `can't find pane: app` and `-t a:b` answers `can't find window: b` — the session exists and
// nothing can address it. A newline is refused outright.
func TestSessionByNameRefusesAnUnaddressableName(t *testing.T) {
	for _, name := range []string{"has.dot", "has:colon", "has\nnewline", ""} {
		if _, err := SessionByName(context.Background(), &fakeRunner{stdout: "$1|@1|%1|1|1|" + name + "\n"},
			target(), name); err == nil {
			t.Errorf("a session named %q was addressed without complaint", name)
		}
	}
}
