package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/fleet"
	"github.com/DawnBreather/tmux-hub/internal/fleetcache"
	"github.com/DawnBreather/tmux-hub/internal/hostset"
	"github.com/DawnBreather/tmux-hub/internal/lines"
)

// remembered turns a table of alias→round-trip into the facts lookup DiscoveredRowsFor reads. It
// keys on the ROOT's own alias, which is where a hop's measured round trip lives — see
// DiscoveredRowsFor for why a machine behind a hop inherits it.
func remembered(rtt map[string]time.Duration) func(fleetcache.Key) (fleetcache.Facts, bool) {
	return func(k fleetcache.Key) (fleetcache.Facts, bool) {
		d, ok := rtt[k.Alias]
		if !ok {
			return fleetcache.Facts{}, false
		}
		return fleetcache.Facts{RTT: d, LastSeen: time.Unix(0, 0)}, true
	}
}

// behind declares machines on a hop, the way a hop's own ~/.ssh/config would, and diagnoses each
// one through the real fleet.Diagnose. Built from the graph rather than hand-written rows, because
// a row whose state and reason were assigned by the test asserts only that assignment works.
func behindHop(t *testing.T, hop string, cands []hostset.Candidate, home string) []DiscoveredRow {
	t.Helper()
	s := newFleetStore()
	s.Observe(crawled(hop, cands, home)...)
	snap := s.Snapshot()
	return DiscoveredRowsFor(snap.Nodes, snap.Candidates, remembered(nil))
}

func labelsOf(rows []DiscoveredRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Label)
	}
	return out
}

// JITTER IS THE DEFECT. Measured on the live fleet, one host's probe answered at 5.4 s, 9.1 s,
// 15.7 s and 18.4 s — so a list sorted on the raw figure reorders between two openings of the same
// screen and the row the operator ticked is a different machine when they come back. Buckets, and
// the name inside a bucket, are what make that impossible rather than unlikely.
func TestTheDiscoveredOrderIsStableWhenLatencyJitters(t *testing.T) {
	rows := func(a, b time.Duration) []string {
		return labelsOf(orderDiscovered([]DiscoveredRow{
			{Label: "alpha", Observer: "hop", RTT: a, Timed: true, State: fleet.Blocked, Reason: "x"},
			{Label: "beta", Observer: "hop", RTT: b, Timed: true, State: fleet.Blocked, Reason: "x"},
		}))
	}
	first := rows(40*time.Millisecond, 45*time.Millisecond)
	second := rows(45*time.Millisecond, 40*time.Millisecond)
	if strings.Join(first, ",") != strings.Join(second, ",") {
		t.Errorf("a 5 ms jitter reordered the list: %v then %v — the row the operator ticked moved",
			first, second)
	}
	// And the positive half, so a comparison of two empty lists cannot satisfy the pair.
	if strings.Join(first, ",") != "alpha,beta" {
		t.Errorf("inside one bucket the order is not by name: %v", first)
	}
}

// The buckets still have to SEPARATE, or "stable" would be satisfied by an order that ignores
// latency altogether.
func TestADifferentBucketStillReordersTheList(t *testing.T) {
	got := labelsOf(orderDiscovered([]DiscoveredRow{
		{Label: "zulu", Observer: "hop", RTT: 40 * time.Millisecond, Timed: true, State: fleet.Blocked, Reason: "x"},
		{Label: "alpha", Observer: "hop", RTT: 400 * time.Millisecond, Timed: true, State: fleet.Blocked, Reason: "x"},
	}))
	if got[0] != "zulu" {
		t.Errorf("the order is %v — a 40 ms machine must come before a 400 ms one whatever its name",
			got)
	}
}

// An unmeasured machine is not a slow one, and it must not be sorted as though it were measured at
// zero: that would put every machine nobody has timed in the FASTEST bucket, which is the one
// direction a remembered order must not fail in.
func TestAMachineNobodyHasTimedIsNeitherFastNorCalledSlow(t *testing.T) {
	timed := DiscoveredRow{Label: "measured", Observer: "hop", RTT: 900 * time.Millisecond, Timed: true}
	untimed := DiscoveredRow{Label: "asked-nothing", Observer: "hop"}

	if discoveredBucket(untimed) <= discoveredBucket(timed) {
		t.Errorf("an untimed machine sorts at bucket %d and a 900 ms one at %d — the unmeasured row "+
			"must not outrank a measured one", discoveredBucket(untimed), discoveredBucket(timed))
	}
	name := discoveredBucketName(untimed)
	if name == discoveredBucketName(timed) {
		t.Errorf("an untimed machine and a 900 ms one both read %q, so the screen cannot tell a "+
			"measurement from its absence", name)
	}
	if strings.Contains(name, "ms") || strings.Contains(name, "slow") {
		t.Errorf("an untimed machine reads %q, which claims a measurement nobody made", name)
	}
	if name == "" {
		t.Error("an untimed machine reads as nothing at all, which is a hole where a fact should be")
	}
}

// Invariant 4 (fleet spec §3.2): every candidate and every unmounted node carries a reason that
// names a remedy, and `unreachable` is not a reason. Built through the real graph and the real
// diagnosis, so the sentences are the product's own.
func TestEveryDiscoveredRowThatIsNotMountedCarriesARemedy(t *testing.T) {
	home := t.TempDir()
	rows := behindHop(t, "hop", []hostset.Candidate{
		{Alias: "leaf", Via: "hop", Recipe: map[string]string{
			"hostname": "leaf.internal", "user": "dev", "identityfile": "~/.ssh/hop-only"}},
		{Alias: "jumped", Via: "hop", Recipe: map[string]string{
			"hostname": "far.internal", "proxyjump": "hop"}},
		{Alias: "pattern-*", Via: "hop", Skip: "a pattern, so there is nothing to probe — name the machine"},
	}, home)

	if len(rows) != 3 {
		t.Fatalf("the hop declared three machines and the section shows %d: %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.State == fleet.Mounted {
			continue
		}
		if strings.TrimSpace(r.Reason) == "" {
			t.Errorf("%s is %v with no reason — a graph that silently omits is indistinguishable "+
				"from a graph that finished", r.Label, r.State)
		}
		if strings.Contains(r.Reason, "unreachable") {
			t.Errorf("%s reads %q; `unreachable` names no act the operator can perform", r.Label, r.Reason)
		}
	}
}

// The section is about what is BEHIND a hop. The root's own candidates already have a row with a
// tick box a few lines above, and drawing one machine twice is the defect this repository has been
// reported three times — each time as "I think I am seeing duplicates".
func TestTheSectionShowsWhatAHopDeclaredAndNotWhatTheRootAlreadyLists(t *testing.T) {
	s := newFleetStore()
	s.Observe(
		fleet.Observation{Observer: rootObserver, Label: "nuc", Reason: "a root candidate"},
		fleet.Observation{Observer: "nuc", Label: "leaf", Reason: "declared by the hop"},
	)
	snap := s.Snapshot()
	rows := DiscoveredRowsFor(snap.Nodes, snap.Candidates, remembered(nil))
	if got := labelsOf(rows); len(got) != 1 || got[0] != "leaf" {
		t.Errorf("the section shows %v — it must hold exactly the machines a HOP declared", got)
	}
}

// Three tiers (plan task 6 step 2): what the root already polls, what it could poll, then the rest
// by bucket. The tiers outrank the bucket, so a mounted machine on a slow link still sits above a
// fast candidate — the question the top of this section answers is "is there anything to do", and a
// machine already mounted needs nothing done.
func TestTheTiersPutMountedFirstThenAvailableThenTheRest(t *testing.T) {
	fast := 10 * time.Millisecond
	got := labelsOf(orderDiscovered([]DiscoveredRow{
		{Label: "cand", Observer: "hop", State: fleet.Candidate, Reason: "x", RTT: fast, Timed: true},
		{Label: "avail", Observer: "hop", State: fleet.Available, Reason: "x", RTT: 2 * time.Second, Timed: true},
		{Label: "mount", Observer: "hop", State: fleet.Mounted, RTT: 3 * time.Second, Timed: true},
		{Label: "block", Observer: "hop", State: fleet.Blocked, Reason: "x", RTT: fast, Timed: true},
	}))
	if strings.Join(got[:2], ",") != "mount,avail" {
		t.Errorf("the order is %v — the two tiers the operator has already decided about come first", got)
	}
	// The tail is by bucket and then by name, and both of these are in the same bucket.
	if strings.Join(got[2:], ",") != "block,cand" {
		t.Errorf("the tail reads %v, want the rest ordered by bucket then by name", got[2:])
	}
}

// THE FRAME. A screen defined, tested and never called is this repository's signature defect, so
// this asserts on the string View() returns, at the 80 columns §16 commits to.
func TestTheDiscoveredSectionIsOnTheScreenAt80Columns(t *testing.T) {
	m := pickerModel(t, 80, 24, samplePickerRows(t), hosts2())
	m.discovered = behindHop(t, "hop", []hostset.Candidate{
		{Alias: "leaf", Via: "hop", Recipe: map[string]string{
			"hostname": "leaf.internal", "user": "dev", "identityfile": "~/.ssh/hop-only"}},
	}, t.TempDir())
	screen := m.View()

	for _, want := range []string{"Behind your hops", "leaf", "hop", "blocked"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the picker at 80 columns does not print %q:\n%s", want, screen)
		}
	}
	// And the candidate list is still there: the section must not have taken the screen.
	if !strings.Contains(screen, "space: keep this host") {
		t.Errorf("the picker's own key line is gone:\n%s", screen)
	}
}

// The remedy is the product (fleet spec §5 point 4). A section that printed `blocked` and dropped
// the command is this repository's oldest defect class — keep the label, lose the action.
func TestTheSectionKeepsTheWholeRemedyAt80Columns(t *testing.T) {
	rows := behindHop(t, "hop", []hostset.Candidate{
		{Alias: "leaf", Via: "hop", Recipe: map[string]string{
			"hostname": "leaf.internal", "user": "dev", "identityfile": "~/.ssh/hop-only"}},
	}, t.TempDir())
	if len(rows) != 1 {
		t.Fatalf("want one row, got %d", len(rows))
	}
	drawn := strings.Join(RenderDiscovered(rows, 80, 20), "\n")
	// Every word of the reason has to survive somewhere in the block, because the wrap is what
	// makes that possible and a truncation is what would not.
	for _, word := range strings.Fields(rows[0].Reason) {
		if !strings.Contains(drawn, word) {
			t.Errorf("the drawn section lost %q from the remedy %q:\n%s", word, rows[0].Reason, drawn)
		}
	}
}

// A line wider than the terminal SOFT-WRAPS, so a section of exactly N lines would occupy more
// than N visual rows and every frame below it desynchronises.
func TestTheSectionNeverDrawsPastTheTerminal(t *testing.T) {
	rows := manyDiscovered(24)
	for _, w := range []int{40, 60, 80, 100, 120, 200} {
		for _, ln := range RenderDiscovered(rows, w, 12) {
			if got := lines.Width(ln); got > w {
				t.Errorf("at %d columns a line is %d wide: %q", w, got, ln)
			}
		}
	}
}

// Monotonicity over width: a wider terminal must never show FEWER machines. It is the assertion
// that catches "composed at one width, rendered at another" without naming a single instance.
func TestAWiderTerminalNeverShowsFewerDiscoveredMachines(t *testing.T) {
	rows := manyDiscovered(12)
	var last int
	for _, w := range []int{40, 60, 80, 100, 120, 160, 200} {
		drawn := strings.Join(RenderDiscovered(rows, w, 14), "\n")
		var shown int
		for _, r := range rows {
			if strings.Contains(drawn, r.Label) {
				shown++
			}
		}
		if shown < last {
			t.Errorf("at %d columns the section shows %d machines where a narrower one showed %d:\n%s",
				w, shown, last, drawn)
		}
		last = shown
	}
	if last == 0 {
		t.Fatal("no width showed a single machine, so the comparison above looked at nothing")
	}
}

// What is cut must be COUNTED. A section that quietly stopped listing is indistinguishable from a
// fleet with nothing more behind it.
func TestTheSectionSaysHowManyMachinesItDidNotShow(t *testing.T) {
	rows := manyDiscovered(20)
	drawn := strings.Join(RenderDiscovered(rows, 80, 6), "\n")
	if !strings.Contains(drawn, "more") {
		t.Errorf("the section cut its list and said nothing about it:\n%s", drawn)
	}
	// The heading carries the total whatever the height, so the number is never the thing that is
	// lost — which is what makes cutting the list safe.
	if !strings.Contains(drawn, "20") {
		t.Errorf("the section does not carry its total of 20:\n%s", drawn)
	}
}

// A section with nothing in it draws nothing: the picker looks exactly as it does today, and the
// count line above it already speaks for the fleet. The reason a CRAWL could not run is a note,
// not an empty section, because those two are different facts.
func TestAnEmptySectionTakesNoRows(t *testing.T) {
	if got := RenderDiscovered(nil, 80, 12); len(got) != 0 {
		t.Errorf("an empty section drew %d lines: %q", len(got), got)
	}
}

// THE TILDE HAZARD, and it fails in the direction that looks like success. `ssh -G` does NOT expand
// `~`, and fleet.Diagnose is handed the path exactly as ssh reported it because expanding inside
// that function would make it read HOME. The CALLER owns HOME — so if this stops expanding, every
// key reads absent, the whole fleet comes back Blocked, and every row advertises an `ssh-copy-id`
// nobody needs.
func TestTheCrawlExpandsTheTildeThatSSHDoesNot(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(home, ".ssh", "id_ed25519")
	if err := os.WriteFile(key, []byte("not a real key"), 0o600); err != nil {
		t.Fatal(err)
	}
	rows := behindHop(t, "hop", []hostset.Candidate{
		{Alias: "leaf", Via: "hop", Recipe: map[string]string{
			"hostname": "leaf.internal", "user": "dev", "identityfile": "~/.ssh/id_ed25519"}},
	}, home)
	if len(rows) != 1 {
		t.Fatalf("want one row, got %d", len(rows))
	}
	if rows[0].State == fleet.Blocked {
		t.Errorf("a key that IS here read as absent, so the row says %q — the `~` ssh leaves alone "+
			"was passed to os.Stat unexpanded", rows[0].Reason)
	}

	// The other pole, in the same shape: a key that is genuinely not here must still be Blocked,
	// or "expanded" would be satisfied by a predicate that answers yes to everything.
	absent := behindHop(t, "hop", []hostset.Candidate{
		{Alias: "leaf", Via: "hop", Recipe: map[string]string{
			"hostname": "leaf.internal", "user": "dev", "identityfile": "~/.ssh/hop-only"}},
	}, home)
	if absent[0].State != fleet.Blocked {
		t.Errorf("a key that is NOT here read as held: %v %q", absent[0].State, absent[0].Reason)
	}
	if !strings.Contains(absent[0].Reason, "hop-only") {
		t.Errorf("the remedy %q does not name the key the recipe asked for", absent[0].Reason)
	}
}

// A path with no `~` at all must be left exactly as ssh gave it — an absolute path is already
// resolved, and rewriting one would be the same defect in the other direction.
func TestAnAbsolutePathIsNotRewritten(t *testing.T) {
	home := t.TempDir()
	key := filepath.Join(home, "elsewhere.pem")
	if err := os.WriteFile(key, []byte("not a real key"), 0o600); err != nil {
		t.Fatal(err)
	}
	held := keyHeldHere("/nowhere-that-exists")
	if !held(key) {
		t.Errorf("an absolute path %q was not found, so it was rewritten against HOME", key)
	}
	if held(filepath.Join(home, "no-such-file.pem")) {
		t.Error("a path that is not there answered yes")
	}
}

// HAZARD 2, and it asserts the VALUE rather than the silence. The store is written by a tea.Cmd
// body while bubbletea's Update reads it, so a lock is a requirement — and a copy-on-write "fix"
// that discarded the writes would keep `-race` quiet while losing every machine the crawl found,
// which is worse than the bug because it removes the evidence and keeps the symptom.
func TestEveryObservationMadeWhileTheScreenWasReadingSurvives(t *testing.T) {
	s := newFleetStore()
	const writers = 24
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.Observe(fleet.Observation{
				Observer: "hop", Label: "m" + string(rune('a'+i)),
				Reason: "declared and not verified",
			})
		}(i)
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snap := s.Snapshot()
			// Read right through the snapshot, because a snapshot that aliased the graph's own
			// memory would be mutated under this loop.
			for _, c := range snap.Candidates {
				_ = c.Alias
			}
		}()
	}
	wg.Wait()
	if got := len(s.Snapshot().Candidates); got != writers {
		t.Errorf("%d of %d observations survived — a lost one is a machine the operator never sees",
			got, writers)
	}
}

// The other half of the same hazard: a snapshot the screen is HOLDING must not change under a
// writer. Never a pointer into shared state (CLAUDE.md) — the reader works on a private copy.
func TestASnapshotDoesNotChangeUnderAConcurrentWriter(t *testing.T) {
	s := newFleetStore()
	s.Observe(fleet.Observation{Observer: "hop", Label: "first", Reason: "x"})
	held := s.Snapshot()
	if len(held.Candidates) != 1 {
		t.Fatalf("the held snapshot starts with %d candidates, want 1", len(held.Candidates))
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.Observe(fleet.Observation{Observer: "hop", Label: "later" + string(rune('a'+i)), Reason: "x"})
		}(i)
	}
	wg.Wait()

	if got := len(held.Candidates); got != 1 {
		t.Errorf("the snapshot the screen was holding grew to %d rows — it was a window on the "+
			"graph rather than a copy of it", got)
	}
	if held.Candidates[0].Alias != "first" {
		t.Errorf("the held snapshot's row now reads %q", held.Candidates[0].Alias)
	}
	if got := len(s.Snapshot().Candidates); got != 17 {
		t.Errorf("a fresh snapshot holds %d rows, want 17 — the writes must land somewhere", got)
	}
}

// The crawl end to end, through the port a hub is wired with: a hop declares a machine, the hub
// diagnoses it, and it arrives as a row with a remedy. The port is a fake, so no network.
func TestTheCrawlFoldsWhatAHopDeclaredIntoTheSection(t *testing.T) {
	m := pickerModel(t, 80, 24, samplePickerRows(t), hosts2())
	m.store = newFleetStore()
	m.home = t.TempDir()
	m.pickerKept = []hostset.Entry{{Alias: "nuc", Enabled: true}, {Alias: "off", Enabled: false}}
	var asked []string
	m.pickerPorts.Behind = func(_ context.Context, hop string) ([]hostset.Candidate, error) {
		asked = append(asked, hop)
		return []hostset.Candidate{{Alias: "leaf", Via: hop, Recipe: map[string]string{
			"hostname": "leaf.internal", "user": "dev", "identityfile": "~/.ssh/hop-only"}}}, nil
	}

	cmd := m.crawlBehind()
	if cmd == nil {
		t.Fatal("a hub with a Behind port and one mounted hop produced no crawl")
	}
	msg, ok := cmd().(discoveredMsg)
	if !ok {
		t.Fatalf("the crawl answered %T", cmd())
	}
	// Only the hosts the file ENABLES are asked: a host the operator turned off has no master to
	// travel over, so asking it would spend a whole connect timeout on a machine nobody wants.
	if strings.Join(asked, ",") != "nuc" {
		t.Errorf("the crawl asked %v, want only the enabled host", asked)
	}
	next, _ := m.Update(msg)
	got := next.(model)
	if len(got.discovered) != 1 {
		t.Fatalf("the section holds %d rows after the crawl: %+v", len(got.discovered), got.discovered)
	}
	row := got.discovered[0]
	if row.Label != "leaf" || row.Observer != "nuc" {
		t.Errorf("the row reads %q @%q, want leaf @nuc", row.Label, row.Observer)
	}
	if row.State != fleet.Blocked || !strings.Contains(row.Reason, "hop-only") {
		t.Errorf("the row is %v %q, want Blocked naming the key the hop's recipe asked for",
			row.State, row.Reason)
	}
	if !strings.Contains(got.View(), "leaf") {
		t.Errorf("the crawl's row is not on the screen:\n%s", got.View())
	}
}

// A hop the crawl could not ASK must be named, and the row it would have contributed must not be
// invented in its place. Those two are the same defect from opposite sides: a silent failure reads as
// a hop with nothing behind it, and a fabricated row reads as a machine the hub has spoken to.
//
// It also pins WHAT the note may not say. This repository has shipped a status naming a mechanism the
// design did not contain, and this sentence is where that is cheapest to do again: the root never
// reaches the far machines either way, so a reason about the root's own transport would be true of a
// working fleet too.
func TestACrawlThatCouldNotAskAHopSaysWhichHop(t *testing.T) {
	m := pickerModel(t, 80, 24, samplePickerRows(t), hosts2())
	m.store = newFleetStore()
	m.home = t.TempDir()
	m.pickerKept = []hostset.Entry{{Alias: "depot-a", Enabled: true}}
	m.pickerPorts.Behind = func(context.Context, string) ([]hostset.Candidate, error) {
		return nil, errors.New("ssh: Could not resolve hostname depot-a: Name or service not known")
	}

	cmd := m.crawlBehind()
	if cmd == nil {
		t.Fatal("no crawl was produced for one enabled hop")
	}
	msg, ok := cmd().(discoveredMsg)
	if !ok {
		t.Fatalf("the crawl answered %T", cmd())
	}
	if len(msg.rows) != 0 {
		t.Errorf("a hop that could not be asked contributed %d rows: %+v — nothing may be invented "+
			"from a crawl that failed", len(msg.rows), msg.rows)
	}
	if msg.note == "" {
		t.Fatal("the crawl said nothing about a hop it could not reach, so an unreachable hop reads " +
			"exactly like a hop with nothing behind it")
	}
	if !strings.Contains(msg.note, "depot-a") {
		t.Errorf("the note %q does not name the hop it is about; a crawl reports several and an "+
			"unnamed failure is not actionable", msg.note)
	}
	for _, forbidden := range []string{"tunnel", "unreachable"} {
		if strings.Contains(strings.ToLower(msg.note), forbidden) {
			t.Errorf("the note %q names %q — a mechanism this design does not have, or a word that "+
				"names no act", msg.note, forbidden)
		}
	}
	// And it reaches the screen, because a note the model holds and the renderer drops is a note
	// nobody reads.
	next, _ := m.Update(msg)
	if !strings.Contains(next.(model).View(), "depot-a") {
		t.Errorf("the note is not on the screen:\n%s", next.(model).View())
	}
}

// openFleetCache is the production pair of ports over a REAL cache, built the way main.go builds
// it: `fleetcache.Open`, then `Facts` for the reader and `Record` for the writer.
//
// A hand-written closure is the wrong fixture for anything about ORDER, and that is measured rather
// than argued — see TestAProbeRoundDoesNotReorderTheSectionUnderItsReader, whose whole defect was a
// Learn port with no Facts port behind it. The real pair also proves the round trip through the file,
// which a captured map cannot: `Record` is what persists, and the map handed to it is the ARGUMENT.
func openFleetCache(t *testing.T) *fleetcache.Cache {
	t.Helper()
	c, err := fleetcache.Open(filepath.Join(t.TempDir(), "fleet-cache.json"))
	if err != nil {
		t.Fatalf("opening a fresh fleet cache: %v", err)
	}
	if c.Len() != 0 {
		t.Fatalf("a fresh cache remembers %d machines", c.Len())
	}
	return c
}

// A probe round that lands WHILE the picker is open must not reorder the list under its reader.
//
// This is the second half of the anti-jitter rule and the easier one to lose: bucketing stops the
// order changing between two openings, and this stops it changing DURING one.
//
// THE FIXTURE IS THE WHOLE CASE, and the first version of it could not fail. It wired `Learn` and
// left `Facts` nil, so `DiscoveredRowsFor` fell back to its "this hub remembers nothing" lookup and
// EVERY row was untimed whatever the round measured — the faster bucket the comment claimed to build
// was unreachable, and a `learnFromProbe` that rebuilt the section from the freshly learned figures
// passed. Measured with that mutant in place, one variable between the two arms — the model's `Facts`
// PORT: unwired the case reports `ok` at rc=0, wired it FAILS with `the list read [one two] and now
// reads [two one]`. So the ports here are the production pair over a real cache, and the fixture's
// potency is asserted below rather than described.
func TestAProbeRoundDoesNotReorderTheSectionUnderItsReader(t *testing.T) {
	cache := openFleetCache(t)
	m := pickerModel(t, 80, 24, samplePickerRows(t), hosts2())
	m.store = newFleetStore()
	m.pickerPorts.Facts, m.pickerPorts.Learn = cache.Facts, cache.Record
	m.store.Observe(
		fleet.Observation{Observer: "alpha-hop", Label: "one", Reason: "x"},
		fleet.Observation{Observer: "zulu-hop", Label: "two", Reason: "x"},
	)
	// Through the SAME reader production reads with, on a cache that is still empty: two untimed
	// rows, ordered by name. `one` before `two`, which is the order the round below inverts.
	snap := m.store.Snapshot()
	m.discovered = DiscoveredRowsFor(snap.Nodes, snap.Candidates, cache.Facts)
	before := labelsOf(m.discovered)
	if strings.Join(before, ",") != "one,two" {
		t.Fatalf("the list starts as %v; this case is about the order changing, so it has to know "+
			"which order it started in", before)
	}

	// A round that puts `zulu-hop` two buckets ahead of `alpha-hop` — 10 ms is `<50ms` and 900 ms is
	// `<1s` — so a section rebuilt from these figures MUST come out in the other order. That is the
	// cell being on the side where the branch decides: a pair of figures inside one bucket would
	// leave the order alone under any code and prove nothing.
	got := m.learnFromProbe([]hostset.Result{
		{Alias: "alpha-hop", Version: "3.2a", Usable: true, Took: 900 * time.Millisecond},
		{Alias: "zulu-hop", Version: "3.2a", Usable: true, Took: 10 * time.Millisecond},
	}, time.Unix(0, 0))

	if after := labelsOf(got.discovered); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Errorf("the list read %v and now reads %v — it reordered while the operator was looking at it",
			before, after)
	}

	// THE POTENCY OF THE FIXTURE, asserted and not assumed. The round's figures have to be enough to
	// move this list, or "it did not move" is a statement about the fixture rather than about the
	// code — which is exactly what it was. Same graph, same reader, after the round.
	rebuilt := labelsOf(DiscoveredRowsFor(snap.Nodes, snap.Candidates, cache.Facts))
	if strings.Join(rebuilt, ",") != "two,one" {
		t.Fatalf("a rebuild after the round reads %v, want two,one — the round's figures cannot "+
			"reorder this list, so the assertion above cannot fail and is not a test", rebuilt)
	}

	// And the figures ARE remembered, read back through the reader rather than out of the map handed
	// to the writer: the map is what the model asked for, the cache is what happened. Without this
	// the case would pass against a round that measured nothing at all.
	if cache.Len() != 2 {
		t.Fatalf("the cache remembers %d machines after a two-host round", cache.Len())
	}
	f, ok := cache.Facts(fleetcache.Key{Alias: "zulu-hop"})
	if !ok {
		t.Fatal("nothing was remembered for zulu-hop, so the next opening waits for a probe again")
	}
	if f.RTT != 10*time.Millisecond || f.TmuxVersion != "3.2a" {
		t.Errorf("what was remembered for zulu-hop is %+v", f)
	}
}

// A timed-out probe's `Took` is the operator's own deadline and not the machine's distance, so it must
// not be filed as a measurement — a remembered timeout would sort a slow-but-present host by how long
// the hub was willing to wait.
func TestATimedOutProbeIsNotRememberedAsAMeasurement(t *testing.T) {
	m := pickerModel(t, 80, 24, samplePickerRows(t), hosts2())
	m.store = newFleetStore()
	var wrote map[fleetcache.Key]fleetcache.Facts
	m.pickerPorts.Learn = func(f map[fleetcache.Key]fleetcache.Facts) error {
		wrote = f
		return nil
	}
	m.learnFromProbe([]hostset.Result{
		{Alias: "slow", TimedOut: true, Took: 10 * time.Second},
		{Alias: "fine", Version: "3.2a", Usable: true, Took: 40 * time.Millisecond},
	}, time.Unix(0, 0))
	if _, ok := wrote[fleetcache.Key{Alias: "slow"}]; ok {
		t.Error("a timed-out probe was filed as a round trip")
	}
	if _, ok := wrote[fleetcache.Key{Alias: "fine"}]; !ok {
		t.Error("the host that DID answer was not remembered, so the filter above is too wide")
	}
}

// A hub with no way to look behind a hop must SAY so rather than showing an empty section: an empty
// section and a crawl that never ran are different facts, and a key that does nothing and says
// nothing reads as a broken key.
func TestAHubWithNoWayToLookBehindAHopSaysSo(t *testing.T) {
	m := pickerModel(t, 80, 24, samplePickerRows(t), hosts2())
	m.store = newFleetStore()
	m.pickerKept = []hostset.Entry{{Alias: "nuc", Enabled: true}}
	if cmd := m.crawlBehind(); cmd != nil {
		t.Fatal("a hub with no Behind port produced a crawl anyway")
	}
	if got := m.crawlRefusal(); got == "" {
		t.Error("a hub that cannot crawl has nothing to say about it")
	}
}

// A budget that cuts must report the count (fleet spec §3.3), and the crawl is where the cut
// happens. A silent horizon reads as a finished crawl.
func TestTheCrawlReportsABudgetCutWithItsCount(t *testing.T) {
	s := newFleetStore()
	labels := []string{"a", "b", "c", "d", "e"}
	got := s.Allow(fleet.Budget{MaxDepth: 2, MaxPerObserver: 2}, "hop", 1, labels)
	if len(got) != 2 {
		t.Fatalf("the budget allowed %d of 5, want 2", len(got))
	}
	cuts := s.Snapshot().Cuts
	if len(cuts) != 1 {
		t.Fatalf("three aliases were dropped and %d cuts were reported", len(cuts))
	}
	if cuts[0].Skipped != 3 {
		t.Errorf("the cut says %d skipped, want 3", cuts[0].Skipped)
	}
	if !strings.Contains(cuts[0].Why, "3") || !strings.Contains(cuts[0].Why, "raise") {
		t.Errorf("the cut sentence %q carries no count or no act", cuts[0].Why)
	}
}

// manyDiscovered is a fleet big enough to reach every truncation and cut path. The names are the
// LENGTH real ones are: a hop's alias is another machine's vocabulary and nothing bounds it.
func manyDiscovered(n int) []DiscoveredRow {
	out := make([]DiscoveredRow, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, DiscoveredRow{
			Label:    "reverse-engineering-box-" + string(rune('a'+i)),
			Observer: "a-hop-with-a-long-name",
			State:    fleet.Blocked,
			RTT:      time.Duration(i*30) * time.Millisecond,
			Timed:    true,
			Reason: "run `ssh-copy-id dev@machine-" + string(rune('a'+i)) +
				".internal`, or copy ~/.ssh/hop-only to this machine — the recipe names no key that is here",
		})
	}
	return orderDiscovered(out)
}

// samplePickerRows is a small candidate list so the picker has something above the section. It goes
// through PickerRowsFor, which is the one owner of what a row says.
func samplePickerRows(t *testing.T) []PickerRow {
	t.Helper()
	return PickerRowsFor(
		[]hostset.Candidate{{Alias: "nuc"}, {Alias: "office-nas"}},
		[]hostset.Result{
			{Alias: "nuc", Version: "3.2a", Usable: true},
			{Alias: "office-nas", Version: "3.2a", Usable: true},
		}, nil, nil, time.Unix(0, 0))
}

// A model built by the fixtures never went through WithPicker, so it holds no store. Every READ
// path has to survive that, because the section is drawn on screens no crawl ever touched.
func TestANilStoreReadsAsAnEmptyFleetRatherThanPanicking(t *testing.T) {
	var s *fleetStore
	snap := s.Snapshot()
	if len(snap.Nodes) != 0 || len(snap.Candidates) != 0 || len(snap.Cuts) != 0 {
		t.Errorf("a nil store answered %+v", snap)
	}
	m := pickerModel(t, 80, 24, samplePickerRows(t), hosts2())
	if got := m.View(); got == "" {
		t.Error("a model with no store rendered nothing")
	}
	if cmd := m.crawlBehind(); cmd != nil {
		t.Error("a model with no store produced a crawl")
	}
}

var _ = tea.Cmd(nil)

// THE SECTION NEVER RETURNS MORE LINES THAN IT WAS GIVEN, and it never spends a line saying
// "N not shown" while naming no machine.
//
// Both halves were measured by an independent verifier: `RenderDiscovered(rows, width, 1)` returned
// TWO lines — one past the contract its own doc comment states — and at heights 2 and 3 it returned the
// heading plus `↓ N machines not shown` without ever naming a machine. The second line is redundant
// there rather than merely wasteful: the heading already carries the TOTAL, so a reader who sees no
// machine listed under "N machines your hosts declare" has already been told the count.
func TestTheSectionHonoursItsHeightAndNamesAMachineOrSaysNothing(t *testing.T) {
	rows := []DiscoveredRow{
		{Label: "leaf", Observer: "hop", State: fleet.Blocked,
			Reason: "run `ssh-copy-id dev@leaf`, or copy ~/.ssh/hop-only to this machine"},
		{Label: "twin", Observer: "hop", State: fleet.Blocked,
			Reason: "run `ssh-copy-id dev@twin`, or copy ~/.ssh/hop-only to this machine"},
	}
	for h := 1; h <= 6; h++ {
		got := RenderDiscovered(rows, 80, h)
		if len(got) > h {
			t.Errorf("height %d returned %d lines, which overruns whatever drew it:\n%q", h, len(got), got)
		}
		named := false
		for _, l := range got {
			if strings.Contains(l, "leaf") || strings.Contains(l, "twin") {
				named = true
			}
		}
		for _, l := range got {
			if strings.Contains(l, "not shown") && !named {
				t.Errorf("height %d spends a line on %q while naming no machine, and the heading "+
					"already carries the count:\n%q", h, l, got)
			}
		}
	}
}
