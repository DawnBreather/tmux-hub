//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// HIDING THAT SURVIVES A RESTART, through the real binary in a real terminal.
//
// The gap these cases close: `hide.Set` round-trips through a file in a unit test and the interface
// hides a row in an e2e test, and NOTHING joined the two — no case had ever started the hub twice
// against one `hidden.json`. That round trip is the entire point of the set (§18 promises "never show
// me this again", not "not until you restart"), and it is the one property a single-run test cannot
// see: a mark written under a key the reader disagrees with, or a `--hidden` path the second run
// resolves differently, produces a perfectly green suite and a row that comes back.
//
// The rest are §18's exception, its key, and `X`. A pane that is WAITING stays on screen when marked
// and the hub says when the mark takes effect, and the mark then applies once the row stops asking; a
// hidden pane that STARTS asking comes back, which is the only safety net a wrong key match has; `X`
// is a way of LOOKING at hidden rows and must not clear a mark; and a window RENAME must not un-hide
// anything, because the key holds the window's index and not its name.
//
// CALIBRATED BOTH WAYS. Green on the real product, and every one of the six RUNNING cases killed by a
// product mutation built in a `git archive HEAD` tree, so this checkout was never touched — `Hidden`
// never true kills five of the six (the note case survives, correctly, since it is about
// `Resurfaced`); the resurface rule
// keyed on a state no live pane has kills exactly the three resurface cases; a key that follows the
// window NAME, which is the v1 defect, kills the restart case's on-disk assertion and the rename
// case; deleting the note kills only the note case; and a footer that never counts kills exactly the
// four cases that read a count. Every mutant compiled, because a mutant that does not build prints
// FAIL for the wrong reason.
//
// The SEVENTH case is skipped, and deliberately so: it asks what the show-hidden view says about
// itself, and the answer is nothing. The evidence is in its skip message.

// ── the fixture, which has to be able to start the hub TWICE ──────────────────────────────────────

// hidePersistFixture is one watched server, one hidden set, and a hub that can be started again
// against both.
//
// It is not `hubWith`, for two reasons that are both about this area rather than about taste:
//
//   - `hubWith` starts the hub INSIDE the helper, so there is no second start. The whole area is
//     about what survives one.
//   - `hubWith` keeps the operator's own HOME, so the fleet carries their Claude sessions as
//     pane-less rows — 27 of them on this machine when this file was written. For most cases that is
//     noise; here it is fatal, because the inbox does not scroll (`RenderInbox` stops at the body
//     height) and a watched pane pushed past row 40 is a pane `x` cannot reach.
//
// Everything else — send, screenHas, capturePane, waitUntil — is the shared harness.
type hidePersistFixture struct {
	bin    string // the hub binary, built once per case
	ui     string // the socket the hub is DISPLAYED on
	target string // the socket it is told to WATCH
	work   string
	hidden string   // the --hidden path, the file this whole area is about
	panes  []string // the watched pane ids, in creation order: index 0 is window 0
}

// hidePersistFleet builds the binary, starts a private watched server with `panes` cat panes — one
// per WINDOW, so the window index is what distinguishes them — and returns everything needed to
// start the hub against it. It does not start the hub: the cases do, because one of them does it
// twice.
func hidePersistFleet(t *testing.T, panes int) *hidePersistFixture {
	t.Helper()
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	f := &hidePersistFixture{bin: buildBinary(t), work: t.TempDir()}
	f.ui = filepath.Join(f.work, "ui.sock")
	f.target = filepath.Join(f.work, "target.sock")
	f.hidden = filepath.Join(f.work, "hidden.json")

	run := func(socket string, args ...string) (string, error) {
		full := append([]string{"-S", socket, "-f", "/dev/null"}, args...)
		out, err := exec.Command("tmux", full...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", f.target, "kill-server").Run()
		_ = exec.Command("tmux", "-S", f.ui, "kill-server").Run()
	})

	// `cat` for the same reason the rest of this package uses it: it is plainly not an agent, it
	// echoes what is written to it, and #{pane_start_command} is `cat` rather than empty — so the
	// key under test carries its corroborator instead of resting on position alone.
	id, err := run(f.target, "new-session", "-d", "-s", "watched", "-c", f.work,
		"-P", "-F", "#{pane_id}", "cat")
	if err != nil {
		t.Fatalf("new-session: %v: %s", err, id)
	}
	f.panes = append(f.panes, id)
	for i := 1; i < panes; i++ {
		id, err := run(f.target, "new-window", "-t", "watched", "-c", f.work,
			"-P", "-F", "#{pane_id}", "cat")
		if err != nil {
			t.Fatalf("new-window: %v: %s", err, id)
		}
		f.panes = append(f.panes, id)
	}

	// A hosts file that has DECIDED something, so the picker does not open at startup and swallow
	// every keystroke a case sends.
	if err := os.WriteFile(filepath.Join(f.work, "hosts.toml"),
		[]byte("[[host]]\nalias = \"nothing\"\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The PATH the hub runs with, and it is the most load-bearing line in this fixture. It holds a
	// link to the REAL tmux and nothing else, which buys two things:
	//
	//   - No `claude` on PATH, so the fleet is exactly the panes this case created and the row `x`
	//     is about is always on screen. The hub says so on its own footer rather than pretending —
	//     `scratch up (agents: claude is not installed here)`.
	//   - The operator's own HOME is kept. The obvious alternative, an isolated HOME like
	//     `hubWithHome` uses, is measured BROKEN for a watched server on this machine: `tmux` on the
	//     PATH inside a pane is a mise shim that resolves the real binary through
	//     $HOME/.local/share/mise, so with HOME pointed at an empty directory the hub reports
	//     `scratch down` with `0 sessions` and the reason `list-panes rc=1: mise ERROR tmux is not a
	//     valid shim`. Pinning PATH at the resolved binary is what makes a small fleet possible
	//     without breaking the transport. (`hubWithHome`'s comment used to attribute that empty
	//     screen to the pane list arriving late; re-measured at 4 s and 16 s, it is not late — the
	//     host is down and stays down. That comment now carries the measurement.)
	bin := filepath.Join(f.work, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(tmuxPath, filepath.Join(bin, "tmux")); err != nil {
		t.Fatal(err)
	}
	return f
}

// start runs the hub in the ui server's `ui` session, killing any previous run's session first so
// that every shared helper — all of which address `-t ui` — addresses the run that is live.
//
// waitFor is the string that says the FLEET has arrived. It is never the header: §16 promises a
// usable screen before any poll completes and keeps that promise, so a harness that waits for
// `tmux-hub` reads the pre-poll frame and every assertion about content races it.
func (f *hidePersistFixture) start(t *testing.T, cols, rows int, waitFor string) {
	t.Helper()
	_ = exec.Command("tmux", "-S", f.ui, "kill-session", "-t", "ui").Run()
	cmd := fmt.Sprintf("PATH=%s %s --hosts %s --no-local --host scratch=%s,local "+
		"--hidden %s --view=flat --no-history; echo EXITED-rc=$?; sleep 60",
		filepath.Join(f.work, "bin"), f.bin, filepath.Join(f.work, "hosts.toml"), f.target, f.hidden)
	out, err := exec.Command("tmux", "-S", f.ui, "-f", "/dev/null", "new-session", "-d", "-s", "ui",
		"-x", fmt.Sprint(cols), "-y", fmt.Sprint(rows), "-c", f.work, cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("start the hub: %v: %s", err, out)
	}
	waitUntil(t, "the fleet to reach the screen (waiting for "+waitFor+")", 30*time.Second,
		func() bool {
			s, err := paneScreen(t, f.ui, "ui")
			return err == nil && strings.Contains(s, waitFor)
		})
}

// hidePersistInbox is the part of a screen line that belongs to the INBOX.
//
// At 100 columns and above the tile is drawn to the RIGHT of the inbox on the same line, and the
// tile's header names the pane id too — so a `Contains` over a whole line cannot say which surface
// answered, and a row that had lost its `[↑]` would still pass against the tile beside it. Below 100
// the tile is stacked UNDERNEATH and its header names the id on a line of its own.
//
// The cut is at the first box-drawing rune, which handles both: every tile line carries one at the
// tile's left edge, and no dashboard inbox row contains one. It is deliberately NOT a cut at
// `ui.InboxWidth` — measured, that is the width of the inbox only in the grouped band, and at the
// 80 columns §16 commits to the inbox is the whole line, so a 28-column cut ate the pane id and
// `hidePersistWalkTo` reported "the cursor never reached the row" with the cursor sitting on it.
func hidePersistInbox(line string) string {
	if i := strings.IndexAny(line, "│┌└├┤┐┘─"); i >= 0 {
		line = line[:i]
	}
	return strings.TrimRight(line, " ")
}

// hidePersistRow is the inbox row that names id, or "" when no row does.
func hidePersistRow(t *testing.T, uiSock, id string) string {
	t.Helper()
	for _, line := range strings.Split(capturePane(t, uiSock, "ui"), "\n") {
		if row := hidePersistInbox(line); strings.Contains(row, id) {
			return row
		}
	}
	return ""
}

// hidePersistCursor is the inbox row the `>` marker is on.
func hidePersistCursor(t *testing.T, uiSock string) string {
	t.Helper()
	for _, line := range strings.Split(capturePane(t, uiSock, "ui"), "\n") {
		if row := hidePersistInbox(line); strings.HasPrefix(row, ">") {
			return row
		}
	}
	return ""
}

// hidePersistWalkTo puts the cursor on the row that names id.
//
// It goes UP before it goes down, which is why it is not the shared `walkTo`: a row that starts
// asking sorts to the TOP of the inbox, above the cursor, and the hub's `j` does not wrap
// (`cursorTo` clamps). A j-only walk simply never arrives, and then acts on whatever row it stopped
// on — the failure the shared helper's own comment records.
//
// Every press is its own keystroke: a run injected in one `send-keys` arrives as ONE key message.
func hidePersistWalkTo(t *testing.T, uiSock, id string) {
	t.Helper()
	for i := 0; i < 20; i++ {
		send(t, uiSock, "k")
		time.Sleep(40 * time.Millisecond)
	}
	for i := 0; i < 40; i++ {
		if strings.Contains(hidePersistCursor(t, uiSock), id) {
			return
		}
		send(t, uiSock, "j")
		time.Sleep(80 * time.Millisecond)
	}
	t.Fatalf("the cursor never reached the row for %s, so this case could not act on it\n%s",
		id, capturePane(t, uiSock, "ui"))
}

// hidePersistWaitFrame waits for ONE frame that holds every string in want and none in absent.
//
// Both halves in the same frame, on purpose. The first frame after a start carries no pane ids at
// all, so "the hidden pane is not on the screen" is trivially true before the fleet has arrived —
// an absence read too early is indistinguishable from a hidden row, and it is this suite's own
// recorded failure shape.
func hidePersistWaitFrame(t *testing.T, uiSock string, want, absent []string, why string) {
	t.Helper()
	var last string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		s, err := paneScreen(t, uiSock, "ui")
		if err == nil {
			last = s
			ok := true
			for _, w := range want {
				ok = ok && strings.Contains(s, w)
			}
			for _, a := range absent {
				ok = ok && !strings.Contains(s, a)
			}
			if ok {
				return
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("no frame showed %v while hiding %v — %s\n%s", want, absent, why, last)
}

// hidePersistFooter is the last non-empty line, which is the status line every count lands on.
func hidePersistFooter(t *testing.T, uiSock string) string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(capturePane(t, uiSock, "ui"), "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return strings.TrimRight(lines[i], " ")
		}
	}
	return ""
}

// hidePersistWaitFooter waits for the status line to say what a keystroke was supposed to make it
// say, and prints the line it did say when it never does.
func hidePersistWaitFooter(t *testing.T, uiSock, want, why string) {
	t.Helper()
	var last string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		last = hidePersistFooter(t, uiSock)
		if strings.Contains(last, want) {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("the status line never said %q — %s\nit said: %q", want, why, last)
}

// hidePersistPrompt puts a real question on a watched pane and waits for it to be ON the pane.
//
// The pane runs `cat`, so the terminal echoes the text and the pane's own screen carries a prompt
// `state.Classify` recognises — `[y/n]` is one of its literals. Nothing about the state is faked:
// the hub reads these pixels through the same capture every other pane goes through.
func hidePersistPrompt(t *testing.T, target, pane, text string) {
	t.Helper()
	if out, err := exec.Command("tmux", "-S", target, "send-keys", "-t", pane, "-l", text).
		CombinedOutput(); err != nil {
		t.Fatalf("send-keys to %s: %v: %s", pane, err, out)
	}
	// The TRIMMED text: `capture-pane` drops trailing whitespace from every line, so waiting for a
	// prompt that ends in the space a real prompt ends in waits forever. Measured — this timed out at
	// ten seconds against a pane that plainly carried the question.
	want := strings.TrimRight(text, " ")
	waitUntil(t, "the prompt to reach pane "+pane, 10*time.Second, func() bool {
		s, err := paneScreen(t, target, pane)
		return err == nil && strings.Contains(s, want)
	})
}

// hidePersistStopAsking pushes the prompt out of the classification zone.
//
// `tmux.ZoneLines` is 6 — the zone is the six lines ending at the cursor — so eight newlines leave
// the question in the scrollback where `Classify` cannot see it, and the pane goes back to a state
// that is not `needs`. This is the honest way to make a real pane stop asking: the text is still on
// the pane's screen history, which is exactly what happens when an agent answers a prompt and moves
// on.
func hidePersistStopAsking(t *testing.T, target, pane string) {
	t.Helper()
	for i := 0; i < 8; i++ {
		if out, err := exec.Command("tmux", "-S", target, "send-keys", "-t", pane, "Enter").
			CombinedOutput(); err != nil {
			t.Fatalf("send-keys Enter to %s: %v: %s", pane, err, out)
		}
	}
}

// hidePersistKeyFile is `hidden.json` as this test understands it, declared LITERALLY rather than
// imported from `hide`: a fixture that unmarshals into the type under test asserts that assignment
// works. The six fields are §18's key, and the one that matters most here is `window_index` — a
// window NAME in this file is the v1 defect, and there is a check for it below.
type hidePersistKeyFile struct {
	Version int `json:"v"`
	Keys    []struct {
		Kind        string `json:"kind"`
		Host        string `json:"host"`
		Session     string `json:"session"`
		WindowIndex int    `json:"window_index"`
		PaneIndex   int    `json:"pane_index"`
		Start       string `json:"start"`
	} `json:"keys"`
}

func hidePersistReadFile(t *testing.T, path string) (hidePersistKeyFile, []byte) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the hub wrote no hidden set at %s, so nothing could survive a restart: %v",
			path, err)
	}
	var f hidePersistKeyFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("the hidden set on disk is not readable as the shape §18 specifies (%v):\n%s",
			err, raw)
	}
	return f, raw
}

// ── the round trip through the file ───────────────────────────────────────────────────────────────

// `x`, `q`, start again: the pane is STILL hidden.
//
// No case had ever done this. Both halves were covered separately — a unit test writes the file and
// reads it back, an e2e case hides a row on screen — and the join is where the interesting failures
// live: a key the writer and the reader disagree about, a `--hidden` path resolved from a working
// directory the second run does not share, a set read before the flag is parsed. Every one of those
// is invisible to a suite that starts the hub once, and every one of them means the operator's
// decision was thrown away.
//
// The last two presses are the control, and they are what makes the absence evidence: `X` brings the
// row back, so the row EXISTS in the second run's fleet and was missing because it is hidden — not
// because the poll failed, not because the pane died with the first hub.
func TestE2EUIHidePersistAMarkSurvivesAQuitAndAFreshStart(t *testing.T) {
	f := hidePersistFleet(t, 2)
	f.start(t, 120, 40, f.panes[1])
	noisy, kept := f.panes[1], f.panes[0]

	hidePersistWalkTo(t, f.ui, noisy)
	send(t, f.ui, "x")
	hidePersistWaitFrame(t, f.ui, []string{kept}, []string{noisy},
		"`x` must take the row off the screen before there is anything for a restart to remember")
	hidePersistWaitFooter(t, f.ui, "1 hidden",
		"the operator has to be told a row is being kept off the screen, or the fleet just looks smaller")

	// The file is the medium the whole area rests on, so its CONTENT is asserted rather than
	// inferred from the screen: a hub that hid the row in memory and wrote nothing would pass every
	// assertion above.
	set, raw := hidePersistReadFile(t, f.hidden)
	if len(set.Keys) != 1 {
		t.Fatalf("one press of `x` wrote %d keys, not 1 — the file the next run reads is wrong:\n%s",
			len(set.Keys), raw)
	}
	k := set.Keys[0]
	if k.Kind != "pane" || k.Host != "scratch" || k.Session != "watched" ||
		k.WindowIndex != 1 || k.PaneIndex != 0 || k.Start != "cat" {
		t.Errorf("the persisted key does not name the pane the operator marked (window 1, pane 0 of "+
			"`watched` on `scratch`, started `cat`): %+v\n%s", k, raw)
	}
	// The v1 defect, checked on the artefact rather than argued about: a NAME in this file is a mark
	// that un-hides itself the moment tmux renames the window under it.
	if strings.Contains(string(raw), "\"window\":") {
		t.Errorf("the persisted key carries a window NAME, which automatic-rename rewrites — §18's "+
			"key is the index:\n%s", raw)
	}

	send(t, f.ui, "q")
	screenHas(t, f.ui, "EXITED-rc=0",
		"the hub must quit cleanly, because a mark that only survives a crash is not persistence")

	// A SECOND process, reading the same file. Nothing is carried in memory.
	f.start(t, 120, 40, kept)
	hidePersistWaitFrame(t, f.ui, []string{kept}, []string{noisy},
		"a hidden pane must still be hidden after a restart — §18 promises `never show me this "+
			"again`, and a mark the next run forgets means the operator has to hide it every morning")
	hidePersistWaitFooter(t, f.ui, "1 hidden",
		"the second run must also COUNT the row it is hiding, or the operator cannot tell a hidden "+
			"pane from a dead one")

	// The control: the row is there, and only the mark was keeping it off the screen.
	send(t, f.ui, "X")
	hidePersistWaitFrame(t, f.ui, []string{kept, noisy}, nil,
		"`X` must reveal the pane the restored mark was hiding — without this the absence above "+
			"could just as well be a pane that never arrived")
}

// ── `X` is a VIEW, not an unhide ──────────────────────────────────────────────────────────────────

// `X` shows the hidden rows and takes the count off the footer, and it must not touch the file.
//
// At 80×24, the size §16 calls "the size to hold, not a degraded case": the footer is where the
// count lives and the narrow band is where a count gets dropped. The byte comparison of the file
// across the toggle is the half that matters — a `X` that cleared the marks would look identical on
// screen and lose every decision the operator has made.
func TestE2EUIHidePersistXShowsHiddenRowsWithoutClearingTheMark(t *testing.T) {
	f := hidePersistFleet(t, 2)
	f.start(t, 80, 24, f.panes[1])
	noisy, kept := f.panes[1], f.panes[0]

	hidePersistWalkTo(t, f.ui, noisy)
	send(t, f.ui, "x")
	hidePersistWaitFrame(t, f.ui, []string{kept}, []string{noisy}, "`x` must hide the row")
	hidePersistWaitFooter(t, f.ui, "1 hidden",
		"the count is the only thing on an 80-column screen that says the fleet is not smaller than "+
			"it looks")
	_, before := hidePersistReadFile(t, f.hidden)

	send(t, f.ui, "X")
	hidePersistWaitFrame(t, f.ui, []string{kept, noisy}, nil,
		"`X` must show the hidden rows, which is the only way to find what you hid last week")
	// The COUNT, not the word: `N hidden` is a claim that N rows are being kept off this screen, and
	// with the toggle on that is false. A footer that said `showing 1 hidden row` would be right and
	// must not fail here — which is why this checks the sentence and not the substring. (The gap that
	// leaves is its own case, TestE2EUIHidePersistXSaysWhichRowsAreHidden.)
	if foot := hidePersistFooter(t, f.ui); strings.Contains(foot, "1 hidden") {
		t.Errorf("the footer claims a row is being kept off the screen while every row is on it, so "+
			"the number describes nothing the operator can see: %q", foot)
	}
	_, after := hidePersistReadFile(t, f.hidden)
	if string(before) != string(after) {
		t.Errorf("`X` rewrote the hidden set — it is a way of LOOKING at hidden rows, and an "+
			"operator who pressed it to check what they hid must not lose the marks\nbefore:\n%s\nafter:\n%s",
			before, after)
	}

	// And back: the same key returns to the filtered view with the count restored.
	send(t, f.ui, "X")
	hidePersistWaitFrame(t, f.ui, []string{kept}, []string{noisy},
		"`X` must toggle, so the operator can get their quiet screen back")
	hidePersistWaitFooter(t, f.ui, "1 hidden",
		"and the count must come back with it")
}

// While `X` is on, the screen must say WHICH rows are the hidden ones.
//
// The gesture only exists to answer "what did I hide?" (§12: "`X` toggles showing all hidden panes"),
// and on the screen it produces there is nothing to answer it with: a hidden row and a plainly
// visible one render as the same string but for their pane id, and the footer's count is suppressed
// while the toggle is on, so no surface says the view is unfiltered either.
func TestE2EUIHidePersistXSaysWhichRowsAreHidden(t *testing.T) {
	f := hidePersistFleet(t, 2)
	f.start(t, 80, 24, f.panes[1])
	noisy, kept := f.panes[1], f.panes[0]

	hidePersistWalkTo(t, f.ui, noisy)
	send(t, f.ui, "x")
	hidePersistWaitFrame(t, f.ui, []string{kept}, []string{noisy}, "`x` must hide the row")
	send(t, f.ui, "X")
	hidePersistWaitFrame(t, f.ui, []string{kept, noisy}, nil, "`X` must show the hidden row")

	// The comparison is between two rows of ONE frame, so no clock and no tile can move under it.
	// The cursor point and the pane id are the only differences a correct screen is allowed to have.
	strip := func(row, id string) string {
		return strings.TrimSpace(strings.ReplaceAll(strings.TrimLeft(row, "> "), id, ""))
	}
	hiddenRow := strip(hidePersistRow(t, f.ui, noisy), noisy)
	shownRow := strip(hidePersistRow(t, f.ui, kept), kept)
	if hiddenRow == shownRow {
		t.Errorf("the hidden row and the visible row are the same string, so `X` shows the operator "+
			"everything and tells them nothing: both read %q", hiddenRow)
	}
	if foot := hidePersistFooter(t, f.ui); !strings.Contains(foot, "hidden") {
		t.Errorf("the status line says nothing while the hidden rows are on show, so a screen with "+
			"the toggle left on is indistinguishable from a fleet with nothing hidden: %q", foot)
	}
}

// ── §18's exception: a row that is WAITING ────────────────────────────────────────────────────────

// hidePersistMarkedWhileWaiting is the fixture for the two cases below: a real waiting pane, marked.
//
// It returns the fixture and the pane that is asking. The state comes from `state.Classify` over the
// pane's own pixels — the hub is not told anything — and the case FAILS rather than skips if the pane
// does not reach `needs`, because a skip here would report PASS for the property nobody checked.
func hidePersistMarkedWhileWaiting(t *testing.T, cols, rows int) (*hidePersistFixture, string, string) {
	t.Helper()
	f := hidePersistFleet(t, 2)
	f.start(t, cols, rows, f.panes[1])
	asking, kept := f.panes[1], f.panes[0]

	hidePersistPrompt(t, f.target, asking, "Overwrite the config? [y/n] ")
	// The HUB's own reading of that pane, not the pane's screen: the row must say `needs` before a
	// case about the waiting exception presses anything.
	waitUntil(t, "the hub to classify the asking pane as needs", 20*time.Second, func() bool {
		return strings.Contains(hidePersistRow(t, f.ui, asking), "needs")
	})
	hidePersistWalkTo(t, f.ui, asking)
	send(t, f.ui, "x")
	return f, asking, kept
}

// `x` on a waiting row keeps it on screen, marks it as come-back, and SAYS when the mark applies.
//
// §18's resurface rule is the only safety net a wrong key match has, so the row staying is correct
// and the operator has no way to know their keystroke did anything — which is what the sentence is
// for. Both halves are asserted here because both are what an operator reads: the `[↑]` on the row,
// and the note that says the mark takes effect when the row stops asking.
func TestE2EUIHidePersistMarkingAWaitingRowKeepsItOnScreenAndSaysWhen(t *testing.T) {
	f, asking, _ := hidePersistMarkedWhileWaiting(t, 120, 40)

	screenHas(t, f.ui, "stays while it is waiting, and goes when it stops asking",
		"`x` on a row that is asking for the operator looks like it did nothing — the row is still "+
			"there — so the hub has to say what it recorded and when it applies")
	waitUntil(t, "the resurfaced marker to appear on the row", 15*time.Second, func() bool {
		return strings.Contains(hidePersistRow(t, f.ui, asking), "[↑]")
	})
	row := hidePersistRow(t, f.ui, asking)
	if !strings.Contains(row, "needs") {
		t.Errorf("the marked row is on screen without its state, so nothing says WHY it is still "+
			"here: %q", row)
	}

	// The mark is real, on disk, while the row is still visible. A waiting row that looks unhidden
	// and is unmarked would be a keystroke that silently did nothing.
	set, raw := hidePersistReadFile(t, f.hidden)
	if len(set.Keys) != 1 {
		t.Errorf("`x` on a waiting row recorded %d keys, not 1 — the row stays on screen by design, "+
			"so the file is the only evidence the keystroke landed:\n%s", len(set.Keys), raw)
	}
}

// The sentence is TRUE: the row goes when it stops asking.
//
// The case above asserts what the hub SAYS. This one asserts the thing said — the mark is not a
// no-op the resurface rule swallows for good, and it takes effect without another keystroke and
// without a restart. A hub that printed the sentence and dropped the mark would pass the case above.
func TestE2EUIHidePersistTheMarkTakesEffectWhenTheRowStopsAsking(t *testing.T) {
	f, asking, kept := hidePersistMarkedWhileWaiting(t, 120, 40)
	// WAIT for the mark to be on the row rather than reading once. `x` is a keystroke the runtime has
	// to decode, apply and repaint, and the first version of this line read the frame in between: the
	// row was there, `⚑ needs`, with no `[↑]` yet, which reads exactly like a mark that never landed.
	waitUntil(t, "the marked row to carry the resurfaced marker", 15*time.Second, func() bool {
		return strings.Contains(hidePersistRow(t, f.ui, asking), "[↑]")
	})

	hidePersistStopAsking(t, f.target, asking)
	hidePersistWaitFrame(t, f.ui, []string{kept}, []string{asking},
		"a marked row that has stopped asking must go — that is the sentence `x` printed, and a "+
			"mark that never applies means the operator must hide the pane again every time it "+
			"speaks")
	hidePersistWaitFooter(t, f.ui, "1 hidden",
		"and the row must be counted as hidden once it goes, not silently dropped")
}

// The other direction, and it is the load-bearing one: a HIDDEN pane that starts asking comes back.
//
// §18 calls this the actual safety net — a key can only mis-match a pane that shares a host,
// session, window index, pane index and start command with the one the operator hid, and even then
// the mis-matched pane cannot be hidden while it is blocked. So this is the case that says a wrong
// mark cannot lose work, and the footer's second count is how the operator learns about it without
// pressing anything.
//
// At 80×24 on purpose: `N of them waiting for input` is the LAST part of the status line and
// therefore the first one dropped, so the committed size is where it has to be checked.
func TestE2EUIHidePersistAHiddenPaneComesBackWhenItStartsAsking(t *testing.T) {
	f := hidePersistFleet(t, 2)
	f.start(t, 80, 24, f.panes[1])
	noisy, kept := f.panes[1], f.panes[0]

	// Marked while QUIET, so it really leaves the screen: this case is about it coming back on its
	// own, not about the mark-time exception.
	hidePersistWalkTo(t, f.ui, noisy)
	send(t, f.ui, "x")
	hidePersistWaitFrame(t, f.ui, []string{kept}, []string{noisy}, "`x` must hide a quiet row")

	hidePersistPrompt(t, f.target, noisy, "Do you want to proceed? [y/n] ")
	hidePersistWaitFrame(t, f.ui, []string{kept, noisy}, nil,
		"a hidden pane that starts asking must come back on its own — this is the rule that makes a "+
			"wrong hide survivable, and without it a marked pane could sit on a prompt forever")
	row := hidePersistRow(t, f.ui, noisy)
	if !strings.Contains(row, "[↑]") {
		t.Errorf("the row came back with nothing saying it is a hidden row that resurfaced, so the "+
			"operator cannot tell why a pane they hid is on screen: %q", row)
	}
	if !strings.Contains(row, "needs") {
		t.Errorf("the resurfaced row does not carry the state that brought it back: %q", row)
	}
	hidePersistWaitFooter(t, f.ui, "1 of them waiting for input",
		"the footer must say a hidden pane is waiting: the whole reason hidden panes are still "+
			"polled is that one of them can be blocked, and at 80 columns this is the count that "+
			"gets dropped first")
}

// ── the key holds the window's INDEX, not its name ────────────────────────────────────────────────

// A window RENAMED under a mark stays hidden.
//
// The v1 key held the window's name and tmux ships `automatic-rename on`, so a mark un-hid itself the
// moment the operator ran anything in the pane. `internal/hide` has a unit case for the key and this
// is the same claim through the product: the rename happens on the real server, the hub re-polls,
// and the row must not come back.
//
// The third pane is the control, and it is what makes the absence mean something: a pane created
// AFTER the rename appears on screen, so the frame being read is one the hub built from a poll that
// followed the rename. Without it, "the row did not come back" is equally consistent with a hub that
// stopped polling.
func TestE2EUIHidePersistARenamedWindowStaysHidden(t *testing.T) {
	f := hidePersistFleet(t, 2)
	f.start(t, 120, 40, f.panes[1])
	noisy, kept := f.panes[1], f.panes[0]

	hidePersistWalkTo(t, f.ui, noisy)
	send(t, f.ui, "x")
	hidePersistWaitFrame(t, f.ui, []string{kept}, []string{noisy}, "`x` must hide the row first")

	const renamed = "renamed-under-the-mark"
	if out, err := exec.Command("tmux", "-S", f.target, "rename-window", "-t", noisy, renamed).
		CombinedOutput(); err != nil {
		t.Fatalf("rename-window: %v: %s", err, out)
	}
	// The rename must have LANDED, or this case proves nothing.
	out, err := exec.Command("tmux", "-S", f.target, "list-windows", "-a", "-F",
		"#{window_index} #{window_name}").Output()
	if err != nil {
		t.Fatalf("list-windows: %v", err)
	}
	if !strings.Contains(string(out), "1 "+renamed) {
		t.Fatalf("the rename did not land, so the mark was never tested against one:\n%s", out)
	}

	// The control: a pane created after the rename, so the frame below is built from a later poll.
	fresh, err := exec.Command("tmux", "-S", f.target, "-f", "/dev/null", "new-window",
		"-t", "watched", "-c", f.work, "-P", "-F", "#{pane_id}", "cat").Output()
	if err != nil {
		t.Fatalf("control window: %v", err)
	}
	control := strings.TrimSpace(string(fresh))

	hidePersistWaitFrame(t, f.ui, []string{kept, control}, []string{noisy},
		"a mark must survive a window rename — the key holds the window's INDEX, and a name-keyed "+
			"mark un-hides itself the moment tmux renames the window under it, which "+
			"automatic-rename does on every command the operator runs")
}
