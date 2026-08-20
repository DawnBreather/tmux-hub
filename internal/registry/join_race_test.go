package registry

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// The join runs on the agents poll's goroutine while the tick writes panes and the UI thread reads
// them, so it needs its own concurrent test in the same commit as its first cross-goroutine caller.
//
// It asserts the VALUE, not the silence. A test that only asks whether `-race` stayed quiet passes
// against a copy-on-write "fix" that discards the write — which is the one outcome worse than a
// race, because it removes the evidence and keeps the symptom. This repo shipped that shape once:
// `Poller.Add` appended to a slice while a tick held `&p.hosts[i]`, growslice reallocated, and the
// tick's status writes landed in the abandoned array. `-race` was green throughout, because no test
// called the two together.
//
// What makes the join safe is that `Update` MUTATES the existing *Pane for a key it has seen before
// and allocates only on first sight, and it never assigns ClaudeSession — so the field has exactly
// one writer and merging is by KEY, never by index.
func TestTheJoinsWriteSurvivesConcurrentTicksAndReads(t *testing.T) {
	const n = 64
	now := time.Unix(1786450000, 0)
	r := New()

	ds := make([]tmux.Delta, 0, n)
	ls := map[string]tmux.Labels{}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%%%d", i)
		ds = append(ds, tmux.Delta{PaneID: id, Activity: now.Unix(), PaneHeight: 24,
			WindowWidth: 80, PanePID: 1000 + i, SessionID: "$0", WindowID: "@1"})
		ls[id] = tmux.Labels{Session: "api", Window: "w", Command: "claude"}
	}
	r.Update("local", ds, ls, nil, nil, now, 0)

	want := func(i int) string { return fmt.Sprintf("sess-%d", i) }

	var wg sync.WaitGroup
	wg.Add(3)
	// The tick: keeps rewriting every pane while the join runs.
	go func() {
		defer wg.Done()
		for round := 0; round < 40; round++ {
			r.Update("local", ds, ls, nil, nil, now.Add(time.Duration(round)*time.Second), 0)
		}
	}()
	// The join.
	go func() {
		defer wg.Done()
		for round := 0; round < 40; round++ {
			for i := 0; i < n; i++ {
				r.SetClaudeSession("local", fmt.Sprintf("%%%d", i), want(i))
			}
		}
	}()
	// The paint, which is the reader that used to hold a pointer into shared state.
	go func() {
		defer wg.Done()
		for round := 0; round < 40; round++ {
			for _, p := range r.Panes() {
				_ = p.State()
			}
		}
	}()
	wg.Wait()

	// Every write must have SURVIVED forty rounds of ticks. A resort is needed because Panes()
	// hands out the snapshot the last update built, and the last writer here may have been the
	// join rather than a tick.
	r.UpdateAgents("local", nil, now)
	got := map[string]string{}
	for _, p := range r.Panes() {
		got[p.PaneID] = p.ClaudeSession
	}
	if len(got) != n {
		t.Fatalf("%d panes survived, want %d", len(got), n)
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%%%d", i)
		if got[id] != want(i) {
			t.Errorf("pane %s carries ClaudeSession %q, want %q — a tick discarded the join's write",
				id, got[id], want(i))
		}
	}
}

// The ordering the concurrent test above LEAVES TO THE SCHEDULER, asserted deterministically.
//
// A review measured that the join wrote last in 25 of 25 repetitions, so the concurrent test never
// actually exercised "a tick ran after the join" — the case the whole fix rests on. This one forces
// it: write the join, then take a full tick, then require the value to still be there. Calibrated
// against a mutant that copies the pane instead of mutating it, which this catches and the
// concurrent version can miss.
func TestATickAfterTheJoinDoesNotDiscardIt(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	ds := []tmux.Delta{{PaneID: "%3", Activity: now.Unix(), PaneHeight: 24, WindowWidth: 80,
		PanePID: 7, SessionID: "$0", WindowID: "@1"}}
	ls := map[string]tmux.Labels{"%3": {Session: "api", Window: "fix", Command: "claude"}}

	r.Update("local", ds, ls, nil, nil, now, 0)
	r.SetClaudeSession("local", "%3", "sess-uuid")
	// THE tick, after the join. Three of them, because a defect that survives one write-back may
	// not survive several.
	for i := 1; i <= 3; i++ {
		r.Update("local", ds, ls, nil, nil, now.Add(time.Duration(i)*time.Second), time.Second)
	}

	rows := r.Panes()
	if len(rows) != 1 {
		t.Fatalf("%d rows", len(rows))
	}
	if rows[0].ClaudeSession != "sess-uuid" {
		t.Errorf("ClaudeSession is %q after three ticks — a tick discarded the join's write, and the "+
			"absorb branch it feeds can never fire", rows[0].ClaudeSession)
	}
}
