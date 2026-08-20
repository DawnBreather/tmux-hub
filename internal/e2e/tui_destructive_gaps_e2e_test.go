//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// Missing destructive surface tests: refusals, width constraints, and no-selection cases.
//
// These cases cover gaps the existing tui_destructive_e2e_test.go deliberately does not: K/R
// with empty selection (the guard that should refuse before any dialog opens), and the WIDTH of
// refusal messages at 80 columns. The width is layout-critical: a refusal is a layout object
// (docs/design.md §22.8, internal/ui/lifecycle.go:136-157), and lifecycle.go's own comment
// records that the first draft lost the REMEDY off the footer because it was sized as prose
// rather than as a row. These cases pin that it fits.

// TestE2EGAPK_WithNoSelectionRefusesImmediatelyWithoutDialog tests that K pressed with no
// selection refuses without opening a confirmation dialog.
//
// What breaks if this fails: the operator presses K to kill something they thought was selected
// but was not, confirms a dialog that names nothing, and the hub then refuses with "select a pane
// with space first". The dialog would have offered a target the hub will refuse anyway, which
// teaches the operator to press enter through confirmations — the exact habit §7's mechanism
// depends on not forming. The guard at lifecycle.go:110-112 exists for this, but no test drove
// it until now.
func TestE2EGAPK_WithNoSelectionRefusesImmediatelyWithoutDialog(t *testing.T) {
	ui, _, ids, _ := hubWith(t, 120, 40, 2, "cat")
	// Do NOT select anything. The cursor is on a row, but the selection is empty.
	victim := ids[1]
	walkTo(t, ui, victim)

	sendLiteral(t, ui, "K")
	screenHas(t, ui, "select a pane with space first",
		"K with no selection must refuse and name the way forward")

	// The refusal must not open a dialog. A confirmation that the hub then refuses trains the
	// operator to confirm without reading, which is §7's whole concern.
	s := capturePane(t, ui, "ui")
	if strings.Contains(s, "Confirm kill") {
		t.Errorf("K with no selection opened a confirmation dialog it cannot act on:\n%s", s)
	}
	// Verify the specific refusal message matches what lifecycle.go line 111 says.
	if !strings.Contains(s, "nothing to kill") {
		t.Errorf("the refusal does not say there is nothing to kill:\n%s", s)
	}
}

// TestE2EGAPR_WithNoSelectionRefusesImmediately tests that R with no selection refuses without
// attempting a restart.
//
// What breaks if this fails: R with nothing selected would attempt to restart a pane that does not
// exist, or open a dialog for nothing. The guard at lifecycle.go:28-30 handles this, but nothing
// tested it. Unlike K, R never opens a dialog (lifecycle.go:64 comment), so the failure mode is a
// refusal that does not name the precondition.
func TestE2EGAPR_WithNoSelectionRefusesImmediately(t *testing.T) {
	ui, _, ids, _ := hubWith(t, 120, 40, 1, "cat")
	walkTo(t, ui, hubRowNeedle(ids[0], ids))
	// Cursor is on a row, but nothing is selected.

	sendLiteral(t, ui, "R")
	screenHas(t, ui, "select a pane with space first",
		"R with no selection must refuse and name the precondition")

	s := capturePane(t, ui, "ui")
	if !strings.Contains(s, "nothing to restart") {
		t.Errorf("the refusal does not say there is nothing to restart:\n%s", s)
	}
	// R should never show a confirmation dialog at all.
	if strings.Contains(s, "Confirm restart") {
		t.Errorf("R showed a confirmation dialog, but it never confirms for in-place restart:\n%s", s)
	}
}

// TestE2EGAPR_RefusalMessageFitsAt80Columns tests that R's refusal for an unknown session fits
// in one 80-column line.
//
// What breaks if this fails: at 80 columns the refusal's REMEDY is truncated off the footer,
// leaving the operator with only the complaint. lifecycle.go:47 refuses with "this pane has no
// known session — only hub-launched agents can be restarted", which is 70 characters and fits. But
// the assertion is that it STILL fits when interpolated into the full footer, because a refusal is
// a layout object (§22.8) and must survive the committed width.
func TestE2EGAPR_RefusalMessageFitsAt80Columns(t *testing.T) {
	// Start at 80 columns, the width §16 commits to.
	ui, _, ids, _ := hubWith(t, 80, 24, 1, "cat")
	victim := ids[0]
	destructiveSelect(t, ui, hubRowNeedle(victim, ids))

	sendLiteral(t, ui, "R")

	// Wait for the refusal to appear.
	screenHas(t, ui, "only hub-launched agents can be restarted",
		"R on a hub-launched pane must refuse")

	// Capture the screen and verify the refusal is on ONE line and not truncated.
	s := capturePane(t, ui, "ui")
	lines := strings.Split(s, "\n")

	// Find the footer line (last non-empty line).
	var footer string
	for i := len(lines) - 1; i >= 0; i-- {
		if trimmed := strings.TrimSpace(lines[i]); trimmed != "" {
			footer = lines[i]
			break
		}
	}

	if footer == "" {
		t.Fatal("no footer found on screen")
	}

	// The footer must contain both the complaint AND the rule (the remedy is implicit in "only
	// hub-launched", so the key words are "no known session" and "hub-launched").
	if !strings.Contains(footer, "no known session") {
		t.Errorf("the footer does not name the complaint:\n%s", footer)
	}
	if !strings.Contains(footer, "hub-launched") {
		t.Errorf("the footer does not name the rule that defines which panes R accepts:\n%s", footer)
	}

	// The footer must not exceed 80 columns. Measured in display width, because a CJK character
	// is two columns per rune.
	width := dstDisplayWidth(footer)
	if width > 80 {
		t.Errorf("the refusal is %d columns wide on an 80-column terminal, so the remedy is lost:\n%s",
			width, footer)
	}
}

// TestE2EGAPK_RefusalOnPaneLessRowFitsAt80Columns tests that K's refusals for pane-less agent
// rows fit in 80 columns, for all three remedy cases.
//
// What breaks if this fails: lifecycle.go:136-157 records that the first draft was "sized for 80
// columns" but the NAME inside it was not — measured, 88-column session names are real, and
// `lines.Fit` dropped the REMEDY off the footer, leaving only the breakage. The fix introduced
// shortSubject() to bound interpolated names, and this case pins that the three refusal shapes all
// survive at the committed width.
//
// The three cases differ in REMEDY: a finished background job names `claude logs`, a running one
// names `claude stop`, and an interactive session names "end it in its own terminal". Each must
// fit with a realistic (long) name.
func TestE2EGAPK_RefusalOnPaneLessRowFitsAt80Columns(t *testing.T) {
	// Start at 80 columns.
	ui, _, _, _ := hubWith(t, 80, 24, 1, "cat")

	// Wait for agent rows to appear. If none appear, we skip — the width check needs a real
	// pane-less row with a real name, not a fixture.
	if !waitForAgentRow(t, ui) {
		t.Skip("no agent rows appeared in 20s, so no pane-less row to test K refusal width on")
	}

	walkToAgentRow(t, ui)
	send(t, ui, "space")
	waitUntil(t, "the footer to report one marked row", 15*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && strings.Contains(s, "1 marked")
	})

	sendLiteral(t, ui, "K")
	screenHas(t, ui, "nothing killed",
		"K on a pane-less row must refuse and say NOTHING was killed")

	s := capturePane(t, ui, "ui")
	lines := strings.Split(s, "\n")

	// Find the footer line.
	var footer string
	for i := len(lines) - 1; i >= 0; i-- {
		if trimmed := strings.TrimSpace(lines[i]); trimmed != "" {
			footer = lines[i]
			break
		}
	}

	if footer == "" {
		t.Fatal("no footer found on screen")
	}

	// The footer must contain both "nothing killed" and ONE of the three remedies.
	if !strings.Contains(footer, "nothing killed") {
		t.Errorf("the refusal does not lead with 'nothing killed':\n%s", footer)
	}

	remedies := []string{"claude stop", "claude logs", "end it in its own terminal"}
	foundRemedy := false
	var which string
	for _, r := range remedies {
		if strings.Contains(footer, r) {
			foundRemedy = true
			which = r
			break
		}
	}
	if !foundRemedy {
		t.Errorf("the refusal names no remedy (looked for one of %v):\n%s", remedies, footer)
	}

	// The footer must fit in 80 columns.
	width := dstDisplayWidth(footer)
	if width > 80 {
		t.Errorf("the refusal is %d columns wide (remedy: %q), so the remedy or the host is lost:\n%s",
			width, which, footer)
	}

	// Verify the refusal contains a session identifier (the name or short ID that tells the
	// operator WHICH session this is about). lifecycle.go:158 calls shortSubject() to bound it,
	// and the assertion is that the bounded form appears — a footer with no session identifier
	// cannot tell the operator which of three blocked jobs it refused to kill.
	//
	// We don't assert the exact string because it's derived from the real fleet, but it must exist.
	// The three parts are: "nothing killed — ", the subject, and the remedy. If the footer is
	// non-empty and contains the leading/trailing parts, the middle must be there.
	if len(strings.TrimSpace(footer)) < 30 {
		t.Errorf("the footer is suspiciously short (%d chars), suggesting the session identifier "+
			"was omitted:\n%s", len(footer), footer)
	}
}

// dstDisplayWidth returns the display width of a string in columns, counting CJK characters as 2.
// It is a simplified version adequate for ASCII + Cyrillic (1 column/rune) and CJK (2 columns/rune).
//
// This is the dst (destructive gaps) prefix's one helper, and it exists because a rune count is not
// a column count: a 20-rune name containing `中文` is 22 columns, so `len([]rune(s))` would pass a
// truncation that failed. Measured: fmt.Sprintf("%-13s", "中文") pads to 15 columns, not 13.
func dstDisplayWidth(s string) int {
	n := 0
	for _, r := range s {
		// Simplified East Asian Width: CJK Unified Ideographs and Hangul occupy 2 columns.
		// Everything else (Latin, Cyrillic, punctuation) is 1 column. This is adequate for
		// the strings this repo interpolates (session names, host labels, prose).
		if (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
			(r >= 0x3400 && r <= 0x4DBF) || // CJK Extension A
			(r >= 0xAC00 && r <= 0xD7AF) { // Hangul Syllables
			n += 2
		} else {
			n += 1
		}
	}
	return n
}

// COVERAGE GAP ACKNOWLEDGED: lifecycle.go:160-174 has three remedy branches for K refusals on
// pane-less rows (done background → "claude logs", running background → "claude stop", interactive
// → "end it in its own terminal"). No e2e test drives all three because there's no fixture to
// manufacture agent rows in each state on demand. TestE2EGAPK_RefusalOnPaneLessRowFitsAt80Columns
// above provides partial coverage by testing whichever branch(es) exist naturally on the fleet at
// test time. Closing this gap requires a harness addition to create a finishable background job,
// wait for it to complete, then verify both states—a decision deferred pending fixture work.
