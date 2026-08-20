package project

import (
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

func row(host, path string, st state.State) registry.Pane {
	return registry.Pane{Kind: registry.KindPane, Host: host, Path: path, ClassifiedState: st}
}

// The list is sorted by waiting, then broken, then unknown, then size, then name, with the
// unassigned bucket pinned last whatever its size (docs/design.md §21.2). A group where
// three of four sessions want you must not be nineteen rows down.
func TestTheListPutsWhatWantsYouFirstAndPinsUnassignedLast(t *testing.T) {
	rows := []registry.Pane{
		row("local", "/w/big", state.Works), row("local", "/w/big", state.Works),
		row("local", "/w/big", state.Works), row("local", "/w/big", state.Works),
		row("local", "/w/broken", state.Error),
		row("local", "/w/waits", state.Needs),
		row("local", "/w/dunno", state.Unknown),
		{Kind: registry.KindAgent, Host: "local", ClassifiedState: state.Needs}, // no cwd
	}
	got := Summarise(Rules{}, rows)
	var order []string
	for _, s := range got {
		order = append(order, s.Group.Label)
	}
	want := []string{"waits", "broken", "dunno", "big", "unassigned"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", order, want)
	}
	// The unassigned bucket is pinned last even though it CONTAINS a waiting row: a
	// bucket the operator cannot act on must not head the list.
	last := got[len(got)-1]
	if last.Group.Kind != Unassigned || last.Waiting != 1 {
		t.Errorf("last = %+v, want the unassigned bucket carrying its waiting row", last)
	}
}

// Every row is in exactly one group and the sum equals the population, INCLUDING a row
// whose path could not be read (§21.6). Two screens one keystroke apart must not disagree.
func TestEveryRowIsInExactlyOneGroupAndTheSumIsThePopulation(t *testing.T) {
	rows := []registry.Pane{
		row("local", "/w/a", state.Works),
		row("nuc", "/w/a", state.Needs),
		row("local", "/w/b", state.Error),
		{Kind: registry.KindPane, Host: "local"},                               // Pending
		{Kind: registry.KindAgent, Host: "local", ClassifiedState: state.Idle}, // Unassigned
	}
	got := Summarise(Rules{}, rows)
	sum := 0
	for _, s := range got {
		sum += s.Total
	}
	if sum != len(rows) {
		t.Errorf("the groups hold %d rows, the fleet has %d — the two screens would "+
			"disagree about the total", sum, len(rows))
	}
	// And Pending is its own bucket, not folded into unassigned: §21.9 gives them
	// different remedies, so a screen that merged them would print the wrong one.
	kinds := map[Kind]bool{}
	for _, s := range got {
		kinds[s.Group.Kind] = true
	}
	if !kinds[Pending] || !kinds[Unassigned] {
		t.Errorf("pending and unassigned did not both appear: %v", kinds)
	}
}

// The cell is defined rather than implied: up to three facts then `of N`, the word `none` in place
// of the facts when there are none, and never a state glyph as filler — `·` is Works and `?` is
// Unknown, so a filler would assert a state.
//
// `none` was added after reading the real list, where a project with nothing to report drew
// `album.QUTp1q      of 5` — half a sentence, and the eye looks for what the 5 is OF. The rule it
// does NOT break is the one this test's last loop enforces: `none` is a WORD and asserts nothing
// about any row's state, unlike a glyph, and it is true of exactly the three facts above. It yields
// to width like anything else: too narrow for the word and `of N` stands alone, because that is the
// one part the header can be checked against.
// A column too narrow for the word keeps the part that must never drop.
func TestTheAttentionCellGivesUpTheWordBeforeTheCount(t *testing.T) {
	if got := Cell(Summary{Total: 12}, 6); strings.TrimSpace(got) != "of 12" {
		t.Errorf("Cell at 6 columns = %q, want %q — the count is the fact the header is checked "+
			"against, so it outranks the word", strings.TrimSpace(got), "of 12")
	}
	if got := Cell(Summary{Total: 12}, 22); strings.TrimSpace(got) != "none  of 12" {
		t.Errorf("Cell at 22 columns = %q, want the word and the count", strings.TrimSpace(got))
	}
}

func TestTheAttentionCellSaysOnlyWhatIsTrue(t *testing.T) {
	for _, c := range []struct {
		name string
		s    Summary
		want string
	}{
		{"nothing to say", Summary{Total: 4}, "none  of 4"},
		{"waiting only", Summary{Total: 4, Waiting: 3}, "⚑ 3  of 4"},
		{"all three", Summary{Total: 4, Waiting: 3, Broken: 1, Unknown: 1}, "⚑ 3  ✗ 1  ? 1  of 4"},
		{"broken without waiting", Summary{Total: 2, Broken: 2}, "✗ 2  of 2"},
	} {
		got := Cell(c.s, 22)
		if strings.TrimSpace(got) != c.want {
			t.Errorf("%s: Cell = %q, want %q", c.name, strings.TrimSpace(got), c.want)
		}
		for _, glyph := range []string{"·"} {
			if strings.Contains(got, glyph) {
				t.Errorf("%s: the cell used %q as filler, which asserts a state", c.name, glyph)
			}
		}
	}
}

// 16 columns is the budget, and two-digit counts on all three facts need 22 — so the
// overflow rule is required rather than theoretical: drop from the RIGHT, unknown first
// then broken, and mark the loss by appending `+` to the last fact kept.
func TestTheCellDropsFromTheRightAndSaysItDropped(t *testing.T) {
	wide := Summary{Total: 40, Waiting: 12, Broken: 34, Unknown: 56}
	got := Cell(wide, 16)
	if lines.Width(got) > 16 {
		t.Errorf("Cell = %q is %d columns, over the 16 budget", got, lines.Width(got))
	}
	if strings.Contains(got, "? 56") {
		t.Errorf("unknown survived the overflow: %q — it is dropped first", got)
	}
	if !strings.Contains(got, "+") {
		t.Errorf("Cell = %q dropped a fact without saying so", got)
	}
	if !strings.Contains(got, "of 40") {
		t.Errorf("Cell = %q lost the total, which is the one fact that always fits", got)
	}
	// A cell that fits EXACTLY is never marked, or `+` would stop meaning anything.
	exact := Cell(Summary{Total: 4, Waiting: 3, Unknown: 1}, 16)
	if strings.Contains(exact, "+") {
		t.Errorf("a cell that fits was marked as truncated: %q", exact)
	}
}

// The budget itself, written out as a literal. Substituting the constant into the two Cell
// tests would make them import their expected value from the code under test, and would
// destroy the overflow test, whose whole point needs a fixed NARROW width — so the constant
// gets its own one-line pin instead. Measured: shrinking it to 10 left every test green
// while RenderProjects laid out 10-column cells.
func TestTheCellBudgetIsWhatTheDesignSpecifies(t *testing.T) {
	if CellWidth != 16 {
		t.Errorf("CellWidth = %d, docs/design.md §21.2 specifies 16", CellWidth)
	}
}

// The widest cell this fleet produces is 14 columns, measured — asserted so the 16-column
// budget is a fact about the data rather than a guess.
func TestTheMeasuredWidestFleetCellFitsTheBudget(t *testing.T) {
	got := Cell(Summary{Total: 4, Waiting: 3, Unknown: 1}, 16)
	if w := lines.Width(strings.TrimRight(got, " ")); w != 14 {
		t.Errorf("the measured widest cell is %d columns, the design says 14: %q", w, got)
	}
}

// A named rule collects rows from more than one host when the operator says so, which is
// the explicit merge §21.1 requires.
func TestANamedRuleMergesHostsWhenTheOperatorSaysSo(t *testing.T) {
	r, err := Parse("[[project]]\nname = \"st\"\nprefix = \"/w/st\"\n")
	if err != nil {
		t.Fatal(err)
	}
	got := Summarise(r, []registry.Pane{
		row("local", "/w/st/one", state.Works),
		row("nuc", "/w/st/two", state.Needs),
	})
	if len(got) != 1 || got[0].Total != 2 {
		t.Fatalf("got %+v, want one group of two", got)
	}
	if got[0].Group.Kind != Named {
		t.Errorf("kind = %v, want Named", got[0].Group.Kind)
	}
}
