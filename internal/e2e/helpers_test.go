//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// capturePane reads a pane's visible screen from a private server.
//
// It lives here, with one owner, because three of this package's groups need it and the
// alternative is three copies of the same six lines — which is the duplication this
// project spent a fix round removing from the renderer. It was originally written inside
// one group's file, another group called it across the file boundary, the owner renamed
// it, and the package stopped compiling. A helper two files apart is a coupling nobody
// is watching; a helper with an owner is not.
//
// Every E2E assertion about delivery reads the PANE through this, never what the hub
// printed: a confirmation fires whenever the pane resolves and the guard passed, so it
// cannot see whether any bytes arrived. That is the measured failure the whole write
// path is built around.
func capturePane(t *testing.T, socket, paneID string) string {
	t.Helper()
	screen, err := paneScreen(t, socket, paneID)
	if err != nil {
		t.Fatalf("capture-pane %s on %s: %v", paneID, socket, err)
	}
	return screen
}

// paneScreen is the same read with the failure handed back instead of fatal, and the
// two share one implementation so the "one owner" rule above still holds. A poll that
// watches a window survive its payload needs it: there, a pane that is GONE is an
// answer about the product (the window closed with its payload) rather than a broken
// harness, and capture-pane is what reports it.
func paneScreen(t *testing.T, socket, paneID string) (string, error) {
	t.Helper()
	out, err := exec.Command("tmux", "-S", socket, "capture-pane", "-p", "-t", paneID).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// liveServer creates a private tmux server with N cat panes for safe testing.
func liveServer(t *testing.T, panes int) (socket string, paneIDs []string) {
	t.Helper()
	return liveServerWithCmd(t, panes, "cat")
}

// buildBinary compiles tmux-hub once per test run.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "tmux-hub")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/tmux-hub")
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v: %s", err, out)
	}
	return bin
}

// repoRoot finds the repository root from this test file's location.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	// From internal/e2e, go up two levels.
	return filepath.Join(wd, "..", "..")
}

// listBuffers returns paste buffer names currently in the server.
func listBuffers(t *testing.T, socket string) []string {
	t.Helper()
	out, err := exec.Command("tmux", "-S", socket, "list-buffers", "-F", "#{buffer_name}").Output()
	if err != nil {
		// No buffers is fine, tmux returns rc=1.
		return nil
	}
	var names []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			names = append(names, l)
		}
	}
	return names
}

// waitUntil polls until the condition holds, or fails at the deadline.
//
// It exists because a fixed sleep against a live TUI is a coin flip, and this
// suite lost two tests to that. `TestE2EHappypathClaudeProcess` slept 300 ms after
// pasting three lines into a real Claude pane and then asserted they were on
// screen; under load the capture still showed Claude's welcome banner. Measured on
// this machine, both that test and the hide resurface test failed on a commit that
// contained no product change at all, and both pass in isolation — which is the
// signature of a timing assumption, not of a defect.
//
// A green suite that is green only on an idle machine is worse than a red one: the
// next real regression arrives looking exactly like the flake, and gets dismissed
// as one. The rule this repo already wrote down is to wait on the FACT.
func waitUntil(t *testing.T, what string, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if fn() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
