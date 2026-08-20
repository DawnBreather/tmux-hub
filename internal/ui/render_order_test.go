package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// The ordering on a real screen, at the width §16 commits to. Two rows in ONE rank, so only recency
// can separate them, and the names run against the alphabetical tiebreak — without that the assertion
// passes on the old comparator and tests nothing.
//
// The rows are PANE-LESS, so the field that orders them is StateSince, not Activity: UpdateAgents sets
// a pane-less row's Activity to its session's START time and never moves it. Activity is set here too,
// and set INVERTED against the state changes, so the test also proves birth does not win — an earlier
// version of this fixture set only Activity and passed for the wrong reason until the comparator
// started branching on Kind.
//
// This is the only screen coverage the ordering has: 4 of 83 Pane literals in this package set
// Activity, and the mockup generator sets none, which is why the mockup regenerates identical.
func TestTheScreenShowsTheNewestRowFirstWithinARank(t *testing.T) {
	now := time.Now()
	older := registry.Pane{
		Kind: registry.KindAgent, Command: "background", Host: "local",
		Session: "aaa-failed-two-hours-ago", PaneID: "agent:11111111@aaaaaaaa",
		ClassifiedState: state.Error,
		StateSince:      now.Add(-2 * time.Hour),
		Activity:        now, // born late; must not win
	}
	newer := registry.Pane{
		Kind: registry.KindAgent, Command: "background", Host: "local",
		Session: "zzz-failed-two-minutes-ago", PaneID: "agent:22222222@bbbbbbbb",
		ClassifiedState: state.Error,
		StateSince:      now.Add(-2 * time.Minute),
		Activity:        now.Add(-3 * time.Hour), // born early
	}

	// ONE slice, sorted in place, and the SAME slice handed to Render. Sorting a fresh literal and
	// rendering another one throws the sort away and the test then fails in both poles.
	rows := []registry.Pane{older, newer}
	registry.SortByAttention(rows) // the product's own comparator, not a hand-ordered slice

	out := Render(Frame{Panes: rows, Width: 80, Height: 24})
	iNew := strings.Index(out, "zzz-failed-two-minutes-ago")
	iOld := strings.Index(out, "aaa-failed-two-hours-ago")
	if iNew < 0 || iOld < 0 {
		t.Fatalf("both rows must be on the screen: newest at %d, oldest at %d\n%s", iNew, iOld, out)
	}
	if iNew > iOld {
		t.Errorf("the older row is drawn first; recency must put the newest above\n%s", out)
	}
}
