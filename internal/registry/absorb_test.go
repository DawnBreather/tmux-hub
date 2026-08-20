package registry

import (
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/agents"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// Wiring the pane-to-session join made the absorb branch reachable, and reachable it had two ways of
// making the screen WORSE than the pixels alone.
//
// Both were found by an adversarial review of the commit that wired it, and both are regressions of
// that commit rather than old defects: while nothing called SetClaudeSession, `AgentSeenAt` was
// always zero and `State()` never preferred the listing.

func onePaneAt(now time.Time, zone []string) *Registry {
	r := New()
	r.Update("local",
		[]tmux.Delta{{PaneID: "%3", Activity: now.Unix(), PaneHeight: 24, WindowWidth: 80,
			PanePID: 7, CursorY: len(zone) - 1, SessionID: "$0", WindowID: "@1"}},
		map[string]tmux.Labels{"%3": {Session: "api", Window: "fix", Command: "claude"}},
		[]tmux.Capture{{PaneID: "%3", Height: 24, Lines: zone}},
		map[string]tmux.Capture{"%3": {PaneID: "%3", Height: 24, Lines: zone}},
		now, time.Second)
	r.SetClaudeSession("local", "%3", "sess-uuid")
	return r
}

// A permission dialog is the ONE thing only the pixels can see, and the listing must not be allowed
// to paper over it.
//
// Measured through the real path: of the words `claude agents --json` reports, only `blocked` maps to
// `needs`, and on 2.1.233 `blocked` is given to `kind: background` rows — while every pane the launch
// door creates is INTERACTIVE, which is exactly the population the join reaches. So for the panes
// this join joins, the listing's word can never be `needs`, and each of the others demoted a waiting
// row: "" to `unknown` (rank 5), `idle` to 3, `working` to 4, `done` to 6. A row asking for the
// operator sank below rows that want nothing, for the ten minutes the fact stays fresh.
func TestAListingWordNeverHidesAWaitingPane(t *testing.T) {
	now := time.Unix(1786450000, 0)
	dialog := []string{"● Ran tests", "Do you want to proceed?", "❯ 1. Yes"}

	for _, word := range []string{"", "idle", "working", "busy", "done", "completed", "queued", "wat"} {
		r := onePaneAt(now, dialog)
		rows := r.Panes()
		if len(rows) != 1 || rows[0].ClassifiedState != state.Needs {
			t.Fatalf("the fixture is not a waiting pane: %d rows, state %v",
				len(rows), rows[0].ClassifiedState)
		}
		r.UpdateAgents("local", []agents.Session{
			{ID: "sess", SessionID: "sess-uuid", Kind: "interactive", Name: "api", State: word,
				StartedAt: now.Add(-time.Hour)},
		}, now)

		got := r.Panes()
		if len(got) != 1 {
			t.Fatalf("word %q produced %d rows, want the pane absorbed", word, len(got))
		}
		if st := got[0].stateAt(now); st != state.Needs {
			t.Errorf("word %q turned a waiting pane into %v (rank %d) — only the pixels can see a "+
				"dialog, and the operator stops being told", word, st, st.Rank())
		}
	}
}

// The listing still WINS where it knows more than the pixels, which is the whole reason for the
// join: the classifier cannot tell a finished agent from one that is merely silent, and `quiet` is a
// guess where `done` is a fact.
func TestTheListingStillOverridesASilentPane(t *testing.T) {
	now := time.Unix(1786450000, 0)
	// An old prompt and nothing else: the classifier reaches `quiet` from the activity age.
	r := New()
	r.Update("local",
		[]tmux.Delta{{PaneID: "%3", Activity: now.Add(-time.Hour).Unix(), PaneHeight: 24,
			WindowWidth: 80, PanePID: 7, SessionID: "$0", WindowID: "@1"}},
		map[string]tmux.Labels{"%3": {Session: "api", Window: "fix", Command: "claude"}},
		[]tmux.Capture{{PaneID: "%3", Height: 24, Lines: []string{"● done", "❯"}}},
		map[string]tmux.Capture{"%3": {PaneID: "%3", Height: 24, Lines: []string{"● done", "❯"}}},
		now, time.Second)
	r.SetClaudeSession("local", "%3", "sess-uuid")
	if got := r.Panes()[0].ClassifiedState; got != state.Quiet {
		t.Fatalf("the fixture classifies as %v, not quiet — this test needs a silent pane", got)
	}

	r.UpdateAgents("local", []agents.Session{
		{ID: "sess", SessionID: "sess-uuid", Kind: "background", Name: "api", State: "done",
			StartedAt: now.Add(-time.Hour)},
	}, now)

	if st := r.Panes()[0].stateAt(now); st != state.Done {
		t.Errorf("the listing's `done` did not override a guessed `quiet`: %v", st)
	}
}

// An empty word carries no information, so it must not stamp AgentSeenAt at all: a fact that says
// nothing must not become the freshest fact about the row.
func TestAnEmptyListingWordLeavesThePixelsInCharge(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := onePaneAt(now, []string{"● working", "❯"})
	r.UpdateAgents("local", []agents.Session{
		{ID: "sess", SessionID: "sess-uuid", Kind: "interactive", Name: "api", State: "",
			StartedAt: now.Add(-time.Hour)},
	}, now)

	got := r.Panes()[0]
	if !got.AgentSeenAt.IsZero() {
		t.Errorf("an empty word stamped AgentSeenAt (%v), so State() will prefer a fact that says "+
			"nothing for the whole freshness window", got.AgentSeenAt)
	}
	// The name is still worth taking: it is what the operator called the session, and it is not a
	// state claim.
	if got.AgentName != "api" {
		t.Errorf("the name was dropped with the state: %q", got.AgentName)
	}
}

// A pane tmux has RETIRED must fall out of the join, or the session's row folds into a corpse: the
// dead pane reads the listing's `works` for the freshness window instead of the session appearing as
// its own row with a door.
func TestAVanishedPaneStopsAbsorbingItsSession(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := onePaneAt(now, []string{"● working", "❯"})

	// The pane disappears: an Update with no deltas retires it.
	r.Update("local", nil, nil, nil, nil, now.Add(time.Second), time.Second)
	var goneRow Pane
	for _, p := range r.Panes() {
		if p.PaneID == "%3" {
			goneRow = p
		}
	}
	if goneRow.ClassifiedState != state.Gone {
		t.Fatalf("the fixture's pane is %v, not gone — this test needs a retired pane",
			goneRow.ClassifiedState)
	}

	r.UpdateAgents("local", []agents.Session{
		{ID: "sess", SessionID: "sess-uuid", Kind: "background", Name: "api", State: "working",
			StartedAt: now.Add(-time.Hour)},
	}, now.Add(2*time.Second))

	rows := r.Panes()
	var agentRows, paneRows int
	for _, p := range rows {
		switch p.Kind {
		case KindAgent:
			agentRows++
		case KindPane:
			if st := p.stateAt(now.Add(2 * time.Second)); st != state.Gone {
				t.Errorf("the retired pane reads %v — a corpse absorbed the listing's fact", st)
			}
			paneRows++
		}
	}
	if agentRows != 1 {
		t.Errorf("%d agent rows: a session whose pane has gone must get its own row back", agentRows)
	}
}
