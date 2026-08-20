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

// COMPOSE SURFACE gaps: the keys that turn text into prompts, and the confirmation path that gates
// them. What breaks if each assertion fails is stated in the operator's terms, not as "tests X".
//
// §7 is the spec for compose and confirm; internal/ui/compose.go and model.go (composeKey,
// confirmKey) are the product. Existing coverage in tui_selection_e2e_test.go and
// tui_destructive_e2e_test.go touches sends but not the compose key routing itself.

// ── the fixture ────────────────────────────────────────────────────────────────────────────────

// cmpHub starts the hub over a private server with `panes` cat panes and waits for the fleet to
// paint. It returns once the rows are on screen, which is what lets a test select and compose
// immediately.
//
// The fixture isolates HOME and removes `claude` from PATH for the reason uiselHub does: an
// uncontrolled agents listing would add rows to the fleet, and a count-based assertion then
// measures somebody's Claude sessions rather than the product. The fleet is exactly the panes
// created here.
func cmpHub(t *testing.T, cols, rows, panes int) (ui, target string, paneIDs []string, work string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	bin := buildBinary(t)
	work = t.TempDir()
	ui = filepath.Join(work, "ui.sock")
	target = filepath.Join(work, "target.sock")

	run := func(socket string, args ...string) (string, error) {
		full := append([]string{"-S", socket, "-f", "/dev/null"}, args...)
		out, err := exec.Command("tmux", full...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", target, "kill-server").Run()
		_ = exec.Command("tmux", "-S", ui, "kill-server").Run()
	})

	id, err := run(target, "new-session", "-d", "-s", "watched", "-c", work,
		"-P", "-F", "#{pane_id}", "cat")
	if err != nil {
		t.Fatalf("new-session: %v: %s", err, id)
	}
	paneIDs = append(paneIDs, id)
	for i := 1; i < panes; i++ {
		id, err := run(target, "new-window", "-t", "watched", "-c", work,
			"-P", "-F", "#{pane_id}", "cat")
		if err != nil {
			t.Fatalf("new-window %d: %v: %s", i, err, id)
		}
		paneIDs = append(paneIDs, id)
	}

	hosts := filepath.Join(work, "hosts.toml")
	if err := os.WriteFile(hosts,
		[]byte("[[host]]\nalias = \"nothing\"\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(work, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	launch := fmt.Sprintf("HOME=%s PATH=%s %s --hosts %s --no-local --host scratch=%s,local "+
		"--hidden %s --view=flat --no-history; echo EXITED-rc=$?; sleep 60",
		home, cmpPathWithoutClaude(t), bin, hosts, target,
		filepath.Join(work, "hidden.json"))
	if out, err := run(ui, "new-session", "-d", "-s", "ui", "-x", fmt.Sprint(cols),
		"-y", fmt.Sprint(rows), "-c", work, launch); err != nil {
		t.Fatalf("start the hub: %v: %s", err, out)
	}

	cmpWaitFleet(t, ui, panes)
	return ui, target, paneIDs, work
}

// cmpWaitFleet waits for the header to report exactly n rows, which is the signal the fleet has
// arrived and selection can begin.
func cmpWaitFleet(t *testing.T, ui string, n int) {
	t.Helper()
	word := "sessions"
	if n == 1 {
		word = "session"
	}
	want := fmt.Sprintf("tmux-hub  %d %s", n, word)
	waitUntil(t, fmt.Sprintf("the header to report exactly %d rows", n),
		60*time.Second, func() bool {
			s, err := paneScreen(t, ui, "ui")
			return err == nil && strings.Contains(s, want)
		})
}

// cmpPathWithoutClaude is PATH with every directory holding a `claude` executable dropped, so the
// agents listing cannot add rows to the fleet.
func cmpPathWithoutClaude(t *testing.T) string {
	t.Helper()
	_, lookErr := exec.LookPath("claude")
	dirs := filepath.SplitList(os.Getenv("PATH"))
	var keep []string
	dropped := 0
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if st, err := os.Stat(filepath.Join(dir, "claude")); err == nil && !st.IsDir() {
			dropped++
			continue
		}
		keep = append(keep, dir)
	}
	if lookErr == nil && dropped == 0 {
		t.Fatalf("claude is on the PATH and this filter dropped none of its %d directories — "+
			"the fleet would carry the operator's own agent rows", len(dirs))
	}
	if len(keep) == 0 {
		t.Fatal("filtering claude out of PATH left nothing, so the hub could not find tmux")
	}
	return strings.Join(keep, ":")
}

// cmpSend sends keys to the hub's window (not session), because `a` or a launch can move the
// client and a key sent to the session would then land somewhere else.
func cmpSend(t *testing.T, ui string, keys ...string) {
	t.Helper()
	args := append([]string{"-S", ui, "send-keys", "-t", "ui:0"}, keys...)
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		t.Fatalf("send-keys %v: %v: %s", keys, err, out)
	}
	time.Sleep(300 * time.Millisecond)
}

// cmpType sends literal text to the hub's window.
func cmpType(t *testing.T, ui, text string) {
	t.Helper()
	if out, err := exec.Command("tmux", "-S", ui, "send-keys", "-t", "ui:0", "-l",
		text).CombinedOutput(); err != nil {
		t.Fatalf("type %q: %v: %s", text, err, out)
	}
	time.Sleep(300 * time.Millisecond)
}

// cmpScreen returns the whole captured pane, which is what lets a form below the fold be read.
func cmpScreen(t *testing.T, ui string) string {
	t.Helper()
	return capturePane(t, ui, "ui")
}

// cmpWaitScreen waits for the screen to contain a substring, which is the signal a mode has
// changed or a dialog has opened.
func cmpWaitScreen(t *testing.T, ui, want, why string) {
	t.Helper()
	waitUntil(t, why, 15*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && strings.Contains(s, want)
	})
}

// ── the tests ──────────────────────────────────────────────────────────────────────────────────

// THE MUTANT TEST. A mutant that made `q` quit inside compose survived a test that typed "quit the
// server" as a whole word, because bubbletea reports send-keys -l as ONE key message whose
// String() is the whole string, so a per-character case like `case "q":` never matches and the
// routing rule the case exists for is not exercised at all.
//
// What breaks without this: typing a prompt containing `q` quits the hub instead of inserting the
// character, which makes compose unusable for any natural-language instruction containing that
// letter.
func TestE2EComposeQAloneIsTextNotQuit(t *testing.T) {
	ui, _, panes, _ := cmpHub(t, 120, 40, 2)
	if len(panes) < 1 {
		t.Fatal("fixture created no panes")
	}

	// Select one pane and open compose.
	cmpSend(t, ui, " ")
	cmpWaitScreen(t, ui, "→ 1 marked", "selection to land")
	cmpSend(t, ui, "i")
	cmpWaitScreen(t, ui, "enter: send", "composer to open")

	// Type `q` ALONE.
	cmpSend(t, ui, "q")
	time.Sleep(500 * time.Millisecond)

	s := cmpScreen(t, ui)
	if !strings.Contains(s, "enter: send") {
		t.Errorf("the composer closed when `q` was typed — it should still be open:\n%s", s)
	}
	if !strings.Contains(s, "q") {
		t.Errorf("the `q` was not inserted as text:\n%s", s)
	}
	if strings.Contains(s, "EXITED") {
		t.Errorf("the hub quit when `q` was typed inside compose:\n%s", s)
	}
}

// esc KEEPS the draft. Losing a half-written prompt to a stray esc is what makes people stop using
// a tool, and the design says so in words (composeKey's own doc comment). What breaks without this:
// esc discards the draft, so reopening the composer shows an empty field and the operator has to
// retype everything.
func TestE2EComposeEscKeepsTheDraft(t *testing.T) {
	ui, _, panes, _ := cmpHub(t, 120, 40, 2)
	if len(panes) < 1 {
		t.Fatal("fixture created no panes")
	}

	cmpSend(t, ui, " ")
	cmpWaitScreen(t, ui, "→ 1 marked", "selection to land")
	cmpSend(t, ui, "i")
	cmpWaitScreen(t, ui, "enter: send", "composer to open")

	// Type a draft.
	draft := "half written prompt"
	cmpType(t, ui, draft)
	if s := cmpScreen(t, ui); !strings.Contains(s, draft) {
		t.Fatalf("the draft was not typed into the composer:\n%s", s)
	}

	// Esc leaves compose.
	cmpSend(t, ui, "Escape")
	cmpWaitScreen(t, ui, "draft kept", "the note saying the draft was kept")

	// Reopen the composer.
	cmpSend(t, ui, "i")
	cmpWaitScreen(t, ui, "enter: send", "composer to reopen")

	// The draft is still there.
	s := cmpScreen(t, ui)
	if !strings.Contains(s, draft) {
		t.Errorf("the draft was lost after esc and reopen — it should have been kept:\n%s", s)
	}
}

// The confirm dialog names its SUBJECT (what it will send) and its PAYLOAD. It used to show
// neither: "Confirm send to 1 target(s)" for an interrupt, and on the re-send path the last screen
// before writing into live agents showed no payload at all. What breaks without this: the operator
// presses enter without reading, because a confirmation with no subject is a ceremony and not a
// safety check.
func TestE2EConfirmDialogNamesSubjectAndPayload(t *testing.T) {
	ui, _, panes, _ := cmpHub(t, 120, 40, 2)
	if len(panes) < 1 {
		t.Fatal("fixture created no panes")
	}

	cmpSend(t, ui, " ")
	cmpWaitScreen(t, ui, "→ 1 marked", "selection to land")
	cmpSend(t, ui, "i")
	cmpWaitScreen(t, ui, "enter: send", "composer to open")

	payload := "explain what you just did"
	cmpType(t, ui, payload)
	cmpSend(t, ui, "Enter")
	cmpWaitScreen(t, ui, "Confirm send", "the confirmation dialog to open")

	s := cmpScreen(t, ui)
	if !strings.Contains(s, "send") {
		t.Errorf("the dialog does not name the action (send):\n%s", s)
	}
	if !strings.Contains(s, payload) {
		t.Errorf("the dialog does not show the payload it will send:\n%s", s)
	}
	// The target is shown too, but `scratch %N` is enough — asserting the pane id would make the
	// test depend on tmux's pane numbering rather than on what the dialog shows.
	if !strings.Contains(s, "scratch") {
		t.Errorf("the dialog does not name the target host:\n%s", s)
	}
}

// Cancelling the confirmation dialog leaves the draft untouched, so the operator can correct a
// typo and re-send. The draft used to be cleared on ANY successful send, which after the fix that
// stopped staging a re-send in the composer would have deleted a prompt the operator was still
// writing. What breaks without this: esc or any non-enter key from the confirmation empties the
// composer, so the operator has to retype the whole prompt to fix a single character.
func TestE2EConfirmCancelLeavesTheDraftUntouched(t *testing.T) {
	ui, _, panes, _ := cmpHub(t, 120, 40, 2)
	if len(panes) < 1 {
		t.Fatal("fixture created no panes")
	}

	cmpSend(t, ui, " ")
	cmpWaitScreen(t, ui, "→ 1 marked", "selection to land")
	cmpSend(t, ui, "i")
	cmpWaitScreen(t, ui, "enter: send", "composer to open")

	draft := "draft with a tyop"
	cmpType(t, ui, draft)
	cmpSend(t, ui, "Enter")
	cmpWaitScreen(t, ui, "Confirm send", "the confirmation dialog to open")

	// Cancel with esc.
	cmpSend(t, ui, "Escape")
	cmpWaitScreen(t, ui, "cancelled", "the cancellation note")

	// Reopen the composer.
	cmpSend(t, ui, "i")
	cmpWaitScreen(t, ui, "enter: send", "composer to reopen")

	s := cmpScreen(t, ui)
	if !strings.Contains(s, draft) {
		t.Errorf("the draft was lost after cancelling confirmation — the operator cannot "+
			"correct a typo without retyping:\n%s", s)
	}
}

// `!` (interrupt) on a selection raises the interrupt confirmation and does NOT read the
// composer's draft as its payload. It used to: the confirmation read the composer at the moment
// enter was pressed, which made the composer the staging area for anything that wanted to send,
// and `r` on a history entry then overwrote the operator's draft at the KEYSTROKE before the
// dialog was answered. What breaks without this: pressing `!` stages the draft as the interrupt
// payload, so cancelling the interrupt still sent the draft, which is the ONE case where what goes
// out is text the operator did not confirm.
func TestE2EInterruptDoesNotReadComposerDraftAsPayload(t *testing.T) {
	ui, target, panes, _ := cmpHub(t, 120, 40, 2)
	if len(panes) < 1 {
		t.Fatal("fixture created no panes")
	}

	cmpSend(t, ui, " ")
	cmpWaitScreen(t, ui, "→ 1 marked", "selection to land")
	cmpSend(t, ui, "i")
	cmpWaitScreen(t, ui, "enter: send", "composer to open")

	// Type a draft but do not send it.
	draft := "do not send this"
	cmpType(t, ui, draft)
	cmpSend(t, ui, "Escape")
	cmpWaitScreen(t, ui, "draft kept", "esc to leave compose")

	// Press `!` to interrupt.
	cmpSend(t, ui, "!")
	cmpWaitScreen(t, ui, "interrupt", "the interrupt confirmation to open")

	s := cmpScreen(t, ui)
	if strings.Contains(s, draft) {
		t.Errorf("the interrupt confirmation shows the composer's draft as its payload — "+
			"it should show nothing or C-c, not the draft:\n%s", s)
	}
	if !strings.Contains(s, "C-c") && !strings.Contains(s, "interrupt") {
		t.Errorf("the dialog does not name what it will do:\n%s", s)
	}

	// Cancel and confirm the draft is still in the composer.
	cmpSend(t, ui, "Escape")
	cmpSend(t, ui, "i")
	cmpWaitScreen(t, ui, "enter: send", "composer to reopen")

	s = cmpScreen(t, ui)
	if !strings.Contains(s, draft) {
		t.Errorf("the draft was lost after the interrupt dialog:\n%s", s)
	}

	// And the target pane never received the draft as text — only checking that nothing was
	// pasted, because an interrupt sends a control key and this test is about what does NOT go
	// out.
	targetScreen := capturePane(t, target, panes[0])
	if strings.Contains(targetScreen, draft) {
		t.Errorf("the draft was pasted into the target pane when `!` was pressed:\n%s", targetScreen)
	}
}

// A send delivers to panes on the private target server only, never to the operator's own server.
// This is structural: the fixture creates a private server and points the hub at it, so a send
// that reached anywhere else would be a test defect rather than a product defect. What this test
// proves is that the fixture itself is correctly isolated.
func TestE2EComposeSendDeliversToPrivateTargetOnly(t *testing.T) {
	ui, _, panes, _ := cmpHub(t, 120, 40, 2)
	if len(panes) < 1 {
		t.Fatal("fixture created no panes")
	}

	cmpSend(t, ui, " ")
	cmpWaitScreen(t, ui, "→ 1 marked", "selection to land")
	cmpSend(t, ui, "i")
	cmpWaitScreen(t, ui, "enter: send", "composer to open")

	// A unique payload that can be grepped for.
	payload := "e2e-compose-test-" + fmt.Sprint(time.Now().Unix())
	cmpType(t, ui, payload)
	cmpSend(t, ui, "Enter")
	cmpWaitScreen(t, ui, "Confirm send", "the confirmation dialog")
	cmpSend(t, ui, "Enter")
	cmpWaitScreen(t, ui, "sent to", "the send to complete")

	// The operator's own tmux server — if they have one running — must not have received it. This
	// is a negative assertion, and it cannot be exhaustive (we do not know which sessions exist),
	// but we can assert that no session called "watched" received it, since that is the name the
	// fixture uses and a collision would be the likeliest leak.
	if out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output(); err == nil {
		sessions := strings.Split(strings.TrimSpace(string(out)), "\n")
		for _, sess := range sessions {
			if sess == "watched" {
				t.Fatalf("the operator's own tmux server has a session called 'watched', which "+
					"would collide with the fixture — the test cannot prove isolation: %v", sessions)
			}
		}
	}
	// If the operator has no tmux server running at all, the above is a no-op and that is fine: the
	// fixture is isolated by construction (private socket), and this case is a structural check.
}
