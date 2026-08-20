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

// THE FIRST-RUN ONBOARDING JOURNEY: the operator has never run the hub before, hosts.toml does not
// exist, and every decision is yet to be made.
//
// This is the flow a person actually performs on their first launch, and the one no test sequence
// has driven as a WHOLE: existing cases press `p` on a dashboard that already has a fleet, or they
// open the picker and test one key in isolation. This journey walks from launch to a working
// dashboard to a second start, asserting at every step that:
//   1. the state the step should produce really exists, and
//   2. the screen SAYS what happened and what can be done next.
//
// The second question is not decoration. A journey that works and does not explain itself is a
// defect on this project, and the report has a separate place for it.

// jfrHub starts the hub for the first-run journey: NO hosts.toml (first run), an isolated HOME
// with a seeded ssh config, and --probe-timeout 1ms so candidates timeout deterministically.
//
// It is built from pickerHub with two changes: (1) hostsTOML is always "" so the picker opens on
// its own, and (2) it returns the paths a second-start verification needs.
func jfrHub(t *testing.T, cols, rows int, sshConfig string) (ui, target, work, home, hostsPath string, watchedPane string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	bin := buildBinary(t)
	work = t.TempDir()
	ui = filepath.Join(work, "ui.sock")
	target = filepath.Join(work, "target.sock")
	home = filepath.Join(work, "home")
	hostsPath = filepath.Join(work, "hosts.toml")

	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	// 0600, because ssh refuses a group-readable config.
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(sshConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(work, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// A tmux the hub can still run once HOME has moved (fact 2 from tui_picker_e2e_test.go).
	binDir := filepath.Join(work, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(jfrTmuxForIsolatedHome(t, home), filepath.Join(binDir, "tmux")); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", target, "kill-server").Run()
		_ = exec.Command("tmux", "-S", ui, "kill-server").Run()
	})

	// One pane on the watched server, so the fleet is not empty and the first frame has something
	// to prove the poll works.
	out, err := exec.Command("tmux", "-S", target, "-f", "/dev/null", "new-session", "-d",
		"-s", "watched", "-c", work, "-P", "-F", "#{pane_id}", "cat").CombinedOutput()
	if err != nil {
		t.Fatalf("the watched server: %v: %s", err, out)
	}
	watchedPane = strings.TrimSpace(string(out))

	launch := fmt.Sprintf("PATH=%s:$PATH HOME=%s XDG_RUNTIME_DIR=%s %s "+
		"--hosts %s --no-local --host scratch=%s,local --hidden %s --view=flat --no-history --probe-timeout 1ms; "+
		"echo EXITED-rc=$?; sleep 90",
		binDir, home, runDir, bin, hostsPath, target,
		filepath.Join(work, "hidden.json"))
	if out, err := exec.Command("tmux", "-S", ui, "-f", "/dev/null", "new-session", "-d", "-s", "ui",
		"-x", fmt.Sprint(cols), "-y", fmt.Sprint(rows), "-c", work, launch).CombinedOutput(); err != nil {
		t.Fatalf("start the hub: %v: %s", err, out)
	}

	// Wait for the watched PANE, not for the header. §16 paints a usable screen before any poll
	// completes, so a harness that waits for the header reads the pre-poll frame.
	waitUntil(t, "the watched pane "+watchedPane+" to reach the screen", 30*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && strings.Contains(s, watchedPane)
	})
	return ui, target, work, home, hostsPath, watchedPane
}

// jfrTmuxForIsolatedHome answers with a tmux binary that still works once HOME has been redirected.
// Copied from pickerTmuxForIsolatedHome.
func jfrTmuxForIsolatedHome(t *testing.T, home string) string {
	t.Helper()
	var tried []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		tried = append(tried, p)
	}
	if p, err := exec.LookPath("tmux"); err == nil {
		add(p)
		if real, err := filepath.EvalSymlinks(p); err == nil {
			add(real)
		}
	}
	add("/usr/bin/tmux")
	add("/usr/local/bin/tmux")
	add("/bin/tmux")

	var why []string
	for _, p := range tried {
		cmd := exec.Command(p, "-V")
		cmd.Env = jfrEnvWithHome(home)
		out, err := cmd.CombinedOutput()
		if err == nil && strings.HasPrefix(string(out), "tmux ") {
			return p
		}
		why = append(why, fmt.Sprintf("%s: %v %s", p, err, strings.TrimSpace(string(out))))
	}
	t.Skipf("no tmux binary answers `tmux -V` with HOME redirected to a temp dir, so the first-run "+
		"journey's own fleet could not be polled and no assertion about it would mean anything. Tried:\n\t%s",
		strings.Join(why, "\n\t"))
	return ""
}

// jfrEnvWithHome replaces HOME in the environment rather than shadowing it.
func jfrEnvWithHome(home string) []string {
	out := []string{"HOME=" + home}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HOME=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// jfrPickerBodyLines is the picker overlay's own lines, below the full-width rule.
func jfrPickerBodyLines(t *testing.T, ui string) []string {
	t.Helper()
	lines := strings.Split(capturePane(t, ui, "ui"), "\n")
	rule := -1
	for i, l := range lines {
		if trimmed := strings.TrimRight(l, " "); trimmed != "" && strings.Trim(trimmed, "─") == "" {
			rule = i
		}
	}
	if rule < 0 {
		return nil
	}
	return lines[rule+1:]
}

// jfrPickerSqueezed is the picker overlay joined into one line with runs of spaces collapsed, so
// wrapped reasons can be asserted in full.
func jfrPickerSqueezed(t *testing.T, ui string) string {
	t.Helper()
	joined := strings.Join(jfrPickerBodyLines(t, ui), " ")
	return strings.Join(strings.Fields(joined), " ")
}

// jfrPickerBodyHas waits for the picker overlay to say something, and prints the overlay when it
// does not.
func jfrPickerBodyHas(t *testing.T, ui, want, why string) {
	t.Helper()
	var last string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		last = jfrPickerSqueezed(t, ui)
		if strings.Contains(last, want) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the picker never said %q — %s\nwhat it said:\n%s\nthe whole screen:\n%s",
		want, why, last, capturePane(t, ui, "ui"))
}

// jfrPickerCursorLine is the picker overlay row the cursor is on (marker `›`).
func jfrPickerCursorLine(t *testing.T, ui string) string {
	t.Helper()
	for _, l := range jfrPickerBodyLines(t, ui) {
		if strings.HasPrefix(l, "›") {
			return strings.TrimRight(l, " ")
		}
	}
	return ""
}

// jfrPickerWalkTo moves the picker's cursor with `j` until it rests on the named candidate.
func jfrPickerWalkTo(t *testing.T, ui, alias string) {
	t.Helper()
	for i := 0; i < 30; i++ {
		if strings.Contains(jfrPickerCursorLine(t, ui), alias) {
			return
		}
		send(t, ui, "j")
		time.Sleep(120 * time.Millisecond)
	}
	t.Fatalf("the picker's cursor never reached %q in 30 presses:\n%s",
		alias, capturePane(t, ui, "ui"))
}

// TestE2EJourneyFirstRunOnboarding is the complete first-run journey: the hub opens the picker on
// its own when hosts.toml does not exist, the operator keeps one host, the dashboard appears with
// that host's fleet, and a second start does not ask again.
//
// Every step asserts TWO things: the state that should exist really does, and the screen says what
// happened and what to do next. The second is a usability requirement, not decoration.
func TestE2EJourneyFirstRunOnboarding(t *testing.T) {
	// One candidate that will timeout (using --probe-timeout 1ms), so there is something to keep.
	// The alias must be one the operator's own ssh config does not name, or the probe would reach
	// their machine.
	ui, target, _, _, hostsPath, watchedPane := jfrHub(t, 120, 40, "Host hub-e2e-first-run\n")

	// ── STEP 1: the picker opens on its own ───────────────────────────────────────────────────

	// The picker IS open, without any keystroke. The full-width rule is its signature.
	var pickerOpen bool
	for _, l := range strings.Split(capturePane(t, ui, "ui"), "\n") {
		trimmed := strings.TrimRight(l, " ")
		if trimmed != "" && strings.Trim(trimmed, "─") == "" {
			pickerOpen = true
			break
		}
	}
	if !pickerOpen {
		t.Errorf("the picker did not open on its own when hosts.toml was absent — the first run "+
			"shows a dashboard with no way to add a host:\n%s", capturePane(t, ui, "ui"))
	}

	// The dashboard is BEHIND the picker — the local fleet visible, proving §16's commitment that
	// the screen is usable before any network work.
	if !strings.Contains(capturePane(t, ui, "ui"), watchedPane) {
		t.Errorf("the local fleet is not on screen while the picker is open — §16 commits to "+
			"showing it before any network work starts:\n%s", capturePane(t, ui, "ui"))
	}

	// UX: The screen must say WHAT this overlay is. "Hosts —" is the heading.
	jfrPickerBodyHas(t, ui, "Hosts —",
		"the picker must identify itself; an overlay with no heading leaves the operator unsure "+
			"what screen they are on")

	// Wait for the probe to land. The picker opens on "nothing to show yet" and the probe replaces
	// it with the count line — asserting against the first frame would find no candidates yet.
	jfrPickerBodyHas(t, ui, "hub-e2e-first-run",
		"the probe must land and list the candidate from ~/.ssh/config; a screen still saying "+
			"`nothing to show yet` has asked nobody")

	// UX: The candidate row must say what STATE it is in. With --probe-timeout 1ms, every candidate
	// times out — and a timeout row carries `[ ]` and a reason mentioning the timeout.
	body := jfrPickerSqueezed(t, ui)
	if !strings.Contains(body, "[ ] hub-e2e-first-run") {
		t.Errorf("the candidate row does not show its tick box `[ ]`, so the operator cannot tell "+
			"which hosts are kept:\n%s", body)
	}
	if !strings.Contains(body, "no answer") && !strings.Contains(body, "1ms") {
		t.Errorf("the timed-out candidate does not mention the timeout, so the operator cannot tell "+
			"why it was not auto-kept:\n%s", body)
	}

	// UX: The picker must say what keys DO. The footer line explains the actions.
	pickerScreen := capturePane(t, ui, "ui")
	if !strings.Contains(pickerScreen, "space:") {
		t.Errorf("the picker does not document the `space` key:\n%s", pickerScreen)
	}
	if !strings.Contains(pickerScreen, "enter:") {
		t.Errorf("the picker does not document the `enter` key:\n%s", pickerScreen)
	}

	capture1 := capturePane(t, ui, "ui")

	// ── STEP 2: tick the host ─────────────────────────────────────────────────────────────────

	jfrPickerWalkTo(t, ui, "hub-e2e-first-run")
	send(t, ui, "space")

	// The tick lands: `[x]` appears.
	jfrPickerBodyHas(t, ui, "[x] hub-e2e-first-run",
		"`space` must tick the row under the cursor; a screen that stays `[ ]` has not responded")

	capture2 := capturePane(t, ui, "ui")

	// ── STEP 3: save with enter ────────────────────────────────────────────────────────────────

	send(t, ui, "Enter")

	// UX: The hub must SAY what it did. "N host kept in hosts.toml" is the confirmation.
	screenHas(t, ui, "1 host kept in hosts.toml",
		"`enter` must report what it committed; a save that says nothing is indistinguishable from "+
			"a swallowed key")

	// THE FILE. The write happens in a tea.Cmd, so wait for the bytes rather than reading once.
	var got string
	waitUntil(t, "hosts.toml to hold the ticked host", 20*time.Second, func() bool {
		b, err := os.ReadFile(hostsPath)
		if err != nil {
			return false
		}
		got = string(b)
		return strings.Contains(got, "hub-e2e-first-run")
	})
	if !strings.Contains(got, "alias = \"hub-e2e-first-run\"\nenabled = true\n") {
		t.Errorf("hosts.toml does not enable the host that was ticked:\n%s", got)
	}

	capture3 := capturePane(t, ui, "ui")

	// ── STEP 4: the dashboard appears ─────────────────────────────────────────────────────────

	// The picker CLOSES. The full-width rule is gone.
	var pickerStillOpen bool
	waitUntil(t, "the picker to close after saving", 15*time.Second, func() bool {
		pickerStillOpen = false
		for _, l := range strings.Split(capturePane(t, ui, "ui"), "\n") {
			trimmed := strings.TrimRight(l, " ")
			if trimmed != "" && strings.Trim(trimmed, "─") == "" {
				pickerStillOpen = true
				break
			}
		}
		return !pickerStillOpen
	})
	if pickerStillOpen {
		t.Errorf("the picker did not close after `enter`, so the operator is left on a screen they "+
			"have finished with:\n%s", capturePane(t, ui, "ui"))
	}

	// THE FLEET. The new host appears in the footer with a transport verdict (connecting/down).
	// The alias in the footer can only come from the host line, never from the save's note.
	waitUntil(t, "the new host to reach the dashboard's fleet line", 60*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && (strings.Contains(s, "hub-e2e-first-run connecting") ||
			strings.Contains(s, "hub-e2e-first-run down"))
	})

	capture4 := capturePane(t, ui, "ui")

	// ── STEP 5: persistence — the second start does not ask again ─────────────────────────────

	// Kill the first hub cleanly.
	send(t, ui, "q")
	screenHas(t, ui, "EXITED-rc=0", "the hub must quit cleanly with rc=0")

	// Start a second hub with the SAME paths: the same hosts.toml, the same HOME. The picker must
	// NOT open on its own this time, because the file now holds a decision.
	//
	// This is a SEPARATE launch, not a resume, so it proves persistence across restarts.
	bin := buildBinary(t)
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Dir(filepath.Dir(hostsPath)) // work/home, where hostsPath is work/hosts.toml
	if err := os.Symlink(jfrTmuxForIsolatedHome(t, home), filepath.Join(binDir, "tmux")); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(filepath.Dir(hostsPath), "run2")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}

	ui2 := filepath.Join(filepath.Dir(hostsPath), "ui2.sock")
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", ui2, "kill-server").Run()
	})

	launch := fmt.Sprintf("PATH=%s:$PATH HOME=%s XDG_RUNTIME_DIR=%s %s "+
		"--hosts %s --no-local --host scratch=%s,local --hidden %s --view=flat --no-history --probe-timeout 1ms; "+
		"echo EXITED-rc=$?; sleep 90",
		binDir, home, runDir, bin, hostsPath, target,
		filepath.Join(filepath.Dir(hostsPath), "hidden.json"))
	if out, err := exec.Command("tmux", "-S", ui2, "-f", "/dev/null", "new-session", "-d", "-s", "ui2",
		"-x", "120", "-y", "40", "-c", filepath.Dir(hostsPath), launch).CombinedOutput(); err != nil {
		t.Fatalf("start the second hub: %v: %s", err, out)
	}

	// Wait for the watched pane again.
	waitUntil(t, "the second hub's watched pane to reach the screen", 30*time.Second, func() bool {
		s, err := paneScreen(t, ui2, "ui2")
		return err == nil && strings.Contains(s, watchedPane)
	})

	// The picker must NOT be open. No full-width rule on the screen.
	var pickerOpenAgain bool
	for _, l := range strings.Split(capturePane(t, ui2, "ui2"), "\n") {
		trimmed := strings.TrimRight(l, " ")
		if trimmed != "" && strings.Trim(trimmed, "─") == "" {
			pickerOpenAgain = true
			break
		}
	}
	if pickerOpenAgain {
		t.Errorf("the picker opened on the second start even though hosts.toml holds a decision — "+
			"the first run's work does not persist:\n%s", capturePane(t, ui2, "ui2"))
	}

	// The dashboard is showing the fleet directly, with the kept host in it.
	secondStart := capturePane(t, ui2, "ui2")
	if !strings.Contains(secondStart, "hub-e2e-first-run") {
		t.Errorf("the second start does not show the kept host in the fleet, so the first run's "+
			"decision was lost:\n%s", secondStart)
	}

	capture5 := secondStart

	// Clean up the second hub.
	send(t, ui2, "q")
	screenHas(t, ui2, "EXITED-rc=0", "the second hub must quit cleanly")

	// ── UX FINDINGS: places where the journey WORKED but the screen did not explain itself ────

	_ = capture1 // Step 1: picker just opened
	_ = capture2 // Step 2: host ticked
	_ = capture3 // Step 3: after enter
	_ = capture4 // Step 4: dashboard with new host
	_ = capture5 // Step 5: second start

	// UX FINDING (capture2): After pressing space to tick a host, the row changes from `[ ]` to
	// `[x]`, which confirms the keystroke registered. However, the picker's own footer does not
	// show a count of ticked hosts ("→ N marked" appears only on the dashboard footer, which is
	// behind the overlay). For an operator who has ticked multiple hosts, there is no summary of
	// "how many am I about to save" visible on the screen where they will press enter. The tick
	// box change is the only feedback, and it's per-row rather than aggregate.
}
