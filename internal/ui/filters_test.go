package ui

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// Two narrowings the operator asked for: `*` shows only what they pinned, and `/` keeps only rows
// whose name or project answers a keyword (docs/design.md §21.16).
//
// They are NOT modes of `v`. `v` answers "how are rows GROUPED" and these answer "which rows are
// SHOWN" — folding them into one three-position cycle would make "only my favourites, grouped by
// project" unreachable, and that combination is the point of having both.
//
// Both live in rowsForScreen beside the project filter, never in visibleRows: that function is the
// only input to sel.Prune, so a narrowing placed there would silently drop a MARK the moment a row
// stopped matching — the rule §21.5 already states for the project filter.

// typeSearch performs the whole gesture: `/`, then the text one key at a time, then enter. One key
// at a time because an injected run arrives as ONE tea.KeyMsg whose String() is the whole string, so
// typing a word proves nothing about what any single key means.
func typeSearch(t *testing.T, m model, text string) model {
	t.Helper()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = next.(model)
	if m.mode != modeSearch {
		t.Fatalf("`/` did not open the search field; mode = %v", m.mode)
	}
	for _, r := range text {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(model)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return next.(model)
}

func filterRow(host, session, alias, path string, fav bool, st state.State) registry.Pane {
	return registry.Pane{Kind: registry.KindPane, Host: host, PaneID: "%" + session[:1],
		SessionID: "$1", Session: session, Command: "sh", Path: path, ClassifiedState: st}
}

// filterModel is a fleet of four rows with two of them pinned, which is the smallest fleet that can
// tell "only favourites" from "everything" and from "nothing".
func filterModel(t *testing.T) model {
	t.Helper()
	rows := []registry.Pane{
		filterRow("local", "cicd-billing", "", "/w/billing-iac", true, state.Needs),
		filterRow("local", "gis-reader", "", "/w/xmap", true, state.Works),
		filterRow("nuc", "envoy-ops", "", "/w/ops", false, state.Idle),
		filterRow("nuc", "рендеринг-карты", "", "/w/maps", false, state.Quiet),
	}
	m := modelWith(t, rows...)
	m.width, m.height = 100, 40
	m.favs = favSetWith(t, rows[0], rows[1])
	return m
}

func rowNames(m model) []string {
	var out []string
	for _, p := range m.rowsForScreen() {
		out = append(out, p.Session)
	}
	return out
}

// `*` keeps the pinned rows and nothing else.
func TestStarShowsOnlyTheFavourites(t *testing.T) {
	m := filterModel(t)
	if got := len(rowNames(m)); got != 4 {
		t.Fatalf("the fixture starts with %d rows, want 4", got)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("*")})
	m = next.(model)

	got := rowNames(m)
	if len(got) != 2 {
		t.Fatalf("`*` left %d rows, want the 2 pinned ones: %v", len(got), got)
	}
	for _, want := range []string{"cicd-billing", "gis-reader"} {
		if !slices.Contains(got, want) {
			t.Errorf("`*` dropped the pinned row %q: %v", want, got)
		}
	}
	// And again puts everything back: a filter the operator cannot leave is a trap.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("*")})
	if got := len(rowNames(next.(model))); got != 4 {
		t.Errorf("a second `*` left %d rows, want all 4", got)
	}
}

// `*` on a fleet with NOTHING pinned refuses and says how to pin, rather than emptying the screen.
// An empty list is indistinguishable from a fleet that went away, which is the class this repo has
// paid for more than once.
func TestStarWithNothingPinnedRefusesAndSaysHow(t *testing.T) {
	rows := []registry.Pane{filterRow("local", "cicd-billing", "", "/w/x", false, state.Needs)}
	m := modelWith(t, rows...)
	m.width, m.height = 100, 40

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("*")})
	got := next.(model)
	if got.favouritesOnly {
		t.Error("`*` turned on with nothing pinned, so the screen went empty for no reason")
	}
	if !strings.Contains(got.note, "f") {
		t.Errorf("the refusal does not say which key pins a row: %q", got.note)
	}
	if len(rowNames(got)) != 1 {
		t.Errorf("the row left the screen anyway: %v", rowNames(got))
	}
}

// `/` filters by keyword, case-insensitively, and the fold has to work for the Cyrillic names this
// fleet actually has — a session here is named after the prompt that started it.
func TestSearchMatchesByKeywordCaseInsensitively(t *testing.T) {
	for _, c := range []struct {
		query string
		want  []string
	}{
		{"cicd", []string{"cicd-billing"}},
		{"CICD", []string{"cicd-billing"}},
		{"РЕНДЕР", []string{"рендеринг-карты"}},
		{"рендер", []string{"рендеринг-карты"}},
		{"xmap", []string{"gis-reader"}},                  // by PROJECT path, not by name
		{"nuc", []string{"envoy-ops", "рендеринг-карты"}}, // by host
		{"zzz", nil},
	} {
		m := filterModel(t)
		m = typeSearch(t, m, c.query)
		got := rowNames(m)
		if len(got) != len(c.want) {
			t.Errorf("%q left %d rows, want %d: %v", c.query, len(got), len(c.want), got)
			continue
		}
		for _, w := range c.want {
			if !slices.Contains(got, w) {
				t.Errorf("%q did not find %q: %v", c.query, w, got)
			}
		}
	}
}

// A row the operator RENAMED must be findable by both names: the one they typed and the one the
// product derived. Searching only the display name would lose the tmux name the moment an alias
// hides it, which is exactly when the operator reaches for the old word they remember.
func TestSearchFindsBothTheAliasAndTheDerivedName(t *testing.T) {
	base := filterModel(t)
	row := base.panes[0] // cicd-billing
	var al project.Aliases
	al.Set(project.AliasKeyOf(row), "прод-выкатка")
	for _, q := range []string{"прод", "cicd"} {
		m := filterModel(t)
		m.aliases = al
		m = typeSearch(t, m, q)
		if got := rowNames(m); len(got) != 1 || got[0] != "cicd-billing" {
			t.Errorf("%q found %v, want just the renamed row (its alias AND its derived name "+
				"must both match)", q, got)
		}
	}
}

// The two narrowings COMPOSE: only pinned rows that also answer the keyword.
func TestStarAndSearchCompose(t *testing.T) {
	m := filterModel(t)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("*")})
	m = typeSearch(t, next.(model), "gis")
	if got := rowNames(m); len(got) != 1 || got[0] != "gis-reader" {
		t.Errorf("`*` plus a keyword left %v, want only the pinned row that matches", got)
	}
}

// A MARK survives a narrowing, because the narrowing decides what is SHOWN and the hidden set
// decides what EXISTS (§21.5). A filter that pruned marks would drop them mid-compose, which is why
// neither of these belongs in visibleRows.
func TestANarrowingDoesNotDropAMark(t *testing.T) {
	m := filterModel(t)
	m.sel.Toggle(selKey(m.panes[2])) // envoy-ops, which the next filter removes
	if m.sel.Len() != 1 {
		t.Fatalf("the fixture failed to mark a row: %d", m.sel.Len())
	}
	m = typeSearch(t, m, "cicd")
	if m.sel.Len() != 1 {
		t.Errorf("the mark was pruned by a narrowing: %d selected", m.sel.Len())
	}
	if n := m.selectedOutsideFilter(); n != 1 {
		t.Errorf("selectedOutsideFilter = %d, want 1 — a mark the screen cannot show must "+
			"still be counted, or `enter` sends somewhere nobody was told about", n)
	}
}

// `esc` means WIDEN, and it must widen every narrowing rather than one of them: an operator who
// pressed two keys should not have to remember which one esc undoes.
func TestEscWidensEveryNarrowing(t *testing.T) {
	m := filterModel(t)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("*")})
	m = typeSearch(t, next.(model), "cicd")
	if len(rowNames(m)) != 1 {
		t.Fatalf("the fixture is not narrowed: %v", rowNames(m))
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if got := rowNames(m); len(got) != 4 {
		t.Errorf("esc left %d rows, want all 4: %v", len(got), got)
	}
	if m.favouritesOnly || m.searchQuery() != "" {
		t.Errorf("esc left a narrowing on: favouritesOnly=%v query=%q",
			m.favouritesOnly, m.searchQuery())
	}
}

// The footer SAYS which narrowings are on and what they cost, always with numbers — a mode that
// changes what the screen means must change the sentence, never return a zero (the lesson `X` paid
// for). And the query is BOUNDED: a session here is named after the prompt that started it, so an
// interpolated one runs to 88 columns and would eat the fleet line.
func TestTheFooterNamesTheNarrowingsAndIsBounded(t *testing.T) {
	m := filterModel(t)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("*")})
	m = typeSearch(t, next.(model), "cicd")

	out := m.View()
	for _, want := range []string{"★", "cicd", "1 of 4"} {
		if !strings.Contains(out, want) {
			t.Errorf("the footer does not say %q:\n%s", want, out)
		}
	}
	// A long query at the size §16 commits to.
	long := "давай раскатаем dockerfile goldens по всем окружениям и проверим ci"
	for _, cols := range []int{80, 100, 200} {
		m := filterModel(t)
		m.width, m.height = cols, 40
		m = typeSearch(t, m, long)
		for _, l := range strings.Split(m.View(), "\n") {
			if w := lines.Width(l); w > cols {
				t.Errorf("%d cols: a %d-column line — the query was interpolated unbounded: %q",
					cols, w, l)
			}
		}
	}
}

// A narrowing that empties the screen must say WHICH one did it and how to widen. An empty list is
// indistinguishable from a fleet that went away, and the remedy is the one thing to say.
func TestAnEmptyResultSaysWhyAndHowToWiden(t *testing.T) {
	m := filterModel(t)
	m = typeSearch(t, m, "nothing-matches-this")
	out := m.View()
	if len(rowNames(m)) != 0 {
		t.Fatalf("the fixture is not empty: %v", rowNames(m))
	}
	for _, want := range []string{"esc"} {
		if !strings.Contains(out, want) {
			t.Errorf("an empty screen does not say how to widen (%q):\n%s", want, out)
		}
	}
	if !strings.Contains(out, "nothing-matches-this") {
		t.Errorf("an empty screen does not say what was searched for:\n%s", out)
	}
}

// While the field has focus every key is TEXT. `j` must type a `j`, not move the cursor — the rule
// this repo wrote down after a mutant that made `q` quit inside the composer survived a test that
// typed a whole word.
func TestInsideTheSearchFieldEveryKeyIsText(t *testing.T) {
	m := filterModel(t)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = next.(model)
	if m.mode != modeSearch {
		t.Fatalf("`/` did not open the field; mode = %v", m.mode)
	}
	for _, k := range []string{"j", "k", "q", "*", "x"} {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		m = next.(model)
		if m.mode != modeSearch {
			t.Fatalf("%q left the search field; mode = %v", k, m.mode)
		}
	}
	if got := m.searchQuery(); got != "jkq*x" {
		t.Errorf("the field holds %q, want every key as text", got)
	}
}

// A query of TWO WORDS must arrive as two words.
//
// bubbletea reports a space as KeySpace with Runes ALSO set, and the first version of this handler
// inserted both — `Insert(string(msg.Runes))` and then a second `Insert(" ")` for the space — so
// `two words` became `two  words` and matched nothing on a fleet where no name has a double space.
// Calibrated against exactly that: restoring the second insert fails these two.
func TestASpaceInTheQueryIsInsertedOnce(t *testing.T) {
	m := filterModel(t)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = next.(model)
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("a")},
		{Type: tea.KeySpace, Runes: []rune(" ")},
		{Type: tea.KeyRunes, Runes: []rune("b")},
	} {
		next, _ = m.Update(k)
		m = next.(model)
	}
	if got := m.search.Text(); got != "a b" {
		t.Errorf("the field holds %q, want %q", got, "a b")
	}
}

// And a two-word keyword actually matches a two-word name, which is the property the doubled space
// silently broke: nothing on this fleet contains a double space.
func TestATwoWordQueryMatches(t *testing.T) {
	row := filterRow("local", "count critical actions", "", "/w/x", false, state.Needs)
	m := modelWith(t, row)
	m.width, m.height = 100, 40
	m = typeSearch(t, m, "critical actions")
	if got := rowNames(m); len(got) != 1 {
		t.Errorf("a two-word keyword found %v, want the row whose name contains both words", got)
	}
}

// A project filter is a narrowing like the other two, and the footer says so.
//
// It did not. Measured on 8 rows before this: `enter` on a project drew 3 rows under a header
// reading `tmux-hub  3 sessions` and a footer reading `local up · nuc up`, so the one surface that
// could have said the screen was narrowed reported a number indistinguishable from a smaller fleet
// — the same shape as `hiddenStats` returning 0,0 while `X` was on. `tab` cycles projects with no
// label at all, which is the case that has no memory to fall back on.
func TestTheFooterNamesTheProjectTheListIsFilteredTo(t *testing.T) {
	var rows []registry.Pane
	for i := 0; i < 8; i++ {
		p := agentRow(string(rune('a'+i))+"aaaaaaa", "sess-"+string(rune('a'+i)),
			"background", state.Idle)
		p.Path = "/w/proj-one"
		if i >= 3 {
			p.Path = "/w/proj-two"
		}
		rows = append(rows, p)
	}
	m := base(t, 100, 24, rows...)
	m = m.openProject(m.rules.OfPane(rows[0]).ID, false)
	if got := len(m.rowsForScreen()); got != 3 {
		t.Fatalf("the fixture does not narrow: %d rows", got)
	}
	foot := lastLine(m.View())
	if !strings.Contains(foot, "proj-one") {
		t.Errorf("the footer does not name the project the list is filtered to: %q", foot)
	}
	if !strings.Contains(foot, "3 of 8") {
		t.Errorf("the footer does not say what the filter costs — a narrowed list that does not "+
			"count is a list that lies about the fleet: %q", foot)
	}
	// And the fleet is still there, so the host line cannot have been pushed off by it.
	if !strings.Contains(foot, "local up") {
		t.Errorf("the narrowing took the host line with it: %q", foot)
	}
}

// The two predicates that answer "is the list narrowed" must agree over every combination. They
// cannot be one function — one is a view model the renderer holds, the other a question about the
// model — so this is what keeps a fourth narrowing from being added to one and not the other. The
// project filter WAS added to one and not the other, which is the defect above.
func TestTheTwoNarrowingPredicatesAgreeOverEveryCombination(t *testing.T) {
	row := agentRow("aaaaaaaa", "sess-a", "background", state.Idle)
	row.Path = "/w/proj-one"
	for _, proj := range []bool{false, true} {
		for _, fav := range []bool{false, true} {
			for _, q := range []string{"", "sess"} {
				m := base(t, 100, 24, row)
				if proj {
					m = m.openProject(m.rules.OfPane(row).ID, false)
				}
				if fav {
					m.favs = favSetWith(t, row)
					m.favouritesOnly = true
				}
				if q != "" {
					m.search.Insert(q)
				}
				shown := m.rowsForScreen()
				want := m.narrowed()
				got := m.filters(shown, len(m.visibleRows()), false).on()
				if got != want {
					t.Errorf("project=%v favourites=%v query=%q: narrowed()=%v but the tally "+
						"says on()=%v — one of them has a narrowing the other does not",
						proj, fav, q, want, got)
				}
			}
		}
	}
}

// lastLine is the footer: the last non-empty line of a frame.
func lastLine(screen string) string {
	ls := strings.Split(screen, "\n")
	for i := len(ls) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(ls[i]); t != "" {
			return t
		}
	}
	return ""
}

// The search FIELD carries the count while it has focus, not only after enter.
//
// The list narrows live, so the number is already true on the screen above the field — and without it
// the operator types a keyword and cannot tell a narrowing to one row from one that matched nothing
// until they commit it. Measured on a real journey: the field read `search: journey-naming-e2e▏ ·
// enter: keep · esc: cancel` and said nothing about what the word had done.
func TestTheSearchFieldSaysWhatTheKeywordHasDone(t *testing.T) {
	got := searchLine("cicd", 3, 45, 100, false)
	if !strings.Contains(got, "3 of 45") {
		t.Errorf("the field does not carry the count: %q", got)
	}
	if !strings.Contains(got, "cicd") {
		t.Errorf("the field does not show what is being typed: %q", got)
	}
	if lines.Width(got) > 100 {
		t.Errorf("the field is %d columns at 100: %q", lines.Width(got), got)
	}
	// An EMPTY field says nothing about a count, because there is no narrowing yet and `0 of 45`
	// would read as a fleet that vanished.
	if empty := searchLine("", 45, 45, 100, false); strings.Contains(empty, " of ") {
		t.Errorf("an empty field claims a count: %q", empty)
	}
	// At the committed width the KEYS yield before the field does — a field whose end you cannot see
	// is a field you cannot type in, which is the rule this line already had.
	narrow := searchLine("a-very-long-keyword-being-typed-right-now", 1, 45, 80, false)
	if lines.Width(narrow) > 80 {
		t.Errorf("the field is %d columns at 80: %q", lines.Width(narrow), narrow)
	}
	if !strings.Contains(narrow, "▏") {
		t.Errorf("the caret was cut off, so the operator cannot see where they are typing: %q", narrow)
	}
}
