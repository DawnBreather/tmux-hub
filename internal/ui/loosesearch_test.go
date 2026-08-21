package ui

import (
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// looseFleet is shaped like the operator's own: sessions named after the prompt that started them,
// which is what makes a bare subsequence useless and a fallback safe. Measured on their 64 rows,
// `sec` matched 2 by substring and 24 as a subsequence; the names below reproduce that ratio.
func looseFleet() []registry.Pane {
	name := func(host, sess string, st state.State) registry.Pane {
		return registry.Pane{
			Kind: registry.KindAgent, Command: "background", Host: host, Session: sess,
			AgentID: sess[:4] + "0000", SessionID: sess[:4] + "0000-aaaa",
			PaneID: "agent:" + sess[:4] + "0000@bbbb", ClassifiedState: st,
			Content: []string{"  (no pane)"},
		}
	}
	return []registry.Pane{
		name("nuc", "20260701-ops-schdev3-envoy-access-logs--compact", state.Needs),
		name("nuc", "envoy-ops-svcdev4-ci-hotfix-branch-support", state.Idle),
		name("local", "20260709--clarifying-security-controls--spa", state.Done),
		name("local", "20260803--testing-seedtool", state.Quiet),
		name("local", "20260817-cicd-30f3382b", state.Works),
	}
}

func looseModel(t *testing.T, q string) model {
	t.Helper()
	m := base(t, 120, 24, looseFleet()...)
	for _, r := range q {
		m.search.Insert(string(r))
	}
	return m
}

// THE WHOLE DESIGN IN ONE TABLE: the substring pass answers whenever it can, and the subsequence
// pass answers only when it keeps nothing. Every row of this table is a measurement from the
// operator's own fleet, restated on a fixture of the same shape.
func TestTheKeywordFallsBackToASubsequenceOnlyWhenNothingContainsIt(t *testing.T) {
	for _, c := range []struct {
		q     string
		kept  int
		loose bool
		why   string
	}{
		// Substring hits exist, so the flood cases can never reach the loose pass. `sec` matches 24
		// of 64 rows as a subsequence on the real fleet and 2 by substring; here it is 1 and 1.
		{"sec", 1, false, "`security` contains it, so the loose pass must not run"},
		{"cicd", 1, false, "`20260817-cicd` contains it"},
		{"envoy", 2, false, "two rows contain it"},
		{"test", 1, false, "`testing-seedtool` contains it"},
		// NOTHING contains these, and each is exactly the fzf gesture: type across a separator.
		// BOTH ops rows, and that is the point rather than a surprise: the letters `s`,`c`,`h` occur
		// in order across three words of the sibling name (`**shc**dev4-**c**i-**h**otfix`), so the
		// gesture finds the pair the operator is choosing between instead of guessing for them.
		{"opssch", 2, true, "`ops-schdev3` is a subsequence and not a substring"},
		{"opsshc", 2, true, "the sibling row, spelled the way the directory is"},
		{"seedt", 1, false, "`seedtool` contains it — a whole word, not a gesture"},
		// Neither pass finds anything: the screen must NOT claim a loose answer it does not have.
		{"zzzq", 0, false, "no row contains it and no row resembles it"},
		// Cyrillic, folded and read by RUNE — half this fleet is named in it on the real machine.
		{"COMPACT", 1, false, "the fold is Unicode-aware in both directions"},
	} {
		m := looseModel(t, c.q)
		rows, loose := m.rowsForScreenLoose()
		if len(rows) != c.kept || loose != c.loose {
			var got []string
			for _, r := range rows {
				got = append(got, r.Session)
			}
			t.Errorf("%q kept %d rows loose=%v, want %d loose=%v (%s)\n  kept: %v",
				c.q, len(rows), loose, c.kept, c.loose, c.why, got)
		}
	}
}

// A loose answer SAYS it is loose, on both surfaces that can carry it.
//
// The count is the reason: `1 of 5` after a substring pass means one row has the word in it, and the
// same figure after a subsequence pass means one row resembles it. A screen that shows the second
// while reading like the first is the shape this repo keeps paying for — `hiddenStats` returning a
// zero, the project filter leaving no trace, `X` deleting its own numbers.
func TestALooseAnswerSaysSoOnTheScreen(t *testing.T) {
	// The KEPT footer, after enter.
	m := looseModel(t, "opssch")
	screen := m.View()
	if !strings.Contains(screen, `like "opssch"`) {
		t.Errorf("the footer does not admit the rows merely resemble the keyword:\n%s", screen)
	}
	// And the FIELD, while it still has focus, which is where the operator is looking as they type.
	field := looseModel(t, "opssch")
	field.mode = modeSearch
	if s := field.View(); !strings.Contains(s, "nothing contains it") {
		t.Errorf("the open field does not say the answer is loose:\n%s", s)
	}

	// The other pole: a strict answer must not be marked, or the marker means nothing.
	strict := looseModel(t, "envoy")
	if s := strict.View(); strings.Contains(s, "like \"envoy\"") ||
		strings.Contains(s, "nothing contains it") {
		t.Errorf("a substring answer was marked as loose:\n%s", s)
	}
	strictField := looseModel(t, "envoy")
	strictField.mode = modeSearch
	if s := strictField.View(); strings.Contains(s, "nothing contains it") {
		t.Errorf("a substring answer was marked as loose in the open field:\n%s", s)
	}
}

// The loose pass leaves the ORDER alone, which is what makes it safe to add at all.
//
// fzf ranks by match score; this screen is ordered by ATTENTION (§21.11), with the longest-waiting row
// first inside its band, and that ordering is the reason the screen exists. A loose pass that sorted
// by score would trade "who needs me" for "what did I type".
func TestALooseAnswerKeepsTheAttentionOrder(t *testing.T) {
	m := looseModel(t, "ops") // substring: both ops rows, in attention order
	strictRows, loose := m.rowsForScreenLoose()
	if loose || len(strictRows) != 2 {
		t.Fatalf("expected a strict answer of two rows, got %d loose=%v", len(strictRows), loose)
	}
	// `needs` outranks `idle`, whatever the keyword did.
	if strictRows[0].State() != state.Needs {
		t.Errorf("the keyword reordered the fleet: first row is %v, want needs", strictRows[0].State())
	}

	// And the same for a loose answer, on a query that keeps both of those rows loosely.
	loose2 := looseModel(t, "opscompact")
	rows2, isLoose := loose2.rowsForScreenLoose()
	if !isLoose {
		t.Fatalf("`opscompact` should have needed the loose pass, kept %d rows", len(rows2))
	}
	for i := 1; i < len(rows2); i++ {
		if rows2[i-1].State().Rank() > rows2[i].State().Rank() {
			t.Errorf("the loose pass reordered the fleet at row %d: %v after %v",
				i, rows2[i].State(), rows2[i-1].State())
		}
	}
}

// looselyMatchesQuery reads the same fields as matchesQuery, through the same fold, by RUNE.
//
// The field list is the claim: an operator who types a word they remember and gets nothing must not
// have to guess which of a row's names the loose pass reads. Indexing by BYTE would advance the query
// inside a two-byte letter and match nothing at all, which on this fleet is half the rows.
func TestTheLooseMatcherReadsEveryNameAndFoldsUnicode(t *testing.T) {
	p := registry.Pane{
		Session: "20260701-ops-schdev3-envoy-access-logs--compact", Host: "nuc",
		Window: "logs", Path: "/home/dev/lab/streams/ops", AgentName: "рендеринг-карты",
	}
	for _, c := range []struct {
		q    string
		want bool
		in   string
	}{
		{"opssch", true, "the session, across a separator"},
		{"OPSSCH", true, "folded"},
		{"рекарт", true, "the agent name, in Cyrillic, by rune"},
		{"РЕКАРТ", true, "folded in Cyrillic too"},
		{"lgs", true, "the window"},
		{"strms", true, "the path"},
		{"nuc", true, "the host"},
		{"zqx", false, "in none of them"},
		{"", true, "an empty keyword matches everything, like the strict pass"},
	} {
		if got := looselyMatchesQuery(p, project.Aliases{}, "", c.q); got != c.want {
			t.Errorf("%q = %v, want %v (%s)", c.q, got, c.want, c.in)
		}
	}
	// The project LABEL is in the list too, because "everything in this project" is the same question
	// asked with a different word — the strict matcher's own comment says so.
	if !looselyMatchesQuery(registry.Pane{Session: "x"}, project.Aliases{}, "tmux-hub", "tmxhub") {
		t.Error("the loose matcher does not read the project label, which the strict one does")
	}
}
