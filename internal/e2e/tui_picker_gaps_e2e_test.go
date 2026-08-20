//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// PICKER GAPS: test cases that expose defects the existing picker suite does not cover.
//
// The existing tui_picker_e2e_test.go covers the probe outcomes, save/esc paths, and the
// hosts.toml warning persistence. What it does NOT explicitly assert: (1) the screen when
// there are NO candidates to show at all — which is the first-run case before ~/.ssh/config
// has any real hosts; (2) that space UNTICKS as well as ticking, which is the only way to
// correct a mis-tick without esc; (3) that j/k/space keep the picker OPEN rather than
// closing the overlay, which would leave the operator unable to navigate; (4) that all
// candidates being refused still shows the LIST with reasons, rather than crashing or
// showing nothing.

// The picker opened on a hub with no ssh config at all shows "nothing to show yet" and
// stays there — no candidates means no rows, and `r` on an empty config re-asks nobody.
//
// This is the FIRST RUN case. The operator has installed the hub, has no hosts.toml, and
// their ~/.ssh/config is either empty or holds only the systemd patterns hostset.Skip marks
// as non-hosts. pickerOpen waits for "answer with tmux" which implies candidates exist, so
// this case cannot use it: the screen here never says that.
func TestE2EGAPPickerEmptySSHConfigShowsNothingToShowYet(t *testing.T) {
	f := pickerHub(t, pickerOpts{
		cols: 120, rows: 40, probeTimeout: "1ms",
		sshConfig: "", // No candidates at all.
		hostsTOML: pickerOneDisabledEntry,
	})
	send(t, f.ui, "p")
	screenHas(t, f.ui, "Hosts —", "`p` must open the picker")

	const expected = "Hosts — nothing to show yet; r asks every candidate in ~/.ssh/config"
	body := pickerSqueezed(t, f.ui)
	if !strings.Contains(body, expected) {
		t.Errorf("the picker with no candidates must say 'nothing to show yet', so the operator "+
			"knows the screen is working and what to do next (edit ~/.ssh/config). got:\n%s", body)
	}

	// A moment for a probe round that should not exist to land, then verify the screen has
	// not changed: there are no candidates to probe, so it should not replace the message.
	time.Sleep(1500 * time.Millisecond)
	body = pickerSqueezed(t, f.ui)
	if !strings.Contains(body, expected) {
		t.Errorf("'nothing to show yet' disappeared after waiting, so it reads as a loading "+
			"state rather than the answer — but with no candidates there is nothing to wait "+
			"for:\n%s", body)
	}

	// And `r` on an empty config still shows the same screen, because there is nothing to
	// re-ask. A screen that changes to show zero rows would read as the probe deleting
	// something, when the config held nothing to begin with.
	send(t, f.ui, "r")
	time.Sleep(1500 * time.Millisecond)
	body = pickerSqueezed(t, f.ui)
	if !strings.Contains(body, expected) {
		t.Errorf("`r` on an empty candidate list changed the screen, so re-probing zero hosts "+
			"looks different from having zero hosts — but both are the same fact:\n%s", body)
	}
}

// Space ticks a candidate, and space AGAIN on the same candidate unticks it — this is the
// only in-picker way to correct a mis-tick without esc-ing the whole screen.
//
// The existing suite ticks with space and then either commits with enter or re-probes with
// r, but it never unticks. So a defect where the second space did nothing, or where it
// produced a note but left the box ticked, would pass every existing case — and the operator
// would have no remedy but esc, losing every other tick on the screen.
func TestE2EGAPPickerSpaceTogglesTickBoxOnAndOff(t *testing.T) {
	f := pickerHub(t, pickerOpts{
		cols: 120, rows: 40, probeTimeout: "1ms",
		sshConfig: pickerTwoCandidates,
		hostsTOML: pickerOneDisabledEntry,
	})
	pickerOpen(t, f.ui)

	pickerWalkTo(t, f.ui, "hub-e2e-keep")
	send(t, f.ui, "space")
	pickerBodyHas(t, f.ui, "[x] hub-e2e-keep",
		"the first `space` must tick the box — without it, this case proves only that a box "+
			"already ticked can be unticked")

	// The same key again, on the same row. If space is only a SET rather than a TOGGLE, the
	// box would stay ticked and the screen would be unchanged, which is a keystroke that does
	// nothing — and the operator's only remedy is `esc`, which clears every other decision.
	send(t, f.ui, "space")
	waitUntil(t, "the second `space` to untick the box it just ticked", 15*time.Second, func() bool {
		return strings.Contains(pickerSqueezed(t, f.ui), "[ ] hub-e2e-keep")
	})

	// And the box really is unticked, not merely drawn that way: `enter` must write the file
	// with this host DISABLED or the toggle is only cosmetic. This also proves the ticker
	// isn't broken in the "stuck on" direction by a row that was never Kept.
	send(t, f.ui, "Enter")
	screenHas(t, f.ui, "0 hosts kept",
		"`enter` after unticking must report '0 hosts kept', because turning a never-enabled host "+
			"off is not a decision — the fixture had one disabled host, and ticking then unticking "+
			"a different candidate leaves zero enabled")
}

// j/k navigate within the picker, and space toggles a box — all three keys keep the picker
// OPEN rather than closing it back to the dashboard, which would make multi-host selection
// impossible.
//
// The existing suite uses j via pickerWalkTo and uses space, but does not explicitly assert
// that the picker stays open: it checks what the ROWS say, which would still be readable in
// a hypothetical broken product that closed the overlay after every key and then reopened it.
// A picker that closed on j/space would make selecting two hosts impossible.
func TestE2EGAPPickerJKSpaceKeepTheOverlayOpen(t *testing.T) {
	f := pickerHub(t, pickerOpts{
		cols: 120, rows: 40, probeTimeout: "1ms",
		sshConfig: pickerTwoCandidates,
		hostsTOML: pickerOneDisabledEntry,
	})
	pickerOpen(t, f.ui)

	// The overlay's own rule is its marker: a full-width horizontal rule that the dashboard
	// does not draw. If that rule is on screen, the picker is open; if not, it closed.
	screen := capturePane(t, f.ui, "ui")
	if !strings.Contains(screen, "─────") {
		t.Fatalf("the picker's horizontal rule is not on screen after `p`, so the overlay did "+
			"not open and this case cannot test whether it stays open:\n%s", screen)
	}

	// j moves the cursor. The assertion is not WHERE it moved, but that the picker is still
	// showing: a product that closed on j would re-show the dashboard with no overlay at all.
	send(t, f.ui, "j")
	time.Sleep(200 * time.Millisecond)
	screen = capturePane(t, f.ui, "ui")
	if !strings.Contains(screen, "─────") {
		t.Errorf("`j` closed the picker, so navigation within the overlay is impossible — the "+
			"operator can only look at the first row:\n%s", screen)
	}

	// k in the other direction, same assertion: the picker must still be open.
	send(t, f.ui, "k")
	time.Sleep(200 * time.Millisecond)
	screen = capturePane(t, f.ui, "ui")
	if !strings.Contains(screen, "─────") {
		t.Errorf("`k` closed the picker, so upward navigation is impossible:\n%s", screen)
	}

	// space ticks a box. If space closed the overlay, the operator could enable only one
	// host per `p` invocation, and a fleet of three hosts would need three separate opens.
	send(t, f.ui, "space")
	time.Sleep(200 * time.Millisecond)
	screen = capturePane(t, f.ui, "ui")
	if !strings.Contains(screen, "─────") {
		t.Errorf("`space` closed the picker, so ticking two hosts is impossible without `enter` "+
			"between them — the operator would have to save and reopen for every host:\n%s", screen)
	}
	// And verify the tick actually happened, so the case is not passing on a product that
	// lost space entirely: a ticked box somewhere on screen proves space was delivered.
	if !strings.Contains(screen, "[x]") {
		t.Errorf("`space` kept the picker open but did not tick anything, so this case may be "+
			"passing on a product where space does nothing:\n%s", screen)
	}
}

// All candidates being refused (e.g., all colliding with existing labels) still shows the
// LIST with each candidate's reason, rather than showing "nothing to show yet" or crashing.
//
// A candidate that is refused gets a row with a reason and no box (measured in
// TestE2EUIPickerRefusesACandidateWhoseLabelTheFleetOwns). But that test has ONE refused
// among two valid candidates. If ALL candidates are refused — the operator's ssh config
// names only hosts the fleet already has — the picker must still show the list with reasons,
// so they can see what collided and act on it (rename in ssh config, or drop the --host
// entry). A picker showing "nothing to show yet" in this case would read as "the hub cannot
// see your ssh config", which is false.
func TestE2EGAPPickerAllRefusedCandidatesStillShowTheList(t *testing.T) {
	f := pickerHub(t, pickerOpts{
		cols: 160, rows: 40, probeTimeout: "1ms",
		// Both candidates collide with the fixture's --host entry `scratch`, and the
		// second one collides with the local label too. So every row is refused.
		sshConfig: "Host scratch\nHost local\n",
		hostsTOML: pickerOneDisabledEntry,
	})
	pickerOpen(t, f.ui)

	body := pickerSqueezed(t, f.ui)
	// It must NOT say "nothing to show yet", because there ARE candidates — they are
	// refused, but the picker found them and the count line must say so.
	if strings.Contains(body, "nothing to show yet") {
		t.Errorf("the picker says 'nothing to show yet' when all candidates are refused, so it "+
			"reads as 'the hub cannot see your ssh config' — but the config has hosts, they "+
			"just collide:\n%s", body)
	}

	// The count line must say how many candidates were found, so the operator knows the
	// picker did read their config and every one of them had a problem.
	if !strings.Contains(body, "2 candidates in ~/.ssh/config") {
		t.Errorf("the picker does not say how many candidates it found when all are refused, "+
			"so the operator cannot tell 'refused everything' from 'found nothing':\n%s", body)
	}

	// And each candidate must be shown with its reason. `scratch` collides with a --host
	// entry, `local` collides with this machine's own server label.
	for _, pair := range [][2]string{
		{"scratch", "already given to a --host entry"},
		{"local", "taken by this machine's own server"},
	} {
		alias, fragment := pair[0], pair[1]
		if !strings.Contains(body, alias) {
			t.Errorf("candidate %q is not shown when it is refused, so the operator has no way "+
				"to learn that the picker saw it and why it cannot be enabled:\n%s", alias, body)
		}
		if !strings.Contains(body, fragment) {
			t.Errorf("the reason for refusing %q is not on screen, so the operator knows the host "+
				"is refused but not what to do about it:\n%s", alias, body)
		}
	}

	// And crucially: neither candidate has a tick box. With boxes, `enter` would write a
	// file the next startup refuses, from the screen meant to prevent exactly that.
	if strings.Contains(body, "[ ] scratch") || strings.Contains(body, "[x] scratch") {
		t.Errorf("the colliding candidate `scratch` has a tick box, so the picker will write a "+
			"hosts.toml that makes the next startup fail:\n%s", body)
	}
	if strings.Contains(body, "[ ] local") || strings.Contains(body, "[x] local") {
		t.Errorf("the colliding candidate `local` has a tick box, so the picker will write a "+
			"file with a duplicate label and exit 1:\n%s", body)
	}
}
