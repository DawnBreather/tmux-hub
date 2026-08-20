package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/broadcast"
	"github.com/DawnBreather/tmux-hub/internal/history"
	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/registry"
)

// In compose mode every rune is TEXT. `q` quits in browse mode, and a dashboard
// that exits when someone types "quit the stuck server" into a prompt has lost the
// user's work for a keystroke they did not mean as a command.
func TestQDoesNotQuitInComposeMode(t *testing.T) {
	m := model{mode: modeCompose}
	got, cmd := m.composeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd != nil {
		t.Error("compose mode issued a command for an ordinary rune")
	}
	after := got.(model)
	if text := after.composer.Text(); text != "q" {
		t.Errorf("composer holds %q, want the rune as text", text)
	}
}

// Esc keeps the draft. Losing a half-written prompt is what makes people stop
// trusting an input box.
func TestEscapeKeepsTheDraft(t *testing.T) {
	m := model{mode: modeCompose}
	m.composer.Insert("half a prompt")
	got, _ := m.composeKey(tea.KeyMsg{Type: tea.KeyEsc})
	after := got.(model)
	if after.mode != modeBrowse {
		t.Error("esc did not leave compose")
	}
	if after.composer.Text() != "half a prompt" {
		t.Errorf("the draft was lost: %q", after.composer.Text())
	}
}

// Anything but enter cancels a confirmation: the safe answer to a dialog nobody
// understands is to do nothing.
func TestOnlyEnterConfirms(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("y")},
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune(" ")},
	} {
		m := model{mode: modeConfirm, pending: []broadcast.Reason{broadcast.ReasonMultiple}}
		got, cmd := m.confirmKey(key)
		if cmd != nil {
			t.Errorf("%v sent something", key)
		}
		if got.(model).mode != modeBrowse {
			t.Errorf("%v did not dismiss the dialog", key)
		}
	}
}

// A refused send must not eat the text.
func TestARefusedSendKeepsTheDraft(t *testing.T) {
	m := model{mode: modeBrowse}
	m.composer.Insert("please retry this")
	got, _ := m.Update(sentMsg{results: []broadcast.Result{
		{Outcome: broadcast.Refused, Reason: "the pane is gone"},
	}})
	after := got.(model)
	if text := after.composer.Text(); text != "please retry this" {
		t.Errorf("the draft was cleared after a refusal: %q", text)
	}
}

// A CLEAN send clears the draft — that is the rule, and the whole rule is "clears the DRAFT". A
// re-send from the log sends text the composer never held, so succeeding at one must not delete a
// prompt the operator is still writing.
//
// Both arms in one table, because the guard is one boolean and a test of either arm alone passes
// against a constant.
func TestOnlyASendOfTheDraftClearsTheDraft(t *testing.T) {
	const draft = "a prompt the operator is still writing"
	for _, c := range []struct {
		name        string
		fromHistory bool
		want        string
	}{
		{"the operator's own send", false, ""},
		{"a re-send from the log", true, draft},
	} {
		m := model{mode: modeBrowse, fromHistory: c.fromHistory}
		m.composer.Insert(draft)
		got, _ := m.Update(sentMsg{act: actionSend, results: []broadcast.Result{
			{Outcome: broadcast.Delivered},
		}})
		after := got.(model)
		if text := after.composer.Text(); text != c.want {
			t.Errorf("%s: composer = %q, want %q", c.name, text, c.want)
		}
	}
}

// A selects exactly the panes visible in the inbox viewport, not all panes and
// not just the cursor. The viewport is a scrolled window; pressing A with 50 panes
// and a 24-row terminal must select the ~10 rows on screen, not all 50.
func TestSelectAllSelectsTheViewport(t *testing.T) {
	// Create 50 panes so scrolling is required.
	var panes []registry.Pane
	for i := 0; i < 50; i++ {
		panes = append(panes, registry.Pane{
			Host:   "local",
			PaneID: fmt.Sprintf("%%%d", i),
		})
	}

	// Position the cursor at pane 30, which forces scrolling past the top.
	m := model{
		panes:  panes,
		width:  80,
		height: 24,
	}
	// Through the accessor, which is the only writer: it stamps the row's IDENTITY, so the
	// window this test is about is the one `A` sees rather than a number nothing resolves.
	m = m.cursorTo(30)

	// Press A
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	after := got.(model)

	// The expectation comes from the SCREEN, not from the viewport arithmetic. A window written out
	// as `panes[9:31]` had to be re-derived every time the body's height changed — it was 22 panes
	// until the details band took the bottom of the body, and 14 after — and a number copied from
	// the function under test asserts only that assignment works. What `A` promises is exactly this:
	// the selection is the set of rows the renderer DREW.
	screen := m.View()
	drawn := map[string]bool{}
	for _, p := range panes {
		// By what the ROW draws (rowNeedle), not by the pane id: rowIdentity keeps the id off a row
		// whose label nothing else draws, and this counter then reported ONE row drawn out of the
		// fourteen on screen — which reads as a catastrophic `A` bug and is a stale needle.
		if drawsRow(screen, rowNeedle(p, panes)) {
			drawn[p.PaneID] = true
		}
	}
	if len(drawn) == 0 {
		t.Fatalf("the fixture drew no panes at all:\n%s", screen)
	}
	if after.sel.Len() != len(drawn) {
		t.Errorf("selected %d panes where the screen drew %d", after.sel.Len(), len(drawn))
	}
	for id := range drawn {
		if !after.sel.Has(SelectionKey{Host: "local", PaneID: id}) {
			t.Errorf("pane %s is on screen and was not selected", id)
		}
	}

	// And nothing the screen did NOT draw: the other half of the promise, and the half that matters,
	// because a pane selected off screen is one the operator writes into without looking at it.
	for _, p := range panes {
		if !drawn[p.PaneID] && after.sel.Has(SelectionKey{Host: "local", PaneID: p.PaneID}) {
			t.Errorf("pane %s is scrolled off and was selected anyway", p.PaneID)
		}
	}
}

// A adds the rest of the viewport when some panes are already selected. It must
// not be a no-op in that case.
func TestSelectAllAddsToExistingSelection(t *testing.T) {
	var panes []registry.Pane
	for i := 0; i < 20; i++ {
		panes = append(panes, registry.Pane{
			Host:   "local",
			PaneID: fmt.Sprintf("%%%d", i),
		})
	}

	m := model{
		panes:  panes,
		width:  80,
		height: 24,
	}
	m = m.cursorTo(10)

	// Pre-select two panes in the visible window.
	m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%5"})
	m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%12"})

	// Press A
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	after := got.(model)

	// `A` ADDS: everything on screen, and the two that were already selected stay selected whether
	// the screen still shows them or not. The count is the screen's, not a constant — 20 panes no
	// longer all fit at 80×24 now that the details band holds the bottom of the body.
	screen := m.View()
	onScreen := 0
	for _, p := range panes {
		if strings.Contains(screen, p.PaneID+" ") || strings.HasSuffix(screen, p.PaneID) {
			onScreen++
			if !after.sel.Has(SelectionKey{Host: "local", PaneID: p.PaneID}) {
				t.Errorf("pane %s is on screen and was not selected", p.PaneID)
			}
		}
	}
	if onScreen == 0 {
		t.Fatalf("the fixture drew no panes:\n%s", screen)
	}
	for _, id := range []string{"%5", "%12"} {
		if !after.sel.Has(SelectionKey{Host: "local", PaneID: id}) {
			t.Errorf("%s was selected before `A` and is not selected after it", id)
		}
	}
}

// InboxViewport is the single source of truth for viewport arithmetic. This test
// verifies that VisiblePanes uses it, so a future change to one cannot drift from
// the other. If this test fails, the renderer and select-all disagree on what is
// visible, breaking the rule that A only selects what the user can see.
func TestVisiblePanesUsesSharedViewportArithmetic(t *testing.T) {
	var panes []registry.Pane
	for i := 0; i < 100; i++ {
		panes = append(panes, registry.Pane{
			Host:   "local",
			PaneID: fmt.Sprintf("%%%d", i),
		})
	}

	testCases := []struct {
		name   string
		width  int
		height int
		cursor int
	}{
		{"small terminal", 80, 10, 5},
		{"normal terminal", 120, 24, 30},
		{"wide terminal", 200, 40, 50},
		{"cursor at top", 80, 24, 0},
		{"cursor at bottom", 80, 24, 99},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Get the viewport from the shared function.
			first, count := InboxViewport(Frame{Panes: panes, Width: tc.width,
				Height: tc.height, Cursor: tc.cursor})

			// Get the panes from VisiblePanes.
			visible := VisiblePanes(Frame{Panes: panes, Width: tc.width,
				Height: tc.height, Cursor: tc.cursor})

			// They must agree.
			if len(visible) != count {
				t.Errorf("VisiblePanes returned %d panes, InboxViewport says %d",
					len(visible), count)
			}

			// Verify the slice matches the window.
			if count > 0 {
				if visible[0].PaneID != panes[first].PaneID {
					t.Errorf("VisiblePanes starts at wrong index: got %s, want %s",
						visible[0].PaneID, panes[first].PaneID)
				}
				if visible[len(visible)-1].PaneID != panes[first+count-1].PaneID {
					t.Errorf("VisiblePanes ends at wrong index: got %s, want %s",
						visible[len(visible)-1].PaneID, panes[first+count-1].PaneID)
				}
			}
		})
	}
}

// A must report when the viewport is empty rather than being silent.
func TestSelectAllReportsEmptyViewport(t *testing.T) {
	// Terminal too small to show any panes (height 2 gives bodyH = 0).
	m := model{
		panes:  []registry.Pane{{Host: "local", PaneID: "%0"}},
		width:  80,
		height: 2, // bodyH = 0, impossible to render
	}

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	after := got.(model)

	if after.note == "" {
		t.Error("A set no note when viewport was empty — user cannot tell it from broken")
	}
	if after.sel.Len() != 0 {
		t.Error("A selected something despite empty viewport")
	}
}

// A history re-send must ALWAYS confirm, because the one thing that separates it
// from an ordinary send is that the user did not just type the text.
func TestHistoryResendAlwaysConfirms(t *testing.T) {
	m := model{
		mode:    modeHistory,
		history: []history.Entry{{Text: "an old prompt", Outcome: "delivered"}},
		panes:   []registry.Pane{{Host: "local", PaneID: "%1"}},
	}
	m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%1"})

	got, _ := m.historyKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	after := got.(model)
	if after.mode != modeConfirm {
		t.Fatalf("mode = %v, want modeConfirm — a re-send must ask", after.mode)
	}
	if after.pendingText != "an old prompt" {
		t.Errorf("the dialog does not carry the entry's text: %q", after.pendingText)
	}
	// And it did NOT go through the composer, which is what makes the re-send non-destructive
	// before it is answered.
	if got := after.composer.Text(); got != "" {
		t.Errorf("`r` wrote into the composer: %q", got)
	}
	if !after.fromHistory {
		t.Error("fromHistory was not set, so the confirmation rule cannot see it")
	}
}

// With nothing selected, a re-send does nothing and says why. The entry's own
// recorded targets are deliberately not used.
func TestHistoryResendNeedsACurrentSelection(t *testing.T) {
	m := model{mode: modeHistory,
		history: []history.Entry{{Text: "x", Host: "nuc", PaneID: "%9"}}}
	got, cmd := m.historyKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if cmd != nil {
		t.Error("a re-send with no selection sent something")
	}
	if after := got.(model); after.note == "" {
		t.Error("it failed silently instead of saying what to do")
	}
}

func TestRenderHistoryShowsTheOutcomeWord(t *testing.T) {
	es := []history.Entry{
		{At: time.Unix(1786487832, 0), Host: "nuc", PaneID: "%3",
			Text: "first\nsecond", Outcome: "delivered"},
		{At: time.Unix(1786487833, 0), Host: "eu", PaneID: "%1",
			Text: "other", Outcome: "refused"},
	}
	out := RenderHistory(es, 80, 6, 0)
	if !strings.Contains(out[0], "✓") || !strings.Contains(out[1], "✗") {
		t.Errorf("outcomes are not distinguishable:\n%s", strings.Join(out, "\n"))
	}
	if strings.Contains(out[0], "\n") {
		t.Error("a multi-line prompt broke the row")
	}
	for _, l := range out {
		if lines.Width(l) > 80 {
			t.Errorf("row exceeds the width: %q", l)
		}
	}
}

// Compose mode must show the text the user is typing. An invisible input box is
// why people stop using a tool.
func TestComposeModeShowsTheText(t *testing.T) {
	m := model{
		mode:   modeCompose,
		panes:  []registry.Pane{{Host: "local", PaneID: "%0"}},
		width:  80,
		height: 24,
	}
	m.composer.Insert("this is my prompt")

	out := m.View()
	if !strings.Contains(out, "this is my prompt") {
		t.Error("compose mode did not show the typed text")
	}
	if !strings.Contains(out, "enter: send") {
		t.Error("compose mode did not show the hint line")
	}
}

// Confirm mode must show the reasons, so the user can decide. A dialog that shows
// only "press enter" trains people to press enter.
func TestConfirmModeShowsTheReasons(t *testing.T) {
	m := model{
		mode: modeConfirm,
		pending: []broadcast.Reason{
			broadcast.ReasonMultiple,
			broadcast.ReasonFromHistory,
		},
		panes:  []registry.Pane{{Host: "local", PaneID: "%0"}},
		width:  80,
		height: 24,
	}
	m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%0"})
	m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%1"})

	out := m.View()
	if !strings.Contains(out, "more than one target") {
		t.Error("confirm mode did not show ReasonMultiple")
	}
	if !strings.Contains(out, "from the history view") {
		t.Error("confirm mode did not show ReasonFromHistory")
	}
	if !strings.Contains(out, "enter: send anyway") {
		t.Error("confirm mode did not show the action hint")
	}
}

// History mode must show the entries and the outcome words.
func TestHistoryModeShowsEntries(t *testing.T) {
	m := model{
		mode: modeHistory,
		history: []history.Entry{
			{At: time.Unix(1786487832, 0), Host: "nuc", PaneID: "%3",
				Text: "a prompt that worked", Outcome: "delivered"},
			{At: time.Unix(1786487833, 0), Host: "eu", PaneID: "%1",
				Text: "a refused one", Outcome: "refused"},
		},
		histCursor: 0,
		width:      80,
		height:     24,
	}

	out := m.View()
	if !strings.Contains(out, "a prompt that worked") {
		t.Error("history mode did not show the first entry's text")
	}
	if !strings.Contains(out, "✓") {
		t.Error("history mode did not show the delivered glyph")
	}
	if !strings.Contains(out, "r: re-send") {
		t.Error("history mode did not show the key hints")
	}
}

// Browse mode must still show the dashboard. The dispatch cannot silently break
// the normal path.
func TestBrowseModeShowsDashboard(t *testing.T) {
	m := model{
		mode:   modeBrowse,
		panes:  []registry.Pane{{Host: "local", PaneID: "%0", Session: "work"}},
		width:  80,
		height: 24,
	}

	out := m.View()
	if !strings.Contains(out, "tmux-hub") {
		t.Error("browse mode did not show the header")
	}
	if !strings.Contains(out, "work") {
		t.Error("browse mode did not show the pane list")
	}
}

// A re-send that is CANCELLED must leave the operator's draft exactly where it was, and must
// leave nothing of the dialog behind.
//
// This replaces a test that asserted the old mechanism — typing into the composer cleared
// fromHistory, because `r` staged its text there and the operator could type over it. The text no
// longer goes near the composer, so that assertion could only pass vacuously. The property worth
// holding is this one: nothing was sent, so nothing was lost and nothing is still pending.
func TestACancelledResendLeavesTheDraftAlone(t *testing.T) {
	const draft = "a prompt the operator is still writing"
	m := model{
		mode:    modeHistory,
		history: []history.Entry{{Text: "an old prompt", Outcome: "delivered"}},
		panes:   []registry.Pane{{Host: "local", PaneID: "%1"}},
	}
	m.composer.Insert(draft)
	m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%1"})

	got, _ := m.historyKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	asked := got.(model)
	if asked.mode != modeConfirm {
		t.Fatalf("mode = %v, want modeConfirm", asked.mode)
	}
	if asked.composer.Text() != draft {
		t.Fatalf("`r` ate the draft before the dialog was answered: %q", asked.composer.Text())
	}

	// `x` is any-key-that-is-not-enter, which is what the dialog itself says cancels.
	got, cmd := asked.confirmKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	after := got.(model)
	if cmd != nil {
		t.Error("a cancelled re-send returned a command, so something was sent")
	}
	if after.composer.Text() != draft {
		t.Errorf("the draft did not survive a cancelled re-send: %q", after.composer.Text())
	}
	if after.pendingText != "" || after.pendingAct != actionNone {
		t.Errorf("the dialog outlived its own cancellation: text %q act %v",
			after.pendingText, after.pendingAct)
	}
	if after.fromHistory {
		t.Error("a cancelled re-send left `always ask` set, so the next ordinary send inherits a " +
			"reason to confirm from a send that never happened")
	}
}

// Test helpers for hiding tests
