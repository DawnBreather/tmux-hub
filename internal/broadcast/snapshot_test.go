package broadcast

import (
	"sync"
	"testing"
)

// SessionSnapshot exists so the join can be applied WITHOUT holding the Keeper's lock.
//
// The obvious alternative — an `EachSession(func(...))` iterator — would run the caller's body with
// k.mu held, and the caller takes the registry's lock inside it. The paint path already takes those
// two in the opposite order (`reg.Panes()` then `keeper.Identified()`), so the nested form is a
// deadlock in waiting rather than a style preference. That is why this returns a copy.
func TestSessionSnapshotCopiesEveryAdoptedPane(t *testing.T) {
	k := NewKeeper(nil)
	k.Adopt("local", "%1", "aaaaaaaa-0000")
	k.Adopt("nuc", "%7", "bbbbbbbb-0000")
	// Identified without a session: it was walked, not adopted, so it has no session to join by
	// and must not appear.
	k.set("nuc", "%9", true)

	got := map[string]string{}
	for _, s := range k.SessionSnapshot() {
		got[s.Host+" "+s.PaneID] = s.SessionID
	}
	if len(got) != 2 {
		t.Fatalf("snapshot holds %d entries, want 2: %v", len(got), got)
	}
	for k2, want := range map[string]string{"local %1": "aaaaaaaa-0000", "nuc %7": "bbbbbbbb-0000"} {
		if got[k2] != want {
			t.Errorf("snapshot[%q] = %q, want %q", k2, got[k2], want)
		}
	}
	if _, ok := got["nuc %9"]; ok {
		t.Error("a pane identified by a walk has no session id and must not be in the snapshot")
	}
}

// A nil Keeper is a real state — `--no-history` style construction and every test that builds a
// model by hand — and the caller must need no branch.
func TestSessionSnapshotOfANilKeeperIsEmpty(t *testing.T) {
	var k *Keeper
	if got := k.SessionSnapshot(); len(got) != 0 {
		t.Errorf("a nil Keeper answered %v", got)
	}
}

// It is a COPY: mutating what the caller got must not reach the Keeper, or the join would be able
// to rewrite the identity store it is reading.
func TestSessionSnapshotIsACopy(t *testing.T) {
	k := NewKeeper(nil)
	k.Adopt("local", "%1", "aaaaaaaa-0000")
	snap := k.SessionSnapshot()
	snap[0].SessionID = "tampered"

	if id, _ := k.Session("local", "%1"); id != "aaaaaaaa-0000" {
		t.Errorf("the Keeper's own answer became %q after the caller edited the snapshot", id)
	}
}

// The snapshot is taken from the tick's goroutine while Adopt runs on another, so it needs its own
// concurrent test in the same commit — and the test asserts the VALUE, not the silence: a run where
// `-race` merely stays quiet also passes against a copy-on-write fix that discards the write, which
// is worse than the bug because it removes the evidence and keeps the symptom.
func TestSessionSnapshotUnderConcurrentAdopt(t *testing.T) {
	k := NewKeeper(nil)
	const n = 200
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			k.Adopt("local", paneName(i), "sess-"+paneName(i))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			_ = k.SessionSnapshot()
		}
	}()
	wg.Wait()

	// Every write must have SURVIVED, which is the assertion a silence check cannot make.
	snap := k.SessionSnapshot()
	if len(snap) != n {
		t.Fatalf("snapshot holds %d panes after %d adopts", len(snap), n)
	}
	for _, s := range snap {
		if want := "sess-" + s.PaneID; s.SessionID != want {
			t.Errorf("pane %q carries %q, want %q", s.PaneID, s.SessionID, want)
		}
	}
}

func paneName(i int) string {
	return "%" + string(rune('a'+i%26)) + string(rune('a'+i/26))
}
