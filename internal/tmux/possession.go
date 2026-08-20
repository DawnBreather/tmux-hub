package tmux

import (
	"context"
	"fmt"
	"strings"
)

// SwitchClient moves the attached client to a session on THIS server.
//
// Measured on tmux 3.7b, run from inside a pane of the same server with no -c:
// rc=0, and the client moves — status-left goes [hub] to [work]. The client is
// resolved from the invoking pane's own $TMUX, so the hub does not have to name
// one; naming one would also be wrong, because a session can have several.
//
// By session ID, never by name: a name does not survive a rename (§7). The seam
// enforces the shape, which matters here more than anywhere else — $TMUX's third
// field is a BARE number, so a forgotten $ sigil would target a session named 0.
func SwitchClient(ctx context.Context, r Runner, t Target, sessionID string) error {
	res, err := r.Run(ctx, t, "switch-client", "-t", sessionID)
	if err != nil {
		return err
	}
	if res.RC != 0 {
		return fmt.Errorf("switch-client: %s",
			firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC)))
	}
	return nil
}

// SelectWindow makes windowID current in its session, by window ID.
func SelectWindow(ctx context.Context, r Runner, t Target, windowID string) error {
	res, err := r.Run(ctx, t, "select-window", "-t", windowID)
	if err != nil {
		return err
	}
	if res.RC != 0 {
		return fmt.Errorf("select-window: %s",
			firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC)))
	}
	return nil
}

// AttachWindow opens a window in sessionID running cmd — the container §20 puts
// the existing remote attach in, so the attach stops taking the hub's terminal.
//
// cmd is ONE argument, and tmux hands a single trailing argument to `$SHELL -c`.
// So a caller wrapping an argv must shell-quote every element rather than joining
// with spaces: measured, an unquoted `tmux attach -t $3` reaches the far side as
// `tmux attach -t`, and `-t $10` as a bare `0`, which attaches to whatever session
// is NAMED 0 — past the seam, since Validate has already approved the argv.
// internal/ui's WindowPayload is what builds that one argument. Note that only the
// argv travels: AttachCmd's TMUX-stripped environment does not, which is harmless
// for every target this path serves and is documented at the call site.
//
// It is a sibling of NewWindow rather than a parameter on it, deliberately.
// NewWindow's contract is "create a pane and tell me its id" (that is what makes
// identity-at-birth possible, §19) and it takes a cwd; this one needs a window
// NAME and has no cwd. Adding -n to NewWindow would change a signature used at
// nine call sites for a parameter one caller needs.
//
// It sets NO option, and the absence is the fix for a measured race rather than an
// omission. `new-window` answers rc=0 as soon as the window exists and says nothing
// about what the payload then does, and the failure that matters is a remote host
// whose ssh master has died: the payload prints `Control socket connect(...):
// Connection refused` or `ssh: Could not resolve hostname …`, exits, and with
// tmux's default `remain-on-exit off` the window closes on the spot while the hub
// reports `back from api:review`. This function used to follow the create with
// `set -w remain-on-exit on` to hold the window open — and that ordering is a race
// it can only win on time, because new-window is what STARTS the payload, so there
// is no earlier place to put the option. Measured on tmux 3.7b over a private
// socket, 12 trials each: a payload of `false` survived 6 and was lost 6; a payload
// that spawns a shell first survived 12, i.e. the sign of the race is a property of
// the machine, not a bound. The common remote failures are the fast ones.
//
// So the PAYLOAD is what keeps the window now: internal/ui's WindowPayload wraps it
// in a shell that reports the exit status and waits, so the window's process never
// exits and the window cannot vanish. That also repairs the half the option never
// repaired, which is the reason reaching the operator at all — measured, a pane
// held by remain-on-exit shows only tmux's own `Pane is dead (status 255, …)` on
// its visible screen, with the payload's own message pushed into the scrollback
// where neither the operator nor a `capture-pane -p` looks. Nothing clears a live
// pane, so ssh's words stay where ssh wrote them.
//
// It therefore returns an error and nothing else, and the two outcomes are total:
// rc=0 means the window exists and — no `-d` — is the current window, so the
// operator HAS moved; anything else means nothing was created and nothing moved.
// The window id used to come back through `-P -F '#{window_id}'`, and that
// read-back existed solely to name the window for `set -w`. With the option gone
// its only consumer is gone, and with it the third outcome the caller had to tell
// apart ("open, but its id could not be read").
//
// Measured: closing this window leaves the other server's pane_pid unchanged and
// its session present, with the client count dropping to zero. Closing a
// link-window'ed copy kills the agent instead — the asymmetry the whole design
// rests on, which is why the hub only ever creates windows it owns.
func AttachWindow(ctx context.Context, r Runner, t Target, sessionID, name, cmd string) error {
	res, err := r.Run(ctx, t, "new-window", "-t", sessionID, "-n", name, cmd)
	if err != nil {
		return err
	}
	if res.RC != 0 {
		return fmt.Errorf("new-window: %s",
			firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC)))
	}
	return nil
}

// AttachedWindow is the window of sessionID already showing that target, or "" when there is none.
// It is what makes `a` idempotent: go to the window a previous `a` opened instead of opening another.
//
// Measured on the operator's own server before this existed: five presses had left five windows all
// named `nuc`, all ssh-attached to one remote session, which reported `attached=5` — and because
// `window-size` is `latest` on both ends, each new attach RESIZED the shared session. It stood at
// 153x42 while two of those clients were 130x43 and 135x48, so the older windows were drawing a
// session wider than their own terminal. The duplicates were not only clutter in `C-b w`; the second
// attach degraded the first.
//
// THE KEY IS THE WINDOW'S NAME, which is also what the operator reads in `C-b w`, and that is the
// whole design rather than a shortcut. A marker option would have to be written AFTER `new-window`,
// and this file already carries the measurement that refuses that shape: `new-window` is what starts
// the payload, so a second command loses a race it can only win on time. The name is set by the
// create itself, in the same invocation, so there is no window that exists unmarked. Measured on
// BOTH versions in the fleet (3.7b and 3.2a): `new-window -n <name>` turns `automatic-rename` off by
// itself, so the name survives — an un-named sibling was renamed to `sleep` and `bash` within three
// seconds, which is why every door session reads `sh` in the session tree.
//
// What it costs: an operator who renames the window themselves, or an alias changed after the window
// was opened, makes the next `a` open a second one. The hub republishes the name for windows it owns
// (§21.18), and a window the operator renamed is one they took over — losing track of it is the
// honest outcome, and the name they see says why.
//
// It asks with `-a` and filters HERE rather than passing `-t`, which keeps the seam out of it: the
// shape check has no rule for `list-windows`, so its default would demand a pane id for what is a
// session target. The name is read LAST and with SplitN, so a `|` inside it cannot shift the fields
// it is compared against — the defect the label reader has paid for once.
func AttachedWindow(ctx context.Context, r Runner, t Target, sessionID, name string) (string, error) {
	if sessionID == "" || name == "" {
		return "", nil
	}
	res, err := r.Run(ctx, t, "list-windows", "-a", "-F",
		"#{window_id}|#{session_id}|#{window_name}")
	if err != nil {
		return "", err
	}
	if res.RC != 0 {
		return "", fmt.Errorf("list-windows: %s",
			firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC)))
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		f := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(f) == 3 && f[1] == sessionID && f[2] == name {
			return f[0], nil
		}
	}
	return "", nil
}

// ServerEpoch is what a server says about itself: `#{pid}:#{start_time}`, the
// same string the registry already carries on every pane (delta.go). Comparing it
// is how the hub decides "same server" — an equality of what each end REPORTS,
// never of the paths the operator typed, because a symlink to a socket reaches
// the same server while comparing unequal (measured).
//
// Measured: this answers rc=0 with NO client attached, which the hub needs — it
// is not an attached client of the servers it polls.
func ServerEpoch(ctx context.Context, r Runner, t Target) (string, error) {
	res, err := r.Run(ctx, t, "display", "-p", "-F", "#{pid}:#{start_time}")
	if err != nil {
		return "", err
	}
	if res.RC != 0 {
		return "", fmt.Errorf("display: %s",
			firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC)))
	}
	// An empty answer at rc=0 fails OPEN: two empty epochs compare equal, and the
	// hub would then run switch-client against a pane on somebody else's server.
	epoch := strings.TrimSpace(res.Stdout)
	if epoch == "" {
		return "", fmt.Errorf("display: empty #{pid}:#{start_time} — cannot identify this server")
	}
	return epoch, nil
}

// WindowRename is one window and what it should be called.
type WindowRename struct {
	WindowID string
	Name     string
	// AutoOff asks for `automatic-rename off` beside the rename. A window the hub created with `-n`
	// already has it off (measured on 3.7b and 3.2a), but one made by an older hub — or by tmux
	// itself — does not, and there the rename would be undone by the next command the pane runs.
	AutoOff bool
}

// RenameWindows applies every rename in ONE invocation, the way SetSessionOptions writes options: a
// poll that renames three windows pays one round trip.
//
// It is safe to send after a create, unlike the option this file refuses to set there: these windows
// are established, so nothing races their own payload.
func RenameWindows(ctx context.Context, r Runner, t Target, ws []WindowRename) error {
	if len(ws) == 0 {
		return nil
	}
	var args []string
	for _, w := range ws {
		if w.WindowID == "" || w.Name == "" {
			continue
		}
		if strings.HasPrefix(w.Name, "-") {
			// tmux reads it as a FLAG, because the name is a positional argument here: measured,
			// `rename-window -t @1 -wip` answers rc=1 `unknown flag -w` and `--` is refused as well
			// (`invalid flag --`). Every rename on a host travels in one invocation, so one such
			// name would cost every other window its own — the same batch failure an operator alias
			// with a `%` in it caused for the status line. Skipped HERE as well as at the namer,
			// because this is the layer that builds the argv.
			continue
		}
		if len(args) > 0 {
			args = append(args, ";")
		}
		args = append(args, "rename-window", "-t", w.WindowID, w.Name)
		if w.AutoOff {
			args = append(args, ";", "set", "-w", "-t", w.WindowID, "automatic-rename", "off")
		}
	}
	if len(args) == 0 {
		return nil
	}
	res, err := r.Run(ctx, t, args...)
	if err != nil {
		return err
	}
	if res.RC != 0 {
		return fmt.Errorf("rename-window: %s",
			firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC)))
	}
	return nil
}
