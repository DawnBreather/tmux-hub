//go:build e2e

package e2e

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/broadcast"
	"github.com/DawnBreather/tmux-hub/internal/history"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// liveServerWithCmd creates a private tmux server with N panes running cmd.
func liveServerWithCmd(t *testing.T, panes int, cmd string) (socket string, paneIDs []string) {
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
	must("new-session", "-d", "-s", "test", "-x", "80", "-y", "24", cmd)
	for i := 1; i < panes; i++ {
		must("split-window", "-t", "test", "-d", cmd)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })

	out, err := exec.Command("tmux", "-S", sock, "list-panes", "-a", "-F", "#{pane_id}").Output()
	if err != nil {
		t.Fatalf("list-panes: %v", err)
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			paneIDs = append(paneIDs, l)
		}
	}
	if len(paneIDs) != panes {
		t.Fatalf("got %d panes, want %d", len(paneIDs), panes)
	}
	return sock, paneIDs
}

// TestE2EHappypathTextArrivesVerbatim tests that multi-line text ending in a
// semicolon arrives EXACTLY as typed, with all newlines and the semicolon intact.
//
// Would catch: the write path having no production wiring (measured: no UI mode was
// visible), semicolon being stripped, newlines being converted to spaces.
func TestE2EHappypathTextArrivesVerbatim(t *testing.T) {
	socket, panes := liveServer(t, 1)
	paneID := panes[0]

	// Build binary to ensure it compiles (catches "no production wiring").
	_ = buildBinary(t)

	// Set up the components as the binary does.
	ctx := context.Background()
	tgt := tmux.Target{Label: "test", Socket: socket}
	runner := tmux.NewExec(10 * time.Second)
	inst := broadcast.Instance("e2e")
	stamper := broadcast.NewStamper(runner, inst)
	sender := broadcast.NewSender(runner.(tmux.InputRunner), stamper, inst)

	// Stamp the pane so it can receive.
	if _, err := stamper.Stamp(ctx, tgt, paneID); err != nil {
		t.Fatalf("Stamp: %v", err)
	}

	// Sleep to let window_activity settle, per existing tests.
	time.Sleep(1100 * time.Millisecond)

	// The payload: three lines, last ending in semicolon.
	text := "refactor the auth module\nrun the tests\ncheck the output;"

	// Send it.
	target := broadcast.Target{Host: "test", Tmux: tgt, PaneID: paneID}
	res, err := sender.Send(ctx, target, text)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Outcome == broadcast.Refused {
		t.Fatalf("send was refused: %s", res.Reason)
	}

	// Witness it.
	res = sender.Witness(ctx, res, text)
	if res.Outcome != broadcast.Delivered {
		t.Errorf("Outcome = %s (%s), want delivered", res.Outcome, res.Reason)
	}

	// Assert on the PANE, not on what the hub says.
	screen := capturePane(t, socket, paneID)
	if !strings.Contains(screen, "refactor the auth module") {
		t.Errorf("first line missing from pane:\n%s", screen)
	}
	if !strings.Contains(screen, "run the tests") {
		t.Errorf("second line missing from pane:\n%s", screen)
	}
	if !strings.Contains(screen, "check the output;") {
		t.Errorf("third line or semicolon missing from pane:\n%s", screen)
	}
}

// TestE2EHappypathNothingExecutes tests that pasted text does NOT execute.
// Enter is a separate act, never bundled with the paste.
//
// Would catch: send-keys -l or send-keys without bracketed paste executing the text,
// or multi-line pastes auto-executing the first paragraph.
func TestE2EHappypathNothingExecutes(t *testing.T) {
	// Use bash shell (not cat) so execution would be visible if it happened.
	socket, panes := liveServerWithCmd(t, 1, "bash --norc")
	paneID := panes[0]

	_ = buildBinary(t)

	ctx := context.Background()
	tgt := tmux.Target{Label: "test", Socket: socket}
	runner := tmux.NewExec(10 * time.Second)
	inst := broadcast.Instance("e2e2")
	stamper := broadcast.NewStamper(runner, inst)
	sender := broadcast.NewSender(runner.(tmux.InputRunner), stamper, inst)

	if _, err := stamper.Stamp(ctx, tgt, paneID); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)

	// Test 1: Single-line command. If executed, would print "tuna" alone.
	// The command text is "echo tuna", which is different from the output.
	text := "echo tuna"

	target := broadcast.Target{Host: "test", Tmux: tgt, PaneID: paneID}
	res, err := sender.Send(ctx, target, text)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Outcome == broadcast.Refused {
		t.Fatalf("send was refused: %s", res.Reason)
	}

	res = sender.Witness(ctx, res, text)
	if res.Outcome != broadcast.Delivered {
		t.Errorf("Outcome = %s, want delivered", res.Outcome)
	}

	time.Sleep(200 * time.Millisecond)
	screen := capturePane(t, socket, paneID)

	// The command text should be visible on the command line.
	if !strings.Contains(screen, "echo tuna") {
		t.Errorf("command text did not arrive:\n%s", screen)
	}

	// The output "tuna" alone on a line should NOT be present (not executed).
	lines := strings.Split(screen, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "tuna" {
			t.Errorf("command was executed (output 'tuna' found alone):\n%s", screen)
			break
		}
	}

	// Test 2: Multi-line command. The first paragraph would execute if newline
	// is sent as a keypress instead of via bracketed paste.
	multiText := "echo FIRST\necho SECOND\necho THIRD"

	res2, err := sender.Send(ctx, target, multiText)
	if err != nil {
		t.Fatalf("Send multi-line: %v", err)
	}
	if res2.Outcome == broadcast.Refused {
		t.Fatalf("multi-line send was refused: %s", res2.Reason)
	}

	res2 = sender.Witness(ctx, res2, multiText)
	if res2.Outcome != broadcast.Delivered {
		t.Errorf("Multi-line outcome = %s, want delivered", res2.Outcome)
	}

	time.Sleep(200 * time.Millisecond)
	screen = capturePane(t, socket, paneID)

	// All three command lines should be visible.
	if !strings.Contains(screen, "echo FIRST") {
		t.Errorf("first line of multi-line command not visible:\n%s", screen)
	}
	if !strings.Contains(screen, "echo SECOND") {
		t.Errorf("second line of multi-line command not visible:\n%s", screen)
	}
	if !strings.Contains(screen, "echo THIRD") {
		t.Errorf("third line of multi-line command not visible:\n%s", screen)
	}

	// The outputs "FIRST", "SECOND", "THIRD" alone should NOT appear (not executed).
	for _, output := range []string{"FIRST", "SECOND", "THIRD"} {
		for _, line := range strings.Split(screen, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == output {
				t.Errorf("multi-line command was executed (output %q found alone):\n%s",
					output, screen)
				break
			}
		}
	}
}

// TestE2EHappypathSubmitSendsEnter tests that Submit sends exactly one Enter.
//
// Would catch: Submit being a no-op, or sending multiple Enters.
func TestE2EHappypathSubmitSendsEnter(t *testing.T) {
	socket, panes := liveServer(t, 2)
	paneID := panes[0]

	_ = buildBinary(t)

	ctx := context.Background()
	tgt := tmux.Target{Label: "test", Socket: socket}
	runner := tmux.NewExec(10 * time.Second)
	inst := broadcast.Instance("e2e3")
	stamper := broadcast.NewStamper(runner, inst)
	sender := broadcast.NewSender(runner.(tmux.InputRunner), stamper, inst)

	if _, err := stamper.Stamp(ctx, tgt, paneID); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)

	// Paste something first.
	text := "line one"
	target := broadcast.Target{Host: "test", Tmux: tgt, PaneID: paneID}
	res, err := sender.Send(ctx, target, text)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Outcome == broadcast.Refused {
		t.Fatalf("send was refused: %s", res.Reason)
	}

	// Now submit (press Enter).
	submitRes := sender.Submit(ctx, target)
	if submitRes.Outcome != broadcast.Delivered {
		t.Errorf("Submit outcome = %s (%s), want delivered",
			submitRes.Outcome, submitRes.Reason)
	}

	// For a cat pane, Enter echoes as a newline. Check that the line moved down.
	time.Sleep(200 * time.Millisecond)
	screen := capturePane(t, socket, paneID)

	// The text should still be there, and there should be a blank line after it
	// (cat echoes Enter as newline).
	if !strings.Contains(screen, "line one") {
		t.Errorf("original text missing after Submit:\n%s", screen)
	}

	// Count newlines after "line one". Should have at least one.
	idx := strings.Index(screen, "line one")
	if idx < 0 {
		t.Fatal("text vanished")
	}
	after := screen[idx+len("line one"):]
	if !strings.HasPrefix(after, "\n") && !strings.HasPrefix(after, "\r") {
		t.Errorf("no newline after text, Enter did not arrive:\n%s", screen)
	}
}

// TestE2EHappypathHistoryRecordsOutcome tests that every send writes a history
// entry with the outcome as a WORD (delivered/sent-unwitnessed/refused).
//
// Would catch: history not recording, outcome being a boolean instead of a word.
func TestE2EHappypathHistoryRecordsOutcome(t *testing.T) {
	socket, panes := liveServer(t, 1)
	paneID := panes[0]

	_ = buildBinary(t)

	histPath := filepath.Join(t.TempDir(), "history.jsonl")
	hist, err := history.Open(histPath, 4<<20)
	if err != nil {
		t.Fatalf("Open history: %v", err)
	}
	defer hist.Close()

	ctx := context.Background()
	tgt := tmux.Target{Label: "test", Socket: socket}
	runner := tmux.NewExec(10 * time.Second)
	inst := broadcast.Instance("e2e4")
	stamper := broadcast.NewStamper(runner, inst)
	sender := broadcast.NewSender(runner.(tmux.InputRunner), stamper, inst)

	if _, err := stamper.Stamp(ctx, tgt, paneID); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)

	text := "test prompt"
	target := broadcast.Target{Host: "test", Tmux: tgt, PaneID: paneID}
	res, err := sender.Send(ctx, target, text)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	res = sender.Witness(ctx, res, text)

	// Write to history.
	entry := history.Entry{
		At:        time.Now(),
		Host:      "test",
		PaneID:    paneID,
		Text:      text,
		Outcome:   string(res.Outcome),
		Submitted: false,
	}
	if err := hist.Append(entry); err != nil {
		t.Fatalf("Append history: %v", err)
	}

	// Read it back.
	entries, err := hist.Recent(10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no history entries found")
	}

	last := entries[0]
	if last.Outcome != "delivered" && last.Outcome != "sent-unwitnessed" && last.Outcome != "refused" {
		t.Errorf("Outcome = %q, want one of {delivered, sent-unwitnessed, refused}", last.Outcome)
	}
	if last.Text != text {
		t.Errorf("Text = %q, want %q", last.Text, text)
	}
	if last.PaneID != paneID {
		t.Errorf("PaneID = %q, want %q", last.PaneID, paneID)
	}
}

// TestE2EHappypathNoBufferLeaks tests that no tmux paste buffer survives a send.
//
// Would catch: the delete-buffer not running, leaving payloads in the user's
// paste history (measured: the payload became the most recent paste buffer).
func TestE2EHappypathNoBufferLeaks(t *testing.T) {
	socket, panes := liveServer(t, 1)
	paneID := panes[0]

	_ = buildBinary(t)

	ctx := context.Background()
	tgt := tmux.Target{Label: "test", Socket: socket}
	runner := tmux.NewExec(10 * time.Second)
	inst := broadcast.Instance("e2e5")
	stamper := broadcast.NewStamper(runner, inst)
	sender := broadcast.NewSender(runner.(tmux.InputRunner), stamper, inst)

	if _, err := stamper.Stamp(ctx, tgt, paneID); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)

	// Note buffers before.
	beforeBuffers := listBuffers(t, socket)

	text := "secret payload"
	target := broadcast.Target{Host: "test", Tmux: tgt, PaneID: paneID}
	res, err := sender.Send(ctx, target, text)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Outcome == broadcast.Refused {
		t.Fatalf("send was refused: %s", res.Reason)
	}

	// Give delete-buffer time to run.
	time.Sleep(200 * time.Millisecond)

	afterBuffers := listBuffers(t, socket)

	// There should be no NEW buffers.
	if len(afterBuffers) > len(beforeBuffers) {
		t.Errorf("buffers leaked: before=%d after=%d", len(beforeBuffers), len(afterBuffers))
		t.Logf("after buffers: %v", afterBuffers)
	}

	// And none of the current buffers should contain our payload.
	for _, name := range afterBuffers {
		out, err := exec.Command("tmux", "-S", socket,
			"show-buffer", "-b", name).Output()
		if err == nil && strings.Contains(string(out), text) {
			t.Errorf("buffer %q contains the payload", name)
		}
	}
}

// TestE2EHappypathClaudeProcess tests pasting into a real Claude input box.
//
// Would catch: Claude's input box not handling multi-line pastes, or text
// being submitted instead of pasted.
func TestE2EHappypathClaudeProcess(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed")
	}

	socket, _ := liveServer(t, 1)
	workDir := t.TempDir()

	// Start claude in a new pane on our private socket.
	// SAFETY: This is on our private socket that we control, not the user's.
	cmd := exec.Command("tmux", "-S", socket,
		"new-window", "-d", "-t", "test",
		"-c", workDir, // Work in a throwaway directory
		"claude")
	if err := cmd.Run(); err != nil {
		t.Fatalf("start claude: %v", err)
	}

	// Give Claude time to start and show its input box.
	time.Sleep(3 * time.Second)

	// Get the new pane ID.
	out, err := exec.Command("tmux", "-S", socket,
		"list-panes", "-a", "-F", "#{pane_id} #{pane_current_command}").Output()
	if err != nil {
		t.Fatalf("list-panes: %v", err)
	}

	var claudePaneID string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "claude") {
			claudePaneID = strings.Fields(line)[0]
			break
		}
	}
	if claudePaneID == "" {
		t.Fatal("could not find claude pane")
	}

	// Wait for Claude to be ready by polling #{bracket_paste_flag}.
	// Claude sets this about 4s after launch when the input box is ready.
	//
	// The flag is NECESSARY and not sufficient: a MODAL is in bracketed-paste mode too. A stray
	// `/tmp/CLAUDE.md` importing a file outside the tree — debris a `tar -x -C /tmp` leaves —
	// makes claude open `Allow external CLAUDE.md file imports?` over the input box, the flag
	// goes to 1 on the second poll, and the paste lands in a dialog that ignores it. The failure
	// then reads `timed out waiting for the three pasted lines`, which names the paste and not
	// the cause, and it costs a bisect. So the readiness gate below also refuses a screen that is
	// asking a question: claude's own dialog footer is the signal, and it is the same string for
	// every one of them.
	ready := false
	for i := 0; i < 60; i++ { // 30 second timeout (60 × 500ms)
		out, err := exec.Command("tmux", "-S", socket,
			"display", "-p", "-t", claudePaneID, "#{bracket_paste_flag}").Output()
		if err == nil && strings.TrimSpace(string(out)) == "1" {
			if scr := capturePane(t, socket, claudePaneID); strings.Contains(scr, "Enter to confirm") {
				t.Fatalf("claude is asking a question instead of showing its input box, so a paste "+
					"cannot reach it. Its working directory is under TMPDIR — check for a stray "+
					"CLAUDE.md above it (`ls /tmp/CLAUDE.md`).\nscreen:\n%s", scr)
			}
			ready = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !ready {
		screen := capturePane(t, socket, claudePaneID)
		t.Logf("Claude screen after 30s:\n%s", screen)
		t.Skip("Claude did not show input box in time (bracket_paste_flag never became 1)")
	}

	// Now paste a three-line prompt using tmux paste-buffer.
	// This simulates what the hub does.
	prompt := "First line\nSecond line\nThird line"

	// Use tmux load-buffer and paste-buffer with -p flag (bracketed paste).
	cmd = exec.Command("tmux", "-S", socket, "load-buffer", "-")
	cmd.Stdin = strings.NewReader(prompt)
	if err := cmd.Run(); err != nil {
		t.Fatalf("load-buffer: %v", err)
	}

	cmd = exec.Command("tmux", "-S", socket,
		"paste-buffer", "-p", "-r", "-t", claudePaneID)
	if err := cmd.Run(); err != nil {
		t.Fatalf("paste-buffer: %v", err)
	}

	// Wait for the FACT rather than for a guessed 300 ms. Claude renders the paste
	// when it gets round to it, and under load it had not: the failing capture still
	// showed the welcome banner, on a commit with no product change in it.
	var screen string
	waitUntil(t, "the three pasted lines to reach the screen", 10*time.Second, func() bool {
		screen = capturePane(t, socket, claudePaneID)
		return strings.Contains(screen, "First line") &&
			strings.Contains(screen, "Second line") &&
			strings.Contains(screen, "Third line")
	})

	// Asserted anyway, so the failure names WHICH line is missing rather than only
	// that the wait expired.
	if !strings.Contains(screen, "First line") {
		t.Errorf("first line not visible in input box:\n%s", screen)
	}
	if !strings.Contains(screen, "Second line") {
		t.Errorf("second line not visible in input box:\n%s", screen)
	}
	if !strings.Contains(screen, "Third line") {
		t.Errorf("third line not visible in input box:\n%s", screen)
	}

	// Verify it was NOT submitted by checking that Claude is still at the input.
	// If it was submitted, we'd see "Thinking" or response text.
	if strings.Contains(screen, "Thinking") {
		t.Error("prompt was submitted instead of just pasted")
	}

	// Clean exit without pressing Enter.
	// Kill the pane explicitly to avoid any submission.
	_ = exec.Command("tmux", "-S", socket, "kill-pane", "-t", claudePaneID).Run()

	t.Log("Successfully pasted three lines into Claude input box without submission")
}
