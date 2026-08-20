// Package lines classifies a captured pane line as chrome or content, and
// measures and truncates by display width.
//
// A tile that renders "the last N lines" of a pane shows nothing useful: on a
// real 80x24 Claude Code pane only 6 of 25 lines are content, and the bottom 10
// are rule lines, an empty prompt box, a constant footer and blanks. Measured
// against four other pane kinds, the raw tile failed on every non-alt-screen
// pane for the same reason — once a command returns, the bottom of a shell pane
// is a prompt and blank space. See docs/design.md §6.
package lines

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

type Kind int

const (
	Blank Kind = iota
	Rule
	Prompt
	Footer
	Spinner
	Content
)

func (k Kind) String() string {
	switch k {
	case Blank:
		return "blank"
	case Rule:
		return "rule"
	case Prompt:
		return "prompt"
	case Footer:
		return "footer"
	case Spinner:
		return "spinner"
	default:
		return "content"
	}
}

var (
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)
	// A rule is a run of box-drawing or dash characters and nothing else.
	ruleRe = regexp.MustCompile(`^[\p{Pd}\x{2500}-\x{257F}_=]{8,}$`)
	// An empty prompt: a marker, optionally one cursor-ish glyph, nothing more.
	promptRe = regexp.MustCompile(`^[❯>\$#][\s\x{00a0}]*$`)
	// Footer text is Claude Code's own status furniture. Patterns live here for
	// now; they move to a config file when the classifier is calibrated on a
	// wider sample (docs/design.md §12 records that this stays a heuristic).
	// The mode indicators are a closed set of literals in Claude Code's bundle:
	// manual mode, plan mode, accept edits, bypass permissions, don't ask, auto
	// mode. Knowing only two of them made the footer line read as CONTENT in the
	// other four modes, so it leaked into every tile.
	footerRe = regexp.MustCompile(`(manual mode|plan mode|accept edits|bypass permissions|don't ask|auto mode|shift\+tab|for agents|new task\?|/clear to save|esc to interrupt|\d+k tokens)`)
	spinRe   = regexp.MustCompile(`^[✻✽✢·*]\s`)
)

// StripANSI removes CSI escape sequences.
func StripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// Normalize prepares a captured line for MATCHING: it strips CSI escapes and folds
// every Unicode space to a plain ASCII space.
//
// The folding is not cosmetic, and it fixes a silent failure in the most important
// state there is. Claude Code renders its prompt as `❯` followed by **U+00A0**, a
// no-break space — measured off a live pane. Go's regexp `\s` is ASCII-only and
// does not match U+00A0, so every SHAPE pattern in the state classifier failed
// against the real screen: `❯<NBSP>[✔] context7`, the MCP approval dialog,
// classified as `quiet` rather than `needs`, and the numbered trust dialog survived
// only because a LITERAL matched it. That is backwards from the design, where the
// shape is load-bearing and the words are corroboration.
//
// Folding at the seam kills the class rather than patching one pattern: no matcher
// downstream has to know that a space might not be a space.
func Normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range StripANSI(s) {
		if r != '\n' && unicode.IsSpace(r) {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Classify labels one captured line.
func Classify(line string) Kind {
	bare := Normalize(line)
	trimmed := strings.TrimRight(bare, " \t")
	compact := strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == ' ' {
			return ' '
		}
		return r
	}, trimmed))

	if compact == "" {
		return Blank
	}
	if ruleRe.MatchString(compact) {
		return Rule
	}
	if promptRe.MatchString(compact) {
		return Prompt
	}
	if footerRe.MatchString(compact) {
		return Footer
	}
	if spinRe.MatchString(compact) {
		return Spinner
	}
	return Content
}

// Width is the number of terminal cells a string occupies.
func Width(s string) int {
	n := 0
	for _, r := range StripANSI(s) {
		n += runeWidth(r)
	}
	return n
}

func runeWidth(r rune) int {
	switch {
	case r == ' ':
		return 1
	case unicode.Is(unicode.Mn, r), unicode.Is(unicode.Me, r), unicode.Is(unicode.Cf, r):
		return 0
	case wide(r):
		return 2
	default:
		return 1
	}
}

// wide covers the East Asian Wide and Fullwidth ranges the hub can encounter.
func wide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0xA4CF, // CJK radicals through Yi
		r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK compatibility
		r >= 0xFE30 && r <= 0xFE6F, // CJK forms
		r >= 0xFF00 && r <= 0xFF60, // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1F64F, // emoji blocks in common use
		r >= 0x20000 && r <= 0x3FFFD:
		return true
	}
	return false
}

const sgrReset = "\x1b[0m"

// Truncate cuts a line to cols display cells without splitting an escape
// sequence, and re-emits an SGR reset so colour cannot bleed past a tile edge.
func Truncate(s string, cols int) string {
	if cols <= 0 {
		return ""
	}
	var b strings.Builder
	sawEscape := false
	used := 0
	rs := []rune(s)
	for i := 0; i < len(rs); {
		if rs[i] == 0x1b {
			// Copy the whole escape sequence; it costs no cells.
			j := i + 1
			for j < len(rs) && !unicode.IsLetter(rs[j]) {
				j++
			}
			if j < len(rs) {
				j++
			}
			b.WriteString(string(rs[i:j]))
			sawEscape = true
			i = j
			continue
		}
		w := runeWidth(rs[i])
		if used+w > cols {
			break
		}
		b.WriteRune(rs[i])
		used += w
		i++
	}
	out := b.String()
	if sawEscape && !strings.HasSuffix(out, sgrReset) {
		out += sgrReset
	}
	return out
}

// TruncateMarked is Truncate plus the one thing Truncate deliberately does not do: it SAYS that it
// cut. The last cell becomes `…`, so a line that ends because the name ran out is distinguishable
// from a line that ends because the terminal did.
//
// It exists because three surfaces were cutting silently at the one width this project commits to,
// and each loss was of the part the reader needed most:
//
//	a list row:      20260803--store-online-takes-too-long-to-ci-cd-troubleshooting ⑂ дав
//	a tile's header: ┌─ scratch 20260803--store-online-takes-too-long-to-ci-cd-troubleshooti┐
//	a warning:       • this pane does not accept pasted text — it will read the prompt as keypresse
//
// The third is the expensive one: that sentence is the operator's only warning that a send may be
// read as keystrokes, and it lost its object. The footer's own `Fit` has always marked a dropped
// claimant with `+N`, so the vocabulary for "something is missing" already existed on the screen —
// this gives the same honesty to a line that is cut rather than dropped.
//
// Truncate KEEPS its contract, because it has callers for whom a marker would be wrong: a tile's
// body is a copy of somebody else's screen, and adding a character to it would be putting words in
// another program's mouth.
func TruncateMarked(s string, cols int) string {
	if cols <= 0 {
		return ""
	}
	out := Truncate(s, cols)
	if Width(out) >= Width(s) {
		// Nothing was cut — including the case where the string ends exactly at the edge, where a
		// marker would claim a loss that did not happen.
		return out
	}
	// The marker replaces the last cell rather than being appended, so the result is never wider
	// than what was asked for. Measured in CELLS: a CJK name is two per rune, so cutting a rune
	// count would leave the row one column over the edge at exactly the widths this exists for.
	room := cols - 1
	if room <= 0 {
		return Truncate("…", cols)
	}
	return Truncate(s, room) + "…"
}

// ContentTail returns the last n content-bearing lines, chrome removed. Spinner
// lines are kept: "Churned for 6s" is information even though it is furniture.
func ContentTail(all []string, n int) []string {
	var keep []string
	for _, l := range all {
		switch Classify(l) {
		case Content, Spinner:
			keep = append(keep, strings.TrimRight(l, " \t"))
		}
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return keep
}

// Pad right-pads s to cols DISPLAY columns, and never truncates.
//
// It exists because `strings.Repeat(" ", cols-lines.Width(s))` was written out at four
// call sites in the renderer, and width arithmetic is the one thing in this project that
// must have a single owner: `len(s)` is wrong for every non-ASCII name, a CJK glyph is two
// columns wide, and a name a person typed can hold either. Four copies of the subtraction
// are four chances for one of them to use len.
//
// A string already at or over cols is returned unchanged: padding is alignment, and
// deciding what to cut is Truncate's job. A caller that wants both composes them, which
// makes the order of the two decisions visible.
func Pad(s string, cols int) string {
	w := Width(s)
	if w >= cols {
		return s
	}
	return s + strings.Repeat(" ", cols-w)
}

// Fit joins parts with sep into at most cols display columns, dropping from the TAIL until
// the result fits and marking the loss with ` +N` — the count of what was dropped. (An
// earlier version of this sentence said "an ellipsis", which the code has never done.)
//
// It exists because truncation at 80 columns was a CLASS in this project rather than three
// bugs (known-issues L2): a footer lost the one host that was DOWN because an earlier host's
// reason ate the room, a window-path hint lost its sentence at 86 runes, and a timed-out
// host's remedy was cut mid-word at both 80 and 100. All three were a hard Truncate on a
// line assembled without asking whether the parts fit, and all three lost the part carrying
// the ACTION while keeping the part carrying the label.
//
// So the caller states PRIORITY instead of hoping: identity first, action second,
// explanation last. A line that does not fit then degrades by design.
//
// The FIRST part is never dropped. A line with no identity is worse than a truncated one —
// a status line that omits the broken host is worse than one that omits why — so if even
// that does not fit, it is truncated, which is the one case where there is no better answer.
func Fit(cols int, sep string, parts ...string) string {
	return fit(cols, sep, true, parts...)
}

// FitQuiet is Fit without the `+N` marker, for parts whose absence is not a loss the
// operator needs to be told about.
//
// The distinction is the MEANING of the dropped part, not a style choice. A dropped HOST is
// data about the fleet, so the count matters: without it the line silently claims to be the
// whole fleet. A dropped HINT is a reminder — and `tmux-hub  2 sessions +1` on the line that
// carries the session count reads as "one session is missing", which is a lie about the
// fleet, and a worse one than the truncation it replaced. Measured: that is exactly what the
// header printed at 80 columns before this split existed.
func FitQuiet(cols int, sep string, parts ...string) string {
	return fit(cols, sep, false, parts...)
}

func fit(cols int, sep string, mark bool, parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	// Down to ONE part, still marked. Falling out of the loop at n == 2 would make the
	// worst case — only the identity fits — the one case that loses its parts silently,
	// which is where saying so matters most.
	for n := len(kept); n >= 1; n-- {
		out := strings.Join(kept[:n], sep)
		if mark && n < len(kept) {
			// `+N`, not an ellipsis, for two reasons: it says HOW MANY parts are missing,
			// which an ellipsis does not, and it costs three columns against four — on the
			// line that is already out of room, and where the marker competing with the
			// content is what made the first attempt drop a part it could have kept.
			out += fmt.Sprintf(" +%d", len(kept)-n)
		}
		if Width(out) <= cols {
			return out
		}
	}
	// Not even the identity plus its marker fits, so the identity is CUT to pay for the
	// marker — a cut label still identifies the host, while a missing `+N` misstates the
	// fleet, and this is the path that degrades hardest so it is the last place to go quiet.
	//
	// An earlier version returned the identity unmarked and justified it with "Truncate's own
	// ellipsis already says the line was cut". Truncate emits no ellipsis — only an SGR reset
	// — so the justification was false and the one silent path was the one that mattered
	// most. TestTruncateDoesNotAddAnEllipsis pins that.
	if mark && len(kept) > 1 {
		marker := fmt.Sprintf(" +%d", len(kept)-1)
		if w := Width(marker); w < cols {
			return Truncate(kept[0], cols-w) + marker
		}
	}
	return Truncate(kept[0], cols)
}
