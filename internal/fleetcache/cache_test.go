package fleetcache

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/fleet"
)

func tmp(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "fleet-cache.json")
}

// An absent file is a first run, not a complaint. This is the commonest state there is and it must
// be distinguishable from a file the reader refused — one is silence, the other is a warning.
func TestAnAbsentCacheIsNoFactsYetAndSaysNothing(t *testing.T) {
	c, err := Open(tmp(t))
	if err != nil {
		t.Fatalf("an absent cache is a first run, not an error: %v", err)
	}
	if got := c.Len(); got != 0 {
		t.Errorf("an absent cache holds %d facts, want 0", got)
	}
	if w := c.Warning(); w != "" {
		t.Errorf("an absent cache warns %q — a first run has nothing to complain about", w)
	}
}

// An unreadable cache must degrade to "no facts yet" and NEVER to a wrong order: the whole reason
// the order is taken from a cache is that a live probe swings fourfold, so a half-read cache would
// move the row the operator ticked. `fav` is the sibling with the same failure direction.
func TestAMalformedCacheDegradesToNoFactsAndNeverToAWrongOrder(t *testing.T) {
	path := tmp(t)
	if err := os.WriteFile(path, []byte("{this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Open(path)
	if err != nil {
		t.Fatalf("a malformed cache must not fail the hub: %v", err)
	}
	if got := c.Len(); got != 0 {
		t.Errorf("a malformed cache yielded %d facts — half a cache is a wrong order", got)
	}
	w := c.Warning()
	if w == "" {
		t.Fatal("a malformed cache said nothing, so the operator cannot tell it from a first run")
	}
	// The sentence has to say what was LOST, not just quote Go's parser: the consequence is what
	// tells the reader whether to care.
	if !strings.Contains(w, "order") {
		t.Errorf("the warning %q does not say what the operator loses (the remembered order)", w)
	}
}

// A change of VERSION is a different failure from a change of field, and the reader must say which
// shape it expected — the lesson `hide` paid for when an array-into-struct reported Go's type error.
func TestACacheFromAnotherShapeIsRefusedAndNamesBothVersions(t *testing.T) {
	path := tmp(t)
	if err := os.WriteFile(path, []byte(`{"v":0,"nodes":[{"alias":"nuc","rtt_ms":40}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Open(path)
	if err != nil {
		t.Fatalf("a cache of another version must not fail the hub: %v", err)
	}
	if got := c.Len(); got != 0 {
		t.Errorf("a cache of version 0 contributed %d facts", got)
	}
	for _, want := range []string{"0", "1"} {
		if !strings.Contains(c.Warning(), want) {
			t.Errorf("the warning %q names neither the version found nor the version written: want %q",
				c.Warning(), want)
		}
	}
}

// An I/O error (permission denied, disk full) is distinguished from a content problem and DOES fail
// Open, because the operator cannot fix it by deleting the file — the failure is not about the cache.
func TestAnUnreadableCacheReturnsAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet-cache.json")
	if err := os.WriteFile(path, []byte(`{"v":1,"nodes":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Make the file unreadable (mode 000).
	if err := os.Chmod(path, 0o000); err != nil {
		t.Skipf("cannot make the file unreadable: %v", err)
	}
	c, err := Open(path)
	if err == nil {
		t.Fatal("Open of an unreadable file returned nil error — the caller has no way to report it")
	}
	// The error must name the path, so the operator knows which file.
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the path %q", err, path)
	}
	// The cache is returned even on error, and must not have a warning — the warning is for content
	// problems, the error is for I/O problems.
	if w := c.Warning(); w != "" {
		t.Errorf("a cache that failed to read has warning %q — the error already carries the problem", w)
	}
}

// The round trip through the FILE, which is the only thing that makes the order survive a restart.
// A test that recorded and read back from the same in-memory map would pass against a cache that
// never wrote anything.
func TestFactsSurviveTheFile(t *testing.T) {
	path := tmp(t)
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seen := time.Date(2026, 8, 20, 11, 22, 33, 0, time.UTC)
	k := Key{Fingerprint: "SHA256:aaa"}
	if err := c.Record(map[Key]Facts{k: {
		RTT: 41 * time.Millisecond, TmuxVersion: "3.2a", LastSeen: seen,
	}}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := again.Facts(k)
	if !ok {
		t.Fatalf("the fact did not survive the file; the cache holds %d", again.Len())
	}
	if got.RTT != 41*time.Millisecond {
		t.Errorf("RTT came back %v, want 41ms", got.RTT)
	}
	if got.TmuxVersion != "3.2a" {
		t.Errorf("TmuxVersion came back %q, want 3.2a", got.TmuxVersion)
	}
	if !got.LastSeen.Equal(seen) {
		t.Errorf("LastSeen came back %v, want %v", got.LastSeen, seen)
	}
}

// A key nobody has measured must answer NO, not a zero that reads like a measurement of zero
// milliseconds — which would sort an unknown machine into the fastest bucket.
func TestAnUnknownKeyIsAnAbsenceAndNotAZero(t *testing.T) {
	c, err := Open(tmp(t))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := c.Facts(Key{Alias: "never-asked"})
	if ok {
		t.Errorf("an unmeasured key answered %+v with ok=true", got)
	}
	if got.RTT != 0 {
		t.Errorf("the zero Facts carries RTT %v", got.RTT)
	}
}

// A nil cache is a hub started without one, and every reader has to survive it — the ports that
// supply it are nil-able by design, and a screen that panicked on a missing cache would make the
// order's own fallback unreachable.
func TestANilCacheAnswersNothingRatherThanPanicking(t *testing.T) {
	var c *Cache
	if _, ok := c.Facts(Key{Alias: "nuc"}); ok {
		t.Error("a nil cache claimed to hold a fact")
	}
	if got := c.Len(); got != 0 {
		t.Errorf("a nil cache reports %d facts", got)
	}
	if w := c.Warning(); w != "" {
		t.Errorf("a nil cache warns %q", w)
	}
	if err := c.Record(map[Key]Facts{{Alias: "nuc"}: {}}); err == nil {
		t.Error("a nil cache accepted a write, so the caller cannot tell nothing was kept")
	}
}

// The bytes are SORTED, so two runs that learned the same things produce the same file and a diff
// of it says what changed rather than that a map was walked.
func TestTwoRunsThatLearnedTheSameThingsWriteTheSameBytes(t *testing.T) {
	facts := map[Key]Facts{
		{Fingerprint: "SHA256:bbb"}:                  {RTT: time.Second},
		{Fingerprint: "SHA256:aaa"}:                  {RTT: 2 * time.Millisecond},
		{Observer: "hop", Alias: "leaf"}:             {RTT: 180 * time.Millisecond},
		{Observer: "other-hop", Alias: "leaf"}:       {RTT: 190 * time.Millisecond},
		{Observer: "hop", Alias: "another-leaf-far"}: {},
	}
	var wrote [2][]byte
	for i := range wrote {
		path := tmp(t)
		c, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Record(facts); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		wrote[i] = raw
	}
	if string(wrote[0]) != string(wrote[1]) {
		t.Errorf("two runs wrote different bytes for the same facts:\n%s\n---\n%s", wrote[0], wrote[1])
	}
}

// The KEY is the identity where there is one and the observer's vocabulary otherwise — the same
// split `fav.KeyOf` makes for the Claude uuid, and for the same reason: a fingerprint is not
// vocabulary, and an alias is only meaningful beside the observer that used it.
func TestTheKeyIsTheFingerprintWhereThereIsOneAndTheObserversAliasOtherwise(t *testing.T) {
	node := fleet.Node{
		Fingerprints: []string{"SHA256:aaa", "SHA256:bbb"},
		Labels:       []fleet.Label{{Observer: "root", Alias: "hop"}, {Observer: "root", Alias: "hop-again"}},
	}
	if got, want := KeyOfNode(node), (Key{Fingerprint: "SHA256:aaa"}); got != want {
		t.Errorf("KeyOfNode = %+v, want %+v — a node's key is its identity", got, want)
	}
	// A second alias for the same machine must land on the SAME key, or the fact learned under one
	// name is invisible under the other and the order changes with which alias was probed last.
	swapped := node
	swapped.Labels = []fleet.Label{{Observer: "root", Alias: "hop-again"}}
	if KeyOfNode(swapped) != KeyOfNode(node) {
		t.Error("two aliases for one machine produced two keys")
	}
}

// The cache is read from bubbletea's Update while a tea.Cmd writes it, so the lock is not care but
// a requirement — and this asserts the VALUE, not the silence: a copy-on-write that discarded the
// writes would keep `-race` quiet and lose every fact.
func TestEveryConcurrentWriteSurvives(t *testing.T) {
	c, err := Open(tmp(t))
	if err != nil {
		t.Fatal(err)
	}
	const writers = 24
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := Key{Fingerprint: "SHA256:" + string(rune('a'+i))}
			if err := c.Record(map[Key]Facts{k: {RTT: time.Duration(i) * time.Millisecond}}); err != nil {
				t.Errorf("Record: %v", err)
			}
		}(i)
	}
	// Readers in the same window, because a reader holding a map while it is written is the
	// second half of the hazard.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Facts(Key{Fingerprint: "SHA256:a"})
			c.Len()
		}()
	}
	wg.Wait()
	if got := c.Len(); got != writers {
		t.Errorf("%d of %d concurrent writes survived — a lost write is a fact the next run does not have",
			got, writers)
	}
}
