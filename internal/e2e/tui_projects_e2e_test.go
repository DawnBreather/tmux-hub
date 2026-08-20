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

// The project list and the filter, driven through the real binary in a real terminal.
//
// What these cases can see that an in-process test cannot: the SCREEN an operator reads after a
// keystroke, over a fleet whose paths came from tmux's own `#{pane_current_path}` rather than from a
// hand-built `registry.Pane`. Every unit test of the grouping sets `Pane.Path` itself, so all of them
// would pass with the label unfetched and the field permanently empty — this repository's own
// recorded defect, twice over (`#{pane_start_command}` in the label table nobody read, and
// `UpdateAgents` forgetting `s.CWD` while hashing it into the row id).
//
// ── why this group brings its own hub helper ───────────────────────────────────────────────────────
//
// `hubWith` puts every watched pane in ONE directory, and the derived grouping with no projects.toml
// is each pane's cwd's last segment — so that fleet has exactly one project and nothing to walk
// between. These cases need at least two, which means panes started with different `-c`.
//
// The second reason is DETERMINISM, and it is the part that cost the measurements. With the
// operator's own HOME the hub also lists their Claude sessions, each row carrying its own working
// directory, so the project list holds their projects as well as the fixture's: `2 projects` becomes
// however many they happen to have open, and `tab`'s "next project" is a stranger's. So the hub here
// runs under an ISOLATED HOME.
//
// That is not free, and the failure is worth writing down because the existing harness attributes
// the almost-empty fleet under an isolated home to `~/.claude` alone. Measured: with `HOME` moved and
// PATH untouched, the watched host reads `scratch down` with
// `list-panes rc=1: mise ERROR tmux is not a valid shim` — the pane's PATH leads with mise's shim
// directory, and a shim cannot resolve its tool once HOME moves. The fleet is not "almost nothing"
// there, it is NOTHING, and a case asserting on rows would have read a broken transport as a product
// defect. So the hub is given a PATH holding one symlink to the tmux binary this test process itself
// resolved. Leaving `claude` off that PATH is deliberate and is what makes the fleet exactly the
// fixture: the footer then says `scratch up (agents: claude is not installed here)`, which is the
// honest thing for it to say.

// projectsFleet is the fixture these cases share: three panes in two directories, one of them
// waiting on the operator.
//
// `alpha` holds two panes and `beta` one, so the two rows of the project list cannot be confused by
// their counts (`of 2` against `of 1`). The first alpha pane prints `Do you want to proceed?` and
// then reads, which `state.Classify` answers with `Needs` from the captured zone — so the attention
// cell has something to say, and the list's own ordering rule (waiting first) has a reason to put
// alpha above beta.
type projectsFleet struct {
	ui     string              // the socket the hub is DISPLAYED on
	target string              // the socket the hub WATCHES
	panes  []string            // pane ids: [0] and [1] in alpha, [2] in beta
	dirs   map[string][]string // label -> the pane ids that project holds
}

// panesOf is the fixture's own answer to "which panes does this project hold", so a case never has
// to hard-code the mapping twice.
func (f projectsFleet) panesOf(label string) []string { return f.dirs[label] }

// others returns every pane id NOT in the named project, which is the half of "narrows to one
// project" that a positive assertion cannot make.
func (f projectsFleet) others(label string) []string {
	keep := map[string]bool{}
	for _, id := range f.dirs[label] {
		keep[id] = true
	}
	var out []string
	for _, id := range f.panes {
		if !keep[id] {
			out = append(out, id)
		}
	}
	return out
}

func projectsHub(t *testing.T, cols, rows int) projectsFleet {
	t.Helper()
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	bin := buildBinary(t)
	work := t.TempDir()
	f := projectsFleet{
		ui:     filepath.Join(work, "ui.sock"),
		target: filepath.Join(work, "target.sock"),
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", f.target, "kill-server").Run()
		_ = exec.Command("tmux", "-S", f.ui, "kill-server").Run()
	})

	run := func(socket string, args ...string) string {
		t.Helper()
		full := append([]string{"-S", socket, "-f", "/dev/null"}, args...)
		out, err := exec.Command("tmux", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	alpha := filepath.Join(work, "alpha")
	beta := filepath.Join(work, "beta")
	// An isolated HOME for the hub, and a PATH with nothing on it but tmux — see the file comment.
	home := filepath.Join(work, "home")
	cfg := filepath.Join(work, "cfg")
	binDir := filepath.Join(work, "bin")
	for _, d := range []string{alpha, beta, home, cfg, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(tmuxPath, filepath.Join(binDir, "tmux")); err != nil {
		t.Fatal(err)
	}

	// The waiting pane. `sh -c` rather than a bare echo, because the pane has to keep READING after
	// it prints: a pane whose command exits takes its window with it and the fixture loses a third
	// of the fleet before the first poll.
	f.panes = append(f.panes, run(f.target, "new-session", "-d", "-s", "one", "-c", alpha,
		"-P", "-F", "#{pane_id}", `sh -c "echo Do you want to proceed?; cat"`))
	f.panes = append(f.panes, run(f.target, "new-window", "-t", "one", "-c", alpha,
		"-P", "-F", "#{pane_id}", "cat"))
	f.panes = append(f.panes, run(f.target, "new-window", "-t", "one", "-c", beta,
		"-P", "-F", "#{pane_id}", "cat"))
	f.dirs = map[string][]string{
		"alpha": {f.panes[0], f.panes[1]},
		"beta":  {f.panes[2]},
	}

	// A hosts file that has DECIDED something (one disabled entry), because a file that has decided
	// nothing opens the picker at startup and it would swallow every keystroke these cases send.
	hosts := filepath.Join(work, "hosts.toml")
	if err := os.WriteFile(hosts,
		[]byte("[[host]]\nalias = \"nothing\"\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	launch := fmt.Sprintf("HOME=%s XDG_CONFIG_HOME=%s XDG_STATE_HOME=%s PATH=%s "+
		"%s --hosts %s --no-local --host scratch=%s,local --hidden %s --view=flat --no-history; "+
		"echo EXITED-rc=$?; sleep 60",
		home, cfg, cfg, binDir, bin, hosts, f.target, filepath.Join(work, "hidden.json"))
	run(f.ui, "new-session", "-d", "-s", "ui", "-x", fmt.Sprint(cols), "-y", fmt.Sprint(rows),
		"-c", work, launch)

	// Wait for the SIGNAL these cases are about: every watched pane on the screen AND the waiting
	// one classified. The header paints before any poll completes (§16 promises that), so waiting on
	// it races every assertion here — and the attention cell needs the classification, which arrives
	// with the first captured zone rather than with the pane list.
	deadline := time.Now().Add(40 * time.Second)
	for {
		screen, err := paneScreen(t, f.ui, "ui")
		if err == nil {
			ok := strings.Contains(screen, "needs")
			for _, id := range f.panes {
				ok = ok && strings.Contains(screen, id)
			}
			if ok {
				break
			}
			if strings.Contains(screen, "not a valid shim") {
				t.Skip("the hub's tmux could not run under an isolated HOME (a mise shim on PATH " +
					"resolves its tool through HOME), so the fixture's fleet never arrived: " + screen)
			}
		}
		if time.Now().After(deadline) {
			s, _ := paneScreen(t, f.ui, "ui")
			t.Fatalf("the fixture's three panes and its waiting row never reached the screen, so no "+
				"case below is testing the product:\n%s", s)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return f
}

// projectsListRow returns the project list's row for one label, identified by its LABEL COLUMN
// rather than by a match anywhere on the screen.
//
// `strings.Contains` over a whole screen cannot test a per-row property: the note line, the footer
// and a second project's row all satisfy it. The row is the line whose first word after the cursor
// marker is the label and which carries an `of N` cell, which is the shape RenderProjects draws.
func projectsListRow(t *testing.T, ui, label string) string {
	t.Helper()
	for _, line := range strings.Split(capturePane(t, ui, "ui"), "\n") {
		body := strings.TrimSpace(strings.TrimPrefix(line, "❯ "))
		if !strings.Contains(body, "of ") {
			continue
		}
		if body == label || strings.HasPrefix(body, label+" ") {
			return strings.TrimRight(line, " ")
		}
	}
	return ""
}

// projectsWaitListRow waits for one project's row to carry what a producer has to supply — the
// attention roll-up arrives with the classification, not with the keystroke.
func projectsWaitListRow(t *testing.T, ui, label, want, why string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		row := projectsListRow(t, ui, label)
		if strings.Contains(row, want) {
			return row
		}
		if time.Now().After(deadline) {
			t.Fatalf("the %q row never showed %q — %s\nrow: %q\n%s",
				label, want, why, row, capturePane(t, ui, "ui"))
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// projectsListOrder reads the labels the list shows, in the order it shows them.
//
// Derived rather than assumed, and that is the point: `tab` claims to walk the list's order, so a
// case that hard-codes the order cannot tell "tab walks it" from "tab happens to agree with my
// guess". Only the labels this fixture owns are returned, so a stray row could never be walked to.
func projectsListOrder(t *testing.T, ui string, f projectsFleet) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(capturePane(t, ui, "ui"), "\n") {
		body := strings.TrimSpace(strings.TrimPrefix(line, "❯ "))
		if !strings.Contains(body, "of ") {
			continue
		}
		for label := range f.dirs {
			if body == label || strings.HasPrefix(body, label+" ") {
				out = append(out, label)
			}
		}
	}
	return out
}

// projectsWalkList moves the list cursor with `j` until the marked row is the wanted label.
func projectsWalkList(t *testing.T, ui, label string) {
	t.Helper()
	for i := 0; i < 20; i++ {
		for _, line := range strings.Split(capturePane(t, ui, "ui"), "\n") {
			if !strings.HasPrefix(line, "❯ ") {
				continue
			}
			body := strings.TrimSpace(strings.TrimPrefix(line, "❯ "))
			if body == label || strings.HasPrefix(body, label+" ") {
				return
			}
		}
		send(t, ui, "j")
		time.Sleep(120 * time.Millisecond)
	}
	t.Fatalf("the list cursor never reached %q in 20 presses\n%s", label, capturePane(t, ui, "ui"))
}

// projectsWaitFrame waits for ONE frame that satisfies the whole claim: everything in `want`
// present and everything in `unwanted` absent, in the same capture.
//
// One capture, not two, because the tick repaints under the assertion: a positive read followed by a
// separate negative read can be satisfied by two different frames, and a narrowing that showed the
// right rows one tick and the wrong ones the next would pass.
func projectsWaitFrame(t *testing.T, ui string, want, unwanted []string, why string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last, missing, leaked string
	for {
		screen, err := paneScreen(t, ui, "ui")
		if err == nil {
			last = screen
			missing, leaked = "", ""
			for _, w := range want {
				if !strings.Contains(screen, w) {
					missing = w
					break
				}
			}
			for _, u := range unwanted {
				if strings.Contains(screen, u) {
					leaked = u
					break
				}
			}
			if missing == "" && leaked == "" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no frame satisfied the claim — %s\nmissing: %q  ·  should not be there: %q\n%s",
				why, missing, leaked, last)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// The list header text, which is also the marker for "am I still on the list".
const projectsListHeader = "enter narrows, esc goes back"

// ── `P`: the list, and what each row says about its project ───────────────────────────────────────

// `P` opens the list, and each row says how much of that project wants the operator.
//
// The counts are the whole product here: a list of names the operator already knows tells them
// nothing, and the reason this screen exists is that a globally sorted dashboard buries the answer.
// So the assertion is per ROW and on the CELL — `⚑ 1  of 2` for the project holding the waiting
// pane, `of 1` and no flag for the one that does not.
func TestE2EUIProjectsPOpensTheListAndEachRowRollsUpItsAttention(t *testing.T) {
	f := projectsHub(t, 120, 40)

	send(t, f.ui, "P")
	screenHas(t, f.ui, "2 projects",
		"`P` must open the project list and count the projects — the fixture's panes sit in two "+
			"directories, and with no projects.toml that is two projects")
	screenHas(t, f.ui, projectsListHeader,
		"the list must say what enter and esc do, or the operator has to guess whether opening it "+
			"has already narrowed their fleet")

	// The waiting project. The count arrives with the classification, so wait for it.
	alpha := projectsWaitListRow(t, f.ui, "alpha", "of 2",
		"a row that does not say how many sessions the project holds is a name the operator "+
			"already knew")
	if !strings.Contains(alpha, "⚑ 1") {
		t.Errorf("the alpha row does not say one of its two sessions is waiting on the operator — "+
			"which is the only fact that makes this screen worth opening:\n%s\n%s",
			alpha, capturePane(t, f.ui, "ui"))
	}

	// And the project with nothing waiting must not claim otherwise: a flag on a quiet project
	// sends the operator to a session that does not want them.
	beta := projectsListRow(t, f.ui, "beta")
	if !strings.Contains(beta, "of 1") {
		t.Errorf("the beta row does not say it holds one session: %q\n%s",
			beta, capturePane(t, f.ui, "ui"))
	}
	if strings.Contains(beta, "⚑") {
		t.Errorf("the beta row flags a waiting session and nothing in it is waiting: %q — a false "+
			"flag costs more than a missing one, because the operator acts on it", beta)
	}
}

// ── `enter`: narrowing, in both directions ────────────────────────────────────────────────────────

// `enter` narrows the dashboard to one project: the rows inside it stay and the rows outside LEAVE.
//
// Both halves in one frame. A narrowing that keeps the right rows and also keeps the wrong ones is
// the failure that reads as success — the operator sees their project's sessions and believes the
// screen is answering about their project.
func TestE2EUIProjectsEnterNarrowsTheDashboardToOneProject(t *testing.T) {
	f := projectsHub(t, 120, 40)

	send(t, f.ui, "P")
	screenHas(t, f.ui, "2 projects", "`P` must open the project list")
	projectsWalkList(t, f.ui, "beta")
	send(t, f.ui, "Enter")

	// `beta` holds exactly one of the three panes, so the header count is a second, independent
	// witness: it is computed from the rows the screen draws.
	want := append([]string{"tmux-hub  1 session"}, f.panesOf("beta")...)
	projectsWaitFrame(t, f.ui, want, f.others("beta"),
		"`enter` on a project must leave the operator looking at that project alone — the rows "+
			"outside it staying on screen is the failure that looks like success")

	// And it is the DASHBOARD the operator is left on, not the list: `enter` is a doorway, so every
	// dashboard key keeps its meaning inside a project.
	if s := capturePane(t, f.ui, "ui"); strings.Contains(s, projectsListHeader) {
		t.Errorf("`enter` narrowed but left the operator on the list, so the project they chose "+
			"cannot be acted on without another keystroke:\n%s", s)
	}
}

// ── `esc`: widening ───────────────────────────────────────────────────────────────────────────────

// `esc` widens again and the rows come back.
//
// The rows COMING BACK is the assertion, and it is not the same as the filter flag clearing: a
// widen that clears the filter while the row set is rebuilt from a stale snapshot shows the
// project's rows on a screen that claims to hold the fleet.
func TestE2EUIProjectsEscWidensBackToTheWholeFleet(t *testing.T) {
	f := projectsHub(t, 120, 40)

	send(t, f.ui, "P")
	screenHas(t, f.ui, "2 projects", "`P` must open the project list")
	projectsWalkList(t, f.ui, "beta")
	send(t, f.ui, "Enter")
	projectsWaitFrame(t, f.ui, append([]string{"tmux-hub  1 session"}, f.panesOf("beta")...),
		f.others("beta"), "the case needs a narrowed screen before it can widen one")

	send(t, f.ui, "Escape")
	projectsWaitFrame(t, f.ui, append([]string{"tmux-hub  3 sessions"}, f.panes...), nil,
		"`esc` must widen the fleet back — an operator who narrowed to one project and cannot get "+
			"the rest back has lost every session they were not looking at")
}

// ── `tab`: the walk the list exists to start ──────────────────────────────────────────────────────

// `tab` walks to the next project WITHOUT going back to the list.
//
// The order is read off the list rather than assumed, so this case cannot pass by agreeing with a
// guess: `tab`'s claim is that it walks the order the list shows, and the list is asked what that is.
func TestE2EUIProjectsTabWalksToTheNextProjectWithoutTheList(t *testing.T) {
	f := projectsHub(t, 120, 40)

	send(t, f.ui, "P")
	screenHas(t, f.ui, "2 projects", "`P` must open the project list")
	projectsWaitListRow(t, f.ui, "alpha", "of 2", "the roll-up has to have arrived before the "+
		"order it produces can be read")
	order := projectsListOrder(t, f.ui, f)
	if len(order) != 2 {
		t.Fatalf("the list shows %v of this fixture's projects, want both — the walk below would "+
			"be checked against an order nobody read\n%s", order, capturePane(t, f.ui, "ui"))
	}
	// Leaving the list must decide nothing, which is also what puts this case at "no filter".
	send(t, f.ui, "Escape")
	projectsWaitFrame(t, f.ui, append([]string{"tmux-hub  3 sessions"}, f.panes...), nil,
		"esc on the list must leave it without narrowing anything")

	send(t, f.ui, "tab")
	projectsWaitFrame(t, f.ui, f.panesOf(order[0]), f.others(order[0]),
		"`tab` with no project selected must open the FIRST one the list shows — a key that reads "+
			"as \"next\" and does nothing at the start is indistinguishable from a broken key")
	if s := capturePane(t, f.ui, "ui"); strings.Contains(s, projectsListHeader) {
		t.Errorf("`tab` threw the operator back onto the project list; the walk exists so they do "+
			"not have to go through it:\n%s", s)
	}

	send(t, f.ui, "tab")
	projectsWaitFrame(t, f.ui, f.panesOf(order[1]), f.others(order[1]),
		"a second `tab` must move to the next project in the list's own order")
	if s := capturePane(t, f.ui, "ui"); strings.Contains(s, projectsListHeader) {
		t.Errorf("the second `tab` opened the list:\n%s", s)
	}
}

// `tab` cycles at the end rather than stopping.
//
// Its own case because the boundary is where the two designs differ: a `tab` that stops silently on
// the last project looks exactly like a `tab` that has broken, and there is no third project here to
// tell them apart from the middle of the walk.
func TestE2EUIProjectsTabCyclesAtTheLastProject(t *testing.T) {
	f := projectsHub(t, 120, 40)

	send(t, f.ui, "P")
	screenHas(t, f.ui, "2 projects", "`P` must open the project list")
	projectsWaitListRow(t, f.ui, "alpha", "of 2", "the roll-up has to have arrived before the "+
		"order it produces can be read")
	order := projectsListOrder(t, f.ui, f)
	if len(order) != 2 {
		t.Fatalf("the list shows %v of this fixture's projects, want both\n%s",
			order, capturePane(t, f.ui, "ui"))
	}
	send(t, f.ui, "Escape")
	projectsWaitFrame(t, f.ui, append([]string{"tmux-hub  3 sessions"}, f.panes...), nil,
		"the walk starts from an unfiltered dashboard")

	// Walk to the LAST project, then once more.
	send(t, f.ui, "tab")
	projectsWaitFrame(t, f.ui, f.panesOf(order[0]), f.others(order[0]),
		"the first `tab` must open the first project")
	send(t, f.ui, "tab")
	projectsWaitFrame(t, f.ui, f.panesOf(order[1]), f.others(order[1]),
		"the second `tab` must reach the last project, which is where the cycle is tested")

	send(t, f.ui, "tab")
	projectsWaitFrame(t, f.ui, f.panesOf(order[0]), f.others(order[0]),
		"`tab` past the last project must come back to the first — stopping there leaves the "+
			"operator pressing a key that answers nothing, and an empty screen would mean the "+
			"filter is pointing at a project that is not in the list")
}

// ── the keys whose subject is a SESSION, pressed where there is none ───────────────────────────────

// A key that acts on a session says so on the list instead of doing nothing, and NAMES ITSELF.
//
// The README states this explicitly, and "instead of doing nothing" is the load-bearing half: a
// silent no-op is what a broken key looks like, and this repository has shipped that shape before —
// a cursor left past the end of a shorter listing, where every key silently did nothing.
//
// Each key is sent ALONE. A run injected in one `send-keys -l` arrives as ONE key message whose
// String() is the whole run, so a per-key rule would not be exercised at all.
func TestE2EUIProjectsASessionKeyOnTheListSaysSoAndNamesItself(t *testing.T) {
	f := projectsHub(t, 120, 40)

	send(t, f.ui, "P")
	screenHas(t, f.ui, "2 projects", "`P` must open the project list")

	for _, key := range []string{"i", "R", "K", "x"} {
		typeOneAtATime(t, f.ui, key)
		screenHas(t, f.ui, key+" acts on a session",
			"`"+key+"` on the project list must name itself and say its subject is missing — an "+
				"operator who presses it and sees nothing cannot tell the key from a broken hub")
		screenHas(t, f.ui, "press enter to open a project first",
			"the refusal must name the way forward, not only refuse")
		// The screen must not have MOVED: a refusal that also leaves the list is a second surprise.
		if s := capturePane(t, f.ui, "ui"); !strings.Contains(s, projectsListHeader) {
			t.Fatalf("`%s` refused and still left the project list:\n%s", key, s)
		}
	}

	// And `x` in particular refused rather than acted. It is the one key on that list whose real
	// effect is invisible until the operator looks for a row that is gone, so the negative is worth
	// its own assertion: nothing may be hidden.
	if s := capturePane(t, f.ui, "ui"); strings.Contains(s, "hidden") {
		t.Errorf("`x` on the project list hid something — the row it took away is the one thing an "+
			"operator cannot notice:\n%s", s)
	}
	send(t, f.ui, "Escape")
	projectsWaitFrame(t, f.ui, append([]string{"tmux-hub  3 sessions"}, f.panes...), nil,
		"after four refused keys the fleet must be exactly what it was — a refusal that hid, "+
			"killed or restarted something would show up here as a missing row")
}

// `space` on the project list refuses with a sentence that names NO key.
//
// PRODUCT DEFECT, and the skip carries the screen. `space` is in the same refusal arm as `i`, `R`,
// `K` and `x` — its subject on the dashboard is a pane, and a list of projects has none — but the
// note is built as `fmt.Sprintf("%s acts on a session — …", msg.String())` and a space key's
// String() is `" "`. Measured on the real screen at 120 columns:
//
//	··acts·on·a·session·—·press·enter·to·open·a·project·first·+1
//
// (cat -A of the captured line; the two leading columns are the note's own indent plus the key that
// was supposed to be named). Every other key in that arm prints its own character first, so the one
// key an operator presses most on the dashboard is the one whose refusal cannot be attributed to it.
// The remedy is the same one the picker's own key table uses: a display name per key, so `" "` reads
// as `space`.
func TestE2EUIProjectsSpaceOnTheListNamesTheKeyItRefused(t *testing.T) {
	f := projectsHub(t, 120, 40)
	send(t, f.ui, "P")
	screenHas(t, f.ui, "2 projects", "`P` must open the project list")

	send(t, f.ui, "Space")
	screenHas(t, f.ui, "space acts on a session",
		"`space` on the project list must name itself the way every other refused key does — the "+
			"key an operator presses most often is the one whose refusal must be attributable")
}
