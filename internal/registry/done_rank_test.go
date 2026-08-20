package registry

import (
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/agents"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// A finished background job must sort BELOW a live interactive session, and it is measured
// through the PRODUCER on purpose: the fold that made the two equal lived in
// agents.Attention (`done` -> `idle`), so a test that hand-builds two Panes with the states it
// expects asserts its own fixture and cannot see it. Third instance of that shape in this repo.
func TestAFinishedJobSortsBelowALiveSession(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	r.UpdateAgents("local", []agents.Session{
		{ID: "fin", SessionID: "fin-uuid", Kind: "background", Name: "envoy hotfix",
			State: "done", StartedAt: now.Add(-time.Minute)},
		{ID: "liv", SessionID: "liv-uuid", Kind: "interactive", Name: "scratch",
			State: "idle", StartedAt: now.Add(-time.Hour)},
	}, now)

	rows := r.Panes()
	if len(rows) != 2 {
		t.Fatalf("the listing produced %d rows, want 2", len(rows))
	}
	SortByAttention(rows)

	byID := map[string]Pane{}
	for _, p := range rows {
		byID[p.SessionID] = p
	}
	fin, live := byID["fin-uuid"], byID["liv-uuid"]
	if fin.State() != state.Done {
		t.Errorf("the finished job is %v, want done", fin.State())
	}
	if live.State() != state.Idle {
		t.Errorf("the live session is %v, want idle", live.State())
	}
	// The order, and then WHY it is that order. Asserting the rank difference too is what
	// separates "sorted correctly" from "happened to tie and fell out this way": with both
	// rows folded onto idle the tiebreak is alphabetical, and `fin` precedes `liv`.
	if rows[0].SessionID != "liv-uuid" || rows[1].SessionID != "fin-uuid" {
		t.Errorf("the order is %q then %q; a finished job must sit below a live session",
			rows[0].SessionID, rows[1].SessionID)
	}
	if live.State().Rank() >= fin.State().Rank() {
		t.Errorf("live ranks %d and finished ranks %d — one rank for both means only the "+
			"alphabetical tiebreak separates a job that ended from a session waiting for you",
			live.State().Rank(), fin.State().Rank())
	}
}

// And it must not sink so far that it stops being reachable: a done row still outranks the two
// states that mean "nothing is known about this row at all", because the operator reads a
// finished job's output and cannot read either of those.
func TestAFinishedJobStillOutranksAVanishedOne(t *testing.T) {
	if state.Done.Rank() >= state.Gone.Rank() {
		t.Errorf("done ranks %d and gone ranks %d — a finished job is still a row to read",
			state.Done.Rank(), state.Gone.Rank())
	}
	if state.Done.Rank() <= state.Unknown.Rank() {
		t.Errorf("done ranks %d and unknown ranks %d — a row whose state could not be read "+
			"wants a look, and a finished one does not", state.Done.Rank(), state.Unknown.Rank())
	}
}
