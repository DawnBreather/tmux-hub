package ui

import (
	"fmt"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// The GEOMETRY of the filesystem view: the frame's height, the band's top edge, and the head line's
// behaviour as the terminal narrows. The dashboard has a case for each of these three and the tree had
// none — and the tree is the screen the hub opens on, so a frame one row too tall here is the first
// thing an operator sees rather than something they reach by pressing a key.

// treeFleetOf is n pathless sessions on one volume, which is the cheapest way to make the LIST longer without
// changing anything else about the screen. Pathless on purpose: a row with no path hangs off its volume,
// so n rows cost n+1 lines and no directory arithmetic enters the measurement.
func treeFleetOf(n int) []registry.Pane {
	var ps []registry.Pane
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%08d", i+1)
		ps = append(ps, registry.Pane{
			Kind: registry.KindAgent, Command: "background", Host: "local",
			Session: fmt.Sprintf("sess-%03d", i), AgentID: id, SessionID: id + "-a",
			PaneID: "agent:" + id + "@b", ClassifiedState: state.Idle,
			Content: []string{"  (no pane)"},
		})
	}
	return ps
}

// treeFrameOf paints the tree at a stated size on a stated fleet, through the model, so the frame is
// the one the runtime would produce rather than one this test composed.
func treeFrameOf(t *testing.T, w, h int, panes []registry.Pane) string {
	t.Helper()
	m := base(t, w, h, panes...)
	m.home = "/home/dev"
	m.mode = modeTree
	return m.View()
}

// THE FRAME IS EXACTLY AS TALL AS THE TERMINAL, at every height the project speaks to.
//
// One row too many and tmux scrolls the top line off; one row too few and the bottom of the screen keeps
// whatever was there before. This screen computes its own body from `treeBodyHeight`, which subtracts
// three rows of chrome where the dashboard's `bodyHeight` subtracts two — a real difference (a title,
// the rule, and the footer), and the kind of arithmetic that is right for one screen and one off for the
// next.
func TestTheTreeFrameIsExactlyAsTallAsTheTerminal(t *testing.T) {
	for _, h := range []int{10, 20, 24, 40, 50} {
		for _, w := range []int{80, 120} {
			screen := treeFrameOf(t, w, h, treeFleet())
			if got := len(strings.Split(screen, "\n")); got != h {
				t.Errorf("%dx%d: the frame is %d lines, want %d", w, h, got, h)
			}
		}
	}
}

// THE BAND'S TOP EDGE DOES NOT MOVE WITH THE LIST'S LENGTH.
//
// The dashboard has this case and the reason transfers whole: a band whose top edge moves is not
// pinned, so the list under the cursor shrinks and grows as sessions come and go, and the row the
// operator was reading walks off under their eye. Here it also guards the sharing — the tree takes its
// band from `detailsHeight` and its list from `treeListHeight`, and those two must be computed from the
// same body or the two edges meet in the wrong place.
func TestTheTreeBandsTopEdgeDoesNotMoveWithTheListsLength(t *testing.T) {
	edge := func(n int) int {
		// The cursor is put on a ROW, because a directory gets a DIFFERENT tile (nodeTile) and this
		// case is about the band's top edge rather than about which tile fills it. Both are three
		// rows tall, so either would do — the row is chosen because it is the tile whose top edge the
		// dashboard's own case pins.
		m := base(t, 100, 24, treeFleetOf(n)...)
		m.home = "/home/dev"
		m.mode = modeTree
		tls := m.treeShown()
		for i, l := range tls {
			if l.IsRow {
				m = m.treeTo(tls, i)
				break
			}
		}
		screen := m.View()
		for i, ln := range strings.Split(screen, "\n") {
			if strings.HasPrefix(strings.TrimSpace(ln), "┌─") {
				return i
			}
		}
		t.Fatalf("%d sessions drew no band:\n%s", n, screen)
		return -1
	}
	want := edge(1)
	for _, n := range []int{2, 5, 9, 14, 40} {
		if got := edge(n); got != want {
			t.Errorf("with %d sessions the band starts on row %d, with 1 session on row %d", n, got, want)
		}
	}
}

// A WIDER TERMINAL NEVER SHOWS LESS OF THE HEAD LINE.
//
// Three lines, no knowledge of the layout, and it is the assertion that catches the whole class of
// "composed at one width, rendered at another" — the class this repo has paid for on the footer, on the
// pinned row and on the launch form's key row. The head line is a priority list now, and a priority list
// is exactly where a greedy pass can regress into showing more at 80 than at 100.
func TestTheTreesHeadLineNeverShowsLessAsTheTerminalWidens(t *testing.T) {
	shown := func(w int) int {
		head := strings.Split(treeFrameOf(t, w, 24, treeFleet()), "\n")[0]
		if got := lines.Width(strings.TrimRight(head, " ")); got > w {
			t.Fatalf("width %d: the head line is %d columns: %q", w, got, head)
		}
		// The number of PARTS it kept, not its byte length: the `+N` marker makes a narrow line longer
		// while saying less, so length is the wrong measure of how much was shown.
		return strings.Count(head, " · ") + 1
	}
	prev, prevW := 0, 0
	for _, w := range []int{50, 60, 70, 80, 100, 120, 160, 200} {
		got := shown(w)
		if got < prev {
			t.Errorf("width %d keeps %d parts of the head line, while the narrower %d kept %d",
				w, got, prevW, prev)
		}
		prev, prevW = got, w
	}
}

// THE HEAD LINE MARKS WHAT IT DROPPED, and the two keys that get the operator OUT and IN survive to the
// narrowest width the screen draws at.
//
// Measured before this was a priority list: at 60 columns the line read `enter opens, a goes to what
// wait` — cut mid-word, with no mark, which is this repo's oldest defect class (keeping the label and
// losing the action). `esc leaves` is the one part that must never be the thing that goes: a screen with
// no visible way out is a trap, and this is the screen the hub opens on.
func TestTheTreesHeadLineKeepsTheWayOutAndMarksWhatItDropped(t *testing.T) {
	for _, w := range []int{50, 60, 80, 100} {
		head := strings.Split(treeFrameOf(t, w, 24, treeFleet()), "\n")[0]
		if !strings.Contains(head, "enter opens") {
			t.Errorf("width %d: the head line lost the way in: %q", w, head)
		}
		// BOTH keys from 60 columns up. At 50 — below anything §16 promises, and 30 columns below the
		// committed size — `lines.Fit` has room for one, and the census plus the way IN is what it
		// keeps. Asserting both at 50 would be asserting a line the screen cannot draw; asserting
		// neither would let the whole legend vanish unnoticed, so the floor is stated rather than
		// skipped.
		if w >= 60 && !strings.Contains(head, "esc leaves") {
			t.Errorf("width %d: the head line lost the way out: %q", w, head)
		}
		// Where parts were dropped the line says so. At 120 nothing is dropped, which is why this case
		// stops at 100 — asserting a marker at every width would assert a defect at the wide end.
		if !strings.Contains(head, " +") {
			t.Errorf("width %d: parts do not fit and the line does not say so: %q", w, head)
		}
	}
	// And at a width where everything fits there is no marker, because a `+0` would be a lie about a
	// complete line. 200 columns is well past the 8 parts' total.
	wide := strings.Split(treeFrameOf(t, 200, 24, treeFleet()), "\n")[0]
	if strings.Contains(wide, " +") {
		t.Errorf("at 200 columns the head line claims it dropped something: %q", wide)
	}
	if !strings.Contains(wide, "q quits") {
		t.Errorf("at 200 columns the head line does not carry its last part: %q", wide)
	}
}

// THE BAND UNDER THE TREE IS THE DASHBOARD'S, on the row the tree's cursor is on.
//
// A second tile renderer would be a second answer to "what does a pane with nothing captured look
// like", and §17's fallbacks are the expensive half of that answer. The subject is what needs the test:
// the band must follow the TREE's cursor, not the dashboard's, and the two are independent.
func TestTheTreesBandFollowsTheTreesOwnCursor(t *testing.T) {
	m := treeModelOpened(t)
	tls := m.treeShown()
	var first, second int = -1, -1
	for i, l := range tls {
		if !l.IsRow {
			continue
		}
		if first < 0 {
			first = i
			continue
		}
		if l.Pane.Session != tls[first].Pane.Session {
			second = i
			break
		}
	}
	if first < 0 || second < 0 {
		t.Fatalf("the fixture has fewer than two distinct rows:\n%s", strings.Join(treeShape(t, tls), "\n"))
	}
	on := func(at int) string {
		return m.treeTo(tls, at).View()
	}
	a, b := tls[first].Pane.Session, tls[second].Pane.Session
	if !strings.Contains(on(first), "┌─ local "+a) {
		t.Errorf("with the cursor on %q the band does not name it:\n%s", a, on(first))
	}
	if !strings.Contains(on(second), "┌─ local "+b) {
		t.Errorf("with the cursor on %q the band does not name it:\n%s", b, on(second))
	}
	// The dashboard's cursor never moved through any of that, which is what makes the two independent.
	if m.cursorIndex() != 0 {
		t.Errorf("the dashboard's cursor moved to %d while walking the tree", m.cursorIndex())
	}
	_ = project.Aliases{}
}

// treeCursorOn puts the tree's cursor on the node with a stated label and returns the frame.
func treeCursorOn(t *testing.T, m model, label string) string {
	t.Helper()
	tls := m.treeShown()
	for i, l := range tls {
		if !l.IsRow && l.Label == label {
			return m.treeTo(tls, i).View()
		}
	}
	t.Fatalf("no node labelled %q on the screen:\n%s", label, strings.Join(treeShape(t, tls), "\n"))
	return ""
}

// A DIRECTORY UNDER THE CURSOR GETS A TILE, and it says what is inside.
//
// The band is where this screen answers "what am I pointing at", and the cursor is on a directory for
// most of the lines here — so without this the answer was five blank rows at the committed size. The
// tile carries the two things a directory has that the line above it does not: the REAL path (the label
// is folded to `~` and collapsed across several nodes) and the names of the sessions inside.
func TestADirectoryUnderTheCursorGetsATileNamingWhatIsInside(t *testing.T) {
	screen := treeCursorOn(t, treeModel(t), "frontend")
	for _, want := range []string{
		"┌─ local frontend",                 // the address, host first
		"2 sessions · 2 asking",             // the roll-up, the reason to open it
		"/home/dev/lab/streams/st/frontend", // the REAL path, not the drawn label
		"healthchecks",                      // and the sessions themselves
		"troubleshooting",
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("the directory tile does not carry %q:\n%s", want, screen)
		}
	}
}

// A CLOSED node's tile still names what is inside it, which is the whole permission to close one.
//
// The rows come from the FLEET and not from the drawn lines — a closed node contributes none of those —
// and that is the difference between a tree that hides work and a tree that summarises it.
func TestAClosedDirectorysTileStillNamesWhatIsInside(t *testing.T) {
	m := treeModel(t)
	tls := m.treeShown()
	var key string
	for _, l := range tls {
		if l.Label == "st" {
			key = l.Key
		}
	}
	if key == "" {
		t.Fatalf("the fixture has no `st` node:\n%s", strings.Join(treeShape(t, tls), "\n"))
	}
	closed := m.setTreeOpen(key, false)
	screen := treeCursorOn(t, closed, "st")
	if strings.Count(screen, "healthchecks") != 1 {
		// Once: in the TILE. The list is closed, so the row itself is not drawn.
		t.Errorf("a closed directory's tile does not name the session inside it exactly once:\n%s", screen)
	}
	for _, want := range []string{"4 sessions · 3 asking", "store-online", "observability"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the closed directory's tile does not carry %q:\n%s", want, screen)
		}
	}
}

// THE CENSUS IS THE FLEET'S, so closing a directory does not make sessions disappear from the head.
//
// Measured before this was fixed: the head counted the DRAWN lines, so closing `st` took it from
// `7 sessions · 4 asking` to `3 sessions · 1 asking` — the operator's own fold reported as work going
// away, on the one line that says how much there is. This repo has the same defect on record for the
// hidden-row counts, where a mode answered by returning a zero.
func TestClosingADirectoryDoesNotChangeTheCensus(t *testing.T) {
	m := treeModel(t)
	head := func(m model) string { return strings.Split(m.View(), "\n")[0] }
	before := head(m)
	if !strings.Contains(before, "7 sessions · 4 asking") {
		t.Fatalf("the fixture's census is not what this case is written against: %q", before)
	}
	for _, l := range m.treeShown() {
		if !l.IsRow {
			m = m.setTreeOpen(l.Key, false)
		}
	}
	if got := head(m); !strings.Contains(got, "7 sessions · 4 asking") {
		t.Errorf("with every directory closed the head says %q, want the same census as %q", got, before)
	}
}

// A TALLY THAT SAYS `… and N more` ADDS UP.
//
// The last row of the tile is REPLACED by the tally, so the session that was on it is no longer listed
// and must be counted among the remainder. Measured before this was right: six sessions listed three
// and claimed two more, which is five — an arithmetic error nobody reads as one, because a plausible
// number in a small box looks like a fact.
func TestTheDirectoryTilesTallyAddsUp(t *testing.T) {
	screen := treeCursorOn(t, treeModel(t), "local")
	var listed, more int
	for _, ln := range strings.Split(screen, "\n") {
		if !strings.HasPrefix(ln, "│") {
			continue
		}
		if n, err := fmt.Sscanf(strings.TrimSpace(strings.Trim(ln, "│ ")), "… and %d more", &more); err == nil && n == 1 {
			continue
		}
		// A session row inside the tile is one with a state word on it, which is what the row shape is.
		for _, st := range []string{"needs", "done", "idle", "works", "error", "quiet"} {
			if strings.Contains(ln, " "+st+" ") {
				listed++
				break
			}
		}
	}
	if more == 0 {
		t.Fatalf("this fixture does not overflow the tile, so the tally is not exercised:\n%s", screen)
	}
	// Six sessions on the local volume, and every one of them is either listed or counted.
	if want := 6; listed+more != want {
		t.Errorf("the tile lists %d sessions and claims %d more, which is %d of %d:\n%s",
			listed, more, listed+more, want, screen)
	}
}

// MARKED PANES WIN THE BAND, even with the cursor on a directory.
//
// §7's rule is that a send's target is a tile the operator can see, and a directory tile would put the
// screen's most consequential state (what a prompt is about to go to) behind a summary of a folder.
func TestMarkedPanesWinTheBandOverADirectoryTile(t *testing.T) {
	m := treeModelOpened(t)
	tls := m.treeShown()
	at := -1
	for i, l := range tls {
		if l.IsRow {
			at = i
			break
		}
	}
	if at < 0 {
		t.Fatal("the fixture has no rows")
	}
	marked := tls[at].Pane
	m = m.treeTo(tls, at)
	m.mark(marked)
	// Now stand on a DIRECTORY with the mark still in place.
	screen := treeCursorOn(t, m, "frontend")
	if strings.Contains(screen, "┌─ local frontend") {
		t.Errorf("the directory tile took the band from a marked pane:\n%s", screen)
	}
	if !strings.Contains(screen, "┌─ local "+marked.Session) {
		t.Errorf("the marked pane's tile is not in the band:\n%s", screen)
	}
}

// THE KEYWORD FIELD LOOKS THE SAME ON BOTH SCREENS, and the assertion is a COMPARISON rather than a
// literal.
//
// Measured before this was shared: on the filesystem view the footer drew the applied-filter sentence
// (`"fro" · 2 of 7`) while the operator was typing, so the CARET and the two keys that end the field
// were on the dashboard and nowhere else — a field whose end you cannot see is a field you cannot type
// in, which is the ruling the naming overlay had to learn. Comparing the two screens is what makes this
// case fixture-independent: it cannot pass because a string was updated in one place.
func TestTheKeywordFieldIsTheSameLineOnBothScreens(t *testing.T) {
	field := func(onTree bool) string {
		m := base(t, 100, 20, treeFleet()...)
		m.home = "/home/dev"
		if onTree {
			out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
			m = out.(model)
		}
		out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
		m = out.(model)
		for _, r := range "fro" {
			o, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			m = o.(model)
		}
		ls := strings.Split(m.View(), "\n")
		return ls[len(ls)-1]
	}
	onTree, onFlat := field(true), field(false)
	if onTree != onFlat {
		t.Errorf("the field differs between the two screens:\n  tree %q\n  flat %q", onTree, onFlat)
	}
	// And it really is the FIELD and not the applied-filter sentence, or the comparison above would be
	// satisfied by two screens that are both wrong.
	for _, want := range []string{"search: fro", "▏", "enter: keep", "esc: cancel"} {
		if !strings.Contains(onTree, want) {
			t.Errorf("the field on the tree is missing %q: %q", want, onTree)
		}
	}
}

// A PINNED ROW IN THE TREE CARRIES ITS ADDRESS, because nothing above it does.
//
// Asked for in the operator's own words — "the record under FAVOURITES special/short, {status} name
// @host:path" — and on this screen it is not a flourish: every other row reads its host from the volume
// and its directory from the node above it, while the pinned band is a LIST rather than a place. Without
// the address, two favourites of one name in two checkouts are the same line twice, which is the
// duplicate report this whole screen came from.
func TestAPinnedRowInTheTreeCarriesItsAddress(t *testing.T) {
	fleet := treeFleet()
	fav := map[string]bool{MarkKey(fleet[0]): true} // store-online, inside st-edgebox
	for _, w := range []int{80, 120, 200} {
		f := Frame{Panes: fleet, Hosts: hosts2(), Width: w, Height: 24, Home: "/home/dev",
			Favourites: fav, Screen: modeTree, TreeCursor: 0,
			Tree: treeLines(fleet, project.Aliases{}, fav, nil, "/home/dev", "local")}
		var row string
		for _, l := range RenderTreeScreen(f) {
			if strings.Contains(l, "store-online") {
				row = l
				break
			}
		}
		if row == "" {
			t.Fatalf("width %d: the pinned row is not drawn:\n%s", w,
				strings.Join(RenderTreeScreen(f), "\n"))
		}
		if !strings.Contains(row, "@local:~/lab/streams/st/st-edgebox") {
			t.Errorf("width %d: the pinned row does not carry its address: %q", w, row)
		}
		// HOME is folded, which is what buys the eight columns that decide whether it fits at 80.
		if strings.Contains(row, "/home/dev") {
			t.Errorf("width %d: the row spells HOME out: %q", w, row)
		}
		if got := lines.Width(strings.TrimRight(row, " ")); got > w {
			t.Errorf("width %d: the row is %d columns wide: %q", w, got, row)
		}
	}
	// And an UNPINNED row does not wear the shape, or the shape says nothing about being pinned: the
	// same session inside its directory reads as a plain row.
	f := Frame{Panes: fleet, Hosts: hosts2(), Width: 120, Height: 24, Home: "/home/dev",
		Screen: modeTree, TreeCursor: 0,
		Tree: treeLines(fleet, project.Aliases{}, nil, nil, "/home/dev", "local")}
	open := map[string]bool{}
	for _, l := range f.Tree {
		if !l.IsRow {
			open[l.Key] = true
		}
	}
	f.Tree = treeLines(fleet, project.Aliases{}, nil, open, "/home/dev", "local")
	for _, l := range RenderTreeScreen(f) {
		if strings.Contains(l, "store-online") && strings.Contains(l, "@local:") {
			t.Errorf("an unpinned row is wearing the pinned shape: %q", l)
		}
	}
}

// AN OVERLAY RAISED FROM THE PROJECT LIST PAINTS THE PROJECT LIST, not the dashboard.
//
// Found by an adversarial review of the commit that introduced `backdrop`, and it was live: the project
// list raises the naming overlay (`N` on a project), `raise` recorded `modeProjects` correctly, and
// `backdrop` read every non-tree screen as the dashboard — so naming a project replaced the list the
// operator was reading, INCLUDING THE ROW BEING NAMED, with a list of sessions, while `dismiss`
// afterwards returned them to the list. The picture and the return disagreed.
//
// The needles belong to exactly one screen each: the project list's own head line says
// `enter narrows, esc goes back`, and only the dashboard prints the program's name as a title.
func TestAnOverlayRaisedFromTheProjectListPaintsTheProjectList(t *testing.T) {
	m := base(t, 100, 24, treeFleet()...)
	m.home = "/home/dev"
	m.projectsPath = filepath.Join(t.TempDir(), "projects.toml")

	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("P")})
	m = out.(model)
	if m.mode != modeProjects {
		t.Fatalf("P did not open the project list: mode %v", m.mode)
	}
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")})
	m = out.(model)
	if m.mode != modeNaming {
		t.Fatalf("N on the project list did not open the naming overlay: mode %v, note %q",
			m.mode, m.note)
	}
	screen := m.View()
	if !strings.Contains(screen, "name this project") {
		t.Fatalf("the overlay is not the project-naming form:\n%s", screen)
	}
	if !strings.Contains(screen, "enter narrows, esc goes back") {
		t.Errorf("the backdrop is not the project list:\n%s", screen)
	}
	// NOT the dashboard, and the needle is its ROW SHAPE — `host/session` — because the fixture holds
	// a project called `tmux-hub` and the program's own title is therefore not a signature here. That
	// is the trap this file has already corrected twice: a needle both screens can print asserts
	// nothing.
	if strings.Contains(screen, "local/store-online") {
		t.Errorf("the backdrop is the DASHBOARD, which is not the screen the operator was on:\n%s",
			screen)
	}
	// The row being named is still on the screen, which is the operator-visible point: they can read
	// what they are naming while they type.
	if !strings.Contains(screen, "frontend") {
		t.Errorf("the row being named is not visible behind the form:\n%s", screen)
	}
	// And the list's own FOOTER survives, which is what a truncated pre-rendered backdrop would have
	// lost: the base is re-rendered at the reduced height rather than cut.
	if !strings.Contains(screen, "of 2") {
		t.Errorf("the project list's cells are gone from the backdrop:\n%s", screen)
	}
}

// THE EXPANSION SET HOLDS ONLY WHAT THE OPERATOR DECIDED, and one press decides one node.
//
// A review found the coupling this replaces: the map used to be SEEDED on first use from the WHOLE fleet
// while the screen draws the narrowed rows, so with a filter on it held 8 entries for a tree drawing 2
// nodes. The seed existed so that a first `h` would close ONE node instead of collapsing a tree nobody
// had expanded — so the fix had to keep that property while removing the second source, and making an
// ABSENT key mean "whatever the default says" does both.
//
// Both halves are asserted, because either alone would pass against the defect: the shape must not move
// when a node is set to the value it already had, and one press must move exactly one node.
func TestTheExpansionSetHoldsOnlyTheOperatorsDecisions(t *testing.T) {
	m := treeModelOpened(t)
	for _, r := range "frontend" {
		m.search.Insert(string(r))
	}
	m.treeOpen = nil // as if nothing had been pressed, with the narrowing on

	before := treeShape(t, m.treeShown())
	// A no-op decision: set the first node to the state it is already in.
	first := m.treeShown()[0]
	m = m.setTreeOpen(first.Key, first.Open)
	if got := treeShape(t, m.treeShown()); strings.Join(got, "\n") != strings.Join(before, "\n") {
		t.Errorf("writing a node's own state changed the tree:\n  before %v\n  after  %v", before, got)
	}
	if len(m.treeOpen) != 1 {
		t.Errorf("one press wrote %d entries — the map is being seeded from something", len(m.treeOpen))
	}

	// And one press moves ONE node: closing a node with children hides only its own subtree.
	tls := m.treeShown()
	var target treeLine
	for _, l := range tls {
		if !l.IsRow && l.Open && l.Expandable {
			target = l
			break
		}
	}
	if target.Key == "" {
		t.Fatalf("the narrowed fixture has no open node to close, so this half cannot run:\n%s",
			strings.Join(treeShape(t, tls), "\n"))
	}
	closed := treeShape(t, m.setTreeOpen(target.Key, false).treeShown())
	if len(closed) >= len(tls) {
		t.Errorf("closing %q did not hide anything: %d lines before, %d after", target.Label,
			len(tls), len(closed))
	}
	for _, l := range m.setTreeOpen(target.Key, false).treeShown() {
		if l.Key == target.Key && l.Open {
			t.Errorf("%q is still open after being closed", target.Label)
		}
	}
}
