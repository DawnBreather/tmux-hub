package ui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/broadcast"
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// restart respawns the selected pane with its Claude session resumed. It requires
// exactly one selected pane and that the hub knows its session ID (which it does
// for hub-created agents via Adopt).
//
// Restart-in-place is safe by construction: respawn-pane -k kills then spawns in
// one call, so no window exists where two processes share the transcript. No
// PaneAlive check is needed for this path.
//
// Measured: `claude --resume <uuid>` is non-exclusive — a second resume of a live
// session returns rc=0 with no lock and no warning, and two processes append to one
// transcript. Any FUTURE path which resumes a session into a DIFFERENT pane must
// check tmux.PaneAlive on the owning pane first and refuse while it returns true.
func (m model) restart() (tea.Model, tea.Cmd) {
	if m.sel.Len() == 0 {
		m.note = "select a pane with space first — there is nothing to restart"
		return m, nil
	}
	if m.sel.Len() > 1 {
		m.note = "restart requires exactly one pane — select one with space"
		return m, nil
	}

	// Get the single selected pane
	var target SelectionKey
	for _, k := range m.sel.Members() {
		target = k
		break
	}

	// Get the Claude session ID for this pane
	sessionID, ok := m.keeper.Session(target.Host, target.PaneID)
	if !ok || sessionID == "" {
		m.note = "this pane has no known session — only hub-launched agents can be restarted"
		return m, nil
	}

	// Find the pane in the registry to get its session and window
	var pane *registry.Pane
	for i := range m.panes {
		if m.panes[i].Host == target.Host && m.panes[i].PaneID == target.PaneID {
			pane = &m.panes[i]
			break
		}
	}
	if pane == nil {
		m.note = "pane not found in registry"
		return m, nil
	}

	// Perform the restart immediately (no confirmation for in-place restart)
	return m, m.doRestartCmd(target.Host, target.PaneID, pane.SessionID, sessionID)
}

func (m model) doRestartCmd(host, paneID, sessionID, claudeSession string) tea.Cmd {
	r, ctx, k, st := m.run, m.ctx, m.keeper, m.stamper
	return func() tea.Msg {
		// Build the resume command
		cmd := "claude --resume " + claudeSession

		// Respawn the pane with the resume command
		tgt := targetFor(m.hosts, host)
		if err := tmux.RespawnPane(ctx, r, tgt, paneID, cmd); err != nil {
			return restartMsg{err: err}
		}

		// Invalidate identity — measured: respawn-pane -k keeps pane_id and @hub_*
		// while changing pane_pid, so the stamp survives but the process is different
		k.ForgetPane(host, paneID)

		// Re-stamp the pane so it can receive writes immediately after re-identification
		if st != nil {
			_, _ = st.Stamp(ctx, tgt, paneID)
		}

		return restartMsg{paneID: paneID}
	}
}

// doRestart is called after confirmation (currently unused, since restart doesn't
// confirm for in-place respawn).
func (m model) doRestart() tea.Cmd {
	// This would be reached if we added confirmation, but for now restart() goes
	// directly to doRestartCmd
	return nil
}

type restartMsg struct {
	paneID string
	err    error
}

// confirmKill opens the kill confirmation dialog. Kill ALWAYS confirms, naming what
// is running (agent / dead / unidentified) so "Kill this?" with no subject cannot
// destroy the wrong window.
func (m model) confirmKill() (tea.Model, tea.Cmd) {
	if m.sel.Len() == 0 {
		m.note = "select a pane with space first — there is nothing to kill"
		return m, nil
	}

	// A pane-less row has no pane to kill, and §22.9 decision 3 rules that the hub does not end
	// Claude sessions. The guard is here rather than in killSelected so the dialog never offers a
	// target the hub will refuse: before this, `K` confirmed and then issued
	// `kill-pane -t agent:<shortid>@<hash>`, which tmux reads as session `agent`, and the failure
	// was counted into killMsg.failed with no reason shown.
	//
	// A MIXED selection refuses whole. Killing the pane rows and skipping the rest would be a
	// partial action the operator did not ask for.
	//
	// Two sentences, because one is wrong for half the population: an interactive session carries
	// no background id (measured 0 of 13 rows across two hosts), so there is nothing to put in a
	// `claude stop` command.
	for _, k := range m.sel.Members() {
		for _, p := range m.panes {
			if p.Host != k.Host || p.PaneID != k.PaneID || p.Kind != registry.KindAgent {
				continue
			}
			// "nothing killed" leads, because the refusal always means the whole action was
			// declined — including a MIXED selection, where the pane rows are left alone too and
			// an operator who read only the tail would assume they were killed.
			//
			// Sized for 80 columns, the width §16 commits to. The first draft ran 190 runes and
			// `lines.Fit` truncated the fix clause off the footer, leaving only the breakage — the
			// one part a refusal exists to carry. The policy behind it (the hub does not stop
			// Claude sessions, §22.9 decision 3) lives in the design rather than in the footer.
			//
			// The argument is AgentID, the listing's SHORT id, never SessionID: measured,
			// `claude logs <full uuid>` answers `No job matching` while the short id resolves, so
			// interpolating the uuid would print a command that fails.
			// And the NAME is bounded, which is the same defect one layer along. The comment above
			// says this sentence was "sized for 80 columns" and it was — as a template. The
			// variable is not: on the operator's own fleet a session is named after the prompt
			// that started it, and measured live, `nothing killed — 20260803--store-online-takes-
			// too-long-to-ci-cd-troubleshooting ⑂ давай раскатаем Dockerfile goldens  +2` is what
			// the footer drew — the `+2` being lines.Fit reporting that it had dropped the remedy
			// and the host. Keeping the label and losing the action, through the one part that
			// cannot be sized at the source.
			//
			// The row is on the screen and marked, so the operator can see which session this is
			// about; what they cannot get anywhere else is the command. So the name yields — and so
			// does `has no pane`, which the row already says by carrying no pane id and which `K`
			// refusing already implies. Measured: with both clauses the sentence ran 87 columns at a
			// 20-column name, so one of them had to go and the remedy was never a candidate.
			subject := shortSubject(p.Session)
			switch {
			case p.Command == "background" && p.State() == state.Done:
				// A job that has ENDED needs no stopping, and until `done` was a state of its
				// own the hub could not tell — `done` folded onto `idle`, so this branch printed
				// "run claude stop <id>" for something that was not running. The wording echoes
				// the word on the row (`✓ done`) so the sentence and the list agree, and it keeps
				// the sibling's shape: `is done: "claude logs"` against a bare `: run "claude stop"`.
				m.note = fmt.Sprintf("nothing killed — %s on %s is done: \"claude logs %s\"",
					subject, p.Host, p.AgentID)
			case p.Command == "background":
				m.note = fmt.Sprintf("nothing killed — %s on %s: run \"claude stop %s\"",
					subject, p.Host, p.AgentID)
			default:
				m.note = fmt.Sprintf("nothing killed — %s on %s: end it in its own terminal",
					subject, p.Host)
			}
			return m, nil
		}
	}

	// Build kill reasons for each selected pane
	allReasons := []broadcast.Reason{}
	seenReasons := map[broadcast.Reason]bool{}

	for _, k := range m.sel.Members() {
		// Find the pane to check if it's identified or dead
		var identified, dead bool
		for _, p := range m.panes {
			if p.Host == k.Host && p.PaneID == k.PaneID {
				identified = m.keeper.Identified(k.Host, k.PaneID)
				dead = p.Dead
				break
			}
		}

		reasons := broadcast.KillReasons(identified, dead)
		for _, r := range reasons {
			if !seenReasons[r] {
				seenReasons[r] = true
				allReasons = append(allReasons, r)
			}
		}
	}

	m.pending, m.pendingAct = allReasons, actionKill
	return m.raise(modeConfirm), nil
}

// doKill performs the actual kill after confirmation.
func (m model) doKill() tea.Cmd {
	if m.sel.Len() == 0 {
		return nil
	}

	return m.killSelected()
}

func (m model) killSelected() tea.Cmd {
	selected := m.sel.Members()
	r, ctx := m.run, m.ctx

	// Group panes by host
	type hostGroup struct {
		target tmux.Target
		panes  []registry.Pane
	}

	groups := make(map[string]*hostGroup)
	for _, k := range selected {
		if groups[k.Host] == nil {
			groups[k.Host] = &hostGroup{target: targetFor(m.hosts, k.Host)}
		}
		// Find the full pane
		for _, p := range m.panes {
			if p.Host == k.Host && p.PaneID == k.PaneID {
				groups[k.Host].panes = append(groups[k.Host].panes, p)
				break
			}
		}
	}

	return func() tea.Msg {
		var killed, failed int
		for _, g := range groups {
			tgt := g.target
			for _, p := range g.panes {
				// Kill the pane (which kills its window if it's the last pane)
				if err := tmux.KillPane(ctx, r, tgt, p.PaneID); err != nil {
					failed++
					continue
				}
				killed++
			}
		}
		return killMsg{killed: killed, failed: failed}
	}
}

type killMsg struct {
	killed int
	failed int
}

// targetFor is how this package addresses a host it knows only by label. It replaced
// a socketFor that answered with a SOCKET, which built `Target{Label, Socket}` at
// three call sites and therefore could not name a host that has no socket — every
// host out of hosts.toml (§5 deleted the forward). An unknown label still yields a
// bare target, which tmux.build refuses with ErrNoSocket rather than running
// anything.
func targetFor(hosts []hub.Host, label string) tmux.Target {
	if h, ok := hostFor(hosts, label); ok {
		return h.Target()
	}
	return tmux.Target{Label: label}
}
