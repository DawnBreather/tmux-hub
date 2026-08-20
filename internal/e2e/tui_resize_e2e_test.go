//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// RESIZE, which is the one thing no in-process test can do.
//
// `internal/ui` can hand `Update` a `tea.WindowSizeMsg` and read `View()`, and that covers the
// renderer at a chosen width. What it cannot do is change the terminal UNDER a running program:
// `tmux resize-window` delivers a real SIGWINCH to the real binary, and what comes back is the
// bytes a terminal of that size really holds. Three things live only here. A screen that spends
// more ROWS than the terminal has scrolls its own top line away, which no `View()` string shows.
// A layout that keeps rendering for the PREVIOUS size is a defect a program started at one size
// never has. And a screen assembled from state that survives the resize — the cursor, the footer's
// idea of how much room it has — can be right at every width taken separately and wrong on the way
// between them.
//
// The property this file exists for is MONOTONICITY, and it is not a theory: the project journal
// records a footer composed at one width and rendered at another, where a WIDER terminal showed
// FEWER hosts. That defect is invisible to any test that renders once, and it is exactly what an
// operator hits when they drag a window edge.
//
// ── the fixture, and why it is shaped like this ────────────────────────────────────────────────
//
// SEVEN hosts, two of them live. The footer degrades by dropping identities from the tail and
// counting what it dropped, so a fleet that FITS at every width cannot show the defect at all:
// measured on this fixture the footer names 1 host at 40 columns, 3 at 80, 4 at 100 and all 7 at
// 160, which is a four-step staircase every step of which can go the wrong way. A one-host fleet —
// what the rest of this package runs with — is a monotonicity test that cannot fail.
//
// The five dead hosts are `--host label=<a path with no server>`: reachable-looking coordinates
// with nothing behind them, which is what a host that is switched off looks like. They cost no ssh
// and no network.
//
// `claude` is STUBBED to the empty listing, which is a fixture decision about the FLEET and not
// about the code under test. Every case here is about layout, and layout over the operator's own
// live Claude sessions is a coin flip: measured on this machine the listing returns 52 sessions per
// local host, so with two local hosts the inbox holds 104 agent rows, the tmux pane rows sort below
// every one of them, and `>` cannot be walked to a pane row in 60 presses. `[]` at rc=0 is the
// answer `agents.Parse`'s own doc comment calls "valid and common", so the stub is a machine with
// nothing running rather than a lie. HOME is deliberately NOT isolated: measured, an isolated HOME
// makes the hub's own `tmux` resolve to a mise SHIM that then fails
// (`mise ERROR tmux is not a valid shim`), and all seven hosts read `down` — the fleet would be
// gone for a reason that has nothing to do with resizing.

// ── calibration ────────────────────────────────────────────────────────────────────────────────
//
// Every case here was run against an injected defect in a `git archive HEAD` tree, because a case
// that has never failed is indistinguishable from one that cannot. One mutant per case, each of
// which compiles, and each killed by the case it targets while the others stayed green:
//
//	the frame        `bodyHeight` reserving 1 chrome row instead of 2 → the header leaves line 1
//	monotonicity     the footer given a 60-column budget above 100 → 80 names 3 hosts, 100 names 2
//	note + fleet     known-issues M1 restored (`if note != "" { foot = note }`) → 0 hosts at 200
//	the `+N` count   the fitter dropping parts without the marker → 3 of 7 named, +0 admitted
//	the band         `LayoutFor`'s `width < 100` → `width < 101` → 100 columns still inline
//	the cursor       `m = m.cursorTo(0)` on WindowSizeMsg → the cursor jumps to the top row
//	tiny and back    every SIGWINCH after the first ignored → 40x6 blank, 120x30 still narrow

// uiResizeFleet is the fleet in the order the flags are given, which is the order the footer drops
// from the tail of. No label may be a substring of another, because the assertions count labels in
// one line; uiResizeHub asserts that, so a future rename cannot silently make the counting lie.
var uiResizeFleet = []string{
	"northwind-alpha",  // live, two panes
	"southwind-bravo",  // live, one pane
	"eastwind-charlie", // the rest have no server behind their socket
	"westwind-delta",
	"upwind-echo",
	"downwind-foxtrot",
	"crosswind-golf",
}

// uiResizeHub starts the hub at 80×24 — the size §16 commits to, and the size every case here
// resizes AWAY from — and returns the display socket plus the pane ids of the two-pane host.
//
// Two panes on one host is what makes a row identifiable at both shapes: above 100 columns the
// host reaches the screen through the group header and the ROW carries only the pane id, so two
// hosts with one pane each are two rows both reading `%0` and neither says which host it is.
func uiResizeHub(t *testing.T) (ui string, alphaPanes []string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for i, a := range uiResizeFleet {
		for j, b := range uiResizeFleet {
			if i != j && strings.Contains(b, a) {
				t.Fatalf("host label %q is a substring of %q, so counting labels in the footer "+
					"would over-count and every monotonicity verdict here would be meaningless", a, b)
			}
		}
	}
	bin := buildBinary(t)
	work := t.TempDir()
	ui = filepath.Join(work, "ui.sock")

	run := func(socket string, args ...string) (string, error) {
		full := append([]string{"-S", socket, "-f", "/dev/null"}, args...)
		out, err := exec.Command("tmux", full...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	sock := func(label string) string { return filepath.Join(work, label+".sock") }
	t.Cleanup(func() {
		for _, label := range uiResizeFleet {
			_ = exec.Command("tmux", "-S", sock(label), "kill-server").Run()
		}
		_ = exec.Command("tmux", "-S", ui, "kill-server").Run()
	})

	// The live hosts. `cat` is this package's own choice for a pane that just sits there.
	alpha, bravo := uiResizeFleet[0], uiResizeFleet[1]
	id, err := run(sock(alpha), "new-session", "-d", "-s", "watched", "-c", work,
		"-P", "-F", "#{pane_id}", "cat")
	if err != nil {
		t.Fatalf("new-session on %s: %v: %s", alpha, err, id)
	}
	alphaPanes = append(alphaPanes, id)
	id, err = run(sock(alpha), "new-window", "-t", "watched", "-c", work,
		"-P", "-F", "#{pane_id}", "cat")
	if err != nil {
		t.Fatalf("new-window on %s: %v: %s", alpha, err, id)
	}
	alphaPanes = append(alphaPanes, id)
	if out, err := run(sock(bravo), "new-session", "-d", "-s", "watched", "-c", work, "cat"); err != nil {
		t.Fatalf("new-session on %s: %v: %s", bravo, err, out)
	}

	stub := filepath.Join(work, "stub")
	if err := os.MkdirAll(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stub, "claude"),
		[]byte("#!/bin/sh\nprintf '[]'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	hosts := filepath.Join(work, "hosts.toml")
	if err := os.WriteFile(hosts,
		[]byte("[[host]]\nalias = \"nothing\"\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"--hosts", hosts, "--no-local",
		"--host", alpha + "=" + sock(alpha) + ",local",
		"--host", bravo + "=" + sock(bravo) + ",local"}
	for _, label := range uiResizeFleet[2:] {
		// A socket path with no server behind it: a host that is switched off.
		args = append(args, "--host", label+"="+filepath.Join(work, label+"-absent.sock"))
	}
	args = append(args, "--hidden", filepath.Join(work, "hidden.json"), "--view=flat --no-history")
	// `$PATH` is left for the pane's own shell to expand, so the stub goes IN FRONT of the real
	// PATH rather than replacing it — the hub still needs the real `tmux`.
	launch := fmt.Sprintf("PATH=%s:$PATH %s %s; echo EXITED-rc=$?; sleep 60",
		stub, bin, strings.Join(args, " "))
	if out, err := run(ui, "new-session", "-d", "-s", "ui", "-x", "80", "-y", "24",
		"-c", work, launch); err != nil {
		t.Fatalf("start the hub: %v: %s", err, out)
	}

	// Wait for the PANE LIST, not for the header. §16 promises a usable screen before any poll
	// completes and keeps it, so the header is on the screen before the fleet is and every
	// assertion about content would race it. The second pane of the two-pane host is the signal
	// because it is the last thing this fixture produces.
	waitUntil(t, "the watched panes to reach the screen", 30*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && strings.Contains(s, alphaPanes[1])
	})
	return ui, alphaPanes
}

// uiResizeTo changes the real terminal and waits for the hub to have finished answering.
//
// Two waits, and each one is load-bearing. tmux applying the size is not the hub having repainted,
// so the first wait is on `#{pane_width}` and the second is on the SCREEN going still. The dwell
// before stillness is accepted is deliberate: the poll tick is one second, and a hub that repainted
// only on the next tick would otherwise be read at its STALE frame — which is stable, because
// nothing is writing to it, and would fail every assertion below for the wrong reason.
//
// Neither wait is the assertion. Nothing here looks at width, at the header or at the footer's
// contents; a hub that ignores SIGWINCH entirely settles immediately on its old frame and is then
// caught by the cases, which is the direction that must not be masked.
func uiResizeTo(t *testing.T, ui string, cols, rows int) {
	t.Helper()
	if out, err := exec.Command("tmux", "-S", ui, "resize-window", "-t", "ui",
		"-x", strconv.Itoa(cols), "-y", strconv.Itoa(rows)).CombinedOutput(); err != nil {
		t.Fatalf("resize-window to %dx%d: %v: %s", cols, rows, err, out)
	}
	want := fmt.Sprintf("%dx%d", cols, rows)
	waitUntil(t, "tmux to report the terminal as "+want, 10*time.Second, func() bool {
		out, err := exec.Command("tmux", "-S", ui, "display-message", "-p", "-t", "ui",
			"#{pane_width}x#{pane_height}").Output()
		return err == nil && strings.TrimSpace(string(out)) == want
	})

	settled := time.Now().Add(1200 * time.Millisecond)
	prev, stable := "", 0
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		s, err := paneScreen(t, ui, "ui")
		if err == nil {
			if s == prev {
				stable++
			} else {
				stable = 0
			}
			prev = s
			if stable >= 2 && time.Now().After(settled) {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the screen never stopped changing in 20s after a resize to %s — an operator who "+
		"drags a window edge is left reading a screen that will not settle\n%s", want, prev)
}

// uiResizeLines is the visible screen with the trailing blank rows removed, so the last entry is
// the bottom-most line the hub actually drew.
func uiResizeLines(t *testing.T, ui string) []string {
	t.Helper()
	ls := strings.Split(capturePane(t, ui, "ui"), "\n")
	for len(ls) > 0 && strings.TrimSpace(ls[len(ls)-1]) == "" {
		ls = ls[:len(ls)-1]
	}
	return ls
}

// uiResizeNamedHosts counts fleet labels in one line. A COUNT and not a Contains, for the reason
// this repo already paid for once: a screen carries a name on more than one surface, so presence
// answers a different question from how many the operator can read.
func uiResizeNamedHosts(line string) []string {
	var named []string
	for _, label := range uiResizeFleet {
		// The trailing space is what makes this a whole-label match: the footer writes
		// `<label> <status>`, so a label followed by anything else is not a fleet entry.
		if strings.Contains(line, label+" ") {
			named = append(named, label)
		}
	}
	return named
}

var uiResizeDroppedRE = regexp.MustCompile(`\+(\d+)\s*$`)

// uiResizeDropped reads the `+N` the fitter appends when it had to drop claimants.
func uiResizeDropped(line string) int {
	m := uiResizeDroppedRE.FindStringSubmatch(strings.TrimRight(line, " "))
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

// uiResizeInboxPart is the part of a screen line that belongs to the INBOX.
//
// It exists because at 100 columns and above the tile is drawn BESIDE the list, so one line of the
// screen holds two surfaces — measured, the tile's bottom border `└───┘` shares a line with the
// second pane row, and the tile draws the focused pane's own id in its top border. A whole-line
// match therefore answers "is this id anywhere on this line", which is a different question from
// "is this the row", and it can be satisfied by the tile alone.
//
// The cut is at the first box-drawing rune, so it needs no knowledge of the inbox's width: the tile
// always sits to the RIGHT of the list, and below 100 columns it sits underneath and no line is
// shared at all.
func uiResizeInboxPart(line string) string {
	if i := strings.IndexAny(line, "┌┐└┘│─"); i >= 0 {
		return line[:i]
	}
	return line
}

// uiResizeFooter is the bottom-most line the hub drew, which is where the fleet lives.
func uiResizeFooter(t *testing.T, ui string) string {
	t.Helper()
	ls := uiResizeLines(t, ui)
	if len(ls) == 0 {
		t.Fatalf("the hub drew nothing at all")
	}
	return strings.TrimRight(ls[len(ls)-1], " ")
}

// ── the frame survives every width ────────────────────────────────────────────────────────────

// A real SIGWINCH at each of the four widths the design names, up and back down, and after every
// one of them the screen still has its frame: the header on the FIRST line, the fleet on the LAST,
// and nothing wider than the terminal.
//
// The checks are one claim seen from several sides, and the load-bearing one is the HEADER'S
// POSITION. A screen that spends one row too many — a line that wrapped, or a body one row taller
// than the terminal — pushes its own top line off the alt screen, so "line 1 is the header" is what
// detects both. Calibrated against a chrome miscount (`bodyHeight` reserving one row instead of
// two): the header leaves line 1 at all four widths and this case fails at every one of them,
// while the line COUNT stays at 24 because the surplus row scrolled rather than accumulated.
//
// The rune-width check is a backstop and is honestly labelled as one: measured against a footer
// deliberately composed 40 columns too wide, the bytes never reach the terminal at all, because
// bubbletea's renderer truncates every line to the width it was told. So the product cannot bleed
// past the right edge while that holds — this check is what notices if it stops holding, and it is
// not the reason the case is here.
func TestE2EUIResizeKeepsTheFrameIntactAtEveryWidth(t *testing.T) {
	ui, _ := uiResizeHub(t)

	// Up through the bands and back down, so a frame that survives growing and not shrinking is
	// caught too. 24 rows throughout: this case is about width.
	for _, cols := range []int{80, 100, 160, 200, 160, 100, 80} {
		uiResizeTo(t, ui, cols, 24)
		ls := uiResizeLines(t, ui)
		if len(ls) == 0 {
			t.Fatalf("the hub drew nothing at %d columns", cols)
		}
		if !strings.HasPrefix(ls[0], "tmux-hub") {
			t.Errorf("at %d columns the first line is not the header but %q — the operator has "+
				"lost the session count and the hint for leaving an attached session, and the "+
				"reason is that the hub spent one row more than the terminal has: either a line "+
				"wrapped or the body was drawn one row too tall, and the top scrolled away:\n%s",
				cols, ls[0], strings.Join(ls, "\n"))
		}
		footer := strings.TrimRight(ls[len(ls)-1], " ")
		if named := uiResizeNamedHosts(footer); len(named) == 0 {
			t.Errorf("at %d columns the bottom line names no host (%q) — the fleet's health is "+
				"the one positive statement on the screen, and losing it is how an operator "+
				"comes to act on a host that is down:\n%s",
				cols, footer, strings.Join(ls, "\n"))
		}
		// The footer is on the BOTTOM row, which is a positive statement and not the tautology
		// `len(ls) <= 24` would be: capture-pane cannot return more lines than the pane has rows,
		// so an upper bound here could never fail. A screen that fills 20 of 24 rows leaves the
		// fleet floating in the middle with four rows of nothing under it.
		if n := len(ls); n != 24 {
			t.Errorf("at %d columns the hub drew %d lines on a 24-row terminal, so the fleet is "+
				"not on the bottom row where the operator looks for it:\n%s",
				cols, n, strings.Join(ls, "\n"))
		}
		for i, l := range ls {
			if r := len([]rune(l)); r > cols {
				t.Errorf("at %d columns line %d is %d runes wide and therefore draws outside "+
					"the terminal: %q", cols, i+1, r, l)
			}
		}
	}
}

// ── monotonicity: a wider terminal must never show LESS ───────────────────────────────────────

// The property the journal names: a WIDER terminal showing FEWER hosts than a narrower one.
//
// It is asserted over a fleet that really does degrade — measured 1 host at 40 columns, 3 at 80, 4
// at 100, all 7 at 160 — so every step can go the wrong way. Each width is visited TWICE, once
// growing and once shrinking, and the two visits must agree: a footer whose contents depend on the
// width it was last rendered at, rather than on the width it is rendered at, is the same defect
// wearing a different hat, and only a real resize can tell them apart.
func TestE2EUIResizeNeverShowsFewerHostsOnAWiderTerminal(t *testing.T) {
	ui, _ := uiResizeHub(t)

	ladder := []int{80, 100, 160, 200, 160, 100, 80}
	seen := map[int][]string{}
	for _, cols := range ladder {
		uiResizeTo(t, ui, cols, 24)
		footer := uiResizeFooter(t, ui)
		named := uiResizeNamedHosts(footer)
		if before, ok := seen[cols]; ok {
			if strings.Join(before, ",") != strings.Join(named, ",") {
				t.Errorf("%d columns showed %v on the way up and %v on the way back down — the "+
					"footer depends on the width it was LAST rendered at rather than the width "+
					"it is rendered at, so what an operator sees depends on how they got there\n%s",
					cols, before, named, footer)
			}
			continue
		}
		seen[cols] = named
	}

	widths := []int{80, 100, 160, 200}
	for i := 1; i < len(widths); i++ {
		narrow, wide := widths[i-1], widths[i]
		if len(seen[wide]) < len(seen[narrow]) {
			t.Errorf("%d columns names %d hosts (%v) but the NARROWER %d names %d (%v) — an "+
				"operator who makes their window bigger must not lose a host by doing it",
				wide, len(seen[wide]), seen[wide], narrow, len(seen[narrow]), seen[narrow])
		}
		// The wider screen's fleet must be a SUPERSET, not merely as long: swapping one host for
		// another keeps the count and still loses the host the operator was watching.
		for _, label := range seen[narrow] {
			if !uiResizeHas(seen[wide], label) {
				t.Errorf("%s is named at %d columns and gone at the wider %d (%v) — a wider "+
					"terminal may add hosts and may never trade one away",
					label, narrow, wide, seen[wide])
			}
		}
	}
}

func uiResizeHas(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// ── a note and the fleet share one row, at every width ────────────────────────────────────────

// The historical defect in full: a NOTE and the FLEET both want the footer, and the fleet used to
// lose it whole. Known-issues M1 is `if note != "" { foot = note }` — at 80×24 the whole line
// `local up · nuc degraded:format` left the screen the moment a note appeared, so the one positive
// statement about fleet health vanished at the exact moment the operator was acting. The lesson
// beside it is the other half: with the note composed at full width and handed to the fitter as one
// part, the fleet could only drop WHOLE, and a WIDER terminal could show fewer hosts.
//
// Both halves are one screen away from each other, and this case is the only place they meet: a
// real note, a fleet too big for the narrow band, and a real resize between the widths. Measured on
// this fixture, the correct behaviour is a staircase — 0 hosts at 80 with the loss marked `+7`, then
// 1, then 4, then 6 at 200 — the note never dropped, and `named + N == 7` at all four.
//
// `R` with nothing marked is the note used because it changes nothing: it refuses, it says why, and
// measured it is still on the screen nine seconds and several ticks later, so it is a claimant that
// can be resized around rather than a flash.
func TestE2EUIResizeANoteAndTheFleetShareTheFooterAtEveryWidth(t *testing.T) {
	ui, _ := uiResizeHub(t)

	send(t, ui, "R")
	screenHas(t, ui, "select a pane with space first",
		"`R` with nothing marked must refuse and say so — this case needs a note in the footer")

	widths := []int{80, 100, 160, 200}
	counts := map[int]int{}
	for _, cols := range widths {
		uiResizeTo(t, ui, cols, 24)
		footer := uiResizeFooter(t, ui)
		if !strings.Contains(footer, "select a pane with space first") {
			t.Errorf("at %d columns the note is no longer in the footer (%q) — the answer to what "+
				"the operator just pressed outranks ambient status, and losing it means a key "+
				"that reported nothing", cols, footer)
		}
		named := uiResizeNamedHosts(footer)
		dropped := uiResizeDropped(footer)
		counts[cols] = len(named)
		if len(named)+dropped != len(uiResizeFleet) {
			t.Errorf("at %d columns a note is showing and the footer accounts for %d of %d hosts "+
				"(%d named, +%d): %q — a note may cost the fleet its ROOM and may never cost it "+
				"its COUNT, because a fleet that reads as complete is one the operator stops "+
				"checking", cols, len(named)+dropped, len(uiResizeFleet), len(named), dropped, footer)
		}
	}
	for i := 1; i < len(widths); i++ {
		narrow, wide := widths[i-1], widths[i]
		if counts[wide] < counts[narrow] {
			t.Errorf("with a note showing, %d columns names %d hosts and the NARROWER %d names %d "+
				"— the note is being composed for a width the row does not have, which is the "+
				"defect that made this footer non-monotonic once already",
				wide, counts[wide], narrow, counts[narrow])
		}
	}
	// The widest screen has room for both, and that is the M1 assertion: a note must SHARE the row,
	// not take it. A footer that never names a host however wide the terminal gets has replaced the
	// fleet rather than crowded it.
	if counts[200] == 0 {
		t.Errorf("at 200 columns the footer still names no host while a note shows — the note has "+
			"REPLACED the fleet instead of sharing the row with it, which is known-issues M1:\n%s",
			uiResizeFooter(t, ui))
	}
}

// ── the footer's account of the fleet must add up at every size ────────────────────────────────

// When the footer cannot name every host it appends `+N`, and named + N must equal the fleet. That
// is the difference between "three hosts, and four more you cannot see here" and "you have three
// hosts" — the second is a screen that tells the operator their fleet is smaller than it is.
//
// Asserted at three sizes because the arithmetic is recomputed at each one, and the smallest is the
// one where it matters most: at 40×6 six of the seven are dropped.
func TestE2EUIResizeTheDroppedHostCountAddsUpAtEverySize(t *testing.T) {
	ui, _ := uiResizeHub(t)

	for _, size := range []struct{ cols, rows int }{{40, 6}, {80, 24}, {100, 24}} {
		uiResizeTo(t, ui, size.cols, size.rows)
		footer := uiResizeFooter(t, ui)
		named := uiResizeNamedHosts(footer)
		dropped := uiResizeDropped(footer)
		if len(named)+dropped != len(uiResizeFleet) {
			t.Errorf("at %dx%d the footer names %d hosts and admits +%d, which accounts for %d "+
				"of a %d-host fleet — the operator is told their fleet is smaller than it is: %q",
				size.cols, size.rows, len(named), dropped, len(named)+dropped,
				len(uiResizeFleet), footer)
		}
		if len(named) < len(uiResizeFleet) && dropped == 0 {
			t.Errorf("at %dx%d the footer names only %d of %d hosts and says nothing about the "+
				"rest: %q — a silently short list reads as a complete one",
				size.cols, size.rows, len(named), len(uiResizeFleet), footer)
		}
	}
}

// ── the band boundary, one column apart ───────────────────────────────────────────────────────

// Crossing 100 columns changes the inbox from the inline shape to the grouped one, and the row must
// stay identifiable in BOTH. The crossing is asserted at 99 → 100 → 99, one column apart, because
// that is where the branch is: a case at 80 and 160 proves the two shapes exist and not that the
// boundary is where the design says.
//
// What "identifiable" means differs by shape, and that is the whole point. Below 100 the row
// carries `host/session` and the pane id inline. At and above 100 the row carries only the pane id
// and the command, and the host reaches the screen through the group header, UPPERCASED — so a
// lowercase match on the host would fail against a correct screen, and an assertion written only
// for one shape passes on a screen that has lost the other's identity entirely.
func TestE2EUIResizeCrossingTheBandBoundaryKeepsTheRowIdentifiable(t *testing.T) {
	ui, panes := uiResizeHub(t)
	alpha := uiResizeFleet[0]
	inline := alpha + "/watched"
	group := strings.ToUpper(alpha) + " WATCHED"

	// 99 columns: the last width of the inline shape.
	uiResizeTo(t, ui, 99, 24)
	screen := capturePane(t, ui, "ui")
	if !strings.Contains(screen, inline+" "+panes[1]) {
		t.Errorf("at 99 columns no row carries %q — below 100 the row is the only place the host "+
			"and session appear, so without it the operator cannot tell which machine a pane is "+
			"on:\n%s", inline+" "+panes[1], screen)
	}
	if strings.Contains(screen, group) {
		t.Errorf("at 99 columns the inbox drew the GROUPED header %q — the grouped shape spends "+
			"a row per session, which §16 measured as 5 of 11 body rows at the committed "+
			"size:\n%s", group, screen)
	}

	// 100 columns: the first width of the grouped shape, one column later.
	uiResizeTo(t, ui, 100, 24)
	screen = capturePane(t, ui, "ui")
	if !strings.Contains(screen, group) {
		t.Errorf("at 100 columns the group header %q is not on the screen — the grouped shape "+
			"takes the host off the row, so without the header no row says which host it is "+
			"on:\n%s", group, screen)
	}
	if strings.Contains(screen, inline) {
		t.Errorf("at 100 columns a row still carries the inline form %q — the layout did not change "+
			"band at the width the design says it does, and if the group header is there too then "+
			"one screen names one row twice, which §21.12 forbids in terms:\n%s", inline, screen)
	}
	// The inbox ROW, not the tile: at this width they share a line, and the tile draws the focused
	// pane's id in its own border. No state word is matched either — the classification of a `cat`
	// pane is legitimately `works` or `idle` depending on when it was last observed, and an
	// assertion that names one is a coin flip on a correct screen.
	var rowLine string
	for _, l := range strings.Split(screen, "\n") {
		if left := uiResizeInboxPart(l); strings.Contains(left, panes[1]+" ") {
			rowLine = left
			break
		}
	}
	if rowLine == "" {
		t.Errorf("at 100 columns no grouped inbox row carries pane %s — in this shape the pane id "+
			"is the row's whole identity, so a row without it names nothing:\n%s",
			panes[1], screen)
	} else if !strings.Contains(rowLine, "cat") {
		t.Errorf("at 100 columns the row for %s does not say what it is running (%q) — the pane "+
			"id alone identifies nothing to a person:\n%s", panes[1], rowLine, screen)
	}

	// And back: the inline shape must RETURN, not be a one-way trip.
	uiResizeTo(t, ui, 99, 24)
	screen = capturePane(t, ui, "ui")
	if !strings.Contains(screen, inline+" "+panes[1]) {
		t.Errorf("shrinking back to 99 columns did not restore the inline shape — %q is not on "+
			"the screen, so a window that was widened once stays in a layout that does not "+
			"fit:\n%s", inline+" "+panes[1], screen)
	}
}

// ── the cursor is a ROW, not a screen position ─────────────────────────────────────────────────

// A resize re-sorts nothing and must move nothing: the row under the cursor before the resize is
// the row under it after. The journal's ruling is that the cursor is named by row IDENTITY with the
// position kept only as a fallback for the row vanishing — and a resize is the case that separates
// the two, because it changes where a row is drawn without changing which rows exist.
//
// The pane id is what proves it, and the TILE is the second witness: it draws the focused pane's own
// id in its border, so a cursor that quietly moved shows up there too.
func TestE2EUIResizeKeepsTheCursorOnTheSameRow(t *testing.T) {
	ui, panes := uiResizeHub(t)

	// Put the cursor on the SECOND pane of the two-pane host, so "it did not move" is a real
	// claim: a cursor reset to the top would land on the first row and look plausible.
	walkTo(t, ui, panes[1])
	before := cursorRow(t, ui)

	for _, cols := range []int{100, 160, 200, 80} {
		uiResizeTo(t, ui, cols, 24)
		// The INBOX part of the cursor's line. Above 100 columns the tile shares the line and
		// draws the focused pane's id in its border, so a whole-line match would be satisfied by
		// the tile even if the cursor had moved to another row — which is the failure this case
		// exists to catch.
		row := uiResizeInboxPart(cursorRow(t, ui))
		if strings.TrimSpace(row) == "" {
			t.Fatalf("at %d columns there is no cursor on the screen at all (it was %q) — every "+
				"key an operator presses acts on the row under the cursor\n%s",
				cols, before, capturePane(t, ui, "ui"))
		}
		if !strings.Contains(row, panes[1]) {
			t.Errorf("at %d columns the cursor is on %q, not on the row for pane %s it was on "+
				"before the resize (%q) — resizing a window silently re-aims every key, and "+
				"`x`, `!` and enter all act on whatever it landed on\n%s",
				cols, row, panes[1], before, capturePane(t, ui, "ui"))
		}
		// The tile is the second surface, and it must agree with the list.
		if s := capturePane(t, ui, "ui"); !strings.Contains(s, "watched "+panes[1]) {
			t.Errorf("at %d columns the tile does not show pane %s — the list and the tile "+
				"disagree about what is focused:\n%s", cols, panes[1], s)
		}
	}
}

// ── down to a terminal smaller than the chrome, and back ──────────────────────────────────────

// 40×6 is smaller than the chrome and an operator really does it. What it shows is allowed to be
// almost nothing; what it may not do is panic, draw outside itself, or STAY degraded when the
// window comes back — a hub that keeps rendering for 40 columns on a 120-column terminal is the
// failure that only a resize can find, because a program started at 120 never sees it.
func TestE2EUIResizeDownToATinyTerminalAndBackUpRecovers(t *testing.T) {
	ui, panes := uiResizeHub(t)

	uiResizeTo(t, ui, 40, 6)
	tiny := uiResizeLines(t, ui)
	if len(tiny) > 6 {
		t.Errorf("the hub drew %d lines on a 6-row terminal:\n%s", len(tiny), strings.Join(tiny, "\n"))
	}
	for i, l := range tiny {
		if r := len([]rune(l)); r > 40 {
			t.Errorf("at 40 columns line %d is %d runes wide: %q", i+1, r, l)
		}
	}
	joined := strings.Join(tiny, "\n")
	if strings.Contains(joined, "panic") || strings.Contains(joined, "goroutine") {
		t.Fatalf("the hub panicked at 40x6:\n%s", joined)
	}
	if !strings.HasPrefix(tiny[0], "tmux-hub") {
		t.Errorf("at 40x6 the top line is %q — even the smallest screen has to say what it "+
			"is:\n%s", tiny[0], joined)
	}
	tinyHosts := len(uiResizeNamedHosts(uiResizeFooter(t, ui)))

	// Back up to a width in the grouped band, which is also a band CHANGE from the tiny screen.
	uiResizeTo(t, ui, 120, 30)
	back := uiResizeLines(t, ui)
	joined = strings.Join(back, "\n")
	if len(back) <= len(tiny) {
		t.Errorf("growing from 40x6 to 120x30 left the hub drawing %d lines, no more than the "+
			"%d it drew when tiny — the screen stayed degraded after the window came back:\n%s",
			len(back), len(tiny), joined)
	}
	if !strings.Contains(joined, strings.ToUpper(uiResizeFleet[0])+" WATCHED") {
		t.Errorf("after growing back to 120 columns the grouped header is missing, so the hub is "+
			"still rendering the narrow layout on a wide terminal:\n%s", joined)
	}
	if !strings.Contains(joined, panes[1]) {
		t.Errorf("after growing back to 120x30 pane %s is not on the screen, so rows that fit "+
			"were not restored:\n%s", panes[1], joined)
	}
	if n := len(uiResizeNamedHosts(uiResizeFooter(t, ui))); n <= tinyHosts {
		t.Errorf("the footer names %d hosts at 120 columns and named %d at 40 — growing the "+
			"window bought the operator nothing", n, tinyHosts)
	}

	// And the runtime survived all of it: the quit path is the proof there is no wedged program
	// behind a screen that merely looks right.
	send(t, ui, "q")
	screenHas(t, ui, "EXITED-rc=0",
		"the hub must still quit cleanly after being resized down to 40x6 and back")
}
