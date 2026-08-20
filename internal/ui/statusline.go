package ui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// The operator's alias, published to the tmux session itself, so a session says its own name from
// INSIDE — in the status line, as `alias (original)` (docs/design.md §21.16).
//
// The whole decision is aliasPublications, which is a PURE function of the rows and the aliases. It
// runs in Update, where nothing else is running, and hands a tea.Cmd a finished list: a command body
// runs concurrently with Update, so a command that worked out what to write by reading the model
// would be reading state another goroutine owns — the class CLAUDE.md records as having shipped once.
//
// It writes only DIFFERENCES, and it can, because the poll reads back what each session's server
// currently holds (registry.Pane.SessionAlias and friends). So the steady state costs no tmux
// commands at all, and a restarted hub costs none either — the server already holds the answer,
// which is stronger than a cache that has to be kept in step.

// aliasTarget is one tmux session the publisher has something to say about.
type aliasTarget struct {
	host    string
	session string // the tmux session ID, `$3`
	name    string // the tmux session's own name, for the composed length
	// alias is what the operator called it, empty when they have not.
	alias string
	// The session's options as the server currently holds them.
	haveAlias  string
	haveLeft   string
	haveLength string
}

// aliasTargets folds the rows down to one entry per tmux SESSION.
//
// Per session and not per row: every pane of a session reports the same options, so a publisher that
// keyed on the row would write the same three options once per pane. A row with no tmux session of
// its own — a pane-less conversation — has nowhere to publish and is skipped; its alias lives in
// projects.toml until the door gives it a pane.
func aliasTargets(rows []registry.Pane, aliases project.Aliases) []aliasTarget {
	seen := map[string]bool{}
	var out []aliasTarget
	for _, p := range rows {
		if p.Kind != registry.KindPane || p.SessionID == "" {
			continue
		}
		key := p.Host + " " + p.SessionID
		if seen[key] {
			continue
		}
		seen[key] = true
		t := aliasTarget{host: p.Host, session: p.SessionID, name: p.Session,
			haveAlias: p.SessionAlias, haveLeft: p.StatusLeft, haveLength: p.StatusLeftLength}
		if name, own := aliases.DisplayName(p); own {
			t.alias = name
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].host != out[j].host {
			return out[i].host < out[j].host
		}
		return out[i].session < out[j].session
	})
	return out
}

// aliasPublications is what the hub would write, for one host's worth of rows or the whole fleet.
//
// Three options go together per named session, and the third is not an afterthought: tmux defaults
// `status-left-length` to TEN columns on both versions of this fleet, measured, and a real attached
// client confirmed it by drawing `[billing-c` and stopping. A composition without the room is a
// feature that looks broken.
func aliasPublications(rows []registry.Pane, aliases project.Aliases) []tmux.SessionOption {
	var out []tmux.SessionOption
	for _, t := range aliasTargets(rows, aliases) {
		switch {
		case t.alias != "":
			// A name the seam would refuse is SKIPPED rather than sent, because every session on
			// one host travels in one tmux invocation: measured, `50% done` makes Validate refuse
			// the whole argv, so one name with a `%` in it left every OTHER session on that host
			// without its own. The operator hears about it through aliasNote.
			if t.haveAlias != t.alias && tmux.ValidOptionValue(t.alias) == nil {
				out = append(out, tmux.SessionOption{Session: t.session,
					Option: tmux.AliasOption, Value: t.alias})
			}
			// The drawing is only ours to write while it is tmux's own default or already
			// ours. An operator who has written their own status line has made a decision.
			if !tmux.StatusLeftIsOurs(t.haveLeft) {
				continue
			}
			if t.haveLeft != tmux.AliasStatusLeft {
				out = append(out, tmux.SessionOption{Session: t.session,
					Option: tmux.StatusLeftOption, Value: tmux.AliasStatusLeft})
			}
			room := strconv.Itoa(tmux.AliasStatusLeftLength(t.alias, t.name))
			if t.haveLength != room {
				out = append(out, tmux.SessionOption{Session: t.session,
					Option: tmux.StatusLeftLengthOption, Value: room})
			}
		default:
			// Un-naming puts the session back as it was, the hub's own format included —
			// leaving it behind would be the hub keeping a hold on a session it has nothing
			// left to say about.
			if t.haveAlias != "" {
				out = append(out, tmux.SessionOption{Session: t.session,
					Option: tmux.AliasOption, Unset: true})
			}
			if t.haveLeft == tmux.AliasStatusLeft {
				out = append(out, tmux.SessionOption{Session: t.session,
					Option: tmux.StatusLeftOption, Unset: true},
					tmux.SessionOption{Session: t.session,
						Option: tmux.StatusLeftLengthOption, Unset: true})
			}
		}
	}
	return out
}

// aliasNote is what the operator is told when the hub DECLINES to draw, and it carries the line to
// paste rather than only the complaint: a feature that silently does nothing is indistinguishable
// from one that is broken, and this repo's rule is that a refusal names its remedy.
//
// Empty when there is nothing to say, which is the common case.
func aliasNote(rows []registry.Pane, aliases project.Aliases) string {
	for _, t := range aliasTargets(rows, aliases) {
		if t.alias == "" {
			continue
		}
		// A name the seam refuses is the more urgent sentence: the operator can see it on the
		// dashboard and will not see it in the session, with nothing to explain the difference.
		if err := tmux.ValidOptionValue(t.alias); err != nil {
			return fmt.Sprintf("%q cannot be published to tmux (%v) — it names the row here, "+
				"and the session keeps its own name", shortSubject(t.alias), err)
		}
		if tmux.StatusLeftIsOurs(t.haveLeft) {
			continue
		}
		return fmt.Sprintf("%s is yours, so the name is published as %s only — to draw it, add: "+
			"set -g %s '%s'", tmux.StatusLeftOption, tmux.AliasOption,
			tmux.StatusLeftOption, tmux.AliasStatusLeft)
	}
	return ""
}

// publishedMsg reports what a publish round did, so a failure is a sentence on the footer rather
// than silence.
type publishedMsg struct {
	writes int
	err    error
}

// writeSessionOptions writes one host's options in ONE tmux invocation.
//
// It takes the finished list, and the target, precisely so nothing in this body reads the model.
func writeSessionOptions(run tmux.Runner, target tmux.Target, ws []tmux.SessionOption) tea.Cmd {
	if len(ws) == 0 || run == nil {
		return nil
	}
	return func() tea.Msg {
		// Bounded like every other write this package makes: a host that has gone away must
		// cost one timeout, not a wedged interface.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tmux.SetSessionOptions(ctx, run, target, ws); err != nil {
			return publishedMsg{writes: len(ws), err: err}
		}
		return publishedMsg{writes: len(ws)}
	}
}

// publishAliases is the model's side: one batched write per HOST, because each host is its own tmux
// server.
//
// Everything it needs is read HERE, in Update, and handed over as plain values — the rows, the
// aliases and the target. A command body runs concurrently with Update, so a command that reached
// back into the model for them would be reading state another goroutine owns, which is the class
// CLAUDE.md records as having shipped once.
func (m model) publishAliases() tea.Cmd {
	if m.run == nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, h := range m.hosts {
		var rows []registry.Pane
		for _, p := range m.panes {
			if p.Host == h.Label {
				rows = append(rows, p)
			}
		}
		if c := writeSessionOptions(m.run, h.Target(), aliasPublications(rows, m.aliases)); c != nil {
			cmds = append(cmds, c)
		}
		if c := renameWindows(m.run, h.Target(), windowRenames(rows, m.aliases, m.askedWindowName)); c != nil {
			cmds = append(cmds, c)
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// windowRenames is every window whose name has drifted from the name the DASHBOARD shows, and only
// for windows the hub itself made.
//
// It exists because the operator's own session tree read `sh` under every door session: tmux renames
// a window to whatever is running unless it was created with `-n`, and until §21.18 the door did not
// pass one. This is the half that repairs the sessions an older hub left behind, and the half that
// keeps a name in step when an alias is typed AFTER the door opened — the alias publisher's own poll,
// the same differences-only rule, so a fleet in step costs no tmux commands.
//
// The gate is `registry.AttachedSessionID`, which reads the pane's own start command: a window the
// hub did not make is one the operator owns, and renaming it would be the hub writing over their
// work. There is no window option to consult for the same reason the attach window has none — an
// option set after a create loses a race against the payload.
// asked is the hub's memory of what it has already ASKED for, per window id — see
// model.askedWindowName. A nil map disables the concession, which is what the pure frame tests
// want: they assert the rename this function WOULD make.
func windowRenames(rows []registry.Pane, aliases project.Aliases,
	asked map[string]string) []tmux.WindowRename {
	var out []tmux.WindowRename
	seen := map[string]bool{}
	for _, p := range rows {
		if p.WindowID == "" || seen[p.WindowID] {
			continue
		}
		if registry.AttachedSessionID(p.StartCommand) == "" {
			continue
		}
		want := doorWindowName(p, aliases)
		if want == "" || want == p.Window {
			continue
		}
		// ALREADY ASKED FOR EXACTLY THIS NAME, and the window is not carrying it: another program
		// writes that name too, and asking again is the loop the operator sees shimmer. Conceded.
		if asked != nil && asked[p.WindowID] == want {
			continue
		}
		if err := tmux.ValidOptionValue(want); err != nil {
			// The same refusal the alias publisher makes, for the same reason: every rename on a
			// host travels in ONE invocation, so a name the seam refuses would take the whole batch
			// with it and leave every other window unnamed.
			continue
		}
		seen[p.WindowID] = true
		if asked != nil {
			asked[p.WindowID] = want
		}
		// AutoOff because this window may predate the door passing `-n`, in which case tmux is still
		// renaming it and the rename alone would not survive the next command the pane runs.
		out = append(out, tmux.WindowRename{WindowID: p.WindowID, Name: want, AutoOff: true})
	}
	return out
}

// renameWindows is the tea.Cmd for one host's renames, or nil when there are none — so a fleet whose
// names are already in step issues no command at all.
func renameWindows(r tmux.Runner, t tmux.Target, ws []tmux.WindowRename) tea.Cmd {
	if len(ws) == 0 || r == nil {
		return nil
	}
	return func() tea.Msg {
		// Bounded like every other write this package makes, and reported through the same message:
		// a host that has gone away costs one timeout, not a wedged interface.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tmux.RenameWindows(ctx, r, t, ws); err != nil {
			return publishedMsg{writes: len(ws), err: err}
		}
		return publishedMsg{writes: len(ws)}
	}
}
