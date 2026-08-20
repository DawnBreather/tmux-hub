//go:build e2e

package e2e

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// `/` narrows the list to a keyword and `esc` widens it again, THROUGH THE INTERFACE
// (docs/design.md §21.16).
//
// It is an interface case rather than a model one because the thing most likely to be wrong is the
// key ROUTING: inside the field every key must be text, and `send-keys -l "cicd"` arrives as ONE key
// message whose String() is the whole string — so a test that types a word cannot tell a field that
// takes text from one that took `c` as a command. Every keystroke here is sent on its own.
func TestE2EUISearchNarrowsToAKeywordAndEscWidens(t *testing.T) {
	h := namingStart(t, 120, 40, 1)

	// A second session, so "the keyword kept one row" is distinguishable from "the keyword kept
	// everything" and from "the list went blank".
	other, err := exec.Command("tmux", "-S", h.target, "-f", "/dev/null", "new-session", "-d",
		"-s", "other-e2e", "-c", h.proj, "-P", "-F", "#{pane_id}", "sleep", "300").Output()
	if err != nil {
		t.Fatalf("second session: %v", err)
	}
	// By SESSION NAME and not by pane id: each of these sessions holds ONE pane, so the hub draws
	// no id on their rows (rowPaneID — an id that distinguishes nothing is a column nobody can
	// read), and the only id on the screen is the CURSOR's, inside the tile. A wait for two ids can
	// therefore never be satisfied by two rows, and it timed out against a correct screen.
	_ = strings.TrimSpace(string(other))
	waitUntil(t, "both sessions to reach the screen", 30*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && strings.Contains(s, "other-e2e") && strings.Contains(s, namingSession)
	})

	send(t, h.ui, "/")
	screenHas(t, h.ui, "search:", "`/` must open the keyword field")
	// One key at a time, and the field must still be open after each: a key taken as a command
	// would leave the field and the next letters would run as commands on the dashboard.
	for _, k := range []string{"o", "t", "h", "e", "r"} {
		send(t, h.ui, k)
		screenHas(t, h.ui, "search:", "the field must survive the key "+k)
	}
	screenHas(t, h.ui, "search: other", "every key must reach the field as TEXT")

	send(t, h.ui, "Enter")
	// Wait for the FIELD TO CLOSE, which is the signal the assertions below are about. Waiting for
	// "the list narrowed" reads as instant and proves nothing: the filter is live while typing, so
	// that was already true before enter was pressed — and the capture then landed on a screen
	// whose field was still open, reporting a missing footer that arrives a moment later.
	waitUntil(t, "the keyword field to close", 15*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && !strings.Contains(s, "search:")
	})
	s := capturePane(t, h.ui, "ui")
	if strings.Contains(s, h.paneIDs[0]) {
		t.Errorf("the row that does not match the keyword is still on screen:\n%s", s)
	}
	if !strings.Contains(s, "other") {
		t.Errorf("the footer does not say what the list is narrowed to:\n%s", s)
	}
	if !strings.Contains(s, "1 of 2") {
		t.Errorf("the footer does not say what the narrowing costs — a filtered list that does "+
			"not count is a list that lies about the fleet:\n%s", s)
	}

	send(t, h.ui, "Escape")
	waitUntil(t, "esc to widen", 15*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && strings.Contains(s, namingSession) && strings.Contains(s, "other-e2e")
	})
}

// `*` shows only the pinned rows, and REFUSES with the remedy on a fleet where nothing is pinned —
// turning it on there would empty the screen, and an empty list is indistinguishable from a fleet
// that went away.
func TestE2EUIStarShowsOnlyFavouritesAndRefusesWhenNonePinned(t *testing.T) {
	h := namingStart(t, 120, 40, 1)
	other, err := exec.Command("tmux", "-S", h.target, "-f", "/dev/null", "new-session", "-d",
		"-s", "other-e2e", "-c", h.proj, "-P", "-F", "#{pane_id}", "sleep", "300").Output()
	if err != nil {
		t.Fatalf("second session: %v", err)
	}
	// By SESSION NAME and not by pane id: each of these sessions holds ONE pane, so the hub draws
	// no id on their rows (rowPaneID — an id that distinguishes nothing is a column nobody can
	// read), and the only id on the screen is the CURSOR's, inside the tile. A wait for two ids can
	// therefore never be satisfied by two rows, and it timed out against a correct screen.
	_ = strings.TrimSpace(string(other))
	waitUntil(t, "both sessions to reach the screen", 30*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && strings.Contains(s, "other-e2e") && strings.Contains(s, namingSession)
	})

	// Nothing pinned yet: the key must refuse and name the key that pins.
	send(t, h.ui, "*")
	screenHas(t, h.ui, "nothing is pinned", "`*` on an unpinned fleet must refuse rather than "+
		"empty the screen")
	screenHas(t, h.ui, "f pins", "the refusal must carry its remedy")
	s := capturePane(t, h.ui, "ui")
	if !strings.Contains(s, namingSession) || !strings.Contains(s, "other-e2e") {
		t.Fatalf("the refusal narrowed the list anyway:\n%s", s)
	}

	// Pin the row under the cursor, then `*` keeps it and drops the other.
	send(t, h.ui, "f")
	waitUntil(t, "the pin to appear", 15*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && strings.Contains(s, "★")
	})
	send(t, h.ui, "*")
	waitUntil(t, "the list to show only what is pinned", 15*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && !strings.Contains(s, "other-e2e")
	})
	if s := capturePane(t, h.ui, "ui"); !strings.Contains(s, "1 of 2") {
		t.Errorf("the footer does not count what `*` keeps off the screen:\n%s", s)
	}
}

// A keyword nothing CONTAINS falls back to a subsequence, and the screen says it did.
//
// This is the interface half of the fzf question: the matcher is unit-tested, and what only the real
// binary can show is that the fallback survives the key ROUTING — every letter of `drvd` has to reach
// the field as text, and the footer that admits the looseness has to fit beside the count on a real
// terminal. `derived-e2e` does not contain `drvd`; it contains d…r…v…d in order.
//
// Sent one key at a time, because `send-keys -l "drvd"` arrives as ONE key message whose String() is
// the whole word — a test that types a word cannot tell a field that takes text from one that took
// `d` as a command.
func TestE2EUIASubsequenceAnswersWhenNothingContainsTheKeyword(t *testing.T) {
	h := namingStart(t, 120, 40, 1)
	if out, err := exec.Command("tmux", "-S", h.target, "-f", "/dev/null", "new-session", "-d",
		"-s", "other-e2e", "-c", h.proj, "sleep", "300").CombinedOutput(); err != nil {
		t.Fatalf("second session: %v: %s", err, out)
	}
	waitUntil(t, "both sessions to reach the screen", 30*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && strings.Contains(s, "other-e2e") && strings.Contains(s, namingSession)
	})

	send(t, h.ui, "/")
	screenHas(t, h.ui, "search:", "`/` must open the keyword field")
	for _, k := range []string{"d", "r", "v", "d"} {
		send(t, h.ui, k)
		time.Sleep(120 * time.Millisecond)
	}

	// The FIELD says it while it still has focus, which is where the operator is looking.
	screenHas(t, h.ui, "nothing contains it",
		"a keyword no row contains must say so while the field is open, or `1 of 2` reads as one "+
			"row containing the word")
	s := capturePane(t, h.ui, "ui")
	if !strings.Contains(s, namingSession) {
		t.Errorf("the subsequence answer lost the row it should have found:\n%s", s)
	}
	if strings.Contains(s, "other-e2e") {
		t.Errorf("the subsequence answer kept a row that does not even resemble the keyword:\n%s", s)
	}

	// And after enter, the kept footer carries the same admission in its own words.
	send(t, h.ui, "Enter")
	waitUntil(t, "the field to close and the footer to take over", 15*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && !strings.Contains(s, "search:")
	})
	if s := capturePane(t, h.ui, "ui"); !strings.Contains(s, `like "drvd"`) {
		t.Errorf("the kept footer does not say the rows merely resemble the keyword:\n%s", s)
	}

	// esc widens, and the row that never matched comes back.
	send(t, h.ui, "Escape")
	waitUntil(t, "esc to widen", 15*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && strings.Contains(s, "other-e2e") && !strings.Contains(s, `like "drvd"`)
	})
}
