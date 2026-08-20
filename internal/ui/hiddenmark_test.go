package ui

import (
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/state"
)

// `X` exists to answer ONE question — "what did I hide?" — and a screen that shows the hidden rows
// without saying which ones they are does not answer it.
//
// Measured before the fix, at 80×24 with one of two panes marked and `X` pressed: the two rows
// reduced to the same string but for their pane id, and the footer carried the same line it carries
// with nothing hidden at all, because `hiddenStats` returned 0,0 while the toggle was on. So the
// operator who pressed `X` to review their marks could not tell a hidden row from a visible one, and
// the operator who left it on had no way to know their screen was unfiltered.
//
// Both halves are asserted here, and both go through `View()` — the ONE producer of these fields —
// rather than through RenderInbox directly, because a marker the renderer can draw and the model
// never passes is this repo's signature defect.

// footerOf is the last line the screen carries: the fleet line, where the counts live.
func footerOf(t *testing.T, screen string) string {
	t.Helper()
	ls := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	for i := len(ls) - 1; i >= 0; i-- {
		if strings.TrimSpace(ls[i]) != "" {
			return ls[i]
		}
	}
	t.Fatalf("the screen has no non-blank line:\n%s", screen)
	return ""
}

// hiddenMarkFixture is two quiet panes with the SECOND one marked hidden, at the size §16 commits to.
func hiddenMarkFixture(t *testing.T) model {
	t.Helper()
	m := base(t, 80, 24,
		pane("local", "kept", "cat", 1, "cat", state.Quiet),
		pane("local", "noisy", "tail", 2, "tail -f x", state.Quiet))
	// Mark by identity, not by index: base() re-sorts the rows.
	for i, p := range m.panes {
		if p.Window == "noisy" {
			hidePane(t, &m, i)
		}
	}
	if len(m.visibleRows()) != 1 {
		t.Fatalf("the fixture must hide exactly one of two rows, %d visible", len(m.visibleRows()))
	}
	return m
}

func TestXMarksWhichRowsAreHidden(t *testing.T) {
	m := hiddenMarkFixture(t)
	m.showHidden = true
	screen := m.View()

	if row := inboxRow(t, screen, "%2"); !strings.Contains(row, hiddenMark) {
		t.Errorf("the hidden row carries no %s, so `X` shows the operator everything and tells them "+
			"nothing: %q", hiddenMark, row)
	}
	if row := inboxRow(t, screen, "%1"); strings.Contains(row, hiddenMark) {
		t.Errorf("a row nobody hid is marked %s, which makes the marker say nothing: %q",
			hiddenMark, row)
	}
}

func TestTheFooterSaysTheScreenIsUnfilteredWhileXIsOn(t *testing.T) {
	m := hiddenMarkFixture(t)
	m.showHidden = true
	foot := footerOf(t, m.View())

	if !strings.Contains(foot, "hidden") {
		t.Errorf("the footer says nothing while the hidden rows are on show, so a screen with the "+
			"toggle left on is indistinguishable from a fleet with nothing hidden: %q", foot)
	}
	// And it must not claim a row is being KEPT OFF the screen while every row is on it — that is
	// the sentence the filtered footer carries, and reusing it here would be a lie in the other
	// direction.
	if strings.Contains(foot, "1 hidden") {
		t.Errorf("the footer claims a row is being kept off the screen while every row is on it: %q",
			foot)
	}
}

func TestTheFilteredFooterStillCountsWhatItIsHiding(t *testing.T) {
	m := hiddenMarkFixture(t)
	if foot := footerOf(t, m.View()); !strings.Contains(foot, "1 hidden") {
		t.Errorf("the filtered footer stopped counting the row it is hiding: %q", foot)
	}
}

// A resurfaced row is marked hidden AND on the screen, so both markers could claim it. It keeps its
// own: `[↑]` says "hidden, and it came back because it is asking", which is strictly more than
// "hidden" — and one marker per row is what makes either readable.
func TestAResurfacedRowKeepsItsOwnMarker(t *testing.T) {
	m := hiddenMarkFixture(t)
	for i, p := range m.panes {
		if p.Window == "noisy" {
			m.panes[i].ClassifiedState = state.Needs
		}
	}
	m.showHidden = true

	row := inboxRow(t, m.View(), "%2")
	if !strings.Contains(row, resurfacedMark) {
		t.Errorf("a waiting hidden row lost %s: %q", resurfacedMark, row)
	}
	if strings.Contains(row, hiddenMark) {
		t.Errorf("a waiting hidden row carries both markers, so neither reads as one thing: %q", row)
	}
}
