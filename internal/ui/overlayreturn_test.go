package ui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/project"
	tea "github.com/charmbracelet/bubbletea"
)

// EVERY OVERLAY RETURNS TO THE SCREEN THAT RAISED IT, both on cancel and on commit.
//
// This is the mechanism's raison d'être: before it existed, every overlay returned to
// modeBrowse hard-coded, so cancelling a project name from the tree threw the operator back
// to the dashboard while committing kept them on the tree — cancelling cost strictly more than
// committing, which is backwards. The fix is that `raise` records the screen underneath and
// `dismiss` returns to it, so the two paths are symmetric and the operator lands where they
// started regardless of which path they took out.
//
// The table covers every overlay the product can raise (compose, naming, launch, search) from
// both screens that can raise overlays (dashboard, tree), asserting that cancelling each one
// returns to the screen it was raised over. Confirm is covered separately (see test 4). This
// is the property the underlay field exists to preserve.
func TestEveryOverlayReturnsToTheScreenThatRaisedIt(t *testing.T) {
	cases := []struct {
		name       string
		screen     uiMode            // the screen to raise from
		setupMode  func(model) model // how to reach that screen
		raiseKey   string            // the key that opens the overlay
		expectMode uiMode            // the overlay it should open
		cancelKey  tea.KeyType       // the key that cancels it
	}{
		// From dashboard
		{name: "compose from dashboard", screen: modeBrowse, setupMode: func(m model) model { return m }, raiseKey: "i", expectMode: modeCompose, cancelKey: tea.KeyEsc},
		{name: "naming from dashboard", screen: modeBrowse, setupMode: func(m model) model { return m }, raiseKey: "N", expectMode: modeNaming, cancelKey: tea.KeyEsc},
		{name: "launch from dashboard", screen: modeBrowse, setupMode: func(m model) model { return m }, raiseKey: "n", expectMode: modeLaunch, cancelKey: tea.KeyEsc},
		{name: "search from dashboard", screen: modeBrowse, setupMode: func(m model) model { return m }, raiseKey: "/", expectMode: modeSearch, cancelKey: tea.KeyEsc},
		// From tree - use treeModelOpened then walk to session row for keys that need it
		{name: "compose from tree", screen: modeTree, setupMode: func(m model) model { return walkToSessionRow(t, treeModelOpened(t)) }, raiseKey: "i", expectMode: modeCompose, cancelKey: tea.KeyEsc},
		{name: "naming from tree", screen: modeTree, setupMode: func(m model) model { return walkToSessionRow(t, treeModelOpened(t)) }, raiseKey: "N", expectMode: modeNaming, cancelKey: tea.KeyEsc},
		{name: "launch from tree", screen: modeTree, setupMode: func(m model) model { return walkToSessionRow(t, treeModelOpened(t)) }, raiseKey: "n", expectMode: modeLaunch, cancelKey: tea.KeyEsc},
		{name: "search from tree", screen: modeTree, setupMode: func(m model) model { return key(t, m, "t") }, raiseKey: "/", expectMode: modeSearch, cancelKey: tea.KeyEsc},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := base(t, 120, 24, treeFleet()...)
			m.home = "/home/dev"
			m = tc.setupMode(m)
			if m.mode != tc.screen {
				t.Fatalf("setup did not reach %v, mode is %v", tc.screen, m.mode)
			}

			// Mark a row if the key needs a subject (compose, naming). Space works on both screens
			// but only when the cursor is on a session row, which search-from-tree's setup doesn't
			// guarantee (it just opens the tree with `t`, not walking to a session).
			if tc.raiseKey == "i" || tc.raiseKey == "N" {
				m = key(t, m, " ")
				if m.sel.Len() == 0 {
					t.Fatalf("space did not mark a row on %v", tc.screen)
				}
			}

			// Raise the overlay
			m = key(t, m, tc.raiseKey)
			if m.mode != tc.expectMode {
				t.Fatalf("%s did not open %v, mode is %v", tc.raiseKey, tc.expectMode, m.mode)
			}
			if m.underlay != tc.screen {
				t.Errorf("underlay is %v, want %v (the screen we raised from)", m.underlay, tc.screen)
			}

			// Cancel it
			m = special(t, m, tc.cancelKey)
			if m.mode != tc.screen {
				t.Errorf("cancelling %v left mode %v, want %v (the screen it was raised over)",
					tc.expectMode, m.mode, tc.screen)
			}
		})
	}
}

// walkToSessionRow moves the cursor down to the first session row it finds. Keys like i, N, n
// work on session rows (they refuse directory/volume lines), and space marks the row under the
// cursor, so tests that press those keys from the tree must call this after treeModelOpened.
func walkToSessionRow(t *testing.T, m model) model {
	t.Helper()
	lines := m.treeShown()
	for i, l := range lines {
		if l.IsRow {
			// Found a session row - walk cursor there
			for j := 0; j < i; j++ {
				m = key(t, m, "j")
			}
			return m
		}
	}
	t.Fatal("no session rows in tree - cannot walk to one")
	return m
}

// CONFIRM RAISED FROM COMPOSE CANNOT BE TESTED in the table above because the fixture fleet
// has no runner, so compose's enter sets a note and stays in compose. The confirm dialog is
// raised from compose's enter when the draft is non-empty and there are pending targets, which
// is a shape the table does not construct. This case is covered separately in test 4 below
// (DismissNeverLandsOnAnOverlay), which exercises the composer → confirm → cancel path and
// asserts the final mode is the underlay the composer was raised over, not the composer itself.

// THE COMMIT PATH RETURNS TOO, not only the cancel path.
//
// Before the underlay mechanism, cancelling a name from the tree threw the operator to the dashboard
// while COMMITTING the name kept them on the tree — one outcome per path, and the worse outcome on
// the safer key. The fix makes both paths symmetric: naming raised from the tree and SAVED must come
// back to the tree, not to the dashboard. This is the property that makes cancelling and committing
// cost the same, which is the only sensible contract.
func TestNamingCommittedFromTheTreeReturnsToTheTree(t *testing.T) {
	// Reach the tree with directories opened and cursor on a session row
	m := treeModelOpened(t)
	if m.mode != modeTree {
		t.Fatalf("treeModelOpened did not give modeTree, mode is %v", m.mode)
	}
	m = walkToSessionRow(t, m)

	// Configure projects.toml path so the name can be saved
	m.projectsPath = filepath.Join(t.TempDir(), "projects.toml")

	// Open naming overlay from tree
	m = key(t, m, "N")
	if m.mode != modeNaming {
		t.Fatalf("N did not open naming, mode is %v", m.mode)
	}
	if m.underlay != modeTree {
		t.Errorf("underlay is %v, want modeTree", m.underlay)
	}

	// Type a name and commit it with enter
	m = typeInto(t, m, "test-name")

	// Capture the subject BEFORE pressing enter - this is the row the overlay is naming
	subject := m.naming.subject

	m = special(t, m, tea.KeyEnter)

	// The commit path must return to the tree, not the dashboard
	if m.mode != modeTree {
		t.Errorf("committing a name from the tree left mode %v, want modeTree", m.mode)
	}

	// Verify the alias actually landed (look up the row the overlay named, not cursorRow)
	key := project.AliasKeyOf(subject)
	got, ok := m.aliases.Get(key)
	if !ok || got != "test-name" {
		t.Errorf("alias for %v is %q (found=%v), want %q (commit must save, not just return)", key, got, ok, "test-name")
	}
}

// THE BACKDROP IS THE SCREEN UNDERNEATH, and an overlay raised from the tree paints the tree
// behind it while one raised from the dashboard paints the dashboard.
//
// This is what makes the overlay LOOK like an overlay: the screen you were on is still visible
// behind the dialog, so you can see what you are acting on. The backdrop function decides which
// screen to paint based on Frame.Screen, which View() sets by looking at the underlay when the
// current mode is an overlay. The assertion uses strings that belong to exactly one surface:
// tree draws `▾ ` (open-node arrow); dashboard draws `tmux-hub  ` (title line).
func TestTheBackdropIsTheScreenUnderneath(t *testing.T) {
	cases := []struct {
		name      string
		screen    uiMode
		setup     func(model) model
		overlay   string
		expectIn  string // a string only this screen can produce
		expectOut string // a string the OTHER screen produces, which must be absent
	}{
		// Compose
		{
			name:      "compose from tree shows tree backdrop",
			screen:    modeTree,
			setup:     func(m model) model { return walkToSessionRow(t, treeModelOpened(t)) },
			overlay:   "i",
			expectIn:  "▾ ",         // open-node arrow - tree only
			expectOut: "tmux-hub  ", // dashboard title - never in tree
		},
		{
			name:      "compose from dashboard shows dashboard backdrop",
			screen:    modeBrowse,
			setup:     func(m model) model { return m },
			overlay:   "i",
			expectIn:  "tmux-hub  ", // dashboard title
			expectOut: "▾ ",         // tree open-node arrow - never on dashboard
		},
		// Naming
		{
			name:      "naming from tree shows tree backdrop",
			screen:    modeTree,
			setup:     func(m model) model { return walkToSessionRow(t, treeModelOpened(t)) },
			overlay:   "N",
			expectIn:  "▾ ",
			expectOut: "tmux-hub  ",
		},
		{
			name:      "naming from dashboard shows dashboard backdrop",
			screen:    modeBrowse,
			setup:     func(m model) model { return m },
			overlay:   "N",
			expectIn:  "tmux-hub  ",
			expectOut: "▾ ",
		},
		// Launch
		{
			name:      "launch from tree shows tree backdrop",
			screen:    modeTree,
			setup:     func(m model) model { return walkToSessionRow(t, treeModelOpened(t)) },
			overlay:   "n",
			expectIn:  "▾ ",
			expectOut: "tmux-hub  ",
		},
		{
			name:      "launch from dashboard shows dashboard backdrop",
			screen:    modeBrowse,
			setup:     func(m model) model { return m },
			overlay:   "n",
			expectIn:  "tmux-hub  ",
			expectOut: "▾ ",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := base(t, 120, 24, treeFleet()...)
			m.home = "/home/dev"
			m = tc.setup(m)
			if m.mode != tc.screen {
				t.Fatalf("setup did not reach %v, mode is %v", tc.screen, m.mode)
			}

			// Raise the overlay
			m = key(t, m, tc.overlay)
			screen := m.View()

			// The backdrop must contain the expected screen's signature string
			if !strings.Contains(screen, tc.expectIn) {
				t.Errorf("backdrop does not contain %q (the %v screen's signature)", tc.expectIn, tc.screen)
			}

			// And must NOT contain the other screen's signature
			if strings.Contains(screen, tc.expectOut) {
				t.Errorf("backdrop contains %q (the OTHER screen's signature, not %v)", tc.expectOut, tc.screen)
			}
		})
	}
}

// DISMISS NEVER LANDS ON AN OVERLAY, and this is the safety property that makes nesting safe.
//
// The composer's enter raises the confirm dialog, so the nesting is composer-over-screen →
// confirm-over-composer. When the confirm is dismissed, the underlay it returns to must be
// the SCREEN, not the composer — otherwise the operator lands in the composer with no way out
// except to send or to lose their draft. The implementation is that `raise` only sets underlay
// when the current mode is NOT an overlay, so the confirm inherits the composer's underlay rather
// than making the composer its own, and `dismiss` asserts this property: if the underlay is itself
// an overlay, it falls back to modeBrowse rather than returning to it.
//
// This test exercises the pair directly — for every mode, raise then dismiss returns the original
// mode, and raise-from-inside-an-overlay keeps the earlier underlay — and also exercises the real
// nesting path (composer → confirm → cancel) to prove it lands on the screen the composer was raised
// over, not in the composer.
func TestDismissNeverLandsOnAnOverlay(t *testing.T) {
	// For every SCREEN, raise an overlay then dismiss it — the result must be the original screen.
	//
	// Screens, not every mode: `raise` from inside an OVERLAY deliberately keeps the earlier underlay
	// (that is what makes the composer's enter → confirm dialog land back on the screen rather than in
	// the composer), so "dismiss returns to where you were" is a claim about screens only. The list is
	// derived from `isOverlay` below rather than written twice, so a mode reclassified in one place
	// cannot leave a stale expectation here — which is exactly what happened when the picker moved: it
	// draws a base above the rule, so it is an overlay by its own renderer's words, and this table used
	// to assert that an overlay could be returned to.
	allModes := []struct {
		mode uiMode
		name string
	}{
		{modeBrowse, "modeBrowse"},
		{modeHistory, "modeHistory"},
		{modePicker, "modePicker"},
		{modeProjects, "modeProjects"},
		{modeTree, "modeTree"},
	}
	for _, tc := range allModes {
		if tc.mode.isOverlay() {
			// An overlay is not a screen and cannot be returned to; the nesting case below is what
			// covers raising from inside one.
			continue
		}
		t.Run("raise from "+tc.name, func(t *testing.T) {
			mode := tc.mode
			m := model{mode: mode, width: 120, height: 24}
			original := m.mode

			raised := m.raise(modeCompose)
			if raised.mode != modeCompose {
				t.Fatalf("raise did not set mode to modeCompose, got %v", raised.mode)
			}
			if raised.underlay != original {
				t.Errorf("underlay is %v, want %v (the mode we raised from)", raised.underlay, original)
			}

			dismissed := raised.dismiss()
			if dismissed.mode != original {
				t.Errorf("dismiss landed on %v, want %v (the original mode)", dismissed.mode, original)
			}
		})
	}

	// Raise from inside an overlay keeps the earlier underlay, not the current overlay
	t.Run("raise from inside overlay keeps original underlay", func(t *testing.T) {
		m := model{mode: modeBrowse, width: 120, height: 24}
		// Raise composer over the dashboard
		m = m.raise(modeCompose)
		if m.underlay != modeBrowse {
			t.Fatalf("composer's underlay is %v, want modeBrowse", m.underlay)
		}
		// Raise confirm over the composer — its underlay must be modeBrowse, not modeCompose
		m = m.raise(modeConfirm)
		if m.mode != modeConfirm {
			t.Fatalf("second raise did not set mode to modeConfirm, got %v", m.mode)
		}
		if m.underlay != modeBrowse {
			t.Errorf("confirm's underlay is %v, want modeBrowse (not modeCompose)", m.underlay)
		}
		// Dismiss the confirm — it must land on modeBrowse, not modeCompose
		m = m.dismiss()
		if m.mode != modeBrowse {
			t.Errorf("dismiss landed on %v, want modeBrowse (not modeCompose)", m.mode)
		}
	})

	// The real nesting path: compose → confirm → cancel. The fixture fleet has no runner,
	// so the confirm path is unreachable in a full integration test, but we can exercise
	// the raise/dismiss pair directly with the same modes the product uses.
	t.Run("composer enter then cancel lands on screen not composer", func(t *testing.T) {
		// From dashboard
		dashboard := model{mode: modeBrowse, width: 120, height: 24}
		dashboard = dashboard.raise(modeCompose)
		if dashboard.underlay != modeBrowse {
			t.Fatalf("composer underlay is %v, want modeBrowse", dashboard.underlay)
		}
		// Enter in composer raises confirm (in the real code this is conditional on a non-empty
		// draft and pending targets, but the raise/dismiss mechanics are unconditional)
		dashboard = dashboard.raise(modeConfirm)
		if dashboard.mode != modeConfirm {
			t.Fatalf("mode is %v, want modeConfirm", dashboard.mode)
		}
		// The confirm's underlay must be modeBrowse, not modeCompose
		if dashboard.underlay != modeBrowse {
			t.Errorf("confirm underlay is %v, want modeBrowse (the screen, not the composer)", dashboard.underlay)
		}
		// Cancelling the confirm must land on modeBrowse
		dashboard = dashboard.dismiss()
		if dashboard.mode != modeBrowse {
			t.Errorf("dismiss landed on %v, want modeBrowse", dashboard.mode)
		}

		// From tree
		tree := model{mode: modeTree, width: 120, height: 24}
		tree = tree.raise(modeCompose)
		if tree.underlay != modeTree {
			t.Fatalf("composer underlay is %v, want modeTree", tree.underlay)
		}
		tree = tree.raise(modeConfirm)
		if tree.underlay != modeTree {
			t.Errorf("confirm underlay is %v, want modeTree", tree.underlay)
		}
		tree = tree.dismiss()
		if tree.mode != modeTree {
			t.Errorf("dismiss landed on %v, want modeTree", tree.mode)
		}
	})
}

// THE KEYWORD FIELD PAINTS ITS OWN SCREEN, and `/` from the tree keeps painting the tree
// (the frame still holds node lines) while showing the field's own footer.
//
// This is what makes search LOOK like search: the rows you are filtering are still visible,
// and the footer changes to show the field instead of the screen's normal footer. The field
// is a mode rather than an overlay-with-a-backdrop because every key inside it is text: `j`
// types a `j` rather than moving the cursor. But it behaves like an overlay in one way —
// it is raised over the screen and dismissed back to it — so it is tested here alongside
// the other overlays.
//
// The property is that the tree remains visible (node lines are still drawn) and the footer
// changes to show the field. And cancelling the search with esc must restore the query that
// was applied before the field opened, which is the lossless-cancel property the search mode
// shares with the naming overlay.
func TestTheKeywordFieldPaintsItsOwnScreen(t *testing.T) {
	m := base(t, 120, 24, treeFleet()...)
	m.home = "/home/dev"

	// Reach the tree
	m = key(t, m, "t")
	if m.mode != modeTree {
		t.Fatalf("t did not open the tree, mode is %v", m.mode)
	}

	// Apply a query by typing and entering (this narrows the tree)
	m = key(t, m, "/")
	if m.mode != modeSearch {
		t.Fatalf("/ did not open search, mode is %v", m.mode)
	}
	// Type the query character by character
	for _, r := range "edge" {
		out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = out.(model)
	}
	m = special(t, m, tea.KeyEnter)
	if m.mode != modeTree {
		t.Fatalf("enter did not leave search, mode is %v", m.mode)
	}
	beforeQuery := m.search.Text()
	if beforeQuery != "edge" {
		t.Fatalf("the applied query is %q, want %q", beforeQuery, "edge")
	}

	// Open search again — the field must hold the applied query
	m = key(t, m, "/")
	if m.mode != modeSearch {
		t.Fatalf("second / did not open search, mode is %v", m.mode)
	}
	if m.search.Text() != "edge" {
		t.Errorf("the field holds %q, want %q (the query already applied)", m.search.Text(), "edge")
	}
	screen := m.View()

	// The tree must still be visible (node lines with the open-node arrow)
	if !strings.Contains(screen, "▾") {
		t.Errorf("the screen does not contain ▾ (tree node lines), but search must paint the tree")
	}

	// And the footer must show the field (search mode draws its own footer with the field in it)
	// We cannot assert "the dashboard footer is absent" because both footers can contain overlapping
	// words, but we can assert that the screen is taller than it would be without the field —
	// the field adds a row.
	lines := strings.Split(screen, "\n")
	if len(lines) != m.height {
		t.Errorf("screen is %d lines, want %d (the field must be drawn)", len(lines), m.height)
	}

	// Cancel the search — the applied query must be restored
	m = special(t, m, tea.KeyEsc)
	if m.mode != modeTree {
		t.Errorf("esc left mode %v, want modeTree", m.mode)
	}
	if m.search.Text() != beforeQuery {
		t.Errorf("after cancel the query is %q, want %q (the one applied before the field opened)",
			m.search.Text(), beforeQuery)
	}
}

// THE MATRIX: every SCREEN against every overlay it can raise, asserted on all three halves — the
// underlay recorded, the screen PAINTED behind the overlay, and the screen returned to.
//
// Asked for by the adversarial review that found the boolean backdrop: "audit every non-overlay mode
// against every overlay it can raise; encode that matrix in tests." The three halves are separate
// claims and the product got two of them right while the third was wrong for a whole screen — the
// project list's naming overlay returned correctly and painted the dashboard — so a case that checks
// only the return is a case that would have passed through that defect.
//
// Each row names the needle that belongs to exactly ONE screen. That is the discipline this file has
// had to learn three times: a needle both screens print asserts nothing. The dashboard's is its row
// shape `host/session`, the tree's is the open-node arrow, and the list's is its own head line.
func TestEveryScreenAndEveryOverlayItCanRaise(t *testing.T) {
	type row struct {
		screen  uiMode
		reach   func(model) model // how the operator gets to that screen
		needle  string            // a string ONLY that screen draws
		key     string            // the key that raises the overlay
		overlay uiMode
		mark    bool // press space first, because the key needs a selection
	}
	// The tree's needle is its HEAD LINE and not a node line: an overlay reduces the height, the tree
	// then scrolls to keep the cursor, and the volume line goes off the top — measured, with the launch
	// form's twelve rows. A needle that a correct frame can legitimately lose is a needle that fails
	// for the wrong reason.
	dashNeedle, treeNeedle, listNeedle := "local/store-online", "asking · enter opens",
		"enter narrows, esc goes back"
	onTree := func(m model) model { return walkToSessionRow(t, treeModelOpened(t)) }
	onList := func(m model) model { return key(t, m, "P") }
	rows := []row{
		{modeBrowse, func(m model) model { return m }, dashNeedle, "i", modeCompose, true},
		{modeBrowse, func(m model) model { return m }, dashNeedle, "N", modeNaming, false},
		{modeBrowse, func(m model) model { return m }, dashNeedle, "n", modeLaunch, false},
		{modeBrowse, func(m model) model { return m }, dashNeedle, "p", modePicker, false},
		{modeTree, onTree, treeNeedle, "i", modeCompose, true},
		{modeTree, onTree, treeNeedle, "N", modeNaming, false},
		{modeTree, onTree, treeNeedle, "n", modeLaunch, false},
		{modeProjects, onList, listNeedle, "N", modeNaming, false},
		{modeProjects, onList, listNeedle, "n", modeLaunch, false},
		{modeProjects, onList, listNeedle, "p", modePicker, false},
	}
	for _, r := range rows {
		t.Run(fmt.Sprintf("%v raises %v with %s", r.screen, r.overlay, r.key), func(t *testing.T) {
			m := base(t, 120, 24, treeFleet()...)
			m.home = "/home/dev"
			m.projectsPath = filepath.Join(t.TempDir(), "projects.toml")
			m = r.reach(m)
			if m.mode != r.screen {
				t.Fatalf("the setup reached %v, want %v", m.mode, r.screen)
			}
			if !strings.Contains(m.View(), r.needle) {
				t.Fatalf("%q is not on the screen this row is about, so its needle proves nothing:\n%s",
					r.needle, m.View())
			}
			if r.mark {
				m = key(t, m, " ")
				if m.sel.Len() == 0 {
					t.Fatalf("space marked nothing on %v", r.screen)
				}
			}
			m = key(t, m, r.key)
			if m.mode != r.overlay {
				t.Fatalf("%s did not raise %v: mode %v, note %q", r.key, r.overlay, m.mode, m.note)
			}
			if m.underlay != r.screen {
				t.Errorf("the underlay is %v, want %v", m.underlay, r.screen)
			}
			if painted := m.View(); !strings.Contains(painted, r.needle) {
				t.Errorf("the overlay does not paint %v behind it (%q missing):\n%s", r.screen,
					r.needle, painted)
			}
			back := special(t, m, tea.KeyEsc)
			if back.mode != r.screen {
				t.Errorf("cancelling %v landed on %v, want %v", r.overlay, back.mode, r.screen)
			}
		})
	}
}

// THE FIRST RUN LANDS ON THE DEFAULT SCREEN, in both option orders.
//
// On a machine with no hosts.toml the picker opens at startup (§9), and the default view is the
// filesystem one — so the two options interact. Measured before this was fixed: `WithPicker` set the mode
// by hand, leaving the underlay at the dashboard, and `WithView` then refused to act because a mode was
// already set. Saving the host set dropped the operator onto the FLAT list on the one run where they have
// never seen either screen, and the picker's own base drew that list behind it.
//
// BOTH ORDERS are asserted because the fix is meant to make the order irrelevant: `WithPicker` records
// its underlay through `raise`, and `WithView` writes the screen underneath when an overlay is already
// open. An option pair whose behaviour depends on the order in `main` is a defect waiting for the next
// person who tidies that list.
func TestTheFirstRunPickerReturnsToTheDefaultScreen(t *testing.T) {
	for _, order := range []string{"picker first", "view first"} {
		t.Run(order, func(t *testing.T) {
			m := base(t, 100, 24, treeFleet()...)
			if order == "picker first" {
				WithPicker(PickerPorts{}, nil, nil, true)(&m)
				WithView("tree")(&m)
			} else {
				WithView("tree")(&m)
				WithPicker(PickerPorts{}, nil, nil, true)(&m)
			}
			if m.mode != modePicker {
				t.Fatalf("the picker is not open at startup: mode %v", m.mode)
			}
			if m.underlay != modeTree {
				t.Errorf("the screen under the first-run picker is %v, want the filesystem view",
					m.underlay)
			}
			if got := m.dismiss().mode; got != modeTree {
				t.Errorf("dismissing the first-run picker lands on %v, want the filesystem view", got)
			}
		})
	}
	// And `--view=flat` still means the dashboard, which is the other half of the flag.
	m := base(t, 100, 24, treeFleet()...)
	WithPicker(PickerPorts{}, nil, nil, true)(&m)
	WithView("flat")(&m)
	if m.underlay != modeBrowse {
		t.Errorf("with --view=flat the screen under the picker is %v, want the dashboard", m.underlay)
	}
}
