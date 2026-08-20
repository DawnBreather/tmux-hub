package ui

import (
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/project"
	tea "github.com/charmbracelet/bubbletea"
)

// treeModel is the tree screen open on the fixture fleet, reached the way an operator reaches it.
func treeModel(t *testing.T) model {
	t.Helper()
	m := base(t, 120, 24, treeFleet()...)
	m.home = "/home/dev"
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	m = out.(model)
	if m.mode != modeTree {
		t.Fatalf("`t` did not open the filesystem view, mode is %v", m.mode)
	}
	return m
}

// treeModelOpened is the tree with every directory OPENED, which is the state an operator reaches with
// a few `enter`s and the state any case about SESSION rows needs.
//
// The default folds the folders — `openByDefault` opens the map and shuts the leaves — so a case that
// walks to a row, marks one, or counts them must open them first or it is a case about an empty screen.
// Two passes, because opening a closed node can reveal child directories that are themselves closed.
func treeModelOpened(t *testing.T) model {
	t.Helper()
	m := treeModel(t)
	for pass := 0; pass < 2; pass++ {
		for _, l := range m.treeShown() {
			if !l.IsRow {
				m = m.setTreeOpen(l.Key, true)
			}
		}
	}
	return m
}

func treeScreenOf(t *testing.T, m model) string {
	t.Helper()
	return m.View()
}

// `t` OPENS IT AND `esc` LEAVES IT, which is the smallest claim a new screen has to answer: a screen
// with no way out traps the operator, and this repo has an e2e case for exactly that on the history
// view.
func TestTOpensTheFilesystemViewAndEscLeaves(t *testing.T) {
	m := treeModel(t)
	if s := treeScreenOf(t, m); !strings.Contains(s, "esc leaves") {
		t.Errorf("the screen does not say how to leave:\n%s", s)
	}
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := out.(model).mode; got != modeBrowse {
		t.Errorf("esc left the tree in mode %v, want the dashboard", got)
	}
	// And `t` again is the same door in reverse, so the key that opened it closes it.
	again, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if got := again.(model).mode; got != modeBrowse {
		t.Errorf("`t` inside the tree left mode %v, want the dashboard", got)
	}
}

// NOTHING RUNS OVER THE EDGE, at every width the project speaks to.
//
// A row wider than the terminal is what tmux wraps, and a wrapped line costs a row the fold then eats —
// which on a tree means a node's children appearing to belong to the line below it.
func TestNoTreeLineRunsOverTheEdge(t *testing.T) {
	for _, w := range []int{60, 80, 120, 200} {
		m := treeModel(t)
		m.width = w
		for i, l := range strings.Split(m.View(), "\n") {
			if got := lines.Width(strings.TrimRight(l, " ")); got > w {
				t.Errorf("width %d: line %d is %d columns: %q", w, i, got, l)
			}
		}
	}
}

// j and k move the cursor by LINE, and the cursor is keyed on the line's identity rather than an index:
// a node opening under the operator's hand changes every index below it.
func TestTheTreeCursorFollowsTheLineAndNotTheIndex(t *testing.T) {
	m := treeModel(t)
	tls := m.treeShown()
	if len(tls) < 4 {
		t.Fatalf("the fixture draws %d lines, too few to move through", len(tls))
	}
	// Walk to a line deep enough to have a node above it, and remember WHAT it is.
	for i := 0; i < 3; i++ {
		out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		m = out.(model)
	}
	held := m.treeShown()[m.treeIndex(m.treeShown())]
	// Now close the FAVOURITES node above it, which shortens the list.
	m = m.setTreeOpen(favouritesKey, false)
	after := m.treeShown()
	if got := after[m.treeIndex(after)].Key; got != held.Key {
		t.Errorf("closing a node above moved the cursor from %q to %q — a stored index would do "+
			"exactly that", held.Key, got)
	}
}

// enter OPENS and h CLOSES, and both answer on a row rather than doing nothing.
func TestEnterOpensAndHClosesAndBothAnswerOnARow(t *testing.T) {
	m := treeModel(t)
	tls := m.treeShown()
	nodeAt, rowAt := -1, -1
	for i, l := range tls {
		if !l.IsRow && l.Open && nodeAt < 0 {
			nodeAt = i
		}
		if l.IsRow && rowAt < 0 {
			rowAt = i
		}
	}
	if nodeAt < 0 || rowAt < 0 {
		t.Fatalf("the fixture has no open node or no row:\n%s", strings.Join(treeShape(t, tls), "\n"))
	}
	key := tls[nodeAt].Key
	m = m.treeTo(tls, nodeAt)
	closed, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = closed.(model)
	if lineByKey(m.treeShown(), key).Open {
		t.Error("enter on an open node did not close it")
	}
	opened, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = opened.(model)
	if !lineByKey(m.treeShown(), key).Open {
		t.Error("enter on a closed node did not open it")
	}

	// On a ROW both keys answer. A key that silently does nothing is what a broken key looks like.
	m = m.treeTo(m.treeShown(), rowAt)
	onRow, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if note := onRow.(model).note; !strings.Contains(note, "a goes to it") {
		t.Errorf("enter on a session says %q, want it to name the key that acts", note)
	}
}

// `a` ON A NODE goes to the row that has waited longest inside it, opening the node on the way — which
// is what `a` means on the project list, so the key does not change meaning between two screens.
func TestAOnANodeGoesToWhatWaitsInsideIt(t *testing.T) {
	m := treeModel(t)
	// Close everything, then press `a` on the local volume.
	m = m.setTreeOpen(favouritesKey, false)
	tls := m.treeShown()
	at := -1
	for i, l := range tls {
		if !l.IsRow && l.Label == "local" {
			at = i
		}
	}
	if at < 0 {
		t.Fatalf("the local volume is not on the screen:\n%s", strings.Join(treeShape(t, tls), "\n"))
	}
	m = m.treeTo(tls, at)
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = out.(model)
	after := m.treeShown()
	landed := after[m.treeIndex(after)]
	if !landed.IsRow {
		t.Fatalf("`a` on a volume left the cursor on %q, want a waiting session:\n%s",
			landed.Label, strings.Join(treeShape(t, after), "\n"))
	}
	if !waitsForOperator(landed.Pane) {
		t.Errorf("`a` landed on %q, which is %v and not waiting", landed.Pane.Session,
			landed.Pane.State())
	}
}

// `a` FROM A CLOSED VOLUME REACHES THE SESSION, opening every directory on the way.
//
// This is the promise the head line makes on every frame ("a goes to what waits"), and the default
// expansion made it false for a while: the first version opened the node ONE level and scanned the drawn
// lines, so with the folders shut it found nothing and the cursor did not move — a key silently doing
// nothing, on the screen's most-advertised gesture. The fix takes the target from the FLEET and opens
// the path from the target (`openPathTo`), which is why this case starts from a tree with everything
// closed and asserts on the ancestors as well as on the cursor.
func TestAFromAClosedVolumeOpensThePathToTheWaitingSession(t *testing.T) {
	m := treeModel(t)
	// Everything closed, which is stronger than the default: not even the map is open.
	for _, l := range m.treeShown() {
		if !l.IsRow {
			m = m.setTreeOpen(l.Key, false)
		}
	}
	tls := m.treeShown()
	at := -1
	for i, l := range tls {
		if !l.IsRow && l.Label == "local" {
			at = i
		}
	}
	if at < 0 {
		t.Fatalf("the local volume is not on the screen:\n%s", strings.Join(treeShape(t, tls), "\n"))
	}
	for _, l := range tls {
		if l.IsRow && l.Depth > 1 {
			t.Fatalf("this case needs a closed tree and a session row is drawn:\n%s",
				strings.Join(treeShape(t, tls), "\n"))
		}
	}
	out, _ := m.treeTo(tls, at).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	after := out.(model)
	lines := after.treeShown()
	landed := lines[after.treeIndex(lines)]
	if !landed.IsRow {
		t.Fatalf("`a` on a closed volume left the cursor on %q:\n%s", landed.Label,
			strings.Join(treeShape(t, lines), "\n"))
	}
	if !waitsForOperator(landed.Pane) {
		t.Errorf("`a` landed on %q, which is %v and not waiting", landed.Pane.Session, landed.Pane.State())
	}
	// THE LONGEST-WAITING one, which is what the fleet's own order puts first — not merely some waiting
	// row. Derived from the fleet rather than from the tree, so the two orders are compared rather than
	// one of them restated.
	rows, _ := after.rowsForScreenLoose()
	for _, p := range rows {
		if waitsForOperator(p) && p.Host == "local" {
			if landed.Pane.Session != p.Session {
				t.Errorf("`a` landed on %q, but the longest-waiting session on that volume is %q",
					landed.Pane.Session, p.Session)
			}
			break
		}
	}
	// And every directory on the way is OPEN, or the row it landed on could not be drawn — which is the
	// same fact from the other side, and the one a future `a` would break first.
	// BACKWARDS from the row, taking the nearest line at each shallower depth: those are its ancestors.
	// Walking forwards and matching on increasing depth picks SIBLINGS — `frontend` and `st-edgebox` are
	// both depth 2, and the first one found is not the one the row is under. (That is how this assertion
	// failed on its first run, against a correct product.)
	seen := 0
	at = after.treeIndex(lines)
	for i, want := at-1, landed.Depth-1; i >= 0 && want >= 0; i-- {
		if lines[i].IsRow || lines[i].Depth != want {
			continue
		}
		if !lines[i].Open {
			t.Errorf("%q is an ancestor of the cursor's row and is closed", lines[i].Label)
		}
		seen++
		want--
	}
	if seen != landed.Depth {
		t.Errorf("the row is at depth %d and only %d ancestors were found above it", landed.Depth, seen)
	}
}

// A node whose subtree has nothing waiting SAYS so rather than moving the cursor somewhere arbitrary.
func TestAOnAQuietNodeSaysNothingIsWaiting(t *testing.T) {
	m := treeModel(t)
	tls := m.treeShown()
	at := -1
	for i, l := range tls {
		if !l.IsRow && l.Sum.Waiting == 0 && l.Sum.Broken == 0 && l.Sum.Total > 0 {
			at = i
			break
		}
	}
	if at < 0 {
		t.Skip("this fixture has no node with nothing waiting in it")
	}
	label := tls[at].Label
	m = m.treeTo(tls, at)
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if note := out.(model).note; !strings.Contains(note, "waiting") {
		t.Errorf("`a` on the quiet node %q says %q, want it to say nothing is waiting", label, note)
	}
}

// The FOOTER is the dashboard's, so a change of view does not cost the operator the fleet's health —
// the defect known-issues M1 records for the dashboard's own footer.
func TestTheTreeKeepsTheFleetFooter(t *testing.T) {
	m := treeModel(t)
	s := m.View()
	for _, want := range []string{"local up", "nuc up"} {
		if !strings.Contains(s, want) {
			t.Errorf("the tree's footer lost %q:\n%s", want, s)
		}
	}
}

// The tree paints the SAME narrowed set the dashboard paints, so `/` cannot mean two things.
func TestTheTreeObeysTheKeywordNarrowing(t *testing.T) {
	m := treeModelOpened(t)
	before := 0
	for _, l := range m.treeShown() {
		if l.IsRow {
			before++
		}
	}
	for _, r := range "frontend" {
		m.search.Insert(string(r))
	}
	after := 0
	for _, l := range m.treeShown() {
		if l.IsRow {
			after++
		}
	}
	if after >= before || after == 0 {
		t.Errorf("the keyword kept %d rows of %d — the tree is ignoring the narrowing the footer "+
			"claims is on", after, before)
	}
	_ = project.Aliases{}
}

// lineByKey is how these cases find a line they are about after the list has changed shape, because an
// index into a tree that opens and closes is a reference to a stranger.
func lineByKey(tls []treeLine, key string) treeLine {
	for _, l := range tls {
		if l.Key == key {
			return l
		}
	}
	return treeLine{}
}
