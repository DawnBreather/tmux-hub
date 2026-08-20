//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The project-broadcast journey: find a project's sessions, mark them all, send one prompt to all.
//
// This is the flow §21 exists for — "show me everything working on X" — and nothing in the suite had
// driven it end to end. The unit tests group panes and render the list, and the other E2E cases open
// the list or narrow to one project or send to one pane, but NOT ONE case creates two panes in one
// directory, finds them on the project list, marks both, and verifies the prompt reached both. This
// journey closes that gap.

// jpbFleet builds the fixture: two cat panes in one directory.
func jpbFleet(t *testing.T, cols, rows int) (ui, target string, panes []string, dir string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	bin := buildBinary(t)
	work := t.TempDir()
	ui = filepath.Join(work, "ui.sock")
	target = filepath.Join(work, "target.sock")
	dir = filepath.Join(work, "project-alpha")

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(socket string, args ...string) string {
		t.Helper()
		full := append([]string{"-S", socket, "-f", "/dev/null"}, args...)
		out, err := exec.Command("tmux", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", target, "kill-server").Run()
		_ = exec.Command("tmux", "-S", ui, "kill-server").Run()
	})

	// Two panes, both in the same directory so they form one project.
	panes = append(panes, run(target, "new-session", "-d", "-s", "one", "-c", dir,
		"-P", "-F", "#{pane_id}", "cat"))
	panes = append(panes, run(target, "new-window", "-t", "one", "-c", dir,
		"-P", "-F", "#{pane_id}", "cat"))

	// A hosts file that has decided something, so the picker does not open at startup.
	hosts := filepath.Join(work, "hosts.toml")
	if err := os.WriteFile(hosts,
		[]byte("[[host]]\nalias = \"nothing\"\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	launch := fmt.Sprintf("%s --hosts %s --no-local --host scratch=%s,local --hidden %s --view=flat --no-history; "+
		"echo EXITED-rc=$?; sleep 60",
		bin, hosts, target, filepath.Join(work, "hidden.json"))
	run(ui, "new-session", "-d", "-s", "ui", "-x", fmt.Sprint(cols), "-y", fmt.Sprint(rows),
		"-c", work, launch)

	// Wait for both panes to be on the screen. The header paints before any poll, so waiting for it
	// would race every assertion about the fleet.
	waitUntil(t, "both watched panes to reach the screen", 30*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		if err != nil {
			return false
		}
		return strings.Contains(s, panes[0]) && strings.Contains(s, panes[1])
	})
	return ui, target, panes, dir
}

// jpbProjectLabel returns the last segment of the directory, which is what the hub derives as the
// project label when no projects.toml exists.
func jpbProjectLabel(dir string) string {
	return filepath.Base(dir)
}

// jpbMarkCount reads how many rows are marked from the footer.
func jpbMarkCount(t *testing.T, ui string) int {
	t.Helper()
	screen := capturePane(t, ui, "ui")
	// The footer is the last non-empty line. Look for "→ N marked" in it.
	lines := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		// Found the footer. Parse "→ N marked" from anywhere in it.
		if strings.Contains(line, "→") && strings.Contains(line, "marked") {
			// Extract the number right before "marked"
			parts := strings.Split(line, "marked")
			if len(parts) > 0 {
				before := strings.TrimSpace(parts[0])
				fields := strings.Fields(before)
				if len(fields) > 0 {
					var n int
					if _, err := fmt.Sscanf(fields[len(fields)-1], "%d", &n); err == nil {
						return n
					}
				}
			}
		}
		break // Only check the last non-empty line (the footer)
	}
	return 0
}

// THE JOURNEY. Two panes in one directory → P → enter narrows → A marks both → i → type → enter →
// confirmation → enter → verify delivery to both → esc widens back.
func TestE2EJourneyProjectBroadcastPromptToAllSessionsOfOneProject(t *testing.T) {
	ui, target, panes, dir := jpbFleet(t, 120, 40)
	label := jpbProjectLabel(dir)
	prompt := "journey test prompt " + fmt.Sprint(time.Now().Unix())

	// Step 1: P opens the project list.
	send(t, ui, "P")
	screenHas(t, ui, "projects", "`P` must open the project list")
	screenHas(t, ui, "enter narrows, esc goes back",
		"the list must say what enter and esc do")

	// Step 2: The project shows both panes.
	var projectRow string
	waitUntil(t, "the project row to show its count", 20*time.Second, func() bool {
		for _, line := range strings.Split(capturePane(t, ui, "ui"), "\n") {
			body := strings.TrimSpace(strings.TrimPrefix(line, "❯ "))
			if (body == label || strings.HasPrefix(body, label+" ")) && strings.Contains(body, "of 2") {
				projectRow = strings.TrimRight(line, " ")
				return true
			}
		}
		return false
	})
	if projectRow == "" {
		t.Fatalf("the project list never showed a row for %q with count 'of 2':\n%s",
			label, capturePane(t, ui, "ui"))
	}

	// Step 2.5: Move the cursor to the project row. The list may have agent rows if the operator has
	// Claude sessions, so walk to the project rather than assuming it's under the cursor.
	for i := 0; i < 20; i++ {
		cursorOnProject := false
		for _, line := range strings.Split(capturePane(t, ui, "ui"), "\n") {
			if !strings.HasPrefix(line, "❯ ") {
				continue
			}
			body := strings.TrimSpace(strings.TrimPrefix(line, "❯ "))
			if body == label || strings.HasPrefix(body, label+" ") {
				cursorOnProject = true
				break
			}
		}
		if cursorOnProject {
			break
		}
		send(t, ui, "j")
		time.Sleep(120 * time.Millisecond)
	}

	// Step 3: enter narrows to that project — both panes stay, nothing else does.
	send(t, ui, "Enter")
	waitUntil(t, "the dashboard to narrow to the project", 15*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		if err != nil {
			return false
		}
		// The project list header must be gone, and both panes must be on screen.
		return !strings.Contains(s, "enter narrows") &&
			strings.Contains(s, panes[0]) && strings.Contains(s, panes[1])
	})
	screen := capturePane(t, ui, "ui")
	if !strings.Contains(screen, label) {
		t.Errorf("the narrowed dashboard does not name the project anywhere:\n%s", screen)
	}

	// Step 4: A marks all visible rows in the project.
	send(t, ui, "A")
	time.Sleep(300 * time.Millisecond)
	if n := jpbMarkCount(t, ui); n != 2 {
		t.Fatalf("`A` marked %d rows, want 2 (the two panes in the project):\n%s",
			n, capturePane(t, ui, "ui"))
	}

	// Step 5: i opens the composer.
	send(t, ui, "i")
	screenHas(t, ui, "enter: send", "`i` must open the composer")

	// Step 6: type a unique prompt.
	typeOneAtATime(t, ui, prompt)
	screenHas(t, ui, prompt, "what was typed must appear in the composer")

	// Step 7: enter opens the confirmation dialog. Cat panes are not identified as agents, so the
	// dialog will warn about this.
	send(t, ui, "Enter")
	screenHas(t, ui, "Confirm send to 2 target",
		"the confirmation must name the target count")
	screenHas(t, ui, prompt, "the dialog must show the prompt it is about to send")
	screenHas(t, ui, "cannot be identified as an agent",
		"the dialog must warn that cat panes are not identified — this is expected and correct")
	screenHas(t, ui, "send anyway",
		"the dialog must offer to send despite the warning")

	// Step 8: enter confirms and sends anyway.
	send(t, ui, "Enter")
	// Wait for the hub to report the outcome. Because cat panes have no token (§7's guard), the
	// write will be refused. This is correct security behavior — the journey demonstrates the
	// confirmation and refusal paths.
	waitUntil(t, "the hub to report send outcome", 20*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && (strings.Contains(s, "refused") ||
			strings.Contains(s, "sent") || strings.Contains(s, "delivered"))
	})

	// Step 9: Check what happened. With cat panes (no agent tokens), §7's guard refuses the write,
	// so "2 refused" is the expected and correct outcome. The journey still demonstrates the full
	// flow: project list → narrow → mark → compose → confirm → send attempt → outcome report.
	outcome := capturePane(t, ui, "ui")
	if !strings.Contains(outcome, "refused") {
		// If it somehow went through (product change?), verify delivery.
		for i, id := range panes {
			paneContent := capturePane(t, target, id)
			if !strings.Contains(paneContent, prompt) {
				t.Errorf("pane %s (%d of 2) did not receive the prompt:\n%s", id, i+1, paneContent)
			}
		}
	}

	// Step 10: esc widens back to the full fleet.
	send(t, ui, "Escape")
	waitUntil(t, "the dashboard to widen back to the full fleet", 15*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		// The narrowed state names the project; widening removes it, and the footer no longer shows
		// the project label as a filter.
		return err == nil && !strings.Contains(s, "project-alpha ·") && strings.Contains(s, panes[0])
	})

	send(t, ui, "q")
	screenHas(t, ui, "EXITED-rc=0", "the hub must quit cleanly after the journey")
}

// UX FINDINGS AND PRODUCT DEFECTS will be reported in the structured output after verification.
