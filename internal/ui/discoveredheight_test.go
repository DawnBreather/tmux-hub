package ui

import (
	"fmt"
	"github.com/DawnBreather/tmux-hub/internal/fleetcache"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/fleet"
	"github.com/DawnBreather/tmux-hub/internal/lines"
)

// The section's height contract, swept rather than sampled — and it exists because the sampled
// version missed a live defect.
//
// TestThePickerSharesItsBodyWithTheSectionAndKeepsTheCursor in picker_test.go sweeps heights too, but
// its fixture's remedy wraps to ONE line at 80 columns, so that fixture's whole-remedy form and its
// compact form are the same shape and the branch that chooses between them cannot be reached. A
// reviewer found what hid there: the form was chosen against `height-1` while the fit ran against
// `height-2`, so at a first block exactly `height-1` tall the section printed its heading over an
// empty list — and at 60 columns height 4 named a machine while height 5 named none, a taller
// terminal saying less.
//
// So the fixture here is the sentence `fleet.Diagnose` really builds for the commonest case, at the
// width where it wraps: five machines, each blocked, each carrying a two-remedy reason with the key
// list. Four properties, all of them about the CONTRACT rather than about any frame:
//
//   - never more lines than the height it was given (it is drawn inside somebody else's box);
//   - the machines named never DECREASE as the height grows — docs/design.md §9 asserts this in those
//     words, and it was FALSE when that sentence was written;
//   - a row is never spent on the "N not shown" marker while no machine is named, because the heading
//     already carries the total;
//   - and once a machine is named, its remedy is whole — a remedy cut in half looks complete.
func TestTheSectionsHeightContractHoldsAtEveryWidthAndHeight(t *testing.T) {
	const remedy = "run `ssh-copy-id dev@leaf.internal`, or copy one of ~/.ssh/id_rsa, " +
		"~/.ssh/id_ecdsa, ~/.ssh/id_ed25519 to this machine — the recipe names no key that is here"
	var rows []DiscoveredRow
	for i := 0; i < 5; i++ {
		rows = append(rows, DiscoveredRow{
			Label: fmt.Sprintf("leaf%d", i), Observer: "hop", State: fleet.Blocked, Reason: remedy,
		})
	}

	for _, width := range []int{60, 80, 100, 120, 200} {
		prev := -1
		for height := 1; height <= 30; height++ {
			out := RenderDiscovered(rows, width, height)
			screen := strings.Join(out, "\n")
			if len(out) > height {
				t.Errorf("%dx%d returned %d lines, overrunning the box it was given:\n%s",
					width, height, len(out), screen)
			}
			named := 0
			for i := 0; i < 5; i++ {
				if strings.Contains(screen, fmt.Sprintf("leaf%d", i)) {
					named++
				}
			}
			if named < prev {
				t.Errorf("%dx%d names %d machines where height %d named %d — a taller terminal showing less:\n%s",
					width, height, named, height-1, prev, screen)
			}
			prev = named
			if named == 0 && strings.Contains(screen, "not shown") {
				t.Errorf("%dx%d spends a row on the marker while naming no machine:\n%s", width, height, screen)
			}
			// Every named machine's remedy must be present in full. The compact form legitimately cuts
			// the sentence and marks it, so the check is that the ACT survives whole — that is the half
			// fleet.Diagnose puts first precisely so one line can carry it.
			if named > 0 && !strings.Contains(screen, "run `ssh-copy-id dev@leaf.internal`") {
				t.Errorf("%dx%d names %d machines and not one act the operator can run:\n%s",
					width, height, named, screen)
			}
		}
		// And a floor per width, so a matcher that found nothing cannot pass this loop silently: by
		// height 30 every one of the five is named at every width in the band.
		if last := RenderDiscovered(rows, width, 30); !strings.Contains(strings.Join(last, "\n"), "leaf4") {
			t.Errorf("at %dx30 the section still does not name the fifth machine:\n%s",
				width, strings.Join(last, "\n"))
		}
	}
}

// A machine's tmux version was CARRIED and never DRAWN: `crawled` copies it out of the fleet cache
// into `DiscoveredRow.Version` and no surface read it, so the field was assigned by one producer and
// read by nobody — this repository's signature defect in its smallest form. It is worth drawing
// because it answers the question a row in this section raises: can this machine be polled at all.
//
// Both poles, because "prints the version" is satisfied by a renderer that prints a gap for every row:
// a row with no version must produce no `tmux` on its line at all, which is the commonest case for a
// machine a hop declared and nobody has yet shaken hands with.
func TestAKnownTmuxVersionIsDrawnAndAnUnknownOneLeavesNoGap(t *testing.T) {
	rows := []DiscoveredRow{
		{Label: "known", Observer: "hop", State: fleet.Ready, Version: "3.5a"},
		{Label: "unknown", Observer: "hop", State: fleet.Ready},
	}
	out := RenderDiscovered(rows, 100, 12)
	var knownLine, unknownLine string
	for _, l := range out {
		if strings.Contains(l, "known") && !strings.Contains(l, "unknown") {
			knownLine = l
		}
		if strings.Contains(l, "unknown") {
			unknownLine = l
		}
	}
	if knownLine == "" || unknownLine == "" {
		t.Fatalf("both machines must be drawn at 100x12:\n%s", strings.Join(out, "\n"))
	}
	if !strings.Contains(knownLine, "tmux 3.5a") {
		t.Errorf("a machine whose version the cache holds does not print it:\n%q", knownLine)
	}
	if strings.Contains(unknownLine, "tmux") {
		t.Errorf("a machine with no known version prints a version anyway:\n%q", unknownLine)
	}
	// And the line still obeys the width, since a version is a new claimant on a row that already had
	// three: at 60 columns nothing may exceed the terminal.
	for _, l := range RenderDiscovered(rows, 60, 12) {
		if lines.Width(l) > 60 {
			t.Errorf("a version pushed a line to %d columns at width 60:\n%q", lines.Width(l), l)
		}
	}
}

// A MERGED NODE MUST KEEP WHAT IT REMEMBERS, and before this it did not: a node's fingerprint set
// grows, so two machines seen separately are two nodes with two cache keys, and one later observation
// carrying both fingerprints merges them into one node keyed on the KEEPER's first fingerprint.
// Everything filed under the absorbed fingerprint became unreachable — the row lost its RTT, printed
// `no timing`, and sorted into a slower bucket than the machine deserves.
//
// Found by an adversarial reviewer and reproduced before fixing: a node holding
// {SHA256:aaa, SHA256:bbb} looked up `SHA256:aaa` alone and found nothing, though the facts were
// there under `SHA256:bbb`.
//
// The fixture drives the REAL graph rather than hand-building a node, because the whole defect is a
// property of what `Observe` does to a fingerprint set when it merges — a hand-built node is the one
// shape that cannot show it.
func TestAMergedMachineKeepsTheTimingItWasRememberedUnder(t *testing.T) {
	g := &fleet.Graph{}
	g.Observe(fleet.Observation{Label: "one", Verified: true, Fingerprints: []string{"SHA256:aaa"}})
	g.Observe(fleet.Observation{Label: "two", Observer: "hop", Verified: true,
		Fingerprints: []string{"SHA256:bbb"}})
	// One machine all along: an observation that sees both keys at once.
	g.Observe(fleet.Observation{Label: "one", Verified: true,
		Fingerprints: []string{"SHA256:aaa", "SHA256:bbb"}})

	nodes := g.Nodes()
	if len(nodes) != 1 {
		t.Fatalf("the fixture did not merge: %d nodes, want 1 — without a merge this case cannot "+
			"see the defect at all", len(nodes))
	}

	// Remembered under the ABSORBED fingerprint, which is where a real cache would have filed it
	// when that machine was a node of its own.
	remembered := map[fleetcache.Key]fleetcache.Facts{
		{Fingerprint: "SHA256:bbb"}: {RTT: 42 * time.Millisecond, TmuxVersion: "3.5a"},
	}
	look := func(k fleetcache.Key) (fleetcache.Facts, bool) {
		f, ok := remembered[k]
		return f, ok
	}

	rows := DiscoveredRowsFor(nodes, nil, look)
	if len(rows) == 0 {
		t.Fatal("no row for a merged node that a hop declares")
	}
	for _, r := range rows {
		if !r.Timed {
			t.Errorf("%s reads as never timed, though the merged node's other fingerprint carries a "+
				"42ms measurement", r.Label)
		}
		if r.RTT != 42*time.Millisecond {
			t.Errorf("%s carries RTT %v, want 42ms — the memory was filed under the fingerprint the "+
				"merge absorbed", r.Label, r.RTT)
		}
	}
}
