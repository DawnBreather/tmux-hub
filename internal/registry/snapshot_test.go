package registry

import (
	"slices"
	"testing"
	"time"
)

// Panes() must hand out a COPY, and this is the same class as the defect that shipped
// one package over: `Poller.Tick` handed out `&p.hosts[i]` and a concurrent `Add`
// reallocated under it, so a tick's writes went to an abandoned array. Here the
// direction is reversed — the registry hands a caller a reference INTO its own
// published snapshot, and the caller can write it.
//
// It is latent rather than live: today's four production callers only range over the
// result. What makes it worth closing anyway is how ordinary the mistake is.
// SortByAttention is EXPORTED and sorts IN PLACE, so `SortByAttention(reg.Panes())`
// compiles, reads like the obvious thing, and mutates the registry's live snapshot
// outside the lock while other goroutines read it — and the codebase already reaches
// for that idiom (internal/ui's screen fixture calls it on a slice of its own).
func TestPanesHandsOutACopyRatherThanTheRegistrysOwnSnapshot(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	ds, ls, zones, fulls := sample(now)
	r.Update("local", ds, ls, zones, fulls, now, 0)

	got := r.Panes()
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	first := got[0].Session

	// One caller writes an element, which is what "I will just mark this row" looks like.
	got[0].Session = "clobbered-by-a-caller"
	// And one reorders, which is what SortByAttention(reg.Panes()) does.
	slices.Reverse(got)

	again := r.Panes()
	// Anywhere, not at index 0: the reversal above moves the written element, so an
	// index-anchored check would be satisfied by the reorder rather than by the write —
	// two arms in one test, each able to hide the other.
	for _, p := range again {
		if p.Session == "clobbered-by-a-caller" {
			t.Error("a caller's write to the returned slice reached the registry: every " +
				"later reader now sees it, and the write happened outside the lock")
		}
	}
	if again[0].Session != first {
		t.Errorf("the registry's order changed because a caller reordered ITS slice: "+
			"first = %q, want %q", again[0].Session, first)
	}
}

// The half a clone does NOT buy, written down so the next reader does not assume it.
// A Pane carries `Zone` and `Content` as slices, and copying the Pane copies the slice
// HEADERS — so `panes[0].Content[0] = "x"` still reaches the registry's own strings.
// That is safe today for a reason worth stating: nothing ever writes those in place.
// `Update` REPLACES them (`p.Zone = c.Lines`, `p.Content = lines.ContentTail(…)`), and
// ContentTail builds a fresh slice rather than sub-slicing its input, so a published
// payload is never written again by anybody.
func TestAPublishedPanesContentIsNeverWrittenInPlace(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	ds, ls, zones, fulls := sample(now)
	r.Update("local", ds, ls, zones, fulls, now, 0)

	before := r.Panes()
	held := before[0].Content
	if len(held) == 0 {
		t.Fatal("no content captured, so this test cannot see what it is about")
	}
	snapshot := slices.Clone(held)

	// A second poll of the same pane with different output.
	zones[0].Lines = []string{"● Ran tests again", "❯"}
	fulls["%0"] = zones[0]
	r.Update("local", ds, ls, zones, fulls, now.Add(time.Second), time.Second)

	// The slice the first caller is still holding must read exactly as it did.
	if !slices.Equal(held, snapshot) {
		t.Errorf("a second Update wrote into content already published: %q, want %q — a UI "+
			"goroutine rendering that slice would race with the poll", held, snapshot)
	}
}
