package ui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/broadcast"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// mockRunner records tmux commands for lifecycle test verification.
// It implements tmux.Exec (both Runner and InputRunner).
type mockRunner struct {
	last string // The last command run, for test assertions
}

func (r *mockRunner) Run(_ context.Context, _ tmux.Target, args ...string) (tmux.Result, error) {
	r.last = strings.Join(args, " ")
	return tmux.Result{RC: 0}, nil
}

func (r *mockRunner) RunInput(_ context.Context, _ tmux.Target, _ []byte, args ...string) (tmux.Result, error) {
	r.last = strings.Join(args, " ")
	return tmux.Result{RC: 0}, nil
}

func TestRestartResumesTheSameConversation(t *testing.T) {
	// Measured: --session-id on an existing id exits 1 with
	// `Session ID <uuid> is already in use.` So continuity is --resume, and a
	// restart that reached for --session-id again would kill the pane it just
	// made.
	r := &mockRunner{}
	k := broadcast.NewKeeper(broadcast.NewStamper(r, broadcast.Instance("test")))
	k.Adopt("local", "%3", "7007b23f-1599-4efa-81c5-4195621cc273")

	m := model{
		run:    r,
		keeper: k,
		ctx:    context.Background(),
		panes: []registry.Pane{
			{Host: "local", PaneID: "%3", Session: "$0", Window: "@1"},
		},
	}
	m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%3"})

	// Press R to restart
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")})
	after := got.(model)

	// Execute the command (which does the actual restart)
	if cmd != nil {
		msg := cmd()
		got2, _ := after.Update(msg)
		after = got2.(model)
	}

	// The restart command must use --resume
	if !strings.Contains(r.last, "--resume 7007b23f-1599-4efa-81c5-4195621cc273") {
		t.Fatalf("restart must resume, got %q", r.last)
	}
	if strings.Contains(r.last, "--session-id") {
		t.Fatalf("restart must NOT dictate an id again: %q", r.last)
	}

	// Must call respawn-pane -k
	if !strings.Contains(r.last, "respawn-pane") || !strings.Contains(r.last, "-k") {
		t.Fatalf("restart must call respawn-pane -k, got %q", r.last)
	}

	// Identity must be invalidated after respawn
	if k.Identified("local", "%3") {
		t.Fatal("a respawned pane must be re-identified, not inherited")
	}

	// The note should indicate restart happened
	if !strings.Contains(after.note, "restart") {
		t.Errorf("restart should report in note, got %q", after.note)
	}
}

func TestRestartInvalidatesIdentityBecauseTheSTAMPSURVIVES(t *testing.T) {
	// Measured: respawn-pane -k keeps pane_id AND the @hub_* option, and
	// changes pane_pid. So the guarded write path still trusts the pane while
	// the process behind it is a different one. Identity must be dropped
	// explicitly; nothing about the pane will tell us.
	r := &mockRunner{}
	k := broadcast.NewKeeper(broadcast.NewStamper(r, broadcast.Instance("test")))
	k.Adopt("local", "%3", "session-uuid")

	m := model{
		run:    r,
		keeper: k,
		ctx:    context.Background(),
		panes: []registry.Pane{
			{Host: "local", PaneID: "%3", Session: "$0", Window: "@1"},
		},
	}
	m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%3"})

	// Verify it's identified before restart
	if !k.Identified("local", "%3") {
		t.Fatal("pane must be identified before restart for this test")
	}

	// Press R to restart
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")})
	after := got.(model)

	// Execute the command
	if cmd != nil {
		msg := cmd()
		got2, _ := after.Update(msg)
		after = got2.(model)
	}

	// Identity must be dropped
	if k.Identified("local", "%3") {
		t.Fatal("a respawned pane must be re-identified, not inherited")
	}
}

func TestAFailedResumeIsALIVEPaneShowingAPickerNotADeadOne(t *testing.T) {
	// Measured: `claude --resume <nonexistent>` does NOT exit. pane_dead=0, no
	// status, and the pane draws an interactive session picker and waits. So the
	// hub must not report a failed restart by looking for an exit code — there
	// isn't one. The honest report is that the pane is alive and the hub no
	// longer knows what it is, i.e. identity was dropped and not re-established.

	// This is verified by the identity invalidation test above - after a restart,
	// identity is dropped. The next poll will see the pane is alive but not
	// identified, which is the correct state for a failed resume.
	r := &mockRunner{}
	k := broadcast.NewKeeper(broadcast.NewStamper(r, broadcast.Instance("test")))
	k.Adopt("local", "%3", "session-uuid")

	m := model{
		run:    r,
		keeper: k,
		ctx:    context.Background(),
		panes: []registry.Pane{
			{Host: "local", PaneID: "%3", Session: "$0", Window: "@1", Dead: false},
		},
	}
	m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%3"})

	// Do restart
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")})
	after := got.(model)

	// Execute the command
	if cmd != nil {
		msg := cmd()
		got2, _ := after.Update(msg)
		after = got2.(model)
	}

	// Identity is dropped
	if k.Identified("local", "%3") {
		t.Fatal("after restart, identity must be dropped")
	}

	// The hub will not report this pane as identified on next poll
	// A failed resume leaves pane_dead=0 (alive) with no identification
}

func TestKillAlwaysConfirmsAndTheDialogNAMESWhatIsRunning(t *testing.T) {
	// "Kill this?" with no subject is how the wrong window dies.
	r := &mockRunner{}
	k := broadcast.NewKeeper(broadcast.NewStamper(r, broadcast.Instance("test")))
	k.Adopt("local", "%3", "session-uuid")

	m := model{
		run:    r,
		keeper: k,
		ctx:    context.Background(),
		panes: []registry.Pane{
			{Host: "local", PaneID: "%3", Session: "$0", Window: "api", Command: "claude"},
		},
		width:  80,
		height: 24,
	}
	m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%3"})

	// Press K to kill
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("K")})
	after := got.(model)

	// Must enter confirm mode
	if after.mode != modeConfirm {
		t.Fatalf("K must confirm, got mode=%v", after.mode)
	}

	// The confirmation must have reasons
	if len(after.pending) == 0 {
		t.Fatal("kill confirmation must have reasons")
	}

	// Render the view to check the subject is named
	out := after.View()
	if !strings.Contains(out, "api") || !strings.Contains(out, "claude") {
		t.Fatalf("the confirmation must name its subject:\n%s", out)
	}
}

func TestKillingADeadPaneStillConfirmsButSaysNothingIsRunning(t *testing.T) {
	// A dead pane is the common case for cleanup. The dialog stays — one habit,
	// not two — but it must not imply work is at risk.
	r := &mockRunner{}
	k := broadcast.NewKeeper(broadcast.NewStamper(r, broadcast.Instance("test")))

	m := model{
		run:    r,
		keeper: k,
		ctx:    context.Background(),
		panes: []registry.Pane{
			{Host: "local", PaneID: "%3", Session: "$0", Window: "@1",
				Dead: true, DeadStatus: 0},
		},
	}
	m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%3"})

	// Press K
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("K")})
	after := got.(model)

	// Must confirm
	if after.mode != modeConfirm {
		t.Fatal("K must confirm even for dead panes")
	}

	// Must have a reason that indicates nothing is running
	hasDeadReason := false
	for _, r := range after.pending {
		s := string(r)
		if strings.Contains(s, "dead") || strings.Contains(s, "nothing") || strings.Contains(s, "exited") {
			hasDeadReason = true
			break
		}
	}
	if !hasDeadReason {
		t.Errorf("kill confirmation for dead pane must say nothing is running, got %v", after.pending)
	}
}

func TestEscapeDuringAKillConfirmationKillsNothing(t *testing.T) {
	r := &mockRunner{}
	k := broadcast.NewKeeper(broadcast.NewStamper(r, broadcast.Instance("test")))

	m := model{
		run:    r,
		keeper: k,
		ctx:    context.Background(),
		panes: []registry.Pane{
			{Host: "local", PaneID: "%3", Session: "$0", Window: "@1"},
		},
		mode:       modeConfirm,
		pendingAct: actionKill,
		pending:    []broadcast.Reason{broadcast.ReasonAgentRunning},
	}
	m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%3"})

	// Press Esc
	got, _ := m.confirmKey(tea.KeyMsg{Type: tea.KeyEsc})
	after := got.(model)

	// Must return to browse
	if after.mode != modeBrowse {
		t.Fatal("esc must dismiss confirmation")
	}

	// Must not have run any kill command
	if strings.Contains(r.last, "kill") {
		t.Fatalf("esc must not kill, but ran %q", r.last)
	}

	// Note should say cancelled
	if !strings.Contains(after.note, "cancel") {
		t.Errorf("note should indicate cancellation, got %q", after.note)
	}
}

func TestRestartRequiresSelection(t *testing.T) {
	r := &mockRunner{}
	k := broadcast.NewKeeper(broadcast.NewStamper(r, broadcast.Instance("test")))

	m := model{
		run:    r,
		keeper: k,
		ctx:    context.Background(),
		panes: []registry.Pane{
			{Host: "local", PaneID: "%3", Session: "$0", Window: "@1"},
		},
		// No selection
	}

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")})
	after := got.(model)

	// Must report need for selection
	if !strings.Contains(after.note, "select") {
		t.Errorf("R with no selection must report that, got note=%q", after.note)
	}

	// Must not have run respawn
	if strings.Contains(r.last, "respawn") {
		t.Fatalf("R with no selection must not respawn, but ran %q", r.last)
	}
}

func TestKillRequiresSelection(t *testing.T) {
	r := &mockRunner{}
	k := broadcast.NewKeeper(broadcast.NewStamper(r, broadcast.Instance("test")))

	m := model{
		run:    r,
		keeper: k,
		ctx:    context.Background(),
		panes: []registry.Pane{
			{Host: "local", PaneID: "%3", Session: "$0", Window: "@1"},
		},
		// No selection
	}

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("K")})
	after := got.(model)

	// Must report need for selection
	if !strings.Contains(after.note, "select") {
		t.Errorf("K with no selection must report that, got note=%q", after.note)
	}

	// Must not confirm
	if after.mode == modeConfirm {
		t.Fatal("K with no selection must not open confirmation")
	}
}

func TestRestartWithNoSessionIDReportsIt(t *testing.T) {
	r := &mockRunner{}
	k := broadcast.NewKeeper(broadcast.NewStamper(r, broadcast.Instance("test")))
	// Pane %3 is NOT adopted, so keeper has no session for it

	m := model{
		run:    r,
		keeper: k,
		ctx:    context.Background(),
		panes: []registry.Pane{
			{Host: "local", PaneID: "%3", Session: "$0", Window: "@1"},
		},
	}
	m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%3"})

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")})
	after := got.(model)

	// Must report that there's no session
	if !strings.Contains(after.note, "session") && !strings.Contains(after.note, "identified") {
		t.Errorf("R on unidentified pane must report no session, got note=%q", after.note)
	}

	// Must not have run respawn
	if strings.Contains(r.last, "respawn") {
		t.Fatalf("R with no session must not respawn, but ran %q", r.last)
	}
}

// C2 test: K kills the PANE, not the window

func TestKKillsTheSelectedPaneNotTheWindow(t *testing.T) {
	// C2: killSelected must call kill-pane targeting the selected pane's id,
	// not kill-window targeting the window. A window holding a selected pane
	// and a second unselected pane must only kill the selected one.
	r := &mockRunner{}
	k := broadcast.NewKeeper(broadcast.NewStamper(r, broadcast.Instance("test")))

	// Window @1 holds two panes: %3 (selected) and %4 (not selected)
	m := model{
		run:    r,
		keeper: k,
		ctx:    context.Background(),
		panes: []registry.Pane{
			{Host: "local", PaneID: "%3", Session: "$0", Window: "@1", Command: "vim"},
			{Host: "local", PaneID: "%4", Session: "$0", Window: "@1", Command: "claude"},
		},
		width:  80,
		height: 24,
	}
	// Select only %3
	m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%3"})

	// Press K to kill
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("K")})
	after := got.(model)

	// Must enter confirm mode
	if after.mode != modeConfirm {
		t.Fatalf("K must confirm, got mode=%v", after.mode)
	}

	// Press enter to confirm
	got, cmd := after.confirmKey(tea.KeyMsg{Type: tea.KeyEnter})
	after = got.(model)

	// Execute the kill command
	if cmd != nil {
		msg := cmd()
		got, _ = after.Update(msg)
		after = got.(model)
	}

	// Must have issued kill-pane, not kill-window
	if !strings.Contains(r.last, "kill-pane") {
		t.Errorf("K must issue kill-pane, got %q", r.last)
	}
	if strings.Contains(r.last, "kill-window") {
		t.Errorf("K must NOT issue kill-window, got %q", r.last)
	}

	// Must target the selected pane's id (%3), not the window (@1)
	if !strings.Contains(r.last, "%3") {
		t.Errorf("kill-pane must target the selected pane %%3, got %q", r.last)
	}
	if strings.Contains(r.last, "@1") {
		t.Errorf("kill-pane must NOT target the window @1, got %q", r.last)
	}
}
