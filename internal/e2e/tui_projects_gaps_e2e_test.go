//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// Missing coverage for the projects screen: the footer's role in making the filter visible.
//
// The existing seven cases cover the mechanics (P opens the list, enter narrows, tab walks, esc
// widens) and prove the ROWS change. What they do not cover is the footer's job: naming the project
// an operator arrived at with `tab`, showing the narrowing COST (N of M), and clearing the project
// name after esc. Without the footer, an operator who presses `tab` sees the right rows but cannot
// tell which project they are looking at, and `tab` again moves them to a project they cannot
// identify — which is the defect filterTally.Project was added to fix (internal/ui/filters.go:60-66).

// prjFooter extracts the footer line from the screen. The footer is the last non-empty line.
func prjFooter(t *testing.T, ui string) string {
	t.Helper()
	lines := strings.Split(capturePane(t, ui, "ui"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if trimmed := strings.TrimSpace(lines[i]); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// prjWaitFooter waits for the footer to contain a specific string.
func prjWaitFooter(t *testing.T, ui, want, why string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		footer := prjFooter(t, ui)
		if strings.Contains(footer, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("footer never showed %q — %s\nfooter: %q\n%s",
				want, why, footer, capturePane(t, ui, "ui"))
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// `tab` names the project in the footer: "in <project> · N of M".
//
// This is the regression guard for the fix that added filterTally.Project. Before that fix, an
// operator who pressed `tab` saw the right ROWS but no indication of which project they had arrived
// at — measured on a fleet of 8 rows, `tab` drew 3 rows with a footer reading "local up · nuc up"
// and no project name. `tab` again moved to a project they could not identify. The footer must name
// the project and show the narrowing cost, or the walk the project list exists to start is blind.
func TestE2EGAPTabNamesTheProjectInFooter(t *testing.T) {
	f := projectsHub(t, 120, 40)

	send(t, f.ui, "P")
	screenHas(t, f.ui, "2 projects", "`P` must open the project list")
	projectsWaitListRow(t, f.ui, "alpha", "of 2", "the roll-up has to arrive")
	order := projectsListOrder(t, f.ui, f)
	if len(order) != 2 {
		t.Fatalf("the list shows %v projects, want both\n%s", order, capturePane(t, f.ui, "ui"))
	}
	send(t, f.ui, "Escape")
	projectsWaitFrame(t, f.ui, append([]string{"tmux-hub  3 sessions"}, f.panes...), nil,
		"start from an unfiltered dashboard")

	send(t, f.ui, "tab")
	projectsWaitFrame(t, f.ui, f.panesOf(order[0]), f.others(order[0]),
		"`tab` must narrow to the first project")

	// The footer must name the project and show the cost.
	prjWaitFooter(t, f.ui, "in "+order[0],
		"the footer must name the project the operator arrived at — without it, pressing `tab` "+
			"again moves to a project they cannot identify, which is the defect this fix closed")
	footer := prjFooter(t, f.ui)
	if !strings.Contains(footer, " of ") {
		t.Errorf("the footer does not show the narrowing cost (N of M): %q — the operator "+
			"cannot tell how much of their fleet is hidden", footer)
	}
	// The count must be 2 of 3: alpha holds 2 panes, the fixture has 3 total.
	if !strings.Contains(footer, "2 of 3") {
		t.Errorf("the footer does not show the correct count (2 of 3): %q", footer)
	}
}

// The footer shows the narrowing COST: N rows shown of M total.
//
// The cost is the information the dashboard loses by narrowing: an operator who sees "3 sessions" on
// a header while filtered to one project cannot tell whether the filter is doing anything, and "3
// sessions" on a screen showing 1 is a lie about which question the screen answers. The footer must
// say "in <project> · 1 of 3", where the M is the whole fleet and the operator can derive the loss.
func TestE2EGAPFooterShowsFilterCost(t *testing.T) {
	f := projectsHub(t, 120, 40)

	send(t, f.ui, "P")
	screenHas(t, f.ui, "2 projects", "`P` must open the project list")
	projectsWalkList(t, f.ui, "beta")
	send(t, f.ui, "Enter")

	projectsWaitFrame(t, f.ui, append([]string{"tmux-hub  1 session"}, f.panesOf("beta")...),
		f.others("beta"), "`enter` must narrow to beta")

	// The footer must show 1 of 3: beta has 1 pane, the fixture has 3 total.
	prjWaitFooter(t, f.ui, "1 of 3",
		"the footer must show how many rows are kept (1) against the total fleet (3) — without "+
			"the denominator, an operator filtering a large fleet cannot tell when they have "+
			"narrowed it to one session, and \"1 session\" in the header is ambiguous")
	footer := prjFooter(t, f.ui)
	if !strings.Contains(footer, "in beta") {
		t.Errorf("the footer does not name the project: %q — the operator cannot tell which "+
			"project they are looking at", footer)
	}
}

// `A` (mark-all) respects the project filter: it marks only the rows the filter kept.
//
// A mark-all that ignores the filter would mark rows the operator cannot see, which reads as
// "nothing happened" at the keystroke (the one visible row was already marked or there is nothing to
// mark) and then as a surprise when esc brings back the rest of the fleet already marked. The
// assertion is the footer's mark count: 1 marked in beta's view must stay 1 marked after esc widens
// to 3 rows, not become 3.
func TestE2EGAPMarkAllRespectsProjectFilter(t *testing.T) {
	f := projectsHub(t, 120, 40)

	send(t, f.ui, "P")
	screenHas(t, f.ui, "2 projects", "`P` must open the project list")
	projectsWalkList(t, f.ui, "beta")
	send(t, f.ui, "Enter")
	projectsWaitFrame(t, f.ui, f.panesOf("beta"), f.others("beta"), "`enter` must narrow to beta")

	// Mark all within the project filter. Beta has 1 pane.
	send(t, f.ui, "A")
	prjWaitFooter(t, f.ui, "→ 1 marked",
		"`A` must mark beta's one pane and the footer must say so")

	// Widen to see the whole fleet.
	send(t, f.ui, "Escape")
	projectsWaitFrame(t, f.ui, append([]string{"tmux-hub  3 sessions"}, f.panes...), nil,
		"`esc` must widen back to the whole fleet")

	// The mark count must still be 1: only beta's pane was in the filtered view.
	prjWaitFooter(t, f.ui, "→ 1 marked",
		"after widening, the mark count must still be 1 — a mark-all that marked the whole fleet "+
			"(3 panes) while showing only one project would read as \"nothing happened\" at the "+
			"keystroke and then surprise the operator when esc reveals 3 marked, costing an esc c "+
			"to recover")
}

// `esc` clears the project name from the footer.
//
// The footer names the project while one is selected, and must stop naming it when esc widens the
// view back to the whole fleet. A footer that keeps saying "in <project>" on a screen showing the
// whole fleet is a lie in exactly the direction that hides the way back: the operator believes they
// are still narrowed, concludes that esc has done nothing, and has no visible path to the rows they
// cannot see. The header changes (1 session → 3 sessions) but that is unreliable when the numbers
// happen to match.
func TestE2EGAPEscClearsProjectNameFromFooter(t *testing.T) {
	f := projectsHub(t, 120, 40)

	send(t, f.ui, "P")
	screenHas(t, f.ui, "2 projects", "`P` must open the project list")
	projectsWalkList(t, f.ui, "alpha")
	send(t, f.ui, "Enter")
	projectsWaitFrame(t, f.ui, f.panesOf("alpha"), f.others("alpha"), "`enter` must narrow to alpha")
	prjWaitFooter(t, f.ui, "in alpha", "the narrowed view must name the project in the footer")

	send(t, f.ui, "Escape")
	projectsWaitFrame(t, f.ui, append([]string{"tmux-hub  3 sessions"}, f.panes...), nil,
		"`esc` must widen back to the whole fleet")

	// The footer must not name a project any more.
	deadline := time.Now().Add(20 * time.Second)
	for {
		footer := prjFooter(t, f.ui)
		if !strings.Contains(footer, "in alpha") && !strings.Contains(footer, "in beta") {
			// Good: no project name in footer.
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the footer still names a project after esc widened the view — the operator "+
				"believes they are still narrowed and cannot tell that esc worked:\nfooter: %q\n%s",
				footer, capturePane(t, f.ui, "ui"))
		}
		time.Sleep(150 * time.Millisecond)
	}

	// And it must not show a narrowing cost either: when nothing is filtered, there is no "N of M".
	// The "of" word is what distinguishes a count from other numbers in the footer.
	footer := prjFooter(t, f.ui)
	if strings.Contains(footer, " of ") && (strings.Contains(footer, "1 of 3") ||
		strings.Contains(footer, "2 of 3")) {
		t.Errorf("the footer still shows a narrowing cost after esc: %q — the screen shows the "+
			"whole fleet and the footer claiming \"N of M\" is a lie that says the view is still "+
			"narrowed", footer)
	}
}
