//go:build e2e

package e2e

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A SECOND `a` on a row of another server goes to the window the first one opened, and the window is
// named after the ROW (docs/design.md §21.18).
//
// This is the operator's own report, measured on their server before the fix: five presses had left
// five windows all called `nuc`, all attached to one remote session, which reported `attached=5`. It
// was not only clutter in `C-b w` — `window-size` is `latest` on both ends, so each new attach
// RESIZED the shared session, leaving the older windows drawing a session wider than their terminal.
//
// It is an INTERFACE case because the parts that can be wrong are all outside the model: whether the
// lookup's format reaches tmux, whether tmux answers it for a window the hub created in the same
// session, and whether the name the create passes survives `automatic-rename`. A unit test with a
// fake runner answers none of those.
func TestE2EUIPossessionASecondAOpensNoSecondWindow(t *testing.T) {
	ui, _, paneIDs, _ := hubWith(t, 120, 40, 1, "sleep 300")

	// The hub lives in window 0 and `a` moves the CLIENT, so every key after the first press has to
	// name that window rather than the session — otherwise it lands in the attach.
	hub := "ui:0"
	sendTo := func(keys ...string) {
		t.Helper()
		args := append([]string{"-S", ui, "send-keys", "-t", hub}, keys...)
		if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
			t.Fatalf("send-keys %v: %v: %s", keys, err, out)
		}
	}
	windows := func() []string {
		t.Helper()
		out, err := exec.Command("tmux", "-S", ui, "list-windows", "-t", "ui",
			"-F", "#{window_index}:#{window_name}").Output()
		if err != nil {
			t.Fatalf("list-windows: %v", err)
		}
		var got []string
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if l != "" {
				got = append(got, l)
			}
		}
		return got
	}

	if got := windows(); len(got) != 1 {
		t.Fatalf("the fixture starts with %d windows, want 1 (the hub): %v", len(got), got)
	}
	walkTo(t, ui, hubRowNeedle(paneIDs[0], paneIDs))

	send(t, ui, "a")
	waitUntil(t, "the attach window to open", 20*time.Second, func() bool {
		return len(windows()) == 2
	})
	opened := windows()

	// The NAME, which is the other half of the report: the session tree used to say `nuc` for every
	// one of these, matching nothing the dashboard showed. `scratch` is the host label this fixture
	// gives the target server and `watched` is the session on it.
	var name string
	for _, w := range opened {
		if !strings.HasPrefix(w, "0:") {
			name = w
		}
	}
	if !strings.Contains(name, "scratch/watched") {
		t.Errorf("the attach window is called %q, want the host and the row's own name — a window "+
			"named after the host alone is what made five of these indistinguishable", name)
	}

	// The second press. It must go to the window that is already showing this session.
	sendTo("a")
	time.Sleep(3 * time.Second) // long enough for a second create to have landed
	after := windows()
	if len(after) != 2 {
		t.Errorf("%d windows after two presses, want 2 (the hub and ONE attach): %v", len(after), after)
	}
	// And the hub says which of the two happened, because "you are already there" and "nothing
	// happened" look identical on a screen the hub does not draw.
	waitUntil(t, "the hub to say the window was reused", 15*time.Second, func() bool {
		s, err := paneScreen(t, ui, hub)
		return err == nil && strings.Contains(s, "already open")
	})
}
