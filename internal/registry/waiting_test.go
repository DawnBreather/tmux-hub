package registry

import (
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/agents"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// The inbox answers "who needs me", and among the rows that do, the one that has waited
// LONGEST is the one to answer first. Today the waiting block ties on host, then session,
// then pane id, so five waiting rows come out alphabetically and a row blocked for three
// hours sits below one blocked for twenty seconds (docs/design.md §21.11.1).
//
// The tiebreak goes in SortByAttention rather than in a screen, so every surface orders
// identically and a fixture cannot produce an order production cannot.
func TestTheWaitingBlockPutsTheLongestWaitFirst(t *testing.T) {
	t0 := time.Unix(1786450000, 0)
	rows := []Pane{
		// Alphabetically first, waiting twenty seconds.
		{Host: "a-host", Session: "aaa", PaneID: "%1",
			ClassifiedState: state.Needs, StateSince: t0.Add(-20 * time.Second)},
		// Alphabetically last, waiting three hours.
		{Host: "z-host", Session: "zzz", PaneID: "%9",
			ClassifiedState: state.Needs, StateSince: t0.Add(-3 * time.Hour)},
		// In between, waiting five minutes.
		{Host: "m-host", Session: "mmm", PaneID: "%5",
			ClassifiedState: state.Needs, StateSince: t0.Add(-5 * time.Minute)},
	}
	SortByAttention(rows)
	want := []string{"%9", "%5", "%1"}
	for i, id := range want {
		if rows[i].PaneID != id {
			var got []string
			for _, p := range rows {
				got = append(got, p.PaneID)
			}
			t.Fatalf("order = %v, want %v — the longest wait must be answered first", got, want)
		}
	}
}

// The tiebreak is scoped to the waiting block. Every other rank keeps host, session, pane
// id, because that order is what makes the list stable between ticks and nothing has
// asked for it to change.
// Renamed and re-fixtured when §22.6's recency tiebreak landed. Outside the waiting block the first
// tiebreak is now RECENCY, so the old fixture — one row three hours old, one twenty seconds old —
// passed for the new reason rather than the one its name claimed, and only because the newer row also
// sorted first alphabetically. What still holds, and what this now tests, is that host/session/pane id
// decides WITHIN one recency bucket: both rows share a minute, so nothing but the alphabet can order
// them, and the fixture puts the alphabetically-first row second in the input.
func TestOtherRanksKeepTheirStableTiebreakInsideOneRecencyBucket(t *testing.T) {
	t0 := time.Unix(1786450000, 0)
	rows := []Pane{
		{Host: "z-host", Session: "zzz", PaneID: "%9",
			ClassifiedState: state.Error, StateSince: t0},
		{Host: "a-host", Session: "aaa", PaneID: "%1",
			ClassifiedState: state.Error, StateSince: t0.Add(-2 * time.Second)},
	}
	SortByAttention(rows)
	if rows[0].PaneID != "%1" {
		t.Errorf("inside one recency bucket the order is host/session/pane id, so %%1 comes "+
			"first, got %s", rows[0].PaneID)
	}
}

// A row with no recorded entry time must not sort to the top: a zero time is the OLDEST
// possible instant, so an unset field would outrank every real wait. Unset means unknown,
// and unknown goes last among equals.
func TestAnUnsetEntryTimeDoesNotWinTheWaitingBlock(t *testing.T) {
	t0 := time.Unix(1786450000, 0)
	rows := []Pane{
		{Host: "a", Session: "a", PaneID: "%0", ClassifiedState: state.Needs},
		{Host: "b", Session: "b", PaneID: "%1",
			ClassifiedState: state.Needs, StateSince: t0.Add(-time.Second)},
	}
	SortByAttention(rows)
	if rows[0].PaneID != "%1" {
		t.Errorf("the row with no entry time won the block; want %%1 first, got %s",
			rows[0].PaneID)
	}
}

// StateSince is the tick the row ENTERED its state, so it must not move while the row
// stays in that state — otherwise "how long has it waited" resets every tick and the
// sort above measures nothing.
func TestStateSinceIsTheEntryTickAndDoesNotMoveWhileTheStateHolds(t *testing.T) {
	t0 := time.Unix(1786450000, 0)
	ds := []tmux.Delta{{PaneID: "%0", Activity: t0.Unix(), PaneHeight: 24, WindowWidth: 80}}
	ls := map[string]tmux.Labels{"%0": {Session: "s", Command: "claude"}}
	asking := []tmux.Capture{{PaneID: "%0", Height: 24,
		Lines: []string{"● Ran tests", "Do you want to proceed?", "❯"}}}

	r := New()
	r.Update("local", ds, ls, asking, nil, t0, time.Second)
	first := r.Panes()[0]
	if first.State() != state.Needs {
		t.Fatalf("state = %v, want needs — the fixture is not asking a question", first.State())
	}
	if !first.StateSince.Equal(t0) {
		t.Errorf("StateSince = %v, want %v on the tick it was first seen", first.StateSince, t0)
	}

	// Three more ticks, still asking. The zone changes each time (so the pane counts as
	// active), which is exactly the case that would reset a naive implementation.
	for i := 1; i <= 3; i++ {
		tn := t0.Add(time.Duration(i) * time.Second)
		z := []tmux.Capture{{PaneID: "%0", Height: 24,
			Lines: []string{"● Ran tests", "Do you want to proceed?", "❯" + string(rune('a'+i))}}}
		r.Update("local", ds, ls, z, nil, tn, time.Second)
	}
	held := r.Panes()[0]
	if !held.StateSince.Equal(t0) {
		t.Errorf("StateSince moved to %v after three ticks in the same state; it must stay "+
			"at %v or the waiting sort measures the tick rate", held.StateSince, t0)
	}

	// Now it stops asking: the entry time must move to the tick the state changed.
	t4 := t0.Add(4 * time.Second)
	quiet := []tmux.Capture{{PaneID: "%0", Height: 24, Lines: []string{"● done", "❯"}}}
	r.Update("local", ds, ls, quiet, nil, t4, time.Second)
	after := r.Panes()[0]
	if after.State() == state.Needs {
		t.Fatalf("still needs after the question went away: %v", after.State())
	}
	if !after.StateSince.Equal(t4) {
		t.Errorf("StateSince = %v after the state changed, want %v", after.StateSince, t4)
	}
}

// An agent row waits too, and every waiting row on the 21-row fleet of 2026-08-13 had no
// pane at all — a claim §22.1 does not re-derive at its 45-row denominator, though the
// tiebreak it motivates stands: one that only worked for panes would order the wrong screen.
func TestAnAgentRowRecordsWhenItStartedWaiting(t *testing.T) {
	t0 := time.Unix(1786450000, 0)
	blocked := agents.Session{ID: "aaaa1111", SessionID: "aaaa1111-x", Kind: "background",
		Name: "waiting", State: "blocked", CWD: "/tmp/a"}
	r := New()
	r.UpdateAgents("nuc", []agents.Session{blocked}, t0)
	if got := r.Panes()[0].StateSince; !got.Equal(t0) {
		t.Errorf("StateSince = %v, want %v", got, t0)
	}
	// Still blocked one minute later: the entry time holds.
	r.UpdateAgents("nuc", []agents.Session{blocked}, t0.Add(time.Minute))
	if got := r.Panes()[0].StateSince; !got.Equal(t0) {
		t.Errorf("StateSince moved to %v while the session stayed blocked; want %v", got, t0)
	}
	// It answers: the entry time moves.
	working := blocked
	working.State = "working"
	t2 := t0.Add(2 * time.Minute)
	r.UpdateAgents("nuc", []agents.Session{working}, t2)
	if got := r.Panes()[0].StateSince; !got.Equal(t2) {
		t.Errorf("StateSince = %v after the session started working, want %v", got, t2)
	}
}
