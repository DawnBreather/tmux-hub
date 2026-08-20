package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/registry"
)

// AttachCmd builds the command that hands the terminal to a real tmux attach.
//
// Verified end to end by a spike (docs/design.md §8): tea.ExecProcess suspends
// the TUI, the target session fills the terminal, and detaching returns cleanly.
//
// Two details are load-bearing and both are measured:
//
//   - TMUX must be removed from the child's environment. When the hub runs
//     inside tmux, plain `tmux attach` is refused with $TMUX set, so a hub
//     started from a tmux pane could never attach to anything.
//   - the target is the pane's SESSION, not its pane id. Attaching is how a
//     person takes over a whole session; tmux has no notion of attaching to one
//     pane, and naming the pane would silently attach to its session anyway.
func AttachCmd(p registry.Pane, h hub.Host) (*exec.Cmd, error) {
	// An agent row is not attachable, and refusing it here is a fix rather than a
	// guard. `SessionID` carries two different things depending on where the row
	// came from: a tmux `$N` for a pane, and Claude's own session UUID for a row
	// that came from `claude agents --json`. Nothing checked which, so pressing `a`
	// on an agent row ran `tmux attach -t <uuid>` and failed with a tmux error that
	// says nothing about the actual problem.
	//
	// Most agent sessions have no pane at all — that is why §17's producer exists —
	// so "no pane to attach to" is the normal answer and it should say so.
	if p.Kind == registry.KindAgent {
		return nil, fmt.Errorf("%q is a Claude session, not a tmux pane — "+
			"there is nothing to attach to until it runs in one", p.Session)
	}
	// Target the session ID, not its name: a name does not survive a rename
	// (measured, `has-session -t <old>` fails rc=1 immediately after one) so a
	// session renamed between the poll and the keypress would fail to attach.
	target := p.SessionID
	if target == "" {
		target = p.Session
	}
	if target == "" {
		return nil, fmt.Errorf("pane %s has no session yet", p.PaneID)
	}
	if h.Remote() {
		if h.ControlPath == "" {
			return nil, fmt.Errorf("host %q has no ssh control path, so it cannot be attached", p.Host)
		}
		// -t forces a tty on the remote side. The forwarded socket is useless
		// here: measured, attaching over it fails "open terminal failed: not a
		// terminal" even from a real pty, because the terminal fd travels over
		// SCM_RIGHTS and a forward drops ancillary data.
		//
		// The TARGET is shell-quoted and nothing else is, because the remote path
		// has TWO shells and only the near one was ever handled. ssh JOINS its
		// command arguments into one string and hands that to the REMOTE user's
		// shell, so a `$N` session id is expanded before tmux sees it — measured
		// over a live master, `ssh -S <ctl> nuc 'echo -t $0'` printed `-t bash`,
		// and this argv unquoted failed `can't find session: bash` (rc=1, 0
		// clients) while the quoted form attached, 1 client, the remote status line
		// on screen. Remote attach had never worked, for any session.
		//
		// ssh's own arguments (-S, its control path, -t, the destination) stay bare:
		// ssh consumes them itself and never shows them to a shell, so quoting them
		// would only send the quotes to the far side as text.
		//
		// This is a DIFFERENT shell from the one shellJoin answers to. §20's window
		// path puts this same argv through `$SHELL -c` on THIS machine, and the two
		// compose: shellJoin quotes each element whole, so the quotes below survive
		// the local shell and arrive at ssh intact (proved through real shells in
		// TestNoShellCanAlterTheWindowPathPayload). The local branch below must NOT
		// be quoted — exec runs it with no shell at all, and a quoted target there
		// makes tmux look for a session literally named `'$0'`.
		c := exec.Command("ssh", "-S", h.ControlPath, "-t", h.SSHDest,
			"tmux", "attach", "-t", shellQuote(target))
		c.Env = withoutTMUX(currentEnv())
		return c, nil
	}
	if h.Socket == "" {
		return nil, fmt.Errorf("no socket known for host %q", p.Host)
	}
	c := exec.Command("tmux", "-S", h.Socket, "attach", "-t", target)
	c.Env = withoutTMUX(currentEnv())
	return c, nil
}

// withoutTMUX strips TMUX so a nested attach is not refused. TMUX_PANE is left
// alone: it is informational and tmux does not gate on it.
func withoutTMUX(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, "TMUX=") {
			continue
		}
		out = append(out, e)
	}
	return out
}

func currentEnv() []string { return os.Environ() }

// hostFor finds the host a pane lives on. A pane carries its host label, and
// only the poller knows that label's socket and ssh coordinates.
func hostFor(hosts []hub.Host, label string) (hub.Host, bool) {
	for _, h := range hosts {
		if h.Label == label {
			return h, true
		}
	}
	return hub.Host{}, false
}
