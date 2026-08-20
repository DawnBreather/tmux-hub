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
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

const remoteHost = "nuc"

// checkRemoteReachable returns true if the remote host is reachable via ssh in
// BatchMode. It does NOT start any ssh master or forward — this is the
// precondition check only.
func checkRemoteReachable(t *testing.T) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes",
		"-o", "ConnectTimeout=3", remoteHost, "true")
	return cmd.Run() == nil
}

// remoteServer creates a PRIVATE tmux server on the remote host under a temp
// directory. It returns the remote socket path and a cleanup function that
// kills the server. This never touches the host's default socket.
func remoteServer(t *testing.T) (remoteSocket string, cleanup func()) {
	t.Helper()

	// Create a temp dir on the remote side for our private socket.
	ctx := context.Background()
	out, err := exec.CommandContext(ctx, "ssh", remoteHost, "mktemp -d").CombinedOutput()
	if err != nil {
		t.Fatalf("mktemp on %s: %v: %s", remoteHost, err, out)
	}
	remoteDir := strings.TrimSpace(string(out))
	remoteSocket = filepath.Join(remoteDir, "p.sock")

	// Start a tmux server on that socket with one session running `cat` so it
	// stays alive. The -f /dev/null ensures no user config is loaded.
	cmd := fmt.Sprintf("tmux -S %s -f /dev/null new-session -d -s test -x 80 -y 24 cat", remoteSocket)
	out, err = exec.CommandContext(ctx, "ssh", remoteHost, cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("new-session on %s: %v: %s", remoteHost, err, out)
	}

	cleanup = func() {
		// Kill the server and remove the temp directory.
		_ = exec.Command("ssh", remoteHost,
			fmt.Sprintf("tmux -S %s kill-server; rm -rf %s", remoteSocket, remoteDir)).Run()
	}
	return remoteSocket, cleanup
}

// sshForward creates an ssh master connection with a local→remote socket
// forward. It returns the local socket path, the control socket path, and a
// cleanup function that kills the master by PID.
//
// This uses -M (master mode) and -o ExitOnForwardFailure=yes so the forward
// failing is a startup error rather than a silent degradation.
func sshForward(t *testing.T, remoteSocket string) (localSocket, ctlPath string, pid int, cleanup func()) {
	t.Helper()

	tmpDir := t.TempDir()
	localSocket = filepath.Join(tmpDir, "local.sock")
	ctlPath = filepath.Join(tmpDir, "ssh-ctl")

	// Start the ssh master in the background. We need to capture its PID so we
	// can kill it by PID rather than with pkill.
	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "ssh",
		"-N", "-M", "-S", ctlPath,
		"-L", localSocket+":"+remoteSocket,
		"-o", "BatchMode=yes",
		"-o", "ExitOnForwardFailure=yes",
		remoteHost)

	if err := cmd.Start(); err != nil {
		t.Fatalf("ssh master: %v", err)
	}
	pid = cmd.Process.Pid

	// Wait for the control socket to appear, which is when the master is ready.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ctlPath); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(ctlPath); err != nil {
		t.Fatalf("control socket did not appear: %v", err)
	}

	cleanup = func() {
		// Kill by the exact PID we started, not by a pattern that could match
		// anything else.
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
			_, _ = cmd.Process.Wait()
		}
	}
	return localSocket, ctlPath, pid, cleanup
}

// remoteVersion fetches the tmux version from the remote server. This is what
// proves we reached the far end rather than a local squatter.
func remoteVersion(t *testing.T, remoteSocket string) string {
	t.Helper()
	ctx := context.Background()
	cmd := fmt.Sprintf("tmux -S %s display -p '#{version}'", remoteSocket)
	out, err := exec.CommandContext(ctx, "ssh", remoteHost, cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("remote version query: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// localVersion fetches the LOCAL tmux version, which must differ from the
// remote if the remote is truly a different machine.
func localVersion(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("tmux", "-V").CombinedOutput()
	if err != nil {
		t.Fatalf("local tmux -V: %v: %s", err, out)
	}
	// tmux -V outputs "tmux 3.2a" or similar; extract just the version.
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) >= 2 {
		return fields[1]
	}
	return strings.TrimSpace(string(out))
}

// remoteCapturePaneText reads the visible screen of a pane on the remote server.
// This is what we use to assert text actually arrived.
func remoteCapturePaneText(t *testing.T, remoteSocket, paneID string) string {
	t.Helper()
	ctx := context.Background()
	cmd := fmt.Sprintf("tmux -S %s capture-pane -p -t %s", remoteSocket, paneID)
	out, err := exec.CommandContext(ctx, "ssh", remoteHost, cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("capture-pane: %v: %s", err, out)
	}
	return string(out)
}

// TestE2ERemotePollSeesRemotePanes checks that the hub can poll a remote tmux
// server over an ssh forward and sees the panes there.
//
// Would catch: broken dial path, the forward reaching a local squatter instead
// of the remote, the exclusion logic hiding all panes.
func TestE2ERemotePollSeesRemotePanes(t *testing.T) {
	if !checkRemoteReachable(t) {
		t.Skipf("remote host %s not reachable", remoteHost)
	}

	remoteSock, cleanupRemote := remoteServer(t)
	defer cleanupRemote()

	localSock, ctlPath, _, cleanupSSH := sshForward(t, remoteSock)
	defer cleanupSSH()

	// Poll the remote host through the hub's own API.
	reg := registry.New()
	poller := hub.NewPoller(tmux.NewExec(5*time.Second), reg)
	poller.Add(hub.Host{
		Label:       "remote",
		Socket:      localSock,
		SSHDest:     remoteHost,
		ControlPath: ctlPath,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	hosts := poller.Tick(ctx, time.Now(), nil)
	if len(hosts) != 1 {
		t.Fatalf("got %d hosts, want 1", len(hosts))
	}
	h := hosts[0]
	if h.Status != hub.Up {
		t.Errorf("host status = %s (%s), want Up", h.Status, h.Reason)
	}

	panes := reg.Panes()
	if len(panes) == 0 {
		t.Fatal("poll saw zero panes; want at least the one we created")
	}

	var found bool
	for _, p := range panes {
		if p.Host == "remote" {
			found = true
			break
		}
	}
	if !found {
		t.Error("no pane has host=remote; all panes:", panes)
	}
}

// TestE2ERemoteVersionIsRemote asserts the version the hub reports for a
// remote host is the REMOTE tmux's version, not the local one. This proves the
// forward reaches the far end rather than a local squatter.
//
// Would catch: a local tmux server answering on the forward socket, making the
// hub think it's polling the remote when it's reading localhost.
func TestE2ERemoteVersionIsRemote(t *testing.T) {
	if !checkRemoteReachable(t) {
		t.Skipf("remote host %s not reachable", remoteHost)
	}

	remoteSock, cleanupRemote := remoteServer(t)
	defer cleanupRemote()

	localSock, ctlPath, _, cleanupSSH := sshForward(t, remoteSock)
	defer cleanupSSH()

	// Fetch the remote version directly over ssh.
	wantVersion := remoteVersion(t, remoteSock)
	localVer := localVersion(t)

	reg := registry.New()
	poller := hub.NewPoller(tmux.NewExec(5*time.Second), reg)
	poller.Add(hub.Host{
		Label:       "remote",
		Socket:      localSock,
		SSHDest:     remoteHost,
		ControlPath: ctlPath,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	hosts := poller.Tick(ctx, time.Now(), nil)
	if len(hosts) != 1 {
		t.Fatalf("got %d hosts, want 1", len(hosts))
	}
	h := hosts[0]
	if h.Version == "" {
		t.Fatal("host.Version is empty; want the remote tmux version")
	}
	if h.Version == localVer {
		t.Errorf("host.Version = %q (local version); proves forward reached localhost, not %s",
			h.Version, remoteHost)
	}
	if h.Version != wantVersion {
		t.Errorf("host.Version = %q; want %q (remote)", h.Version, wantVersion)
	}
}

// TestE2ERemoteSendDeliversToRemotePane polls a remote host, sends text to a
// remote pane via the broadcast API, and asserts the text arrives.
//
// Would catch: send path broken over ssh forwards, guard refusing everything,
// witness never confirming, remote host plumbing delivering to the wrong server.
func TestE2ERemoteSendDeliversToRemotePane(t *testing.T) {
	if !checkRemoteReachable(t) {
		t.Skipf("remote host %s not reachable", remoteHost)
	}

	remoteSock, cleanupRemote := remoteServer(t)
	defer cleanupRemote()

	localSock, ctlPath, _, cleanupSSH := sshForward(t, remoteSock)
	defer cleanupSSH()

	// Poll once to discover the pane.
	ctx := context.Background()
	run := tmux.NewExec(5 * time.Second)
	reg := registry.New()
	poller := hub.NewPoller(run, reg)
	poller.Add(hub.Host{
		Label:       "remote",
		Socket:      localSock,
		SSHDest:     remoteHost,
		ControlPath: ctlPath,
	})
	hosts := poller.Tick(ctx, time.Now(), nil)
	if hosts[0].Status != hub.Up {
		t.Fatalf("host not up: %s", hosts[0].Reason)
	}

	// Find the remote pane from the poll.
	var remotePane string
	for _, p := range reg.Panes() {
		if p.Host == "remote" && p.Kind == registry.KindPane {
			remotePane = p.PaneID
			break
		}
	}
	if remotePane == "" {
		t.Fatal("the poll found no pane on the remote host, so there is nothing to send to")
	}

	// Stamp and send through the broadcast API.
	inst := broadcast.NewInstance()
	st := broadcast.NewStamper(run, inst)
	sender := broadcast.NewSender(run.(tmux.InputRunner), st, inst)

	if _, err := st.Stamp(ctx, tmux.Target{Label: "remote", Socket: localSock}, remotePane); err != nil {
		t.Fatalf("stamp %s over the forward: %v", remotePane, err)
	}

	tg := broadcast.Target{Host: "remote", Tmux: tmux.Target{Label: "remote", Socket: localSock}, PaneID: remotePane}
	text := "remote delivery probe"
	res, err := sender.Send(ctx, tg, text)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if res.Outcome == broadcast.Refused {
		t.Fatalf("send refused: %s", res.Reason)
	}

	// Wait for the witness delay and check.
	res = sender.Witness(ctx, res, text)
	if res.Outcome != broadcast.Delivered {
		t.Errorf("send outcome = %s (%s); want Delivered", res.Outcome, res.Reason)
	}

	// Assert the text is actually on the remote pane's screen.
	screen := remoteCapturePaneText(t, remoteSock, remotePane)
	if !strings.Contains(screen, text) {
		t.Errorf("remote pane screen does not contain %q; screen:\n%s", text, screen)
	}
}

// TestE2ERemoteReadOnlyHostCannotSend checks that a remote host configured
// WITHOUT ctl= is read-only: it can be polled but not sent to, and the hub
// says so rather than pretending to accept.
//
// Would catch: send path ignoring the read-only constraint, sending to a
// remote host with no ssh master, the error message being unhelpful.
func TestE2ERemoteReadOnlyHostCannotSend(t *testing.T) {
	if !checkRemoteReachable(t) {
		t.Skipf("remote host %s not reachable", remoteHost)
	}

	remoteSock, cleanupRemote := remoteServer(t)
	defer cleanupRemote()

	localSock, _, _, cleanupSSH := sshForward(t, remoteSock)
	defer cleanupSSH()

	// Register the host WITHOUT ctl=, which makes it read-only.
	run := tmux.NewExec(5 * time.Second)
	reg := registry.New()
	poller := hub.NewPoller(run, reg)
	poller.Add(hub.Host{
		Label:  "remote-ro",
		Socket: localSock,
		// SSHDest and ControlPath are EMPTY, so attach and listing cannot work.
	})

	ctx := context.Background()
	hosts := poller.Tick(ctx, time.Now(), nil)
	if hosts[0].Status != hub.Up {
		t.Fatalf("host not up: %s", hosts[0].Reason)
	}

	panes := reg.Panes()
	if len(panes) == 0 {
		t.Fatal("poll saw zero panes")
	}
	paneID := panes[0].PaneID

	// Try to send. This should refuse because there's no way to stamp the pane
	// (stamping needs the agent listing, which needs ssh+ctl).
	// One instance for both, because the token the sender checks must be the one the
	// stamper would have written — two instances mean two option names and the refusal
	// would be for the wrong reason.
	inst := broadcast.NewInstance()
	st := broadcast.NewStamper(run, inst)
	sender := broadcast.NewSender(run.(tmux.InputRunner), st, inst)
	tg := broadcast.Target{
		Host:   "remote-ro",
		Tmux:   tmux.Target{Label: "remote-ro", Socket: localSock},
		PaneID: paneID,
	}

	res, err := sender.Send(ctx, tg, "should-be-refused")
	if err != nil {
		t.Fatalf("send returned error: %v; want Refused outcome, not error", err)
	}
	if res.Outcome != broadcast.Refused {
		t.Errorf("send outcome = %s; want Refused for a read-only host", res.Outcome)
	}
	if !strings.Contains(res.Reason, "no identity token") {
		t.Errorf("refusal reason = %q; want mention of identity token", res.Reason)
	}
}

// TestE2ERemoteTickLatency measures the per-tick cost of polling a remote
// host. This does NOT assert a threshold — the remote is a shared machine and
// network conditions vary — but reports the measurement so a regression is
// observable.
//
// Would catch: nothing by itself, but documents the baseline so a future
// change that makes polling 10x slower is visible.
func TestE2ERemoteTickLatency(t *testing.T) {
	if !checkRemoteReachable(t) {
		t.Skipf("remote host %s not reachable", remoteHost)
	}

	remoteSock, cleanupRemote := remoteServer(t)
	defer cleanupRemote()

	localSock, ctlPath, _, cleanupSSH := sshForward(t, remoteSock)
	defer cleanupSSH()

	reg := registry.New()
	poller := hub.NewPoller(tmux.NewExec(5*time.Second), reg)
	poller.Add(hub.Host{
		Label:       "remote",
		Socket:      localSock,
		SSHDest:     remoteHost,
		ControlPath: ctlPath,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Warm up: first tick discovers, second reads content.
	_ = poller.Tick(ctx, time.Now(), nil)
	_ = poller.Tick(ctx, time.Now(), nil)

	// Measure five ticks.
	const N = 5
	var total time.Duration
	for i := 0; i < N; i++ {
		start := time.Now()
		hosts := poller.Tick(ctx, time.Now(), nil)
		elapsed := time.Since(start)
		if hosts[0].Status != hub.Up {
			t.Fatalf("tick %d: host not up: %s", i, hosts[0].Reason)
		}
		total += elapsed
		t.Logf("tick %d: %v", i, elapsed)
	}
	avg := total / N
	t.Logf("average tick latency over ssh to %s: %v", remoteHost, avg)

	// No assertion — this is a measurement, not a gate. If it regresses to
	// seconds, someone will notice.
}

// TestE2ERemoteLaunchPaneIsNotWalkedAgainstLocalProcs creates a window on a
// remote server through a forwarded socket and asserts the pane appears in the
// poll AND is NOT walked against the local process table. This is the Critical
// defect Task 15 exists to prevent: a forwarded socket with no ssh= was
// incorrectly identified as local, so 97 of 3117 local pids falsely answered
// "agent here".
//
// Would catch: remote host incorrectly marked as LocalProc=true, pane identity
// walked against the wrong process table, launch creating a pane we cannot see.
func TestE2ERemoteLaunchPaneIsNotWalkedAgainstLocalProcs(t *testing.T) {
	if !checkRemoteReachable(t) {
		t.Skipf("remote host %s not reachable", remoteHost)
	}

	remoteSock, cleanupRemote := remoteServer(t)
	defer cleanupRemote()

	localSock, ctlPath, _, cleanupSSH := sshForward(t, remoteSock)
	defer cleanupSSH()

	// Create a new window on the remote server through the forwarded socket.
	// This simulates what a launch does: new-window over the socket.
	ctx := context.Background()
	cmd := fmt.Sprintf("tmux -S %s new-window -t test: -P -F '#{pane_id}' sleep 30", remoteSock)
	out, err := exec.CommandContext(ctx, "ssh", remoteHost, cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("new-window through ssh: %v: %s", err, out)
	}
	newPaneID := strings.TrimSpace(string(out))
	if newPaneID == "" {
		t.Fatal("new-window returned empty pane_id")
	}

	// Poll the host through the hub.
	run := tmux.NewExec(5 * time.Second)
	reg := registry.New()
	poller := hub.NewPoller(run, reg)
	h := hub.Host{
		Label:       "remote",
		Socket:      localSock,
		SSHDest:     remoteHost,
		ControlPath: ctlPath,
		// LocalProc is NOT set, so it defaults to false — this is a remote host.
	}
	poller.Add(h)

	hosts := poller.Tick(ctx, time.Now(), nil)
	if len(hosts) != 1 {
		t.Fatalf("got %d hosts, want 1", len(hosts))
	}
	polledHost := hosts[0]
	if polledHost.Status != hub.Up {
		t.Fatalf("host not up: %s", polledHost.Reason)
	}

	// Assert the host is NOT marked as LocalProc. This is the Critical assertion:
	// a forwarded socket must never be walked against the local process table.
	if polledHost.LocalProc {
		t.Errorf("host.LocalProc = true for a remote host; this is the Critical defect that " +
			"made 97 of 3117 local pids falsely answer 'agent here' on a remote pane")
	}

	// Assert the newly created pane appears in the registry.
	panes := reg.Panes()
	var foundNew bool
	for _, p := range panes {
		if p.Host == "remote" && p.PaneID == newPaneID {
			foundNew = true
			break
		}
	}
	if !foundNew {
		t.Errorf("poll did not see the newly created pane %s; got %d panes", newPaneID, len(panes))
	}
}
