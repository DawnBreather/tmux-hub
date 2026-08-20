package lines

import (
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		in   string
		want Kind
	}{
		{"", Blank},
		{"   ", Blank},
		{strings.Repeat("─", 40), Rule},
		{"❯ ", Prompt},
		{"❯", Prompt},
		{"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents", Footer},
		{"      new task? /clear to save 142k tokens", Footer},
		{"✻ Churned for 6s", Spinner},
		{"● Hi! What you need?", Content},
		{"⎿  ITER2-LOCAL-FIXED", Content},
		{"❯ echo hello", Content}, // a prompt WITH text typed is content
	}
	for _, c := range cases {
		if got := Classify(c.in); got != c.want {
			t.Errorf("Classify(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestClassifyIgnoresANSI(t *testing.T) {
	in := "\x1b[38;5;231m●\x1b[39m \x1b[1mBash\x1b[0m(echo hi)"
	if got := Classify(in); got != Content {
		t.Errorf("Classify(ansi content) = %v, want Content", got)
	}
}

func TestWidthCountsDisplayCells(t *testing.T) {
	if got := Width("abc"); got != 3 {
		t.Errorf("Width(abc) = %d, want 3", got)
	}
	// A CJK glyph occupies two cells.
	if got := Width("日本"); got != 4 {
		t.Errorf("Width(日本) = %d, want 4", got)
	}
	// ANSI escapes occupy none.
	if got := Width("\x1b[31mab\x1b[0m"); got != 2 {
		t.Errorf("Width(ansi ab) = %d, want 2", got)
	}
}

func TestTruncateNeverSplitsAnEscape(t *testing.T) {
	in := "\x1b[31mHELLO\x1b[0m world"
	got := Truncate(in, 4)
	if Width(got) > 4 {
		t.Fatalf("Truncate width = %d, want <= 4: %q", Width(got), got)
	}
	// The reset must be re-emitted so colour cannot bleed past the tile.
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatalf("Truncate(%q) = %q, want a trailing SGR reset", in, got)
	}
}

func TestContentTailDropsChrome(t *testing.T) {
	in := []string{
		"● first answer",
		"",
		"✻ Churned for 6s",
		strings.Repeat("─", 30),
		"❯",
		strings.Repeat("─", 30),
		"  ⏵⏵ bypass permissions on (shift+tab to cycle)",
		"      new task? /clear to save 1k tokens",
		"",
	}
	got := ContentTail(in, 3)
	want := []string{"● first answer", "✻ Churned for 6s"}
	if len(got) != len(want) {
		t.Fatalf("ContentTail = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ContentTail[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// The mode indicators are a closed set of literals in Claude Code's bundle.
// Knowing only two of them made the footer read as CONTENT in the other four
// modes, so it leaked into every tile.
func TestEveryModeFooterIsChrome(t *testing.T) {
	for _, footer := range []string{
		"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents",
		"  ⏸ plan mode on (shift+tab to cycle)",
		"  ⏵⏵ accept edits on (shift+tab to cycle)",
		"  ⏵⏵ auto mode on (shift+tab to cycle)",
		"  ⏵⏵ don't ask on (shift+tab to cycle)",
		"  ⏸ manual mode",
	} {
		if got := Classify(footer); got != Footer {
			t.Errorf("Classify(%q) = %v, want Footer", footer, got)
		}
	}
}

// Pad aligns by DISPLAY width, which is the whole reason it exists: a project name a
// person typed can hold a wide glyph or a combining mark, and len() would misalign the
// column the eye is meant to scan down.
func TestPadAlignsByDisplayWidthAndNeverTruncates(t *testing.T) {
	for _, c := range []struct {
		name, in string
		cols     int
		want     int
	}{
		{"ascii", "abc", 6, 6},
		{"already exact", "abcdef", 6, 6},
		{"cyrillic is one column per rune", "путь", 6, 6},
		{"a CJK glyph is two columns", "中文", 6, 6},
		{"an emoji is two columns", "🙂x", 6, 6},
		{"empty", "", 3, 3},
	} {
		got := Pad(c.in, c.cols)
		if w := Width(got); w != c.want {
			t.Errorf("%s: Pad(%q, %d) is %d columns, want %d (%q)",
				c.name, c.in, c.cols, w, c.want, got)
		}
		if !strings.HasPrefix(got, c.in) {
			t.Errorf("%s: Pad changed the value: %q", c.name, got)
		}
	}
	// Over the budget it is returned UNCHANGED: deciding what to cut is Truncate's job,
	// and a Pad that silently truncated would make the two decisions invisible.
	long := "abcdefghij"
	if got := Pad(long, 3); got != long {
		t.Errorf("Pad truncated: %q", got)
	}
}

// Fit is the answer to a CLASS: a line assembled without asking whether its parts fit, then
// hard-truncated, loses the part carrying the ACTION and keeps the label. Measured three
// times in this project (known-issues L2).
func TestFitDropsFromTheTailAndNeverTheIdentity(t *testing.T) {
	const sep = " · "
	for _, c := range []struct {
		name string
		cols int
		in   []string
		want string
	}{
		{"everything fits", 40, []string{"local up", "nuc up"}, "local up · nuc up"},
		// The marker says HOW MANY are missing, and it costs three columns rather than the
		// four an ellipsis-with-separator would — on the line that is already out of room.
		{"the tail goes first", 20, []string{"local up", "nuc up", "dead down: a long reason"},
			"local up · nuc up +1"},
		{"two go", 12, []string{"local up", "nuc up", "dead down"}, "local up +2"},
		{"empty parts are not parts", 40, []string{"local up", "", "nuc up"}, "local up · nuc up"},
		{"one part, room to spare", 40, []string{"local up"}, "local up"},
	} {
		got := Fit(c.cols, sep, c.in...)
		if got != c.want {
			t.Errorf("%s: Fit(%d) = %q, want %q", c.name, c.cols, got, c.want)
		}
		if Width(got) > c.cols {
			t.Errorf("%s: Fit(%d) = %q is %d columns", c.name, c.cols, got, Width(got))
		}
	}
}

// The identity is never dropped, because a status line that omits the broken host is worse
// than one that omits why. When even the identity does not fit there is no better answer
// than truncating it — and no column is spent saying so on the line with the least room.
func TestFitKeepsTheIdentityEvenWhenItMustBeCut(t *testing.T) {
	got := Fit(6, " · ", "a-very-long-host-label", "nuc up")
	if Width(got) > 6 {
		t.Errorf("Fit = %q is %d columns, want at most 6", got, Width(got))
	}
	if got == "" {
		t.Error("Fit dropped the identity entirely")
	}
	if strings.Contains(got, "nuc") {
		t.Errorf("Fit = %q kept a later part while cutting the identity", got)
	}
}

// It counts DISPLAY columns, so a name with a wide glyph cannot overflow by counting bytes.
func TestFitMeasuresColumnsNotBytes(t *testing.T) {
	got := Fit(12, " · ", "中文中文", "nuc up")
	if Width(got) > 12 {
		t.Errorf("Fit = %q is %d columns, want at most 12", got, Width(got))
	}
	if !strings.HasPrefix(got, "中文中文") {
		t.Errorf("Fit = %q lost the identity", got)
	}
}

// The two variants differ by the MEANING of the dropped part, and the difference is
// asserted so neither call site can be changed to the other by accident.
func TestFitQuietDropsWithoutClaimingSomethingIsMissing(t *testing.T) {
	loud := Fit(12, " · ", "local up", "nuc up", "dead down")
	quiet := FitQuiet(12, " · ", "local up", "nuc up", "dead down")
	if !strings.Contains(loud, "+") {
		t.Errorf("Fit = %q dropped hosts without counting them", loud)
	}
	if strings.Contains(quiet, "+") {
		t.Errorf("FitQuiet = %q announced a drop; `+1` beside a session count reads as "+
			"\"one session is missing\"", quiet)
	}
	if Width(quiet) > 12 {
		t.Errorf("FitQuiet = %q is %d columns", quiet, Width(quiet))
	}
}

// The fallback — only the identity fits — must still SAY a part was dropped. It did not, so
// the one path that degrades hardest was the one path that lied, and the comment justifying
// it claimed "Truncate's own ellipsis already says the line was cut" when Truncate emits no
// ellipsis at all.
func TestTheFallbackStillSaysAPartWasDropped(t *testing.T) {
	// Wide enough for the identity, too narrow for identity + separator + a second part.
	got := Fit(24, " · ", "aggregator-eu-west-1 up", "dead down")
	if Width(got) > 24 {
		t.Fatalf("Fit = %q is %d columns", got, Width(got))
	}
	if !strings.Contains(got, "+") {
		t.Errorf("Fit = %q dropped a part silently — this is the path where a footer would "+
			"claim to be the whole fleet", got)
	}
	if !strings.Contains(got, "aggregator") {
		t.Errorf("Fit = %q lost the identity", got)
	}
	// FitQuiet is still quiet here: the distinction is the meaning of the part, not the path.
	if q := FitQuiet(24, " · ", "aggregator-eu-west-1 up", "dead down"); strings.Contains(q, "+") {
		t.Errorf("FitQuiet = %q announced a drop", q)
	}
}

// Truncate emits NO ellipsis, which two comments in this file used to claim it did — one of
// them as the justification for the silent fallback. Asserted so the claim cannot drift back.
func TestTruncateDoesNotAddAnEllipsis(t *testing.T) {
	got := Truncate("local up", 5)
	if strings.ContainsAny(got, "…") {
		t.Errorf("Truncate = %q added an ellipsis; the comments that assumed it did were "+
			"wrong and this is what pins that", got)
	}
}

// A width no part can fit must still return something inside the budget rather than
// overflowing or panicking.
func TestFitNeverOverflowsAtAnyWidth(t *testing.T) {
	lists := [][]string{
		{"local up"},
		{"local up", "nuc up"},
		{"aggregator-eu-west-1 up", "dead down", "→ 3 marked", "2 hidden"},
		{"中文中文 up", "nuc up"},
		{"", "nuc up", ""},
	}
	for _, parts := range lists {
		for cols := 0; cols <= 40; cols++ {
			for _, f := range []func(int, string, ...string) string{Fit, FitQuiet} {
				got := f(cols, " · ", parts...)
				if Width(got) > cols {
					t.Fatalf("width %d over budget %d for %q: %q", Width(got), cols, parts, got)
				}
			}
		}
	}
}

// TruncateMarked says that it cut, and never claims a cut that did not happen.
func TestTruncateMarkedSaysThatItCut(t *testing.T) {
	for _, c := range []struct {
		in   string
		cols int
		want string
	}{
		{"abcdef", 10, "abcdef"}, // fits: untouched
		{"abcdef", 6, "abcdef"},  // exactly at the edge: no marker, because nothing was lost
		{"abcdef", 5, "abcd…"},   // cut: the marker takes the last cell, width still 5
		{"abcdef", 1, "…"},       // one cell of room is the marker alone
		{"abcdef", 0, ""},        // no room at all
	} {
		got := TruncateMarked(c.in, c.cols)
		if got != c.want {
			t.Errorf("TruncateMarked(%q, %d) = %q, want %q", c.in, c.cols, got, c.want)
		}
		if Width(got) > c.cols {
			t.Errorf("TruncateMarked(%q, %d) = %q, which is %d cells — over the edge",
				c.in, c.cols, got, Width(got))
		}
	}
	// A CJK name is two CELLS per rune, so a rune count would put the row one column over. This is
	// the case the marker's arithmetic exists for.
	if got := TruncateMarked("中文中文", 5); Width(got) > 5 {
		t.Errorf("TruncateMarked on a wide-glyph name = %q at %d cells, want at most 5", got, Width(got))
	}
	// And the marker is not added to a string that ends exactly where the room does, which would be
	// a surface lying about a loss.
	if got := TruncateMarked("中文", 4); got != "中文" {
		t.Errorf("TruncateMarked(%q, 4) = %q, want it untouched", "中文", got)
	}
}
