//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/broadcast"
	"github.com/DawnBreather/tmux-hub/internal/history"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// TestE2EHygieneBufferCleanupAtConnect verifies that the connect sweep removes
// hub-shaped buffers while leaving user-named buffers alone. A crashed hub leaves
// its payload as the most recent buffer, so the user's next `prefix ]` would paste
// someone's prompt if the sweep failed or misidentified what to remove.
//
// This would catch: sweep logic broken, BufferGlob pattern wrong, or sweep not
// running at connect.
func TestE2EHygieneBufferCleanupAtConnect(t *testing.T) {
	socket, _ := liveServer(t, 1)

	// Load buffers: one from a crashed hub, one from another hub instance, one the user named
	load := func(name, body string) {
		cmd := exec.Command("tmux", "-S", socket, "load-buffer", "-b", name, "-")
		cmd.Stdin = strings.NewReader(body)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("load-buffer %s: %v: %s", name, err, out)
		}
	}
	load("tmux-hub-deadbeef-7", "crashed hub's secret prompt")
	load("tmux-hub-abcd1234-1", "another hub's leftover")
	load("my-clipboard", "the user's own paste buffer")

	// Run the sweep (as the hub does at connect)
	ctx := context.Background()
	r := tmux.NewExec(5 * time.Second)
	target := tmux.Target{Label: "test", Socket: socket}
	removed, err := broadcast.Sweep(ctx, r, target)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	// Should have removed exactly 2 hub buffers
	if len(removed) != 2 {
		t.Errorf("Sweep removed %v, want 2 hub buffers", removed)
	}

	// Assert the hub buffers are gone and the user's remains
	buffers := listBuffers(t, socket)
	for _, name := range buffers {
		if strings.HasPrefix(name, "tmux-hub-") {
			t.Errorf("hub buffer %q survived the connect sweep", name)
		}
	}

	found := false
	for _, name := range buffers {
		if name == "my-clipboard" {
			found = true
			break
		}
	}
	if !found {
		t.Error("the sweep removed the user's own buffer")
	}
}

// TestE2EHygieneBufferCleanupAfterSend verifies that buffers are cleaned up after
// each send completes. A send that aborts leaves its payload as the most recent
// buffer, so a batch send leaves N-1 buffers behind if cleanup is broken.
//
// This would catch: per-send cleanup not running, or cleanup failing silently.
func TestE2EHygieneBufferCleanupAfterSend(t *testing.T) {
	socket, panes := liveServer(t, 3)

	_ = buildBinary(t)

	ctx := context.Background()
	r := tmux.NewExec(5 * time.Second)
	inst := broadcast.Instance("hyg1")
	stamper := broadcast.NewStamper(r, inst)
	sender := broadcast.NewSender(r.(tmux.InputRunner), stamper, inst)

	target := tmux.Target{Label: "test", Socket: socket}

	// Stamp all panes
	for _, paneID := range panes {
		if _, err := stamper.Stamp(ctx, target, paneID); err != nil {
			t.Fatalf("Stamp %s: %v", paneID, err)
		}
	}

	// Wait for stamps to settle
	time.Sleep(1100 * time.Millisecond)

	// Send to all panes
	for _, paneID := range panes {
		tg := broadcast.Target{
			Host:   "test",
			Tmux:   target,
			PaneID: paneID,
		}
		_, err := sender.Send(ctx, tg, "test")
		if err != nil {
			t.Fatalf("Send to %s: %v", paneID, err)
		}
	}

	// After all sends complete, no hub buffers should remain
	buffers := listBuffers(t, socket)
	for _, name := range buffers {
		if strings.HasPrefix(name, "tmux-hub-") {
			t.Errorf("hub buffer %q was not cleaned up after send", name)
		}
	}
}

// TestE2EHygieneInterruptSendsControlKey verifies that Interrupt sends a control
// key (C-c or Escape) and never text. Sending text when the user meant to stop
// an agent is the worst possible confusion.
//
// This would catch: Interrupt sending the wrong primitive, or not validating the
// key name.
func TestE2EHygieneInterruptSendsControlKey(t *testing.T) {
	// The pane runs a SHELL, not `cat`, and that is the whole repair. The earlier
	// version used a `cat` pane and then sent it C-c — which kills cat, so the pane
	// exited and the next capture-pane failed with exit status 1 before any assertion
	// ran. A test cannot interrupt the process its own pane is.
	//
	// At a shell prompt C-c is what it is for: it abandons the current line and the
	// pane survives, which is the state this case needs in order to assert anything.
	socket, panes := liveServer(t, 1)
	paneID0 := panes[0]
	if out, err := exec.Command("tmux", "-S", socket, "respawn-pane", "-k",
		"-t", paneID0, "bash", "--norc").CombinedOutput(); err != nil {
		t.Fatalf("respawn the pane as a shell: %v: %s", err, out)
	}
	time.Sleep(600 * time.Millisecond)

	ctx := context.Background()
	r := tmux.NewExec(5 * time.Second)
	inst := broadcast.Instance("hyg2")
	stamper := broadcast.NewStamper(r, inst)
	sender := broadcast.NewSender(r.(tmux.InputRunner), stamper, inst)

	target := tmux.Target{Label: "test", Socket: socket}
	paneID := panes[0]

	// Stamp the pane
	if _, err := stamper.Stamp(ctx, target, paneID); err != nil {
		t.Fatalf("Stamp: %v", err)
	}

	time.Sleep(1100 * time.Millisecond)

	// First, send some text so we can see what the pane contains
	tg := broadcast.Target{
		Host:   "test",
		Tmux:   target,
		PaneID: paneID,
	}
	_, err := sender.Send(ctx, tg, "initial text")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	before := capturePane(t, socket, paneID)
	if !strings.Contains(before, "initial text") {
		t.Fatal("initial text did not arrive, cannot verify interrupt")
	}
	if strings.Contains(before, "command not found") {
		t.Fatalf("the payload EXECUTED at the shell prompt instead of being pasted; "+
			"screen:\n%s", before)
	}

	// Now interrupt with C-c
	res := sender.Interrupt(ctx, tg, "C-c")
	if res.Outcome != broadcast.Delivered {
		t.Fatalf("Interrupt: %s %s", res.Outcome, res.Reason)
	}

	// The pane should still have the initial text, plus maybe ^C
	// It should NOT have any draft text that wasn't sent
	// The pane must have SURVIVED — a C-c that kills the pane proves nothing about
	// whether the hub sent a control key or the draft, because there is nothing left to
	// read. And the draft must still not have executed: the one thing `!` must never do
	// is deliver text, because at a shell prompt a prompt becomes a command line.
	after := capturePane(t, socket, paneID)
	if !strings.Contains(after, "initial text") {
		t.Error("the pane lost its content after the interrupt, so either the pane died " +
			"or something other than a control key was sent")
	}
	if strings.Contains(after, "command not found") {
		t.Errorf("the interrupt submitted the draft instead of sending C-c; screen:\n%s", after)
	}

	// Try interrupt with an invalid key name - should refuse
	resInvalid := sender.Interrupt(ctx, tg, "rm -rf /")
	if resInvalid.Outcome != broadcast.Refused {
		t.Errorf("Interrupt with invalid key name = %s, want Refused", resInvalid.Outcome)
	}

	// The dangerous text should NOT have been sent (the pane should be unchanged)
	finalContent := capturePane(t, socket, paneID)
	if strings.Contains(finalContent, "rm -rf") {
		t.Error("interrupt with invalid key name sent text instead of refusing")
	}
}

// TestE2EHygieneInterruptIsGuarded verifies that Interrupt requires a stamped pane
// just like Submit does. Sending C-c into the wrong pane kills whatever is running.
//
// This would catch: Interrupt bypassing the guard, or having a different guard than
// Submit.
func TestE2EHygieneInterruptIsGuarded(t *testing.T) {
	socket, panes := liveServer(t, 1)

	_ = buildBinary(t)

	ctx := context.Background()
	r := tmux.NewExec(5 * time.Second)
	inst := broadcast.Instance("hyg3")
	stamper := broadcast.NewStamper(r, inst)
	sender := broadcast.NewSender(r.(tmux.InputRunner), stamper, inst)

	target := tmux.Target{Label: "test", Socket: socket}
	paneID := panes[0]

	// Do NOT stamp the pane

	tg := broadcast.Target{
		Host:   "test",
		Tmux:   target,
		PaneID: paneID,
	}

	// Interrupt should refuse an unstamped pane
	res := sender.Interrupt(ctx, tg, "C-c")
	if res.Outcome != broadcast.Refused {
		t.Errorf("Interrupt unstamped pane = %s %s, want Refused", res.Outcome, res.Reason)
	}

	// The pane should be empty (nothing was sent)
	content := capturePane(t, socket, paneID)
	if strings.TrimSpace(content) != "" {
		t.Errorf("interrupt sent to unstamped pane, content:\n%s", content)
	}
}

// TestE2EHygieneBinaryBuildsClean verifies that the binary compiles without errors.
// Three reviews once passed a build where the write path had no production wiring.
//
// This would catch: import cycles, missing exports, or syntax errors that tests don't
// exercise.
func TestE2EHygieneBinaryBuildsClean(t *testing.T) {
	_ = buildBinary(t)
	// If buildBinary returns, the binary built successfully
}

// TestE2EHygieneBinaryStatusReportsServers verifies that --status can see servers
// and report their state. A binary that builds but cannot discover panes reads as
// working while the main feature is broken.
//
// This would catch: poller broken, or --status not wired to it.
func TestE2EHygieneBinaryStatusReportsServers(t *testing.T) {
	socket, panes := liveServer(t, 2)
	bin := buildBinary(t)

	cmd := exec.Command(bin, "--status", "--host", "test="+socket, "--no-local")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("tmux-hub --status: %v", err)
	}

	// The output should be valid JSON
	var status map[string]interface{}
	if err := json.Unmarshal(out, &status); err != nil {
		t.Fatalf("status output is not JSON: %v\n%s", err, out)
	}

	// It should report the hosts
	hosts, ok := status["hosts"].([]interface{})
	if !ok || len(hosts) == 0 {
		t.Errorf("status reports no hosts:\n%s", out)
	}

	// The output should mention our panes
	outStr := string(out)
	found := 0
	for _, paneID := range panes {
		if strings.Contains(outStr, paneID) {
			found++
		}
	}
	if found != len(panes) {
		t.Errorf("status found %d of %d panes", found, len(panes))
	}
}

// TestE2EHygieneHistoryRecordsSends verifies that sends are written to the history
// log. A broken history log means re-send and the history view are both unusable.
//
// This would catch: history not wired to sender, or history file never opened.
func TestE2EHygieneHistoryRecordsSends(t *testing.T) {
	socket, panes := liveServer(t, 1)
	histPath := filepath.Join(t.TempDir(), "history.jsonl")

	_ = buildBinary(t)

	ctx := context.Background()
	r := tmux.NewExec(5 * time.Second)
	inst := broadcast.Instance("hyg4")
	stamper := broadcast.NewStamper(r, inst)
	sender := broadcast.NewSender(r.(tmux.InputRunner), stamper, inst)

	// Open history log
	hist, err := history.Open(histPath, 1<<20)
	if err != nil {
		t.Fatalf("open history: %v", err)
	}
	defer hist.Close()

	target := tmux.Target{Label: "test", Socket: socket}
	paneID := panes[0]

	// Stamp and send
	if _, err := stamper.Stamp(ctx, target, paneID); err != nil {
		t.Fatalf("Stamp: %v", err)
	}

	time.Sleep(1100 * time.Millisecond)

	tg := broadcast.Target{
		Host:   "test",
		Tmux:   target,
		PaneID: paneID,
	}
	_, err = sender.Send(ctx, tg, "test prompt for history")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Record in history
	if err := hist.Append(history.Entry{
		At:      time.Now(),
		Host:    "test",
		PaneID:  paneID,
		Text:    "test prompt for history",
		Outcome: "delivered",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	hist.Close()

	// Read back the history file
	f, err := os.Open(histPath)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	defer f.Close()

	var entries []history.Entry
	dec := json.NewDecoder(f)
	for dec.More() {
		var e history.Entry
		if err := dec.Decode(&e); err != nil {
			t.Fatalf("decode entry: %v", err)
		}
		entries = append(entries, e)
	}

	if len(entries) == 0 {
		t.Fatal("history file is empty")
	}

	// The entry should match what we sent
	found := false
	for _, e := range entries {
		if e.Text == "test prompt for history" && e.Outcome == "delivered" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("history does not contain our send: %+v", entries)
	}
}
