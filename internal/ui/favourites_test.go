package ui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/agents"
	"github.com/DawnBreather/tmux-hub/internal/fav"
	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// Favourites do not reorder the fleet, they SPLIT it in two: every pinned row above every unpinned
// one, with the attention order untouched inside each half. That is what makes the promise usable —
// the operator still reads the pinned band top-down by who wants them most.

func favModel(t *testing.T, rows ...registry.Pane) model {
	t.Helper()
	m := base(t, 100, 40, rows...)
	m.favs = favSetWith(t)
	return m
}

// favSetWith is a favourites set holding exactly these rows, built through the real store so the
// key it writes is the one the product writes. With no rows it is an empty set, which is what most
// cases want — three of them opened one by hand before this existed.
func favSetWith(t *testing.T, rows ...registry.Pane) *fav.Set {
	t.Helper()
	set, err := fav.Open(filepath.Join(t.TempDir(), "favourites.json"))
	if err != nil {
		t.Fatalf("fav.Open: %v", err)
	}
	for _, r := range rows {
		if err := set.ToggleSession(r); err != nil {
			t.Fatalf("ToggleSession: %v", err)
		}
	}
	return set
}

// rowOrder is the sessions of the painted set, in order.
func rowOrder(m model) []string {
	var out []string
	for _, p := range m.rowsForScreen() {
		out = append(out, p.Session)
	}
	return out
}

func TestAPinnedSessionRisesAboveEverythingElse(t *testing.T) {
	m := favModel(t,
		pane("local", "w", "claude", 1, "claude", state.Needs),      // needs: the top by attention
		agentRow("30f3382b", "quiet-one", "background", state.Idle), // idle: near the bottom
		agentRow("45dfef2f", "busy-one", "background", state.Works))

	before := rowOrder(m)
	if before[0] == "quiet-one" {
		t.Fatalf("the fixture already leads with the row to pin: %v", before)
	}

	// Pin the QUIETEST row, which attention would keep at the bottom.
	var quiet registry.Pane
	for _, p := range m.panes {
		if p.Session == "quiet-one" {
			quiet = p
		}
	}
	if err := m.favs.ToggleSession(quiet); err != nil {
		t.Fatal(err)
	}
	if got := rowOrder(m); got[0] != "quiet-one" {
		t.Errorf("the pinned row is not first: %v", got)
	}
	// And the rest keep the order they had.
	rest := rowOrder(m)[1:]
	var wantRest []string
	for _, s := range before {
		if s != "quiet-one" {
			wantRest = append(wantRest, s)
		}
	}
	if strings.Join(rest, ",") != strings.Join(wantRest, ",") {
		t.Errorf("the unpinned rows were reordered: got %v, want %v", rest, wantRest)
	}
}

// Pinning a PROJECT lifts every session in it, which is the whole point of pinning a project rather
// than four sessions.
func TestPinningAProjectLiftsEverySessionInIt(t *testing.T) {
	one := agentRow("aaaaaaaa", "iac-one", "background", state.Idle)
	one.Path = "/w/billing-iac"
	two := agentRow("bbbbbbbb", "iac-two", "background", state.Quiet)
	two.Path = "/w/billing-iac"
	other := pane("local", "w", "claude", 1, "claude", state.Needs)
	other.Path = "/w/somewhere-else"

	m := favModel(t, one, two, other)
	id := m.rules.OfPane(one).ID
	if id != m.rules.OfPane(two).ID {
		t.Fatalf("the fixture's two rows are not one project: %q vs %q", id, m.rules.OfPane(two).ID)
	}
	if err := m.favs.ToggleProject(id); err != nil {
		t.Fatal(err)
	}

	got := rowOrder(m)
	if len(got) != 3 {
		t.Fatalf("rows = %v", got)
	}
	if got[2] != "sess1" {
		t.Errorf("the unpinned row is not last: %v", got)
	}
	// Both pinned rows are above it, and the attention order between them SURVIVES. The order is
	// `quiet` before `idle` — read off the constants rather than from memory, where I had it the
	// other way round: `Needs, Error, Quiet, Idle, Works, Unknown, Done, Gone` (internal/state), so a
	// pane silent long enough to be suspect outranks one sitting at a prompt.
	if got[0] != "iac-two" || got[1] != "iac-one" {
		t.Errorf("the pinned project's rows are not in attention order: %v", got)
	}
}

// `f` pins the row under the cursor and SAYS so — and it acts on the cursor, never on the selection,
// which is the one place it differs from `x` on purpose.
func TestFPinsTheRowUnderTheCursorAndSaysSo(t *testing.T) {
	m := favModel(t,
		pane("local", "w", "claude", 1, "claude", state.Needs),
		agentRow("30f3382b", "cicd", "background", state.Idle))
	m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%1"})
	m = m.cursorTo(1) // the agent row, which is NOT the selected one

	under, ok := m.cursorRow()
	if !ok {
		t.Fatal("no row under the cursor")
	}
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	after := out.(model)

	if !after.favs.HasSession(under) {
		t.Errorf("`f` did not pin the row under the cursor (%s)", under.Session)
	}
	if after.favs.HasSession(m.panes[0]) && m.panes[0].Session != under.Session {
		t.Error("`f` pinned the SELECTED row as well — pinning is about one thing the operator keeps " +
			"coming back to, and a selection-wide pin cannot be undone by pressing f again")
	}
	if !strings.Contains(after.note, "pinned "+under.Session) {
		t.Errorf("the note does not say what was pinned: %q", after.note)
	}
	if !strings.Contains(after.note, "stays above the rest") {
		t.Errorf("the note does not say what pinning DOES: %q", after.note)
	}

	// And again unpins, with its own sentence.
	out2, _ := after.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	back := out2.(model)
	if back.favs.HasSession(under) {
		t.Error("a second `f` did not unpin")
	}
	if !strings.Contains(back.note, "unpinned "+under.Session) {
		t.Errorf("the note does not say it came off: %q", back.note)
	}
}

// The star and the ORDER come from one predicate, so a row that rose must also be marked. A screen
// where the two disagreed would be worse than either alone: the operator could not tell whether the
// band at the top was the pinned one.
func TestThePinnedRowCarriesItsStar(t *testing.T) {
	m := favModel(t,
		pane("local", "w", "claude", 1, "claude", state.Needs),
		agentRow("30f3382b", "cicd", "background", state.Idle))
	var agent registry.Pane
	for _, p := range m.panes {
		if p.Session == "cicd" {
			agent = p
		}
	}
	if err := m.favs.ToggleSession(agent); err != nil {
		t.Fatal(err)
	}
	screen := m.View()
	row := inboxRow(t, screen, "cicd")
	if !strings.Contains(row, favouriteMark) {
		t.Errorf("the pinned row carries no %s: %q", favouriteMark, row)
	}
	if other := inboxRow(t, screen, "claude"); strings.Contains(other, favouriteMark) {
		t.Errorf("an unpinned row is marked: %q", other)
	}
	// The row that rose IS the marked one, on the same frame.
	if got := rowOrder(m)[0]; got != "cicd" {
		t.Errorf("the marked row did not rise: %v", rowOrder(m))
	}
}

// `f` on the project list pins the PROJECT under the cursor, and the list shows it.
func TestFOnTheProjectListPinsTheProject(t *testing.T) {
	one := agentRow("aaaaaaaa", "iac-one", "background", state.Idle)
	one.Path = "/w/billing-iac"
	m := favModel(t, one, pane("local", "w", "claude", 1, "claude", state.Needs))
	m.mode = modeProjects

	rows := m.projectRows()
	if len(rows) == 0 {
		t.Fatal("the fixture has no projects")
	}
	m.projCursor = 0
	want := rows[0].Group

	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	after := out.(model)
	if !after.favs.HasProject(want.ID) {
		t.Errorf("`f` did not pin %q", want.Label)
	}
	if !strings.Contains(after.note, "pinned "+want.Label) {
		t.Errorf("the note does not name the project: %q", after.note)
	}
	if screen := after.View(); !strings.Contains(screen, favouriteMark) {
		t.Errorf("the project list does not show the pin:\n%s", screen)
	}
}

// A hub with no favourites set is a working hub: the key says so instead of doing nothing, and
// nothing anywhere branches on the absence.
func TestAHubWithNoFavouritesSaysSoRatherThanIgnoringTheKey(t *testing.T) {
	m := base(t, 100, 40, pane("local", "w", "claude", 1, "claude", state.Needs))
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if cmd != nil {
		t.Error("`f` on a hub with no favourites returned a command")
	}
	if note := out.(model).note; !strings.Contains(note, "no favourites") {
		t.Errorf("the key was silent: %q", note)
	}
	// And the order is untouched.
	if got := len(out.(model).rowsForScreen()); got != 1 {
		t.Errorf("%d rows", got)
	}
}

// A row with no session NAME cannot be keyed — a key of ("", host) would match every other nameless
// row on that host — so it is refused rather than written.
func TestARowWithNoNameIsRefusedRatherThanKeyedOnNothing(t *testing.T) {
	nameless := registry.Pane{Kind: registry.KindPane, Host: "local", PaneID: "%7",
		ClassifiedState: state.Idle}
	m := favModel(t, nameless)
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	after := out.(model)
	if after.favs.HasSession(nameless) {
		t.Error("a nameless row was pinned, so every other nameless row on that host now reads as pinned")
	}
	if !strings.Contains(after.note, "no session name") {
		t.Errorf("the refusal does not say why: %q", after.note)
	}
}

// The pinned set survives a reopen through the REAL path — the file, not a hand-built set. A loader
// that forgot a field would pass every test that assigned the field itself.
func TestPinsSurviveThroughTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "favourites.json")
	first, err := fav.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m := base(t, 100, 40, agentRow("30f3382b", "cicd", "background", state.Idle))
	m.favs = first
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	_ = out

	second, err := fav.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m2 := base(t, 100, 40, agentRow("30f3382b", "cicd", "background", state.Idle))
	m2.favs = second
	if got := m2.favouriteKeys(); len(got) != 1 {
		t.Errorf("the pin did not come back through the file: %v", got)
	}
	if screen := m2.View(); !strings.Contains(screen, favouriteMark) {
		t.Errorf("the reopened hub does not mark the pinned row:\n%s", screen)
	}
}

// The project list's own summary counts what is pinned, because the operator who pinned four things
// across two screens has no other way to ask how many.
func TestTheProjectListSaysHowManyArePinned(t *testing.T) {
	one := agentRow("aaaaaaaa", "iac-one", "background", state.Idle)
	one.Path = "/w/billing-iac"
	m := favModel(t, one)
	m.mode = modeProjects
	if err := m.favs.ToggleProject(m.rules.OfPane(one).ID); err != nil {
		t.Fatal(err)
	}
	if err := m.favs.ToggleSession(one); err != nil {
		t.Fatal(err)
	}
	screen := m.View()
	if !strings.Contains(screen, "1 favourite session") || !strings.Contains(screen, "1 favourite project") {
		t.Errorf("the list does not count the pins:\n%s", screen)
	}
}

// The pin note is a footer claimant like any other, so a session named after its own prompt must not
// push the clause that says what pinning DOES off the line. Measured live at 100+ columns before
// shortSubject reached it.
func TestThePinNoteFitsTheCommittedWidth(t *testing.T) {
	long := "20260803--store-online-takes-too-long-to-ci-cd-troubleshooting ⑂ давай раскатаем " +
		"Dockerfile goldens - по всему флоту"
	m := favModel(t, agentRow("30f3382b", long, "background", state.Idle))
	m = m.cursorTo(0)
	for _, want := range []string{"stays above the rest", "unpinned"} {
		out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
		m = out.(model)
		if lines.Width(m.note) > 80 {
			t.Errorf("the note is %d columns, so the footer drops its tail: %q",
				lines.Width(m.note), m.note)
		}
		if want == "stays above the rest" && !strings.Contains(m.note, want) {
			t.Errorf("the note does not say what pinning does: %q", m.note)
		}
	}
}

// The pin survives the door, THROUGH THE PRODUCER. This is the case the operator's report earned:
// "after attaching to a favourite session it stops being in the list" — which is what a row dropping
// out of the pinned band looks like when there are forty others.
//
// It goes through registry.UpdateAgents, Update and SetClaudeSession rather than building the two row
// shapes by hand, because the point is that the SAME session produces two different shapes and the
// registry is what turns one into the other. A hand-built pair would have agreed with whatever the key
// happened to be.
func TestAPinSurvivesTheDoorThroughTheRegistry(t *testing.T) {
	const uuid = "30f3382b-f68c-4baf-98fd-68d4fd1c3da4"
	now := time.Unix(1786450000, 0)
	listing := []agents.Session{{ID: "30f3382b", SessionID: uuid, Kind: "background",
		Name: "20260817-cicd", CWD: "/w/iac", State: "blocked", StartedAt: now}}

	reg := registry.New()
	reg.SetHostOrder([]string{"local"})
	reg.UpdateAgents("local", listing, now)

	set := favSetWith(t)
	paneLess := reg.Panes()[0]
	if paneLess.Kind != registry.KindAgent {
		t.Fatalf("the fixture is not a pane-less row: %+v", paneLess)
	}
	if err := set.ToggleSession(paneLess); err != nil {
		t.Fatal(err)
	}

	// The door: a pane appears, the hub adopts it with the row's own uuid, and the next agents poll
	// folds the pane-less row into that pane.
	reg.Update("local",
		[]tmux.Delta{{PaneID: "%9", Activity: now.Unix(), PaneHeight: 24, WindowWidth: 80,
			PanePID: 7, SessionID: "$3", WindowID: "@7"}},
		map[string]tmux.Labels{"%9": {Session: "20260817-cicd-30f3382b", Window: "sh",
			Command: "claude"}},
		[]tmux.Capture{{PaneID: "%9", Height: 24, Lines: []string{"❯"}}},
		map[string]tmux.Capture{"%9": {PaneID: "%9", Height: 24, Lines: []string{"❯"}}},
		now, time.Second)
	reg.SetClaudeSession("local", "%9", uuid)
	reg.UpdateAgents("local", listing, now.Add(time.Second))

	rows := reg.Panes()
	if len(rows) != 1 || rows[0].Kind != registry.KindPane {
		t.Fatalf("the join did not happen: %d rows, first kind %q", len(rows), rows[0].Kind)
	}
	woken := rows[0]
	if woken.Session == paneLess.Session {
		t.Fatalf("the fixture does not exercise the defect: the name did not change (%q)", woken.Session)
	}
	if !set.HasSession(woken) {
		t.Errorf("the pin came off when the session gained a pane — %q pinned, %q not:\n  %+v\n  %+v",
			paneLess.Session, woken.Session, fav.KeyOf(paneLess), fav.KeyOf(woken))
	}

	// And it still RISES, which is what the operator noticed the loss of.
	m := base(t, 100, 40, woken, pane("local", "other", "claude", 2, "claude", state.Needs))
	m.favs = set
	if got := rowOrder(m); got[0] != woken.Session {
		t.Errorf("the woken favourite is not first: %v", got)
	}
	// By the name the row DRAWS: after the join the row reads by the CLAUDE session's name (the
	// absorb branch copies it into AgentName and DisplayName prefers it), so the tmux session the
	// door created appears nowhere on the row — which is itself the right answer and is what
	// rowNeedle builds. It used to be found by pane id, which this row no longer carries: its
	// session puts one row on the screen, so the id distinguishes nothing.
	if screen := m.View(); !strings.Contains(inboxRow(t, screen, rowNeedle(woken, m.panes)), favouriteMark) {
		t.Errorf("the woken favourite carries no star:\n%s", screen)
	}
	// On the ROW, and in the case the source gave it. This used to assert `20260817-CICD` against
	// the whole screen, which was really asserting that the row had a HEADER of its own — upper
	// case is the header convention for a derived name. A conversation now leads with its name
	// wherever it is, so the property can be asserted where it belongs, and the upper-case form
	// would pass on a screen whose row said `%9 sh`.
	if screen := m.View(); !strings.Contains(inboxRow(t, screen, rowNeedle(woken, m.panes)), "20260817-cicd") {
		t.Errorf("the woken row does not read by its Claude session's name:\n%s", screen)
	}
}
