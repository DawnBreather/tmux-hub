package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/broadcast"
	"github.com/DawnBreather/tmux-hub/internal/history"
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/launch"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// recordingRunner tracks all tmux commands issued, for asserting order and content.
// It implements both Runner and InputRunner (i.e., Exec).
type recordingRunner struct {
	calls    []string
	paneID   string // what new-window returns
	windowID string // what display returns for window_id
}

func (r *recordingRunner) Run(_ context.Context, _ tmux.Target, args ...string) (tmux.Result, error) {
	r.calls = append(r.calls, strings.Join(args, " "))

	// list-sessions is how a launch into a new WINDOW learns which session to put it in. `$0` used
	// to be hard-coded there, and `$0` is only the first session a server ever had — kill it and
	// every such launch failed with `can't find session: $0`, which is what an operator reported.
	// A fake that answered nothing here made these three tests refuse with `has no tmux session to
	// put a window in`, correctly: that is what a server with no sessions deserves.
	if len(args) > 0 && args[0] == "list-sessions" {
		return tmux.Result{RC: 0, Stdout: "$3\n"}, nil
	}

	// new-window -P -F '#{pane_id}' returns the pane id
	if len(args) > 0 && args[0] == "new-window" {
		return tmux.Result{RC: 0, Stdout: r.paneID + "\n"}, nil
	}

	// new-session -P -F '#{pane_id}' returns the pane id
	if len(args) > 0 && args[0] == "new-session" {
		return tmux.Result{RC: 0, Stdout: r.paneID + "\n"}, nil
	}

	// display -p -t <pane> '#{window_id}' returns the window id
	if len(args) > 0 && args[0] == "display" && contains(args, "#{window_id}") {
		return tmux.Result{RC: 0, Stdout: r.windowID + "\n"}, nil
	}

	// All other commands succeed silently
	return tmux.Result{RC: 0}, nil
}

func (r *recordingRunner) RunInput(ctx context.Context, t tmux.Target, stdin []byte, args ...string) (tmux.Result, error) {
	// For now, just delegate to Run — our launch tests don't use stdin
	return r.Run(ctx, t, args...)
}

func contains(args []string, s string) bool {
	for _, a := range args {
		if a == s {
			return true
		}
	}
	return false
}

func TestLaunchCreatesStampsAndAdoptsInThatOrder(t *testing.T) {
	// Order matters: the pane must be stamped and adopted BEFORE the user can
	// select it, or the first send falls back to the inference path for a pane
	// whose identity was never in doubt.
	r := &recordingRunner{paneID: "%42", windowID: "@7"}
	stamper := broadcast.NewStamper(r, broadcast.Instance("test"))
	keeper := broadcast.NewKeeper(stamper)

	m := model{
		ctx:     context.Background(),
		run:     r,
		stamper: stamper,
		keeper:  keeper,
		hosts:   []hub.Host{{Label: "local", Socket: "/tmp/test.sock"}},
	}

	spec := launch.Spec{
		Host:  "local",
		CWD:   "/srv/api",
		Model: "opus",
	}

	// Execute the launch
	cmd := m.launch(spec)
	msg := cmd()

	// Check that launch succeeded
	lmsg, ok := msg.(launchMsg)
	if !ok {
		t.Fatalf("expected launchMsg, got %T", msg)
	}
	if lmsg.err != nil {
		t.Fatalf("launch failed: %v", lmsg.err)
	}
	if lmsg.paneID != "%42" {
		t.Fatalf("expected pane id %q, got %q", "%42", lmsg.paneID)
	}

	// Assert command order by INDEX: new-window BEFORE display BEFORE set -w BEFORE set -p
	// This is the load-bearing property: stamp and adopt must complete before the user can select the pane.
	idxWindow := -1
	idxDisplay := -1
	idxRemain := -1
	idxStamp := -1

	for i, c := range r.calls {
		if strings.Contains(c, "new-window") {
			idxWindow = i
		}
		if strings.Contains(c, "display") && strings.Contains(c, "#{window_id}") {
			idxDisplay = i
		}
		if strings.Contains(c, "set -w") && strings.Contains(c, "remain-on-exit") {
			idxRemain = i
		}
		if strings.Contains(c, "set -p") && strings.Contains(c, "@hub_") {
			idxStamp = i
		}
	}

	if idxWindow == -1 {
		t.Fatalf("new-window not called. Calls: %v", r.calls)
	}
	if idxDisplay == -1 {
		t.Fatalf("display for window_id not called. Calls: %v", r.calls)
	}
	if idxRemain == -1 {
		t.Fatalf("remain-on-exit not set. Calls: %v", r.calls)
	}
	if idxStamp == -1 {
		t.Fatalf("pane not stamped. Calls: %v", r.calls)
	}

	// Assert order: new-window < display < set -w < set -p
	if idxWindow >= idxDisplay {
		t.Errorf("new-window (index %d) must come BEFORE display (index %d). Calls: %v", idxWindow, idxDisplay, r.calls)
	}
	if idxDisplay >= idxRemain {
		t.Errorf("display (index %d) must come BEFORE set -w remain-on-exit (index %d). Calls: %v", idxDisplay, idxRemain, r.calls)
	}
	if idxRemain >= idxStamp {
		t.Errorf("set -w remain-on-exit (index %d) must come BEFORE set -p stamp (index %d). Calls: %v", idxRemain, idxStamp, r.calls)
	}

	// Check that the pane was adopted (which happens after stamping)
	if !keeper.Identified("local", "%42") {
		t.Error("pane was not adopted — Identified returned false")
	}
}

func TestLaunchSetsRemainOnExitOnITSOWNWindowOnly(t *testing.T) {
	// A global set would change the behaviour of the user's own windows. The
	// hub's footprint stays inside what it created (docs/design.md §19).
	r := &recordingRunner{paneID: "%42", windowID: "@7"}
	stamper := broadcast.NewStamper(r, broadcast.Instance("test"))
	keeper := broadcast.NewKeeper(stamper)

	m := model{
		ctx:     context.Background(),
		run:     r,
		stamper: stamper,
		keeper:  keeper,
		hosts:   []hub.Host{{Label: "local", Socket: "/tmp/test.sock"}},
	}

	spec := launch.Spec{
		Host:  "local",
		CWD:   "/srv/api",
		Model: "opus",
	}

	// Execute the launch
	cmd := m.launch(spec)
	_ = cmd()

	// Verify no global set of remain-on-exit
	for _, c := range r.calls {
		if strings.HasPrefix(c, "set -g") && strings.Contains(c, "remain-on-exit") {
			t.Fatalf("remain-on-exit must never be set globally: %q", c)
		}
	}

	// Verify that remain-on-exit WAS set on a window
	found := false
	for _, c := range r.calls {
		if strings.Contains(c, "set -w") && strings.Contains(c, "remain-on-exit") {
			found = true
			break
		}
	}
	if !found {
		t.Error("remain-on-exit was not set at all — it must be set on the window")
	}
}

func TestAFailedLaunchSaysWhatToDoAndShowsInView(t *testing.T) {
	// A failure must not leave a half-adopted pane. The error message must appear
	// in View() so the user sees it, and it must carry the fix.
	r := &recordingRunner{paneID: "%42", windowID: "@7"}
	stamper := broadcast.NewStamper(r, broadcast.Instance("test"))
	keeper := broadcast.NewKeeper(stamper)

	m := model{
		ctx:     context.Background(),
		run:     r,
		stamper: stamper,
		keeper:  keeper,
		hosts:   []hub.Host{{Label: "local", Socket: "/tmp/test.sock"}},
		width:   80,
		height:  24,
	}

	// Launch with a nonexistent host
	spec := launch.Spec{
		Host:  "nonexistent",
		CWD:   "/srv/api",
		Model: "opus",
	}

	cmd := m.launch(spec)
	msg := cmd()

	// Update the model with the error message
	m2, _ := m.Update(msg)
	updated := m2.(model)

	// The error must appear in View()
	view := updated.View()
	if !strings.Contains(view, "launch failed") {
		t.Errorf("View() does not show launch error. View: %q", view)
	}
	if !strings.Contains(view, "nonexistent") {
		t.Errorf("View() does not name the failed host. View: %q", view)
	}

	// The pane must NOT be adopted when launch fails
	if keeper.Identified("nonexistent", "%42") {
		t.Error("a failed launch must not adopt the pane")
	}
}

func TestLaunchIsRecordedInTheHistoryLog(t *testing.T) {
	// A created session is an action with a consequence, exactly like a send.
	r := &recordingRunner{paneID: "%42", windowID: "@7"}
	stamper := broadcast.NewStamper(r, broadcast.Instance("test"))
	keeper := broadcast.NewKeeper(stamper)

	// Create a real history log in a temp file.
	tmpDir := t.TempDir()
	histPath := tmpDir + "/history.jsonl"
	hist, err := history.Open(histPath, 1<<20) // 1 MB for testing
	if err != nil {
		t.Fatalf("Open history: %v", err)
	}
	defer hist.Close()

	m := model{
		ctx:     context.Background(),
		run:     r,
		stamper: stamper,
		keeper:  keeper,
		hist:    hist,
		hosts:   []hub.Host{{Label: "local", Socket: "/tmp/test.sock"}},
	}

	spec := launch.Spec{
		Host:  "local",
		CWD:   "/srv/api",
		Model: "opus",
	}

	// Execute the launch
	cmd := m.launch(spec)
	msg := cmd()

	// Check that launch succeeded
	lmsg, ok := msg.(launchMsg)
	if !ok {
		t.Fatalf("expected launchMsg, got %T", msg)
	}
	if lmsg.err != nil {
		t.Fatalf("launch failed: %v", lmsg.err)
	}

	// Verify that history was recorded
	entries, err := hist.Recent(10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(entries))
	}

	e := entries[0]
	if e.Host != "local" {
		t.Errorf("history entry Host = %q, want %q", e.Host, "local")
	}
	if e.PaneID != "%42" {
		t.Errorf("history entry PaneID = %q, want %q", e.PaneID, "%42")
	}
	if e.Outcome != "launched" {
		t.Errorf("history entry Outcome = %q, want %q", e.Outcome, "launched")
	}
	if !strings.Contains(e.Text, "claude") || !strings.Contains(e.Text, "--session-id") {
		t.Errorf("history entry Text does not contain the launch command: %q", e.Text)
	}
}
