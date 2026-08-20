package state

import "testing"

// A FINISHED background job and a LIVE interactive session used to be the same state, and the
// word the screen printed for both was `idle`. The document's own gloss of that word is §14's
// "`idle` — prompt present, ready for the next thing", reading §6's state table, and a finished
// job has none of it: there is no prompt, and `a` on it reaches nothing to type into. So one fold
// cost the operator both facts at once — what the row IS, and where it belongs in a list ordered
// by how much each row wants them.
//
// The quote is the DOCUMENT's, not state.go's comment beside the constant: a test that quotes the
// code under test as though it were the spec asserts only that the two agree with themselves.
func TestDoneIsItsOwnStateBelowEveryLiveOne(t *testing.T) {
	// The WHOLE order, so an insertion cannot land in the wrong place unnoticed. `unknown`
	// stays ABOVE done deliberately: a row whose state the hub could not read wants a look,
	// and a finished one wants nothing.
	order := []State{Needs, Error, Quiet, Idle, Works, Unknown, Done, Gone}
	for i := 1; i < len(order); i++ {
		if order[i-1].Rank() >= order[i].Rank() {
			t.Fatalf("%v (rank %d) must rank before %v (rank %d)",
				order[i-1], order[i-1].Rank(), order[i], order[i].Rank())
		}
	}
}

func TestDoneCarriesItsOwnWordAndGlyph(t *testing.T) {
	if got := Done.String(); got != "done" {
		t.Errorf("Done.String() = %q, want \"done\" — the row read `idle`, which is a LIVE state",
			got)
	}
	// One column, like every other glyph: a two-column one shifts the whole state column on
	// every row. Measured with lines.Width — ⚑ ✗ ✱ ▸ · ? ✝ and ✓ all come out 1.
	if got := Done.Glyph(); got != "✓" {
		t.Errorf("Done.Glyph() = %q, want \"✓\"", got)
	}
	if Done.Glyph() == Idle.Glyph() || Done.String() == Idle.String() {
		t.Error("done and idle are still one appearance, so the screen cannot tell a finished " +
			"job from a session waiting for you")
	}
}

// FromWord is the only door from Claude's own vocabulary into ours, and `idle` has to keep
// meaning idle: 2.1.224 reports `idle` for a LIVE interactive session, so folding it into done
// would be the same defect pointing the other way.
func TestFromWordSeparatesDoneFromIdle(t *testing.T) {
	for word, want := range map[string]State{
		"done":    Done,
		"idle":    Idle,
		"works":   Works,
		"needs":   Needs,
		"error":   Error,
		"quiet":   Quiet,
		"":        Unknown,
		"nowhere": Unknown,
	} {
		if got := FromWord(word); got != want {
			t.Errorf("FromWord(%q) = %v, want %v", word, got, want)
		}
	}
}

// String and FromWord have to be each other's inverse for every state the agents listing can
// produce, or a state survives one hop and is lost on the next — the state log writes the WORD.
func TestEveryWordSurvivesTheRoundTrip(t *testing.T) {
	for _, s := range []State{Needs, Error, Quiet, Idle, Works, Done} {
		if got := FromWord(s.String()); got != s {
			t.Errorf("FromWord(%q) = %v, want %v — the word does not survive a round trip",
				s.String(), got, s)
		}
	}
}
