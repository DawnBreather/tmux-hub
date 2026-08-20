package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
)

// The operator's two narrowings: `*` shows only what they pinned, `/` keeps only rows whose name or
// project answers a keyword (docs/design.md §21.16).
//
// Neither is a mode of `v`. `v` answers "how are rows GROUPED" and these answer "which rows are
// SHOWN"; one three-position cycle would make "only my favourites, grouped by project" unreachable,
// and that combination is the reason to have both.

// searchQuery is the keyword currently applied, trimmed. It is the ONE source of truth for "a search
// is on", so no boolean can disagree with the text it filters by.
func (m model) searchQuery() string { return strings.TrimSpace(m.search.Text()) }

// matchesQuery reports whether one row answers the operator's keyword.
//
// It searches every name the row has, not just the one on screen, and that is the whole point: an
// alias HIDES the derived name, and the moment an operator reaches for search they are usually
// reaching for the word they remember rather than the one they typed. So `cicd` still finds a row
// renamed `прод-выкатка`, and `прод-выкатка` finds it too.
//
// The host and the project label are in as well, because "everything on nuc" and "everything in
// this project" are the same question asked with a different word, and answering only one of them
// would send the operator to the project screen for something they already have a key for.
//
// Folded with strings.ToLower, which is Unicode-aware — this fleet names sessions after the prompt
// that started them, so half of them are Cyrillic.
func matchesQuery(p registry.Pane, aliases project.Aliases, groupLabel, q string) bool {
	if q == "" {
		return true
	}
	q = strings.ToLower(q)
	shown, _ := aliases.DisplayName(p)
	for _, field := range []string{shown, p.Session, p.AgentName, p.Window, p.Host, groupLabel, p.Path} {
		if field != "" && strings.Contains(strings.ToLower(field), q) {
			return true
		}
	}
	return false
}

// looselyMatchesQuery is the same question read as a SUBSEQUENCE: every character of the keyword
// appears in the field, in order, with anything allowed between them. It is fzf's matcher.
//
// It searches exactly the fields matchesQuery searches, through the same fold, because a fallback
// that looked in a different place would answer a different question — an operator who types a word
// they remember and gets nothing must not have to guess WHICH name the loose pass reads.
//
// It is only ever asked when the substring pass keeps nothing (rowsForScreenLoose says why), so its
// permissiveness — measured, `sec` matches 24 of 64 rows on the operator's fleet against 2 by
// substring — never reaches the screen while a stricter answer exists.
//
// Runes and not bytes: half this fleet is named in Cyrillic, and indexing a folded string by byte
// would advance the query by one byte inside a two-byte letter and match nothing.
func looselyMatchesQuery(p registry.Pane, aliases project.Aliases, groupLabel, q string) bool {
	if q == "" {
		return true
	}
	want := []rune(strings.ToLower(q))
	shown, _ := aliases.DisplayName(p)
	for _, field := range []string{shown, p.Session, p.AgentName, p.Window, p.Host, groupLabel, p.Path} {
		if field == "" {
			continue
		}
		i := 0
		for _, r := range strings.ToLower(field) {
			if r == want[i] {
				i++
				if i == len(want) {
					return true
				}
			}
		}
	}
	return false
}

// filterTally is what the footer says about the narrowings, and it carries the COUNTS rather than
// deciding whether to mention them.
//
// The shape is `hiddenTally`'s, for the reason that one exists: a mode which changes what the screen
// MEANS has to change the sentence, and the first version of `X` implemented that as "return a zero"
// — which reads exactly like a fleet with nothing hidden. Count always; word differently.
type filterTally struct {
	// Project is the LABEL of the project the list is filtered to, empty when it is not. It was
	// missing, and the project filter was therefore the one narrowing that left no trace: measured
	// on a fleet of 8 rows, `enter` on a project drew 3 rows under a header reading
	// `tmux-hub  3 sessions` and a footer reading `local up · nuc up` — nothing named the project,
	// the count or the way back, while `*` and `/` on the same fleet said `★ only · 1 of 8` and
	// `"sess-a" · 1 of 8`. `tab` walks between projects with the same silence, so the operator
	// could not tell which one they had arrived at.
	Project        string
	FavouritesOnly bool
	Query          string
	// Loose is set when nothing CONTAINED the keyword and the subsequence pass answered instead.
	// It is a separate field rather than a different Query string because the count means something
	// different in that case: `2 of 64` after a substring pass is "two rows have this word in them",
	// and the same figure after a loose pass is "two rows resemble it". A screen that shows the second
	// while reading like the first is the defect this repo keeps finding — hiddenStats returning a
	// zero, the project filter leaving no trace, `X` deleting its own numbers.
	Loose bool
	Kept  int
	Total int
}

// on reports whether anything is narrowing the list. It must agree with `model.narrowed()`, and a
// test asserts that over every combination — the two cannot be one function, because this is a view
// model the renderer holds and that one is a question about the model.
func (t filterTally) on() bool { return t.Project != "" || t.FavouritesOnly || t.Query != "" }

// sentence is the footer's claimant, empty when nothing is on.
//
// The query is BOUNDED through shortSubject: a session here is named after the prompt that started
// it — measured, 88 columns — and an unbounded interpolation is this repo's oldest layout defect,
// where the fixed text survives and the operator's own word is what falls off the row.
func (t filterTally) sentence() string {
	if !t.on() {
		return ""
	}
	var parts []string
	if t.Project != "" {
		// FIRST, because it is the narrowing the operator arrived through and the one that changes
		// which question the screen is answering. Bounded like the query: a project is named after
		// a directory or by the operator, and neither is sized for a footer.
		parts = append(parts, "in "+shortSubject(t.Project))
	}
	if t.FavouritesOnly {
		parts = append(parts, favouriteMark+" only")
	}
	if t.Query != "" {
		q := "\"" + shortSubject(t.Query) + "\""
		if t.Loose {
			// The word LOOSELY, said in words rather than with a glyph: a `~` in front of a quoted
			// string is not something an operator can be expected to read, and this sentence is the
			// only place the screen can admit that the rows below merely resemble what was typed.
			q = "like " + q
		}
		parts = append(parts, q)
	}
	return fmt.Sprintf("%s · %d of %d", strings.Join(parts, " · "), t.Kept, t.Total)
}

// searchLine is the field while it has focus: what has been typed, the caret, and the two keys that
// end it.
//
// The tail is shown rather than the head when the query outgrows the row, and the caret is always
// last — the same rule the naming overlay had to learn, because a field whose end you cannot see is
// a field you cannot type in.
func searchLine(query string, kept, total, width int, loose bool) string {
	const label = "search: "
	// minField is what the FIELD keeps whatever else wants the row, because a field whose end you
	// cannot see is a field you cannot type in — the same ruling the naming overlay had to learn.
	const minField = 8
	// ONE PRIORITY LIST, not a subtraction. `width - Width(foot)` reserved the whole footer first and
	// then threw ALL of it away when the field had less than eight columns left — measured, the loose
	// admission below made `foot` about seventy columns, so at the committed 80 the count vanished
	// with it and the published mockup frame carried neither. A pre-reservation is a priority
	// decision written as arithmetic; this repo has paid for that three times (known-issues M1, S3).
	//
	// The ORDER is the argument: the admission outranks the count, because a count after a
	// subsequence pass is the misleading half on its own — `2 of 64` reads as two rows containing the
	// word — while "nothing contains it" is true with or without a number. The keys go last: they are
	// a standing reminder and a forgotten key costs one guess.
	var parts []string
	if query != "" && total > 0 {
		if loose {
			// SHORT, because it competes with the field and with the count on one row: the first
			// draft ran 42 columns and at 60 it took the field down to four (`…sch`), which is the
			// defect this repo names "a refusal is a layout object". Nineteen columns fits beside
			// both at every width down to 60, and the kept footer says the rest (`like "opssch"`).
			parts = append(parts, "nothing contains it")
		}
		// What the keyword has DONE, beside what it is.
		parts = append(parts, fmt.Sprintf("%d of %d", kept, total))
	}
	parts = append(parts, "enter: keep · esc: cancel")
	foot := ""
	// FitQuiet and not Fit: a `+1` beside a session count reads as "one session is missing", which is
	// the reason the header made the same choice.
	// The separator's own width is part of the reservation. Leaving it out is how the first version
	// handed the parts 43 columns, got 42 back, and then found the field with four: the prefix below
	// is five columns that nobody had counted.
	const sep = "  ·  "
	if room := width - lines.Width(label) - minField - lines.Width(sep) - 1; room > 0 {
		if f := lines.FitQuiet(room, " · ", parts...); f != "" {
			foot = sep + f
		}
	}
	room := width - lines.Width(label) - lines.Width(foot) - 1
	shown := query
	if lines.Width(shown) > room {
		// Cut from the LEFT and mark it: the operator is typing at the right-hand end, so the
		// tail is what has to stay. Measured in COLUMNS, since a Cyrillic or CJK query is not
		// one column per rune.
		for lines.Width(shown) > room-1 && shown != "" {
			_, size := utf8.DecodeRuneInString(shown)
			shown = shown[size:]
		}
		shown = "…" + shown
	}
	return lines.Truncate(label+shown+"▏"+foot, width)
}

// widen clears every narrowing, which is what `esc` means on this screen.
//
// Every one of them, not the most recent: an operator who pressed two keys should not have to
// remember which one esc undoes. It returns whether anything changed, so the caller can leave the
// cursor alone when nothing did.
func (m *model) widen() bool {
	changed := m.filter.on || m.favouritesOnly || m.searchQuery() != ""
	m.filter.on, m.filter.group = false, ""
	m.favouritesOnly = false
	m.search = Composer{}
	return changed
}

// searchKey routes the search field. EVERY key is text except the three that end or edit it, which
// is why this is a mode: `j` types a `j` here, and a test that types a whole word cannot prove that
// — an injected run arrives as ONE key message whose String() is the whole string.
func (m model) searchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		// Lossless: back to whatever was applied when the field opened.
		m.search = Composer{}
		m.search.Insert(m.searchBefore)
		m.searchBefore = ""
		return m.dismiss(), nil
	case tea.KeyEnter:
		// Keep what is typed and hand the screen back. The filter was already live while
		// typing, so enter changes nothing about the list — it only returns the keys.
		m.searchBefore = ""
		return m.dismiss(), nil
	case tea.KeyCtrlU:
		m.search = Composer{}
		return m, nil
	case tea.KeyBackspace:
		m.search.Backspace()
		return m, nil
	case tea.KeyRunes:
		fallthrough
	case tea.KeySpace:
		// ONE arm through typedText: bubbletea reports a space as KeySpace with Runes ALSO set, so
		// folding the two together by hand inserted the character twice (a search for `two words`
		// became `two  words` and matched nothing) while naming only KeyRunes dropped it (the launch
		// form's own defect). Three fields, one rule.
		text, _ := typedText(msg)
		m.search.Insert(text)
		return m, nil
	}
	return m, nil
}

// narrowed reports whether anything is keeping rows off the screen — the project filter, `*` or a
// keyword. It is one predicate because every caller asks the same question: "is what the operator
// sees smaller than what exists", and three separate tests are three chances to forget the newest.
func (m model) narrowed() bool {
	return m.filter.on || m.favouritesOnly || m.searchQuery() != ""
}

// filters is what the footer says about the narrowings.
//
// The two counts are PASSED IN rather than derived here, and that is not a micro-optimisation: the
// caller has already walked the fleet for both of them, and computing them again meant two more
// passes per frame — over a 45-row fleet, three or four times a second, each pass re-deriving every
// row's project. It is still counted at PAINT time rather than stored, because a stored count goes
// stale on the tick a row stops matching.
// It takes `loose` rather than asking again: rowsForScreenLoose already answered when the caller
// built `shown`, and calling it a second time here would run the whole narrowing twice per frame.
func (m model) filters(shown []registry.Pane, total int, loose bool) filterTally {
	// The label comes off the first SHOWN row rather than from a walk of the rules, because under
	// this filter every shown row belongs to that project and one `OfPane` is the whole cost. When
	// the filter empties the list there is no row to read it from, and that case has its own
	// sentence already: View says "no rows in this project any more — esc shows the whole fleet",
	// which names the remedy this footer does not have room for.
	project := ""
	if m.filter.on && len(shown) > 0 {
		project = m.rules.OfPane(shown[0]).Label
	}
	return filterTally{
		Project:        project,
		FavouritesOnly: m.favouritesOnly,
		Query:          m.searchQuery(),
		Loose:          loose,
		Kept:           len(shown),
		Total:          total,
	}
}
