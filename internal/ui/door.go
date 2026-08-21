package ui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/history"
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/launch"
	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// kindBackground is the only kind Claude gives a background id for, and the id is the only argument
// `attach`, `logs` and `stop` accept.
//
// Measured 2026-08-15 on 2.1.233 and 2.1.224, closed both ways on both hosts: `kind: background` ⟺
// the row carries an `id` — background 57 of 57, interactive 0 of 13. That is a fact about the bytes
// on the wire rather than about the vendor's intent, which is why the gate reads the kind and not the
// id (docs/design.md §22.8).
const kindBackground = "background"

// wakeable reports whether `a` on this row opens the door, and returns the sentence to show when it
// does not.
//
// The gate is Kind == KindAgent AND the row's own kind word, never a non-empty id: `agents.Parse`
// back-fills ID from SessionID[:8], so an INTERACTIVE row carries a plausible short id with no
// daemon behind it — a gate written `ID != ""` reads correctly and fails at runtime.
//
// The refusal carries the fix rather than the breakage, and it QUOTES the kind off the row instead of
// describing it: a row whose kind is empty or a word this fleet has not shown must not be told it is
// "interactive", which is a claim nobody measured. It also says *Claude gives no id*, not *the hub
// has no id*, because the hub does hold the back-filled one.
func wakeable(p registry.Pane) (bool, string) {
	if p.Kind != registry.KindAgent {
		return false, ""
	}
	if p.Command == kindBackground {
		// A background row with NO SHORT ID cannot be woken, and saying so is better than either
		// thing the hub would otherwise do. Measured on a row whose AgentID is empty — which
		// `agents.Session.ID` is documented to be on some versions of the vendor's listing:
		//
		//	wakePayload("")  →  claude attach ''      the verb with an empty argument, which fails
		//	wakeName(row)    →  myproject             for EVERY such row of that name, so two of them
		//	                                          share one door and the second `a` enters the first
		//
		// The name is a pure function of the row and the id is the only field guaranteed unique among
		// background rows (registry's own comment says so), so without it there is nothing to make the
		// name unique with — the door's find-or-create would then hand the operator somebody else's
		// session. The refusal names the remedy in the part that fits at 80 columns.
		if p.AgentID == "" {
			return false, fmt.Sprintf("%s reports no short id — run `claude agents` and wake it there",
				wakeSubjectName(p))
		}
		return true, ""
	}
	// SHORT, and the remedy is inside the part that fits. §22.8 specifies a 190-character sentence;
	// measured at 80 columns — the size §16 calls the one to hold — the footer gave it one line and
	// cut it after "so there is nothing for the hub to", losing `/background` entirely. That is this
	// repo's oldest class: keeping the label and losing the action. So the kind and the remedy lead,
	// and the explanation the long form carried lives in this comment instead: `agents.Parse` no
	// longer back-fills an id, and a row of any other kind genuinely has none — it is Claude that
	// gives no background id, not the hub that mislaid one.
	return false, fmt.Sprintf("%s is kind %q and has no background id — type /background in it",
		wakeSubjectName(p), p.Command)
}

// wakeSubjectName is what to CALL the row in a sentence: the operator's name for the session, or
// its id when it has none. One function because three refusals name it and a fourth will.
func wakeSubjectName(p registry.Pane) string {
	if p.Session != "" {
		return p.Session
	}
	if p.AgentID != "" {
		return p.AgentID
	}
	return "this row"
}

// shortSubject is a session's name as a REFUSAL may quote it: bounded, and marked when it was cut.
//
// It exists because the same defect has now been found three times, in three sentences that each
// interpolated `p.Session` unbounded. On this operator's fleet a session is named after the prompt
// that started it — measured, 88 columns — and the footer gives a note one line, so `lines.Fit`
// dropped the tail and the tail is where the remedy is. Live examples, both from the same run:
//
//	nothing killed — 20260803--store-online-takes-too-long-to-ci-cd-troubleshooting ⑂ давай  +2
//	nothing hidden — 20260803--store-online-takes-too-long-to-ci-cd-troubleshooting ⑂ давай  +1
//
// Twenty columns, because the row this is about is on the screen and marked — the operator can see
// WHICH session it is — while the command or the reason is available nowhere else. A template can be
// sized once; a variable in it has to be sized every time, which is why this is a function and not a
// habit.
func shortSubject(name string) string {
	const room = 20
	if lines.Width(name) <= room {
		return name
	}
	return lines.Truncate(name, room) + "…"
}

// wakeName is the session the door creates: `<name>-<short id>`.
//
// The name answers "where am I" — `[refactor-parser-3f2a1b09]` does and `[3f2a1b09]` does not — and
// the short id is the only field guaranteed unique among background rows, since two rows on this
// fleet share a name AND a cwd. It is a pure function of the row, which is what makes a second `a`
// find the first door instead of making a second one.
//
// The sanitising is launch's, one rule with two callers: tmux STORES `.` and `:` in a session name
// and then cannot address it, so a name carrying either is a session nobody can reach.
func wakeName(p registry.Pane) string {
	base := p.Session
	if base == "" {
		base = "claude"
	}
	return launch.SessionNameWithID(base, p.AgentID)
}

// doorWindowName is what the door's window is called: the name the DASHBOARD shows for that row.
//
// The session already carries `<name>-<short id>`, and that name cannot change — it is the dedup key,
// and renaming it would make a second `a` create a second session. So the WINDOW is where an alias
// becomes visible outside the hub, and it is the line `C-b w` shows under the session.
//
// Through the same sanitiser the session name uses, for the same two reasons: tmux stores `.` and `:`
// and then cannot address the window, and the hub's seam refuses a literal `%` outside a `-t` value,
// so a percentage in a prompt-derived name would make the create fail rather than open.
func doorWindowName(p registry.Pane, aliases project.Aliases) string {
	name, _ := aliases.DisplayName(p)
	if name == "" {
		name = p.Session
	}
	return windowNameFrom(name)
}

// wakePayload is the ONE argument `new-session` hands to `$SHELL -c` for the door.
//
// It is `LoginPayload` and not §20's `WindowPayload`, which is what it used to be, for two reasons
// measured on dev-air (login shell nushell, tmux 3.7b) — and the door runs on the ROW's host, so a
// remote one is the normal case rather than the exotic one:
//
//   - `WindowPayload`'s script holds a `'` (its `printf` idiom), and the transport's POSIX quoting
//     turns that into close-quote, backslash-quote, reopen-quote, which nushell reads as the END of the string and then parses the rest
//     as nu code: `Error: nu::parser::parse_mismatch` at **rc=0**, so the hub saw a create succeed
//     with no session behind it.
//   - the pane inherits the ssh client's NON-LOGIN PATH, which on that host does not contain
//     `claude` at all.
//
// Both wrappers answer the same third requirement, which is why this is a swap and not a loss: one
// measured `claude` failure exits 1 with a single stderr line, so a shell has to outlive the payload
// or the message evaporates with the pane.
//
// It carries NO `--debug-file`, and that closes one of §22.3's UNVERIFIED items with a measurement
// rather than a guess: `claude attach --debug-file /tmp/x deadbeef` answers rc=1 with
// `unknown option '--debug-file'` and `Usage: claude attach <id>` (2026-08-17, 2.1.233), so the flag
// the design planned to put here would have made every wake fail before it reached the daemon. The
// verb's own failure line is what the wrapper keeps on screen instead.
//
// The argument is the SHORT id: measured, `claude logs <full uuid>` answers `No job matching`, and
// none of the three verbs resolves a uuid.
func wakePayload(agentID string) (string, error) {
	return LoginPayload([]string{"claude", "attach", agentID})
}

// wokenMsg reports what the door did. `created` is empty when `err` is set, except that a name
// already taken is not an error at all — the door found what it would have made.
type wokenMsg struct {
	made  tmux.Created
	name  string
	host  string
	found bool // the name was taken, so this is the session a previous `a` made
	err   error
}

// wake is `a` on a pane-less background row: it makes the pane the row is missing.
//
// One tmux call does the work, because `new-session -P -F` returns everything needed to go to what it
// just made (docs/design.md §22.3). The epoch in that read-back is mandatory rather than tidy: an
// empty epoch on a LOCAL host takes possession's full-screen path, which hands the terminal over and
// blocks the hub — and the commonest row here is a background agent on this very machine.
//
// Find-or-create keeps NO state anywhere, which is the only version that leaves §20's "nothing is
// kept" literally true. Its cost is that a pre-existing session of that name is entered as if it were
// the door; the name is a pure function of the row, so in practice that session IS the door a
// previous `a` made.
func (m model) wake(p registry.Pane, h hub.Host) (tea.Model, tea.Cmd) {
	run, ctx, keeper, stamper, hist := m.run, m.ctx, m.keeper, m.stamper, m.hist
	name := wakeName(p)
	// The guard can only refuse an id that is not a plain word, and `wakeable` has already refused an
	// EMPTY one — so this is the case where the listing gave a short id the hub cannot put in a command
	// line. Checked here rather than inside the tea.Cmd because it needs no host and no runner.
	//
	// The builder's own sentence is not the one shown: it explains quoting, and its remedy is at the
	// tail where the footer's one line cuts it. This one leads with the remedy, bounded like every
	// other refusal that quotes a name off the row.
	payload, err := wakePayload(p.AgentID)
	if err != nil {
		m.note = fmt.Sprintf("%s reports a short id the hub cannot put in a command line (%q) — "+
			"run `claude agents` and wake it there", shortSubject(wakeSubjectName(p)), p.AgentID)
		return m, nil
	}
	// What `C-b w` will call the window under that session. Without it tmux renames the window to the
	// wrapper shell within a second, so the operator's own session tree read `sh` under every door
	// session while the dashboard showed them the row's name (§21.18).
	winName := doorWindowName(p, m.aliases)
	host, agentID, uuid, cwd := p.Host, p.AgentID, p.SessionID, p.Path
	m.note = ""

	return m, func() tea.Msg {
		// The same guard sweep() and launch() carry: a model assembled field by field has no
		// runner, and a nil interface inside a tea.Cmd is a panic bubbletea surfaces as a dead TUI
		// rather than as a message.
		if run == nil {
			return wokenMsg{name: name, host: host,
				err: fmt.Errorf("this hub has no tmux runner, so no pane can be made")}
		}
		target := h.Target()
		// A host with neither a socket nor an ssh destination has no transport at all. tmux.build
		// would refuse it too, but its message is about sockets and this one names the host.
		if target.Socket == "" && !target.Remote() {
			return wokenMsg{name: name, host: host,
				err: fmt.Errorf("%s has neither a socket nor an ssh destination, so there is "+
					"nowhere to make a pane", host)}
		}

		made, err := tmux.CreateSession(ctx, run, target,
			tmux.CreateSpec{Name: name, CWD: cwd, WindowName: winName, Cmd: payload})
		found := false
		if errIsDuplicateSession(err) {
			// tmux's OWN words, and only those, mean find-or-create. rc=1 alone is not evidence:
			// the same call answers rc=1 when the far tmux is missing, when the ssh master died
			// between the poll and the keypress, and when the socket path is wrong — each of which
			// is reported as itself.
			made, err = tmux.SessionByName(ctx, run, target, name)
			found = true
		}
		if err != nil {
			return wokenMsg{name: name, host: host, err: err}
		}

		// Identity at birth. The pane the hub just made is running a session whose uuid the row
		// already carried, so nothing has to walk a process tree to find out — and the join then
		// folds the row into the pane on the next agents poll (§22.3).
		keeper.Adopt(host, made.PaneID, uuid)
		if stamper != nil {
			// A stamp is what makes the pane a send target. Failing to get one is not a failed
			// wake: the pane exists and the operator is about to be standing in it, so the note
			// says what is missing rather than pretending the door did not open.
			if _, serr := stamper.Stamp(ctx, target, made.PaneID); serr != nil {
				return wokenMsg{made: made, name: name, host: host, found: found,
					err: fmt.Errorf("made %s on %s, but it holds no identity token yet, so the "+
						"hub will refuse a send to it: %w", name, host, serr)}
			}
		}
		if hist != nil {
			// §16 calls history.jsonl the only record that a prompt reached a machine, and a wake
			// is the one §22 action that can abandon work, so it gets its own outcome word. The
			// Entry has no column for a session id, so the id lands inside Text.
			_ = hist.Append(history.Entry{
				At: time.Now(), Host: host, PaneID: made.PaneID, SessionName: name,
				Text:    "claude attach " + agentID,
				Outcome: "woken",
			})
		}
		return wokenMsg{made: made, name: name, host: host, found: found}
	}
}

// errIsDuplicateSession keeps the sentinel test in one place, so a second caller cannot compare
// strings instead.
func errIsDuplicateSession(err error) bool {
	return err != nil && tmux.IsDuplicateSession(err)
}
