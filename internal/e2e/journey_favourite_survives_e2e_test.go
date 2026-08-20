//go:build e2e

package e2e

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The journey that once broke: pin a session, narrow to favourites, then act on it.
//
// This tests the defect reported as "after attaching to a favourite it stops being in the list".
// The pin was keyed on (kind, host, name) and all three change when the door creates a tmux
// session named <name>-<short id> and the join folds the row into that pane. The key is the Claude
// uuid now (docs/design.md §16), so the pin survives.

// Pressing `f` toggles the pin marker on the row.
func TestJourneyFavouritePinTogglesTheMarker(t *testing.T) {
	f := doorFleet(t, "blocked")
	f.start(t, 120, 40) // This already waits for the agent row to appear

	// Step 1: Walk to the agent row.
	walkTo(t, f.ui, doorAgentName)
	before := cursorRow(t, f.ui)
	if !strings.Contains(before, doorAgentName) {
		t.Fatalf("could not find the agent row on screen:\n%s", before)
	}
	hadStar := strings.Contains(before, "★")

	// Step 2: Press `f` to toggle the pin.
	send(t, f.ui, "f")
	time.Sleep(500 * time.Millisecond)

	// Step 3: Verify the star marker toggled.
	after := cursorRow(t, f.ui)
	hasStar := strings.Contains(after, "★")
	if hadStar == hasStar {
		t.Errorf("pressing f did not toggle the pin marker (was %v, now %v):\nbefore: %s\nafter:  %s",
			hadStar, hasStar, before, after)
	}

	// Step 4: Press `f` again to toggle back.
	send(t, f.ui, "f")
	time.Sleep(500 * time.Millisecond)
	final := cursorRow(t, f.ui)
	hasStar2 := strings.Contains(final, "★")
	if hasStar2 != hadStar {
		t.Errorf("pressing f twice did not return to the original state (was %v, now %v):\noriginal: %s\nfinal:    %s",
			hadStar, hasStar2, before, final)
	}
}

// Narrowing to favourites shows only pinned rows, and the footer indicates the filter.
func TestJourneyFavouriteNarrowShowsOnlyPinnedRows(t *testing.T) {
	f := doorFleet(t, "blocked")
	// A SECOND row that nobody pins, created before the hub starts so the first poll sees it. Without
	// it `*` cannot be observed to filter at all: a fleet of one pinned row looks identical whether the
	// narrowing works or is deleted entirely, which is what the first version of this case asserted.
	if out, err := exec.Command("tmux", "-S", f.target, "-f", "/dev/null", "new-session", "-d",
		"-s", jfvUnpinnedName, "-c", f.work, "sleep", "600").CombinedOutput(); err != nil {
		t.Fatalf("the unpinned row: %v: %s", err, out)
	}
	f.start(t, 120, 40) // This already waits for the agent row to appear
	waitUntil(t, "the unpinned row to reach the screen", 30*time.Second, func() bool {
		s, err := paneScreen(t, f.ui, "ui:0")
		return err == nil && strings.Contains(s, jfvUnpinnedName)
	})

	// Step 1: Ensure the agent row is pinned.
	walkTo(t, f.ui, doorAgentName)
	if !strings.Contains(cursorRow(t, f.ui), "★") {
		// Not pinned yet, so pin it.
		send(t, f.ui, "f")
		time.Sleep(500 * time.Millisecond)
	}

	// Verify the pin is present.
	if !strings.Contains(cursorRow(t, f.ui), "★") {
		t.Fatalf("the row is not pinned:\n%s", capturePane(t, f.ui, "ui"))
	}

	// Step 2: Press `*` to show only favourites.
	send(t, f.ui, "*")
	time.Sleep(500 * time.Millisecond)

	// Step 3: Verify only the pinned row is visible.
	screen := capturePane(t, f.ui, "ui")
	if !strings.Contains(screen, doorAgentName) {
		t.Errorf("favourites view does not show the pinned agent:\n%s", screen)
	}
	if !strings.Contains(screen, "★") {
		t.Errorf("favourites view does not show the star marker:\n%s", screen)
	}
	// THE HALF THAT CAN FAIL. "The pinned row is still here" is true of a filter that filters nothing:
	// with the narrowing deleted from rowsForScreen this case stayed green, because the fleet was one
	// pinned row. The UNPINNED row's absence is the assertion, and its presence in the fixture is what
	// makes the key observable at all.
	if strings.Contains(screen, jfvUnpinnedName) {
		t.Errorf("`*` kept the UNPINNED row %q on screen, so the narrowing is not narrowing:\n%s",
			jfvUnpinnedName, screen)
	}
	// And the footer says the narrowing is on and what it costs: a screen with rows missing and no
	// sentence is indistinguishable from a fleet that went away.
	if !strings.Contains(screen, "★ only") {
		t.Errorf("the footer does not say the favourites narrowing is on:\n%s", screen)
	}

	// The fixture has only one row (the agent), so after pinning and narrowing we should see
	// exactly one row. The "watched" pane from the fixture might also be visible if it wasn't
	// pinned, so we mainly verify that the pinned agent is present.

	// Step 4: Press `*` again to show everything.
	send(t, f.ui, "*")
	time.Sleep(500 * time.Millisecond)
	screen = capturePane(t, f.ui, "ui:0")
	if !strings.Contains(screen, doorAgentName) {
		t.Errorf("after toggling favourites off, the agent row is missing:\n%s", screen)
	}
	if !strings.Contains(screen, jfvUnpinnedName) {
		t.Errorf("after toggling favourites off, the UNPINNED row did not come back (%s) — a toggle "+
			"that only goes one way is a filter the operator cannot leave:\n%s",
			jfvUnpinnedName, screen)
	}
}

// The pin survives when the row is renamed by the door.
func TestJourneyFavouriteSurvivesTheDoorRename(t *testing.T) {
	f := doorFleet(t, "blocked")
	f.start(t, 120, 40) // This already waits for the agent row to appear

	// Step 1: Ensure the agent row is pinned.
	walkTo(t, f.ui, doorAgentName)
	if !strings.Contains(cursorRow(t, f.ui), "★") {
		// Not pinned yet, so pin it.
		send(t, f.ui, "f")
		time.Sleep(500 * time.Millisecond)
	}

	// Verify the pin is present.
	if !strings.Contains(cursorRow(t, f.ui), "★") {
		t.Fatalf("the row is not pinned:\n%s", capturePane(t, f.ui, "ui"))
	}

	// Step 2: Narrow to favourites.
	send(t, f.ui, "*")
	time.Sleep(500 * time.Millisecond)
	before := capturePane(t, f.ui, "ui")
	if !strings.Contains(before, doorAgentName) {
		t.Fatalf("the pinned row is not in favourites view before attach:\n%s", before)
	}
	if !strings.Contains(before, "★") {
		t.Fatalf("the star marker is missing before attach:\n%s", before)
	}

	// Step 3: Attach to the row, which creates a door session with a renamed identity.
	// The hub's window is "ui:0", so send to that to ensure the keystroke reaches the hub.
	send(t, f.ui, "a")

	// Wait for the door to create the session. The operator ends up INSIDE the woken session,
	// so the hub's client is no longer showing the hub.
	want := doorAgentName + "-" + doorAgentID
	f.waitForSession(t, want)

	// Verify the verb ran.
	f.waitOr(t, "the fake claude to record an attach", 20*time.Second, func() bool {
		for _, v := range f.verbs(t) {
			if strings.HasPrefix(v, "attach ") {
				return true
			}
		}
		return false
	})

	// The operator is now inside the woken session. Verify they can see it.
	f.waitOr(t, "the hub's client to be inside the woken session", 20*time.Second, func() bool {
		s, err := paneScreen(t, f.ui, "ui")
		return err == nil && strings.Contains(s, "WOKE-"+doorAgentID)
	})

	// Step 4: Return to the hub with C-b d (detach).
	// Send to "ui" which is now the woken session.
	send(t, f.ui, "C-b", "d")
	time.Sleep(1 * time.Second)

	// Wait for the hub to reappear on screen.
	waitUntil(t, "the hub to reappear after detach", 15*time.Second, func() bool {
		s, err := paneScreen(t, f.ui, "ui")
		return err == nil && strings.Contains(s, "tmux-hub")
	})

	// Step 5: Verify the pin survived. The favourites filter is still on from step 2.
	// The row's name has changed from doorAgentName to doorAgentName-doorAgentID, but the
	// pin is keyed on the uuid, so it should still be pinned.
	after := capturePane(t, f.ui, "ui")

	// The star marker should still be present on some row.
	if !strings.Contains(after, "★") {
		t.Errorf("the star marker is missing after the door renamed the row:\n%s", after)
	}

	// The renamed session should be visible. The door creates it with the pattern name-id.
	if !strings.Contains(after, doorAgentID) {
		t.Errorf("the renamed session is not visible after attach:\n%s", after)
	}

	// Since favourites filter is still on and the row is still pinned, it should be visible.
	// The exact name might be truncated, so we check for the ID which is the stable part.
}

// jfvUnpinnedName is the row nobody pins: without it `*` cannot be observed to filter, because a fleet
// of one pinned row looks identical whether the narrowing works or is deleted entirely.
const jfvUnpinnedName = "jfv-not-pinned"
