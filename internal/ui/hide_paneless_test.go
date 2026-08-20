package ui

import (
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/hide"
	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// `x` on a row with no pane is REFUSED, and the reason is LIFETIME: the mark would carry nothing
// that expires, so it outlives the session it was taken against.
//
// It first shipped with a different reason — that the mark would land on a DIFFERENT row — which was
// true then. A review found the guard covered only the agent SUBJECT, so `x` on the colliding PANE
// still hid the row beside it, and the answer was structural rather than a second guard: `Kind` is in
// `hide.Key` now (v3), the two keys cannot be equal, and `internal/hide/kind_test.go` owns that
// property. What is left here is the policy, and the sentence says the policy's reason.
//
// Measured before the key carried its kind:
//
//   - two background sessions with one name on one host produce the identical key
//     `{Host:nuc Session:deploy-audit WindowIndex:0 PaneIndex:0 Start:}`;
//   - two NAMELESS agent rows on a host also share one key, so on a version that reports no name
//     every pane-less row on that host collapses onto one;
//   - a REAL tmux pane at window 0, index 0 of a session with that name shares the agent row's
//     key — so the collision crosses the Kind boundary in both directions.
//
// §22.5 took this decision in writing and no code implemented it, while known-issues' N7 and
// §22.9's decision 4 both cite the refusal as an existing constraint: a ruling that denies a
// `failed` row its dismissal rested on a guard that did not exist.

// paneAt is a real tmux pane whose hide key COLLIDES with a pane-less row named sess: window 0,
// index 0, and no start command, which is what an agent row's key degenerates to.
//
// It is built from the key function rather than from intuition, and the test below asserts the
// collision instead of assuming it — a fixture for "these two collide" that does not collide would
// make every assertion here pass for free.
func paneAt(host, sess string) registry.Pane {
	return registry.Pane{
		Kind: registry.KindPane, Host: host, Session: sess,
		Window: "0", WindowIndex: 0, Index: 0, PaneID: "%0",
		Command: "claude", ClassifiedState: state.Works,
	}
}

func panelessRow(host, sess, id string, st state.State) registry.Pane {
	return registry.Pane{
		Kind: registry.KindAgent, Command: "background", Host: host, Session: sess,
		AgentID: id, SessionID: id + "-0000-0000-0000-000000000000",
		PaneID: "agent:" + id + "@1a2b3c4d", ClassifiedState: st,
	}
}

// The whole point, stated as the harm: the row that was never marked must still be visible.
func TestXOnAPaneLessRowRefusesAndLeavesTheCollidingPaneAlone(t *testing.T) {
	agent := panelessRow("nuc", "deploy-audit", "84dc5a2e", state.Error)
	pane := paneAt("nuc", "deploy-audit")

	// These two USED to share a key — the shape that made one press hide both. They no longer
	// can, and that is asserted here so this test says which property is whose: the separation is
	// the KEY's (internal/hide/kind_test.go), and the refusal below is the POLICY's.
	if hide.KeyOf(agent) == hide.KeyOf(pane) {
		t.Fatalf("the agent row and the pane still share a key:\n  %+v", hide.KeyOf(agent))
	}

	m := base(t, 100, 24, agent, pane)
	m.sel.Toggle(selKey(agent))
	after := key(t, m, "x")

	if after.hidden.Marked(hide.KeyOf(agent)) {
		t.Error("x marked a pane-less row, so the tmux pane sharing its key is hidden too")
	}
	// The row beside it has to be on the screen. Before the guard, one press took a two-row screen
	// to zero rows — the key made them one mark, and the guard refuses the press regardless.
	if !strings.Contains(after.View(), "%0") {
		t.Errorf("the colliding pane left the screen after x on the agent row:\n%s", after.View())
	}
	if n := len(after.visibleRows()); n != 2 {
		t.Errorf("%d rows visible after a refused x, want both", n)
	}
	for _, want := range []string{"nothing hidden", "deploy-audit", "never expire"} {
		if !strings.Contains(after.note, want) {
			t.Errorf("the refusal does not name %q: %q", want, after.note)
		}
	}
}

// The cursor path, not just the selection: hideSubject acts on the cursor when nothing is selected,
// so a guard placed only in the selection branch would leave `x` live on the commonest gesture.
func TestXOnAPaneLessRowUnderTheCursorRefuses(t *testing.T) {
	agent := panelessRow("local", "envoy-hotfix", "1ff133f7", state.Done)
	m := base(t, 100, 24, agent)
	if m.sel.Len() != 0 {
		t.Fatal("this test is about the cursor path and something is selected")
	}
	row, ok := m.cursorRow()
	if !ok || row.PaneID != agent.PaneID {
		t.Fatalf("the cursor is not on the agent row: %+v", row)
	}

	after := key(t, m, "x")

	if after.hidden.Marked(hide.KeyOf(agent)) {
		t.Error("x under the cursor marked a pane-less row")
	}
	if !strings.Contains(after.note, "nothing hidden") {
		t.Errorf("the cursor path did not refuse: %q", after.note)
	}
}

// A MIXED selection refuses WHOLE, the same rule `K` follows: hiding the pane rows and skipping the
// rest is a partial action nobody asked for, and the operator who read only the tail would believe
// the panes were hidden.
func TestXRefusesAMixedSelectionWholeAndHidesNothing(t *testing.T) {
	agent := panelessRow("nuc", "deploy-audit", "84dc5a2e", state.Error)
	pane := registry.Pane{
		Kind: registry.KindPane, Host: "local", Session: "api", Window: "fix",
		WindowIndex: 1, Index: 2, PaneID: "%7", Command: "claude",
		StartCommand: `"claude"`, ClassifiedState: state.Works,
	}
	m := base(t, 100, 24, agent, pane)
	m.sel.Toggle(selKey(agent))
	m.sel.Toggle(selKey(pane))

	after := key(t, m, "x")

	if after.hidden.Marked(hide.KeyOf(pane)) {
		t.Error("a mixed x hid the pane row; it must refuse whole")
	}
	if after.hidden.Marked(hide.KeyOf(agent)) {
		t.Error("a mixed x hid the agent row")
	}
	if !strings.HasPrefix(after.note, "nothing hidden") {
		t.Errorf("a mixed refusal must lead with the fact that nothing happened: %q", after.note)
	}
	if !strings.Contains(after.note, "deploy-audit") {
		t.Errorf("the refusal does not name the row that cannot be hidden: %q", after.note)
	}
}

// The guard must not cost the feature: a pane row still hides, selected or under the cursor.
func TestXStillHidesAPaneRow(t *testing.T) {
	pane := registry.Pane{
		Kind: registry.KindPane, Host: "local", Session: "logs", Window: "tail",
		WindowIndex: 3, Index: 1, PaneID: "%4", Command: "tail",
		StartCommand: `"tail"`, ClassifiedState: state.Works,
	}
	other := registry.Pane{
		Kind: registry.KindPane, Host: "local", Session: "api", Window: "fix",
		WindowIndex: 1, Index: 0, PaneID: "%1", Command: "claude",
		StartCommand: `"claude"`, ClassifiedState: state.Works,
	}
	m := base(t, 100, 24, pane, other)
	m.sel.Toggle(selKey(pane))
	after := key(t, m, "x")

	if !after.hidden.Marked(hide.KeyOf(pane)) {
		t.Errorf("x no longer hides an ordinary pane row; note = %q", after.note)
	}
	if after.hidden.Marked(hide.KeyOf(other)) {
		t.Error("x hid a row that was not selected")
	}
}

// 80 columns, the size §16 commits to, and the assertion is on the RENDERED screen: the note is one
// claimant of the footer's Fit list, which reserves 3 columns for its ` +N` marker once anything
// shares the row, so a note measuring 80 still loses its tail on an 80-column terminal.
func TestTheHideRefusalSurvivesTheWidthTheProjectCommitsTo(t *testing.T) {
	const budget = 80 - 3
	agent := panelessRow("nuc", "deploy-audit", "84dc5a2e", state.Error)
	m := base(t, 80, 24, agent)
	m.sel.Toggle(selKey(agent))
	after := key(t, m, "x")

	if w := lines.Width(after.note); w > budget {
		t.Errorf("the refusal is %d columns against a budget of %d: %q", w, budget, after.note)
	}
	if !strings.Contains(after.View(), "never expire") {
		t.Errorf("the refusal lost its reason on an 80-column screen:\n%s", after.View())
	}
}

// The nameless row, which had no test anywhere: a mutant reducing the fallback to `name :=
// p.Session` compiled and left all 18 packages green.
//
// A listing that reports no name is measured, not hypothetical, and a refusal reading
// `nothing hidden —  has no pane` names nothing at all. The short id is what `claude logs` takes, so
// it is the second choice; with neither, the sentence still has to refer to something.
func TestTheRefusalNamesANamelessRow(t *testing.T) {
	for _, c := range []struct {
		name string
		row  registry.Pane
		want string
	}{
		{"no session name, a short id", registry.Pane{
			Kind: registry.KindAgent, Command: "background", Host: "nuc", AgentID: "84dc5a2e",
			PaneID: "agent:84dc5a2e@1a2b3c4d", ClassifiedState: state.Error}, "84dc5a2e"},
		{"neither", registry.Pane{
			Kind: registry.KindAgent, Command: "interactive", Host: "nuc",
			PaneID: "agent:9f9f9f9f@5e6f7a8b", ClassifiedState: state.Idle}, "this row"},
	} {
		m := base(t, 100, 24, c.row)
		m.sel.Toggle(selKey(c.row))
		after := key(t, m, "x")

		if !strings.Contains(after.note, c.want) {
			t.Errorf("%s: the refusal does not name %q: %q", c.name, c.want, after.note)
		}
		if strings.Contains(after.note, "—  has") {
			t.Errorf("%s: the refusal has an empty name in it: %q", c.name, after.note)
		}
		if after.hidden.Marked(hide.KeyOf(c.row)) {
			t.Errorf("%s: the row was hidden", c.name)
		}
	}
}
