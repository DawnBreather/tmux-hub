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

// THE HISTORY VIEW, driven through the real binary in a real terminal: `h` from the dashboard, and
// `r` inside it.
//
// What an in-process test structurally cannot reach here is the FILE. `internal/ui` hands the model
// a `[]history.Entry` it built in Go, so every one of its cases starts after the read: the log's
// on-disk dialect, the reader that skips what it cannot parse, and the fact that a hub started
// without a log says so instead of drawing an empty list are all outside its reach. The existing
// `internal/e2e/history_test.go` covers the other half — it drives `history.Log` and
// `broadcast.Sender` directly, and never presses a key. These cases are the join: a log this file
// wrote, read by the binary, shown on a screen, acted on with a keystroke.
//
// Every log here is written by the case under `t.TempDir()` and named with `--history`. The
// operator's own `history.jsonl` is never opened: `--history` is always passed, so
// `history.DefaultPath()` is never reached.

// historyHub starts the hub over a private watched server WITH a real send log.
//
// It exists because the two shared launchers cannot: `hubUI` and `hubWith` both pass `--no-history`,
// deliberately, so that no other area's case can touch a log. This one passes `--history <path>`
// instead, and the path is the caller's — the fixture is a file the case wrote. It pins `--view=flat`
// like every other launcher in this suite, and that is not decoration: `h` is a DASHBOARD key, so a
// launcher that let the hub open on its default screen would fail every case in this file with a
// screen that is working correctly.
//
// HOME is left alone, exactly as `hubWith` does, because an isolated home changes when the pane list
// arrives (that helper's own comment records a picker case timing out for thirty seconds over it).
// The operator's agent rows are noise for these cases rather than a problem: nothing here asserts a
// row count, and `historySelectPane` walks past them.
func historyHub(t *testing.T, cols, rows, panes int, logPath string) (ui, target string, paneIDs []string, work string) {
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

	// `cat` is this repo's choice for a pane that can be written to and read back, so a claim about
	// where a re-send went has an answer that needs no agent.
	id, err := run(target, "new-session", "-d", "-s", "watched", "-c", work,
		"-P", "-F", "#{pane_id}", "cat")
	if err != nil {
		t.Fatalf("new-session on the watched server: %v: %s", err, id)
	}
	paneIDs = append(paneIDs, id)
	for i := 1; i < panes; i++ {
		id, err := run(target, "new-window", "-t", "watched", "-c", work,
			"-P", "-F", "#{pane_id}", "cat")
		if err != nil {
			t.Fatalf("new-window on the watched server: %v: %s", err, id)
		}
		paneIDs = append(paneIDs, id)
	}

	// The hosts file holds a DECISION (one disabled entry) so the picker does not open at startup
	// over the screen every case below is about.
	hosts := filepath.Join(work, "hosts.toml")
	if err := os.WriteFile(hosts,
		[]byte("[[host]]\nalias = \"nothing\"\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	launch := fmt.Sprintf("%s --hosts %s --no-local --host scratch=%s,local --hidden %s "+
		"--view=flat --history %s; "+
		"echo EXITED-rc=$?; sleep 60",
		bin, hosts, target, filepath.Join(work, "hidden.json"), logPath)
	if out, err := run(ui, "new-session", "-d", "-s", "ui", "-x", fmt.Sprint(cols),
		"-y", fmt.Sprint(rows), "-c", work, launch); err != nil {
		t.Fatalf("start the hub: %v: %s", err, out)
	}

	// Wait for the FLEET, not the header: §16 paints a usable screen before any poll completes, so a
	// helper that waits for the header hands back a pre-poll frame and every assertion after it races.
	// By what the ROW shows: with one pane in `watched` the hub draws no id on it (rowPaneID), and
	// the only id on screen is the cursor's, in the tile.
	fleetNeedle := hubRowNeedle(paneIDs[0], paneIDs)
	waitUntil(t, "the watched pane "+paneIDs[0]+" to reach the screen", 30*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && strings.Contains(s, fleetNeedle)
	})
	return ui, target, paneIDs, work
}

// historyFixtureEntry is one line of a log the case owns. `ago` rather than an absolute time, so the
// fixture reads as "this was sent five minutes ago" and cannot drift into the future.
type historyFixtureEntry struct {
	ago     time.Duration
	host    string
	paneID  string
	session string
	window  string
	text    string
	outcome string
}

// historyFixtureLog writes the log, OLDEST LINE FIRST, which is the order the hub itself appends in.
//
// The JSON is written by hand rather than by marshalling `history.Entry`, and that is the point of
// the fixture: half of what these cases test is the on-disk DIALECT. A renamed tag breaks every log
// an operator already has, and a fixture built from the production struct would follow the rename
// and keep passing — this repo has paid for that shape three times (a loader that forgets a field
// passes every test that hand-builds the struct).
func historyFixtureLog(t *testing.T, path string, es []historyFixtureEntry) {
	t.Helper()
	var b strings.Builder
	for _, e := range es {
		fmt.Fprintf(&b, `{"at":%q,"host":%q,"pane_id":%q,"session_name":%q,"window_name":%q,`+
			`"text":%q,"outcome":%q,"submitted":false}`+"\n",
			time.Now().Add(-e.ago).Format(time.RFC3339Nano),
			e.host, e.paneID, e.session, e.window, e.text, e.outcome)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write the fixture log: %v", err)
	}
}

// The three entries every listing case reads. Their HOST and PANE are deliberately a host the fleet
// does not have and a pane id that does not exist: a re-send that used the recorded target instead
// of the current selection could then not be mistaken for one that used the selection.
//
// The three outcome words are all three the log can hold, so the glyph column is exercised by rows
// that differ rather than by three copies of one row.
var historyThreeSends = []historyFixtureEntry{
	{ago: 30 * time.Minute, host: "gone", paneID: "%91", session: "old", window: "w1",
		text: "history-fixture-oldest-do-not-resend", outcome: "refused"},
	{ago: 20 * time.Minute, host: "gone", paneID: "%92", session: "old", window: "w2",
		text: "history-fixture-middle-do-not-resend", outcome: "sent-unwitnessed"},
	{ago: 10 * time.Minute, host: "gone", paneID: "%93", session: "old", window: "w3",
		text: "history-fixture-newest-and-resendable", outcome: "delivered"},
}

const (
	historyOldest = "history-fixture-oldest-do-not-resend"
	historyMiddle = "history-fixture-middle-do-not-resend"
	historyNewest = "history-fixture-newest-and-resendable"
	// The view's own title, which is how a case tells "the history screen is up" from "a note was
	// printed on the dashboard". Those two are the same number of keystrokes apart and mean
	// opposite things.
	historyTitle = "History (newest first)"
)

// historyCursorHas waits for the `>` marker to land on a row containing want. The marker is the only
// way to know from OUTSIDE which entry the hub thinks is selected, and a keystroke's effect arrives
// asynchronously, so this polls rather than looking once.
func historyCursorHas(t *testing.T, ui, want, why string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		last = cursorRow(t, ui)
		if strings.Contains(last, want) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the cursor never reached the row for %q — %s\nmarker row: %q\n%s",
		want, why, last, capturePane(t, ui, "ui"))
}

// historyScreenLacks asserts on the screen AS IT IS, and is only ever called after something
// positive has been waited for. A bare "X is absent" would pass against a screen that had not
// painted yet, which is the way round that reports a broken product as working.
func historyScreenLacks(t *testing.T, ui, unwanted, why string) {
	t.Helper()
	if s := capturePane(t, ui, "ui"); strings.Contains(s, unwanted) {
		t.Errorf("the screen still shows %q — %s\n%s", unwanted, why, s)
	}
}

// historyRowFor returns the screen line carrying an entry's text, so a claim about a COLUMN is made
// against that entry's own row instead of against the whole screen. A `Contains` over the screen
// cannot tell which row a glyph is on, and three rows carry three different glyphs here.
func historyRowFor(t *testing.T, ui, text string) string {
	t.Helper()
	for _, line := range strings.Split(capturePane(t, ui, "ui"), "\n") {
		if strings.Contains(line, text) {
			return line
		}
	}
	return ""
}

// historySelectPane marks the watched pane and PROVES the mark landed on that pane's row.
//
// The proof is not ceremony. The list re-sorts when the agents listing arrives — a separate, slower
// producer, 0.5-2.8 s after start — so between `walkTo` reading the marker row and the `space`
// reaching the hub, the row under the cursor can change. A `space` that marked an agent row instead
// would aim a re-send at a target the case never chose, and the case would then be measuring the
// wrong pane while looking exactly like a pass.
//
// What it verifies is that EXACTLY ONE row is marked and that it is the one named — which is the
// property the case needs, since everything after it sends to the selection.
//
// It used to require `>◆` at the head of one row: cursor and mark together. That was a proxy for the
// same thing and it is a weaker one, because the cursor may legitimately move AFTER the mark lands —
// measured on the operator's own fleet, the mark sat on `%0` while the cursor had gone to an agent row
// that arrived from the 20-second listing, and the case failed while its premise held. The selection
// is what the send reads; the cursor is not.
// Its argument is what the ROW SHOWS, not the pane id — see hubRowNeedle.
func historySelectPane(t *testing.T, ui, needle string) {
	t.Helper()
	walkTo(t, ui, needle)
	send(t, ui, "space")
	deadline := time.Now().Add(10 * time.Second)
	var marked []string
	for time.Now().Before(deadline) {
		marked = markedRows(t, ui)
		if len(marked) == 1 && strings.Contains(marked[0], needle) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("`space` did not leave the row matching %q as the ONE marked row — this case cannot say "+
		"which pane it selected, so it must not go on to send to it\nmarked: %q\n%s",
		needle, marked, capturePane(t, ui, "ui"))
}

// markedRows are the inbox rows carrying the selection glyph. The glyph sits in a fixed column of the
// row, so a row is marked or it is not — no adjacency to another marker is involved.
func markedRows(t *testing.T, ui string) []string {
	t.Helper()
	s, err := paneScreen(t, ui, "ui")
	if err != nil {
		return nil
	}
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		// The band's own lines can carry a name that holds the glyph; a row is a row only above the
		// band, and the band begins at the first box-drawing rune.
		if strings.ContainsRune(ln, '┌') {
			break
		}
		if strings.Contains(ln, "◆") {
			out = append(out, strings.TrimRight(ln, " "))
		}
	}
	return out
}

// historyLoggedEntry is the reader side of the same hand-written dialect: what the HUB appended,
// read back off disk by the case. The tags are literal for the reason historyFixtureLog's are.
type historyLoggedEntry struct {
	Host    string `json:"host"`
	PaneID  string `json:"pane_id"`
	Text    string `json:"text"`
	Outcome string `json:"outcome"`
}

// historyReadLog returns every parsable line of a log, oldest first.
func historyReadLog(t *testing.T, path string) []historyLoggedEntry {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the log the hub wrote: %v", err)
	}
	var out []historyLoggedEntry
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e historyLoggedEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("the hub wrote a line this reader cannot parse: %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

// ── with no log at all ────────────────────────────────────────────────────────────────────────────

// A hub started with --no-history must SAY so when `h` is pressed, not open an empty list.
//
// The two failures this rules out are opposite and both silent: an empty history view is
// indistinguishable from a hub that has recorded nothing, and a key that answers nothing is
// indistinguishable from a key that is not bound. The shared launchers pass --no-history, so this is
// the one case here that needs no log of its own.
func TestE2EUIHistoryWithNoLogSaysWhyInsteadOfAnEmptyScreen(t *testing.T) {
	ui, _, _, _ := hubWith(t, 120, 40, 1, "cat")

	send(t, ui, "h")
	screenHas(t, ui, "no history log",
		"`h` on a hub started with --no-history must say the log is switched off; an operator who "+
			"gets no answer cannot tell that from a key that does nothing")
	screenHas(t, ui, "--no-history",
		"and it must name the flag that switched it off, which is the only thing the operator can act on")
	historyScreenLacks(t, ui, historyTitle,
		"a hub with no log must not open the view: an empty list reads as \"nothing was ever sent\"")
	// Still on the dashboard, so the refusal kept the screen rather than throwing the operator
	// somewhere else.
	screenHas(t, ui, "tmux-hub", "the refusal must keep the dashboard")
}

// An EMPTY log is a different fact from no log, and the hub must not confuse the two: the file
// exists, the operator has simply not sent anything yet. --history on a path that does not exist
// creates it, which is exactly the state a first run is in.
func TestE2EUIHistoryAnEmptyLogSaysItIsEmpty(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "history.jsonl")
	ui, _, _, _ := historyHub(t, 120, 40, 1, logPath)

	send(t, ui, "h")
	screenHas(t, ui, "history is empty",
		"`h` with a log that holds nothing must say it is empty — a blank list looks like a broken read")
	historyScreenLacks(t, ui, historyTitle,
		"an empty log must not open the view, or the operator is left reading three lines of chrome "+
			"and no answer")
	// The distinction is load-bearing: the two states have different remedies, so they must not
	// share a sentence.
	historyScreenLacks(t, ui, "no history log",
		"an empty log is not a MISSING log — telling the operator to drop --no-history when they "+
			"never passed it sends them after a flag that is not there")
}

// ── the listing ───────────────────────────────────────────────────────────────────────────────────

// `h` lists the sends NEWEST FIRST. The order is the whole reason the view exists: after
// broadcasting to six agents the thing to read is the last few sends, and a list that starts at the
// oldest puts them off the bottom of a screen that holds 200 entries.
func TestE2EUIHistoryListsTheSendsNewestFirst(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "history.jsonl")
	historyFixtureLog(t, logPath, historyThreeSends)
	ui, _, _, _ := historyHub(t, 120, 40, 1, logPath)

	send(t, ui, "h")
	screenHas(t, ui, historyTitle, "`h` with a log that has entries must open the view")
	for _, want := range []string{historyOldest, historyMiddle, historyNewest} {
		screenHas(t, ui, want, "every recorded send must reach the screen: "+want)
	}

	screen := capturePane(t, ui, "ui")
	newest, middle, oldest := strings.Index(screen, historyNewest),
		strings.Index(screen, historyMiddle), strings.Index(screen, historyOldest)
	if !(newest < middle && middle < oldest) {
		t.Errorf("the list is not newest-first (newest at %d, middle at %d, oldest at %d) — the "+
			"operator scans the top of this screen for the sends they just made\n%s",
			newest, middle, oldest, screen)
	}
	// The row carries the target it was SENT to, which is the column that answers "which ones got
	// it" for a broadcast. Both halves: the host and the pane id.
	if row := historyRowFor(t, ui, historyNewest); !strings.Contains(row, "gone") ||
		!strings.Contains(row, "%93") {
		t.Errorf("the newest row does not name the host and pane it was sent to: %q — without them "+
			"a broadcast's rows are indistinguishable from each other", row)
	}
}

// The OUTCOME word survives the round trip from disk to the glyph column.
//
// This is the one column an in-process test cannot vouch for: it hands the model entries it built in
// Go, so a renamed or dropped `outcome` tag would leave every real row rendering `?` — the operator
// then cannot tell a delivered send from a refused one, which is the question the view was built to
// answer — and every unit case would still pass. Asserted per ROW, because three glyphs on one
// screen cannot be attributed by a `Contains`.
func TestE2EUIHistoryTheOutcomeColumnSurvivesTheReadFromDisk(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "history.jsonl")
	historyFixtureLog(t, logPath, historyThreeSends)
	ui, _, _, _ := historyHub(t, 120, 40, 1, logPath)

	send(t, ui, "h")
	screenHas(t, ui, historyTitle, "`h` must open the view")
	screenHas(t, ui, historyOldest, "the oldest row must be on screen before its glyph is judged")

	for _, c := range []struct{ text, glyph, outcome string }{
		{historyNewest, "✓", "delivered"},
		{historyMiddle, "~", "sent-unwitnessed"},
		{historyOldest, "✗", "refused"},
	} {
		row := historyRowFor(t, ui, c.text)
		if !strings.Contains(row, c.glyph) {
			t.Errorf("the row recorded as %q does not carry %q: %q — a `?` in this column means the "+
				"operator cannot tell which sends landed, which is what the view is for",
				c.outcome, c.glyph, row)
		}
	}
}

// `j` and `k` move the selection, and the row under the marker is the one `r` would re-send.
//
// The sequence walks DOWN twice and back UP once, so `k` is judged by the marker leaving the oldest
// row rather than by it sitting where `j` had already put it.
func TestE2EUIHistoryJAndKMoveTheSelection(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "history.jsonl")
	historyFixtureLog(t, logPath, historyThreeSends)
	ui, _, _, _ := historyHub(t, 120, 40, 1, logPath)

	send(t, ui, "h")
	screenHas(t, ui, historyTitle, "`h` must open the view")
	historyCursorHas(t, ui, historyNewest,
		"the view must open on the NEWEST entry — that is the one an operator came here to re-send")

	send(t, ui, "j")
	historyCursorHas(t, ui, historyMiddle, "`j` must move the selection down one entry")
	send(t, ui, "j")
	historyCursorHas(t, ui, historyOldest, "`j` must keep moving down")
	send(t, ui, "k")
	historyCursorHas(t, ui, historyMiddle, "`k` must move the selection back up")

	// The last entry is the floor: a cursor that ran past the end would leave `r` with no entry, and
	// this repo has already shipped a cursor sitting past the end of a shorter list.
	//
	// Four presses, each its own keystroke with a gap. `send(ui, "j", "j", "j", "j")` puts all four
	// into ONE send-keys call, bubbletea coalesces the run into a single key message whose String()
	// is "jjjj", and the selection then moves NOWHERE — measured here, the marker sat on the middle
	// entry and the case failed against a correct product. Same rule as typeOneAtATime, one mode over.
	for i := 0; i < 4; i++ {
		send(t, ui, "j")
		time.Sleep(120 * time.Millisecond)
	}
	historyCursorHas(t, ui, historyOldest,
		"`j` at the last entry must stay there rather than run off the end of the list")
}

// ── leaving the view ──────────────────────────────────────────────────────────────────────────────

// `esc` leaves the history view for the dashboard.
func TestE2EUIHistoryEscReturnsToTheDashboard(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "history.jsonl")
	historyFixtureLog(t, logPath, historyThreeSends)
	ui, _, ids, _ := historyHub(t, 120, 40, 1, logPath)

	send(t, ui, "h")
	screenHas(t, ui, historyTitle, "`h` must open the view")

	send(t, ui, "Escape")
	screenHas(t, ui, hubRowNeedle(ids[0], ids),
		"esc must return to the dashboard — a view with no way out traps the operator in a read-only "+
			"screen while their agents wait")
	historyScreenLacks(t, ui, historyTitle, "esc must leave the history view, not draw it under the fleet")
}

// `q` inside the history view LEAVES IT, and `q` on the dashboard quits the hub. One key, two
// meanings, decided by the mode — which is precisely what a test handing `Update` an already-decoded
// key message cannot distinguish, because both arrive identically.
//
// The direction that matters is the first one: an operator who opens the log, reads it and presses
// `q` must still have a hub.
func TestE2EUIHistoryQLeavesTheViewAndOnlyQuitsFromTheDashboard(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "history.jsonl")
	historyFixtureLog(t, logPath, historyThreeSends)
	ui, _, ids, _ := historyHub(t, 120, 40, 1, logPath)

	send(t, ui, "h")
	screenHas(t, ui, historyTitle, "`h` must open the view")

	send(t, ui, "q")
	screenHas(t, ui, hubRowNeedle(ids[0], ids), "`q` in the history view must return to the dashboard, not quit the hub")
	historyScreenLacks(t, ui, "EXITED-rc=0",
		"`q` in the history view must NOT quit — reading the log would then cost the operator their hub")

	send(t, ui, "q")
	screenHas(t, ui, "EXITED-rc=0", "`q` on the dashboard must still quit")
}

// ── `r`, the reason the log is kept ───────────────────────────────────────────────────────────────

// `r` with nothing selected says what to do instead, and STAYS in the view.
//
// Both halves are the property. A key that answers nothing is this repo's recurring defect, and a
// refusal that also threw the operator back to the dashboard would make them re-open the log and
// find their place again to act on the answer.
func TestE2EUIHistoryResendWithNoSelectionSaysSo(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "history.jsonl")
	historyFixtureLog(t, logPath, historyThreeSends)
	ui, _, _, _ := historyHub(t, 120, 40, 1, logPath)

	send(t, ui, "h")
	screenHas(t, ui, historyTitle, "`h` must open the view")

	send(t, ui, "r")
	screenHas(t, ui, "select the panes to re-send to first",
		"`r` with an empty selection must say a selection is what is missing — a silent `r` is "+
			"indistinguishable from an unbound key")
	screenHas(t, ui, historyTitle,
		"the refusal must keep the history view, so the operator can select and press `r` again "+
			"without finding their place a second time")
}

// A re-send ALWAYS asks, and the dialog says the text came from the history view.
//
// That reason is one of the seven §7 lists, and it is the one that cannot be inferred from anything
// on the pane: every other reason is a fact about the target, this one is a fact about where the
// PAYLOAD came from. It is what tells the operator that the prompt about to be written is not the
// one they just typed — the difference between confirming a decision and confirming a reflex.
func TestE2EUIHistoryResendSaysTheTextCameFromTheHistoryView(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "history.jsonl")
	historyFixtureLog(t, logPath, historyThreeSends)
	ui, _, ids, _ := historyHub(t, 120, 40, 1, logPath)

	historySelectPane(t, ui, hubRowNeedle(ids[0], ids))

	send(t, ui, "h")
	screenHas(t, ui, historyTitle, "`h` must open the view")
	historyCursorHas(t, ui, historyNewest, "the view must open on the newest entry")

	send(t, ui, "r")
	screenHas(t, ui, "Confirm send",
		"a re-send must open the confirmation dialog — §7 makes text the operator did not just type "+
			"a reason to ask, unconditionally")
	screenHas(t, ui, "this came from the history view",
		"the dialog must say the payload came from the log rather than the input box; without it the "+
			"operator cannot tell this send from the one they typed")
	screenHas(t, ui, historyNewest,
		"and it must show the payload it is about to write, which is the only way to check it is the "+
			"entry that was under the cursor")
}

// `r` re-sends to the CURRENT SELECTION, never to the target the entry recorded.
//
// The fixture makes the two impossible to confuse: every entry records host `gone` and pane `%93`,
// which do not exist in this fleet, while the selection is the watched `cat` pane. So both pieces of
// evidence name one or the other and nothing has to be inferred —
//
//   - the dialog lists the pane it is about to write to;
//   - the log the hub then appends records which target the write was attempted against.
//
// The second is real state on disk, written by the hub rather than read off a screen, and it is what
// makes this more than an assertion about a label.
//
// The write itself is REFUSED, and that is the product working: a `cat` pane is not an agent, the
// identification walk therefore never stamps it, and §7's guard refuses a pane the hub holds no
// token for. Which is also why the arriving half of this path is not automated — it needs a real
// Claude pane and a real prompt, and the Enter that follows a paste spends the operator's tokens.
func TestE2EUIHistoryResendAimsAtTheCurrentSelectionNotTheRecordedTarget(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "history.jsonl")
	historyFixtureLog(t, logPath, historyThreeSends)
	ui, target, ids, _ := historyHub(t, 120, 40, 1, logPath)

	historySelectPane(t, ui, hubRowNeedle(ids[0], ids))

	send(t, ui, "h")
	screenHas(t, ui, historyTitle, "`h` must open the view")
	historyCursorHas(t, ui, historyNewest, "the view must open on the newest entry")
	send(t, ui, "r")
	screenHas(t, ui, "Confirm send", "a re-send must confirm")

	// The dialog names the pane the operator selected, and not the one the entry was sent to an
	// hour ago. An hour-old %93 on a host that has since restarted its server is a different pane.
	screenHas(t, ui, "scratch "+ids[0],
		"the dialog must name the CURRENT selection as the target of the re-send")
	historyScreenLacks(t, ui, "%93",
		"the dialog must not offer the target the entry recorded — that pane id names something else "+
			"by now, or nothing at all")

	send(t, ui, "Enter")
	screenHas(t, ui, "sent to 1 target",
		"the re-send must report against ONE target — the selection — and say so")

	// The log the hub wrote is the evidence that is not a label: one new entry, carrying the
	// selected pane rather than the recorded one, with the re-sent text.
	waitUntil(t, "the hub to record the re-send", 20*time.Second, func() bool {
		return len(historyReadLog(t, logPath)) > len(historyThreeSends)
	})
	entries := historyReadLog(t, logPath)
	last := entries[len(entries)-1]
	if last.Text != historyNewest {
		t.Errorf("the recorded re-send carries %q, not the entry that was under the cursor (%q) — "+
			"the operator confirmed one prompt and another was written",
			last.Text, historyNewest)
	}
	if last.Host != "scratch" || last.PaneID != ids[0] {
		t.Errorf("the re-send was aimed at %s %s, not at the selected pane scratch %s — a re-send "+
			"that follows the entry's own record writes into a pane the operator is no longer "+
			"looking at", last.Host, last.PaneID, ids[0])
	}

	// And the write was refused rather than performed, because the hub holds no token for a `cat`
	// pane. The pane itself is the witness: anything that reached it would be echoed on its screen.
	if last.Outcome != "refused" {
		t.Errorf("a re-send into a pane the hub cannot vouch for was recorded as %q — §7's guard "+
			"exists so this cannot be a write", last.Outcome)
	}
	time.Sleep(1500 * time.Millisecond)
	if s, err := paneScreen(t, target, ids[0]); err == nil && strings.Contains(s, historyNewest) {
		t.Errorf("the re-sent payload reached %s despite the refusal:\n%s", ids[0], s)
	}
}

// A DRAFT the operator typed must survive a look at the log.
//
// The composer keeps a draft across `esc` — the README promises it and
// TestE2EQTypesItselfInsideATextBoxAndQuitsOutside asserts it — and `r` fills that same composer from
// the log. So this case asks what a CANCELLED re-send costs: the send did not happen, the operator is
// back where they started, and the only thing that can have changed is what is in their input box.
//
// It is written as the promise rather than as the behaviour, deliberately. If the draft is gone the
// operator lost text they typed, silently, by reading a screen — and the answer to "was that
// intended" is that the product already promises the opposite for the other way out of the composer.
func TestE2EUIHistoryACancelledResendKeepsTheOperatorsDraft(t *testing.T) {
	const draft = "operator-draft-must-survive-the-log"
	logPath := filepath.Join(t.TempDir(), "history.jsonl")
	historyFixtureLog(t, logPath, historyThreeSends)
	ui, _, ids, _ := historyHub(t, 120, 40, 1, logPath)

	historySelectPane(t, ui, hubRowNeedle(ids[0], ids))
	send(t, ui, "i")
	screenHas(t, ui, "enter: send", "`i` must open the composer")
	sendLiteral(t, ui, draft)
	screenHas(t, ui, draft, "what was typed must appear in the composer")

	// The promise this case is built on, asserted first so a failure below cannot be blamed on it.
	send(t, ui, "Escape")
	time.Sleep(400 * time.Millisecond)
	send(t, ui, "i")
	screenHas(t, ui, draft, "esc from the composer must keep the draft — the README says so")
	send(t, ui, "Escape")
	time.Sleep(400 * time.Millisecond)

	send(t, ui, "h")
	screenHas(t, ui, historyTitle, "`h` must open the view")
	send(t, ui, "r")
	screenHas(t, ui, "Confirm send", "a re-send must confirm")

	// Cancel it. `x` is any-key-that-is-not-enter, and the dialog says so itself.
	send(t, ui, "x")
	screenHas(t, ui, "cancelled", "a key that is not enter must cancel the re-send")

	send(t, ui, "i")
	screenHas(t, ui, draft,
		"a CANCELLED re-send must leave the draft alone: nothing was sent, so the operator has "+
			"lost a prompt they typed by doing nothing but read their own log")
}
