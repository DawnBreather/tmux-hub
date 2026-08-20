package ui

import (
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
	tea "github.com/charmbracelet/bubbletea"
)

// The fixtures fill the two id fields the way the PRODUCER does, which is the whole point of the
// pair: `UpdateAgents` puts the listing's SHORT id in AgentID and its full uuid in SessionID, and
// measured on the real CLI only the short one resolves — `claude logs <full uuid>` answers
// `No job matching`. An earlier version of this file put the short id in SessionID, so it could not
// see that the refusal was interpolating the uuid, and the sentence carried a command that fails.
const (
	bgAgentID   = "ea11c9d3"
	bgSessionID = "ea11c9d3-4f01-4690-bbd4-29d42779a154"
)

func backgroundRow() registry.Pane {
	return registry.Pane{
		Kind: registry.KindAgent, Command: "background", Host: "nuc",
		Session: "deploy-audit", AgentID: bgAgentID, SessionID: bgSessionID,
		PaneID: "agent:" + bgAgentID + "@1a2b3c4d", ClassifiedState: state.Error,
	}
}

func interactiveRow() registry.Pane {
	return registry.Pane{
		Kind: registry.KindAgent, Command: "interactive", Host: "local",
		Session: "scratch", SessionID: "9f9f9f9f-0000-0000-0000-000000000001",
		PaneID: "agent:9f9f9f9f@5e6f7a8b", ClassifiedState: state.Idle,
	}
}

// `K` on a pane-less row issued `kill-pane -t agent:<shortid>@<hash>` before this guard, which tmux
// parsed as a session named `agent`, and the failure was counted with no reason shown. §22.9
// decision 3 rules that the hub does not end Claude sessions, so the row is refused with the command
// that does — and the guard precedes the dialog, so it never offers a target the hub will refuse.
//
// The row must be SELECTED: confirmKill acts on m.sel, not on the cursor, and without a selection it
// returns "select a pane with space first", which would make this pass for the wrong reason.
func TestKRefusesWhenTheSelectionHoldsABackgroundRow(t *testing.T) {
	row := backgroundRow()
	m := base(t, 100, 24, row)
	m.sel.Toggle(selKey(row))

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("K")})
	after := got.(model)

	if after.mode == modeConfirm {
		t.Error("K offered a pane-less row for killing; the guard must precede the dialog")
	}
	// "nothing killed" first, because that is the part a mixed selection needs and the part an
	// operator who reads only the head must not be able to misread.
	for _, want := range []string{"nothing killed", "deploy-audit", "nuc", "claude stop " + bgAgentID} {
		if !strings.Contains(after.note, want) {
			t.Errorf("the refusal does not name %q: %q", want, after.note)
		}
	}
	// The argument must be the SHORT id. If someone swaps it back to SessionID the uuid appears here
	// and the command the operator is told to run answers `No job matching`.
	if strings.Contains(after.note, bgSessionID) {
		t.Errorf("the refusal offers the full uuid, which the verb does not resolve: %q", after.note)
	}
}

// The other half of the population needs a different sentence, and it must not claim the row has no
// id at all — the tile beside it shows a session id. What it has no id for is a BACKGROUND job, so
// the refusal names the action instead: end it where it lives.
func TestKRefusesAnInteractiveRowAndDoesNotOfferAStopCommand(t *testing.T) {
	row := interactiveRow()
	m := base(t, 100, 24, row)
	m.sel.Toggle(selKey(row))

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("K")})
	after := got.(model)

	if after.mode == modeConfirm {
		t.Error("K offered an interactive row for killing")
	}
	// `has no pane` is gone from the sentence on purpose: the row already says it by carrying no pane
	// id, `K` refusing already implies it, and with it the refusal ran 87 columns for a session named
	// after its own prompt — so the footer dropped the REMEDY, which is the one part it exists to
	// carry (TestTheKillRefusalKeepsItsRemedyWhateverTheSessionIsCalled).
	for _, want := range []string{"nothing killed", "scratch", "its own terminal"} {
		if !strings.Contains(after.note, want) {
			t.Errorf("the refusal does not name %q: %q", want, after.note)
		}
	}
	if strings.Contains(after.note, "claude stop") {
		t.Errorf("the refusal offers `claude stop` for a row with no background id: %q", after.note)
	}
}

// A MIXED selection refuses whole rather than killing the pane rows and skipping the rest, and it has
// to SAY so: an operator who read only the tail of the sentence would assume the panes were killed.
func TestKRefusesAMixedSelectionWholeAndSaysNothingHappened(t *testing.T) {
	agent := backgroundRow()
	pane := registry.Pane{
		Kind: registry.KindPane, Host: "local", Session: "api", Window: "fix",
		PaneID: "%0", Command: "claude", ClassifiedState: state.Needs,
	}
	m := base(t, 100, 24, pane, agent)
	m.sel.Toggle(selKey(pane))
	m.sel.Toggle(selKey(agent))

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("K")})
	after := got.(model)

	if after.mode == modeConfirm {
		t.Error("a mixed selection opened the dialog; it must refuse whole")
	}
	if !strings.HasPrefix(after.note, "nothing killed") {
		t.Errorf("a mixed refusal must lead with the fact that nothing happened: %q", after.note)
	}
	if !strings.Contains(after.note, "deploy-audit") {
		t.Errorf("the refusal does not name the row that cannot be killed: %q", after.note)
	}
}

// The FIX has to survive the width the project commits to, and the assertion is on the RENDERED
// SCREEN rather than on the raw note. §16 commits to 80 columns; the note is only the FIRST claimant
// of the footer's Fit list, and when other parts compete Fit reserves 3 columns for its ` +N` marker
// and truncates the rest — so a note that measures 80 wide still loses its tail on an 80-column
// terminal. An earlier version of this guard measured the note against 80 and had 0-1 columns of
// headroom on real data; N6 records that this footer has lost content this way before.
func TestBothRefusalsSurviveTheWidthTheProjectCommitsTo(t *testing.T) {
	// Fit's marker budget: a claimant sharing the row is only kept whole if it fits in cols-3.
	const budget = 80 - 3

	for _, c := range []struct {
		name string
		row  registry.Pane
		must string
	}{
		{"background", backgroundRow(), "claude stop " + bgAgentID},
		{"interactive", interactiveRow(), "its own terminal"},
	} {
		m := base(t, 80, 24, c.row)
		m.sel.Toggle(selKey(c.row))
		got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("K")})
		after := got.(model)

		if w := lines.Width(after.note); w > budget {
			t.Errorf("%s refusal is %d columns wide against a budget of %d, so the footer "+
				"truncates it once anything shares the row: %q", c.name, w, budget, after.note)
		}
		// And through the real path: the fix clause must be on the screen the operator sees.
		screen := after.View()
		if !strings.Contains(screen, c.must) {
			t.Errorf("%s refusal lost its fix clause %q on an 80-column screen:\n%s",
				c.name, c.must, screen)
		}
	}
}
