//go:build e2e

package e2e

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// TestE2EASilentPaneIsNotWorkingBecauseItsSiblingIs is the same case as
// registry.TestAPaneIsNotWorkingBecauseItsSiblingIs against a REAL server, and it is
// not redundant: the unit test hand-builds the deltas, so it asserts the hub's model of
// tmux. This one asserts tmux — that `#{window_activity}` really is shared by two panes
// in one window, that `#{history_size}` really moves for only the pane that wrote, and
// that the captures really differ, all through the poll path the product runs.
//
// The defect it guards: Classify returned `works` from the activity age before it tested
// anything else, and the age came from a per-WINDOW timestamp. A pane sharing a window
// with a chatty sibling was pinned at `works`, rank 4, the bottom of the inbox — so a
// pane sitting on a failure could not be seen, which is what the hub exists for.
func TestE2EASilentPaneIsNotWorkingBecauseItsSiblingIs(t *testing.T) {
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
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })

	// One window, two panes, so they SHARE #{window_activity} — that sharing is the
	// defect's precondition, and it is asserted below rather than assumed.
	must("new-session", "-d", "-s", "n1", "-x", "80", "-y", "24",
		"sh", "-c", "printf 'fatal: not a git repository\\n'; sleep 300")
	must("split-window", "-t", "n1", "-d",
		"sh", "-c", "while true; do date; sleep 0.3; done")

	ctx := context.Background()
	r := tmux.NewExec(10 * time.Second)
	tgt := tmux.Target{Label: "test", Socket: sock}

	// Give the chatty pane time to fill some scrollback.
	time.Sleep(2 * time.Second)

	// One registry across both polls: the second tick must be a COMPARISON against the
	// first, not a cold start, because first sight legitimately seeds from the window
	// timestamp.
	reg := registry.New()
	poll := func(now time.Time) map[string]registry.Pane {
		t.Helper()
		ds, err := tmux.FetchDeltas(ctx, r, tgt)
		if err != nil {
			t.Fatalf("FetchDeltas: %v", err)
		}
		snap, err := tmux.FetchSnapshot(ctx, r, tgt, ds, nil)
		if err != nil {
			t.Fatalf("FetchSnapshot: %v", err)
		}
		reg.Update("test", ds, snap.Labels, snap.Zones, snap.Fulls, now, time.Second)
		out := map[string]registry.Pane{}
		for _, p := range reg.Panes() {
			out[p.PaneID] = p
		}
		// THE PRECONDITION, asserted rather than noted: both panes must report the SAME
		// #{window_activity}. Without that this test would pass for the wrong reason —
		// there would be no coupling left to defeat — and a control that cannot fail is
		// not a control. If tmux ever grows a per-pane timestamp, this is what says so,
		// and registry.markActivity can then be simplified.
		if len(ds) != 2 {
			t.Fatalf("got %d deltas, want 2", len(ds))
		}
		if ds[0].Activity != ds[1].Activity {
			t.Fatalf("the two panes report different window_activity (%d vs %d) — the "+
				"coupling this test defeats is gone, so re-read registry.markActivity "+
				"before trusting either",
				ds[0].Activity, ds[1].Activity)
		}
		return out
	}

	t0 := time.Now()
	first := poll(t0)
	if len(first) != 2 {
		t.Fatalf("got %d panes, want 2", len(first))
	}

	// FreshFor is max(2*pollInterval, 4s) and the interval passed above is 1s, so six
	// seconds clears it with room. The silent pane writes nothing in that time; the
	// chatty one writes twenty times.
	time.Sleep(6 * time.Second)
	second := poll(t0.Add(6 * time.Second))

	// Identify the panes by what they show, not by index: split-window's numbering is
	// not something this test should depend on.
	var silent, chatty registry.Pane
	for _, p := range second {
		if strings.Contains(strings.Join(p.Zone, "\n"), "fatal: not a git repository") {
			silent = p
		} else {
			chatty = p
		}
	}
	if silent.PaneID == "" || chatty.PaneID == "" {
		t.Fatalf("could not tell the two panes apart: %#v", second)
	}

	// The sibling wrote, so the hub must still call it works.
	if got := chatty.State(); got != state.Works {
		t.Errorf("the chatty pane %s = %v, want works", chatty.PaneID, got)
	}
	// And the silent one must have fallen through to its screen.
	if got := silent.State(); got != state.Error {
		t.Errorf("the silent pane %s = %v, want error — it has shown `fatal: not a git "+
			"repository` for six seconds and only its SIBLING has written anything. "+
			"Its zone is %q", silent.PaneID, got, silent.Zone)
	}
	// The mechanism, so a future failure says WHICH half broke: the silent pane's
	// recorded activity must not have advanced with its sibling's.
	if !silent.Activity.Equal(first[silent.PaneID].Activity) {
		t.Errorf("the silent pane's Activity moved from %v to %v without it writing "+
			"anything — that is the per-window timestamp leaking back in",
			first[silent.PaneID].Activity, silent.Activity)
	}
	if !chatty.Activity.After(first[chatty.PaneID].Activity) {
		t.Errorf("the chatty pane's Activity did not advance (%v → %v) although it wrote "+
			"about twenty lines", first[chatty.PaneID].Activity, chatty.Activity)
	}
}
