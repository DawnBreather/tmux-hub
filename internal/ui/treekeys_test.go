package ui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/fav"
	"github.com/DawnBreather/tmux-hub/internal/state"
	tea "github.com/charmbracelet/bubbletea"
)

// `space` MARKS THE ROW THE TREE CURSOR IS ON, NOT THE DASHBOARD CURSOR — the two cursors are
// independent, and pressing space must mark what the operator is looking at on THIS screen.
//
// Without this the tree's space would mark a row the dashboard happens to be pointing at, which can be
// off-screen or even from a different view entirely. The defect would read as "space does nothing" or
// "space marks the wrong row."
func TestSpaceMarksTheTreeCursorNotTheDashboard(t *testing.T) {
	m := treeModelOpened(t)
	tls := m.treeShown()
	// Find two different rows to position the cursors at.
	firstRow, secondRow := -1, -1
	for i, l := range tls {
		if l.IsRow {
			if firstRow < 0 {
				firstRow = i
			} else if secondRow < 0 {
				secondRow = i
				break
			}
		}
	}
	if firstRow < 0 || secondRow < 0 {
		t.Fatalf("fixture needs at least 2 rows, got %d total lines", len(tls))
	}

	// Put tree cursor on the second row, leave dashboard cursor untouched (at 0).
	m = m.treeTo(tls, secondRow)
	expected := tls[secondRow].Pane
	dashboardRow := m.rowsForScreen()[0]

	// The two cursors must point at DIFFERENT sessions for this test to have power.
	if expected.Session == dashboardRow.Session {
		t.Fatalf("fixture problem: tree cursor and dashboard cursor both point at %q, "+
			"so the test cannot distinguish which cursor space marks", expected.Session)
	}

	out, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = out.(model)

	// The tree cursor's row must be marked, not the dashboard cursor's row.
	if !m.sel.Has(selKey(expected)) {
		t.Errorf("space did not mark the tree cursor's row %q (at tree index %d), dashboard cursor "+
			"is at index %d", expected.Session, secondRow, m.cursorIndex())
	}
	// And specifically NOT the dashboard cursor's row (unconditional now that we asserted they differ).
	if m.sel.Has(selKey(dashboardRow)) {
		t.Errorf("space marked the dashboard cursor's row %q instead of the tree cursor's %q",
			dashboardRow.Session, expected.Session)
	}
}

// treeNodeAt walks the cursor to the first node with a stated label and returns the model and that line.
//
// By LABEL and not by index, because the tree re-sorts by attention and a node opening changes every
// index below it — the same reason the cursor itself is keyed on the line rather than on a position.
func treeNodeAt(t *testing.T, m model, label string) (model, treeLine) {
	t.Helper()
	tls := m.treeShown()
	for i, l := range tls {
		if !l.IsRow && l.Label == label {
			return m.treeTo(tls, i), l
		}
	}
	t.Fatalf("no node labelled %q on the screen:\n%s", label, strings.Join(treeShape(t, tls), "\n"))
	return m, treeLine{}
}

// paintedRows is how many SESSION lines the tree actually drew, counted from the painted string.
//
// Independent of `windowStart` and of `treeRowsOnScreen`, which are the arithmetic under test. A row
// line is `<glyph> <state word>  <name>` — the tree drops the host and the path, because the volume and
// the directory above it already say those — so BOTH fields are required. A glyph alone is not enough
// in two directions, each of which this matcher got wrong first: `▸` is the `idle` state glyph AND the
// closed-node arrow, so a folded directory counted as a session; and the cursor's own line begins with
// `>`, so the row under the cursor was not counted at all. Either error is silent, and both move the
// number this case compares.
func paintedRows(screen string) int {
	n := 0
	for _, ln := range strings.Split(screen, "\n") {
		fields := strings.Fields(strings.TrimPrefix(strings.TrimLeft(ln, " "), ">"))
		if len(fields) < 2 {
			continue
		}
		for _, st := range []state.State{state.Needs, state.Error, state.Works, state.Quiet,
			state.Idle, state.Done, state.Unknown} {
			if fields[0] == st.Glyph() && fields[1] == st.String() {
				n++
				break
			}
		}
	}
	return n
}

// `A` SELECTS EXACTLY THE ROWS THAT WERE PAINTED, count == drawn, not `>=`. Over-claiming would let
// the operator send to a row they cannot see, and under-claiming leaves rows on screen unselected.
//
// This repo paid twice for viewport estimates that disagreed with the paint (`inboxCapacity` claimed 12
// while 25 were drawn). The test derives "what was painted" from the rendered string, never by calling
// `treeRowsOnScreen` on both sides, which would pass by construction.
func TestASelectsExactlyWhatWasPainted(t *testing.T) {
	for _, h := range []int{30, 50, 70} {
		m := treeModelOpened(t)
		m.height = h
		// TWO CLOSED NODES, deliberately, and the cursor left on a node: `▸` is both the closed-node
		// arrow and the `idle` state glyph, so a matcher that reads only the first field counts each
		// folded directory as a session. Measured: with one closed node AND the cursor on a row the two
		// errors cancel exactly (+1 for the arrow, −1 for the `>` the cursor puts in front of its own
		// line) and a loose matcher passes by luck — which is why this fixture closes two.
		for _, l := range m.treeShown() {
			if !l.IsRow && (l.Label == "frontend" || l.Label == "st-edgebox") {
				m = m.setTreeOpen(l.Key, false)
			}
		}

		// Render the screen to see what actually got painted.
		screen := treeScreenOf(t, m)

		painted := paintedRows(screen)
		if painted == 0 {
			t.Fatalf("height %d: the fixture painted 0 rows — the needle is broken, not the screen empty", h)
		}

		// Press `A`.
		out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
		m = out.(model)

		if got := m.sel.Len(); got != painted {
			t.Errorf("height %d: `A` selected %d rows, but %d were painted", h, got, painted)
		}
	}
}

// `f` PINS THE CURSOR'S ROW, UNPINS IT AGAIN, AND PINNING MOVES IT INTO THE FAVOURITES NODE on the
// next paint. The pinned band is the tree's first node, so this is a structural property: a row that
// was elsewhere appears as a child of the `★` node after `f`.
//
// Without this the tree would not reflect pinning, or the row would stay where it was — both confusing
// since the dashboard shows the same row moved to the top.
func TestFPinsAndMovesRowIntoFavouritesNode(t *testing.T) {
	m := treeModelOpened(t)
	// Initialize favourites store so toggleFavouriteSessionOf can write.
	favs, err := fav.Open(filepath.Join(t.TempDir(), "favourites.json"))
	if err != nil {
		t.Fatalf("fav.Open: %v", err)
	}
	m.favs = favs

	tls := m.treeShown()
	// Find a row that is NOT pinned and is NOT in the favourites node already.
	at := -1
	for i, l := range tls {
		if l.IsRow && !m.isFavourite(l.Pane) {
			at = i
			break
		}
	}
	if at < 0 {
		t.Fatalf("fixture problem: all rows are already pinned, cannot test pinning:\n%s",
			strings.Join(treeShape(t, tls), "\n"))
	}
	target := tls[at].Pane
	m = m.treeTo(tls, at)

	// Pin it.
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	m = out.(model)

	// It must be pinned.
	if !m.isFavourite(target) {
		t.Errorf("f did not pin %q", target.Session)
	}

	// Rebuild the tree to see the favourites node appear.
	afterPin := m.treeShown()
	// Open the favourites node to see its children.
	for _, l := range afterPin {
		if !l.IsRow && l.Pinned {
			m = m.setTreeOpen(l.Key, true)
			break
		}
	}
	// Rebuild again after opening to see the node's children.
	afterPin = m.treeShown()

	foundInFavourites := false
	inFavouritesNode := false
	for _, l := range afterPin {
		if !l.IsRow && l.Pinned {
			inFavouritesNode = true
		} else if !l.IsRow {
			inFavouritesNode = false
		}
		if l.IsRow && l.Pane.Session == target.Session && inFavouritesNode {
			foundInFavourites = true
			break
		}
	}
	if !foundInFavourites {
		t.Errorf("pinned row %q is not a child of the favourites node:\n%s",
			target.Session, strings.Join(treeShape(t, afterPin), "\n"))
	}

	// Unpin it.
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	m = out.(model)

	if m.isFavourite(target) {
		t.Errorf("second f did not unpin %q", target.Session)
	}
}

// `x` ON A DIRECTORY REFUSES WITH A SENTENCE. A pane-less agent row has no pane to carry a hidden
// mark (§22.5), so `x` must refuse rather than silently doing nothing. The sentence must name the key
// so the operator knows what was refused.
//
// Without this the operator would press `x` and see no effect, which reads as "the key is broken."
func TestXOnDirectoryRefusesWithSentence(t *testing.T) {
	m := treeModel(t)
	tls := m.treeShown()
	at := -1
	for i, l := range tls {
		if !l.IsRow {
			at = i
			break
		}
	}
	if at < 0 {
		t.Fatal("fixture has no directory node")
	}
	m = m.treeTo(tls, at)

	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	got := out.(model).note

	if got == "" {
		t.Error("x on a directory set no note — the operator sees silence")
	}
	if !strings.Contains(got, "x") && !strings.Contains(got, "hide") {
		t.Errorf("x on a directory says %q, want it to name the key or the action", got)
	}
}

// `X` TOGGLES THE HIDDEN-ROW VISIBILITY, and the note must say which direction it toggled. Measured on
// the real fleet: `X` twice in a row printed the same sentence both times, so the operator could not
// tell whether hidden rows were now shown or not.
func TestXTogglesShowHiddenAndSaysWhichWay(t *testing.T) {
	m := treeModel(t)
	if m.showHidden {
		t.Fatal("fixture starts with showHidden true, test needs it false")
	}

	// First X: turn it on.
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	m = out.(model)

	if !m.showHidden {
		t.Error("X did not turn showHidden on")
	}
	if note := m.note; !strings.Contains(note, "showing") && !strings.Contains(note, "marked") {
		t.Errorf("X on says %q, want it to say hidden rows are now visible", note)
	}

	// Second X: turn it off.
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	m = out.(model)

	if m.showHidden {
		t.Error("second X did not turn showHidden off")
	}
	if note := m.note; !strings.Contains(note, "off") && !strings.Contains(note, "again") {
		t.Errorf("second X says %q, want it to say hidden rows are off the screen", note)
	}
}

// `n` ON A NODE PRE-FILLS THE DIRECTORY with the node's REAL absolute path, not the `~`-folded label
// the screen draws. The fold relabels a node and never rekeys it, so asserting the label would pass
// while the form held a non-absolute path that `Spec.Validate` refuses.
//
// Without this the operator would press `n` on a project and then type the same path the form should
// have filled for them.
func TestNOnNodePrefillsRealAbsoluteDirectory(t *testing.T) {
	m := treeModel(t)
	tls := m.treeShown()
	// Find a directory node that has a real path (not the favourites node).
	at := -1
	for i, l := range tls {
		if !l.IsRow && l.Path != "" {
			at = i
			break
		}
	}
	if at < 0 {
		t.Fatal("fixture has no directory node with a path")
	}
	node := tls[at]
	m = m.treeTo(tls, at)

	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = out.(model)

	if m.mode != modeLaunch {
		t.Fatalf("n did not open the launch form, mode is %v", m.mode)
	}

	// The form's directory field must hold the node's REAL path.
	got := m.launchForm.dirInput.Text()
	host, path := nodeAddress(node.Key)
	if host == "" || path == "" {
		t.Fatalf("nodeAddress(%q) returned empty, cannot verify", node.Key)
	}
	if got != path {
		t.Errorf("n on node %q pre-filled %q, want the node's real path %q (label was %q)",
			node.Key, got, path, node.Label)
	}
	// And it must be absolute.
	if !strings.HasPrefix(got, "/") {
		t.Errorf("n pre-filled %q, which is not absolute — the fold must not rekey", got)
	}
}

// `n` ON A ROW PRE-FILLS THE ROW'S OWN CWD, which is the sensible default for a sibling session.
func TestNOnRowPrefillsRowCwd(t *testing.T) {
	m := treeModelOpened(t)
	tls := m.treeShown()
	at := -1
	for i, l := range tls {
		if l.IsRow && l.Pane.Path != "" {
			at = i
			break
		}
	}
	if at < 0 {
		t.Skip("fixture has no row with a cwd")
	}
	row := tls[at]
	m = m.treeTo(tls, at)

	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = out.(model)

	if m.mode != modeLaunch {
		t.Fatalf("n did not open the launch form, mode is %v", m.mode)
	}

	got := m.launchForm.dirInput.Text()
	if got != row.Pane.Path {
		t.Errorf("n on row with cwd %q pre-filled %q", row.Pane.Path, got)
	}
}

// `n` ON A VOLUME opens the form for THAT HOST, with no directory.
//
// A volume has no path, and refusing it (which this did at first) made the operator walk into some
// directory they did not want just to change machine — while the form's FIRST field is the host and the
// pre-filled path is a default rather than a decision. Found by an end-to-end case whose walk stopped on
// the volume line, which is the first line with an arrow and a slash on it.
func TestNOnAVolumeOpensTheFormForThatHost(t *testing.T) {
	m, node := treeNodeAt(t, treeModelOpened(t), "nuc")
	if node.Path != "" {
		t.Fatalf("the `nuc` volume carries a path (%q), so this case is not testing a volume", node.Path)
	}
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = out.(model)
	if m.mode != modeLaunch {
		t.Fatalf("n on a volume did not open the launch form: mode %v, note %q", m.mode, m.note)
	}
	screen := m.View()
	if !strings.Contains(screen, "host:  nuc") && !strings.Contains(screen, "host: nuc") {
		t.Errorf("the form does not name the volume as its host:\n%s", screen)
	}
	// And no directory is invented for it: the field is EMPTY, which the form draws as its own hint
	// (`(path to the project)`) rather than as a value. An absolute path here would be a guess
	// presented as a default, which is worse than an empty field the operator has to fill.
	for _, ln := range strings.Split(screen, "\n") {
		if !strings.Contains(ln, "dir:") {
			continue
		}
		if got := strings.SplitN(ln, "dir:", 2)[1]; strings.Contains(got, "/") {
			t.Errorf("the form invented a directory for a volume: %q", strings.TrimSpace(got))
		}
	}
}

// `n` ON THE FAVOURITES NODE REFUSES WITH A SENTENCE, because the favourites node is not a directory.
// Creating a session "in" a logical band makes no sense.
func TestNOnFavouritesNodeRefuses(t *testing.T) {
	m := treeModelOpened(t)
	// Initialize favourites and pin a row to create the favourites node.
	favs, err := fav.Open(filepath.Join(t.TempDir(), "favourites.json"))
	if err != nil {
		t.Fatalf("fav.Open: %v", err)
	}
	m.favs = favs

	// Pin a row so the favourites node appears.
	tls := m.treeShown()
	for i, l := range tls {
		if l.IsRow {
			m = m.treeTo(tls, i)
			out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
			m = out.(model)
			break
		}
	}

	// Now find the favourites node.
	tls = m.treeShown()
	at := -1
	for i, l := range tls {
		if !l.IsRow && l.Pinned {
			at = i
			break
		}
	}
	if at < 0 {
		t.Fatal("pinning a row did not create a favourites node")
	}
	m = m.treeTo(tls, at)

	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	got := out.(model).note

	if got == "" {
		t.Error("n on the favourites node set no note")
	}
	if !strings.Contains(got, "not a directory") && !strings.Contains(got, "band") {
		t.Errorf("n on favourites says %q, want it to say the band is not a directory", got)
	}
}

// `q` QUITS FROM THE TREE, returning tea.Quit. This is the default screen, so a `q` that did nothing
// would leave the operator with no way out of the program from the first thing they see.
func TestQQuitsFromTheTree(t *testing.T) {
	m := treeModel(t)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})

	if cmd == nil {
		t.Fatal("q returned nil cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("q returned %T, want tea.QuitMsg", cmd())
	}
}

// EVERY KEY THAT CANNOT ACT NAMES ITSELF in the refusal note. A table over the keys a directory line
// refuses (`space`, `x`, `f`, `N`, and `n` on the favourites band): the note must be non-empty AND
// must contain the key's own name or the word that tells the operator what to press instead.
//
// Without this the operator presses a key and sees nothing, which reads as "the key is broken."
func TestRefusedKeysNameThemselves(t *testing.T) {
	cases := []struct {
		key      string
		keyMsg   tea.KeyMsg
		needle   string // what the note must contain to pass
		skipNode bool   // if true, only test on favourites node
	}{
		{key: "space", keyMsg: tea.KeyMsg{Type: tea.KeySpace}, needle: "space"},
		{key: "x", keyMsg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}, needle: "x"},
		{key: "f", keyMsg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")}, needle: "f"},
		{key: "N", keyMsg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")}, needle: "N"},
		{key: "n-favourites", keyMsg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")},
			needle: "not a directory", skipNode: true},
	}

	for _, tc := range cases {
		m := treeModelOpened(t)

		// For the favourites case, we need to create the favourites node.
		if tc.skipNode {
			favs, err := fav.Open(filepath.Join(t.TempDir(), "favourites.json"))
			if err != nil {
				t.Fatalf("%s: fav.Open: %v", tc.key, err)
			}
			m.favs = favs

			// Pin a row to create the favourites node.
			tls := m.treeShown()
			for i, l := range tls {
				if l.IsRow {
					m = m.treeTo(tls, i)
					out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
					m = out.(model)
					break
				}
			}
		}

		tls := m.treeShown()
		at := -1
		// Find the appropriate node: either any directory node or specifically the favourites node.
		for i, l := range tls {
			if !l.IsRow {
				if tc.skipNode && !l.Pinned {
					continue
				}
				at = i
				break
			}
		}
		if at < 0 {
			t.Errorf("%s: fixture has no suitable node", tc.key)
			continue
		}

		m = m.treeTo(tls, at)
		out, _ := m.Update(tc.keyMsg)
		note := out.(model).note

		// The node is named in the message, because this table tests TWO shapes — a directory for the
		// session keys and the favourites band for `n` — and "on a directory node" was printed for
		// both, which sends the next reader to the wrong line.
		where := tls[at].Label
		if note == "" {
			t.Errorf("%s on %q set no note — the operator sees silence", tc.key, where)
		}
		if !strings.Contains(note, tc.needle) {
			t.Errorf("%s on %q says %q, want it to contain %q", tc.key, where, note, tc.needle)
		}
	}
}

// `C` CLEARS A SELECTION MADE FROM THE TREE. The selection and the map tracking snapshots must both be
// cleared, and the note must say what happened.
func TestCClearsSelection(t *testing.T) {
	m := treeModelOpened(t)
	tls := m.treeShown()
	// Mark a few rows.
	marked := 0
	for i, l := range tls {
		if l.IsRow && marked < 3 {
			m = m.treeTo(tls, i)
			out, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
			m = out.(model)
			marked++
		}
	}
	if marked == 0 {
		t.Fatal("fixture has no rows to mark")
	}
	if m.sel.Len() == 0 {
		t.Fatal("marking rows did not populate m.sel")
	}

	// Clear it.
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("C")})
	m = out.(model)

	if m.sel.Len() != 0 {
		t.Errorf("C left %d items in the selection, want 0", m.sel.Len())
	}
	if len(m.atSelection) != 0 {
		t.Errorf("C left %d items in atSelection, want 0", len(m.atSelection))
	}
	if !strings.Contains(m.note, "clear") {
		t.Errorf("C says %q, want it to say the selection was cleared", m.note)
	}
}
