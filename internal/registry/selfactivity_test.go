package registry

import (
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// `#{window_activity}` is per-WINDOW, and Classify returns Works from the activity age
// before it tests anything else, so a pane sharing a window with a sibling that prints
// more often than FreshFor could never reach `error`, `quiet` or `idle` — it was pinned
// at `works`, rank 4, the bottom of the inbox. Measured on a private socket, tmux 3.7b:
//
//	window_activity   %0=1786827863  %1=1786827863   ← the SILENT pane reports the
//	window_activity   %0=1786827865  %1=1786827865      chatty one's timestamp
//	history_size      %0=1           %1=9            ← per PANE
//	history_size      %0=1           %1=19              and it moves only for the writer
//
// So the hub keeps each pane's own last-changed tick, and `history_size` is one of the
// three things it watches. This is the whole attention model rather than a corner:
// `needs` survived only because it is tested first.
func TestAPaneIsNotWorkingBecauseItsSiblingIs(t *testing.T) {
	t0 := time.Unix(1786450000, 0)
	// One window, two panes, therefore ONE window_activity — which is the defect's
	// precondition, so the fixture must share it rather than assert on it.
	deltas := func(act int64, silentHist, chattyHist int) []tmux.Delta {
		return []tmux.Delta{
			{PaneID: "%0", Activity: act, HistorySize: silentHist, PaneHeight: 24, WindowWidth: 80,
				WindowID: "@0", CursorY: 3},
			{PaneID: "%1", Activity: act, HistorySize: chattyHist, PaneHeight: 24, WindowWidth: 80,
				WindowID: "@0", CursorY: 23},
		}
	}
	ls := map[string]tmux.Labels{
		"%0": {Session: "s", Window: "w", Command: "bash"},
		"%1": {Session: "s", Window: "w", Command: "bash"},
	}
	// %0 sat down on a failure and has not moved since. %1 chatters.
	silentZone := []string{"$ git status", "fatal: not a git repository", "$"}
	zones := func(chatter string) []tmux.Capture {
		return []tmux.Capture{
			{PaneID: "%0", Height: 24, Lines: silentZone},
			{PaneID: "%1", Height: 24, Lines: []string{chatter, "$"}},
		}
	}

	r := New()
	// Tick 1: nothing is known per pane yet, so the window timestamp is the best prior
	// there is and both panes read as freshly active. That is the defined cold answer.
	r.Update("local", deltas(t0.Unix(), 1, 9), ls, zones("chatter 1"), nil, t0, 2*time.Second)

	// Tick 2, ten seconds later: the window's timestamp advanced because %1 wrote, and
	// %1's history grew. %0's history, cursor and screen are byte-identical.
	t1 := t0.Add(10 * time.Second)
	r.Update("local", deltas(t1.Unix(), 1, 19), ls, zones("chatter 2"), nil, t1, 2*time.Second)

	got := map[string]state.State{}
	for _, p := range r.Panes() {
		got[p.PaneID] = p.State()
	}
	if got["%1"] != state.Works {
		t.Errorf("%%1 (the pane that actually wrote) = %v, want works", got["%1"])
	}
	if got["%0"] != state.Error {
		t.Errorf("%%0 = %v, want error — it has been sitting on `fatal: not a git repository` "+
			"for ten seconds and only its SIBLING has written anything. This is the whole "+
			"defect: a per-window timestamp made every state below `works` unreachable",
			got["%0"])
	}
}

// The other half of the same coupling, and the one the ruling said an ordering change
// could not reach: a pane that is merely silent must become quiet, not works.
func TestASilentPaneGoesQuietEvenWithAChattySibling(t *testing.T) {
	t0 := time.Unix(1786450000, 0)
	mk := func(act int64, chattyHist int) []tmux.Delta {
		return []tmux.Delta{
			{PaneID: "%0", Activity: act, HistorySize: 7, PaneHeight: 24, WindowWidth: 80, WindowID: "@0", CursorY: 3},
			{PaneID: "%1", Activity: act, HistorySize: chattyHist, PaneHeight: 24, WindowWidth: 80, WindowID: "@0", CursorY: 23},
		}
	}
	ls := map[string]tmux.Labels{
		"%0": {Session: "s", Window: "w", Command: "bash"},
		"%1": {Session: "s", Window: "w", Command: "bash"},
	}
	zones := func(chatter string) []tmux.Capture {
		return []tmux.Capture{
			// Nothing a pattern can read: no question, no error, no working marker.
			{PaneID: "%0", Height: 24, Lines: []string{"$ ls", "a  b  c", "$"}},
			{PaneID: "%1", Height: 24, Lines: []string{chatter, "$"}},
		}
	}
	r := New()
	r.Update("local", mk(t0.Unix(), 1), ls, zones("c1"), nil, t0, 2*time.Second)
	// Past QuietAfter, with the sibling still writing on every tick.
	for i := 1; i <= 4; i++ {
		tn := t0.Add(time.Duration(i) * 60 * time.Second)
		r.Update("local", mk(tn.Unix(), 1+i*10), ls, zones("c"+string(rune('1'+i))), nil, tn, 2*time.Second)
	}
	got := map[string]state.State{}
	for _, p := range r.Panes() {
		got[p.PaneID] = p.State()
	}
	if got["%0"] != state.Quiet {
		t.Errorf("%%0 = %v, want quiet — four minutes of its sibling's output is not its "+
			"own activity, and `quiet` is what says a pane may be hung", got["%0"])
	}
	if got["%1"] != state.Works {
		t.Errorf("%%1 = %v, want works", got["%1"])
	}
}

// A pane whose output does not scroll must still count as active: history_size only
// grows when a line leaves the top, so a pane that redraws in place — which is what
// Claude Code does — would look silent if history were the only signal watched.
func TestAPaneThatRedrawsWithoutScrollingIsStillActive(t *testing.T) {
	t0 := time.Unix(1786450000, 0)
	ls := map[string]tmux.Labels{"%0": {Session: "s", Window: "w", Command: "claude"}}
	mk := func() []tmux.Delta {
		// history_size and cursor_y never move.
		return []tmux.Delta{{PaneID: "%0", Activity: t0.Unix(), HistorySize: 5,
			PaneHeight: 24, WindowWidth: 80, WindowID: "@0", CursorY: 12}}
	}
	r := New()
	r.Update("local", mk(), ls, []tmux.Capture{{PaneID: "%0", Height: 24,
		Lines: []string{"● Thinking", "⠋ working", "$"}}}, nil, t0, 2*time.Second)
	// Ten seconds later only the spinner glyph differs — the pane is plainly alive.
	t1 := t0.Add(10 * time.Second)
	r.Update("local", mk(), ls, []tmux.Capture{{PaneID: "%0", Height: 24,
		Lines: []string{"● Thinking", "⠙ working", "$"}}}, nil, t1, 2*time.Second)

	if got := r.Panes()[0].State(); got != state.Works {
		t.Errorf("state = %v, want works — the screen changed, which is this pane's own "+
			"output however little of it scrolled off", got)
	}
}
