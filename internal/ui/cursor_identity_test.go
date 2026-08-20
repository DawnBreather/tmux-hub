package ui

import (
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/hide"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// The cursor names a ROW, not a position. Every test here is about the one property an index
// cannot have: the list re-sorts under the operator's hand — §22.6 orders by last activity, so
// it re-sorts on nearly every tick — and the row they are looking at must still be the row the
// next keystroke acts on.
//
// These are written against the real key handler and the real tick rather than against the
// field, because the field is what changes: an assertion on `m.cursor` would have to be
// rewritten by the same commit it is supposed to guard.

// cursorOnMiddleRow walks the cursor to the middle of three rows with the real `j`, and returns
// the row it landed on.
//
// The MIDDLE is deliberate. A reorder can move a middle row in either direction, so the
// assertion cannot pass because the cursor happened to be pinned against an end — which is
// what a fixture of one or two rows buys you.
func cursorOnMiddleRow(t *testing.T, width int) (model, registry.Pane) {
	t.Helper()
	m := base(t, width, 24,
		agentPane("local", "alpha", "w", "%1", 0, state.Works, "one"),
		agentPane("local", "bravo", "w", "%2", 1, state.Idle, "two"),
		agentPane("nuc", "charlie", "w", "%3", 2, state.Quiet, "three"),
	)
	if n := len(m.rowsForScreen()); n != 3 {
		t.Fatalf("the fixture draws %d rows, want 3", n)
	}
	m = key(t, m, "j")
	held, ok := m.cursorRow()
	if !ok {
		t.Fatal("no row under the cursor after j")
	}
	if want := m.rowsForScreen()[1].PaneID; held.PaneID != want {
		t.Fatalf("j left the cursor on %q, want the middle row %q", held.PaneID, want)
	}
	return m, held
}

// reorderAround returns the fleet re-sorted so that held is NO LONGER where it was, by making
// the last row the most urgent one — which is what a real tick does when an agent starts asking.
//
// It goes through registry.SortByAttention, the production comparator, and it FAILS the test if
// the order did not actually change: a reorder fixture that reorders nothing makes every
// assertion below pass for free.
func reorderAround(t *testing.T, m model, held registry.Pane) []registry.Pane {
	t.Helper()
	rows := m.rowsForScreen()
	next := make([]registry.Pane, len(rows))
	copy(next, rows)
	last := next[len(next)-1].PaneID
	for i := range next {
		if next[i].PaneID == last {
			next[i].ClassifiedState = state.Needs
		}
	}
	registry.SortByAttention(next)
	if next[1].PaneID == held.PaneID {
		t.Fatalf("the reorder left %q at index 1, so nothing under the cursor moved", held.PaneID)
	}
	return next
}

// The class, stated as a test: a tick re-sorts and the cursor keeps its row.
//
// With the cursor as an index this fails by naming whichever row the sort moved into position
// 1 — and every key reads the cursor through cursorRow, so `a` attaches to it, `x` hides it and
// `K` offers it for killing.
func TestTheCursorKeepsItsRowWhenATickReordersTheList(t *testing.T) {
	m, held := cursorOnMiddleRow(t, 80)
	next := reorderAround(t, m, held)

	got, _ := m.Update(tickMsg{hosts: m.hosts, panes: next})
	after := got.(model)

	row, ok := after.cursorRow()
	if !ok {
		t.Fatal("no row under the cursor after the tick")
	}
	if row.PaneID != held.PaneID {
		t.Errorf("the cursor moved from %q to %q across a reorder — the operator's next "+
			"keystroke acts on a row they were not looking at", held.PaneID, row.PaneID)
	}
}

// And through a key that DOES something. cursorRow being right is necessary; what matters is
// that the act lands on the row the operator saw, so this one asserts on which pane got hidden.
func TestXHidesTheRowTheOperatorSawAcrossAReorder(t *testing.T) {
	m, held := cursorOnMiddleRow(t, 80)
	next := reorderAround(t, m, held)
	moved := next[1] // the row an index-based cursor would have hidden instead

	got, _ := m.Update(tickMsg{hosts: m.hosts, panes: next})
	after := key(t, got.(model), "x")

	if !after.hidden.Marked(hide.KeyOf(held)) {
		t.Errorf("x did not hide %q, the row under the cursor", held.PaneID)
	}
	if after.hidden.Marked(hide.KeyOf(moved)) {
		t.Errorf("x hid %q — the row the SORT moved under the cursor's old index, "+
			"not the row the operator was looking at", moved.PaneID)
	}
}

// The screen has to agree, and this is the only assertion here on the string View() returns.
// At 80 columns the inbox is the inline shape, so the pane id is on the row and the cursor's
// own marker `>` identifies the surface — no other surface draws it, so exactly one line
// carries it and that line must name the held row.
func TestTheCursorMarkerOnScreenFollowsTheRowAcrossAReorder(t *testing.T) {
	m, held := cursorOnMiddleRow(t, 80)
	next := reorderAround(t, m, held)

	got, _ := m.Update(tickMsg{hosts: m.hosts, panes: next})
	screen := got.(model).View()

	var pointed []string
	for _, ln := range strings.Split(screen, "\n") {
		if len(ln) > 2 && strings.Contains(ln[:3], ">") {
			pointed = append(pointed, ln)
		}
	}
	if len(pointed) != 1 {
		t.Fatalf("the cursor marker is on %d rows, want exactly 1:\n%s", len(pointed), screen)
	}
	// By whatever the ROW carries, which is no longer always the pane id: rowPaneID draws it only
	// where a session puts more than one row up, and this fixture is one pane per session.
	needle := rowNeedle(held, m.panes)
	if !strings.Contains(pointed[0], needle) {
		t.Errorf("the cursor marker is on %q, which does not name the held row %q:\n%s",
			strings.TrimRight(pointed[0], " "), needle, screen)
	}
}

// The other half of the property, and the one an identity-only cursor gets wrong: when the row
// the cursor names VANISHES, the cursor stays where it was on screen rather than jumping home.
// Hiding a row and finding the cursor back at the top is how a `x`-`x`-`x` pass loses its place.
func TestWhenTheRowUnderTheCursorVanishesTheCursorTakesItsPlace(t *testing.T) {
	m, held := cursorOnMiddleRow(t, 80)

	rows := m.rowsForScreen()
	next := []registry.Pane{rows[0], rows[2]} // the middle row is gone
	successor := rows[2].PaneID

	got, _ := m.Update(tickMsg{hosts: m.hosts, panes: next})
	after := got.(model)

	row, ok := after.cursorRow()
	if !ok {
		t.Fatal("no row under the cursor after the row it named went away")
	}
	if row.PaneID != successor {
		t.Errorf("the cursor is on %q after %q vanished, want %q — the row that took its "+
			"place", row.PaneID, held.PaneID, successor)
	}
}

// The LAST row vanishing is the case that used to crash before clampCursor existed, so it keeps
// its own test: there is no successor, and the cursor must land on the new last row.
func TestWhenTheLastRowVanishesTheCursorLandsOnTheNewLastRow(t *testing.T) {
	m, _ := cursorOnMiddleRow(t, 80)
	m = key(t, m, "j") // onto the last of three

	rows := m.rowsForScreen()
	if last, _ := m.cursorRow(); last.PaneID != rows[2].PaneID {
		t.Fatalf("the cursor is on %q, want the last row %q", last.PaneID, rows[2].PaneID)
	}
	next := []registry.Pane{rows[0], rows[1]}

	got, _ := m.Update(tickMsg{hosts: m.hosts, panes: next})
	after := got.(model)

	row, ok := after.cursorRow()
	if !ok {
		t.Fatal("no row under the cursor after the last row went away")
	}
	if row.PaneID != next[1].PaneID {
		t.Errorf("the cursor is on %q, want the new last row %q", row.PaneID, next[1].PaneID)
	}
}

// j and k stop at the ends. Trivial, and it is the behaviour the bounds checks around the old
// `m.cursor++` provided — a rewrite that moves the clamping somewhere else needs to be told so.
func TestJAndKStopAtTheEndsOfTheList(t *testing.T) {
	m, _ := cursorOnMiddleRow(t, 80)
	rows := m.rowsForScreen()

	down := m
	for i := 0; i < 5; i++ {
		down = key(t, down, "j")
	}
	if row, _ := down.cursorRow(); row.PaneID != rows[2].PaneID {
		t.Errorf("j past the bottom left the cursor on %q, want the last row %q",
			row.PaneID, rows[2].PaneID)
	}

	up := m
	for i := 0; i < 5; i++ {
		up = key(t, up, "k")
	}
	if row, _ := up.cursorRow(); row.PaneID != rows[0].PaneID {
		t.Errorf("k past the top left the cursor on %q, want the first row %q",
			row.PaneID, rows[0].PaneID)
	}
}

// The agents poll assigns the pane list too (§22.1), and it is a SECOND writer — the tmux tick
// is not the only one. A cursor that only survives the tick would still land on a stranger the
// moment `claude agents` came back with the rows in another order.
func TestTheCursorKeepsItsRowWhenTheAgentsPollReordersTheList(t *testing.T) {
	m, held := cursorOnMiddleRow(t, 80)
	next := reorderAround(t, m, held)

	got, _ := m.Update(agentsMsg{hosts: m.hosts, panes: next})
	after := got.(model)

	row, ok := after.cursorRow()
	if !ok {
		t.Fatal("no row under the cursor after the agents poll")
	}
	if row.PaneID != held.PaneID {
		t.Errorf("the cursor moved from %q to %q across an agents poll", held.PaneID, row.PaneID)
	}
}
