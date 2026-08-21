package fleet

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// Identity is a SET and observations MERGE: one machine reached by two aliases is one node.
func TestTwoAliasesForOneMachineAreOneNode(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "root", Label: "hop",
		Fingerprints: []string{"SHA256:aaa"}, Verified: true})
	g.Observe(Observation{Observer: "root", Label: "hop-again",
		Fingerprints: []string{"SHA256:aaa"}, Verified: true})
	if got := len(g.Nodes()); got != 1 {
		t.Fatalf("two aliases for one fingerprint gave %d nodes, want 1", got)
	}
	if got := g.Nodes()[0].Labels; len(got) != 2 {
		t.Errorf("the node carries %d labels, want 2 — a merge must keep both names", len(got))
	}
}

// A machine that presents a second host key is still ONE node: identity is intersection, not equality.
func TestASecondHostKeyJoinsTheSetRatherThanForkingTheNode(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "root", Label: "h", Fingerprints: []string{"SHA256:aaa"}, Verified: true})
	g.Observe(Observation{Observer: "root", Label: "h", Fingerprints: []string{"SHA256:aaa", "SHA256:bbb"}, Verified: true})
	if got := len(g.Nodes()); got != 1 {
		t.Fatalf("an added fingerprint forked the node: %d nodes, want 1", got)
	}
}

// Two DIFFERENT machines answering to one hostname are two nodes — four `web-ws` on the live tailnet.
func TestOneHostnameTwoMachinesAreTwoNodes(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "root", Label: "twin", Hostname: "twin",
		Fingerprints: []string{"SHA256:aaa"}, Verified: true})
	g.Observe(Observation{Observer: "root", Label: "twin", Hostname: "twin",
		Fingerprints: []string{"SHA256:bbb"}, Verified: true})
	if got := len(g.Nodes()); got != 2 {
		t.Fatalf("one hostname over two fingerprints gave %d nodes, want 2", got)
	}
}

// Unverified is a CANDIDATE, never a node (spec §3.2 invariant 3).
func TestAnUnverifiedObservationIsACandidateNotANode(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "hop", Label: "leaf", Verified: false,
		Reason: "the hop's recipe names ~/.ssh/hop-only, which is not here"})
	if got := len(g.Nodes()); got != 0 {
		t.Fatalf("an unverified observation created %d nodes, want 0", got)
	}
	c := g.Candidates()
	if len(c) != 1 || c[0].Reason == "" {
		t.Fatalf("want one candidate carrying a reason, got %+v", c)
	}
}

// The walk visits identities, so a cycle terminates with no special case.
func TestTheWalkTerminatesOnACycle(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "root", Label: "hop", Fingerprints: []string{"SHA256:aaa"}, Verified: true})
	g.Observe(Observation{Observer: "hop", Label: "root", Fingerprints: []string{"SHA256:root"}, Verified: true})
	g.Observe(Observation{Observer: "root", Label: "root", Fingerprints: []string{"SHA256:root"}, Verified: true})
	seen := 0
	g.Walk(func(n Node) { seen++ })
	if seen != len(g.Nodes()) {
		t.Fatalf("the walk visited %d of %d nodes — a cycle must not revisit", seen, len(g.Nodes()))
	}
}

// An observation whose set BRIDGES two known nodes merges them: identity is intersection, so a
// machine that presented `aaa` on one call and `bbb` on another was always one machine, and the
// graph must stop holding two nodes that both answer to `bbb`.
func TestAnObservationBridgingTwoNodesMergesThem(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "root", Label: "one", Fingerprints: []string{"SHA256:aaa"}, Verified: true})
	g.Observe(Observation{Observer: "root", Label: "two", Fingerprints: []string{"SHA256:bbb"}, Verified: true})
	if got := len(g.Nodes()); got != 2 {
		t.Fatalf("two disjoint sets gave %d nodes, want 2 before the bridge", got)
	}
	g.Observe(Observation{Observer: "root", Label: "one",
		Fingerprints: []string{"SHA256:aaa", "SHA256:bbb"}, Verified: true})
	if got := len(g.Nodes()); got != 1 {
		t.Fatalf("a bridging observation left %d nodes, want 1 — both fingerprints are one machine", got)
	}
	n := g.Nodes()[0]
	if len(n.Fingerprints) != 2 {
		t.Errorf("the merged node holds %v, want both fingerprints", n.Fingerprints)
	}
	if len(n.Labels) != 2 {
		t.Errorf("the merged node carries %d labels, want 2 — a merge keeps every name", len(n.Labels))
	}
}

// The poll repeats about four times a second forever, so re-observing one machine must not grow
// anything: a second `[h, h]` label would draw the name twice and the edge list would grow without
// bound.
func TestReObservingOneMachineGrowsNothing(t *testing.T) {
	g := New()
	for range 3 {
		g.Observe(Observation{Observer: "root", Label: "h",
			Fingerprints: []string{"SHA256:aaa", "SHA256:aaa"}, Verified: true})
	}
	n := g.Nodes()[0]
	if len(n.Labels) != 1 {
		t.Errorf("three polls left %d labels, want 1: %+v", len(n.Labels), n.Labels)
	}
	if len(n.Fingerprints) != 1 {
		t.Errorf("three polls left %d fingerprints, want 1: %v", len(n.Fingerprints), n.Fingerprints)
	}
	if got := len(g.Edges()); got != 1 {
		t.Errorf("three polls left %d edges, want 1 — one observer, one alias, one edge", got)
	}
}

// A candidate that the root then verifies must stop being a candidate. Otherwise the picker draws
// the machine twice — once as a node and once as a candidate — which is this repository's oldest
// reported defect wearing a new hat.
func TestAVerifiedDeclarationIsNoLongerACandidate(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "root", Label: "leaf", Reason: "banner exchange timed out"})
	if got := len(g.Candidates()); got != 1 {
		t.Fatalf("the unverified declaration gave %d candidates, want 1", got)
	}
	g.Observe(Observation{Observer: "root", Label: "leaf",
		Fingerprints: []string{"SHA256:leaf"}, Verified: true})
	if got := len(g.Candidates()); got != 0 {
		t.Errorf("the machine is a node AND still a candidate (%d) — one machine, two rows", got)
	}
	if got := len(g.Nodes()); got != 1 {
		t.Errorf("verification gave %d nodes, want 1", got)
	}
}

// Invariant 4: every candidate carries a reason that names a remedy, and `unreachable` is not a
// reason. A producer that names none is a producer bug, and the graph must not store the silence.
func TestACandidateWithNoStatedReasonStillCarriesOne(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "hop", Label: "leaf"})
	c := g.Candidates()
	if len(c) != 1 {
		t.Fatalf("got %d candidates, want 1", len(c))
	}
	if c[0].Reason == "" {
		t.Fatal("a candidate reached the graph with no reason (spec §3.2 invariant 4)")
	}
	if strings.Contains(c[0].Reason, "unreachable") {
		t.Errorf("the reason %q says `unreachable`, which invariant 4 forbids", c[0].Reason)
	}
	if !strings.Contains(c[0].Reason, "leaf") {
		t.Errorf("the reason %q does not name the machine it is about", c[0].Reason)
	}
}

// The node? column of §3.4 is a partition, so the two halves cannot both be true of one row. A
// declaration nobody verified may not claim a node state, and verification refutes `Blocked`.
func TestAStateThatContradictsVerificationIsRefused(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "hop", Label: "leaf", Reason: "no key here", State: Mounted})
	if got := g.Candidates()[0].State; got != Candidate {
		t.Errorf("an unverified declaration claims %v; want Candidate — only a handshake makes a node", got)
	}
	g.Observe(Observation{Observer: "root", Label: "hop", State: Blocked,
		Fingerprints: []string{"SHA256:aaa"}, Verified: true, Reason: "copy ~/.ssh/hop-only here"})
	if got := g.Nodes()[0].State; got != Ready {
		t.Errorf("a verified machine still reads %v; want Ready — the handshake refutes the diagnosis", got)
	}
	// The remedy travels with the state it belonged to. A node reading `ready` beside "copy
	// ~/.ssh/hop-only here" would name a mechanism its own handshake has just disproved, which is
	// the oracle's point 4 and a defect this repository has shipped once already.
	if r := g.Nodes()[0].Reason; strings.Contains(r, "hop-only") {
		t.Errorf("a verified machine kept the blocked remedy %q, which is now false", r)
	}
	if g.Nodes()[0].Reason == "" {
		t.Error("an unmounted node carries no reason at all (invariant 4)")
	}
}

// The zero value of every derived field claims the LEAST. A struct nobody filled in must not read
// `Mounted` (the most privileged state) or `posix` (the shell family that made a host invisible at
// rc=0 on 2026-08-20). This pins the constant order against a later tidy.
func TestTheZeroValuesClaimTheLeast(t *testing.T) {
	var n Node
	if n.State != Candidate {
		t.Errorf("the zero State is %v, want Candidate — an unfilled struct must claim nothing", n.State)
	}
	if n.Shell != ShellOther {
		t.Errorf("the zero Shell is %v, want other — an unproven family is `other` (spec §3.1)", n.Shell)
	}
	if Mounted <= Candidate || Ready <= Blocked {
		t.Error("the states are no longer ordered least-privileged first")
	}
}

// Nodes() hands out a SNAPSHOT. The caller that shares a graph across bubbletea's Update and a
// tea.Cmd works on a private copy and merges back by key — a returned slice aliasing the graph's
// own memory is how this repository lost a host's status writes once already.
func TestNodesHandsOutACopy(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "root", Label: "hop", Hostname: "hop.internal",
		Fingerprints: []string{"SHA256:aaa"}, Verified: true,
		Recipe: map[string]string{"user": "dev"}})
	got := g.Nodes()
	got[0].Labels[0].Alias = "vandalised"
	got[0].Fingerprints[0] = "SHA256:vandalised"
	got[0].Recipe["user"] = "vandalised"
	again := g.Nodes()[0]
	if again.Labels[0].Alias != "hop" {
		t.Errorf("a caller's write reached the graph's labels: %+v", again.Labels)
	}
	if again.Fingerprints[0] != "SHA256:aaa" {
		t.Errorf("a caller's write reached the graph's identity: %v", again.Fingerprints)
	}
	if again.Recipe["user"] != "dev" {
		t.Errorf("a caller's write reached the graph's recipe: %v", again.Recipe)
	}
}

// A field with two homes is a field one of them forgets. `UpdateAgents` copied five fields and not
// the sixth, and every test of the screen that read it passed because every one of them hand-built
// the struct; the same shape has now cost this repository three separate defects. So this asserts the
// PRODUCER rather than the struct: an observation carrying every field must leave no field of the
// node it makes at its zero value, and a field added to Observation later cannot be silently dropped
// by the fold.
func TestObserveCarriesEveryFieldOntoTheNode(t *testing.T) {
	g := New()
	g.Observe(Observation{
		Observer:     "root",
		Label:        "hop",
		Hostname:     "hop.internal",
		Fingerprints: []string{"SHA256:aaa"},
		Verified:     true,
		Reason:       "over the latency budget at 180ms — tick it anyway to mount it",
		Recipe:       map[string]string{"identityfile": "~/.ssh/id_ed25519"},
		Shell:        ShellPOSIX,
		TmuxVersion:  "3.2a",
		UID:          "501",
		Sample:       12 * time.Millisecond,
		State:        Available,
	})
	n := g.Nodes()[0]
	v := reflect.ValueOf(n)
	for i := range v.NumField() {
		if v.Field(i).IsZero() {
			t.Errorf("Observe left Node.%s at its zero value — the fold forgot a field the "+
				"observation carried", v.Type().Field(i).Name)
		}
	}
}

// Decision 6 of the spec: five states, and every one but `Mounted` carries a remedy. Mechanical,
// over all five, because a reason string is easy to produce and hard to keep true — and a state whose
// remedy is missing is exactly the "keep the label, lose the action" class this repository has
// carried as a known issue since its first week.
func TestEveryStateButMountedCarriesARemedy(t *testing.T) {
	fp := map[State]string{Ready: "SHA256:r", Available: "SHA256:a", Mounted: "SHA256:m"}
	for _, want := range []State{Candidate, Blocked, Ready, Available, Mounted} {
		g := New()
		o := Observation{Observer: "root", Label: "machine-" + want.String(), State: want}
		if want.node() {
			o.Fingerprints, o.Verified = []string{fp[want]}, true
		}
		g.Observe(o)
		var got State
		var reason string
		if want.node() {
			got, reason = g.Nodes()[0].State, g.Nodes()[0].Reason
		} else {
			got, reason = g.Candidates()[0].State, g.Candidates()[0].Reason
		}
		if got != want {
			t.Errorf("%v was stored as %v", want, got)
		}
		if want != Mounted && reason == "" {
			t.Errorf("%v carries no remedy (spec §7 decision 6)", want)
		}
		if strings.Contains(reason, "unreachable") {
			t.Errorf("%v says %q — `unreachable` names no act the operator can perform", want, reason)
		}
	}
}

// The mirror of TestAVerifiedDeclarationIsNoLongerACandidate: a probe that fails against a machine
// the root has ALREADY spoken to must not add a second row beside its node. The handshake happened,
// so the identity stands; what changed is only what the hub can say about it. A rule that can be got
// wrong in two directions has already been got wrong in both.
func TestAFailedProbeDoesNotDuplicateAKnownMachine(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "root", Label: "hop",
		Fingerprints: []string{"SHA256:aaa"}, Verified: true})
	g.Observe(Observation{Observer: "root", Label: "hop",
		Reason: "connection timed out during banner exchange — check the ssh master"})
	if got := len(g.Nodes()); got != 1 {
		t.Errorf("a failed probe left %d nodes, want 1 — a handshake that happened cannot un-happen", got)
	}
	if got := len(g.Candidates()); got != 0 {
		t.Errorf("a failed probe added %d candidates beside the node — one machine, two rows", got)
	}
	if r := g.Nodes()[0].Reason; !strings.Contains(r, "banner exchange") {
		t.Errorf("the node's reason is %q; the failure had something to say and it was dropped", r)
	}
}

// Samples are newest first and BOUNDED. The hub polls about four times a second for as long as it is
// open, so an unbounded fold reaches six figures per node in a working day.
//
// Every number here is a LITERAL. The first version of this test wrote `maxSamples+4` observations
// and compared the result against `maxSamples`, which is self-consistent for any window: `const
// maxSamples = 3` and `= 200` both survived a mutation pass. Eight is a decision with a paragraph of
// reasoning beside it in fleet.go, and a test that imports the decision cannot hold it.
func TestSamplesAreNewestFirstAndBounded(t *testing.T) {
	if maxSamples != 8 {
		t.Fatalf("the window is %d; this test's literals were written for 8 — re-derive them against "+
			"fleet.go's reasoning rather than adjusting the expectation", maxSamples)
	}
	g := New()
	for i := 1; i <= 12; i++ {
		g.Observe(Observation{Observer: "root", Label: "hop", Fingerprints: []string{"SHA256:aaa"},
			Verified: true, Sample: time.Duration(i) * time.Millisecond})
	}
	got := g.Nodes()[0].Samples
	ms := time.Millisecond
	want := []time.Duration{12 * ms, 11 * ms, 10 * ms, 9 * ms, 8 * ms, 7 * ms, 6 * ms, 5 * ms}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the window is %v, want %v — eight samples, newest first (spec §3.1)", got, want)
	}
}

// A node's remedy is the next move for the state that node is IN. `declare` folds a failed probe onto
// a machine the root has already spoken to, and the reason that lands must be the FAILURE's own
// sentence — never one computed for `Candidate`, which is a state the node is not in. Both directions
// shipped in the first cut of this package: a Mounted node acquired a candidate's `ssh -v` remedy,
// though §3.4 makes Mounted the one state with no next move, and a node already carrying a true
// remedy had it overwritten by a probe that said nothing at all.
func TestAFailedProbeDoesNotGiveANodeAnotherStatesRemedy(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "root", Label: "hop", Fingerprints: []string{"SHA256:aaa"},
		Verified: true, State: Mounted})
	if n := g.Nodes()[0]; n.State != Mounted || n.Reason != "" {
		t.Fatalf("the fixture is not a mounted node with no remedy: state=%v reason=%q", n.State, n.Reason)
	}
	// A probe that names no reason has learned nothing this graph can phrase: liveness is the
	// poller's, and none of the five states is about it.
	g.Observe(Observation{Observer: "root", Label: "hop"})
	n := g.Nodes()[0]
	if n.State != Mounted {
		t.Errorf("a failed probe moved the node to %v — liveness is not one of the five states", n.State)
	}
	if n.Reason != "" {
		t.Errorf("a mounted node acquired the remedy %q; Mounted is the state with no next move", n.Reason)
	}

	// The other half: a node whose own state carries a remedy keeps THAT remedy.
	g2 := New()
	g2.Observe(Observation{Observer: "root", Label: "hop", Fingerprints: []string{"SHA256:aaa"},
		Verified: true, State: Available,
		Reason: "over the latency budget at 180ms — tick it anyway to mount it"})
	g2.Observe(Observation{Observer: "root", Label: "hop"})
	if r := g2.Nodes()[0].Reason; !strings.Contains(r, "latency budget") {
		t.Errorf("the node's reason is now %q — a probe that said nothing replaced a remedy that was true", r)
	}
}

// A merge shares the sample window between both identities. Samples carry no timestamp, so ORDER is
// the only recency information that exists, and CONCATENATING two newest-first lists claims the
// absorbed node's newest sample is older than the keeper's oldest — a claim neither list contains.
func TestAMergeSharesTheSampleWindowBetweenBothIdentities(t *testing.T) {
	g := New()
	for _, d := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond} {
		g.Observe(Observation{Observer: "root", Label: "one", Fingerprints: []string{"SHA256:aaa"},
			Verified: true, Sample: d})
	}
	for _, d := range []time.Duration{time.Millisecond, 2 * time.Millisecond} {
		g.Observe(Observation{Observer: "root", Label: "two", Fingerprints: []string{"SHA256:bbb"},
			Verified: true, Sample: d})
	}
	// The bridge carries the identity and no sample of its own, so the window below is entirely the
	// merge's work. Four samples is under the bound, so this case is about ORDER alone.
	g.Observe(Observation{Observer: "root", Label: "one",
		Fingerprints: []string{"SHA256:aaa", "SHA256:bbb"}, Verified: true})
	got := g.Nodes()[0].Samples
	want := []time.Duration{200 * time.Millisecond, 2 * time.Millisecond,
		100 * time.Millisecond, time.Millisecond}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the merged window is %v, want %v — each list's newest first, alternating", got, want)
	}
}

// The same merge with two FULL windows, where the bound decides what survives. The first cut kept the
// first `maxSamples` of a concatenation, which is 100% of the keeper and NONE of the machine it had
// just been shown to be — so a merged node's latency window was decided by which alias was observed
// first, a fact about observation order rather than about the machine.
func TestAMergeOfTwoFullWindowsKeepsSamplesFromBoth(t *testing.T) {
	g := New()
	for i := 1; i <= maxSamples; i++ {
		g.Observe(Observation{Observer: "root", Label: "slow", Fingerprints: []string{"SHA256:aaa"},
			Verified: true, Sample: time.Duration(i) * time.Second})
		g.Observe(Observation{Observer: "root", Label: "fast", Fingerprints: []string{"SHA256:bbb"},
			Verified: true, Sample: time.Duration(i) * time.Millisecond})
	}
	g.Observe(Observation{Observer: "root", Label: "slow",
		Fingerprints: []string{"SHA256:aaa", "SHA256:bbb"}, Verified: true})
	got := g.Nodes()[0].Samples
	if len(got) != maxSamples {
		t.Fatalf("the merged window holds %d samples, want the bound of %d", len(got), maxSamples)
	}
	slow, fast := 0, 0
	for _, d := range got {
		if d >= time.Second {
			slow++
		} else {
			fast++
		}
	}
	if slow == 0 || fast == 0 {
		t.Errorf("the merged window %v holds samples from only one of the two identities "+
			"(%d slow, %d fast) — the other machine's round trips were discarded", got, slow, fast)
	}
}

// Walk FOLLOWS EDGES, and the observable is visit order: `seen == len(Nodes())` is satisfied by the
// outer fallback loop on its own, so it cannot tell a traversal from an iteration. Here `a` declares
// `c`, so the walk must reach c FROM a — before b, which no edge reached.
func TestTheWalkFollowsAnEdgeBeforeFallingBack(t *testing.T) {
	g := New()
	for _, alias := range []string{"a", "b", "c"} {
		g.Observe(Observation{Observer: "root", Label: alias,
			Fingerprints: []string{"SHA256:" + alias}, Verified: true})
	}
	// An observer is named by the alias its declarer used, so this edge leaves the node the root
	// calls `a` and lands on the node holding `SHA256:c`.
	g.Observe(Observation{Observer: "a", Label: "c-from-a",
		Fingerprints: []string{"SHA256:c"}, Verified: true})
	var order []string
	g.Walk(func(n Node) { order = append(order, n.Fingerprints[0]) })
	want := []string{"SHA256:a", "SHA256:c", "SHA256:b"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("the walk visited %v, want %v — an edge-blind walk yields the insertion order", order, want)
	}
}

// Every accessor in this package is documented as a snapshot, and one test covered one of them. The
// property is load-bearing for the Update/tea.Cmd split: a returned slice or map that aliased the
// graph's own memory is how this repository lost a whole poll's status writes once already.
func TestEveryAccessorHandsOutACopy(t *testing.T) {
	g := New()
	recipe := map[string]string{"user": "dev"}
	g.Observe(Observation{Observer: "root", Label: "hop", Fingerprints: []string{"SHA256:aaa"},
		Verified: true, Recipe: recipe})
	g.Observe(Observation{Observer: "hop", Label: "leaf", Recipe: map[string]string{"user": "far"},
		Reason: "the hop's recipe names ~/.ssh/hop-only, which is not here — copy it"})
	g.Allow(Budget{MaxDepth: 9, MaxPerObserver: 1}, "hop", 1, []string{"leaf", "other"})

	// The PRODUCER's own map first: a poll that reuses one map across ticks must not find the graph
	// holding it, because then next tick's write reaches a node nobody observed.
	recipe["user"] = "vandalised"
	if got := g.Nodes()[0].Recipe["user"]; got != "dev" {
		t.Errorf("the graph aliases the observation's own recipe map: it now reads %q", got)
	}

	e := g.Edges()
	e[0].Recipe["user"] = "vandalised"
	if got := g.Edges()[0].Recipe["user"]; got == "vandalised" {
		t.Error("a caller's write reached the graph's edge recipe — Edges is documented as a snapshot")
	}
	c := g.Candidates()
	c[0].Recipe["user"] = "vandalised"
	if got := g.Candidates()[0].Recipe["user"]; got == "vandalised" {
		t.Error("a caller's write reached a candidate's recipe — Candidates is documented as a snapshot")
	}
	cuts := g.Cuts()
	if len(cuts) != 1 {
		t.Fatalf("the budget cut one alias and reported %d cuts", len(cuts))
	}
	cuts[0].Why = "vandalised"
	if got := g.Cuts()[0].Why; got == "vandalised" {
		t.Error("a caller's write reached the graph's cut report — Cuts is documented as a snapshot")
	}
}

// Each state's remedy names ITS OWN next move, and each state prints its own word. Swapping the
// `Ready` and `Available` sentences is invisible to a test that only asks whether a reason is
// non-empty — and those two sentences are different instructions: one says add this host to
// hosts.toml, the other says it is already there. That is the "state naming another state's
// mechanism" class this repository has shipped once.
func TestEachStatesRemedyNamesItsOwnNextMove(t *testing.T) {
	for _, c := range []struct {
		state  State
		word   string
		needle string
	}{
		{Candidate, "candidate", "did not answer"},
		{Blocked, "blocked", "needs a credential"},
		{Ready, "ready", "not in hosts.toml"},
		{Available, "available", "verified and not polled"},
	} {
		if got := c.state.String(); got != c.word {
			t.Errorf("state %d prints %q, want %q", int(c.state), got, c.word)
		}
		g := New()
		o := Observation{Observer: "root", Label: "leaf", State: c.state}
		if c.state.node() {
			o.Fingerprints, o.Verified = []string{"SHA256:leaf"}, true
		}
		g.Observe(o)
		var got string
		if c.state.node() {
			got = g.Nodes()[0].Reason
		} else {
			got = g.Candidates()[0].Reason
		}
		if !strings.Contains(got, c.needle) {
			t.Errorf("%s's remedy is %q, which does not say %q — a remedy belonging to another "+
				"state names an act that does not apply here", c.word, got, c.needle)
		}
		if !strings.Contains(got, "leaf") {
			t.Errorf("%s's remedy %q does not name the machine it is about", c.word, got)
		}
	}
	if got := Mounted.String(); got != "mounted" {
		t.Errorf("Mounted prints %q, want %q", got, "mounted")
	}
}

// "No fingerprint, no node, whatever the flag says" is the half of invariant 3 this package CAN
// enforce, and it is also all that stands between a producer bug and an index out of range: verify()
// names its edge target `o.Fingerprints[0]`.
func TestAVerifiedFlagWithNoFingerprintMakesNoNode(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "root", Label: "leaf", Verified: true})
	if got := len(g.Nodes()); got != 0 {
		t.Errorf("a handshake that reported no host key made %d nodes — only a fingerprint makes one", got)
	}
	c := g.Candidates()
	if len(c) != 1 || c[0].State != Candidate || c[0].Reason == "" {
		t.Fatalf("want one candidate carrying a reason, got %+v", c)
	}
}

// An alias is per-observer vocabulary, so the root verifying its OWN `leaf` says nothing about the
// machine a hop calls `leaf`. Dropping the candidate on the alias alone would hide a different
// machine, in the direction that looks like success.
func TestVerifyingOneObserversAliasLeavesAnothersCandidate(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "hop", Label: "leaf",
		Reason: "the hop's recipe names a key that lives on the hop — copy it here"})
	g.Observe(Observation{Observer: "root", Label: "leaf",
		Fingerprints: []string{"SHA256:root-leaf"}, Verified: true})
	c := g.Candidates()
	if len(c) != 1 || c[0].Observer != "hop" {
		t.Errorf("the hop's own declaration of `leaf` was dropped by the root's verification: %+v", c)
	}
}

// A merge keeps the absorbed node's FACTS, not only its fingerprints. Two identities held apart have
// just been shown to be one machine, and a fact the fold dropped is a fact the graph measured and
// then forgot — while a fingerprint it dropped would resolve to no node at all.
func TestAMergeKeepsTheAbsorbedNodesFacts(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "root", Label: "one",
		Fingerprints: []string{"SHA256:aaa"}, Verified: true})
	g.Observe(Observation{Observer: "root", Label: "two",
		Fingerprints: []string{"SHA256:bbb", "SHA256:ccc"}, Verified: true,
		Hostname: "leaf.internal", Shell: ShellPOSIX, TmuxVersion: "3.2a", UID: "501"})
	// The bridge carries identity and NOTHING else, so every fact on the merged node below arrived
	// through the fold rather than through this observation.
	g.Observe(Observation{Observer: "root", Label: "one",
		Fingerprints: []string{"SHA256:aaa", "SHA256:bbb"}, Verified: true})
	if got := len(g.Nodes()); got != 1 {
		t.Fatalf("the bridge left %d nodes, want 1", got)
	}
	n := g.Nodes()[0]
	if len(n.Fingerprints) != 3 {
		t.Errorf("the merged identity is %v, want all three keys", n.Fingerprints)
	}
	if n.Hostname != "leaf.internal" || n.TmuxVersion != "3.2a" || n.UID != "501" || n.Shell != ShellPOSIX {
		t.Errorf("the fold lost the absorbed node's facts: hostname=%q tmux=%q uid=%q shell=%v",
			n.Hostname, n.TmuxVersion, n.UID, n.Shell)
	}
	// And the identity the fold absorbed resolves to the merged node, rather than making a third.
	g.Observe(Observation{Observer: "root", Label: "two",
		Fingerprints: []string{"SHA256:ccc"}, Verified: true})
	if got := len(g.Nodes()); got != 1 {
		t.Errorf("re-observing the absorbed fingerprint gave %d nodes — the fold dropped it", got)
	}
}

// An edge's target, once a handshake has named it, survives a probe that carries no fingerprint.
// Silence means "this probe learned nothing", which is the same rule the node's own facts follow — and
// a blanked target would make Walk stop following an edge to a machine the root HAS spoken to, so the
// traversal would quietly get smaller every time a poll failed.
func TestAFailedProbeDoesNotBlankAKnownEdgesTarget(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "root", Label: "hop",
		Fingerprints: []string{"SHA256:aaa"}, Verified: true})
	g.Observe(Observation{Observer: "root", Label: "hop", Reason: "the master is down — respawn it"})
	e := g.Edges()
	if len(e) != 1 {
		t.Fatalf("got %d edges, want 1", len(e))
	}
	if e[0].To != "SHA256:aaa" {
		t.Errorf("the edge's target is %q, want the identity the handshake named", e[0].To)
	}
}
