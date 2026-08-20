package tmux

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// NewWindow creates a window in session and returns the new pane's id.
//
// `-P -F '#{pane_id}'` is the point of the whole call: tmux prints the created
// pane's id, so the caller never has to search for what it just made. That is
// what lets a hub-created agent be stamped and bound to a session id at birth
// (docs/design.md §19) instead of being recognised later by walking pids.
//
// cwd is its own argv element deliberately — measured, `-c "/a/dir with space"`
// works, while folding the path into the command string would need quoting.
func NewWindow(ctx context.Context, r Runner, t Target, sessionID, cwd, cmd string) (string, error) {
	args := []string{"new-window", "-t", sessionID}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	args = append(args, "-P", "-F", "#{pane_id}")
	if cmd != "" {
		args = append(args, cmd)
	}
	res, err := r.Run(ctx, t, args...)
	if err != nil {
		return "", err
	}
	// RC is checked SEPARATELY from err, because execRunner.Run returns a nil
	// error for a non-zero tmux exit (run.go:201). Without this line a refused
	// new-window — a directory that does not exist is the common case — looks
	// like a success whose pane id could not be read, and tmux's own message
	// (`can't find directory /nope`), which is the only useful thing here, is
	// dropped on the floor. Every other reader in this package does the same:
	// assert.go:54, labels.go:54, delta.go:132, capture.go:129, stamp.go:51.
	if res.RC != 0 {
		return "", fmt.Errorf("new-window: %s", firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC)))
	}
	return firstPaneID(res.Stdout)
}

// NewSession creates a detached session and returns the new pane's id.
func NewSession(ctx context.Context, r Runner, t Target, name, cwd, cmd string) (string, error) {
	args := []string{"new-session", "-d", "-s", name}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	args = append(args, "-P", "-F", "#{pane_id}")
	if cmd != "" {
		args = append(args, cmd)
	}
	res, err := r.Run(ctx, t, args...)
	if err != nil {
		return "", err
	}
	if res.RC != 0 {
		msg := firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC))
		// The sentinel, so a caller can tell "the name is taken" from every other rc=1 — a missing
		// far tmux, a dead ssh master, a wrong socket path. CreateSession has carried it since the
		// door needed it; this create did not, so the launch could only pass tmux's sentence back
		// to an operator who never typed the name.
		if strings.Contains(msg, "duplicate session:") {
			return "", fmt.Errorf("%w: %s", ErrDuplicateSession, msg)
		}
		return "", fmt.Errorf("new-session: %s", msg)
	}
	return firstPaneID(res.Stdout)
}

// FirstSessionID is a session that EXISTS on this server, for a caller that has to put a window
// somewhere and has no session of its own in mind.
//
// It exists because `new-window -t $0` was hard-coded, and `$0` is only the first session a server
// ever had: kill it and the id is gone forever, so on any long-lived server the default launch path
// answered `can't find session: $0` and created nothing. Reported from real use, reproduced through
// the interface in one run.
//
// The empty string with a nil error means the server has no sessions at all — a real state, and the
// caller has to say something better about it than tmux can.
func FirstSessionID(ctx context.Context, r Runner, t Target) (string, error) {
	res, err := r.Run(ctx, t, "list-sessions", "-F", "#{session_id}")
	if err != nil {
		return "", err
	}
	if res.RC != 0 {
		// No server or no sessions: tmux says so on stderr and this is not an error to the caller,
		// who has a better sentence for it than tmux's.
		return "", nil
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if id := strings.TrimSpace(line); id != "" {
			return id, nil
		}
	}
	return "", nil
}

// RespawnPane replaces a pane's process without changing its id.
//
// Measured: `respawn-pane -k` keeps pane_id, @hub_* options and cwd; pane_pid changes.
func RespawnPane(ctx context.Context, r Runner, t Target, paneID, cmd string) error {
	args := []string{"respawn-pane", "-k", "-t", paneID}
	if cmd != "" {
		args = append(args, cmd)
	}
	res, err := r.Run(ctx, t, args...)
	if err != nil {
		return err
	}
	if res.RC != 0 {
		return fmt.Errorf("respawn-pane: %s", firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC)))
	}
	return nil
}

// KillWindow destroys a window.
func KillWindow(ctx context.Context, r Runner, t Target, windowID string) error {
	res, err := r.Run(ctx, t, "kill-window", "-t", windowID)
	if err != nil {
		return err
	}
	if res.RC != 0 {
		return fmt.Errorf("kill-window: %s", firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC)))
	}
	return nil
}

// KillPane destroys a pane. If it's the last pane in its window, the window is
// destroyed; if the window is the last in its session, the session is destroyed.
func KillPane(ctx context.Context, r Runner, t Target, paneID string) error {
	res, err := r.Run(ctx, t, "kill-pane", "-t", paneID)
	if err != nil {
		return err
	}
	if res.RC != 0 {
		return fmt.Errorf("kill-pane: %s", firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC)))
	}
	return nil
}

// KillSession destroys a session.
func KillSession(ctx context.Context, r Runner, t Target, sessionID string) error {
	res, err := r.Run(ctx, t, "kill-session", "-t", sessionID)
	if err != nil {
		return err
	}
	if res.RC != 0 {
		return fmt.Errorf("kill-session: %s", firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC)))
	}
	return nil
}

// SetWindowOption sets a window option.
func SetWindowOption(ctx context.Context, r Runner, t Target, windowID, name, value string) error {
	res, err := r.Run(ctx, t, "set", "-w", "-t", windowID, name, value)
	if err != nil {
		return err
	}
	if res.RC != 0 {
		return fmt.Errorf("set -w: %s", firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC)))
	}
	return nil
}

// PaneAlive answers whether a pane still exists.
//
// The instrument is emptiness, not an error: measured, `display -p -t %999
// '#{pane_id}'` returns rc=0 and an EMPTY string, because tmux does not fail on
// an unknown target with no client attached. A liveness check that waits for a
// non-zero exit therefore reports every pane alive forever. Asking for the
// pane's OWN id also means a non-empty answer about some other pane is refused.
func PaneAlive(ctx context.Context, r Runner, t Target, paneID string) (bool, error) {
	res, err := r.Run(ctx, t, "display", "-p", "-t", paneID, "#{pane_id}")
	if err != nil {
		return false, err
	}
	// A non-zero RC here is NOT "the pane is gone" — measured, tmux returns
	// rc=0 with an empty string for an unknown target when no client is
	// attached. So RC != 0 means something else went wrong and must not be
	// reported as a confident "dead".
	if res.RC != 0 {
		return false, fmt.Errorf("display -t %s: %s", paneID, firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC)))
	}
	return strings.TrimSpace(res.Stdout) == paneID, nil
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var paneIDPattern = regexp.MustCompile(`^%[0-9]+$`)

// firstPaneID extracts a pane id from output, refusing anything malformed.
//
// There is no firstWindowID beside it any more. The only reader of a window id was
// AttachWindow's `set -w remain-on-exit on`, and §20's window now holds itself open
// through its payload instead (internal/ui's WindowPayload), so the read-back and
// its "the window exists but cannot be named" failure went with the option. The
// sigils still matter where an id IS read: a helper taking either pattern would
// accept a `%N` where shapeFor demands `@N`, and the seam would then refuse the
// call with a message about shapes rather than about the pane nobody could name.
//
// A created pane whose id the hub cannot read is a failure, not a pane to guess about.
func firstPaneID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("empty pane id")
	}
	// Take the first whitespace-separated token.
	token := strings.Fields(s)[0]
	if !paneIDPattern.MatchString(token) {
		return "", fmt.Errorf("malformed pane id: %q", token)
	}
	return token, nil
}
