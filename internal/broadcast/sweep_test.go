package broadcast

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// The sweep must remove ANOTHER instance's leftovers, because the case worth
// cleaning is a hub that crashed mid-send — and it must leave the user's own
// buffers strictly alone.
func TestSweepRemovesEveryHubBufferAndNothingElse(t *testing.T) {
	tgt, _ := liveServer(t, 1)
	r := tmux.NewExec(10 * time.Second)

	load := func(name, body string) {
		t.Helper()
		cmd := exec.Command("tmux", "-S", tgt.Socket, "load-buffer", "-b", name, "-")
		cmd.Stdin = strings.NewReader(body)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("load-buffer %s: %v: %s", name, err, out)
		}
	}
	load("tmux-hub-dead1-7", "a crashed hub's secret prompt")
	load("tmux-hub-ab12cd-1", "our own leftover")
	load("mine", "the user's own clipboard")

	removed, err := Sweep(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(removed) != 2 {
		t.Errorf("Sweep removed %v, want both hub buffers", removed)
	}

	out, _ := exec.Command("tmux", "-S", tgt.Socket, "list-buffers",
		"-F", "#{buffer_name}").Output()
	if strings.Contains(string(out), "tmux-hub-") {
		t.Errorf("a hub buffer survived the sweep:\n%s", out)
	}
	if !strings.Contains(string(out), "mine") {
		t.Errorf("the sweep took the user's own buffer:\n%s", out)
	}
}

// A server with no buffers at all answers rc=1, which is not a failure.
func TestSweepOnAnEmptyServer(t *testing.T) {
	tgt, _ := liveServer(t, 1)
	removed, err := Sweep(context.Background(), tmux.NewExec(10*time.Second), tgt)
	if err != nil {
		t.Fatalf("Sweep on an empty server = %v, want nil", err)
	}
	if len(removed) != 0 {
		t.Errorf("Sweep removed %v from an empty server", removed)
	}
}
