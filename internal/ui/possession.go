package ui

import (
	"github.com/DawnBreather/tmux-hub/internal/lines"
	tea "github.com/charmbracelet/bubbletea"
	"strings"

	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/launch"
	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// possessionPath is what `a` will do for the row under the cursor.
type possessionPath int

const (
	// pathJump is the target on the hub's own tmux server: switch-client plus
	// select-window, the hub keeps running, the way back is C-b L.
	pathJump possessionPath = iota
	// pathWindow is the target on another server: today's ssh attach, unchanged,
	// in a new window of the hub's own session.
	pathWindow
	// pathFullScreen is today's behaviour, and it is what happens when the hub is
	// not inside tmux — there is no client to switch and no session to hold a
	// window, so possession is impossible and taking the terminal is honest.
	pathFullScreen
	// pathRefuse is one of the two existing refusals, each of which already names
	// the thing that is missing.
	pathRefuse
)

// decidePossession chooses the path for one row, and returns the note to show
// when the answer is a refusal.
//
// Nothing here is inferred from a string the operator typed. Locality is an
// equality of what each server REPORTS about itself — the pane's Epoch, which is
// `#{pid}:#{start_time}` from the same server, against the hub's own server's —
// because a symlinked socket path reaches the same server while comparing
// unequal (measured). This is the rule Host.LocalProc already establishes: the
// hub never guesses which machine or server something is on.
func decidePossession(p registry.Pane, h hub.Host, selfSession, selfEpoch string) (possessionPath, string) {
	// The refusals come first: they are about the ROW, so they hold whatever the
	// hub's own situation is.
	if _, err := AttachCmd(p, h); err != nil {
		return pathRefuse, "cannot attach: " + err.Error()
	}
	if selfSession == "" {
		return pathFullScreen, ""
	}
	// An unknown epoch must never match itself: two empty strings compare equal,
	// and a jump aimed at another server's session id is the one outcome that
	// puts the operator somewhere nobody checked.
	//
	// What this equality is worth, stated correctly, because the tempting argument
	// for it is wrong. It is NOT "two servers cannot report the same
	// `#{pid}:#{start_time}` because two processes cannot share a pid": that holds
	// inside one pid namespace, and the hosts here are on DIFFERENT MACHINES over
	// ssh — which is the whole reason Host.IsLocalServer is an explicit flag rather
	// than anything derived. Across two machines pids are independent, so a false
	// match needs the pid AND the start SECOND to coincide on both. Improbable, not
	// impossible.
	//
	// The bound that makes it acceptable is therefore the BLAST RADIUS, not the
	// odds. On a collision the row takes pathJump, and pathJump builds its target
	// from the PANE's host — so switch-client runs on that host's socket, where the
	// hub is not an attached client, and §3 measured the only failure mode there:
	// `no current client`. It comes back as a possessedMsg carrying tmux's own
	// words, the operator reads "cannot go there", and nothing is written anywhere.
	// A collision costs a refusal, never a misdirected write.
	//
	// Adding `#{socket_path}` as a third field was considered and REJECTED. It does
	// not discriminate the case that motivates it: two machines both running a
	// default server report the same `/tmp/tmux-<uid>/default`, so the field is
	// equal in exactly the situation it was meant to separate. Where it would
	// discriminate — two servers on ONE machine — the pid already does, perfectly,
	// because they share a pid namespace. Gating the jump on h.IsLocalServer()
	// instead would be a structural kill and is also wrong: a hub pointed at its
	// OWN server under another label is a supported configuration (see
	// Poller.excludeSelf, which exists because of it), and that host carries no
	// LocalProc flag — the gate would push it to pathWindow, which for the same
	// socket is the one thing tmux refuses.
	if selfEpoch != "" && p.Epoch == selfEpoch {
		return pathJump, ""
	}
	if h.Remote() {
		return pathWindow, ""
	}
	// A local server that is not the hub's own — a second socket on this machine.
	// switch-client cannot cross servers, so it takes the window path too, and
	// AttachCmd has already confirmed the socket is known. But an unknown epoch
	// (selfEpoch == "" or p.Epoch == "") must fall back to pathFullScreen for a
	// local target, because the window path does not strip $TMUX and tmux refuses
	// same-socket nesting.
	if selfEpoch != "" && p.Epoch != "" {
		return pathWindow, ""
	}
	return pathFullScreen, ""
}

// possessedMsg reports that a jump finished. The `from` field IS the memory:
// where the operator was travels in the message rather than being stored in
// the model, and the note it produces persists until the next keystroke.
//
// The two fields are not exclusive, and that is deliberate: a `from` alongside an
// `err` is a jump that HALF landed — the client moved and the window did not
// follow — which the note has to report as such rather than as a refusal.
type possessedMsg struct {
	from string
	err  error
	// reused says the window path found the window a previous `a` opened and went there instead of
	// opening a second one. The operator needs the difference: "nothing happened" and "you are
	// already there" look identical on a screen the hub does not draw.
	reused bool
}

// attachWindowName is what `C-b w` will call the window: the host, then the name the dashboard shows
// for that row.
//
// It was the host label alone, so five attaches to one machine drew five windows called `nuc` and
// the operator's own session tree named nothing they could match against the hub. The host stays in
// front because this path only ever serves a server that is NOT the hub's own, so which machine is
// the first thing that distinguishes one of these windows from another — and it is the shape the
// hub's own narrow band already uses for a row (`local/api %10`).
//
// The name goes through the same sanitiser a session name does: tmux stores `.` and `:` and then
// cannot address the window, and the hub's seam refuses a literal `%` anywhere but a `-t` value, so
// an alias with a percentage in it would make `new-window` fail rather than open. Bounded through
// shortSubject for the reason every interpolated name in this repo is bounded.
func attachWindowName(host string, p registry.Pane, aliases project.Aliases) string {
	name, _ := aliases.DisplayName(p)
	if name == "" {
		name = p.Session
	}
	if name == "" {
		return windowNameFrom(host)
	}
	return windowNameFrom(host + "/" + name)
}

// maxWindowName is how wide a window name may be, in COLUMNS — a CJK name is two per rune, so a rune
// count would let it run to twice the room.
//
// 80 is the width §16 commits to for the hub's own screen, so a name that fits the dashboard fits
// here. It is not the footer's 20 (`shortSubject`): a footer is one line shared with other claimants
// and a window name in `C-b w` competes with nothing. Measured against the real fleet, the longest
// door session on the operator's own server is 59 columns
// (`20260813--gis-offline-maps-universal-reader-emulator-7ef2fe7e`), so a tighter bound would stop the
// window name from matching the SESSION name printed directly above it — which is the whole point of
// aligning them. What it does cut is the prompt-derived names, measured at 88.
const maxWindowName = 80

// windowNameFrom is the one function that turns a name into a name tmux will take for a WINDOW, and
// both window namers go through it.
//
// Three rules, each measured rather than assumed:
//
//   - `.` and `:` and `%` go, which is launch's rule: tmux stores the first two and then cannot
//     address the window, and the hub's own seam refuses a literal `%` outside a `-t` value.
//   - a LEADING `-` goes, and this one is tmux's flag parser rather than the seam's rule:
//     `rename-window -t @1 -wip` answers rc=1 `unknown flag -w` and leaves the name alone, because
//     the name is a POSITIONAL argument there. `--` is not an escape either (`invalid flag --`). A
//     session name does not have this problem — measured, `new-session -s -wip` is rc=0 — so the rule
//     belongs here and not in launch's sanitiser.
//   - the width is bounded, in columns.
func windowNameFrom(raw string) string {
	name := strings.TrimLeft(launch.SessionNameFrom(raw), "-")
	if name == "" {
		return ""
	}
	return lines.Truncate(name, maxWindowName)
}

// possess is what `a` does. The hub renders nothing: for a target on its own
// server it moves the client, and for one on another server it puts today's
// unchanged ssh attach in a window of its own session.
func (m model) possess(p registry.Pane, h hub.Host) (tea.Model, tea.Cmd) {
	path, note := decidePossession(p, h, m.selfSession, m.selfEpoch)
	switch path {
	case pathRefuse:
		m.note = note
		return m, nil

	case pathFullScreen:
		// Not inside tmux: today's behaviour, which takes the terminal. Possession
		// needs a client to switch and a session to hold a window, and there is
		// neither.
		c, err := AttachCmd(p, h)
		if err != nil {
			m.note = "cannot attach: " + err.Error()
			return m, nil
		}
		m.note = ""
		return m, tea.ExecProcess(c, func(err error) tea.Msg { return attachedMsg{err} })

	case pathJump:
		where := p.Session + ":" + p.Window
		m.note = ""
		r, ctx := m.run, m.ctx
		tgt := h.Target()
		sess, win := p.SessionID, p.WindowID
		return m, func() tea.Msg {
			// Order matters: select-window addresses a window in the session the
			// client is displaying, so the switch has to land first.
			if err := tmux.SwitchClient(ctx, r, tgt, sess); err != nil {
				return possessedMsg{err: err}
			}
			if err := tmux.SelectWindow(ctx, r, tgt, win); err != nil {
				// The switch already landed, so the client IS displaying the target
				// session — on some other window. `from` travels WITH the error for
				// that reason: reporting only the failure would deny a move that
				// happened, and the operator's next C-b L would bring them back from
				// a place the hub said they never reached.
				return possessedMsg{from: where, err: err}
			}
			return possessedMsg{from: where}
		}

	default: // pathWindow
		c, err := AttachCmd(p, h)
		if err != nil {
			m.note = "cannot attach: " + err.Error()
			return m, nil
		}
		// The attach argv is reused element for element, each element SHELL-QUOTED.
		// tmux hands a trailing argument to `$SHELL -c`, so joining an argv with
		// spaces is a re-parse, not a reuse: measured on tmux 3.7b, `tmux attach -t
		// $3` reaches the far side as `tmux attach -t` (rc=1, "-t expects an
		// argument"), `-t $0` as the shell's own name, and `-t $10` as a bare `0` —
		// which attaches to whatever session is NAMED 0. That last one is exactly
		// what shapeFor's `^\$[0-9]+$` rule for switch-client exists to prevent, and
		// an unquoted join manufactures it AFTER Validate has approved the argv.
		// Quoting keeps the payload one trailing argument, so Task 1's opaqueArg
		// still exempts it (with ssh's own -t and the remote tmux's -t inside) and
		// the forbidden-format scan still covers it.
		//
		// Only c.Args travels: AttachCmd's `c.Env = withoutTMUX(currentEnv())` is
		// DROPPED on this path, which is load-bearing enough to say out loud rather
		// than leave to be rediscovered. It is harmless for both targets this path
		// serves — ssh does not forward TMUX, and for a different local socket,
		// measured, server_client_check_nested keys on the client's tty against
		// panes on the TARGET server, so a different server does not refuse.
		payload := WindowPayload(c.Args)
		where := p.Session + ":" + p.Window
		m.note = ""
		r, ctx := m.run, m.ctx
		self := tmux.Target{Label: hub.LocalLabel, Socket: hub.SelfSocket()}
		sess := m.selfSession
		// The window's NAME is both what `C-b w` shows and what makes a second `a` idempotent.
		name := attachWindowName(h.Label, p, m.aliases)
		return m, func() tea.Msg {
			// One outcome each way, which is what makes this branch short: a refused
			// `new-window` created nothing, so nothing moved and no `from` may travel
			// with the error — "cannot go there" is the whole truth here. The window
			// path has no half-landed state left to report since the window holds
			// itself open (WindowPayload) instead of being held open by an option set
			// after the fact, which was the one thing that could fail with the
			// operator already inside the new window.
			// GO to the window a previous `a` opened rather than opening another. Asked of the
			// server, so a hub that restarted since still finds it. A lookup that FAILS is not a
			// refusal: the fallback is to open a window, which is what this path always did.
			// GO to the window a previous `a` opened rather than opening another. Asked of the
			// server, so a hub that restarted since still finds it. A lookup that FAILS is not a
			// refusal: the fallback is to open a window, which is what this path always did.
			if id, err := tmux.AttachedWindow(ctx, r, self, sess, name); err == nil && id != "" {
				if err := tmux.SelectWindow(ctx, r, self, id); err != nil {
					return possessedMsg{err: err}
				}
				return possessedMsg{from: where, reused: true}
			}
			if err := tmux.AttachWindow(ctx, r, self, sess, name, payload); err != nil {
				return possessedMsg{err: err}
			}
			return possessedMsg{from: where}
		}
	}
}

// WindowPayload is the ONE argument `new-window` hands to `$SHELL -c` for the
// window path: the attach argv, element for element, inside a shell that OUTLIVES
// a failing payload.
//
// The wrapper is here rather than in internal/tmux for two reasons. The seam knows
// argv shapes and nothing about shells — its own doc comments say so — and the
// quoting that makes a payload survive `$SHELL -c` already lives here beside
// AttachCmd, the one builder of the argv. And the line it prints is an operator-
// facing string; internal/tmux puts words on no screen anywhere.
//
// What it replaces is a race. `new-window` STARTS the payload, so a `set -w
// remain-on-exit on` that follows it can only win on time, and measured on tmux
// 3.7b over a private socket a payload of `false` lost 6 of 12 trials — while the
// remote failures this path exists to show are the fast ones (`ssh: Could not
// resolve hostname …`, `Control socket connect(...): Connection refused`). A shell
// that stays in the foreground after the payload returns cannot lose that race,
// because there is nothing left to lose: the window's process is still running.
//
// The wait is conditional: a SUCCESS closes the window silently, which is what a
// detach did before §20 and is the case that happens every time. A FAILURE prints
// why it failed and waits for enter. The wrapper exists to make failures visible;
// a success has nothing to report, and the project's own principle is that value
// comes before ceremony — a keypress on every successful jump is ceremony charged
// to the commonest case. `tmux attach` exits 0 on a clean detach and ssh returns
// the remote command's status, so zero really does mean "the operator finished",
// while every failure mode measured here (a missing control socket, an unresolvable
// host) exits 255.
//
// Three details, each measured:
//
//   - `sh -c` rather than appending the idiom to the payload. `$SHELL` is whatever
//     the operator set as tmux's default-shell, so `s=$?` and `[ … ]` must not be
//     handed to it — only the QUOTING has to survive it, which is the same
//     requirement the unwrapped payload always had.
//   - the status comes from `$?` captured before the conditional, so the number on
//     screen is the payload's own and not printf's.
//   - `read` is what holds the window: the pane's process is a live shell, so
//     `#{pane_dead}` stays 0 and the pane's visible screen is never cleared. A pane
//     held open by remain-on-exit instead showed only `Pane is dead (status 255, …)`
//     with ssh's message pushed into the scrollback — so the window survived and the
//     reason still did not reach the operator.
//
// Enter closes the window, which is why no option is set on it: with
// remain-on-exit on, the keypress this line asks for would leave a dead pane behind
// instead of the window the operator was told it would close.
func WindowPayload(argv []string) string {
	// Two levels of quoting, both through shellJoin, because there are two shells:
	// tmux's default-shell re-splits the outer `sh -c <script>`, and that `sh`
	// re-splits the script into the attach argv. Hand-rolled escapes at either level
	// are how `-t $10` becomes a bare `0` (see the call site).
	script := shellJoin(argv) +
		`; s=$?; [ "$s" -eq 0 ] || { printf '\n[tmux-hub] the attach exited %s — press enter to close this window\n' "$s"; read _; }`
	return shellJoin([]string{"sh", "-c", script})
}

// shellJoin turns an argv into the ONE argument tmux hands to `$SHELL -c`, in a
// form that shell re-splits into exactly that argv again.
func shellJoin(args []string) string {
	return tmux.ShellJoin(args)
}

// shellQuote makes one string survive one shell verbatim.
//
// Single quotes because they suspend every expansion a shell would otherwise do —
// `$`, whitespace, `*`, `~`, `;`, `&`. An embedded single quote is the only
// character they cannot carry, so it is closed, escaped and reopened. A session
// NAME can legitimately hold one (`rename-session "it's mine"`), which is why this
// is not a `"'" + s + "'"` at either call site.
//
// There are two call sites and they answer to two DIFFERENT shells, which is the
// whole reason this is one function: shellJoin above quotes an argv for the shell
// tmux runs a payload through on THIS machine, and AttachCmd quotes a remote attach
// target for the shell on the FAR side, because ssh joins its command arguments into
// one string for the remote user's shell. On the window path both apply to the same
// element, and they compose — the outer layer quotes the inner one whole.
func shellQuote(s string) string {
	return tmux.ShellQuote(s)
}

// hintFor is the header's one-line reminder of how to come back, for the path `a`
// would take on the row under the cursor.
//
// It is empty outside tmux: the warning exists because both servers share the
// default prefix, and there is no outer session to be thrown out of.
func hintFor(path possessionPath) string {
	if !Nested() {
		return ""
	}
	switch path {
	case pathJump:
		return "a → jump into the pane, C-b L comes back"
	case pathWindow:
		return "a → a window with the attach, C-b C-b d leaves the inner tmux"
	default:
		// pathFullScreen takes the terminal, which is what the original phrasing
		// described, and pathRefuse never leaves the hub.
		return "nested: leave an attached session with C-b C-b d"
	}
}

// pathForCursor is the path `a` would take right now, which is what the header
// hint describes. A row that is gone (an empty list, a cursor past the end)
// yields pathRefuse, whose hint is the ambient nested warning.
func (m model) pathForCursor() possessionPath {
	// cursorRow, so the HINT describes the row `a` will actually act on. This was the one
	// cursor-indexing site left reading visibleRows(), and with a project filter on the
	// two lists differ — so the footer promised one possession path while `a` took
	// another. Found by review, not by a test, which is why one exists now.
	p, ok := m.cursorRow()
	if !ok {
		return pathRefuse
	}
	h, ok := hostFor(m.hosts, p.Host)
	if !ok {
		return pathRefuse
	}
	path, _ := decidePossession(p, h, m.selfSession, m.selfEpoch)
	return path
}
