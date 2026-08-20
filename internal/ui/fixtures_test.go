package ui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/broadcast"
	"github.com/DawnBreather/tmux-hub/internal/hide"
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// The screen fixtures the two document generators are built from.
//
// This file carries NO build tag, and that is the whole reason it exists. They
// used to live in mockup_test.go behind `//go:build mockup`, which made every
// assertion expressed in terms of them reachable only under that tag — and
// flows_test.go's fourteen frame assertions then also sat behind an env var, so
// they ran in no gate at all (see flows_frames_test.go). Anything a default-suite
// test needs to build a screen belongs here; the HTML writers stay tagged.

// agentPane is a pane that holds a Claude session, with screen content so tiles
// have something to show.
func agentPane(host, sess, win, id string, idx int, st state.State, body ...string) registry.Pane {
	return registry.Pane{
		Kind: registry.KindPane, Host: host, Session: sess, Window: win,
		PaneID: id, Index: idx, Command: "claude", StartCommand: `"claude"`,
		ClassifiedState: st, Content: body, Bracketed: true,
		SessionID: "$0", WindowID: "@" + id[1:], ClaudeSession: "7007b23f-1599-4efa-81c5-4195621cc273",
		Activity: time.Now().Add(-90 * time.Second), Height: 8, Width: 60,
	}
}

func toolPane(host, sess, win, id, cmd string, idx int, st state.State, body ...string) registry.Pane {
	p := agentPane(host, sess, win, id, idx, st, body...)
	p.Command, p.StartCommand, p.ClaudeSession = cmd, `"`+cmd+`"`, ""
	return p
}

func hosts2() []hub.Host {
	return []hub.Host{
		{Label: "local", Socket: "/tmp/tmux-1000/default", Status: hub.Up, Version: "3.7b", LocalProc: true},
		{Label: "nuc", Socket: "/run/user/1000/nuc.sock", Status: hub.Up, Version: "3.4",
			SSHDest: "nuc", ControlPath: "/home/dev/.ssh/cm-nuc"},
	}
}

// inboxRow is the LIST row naming needle, and it is a helper rather than care because
// `Contains(screen, x)` cannot test a property of ONE row: the focused tile draws its header
// from the same session name, so a screen-wide assertion is satisfied by two surfaces and a
// broken row hides behind a correct tile. That has now cost time three times in this repo —
// once on a `»` marker whose row lost it while the tile still printed it, once on a `done`
// row read from the tile header, and once on a footer.
//
// The surface is separated by the box rule, which no inbox row draws: every tile line and the
// footer rule carry `─`, so skipping those lines needs no knowledge of the layout or the width
// band. The FIRST match wins, because the inbox is above the tiles.
func inboxRow(t *testing.T, screen, needle string) string {
	t.Helper()
	for _, ln := range strings.Split(screen, "\n") {
		if strings.ContainsRune(ln, '─') {
			continue
		}
		if strings.Contains(ln, needle) {
			return ln
		}
	}
	t.Fatalf("no inbox row names %q:\n%s", needle, screen)
	return ""
}

// rowNeedle is the string that finds ONE pane's row on a screen, and it is a helper rather than
// care because the answer stopped being "the pane id" and six tests had their own copy of the old
// answer. rowPaneID draws the id only where a session puts more than one row up; a row whose
// session puts one up leads with `host/session` instead, and an agent row's name IS the row.
//
// It counts the sharing itself instead of calling sharedSessions: a test whose expected value comes
// out of the function under test asserts only that assignment works. Three lines of restatement is
// what makes a change to the production rule fail here.
func rowNeedle(p registry.Pane, fleet []registry.Pane) string {
	// The DERIVED name, because that is what the row draws AND what the collision rule keys on:
	// after the door's join a row reads by the Claude session's name and the tmux session the door
	// created appears nowhere on it.
	derived := func(q registry.Pane) string {
		for _, cand := range []string{q.AgentName, q.Session, q.PaneID, q.SessionID} {
			if strings.TrimSpace(cand) != "" {
				return cand
			}
		}
		return "(unnamed)"
	}
	name := derived(p)
	n := 0
	for _, q := range fleet {
		if q.Host == p.Host && derived(q) == name {
			n++
		}
	}
	if n > 1 {
		// Which id depends on the kind, the same way rowIdentity decides it: tmux's `%N` for a pane
		// and the short Claude id for a row with no pane.
		if p.Kind == registry.KindAgent {
			return p.AgentID
		}
		return p.PaneID
	}
	return p.Host + "/" + name
}

// drawsRow reports whether any line of a screen draws needle as a WHOLE FIELD.
//
// The boundary matters and a trailing space is the wrong way to get it: `%2` is a prefix of `%20`, so
// the counters guarded against it with `Contains(screen, id+" ")` — which then depended on something
// following the id on the row. It did, until the id stopped being the last thing drawn, and the
// counter reported ZERO rows on a screen full of them. Fields have the boundary built in.
func drawsRow(screen, needle string) bool {
	for _, ln := range strings.Split(screen, "\n") {
		for _, f := range strings.Fields(ln) {
			if f == needle {
				return true
			}
		}
	}
	return false
}

func base(t *testing.T, w, h int, panes ...registry.Pane) model {
	t.Helper()
	s, err := hide.Open(filepath.Join(t.TempDir(), "hidden.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Production never hands View() an arbitrary slice: Registry.Update sorts by
	// attention before anything renders. Assigning model.panes directly skipped
	// that, so every screen here used to show rows in the order fleet() happens
	// to list them — and attention ordering is the single property the dashboard
	// exists for. A mockup that gets it wrong calibrates the reviewer wrong.
	registry.SortByAttention(panes)
	return model{
		panes: panes, hosts: hosts2(), hidden: s,
		width: w, height: h,
		atSelection: map[SelectionKey]paneSnapshot{},
		lastOutcome: map[SelectionKey]broadcast.Outcome{},
	}
}
