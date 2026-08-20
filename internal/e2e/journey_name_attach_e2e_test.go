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

// THE NAMING JOURNEY: give a row a name and find that name everywhere it should be.
//
// This is the operator's own words made into a test: "создание новых сессий claude а потом
// подключение к ним — всё должно быть логично с точки зрения usability/UX для оператора".
// Previous tests cover surfaces in isolation; this one covers the FLOW a person actually
// performs: name something, see it everywhere, and still be able to find it by its original name.

// jnmHubEnv is a hub watching one tmux pane, with config isolated under the test's temp dir.
type jnmHubEnv struct {
	ui, target  string
	work        string
	projects    string // <cfg>/tmux-hub/projects.toml
	paneID      string
	sessionName string
	launch      string
	cols, rows  int
}

// jnmStart brings up the hub watching one pane in a session named `journey-naming-e2e`.
func jnmStart(t *testing.T, cols, rows int) *jnmHubEnv {
	t.Helper()
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	bin := buildBinary(t)
	work := t.TempDir()
	h := &jnmHubEnv{
		ui:          filepath.Join(work, "ui.sock"),
		target:      filepath.Join(work, "target.sock"),
		work:        work,
		sessionName: "journey-naming-e2e",
		cols:        cols,
		rows:        rows,
	}
	cfg := filepath.Join(work, "cfg")
	home := filepath.Join(work, "home")
	proj := filepath.Join(work, "proj")
	for _, d := range []string{cfg, home, proj} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.projects = filepath.Join(cfg, "tmux-hub", "projects.toml")

	run := func(socket string, args ...string) (string, error) {
		full := append([]string{"-S", socket, "-f", "/dev/null"}, args...)
		out, err := exec.Command("tmux", full...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", h.target, "kill-server").Run()
		_ = exec.Command("tmux", "-S", h.ui, "kill-server").Run()
	})

	id, err := run(h.target, "new-session", "-d", "-s", h.sessionName, "-c", proj,
		"-P", "-F", "#{pane_id}", "sleep", "300")
	if err != nil {
		t.Fatalf("new-session: %v: %s", err, id)
	}
	h.paneID = id

	hosts := filepath.Join(work, "hosts.toml")
	if err := os.WriteFile(hosts,
		[]byte("[[host]]\nalias = \"nothing\"\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.launch = "HOME=" + home + " XDG_CONFIG_HOME=" + cfg + " PATH=" + filepath.Dir(tmuxBin) +
		":$PATH " + bin + " --hosts " + hosts + " --no-local --host scratch=" + h.target +
		",local --hidden " + filepath.Join(work, "hidden.json") + " --view=flat --no-history; " +
		"echo EXITED-rc=$?; sleep 60"

	out, err := exec.Command("tmux", "-S", h.ui, "-f", "/dev/null", "new-session", "-d", "-s", "ui",
		// fmt.Sprint, NOT string(rune(n)): `string(rune(120+'0'))` is the character `¨`, which tmux
		// answers `width invalid` — the hub never started and every step of the journey timed out
		// against a screen that was a dead shell.
		"-x", fmt.Sprint(h.cols), "-y", fmt.Sprint(h.rows), "-c", h.work, h.launch).
		CombinedOutput()
	if err != nil {
		t.Fatalf("start the hub: %v: %s", err, out)
	}
	waitUntil(t, "the watched pane "+h.paneID+" to reach the screen", 40*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && strings.Contains(s, h.paneID)
	})
	return h
}

// jnmWalkTo walks to a row with `j` and waits for the screen to stop moving before each press.
func jnmWalkTo(t *testing.T, ui, want string) {
	t.Helper()
	const settle = 350 * time.Millisecond
	presses, reads := 0, 0
	for presses < 60 && reads < 240 {
		a := cursorRow(t, ui)
		time.Sleep(settle)
		b := cursorRow(t, ui)
		reads++
		if a != b {
			continue
		}
		if strings.Contains(b, want) {
			return
		}
		send(t, ui, "j")
		presses++
		time.Sleep(settle)
	}
	t.Fatalf("the cursor never settled on a row containing %q after %d presses and %d reads\n%s",
		want, presses, reads, capturePane(t, ui, "ui"))
}

// jnmWaitField waits for the naming overlay's field to show want.
func jnmWaitField(t *testing.T, ui, want, why string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	last := "(no overlay)"
	for time.Now().Before(deadline) {
		for _, l := range strings.Split(capturePane(t, ui, "ui"), "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), "name: ") {
				last = strings.TrimSpace(l)
				if last == want {
					return
				}
			}
		}
		time.Sleep(120 * time.Millisecond)
	}
	t.Fatalf("the field reads %q, want %q — %s\n%s", last, want, why, capturePane(t, ui, "ui"))
}

// jnmWaitOverlayClosed waits for the naming overlay to close.
func jnmWaitOverlayClosed(t *testing.T, ui, why string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		s, err := paneScreen(t, ui, "ui")
		if err == nil && !strings.Contains(s, "enter: save") {
			return
		}
		time.Sleep(120 * time.Millisecond)
	}
	t.Fatalf("the overlay is still open, so the commit was refused — %s\n%s", why,
		capturePane(t, ui, "ui"))
}

// THE NAMING JOURNEY: name a session, see it everywhere, and find it by both names.
//
// This is the operator's complete workflow: dashboard → name → see the name on every surface →
// search by the original name still works. Every step asserts what WORKED and what the screen
// SAID about it.
func TestJourneyNamingAndSearchingByBothNames(t *testing.T) {
	h := jnmStart(t, 120, 40)

	// PRECONDITION: the row reads by its derived name before we do anything.
	preScreen := capturePane(t, h.ui, "ui")
	if !strings.Contains(preScreen, h.sessionName) &&
		!strings.Contains(preScreen, strings.ToUpper(h.sessionName)) {
		t.Fatalf("the row does not show its derived name before naming, so the test's conclusion "+
			"about that name disappearing would be vacuous:\n%s", preScreen)
	}

	// ── STEP 1: walk to the row and press N ──
	jnmWalkTo(t, h.ui, h.sessionName)
	send(t, h.ui, "N")
	screenHas(t, h.ui, "name this session:",
		"`N` must open the naming overlay; if it does not, the key is broken or swallowed")
	screenHas(t, h.ui, "scratch · "+h.sessionName,
		"the overlay must name the row being named — a modal screen that does not say WHICH "+
			"row is being acted on forces the operator to remember it")
	screenHas(t, h.ui, "enter: save",
		"the overlay must name the key that commits; a modal screen with no visible way out "+
			"is a screen the operator has to guess")

	// STEP 1 PRODUCT CHECK: the field opens empty, not pre-filled with the derived name.
	// §21.12 rule 5: a pre-filled field makes freezing a tmux session name one keystroke away.
	for _, l := range strings.Split(capturePane(t, h.ui, "ui"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "name: ") {
			got := strings.TrimSpace(l)
			if got != "name: ▏" {
				t.Errorf("PRODUCT DEFECT: the field opened as %q, want empty with caret — a "+
					"field pre-filled with the derived name freezes a tmux session name into "+
					"projects.toml on an untouched enter", got)
			}
			break
		}
	}

	// ── STEP 2: type a name ──
	const name = "prod deploy"
	typeOneAtATime(t, h.ui, name)
	jnmWaitField(t, h.ui, "name: "+name+"▏",
		"what was typed must appear in the field exactly as typed, with the caret at the end")

	// STEP 2 UX CHECK: can the operator see both what they are typing AND what the row is
	// called today? The overlay is a FOOT, so the dashboard is still visible.
	step2Screen := capturePane(t, h.ui, "ui")
	if !strings.Contains(step2Screen, h.paneID) {
		t.Errorf("UX FINDING (medium): the overlay replaced the dashboard, so the operator "+
			"cannot see the row they are naming while typing:\n%s", step2Screen)
	}

	// ── STEP 3: commit with enter ──
	send(t, h.ui, "Enter")
	jnmWaitOverlayClosed(t, h.ui, "a legal name on an unnamed row must be accepted")
	waitUntil(t, "the name to appear on screen", 15*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && strings.Contains(s, "» "+name)
	})

	// STEP 3 PRODUCT CHECK: the name appears with the » marker on BOTH surfaces.
	// §21.12 rule 6: every surface calls displayName, so no screen can show a different name.
	step3Screen := capturePane(t, h.ui, "ui")
	if n := strings.Count(step3Screen, "» "+name); n != 2 {
		t.Errorf("PRODUCT DEFECT: %d ownership markers for the named row, want 2 (the list and "+
			"the tile) — one surface is calling it something else:\n%s", n, step3Screen)
	}

	// STEP 3 PRODUCT CHECK: the derived name is GONE.
	// §21.12 rule 6: one row reading two ways on one screen is forbidden.
	if strings.Contains(step3Screen, h.sessionName) ||
		strings.Contains(step3Screen, strings.ToUpper(h.sessionName)) {
		t.Errorf("PRODUCT DEFECT: the screen still shows the derived name %q beside the "+
			"operator's name, so one row reads two ways:\n%s", h.sessionName, step3Screen)
	}

	// STEP 3 UX CHECK: does the screen say the name is now applied? The » marker is the only
	// signal, and it is subtle. No note, no confirmation.
	if !strings.Contains(step3Screen, "» ") {
		t.Logf("UX NOTE: no confirmation that the name was saved; the » marker is the only "+
			"signal and it may not be obvious to a first-time user (screen after step 3):\n%s",
			step3Screen)
	}

	// ── STEP 4: the name reaches the tmux SESSION's status line ──
	// §21.16: an attached session shows `alias (original)`, not just the alias.
	waitUntil(t, "the hub to publish @hub_alias to the tmux server", 20*time.Second, func() bool {
		out, err := exec.Command("tmux", "-S", h.target, "show", "-v", "-t", h.sessionName,
			"@hub_alias").CombinedOutput()
		return err == nil && strings.TrimSpace(string(out)) == name
	})

	// STEP 4 PRODUCT CHECK: a CLIENT draws the name in its status line.
	// Not just checking the options — that would prove the writes, not the feature. tmux defaults
	// status-left-length to 10, so a hub that wrote the alias and format but forgot the room
	// draws `[prod dep` and stops.
	if out, err := exec.Command("tmux", "-S", h.ui, "new-window", "-d", "-n", "peek",
		"env -u TMUX tmux -S "+h.target+" attach -t "+h.sessionName).CombinedOutput(); err != nil {
		t.Fatalf("new-window for the attach client: %v: %s", err, out)
	}
	want := "[" + name + " (" + h.sessionName + ")]"
	waitUntil(t, "the attached client to draw the status line", 20*time.Second, func() bool {
		out, err := exec.Command("tmux", "-S", h.ui, "capture-pane", "-p", "-t", "peek").Output()
		return err == nil && strings.Contains(string(out), want)
	})
	clientScreen, _ := exec.Command("tmux", "-S", h.ui, "capture-pane", "-p", "-t", "peek").Output()
	if !strings.Contains(string(clientScreen), want) {
		t.Errorf("PRODUCT DEFECT: the attached session's status line does not show %q — the "+
			"name is on the dashboard and not where the operator is working:\n%s", want, clientScreen)
	}

	// STEP 4 UX CHECK: is the format clear? Does `alias (original)` read as what it means?
	// The parentheses suggest the original is secondary, which is correct.
	t.Logf("UX NOTE: status line format is %q — the parentheses make the original name visually "+
		"secondary, which matches the design intent", want)

	// ── STEP 5: the WINDOW is renamed too ──
	// §21.18: the hub renames door windows to `<host>/<name>`, and the operator's `C-b w` reads
	// like the hub.
	out, err := exec.Command("tmux", "-S", h.target, "list-windows", "-t", h.sessionName,
		"-F", "#{window_name}").Output()
	if err != nil {
		t.Fatalf("list-windows: %v: %s", err, out)
	}
	windowName := strings.TrimSpace(string(out))
	// The session was created with no -n, so tmux auto-renamed it to the command. The hub does
	// NOT rename windows it did not make, so this assertion is about the status line only.
	t.Logf("window name is %q (the hub does not rename windows it did not create)", windowName)

	// ── STEP 6: N again on the same row opens with the CURRENT name ──
	// §21.12 rule 5: the field opens pre-filled with the operator's own name, never with a
	// derived one, so an operator can see what they called it and correct it.
	//
	// Walked to by the NAME the operator gave, not by the tmux session: the row leads with its
	// display name (§21.12 rule 6), so after STEP 4 the session name is not on the row at all.
	// That is the feature working, and a walk keyed on the session name proves it by failing.
	jnmWalkTo(t, h.ui, name)
	send(t, h.ui, "N")
	screenHas(t, h.ui, "name this session:", "`N` on a named row must reopen the overlay")
	jnmWaitField(t, h.ui, "name: "+name+"▏",
		"the field must open with the CURRENT name, so the operator can see what they called it")

	// STEP 6 UX CHECK: does the overlay say this is the operator's own name?
	// The `now:` line should say "(yours)".
	step6Screen := capturePane(t, h.ui, "ui")
	if !strings.Contains(step6Screen, "(yours)") {
		t.Errorf("UX FINDING (low): the overlay does not say the name is the operator's own; "+
			"without that, they cannot tell whether they are editing their own name or overwriting "+
			"a derived one:\n%s", step6Screen)
	}

	// ── STEP 7: esc cancels losslessly ──
	send(t, h.ui, "Escape")
	time.Sleep(500 * time.Millisecond)
	step7Screen := capturePane(t, h.ui, "ui")
	if strings.Contains(step7Screen, "enter: save") {
		t.Errorf("PRODUCT DEFECT: the overlay is still open after esc — esc must cancel:\n%s",
			step7Screen)
	}
	if !strings.Contains(step7Screen, "» "+name) {
		t.Errorf("PRODUCT DEFECT: the name is gone after esc cancelled the overlay — esc must "+
			"be lossless:\n%s", step7Screen)
	}

	// ── STEP 8: search by the ORIGINAL name still finds it ──
	// §21.17: `/` matches "every name a row has", not just the one on screen. This is the
	// usability question the brief asks for: after naming, can the operator still find the
	// session by its original name?
	send(t, h.ui, "/")
	screenHas(t, h.ui, "search:",
		"`/` must open the search field; if it does not, the key is broken")

	// Type the ORIGINAL name, not the alias.
	typeOneAtATime(t, h.ui, h.sessionName)
	waitUntil(t, "the list to narrow to the matching row", 15*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && strings.Contains(s, "» "+name)
	})
	step8Screen := capturePane(t, h.ui, "ui")
	if !strings.Contains(step8Screen, "» "+name) {
		t.Errorf("PRODUCT DEFECT: searching for the original name %q did not find the row — "+
			"after naming, the operator cannot find the session by the name they used to know "+
			"it by:\n%s", h.sessionName, step8Screen)
	}
	// THE OTHER POLE, and it is what makes the assertion above mean anything: a fleet of ONE row
	// cannot be observed to narrow, so "the row is still here" would also be true of a search that
	// filtered nothing at all. Searching for a word that matches NOTHING must empty the list — and
	// then the screen has to say so rather than look like a fleet that went away.
	typeOneAtATime(t, h.ui, "zzz-matches-nothing")
	waitUntil(t, "the list to empty on a keyword that matches nothing", 15*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && !strings.Contains(s, "» "+name)
	})
	empty := capturePane(t, h.ui, "ui")
	if !strings.Contains(empty, "nothing matches") {
		t.Errorf("an emptied list does not say WHY it is empty — an empty screen with no sentence is "+
			"indistinguishable from a fleet that went away:\n%s", empty)
	}
	if !strings.Contains(empty, "esc") {
		t.Errorf("the emptied list does not name the way back:\n%s", empty)
	}
	// Back to the keyword that matches, so the steps below run against a list with the row in it.
	for i := 0; i < len("zzz-matches-nothing"); i++ {
		send(t, h.ui, "BSpace")
	}
	waitUntil(t, "the row to come back", 15*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && strings.Contains(s, "» "+name)
	})

	// STEP 8 UX CHECK: while the FIELD still has focus the footer belongs to it, so the form to assert
	// is the field's own — the keyword UNQUOTED beside the caret, plus what it has done. The quoted
	// form (`"keyword" · N of M`) is what the footer shows AFTER enter, and asserting that here was
	// asserting a screen the operator has not reached yet.
	live := capturePane(t, h.ui, "ui")
	if !strings.Contains(live, "search: "+h.sessionName) {
		t.Errorf("the field does not show the keyword being typed:\n%s", live)
	}
	if !jnmHasCount(live) {
		t.Errorf("UX FINDING: the field does not say what the keyword has DONE — the list narrows "+
			"live, so the operator should not have to commit the word to learn whether it found "+
			"anything:\n%s", live)
	}
	if !strings.Contains(step8Screen, "1 of") && !strings.Contains(step8Screen, "of 1") {
		t.Logf("UX NOTE: the footer does not show the narrowed count (1 of N); without that, "+
			"the operator cannot tell how many rows matched:\n%s", step8Screen)
	}

	// STEP 8 UX CHECK: is it clear that BOTH names work? The screen gives no hint.
	// The operator typed the original name and found the row, but nothing on screen explains
	// that the alias is also searchable. This is only discoverable by trying it.
	t.Logf("UX FINDING (medium): nothing on screen hints that search matches BOTH the alias " +
		"and the original name — the operator discovers this only by trying it, and the " +
		"narrowing overlay has no help text about what fields are matched")

	// ── STEP 9: esc widens ──
	send(t, h.ui, "Escape")
	time.Sleep(500 * time.Millisecond)
	step9Screen := capturePane(t, h.ui, "ui")
	if strings.Contains(step9Screen, "search:") {
		t.Errorf("PRODUCT DEFECT: the search field is still open after esc:\n%s", step9Screen)
	}
	// The list should be WIDENED: the row that was off-screen before is back.
	if !strings.Contains(step9Screen, "sleep") {
		t.Errorf("PRODUCT DEFECT: the list is still narrowed after esc closed the search "+
			"field — esc must widen:\n%s", step9Screen)
	}

	// FINAL CHECK: the name is in projects.toml and will survive a restart.
	body, err := os.ReadFile(h.projects)
	if err != nil {
		t.Fatalf("read projects.toml: %v", err)
	}
	for _, want := range []string{
		"host = \"scratch\"",
		"session = \"" + h.sessionName + "\"",
		"name = \"" + name + "\"",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("PRODUCT DEFECT: projects.toml does not carry %s:\n%s", want, body)
		}
	}
}

// jnmHasCount reports whether a screen carries an `N of M` count, as a SHAPE: this fixture keeps the
// real HOME, so the denominator is the operator's own fleet and a literal would measure their machine.
func jnmHasCount(screen string) bool {
	for _, l := range strings.Split(screen, "\n") {
		if i := strings.Index(l, " of "); i > 0 {
			b, a := strings.TrimSpace(l[:i]), strings.TrimSpace(l[i+4:])
			if b != "" && a != "" && b[len(b)-1] >= '0' && b[len(b)-1] <= '9' && a[0] >= '0' && a[0] <= '9' {
				return true
			}
		}
	}
	return false
}
