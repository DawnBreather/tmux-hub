package hide

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// §18's safety rule and the sentence that announces it must read the SAME field, or the hub promises
// a row stays and hides it in the same keystroke.
//
// The rule lives here: a marked pane that is WAITING is not hidden, because waiting is the only state
// where hiding loses work. Four sites re-derived "is this marked row waiting" and they split on the
// field — `Hidden` read `p.ClassifiedState`, while the note eight lines from its caller read
// `p.State()`. While nothing wired the pane-to-session join the two were identical and the split was
// invisible; the join made them differ.
//
// `State()` is the right field for a safety gate: it prefers the listing's fact while that fact is
// fresh, and for a background row the listing is the ONLY source that can say the session is blocked
// — there are no pixels to read. A gate that loses work should take the fresher answer.
func waitingByListingOnly() registry.Pane {
	return registry.Pane{
		Kind: registry.KindPane, Host: "nuc", Session: "api", Window: "w",
		WindowIndex: 1, Index: 0, PaneID: "%4", StartCommand: `"claude"`,
		// The pixels do not see a prompt; the listing says the session is blocked, freshly.
		ClassifiedState: state.Works,
		AgentState:      state.Needs,
		AgentSeenAt:     nowish(),
	}
}

func TestASafetyGateAndItsSentenceReadOneField(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hidden.json"))
	if err != nil {
		t.Fatal(err)
	}
	p := waitingByListingOnly()
	if p.ClassifiedState == p.State() {
		t.Fatalf("the fixture cannot see the split: classified and effective are both %v", p.State())
	}
	if err := s.Toggle(p); err != nil {
		t.Fatal(err)
	}

	// Marked and waiting: §18 says it stays.
	if s.Hidden(p) {
		t.Errorf("a marked pane the listing reports as waiting was hidden — §18 says a waiting row "+
			"stays, and this is the state where hiding loses work (effective %v)", p.State())
	}
	// And the predicate the note uses must be the same one, so the sentence cannot promise what the
	// gate does not do.
	if !s.Resurfaced(p) {
		t.Error("Resurfaced disagrees with Hidden about the same row")
	}
}

// The ordinary case still works: a marked row that is NOT waiting goes away.
func TestAMarkedRowThatIsNotWaitingIsHidden(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hidden.json"))
	if err != nil {
		t.Fatal(err)
	}
	p := waitingByListingOnly()
	p.AgentState, p.AgentSeenAt = state.Works, nowish()
	if err := s.Toggle(p); err != nil {
		t.Fatal(err)
	}
	if !s.Hidden(p) {
		t.Error("a marked row that wants nothing must go away")
	}
	if s.Resurfaced(p) {
		t.Error("Resurfaced says a row that wants nothing is resurfaced")
	}
}

// An UNMARKED waiting row is not "resurfaced" — that word means a row the operator hid which came
// back because it needs them.
func TestAnUnmarkedWaitingRowIsNotResurfaced(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hidden.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Resurfaced(waitingByListingOnly()) {
		t.Error("a row nobody hid cannot be resurfaced")
	}
}

// nowish is a fresh instant for AgentSeenAt. The freshness window is ten minutes, so "now" is fresh
// by any measure, and the test is about which FIELD is read rather than about the boundary.
func nowish() time.Time { return time.Now() }
