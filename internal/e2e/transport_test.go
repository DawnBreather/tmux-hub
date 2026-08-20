//go:build e2e

package e2e

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// TestE2EARemoteHostIsPolledOverOneMaster proves a remote host is polled over one
// master, with no forward anywhere, and the argv the seam built is what ran.
// Everything below the master is already covered by unit tests; what only a real
// host can show is that the two shells between here and tmux leave the command
// intact.
func TestE2EARemoteHostIsPolledOverOneMaster(t *testing.T) {
	host := os.Getenv("HUB_E2E_HOST")
	if host == "" {
		t.Skip("HUB_E2E_HOST unset — needs an ssh destination with tmux")
	}
	dir := t.TempDir()
	ctl := filepath.Join(dir, "cm")
	m := &hub.Master{Alias: host, ControlPath: ctl}
	raw := tmux.NewRawRunner(20 * time.Second)
	if err := m.Ensure(context.Background(), raw); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// A session on the far side, on a PRIVATE socket, so nothing of the user's is
	// touched and docs/design.md §9's socket override is exercised at the same time.
	//
	// The override rides on the TARGET, which is what lets this test drive the
	// product's own fetch functions rather than hand-written formats. It used to pass
	// `-L hube2e` as the first two elements of argv, and that had a second cost beyond
	// the duplication: Validate reads the verb out of argv[0], so a leading `-L` made
	// every verb collapse to the pane-only default. On the target, the args land after
	// the transport decision and the verb stays where the shape check can see it.
	tgt := tmux.Target{Label: host, SSHDest: host, ControlPath: ctl, TmuxArgs: []string{"-L", "hube2e"}}
	r := tmux.NewExec(20 * time.Second)

	// The master goes down whatever happens below. The far SERVER is a separate
	// cleanup, registered only once it is known to be ours — see the isolation check.
	t.Cleanup(func() { m.Stop(context.Background(), tmux.NewRawRunner(10*time.Second)) })

	// Create session with explicit command: two argv words → "sleep 300" (no quotes).
	res, err := r.Run(context.Background(), tgt, "new-session", "-d",
		"-s", "hube2e", "-x", "80", "-y", "24", "sleep", "300")
	if err != nil {
		t.Fatalf("new-session: %v", err)
	}
	if res.RC != 0 {
		t.Fatalf("new-session rc=%d: %s", res.RC, res.Stderr)
	}

	// Isolation is ASSERTED, positively, before anything destructive is promised —
	// because moving `-L hube2e` onto the target is what made this test's isolation
	// depend on the override travelling. Drop TmuxArgs anywhere along that wire and
	// this target addresses the far host's DEFAULT server, where a kill-server would
	// end the operator's own sessions. #{socket_path} is the far server's answer about
	// itself, measured working on tmux 3.2a and 3.7b alike.
	res, err = r.Run(context.Background(), tgt, "display-message", "-p", "#{socket_path}")
	if err != nil {
		t.Fatalf("display-message for the socket path: %v", err)
	}
	if res.RC != 0 {
		t.Fatalf("display-message rc=%d: %s", res.RC, res.Stderr)
	}
	if sock := strings.TrimSpace(res.Stdout); !strings.HasSuffix(sock, "/hube2e") {
		// No kill-server, and deliberately so: a private server started with `sleep
		// 300` ends by itself when its pane's command exits, while a kill-server aimed
		// at the wrong socket cannot be taken back.
		t.Fatalf("the far server answers on socket %q, not the private one this test asked "+
			"for — the socket override did not reach tmux, so this target is addressing "+
			"the host's own server. Nothing was killed", sock)
	}
	// Only now. Cleanup is LIFO, so the server dies before the master it travelled over.
	t.Cleanup(func() {
		_, _ = tmux.NewExec(10*time.Second).Run(context.Background(), tgt, "kill-server")
	})

	// The product's own delta path, over the real ssh transport: DeltaFormat and
	// ParseDelta rather than a format written out here. A format this test spells for
	// itself is a format the product does not have to be using.
	ds, err := tmux.FetchDeltas(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	if len(ds) != 1 {
		t.Fatalf("got %d panes, want 1", len(ds))
	}
	d := ds[0]

	// Assert: the format survived two shells.
	if d.PaneID == "" {
		t.Fatal("PaneID is empty")
	}
	if d.PaneID[0] != '%' {
		t.Errorf("PaneID = %q, want %%N", d.PaneID)
	}

	// Assert: #{pid}:#{start_time} came back, so selections can be invalidated.
	if d.Epoch == "" {
		t.Fatal("Epoch is empty — #{pid}:#{start_time} did not survive")
	}

	// The product's own LABEL path, for the same reason and with more at stake: this
	// repo has already shipped a bug in exactly this spot (c19846c — fetchLabels
	// hardcoded its formats, so pane_start_command was never read), and a test that
	// writes the format itself is structurally unable to catch the next one. The
	// formats now come from labelFormats, over two shells, against a real tmux.
	ls, err := tmux.FetchLabels(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("FetchLabels: %v", err)
	}
	lab, ok := ls[d.PaneID]
	if !ok {
		t.Fatalf("FetchLabels returned no entry for %q, only %v — the label path and the "+
			"delta path disagree about which panes exist", d.PaneID, keysOf(ls))
	}

	// Assert: StartCommand is the exact literal from the measured table — a session
	// created with `sleep 300` as TWO argv words yields "sleep 300" without quotes.
	// Never computed from the command that was passed: no command at all yields EMPTY,
	// and 'sleep 300' as ONE argv word yields "sleep 300" WITH quotes tmux adds itself,
	// so a non-empty check passes on all three and this field has a known value.
	if want := "sleep 300"; lab.StartCommand != want {
		t.Errorf("StartCommand = %q, want %q", lab.StartCommand, want)
	}
	// The rest of the label path came back too, which is what says the formats survived
	// as a SET rather than one lucky field.
	if lab.Session != "hube2e" {
		t.Errorf("Session = %q, want %q", lab.Session, "hube2e")
	}
	if lab.Command == "" {
		t.Error("Command is empty — #{pane_current_command} did not survive")
	}

	// Assert: send-keys with a window target is refused before anything is sent. The
	// seam still guards the write path when the command is destined for ssh — and now
	// with a socket override in play, which is the case where the verb could have
	// stopped being the first thing Validate reads.
	_, err = r.Run(context.Background(), tgt, "send-keys", "-t", "@4", "-l", "X")
	if !errors.Is(err, tmux.ErrBadTarget) {
		t.Errorf("send-keys with window target: got %v, want ErrBadTarget", err)
	}

	// Kill the remote server.
	res, err = r.Run(context.Background(), tgt, "kill-server")
	if err != nil {
		t.Fatalf("kill-server: %v", err)
	}
	if res.RC != 0 {
		t.Errorf("kill-server rc=%d: %s", res.RC, res.Stderr)
	}
}

// keysOf names the panes the label path returned, so a disagreement with the delta path
// says WHICH ids differ rather than only that they do.
func keysOf(m map[string]tmux.Labels) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
