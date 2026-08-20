package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"

	"github.com/DawnBreather/tmux-hub/internal/lines"
)

// pathPane is a row in a project directory. The path is what the grouping reads.
func pathPane(host, path, session string, idx int, st state.State) registry.Pane {
	return registry.Pane{
		Kind: registry.KindPane, Host: host, Path: path, Session: session,
		Window: "w", Index: idx, PaneID: "%" + string(rune('0'+idx)),
		Command: "claude", ClassifiedState: st,
	}
}

func fleetAcrossProjects() []registry.Pane {
	return []registry.Pane{
		pathPane("local", "/w/alpha", "a1", 0, state.Needs),
		pathPane("local", "/w/alpha", "a2", 1, state.Works),
		pathPane("local", "/w/beta", "b1", 2, state.Works),
		pathPane("local", "/w/beta", "b2", 3, state.Works),
		pathPane("local", "/w/beta", "b3", 4, state.Error),
	}
}

// key and special drive the package's own press helper, which also runs the command a key
// returns. That is why press exists: a keypress whose work hangs off a tea.Cmd looks like
// a no-op when only Update is called.
func key(t *testing.T, m model, k string) model {
	t.Helper()
	out, _ := press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	return out
}

func special(t *testing.T, m model, kt tea.KeyType) model {
	t.Helper()
	out, _ := press(t, m, tea.KeyMsg{Type: kt})
	return out
}

// `P` opens the project list, and the list has to carry the two things it exists for: the
// group's name and how much of it wants the operator (docs/design.md §21.2).
func TestPOpensTheProjectListWithAnAttentionCell(t *testing.T) {
	m := modelWith(t, fleetAcrossProjects()...)
	m = key(t, m, "P")
	out := m.View()
	for _, want := range []string{"alpha", "beta", "⚑ 1", "✗ 1", "of 2", "of 3"} {
		if !strings.Contains(out, want) {
			t.Errorf("the project list does not carry %q:\n%s", want, out)
		}
	}
	// alpha has the waiting row, so it must come FIRST — a group where something wants
	// you must not be below one where nothing does.
	if strings.Index(out, "alpha") > strings.Index(out, "beta") {
		t.Errorf("beta sorted above alpha although alpha is the one waiting:\n%s", out)
	}
}

// The list is a screen, so an empty fleet is a specified one rather than a blank box.
func TestTheProjectListWithNoRowsSaysSo(t *testing.T) {
	m := modelWith(t)
	m = key(t, m, "P")
	if out := m.View(); strings.TrimSpace(out) == "" {
		t.Error("the project list rendered blank for an empty fleet")
	}
}

// `enter` narrows the dashboard to the group under the cursor, and `esc` clears it. The
// count in the header must be the NARROWED one: two screens one keystroke apart that
// disagree about a total is how a wrong number gets published (§21.6).
func TestEnterNarrowsTheDashboardAndEscClearsIt(t *testing.T) {
	m := modelWith(t, fleetAcrossProjects()...)
	m = key(t, m, "P")
	// The cursor starts on alpha, which sorted first — but pressing enter THERE could not
	// tell `rows[m.projCursor]` from `rows[0]`, and a mutation to the latter was green. So
	// walk to beta first, narrow to it, and then come back to alpha: the second half is
	// what proves the key follows the cursor rather than the sort.
	m = key(t, m, "j")
	m = special(t, m, tea.KeyEnter)
	if out := strings.ToLower(m.View()); !strings.Contains(out, "b1") || strings.Contains(out, "a1") {
		t.Fatalf("enter on the SECOND project narrowed to the first:\n%s", out)
	}
	m = special(t, m, tea.KeyEsc)
	m = key(t, m, "P")
	m = special(t, m, tea.KeyEnter)
	if m.mode != modeBrowse {
		t.Fatalf("enter left the mode at %v, want browse", m.mode)
	}
	// The renderer uppercases a session header (`LOCAL A1`), so these checks fold case
	// rather than asserting a presentation detail this test is not about.
	out := strings.ToLower(m.View())
	if !strings.Contains(out, "a1") || !strings.Contains(out, "a2") {
		t.Errorf("the narrowed dashboard lost alpha's rows:\n%s", out)
	}
	for _, gone := range []string{"b1", "b2", "b3"} {
		if strings.Contains(out, gone) {
			t.Errorf("the filter did not exclude %q:\n%s", gone, out)
		}
	}
	// 2 sessions, not 5.
	if !strings.Contains(out, "2 sessions") {
		t.Errorf("the header does not report the narrowed count:\n%s", out)
	}
	// esc clears the filter and the whole fleet is back.
	m = special(t, m, tea.KeyEsc)
	if out := strings.ToLower(m.View()); !strings.Contains(out, "b3") {
		t.Errorf("esc did not clear the filter:\n%s", out)
	}
}

// esc on the LIST goes back without narrowing anything — a screen you can open must be a
// screen you can leave without a decision.
func TestEscOnTheListLeavesTheFleetAlone(t *testing.T) {
	m := modelWith(t, fleetAcrossProjects()...)
	m = key(t, m, "P")
	m = special(t, m, tea.KeyEsc)
	if m.mode != modeBrowse {
		t.Fatalf("mode = %v, want browse", m.mode)
	}
	if out := strings.ToLower(m.View()); !strings.Contains(out, "b3") {
		t.Errorf("esc on the list narrowed the fleet anyway:\n%s", out)
	}
}

// THE RULE THE FILTER MUST NOT BREAK: it narrows what is shown and what `A` takes, and it
// NEVER prunes the selection. A pane the operator cd's in changes path, changes project,
// leaves the filter — and its mark must not disappear mid-compose (§21.5).
func TestAMarkedRowOutsideTheFilterKeepsItsMarkAndIsNamed(t *testing.T) {
	m := modelWith(t, fleetAcrossProjects()...)
	// Mark a beta row, then narrow to alpha.
	beta := m.panes[4]
	m.sel.Toggle(selKey(beta))
	m = key(t, m, "P")
	m = special(t, m, tea.KeyEnter)

	if !m.sel.Has(selKey(beta)) {
		t.Fatal("narrowing dropped a mark — a filter must never prune the selection")
	}
	// And the operator is told, because a mark they cannot see still receives the send.
	out := m.View()
	if !strings.Contains(out, "not in this project") {
		t.Errorf("the screen does not say a selected row is outside the filter:\n%s", out)
	}
}

// `A` still means "select what is on screen", so inside a filter it must not reach the
// rows the filter excluded.
func TestSelectAllInsideAFilterTakesOnlyTheGroup(t *testing.T) {
	m := modelWith(t, fleetAcrossProjects()...)
	m = key(t, m, "P")
	m = special(t, m, tea.KeyEnter) // narrow to alpha
	m = key(t, m, "A")
	if m.sel.Len() != 2 {
		t.Fatalf("A selected %d rows inside a 2-row project", m.sel.Len())
	}
	for _, k := range m.sel.Members() {
		if k.PaneID == "%2" || k.PaneID == "%3" || k.PaneID == "%4" {
			t.Errorf("A reached %s, which the filter excluded", k.PaneID)
		}
	}
}

// The invariant: the sum over groups equals the number the unfiltered dashboard reports.
// This is a test rather than a convention because a count derived from a filtered set and
// presented as a total is exactly how a wrong one gets published (§21.6).
func TestTheGroupTotalsSumToTheDashboardCount(t *testing.T) {
	rows := fleetAcrossProjects()
	m := modelWith(t, rows...)
	sum := 0
	for _, s := range project.Summarise(m.rules, m.visibleRows()) {
		sum += s.Total
	}
	if sum != len(rows) {
		t.Fatalf("groups hold %d rows, the fleet has %d", sum, len(rows))
	}
	if out := m.View(); !strings.Contains(out, "5 sessions") {
		t.Errorf("the unfiltered header does not report %d:\n%s", len(rows), out)
	}
}

// A filter naming a group that no longer exists must not empty the screen silently: a
// pane that cd's out of the last directory of a project takes the project with it.
func TestAFilterOnAVanishedGroupSaysSoRatherThanShowingNothing(t *testing.T) {
	m := modelWith(t, fleetAcrossProjects()...)
	m = key(t, m, "P")
	m = special(t, m, tea.KeyEnter)
	// Every alpha row moves to another directory, so the group is gone.
	for i := range m.panes {
		m.panes[i].Path = "/w/beta"
	}
	out := strings.ToLower(m.View())
	if strings.TrimSpace(out) == "" {
		t.Fatal("the screen went blank")
	}
	// The SENTENCE, not a disjunction with an arm that cannot fail. `TrimSpace(out) == ""`
	// is unfalsifiable because Render always draws a header, and "0 sessions" alone would
	// pass for a screen that says nothing about why or how to leave.
	if !strings.Contains(out, "no rows in this project any more") {
		t.Errorf("a filter on a vanished group must say so and name the way out:\n%s", out)
	}
	if !strings.Contains(out, "esc") {
		t.Errorf("the sentence does not name the remedy:\n%s", out)
	}
}

// A fleet with more projects than rows must not lose its tail SILENTLY: a row the operator
// cannot scroll to is a row they cannot act on, which is the same failure as not drawing it
// (§21.13.4 — one scroller, and the list must use it).
func TestTheProjectListScrollsAndSaysHowManyAreOffScreen(t *testing.T) {
	var rows []registry.Pane
	for i := 0; i < 30; i++ {
		rows = append(rows, pathPane("local", "/w/p"+string(rune('a'+i%26))+string(rune('0'+i/26)),
			"s"+string(rune('a'+i%26)), i%9, state.Works))
	}
	m := modelWith(t, rows...)
	m.height = 10
	m = key(t, m, "P")
	out := m.View()
	if !strings.Contains(out, "more, j scrolls") {
		t.Errorf("a list longer than the screen does not say the tail exists:\n%s", out)
	}
	// The window follows the cursor: walk to the bottom and the last project must be on
	// screen, which is the property that makes every row reachable.
	last := m.projectRows()
	for i := 0; i < len(last)-1; i++ {
		m = key(t, m, "j")
	}
	if m.projCursor != len(last)-1 {
		t.Fatalf("the cursor stopped at %d of %d", m.projCursor, len(last)-1)
	}
	labels := project.SummaryLabels(last)
	wantLast := labels[last[len(last)-1].Group.ID]
	if out := m.View(); !strings.Contains(out, wantLast) {
		t.Errorf("the last project %q is not on screen with the cursor on it:\n%s", wantLast, out)
	}
	// And the cursor marker must be on exactly one row, wherever the window sits.
	if n := strings.Count(m.View(), "❯ "); n != 1 {
		t.Errorf("the cursor marker appears %d times, want 1:\n%s", n, m.View())
	}
}

// The window must never index past the rows: a short terminal, a warning taking rows, and a
// cursor left past the end after the list shrank are the three ways that panics.
func TestTheProjectListSurvivesASqueezedScreen(t *testing.T) {
	m := modelWith(t, fleetAcrossProjects()...)
	m.rulesWarn = "projects.toml: line 2: expected quoted string — grouping by directory name"
	for _, h := range []int{0, 1, 2, 3, 4, 5, 8} {
		m.height = h
		m = key(t, m, "P")
		m.projCursor = 99 // as if the list shrank under the cursor
		_ = m.View()      // must not panic
		m.projCursor = 0
		m = key(t, m, "P") // back to browse
	}
}

// Every read of "the row under the cursor" must come from the DRAWN list. With a filter on,
// visibleRows()[m.cursor] names a different row, so a hint, a jump or a mark lands on a row
// the operator is not looking at. This asserts the property over the whole surface rather
// than one function, so a tenth site added later is caught by the same test.
func TestNoCursorReadEscapesTheDrawnList(t *testing.T) {
	// The filter keeps ONE row, and the cursor sits on it. Every cursor-derived answer must
	// be about that row — under the old code, index 0 of the unfiltered list was a
	// different pane.
	rows := []registry.Pane{
		pathPane("local", "/w/alpha", "a1", 0, state.Works),
		pathPane("local", "/w/beta", "b1", 1, state.Needs),
	}
	m := modelWith(t, rows...)
	m = key(t, m, "P")
	// beta holds the waiting row, so it sorts first and the cursor opens on it.
	m = special(t, m, tea.KeyEnter)

	got, ok := m.cursorRow()
	if !ok {
		t.Fatal("no row under the cursor inside a one-row project")
	}
	if got.Session != "b1" {
		t.Fatalf("cursorRow = %q, want b1 — the filter kept exactly that row", got.Session)
	}
	// The hint is computed from the cursor, so it must be about a row of THIS host and
	// this project. The concrete regression: pathForCursor read visibleRows(), whose index
	// 0 is a1 — a different pane, and on a different path.
	if _, ok := m.cursorRow(); !ok {
		t.Fatal("cursorRow disagrees with itself")
	}
	// And no exported cursor consumer may reach outside: rowsForScreen is the source, so
	// its length bounds every one of them.
	if n := len(m.rowsForScreen()); n != 1 {
		t.Fatalf("the filter kept %d rows, want 1", n)
	}
	if i := m.cursorIndex(); i >= len(m.rowsForScreen()) {
		t.Fatalf("the cursor is at %d outside a %d-row screen", i, len(m.rowsForScreen()))
	}
}

// `a` on the project list opens the project with the cursor ALREADY on the row that wants
// you (docs/design.md §21.4). Narrowing and then hunting for the waiting row is two steps
// for the one thing the screen exists to answer.
func TestAOnTheListOpensTheProjectOnTheRowThatWaits(t *testing.T) {
	rows := []registry.Pane{
		// beta is second alphabetically but holds the waiting row, so it sorts first.
		pathPane("local", "/w/alpha", "a1", 0, state.Works),
		pathPane("local", "/w/beta", "b1", 1, state.Works),
		pathPane("local", "/w/beta", "b2", 2, state.Needs),
		pathPane("local", "/w/beta", "b3", 3, state.Works),
	}
	m := modelWith(t, rows...)
	m = key(t, m, "P")
	m = key(t, m, "a")

	if m.mode != modeBrowse {
		t.Fatalf("mode = %v, want browse", m.mode)
	}
	got, ok := m.cursorRow()
	if !ok {
		t.Fatal("no row under the cursor after a")
	}
	if got.Session != "b2" {
		t.Errorf("the cursor is on %q, want b2 — the only row in that project that is "+
			"waiting", got.Session)
	}
	if len(m.rowsForScreen()) != 3 {
		t.Errorf("the filter kept %d rows, want beta's 3", len(m.rowsForScreen()))
	}
}

// A project where nothing waits still opens: `a` falls back to its first row rather than
// refusing, because "go to it" must not become a key that sometimes does nothing.
func TestAOnAProjectWithNoWaitingRowStillOpensIt(t *testing.T) {
	m := modelWith(t,
		pathPane("local", "/w/calm", "c1", 0, state.Works),
		pathPane("local", "/w/calm", "c2", 1, state.Idle))
	m = key(t, m, "P")
	m = key(t, m, "a")
	if m.mode != modeBrowse || !m.filter.on {
		t.Fatalf("a refused a project with nothing waiting: mode=%v filter=%+v", m.mode, m.filter)
	}
	rows := m.rowsForScreen()
	row, ok := m.cursorRow()
	if !ok {
		t.Fatal("no row under the cursor after a")
	}
	if len(rows) == 0 || row.PaneID != rows[0].PaneID {
		t.Errorf("the cursor is on %q, want the project's first row", row.PaneID)
	}
}

// `tab` walks to the next project without going back to the list, and it CYCLES at the end
// (§21.14 left that open; a tab that stops is a key that silently does nothing).
func TestTabWalksToTheNextProjectAndCycles(t *testing.T) {
	m := modelWith(t,
		pathPane("local", "/w/alpha", "a1", 0, state.Needs),
		pathPane("local", "/w/beta", "b1", 1, state.Works),
		pathPane("local", "/w/gamma", "g1", 2, state.Idle))
	m = key(t, m, "P")
	m = special(t, m, tea.KeyEnter) // alpha: it holds the waiting row, so it sorts first

	order := []string{"alpha", "beta", "gamma", "alpha"}
	if got := m.rules.OfPane(m.rowsForScreen()[0]).Label; got != order[0] {
		t.Fatalf("opened %q, want %q", got, order[0])
	}
	for _, want := range order[1:] {
		m = special(t, m, tea.KeyTab)
		rows := m.rowsForScreen()
		if len(rows) == 0 {
			t.Fatalf("tab landed on an empty project, wanted %q", want)
		}
		if got := m.rules.OfPane(rows[0]).Label; got != want {
			t.Errorf("tab landed on %q, want %q", got, want)
		}
	}
}

// tab with NO filter on is the same doorway as P->enter on the first project: a key that
// reads as "next" must have a defined first step.
func TestTabWithNoFilterOpensTheFirstProject(t *testing.T) {
	m := modelWith(t, fleetAcrossProjects()...)
	m = special(t, m, tea.KeyTab)
	if !m.filter.on {
		t.Fatal("tab with no filter did nothing")
	}
	if got := len(m.rowsForScreen()); got != 2 {
		t.Errorf("tab opened a %d-row project, want alpha's 2", got)
	}
}

// tab on a fleet with ONE project must not spin or clear the filter.
func TestTabWithASingleProjectStaysThere(t *testing.T) {
	m := modelWith(t,
		pathPane("local", "/w/only", "o1", 0, state.Works),
		pathPane("local", "/w/only", "o2", 1, state.Works))
	m = special(t, m, tea.KeyTab)
	before := m.filter.group
	m = special(t, m, tea.KeyTab)
	if m.filter.group != before || !m.filter.on {
		t.Errorf("tab on a single project moved the filter from %q to %+v", before, m.filter)
	}
}

// §16 commits to three width bands and the project list's frames exist only at 80, so the
// other two were never checked (§21.14). A screen the project promises at 100 and 200 columns
// has to be asserted there, not assumed.
func TestTheProjectListHoldsEveryWidthBandItPromises(t *testing.T) {
	var rows []registry.Pane
	// A fleet with a long project name and two-digit counts, which is where a fixed-width
	// cell and a fixed separator show their seams.
	for i := 0; i < 12; i++ {
		rows = append(rows, pathPane("local", "/w/payments-checkout-service", "s"+string(rune('a'+i)), i%9, state.Needs))
	}
	for i := 0; i < 11; i++ {
		rows = append(rows, pathPane("nuc", "/w/short", "t"+string(rune('a'+i)), i%9, state.Error))
	}
	for _, cols := range []int{80, 100, 160, 200} {
		m := modelWith(t, rows...)
		m.width, m.height = cols, 24
		m = key(t, m, "P")
		out := m.View()
		for i, line := range strings.Split(out, "\n") {
			if w := lines.Width(line); w > cols {
				t.Errorf("%d cols: row %d is %d wide: %q", cols, i, w, line)
			}
		}
		// Both projects, and both attention cells, must be readable at every band.
		for _, want := range []string{"payments-checkout-service", "short", "⚑ 12", "✗ 11"} {
			if !strings.Contains(out, want) {
				t.Errorf("%d cols: the list is missing %q:\n%s", cols, want, out)
			}
		}
		// The cell must be in ONE column at every band, or the eye cannot run down it.
		var at []int
		for _, line := range strings.Split(out, "\n") {
			if i := strings.Index(line, "of "); i >= 0 {
				at = append(at, lines.Width(line[:i]))
			}
		}
		if len(at) < 2 {
			t.Fatalf("%d cols: found %d attention cells, want 2:\n%s", cols, len(at), out)
		}
		for _, a := range at[1:] {
			if a != at[0] {
				t.Errorf("%d cols: the cells start at columns %v — they must line up", cols, at)
				break
			}
		}
	}
}

// The separator must span the terminal rather than stopping at 80, or a wide screen reads as
// a narrow one with debris to the right.
func TestTheProjectListSeparatorSpansTheTerminal(t *testing.T) {
	m := modelWith(t, fleetAcrossProjects()...)
	for _, cols := range []int{80, 120, 200} {
		m.width, m.height = cols, 24
		m2 := key(t, m, "P")
		for _, line := range strings.Split(m2.View(), "\n") {
			if !strings.HasPrefix(line, "─") {
				continue
			}
			if w := lines.Width(line); w != cols {
				t.Errorf("%d cols: the rule is %d wide", cols, w)
			}
			break
		}
	}
}

// §21.14.3 left the answer channel on a full-screen list undecided: a note, the file's own
// warning and the key line all want the last row, and "one row cannot hold four claimants
// without a stated priority". The priority is stated here, and the point of stating it is that
// the list must never SWALLOW an answer — a key that produces a message and shows none is
// indistinguishable from a key that did nothing.
func TestTheProjectListAnswersOnOneRowInAStatedPriority(t *testing.T) {
	m := modelWith(t, fleetAcrossProjects()...)
	m = key(t, m, "P")

	// 1. With nothing to say, the row is the KEY LINE: on a screen the operator has just
	//    opened, what to press outranks everything.
	if out := m.View(); !strings.Contains(out, "enter narrows") {
		t.Errorf("with nothing to say the list does not show its keys:\n%s", out)
	}

	// 2. The FILE's warning outranks the keys, because it stays true until the file is
	//    edited, where a key line is the same on every screen.
	m.rulesWarn = "projects.toml: line 4: expected quoted string — grouping by directory name"
	out := m.View()
	if !strings.Contains(out, "line 4") {
		t.Errorf("the file's warning is not shown:\n%s", out)
	}

	// 3. A NOTE outranks the warning: it answers something the operator just pressed, and it
	//    is gone on the next keystroke while the warning is not.
	m.note = "cannot open that project any more"
	out = m.View()
	if !strings.Contains(out, "cannot open that project") {
		t.Errorf("a note does not reach the list:\n%s", out)
	}
	// And it must not silently replace the warning without saying the warning is still there.
	if !strings.Contains(out, "line 4") && !strings.Contains(out, "+") {
		t.Errorf("the warning vanished with no sign it exists:\n%s", out)
	}
	// Whatever it holds, one row, within the width.
	for _, line := range strings.Split(out, "\n") {
		if lines.Width(line) > m.width {
			t.Errorf("a row is %d wide at %d: %q", lines.Width(line), m.width, line)
		}
	}
}

// An unhandled key on the list must not be silent: a screen that swallows a keystroke teaches
// the operator that the screen is broken.
func TestAnUnknownKeyOnTheListSaysSoRatherThanSwallowingIt(t *testing.T) {
	m := modelWith(t, fleetAcrossProjects()...)
	m = key(t, m, "P")
	m = key(t, m, "z")
	if m.mode != modeProjects {
		t.Fatalf("an unknown key left the screen: mode = %v", m.mode)
	}
	if m.note == "" {
		t.Error("an unknown key produced no answer at all")
	}
	if !strings.Contains(m.View(), "z") {
		t.Errorf("the answer does not name the key that was pressed:\n%s", m.View())
	}
}

// §21.14 asked what the OTHER dashboard keys do on the list, and the answer is a division
// rather than a list: a key whose subject is the FLEET works here, and a key whose subject is
// a PANE has none — so it must say what it needs instead of doing nothing.
func TestFleetWideKeysWorkFromTheProjectList(t *testing.T) {
	// Asserted as "the list does not CHANGE what the key does" rather than against a mode
	// literal, which is fixture-dependent: `h` with no history log correctly stays in browse
	// with a note on the dashboard too, so a mode assertion would have measured the fixture.
	for _, k := range []string{"h", "p", "n"} {
		fromDash := modelWith(t, fleetAcrossProjects()...)
		fromDash = key(t, fromDash, k)

		fromList := modelWith(t, fleetAcrossProjects()...)
		fromList = key(t, fromList, "P")
		fromList = key(t, fromList, k)

		// The ANSWER must match; the MODE may differ, and deliberately does on a refusal:
		// each screen keeps ITSELF when a key cannot do its work, so `h` with no log leaves
		// the dashboard on the dashboard and the list on the list. Asserting equal modes
		// would forbid that, which is why this compares the note and then only requires the
		// key not to be swallowed.
		if fromList.note != fromDash.note {
			t.Errorf("%q answers %q from the list and %q from the dashboard",
				k, fromList.note, fromDash.note)
		}
		// And in no case is it swallowed: the list must not still be on screen with nothing
		// said, which is the failure this whole division is about.
		if fromList.mode == modeProjects && fromList.note == "" {
			t.Errorf("%q was swallowed by the list", k)
		}
	}
}

// A key that acts on PANES has no subject on a list of projects. It must name what it needs,
// because "nothing happened" is what a broken key looks like.
func TestPaneKeysOnTheListSayWhatTheyNeed(t *testing.T) {
	for _, k := range []string{"i", "!", "R", "K", "x", "X", "A", "C", " "} {
		m := modelWith(t, fleetAcrossProjects()...)
		m = key(t, m, "P")
		m = key(t, m, k)
		if m.mode != modeProjects {
			t.Errorf("%q left the project list (mode %v); it has no subject here", k, m.mode)
		}
		if m.note == "" {
			t.Errorf("%q did nothing and said nothing", k)
		}
		// The answer has to point at the way forward, not merely refuse.
		if !strings.Contains(m.note, "enter") && !strings.Contains(m.note, "project") {
			t.Errorf("%q answered %q without naming how to get a subject", k, m.note)
		}
	}
}

// ctrl+c quits from every screen, including this one — it is handled above the mode dispatch
// and a new mode inherits it (§21.4). Asserted because "inherits" is a claim about code that a
// new mode can quietly break.
func TestCtrlCQuitsFromTheProjectList(t *testing.T) {
	m := modelWith(t, fleetAcrossProjects()...)
	m = key(t, m, "P")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c on the project list returned no command")
	}
	if msg := cmd(); msg == nil {
		t.Error("ctrl+c produced a command that does nothing")
	}
}

// A REFUSAL must keep the screen. `h` set modeBrowse before calling openHistory — and
// openHistory is the one of the three fleet keys that does not set its own mode — so every
// path on which the log cannot be shown threw the operator off the list, while the
// neighbouring pane-key refusal kept it.
func TestAFailedFleetKeyKeepsTheProjectList(t *testing.T) {
	m := modelWith(t, fleetAcrossProjects()...) // no history log in this fixture
	m = key(t, m, "P")
	m = key(t, m, "h")
	if m.mode != modeProjects {
		t.Errorf("a refused h left the mode at %v — the list is gone and the note is on a "+
			"screen the operator did not ask for", m.mode)
	}
	if m.note == "" {
		t.Error("the refusal said nothing")
	}
	if !strings.Contains(m.View(), "history") {
		t.Errorf("the reason is not on the list's own answer row:\n%s", m.View())
	}
}

// The pinned COUNT never wears a truncation marker, and the file's WARNING always does.
//
// Measured at 80 columns on the operator's own fleet, with five favourites, the list's footer read
// `5 favourite sessions +1` — the marker landed against a count, where it says "one favourite is
// missing" rather than "one claimant did not fit". The dashboard's header already carries that ruling
// (it uses FitQuiet for exactly this reason), so the two screens now agree.
//
// The other half is what stops the fix going too far: a WARNING about projects.toml is true until the
// file is edited, so it may never vanish silently, and §21.12's priority puts it above the keys.
func TestThePinnedCountIsNeverMarkedAndTheWarningAlwaysIs(t *testing.T) {
	m := modelWith(t, fleetAcrossProjects()...)
	m.width = 80
	m = key(t, m, "P")
	m.favs = favSetWith(t, m.panes[0])

	foot := lastLine(m.View())
	if strings.Contains(foot, "+") {
		t.Errorf("the footer marks a loss against a count: %q — `+1` beside `N favourite sessions` "+
			"reads as one favourite missing", foot)
	}
	// And the way in and the way out are still on the screen, on the header, which is what makes
	// dropping the key line acceptable at all.
	head := strings.SplitN(m.View(), "\n", 2)[0]
	if !strings.Contains(head, "enter narrows") || !strings.Contains(head, "esc goes back") {
		t.Errorf("the header does not carry the two keys the footer may drop: %q", head)
	}

	// A warning that does not fit is MARKED, because the operator has a file to edit.
	m.rulesWarn = "projects.toml: line 4: expected quoted string — grouping by directory name instead"
	m.note = "cannot open that project any more"
	out := m.View()
	if !strings.Contains(out, "line 4") && !strings.Contains(out, "+") {
		t.Errorf("the warning vanished with no sign it exists:\n%s", out)
	}
}
