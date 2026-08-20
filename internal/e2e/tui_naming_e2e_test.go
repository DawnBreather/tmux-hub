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

// THE NAMING OVERLAY through the real binary in a real terminal: `N` on a row, `N` on the project
// list, and the file the name lands in.
//
// What an in-process test structurally cannot reach here, and why each case below is worth its
// seconds:
//
//   - The PATH to projects.toml. `internal/ui` is handed `projectsPath` by a fixture; the binary
//     derives it from `project.DefaultPath()` — `configdir.Path("projects.toml")` — and nothing
//     asserts that the process a user runs writes where the documentation says. There is no
//     `--projects` flag beside `--hosts`, `--hidden` and `--history`, so the only lever a test (or
//     an operator) has is $XDG_CONFIG_HOME, and these cases use it. That is also what makes them
//     safe: the operator's own ~/.config/tmux-hub/projects.toml is never opened.
//   - A RESTART. Persistence is the whole reason the name goes to a file, and "the name survives"
//     is a claim about a second process reading what a first one wrote. A model cannot be restarted.
//   - The KEYS as bytes. `ctrl+u`, backspace and the space inside a name arrive as control bytes the
//     runtime decodes; a `tea.KeyMsg` handed to Update has already been decoded.
//   - The FIELD at a real width. `fieldTail` is unit-tested against a chosen `room`; here the room
//     comes from the terminal.
//
// Two environment facts, each measured while writing this file, each of which made the product look
// broken when it was not:
//
//  1. `tmux` in this environment is a mise SHIM, and a shim resolves its version through
//     $XDG_CONFIG_HOME. Overriding that variable — which is the only way to isolate projects.toml —
//     therefore takes tmux away from the hub: every host read `down` with
//     `list-panes rc=1: mise ERROR No version is set for shim: tmux`, which looks exactly like a
//     broken transport. So the launch pins the directory of the tmux the TEST resolved onto the
//     front of PATH, and the hub then runs the same binary this file does.
//  2. The hub's own HOME is isolated as well, so the fleet is exactly the panes these cases create.
//     That is not tidiness: the shared `walkTo` presses `j` every 80 ms and stops on the first frame
//     that shows the marker where it wants it, and against the operator's live Claude fleet the
//     repaint lags the model — measured, the marker settled on `%0` and the model was on an agent
//     row, so `N` opened on `20260803--store-online-…` and wrote
//     `id = "1ff133f7-c34a-4c60-91e5-b0048842cc66"` into projects.toml. Nothing about naming was
//     wrong; the walk had overshot. `namingWalkTo` below waits for the screen to STOP MOVING before
//     each press, which is the only honest way to know which row the next key will act on.

// namingHubEnv is one hub over one private server, with its config directory under this test's
// temp dir so `N` writes a projects.toml nobody else can see.
// namingSession is the tmux session namingStart creates. It is a constant because it is now a
// NEEDLE as well as a fixture: with one pane in it the hub draws no pane id on its row, so a test
// that wants to find that row on the screen looks for this name.
const namingSession = "derived-e2e"

type namingHubEnv struct {
	ui, target string // the socket the hub is DISPLAYED on, and the one it WATCHES
	work       string
	projects   string // <cfg>/tmux-hub/projects.toml — the path the BINARY chose, not one we passed
	proj       string // the directory the watched panes run in, i.e. the derived project
	paneIDs    []string
	launch     string // the whole shell command, kept so a restart is the same process again
	cols, rows int
}

// namingStart brings up the hub with `panes` watched panes in one session named `derived-e2e`.
//
// The session name is the DERIVED name every case below asserts against: `N` names a session, so
// the row's name before anyone types is this string, and after a name is applied it must be gone
// from the screen — §21.12 rule 6 forbids one row reading two ways on one screen.
// readsDerived answers whether the screen calls the row by its DERIVED name, in whichever case the
// current layout puts it: a session HEADER upper-cases a derived name, while a ROW carries it as the
// source spelled it. Four cases here asserted the upper-case form alone, which made them assertions
// about the LAYOUT rather than about the name — they broke the day a session holding a single row
// stopped taking a header, and one of them (the negative in the follows-the-session case) could no
// longer fail at all, because neither case appeared.
func readsDerived(s string) bool {
	return strings.Contains(s, "derived-e2e") || strings.Contains(s, "DERIVED-E2E")
}

func namingStart(t *testing.T, cols, rows, panes int) *namingHubEnv {
	t.Helper()
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	bin := buildBinary(t)
	work := t.TempDir()
	h := &namingHubEnv{
		ui:     filepath.Join(work, "ui.sock"),
		target: filepath.Join(work, "target.sock"),
		work:   work,
		proj:   filepath.Join(work, "namingproj"),
		cols:   cols,
		rows:   rows,
	}
	cfg := filepath.Join(work, "cfg")
	home := filepath.Join(work, "home")
	for _, d := range []string{cfg, home, h.proj} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The path the BINARY will derive, written down here so a case can read the file the product
	// chose rather than one it was handed. If configdir's rule ever changes, these cases fail on
	// the file being absent, which is the right way round.
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

	id, err := run(h.target, "new-session", "-d", "-s", namingSession, "-c", h.proj,
		"-P", "-F", "#{pane_id}", "sleep", "300")
	if err != nil {
		t.Fatalf("new-session: %v: %s", err, id)
	}
	h.paneIDs = append(h.paneIDs, id)
	for i := 1; i < panes; i++ {
		id, err := run(h.target, "new-window", "-t", namingSession, "-c", h.proj,
			"-P", "-F", "#{pane_id}", "sleep", "300")
		if err != nil {
			t.Fatalf("new-window: %v: %s", err, id)
		}
		h.paneIDs = append(h.paneIDs, id)
	}

	// A hosts file that has DECIDED something, so the picker does not open over the dashboard and
	// swallow every key these cases send.
	hosts := filepath.Join(work, "hosts.toml")
	if err := os.WriteFile(hosts,
		[]byte("[[host]]\nalias = \"nothing\"\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.launch = fmt.Sprintf(
		"HOME=%s XDG_CONFIG_HOME=%s PATH=%s:$PATH %s --hosts %s --no-local --host scratch=%s,local "+
			"--hidden %s --view=flat --no-history; echo EXITED-rc=$?; sleep 60",
		home, cfg, filepath.Dir(tmuxBin), bin, hosts, h.target,
		filepath.Join(work, "hidden.json"))
	h.up(t)
	return h
}

// up starts the hub's pane and waits for the FLEET, not the header: §16 paints a usable screen
// before any poll completes, so a harness that waits for the header reads the pre-poll frame.
func (h *namingHubEnv) up(t *testing.T) {
	t.Helper()
	out, err := exec.Command("tmux", "-S", h.ui, "-f", "/dev/null", "new-session", "-d", "-s", "ui",
		"-x", fmt.Sprint(h.cols), "-y", fmt.Sprint(h.rows), "-c", h.work, h.launch).CombinedOutput()
	if err != nil {
		t.Fatalf("start the hub: %v: %s", err, out)
	}
	// By the HEADER'S COUNT, which is the only signal invariant across everything these cases do to
	// the fleet. Three needles were tried and each is a property of something else:
	//
	//   - the pane id is not drawn for a session that puts ONE row up (rowPaneID), and then survives
	//     only in the tile — which a small terminal has no room for;
	//   - the derived name is GONE from the screen once a row is named (§21.12 rule 6), so `restart`
	//     would fail in every case that had named something, which is most of them;
	//   - `scratch/` is drawn only on a HEADERLESS row, so it is absent whenever the fixture asks for
	//     two panes and the session takes a header.
	//
	// The count is a positive assertion about the poll, and §16 paints `0 sessions` before any poll
	// completes, so it is exactly the "the fleet arrived" edge. This fixture isolates HOME, so no
	// agent rows can satisfy it on the operator's behalf.
	waitUntil(t, "the watched fleet to reach the screen", 40*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && strings.Contains(s, "session") &&
			!strings.Contains(s, "tmux-hub  0 session")
	})
}

// restart quits the hub and runs the same command again in a NEW pane.
//
// The pane is new on purpose. A respawn into the same pane leaves the previous hub's bytes on the
// screen, and an assertion that the name is there would then be satisfied by the frame the FIRST
// process painted — the persistence claim would test nothing. Killing the session guarantees the
// only source of anything on screen is the second process.
func (h *namingHubEnv) restart(t *testing.T) {
	t.Helper()
	send(t, h.ui, "q")
	screenHas(t, h.ui, "EXITED-rc=0",
		"the hub must quit cleanly before a restart; anything else here is a panic on the way out")
	if out, err := exec.Command("tmux", "-S", h.ui, "kill-session", "-t", "ui").
		CombinedOutput(); err != nil {
		t.Fatalf("kill the ui session: %v: %s", err, out)
	}
	h.up(t)
	if s := capturePane(t, h.ui, "ui"); strings.Contains(s, "EXITED-rc=0") {
		t.Fatalf("the restarted pane still holds the first hub's output, so nothing on it is "+
			"evidence about the second:\n%s", s)
	}
}

// namingWalkTo moves the cursor with `j` and refuses to press until the screen has STOPPED MOVING.
//
// The shared walkTo presses every 80 ms and returns on the first frame that matches. Against a
// fleet whose rows re-sort — and every fleet with agent rows does — that frame can be a repaint
// behind the model, so the walk overshoots and the next key acts on a row the operator never chose.
// Measured: with the operator's real Claude fleet, walkTo stopped with the marker on `%0` while `N`
// opened the overlay on an agent session and wrote its id into projects.toml. Two consecutive equal
// captures is the cheapest available proof that the frame being read is the model's own.
func namingWalkTo(t *testing.T, ui, want string) {
	t.Helper()
	const settle = 350 * time.Millisecond
	// Two budgets, because they run out for different reasons: PRESSES bound how far down the list
	// this is willing to walk, and READS bound how long it will wait for a fleet that will not sit
	// still. One counter for both would let a moving screen exhaust the walk without ever pressing.
	presses, reads := 0, 0
	for presses < 60 && reads < 240 {
		a := cursorRow(t, ui)
		time.Sleep(settle)
		b := cursorRow(t, ui)
		reads++
		if a != b {
			continue // still moving: read again rather than press into a frame that is stale
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

// namingFieldRow is the overlay's own input row, and whether there is one at all.
//
// `name: ` with the colon is unambiguous — the subject row reads `name this session:` — and reading
// the ROW rather than the whole screen is what makes an assertion about the field an assertion about
// the field. The second return is what keeps a poll honest: a screen with no overlay is an ANSWER
// (the overlay closed, or has not opened yet), not a broken harness, so a poller must be able to see
// it without dying.
func namingFieldRow(t *testing.T, ui string) (string, bool) {
	t.Helper()
	for _, l := range strings.Split(capturePane(t, ui, "ui"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "name: ") {
			return strings.TrimSpace(l), true
		}
	}
	return "", false
}

// namingField is the same read with a missing overlay treated as a failure, for a direct assertion.
func namingField(t *testing.T, ui string) string {
	t.Helper()
	row, ok := namingFieldRow(t, ui)
	if !ok {
		t.Fatalf("the naming overlay has no field row:\n%s", capturePane(t, ui, "ui"))
	}
	return row
}

// namingWaitField waits for the field row to hold want, so a case never races the repaint of a
// keystroke it just sent.
func namingWaitField(t *testing.T, ui, want, why string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	last := "(no overlay on screen)"
	for time.Now().Before(deadline) {
		if row, ok := namingFieldRow(t, ui); ok {
			last = row
			if row == want {
				return
			}
		}
		time.Sleep(120 * time.Millisecond)
	}
	t.Fatalf("the field reads %q, want %q — %s\n%s", last, want, why, capturePane(t, ui, "ui"))
}

// namingWaitOverlayClosed waits for the overlay to go, which is the only signal that a commit was
// ACCEPTED. The name being on screen is not that signal: the field itself shows the name, so a
// refusal — which keeps the overlay open with what was typed — satisfies any `Contains(name)` poll.
// Measured: the project case's first version passed its wait against a refused commit.
func namingWaitOverlayClosed(t *testing.T, ui, why string) {
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

// namingFile is projects.toml as the operator would read it, or "" when the hub has not written one.
func namingFile(t *testing.T, h *namingHubEnv) string {
	t.Helper()
	b, err := os.ReadFile(h.projects)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read %s: %v", h.projects, err)
	}
	return string(b)
}

// namingRequireDerivedName asserts the row reads by its DERIVED name right now.
//
// It is the precondition of every case whose conclusion is "the derived name is gone": without it,
// an assertion that the screen does not contain `derived-e2e` is satisfied by a screen with no rows
// at all, which is exactly what a broken fixture produces. Both cases are accepted because the band
// decides which one is on screen — uppercased in the group header at 100 and above, as typed on the
// inline row below it.
func namingRequireDerivedName(t *testing.T, h *namingHubEnv) {
	t.Helper()
	s := capturePane(t, h.ui, "ui")
	if !readsDerived(s) {
		t.Fatalf("the row does not read by its derived name before anything was named, so any "+
			"conclusion about that name disappearing would be vacuous:\n%s", s)
	}
}

// namingNameTheRow performs the whole gesture on the watched pane's row: walk, `N`, type, `enter`.
// It asserts only the steps it takes, so a case building on it can assert its own property.
func namingNameTheRow(t *testing.T, h *namingHubEnv, name string) {
	t.Helper()
	namingRequireDerivedName(t, h)
	namingWalkTo(t, h.ui, rowNeedleIn(namingSession, h.paneIDs[0], h.paneIDs))
	send(t, h.ui, "N")
	screenHas(t, h.ui, "name this session: scratch · derived-e2e",
		"`N` must open the overlay on the row under the cursor")
	typeOneAtATime(t, h.ui, name)
	namingWaitField(t, h.ui, "name: "+name+"▏", "what was typed must reach the field")
	send(t, h.ui, "Enter")
	namingWaitOverlayClosed(t, h.ui, "a legal name on an unnamed row must be accepted")
	waitUntil(t, "the name to be applied", 15*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && strings.Contains(s, "» "+name)
	})
}

// ── `N` on a row ──────────────────────────────────────────────────────────────────────────────────

// `N` opens the overlay on the row under the cursor, and it opens EMPTY.
//
// Empty is the load-bearing half (§21.12 rule 5): the field carries the operator's own name and
// never a derived one, so an untouched `enter` cannot freeze a tmux session name into the file. A
// pre-filled field would make that mistake one keystroke away and look like a convenience.
func TestE2EUINamingNOpensTheOverlayOnTheRowUnderTheCursor(t *testing.T) {
	h := namingStart(t, 120, 40, 1)
	namingWalkTo(t, h.ui, rowNeedleIn(namingSession, h.paneIDs[0], h.paneIDs))

	send(t, h.ui, "N")
	screenHas(t, h.ui, "name this session: scratch · derived-e2e",
		"`N` must name the row under the cursor, and say WHICH row — a session name is not "+
			"unique across the fleet, so the host is part of the subject")
	// `now:` is the operator's only way to compare what they are typing against what the row is
	// called today, and the provenance is what tells them the name is not already theirs.
	screenHas(t, h.ui, "now:  derived-e2e  (not yours — the tmux session name)",
		"the overlay must say what the row is called today and where that comes from")
	screenHas(t, h.ui, "enter: save  ·  ctrl+u then enter: remove the name  ·  esc: cancel",
		"every key the overlay answers must be on the overlay; a modal screen with no key row "+
			"is a screen an operator has to guess their way out of")

	if got := strings.TrimSpace(namingField(t, h.ui)); got != "name: ▏" {
		t.Errorf("the field opened as %q, want an empty field with the caret — a field that "+
			"opens holding the DERIVED name freezes a tmux session name into projects.toml on "+
			"an untouched enter", got)
	}
	screen := capturePane(t, h.ui, "ui")
	// The overlay is a FOOT, not a replacement: the dashboard it was opened from is still there,
	// so the operator can still see the row they are naming.
	if !strings.Contains(screen, h.paneIDs[0]) {
		t.Errorf("the overlay replaced the dashboard, so the row being named is no longer "+
			"visible:\n%s", screen)
	}
	if strings.Contains(screen, "» ") {
		t.Errorf("a row is marked as carrying the operator's own name before anything was "+
			"typed:\n%s", screen)
	}
}

// Typing reaches the field ONE KEY AT A TIME, caret last.
//
// Per-character is the whole point: `send-keys -l "some text"` arrives as one key message whose
// String() is the entire string, so a run injected in one call proves nothing about what a single
// key means inside this mode. The space is the one that needs a terminal — it arrives as
// tea.KeySpace rather than a rune, and the overlay has to insert it as text.
func TestE2EUINamingTypingReachesTheFieldKeyByKey(t *testing.T) {
	h := namingStart(t, 120, 40, 1)
	namingWalkTo(t, h.ui, rowNeedleIn(namingSession, h.paneIDs[0], h.paneIDs))
	send(t, h.ui, "N")
	screenHas(t, h.ui, "name this session:", "`N` must open the overlay")

	typeOneAtATime(t, h.ui, "the DR plan")
	namingWaitField(t, h.ui, "name: the DR plan▏",
		"a name typed key by key must appear exactly as typed with the caret at the end — a "+
			"field that drops the space, or the caret, is a field the operator cannot type into")

	// Backspace is part of typing, and it is a byte the runtime decodes.
	send(t, h.ui, "BSpace")
	namingWaitField(t, h.ui, "name: the DR pla▏",
		"backspace must take one character off the tail; a field that cannot be corrected forces "+
			"the operator to esc and start again")

	// Nothing is committed by typing: the dashboard still calls the row by its derived name and
	// no file exists yet.
	if s := capturePane(t, h.ui, "ui"); !readsDerived(s) {
		t.Errorf("the row stopped reading by its derived name while the name was still being "+
			"typed:\n%s", s)
	}
	if got := namingFile(t, h); got != "" {
		t.Errorf("typing wrote projects.toml before enter — a half-typed name reached the "+
			"disk:\n%s", got)
	}
}

// `enter` applies the name, and EVERY surface then calls the row by it: the list and the tile both,
// with the marker that says the name is the operator's own, and the derived name gone.
//
// Both width bands, because the surface that carries the name differs: below 100 the inbox row is
// inline and carries it, at 100 and above the group header does. The COUNT is what makes this
// per-surface — `Contains` passed once in this repo's history while the group header's marker had
// been mutated away, because the tile's satisfied it.
func TestE2EUINamingEnterAppliesTheNameToEverySurface(t *testing.T) {
	const name = "the DR plan"
	for _, cols := range []int{80, 120} {
		t.Run(fmt.Sprintf("%d columns", cols), func(t *testing.T) {
			h := namingStart(t, cols, 40, 1)
			namingNameTheRow(t, h, name)

			screen := capturePane(t, h.ui, "ui")
			if n := strings.Count(screen, "» "+name); n != 2 {
				t.Errorf("%d ownership markers for the named row, want 2 (the list and the "+
					"tile) — one surface is calling the row something else, which is the one "+
					"thing §21.12 rule 6 forbids:\n%s", n, screen)
			}
			if readsDerived(screen) {
				t.Errorf("the screen still shows the derived name beside the chosen one, so "+
					"one row reads two ways on one screen:\n%s", screen)
			}
			// The overlay is gone: `enter` is a commit, not a way to leave a screen open.
			if strings.Contains(screen, "enter: save") {
				t.Errorf("the overlay is still open after a successful save:\n%s", screen)
			}
			// And the row is still under the cursor. Naming a row must not move the operator
			// somewhere else, or the next key lands on a row they did not choose.
			if row := cursorRow(t, h.ui); !strings.Contains(row, h.paneIDs[0]) &&
				!strings.Contains(row, name) {
				t.Errorf("the cursor left the row it just named: %q", row)
			}
		})
	}
}

// A name follows the SESSION, not the pane: every pane of the named session reads by it after ONE
// gesture, another session on the same host keeps its own name, and the file holds ONE record.
//
// "An alias names a session and never a pane: the operator names the work, and the work outlives any
// one pane" is `AliasKey`'s own sentence, and it is a claim about a key shape that only a fleet with
// two panes in one session can test. The single record in the file is the half that cannot be
// satisfied by accident — a pane-keyed alias would need one per pane, so it would leave the second
// pane's group header reading the derived name.
func TestE2EUINamingANameFollowsTheSessionNotThePane(t *testing.T) {
	const name = "the DR plan"
	h := namingStart(t, 120, 40, 2)

	// A second session on the same watched server, so "the name reached this session" is
	// distinguishable from "the name reached everything".
	other, err := exec.Command("tmux", "-S", h.target, "-f", "/dev/null", "new-session", "-d",
		"-s", "other-e2e", "-c", h.proj, "-P", "-F", "#{pane_id}", "sleep", "300").Output()
	if err != nil {
		t.Fatalf("second session: %v", err)
	}
	_ = strings.TrimSpace(string(other))
	waitUntil(t, "both sessions to reach the screen", 30*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		// `other-e2e` by NAME (one pane, so no id is drawn) and the watched session by the id of
		// its second pane (two panes, so the ids are what tell those two rows apart).
		return err == nil && strings.Contains(s, "other-e2e") && strings.Contains(s, h.paneIDs[1])
	})

	// The SECOND pane of the first session, so the gesture is on a row that is not the one the
	// derived group header happens to be drawn from.
	namingRequireDerivedName(t, h)
	namingWalkTo(t, h.ui, rowNeedleIn(namingSession, h.paneIDs[1], h.paneIDs))
	send(t, h.ui, "N")
	screenHas(t, h.ui, "name this session: scratch · derived-e2e",
		"`N` on any pane of a session must name that session")
	typeOneAtATime(t, h.ui, name)
	namingWaitField(t, h.ui, "name: "+name+"▏", "what was typed must reach the field")
	send(t, h.ui, "Enter")
	namingWaitOverlayClosed(t, h.ui, "a legal name on an unnamed session must be accepted")

	waitUntil(t, "the named session to read by its name", 15*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && strings.Contains(s, "» "+name)
	})
	screen := capturePane(t, h.ui, "ui")
	if readsDerived(screen) {
		t.Errorf("one pane of the named session still reads by the derived name, so the name "+
			"landed on a PANE rather than on the session — the operator has to repeat the "+
			"gesture per pane:\n%s", screen)
	}
	for _, id := range []string{h.paneIDs[0], h.paneIDs[1]} {
		if !strings.Contains(screen, id) {
			t.Errorf("pane %s of the named session left the screen:\n%s", id, screen)
		}
	}
	// The OTHER session is untouched, name and row both. It holds ONE pane, so it takes no header
	// and its row carries the name — which is why the name is matched case-insensitively here: a
	// header upper-cases a derived name and a row spells it as the source did, and neither is a
	// property of "the neighbour kept its own name".
	if !strings.Contains(strings.ToUpper(screen), "OTHER-E2E") {
		t.Errorf("the other session lost its own name when its neighbour was named:\n%s", screen)
	}
	// Its pane id is deliberately NOT asserted: one pane in that session means the hub draws no id
	// on its row (rowPaneID), so the only place an id could be found is the tile of whichever row the
	// cursor is on — which is a statement about the cursor, not about the neighbour. The name above
	// is the row.

	body := namingFile(t, h)
	if n := strings.Count(body, "[[alias]]"); n != 1 {
		t.Errorf("projects.toml holds %d alias records for one gesture, want 1 — a name per PANE "+
			"is a name that stops applying the moment a pane is closed and reopened:\n%s", n, body)
	}
	for _, id := range []string{h.paneIDs[0], h.paneIDs[1]} {
		if strings.Contains(body, id) {
			t.Errorf("projects.toml keys the name on pane %s:\n%s", id, body)
		}
	}
}

// The name is in projects.toml, at the path the BINARY derives, and it survives a restart.
//
// Two claims, and the file is the reason the second one can hold. The path matters as much as the
// content: nothing else asserts that the process a user runs writes under $XDG_CONFIG_HOME rather
// than into whatever directory it was started in.
func TestE2EUINamingTheNameIsInProjectsTomlAndSurvivesARestart(t *testing.T) {
	const name = "the DR plan"
	h := namingStart(t, 120, 40, 1)
	namingNameTheRow(t, h, name)

	body := namingFile(t, h)
	if body == "" {
		t.Fatalf("no projects.toml at %s after a name was applied — the name is on the screen "+
			"and nowhere else, so it dies with the process", h.projects)
	}
	for _, want := range []string{
		"host = \"scratch\"",        // which host: a session name is not unique across the fleet
		"session = \"derived-e2e\"", // the key is the tmux SESSION, not the pane
		"name = \"" + name + "\"",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("projects.toml does not carry %s:\n%s", want, body)
		}
	}
	// A pane row's alias must NOT be written as an agent key, or the row that reads by the name
	// after a restart is a different row.
	if strings.Contains(body, "id = ") {
		t.Errorf("a pane row's name was written as an agent alias (an `id` key):\n%s", body)
	}

	h.restart(t)

	waitUntil(t, "the restarted hub to call the row by its saved name", 30*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && strings.Contains(s, "» "+name)
	})
	screen := capturePane(t, h.ui, "ui")
	if n := strings.Count(screen, "» "+name); n != 2 {
		t.Errorf("after a restart %d surfaces call the row by its name, want 2 — a name that "+
			"reaches only one surface on a fresh process was not fully loaded:\n%s", n, screen)
	}
	if readsDerived(screen) {
		t.Errorf("the restarted hub shows the derived name — the file was written and "+
			"not read:\n%s", screen)
	}
}

// `ctrl+u` then `enter` REMOVES the name (§21.12 rule 5): the row goes back to its derived name and
// the record leaves the file.
//
// Reopening is asserted first, and it is the half that proves the field reads what was stored: `N`
// on an already-named row must come up holding that name, or editing a name means retyping it.
func TestE2EUINamingCtrlUThenEnterRemovesTheName(t *testing.T) {
	const name = "the DR plan"
	h := namingStart(t, 120, 40, 1)
	namingNameTheRow(t, h, name)

	// By the NAME, because the row now reads by it: §21.12 rule 6 gives one display name, so the
	// derived `derived-e2e` is gone from the screen the moment the name is applied. A walk keyed on
	// the derived name here proves that by failing.
	namingWalkTo(t, h.ui, name)
	send(t, h.ui, "N")
	namingWaitField(t, h.ui, "name: "+name+"▏",
		"`N` on a named row must reopen holding the operator's own name, or a name can only be "+
			"replaced by retyping it")

	send(t, h.ui, "C-u")
	namingWaitField(t, h.ui, "name: ▏",
		"ctrl+u must clear the field — it is the first half of the only way to un-name a row")
	send(t, h.ui, "Enter")

	waitUntil(t, "the row to fall back to its derived name", 15*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && readsDerived(s)
	})
	screen := capturePane(t, h.ui, "ui")
	if strings.Contains(screen, name) {
		t.Errorf("the removed name is still on the screen:\n%s", screen)
	}
	if strings.Contains(screen, "» ") {
		t.Errorf("the row is still marked as carrying the operator's own name after the name "+
			"was removed:\n%s", screen)
	}
	body := namingFile(t, h)
	if strings.Contains(body, name) {
		t.Errorf("the removed name is still in projects.toml, so it comes back on the next "+
			"start:\n%s", body)
	}
	if strings.Contains(body, "[[alias]]") {
		t.Errorf("an empty alias record survived the removal, which reads on screen as a row "+
			"whose name failed to load:\n%s", body)
	}
}

// `esc` cancels: nothing on the screen changes and nothing reaches the disk.
//
// The disk half is the one a model cannot assert. A cancelled name that wrote the file would come
// back on the next start, which is the worst shape a cancel can have — the operator has already
// decided against it.
func TestE2EUINamingEscCancelsAndChangesNothingOnScreenOrOnDisk(t *testing.T) {
	h := namingStart(t, 120, 40, 1)
	namingWalkTo(t, h.ui, rowNeedleIn(namingSession, h.paneIDs[0], h.paneIDs))
	send(t, h.ui, "N")
	screenHas(t, h.ui, "name this session:", "`N` must open the overlay")
	typeOneAtATime(t, h.ui, "half typed")
	namingWaitField(t, h.ui, "name: half typed▏", "what was typed must reach the field")

	send(t, h.ui, "Escape")
	waitUntil(t, "the overlay to close", 15*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && !strings.Contains(s, "enter: save")
	})

	screen := capturePane(t, h.ui, "ui")
	if strings.Contains(screen, "half typed") {
		t.Errorf("a cancelled name is on the dashboard:\n%s", screen)
	}
	if strings.Contains(screen, "» ") {
		t.Errorf("a cancelled name left a row marked as the operator's own:\n%s", screen)
	}
	if !readsDerived(screen) {
		t.Errorf("the row lost its derived name to a name that was cancelled:\n%s", screen)
	}
	if got := namingFile(t, h); got != "" {
		t.Errorf("esc wrote projects.toml, so a name the operator cancelled comes back on the "+
			"next start:\n%s", got)
	}
}

// A name longer than the field shows its TAIL, with the caret, at the width the terminal really is.
//
// §21.12 rules out editing the name INLINE for exactly this reason — "the tail of what you type
// would be invisible" — so an overlay that truncates from the right reproduces the defect it was
// built to avoid. 80 columns is where it bites, and it is the size §16 commits to.
func TestE2EUINamingTheFieldShowsTheTailOfALongName(t *testing.T) {
	h := namingStart(t, 80, 24, 1)
	namingWalkTo(t, h.ui, rowNeedleIn(namingSession, h.paneIDs[0], h.paneIDs))
	send(t, h.ui, "N")
	screenHas(t, h.ui, "name this session:", "`N` must open the overlay")

	// ASCII on purpose, so a rune count IS the column count and this case needs no width table.
	//
	// The LENGTH is the fixture and it has to be derived rather than guessed: the field's room is
	// `cols - len("name: ")`, so a name that merely looks long is a name that fits, and the case
	// then asserts nothing. The first version typed 61 characters at 80 columns — 68 with the label
	// and the caret — and failed for that reason, which is the same shape as a truncation test whose
	// input is too short to reach the truncation.
	typed := "START-" + strings.Repeat("abcdefghij-", 8) + "END"
	if len(typed) <= h.cols-len("name: ") {
		t.Fatalf("the fixture is %d characters and the field has room for %d, so nothing would "+
			"be windowed", len(typed), h.cols-len("name: "))
	}
	typeOneAtATime(t, h.ui, typed)
	waitUntil(t, "the typed name to reach the field", 30*time.Second, func() bool {
		row, ok := namingFieldRow(t, h.ui)
		return ok && strings.Contains(row, "END")
	})

	field := namingField(t, h.ui)
	if !strings.HasSuffix(field, "END▏") {
		t.Errorf("the field is %q — the END of what is being typed and the caret must both be "+
			"visible, or the operator is typing into a box they cannot read", field)
	}
	if strings.Contains(field, "START") {
		t.Errorf("the field %q shows the HEAD of a name too long for the row, which means the "+
			"tail — the part being typed — is what was dropped", field)
	}
	if !strings.Contains(field, "…") {
		t.Errorf("the field %q hides characters without saying so; the operator cannot tell a "+
			"windowed field from a short name", field)
	}
	// Nothing on the screen overflowed: the overlay is a fixed six rows and a long field must not
	// push the dashboard sideways.
	for i, l := range strings.Split(strings.TrimRight(capturePane(t, h.ui, "ui"), "\n"), "\n") {
		if n := len([]rune(l)); n > h.cols {
			t.Errorf("line %d is %d columns on an %d-column terminal: %q", i+1, n, h.cols, l)
		}
	}
}

// ── `N` on the project list ───────────────────────────────────────────────────────────────────────

// `N` on the project list names the PROJECT, which is a prefix RULE rather than an alias, and the
// list then reads by it.
//
// The same overlay serves both halves (§21.12 rule 3) and only the subject row differs, so this
// case asserts the subject row, the rule in the file, and that the operator is returned to the
// screen they were on — a save that dropped them onto the dashboard would cost more than a cancel.
func TestE2EUINamingNOnTheProjectListNamesTheProject(t *testing.T) {
	const name = "the naming work"
	h := namingStart(t, 120, 40, 1)
	send(t, h.ui, "P")
	screenHas(t, h.ui, "namingproj",
		"the project list must show the group the watched pane's directory derives")

	send(t, h.ui, "N")
	screenHas(t, h.ui, "name this project: scratch · namingproj",
		"`N` on the list must say it is naming a PROJECT and which one — the same overlay names "+
			"a session, and the two write different things under different keys")
	screenHas(t, h.ui, "now:  namingproj  (not yours — the last segment of its directory)",
		"the overlay must say where the group's current label comes from, or the refusal about "+
			"ancestors that can follow cannot be read")

	typeOneAtATime(t, h.ui, name)
	namingWaitField(t, h.ui, "name: "+name+"▏", "what was typed must reach the field")
	send(t, h.ui, "Enter")

	namingWaitOverlayClosed(t, h.ui,
		"a project whose rows share an exclusive ancestor must be nameable — the refusal exists "+
			"for a group whose rows sit under different ancestors, and this one has a single "+
			"directory of its own")
	waitUntil(t, "the project list to read by the name", 20*time.Second, func() bool {
		s, err := paneScreen(t, h.ui, "ui")
		return err == nil && strings.Contains(s, name)
	})
	screen := capturePane(t, h.ui, "ui")
	// Back on the LIST, not on the dashboard: cancelling and committing must cost the same.
	if !strings.Contains(screen, "enter: narrow") {
		t.Errorf("a saved project name did not return the operator to the list they were "+
			"on:\n%s", screen)
	}
	if strings.Contains(screen, "namingproj") {
		t.Errorf("the derived label survives beside the chosen one, so the list shows two "+
			"names for one project:\n%s", screen)
	}

	body := namingFile(t, h)
	if !strings.Contains(body, "[[project]]") {
		t.Errorf("a project name was not written as a prefix rule:\n%s", body)
	}
	if !strings.Contains(body, "name = \""+name+"\"") {
		t.Errorf("projects.toml does not carry the project's name:\n%s", body)
	}
	// The rule is a PATH, and it is the group's own directory: a rule on an ancestor would
	// silently rename every project under it.
	if !strings.Contains(body, "prefix = \""+h.proj+"\"") {
		t.Errorf("the rule's prefix is not the project's own directory %q:\n%s", h.proj, body)
	}
}

// The name reaches the SESSION, so an attached session says what the operator called it — and this
// asserts the LINE A CLIENT DRAWS, not only the options the hub wrote (docs/design.md §21.16).
//
// Reading the options back would prove the writes and not the feature: tmux defaults
// `status-left-length` to TEN columns, measured on both versions of this fleet, so a hub that wrote
// the alias and the format and forgot the room draws `[billing-c` and stops — three correct options
// and a broken screen. The only assertion that cannot pass in that state is the drawn one.
func TestE2EUINamingReachesTheAttachedSessionsStatusLine(t *testing.T) {
	const name = "billing-cicd"
	h := namingStart(t, 120, 40, 1)
	namingNameTheRow(t, h, name)

	// The hub's own three options, on the session it was told about.
	show := func(option string) string {
		out, err := exec.Command("tmux", "-S", h.target, "show", "-v", "-t", "derived-e2e",
			option).CombinedOutput()
		if err != nil {
			t.Fatalf("show %s: %v: %s", option, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	waitUntil(t, "the hub to publish the name to the tmux server", 20*time.Second, func() bool {
		out, err := exec.Command("tmux", "-S", h.target, "show", "-v", "-t", "derived-e2e",
			"@hub_alias").CombinedOutput()
		return err == nil && strings.TrimSpace(string(out)) == name
	})
	if got := show("status-left"); !strings.Contains(got, "@hub_alias") {
		t.Errorf("status-left = %q, want the composing format", got)
	}
	// `[billing-cicd (derived-e2e)] ` is 1+12+2+11+3 = 29 columns, written out rather than derived
	// so this asserts the WIDTH and not the formula.
	if got := show("status-left-length"); got != "29" {
		t.Errorf("status-left-length = %q, want 29 — tmux's default of 10 cuts the line to "+
			"`[billing-c`", got)
	}

	// And what a CLIENT actually draws. The client runs in a window of the hub's own tmux server,
	// which is the only way to read a status line: capture-pane shows the pane's screen, and a
	// status line belongs to the client rather than to any pane. `$TMUX` is cleared or the inner
	// tmux refuses to nest.
	if out, err := exec.Command("tmux", "-S", h.ui, "new-window", "-d", "-n", "peek",
		"env -u TMUX tmux -S "+h.target+" attach -t derived-e2e").CombinedOutput(); err != nil {
		t.Fatalf("new-window for the client: %v: %s", err, out)
	}
	want := "[" + name + " (derived-e2e)]"
	waitUntil(t, "the attached session to draw its name", 20*time.Second, func() bool {
		out, err := exec.Command("tmux", "-S", h.ui, "capture-pane", "-p", "-t", "peek").Output()
		return err == nil && strings.Contains(string(out), want)
	})
	out, _ := exec.Command("tmux", "-S", h.ui, "capture-pane", "-p", "-t", "peek").Output()
	if !strings.Contains(string(out), want) {
		t.Errorf("the attached session does not draw %q — the name is on the dashboard and not "+
			"where the operator is working:\n%s", want, out)
	}
}
