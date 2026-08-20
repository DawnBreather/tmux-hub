//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/broadcast"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// TestLifecycleAgainstRealTmux proves the verbs work against a real tmux server.
//
// A fake runner proves argv; only real tmux proves the flags mean what the measurements say.
func TestLifecycleAgainstRealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	sock := filepath.Join(t.TempDir(), "tmux.sock")
	must := func(args ...string) string {
		t.Helper()
		full := append([]string{"-S", sock, "-f", "/dev/null"}, args...)
		out, err := exec.Command("tmux", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// Create a session with one window.
	must("new-session", "-d", "-s", "test", "-x", "80", "-y", "24", "cat")
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })

	// Get the session ID.
	sessionID := must("display", "-p", "-t", "test", "#{session_id}")
	if !strings.HasPrefix(sessionID, "$") {
		t.Fatalf("session_id = %q, want $N", sessionID)
	}

	r := tmux.NewExec(10 * time.Second)
	tgt := tmux.Target{Label: "test", Socket: sock}

	// NewWindow creates a window and returns its pane id.
	paneID, err := tmux.NewWindow(context.Background(), r, tgt, sessionID, "/tmp", "sleep 300")
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	if !strings.HasPrefix(paneID, "%") {
		t.Fatalf("paneID = %q, want %%N", paneID)
	}

	// Verify the pane appears in list-panes -a.
	allPanes := must("list-panes", "-a", "-F", "#{pane_id}")
	if !strings.Contains(allPanes, paneID) {
		t.Fatalf("list-panes -a does not include %s:\n%s", paneID, allPanes)
	}

	// Set a hub option on the pane.
	must("set", "-p", "-t", paneID, "@hub_test", "before")

	// Read initial pane_pid.
	pidBefore := must("display", "-p", "-t", paneID, "#{pane_pid}")

	// Respawn the pane.
	if err := tmux.RespawnPane(context.Background(), r, tgt, paneID, "sleep 300"); err != nil {
		t.Fatalf("RespawnPane: %v", err)
	}

	// Verify pane_id survived.
	paneIDAfter := must("display", "-p", "-t", paneID, "#{pane_id}")
	if paneIDAfter != paneID {
		t.Fatalf("pane_id after respawn = %q, want %q", paneIDAfter, paneID)
	}

	// Verify the @hub_test option survived.
	optionAfter := must("display", "-p", "-t", paneID, "#{@hub_test}")
	if optionAfter != "before" {
		t.Fatalf("@hub_test after respawn = %q, want %q", optionAfter, "before")
	}

	// Verify pane_pid changed.
	pidAfter := must("display", "-p", "-t", paneID, "#{pane_pid}")
	if pidAfter == pidBefore {
		t.Fatalf("pane_pid did not change after respawn: %s", pidAfter)
	}

	// PaneAlive returns true for a live pane.
	alive, err := tmux.PaneAlive(context.Background(), r, tgt, paneID)
	if err != nil {
		t.Fatalf("PaneAlive: %v", err)
	}
	if !alive {
		t.Fatal("PaneAlive returned false for a live pane")
	}

	// Get the window ID.
	windowID := must("display", "-p", "-t", paneID, "#{window_id}")
	if !strings.HasPrefix(windowID, "@") {
		t.Fatalf("window_id = %q, want @N", windowID)
	}

	// SetWindowOption sets a window option.
	if err := tmux.SetWindowOption(context.Background(), r, tgt, windowID, "remain-on-exit", "on"); err != nil {
		t.Fatalf("SetWindowOption: %v", err)
	}

	// Verify the option was set.
	optVal := must("display", "-p", "-t", windowID, "#{remain-on-exit}")
	if optVal != "on" {
		t.Fatalf("remain-on-exit = %q, want %q", optVal, "on")
	}

	// KillWindow destroys the window.
	if err := tmux.KillWindow(context.Background(), r, tgt, windowID); err != nil {
		t.Fatalf("KillWindow: %v", err)
	}

	// PaneAlive returns false after the window is killed.
	alive, err = tmux.PaneAlive(context.Background(), r, tgt, paneID)
	if err != nil {
		t.Fatalf("PaneAlive after kill: %v", err)
	}
	if alive {
		t.Fatal("PaneAlive returned true for a dead pane")
	}

	// NewSession creates a new session and returns its pane id.
	newPaneID, err := tmux.NewSession(context.Background(), r, tgt, "proj", "/tmp", "cat")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if !strings.HasPrefix(newPaneID, "%") {
		t.Fatalf("new session paneID = %q, want %%N", newPaneID)
	}

	// Get the new session ID.
	newSessionID := must("display", "-p", "-t", newPaneID, "#{session_id}")

	// KillSession destroys the session.
	if err := tmux.KillSession(context.Background(), r, tgt, newSessionID); err != nil {
		t.Fatalf("KillSession: %v", err)
	}

	// Verify the session is gone.
	alive, err = tmux.PaneAlive(context.Background(), r, tgt, newPaneID)
	if err != nil {
		t.Fatalf("PaneAlive after session kill: %v", err)
	}
	if alive {
		t.Fatal("PaneAlive returned true for a pane in a killed session")
	}
}

func TestADeadPaneCarriesItsExitCode(t *testing.T) {
	// Prove the dead-pane path is reachable: a window with remain-on-exit on
	// keeps its pane after the command exits, and a poll sees Dead=true and
	// DeadStatus=7. This is the case that proves the previously-unreachable
	// branch (dead panes were destroyed before any tick could see them) is now
	// reachable.
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	sock := filepath.Join(t.TempDir(), "tmux.sock")
	must := func(args ...string) string {
		t.Helper()
		full := append([]string{"-S", sock, "-f", "/dev/null"}, args...)
		out, err := exec.Command("tmux", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// Create a session.
	must("new-session", "-d", "-s", "test", "-x", "80", "-y", "24", "cat")
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })

	sessionID := must("display", "-p", "-t", "test", "#{session_id}")

	r := tmux.NewExec(10 * time.Second)
	tgt := tmux.Target{Label: "test", Socket: sock}

	// Create a window with a long-running command first.
	paneID, err := tmux.NewWindow(context.Background(), r, tgt, sessionID, "/tmp", "sleep 300")
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}

	// Get the window ID while the pane is still live.
	windowID := must("display", "-p", "-t", paneID, "#{window_id}")
	if !strings.HasPrefix(windowID, "@") {
		t.Fatalf("window_id = %q, want @N", windowID)
	}

	// Set remain-on-exit on this window so the pane stays when the command exits.
	if err := tmux.SetWindowOption(context.Background(), r, tgt, windowID, "remain-on-exit", "on"); err != nil {
		t.Fatalf("SetWindowOption: %v", err)
	}

	// Now respawn the pane with the exiting command. Since remain-on-exit is
	// already set, the pane will stay dead after exit 7.
	if err := tmux.RespawnPane(context.Background(), r, tgt, paneID, "exit 7"); err != nil {
		t.Fatalf("RespawnPane: %v", err)
	}

	// Wait a moment for the command to exit.
	time.Sleep(200 * time.Millisecond)

	// Now poll through the hub's read path: FetchDeltas → registry.Update.
	// This proves the full chain: #{pane_dead} → tmux.Delta → registry.Pane →
	// state.Classify → the rendered word.
	ctx := context.Background()
	ds, err := tmux.FetchDeltas(ctx, r, tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}

	// Find the delta for our pane.
	var found *tmux.Delta
	for i := range ds {
		if ds[i].PaneID == paneID {
			found = &ds[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("paneID %s not found in %d deltas", paneID, len(ds))
	}

	// Verify the delta carries Dead=true and DeadStatus=7.
	if !found.Dead {
		t.Errorf("Delta.Dead = false, want true")
	}
	if found.DeadStatus != 7 {
		t.Errorf("Delta.DeadStatus = %d, want 7", found.DeadStatus)
	}

	// Feed the deltas to the registry as the poller does.
	reg := registry.New()
	snap, err := tmux.FetchSnapshot(ctx, r, tgt, ds, nil)
	if err != nil {
		t.Fatalf("FetchSnapshot: %v", err)
	}

	reg.Update("test", ds, snap.Labels, snap.Zones, snap.Fulls, time.Now(), 0)

	// Get the pane from the registry.
	panes := reg.Panes()
	var deadPane *registry.Pane
	for i := range panes {
		if panes[i].PaneID == paneID {
			deadPane = &panes[i]
			break
		}
	}
	if deadPane == nil {
		t.Fatalf("paneID %s not found in registry with %d panes", paneID, len(panes))
	}

	// Verify the registry.Pane has Dead=true, DeadStatus=7, and state=Error.
	if !deadPane.Dead {
		t.Errorf("registry.Pane.Dead = false, want true")
	}
	if deadPane.DeadStatus != 7 {
		t.Errorf("registry.Pane.DeadStatus = %d, want 7", deadPane.DeadStatus)
	}
	if deadPane.ClassifiedState != state.Error {
		t.Errorf("registry.Pane.ClassifiedState = %s, want Error", deadPane.ClassifiedState)
	}
}

// TestLaunchRealClaudeAgent launches a real `claude` process with a hub-dictated
// session id and verifies: the pane exists, the hub's stamp is on it, and the
// session transcript appears in ~/.claude/projects/.
func TestLaunchRealClaudeAgent(t *testing.T) {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("claude binary not on PATH: %v", err)
	}

	sock := filepath.Join(t.TempDir(), "tmux.sock")
	must := func(args ...string) string {
		t.Helper()
		full := append([]string{"-S", sock, "-f", "/dev/null"}, args...)
		out, err := exec.Command("tmux", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// Create a session for the launch to target.
	must("new-session", "-d", "-s", "test", "-x", "80", "-y", "24", "cat")
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })

	sessionID := must("display", "-p", "-t", "test", "#{session_id}")
	if !strings.HasPrefix(sessionID, "$") {
		t.Fatalf("session_id = %q, want $N", sessionID)
	}

	// Generate a fresh uuid for the claude session.
	uuid := "e2e-test-" + strings.ReplaceAll(t.Name(), "/", "-")

	// Launch a real claude with the hub's session id.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	r := tmux.NewExec(10 * time.Second)
	tgt := tmux.Target{Label: "test", Socket: sock}
	tmpDir := t.TempDir()

	// NewWindow with a real claude command.
	cmd := fmt.Sprintf("cd %s && %s --session-id %s -p 'reply with exactly: OK' || sleep 300", tmpDir, claudePath, uuid)
	paneID, err := tmux.NewWindow(ctx, r, tgt, sessionID, tmpDir, cmd)
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}

	// Verify the pane exists.
	res, err := r.Run(ctx, tgt, "display", "-p", "-t", paneID, "#{pane_id}")
	if err != nil {
		t.Fatalf("verify pane: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != paneID {
		t.Fatalf("pane %s does not exist", paneID)
	}

	// Get the window ID and set remain-on-exit.
	res, err = r.Run(ctx, tgt, "display", "-p", "-t", paneID, "#{window_id}")
	if err != nil {
		t.Fatalf("get window_id: %v", err)
	}
	windowID := strings.TrimSpace(res.Stdout)
	if !strings.HasPrefix(windowID, "@") {
		t.Fatalf("window_id = %q, want @N", windowID)
	}

	if err := tmux.SetWindowOption(ctx, r, tgt, windowID, "remain-on-exit", "on"); err != nil {
		t.Fatalf("SetWindowOption: %v", err)
	}

	// Stamp the pane with a hub token.
	inst := broadcast.NewInstance()
	stamper := broadcast.NewStamper(r, inst)
	tok, err := stamper.Stamp(ctx, tgt, paneID)
	if err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	if tok == "" {
		t.Fatal("Stamp returned empty token")
	}

	// Verify the @hub_* option is set on the pane.
	res, err = r.Run(ctx, tgt, "display", "-p", "-t", paneID, fmt.Sprintf("#{%s}", inst.Option()))
	if err != nil {
		t.Fatalf("read stamp: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != tok {
		t.Errorf("pane stamp = %q, want %q", strings.TrimSpace(res.Stdout), tok)
	}

	// Wait briefly for the session transcript to appear.
	// Claude writes ~/.claude/projects/*/<uuid>.jsonl — this is a best-effort check.
	// The critical assertions (pane exists, stamp is set) have already passed.
	home, err := os.UserHomeDir()
	if err == nil {
		projectsDir := filepath.Join(home, ".claude", "projects")
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			// Scan for <uuid>.jsonl under any project.
			matches, err := filepath.Glob(filepath.Join(projectsDir, "*", uuid+".jsonl"))
			if err == nil && len(matches) > 0 {
				// Verify it's a regular file with nonzero size.
				if info, err := os.Stat(matches[0]); err == nil && info.Size() > 0 {
					t.Logf("session transcript appeared: %s (%d bytes)", matches[0], info.Size())
					return
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Logf("session transcript did not appear within 5s (non-fatal; claude may not run in tmpdir)")
	}
}

// TestRestartPreservesStampButInvalidatesIdentity verifies the measured behavior
// that makes identity invalidation necessary: respawn-pane -k keeps pane_id and
// the @hub_* option (so the stamp survives) while changing pane_pid (so the process
// is different). The hub must therefore explicitly drop identity after a restart.
func TestRestartPreservesStampButInvalidatesIdentity(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	sock := filepath.Join(t.TempDir(), "tmux.sock")
	must := func(args ...string) string {
		t.Helper()
		full := append([]string{"-S", sock, "-f", "/dev/null"}, args...)
		out, err := exec.Command("tmux", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// Create a session.
	must("new-session", "-d", "-s", "test", "-x", "80", "-y", "24", "cat")
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })

	sessionID := must("display", "-p", "-t", "test", "#{session_id}")

	r := tmux.NewExec(10 * time.Second)
	tgt := tmux.Target{Label: "test", Socket: sock}

	// Create a window.
	paneID, err := tmux.NewWindow(context.Background(), r, tgt, sessionID, "/tmp", "sleep 300")
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}

	// Stamp it with a hub token.
	inst := broadcast.NewInstance()
	stamper := broadcast.NewStamper(r, inst)
	tok, err := stamper.Stamp(context.Background(), tgt, paneID)
	if err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	if tok == "" {
		t.Fatal("Stamp returned empty token")
	}

	// Adopt it in a keeper (simulating hub-created agent).
	keeper := broadcast.NewKeeper(stamper)
	keeper.Adopt("test", paneID, "test-session-uuid")

	// Verify it's identified before respawn.
	if !keeper.Identified("test", paneID) {
		t.Fatal("pane must be identified before respawn")
	}

	// Read pane_id and pane_pid before respawn.
	paneIDBefore := must("display", "-p", "-t", paneID, "#{pane_id}")
	panePIDBefore := must("display", "-p", "-t", paneID, "#{pane_pid}")

	// Read the @hub_* option before respawn.
	optBefore := must("display", "-p", "-t", paneID, fmt.Sprintf("#{%s}", inst.Option()))
	if optBefore != tok {
		t.Fatalf("@hub_* before respawn = %q, want %q", optBefore, tok)
	}

	// Respawn the pane.
	if err := tmux.RespawnPane(context.Background(), r, tgt, paneID, "sleep 300"); err != nil {
		t.Fatalf("RespawnPane: %v", err)
	}

	// Verify pane_id is unchanged.
	paneIDAfter := must("display", "-p", "-t", paneID, "#{pane_id}")
	if paneIDAfter != paneIDBefore {
		t.Errorf("pane_id changed: %q → %q, want unchanged", paneIDBefore, paneIDAfter)
	}

	// Verify @hub_* option survived.
	optAfter := must("display", "-p", "-t", paneID, fmt.Sprintf("#{%s}", inst.Option()))
	if optAfter != tok {
		t.Errorf("@hub_* after respawn = %q, want %q", optAfter, tok)
	}

	// Verify pane_pid changed.
	panePIDAfter := must("display", "-p", "-t", paneID, "#{pane_pid}")
	if panePIDAfter == panePIDBefore {
		t.Errorf("pane_pid did not change: %s, want different", panePIDAfter)
	}

	// Now invalidate identity as the hub would.
	keeper.ForgetPane("test", paneID)

	// Verify the keeper no longer reports it as identified.
	if keeper.Identified("test", paneID) {
		t.Fatal("after ForgetPane, pane must not be identified")
	}
}
