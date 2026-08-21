package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/hub"
	tea "github.com/charmbracelet/bubbletea"
)

// The launch form could close and launch NOTHING, silently.
//
// `handleKey` reported only `closed`, which is true for both Esc and a successful Enter, so the
// model had to tell them apart by another means — and the means it chose was `f.err == ""`, a value
// that means "the LAST validation failed", used as though it meant "this one did". After an Enter
// that failed validation, a correction made with Backspace ALONE left the stale error in place, and
// the next Enter validated fine, returned closed, and was then discarded by the gate: the form shut,
// the mode went back to browse, no command ran and no note said so.
//
// Only Backspace reaches it. Any rune, Tab, ShiftTab or an arrow clears the error on the way, which
// is why the defect survived — the obvious way to retype a path is to type.
//
// The fix is not to clear the flag. `handleKey` now SAYS which of the three things happened, so the
// caller cannot infer it from leftover state and a fourth caller cannot get the inference wrong.

func launchFormAt(t *testing.T, dir string) launchForm {
	t.Helper()
	f := newLaunchForm([]hub.Host{{Label: "local", Status: hub.Up, LocalProc: true}}, "local", "", "")
	f.focused = 1 // the directory field
	for _, r := range dir {
		f, _, _ = f.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return f
}

func enter(f launchForm) (launchForm, formOutcome) {
	f, _, out := f.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	return f, out
}

// The form's own contract, and it is NOT the reproduction — measured. Against the pre-fix tree
// (accept path keeping the stale error, backspace not clearing it, the gate reading `f.err`) this
// test PASSES, because `handleKey` always reported "closed" for a valid enter and the defect lived
// in the caller's inference. The reproduction is the model-level test below, which fails pre-fix
// with `the form closed and returned no command`. Kept because the contract is what makes the
// caller's switch correct, and a test that cannot see the bug should say so rather than be trusted.
func TestTheFormReportsSubmittedAfterACorrectionMadeWithBackspace(t *testing.T) {
	// A relative path fails validation: the spec requires an absolute one.
	f := launchFormAt(t, "relative/pathx")
	f, out := enter(f)
	if out != formOpen {
		t.Fatalf("a relative path was accepted (outcome %v); this test needs a FAILED first enter", out)
	}
	if f.err == "" {
		t.Fatal("the first enter set no error, so there is no stale error to be tripped by")
	}

	// Correct it with Backspace ALONE — the one path that does not clear the error.
	for i := 0; i < len("relative/pathx"); i++ {
		f, _, _ = f.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	for _, r := range "/tmp" {
		// Typing clears the error, so type the new path AFTER the deletions to keep the stale
		// error alive — which is exactly the sequence an operator performs.
		f.dirInput.Insert(string(r))
	}

	f, out = enter(f)
	if out != formSubmitted {
		t.Errorf("outcome %v after a valid enter; the form closed without launching anything", out)
	}
}

// THE REPRODUCTION. Through the model, which is where the silence was: the returned command is what
// launches, and a nil one with the mode back at browse is the whole defect. Calibrated against a
// tree with all three halves of the fix reverted — it fails there with exactly the sentence below,
// and it needs all three, because either clear alone masks the gate.
func TestTheModelLaunchesAfterACorrectionMadeWithBackspace(t *testing.T) {
	m := base(t, 100, 24)
	m.launchForm = launchFormAt(t, "relative/pathx")
	m.mode = modeLaunch

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	after := got.(model)
	if after.mode != modeLaunch {
		t.Fatalf("the form closed on an invalid path; mode = %v", after.mode)
	}
	f := after.launchForm
	for i := 0; i < len("relative/pathx"); i++ {
		f, _, _ = f.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	for _, r := range "/tmp" {
		f.dirInput.Insert(string(r))
	}
	after.launchForm = f

	got2, cmd := after.Update(tea.KeyMsg{Type: tea.KeyEnter})
	final := got2.(model)
	if final.mode != modeBrowse {
		t.Errorf("a valid enter left the form open; mode = %v", final.mode)
	}
	if cmd == nil {
		t.Error("the form closed and returned no command — nothing was launched and nothing said so")
	}
}

// Esc must still cancel, and this is the assertion the old `f.err == \"\"` gate was standing in for.
// With the outcome named, cancelling is a different answer rather than the absence of an error.
func TestEscCancelsWithoutLaunching(t *testing.T) {
	f := launchFormAt(t, "/tmp")
	_, _, out := f.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if out != formCancelled {
		t.Errorf("esc reported %v, want cancelled", out)
	}

	m := base(t, 100, 24)
	m.launchForm = launchFormAt(t, "/tmp")
	m.mode = modeLaunch
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if got.(model).mode != modeBrowse {
		t.Error("esc did not close the form")
	}
	if cmd != nil {
		t.Error("esc launched something")
	}
}

// The plain path, so the fix cannot pass by refusing everything.
func TestAValidEnterLaunchesFirstTime(t *testing.T) {
	f := launchFormAt(t, "/tmp")
	_, out := enter(f)
	if out != formSubmitted {
		t.Errorf("a valid first enter reported %v, want submitted", out)
	}
}

// The rule the typing path already states, applied to the key that was missing it: a message naming
// a path the field no longer shows is worse than none, because the reader cannot tell whether it
// still applies.
func TestBackspaceClearsAStaleErrorFromTheScreen(t *testing.T) {
	f := launchFormAt(t, "relative/pathx")
	f, _ = enter(f)
	if f.err == "" {
		t.Fatal("no error to clear")
	}
	f, _, _ = f.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if f.err != "" {
		t.Errorf("backspace left the error on screen: %q", f.err)
	}
}

// And the error still REACHES the screen when it is about what the field shows, or clearing it
// eagerly would have removed the message instead of the staleness.
func TestAnInvalidPathStillSaysWhy(t *testing.T) {
	m := base(t, 100, 24)
	m.launchForm = launchFormAt(t, "relative/pathx")
	m.mode = modeLaunch
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	after := got.(model)
	if after.launchForm.err == "" {
		t.Fatal("an invalid path produced no error")
	}
	if !strings.Contains(after.View(), "absolute") {
		t.Errorf("the reason is not on the screen:\n%s", after.View())
	}
}

// A launch into a new session must carry a NAME, or the operator cannot find what they created.
//
// `SessionName` was reserved for a later task, so the spec reached `tmux new-session -d -s ""` —
// which succeeds on both fleet versions and produces a session whose name is the empty string. The
// row then draws as `nuc/%0 %0` and `tmux attach -t <name>` has nothing to take. The hub's own `a`
// was never affected, because it targets the session ID.
func TestALaunchIntoANewSessionCarriesAName(t *testing.T) {
	// A real directory the TEST owns. `launch.Spec.Validate` stats the cwd, so a literal path from
	// somebody's disk makes a green run a property of THAT MACHINE — this one passed here and failed
	// everywhere else, which is recorded in this repo's journal and was fixed in the published mirror
	// alone until now. The last segment is what the assertion below is about, so it is NAMED rather
	// than left to `t.TempDir()`'s counter.
	dir := filepath.Join(t.TempDir(), "st")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("the fixture could not make the directory the form is given: %v", err)
	}
	f := launchFormAt(t, dir)
	f.destIndex = 1 // a new session
	f, out := enter(f)
	if out != formSubmitted {
		t.Fatalf("outcome %v; the form refused a valid launch", out)
	}
	if f.spec.SessionName != "st" {
		t.Errorf("SessionName = %q, want %q — the cwd's last segment, which is what the project "+
			"list already groups by", f.spec.SessionName, "st")
	}
	if !f.spec.NewSession {
		t.Error("the destination did not reach the spec")
	}
}

// And the addressability rule reaches the spec too, not only the helper's own test.
func TestALaunchNameCannotBreakTmuxsTargetSyntax(t *testing.T) {
	f := launchFormAt(t, "/w/my.app")
	f.destIndex = 1
	f, _ = enter(f)
	if f.spec.SessionName != "my-app" {
		t.Errorf("SessionName = %q — a dot makes tmux read the tail as a pane, measured: "+
			"`has-session -t my.app` answers `can't find pane: app`", f.spec.SessionName)
	}
}
