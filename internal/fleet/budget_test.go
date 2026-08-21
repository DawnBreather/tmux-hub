package fleet

import (
	"strconv"
	"strings"
	"testing"
)

// actionable is what every cut must be: it says how many it dropped and what to do about it. A budget
// that cuts silently is the defect (spec §3.3), and a horizon with no count is indistinguishable from
// a crawl that finished.
func actionable(t *testing.T, c Cut) {
	t.Helper()
	if c.Skipped <= 0 {
		t.Errorf("the cut reports %d skipped — a cut with no count is a silent horizon: %+v", c.Skipped, c)
	}
	if !strings.Contains(c.Why, strconv.Itoa(c.Skipped)) {
		t.Errorf("the sentence %q does not carry the count %d that goes with it", c.Why, c.Skipped)
	}
	if !strings.Contains(c.Why, "raise") {
		t.Errorf("the sentence %q names no act — a reason without a remedy is a complaint", c.Why)
	}
	if strings.Contains(c.Why, "unreachable") {
		t.Errorf("the sentence %q says `unreachable`, which invariant 4 forbids", c.Why)
	}
}

// The operator's own machine declares 18 aliases, so breadth is the budget that actually bites.
func TestABreadthCutIsReportedWithItsCount(t *testing.T) {
	g := New()
	labels := []string{"a", "b", "c", "d", "e"}
	got := g.Allow(Budget{MaxDepth: 3, MaxPerObserver: 2}, "hop", 1, labels)
	if len(got) != 2 {
		t.Fatalf("the budget allowed %d of 5, want 2", len(got))
	}
	cuts := g.Cuts()
	if len(cuts) != 1 {
		t.Fatalf("three aliases were dropped and %d cuts were reported", len(cuts))
	}
	if cuts[0].Skipped != 3 {
		t.Errorf("the cut says %d skipped, want 3", cuts[0].Skipped)
	}
	actionable(t, cuts[0])
}

// Depth is the other half, and it cuts a whole observer rather than a tail.
func TestADepthCutIsReportedWithItsCount(t *testing.T) {
	g := New()
	got := g.Allow(Budget{MaxDepth: 1, MaxPerObserver: 9}, "far", 2, []string{"a", "b"})
	if len(got) != 0 {
		t.Fatalf("an observer past the depth budget contributed %v", got)
	}
	cuts := g.Cuts()
	if len(cuts) != 1 {
		t.Fatalf("a whole observer was dropped and %d cuts were reported", len(cuts))
	}
	if cuts[0].Skipped != 2 {
		t.Errorf("the cut says %d skipped, want both aliases", cuts[0].Skipped)
	}
	actionable(t, cuts[0])
}

// The other pole, and it is the one a "cuts are reported" test cannot do without: a budget that cut
// nothing must report nothing, or the report says the crawl was truncated every time it was not.
func TestABudgetThatCutsNothingReportsNothing(t *testing.T) {
	g := New()
	labels := []string{"a", "b"}
	got := g.Allow(Budget{MaxDepth: 2, MaxPerObserver: 2}, "hop", 2, labels)
	if len(got) != 2 {
		t.Errorf("a budget with room to spare allowed %d of 2", len(got))
	}
	if cuts := g.Cuts(); len(cuts) != 0 {
		t.Errorf("nothing was cut and %d cuts were reported: %+v", len(cuts), cuts)
	}
}

// Today's fleet is flat — every host is one hop from the root — so the depth a Budget names is the
// number of hops it will read declarations FROM, and the root is depth 0. A zero MaxDepth must
// therefore still let the root's own hosts.toml through, or the smallest budget expressible would
// refuse the behaviour the hub already has.
func TestTheRootIsDepthZeroAndSurvivesTheSmallestDepthBudget(t *testing.T) {
	g := New()
	got := g.Allow(Budget{MaxPerObserver: 18}, "root", 0, []string{"nuc", "nuc"})
	if len(got) != 2 {
		t.Errorf("MaxDepth 0 refused the ROOT's own %d declarations, allowing %v", 2, got)
	}
	if cuts := g.Cuts(); len(cuts) != 0 {
		t.Errorf("the root's own declarations were reported as cut: %+v", cuts)
	}
}

// Each cut names its own observer. A report that attributed one host's truncation to another is the
// same class as an index write-back landing one host's result on the next, which this repository has
// already shipped once.
func TestEachCutNamesItsOwnObserver(t *testing.T) {
	g := New()
	b := Budget{MaxDepth: 4, MaxPerObserver: 1}
	g.Allow(b, "hop", 1, []string{"a", "b"})
	g.Allow(b, "other-hop", 1, []string{"c", "d", "e"})
	cuts := g.Cuts()
	if len(cuts) != 2 {
		t.Fatalf("two observers were cut and %d cuts were reported: %+v", len(cuts), cuts)
	}
	byObserver := map[string]int{}
	for _, c := range cuts {
		byObserver[c.Observer] = c.Skipped
		if !strings.Contains(c.Why, c.Observer) {
			t.Errorf("the sentence %q does not name the observer it is about", c.Why)
		}
		actionable(t, c)
	}
	if byObserver["hop"] != 1 || byObserver["other-hop"] != 2 {
		t.Errorf("the counts landed on the wrong observers: %v", byObserver)
	}
}

// Allow's answer is a snapshot, as every other accessor in this package is. Returning a window on the
// CALLER's own array is the shape that lets a later write rewrite an answer already handed out — and
// the reason Allow returns the labels rather than a count is precisely that the caller keeps them.
func TestAllowHandsOutACopyOfTheAllowedLabels(t *testing.T) {
	g := New()
	labels := []string{"a", "b", "c"}
	got := g.Allow(Budget{MaxDepth: 1, MaxPerObserver: 2}, "hop", 1, labels)
	if len(got) != 2 {
		t.Fatalf("the budget allowed %d of 3, want 2", len(got))
	}
	labels[0] = "vandalised"
	if got[0] != "a" {
		t.Errorf("the allowed list reads %v after the caller reused its own array — Allow handed "+
			"out a window on it rather than a copy", got)
	}
}

// A budget nobody filled in cuts everything — and SAYS so, which is the whole difference between a
// footgun and a silent horizon. The alternative reading (0 means unbounded) is the one §3.3 exists to
// forbid: an unbounded chain over hosts that each declare 18 aliases is combinatorial.
func TestTheZeroBudgetCutsEverythingAndSaysSo(t *testing.T) {
	g := New()
	if got := g.Allow(Budget{}, "hop", 0, []string{"a", "b", "c"}); len(got) != 0 {
		t.Errorf("the zero budget allowed %v", got)
	}
	cuts := g.Cuts()
	if len(cuts) != 1 {
		t.Fatalf("the zero budget cut everything and reported %d cuts", len(cuts))
	}
	if cuts[0].Skipped != 3 {
		t.Errorf("the cut says %d skipped, want 3", cuts[0].Skipped)
	}
	actionable(t, cuts[0])
}
