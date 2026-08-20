package ui

import (
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/hide"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// `x` on a WAITING row looks like it did nothing, and that is the whole defect. The mark
// is written, but §18's resurface rule keeps a waiting row visible — so the key that
// reads as "not now" silently means "forever, once it goes quiet" (docs/design.md
// §21.11.2, decided: keep the behaviour, lose the silence).
//
// The behaviour is right and stays: it matches §18's promise and it preserves the
// resurface rule, which is the only safety net a wrong hide has.
func TestHidingAWaitingRowSaysWhatWasRecorded(t *testing.T) {
	m := modelWith(t, pane("local", "w", "claude", 0, "claude", state.Needs))
	m = m.hideSubject()

	// The mark exists — the behaviour did not change.
	if !m.hidden.Marked(hide.KeyOf(m.panes[0])) {
		t.Fatal("no mark was written; the behaviour was supposed to be kept")
	}
	// The row is still shown, because it is waiting.
	if m.hidden.Hidden(m.panes[0]) {
		t.Fatal("a waiting row was hidden immediately; the resurface rule is the safety net")
	}
	// And the operator is told, in English, WHEN it takes effect.
	if m.note == "" {
		t.Fatal("note is empty — pressing x on a waiting row produced no visible change " +
			"and no sentence, which is indistinguishable from the key not working")
	}
	for _, want := range []string{"hidden", "waiting"} {
		if !strings.Contains(strings.ToLower(m.note), want) {
			t.Errorf("note = %q, want it to mention %q — it has to name when the mark "+
				"takes effect, not merely that something happened", m.note, want)
		}
	}
}

// A row that is NOT waiting vanishes the moment it is marked, which is its own feedback.
// A note there would be noise, and the note channel is shared.
func TestHidingAQuietRowSaysNothing(t *testing.T) {
	m := modelWith(t, pane("local", "w", "tail", 0, "tail -f log", state.Quiet))
	m = m.hideSubject()
	if !m.hidden.Hidden(m.panes[0]) {
		t.Fatal("a quiet row was not hidden")
	}
	if m.note != "" {
		t.Errorf("note = %q, want empty: the row disappeared, which is the feedback", m.note)
	}
}

// UN-marking a waiting row must not claim it was hidden. The toggle runs both ways and a
// note that only knows one direction is worse than none.
func TestUnhidingAWaitingRowDoesNotClaimItWasHidden(t *testing.T) {
	m := modelWith(t, pane("local", "w", "claude", 0, "claude", state.Needs))
	m = m.hideSubject() // mark
	m.note = ""
	m = m.hideSubject() // un-mark
	if m.hidden.Marked(hide.KeyOf(m.panes[0])) {
		t.Fatal("the second press did not remove the mark")
	}
	if strings.Contains(strings.ToLower(m.note), "hidden") {
		t.Errorf("note = %q after UN-hiding — it must not say the row was hidden", m.note)
	}
}

// The subject may be a selection of several rows, and the sentence must count them
// rather than naming one and leaving the rest silent.
func TestHidingSeveralWaitingRowsCountsThem(t *testing.T) {
	m := modelWith(t,
		pane("local", "w", "claude", 0, "claude", state.Needs),
		pane("local", "w", "claude", 1, "claude", state.Needs))
	for _, p := range m.panes {
		m.sel.Toggle(selKey(p))
	}
	m = m.hideSubject()
	if !strings.Contains(m.note, "2") {
		t.Errorf("note = %q, want it to count the two rows it marked", m.note)
	}
}
