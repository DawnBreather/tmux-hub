package broadcast

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

func liveServer(t *testing.T, panes int) (tmux.Target, []string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := filepath.Join(t.TempDir(), "tmux.sock")
	must := func(args ...string) {
		t.Helper()
		full := append([]string{"-S", sock, "-f", "/dev/null"}, args...)
		if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
	}
	// `cat` panes: input is echoed verbatim and nothing is ever executed, so a
	// test can assert on what ARRIVED rather than on what tmux said.
	must("new-session", "-d", "-s", "w", "-x", "80", "-y", "24", "cat")
	for i := 1; i < panes; i++ {
		must("split-window", "-t", "w", "-d", "cat")
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })

	out, err := exec.Command("tmux", "-S", sock, "list-panes", "-a", "-F", "#{pane_id}").Output()
	if err != nil {
		t.Fatalf("list-panes: %v", err)
	}
	var ids []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			ids = append(ids, l)
		}
	}
	if len(ids) != panes {
		t.Fatalf("got %d panes, want %d", len(ids), panes)
	}
	return tmux.Target{Label: "test", Socket: sock}, ids
}

func TestStampIsPaneScopedAndReadableBack(t *testing.T) {
	tgt, ids := liveServer(t, 2)
	r := tmux.NewExec(10 * time.Second)
	s := NewStamper(r, Instance("t1"))

	tok, err := s.Stamp(context.Background(), tgt, ids[0])
	if err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	if tok == "" {
		t.Fatal("Stamp returned an empty token")
	}

	res, err := r.Run(context.Background(), tgt, "list-panes", "-a",
		"-F", "#{pane_id} [#{@hub_t1}]")
	if err != nil {
		t.Fatalf("list-panes: %v", err)
	}
	if !strings.Contains(res.Stdout, ids[0]+" ["+tok+"]") {
		t.Errorf("the stamped pane does not carry the token:\n%s", res.Stdout)
	}
	// The measured fail-open: `set -t <pane>` with no -p lands at SESSION scope,
	// and an unstamped pane then resolves the value and passes the guard.
	if !strings.Contains(res.Stdout, ids[1]+" []") {
		t.Errorf("the token leaked to another pane — -p is missing:\n%s", res.Stdout)
	}
}

// The option must be invisible outside the pane, or it is a global mutation the
// user could trip over.
func TestStampIsInvisibleGlobally(t *testing.T) {
	tgt, ids := liveServer(t, 1)
	r := tmux.NewExec(10 * time.Second)
	s := NewStamper(r, Instance("t2"))
	if _, err := s.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	res, _ := r.Run(context.Background(), tgt, "show", "-gv", "@hub_t2")
	if res.RC == 0 && strings.TrimSpace(res.Stdout) != "" {
		t.Errorf("the option is visible server-wide: %q", res.Stdout)
	}
}

// Re-stamping must produce a NEW token. A pane-bound token proves pane identity,
// not process identity: respawn-pane keeps the id, the pane_pid and the token
// while replacing the process.
func TestReStampRotatesTheToken(t *testing.T) {
	tgt, ids := liveServer(t, 1)
	s := NewStamper(tmux.NewExec(10*time.Second), Instance("t3"))
	a, err := s.Stamp(context.Background(), tgt, ids[0])
	if err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	b, err := s.Stamp(context.Background(), tgt, ids[0])
	if err != nil {
		t.Fatalf("re-Stamp: %v", err)
	}
	if a == b {
		t.Error("re-stamping reused the token, so a stale selection would still pass")
	}
	if got, ok := s.Token("test", ids[0]); !ok || got != b {
		t.Errorf("Token() = (%q, %v), want the newest token", got, ok)
	}
}

func TestUnstampRemovesTheOptionAndTheMemory(t *testing.T) {
	tgt, ids := liveServer(t, 1)
	r := tmux.NewExec(10 * time.Second)
	s := NewStamper(r, Instance("t4"))
	if _, err := s.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	if err := s.Unstamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Unstamp: %v", err)
	}
	res, _ := r.Run(context.Background(), tgt, "list-panes", "-a",
		"-F", "#{pane_id} [#{@hub_t4}]")
	if !strings.Contains(res.Stdout, ids[0]+" []") {
		t.Errorf("the option survived Unstamp:\n%s", res.Stdout)
	}
	if _, ok := s.Token("test", ids[0]); ok {
		t.Error("the hub still remembers a token it has unstamped")
	}
}

// Unstamping a pane that is already gone is routine, not exceptional: it is what
// happens every time an agent exits.
func TestUnstampAVanishedPaneIsNotAnError(t *testing.T) {
	tgt, _ := liveServer(t, 1)
	s := NewStamper(tmux.NewExec(10*time.Second), Instance("t5"))
	if err := s.Unstamp(context.Background(), tgt, "%999"); err != nil {
		t.Errorf("Unstamp of a vanished pane = %v, want nil", err)
	}
}
