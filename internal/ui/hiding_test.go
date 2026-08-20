package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/hide"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// Test helpers

func pane(host, window, cmd string, idx int, start string, st state.State) registry.Pane {
	return registry.Pane{
		Host:            host,
		Session:         "sess1",
		Window:          window,
		Index:           idx,
		PaneID:          fmt.Sprintf("%%%d", idx),
		Command:         cmd,
		StartCommand:    start,
		ClassifiedState: st,
	}
}

func modelWith(t *testing.T, panes ...registry.Pane) model {
	t.Helper()
	s, err := hide.Open(filepath.Join(t.TempDir(), "hidden.json"))
	if err != nil {
		t.Fatalf("hide.Open: %v", err)
	}
	return model{
		panes:  panes,
		hidden: s,
		width:  100,
		height: 20,
		ctx:    context.Background(),
	}
}

func hidePane(t *testing.T, m *model, idx int) {
	t.Helper()
	if idx >= len(m.panes) {
		t.Fatalf("hidePane: index %d out of range for %d panes", idx, len(m.panes))
	}
	if err := m.hidden.Toggle(m.panes[idx]); err != nil {
		t.Fatalf("Toggle: %v", err)
	}
}

func withState(m model, idx int, st state.State) model {
	if idx < len(m.panes) {
		m.panes[idx].ClassifiedState = st
	}
	return m
}

func TestAHiddenPaneIsNotInTheView(t *testing.T) {
	m := modelWith(t, pane("local", "logs", "tail", 0, `"tail -f app.log"`, state.Works),
		pane("local", "api", "claude", 1, `"claude"`, state.Works))
	hidePane(t, &m, 0)

	out := m.View()
	if strings.Contains(out, "tail") {
		t.Fatalf("the hidden pane is still drawn:\n%s", out)
	}
	if !strings.Contains(out, "claude") {
		t.Fatalf("the visible pane vanished:\n%s", out)
	}
}

func TestTheFooterCountsHiddenPanesAndTheBlockedOnesAmongThem(t *testing.T) {
	// A count with no breakdown reads as "nothing to see". The breakdown is
	// what tells the user a hidden pane is waiting for them.
	m := modelWith(t,
		pane("local", "logs", "tail", 0, `"tail"`, state.Works),
		pane("local", "api", "claude", 1, `"claude"`, state.Needs),
		pane("local", "build", "make", 2, `"make"`, state.Works))
	hidePane(t, &m, 0)
	hidePane(t, &m, 1)

	out := m.View()
	// The literal the footer prints, with the counts in place, so a reworded footer that
	// still contains the words cannot pass.
	if want := "2 hidden · 1 of them waiting for input"; !strings.Contains(out, want) {
		t.Errorf("footer does not read %q:\n%s", want, out)
	}
}

func TestAResurfacedPaneIsDrawnAndMarkedAsResurfaced(t *testing.T) {
	m := modelWith(t, pane("local", "logs", "tail", 0, `"tail -f app.log"`, state.Works))
	hidePane(t, &m, 0)
	m = withState(m, 0, state.Needs)

	out := m.View()
	if !strings.Contains(out, "tail") {
		t.Fatalf("a blocked hidden pane must be drawn:\n%s", out)
	}
	if !strings.Contains(out, resurfacedMark) {
		t.Fatalf("a resurfaced row must say why it is back:\n%s", out)
	}
}

func TestShowHiddenRevealsThemWithoutClearingTheMarks(t *testing.T) {
	m := modelWith(t, pane("local", "logs", "tail", 0, `"tail"`, state.Works))
	hidePane(t, &m, 0)

	// Hidden by default
	if strings.Contains(m.View(), "tail") {
		t.Fatal("pane visible when it should be hidden")
	}

	// Show all
	m.showHidden = true
	out := m.View()
	if !strings.Contains(out, "tail") {
		t.Fatal("showHidden did not reveal the pane")
	}

	// Hide again
	m.showHidden = false
	if strings.Contains(m.View(), "tail") {
		t.Fatal("pane still visible after showHidden=false")
	}
}

func TestXHidesThePaneUnderTheCursor(t *testing.T) {
	m := modelWith(t,
		pane("local", "logs", "tail", 0, `"tail -f app.log"`, state.Works),
		pane("local", "api", "claude", 1, `"claude"`, state.Works))
	m = m.cursorTo(0)

	// Before: both visible
	out := m.View()
	if !strings.Contains(out, "tail") {
		t.Fatal("tail should be visible before hiding")
	}
	if !strings.Contains(out, "claude") {
		t.Fatal("claude should be visible")
	}

	// Press x
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = got.(model)

	// After: tail hidden, claude visible
	out = m.View()
	if strings.Contains(out, "tail") {
		t.Errorf("tail should be hidden after pressing x:\n%s", out)
	}
	if !strings.Contains(out, "claude") {
		t.Errorf("claude should still be visible:\n%s", out)
	}
}

func TestXHidesEverySelectedPaneWhenThereIsASelection(t *testing.T) {
	// The selection is the user's stated subject. Hiding one row while three are
	// selected would be the same "which did I mean" ambiguity the send path
	// already resolves this way.
	m := modelWith(t,
		pane("local", "logs", "tail", 0, `"tail"`, state.Works),
		pane("local", "api", "claude", 1, `"claude"`, state.Works),
		pane("local", "build", "make", 2, `"make"`, state.Works))

	// Select panes 0 and 2
	m.sel.Toggle(selKey(m.panes[0]))
	m.sel.Toggle(selKey(m.panes[2]))
	m = m.cursorTo(1) // Cursor is on pane 1, but it's not selected

	// Press x
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = got.(model)

	// After: the two SELECTED panes are hidden, the cursor pane is still visible
	out := m.View()
	if strings.Contains(out, "tail") {
		t.Errorf("selected tail should be hidden:\n%s", out)
	}
	if !strings.Contains(out, "claude") {
		t.Errorf("unselected claude should still be visible:\n%s", out)
	}
	if strings.Contains(out, "make") {
		t.Errorf("selected make should be hidden:\n%s", out)
	}
}

func TestHidingMovesTheCursorToAStillVisibleRow(t *testing.T) {
	// Hiding the last row must not leave the cursor past the end — the crash
	// this project already fixed once in the viewport.
	m := modelWith(t,
		pane("local", "logs", "tail", 0, `"tail"`, state.Works),
		pane("local", "api", "claude", 1, `"claude"`, state.Works))

	// Cursor at the last row
	m = m.cursorTo(1)

	// Press x to hide the pane under the cursor
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = got.(model)

	// The cursor must land inside the shorter list, and on the row that is left — asserting
	// the ROW rather than the index 0 says what the operator sees, and it is the same
	// sentence whichever way the cursor is stored.
	visible := m.visibleRows()
	if len(visible) != 1 {
		t.Fatalf("x left %d visible rows, want 1", len(visible))
	}
	if i := m.cursorIndex(); i >= len(visible) {
		t.Errorf("cursor %d is past the end of visible rows (len=%d)", i, len(visible))
	}
	row, ok := m.cursorRow()
	if !ok {
		t.Fatal("no row under the cursor after hiding the last one")
	}
	if row.PaneID != visible[0].PaneID {
		t.Errorf("the cursor is on %q, want the one row still visible, %q",
			row.PaneID, visible[0].PaneID)
	}
}

func TestXOnAnEmptyDashboardIsANoOp(t *testing.T) {
	m := modelWith(t) // No panes
	m = m.cursorTo(0)

	// Press x
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = got.(model)

	// Should not crash and note should indicate the operation was benign
	if m.note != "" && !strings.Contains(strings.ToLower(m.note), "no") {
		t.Errorf("expected a note about no panes or no note at all, got: %q", m.note)
	}
}

func TestHidingASelectedPaneRemovesItFromTheSelection(t *testing.T) {
	// The core see-before-send rule: hiding a selected pane must drop it from
	// the selection, or pressing `i` opens compose for panes the user cannot see.
	m := modelWith(t,
		pane("local", "logs", "tail", 0, `"tail"`, state.Works),
		pane("local", "api", "claude", 1, `"claude"`, state.Works))

	// Select the first pane
	m.sel.Toggle(selKey(m.panes[0]))
	if m.sel.Len() != 1 {
		t.Fatalf("setup: selection should have 1 pane, got %d", m.sel.Len())
	}

	// Hide it with x
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = got.(model)

	// The pane should be hidden
	out := m.View()
	if strings.Contains(out, "tail") {
		t.Errorf("tail should be hidden:\n%s", out)
	}

	// Now press i to try to compose
	got, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	m = got.(model)

	// Compose should NOT open, and there should be a note about selecting a pane first
	if m.mode == modeCompose {
		t.Errorf("compose opened with no visible selected panes")
	}
	if !strings.Contains(m.View(), "select a pane with space first") {
		t.Errorf("expected note about selecting a pane, got view:\n%s", m.View())
	}
}

// C1 tests: a pane that becomes hidden for any reason other than x stays selected

func TestAResurfacedPaneThatStopsWaitingBecomesInvisibleAndIsDroppedFromSelection(t *testing.T) {
	// C1 route 1: mark a pane hidden, put it in Needs (so it surfaces), select
	// it, move it back to Works (so it disappears), run a tick, then press i.
	// The tick must drop it from the selection, so compose does NOT open.
	m := modelWith(t, pane("local", "api", "claude", 0, `"claude"`, state.Works))
	hidePane(t, &m, 0)

	// Pane enters Needs state, which resurfaces it
	m = withState(m, 0, state.Needs)
	out := m.View()
	if !strings.Contains(out, "claude") {
		t.Fatalf("resurfaced pane should be visible:\n%s", out)
	}

	// User selects it (legitimate: it's on screen)
	m.sel.Toggle(selKey(m.panes[0]))
	if m.sel.Len() != 1 {
		t.Fatalf("setup: selection should have 1 pane, got %d", m.sel.Len())
	}

	// Agent answers, pane returns to Works, which makes it hidden again
	m = withState(m, 0, state.Works)

	// Run a tick to trigger the prune
	m.hosts = nil
	got, _ := m.Update(tickMsg{hosts: nil, panes: m.panes})
	m = got.(model)

	// The pane should now be invisible
	out = m.View()
	if strings.Contains(out, "claude") {
		t.Errorf("hidden Works pane should not be visible:\n%s", out)
	}

	// Press i to try to compose
	got, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	m = got.(model)

	// Compose should NOT open
	if m.mode == modeCompose {
		t.Errorf("compose opened for an off-screen pane")
	}
	if !strings.Contains(m.View(), "select a pane with space first") {
		t.Errorf("expected note about selecting a pane, got view:\n%s", m.View())
	}
}

func TestPressingXToRevealThenSelectingThenXAgainLeavesNothingSelected(t *testing.T) {
	// C1 route 2: X (reveal), space on a hidden row, X (re-hide). The second X
	// should drop the pane from selection since it's now invisible.
	m := modelWith(t, pane("local", "api", "claude", 0, `"claude"`, state.Works))
	hidePane(t, &m, 0)

	// Before: pane is hidden
	if strings.Contains(m.View(), "claude") {
		t.Fatal("pane should be hidden initially")
	}

	// Press X to reveal
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	m = got.(model)
	if !m.showHidden {
		t.Fatal("X should set showHidden=true")
	}
	if !strings.Contains(m.View(), "claude") {
		t.Fatal("pane should be visible after X")
	}

	// Press space to select it
	got, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = got.(model)
	if m.sel.Len() != 1 {
		t.Fatalf("space should select the pane, got sel.Len()=%d", m.sel.Len())
	}

	// Press X again to re-hide
	got, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	m = got.(model)
	if m.showHidden {
		t.Fatal("X should toggle showHidden back to false")
	}

	// Run a tick to trigger the prune
	got, _ = m.Update(tickMsg{hosts: nil, panes: m.panes})
	m = got.(model)

	// The pane should be invisible
	if strings.Contains(m.View(), "claude") {
		t.Errorf("pane should be hidden again:\n%s", m.View())
	}

	// Press i to try to compose
	got, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	m = got.(model)

	// Compose should NOT open
	if m.mode == modeCompose {
		t.Errorf("compose opened for an off-screen pane")
	}
}

func TestWithShowHiddenTrueAHiddenPaneIsSelectableAndSendsDoNotBlock(t *testing.T) {
	// C1 must not over-fire: while showHidden is true, hidden panes ARE on
	// screen and ARE legitimately selectable, so the send must proceed.
	m := modelWith(t, pane("local", "api", "claude", 0, `"claude"`, state.Works))
	hidePane(t, &m, 0)

	// Set showHidden to reveal the pane
	m.showHidden = true
	if !strings.Contains(m.View(), "claude") {
		t.Fatal("pane should be visible with showHidden=true")
	}

	// Select it
	m.sel.Toggle(selKey(m.panes[0]))
	if m.sel.Len() != 1 {
		t.Fatalf("setup: selection should have 1 pane, got %d", m.sel.Len())
	}

	// Run a tick (which runs the prune)
	got, _ := m.Update(tickMsg{hosts: nil, panes: m.panes})
	m = got.(model)

	// The pane should still be selected
	if m.sel.Len() != 1 {
		t.Errorf("pane should remain selected when showHidden=true, got sel.Len()=%d", m.sel.Len())
	}

	// Press i to compose
	got, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	m = got.(model)

	// Compose SHOULD open (or at least not refuse with "select a pane first")
	// The exact mode depends on whether targets() returns anything, but it
	// must not refuse on the grounds of no selection.
	if strings.Contains(m.View(), "select a pane with space first") {
		t.Errorf("should not refuse to send with a visible selected pane, got view:\n%s", m.View())
	}
}
