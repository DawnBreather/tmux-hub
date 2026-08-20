package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// The details band is pinned to the BOTTOM at every width, and the inbox gets the whole terminal.
//
// Measured before: at 100 and 160 columns the inbox was pinned to InboxWidth (28) with tiles to the
// RIGHT, so a row had 28 columns for point, mark, glyph, a six-column state word and the name — and
// the name was cut to about fifteen:
//
//	  80 cols  > ⚑ needs  20260809--рендеринг-карты
//	 100 cols  > ⚑ needs  20260809--рендери ┌─ local sess1 %1 ────────────…
//
// A WIDER terminal showed LESS of the name than a narrower one, while the tile beside it held an
// almost empty box across seventy columns. Non-monotonic layout is a class this project already
// names (known-issues, the footer's own rule), and here it cost the one field that identifies a
// session to a person.

const detailsLongName = "20260817-cicd-pipeline-rework"

func detailsFixture(t *testing.T, cols, rows int) model {
	t.Helper()
	m := base(t, cols, rows,
		pane("local", "w", "claude", 1, "claude", state.Needs),
		agentRow("30f3382b", detailsLongName, "background", state.Works),
		agentRow("45dfef2f", "20260809--рендеринг-карты", "background", state.Needs))
	return m
}

// inboxRows are the body lines above the details band: everything up to its top border.
func inboxRows(screen string) []string {
	var out []string
	for _, ln := range strings.Split(strings.TrimRight(screen, "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "┌─") {
			break
		}
		out = append(out, ln)
	}
	return out
}

func TestALongSessionNameIsWholeAtEveryPromisedWidth(t *testing.T) {
	for _, cols := range []int{80, 100, 160, 200} {
		screen := detailsFixture(t, cols, 24).View()
		if !strings.Contains(screen, detailsLongName) {
			t.Errorf("%d cols: the name is cut:\n%s", cols, screen)
		}
		for _, ln := range strings.Split(screen, "\n") {
			if lines.Width(ln) > cols {
				t.Errorf("%d cols: a line is %d wide: %q", cols, lines.Width(ln), ln)
			}
		}
	}
}

// The band is at the BOTTOM, so the last body row belongs to it and no inbox row sits under it.
func TestTheDetailsBandIsAtTheBottom(t *testing.T) {
	for _, cols := range []int{80, 100, 160, 200} {
		m := detailsFixture(t, cols, 24)
		screen := m.View()
		lns := strings.Split(strings.TrimRight(screen, "\n"), "\n")
		top := -1
		for i, ln := range lns {
			if strings.HasPrefix(strings.TrimSpace(ln), "┌─") {
				top = i
				break
			}
		}
		if top < 0 {
			t.Fatalf("%d cols: no details band on the screen:\n%s", cols, screen)
		}
		// Everything from the band's top to the footer is the band; no row after it may carry an
		// inbox row's shape (a state word after a cursor point).
		for _, ln := range lns[top : len(lns)-1] {
			if strings.HasPrefix(ln, ">") || strings.HasPrefix(ln, " ◆") {
				t.Errorf("%d cols: an inbox row is BELOW the details band: %q", cols, ln)
			}
		}
		// And the band ends where the body ends: the footer is the last row, the band's last line
		// the one before it.
		if closing := strings.TrimSpace(lns[len(lns)-2]); !strings.HasPrefix(closing, "└") &&
			!strings.HasPrefix(closing, "│") {
			t.Errorf("%d cols: the row above the footer is not the band's: %q", cols, closing)
		}
	}
}

// PINNED means the band's top edge does not move when the list grows. That is the assertion the first
// version of this case missed: it only checked that the row above the footer belonged to the band,
// which is true of a band drawn directly under the list as well — and a mutation that dropped the
// padding survived it while making the frame short.
func TestTheBandsTopEdgeDoesNotMoveWithTheListsLength(t *testing.T) {
	rowsFor := func(n int) int {
		var ps []registry.Pane
		for i := 0; i < n; i++ {
			ps = append(ps, agentRow(fmt.Sprintf("%08d", i), fmt.Sprintf("sess-%03d", i),
				"background", state.Idle))
		}
		m := base(t, 100, 24, ps...)
		screen := m.View()
		for i, ln := range strings.Split(screen, "\n") {
			if strings.HasPrefix(strings.TrimSpace(ln), "┌─") {
				return i
			}
		}
		t.Fatalf("%d sessions drew no band:\n%s", n, screen)
		return -1
	}
	want := rowsFor(1)
	for _, n := range []int{2, 5, 9, 14, 40} {
		if got := rowsFor(n); got != want {
			t.Errorf("with %d sessions the band starts on row %d, with 1 session on row %d — a band "+
				"whose top edge moves is not pinned, and the list under the cursor shifts with it",
				n, got, want)
		}
	}
}

// A wider terminal never shows less. This is the assertion the old layout could not pass, and it
// needs no knowledge of the layout to make.
func TestWiderNeverShowsLessOfAName(t *testing.T) {
	shown := func(cols int) int {
		screen := detailsFixture(t, cols, 24).View()
		best := 0
		for _, ln := range inboxRows(screen) {
			if i := strings.Index(ln, "20260817"); i >= 0 {
				if n := lines.Width(strings.TrimSpace(ln[i:])); n > best {
					best = n
				}
			}
		}
		return best
	}
	prev := 0
	for _, cols := range []int{80, 90, 100, 120, 140, 160, 180, 200} {
		got := shown(cols)
		if got < prev {
			t.Errorf("%d cols shows %d columns of the name where a narrower terminal showed %d",
				cols, got, prev)
		}
		prev = got
	}
}

// The frame is exactly as tall as the terminal, whatever the band does.
func TestTheFrameStaysExactlyAsTallAsTheTerminal(t *testing.T) {
	for _, rows := range []int{10, 24, 40, 60} {
		for _, cols := range []int{80, 160} {
			screen := detailsFixture(t, cols, rows).View()
			if got := len(strings.Split(screen, "\n")); got != rows {
				t.Errorf("%dx%d produced %d rows", cols, rows, got)
			}
		}
	}
}
