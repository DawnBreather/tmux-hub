package tmux

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// createFormat is what a create reads back about what it made, and it is ONE constant because the
// find half of find-or-create asks for the same five fields: two copies of a format are two things
// to keep in step, and the one that drifts is the one nobody tested.
//
// `#{pid}` and `#{start_time}` are the server's own identity, the same pair ServerEpoch asks for.
// Neither is one of the two client variables §3 forbids for segfaulting a 3.2a server, and Validate
// is asserted against this exact string by its own test — the names are deliberately not written
// here, because the tree-wide guard for them is a plain text scan and a comment quoting them is
// indistinguishable to it from a format that emits them.
const createFormat = "#{session_id}|#{window_id}|#{pane_id}|#{pid}|#{start_time}"

// Created is what a create says about what it made, read back in the SAME invocation.
//
// The epoch is why all five fields are read rather than just the pane id. Possession compares the
// target's `#{pid}:#{start_time}` against the hub's own server's, and an empty epoch on a LOCAL host
// falls through to the full-screen attach, which takes the terminal and blocks the hub — and the
// commonest row the door serves is a background agent on this very machine (docs/design.md §22.3).
type Created struct {
	SessionID string // `$N`
	WindowID  string // `@N`
	PaneID    string // `%N`
	Epoch     string // `#{pid}:#{start_time}`, the server's identity
}

// ErrDuplicateSession is tmux saying the name is taken, and it is a sentinel because it is the ONE
// rc=1 a caller may treat as "it is already there".
//
// rc=1 alone is not evidence of a duplicate: the same call answers rc=1 when the far tmux is
// missing, when an ssh master died between the poll and the keypress, and when the socket path is
// wrong. Each of those has to be reported as itself, so the discriminator is tmux's own words
// (docs/design.md §22.3).
var ErrDuplicateSession = errors.New("that session name is already taken")

// IsDuplicateSession is the ONE way to ask, so no caller reaches for strings.Contains on an error
// message that has already been wrapped twice.
func IsDuplicateSession(err error) bool { return errors.Is(err, ErrDuplicateSession) }

// CreateSpec is what to make. It is a struct rather than four strings in a row because three of them
// are a name, a path and a name again — the shape where two adjacent arguments get swapped and every
// test still passes.
type CreateSpec struct {
	// Name is the SESSION name, and for the door it is also its dedup key: `new-session` answering
	// `duplicate session:` is what makes a second `a` find rather than create (docs/design.md §22.3).
	Name string
	// CWD is its own argv element deliberately, as in NewWindow: measured, `-c "/a/dir with space"`
	// works while folding the path into the command string would need quoting.
	CWD string
	// WindowName is what the session's first window is called, and empty means "let tmux decide" —
	// which it does by renaming the window to whatever is running, so every door session read `sh`
	// in the operator's own session tree while the dashboard showed them a name (§21.18).
	//
	// Measured on BOTH versions in the fleet (3.7b and 3.2a): `new-session -n <name>` turns
	// `automatic-rename` off by itself, so the name survives without a second command — which
	// matters, because a command sent after a create loses a race against its own payload.
	WindowName string
	Cmd        string
}

// CreateSession makes a detached session and returns everything needed to go there.
func CreateSession(ctx context.Context, r Runner, t Target, spec CreateSpec) (Created, error) {
	name, cwd, cmd := spec.Name, spec.CWD, spec.Cmd
	if err := checkSessionName(name); err != nil {
		return Created{}, err
	}
	args := []string{"new-session", "-d", "-s", name}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	if spec.WindowName != "" {
		args = append(args, "-n", spec.WindowName)
	}
	args = append(args, "-P", "-F", createFormat)
	if cmd != "" {
		args = append(args, cmd)
	}
	res, err := r.Run(ctx, t, args...)
	if err != nil {
		return Created{}, err
	}
	if res.RC != 0 {
		// RC is checked separately from err because execRunner.Run returns a nil error for a
		// non-zero tmux exit, and tmux's own sentence is the only useful thing here.
		msg := firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC))
		if strings.Contains(msg, "duplicate session:") {
			return Created{}, fmt.Errorf("%w: %s", ErrDuplicateSession, msg)
		}
		return Created{}, fmt.Errorf("new-session: %s", msg)
	}
	return parseCreated(res.Stdout)
}

// SessionByName is the FIND half of find-or-create: the same five fields for a session that already
// exists, so a name the hub itself chose leads to the pane the operator already has rather than to a
// refusal about a session they cannot see.
//
// It lists and MATCHES rather than addressing the session with `-t <name>`, and that is this seam's
// own rule doing its job: Validate requires a `-t` value to be an id of the shape its verb needs, and
// for `display` that is a pane id — so `display -p -t door-30f3382b` is refused before it runs, which
// is right (a `-t` taking a name is how `-t $10` becomes a bare `0`). `list-sessions` takes no target
// at all, so nothing has to be relaxed.
//
// Measured on 3.7b AND on 3.2a, the oldest version in the fleet: a list-sessions format resolves
// `#{window_id}` and `#{pane_id}` in the session's own context, giving its current window and pane —
// `$0|@0|%0|760052|1786979342|door-30f3382b` — which for a session the door made is the only window
// and the only pane there is.
func SessionByName(ctx context.Context, r Runner, t Target, name string) (Created, error) {
	if err := checkSessionName(name); err != nil {
		return Created{}, err
	}
	res, err := r.Run(ctx, t, "list-sessions", "-F", createFormat+"|#{session_name}")
	if err != nil {
		return Created{}, err
	}
	if res.RC != 0 {
		return Created{}, fmt.Errorf("list-sessions: %s",
			firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC)))
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// The NAME goes LAST and the split is bounded, because a session name may legally hold a
		// `|`. With the name first, `a|b|$3|@7|%9|4242|1786450000` is ambiguous between a session
		// called `a|b` and one called `a` — and the ambiguous read produces a plausible five-field
		// record whose session id is a fragment of somebody's name. Bounded from the left, the
		// first five fields are the record and everything after them is the name, whatever it holds.
		f := strings.SplitN(line, "|", 6)
		if len(f) == 6 && f[5] == name {
			return parseCreated(strings.Join(f[:5], "|"))
		}
	}
	return Created{}, fmt.Errorf("no session named %q on %s, though tmux just called the name taken",
		name, t.Label)
}

// checkSessionName refuses a name tmux would accept and then be unable to address.
//
// Measured on 3.7b and 3.2a alike: tmux stores `.` and `:` in a session name, and then
// `has-session -t my.app` answers `can't find pane: app` while `-t a:b` answers `can't find window:
// b` — the session exists and nothing can reach it. A newline is refused by tmux outright. So the
// producer of the name owns this rule (launch.SessionNameFrom applies it), and this is the seam's
// own check that it was applied.
func checkSessionName(name string) error {
	if name == "" {
		return errors.New("a session needs a name")
	}
	if i := strings.IndexAny(name, ".:\n\r"); i >= 0 {
		return fmt.Errorf("session name %q holds %q, which tmux's own -t syntax reads as a window "+
			"or pane, so nothing could address the session", name, name[i:i+1])
	}
	return nil
}

// parseCreated reads the five fields, and refuses an answer that is short or blank.
//
// tmux answers an UNKNOWN format variable with an EMPTY field at rc=0 (docs/design.md §3), so a
// version that did not know one of these would hand back a pane id of "" with no error at all —
// which is how a create that made nothing reads as a create that made something unnameable.
func parseCreated(stdout string) (Created, error) {
	line := ""
	for _, l := range strings.Split(stdout, "\n") {
		if s := strings.TrimSpace(l); s != "" {
			line = s
			break
		}
	}
	f := strings.Split(line, "|")
	if len(f) != 5 {
		return Created{}, fmt.Errorf("the create answered %q, which is %d fields and not the five "+
			"asked for", line, len(f))
	}
	for i, name := range []string{"session id", "window id", "pane id", "server pid", "server start time"} {
		if f[i] == "" {
			return Created{}, fmt.Errorf("the create answered with no %s: %q", name, line)
		}
	}
	return Created{SessionID: f[0], WindowID: f[1], PaneID: f[2], Epoch: f[3] + ":" + f[4]}, nil
}
