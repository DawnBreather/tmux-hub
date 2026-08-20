//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/hide"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// polledPanes drives the hub's OWN read path against a live server and returns
// what the registry ends up holding — the same functions the poller calls, in
// the same order.
func polledPanes(t *testing.T, socket string) []registry.Pane {
	t.Helper()
	r := tmux.NewExec(5 * time.Second)
	tgt := tmux.Target{Label: "hide", Socket: socket}
	ctx := context.Background()

	ds, err := tmux.FetchDeltas(ctx, r, tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	snap, err := tmux.FetchSnapshot(ctx, r, tgt, ds, nil)
	if err != nil {
		t.Fatalf("FetchSnapshot: %v", err)
	}
	reg := registry.New()
	reg.Update("hide", ds, snap.Labels, snap.Zones, snap.Fulls, time.Now(), 0)
	return reg.Panes()
}

func paneByID(t *testing.T, panes []registry.Pane, id string) registry.Pane {
	t.Helper()
	for _, p := range panes {
		if p.PaneID == id {
			return p
		}
	}
	t.Fatalf("pane %s not among the %d polled panes", id, len(panes))
	return registry.Pane{}
}

// The gap this closes: every hiding unit test HAND-BUILDS a registry.Pane, so
// nothing proved that the fields a real tmux server reports — #{pane_index} in
// the delta and #{pane_start_command} in the labels — reach registry.Pane and
// produce a key that matches the one written to disk. If either piece of
// plumbing were wrong, a mark would simply stop matching after a restart and
// every unit test would still pass. That is this repository's own recorded
// lesson: a loader that forgets a field passes every test that hand-builds the
// struct.
func TestE2EHideKeyFromARealServerSurvivesAReopen(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	socket, ids := liveServer(t, 2)
	noisy := ids[0]

	// Mark it through the real key, built from a really-polled pane.
	path := filepath.Join(t.TempDir(), "hidden.json")
	s, err := hide.Open(path)
	if err != nil {
		t.Fatalf("hide.Open: %v", err)
	}
	before := paneByID(t, polledPanes(t, socket), noisy)
	if before.StartCommand == "" {
		t.Fatalf("a real server reported an EMPTY start command for %s — the corroborator "+
			"half of the key is not reaching registry.Pane, so a persisted mark could only "+
			"ever match by position", noisy)
	}
	if err := s.Toggle(before); err != nil {
		t.Fatalf("Toggle: %v", err)
	}

	// A SECOND poll, and a set re-opened from disk: nothing is carried over in
	// memory, so a match here means the key round-tripped through tmux, the
	// registry and the file.
	reopened, err := hide.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	after := paneByID(t, polledPanes(t, socket), noisy)
	if !reopened.Marked(hide.KeyOf(after)) {
		t.Errorf("the mark did not match a freshly polled pane after a reopen\n  wrote: %+v\n  read:  %+v",
			hide.KeyOf(before), hide.KeyOf(after))
	}

	// H1, END TO END AND FORCED RATHER THAN RACED: rename the window and the mark must
	// still match. The key used to hold the window's NAME, and tmux ships
	// `automatic-rename on`, so a mark on a shell pane un-hid itself the moment the
	// operator ran anything — §18 promises "never show me this again", not "not right
	// now". The two hide tests used to call a waitForSettledWindowNames helper to dodge
	// that rename; the helper is gone, because the defect it dodged is.
	if out, err := exec.Command("tmux", "-S", socket, "rename-window",
		"-t", "$0:0", "renamed-under-the-mark").CombinedOutput(); err != nil {
		t.Fatalf("rename-window: %v: %s", err, out)
	}
	renamed := paneByID(t, polledPanes(t, socket), noisy)
	if renamed.Window != "renamed-under-the-mark" {
		t.Fatalf("the rename did not land: window is %q — without it this case proves "+
			"nothing", renamed.Window)
	}
	if !reopened.Marked(hide.KeyOf(renamed)) {
		t.Errorf("the mark stopped matching after the window was RENAMED\n  wrote: %+v\n  read:  %+v",
			hide.KeyOf(before), hide.KeyOf(renamed))
	}

	// The other pane must NOT inherit it. Both live in the same window at
	// different indices, which is the collision the key is shaped to avoid.
	other := paneByID(t, polledPanes(t, socket), ids[1])
	if reopened.Marked(hide.KeyOf(other)) {
		t.Errorf("pane %s inherited %s's mark — the key does not distinguish siblings\n  %+v\n  %+v",
			ids[1], noisy, hide.KeyOf(other), hide.KeyOf(before))
	}
}

// The resurface rule against real state rather than an assigned field: a marked
// pane is hidden while it works and shown while it waits. Unit tests set
// ClassifiedState directly; here the state comes from state.Classify over a real
// screen, which is the only way to know the two halves agree.
func TestE2EHideResurfacesAPaneThatReallyWaits(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	socket, ids := liveServer(t, 1)
	target := ids[0]

	s, err := hide.Open(filepath.Join(t.TempDir(), "hidden.json"))
	if err != nil {
		t.Fatalf("hide.Open: %v", err)
	}
	// The key holds `window_index`, not the window NAME, so this test does NOT race
	// `automatic-rename` — which is why the helper that used to wait it out is gone. The
	// two polls are re-taken for a different reason: the resurface rule has to be read from
	// real state.Classify output rather than from an assigned field. (This comment
	// previously said the opposite, present tense, after the same change that made it
	// false — and pointed the next reader straight back at re-adding the helper.)
	quiet := paneByID(t, polledPanes(t, socket), target)
	if err := s.Toggle(quiet); err != nil {
		t.Fatalf("Toggle: %v", err)
	}
	if !s.Hidden(quiet) {
		t.Fatalf("a marked pane in state %v must be hidden", quiet.ClassifiedState)
	}

	// Put a real prompt on the real pane. `[y/n]` is one of the shapes
	// state.Classify recognises as waiting on the user.
	run := exec.Command("tmux", "-S", socket, "send-keys", "-t", target, "-l",
		"Overwrite the config? [y/n] ")
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("send-keys: %v: %s", err, out)
	}
	// Wait for the prompt to be ON THE PANE rather than for 600 ms to pass.
	waitUntil(t, "the prompt to reach the pane", 5*time.Second, func() bool {
		return strings.Contains(capturePane(t, socket, target), "[y/n]")
	})

	waiting := paneByID(t, polledPanes(t, socket), target)
	if waiting.ClassifiedState != state.Needs {
		t.Skipf("the live pane classified as %v rather than needs, so this case cannot "+
			"exercise the resurface rule on real state; screen content was %q",
			waiting.ClassifiedState, waiting.Content)
	}
	if s.Hidden(waiting) {
		t.Error("a marked pane that is really WAITING must be shown — the resurface rule " +
			"is what makes a wrong key match unable to lose work (docs/design.md §18)")
	}
	if !s.Marked(hide.KeyOf(waiting)) {
		t.Error("resurfacing must not clear the mark: it is temporary, not an unhide")
	}
}

// Fail toward VISIBLE, against a real file: a hidden set the hub cannot parse
// must show everything and SAY so, because a silently empty set is
// indistinguishable from a real one.
func TestE2EHideAMalformedFileShowsEverythingAndSaysWhy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hidden.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := hide.Open(path)
	if err != nil {
		t.Fatalf("a malformed set must not fail the hub's startup: %v", err)
	}
	if s.Count() != 0 {
		t.Errorf("Count = %d, want 0", s.Count())
	}
	if s.Warning() == "" {
		t.Error("a dropped set must say so, or it reads exactly like an empty one")
	}
}
