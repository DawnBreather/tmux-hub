//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/tmux"
	"github.com/DawnBreather/tmux-hub/internal/ui"
)

// TestE2EAPossessionWindowOutlivesAPayloadThatDiesInstantly is the assertion no fake
// runner can make, because the defect it covers is a RACE and a fake has no clock.
//
// The window path used to keep its window open by following `new-window` with `set -w
// remain-on-exit on`, and `new-window` is what STARTS the payload — so the option can
// only win on time. Measured on tmux 3.7b over a private socket, 12 trials each: a
// payload of `false` survived 6 and was lost 6, while one that spawns a shell first
// survived 12. The sign of that race is a property of the machine, and the remote
// failures this path exists to show are the fast ones (`ssh: Could not resolve
// hostname …`, `Control socket connect(...): Connection refused`).
//
// So there are TWO assertions here and the second is the one the old code could never
// satisfy. The window must still be listed — and its pane must be ALIVE, carrying the
// payload's own words. A pane held by remain-on-exit is dead, and measured, a dead
// pane's visible screen holds only tmux's own `Pane is dead (status 255, …)` with the
// payload's message pushed one line into the scrollback, where neither the operator
// nor a `capture-pane -p` looks. Either way the old mechanism fails this: it loses
// the window, or it keeps a window that cannot say why.
//
// The private socket is `-S` under t.TempDir() like the rest of this package, which
// is stricter than a named `-L` server: two runs of this test cannot collide, and the
// server is killed in cleanup.
func TestE2EAPossessionWindowOutlivesAPayloadThatDiesInstantly(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := filepath.Join(t.TempDir(), "hub.sock")
	must := func(args ...string) string {
		t.Helper()
		full := append([]string{"-S", sock, "-f", "/dev/null"}, args...)
		out, err := exec.Command("tmux", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	must("new-session", "-d", "-s", "hub", "-x", "80", "-y", "24", "cat")
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })
	sessionID := must("display", "-p", "-t", "hub", "#{session_id}")

	// The shape of the failure, not a stand-in for it: the payload prints what ssh
	// prints and exits at once. `$3` rides in as an ARGUMENT, so the pane itself
	// reports whether both levels of quoting survived — a shell that ate it prints
	// `target ` and nothing more, which is the defect shapeFor's `^\$[0-9]+$` rule
	// exists to prevent, manufactured past the seam.
	const dying = `printf '%s\n' "ssh: Could not resolve hostname nosuchhost.invalid" >&2; ` +
		`printf 'target %s\n' "$0" >&2; exit 255`
	payload := ui.WindowPayload([]string{"sh", "-c", dying, "$3"})

	r := tmux.NewExec(5 * time.Second)
	tgt := tmux.Target{Label: "hub", Socket: sock}
	if err := tmux.AttachWindow(context.Background(), r, tgt, sessionID, "dying", payload); err != nil {
		t.Fatalf("AttachWindow: %v", err)
	}

	// Wait on the FACT, never on a duration: the wrapper's line can only appear after
	// the payload has exited, which is the instant the old code lost the window. A
	// window that is gone is reported as gone rather than as a timeout, because those
	// are different failures and only one of them is this defect.
	started := time.Now()
	deadline := started.Add(5 * time.Second)
	var screen string
	for {
		listed, dead := windowState(t, sock, "dying")
		if !listed {
			t.Fatalf("the window vanished %s after new-window, before the payload's exit "+
				"could be reported — an option set AFTER new-window cannot keep a window "+
				"whose payload dies first.\nlast screen:\n%s", time.Since(started), screen)
		}
		if dead {
			t.Fatalf("the window is still listed %s after new-window but its pane is DEAD, "+
				"which is what remain-on-exit leaves behind: the visible screen carries "+
				"tmux's own banner and the payload's message is in the scrollback, where "+
				"neither the operator nor capture-pane looks.\nscreen:\n%s",
				time.Since(started), capturePane(t, sock, "hub:dying"))
		}
		s, err := paneScreen(t, sock, "hub:dying")
		if err != nil {
			// Listed one read ago and unreadable now: that is the vanish, caught in the
			// gap between the two reads rather than by the check above.
			t.Fatalf("the window vanished %s after new-window, between two reads — an "+
				"option set AFTER new-window cannot keep a window whose payload dies "+
				"first (capture-pane: %v).\nlast screen:\n%s", time.Since(started), err, screen)
		}
		screen = s
		if strings.Contains(screen, "press enter to close this window") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("5s after the payload exited the pane still does not report it:\n%s", screen)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The reason has to reach the OPERATOR, which is the half the option never fixed.
	for _, want := range []string{
		"ssh: Could not resolve hostname nosuchhost.invalid",
		"target $3",
		"the attach exited 255",
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("the pane does not show %q:\n%s", want, screen)
		}
	}
	// The discriminator between the two mechanisms, and the reason this test cannot
	// pass for the wrong reason: with remain-on-exit the window survives DEAD, and a
	// dead pane shows tmux's banner instead of the payload's message.
	if strings.Contains(screen, "Pane is dead") {
		t.Errorf("the pane is a retained corpse, not a live shell:\n%s", screen)
	}
	if got := must("display", "-p", "-t", "hub:dying", "#{pane_dead}"); got != "0" {
		t.Errorf("pane_dead = %q, want 0 — the window is supposed to be held open by a "+
			"live shell waiting for a keypress, not by a dead pane tmux was told to keep", got)
	}
	if got := must("display", "-p", "-t", "hub:dying", "#{remain-on-exit}"); got != "off" {
		t.Errorf("remain-on-exit = %q on a window the hub created, want off — the option is "+
			"what this fix removed, and with it on the keypress the pane asks for would "+
			"leave a dead pane behind instead of closing the window", got)
	}
}

// TestE2EAPossessionWindowClosesOnSuccess is the complementary assertion: a SUCCESS
// closes the window silently, which is what a detach did before §20 and is the case
// that happens every time.
//
// The wrapper exists to make a FAILURE visible. A success has nothing to report, and
// the project's own principle is that value comes before ceremony — a keypress on
// every successful jump is ceremony charged to the commonest case. `tmux attach` exits
// 0 on a clean detach and ssh returns the remote command's status, so zero really does
// mean "the operator finished".
//
// This test proves the refinement: the window must close after a zero-exit payload,
// and it must NOT wait for a keypress. Against the old unconditional wrapper this test
// would either hang (waiting for a keypress that never comes) or leave the window
// behind, so it must run with a bounded wait.
func TestE2EAPossessionWindowClosesOnSuccess(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := filepath.Join(t.TempDir(), "hub.sock")
	must := func(args ...string) string {
		t.Helper()
		full := append([]string{"-S", sock, "-f", "/dev/null"}, args...)
		out, err := exec.Command("tmux", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	must("new-session", "-d", "-s", "hub", "-x", "80", "-y", "24", "cat")
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })
	sessionID := must("display", "-p", "-t", "hub", "#{session_id}")

	// A payload that prints something and exits 0. The print proves the payload ran;
	// exit 0 is what makes the conditional branch to the silent close.
	const succeeding = `printf 'payload ran successfully\n'; exit 0`
	payload := ui.WindowPayload([]string{"sh", "-c", succeeding})

	r := tmux.NewExec(5 * time.Second)
	tgt := tmux.Target{Label: "hub", Socket: sock}
	if err := tmux.AttachWindow(context.Background(), r, tgt, sessionID, "success", payload); err != nil {
		t.Fatalf("AttachWindow: %v", err)
	}

	// Wait for the window to close. A successful payload exits at once and the wrapper
	// should close the window immediately, so 2 seconds is more than sufficient — and
	// against the old unconditional wrapper this would time out rather than hang
	// forever, which is what makes it a test rather than a stall.
	started := time.Now()
	deadline := started.Add(2 * time.Second)
	for {
		listed, _ := windowState(t, sock, "success")
		if !listed {
			// This is the success case: the window closed.
			break
		}
		if time.Now().After(deadline) {
			screen, _ := paneScreen(t, sock, "hub:success")
			t.Fatalf("2s after a zero-exit payload the window is still listed — a success "+
				"should close the window silently rather than waiting for a keypress.\n"+
				"This proves the wrapper waits unconditionally.\nscreen:\n%s", screen)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Verify it closed quickly. 2s is the timeout; the actual close should happen in
	// tens of milliseconds. If it took more than 500ms something is waiting that
	// shouldn't be.
	elapsed := time.Since(started)
	if elapsed > 500*time.Millisecond {
		t.Errorf("window closed after %s, want <500ms — it should close immediately on "+
			"success, not after a delay", elapsed)
	}
}

// windowState answers whether a window of that name is in the session and whether its
// pane has died — the two ways the old mechanism could leave things, told apart in one
// read so a poll cannot see a window in one call and a corpse in the next.
//
// It tolerates an error rather than failing on it: a window that is gone is the answer
// here, not a reason to stop, and `list-windows` on a live server cannot fail for any
// other reason. `#{pane_dead}` in a window format is the ACTIVE pane's, which for a
// window the hub just created with one pane is that pane (measured: it renders `0`).
func windowState(t *testing.T, socket, name string) (listed, dead bool) {
	t.Helper()
	out, err := exec.Command("tmux", "-S", socket, "list-windows", "-t", "hub",
		"-F", "#{window_name}:#{pane_dead}").Output()
	if err != nil {
		return false, false
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l == name+":0" {
			return true, false
		}
		if l == name+":1" {
			return true, true
		}
	}
	return false, false
}

// TestAJumpMovesTheAttachedClient proves that SwitchClient really moves a client,
// not just returns rc=0. A fake runner cannot catch this failure mode: switch-client
// can return rc=0 without moving anything. So this asserts the CONSEQUENCE — which
// session the client is displaying — against a real tmux server.
//
// It needs a real attached client. The harness creates an outer tmux on a second
// private socket and attaches from inside it to the inner server.
func TestAJumpMovesTheAttachedClient(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	// Create the inner server (the one being controlled).
	innerSock := filepath.Join(t.TempDir(), "inner.sock")
	innerMust := func(args ...string) string {
		t.Helper()
		full := append([]string{"-S", innerSock, "-f", "/dev/null"}, args...)
		out, err := exec.Command("tmux", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux (inner) %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// Create two sessions on the inner server.
	innerMust("new-session", "-d", "-s", "hub", "-x", "80", "-y", "24", "cat")
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", innerSock, "kill-server").Run() })

	innerMust("new-session", "-d", "-s", "work", "-x", "80", "-y", "24", "cat")

	hubSessionID := innerMust("display", "-p", "-t", "hub", "#{session_id}")
	workSessionID := innerMust("display", "-p", "-t", "work", "#{session_id}")
	if !strings.HasPrefix(hubSessionID, "$") || !strings.HasPrefix(workSessionID, "$") {
		t.Fatalf("session_id = %q, %q, want $N", hubSessionID, workSessionID)
	}

	// Get a window ID from the work session for SelectWindow.
	workWindowID := innerMust("display", "-p", "-t", "work", "#{window_id}")
	if !strings.HasPrefix(workWindowID, "@") {
		t.Fatalf("window_id = %q, want @N", workWindowID)
	}

	// Create the outer server (holds the attached client).
	outerSock := filepath.Join(t.TempDir(), "outer.sock")
	outerMust := func(args ...string) string {
		t.Helper()
		full := append([]string{"-S", outerSock, "-f", "/dev/null"}, args...)
		out, err := exec.Command("tmux", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux (outer) %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	outerMust("new-session", "-d", "-s", "viewer", "-x", "80", "-y", "24", "cat")
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", outerSock, "kill-server").Run() })

	// From the outer server, create a window that attaches to the inner hub session.
	// This leaves a client attached to hub on the inner server.
	attachCmd := fmt.Sprintf("tmux -S %s attach -t hub", innerSock)
	outerMust("new-window", "-t", "viewer", "-n", "attach", attachCmd)

	// Wait a moment for the attach to complete.
	time.Sleep(200 * time.Millisecond)

	// Verify the inner server has a client attached to the hub session.
	clientSession := innerMust("display", "-p", "-F", "#{client_session}")
	if clientSession != "hub" {
		t.Fatalf("before switch: client_session = %q, want %q", clientSession, "hub")
	}

	// Now switch the client to the work session.
	ctx := context.Background()
	r := tmux.NewExec(5 * time.Second)
	innerTarget := tmux.Target{Label: "inner", Socket: innerSock}

	if err := tmux.SwitchClient(ctx, r, innerTarget, workSessionID); err != nil {
		t.Fatalf("SwitchClient: %v", err)
	}

	// Select the window within the work session.
	if err := tmux.SelectWindow(ctx, r, innerTarget, workWindowID); err != nil {
		t.Fatalf("SelectWindow: %v", err)
	}

	// Assert the consequence: the client is now displaying the work session.
	got := innerMust("display", "-p", "-F", "#{client_session}")
	if got != "work" {
		t.Fatalf("after switch: client is displaying %q, want %q — switch-client returned rc=0 and moved nothing",
			got, "work")
	}

	// Negative check: the outer server must be untouched.
	// A jump is not allowed to reach across servers.
	outerSession := outerMust("display", "-p", "-t", "viewer", "#{session_name}")
	if outerSession != "viewer" {
		t.Fatalf("the outer server moved: %q, want %q", outerSession, "viewer")
	}
}
