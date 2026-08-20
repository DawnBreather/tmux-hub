//go:build e2e

package e2e

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// THE KILL-CLEANUP JOURNEY: finish with a session and have the fleet agree.
//
// Launch/seed a pane → mark it → K → dialog names what is running → cancel: pane still there on the
// server → K again → enter → pane is gone from the server and from the list, the hub says what it
// did, and the fleet count on the header went down by one.
//
// This is the operator's end-of-life flow: decide a session is done, kill it from the hub, and the
// fleet shrinks. Every step asserts both the STATE (what exists on the watched tmux server) and the
// SCREEN (does the hub tell what happened and what can be done next).

// TestE2EJourneyKillCleanup drives the full kill flow: dialog → cancel → still there → dialog again
// → enter → gone from server, gone from list, footer reports it, header count decreased.
func TestE2EJourneyKillCleanup(t *testing.T) {
	ui, target, ids, _ := hubWith(t, 120, 40, 2, "cat")
	victim, survivor := ids[1], ids[0]

	// Wait for both panes to appear on screen.
	for _, id := range ids {
		waitUntil(t, "pane "+id+" to reach the screen", 30*time.Second, func() bool {
			s, err := paneScreen(t, ui, "ui")
			return err == nil && strings.Contains(s, id)
		})
	}

	// ── STEP 1: select the victim ─────────────────────────────────────────────────────────────────

	walkTo(t, ui, victim)
	send(t, ui, "space")
	waitUntil(t, "the footer to report one marked pane", 15*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && strings.Contains(s, "1 marked")
	})

	// ── STEP 2: press K, verify the dialog names the pane ─────────────────────────────────────────

	sendLiteral(t, ui, "K")
	screenHas(t, ui, "Confirm kill to 1 target:", "`K` must confirm before it kills")

	// The dialog must NAME what it is about to destroy. "Kill this?" with no subject is the failure
	// §7 forbids: it cannot tell the operator which window they are about to lose.
	//
	// Search the dialog's OWN lines, not the whole screen: the pane id appears three times (inbox
	// row, focused tile border, dialog), so a `Contains` over the capture would pass with the dialog
	// listing nothing at all.
	dialogScreen := capturePane(t, ui, "ui")
	foundVictim := false
	for _, l := range jklDialogLines(t, ui, "Confirm kill to") {
		if strings.Contains(l, victim) {
			foundVictim = true
			// The dialog should also name what is running (`cat` here).
			if !strings.Contains(l, "cat") {
				t.Errorf("the kill dialog names the pane but not what is running in it: %q", l)
			}
		}
	}
	if !foundVictim {
		t.Fatalf("the kill dialog does not list the pane it is about to destroy (%s):\n%s",
			victim, dialogScreen)
	}

	// ── STEP 3: cancel with Escape, verify pane still exists ──────────────────────────────────────

	send(t, ui, "Escape")
	time.Sleep(500 * time.Millisecond) // Give any leaked kill time to land

	// The SERVER is the assertion, not the screen: a dialog that paints after the act had already
	// fired would satisfy "the dialog appeared" and still have killed the pane.
	if !jklPaneAlive(t, target, victim) {
		t.Fatalf("cancelling the kill dialog destroyed pane %s anyway: it is gone from the watched "+
			"server\n%s", victim, capturePane(t, ui, "ui"))
	}

	// The screen should not say "killed" either, since we cancelled.
	if s := capturePane(t, ui, "ui"); strings.Contains(s, "killed") {
		t.Errorf("the hub reported killing a pane after the dialog was cancelled:\n%s", s)
	}

	// ── STEP 4: press K again, then Enter to confirm ──────────────────────────────────────────────

	sendLiteral(t, ui, "K")
	screenHas(t, ui, "Confirm kill to 1 target:", "`K` must confirm before it kills (second time)")

	send(t, ui, "Enter")

	// ── STEP 5: verify the pane is gone from the SERVER ───────────────────────────────────────────

	waitUntil(t, "the killed pane to be gone from the watched server", 20*time.Second, func() bool {
		return !jklPaneAlive(t, target, victim)
	})

	// ── STEP 6: verify the hub SAYS it killed the pane ────────────────────────────────────────────

	// The footer must report what happened: "killed 1 pane" or similar. A destructive act with no
	// answer is indistinguishable from a key that did nothing.
	screenHas(t, ui, "killed 1 pane", "the hub must report that it killed the pane")

	// ── STEP 7: verify the row shows the pane as gone ─────────────────────────────────────────────

	// The hub's own row must stop reading as LIVE. Measured in destructive tests: the row becomes
	// `✝ gone   %1   cat` and sorts to the bottom. What must not survive is the row reading `idle`
	// or `works` — an operator who sends to that row gets a refusal about a token instead of the
	// fact that the pane is gone.
	jklWaitScreen(t, ui, "the killed pane's row to be marked gone", 20*time.Second,
		func(s string) bool {
			for _, l := range jklInboxLines(t, ui) {
				if strings.Contains(l, victim+" ") && strings.Contains(l, "gone") {
					return true
				}
			}
			return false
		})

	// Verify it does NOT still show as live.
	for _, l := range jklInboxLines(t, ui) {
		if !strings.Contains(l, victim+" ") || strings.Contains(l, "gone") {
			continue
		}
		for _, live := range []string{"idle", "works", "needs", "quiet"} {
			if strings.Contains(l, live) {
				t.Errorf("the killed pane still has a row reading %q: %q — the operator would send "+
					"to a pane that no longer exists", live, l)
			}
		}
	}

	// ── STEP 8: verify the survivor is untouched ──────────────────────────────────────────────────

	// The neighbour pane must still exist. A kill that takes the pane next to the one named is the
	// worst outcome this dialog exists to prevent.
	if !jklPaneAlive(t, target, survivor) {
		t.Errorf("killing %s took %s with it — the operator loses a window they never named",
			victim, survivor)
	}
}

// ── helpers for this journey ───────────────────────────────────────────────────────────────────

// jklPaneAlive checks if a pane exists on the watched server.
func jklPaneAlive(t *testing.T, target, paneID string) bool {
	t.Helper()
	out, err := exec.Command("tmux", "-S", target, "list-panes", "-a", "-F", "#{pane_id}").Output()
	if err != nil {
		// A server with no sessions left exits, and `list-panes` then fails. That is an answer
		// about the world ("nothing is alive"), not a broken harness.
		return false
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(l) == paneID {
			return true
		}
	}
	return false
}

// jklDialogLines returns the confirmation dialog's OWN lines: everything from the line that opens
// with `head` down to the key line that closes it.
//
// It exists because the dashboard is still drawn underneath. A pane id appears in the inbox row, in
// the focused tile's border and in the dialog, so a `Contains` over the whole capture is satisfied
// by a dialog that names nothing.
func jklDialogLines(t *testing.T, ui, head string) []string {
	t.Helper()
	var out []string
	in := false
	for _, l := range strings.Split(capturePane(t, ui, "ui"), "\n") {
		if strings.Contains(l, head) {
			in = true
			continue
		}
		if !in {
			continue
		}
		if strings.Contains(l, "any other key: cancel") {
			break
		}
		out = append(out, l)
	}
	return out
}

// jklInboxLines is the INBOX COLUMN alone, which is the surface the row lives on.
//
// It exists because a captured line is not a row. Above 100 columns the dashboard puts the inbox in
// a 28-column left column and a tile beside it, so one captured line holds one row's text AND a
// slice of an unrelated pane's tile. Any per-line rule about a row must slice the surface first.
func jklInboxLines(t *testing.T, ui string) []string {
	t.Helper()
	const inboxWidth = 28 // internal/ui.InboxWidth
	var out []string
	for _, l := range strings.Split(capturePane(t, ui, "ui"), "\n") {
		r := []rune(l)
		if len(r) > inboxWidth {
			r = r[:inboxWidth]
		}
		out = append(out, string(r))
	}
	return out
}

// jklWaitScreen waits for the dashboard to satisfy pred and DUMPS the screen when it never does.
// waitUntil names the fact and not the frame, and every question in this file is answered by
// looking at what the operator is looking at.
func jklWaitScreen(t *testing.T, ui, what string, timeout time.Duration, pred func(string) bool) {
	t.Helper()
	last := ""
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s, err := paneScreen(t, ui, "ui"); err == nil {
			last = s
			if pred(s) {
				return
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s\n%s", timeout, what, last)
}
