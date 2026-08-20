//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// MISSING end-to-end tests for the naming surface, focused on gaps the existing suite does not
// cover. These test operator-visible defects that can only be proven in a real terminal with the
// real binary: a space key arrives as tea.KeySpace rather than a rune, and `%` in a value is
// handled by tmux's own seam, which only the real transport reaches.

// TestE2EGAPNamingASpaceSurvivesTypingAndCommit proves spaces are inserted as text and kept.
//
// bubbletea reports a space as tea.KeySpace rather than a rune in msg.Runes, and the other half
// of the composer got that wrong in opposite directions: one path dropped the space, another
// path inserted it twice (`Insert(string(msg.Runes))` then `Insert(" ")` because Runes IS set).
// A name without spaces reaches nothing new; a name containing one exercises both the keystroke
// decoder and the commit path.
func TestE2EGAPNamingASpaceSurvivesTypingAndCommit(t *testing.T) {
	const name = "the DR plan"
	h := namingStart(t, 120, 40, 1)
	namingRequireDerivedName(t, h)
	namingWalkTo(t, h.ui, rowNeedleIn(namingSession, h.paneIDs[0], h.paneIDs))
	send(t, h.ui, "N")
	screenHas(t, h.ui, "name this session:", "`N` must open the overlay")

	typeOneAtATime(t, h.ui, name)
	namingWaitField(t, h.ui, "name: "+name+"▏",
		"a space typed key by key must reach the field as a single space — a doubled space or a "+
			"missing one is a keystroke decoder defect")
	send(t, h.ui, "Enter")
	namingWaitOverlayClosed(t, h.ui, "a legal name on an unnamed row must be accepted")

	waitUntil(t, "the name to reach the screen", 15*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && strings.Contains(s, "» "+name)
	})
	screen := capturePane(t, h.ui, "ui")
	if !strings.Contains(screen, "the DR plan") {
		t.Errorf("the screen does not show the name with its space — the space was dropped on "+
			"commit or on read:\n%s", screen)
	}
	body := namingFile(t, h)
	if !strings.Contains(body, `name = "the DR plan"`) {
		t.Errorf("projects.toml does not carry the name with its space:\n%s", body)
	}
}

// A DUPLICATE NAME's refusal is NOT tested here, and that is a decision rather than an omission.
//
// The case that stood here could not produce a collision: it named two panes of ONE tmux session, and
// an alias is keyed on the SESSION, so naming the second row UPDATED the first name instead of
// colliding with it — measured, projects.toml held one entry with the second name and the screen read
// `SCRATCH » Staging Deploy`. The product was right and the fixture could not express the case.
//
// It is covered three times over where it is cheaper and sharper: `internal/ui/naming_test.go` drives
// the model and asserts the mode stays in naming (a duplicate was accepted → the test fails),
// `internal/project/alias_test.go` pins the rule itself ("checked FLEET-WIDE and CASE-FOLDED, at
// commit", including that a row may keep its OWN name), and the mockup has a frame for it — the scene
// called "3. Имя занято", which is the wording an operator reads. An e2e case would need two SESSIONS
// on the target server to say anything new, and it would then be asserting the same rule a fourth
// time through the slowest surface there is.
