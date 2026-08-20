package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"

	"github.com/DawnBreather/tmux-hub/internal/lines"
)

func namingModel(t *testing.T, rows ...registry.Pane) (model, string) {
	t.Helper()
	m := modelWith(t, rows...)
	path := filepath.Join(t.TempDir(), "projects.toml")
	m.projectsPath = path
	return m, path
}

// The surface is a FIXED six-row overlay at the foot — separator, subject, `now:`, field,
// reason, keys — always six, so nothing beneath it moves (docs/design.md §21.12 rule 1).
// Inline edit was ruled out because the name column is 16-17 columns at width >= 100, so the
// tail of what you type would be invisible.
func TestTheNamingOverlayIsSixRowsAndNamesItsSubject(t *testing.T) {
	m, _ := namingModel(t, pathPane("local", "/w/a", "the-session", 0, state.Needs))
	m = key(t, m, "N")
	if m.mode != modeNaming {
		t.Fatalf("N did not open the naming screen: mode = %v", m.mode)
	}
	out := m.View()
	rows := strings.Split(out, "\n")
	if len(rows) != m.height {
		t.Fatalf("the screen is %d rows, want %d", len(rows), m.height)
	}
	// The last six rows are the overlay, and the subject has to be ON it: §21.12 rule 2
	// requires the subject be visible at the moment of commit.
	tail := strings.Join(rows[len(rows)-6:], "\n")
	if !strings.Contains(tail, "the-session") {
		t.Errorf("the overlay does not name its subject:\n%s", tail)
	}
	for _, want := range []string{"now:", "enter", "esc"} {
		if !strings.Contains(tail, want) {
			t.Errorf("the overlay is missing %q:\n%s", want, tail)
		}
	}
}

// The field opens pre-filled with the current ALIAS only, never with a derived name — so
// committing an untouched field cannot silently freeze Claude's own name, or a tmux session
// name, into the file (§21.12 rule 5).
func TestTheFieldOpensWithTheAliasAndNeverADerivedName(t *testing.T) {
	row := pathPane("local", "/w/a", "claudes-own-name", 0, state.Works)
	m, _ := namingModel(t, row)
	m = key(t, m, "N")
	if got := m.naming.input.Text(); got != "" {
		t.Errorf("the field opened with %q; an unnamed row must open EMPTY or an untouched "+
			"enter would freeze the derived name into the file", got)
	}
	// Now give it a name and reopen: the field carries the operator's own name back.
	m = typeInto(t, m, "the DR plan")
	m = special(t, m, tea.KeyEnter)
	m = key(t, m, "N")
	if got := m.naming.input.Text(); got != "the DR plan" {
		t.Errorf("the field reopened with %q, want the stored alias", got)
	}
}

// Naming a row changes what every surface calls it, through the one displayName.
func TestANamedRowReadsByItsNameAndIsMarkedAsTheOperatorsOwn(t *testing.T) {
	m, path := namingModel(t, pathPane("local", "/w/a", "raw-tmux-name", 0, state.Needs))
	m = key(t, m, "N")
	m = typeInto(t, m, "the DR plan")
	m = special(t, m, tea.KeyEnter)

	if m.mode != modeBrowse {
		t.Fatalf("enter left the mode at %v", m.mode)
	}
	out := m.View()
	if !strings.Contains(out, "the DR plan") {
		t.Errorf("the dashboard does not use the name:\n%s", out)
	}
	// The marker says the name is the operator's own, so a screen cannot pass a derived
	// name off as a chosen one (§21.12 rule 6).
	if !strings.Contains(out, "»") {
		t.Errorf("the name is not marked as the operator's own:\n%s", out)
	}
	// And it reached the FILE, in the one dialect.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the file was not written: %v", err)
	}
	if !strings.Contains(string(raw), "the DR plan") {
		t.Errorf("projects.toml does not hold the name:\n%s", raw)
	}
}

// `ctrl+u`, `enter` removes a name (§21.12 rule 5), and the row goes back to its derived one.
func TestCtrlUThenEnterRemovesTheName(t *testing.T) {
	m, path := namingModel(t, pathPane("local", "/w/a", "raw-tmux-name", 0, state.Needs))
	m = key(t, m, "N")
	m = typeInto(t, m, "temporary")
	m = special(t, m, tea.KeyEnter)
	if !strings.Contains(m.View(), "temporary") {
		t.Fatal("the name was not applied")
	}

	m = key(t, m, "N")
	m = special(t, m, tea.KeyCtrlU)
	if got := m.naming.input.Text(); got != "" {
		t.Fatalf("ctrl+u left %q in the field", got)
	}
	m = special(t, m, tea.KeyEnter)
	out := m.View()
	if strings.Contains(out, "temporary") {
		t.Errorf("the name survived removal:\n%s", out)
	}
	if !strings.Contains(out, "raw-tmux-name") {
		t.Errorf("the row did not fall back to its derived name:\n%s", out)
	}
	if raw, _ := os.ReadFile(path); strings.Contains(string(raw), "temporary") {
		t.Errorf("the removed name is still in the file:\n%s", raw)
	}
}

// esc cancels and keeps nothing — a half-typed name must not be applied by leaving.
func TestEscCancelsTheNamingWithoutApplyingIt(t *testing.T) {
	m, path := namingModel(t, pathPane("local", "/w/a", "raw", 0, state.Needs))
	m = key(t, m, "N")
	m = typeInto(t, m, "half typed")
	m = special(t, m, tea.KeyEsc)
	if m.mode != modeBrowse {
		t.Fatalf("esc left the mode at %v", m.mode)
	}
	if strings.Contains(m.View(), "half typed") {
		t.Error("a cancelled name was applied")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a cancelled name wrote the file")
	}
}

// A duplicate is refused AT COMMIT, fleet-wide and case-folded, and the reason lands INSIDE
// the overlay so naming adds no claimant to the footer (§21.12 rules 1 and 4).
func TestADuplicateNameIsRefusedInsideTheOverlay(t *testing.T) {
	m, _ := namingModel(t,
		pathPane("local", "/w/a", "one", 0, state.Needs),
		pathPane("nuc", "/w/b", "two", 1, state.Works))
	m = key(t, m, "N")
	m = typeInto(t, m, "The DR Plan")
	m = special(t, m, tea.KeyEnter)
	if m.mode != modeBrowse {
		t.Fatalf("the first name was refused: %v", m.naming.reason)
	}
	// The second row, same name in different case.
	m = key(t, m, "j")
	m = key(t, m, "N")
	m = typeInto(t, m, "the dr plan")
	m = special(t, m, tea.KeyEnter)

	if m.mode != modeNaming {
		t.Fatalf("a duplicate name was accepted; mode = %v", m.mode)
	}
	out := m.View()
	if !strings.Contains(out, "already") {
		t.Errorf("the refusal does not say why:\n%s", strings.Join(
			strings.Split(out, "\n")[len(strings.Split(out, "\n"))-6:], "\n"))
	}
	// The field KEEPS what was typed, so the operator can edit rather than retype.
	if got := m.naming.input.Text(); got != "the dr plan" {
		t.Errorf("the field was cleared on refusal: %q", got)
	}
}

// The duplicate check runs against the file RE-READ for the write, never against the copy the
// screen was drawn from (§21.12 rule 4): another hub, or a hand edit, may have taken the name
// since this screen opened.
func TestTheDuplicateCheckRereadsTheFile(t *testing.T) {
	m, path := namingModel(t, pathPane("local", "/w/a", "one", 0, state.Needs))
	// Someone else takes the name while the screen is open.
	other := "[[alias]]\nhost = \"nuc\"\nsession = \"elsewhere\"\nname = \"taken\"\n"
	if err := os.WriteFile(path, []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	m = key(t, m, "N")
	m = typeInto(t, m, "taken")
	m = special(t, m, tea.KeyEnter)
	if m.mode != modeNaming {
		t.Fatal("a name taken since the screen opened was accepted — the check read the " +
			"in-memory copy rather than the file")
	}
	// And the other hub's entry must survive: writing must not drop what was not ours.
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "elsewhere") {
		t.Errorf("the other entry was lost:\n%s", raw)
	}
}

// A name a person types must survive the round trip, which is what the dialect fix bought.
func TestAHostileNameSurvivesTheFile(t *testing.T) {
	m, path := namingModel(t, pathPane("local", "/w/a", "one", 0, state.Needs))
	hostile := `say "yes" \ or no`
	m = key(t, m, "N")
	m = typeInto(t, m, hostile)
	m = special(t, m, tea.KeyEnter)
	if m.mode != modeNaming {
		rules, aliases, err := project.LoadAll(path)
		if err != nil {
			t.Fatalf("the file does not parse: %v\n%s", err, mustRead(t, path))
		}
		_ = rules
		if got, _ := aliases.DisplayName(m.panes[0]); got != hostile {
			t.Errorf("the name round-tripped to %q, want %q\nfile:\n%s",
				got, hostile, mustRead(t, path))
		}
		return
	}
	t.Fatalf("a hostile but legal name was refused: %v", m.naming.reason)
}

// N on an agent row names the SESSION, and the key carries the cwd so two sessions under one
// id cannot share a name (N4).
func TestNamingAnAgentRowKeysOnTheSessionAndItsDirectory(t *testing.T) {
	a := registry.Pane{Kind: registry.KindAgent, Host: "nuc", SessionID: "dup-id",
		Path: "/w/one", Session: "first", ClassifiedState: state.Needs}
	b := registry.Pane{Kind: registry.KindAgent, Host: "nuc", SessionID: "dup-id",
		Path: "/w/two", Session: "second", ClassifiedState: state.Needs}
	m, _ := namingModel(t, a, b)
	m = key(t, m, "N")
	m = typeInto(t, m, "the first one")
	m = special(t, m, tea.KeyEnter)
	if m.mode != modeNaming {
		out := m.View()
		if !strings.Contains(out, "the first one") {
			t.Fatalf("the name was not applied:\n%s", out)
		}
		// The OTHER session, which shares the id, must not inherit it. Asserted by what its
		// own row reads rather than by counting occurrences: the tile under the cursor
		// legitimately repeats the named row's name, so a count is not the property.
		var second string
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "second") {
				second = line
				break
			}
		}
		if second == "" {
			t.Fatalf("the second session has no row:\n%s", out)
		}
		if strings.Contains(second, "the first one") {
			t.Errorf("the second session inherited the name: %q", second)
		}
		return
	}
	t.Fatalf("naming an agent row was refused: %v", m.naming.reason)
}

// typeInto drives the real key path, so a rune that the mode swallows shows up as a test
// failure rather than as a silently empty field.
func typeInto(t *testing.T, m model, s string) model {
	t.Helper()
	for _, r := range s {
		out, _ := press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = out
	}
	if got := m.naming.input.Text(); got != s {
		t.Fatalf("typed %q, the field holds %q", s, got)
	}
	return m
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return "(no file: " + err.Error() + ")"
	}
	return string(b)
}

// The overlay is six rows at every width §16 commits to, and every row must stay inside the
// terminal — including with a long subject, a long typed name and a long refusal, which is
// where a fixed six-row surface with four variable-length rows shows its seams (§21.14).
func TestTheNamingOverlayHoldsEveryWidthBand(t *testing.T) {
	long := registry.Pane{Kind: registry.KindAgent, Host: "an-aggregator-host",
		SessionID: "5a485bc4-4f01-4690-bbd4-29d42779a154", Path: "/home/dev/lab/streams/st",
		Session: "a session whose name nobody thought to keep short", ClassifiedState: state.Needs}
	for _, cols := range []int{80, 100, 160, 200} {
		m, _ := namingModel(t, long)
		m.width, m.height = cols, 24
		m = key(t, m, "N")
		m = typeInto(t, m, strings.Repeat("a very long name ", 6))
		m.naming.reason = strings.Repeat("a refusal that goes on and on ", 4)
		out := m.View()
		rows := strings.Split(out, "\n")
		if len(rows) != 24 {
			t.Errorf("%d cols: the screen is %d rows, want 24", cols, len(rows))
		}
		for i, line := range rows {
			if w := lines.Width(line); w > cols {
				t.Errorf("%d cols: row %d is %d wide: %q", cols, i, w, line)
			}
		}
		// The overlay is still exactly six rows: the base is drawn at height-6, so a longer
		// reason must not push it.
		tail := rows[len(rows)-NamingRows:]
		if !strings.Contains(tail[0], "─") {
			t.Errorf("%d cols: the overlay does not start with its separator: %q", cols, tail[0])
		}
		if !strings.Contains(strings.Join(tail, "\n"), "enter") {
			t.Errorf("%d cols: the key row fell off the overlay:\n%s", cols, strings.Join(tail, "\n"))
		}
	}
}

// The FIELD is what the operator is typing into, so when the name is longer than the row the
// TAIL must be visible — a field that shows the head and hides the cursor is a field you
// cannot type in, which is the reason §21.12 rules out inline editing in the first place.
func TestTheNameFieldShowsWhatIsBeingTyped(t *testing.T) {
	m, _ := namingModel(t, pathPane("local", "/w/a", "s", 0, state.Needs))
	m.width = 60
	m = key(t, m, "N")
	typed := "the-name-that-is-far-longer-than-the-row-can-hold-abcdefghij-END"
	m = typeInto(t, m, typed)
	var field string
	for _, line := range strings.Split(m.View(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "name:") {
			field = line
		}
	}
	if field == "" {
		t.Fatalf("no field row:\n%s", m.View())
	}
	if lines.Width(field) > m.width {
		t.Errorf("the field row is %d wide at %d: %q", lines.Width(field), m.width, field)
	}
	if !strings.Contains(field, "END") {
		t.Errorf("the field shows the head and hides what is being typed: %q", field)
	}
}

// fieldTail's edges, because a text field is where an off-by-one shows as a caret that
// disappears while someone is typing.
func TestFieldTailKeepsTheCaretAtEveryWidth(t *testing.T) {
	for _, text := range []string{"", "a", "abcdefghij", "путь-длиннее-чем-строка", "中文中文中文"} {
		for room := 0; room <= 20; room++ {
			got := fieldTail(text, room)
			if w := lines.Width(got); room >= 1 && w > room {
				t.Errorf("fieldTail(%q, %d) is %d columns: %q", text, room, w, got)
			}
			if !strings.HasSuffix(got, "▏") {
				t.Errorf("fieldTail(%q, %d) lost the caret: %q", text, room, got)
			}
			// Whenever there is room for any of the text, the END of it must be visible —
			// that is the property the whole function exists for.
			if room >= 4 && text != "" {
				rs := []rune(text)
				if !strings.Contains(got, string(rs[len(rs)-1:])) {
					t.Errorf("fieldTail(%q, %d) = %q hides the last character", text, room, got)
				}
			}
		}
	}
}

// §21.12 rule 3: the same overlay serves a PROJECT, and only the subject row differs. What does
// NOT transfer is rule 2 — on the list a group cannot be "the row under the cursor" while the
// list re-sorts under a probe, so the subject is captured at the keystroke.
func TestNOnTheProjectListNamesTheProject(t *testing.T) {
	rows := []registry.Pane{
		pathPane("local", "/w/streams/st", "a1", 0, state.Needs),
		pathPane("local", "/w/streams/st", "a2", 1, state.Works),
		pathPane("local", "/w/streams/maps", "b1", 2, state.Works),
	}
	m, path := namingModel(t, rows...)
	m = key(t, m, "P")
	m = key(t, m, "N")
	if m.mode != modeNaming {
		t.Fatalf("N on the list did not open the overlay: mode = %v", m.mode)
	}
	// The overlay says it is naming a PROJECT, not a session — the same six rows, one row
	// different.
	out := m.View()
	if !strings.Contains(out, "name this project") {
		t.Errorf("the overlay does not say what it is naming:\n%s", out)
	}
	if !strings.Contains(out, "st") {
		t.Errorf("the overlay does not name its subject:\n%s", out)
	}

	m = typeInto(t, m, "the streams work")
	m = special(t, m, tea.KeyEnter)
	if m.mode != modeNaming {
		// Committed: the group reads by the name and the sibling does not.
		got := m.projectRows()
		labels := map[string]bool{}
		for _, s := range got {
			labels[s.Group.Label] = true
		}
		if !labels["the streams work"] {
			t.Errorf("the project list does not use the name: %v", labels)
		}
		if labels["st"] {
			t.Errorf("the old derived label is still a group: %v", labels)
		}
		if !labels["maps"] {
			t.Errorf("the sibling project was swallowed: %v", labels)
		}
		// And it reached the FILE as a rule beside any aliases.
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("no file written: %v", err)
		}
		if !strings.Contains(string(raw), "[[project]]") ||
			!strings.Contains(string(raw), "the streams work") {
			t.Errorf("projects.toml does not hold the rule:\n%s", raw)
		}
		return
	}
	t.Fatalf("naming the project was refused: %v", m.naming.reason)
}

// A project whose rows share no exclusive ancestor is REFUSED inside the overlay, naming what
// the rule would have swallowed — the safety §21.12 was worried about, and it is a measurement
// rather than an argument.
func TestNamingAProjectThatWouldSwallowIsRefusedOnScreen(t *testing.T) {
	rows := []registry.Pane{
		pathPane("local", "/w/a/st", "a1", 0, state.Needs),
		pathPane("local", "/w/b/st", "a2", 1, state.Works),
		pathPane("local", "/w/other", "b1", 2, state.Works),
	}
	m, _ := namingModel(t, rows...)
	m = key(t, m, "P")
	m = key(t, m, "N")
	m = typeInto(t, m, "st everywhere")
	m = special(t, m, tea.KeyEnter)
	if m.mode != modeNaming {
		t.Fatalf("a project that cannot be named was accepted; mode = %v", m.mode)
	}
	out := m.View()
	if !strings.Contains(out, "other") {
		t.Errorf("the refusal does not name what would have been swallowed:\n%s", out)
	}
}

// §21.12 rule 6 is one displayName that EVERY surface calls, "so no screen can show a
// different name from another". Below 100 columns — the 80×24 §16 calls "the size to hold,
// not a degraded case" — the inbox row did not call it, so naming changed the TILE and left
// the LIST reading the derived name: one screen, two names for one row.
//
// Every existing naming test ran at width 100 or wider, so the whole band was untested for
// names. This runs at 80 FIRST, which is where it fails.
func TestANameReachesTheListAtEveryWidthNotJustTheTile(t *testing.T) {
	// The property is §21.12 rule 6 verbatim — "no screen can show a different name from
	// another" — asserted as: the chosen name is ON the screen and the DERIVED name is not.
	// Where the name lands differs by band and that is fine: below 100 the inbox is inline so
	// it belongs on the row, and at 100+ it belongs on the group header above it. What must
	// never happen is both names on one screen, which is what the inline branch did by
	// rendering the row without calling DisplayName while the tile called it.
	for _, cols := range []int{80, 100, 160, 200} {
		for _, kind := range []string{registry.KindPane, registry.KindAgent} {
			row := registry.Pane{Kind: kind, Host: "nuc", Session: "derived-name",
				PaneID: "%0", Command: "claude", ClassifiedState: state.Needs,
				SessionID: "id-1", Path: "/w/st"}
			var al project.Aliases
			al.Set(project.AliasKeyOf(row), "MY CHOSEN NAME")

			out := Render(Frame{Panes: []registry.Pane{row}, Hosts: hosts2(),
				Width: cols, Height: 24, Aliases: al})
			if !strings.Contains(out, "MY CHOSEN") {
				t.Errorf("%d cols / %s: the chosen name is nowhere on screen:\n%s", cols, kind, out)
			}
			// TWO markers, not "at least one": the list side (an inline row below 100, a group
			// header at 100 and above) and the TILE each carry it, and every layout draws
			// exactly two for one named row — measured. A count is what makes this a
			// per-SURFACE assertion: `Contains` passed while the group header's marker was
			// mutated away, because the tile's satisfied it, and that is how the marker had
			// zero real coverage.
			if n := strings.Count(out, "»"); n != 2 {
				t.Errorf("%d cols / %s: %d ownership markers, want 2 — one surface lost "+
					"it:\n%s", cols, kind, n, out)
			}
			if strings.Contains(out, "derived-name") {
				t.Errorf("%d cols / %s: the screen shows the DERIVED name as well as the "+
					"chosen one — two names for one row:\n%s", cols, kind, out)
			}
		}
	}
}

// The prospective-rule check is described by both §21.12 and name.go as "APPLIED to the whole
// fleet". It was applied to visibleRows(), so HIDING a row defeated it in the ACCEPT direction:
// the rule was written, and the hidden row silently changed project. Unlike §18's hide, a wrong
// RULE has no self-repair — it persists in projects.toml.
func TestHidingARowCannotDefeatTheProjectNameCheck(t *testing.T) {
	rows := []registry.Pane{
		pathPane("local", "/w/st", "a1", 0, state.Needs),
		pathPane("local", "/w/st/sub", "a2", 1, state.Works),
	}
	// Arm A: nothing hidden — the name is refused, because /w/st would also take /w/st/sub.
	a, _ := namingModel(t, rows...)
	a = key(t, a, "P")
	a = key(t, a, "N")
	a = typeInto(t, a, "mine")
	a = special(t, a, tea.KeyEnter)
	if a.mode != modeNaming {
		t.Fatalf("with nothing hidden the name was accepted; reason = %q", a.naming.reason)
	}

	// Arm B: the SAME fleet with the subdirectory hidden. The answer must not change — what a
	// rule captures on disk has nothing to do with what is on screen.
	b, path := namingModel(t, rows...)
	if err := b.hidden.Toggle(b.panes[1]); err != nil {
		t.Fatal(err)
	}
	b = key(t, b, "P")
	b = key(t, b, "N")
	b = typeInto(t, b, "mine")
	b = special(t, b, tea.KeyEnter)
	if b.mode != modeNaming {
		raw, _ := os.ReadFile(path)
		t.Fatalf("hiding a row let the name through — the rule was written and the hidden "+
			"row silently changed project:\n%s", raw)
	}
	if a.naming.reason != b.naming.reason {
		t.Errorf("the refusal differs by what is hidden:\n visible: %q\n hidden:  %q",
			a.naming.reason, b.naming.reason)
	}
}

// Cancelling must cost no more than committing. `esc` always returned to the dashboard, so
// cancelling a PROJECT name threw the operator off the list while a successful save returned
// them to it — and the overlay's own key row calls both "cancel" and "save".
func TestEscReturnsToTheScreenTheOverlayWasOpenedFrom(t *testing.T) {
	rows := []registry.Pane{pathPane("local", "/w/st", "a1", 0, state.Needs)}
	// From the LIST.
	m, _ := namingModel(t, rows...)
	m = key(t, m, "P")
	m = key(t, m, "N")
	m = special(t, m, tea.KeyEsc)
	if m.mode != modeProjects {
		t.Errorf("esc from the project half left the mode at %v, want the list it was "+
			"opened from", m.mode)
	}
	// From the DASHBOARD, unchanged.
	d, _ := namingModel(t, rows...)
	d = key(t, d, "N")
	d = special(t, d, tea.KeyEsc)
	if d.mode != modeBrowse {
		t.Errorf("esc from the session half left the mode at %v, want browse", d.mode)
	}
}
