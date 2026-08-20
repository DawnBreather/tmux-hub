//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/broadcast"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// guardListPanes returns all pane IDs on a server.
func guardListPanes(t *testing.T, socket string) []string {
	t.Helper()
	cmd := exec.Command("tmux", "-S", socket, "list-panes", "-aF", "#{pane_id}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list-panes: %v: %s", err, out)
	}
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if id := strings.TrimSpace(line); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// guardPanePID returns the pane_pid of a pane.
func guardPanePID(t *testing.T, socket, paneID string) int {
	t.Helper()
	cmd := exec.Command("tmux", "-S", socket, "display", "-p", "-t", paneID, "#{pane_pid}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("display pane_pid for %s: %v: %s", paneID, err, out)
	}
	var pid int
	if _, serr := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &pid); serr != nil {
		t.Fatalf("parse pane_pid: %v", serr)
	}
	return pid
}

// guardSetOption sets a pane option.
func guardSetOption(t *testing.T, socket, paneID, option, value string) {
	t.Helper()
	cmd := exec.Command("tmux", "-S", socket, "set", "-p", "-t", paneID, option, value)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("set -p %s %s: %v: %s", paneID, option, err, out)
	}
}

// guardGetOption retrieves a pane option value.
func guardGetOption(t *testing.T, socket, paneID, option string) string {
	t.Helper()
	cmd := exec.Command("tmux", "-S", socket, "display", "-p", "-t", paneID, "#{"+option+"}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("display #{%s} for %s: %v: %s", option, paneID, err, out)
	}
	return strings.TrimSpace(string(out))
}

// guardGetEpoch returns the server's epoch (pid:start_time).
func guardGetEpoch(t *testing.T, socket, paneID string) string {
	t.Helper()
	cmd := exec.Command("tmux", "-S", socket, "display", "-p", "-t", paneID,
		"#{pid}:#{start_time}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("display epoch for %s: %v: %s", paneID, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestE2EGuardRefusesUnstampedPane verifies that a send to a pane that was never
// stamped is refused, and the payload does not appear in ANY pane on that server.
//
// Catches: a missing token check that would deliver prompts to arbitrary panes.
func TestE2EGuardRefusesUnstampedPane(t *testing.T) {
	sock, panes := liveServer(t, 1)
	paneID := panes[0]
	tgt := tmux.Target{Label: "test", Socket: sock}

	inst := broadcast.NewInstance()
	run := tmux.NewExec(5 * time.Second)
	st := broadcast.NewStamper(run, inst)
	sender := broadcast.NewSender(run, st, inst)

	payload := "echo 'test payload that must not appear anywhere'"
	target := broadcast.Target{Host: tgt.Label, Tmux: tgt, PaneID: paneID}

	res, err := sender.Send(context.Background(), target, payload)
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}

	if res.Outcome != broadcast.Refused {
		t.Errorf("outcome = %v, want Refused: the hub holds no token for this unstamped pane", res.Outcome)
	}

	// Verify the payload did not leak into ANY pane on the server.
	for _, pid := range panes {
		screen := capturePane(t, sock, pid)
		if strings.Contains(screen, "test payload") {
			t.Errorf("payload appeared in pane %s despite refusal: %s", pid, screen)
		}
	}

	// Verify no leaked paste buffer.
	buffers := listBuffers(t, sock)
	if len(buffers) > 0 {
		t.Errorf("found %d leaked buffers after refused send: %v", len(buffers), buffers)
	}
}

// TestE2EGuardRefusesAfterAgentExits verifies that when the process exits between
// selection and send (so the pane becomes a shell), the guard refuses and the
// payload does not execute as a command.
//
// Catches: a time-of-check-time-of-use gap that would paste a prompt into a shell.
func TestE2EGuardRefusesAfterAgentExits(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	// Create a pane running bash, then start a child inside it. This way, when we kill
	// the child, the pane survives at a shell prompt — which is exactly the scenario we
	// need to guard against: a prompt pasted into a shell.
	sock := filepath.Join(t.TempDir(), "tmux.sock")
	mk := func(args ...string) {
		t.Helper()
		full := append([]string{"-S", sock, "-f", "/dev/null"}, args...)
		if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
	}

	// Start bash --norc as the pane command, then send a long-lived child into it.
	mk("new-session", "-d", "-s", "test", "-x", "80", "-y", "24", "bash", "--norc")
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })

	paneID := guardListPanes(t, sock)[0]

	// Start a child process inside bash (sleep, so it's long-lived and identifiable).
	sendKeys := func(keys string) {
		t.Helper()
		mk("send-keys", "-t", paneID, "-l", keys)
		mk("send-keys", "-t", paneID, "Enter")
	}
	sendKeys("sleep 3600 &")
	sendKeys("echo child-started")

	// Give bash time to start the child.
	time.Sleep(200 * time.Millisecond)

	// Find the child process: it's a descendant of #{pane_pid}.
	panePID := guardPanePID(t, sock, paneID)
	findChild := exec.Command("pgrep", "-P", fmt.Sprintf("%d", panePID), "sleep")
	childOut, err := findChild.Output()
	if err != nil {
		t.Fatalf("pgrep -P %d sleep: %v (the child sleep may not have started)", panePID, err)
	}
	var childPID int
	if _, serr := fmt.Sscanf(strings.TrimSpace(string(childOut)), "%d", &childPID); serr != nil {
		t.Fatalf("parse child pid: %v", serr)
	}
	if childPID == 0 {
		t.Fatal("child pid is 0")
	}

	tgt := tmux.Target{Label: "test", Socket: sock}
	inst := broadcast.NewInstance()
	run := tmux.NewExec(5 * time.Second)
	st := broadcast.NewStamper(run, inst)
	sender := broadcast.NewSender(run, st, inst)

	// Stamp the pane to establish identity (bash is the pane process, sleep is its child).
	tok, err := st.Stamp(context.Background(), tgt, paneID)
	if err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	if tok == "" {
		t.Fatal("Stamp returned empty token")
	}

	// Kill the CHILD (sleep), not bash. The pane survives at a shell prompt.
	if err := syscall.Kill(childPID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill child process %d: %v", childPID, err)
	}
	// Give tmux a moment to notice.
	time.Sleep(100 * time.Millisecond)

	// The token is still set on the pane option, but the "agent" (represented by the child)
	// is gone. In production, the keeper would unstamp after detecting the agent gone.
	// We simulate that here.

	// Unstamp to simulate Keeper's action after detecting agent exit.
	if err := st.Unstamp(context.Background(), tgt, paneID); err != nil {
		t.Fatalf("Unstamp: %v", err)
	}

	payload := "echo 'DANGER: executed in shell'"
	target := broadcast.Target{Host: tgt.Label, Tmux: tgt, PaneID: paneID}

	res, err := sender.Send(context.Background(), target, payload)
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}

	if res.Outcome != broadcast.Refused {
		t.Errorf("outcome = %v, want Refused after agent exit", res.Outcome)
	}

	// Verify the payload did not execute as a shell command.
	screen := capturePane(t, sock, paneID)
	if strings.Contains(screen, "DANGER: executed") {
		t.Errorf("payload executed as shell command despite guard refusal: %s", screen)
	}
}

// TestE2EGuardRefusesWhenPaneVanishes verifies that when a pane is killed between
// selection and send, the guard refuses and no paste buffer leaks.
//
// Catches: a failing guard that returns rc=0 with an error, reported as delivered.
func TestE2EGuardRefusesWhenPaneVanishes(t *testing.T) {
	sock, panes := liveServer(t, 2)
	tgt := tmux.Target{Label: "test", Socket: sock}
	targetPane := panes[1] // The second pane

	inst := broadcast.NewInstance()
	run := tmux.NewExec(5 * time.Second)
	st := broadcast.NewStamper(run, inst)
	sender := broadcast.NewSender(run, st, inst)

	// Stamp the target pane.
	tok, err := st.Stamp(context.Background(), tgt, targetPane)
	if err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	if tok == "" {
		t.Fatal("Stamp returned empty token")
	}

	// Kill the pane.
	killCmd := exec.Command("tmux", "-S", sock, "kill-pane", "-t", targetPane)
	if out, err := killCmd.CombinedOutput(); err != nil {
		t.Fatalf("kill-pane: %v: %s", err, out)
	}

	// Verify the pane is gone.
	remainingPanes := guardListPanes(t, sock)
	for _, p := range remainingPanes {
		if p == targetPane {
			t.Fatalf("pane %s still exists after kill-pane", targetPane)
		}
	}

	payload := "echo 'this should never arrive'"
	target := broadcast.Target{Host: tgt.Label, Tmux: tgt, PaneID: targetPane}

	res, err := sender.Send(context.Background(), target, payload)
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}

	if res.Outcome != broadcast.Refused {
		t.Errorf("outcome = %v, want Refused when pane vanishes", res.Outcome)
	}
	if res.Reason == "" {
		t.Error("Reason is empty; a refused send must name why")
	}

	// Verify no leaked paste buffer on the remaining pane.
	buffers := listBuffers(t, sock)
	if len(buffers) > 0 {
		t.Errorf("found %d leaked buffers after pane vanished: %v", len(buffers), buffers)
	}
}

// TestE2EGuardRefusesWhenTokenChangedOutOfBand verifies that when the pane's
// @hub_<instance> option is modified directly (not via stamper), the guard refuses.
//
// Catches: a token comparison that doesn't verify or uses the wrong field.
func TestE2EGuardRefusesWhenTokenChangedOutOfBand(t *testing.T) {
	sock, panes := liveServer(t, 1)
	paneID := panes[0]
	tgt := tmux.Target{Label: "test", Socket: sock}

	inst := broadcast.NewInstance()
	run := tmux.NewExec(5 * time.Second)
	st := broadcast.NewStamper(run, inst)
	sender := broadcast.NewSender(run, st, inst)

	// Stamp the pane.
	originalTok, err := st.Stamp(context.Background(), tgt, paneID)
	if err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	if originalTok == "" {
		t.Fatal("Stamp returned empty token")
	}

	// Verify the option is set.
	option := inst.Option()
	readTok := guardGetOption(t, sock, paneID, option)
	if readTok != originalTok {
		t.Fatalf("option %s = %q, want %q", option, readTok, originalTok)
	}

	// Change the token out of band.
	fakeToken := "fake-token-12345678"
	guardSetOption(t, sock, paneID, option, fakeToken)

	// Verify it changed.
	readTok = guardGetOption(t, sock, paneID, option)
	if readTok != fakeToken {
		t.Fatalf("option not changed: got %q, want %q", readTok, fakeToken)
	}

	payload := "echo 'must be refused'"
	target := broadcast.Target{Host: tgt.Label, Tmux: tgt, PaneID: paneID}

	res, err := sender.Send(context.Background(), target, payload)
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}

	if res.Outcome != broadcast.Refused {
		t.Errorf("outcome = %v, want Refused when token was changed out of band", res.Outcome)
	}
	if !strings.Contains(res.Reason, "different token") && !strings.Contains(res.Reason, "guard refused") {
		t.Errorf("reason = %q, should mention token mismatch", res.Reason)
	}

	// Verify payload didn't arrive.
	screen := capturePane(t, sock, paneID)
	if strings.Contains(screen, "must be refused") {
		t.Errorf("payload appeared despite token mismatch: %s", screen)
	}
}

// TestE2EGuardRefusesAfterServerRestart verifies that when the tmux server
// restarts (so pane IDs recycle and epoch changes), a stale selection does not
// deliver to the new %0.
//
// Catches: pane ID reuse after restart delivering to the wrong pane.
func TestE2EGuardRefusesAfterServerRestart(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := filepath.Join(t.TempDir(), "tmux.sock")

	// Start first server with cat pane.
	cmd := exec.Command("tmux", "-S", sock, "-f", "/dev/null",
		"new-session", "-d", "-s", "first", "-x", "80", "-y", "24", "cat")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("new-session (first): %v: %s", err, out)
	}

	tgt := tmux.Target{Label: "test", Socket: sock}
	paneID := guardListPanes(t, sock)[0]
	if paneID != "%0" {
		t.Logf("first pane is %s, not %%0 — test continues", paneID)
	}

	inst := broadcast.NewInstance()
	run := tmux.NewExec(5 * time.Second)
	st := broadcast.NewStamper(run, inst)
	sender := broadcast.NewSender(run, st, inst)

	// Stamp and record epoch.
	tok, err := st.Stamp(context.Background(), tgt, paneID)
	if err != nil {
		t.Fatalf("Stamp (first server): %v", err)
	}
	firstEpoch := guardGetEpoch(t, sock, paneID)
	if firstEpoch == "" {
		t.Fatal("first epoch is empty")
	}
	t.Logf("first server: pane=%s token=%s epoch=%s", paneID, tok, firstEpoch)

	// Kill the server.
	killCmd := exec.Command("tmux", "-S", sock, "kill-server")
	if out, err := killCmd.CombinedOutput(); err != nil {
		t.Fatalf("kill-server: %v: %s", err, out)
	}

	// Wait for server to be gone.
	time.Sleep(100 * time.Millisecond)

	// Start a new server on the same socket with cat.
	cmd2 := exec.Command("tmux", "-S", sock, "-f", "/dev/null",
		"new-session", "-d", "-s", "second", "-x", "80", "-y", "24", "cat")
	if out, err := cmd2.CombinedOutput(); err != nil {
		t.Fatalf("new-session (second): %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })

	newPanes := guardListPanes(t, sock)
	if len(newPanes) == 0 {
		t.Fatal("no panes in new server")
	}
	newPaneID := newPanes[0]
	secondEpoch := guardGetEpoch(t, sock, newPaneID)

	t.Logf("second server: pane=%s epoch=%s", newPaneID, secondEpoch)

	// Verify epoch changed.
	if secondEpoch == firstEpoch {
		t.Fatalf("epoch did not change: first=%s second=%s", firstEpoch, secondEpoch)
	}

	// The stamper still holds the token for the OLD paneID. The new server has no
	// such option set. Even if pane IDs match (both %0), the token won't match.
	// This simulates selecting a pane before restart, then sending after.

	payload := "echo 'wrong pane would be disaster'"
	target := broadcast.Target{Host: tgt.Label, Tmux: tgt, PaneID: paneID}

	res, err := sender.Send(context.Background(), target, payload)
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}

	// The guard should refuse because the new server has no @hub_<instance> option.
	if res.Outcome != broadcast.Refused {
		t.Errorf("outcome = %v, want Refused after server restart", res.Outcome)
	}

	// Verify payload didn't reach ANY pane in the new server.
	for _, pid := range newPanes {
		screen := capturePane(t, sock, pid)
		if strings.Contains(screen, "wrong pane") {
			t.Errorf("payload reached pane %s in new server: %s", pid, screen)
		}
	}
}

// TestE2EGuardSurfacesNoBracketedPasteAsConfirmReason verifies that a pane with
// #{bracket_paste_flag}=0 is identified as needing confirmation, not silently sent.
//
// Catches: sending to `less` or similar that interprets pasted text as keystrokes.
//
// NOTE: This test verifies the REASON is surfaced, which happens in the confirmation
// layer (broadcast.Needed), not in Send itself. The guard in Send doesn't check
// bracketed paste — that's a Keeper/confirmation concern. However, we can verify the
// flag is readable and would trigger the confirmation logic.
func TestE2EGuardSurfacesNoBracketedPasteAsConfirmReason(t *testing.T) {
	// `less` is the measured case and the reason this clause exists: it reports
	// #{bracket_paste_flag}=0 and turns a pasted prompt into KEYSTROKES — it opened its
	// own help screen when three lines were pasted into it, and a payload containing `q`
	// would have quit it while `!cmd` would have run a shell command.
	//
	// The original version of this test read the flag and logged it, which proves
	// nothing: it would pass whether or not the clause existed. This one drives the real
	// producer end to end — a real `less` pane, the real delta, the real Needed — so it
	// goes red if the flag stops being collected, if the clause is removed, or if the
	// delta's field is wired to the wrong column.
	if _, err := exec.LookPath("less"); err != nil {
		t.Skip("less not installed, and it is the pane this case is about")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("a\nb\nc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "tmux.sock")
	mk := func(args ...string) {
		t.Helper()
		full := append([]string{"-S", sock, "-f", "/dev/null"}, args...)
		if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
	}
	mk("new-session", "-d", "-s", "bp", "-x", "80", "-y", "24", "-c", dir, "less f.txt")
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })
	time.Sleep(700 * time.Millisecond)

	tgt := tmux.Target{Label: "test", Socket: sock}
	ds, err := tmux.FetchDeltas(context.Background(), tmux.NewExec(10*time.Second), tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	if len(ds) != 1 {
		t.Fatalf("got %d panes, want 1", len(ds))
	}
	if ds[0].Bracketed {
		t.Fatalf("less reported bracketed paste, so this case cannot test what it is for; "+
			"pane %s flag came back true", ds[0].PaneID)
	}

	// The clause must fire for that pane, from the real Needed rather than a hand-built
	// struct: a pane that will read the prompt as keypresses has to reach the user as a
	// reason to confirm, not be sent to silently.
	reasons := broadcast.Needed([]broadcast.TargetState{{
		Host: "test", PaneID: ds[0].PaneID,
		IdentifiedNow: true, IdentifiedAtSelection: true,
		SessionAtSelection: "$0", SessionNow: "$0",
		WindowAtSelection: "@0", WindowNow: "@0",
		EpochAtSelection: "1:1", EpochNow: "1:1",
		LastOutcome: broadcast.Delivered,
		Bracketed:   ds[0].Bracketed,
	}})
	var found bool
	for _, r := range reasons {
		if r == broadcast.ReasonNoBracketedPaste {
			found = true
		}
	}
	if !found {
		t.Errorf("a pane that will read the prompt as keypresses produced reasons %v "+
			"without ReasonNoBracketedPaste — it would be sent to without asking", reasons)
	}
}
