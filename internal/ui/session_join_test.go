package ui

import (
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/agents"
	"github.com/DawnBreather/tmux-hub/internal/broadcast"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// The pane-to-session join was built, tested and documented, and nothing called it.
//
// `Registry.SetClaudeSession` and `proc.SessionID` both had zero callers outside their own tests,
// so the branch in `UpdateAgents` that folds a listing row into the PANE running it could never
// fire. Three consequences, all of them things §22 reasons from: a session with a pane appeared
// TWICE, once as a pane row and once as a pane-less row; §14's only supportable answer to
// quiet-versus-idle (the classifier cannot separate a finished agent from a working one, the
// listing can) was not in effect; and the door would have duplicated every row it woke, because
// every pane `a` creates is hub-created and therefore Adopted.
//
// This tests the WIRING. The registry's own absorb branch already had coverage — which is exactly
// how a join can be tested and dead at the same time.

func joinModel(t *testing.T) (model, *registry.Registry, *broadcast.Keeper) {
	t.Helper()
	reg := registry.New()
	k := broadcast.NewKeeper(nil)
	return model{reg: reg, keeper: k, width: 100, height: 24}, reg, k
}

// seedPane puts ONE real tmux pane in the registry: a claude pane in session api. The names come
// from Labels rather than from the Delta, which is where tmux's own format puts them.
func seedPane(reg *registry.Registry, now time.Time) {
	reg.Update("local",
		[]tmux.Delta{{PaneID: "%3", Activity: now.Unix(), PaneHeight: 24, WindowWidth: 80,
			PanePID: 7, WindowIndex: 1, Index: 0, SessionID: "$0", WindowID: "@1"}},
		map[string]tmux.Labels{"%3": {Session: "api", Window: "fix", Command: "claude"}},
		[]tmux.Capture{{PaneID: "%3", Height: 24, Lines: []string{"● working", "❯"}}},
		map[string]tmux.Capture{"%3": {PaneID: "%3", Height: 24, Lines: []string{"● working", "❯"}}},
		now, 0)
}

// A pane the hub created carries both halves of its identity at birth, so the join needs no
// process walk: Adopt has the session id, and this pushes it where UpdateAgents can read it.
func TestTheJoinPushesAnAdoptedSessionIntoTheRegistry(t *testing.T) {
	m, reg, k := joinModel(t)
	now := time.Unix(1786450000, 0)
	seedPane(reg, now)

	before := reg.Panes()
	if len(before) != 1 || before[0].ClaudeSession != "" {
		t.Fatalf("the fixture starts with %d rows and ClaudeSession %q; it must start unjoined",
			len(before), before[0].ClaudeSession)
	}

	k.Adopt("local", "%3", "1ff133f7-c34a-4c60-91e5-b0048842cc66")
	m.joinAdoptedSessions()

	// Panes() hands out the SORTED SNAPSHOT the last update built, so the write becomes visible on
	// the next one — which is exactly when UpdateAgents reads it. Take that update rather than
	// reaching into the map: this asserts the write through the path production uses.
	reg.UpdateAgents("local", nil, now)
	after := reg.Panes()
	if len(after) != 1 {
		t.Fatalf("the join changed the row count to %d", len(after))
	}
	if after[0].ClaudeSession != "1ff133f7-c34a-4c60-91e5-b0048842cc66" {
		t.Errorf("the pane carries ClaudeSession %q after the join, want the adopted id",
			after[0].ClaudeSession)
	}
}

// The consequence, end to end through the real producer: with the join in place a listing row for a
// session that HAS a pane updates that pane instead of adding a second row.
func TestAJoinedSessionDoesNotProduceASecondRow(t *testing.T) {
	m, reg, k := joinModel(t)
	now := time.Unix(1786450000, 0)
	const uuid = "1ff133f7-c34a-4c60-91e5-b0048842cc66"
	seedPane(reg, now)
	k.Adopt("local", "%3", uuid)
	m.joinAdoptedSessions()

	reg.UpdateAgents("local", []agents.Session{
		{ID: "1ff133f7", SessionID: uuid, Kind: "interactive", Name: "api", State: "blocked",
			StartedAt: now.Add(-time.Hour)},
	}, now)

	rows := reg.Panes()
	if len(rows) != 1 {
		var got []string
		for _, p := range rows {
			got = append(got, string(p.Kind)+":"+p.PaneID)
		}
		t.Fatalf("one session with one pane produced %d rows: %v", len(rows), got)
	}
	if rows[0].Kind != registry.KindPane {
		t.Errorf("the surviving row is %q; the PANE row is the one that can be written to",
			rows[0].Kind)
	}
	// And the listing's fact reached it — which is the whole point of joining rather than merely
	// de-duplicating: the classifier cannot tell a finished agent from a working one, the listing
	// can (§14).
	if rows[0].AgentState != state.Needs {
		t.Errorf("AgentState = %v, want needs — the listing said blocked", rows[0].AgentState)
	}
	if !rows[0].AgentSeenAt.Equal(now) {
		t.Errorf("AgentSeenAt = %v, want the tick's own instant — State() prefers the listing's "+
			"fact only while it is fresh, and a zero stamp is never fresh", rows[0].AgentSeenAt)
	}
	// The effective state is NOT asserted here on purpose: State() reads the wall clock, and this
	// fixture's instant is fixed, so the assertion would measure how long ago 1786450000 was.
	// registry's own TestTheSortUsesTheStateAtOneInstant and TestTheFreshnessBoundaryIsExclusive
	// own that property, at an instant they control.
}

// Without the join the same input produces TWO rows, and this test says so out loud rather than
// leaving the reader to trust the sentence above. It is the control for the test before it: if the
// two ever agree, one of them has stopped measuring anything.
func TestWithoutTheJoinTheSameSessionProducesTwoRows(t *testing.T) {
	_, reg, _ := joinModel(t)
	now := time.Unix(1786450000, 0)
	const uuid = "1ff133f7-c34a-4c60-91e5-b0048842cc66"
	seedPane(reg, now)
	// No Adopt, no join.
	reg.UpdateAgents("local", []agents.Session{
		{ID: "1ff133f7", SessionID: uuid, Kind: "interactive", Name: "api", State: "blocked",
			StartedAt: now.Add(-time.Hour)},
	}, now)

	if n := len(reg.Panes()); n != 2 {
		t.Errorf("unjoined, one session with one pane produced %d rows; the duplicate this "+
			"branch fixes is what makes the joined case worth asserting", n)
	}
}

// A nil Keeper and a nil Registry are both real states — every model built by hand in this package
// has them — so the join must need no branch at its call site.
func TestTheJoinIsSafeWithNothingToJoin(t *testing.T) {
	var m model
	m.joinAdoptedSessions() // must not panic

	m2, _, _ := joinModel(t)
	m2.joinAdoptedSessions() // an empty Keeper is not an error either
}
