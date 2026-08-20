//go:build e2e

package e2e

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// THE FINDING JOURNEY: forty rows, one you half-remember.
//
// A fleet of several panes with different names → `/` → type a fragment of one name → the list
// narrows LIVE while typing, and the footer says what it is narrowed to and what it costs → enter
// keeps the keyword and hands the keys back → the cursor is on a row that MATCHES → act on it — walk
// to it and press a key whose refusal or effect proves WHICH row is under the cursor → esc widens,
// and the row count returns.
//
// §21.17 is the spec. Search matches every name a row has, including the one an alias hides.
func TestE2EJourneySearchAttach(t *testing.T) {
	// Build a fleet with several sessions whose names we control, so the narrowing is predictable.
	ui, target, work := hubUI(t, 120, 40)

	// Five sessions with DISTINCT identifiable names, so we can search for one and verify the
	// narrowing. The names are chosen so that a three-letter fragment narrows to exactly one.
	sessions := []struct {
		name     string // the session name
		fragment string // what to search for to find ONLY this one
	}{
		{"cicd-pipeline", "pipe"},  // matches only this
		{"deploy-staging", "stag"}, // matches only this
		{"development", "deve"},    // matches only this
		{"monitor-prod", "moni"},   // matches only this
		{"testing-env", "testi"},   // matches only this (not "test")
	}

	var paneIDs []string
	for _, s := range sessions {
		out, err := exec.Command("tmux", "-S", target, "-f", "/dev/null", "new-session", "-d",
			"-s", s.name, "-c", work, "-P", "-F", "#{pane_id}", "sleep", "300").Output()
		if err != nil {
			t.Fatalf("create session %s: %v", s.name, err)
		}
		paneIDs = append(paneIDs, strings.TrimSpace(string(out)))
	}

	// Wait for ALL sessions to appear on screen.
	waitUntil(t, "all five sessions to reach the screen", 30*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		if err != nil {
			return false
		}
		for _, name := range []string{"cicd-pipeline", "deploy-staging", "development",
			"monitor-prod", "testing-env"} {
			if !strings.Contains(s, name) {
				return false
			}
		}
		return true
	})

	// STEP 1: Press `/` to open search field.
	send(t, ui, "/")
	screenHas(t, ui, "search:", "`/` must open the keyword field")

	capture1 := capturePane(t, ui, "ui")
	t.Logf("STEP 1 — opened search field:\n%s", capture1)

	// STEP 2: Type "pipe" one character at a time. The list narrows LIVE while typing.
	// Wait for field to still be open after each keystroke — a key taken as a command would close
	// the field and subsequent letters would run as commands.
	keyword := "pipe"
	for i, r := range keyword {
		send(t, ui, string(r))
		time.Sleep(120 * time.Millisecond) // let the live narrowing update
		waitUntil(t, "the field to survive key "+string(r), 10*time.Second, func() bool {
			s, err := paneScreen(t, ui, "ui")
			return err == nil && strings.Contains(s, "search:")
		})

		// After typing enough letters, the other sessions should disappear from the list.
		if i >= 2 { // after "pip", should narrow to just cicd-pipeline
			s, _ := paneScreen(t, ui, "ui")
			if strings.Contains(s, "deploy-staging") || strings.Contains(s, "development") {
				t.Logf("After typing %q, list should be narrowed but still shows other sessions:\n%s",
					keyword[:i+1], s)
			}
		}
	}
	screenHas(t, ui, "search: pipe", "every key must reach the field as TEXT")

	capture2 := capturePane(t, ui, "ui")
	t.Logf("STEP 2 — typed keyword %q (live narrowing):\n%s", keyword, capture2)

	// STEP 3: Press Enter to keep the keyword and close the field.
	// Wait for the FIELD TO CLOSE, not for "the list narrowed" — that was already true while typing.
	send(t, ui, "Enter")
	waitUntil(t, "the keyword field to close", 15*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && !strings.Contains(s, "search:")
	})

	capture3 := capturePane(t, ui, "ui")
	t.Logf("STEP 3 — pressed Enter (field closed, keyword kept):\n%s", capture3)

	// STEP 4: Assert the narrowing worked. The footer must say:
	// - what the list is narrowed to (the keyword shows in the footer)
	// - what it costs (N of M count)
	if !strings.Contains(capture3, "pipe") && !strings.Contains(capture3, "\"pipe\"") {
		t.Errorf("the footer does not say what the list is narrowed to:\n%s", capture3)
	}
	// The COST, as a shape rather than a literal: this fixture keeps the real HOME, so the operator's
	// own agent rows are in the list and the denominator is whatever their fleet is today. Asserting
	// `1 of 5` measured the fixture's own pane count against somebody's Claude sessions — the repo's
	// rule about a floor rather than an exact count, one wave later.
	if !jsrHasNarrowingCount(capture3) {
		t.Errorf("the footer does not say what the narrowing costs (want `N of M`):\n%s", capture3)
	}

	// The MATCHING row (cicd-pipeline) must be visible.
	if !strings.Contains(capture3, "cicd-pipeline") {
		t.Errorf("the row that matches the keyword is not on screen:\n%s", capture3)
	}

	// The NON-MATCHING rows must NOT be visible (they are filtered out).
	for _, name := range []string{"deploy-staging", "development", "monitor-prod", "testing-env"} {
		if strings.Contains(capture3, name) {
			t.Errorf("a row that does not match the keyword is still on screen (%s):\n%s",
				name, capture3)
		}
	}

	// STEP 5: The cursor must be on a MATCHING row. Walk to the matching row explicitly to prove
	// which row is under the cursor, then press a key that acts on it.
	// We'll use `x` (toggle hidden) as the proof — we can verify the mark appears.
	walkTo(t, ui, "cicd-pipeline")

	// Verify cursor is on the right row.
	curRow := cursorRow(t, ui)
	if !strings.Contains(curRow, "cicd-pipeline") {
		t.Errorf("after walk, cursor is on %q, want a row with cicd-pipeline", curRow)
	}

	capture4 := capturePane(t, ui, "ui")
	t.Logf("STEP 5 — cursor on matching row:\n%s", capture4)

	// ACT ON IT, and the acting is the point: the cursor is on a row the narrowing kept, so a key
	// must reach THAT row and not one off screen.
	//
	// `x` hides it, and a hidden row LEAVES the narrowed list — the filter shows what matches AND is
	// not hidden — so the `[x]` marker cannot be seen while the narrowing is on. The first version of
	// this step waited ten seconds for a marker the product is right not to draw. What proves the key
	// landed is that the row is GONE from the narrowed list, and then `X` (show hidden) brings it back
	// wearing the marker: two assertions, and together they say which row the key reached.
	rowBefore := cursorRow(t, ui)
	send(t, ui, "x")
	waitUntil(t, "the hidden row to leave the narrowed list", 10*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && !strings.Contains(s, "cicd-pipeline")
	})
	send(t, ui, "X")
	waitUntil(t, "X to bring it back wearing the hidden marker", 10*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && strings.Contains(s, "cicd-pipeline") && strings.Contains(s, "[x]")
	})
	t.Logf("STEP 5 — the key reached the row the narrowing kept (was %q)", rowBefore)

	capture5 := capturePane(t, ui, "ui")
	t.Logf("STEP 5b — acted on row (pressed x, hidden marker appeared):\n%s", capture5)

	// The row must still be visible (hidden rows are only filtered out when X is pressed).
	if !strings.Contains(capture5, "cicd-pipeline") {
		t.Errorf("after marking hidden, the row disappeared (it should still be visible):\n%s", capture5)
	}

	// STEP 6: Press Esc to widen. All five sessions should return to the list.
	send(t, ui, "Escape")
	waitUntil(t, "esc to widen the list", 15*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		if err != nil {
			return false
		}
		// All five sessions should be visible again.
		for _, name := range []string{"cicd-pipeline", "deploy-staging", "development",
			"monitor-prod", "testing-env"} {
			if !strings.Contains(s, name) {
				return false
			}
		}
		return true
	})

	capture6 := capturePane(t, ui, "ui")
	t.Logf("STEP 6 — pressed Esc (list widened):\n%s", capture6)

	// After widening, the footer must NO LONGER name the keyword — and the assertion has to be about
	// the FOOTER's own form, not about the substring. The keyword appears there QUOTED (`"pipe"`,
	// which is how filterTally.sentence writes it), while the row that matched is called
	// `cicd-pipeline` and contains the same four letters: a bare Contains over the screen therefore
	// reports the keyword as still set precisely BECAUSE widening worked and brought the row back.
	if strings.Contains(capture6, "\"pipe\"") {
		t.Errorf("after widening, the footer still names the keyword:\n%s", capture6)
	}
	// And the count that says a narrowing is on is gone with it.
	if jsrHasNarrowingCount(capture6) && strings.Contains(capture6, "\"") {
		t.Errorf("after widening, the footer still counts a narrowing:\n%s", capture6)
	}

	// All five created sessions should be visible.
	for _, name := range []string{"cicd-pipeline", "deploy-staging", "development",
		"monitor-prod", "testing-env"} {
		if !strings.Contains(capture6, name) {
			t.Errorf("after widening, session %s is not on screen:\n%s", name, capture6)
		}
	}
}

// SEARCHING BY THE ORIGINAL NAME after an alias hides it is NOT tested here, and that is a decision.
//
// A case stood here that did nothing: an unconditional `t.Skip("needs project fixture setup; covered
// manually for now")`, which reports PASS while asserting nothing — the failure mode this repo has a
// rule about, since a skipped case is indistinguishable from a machine with nothing to test.
//
// The property it wanted is covered where the fixture already exists:
// `TestJourneyNamingAndSearchingByBothNames` in journey_name_attach_e2e_test.go names a row through
// the interface and then finds it by the name the alias hid, which is the same claim with the setup
// paid for once.

// jsrHasNarrowingCount reports whether the footer carries an `N of M` count. It is a SHAPE check
// rather than a literal: this fixture keeps the real HOME, so the fleet includes the operator's own
// Claude sessions and the denominator is theirs, not the fixture's.
func jsrHasNarrowingCount(screen string) bool {
	for _, l := range strings.Split(screen, "\n") {
		if i := strings.Index(l, " of "); i > 0 {
			before, after := strings.TrimSpace(l[:i]), strings.TrimSpace(l[i+4:])
			if before == "" || after == "" {
				continue
			}
			bw := before[len(before)-1]
			aw := after[0]
			if bw >= '0' && bw <= '9' && aw >= '0' && aw <= '9' {
				return true
			}
		}
	}
	return false
}
