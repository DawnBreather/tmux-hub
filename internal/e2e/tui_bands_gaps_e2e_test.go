//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/lines"
)

// BAND PROPERTIES ACROSS MODES AND WIDTHS. §16 commits to three width bands and names 80×24
// "the size to hold, not a degraded case". This file asserts that every reachable mode holds
// that commitment at both the narrow committed size and at a wide terminal — properties that
// do not depend on knowing the layout, which is what makes them worth having.
//
// The three properties:
//   - NO OVERFLOW: no drawn line may be wider than the terminal
//   - FOOTER ON SCREEN: the mode's key hint or footer must be visible, not below the fold
//   - NO TRAILING DRAWS: nothing drawn past the terminal's last row
//
// These are NOT frame tests: they assert PROPERTIES, never layout. A case that hard-codes
// "line 24 must contain X" is a layout test wearing a property's name — the property is
// "the footer is visible", which at 80×24 is satisfied by any line 1–24 and is violated
// by line 25 regardless of what the line says.

// bndMode is one TUI mode and how to reach it from the dashboard.
type bndMode struct {
	name       string   // "dashboard", "compose", etc.
	keys       []string // keystrokes to reach this mode from dashboard, empty if dashboard itself
	signal     string   // a string that proves the mode painted (its key hint or a label)
	footerLike string   // a string from the mode's footer or key hint row, for "on screen" check
}

// bndModes are the modes tested. Each is reachable from the dashboard with cheap keystrokes.
// Picker is deliberately absent: it is a startup screen when no hosts are configured, and
// reaching it deterministically from a running hub costs a file mutation mid-run.
// Every signal here is a string COPIED FROM A REAL CAPTURE, not written from memory. The first
// version of this table guessed them — "esc: leave" for the composer, "enter: filter" for the search
// field, "space: name this project" for the project list — and every one of those was wrong, so the
// case failed against a product that was working. The real footers, from the album at
// /home/dev/.claude/jobs/…/ALBUM.md:
//
//	composer:     alt+enter: newline  ·  enter: send  ·  esc: keep draft and leave
//	search:       search: ▏  ·  enter: keep · esc: cancel
//	launch form:  tab/shift+tab: move  ·  left/right: change  ·  enter: create  ·  esc: cancel
//	project list: j/k: move  ·  enter: narrow  ·  f: pin  ·  a: go to what waits  ·  esc: back
//
// HISTORY is deliberately absent: hubUI starts the hub with --view=flat --no-history, so `h` cannot open a log
// that does not exist — it sets a note instead, which is a different case and belongs with the
// history fixture that gives the hub a real log (internal/e2e/tui_history_e2e_test.go).
var bndModes = []bndMode{
	{"dashboard", nil, "tmux-hub", "up"},
	// The space MARKS a row first: `i` refuses when nothing is selected ("select a pane with space
	// first — a prompt needs a target"), so a composer opened without one never paints.
	{"compose", []string{" ", "i"}, "enter: send", "enter: send"},
	{"search", []string{"/"}, "search:", "search:"},
	{"launch", []string{"n"}, "enter: create", "enter: create"},
	// The projects list is named by its HEADER, not by its footer: measured at 80 columns on a fleet
	// with five favourites, the footer's key hints are dropped to make room for the pinned count, and
	// that is allowed precisely because the header carries the two keys that matter — `N projects  ·
	// enter narrows, esc goes back`. A case that waited for the footer was asserting the width of
	// somebody's favourites list.
	{"projects", []string{"P"}, "enter narrows", "enter narrows"},
}

// TestE2EBandPropertiesAtCommittedAndWideSize is the single case that walks every mode at
// both sizes. It is one case to keep the runtime bounded: starting the hub is expensive
// (measured: 3–6s per hubUI call with the fleet poll), and six modes × two sizes × separate
// starts would be 12 hub launches. Walking modes from one start costs only the keystrokes.
func TestE2EBandPropertiesAtCommittedAndWideSize(t *testing.T) {
	for _, size := range []struct{ cols, rows int }{
		{80, 24},  // §16's commitment: "the size to hold, not a degraded case"
		{200, 50}, // wide terminal in the grid band (160+)
	} {
		name := fmt.Sprintf("%dx%d", size.cols, size.rows)
		t.Run(name, func(t *testing.T) {
			ui, _, _ := hubUI(t, size.cols, size.rows)

			for _, mode := range bndModes {
				// Debug: capture screen state if compose fails
				if mode.name == "compose" {
					defer func() {
						if t.Failed() {
							s, _ := paneScreen(t, ui, "ui")
							t.Logf("Compose test failed. Final screen:\n%s", s)
						}
					}()
				}

				// Navigate to the mode. For compose, wait for the selection to register before
				// opening the composer, since compose REFUSES when nothing is selected.
				for i, key := range mode.keys {
					send(t, ui, key)
					if mode.name == "compose" && i == 0 {
						// The first key for compose is space (mark). Wait for the mark to register.
						waitUntil(t, "selection to register", 5*time.Second, func() bool {
							s, err := paneScreen(t, ui, "ui")
							return err == nil && strings.Contains(s, "→ 1 marked")
						})
					} else {
						time.Sleep(150 * time.Millisecond)
					}
				}

				// Wait for the mode's signal to appear, proving it painted. Waiting for the
				// signal the ASSERTION is about is the rule this repo already wrote down;
				// header paints before the mode does, so waiting for "tmux-hub" would race.
				waitUntil(t, mode.name+" mode to paint at "+name, 10*time.Second, func() bool {
					s, err := paneScreen(t, ui, "ui")
					return err == nil && strings.Contains(s, mode.signal)
				})

				screen := capturePane(t, ui, "ui")
				bndAssertProperties(t, mode.name, screen, size.cols, size.rows, mode.footerLike)

				// Return to dashboard for the next mode, but only if we navigated away from it.
				// The dashboard itself has no keys (mode.keys is empty), so skip the return step.
				// All overlay modes (compose, search, launch, history) exit on Escape.
				// Projects list also exits on Escape per docs/design.md §21.12 ("esc leaves").
				if len(mode.keys) > 0 {
					send(t, ui, "Escape")
					// Wait for the mode's overlay/screen to close. Projects changes the header to
					// "16 projects"; when that's gone and "sessions" returns, we're back.
					// Overlays keep the header but change the footer; when the footer no longer
					// has the mode's key hints, we're back.
					waitUntil(t, "return to dashboard after "+mode.name, 5*time.Second, func() bool {
						s, err := paneScreen(t, ui, "ui")
						// Projects is the only mode that hides "sessions" from the header.
						// For overlays, check that their signal is gone (which proves they closed).
						// Both checks together work for all modes: after Escape, the overlay footer
						// disappears OR the projects header is replaced by the session count.
						return err == nil && strings.Contains(s, "sessions") && !strings.Contains(s, mode.signal)
					})
				}
			}
		})
	}
}

// bndAssertProperties checks the three band properties for one mode at one size.
func bndAssertProperties(t *testing.T, mode, screen string, cols, rows int, footerLike string) {
	t.Helper()

	screenLines := strings.Split(screen, "\n")

	// Strip trailing blank lines: capture-pane returns the full terminal height, including
	// empty rows below what the hub drew. The DRAWN line count is what we assert about.
	for len(screenLines) > 0 && strings.TrimSpace(screenLines[len(screenLines)-1]) == "" {
		screenLines = screenLines[:len(screenLines)-1]
	}

	// Property 1: NO OVERFLOW. Every drawn line must fit the terminal width.
	// Measured in display columns, not bytes or runes, because the hub draws content with
	// double-width glyphs and ANSI escapes that a byte count would misreport.
	for i, line := range screenLines {
		if w := lines.Width(line); w > cols {
			t.Errorf("%s at %dx%d: line %d is %d display columns wide (terminal is %d): %q\n"+
				"an operator sees the line wrap or bleed outside the terminal",
				mode, cols, rows, i+1, w, cols, line)
		}
	}

	// Property 2: FOOTER ON SCREEN. The mode's key hint or footer must be visible, not below
	// the fold. footerLike is a substring of the footer row; it does not have to be the whole
	// footer, because the footer's content changes with state (marked panes, hidden rows, etc.)
	// and asserting an exact string would be a state test wearing a geometry name.
	var footerFound bool
	for i, line := range screenLines {
		if strings.Contains(line, footerLike) {
			footerFound = true
			// Also assert it's within the terminal's row count, not in overflow. A line that
			// is on the drawn output but past row N is below the fold when the terminal is
			// N rows tall, because capture-pane returns the full alt-screen regardless of what
			// fits in a real terminal.
			if i+1 > rows {
				t.Errorf("%s at %dx%d: the footer is on line %d, past the terminal's %d rows\n"+
					"the operator cannot see the key hints for what they can press:\n%s",
					mode, cols, rows, i+1, rows, screen)
			}
			break
		}
	}
	if !footerFound {
		t.Errorf("%s at %dx%d: the footer or key hint %q is not on the screen at all\n"+
			"the operator has no indication of what keys do in this mode:\n%s",
			mode, cols, rows, footerLike, screen)
	}

	// Property 3: NO TRAILING DRAWS. Nothing may be drawn past the last row. The hub claims
	// the full alt-screen, so the drawn line count must not exceed the terminal's row count.
	// A hub that draws 25 lines on a 24-row terminal has pushed its own header off the top,
	// which is exactly the defect tui_resize_e2e_test.go's frame case is built around.
	if len(screenLines) > rows {
		t.Errorf("%s at %dx%d: the hub drew %d lines on a %d-row terminal\n"+
			"the operator has lost the top of the screen:\n%s",
			mode, cols, rows, len(screenLines), rows, screen)
	}
}
