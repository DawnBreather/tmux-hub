package project

import (
	"fmt"
	"sort"
	"strings"

	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// CellWidth is the attention cell's budget.
//
// Measured with the product's own width arithmetic, the widest cell this fleet produces is
// 14 columns (`⚑ 3  ? 1  of 4`). Two-digit counts on all three facts would need 22, so the
// overflow rule below is required rather than theoretical (docs/design.md §21.2).
const CellWidth = 16

// Summary is one row of the project list: a group and the attention rolled up over it.
//
// Broken is `Error` only. `Gone` is NOT broken — §6 keeps a vanished pane with its last
// screen and it is not something to act on — so it gets no fact of its own and lives
// inside Total.
type Summary struct {
	Group   Group
	Total   int
	Waiting int
	Broken  int
	Unknown int
}

// Summarise groups the fleet and orders it the way the project list shows it: waiting,
// then broken, then unknown, then size, then name, with the unassigned bucket pinned last
// whatever its size.
//
// Pinning is not cosmetic. A bucket the operator cannot act on heading the list would push
// the answer down exactly as a globally sorted dashboard does, which is the thing this
// screen exists to fix. Pending is its own bucket rather than folded into unassigned,
// because §21.9 gives the two different remedies and a merged bucket would print the
// wrong one.
func Summarise(r Rules, rows []registry.Pane) []Summary {
	byID := map[string]*Summary{}
	var order []string
	for i := range rows {
		g := r.OfPane(rows[i])
		s := byID[g.ID]
		if s == nil {
			s = &Summary{Group: g}
			byID[g.ID] = s
			order = append(order, g.ID)
		}
		s.Total++
		switch rows[i].State() {
		case state.Needs:
			s.Waiting++
		case state.Error:
			s.Broken++
		case state.Unknown:
			s.Unknown++
		}
	}
	out := make([]Summary, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return MoreUrgent(out[i], out[j]) })
	return out
}

// MoreUrgent is the order this screen puts groups in, and it is exported because a SECOND surface now
// asks the same question: the filesystem view sorts a node's children by the attention rolled up over
// them. Two copies of "which of these wants the operator first" would drift the first time one of
// them changed, and the whole point of the roll-up is that the two screens say the same thing.
//
// The unassigned bucket goes last whatever it holds, and that is compared FIRST so no count can lift
// it: a bucket the operator cannot act on heading the list would push the answer down exactly as a
// globally sorted dashboard does, which is the thing this screen exists to fix.
func MoreUrgent(a, b Summary) bool {
	if ap, bp := a.Group.Kind == Unassigned, b.Group.Kind == Unassigned; ap != bp {
		return bp
	}
	if a.Waiting != b.Waiting {
		return a.Waiting > b.Waiting
	}
	if a.Broken != b.Broken {
		return a.Broken > b.Broken
	}
	if a.Unknown != b.Unknown {
		return a.Unknown > b.Unknown
	}
	if a.Total != b.Total {
		return a.Total > b.Total
	}
	return a.Group.Label < b.Group.Label
}

// Cell renders the attention cell, padded to width.
//
// Up to three facts then `of N`, or the word `none` where there is nothing to report. Never a state glyph
// as filler — `·` is Works and `?` is Unknown, so a filler would ASSERT a state, which an
// earlier revision of the frames did.
//
// Overflow drops from the RIGHT — unknown first, then broken — and marks the loss by
// appending `+` to the last fact kept. `of N` is never dropped: it is the one fact that
// always fits and the only one the header can be checked against. A cell that fits
// exactly is never marked, or `+` would stop meaning anything.
func Cell(s Summary, width int) string {
	total := fmt.Sprintf("of %d", s.Total)
	type fact struct{ text string }
	var facts []fact
	if s.Waiting > 0 {
		facts = append(facts, fact{fmt.Sprintf("%s %d", state.Needs.Glyph(), s.Waiting)})
	}
	if s.Broken > 0 {
		facts = append(facts, fact{fmt.Sprintf("%s %d", state.Error.Glyph(), s.Broken)})
	}
	if s.Unknown > 0 {
		facts = append(facts, fact{fmt.Sprintf("%s %d", state.Unknown.Glyph(), s.Unknown)})
	}

	build := func(keep int, marked bool) string {
		parts := make([]string, 0, keep+1)
		for i := 0; i < keep && i < len(facts); i++ {
			t := facts[i].text
			if marked && i == keep-1 {
				t += "+"
			}
			parts = append(parts, t)
		}
		parts = append(parts, total)
		return strings.Join(parts, "  ")
	}

	out := build(len(facts), false)
	for keep := len(facts); lines.Width(out) > width && keep > 0; {
		keep--
		out = build(keep, true)
	}
	if len(facts) == 0 {
		// `of N` on its own is half a sentence. Measured on the real project list, a project with
		// nothing to report read `album.QUTp1q      of 5` — the eye looks for what the 5 is OF and
		// finds nothing, while the row above reads `⚑ 3  of 9` and parses instantly.
		//
		// `none` is the missing subject and it is TRUE of exactly the three facts above: nothing
		// waiting, nothing broken, nothing unknown. It also keeps the column's one shape, since both
		// forms now end in `of N`, which is what the header is checked against. If the column is too
		// narrow for the word, `of N` survives alone — it is still the fact that must never drop.
		if withNone := "none  " + out; lines.Width(withNone) <= width {
			out = withNone
		}
	}
	return lines.Truncate(out, width)
}

// Labels is re-exported over a summary list for the caller's convenience: a screen holds
// summaries, and the collision rule is about the groups inside them.
func SummaryLabels(ss []Summary) map[string]string {
	st := make([]Group, 0, len(ss))
	for _, s := range ss {
		st = append(st, s.Group)
	}
	return Labels(st)
}
