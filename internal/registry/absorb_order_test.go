package registry

import (
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/agents"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// THE ORDERING THE OTHER ABSORB TESTS DO NOT EXERCISE, and the one that actually happens: the
// listing's word is recorded FIRST, while the pane really is working, and the dialog appears
// afterwards.
//
// This is why the `needs` guard has to be on the READ side. A write-side guard — which is what
// shipped first — can only refuse a word that arrives while the prompt is already on screen, and
// `TestAListingWordNeverHidesAWaitingPane` builds its fixture exactly that way, so it tested one
// ordering of two and passed against the defect. Measured before the read guard: the row read
// `works` at rank 4 against `needs` at rank 0 for the full ten-minute freshness window, sorting the
// one row that wants the operator below every row that wants nothing.
//
// The sweep is 20 s and a prompt appears whenever the agent reaches it, so this ordering is the
// common case rather than a corner.
func TestAWordRecordedBeforeTheDialogDoesNotBuryIt(t *testing.T) {
	now := time.Unix(1786450000, 0)
	working := []string{"● building", "esc to interrupt"}
	dialog := []string{"● Ran tests", "Do you want to proceed?", "❯ 1. Yes"}
	seed := func(r *Registry, at time.Time, zone []string) {
		r.Update("local",
			[]tmux.Delta{{PaneID: "%1", Activity: at.Unix(), PaneHeight: 24, WindowWidth: 80,
				PanePID: 7, CursorY: len(zone) - 1, SessionID: "$0", WindowID: "@1"}},
			map[string]tmux.Labels{"%1": {Session: "api", Window: "w", Command: "claude"}},
			[]tmux.Capture{{PaneID: "%1", Height: 24, Lines: zone}},
			map[string]tmux.Capture{"%1": {PaneID: "%1", Height: 24, Lines: zone}},
			at, time.Second)
	}
	r := New()
	seed(r, now, working)
	r.SetClaudeSession("local", "%1", "sess-uuid")
	t.Logf("t0 classified=%v", r.Panes()[0].ClassifiedState)

	// The 20 s sweep records `working` while the pane really is working.
	r.UpdateAgents("local", []agents.Session{
		{ID: "s", SessionID: "sess-uuid", Kind: "interactive", Name: "api", State: "working",
			StartedAt: now.Add(-time.Hour)},
	}, now)

	// Five seconds later the dialog appears. The pixels see it; the listing has not run again.
	seed(r, now.Add(5*time.Second), dialog)
	p := r.Panes()[0]
	t.Logf("t1 classified=%v  agent=%v  effective=%v (rank %d, needs is %d)",
		p.ClassifiedState, p.AgentState, p.stateAt(now.Add(5*time.Second)),
		p.stateAt(now.Add(5*time.Second)).Rank(), state.Needs.Rank())
	for _, d := range []time.Duration{time.Minute, 5 * time.Minute, 9*time.Minute + 50*time.Second, 10 * time.Minute} {
		at := now.Add(5*time.Second + d)
		t.Logf("   +%-8s effective=%v", d, r.Panes()[0].stateAt(at))
	}
	// Every offset inside the window, not just the first: the defect was a ten-minute lie, so a
	// test that checks only the instant after the dialog would pass against a one-tick fix.
	for _, d := range []time.Duration{0, time.Second, time.Minute, 5 * time.Minute, 9*time.Minute + 59*time.Second} {
		at := now.Add(5*time.Second + d)
		if got := r.Panes()[0].stateAt(at); got != state.Needs {
			t.Errorf("+%s after the dialog: a pane asking a permission question reads %v (rank %d), "+
				"so it sorts below rows that want nothing and its glyph says there is nothing to answer",
				d, got, got.Rank())
		}
	}
	// And the listing's fact still wins where the pixels have nothing to say — the guard must be
	// about `needs` alone, not a blanket refusal of the join.
	if got := r.Panes()[0].AgentState; got != state.Works {
		t.Errorf("the recorded word was lost: AgentState = %v", got)
	}
}
