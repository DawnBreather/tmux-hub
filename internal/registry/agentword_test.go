package registry

import (
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/agents"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// AgentWord is the listing's word before the hub folds it, and it exists because the fold is
// lossy in exactly the place two decisions need it: `Attention()` maps `failed` AND `error` onto
// state.Error, so a session whose worker vanished reads the same as one whose work errored.
//
// Both producers get a test, and both go THROUGH the producer. A field with two producers and
// fixtures that set it by hand is how this repo has shipped a dead field three times.

// Producer 1: a session with no pane becomes an agent row.
func TestTheAgentRowCarriesTheListingsOwnWord(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	r.UpdateAgents("local", []agents.Session{
		sess("30f3382b", "20260817-cicd", "background", "/w/iac", "failed", now),
		sess("45dfef2f", "map-render", "background", "/w/map", "blocked", now),
	}, now)

	got := map[string]Pane{}
	for _, p := range r.Panes() {
		got[p.AgentID] = p
	}
	if w := got["30f3382b"].AgentWord; w != "failed" {
		t.Errorf("AgentWord = %q, want the raw `failed` — the door's gate cannot be written "+
			"against state.Error, which a genuine error also produces", w)
	}
	// And the fold is still there beside it, unchanged.
	if st := got["30f3382b"].ClassifiedState; st != state.Error {
		t.Errorf("ClassifiedState = %v, want error", st)
	}
	if w := got["45dfef2f"].AgentWord; w != "blocked" {
		t.Errorf("a blocked row's word is %q", w)
	}
}

// Producer 2: a listing row whose session is running in a pane the hub polls folds INTO that pane.
// The word has to travel with it, or a pane row could never be asked whether its worker is gone.
func TestAnAbsorbedPaneCarriesTheListingsOwnWord(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	r.Update("local",
		[]tmux.Delta{{PaneID: "%3", Activity: now.Unix(), PaneHeight: 24, WindowWidth: 80,
			PanePID: 7, SessionID: "$0", WindowID: "@1"}},
		map[string]tmux.Labels{"%3": {Session: "api", Window: "fix", Command: "claude"}},
		[]tmux.Capture{{PaneID: "%3", Height: 24, Lines: []string{"● working", "❯"}}},
		map[string]tmux.Capture{"%3": {PaneID: "%3", Height: 24, Lines: []string{"● working", "❯"}}},
		now, time.Second)
	r.SetClaudeSession("local", "%3", "30f3382b-f68c-4baf-98fd-68d4fd1c3da4")

	r.UpdateAgents("local", []agents.Session{
		sess("30f3382b", "20260817-cicd", "background", "/w/iac", "failed", now),
	}, now)

	rows := r.Panes()
	if len(rows) != 1 || rows[0].Kind != KindPane {
		t.Fatalf("the fixture did not absorb: %d rows, kind %q", len(rows), rows[0].Kind)
	}
	if w := rows[0].AgentWord; w != "failed" {
		t.Errorf("the absorbed pane's AgentWord is %q — the word was dropped at the one producer "+
			"whose rows have a pane", w)
	}
}

// An empty word must not overwrite what the row already knows. A version that reports neither
// `state` nor `status` is measured and real (2 of 21 sessions), and the absorb branch already
// refuses to stamp AgentSeenAt for it — the word follows the same rule, so a listing that says
// nothing cannot erase a listing that said something.
func TestAnEmptyWordDoesNotEraseTheWordAlreadyThere(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	r.Update("local",
		[]tmux.Delta{{PaneID: "%3", Activity: now.Unix(), PaneHeight: 24, WindowWidth: 80,
			PanePID: 7, SessionID: "$0", WindowID: "@1"}},
		map[string]tmux.Labels{"%3": {Session: "api", Window: "fix", Command: "claude"}},
		[]tmux.Capture{{PaneID: "%3", Height: 24, Lines: []string{"● working", "❯"}}},
		map[string]tmux.Capture{"%3": {PaneID: "%3", Height: 24, Lines: []string{"● working", "❯"}}},
		now, time.Second)
	r.SetClaudeSession("local", "%3", "30f3382b-f68c-4baf-98fd-68d4fd1c3da4")

	one := sess("30f3382b", "20260817-cicd", "background", "/w/iac", "failed", now)
	r.UpdateAgents("local", []agents.Session{one}, now)
	one.State = ""
	r.UpdateAgents("local", []agents.Session{one}, now.Add(time.Second))

	if w := r.Panes()[0].AgentWord; w != "failed" {
		t.Errorf("AgentWord = %q — a listing that reported no word at all erased the last one "+
			"that did", w)
	}
}
