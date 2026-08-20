//go:build e2e

package e2e

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The door-attach journey: a session that exists in `claude agents` but has no pane, made
// reachable through `a`, with the row folding and the window named correctly.
//
// This is the workflow an operator actually performs when a background job needs their attention:
// see it on the dashboard, press `a` to wake it, work in it. The journey checks the STATE at each
// step (session exists, window created, verb ran) AND the SCREEN (does it explain what happened and
// what to do next).

// jdrWindowName reads the window name tmux assigned to a session, which is what the operator sees in
// `C-b w` and what §21.18 says must be "named after the row, not after the host".
func jdrWindowName(t *testing.T, socket, sessionName string) string {
	t.Helper()
	out, err := exec.Command("tmux", "-S", socket, "list-windows", "-t", sessionName,
		"-F", "#{window_name}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// TestJourneyDoorAttachFoldAndName is the complete door workflow: a pane-less background row on the
// dashboard, `a` to create the session, the row folding into one, and `a` again to attach. It
// checks the state AND the screen at every step, and reports where the screen failed to explain itself.
// EVERY read here names the hub's OWN WINDOW ("ui:0") rather than its session.
//
// `a` on a row of another server takes the WINDOW path: the hub opens a window in its own session for
// the attach, and tmux makes that window ACTIVE — so `capture-pane -t ui` follows the active window and
// returns the woken session's screen. The first version of this journey waited 25 seconds for the row
// to fold while reading the attach, and its own diagnostic said so: `the hub's last line:
// "[e2e-door-0:e2e-door*"`. Naming the window is the whole fix.
func TestJourneyDoorAttachFoldAndName(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed; the door needs it to verify the fold")
	}
	f := doorFleet(t, "blocked")
	f.start(t, 120, 40)

	wantSession := doorAgentName + "-" + doorAgentID

	// STEP 1: The pane-less row is on the dashboard.
	//
	// The screen should show the row with its state, and the operator needs to know what `a` will do.
	// This is the BEFORE state: one row, kind "background", no pane id.
	screen := capturePane(t, f.ui, "ui:0")
	if !strings.Contains(screen, doorAgentName) {
		t.Fatalf("the pane-less row %q is not on the dashboard:\n%s", doorAgentName, screen)
	}
	if !strings.Contains(screen, "background") {
		t.Errorf("the row does not show its kind as 'background', so the operator cannot tell it has no pane")
	}

	// Count the LIST ROWS mentioning this agent before the door runs — not the occurrences on the
	// screen, which include the details tile's own header for the focused row.
	beforeRows := jdrCountRows(screen, doorAgentName)
	if beforeRows != 1 {
		t.Errorf("before the door: %d rows mention %q, want exactly 1 (the pane-less row)",
			beforeRows, doorAgentName)
	}

	// STEP 2: Press `a` to wake it.
	//
	// The operator needs to know this keystroke will create a session and may spend tokens (though
	// "blocked" sessions are free). The existing door tests check the dialog for "failed" rows; this
	// journey checks the immediate-action path.
	jdrWalkTo(t, f.ui, doorAgentName)
	jdrKeys(t, f.ui, "a")

	// STEP 3: The session was created and the verb ran.
	//
	// This is STATE: tmux sessions exist on the watched server, and the fake claude recorded the verb.
	f.waitForSession(t, wantSession)

	var attachedVerb string
	f.waitOr(t, "the fake claude to record an attach", 20*time.Second, func() bool {
		for _, v := range f.verbs(t) {
			if strings.HasPrefix(v, "attach ") {
				attachedVerb = v
				return true
			}
		}
		return false
	})
	if attachedVerb != "attach "+doorAgentID {
		t.Errorf("the verb ran as %q, want 'attach %s' (short id only)", attachedVerb, doorAgentID)
	}

	// STEP 4: The row FOLDED — one conversation is one row.
	//
	// Before `a`, the dashboard showed a pane-less row. After the door creates the pane, the next
	// agents poll sees BOTH the agent (from `claude agents`) and the pane (from tmux), and the dedup
	// folds them into one row. The screen should now show exactly one row for this conversation, and
	// it should identify as a PANE (with a pane id) rather than as "background".
	//
	// This is the load-bearing assertion the existing door tests do not make: they check the STATE
	// (one session created) but not the SCREEN (one row drawn).
	f.waitOr(t, "the row to fold after the agents poll", 25*time.Second, func() bool {
		s, err := paneScreen(t, f.ui, "ui:0")
		if err != nil {
			return false
		}
		// The fold happened if:
		// 1. The name still appears (the conversation is still on screen)
		// 2. It appears exactly once (not twice: one pane-less + one pane)
		// 3. The kind "background" is gone (it's now a pane row)
		count := jdrCountRows(s, doorAgentName)
		hasBackground := strings.Contains(s, "background")
		return count == 1 && !hasBackground
	})

	afterScreen := capturePane(t, f.ui, "ui:0")
	// Per ROW, not per occurrence: the details tile draws the focused row's name in its own header,
	// the woken tmux session is called `<name>-<short id>` and CONTAINS the name, and the footer's
	// note can name it too — three ways for one row to be counted four times, which is what this
	// assertion reported before it counted rows.
	afterRows := jdrCountRows(afterScreen, doorAgentName)
	if afterRows != 1 {
		// PRINT the rows, because a count alone cannot say WHICH extra row it found — and the answer
		// decides whether the product or the fixture is wrong. The first version of this journey
		// reported four without naming one of them.
		t.Errorf("after the fold: %d list rows mention %q, want exactly 1 (the folded row); the rows "+
			"were:\n  %s", afterRows, doorAgentName,
			strings.Join(jdrFleetRows(afterScreen), "\n  "))
	}
	if strings.Contains(afterScreen, "background") {
		t.Errorf("after the fold: the row still shows kind 'background', but it should now identify " +
			"as a pane row with a pane id, because the door created a pane")
	}

	// UX CHECK: Does the screen say what happened?
	//
	// The operator pressed `a`, something ran, and the row changed shape. Did the hub say "launched:",
	// "attached to:", or name the created session? The note line is where such confirmations go.
	// This check is reported as a UX finding, not a test failure, because the session exists and
	// works — the question is whether the screen explained it.
	if !strings.Contains(afterScreen, "launched:") &&
		!strings.Contains(afterScreen, wantSession) &&
		!strings.Contains(afterScreen, "attached") {
		t.Logf("UX: after pressing `a`, the screen does not confirm what happened (no 'launched:', "+
			"no session name %q, no 'attached'). The operator sees the row change shape but no "+
			"sentence saying the door created a session.", wantSession)
	}

	// STEP 5: The window is named after the row, not after the host.
	//
	// §21.18 says "the window in `C-b w` is named after the row", so an operator working inside it
	// sees a useful name rather than "scratch" (the host alias). The window name is separate from the
	// session name: the session is `<name>-<id>`, the window should be `<name>`.
	windowName := jdrWindowName(t, f.target, wantSession)
	if windowName == "" {
		t.Errorf("the created session %q has no window, or list-windows failed", wantSession)
	} else if windowName == "scratch" {
		t.Errorf("the window is named %q (the host alias), but §21.18 says it must be named after "+
			"the row (%q), so the operator sees a useful name in `C-b w`", windowName, doorAgentName)
	} else if !strings.Contains(windowName, doorAgentName) && windowName != doorAgentName {
		t.Errorf("the window is named %q, which does not contain the row's name %q; "+
			"want the window named after the row", windowName, doorAgentName)
	}

	// STEP 6: Press `a` again — it attaches to the SAME session, not creating a second one.
	//
	// The operator is now looking at the dashboard (or they're inside the session; both are valid
	// outcomes after the first `a`). Pressing `a` again must attach to the existing session rather
	// than creating a second one. The STATE assertion is that only one session with this id exists.
	// The SCREEN assertion is that the operator ends up inside it.
	jdrWalkTo(t, f.ui, doorAgentName)
	jdrKeys(t, f.ui, "a")

	// The operator should end up inside the session, seeing the verb's output.
	f.waitOr(t, "the operator to be inside the woken session after the second `a`", 20*time.Second,
		func() bool {
			s, err := paneScreen(t, f.ui, "ui:0")
			return err == nil && strings.Contains(s, "WOKE-"+doorAgentID)
		})

	// And only ONE session with this id exists, not two.
	time.Sleep(2 * time.Second) // long enough for a second create to have landed, if it were broken
	sessionCount := 0
	for _, s := range f.sessions(t) {
		if strings.Contains(s, doorAgentID) {
			sessionCount++
		}
	}
	if sessionCount != 1 {
		t.Errorf("after two `a` presses: %d sessions contain the id %q, want exactly 1; "+
			"the second `a` should attach to the existing session, not create a new one:\n%v",
			sessionCount, doorAgentID, f.sessions(t))
	}

	// UX CHECK: Does the screen explain how to get back?
	//
	// The operator is now inside the session, which is where they wanted to be. But do they know how
	// to return to the dashboard? This is reported as a finding if no clue is visible.
	finalScreen := capturePane(t, f.ui, "ui:0")
	if !strings.Contains(finalScreen, "detach") && !strings.Contains(finalScreen, "C-b d") &&
		!strings.Contains(finalScreen, "prefix d") {
		t.Logf("UX: the operator is inside the session and the screen shows no hint about how to " +
			"return to the hub (no 'detach', 'C-b d', or 'prefix d'). A first-time user may not know " +
			"the tmux detach key.")
	}
}

// TestJourneyDoorAttachWithConfirmation is the workflow for a row that costs tokens: the `failed`
// state asks first, and the operator can cancel or proceed.
//
// This journey is shorter than the fold journey because the confirmation is already well-tested in
// the unit door cases. What matters here is the SCREEN: does the dialog explain what it's asking,
// does cancelling say what happened, and does proceeding lead to the same fold as the blocked case.
func TestJourneyDoorAttachWithConfirmation(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed")
	}
	f := doorFleet(t, "failed")
	f.start(t, 120, 40)

	wantSession := doorAgentName + "-" + doorAgentID

	// STEP 1: The row is on the dashboard, and pressing `a` opens a dialog.
	jdrWalkTo(t, f.ui, doorAgentName)
	jdrKeys(t, f.ui, "a")

	screenHas(t, f.ui, "wake "+doorAgentName,
		"the dialog must name the session it's asking about")
	screenHas(t, f.ui, "failed",
		"the dialog must quote the listing's own state word, so the operator knows why it's asking")

	dialogScreen := capturePane(t, f.ui, "ui:0")

	// UX CHECK: Does the dialog explain the cost?
	if !strings.Contains(dialogScreen, "spend") && !strings.Contains(dialogScreen, "token") &&
		!strings.Contains(dialogScreen, "cost") {
		t.Logf("UX: the dialog for a 'failed' row does not mention spending tokens or cost. " +
			"The operator may not understand why this row needs confirmation when 'blocked' rows did not.")
	}

	// UX CHECK: Does it say how to cancel?
	if !strings.Contains(dialogScreen, "any other key") && !strings.Contains(dialogScreen, "esc") &&
		!strings.Contains(dialogScreen, "cancel") {
		t.Logf("UX: the dialog does not explicitly say how to cancel (no 'any other key', 'esc', or " +
			"'cancel'). The operator may not know they can back out.")
	}

	// STEP 2: Cancel by pressing a key that is not enter.
	jdrKeys(t, f.ui, "x")
	screenHas(t, f.ui, "left "+doorAgentName+" alone",
		"after cancelling, the screen must confirm nothing was created")

	// Verify nothing was created.
	for _, s := range f.sessions(t) {
		if strings.Contains(s, doorAgentID) {
			t.Errorf("a cancelled wake created session %q anyway", s)
		}
	}

	// STEP 3: Try again, this time confirming with enter.
	jdrWalkTo(t, f.ui, doorAgentName)
	jdrKeys(t, f.ui, "a")
	screenHas(t, f.ui, "wake "+doorAgentName, "the dialog must reappear")
	jdrKeys(t, f.ui, "Enter")

	// The session should be created and the fold should happen, same as the blocked journey.
	f.waitForSession(t, wantSession)

	f.waitOr(t, "the row to fold after confirming", 25*time.Second, func() bool {
		s, err := paneScreen(t, f.ui, "ui:0")
		if err != nil {
			return false
		}
		count := jdrCountRows(s, doorAgentName)
		hasBackground := strings.Contains(s, "background")
		return count == 1 && !hasBackground
	})

	screenAfter := capturePane(t, f.ui, "ui:0")
	if jdrCountRows(screenAfter, doorAgentName) != 1 {
		// Per ROW: the tile's header, the woken session's `<name>-<short id>` and the footer's note
		// all carry the name, so an occurrence count says four for one row.
		t.Errorf("after confirming and folding: the row appears %d times in the list, want 1",
			jdrCountRows(screenAfter, doorAgentName))
	}
}

// jdrLooksPaneLess returns true if the line looks like a pane-less row: it contains "background" or
// "interactive" in the command column. Copied from tui_test.go's looksPaneLess, with the jdr prefix
// to avoid a package-level name collision.
func jdrLooksPaneLess(line string) bool {
	return strings.Contains(line, "background") || strings.Contains(line, "interactive")
}

// jdrFleetRows are the LIST rows of a capture, with the details tile and the chrome dropped.
//
// It exists because counting a name across the whole screen cannot answer "how many ROWS mention it":
// the details tile draws the SAME name in its own header for the focused row, so this journey reported
// "2 rows mention e2e-door, want exactly 1" against a screen that was correct — the repo's own rule
// that a Contains over a whole screen cannot test a per-surface property, one wave later.
//
// A fleet row is recognised the way cursorRow recognises the cursor's: it begins with the point column
// and carries a state glyph. The tile's box lines, the header, the separator and the footer are all
// dropped by that test.
func jdrFleetRows(screen string) []string {
	var out []string
	for _, l := range strings.Split(screen, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "┌") || strings.HasPrefix(strings.TrimSpace(l), "│") ||
			strings.HasPrefix(strings.TrimSpace(l), "└") {
			continue
		}
		if strings.ContainsAny(l, "⚑✗▸·✱?✓✝") && (strings.HasPrefix(l, ">") || strings.HasPrefix(l, " ")) {
			out = append(out, strings.TrimRight(l, " "))
		}
	}
	return out
}

// jdrCountRows is how many LIST rows mention a name.
func jdrCountRows(screen, name string) int {
	n := 0
	for _, r := range jdrFleetRows(screen) {
		if strings.Contains(r, name) {
			n++
		}
	}
	return n
}

// jdrKeys sends keys to the hub's OWN WINDOW. `send` names the session, which after an attach means
// the attach: this journey's first version typed sixty `j` presses into the woken session and then
// reported that the cursor never moved.
func jdrKeys(t *testing.T, ui string, keys ...string) {
	t.Helper()
	args := append([]string{"-S", ui, "send-keys", "-t", "ui:0"}, keys...)
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		t.Fatalf("send-keys %v: %v: %s", keys, err, out)
	}
	time.Sleep(250 * time.Millisecond)
}

// jdrWalkTo moves the cursor with `j` on the hub's own window until its row matches want.
func jdrWalkTo(t *testing.T, ui, want string) {
	t.Helper()
	for i := 0; i < 60; i++ {
		for _, l := range strings.Split(capturePane(t, ui, "ui:0"), "\n") {
			if strings.HasPrefix(l, ">") && strings.Contains(l, want) {
				return
			}
		}
		jdrKeys(t, ui, "j")
	}
	t.Fatalf("the cursor never reached a row containing %q in 60 presses on the hub's own window:\n%s",
		want, capturePane(t, ui, "ui:0"))
}
