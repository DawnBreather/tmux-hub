package registry

import (
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/agents"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// A HOST WHOSE TMUX POLL STOPS ANSWERING MUST NOT SPLIT ONE SESSION INTO TWO ROWS.
//
// Reported from a live screen as two things, which are one: `ansible-jr-mib` drawn twice on the same
// host with the same name and the same path, and states "blinking" between `works` and `stale`.
//
// The mechanism, measured. `MarkHostStale` marks a host's PANE rows stale and deliberately leaves its
// AGENT rows alone — an agent row's liveness has nothing to do with the tmux tunnel. The join in
// `UpdateAgents` then skipped a stale pane as a target, so the listing row had nowhere to fold and
// became a row of its own: one row `stale` from the pane producer and one row live from the listing
// producer, for one session. When the host answered again the fold resumed and the two collapsed —
// which is the blinking, seen from the operator's side.
//
// The skip is right for a RETIRED pane (`state.Gone`): folding a live fact into a corpse would let a
// dead pane read `works` while the session that is actually alive never appears. A stale host is not
// that case — the pane is last-known rather than gone, it is still the same session, and the row is
// still on screen, so refusing to fold produces a second row instead of protecting anything.
func TestAStaleHostDoesNotSplitASessionIntoTwoRows(t *testing.T) {
	const uuid = "e04c609c-1111-2222-3333-444455556666"
	now := time.Unix(1787200000, 0)
	zone := []string{"● output", "", "· Working… (2m)", "", "──────── ansible ─", "❯ "}

	seed := func(r *Registry) {
		r.Update("nuc",
			[]tmux.Delta{{PaneID: "%4", Activity: now.Unix(), PaneHeight: 40, WindowWidth: 120,
				PanePID: 480403, CursorY: len(zone) - 1, SessionID: "$2", WindowID: "@2", Alt: true}},
			map[string]tmux.Labels{"%4": {Session: "20260818--ansible-e04c609c",
				Window: "ansible", Command: "sh"}},
			[]tmux.Capture{{PaneID: "%4", Height: 40, Lines: zone}},
			map[string]tmux.Capture{"%4": {PaneID: "%4", Height: 40, Lines: zone}},
			now, time.Second)
		r.SetClaudeSession("nuc", "%4", uuid)
	}
	listing := agents.Session{ID: "e04c609c", SessionID: uuid, Kind: "background", PID: 480403,
		Name: "20260818--ansible", State: "working", StartedAt: now.Add(-time.Hour)}

	r := New()
	seed(r)
	r.UpdateAgents("nuc", []agents.Session{listing}, now)
	if got := len(r.Panes()); got != 1 {
		t.Fatalf("with the host answering, one session gives %d rows — the premise of this test is "+
			"that it folds to 1, so something else is wrong", got)
	}

	// The tmux poll for that host fails. Its panes keep their rows and their last screen; the
	// listing producer is unaffected and reports the session again on its own schedule.
	r.MarkHostStale("nuc", now.Add(10*time.Second))
	r.UpdateAgents("nuc", []agents.Session{listing}, now.Add(20*time.Second))

	rows := r.Panes()
	if len(rows) != 1 {
		for i, p := range rows {
			t.Logf("  row %d: kind=%v host=%s session=%q pane=%s stale=%v state=%v word=%q",
				i, p.Kind, p.Host, p.Session, p.PaneID, p.Stale, p.stateAt(now.Add(20*time.Second)),
				p.AgentWord)
		}
		t.Fatalf("a stale host drew %d rows for ONE session — the operator reads that as a duplicate, "+
			"and as the host flaps the pair appears and merges, which is the reported blinking",
			len(rows))
	}

	// And the surviving row must still be carrying the listing's fact, or the dedup would have
	// bought one row by throwing away what the other one knew.
	p := rows[0]
	if p.AgentWord != "working" || p.AgentPID != 480403 {
		t.Errorf("the surviving row reports word %q pid %d, want \"working\" and 480403 — the fold "+
			"must absorb the claim, not just remove the row", p.AgentWord, p.AgentPID)
	}
	if p.Kind != KindPane {
		t.Errorf("the surviving row is %v, want the PANE row — it is the one with a screen and a "+
			"pane id to act on", p.Kind)
	}
}

// A RETIRED pane is still refused as a join target, which is the case the skip exists for.
//
// Without this the fix above would trade a visible duplicate for a silent lie: a pane tmux has
// destroyed would absorb a live listing row and read `works` for the whole freshness window, while
// the session that is genuinely alive — the row with a door on it — would never be drawn at all.
func TestARetiredPaneStillDoesNotAbsorbALiveListing(t *testing.T) {
	const uuid = "beef0000-1111-2222-3333-444455556666"
	now := time.Unix(1787210000, 0)
	zone := []string{"● output", "", "· Working… (1m)", "", "──────── gone ─", "❯ "}

	r := New()
	r.Update("nuc",
		[]tmux.Delta{{PaneID: "%9", Activity: now.Unix(), PaneHeight: 40, WindowWidth: 120,
			PanePID: 7000, CursorY: len(zone) - 1, SessionID: "$3", WindowID: "@3", Alt: true}},
		map[string]tmux.Labels{"%9": {Session: "gone-beef0000", Window: "gone", Command: "sh"}},
		[]tmux.Capture{{PaneID: "%9", Height: 40, Lines: zone}},
		map[string]tmux.Capture{"%9": {PaneID: "%9", Height: 40, Lines: zone}},
		now, time.Second)
	r.SetClaudeSession("nuc", "%9", uuid)

	// tmux no longer reports the pane at all: the row is retired but kept.
	r.Update("nuc", nil, map[string]tmux.Labels{}, nil, map[string]tmux.Capture{},
		now.Add(10*time.Second), time.Second)
	rows := r.Panes()
	if len(rows) != 1 || rows[0].ClassifiedState != state.Gone {
		t.Fatalf("the premise failed: %d rows, first state %v, want 1 row in state gone",
			len(rows), rows[0].ClassifiedState)
	}

	listing := agents.Session{ID: "beef0000", SessionID: uuid, Kind: "background", PID: 7000,
		Name: "gone", State: "working", StartedAt: now.Add(-time.Hour)}
	r.UpdateAgents("nuc", []agents.Session{listing}, now.Add(20*time.Second))

	rows = r.Panes()
	if len(rows) != 2 {
		t.Fatalf("a retired pane and a live listing gave %d rows, want 2 — the live session needs a "+
			"row of its own, because the corpse cannot be attached to or typed into", len(rows))
	}
	var corpse, live *Pane
	for i := range rows {
		if rows[i].Kind == KindPane {
			corpse = &rows[i]
		} else {
			live = &rows[i]
		}
	}
	if corpse == nil || live == nil {
		t.Fatalf("want one pane row and one agent row, got kinds %v and %v", rows[0].Kind, rows[1].Kind)
	}
	if corpse.stateAt(now.Add(20*time.Second)) == state.Works {
		t.Errorf("the retired pane reads works — it absorbed a fact about a process it no longer holds")
	}
}
