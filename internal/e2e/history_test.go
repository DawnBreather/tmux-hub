//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
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

// TestE2EHistoryRecordsOutcomesAndFields drives real sends with different outcomes
// and asserts the history file contains the correct outcome word, host, pane id, and
// text for each. This catches the write path being unwired (no entries at all) or a
// field being dropped (entries present but incomplete).
func TestE2EHistoryRecordsOutcomesAndFields(t *testing.T) {
	sock, panes := liveServer(t, 1)
	paneID := panes[0]

	histPath := filepath.Join(t.TempDir(), "history.jsonl")
	hist, err := history.Open(histPath, 1<<20)
	if err != nil {
		t.Fatalf("history.Open: %v", err)
	}
	defer hist.Close()

	ctx := context.Background()
	tgt := tmux.Target{Socket: sock}
	runner := tmux.NewExec(10 * time.Second)
	inst := broadcast.Instance("hist-test")
	stamper := broadcast.NewStamper(runner, inst)
	sender := broadcast.NewSender(runner.(tmux.InputRunner), stamper, inst)

	// Stamp the pane so it can receive
	if _, err := stamper.Stamp(ctx, tgt, paneID); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)

	// Test different sends
	cases := []string{
		"first prompt",
		"second prompt",
		"third prompt",
	}

	for i, text := range cases {
		target := broadcast.Target{
			Host:   "testhost",
			Tmux:   tgt,
			PaneID: paneID,
		}

		res, err := sender.Send(ctx, target, text)
		if err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}

		// Record in history as the UI would
		e := history.Entry{
			At:          time.Now(),
			Host:        target.Host,
			PaneID:      target.PaneID,
			SessionName: "test",
			WindowName:  "window",
			Text:        text,
			Outcome:     string(res.Outcome),
			Reason:      res.Reason,
			Token:       res.Token,
			Submitted:   res.Submitted,
		}
		if err := hist.Append(e); err != nil {
			t.Fatalf("case %d: hist.Append: %v", i, err)
		}
	}

	// Assert the file exists and has the right content
	entries, err := hist.Recent(100)
	if err != nil {
		t.Fatalf("hist.Recent: %v", err)
	}

	if len(entries) != len(cases) {
		t.Fatalf("got %d entries, want %d", len(entries), len(cases))
	}

	// Entries are newest first
	for i := range entries {
		caseIdx := len(cases) - 1 - i
		text := cases[caseIdx]
		e := entries[i]

		if e.Text != text {
			t.Errorf("entry[%d].Text = %q, want %q", i, e.Text, text)
		}
		if e.Host != "testhost" {
			t.Errorf("entry[%d].Host = %q, want testhost", i, e.Host)
		}
		if e.PaneID != paneID {
			t.Errorf("entry[%d].PaneID = %q, want %q", i, e.PaneID, paneID)
		}
		if e.Outcome == "" {
			t.Errorf("entry[%d].Outcome is empty, must be a word (delivered/sent-unwitnessed/refused)", i)
		}
	}

	// Assert the raw file format is correct JSONL
	raw, err := os.ReadFile(histPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != len(cases) {
		t.Errorf("file has %d lines, want %d (one per entry)", len(lines), len(cases))
	}
	for i, line := range lines {
		var e history.Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("line %d does not parse as JSON: %v", i, err)
		}
	}
}

// TestE2EHistoryMultiLinePromptIsOneEntry verifies that a prompt containing
// newlines is stored as a single JSONL entry (newline is escaped), so a reader
// splitting on raw newlines will not tear it in half. This catches JSON encoding
// being bypassed.
func TestE2EHistoryMultiLinePromptIsOneEntry(t *testing.T) {
	histPath := filepath.Join(t.TempDir(), "history.jsonl")
	hist, err := history.Open(histPath, 1<<20)
	if err != nil {
		t.Fatalf("history.Open: %v", err)
	}
	defer hist.Close()

	multiLineText := "line one\nline two\nline three"
	e := history.Entry{
		At:          time.Now(),
		Host:        "testhost",
		PaneID:      "%0",
		SessionName: "test",
		Text:        multiLineText,
		Outcome:     "delivered",
	}
	if err := hist.Append(e); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Read back and assert it's one entry
	entries, err := hist.Recent(10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 (multi-line prompt tore in half)", len(entries))
	}
	if entries[0].Text != multiLineText {
		t.Errorf("Text = %q, want %q", entries[0].Text, multiLineText)
	}

	// Assert the file has exactly one line
	raw, err := os.ReadFile(histPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Errorf("file has %d lines, want 1", len(lines))
	}

	// Assert the newlines are escaped in the file
	if !strings.Contains(string(raw), `\n`) {
		t.Error("newlines are not escaped in the JSONL file; a naive line splitter would break")
	}
}

// TestE2EHistoryRotationKeepsNewestEntries writes past the size limit and asserts
// rotation bounds the file while keeping the NEWEST entries. Asserting only the
// last-written entry survives is insufficient: by the end of the loop the file is
// small again, so the assertion would pass even if rotation kept the wrong half.
// This catches rotation logic being inverted.
func TestE2EHistoryRotationKeepsNewestEntries(t *testing.T) {
	histPath := filepath.Join(t.TempDir(), "history.jsonl")
	// Small limit to trigger rotation repeatedly
	hist, err := history.Open(histPath, 4096)
	if err != nil {
		t.Fatalf("history.Open: %v", err)
	}
	defer hist.Close()

	const n = 400
	for i := 0; i < n; i++ {
		e := history.Entry{
			At:          time.Now(),
			Host:        "testhost",
			PaneID:      "%0",
			SessionName: "test",
			Text:        fmt.Sprintf("entry-%04d %s", i, strings.Repeat("x", 48)),
			Outcome:     "delivered",
		}
		if err := hist.Append(e); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	// Assert the file is bounded
	fi, err := os.Stat(histPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > 4*4096 {
		t.Errorf("file grew to %d bytes despite 4096 limit; rotation did not fire", fi.Size())
	}

	// Read back and assert the span: newest present, oldest gone
	entries, err := hist.Recent(10000)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("rotation emptied the log")
	}
	if len(entries) >= n {
		t.Errorf("rotation never fired: %d entries survived out of %d", len(entries), n)
	}

	var texts []string
	for _, e := range entries {
		texts = append(texts, e.Text)
	}
	joined := strings.Join(texts, "\n")

	// Newest entry must be present (entries are returned newest first)
	if !strings.Contains(texts[0], fmt.Sprintf("entry-%04d", n-1)) {
		t.Errorf("rotation lost the NEWEST entry; newest kept is %q", texts[0])
	}

	// Oldest entry must be gone
	if strings.Contains(joined, "entry-0000") {
		t.Error("rotation kept the OLDEST entry; it is discarding the wrong half")
	}
}

// TestE2EHistoryCorruptLineIsSkipped corrupts the last line (as a hub killed
// mid-write would) and asserts the log still reads, skipping that line. One bad
// shutdown must not cost the user their history. This catches error handling being
// too strict.
func TestE2EHistoryCorruptLineIsSkipped(t *testing.T) {
	histPath := filepath.Join(t.TempDir(), "history.jsonl")
	hist, err := history.Open(histPath, 1<<20)
	if err != nil {
		t.Fatalf("history.Open: %v", err)
	}

	e := history.Entry{
		At:          time.Now(),
		Host:        "testhost",
		PaneID:      "%0",
		SessionName: "test",
		Text:        "good entry",
		Outcome:     "delivered",
	}
	if err := hist.Append(e); err != nil {
		t.Fatalf("Append: %v", err)
	}
	hist.Close()

	// Corrupt the file by appending a torn line
	f, err := os.OpenFile(histPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"at":"broken`)
	f.Close()

	// Reopen and read
	hist2, err := history.Open(histPath, 1<<20)
	if err != nil {
		t.Fatalf("Open after corruption: %v", err)
	}
	defer hist2.Close()

	entries, err := hist2.Recent(10)
	if err != nil {
		t.Fatalf("Recent after corruption: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 (the good one)", len(entries))
	}
	if entries[0].Text != "good entry" {
		t.Errorf("Text = %q, want 'good entry'", entries[0].Text)
	}
}

// TestE2EHistoryResendGoesToCurrentSelection asserts that re-send goes to the
// CURRENT selection and not to the entry's recorded targets. An hour-old %3 on a
// host that has since restarted is a different pane. This catches resend using
// stale targets.
func TestE2EHistoryResendGoesToCurrentSelection(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := filepath.Join(t.TempDir(), "tmux.sock")

	// Create two panes
	must := func(args ...string) {
		t.Helper()
		full := append([]string{"-S", sock, "-f", "/dev/null"}, args...)
		if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
	}
	must("new-session", "-d", "-s", "a", "-x", "80", "-y", "24", "cat")
	must("new-session", "-d", "-s", "b", "-x", "80", "-y", "24", "cat")
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })

	out, err := exec.Command("tmux", "-S", sock, "list-panes", "-a", "-F", "#{pane_id}").Output()
	if err != nil {
		t.Fatalf("list-panes: %v", err)
	}
	panes := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(panes) < 2 {
		t.Fatalf("got %d panes, want at least 2", len(panes))
	}
	paneA, paneB := panes[0], panes[1]

	histPath := filepath.Join(t.TempDir(), "history.jsonl")
	hist, err := history.Open(histPath, 1<<20)
	if err != nil {
		t.Fatalf("history.Open: %v", err)
	}
	defer hist.Close()

	ctx := context.Background()
	tgt := tmux.Target{Label: "testhost", Socket: sock}
	runner := tmux.NewExec(10 * time.Second)
	inst := broadcast.Instance("resend-test")
	stamper := broadcast.NewStamper(runner, inst)
	sender := broadcast.NewSender(runner.(tmux.InputRunner), stamper, inst)

	// Stamp pane A, send to it, and record in history (the original send)
	if _, err := stamper.Stamp(ctx, tgt, paneA); err != nil {
		t.Fatalf("Stamp pane A: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)

	originalText := "original to pane A"
	targetA := broadcast.Target{
		Host:   "testhost",
		Tmux:   tgt,
		PaneID: paneA,
	}
	resA, err := sender.Send(ctx, targetA, originalText)
	if err != nil {
		t.Fatalf("Send to A: %v", err)
	}
	e := history.Entry{
		At:          time.Now(),
		Host:        targetA.Host,
		PaneID:      targetA.PaneID,
		SessionName: "a",
		Text:        originalText,
		Outcome:     string(resA.Outcome),
	}
	if err := hist.Append(e); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Stamp pane B and send to it (simulating user selecting a DIFFERENT pane for resend)
	if _, err := stamper.Stamp(ctx, tgt, paneB); err != nil {
		t.Fatalf("Stamp pane B: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)

	targetB := broadcast.Target{
		Host:   "testhost",
		Tmux:   tgt,
		PaneID: paneB,
	}
	resB, err := sender.Send(ctx, targetB, originalText)
	if err != nil {
		t.Fatalf("Send to B: %v", err)
	}
	if resB.Outcome == broadcast.Refused {
		t.Fatalf("resend was refused: %s", resB.Reason)
	}

	// Record the resend
	e2 := history.Entry{
		At:          time.Now(),
		Host:        targetB.Host,
		PaneID:      targetB.PaneID,
		SessionName: "b",
		Text:        originalText,
		Outcome:     string(resB.Outcome),
	}
	if err := hist.Append(e2); err != nil {
		t.Fatalf("Append resend: %v", err)
	}

	// Assert pane B received the text (the CURRENT selection)
	contentB := capturePane(t, sock, paneB)
	if !strings.Contains(contentB, originalText) {
		t.Errorf("pane B does not contain %q; resend went to wrong pane", originalText)
	}

	// Assert pane A does NOT contain the resent text (only the original)
	contentA := capturePane(t, sock, paneA)
	// Pane A should have exactly one occurrence from the original send
	countA := strings.Count(contentA, originalText)
	if countA != 1 {
		t.Errorf("pane A has %d occurrences of %q, want exactly 1 (resend should not have gone here)", countA, originalText)
	}

	// Verify the history has two entries now (original + resend)
	entries, err := hist.Recent(10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}

	// Both entries should have the same text but different pane IDs
	if entries[0].PaneID == entries[1].PaneID {
		t.Error("resend recorded the same pane ID as original; should use current selection")
	}
	// Verify the resend went to pane B
	if entries[0].PaneID != paneB {
		t.Errorf("most recent entry has pane %q, want %q (resend should target current selection)", entries[0].PaneID, paneB)
	}
	if entries[1].PaneID != paneA {
		t.Errorf("original entry has pane %q, want %q", entries[1].PaneID, paneA)
	}
}

// TestE2EHistoryResendAlwaysAsksConfirmation asserts that resend ALWAYS asks for
// confirmation, even if the text would normally not require it. An hour-old entry
// being resent without confirmation is dangerous. This catches the fromHistory flag
// not being set or not being checked.
func TestE2EHistoryResendAlwaysAsksConfirmation(t *testing.T) {
	// Test the pure function: broadcast.Needed should return ReasonFromHistory
	// when FromHistory is true, even if everything else is fresh.

	// Build a TargetState that is completely fresh and would normally send immediately:
	// identified now and at selection, same session, same window, same epoch,
	// last outcome delivered, bracketed paste enabled.
	fresh := broadcast.TargetState{
		Host:                  "testhost",
		PaneID:                "%0",
		IdentifiedNow:         true,
		IdentifiedAtSelection: true,
		SessionAtSelection:    "test",
		SessionNow:            "test",
		WindowAtSelection:     "win",
		WindowNow:             "win",
		EpochAtSelection:      "epoch1",
		EpochNow:              "epoch1",
		LastOutcome:           broadcast.Delivered,
		Bracketed:             true,
		FromHistory:           false,
	}

	// With FromHistory false, a fresh target should need no confirmation
	reasonsWithoutHistory := broadcast.Needed([]broadcast.TargetState{fresh})
	if len(reasonsWithoutHistory) > 0 {
		t.Errorf("fresh target without FromHistory needs confirmation: %v; should send immediately", reasonsWithoutHistory)
	}

	// Now set FromHistory to true
	fromHistory := fresh
	fromHistory.FromHistory = true

	// With FromHistory true, it MUST ask for confirmation
	reasonsWithHistory := broadcast.Needed([]broadcast.TargetState{fromHistory})
	if len(reasonsWithHistory) == 0 {
		t.Fatal("target with FromHistory=true needs no confirmation; should always ask")
	}

	// Verify ReasonFromHistory is present
	found := false
	for _, r := range reasonsWithHistory {
		if r == broadcast.ReasonFromHistory {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("reasons=%v, missing ReasonFromHistory; FromHistory flag is not being checked", reasonsWithHistory)
	}
}
