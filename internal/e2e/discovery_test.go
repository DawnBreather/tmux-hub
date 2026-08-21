//go:build e2e

package e2e

// Transitive discovery, against the declared topology.
//
// Every case here runs the PRODUCTION seams — hostset.Probe, hostset.RemoteCandidates, fleet.Graph,
// fleet.Diagnose and the picker's own renderer — against real sshd on real machines with real host
// keys and real per-edge delay. Nothing is faked but the transport's two end points, which are
// `docker compose exec` instead of a local `ssh` binary, and that substitution is what lets the ROOT
// of the topology be a container rather than this workstation.
//
// The oracle is spec §5: the derived graph equals the declared topology minus policy, with a reason
// per exclusion. Point 4 — every candidate carries a non-empty reason naming a remedy — is the one a
// passing suite can hide, because a reason string is easy to produce and hard to keep true, so it is
// asserted in every case that produces a row rather than in one case of its own.
//
// Why not the hub BINARY: the machines live on container networks the workstation is not on, so the
// hub could not reach the hop at all — `Behind` is a port precisely so the thing on the other side of
// it can be swapped, and here it is swapped for one that speaks over `docker compose exec`. What the
// binary's own path is worth is settled by the unit frame at 80 columns and by the wiring guard, and
// what THIS file is worth is that every string below came out of a real handshake.

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/fleet"
	"github.com/DawnBreather/tmux-hub/internal/fleetcache"
	"github.com/DawnBreather/tmux-hub/internal/hostset"
	"github.com/DawnBreather/tmux-hub/internal/ui"
)

// discoveryProbeTimeout is generous because the declared topology puts 180 ms on one edge and an ssh
// handshake is several round trips: the figure has to be about the fixture rather than about this
// machine's load, and a probe that timed out would be reported as a THIRD outcome (slow, not absent)
// that no case here is about.
const discoveryProbeTimeout = 30 * time.Second

// streams runs a command in a container and keeps stdout and stderr APART.
//
// `harness.exec` merges them with CombinedOutput, and merging is fatal for everything here:
// hostset.Probe reads the tmux version off stdout and the machine's identity off stderr, and
// hostset.RemoteCandidates tells "this hop has no ssh config" from "this hop could not be reached"
// by whether `cat`'s complaint is on stderr while stdout is empty. Merged, a `debug1:` line lands in
// the version parse and a `cat:` complaint reads as the config's contents.
func streams(t *testing.T, h *harness, machine, user string, argv ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	full := append([]string{"compose", "-f", h.compose, "exec", "-T", "-u", user, machine}, argv...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	rc := 0
	if err != nil {
		rc = cmd.ProcessState.ExitCode()
		if rc < 0 {
			t.Fatalf("docker compose exec %s %v did not run at all: %v: %s", machine, argv, err, errb.String())
		}
	}
	return out.String(), errb.String(), rc
}

// rootProbe is the production hostset.Runner, with the root CONTAINER standing in for this
// workstation. The argv is the one cmd/tmux-hub builds — `-v` for the identity, BatchMode so a
// password prompt nobody can see cannot hang the round, and a ConnectTimeout.
func rootProbe(t *testing.T, h *harness) hostset.Runner {
	return func(_ context.Context, alias string, args ...string) (string, string, int) {
		argv := append([]string{"ssh", "-v", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", alias}, args...)
		return streams(t, h, "root", "dev", argv...)
	}
}

// rootRemote is the production hostset.RemoteRunner, again from the root container: no `-v` (this
// connection harvests no identity and its transcript would land in a reason), no ConnectTimeout of
// its own, and the payload as a POSITIONAL argument so it reaches the far account's LOGIN shell —
// which is the whole reason hostset's payloads leave the program name bare.
func rootRemote(t *testing.T, h *harness) hostset.RemoteRunner {
	return func(_ context.Context, hop, payload string) (string, string, int) {
		return streams(t, h, "root", "dev", "ssh", "-o", "BatchMode=yes", hop, payload)
	}
}

// observeProbe probes one alias from the root and folds the answer in exactly as production does.
//
// The fingerprint is attributed to the machine ONLY for a direct recipe. Fleet spec §2.2.1, measured:
// a proxied connection reports the JUMP host's key at every verbosity, so under §2.3's
// set-intersection merging the destination would inherit the jump's identity and two machines would
// collapse into one node. Every edge in `basic.toml` is direct, so this is the honest path here and
// the guard is on the recipe rather than on the topology's say-so.
func observeProbe(t *testing.T, h *harness, g *fleet.Graph, alias string) hostset.Result {
	t.Helper()
	r := hostset.Probe(context.Background(), alias, discoveryProbeTimeout, rootProbe(t, h))
	g.Observe(fleet.Observation{
		Observer:     "", // the root's own vocabulary: hostset.Candidate.Via is empty for it
		Label:        alias,
		Fingerprints: r.Fingerprints,
		Verified:     len(r.Fingerprints) > 0,
		TmuxVersion:  r.Version,
		Sample:       r.Took,
		Reason:       r.Reason,
	})
	return r
}

// STEP 1a: the root probes the hop and the probe's OWN handshake carries the identity.
//
// The fingerprint costs no round trip — it is a by-product of the connection the picker was making
// anyway — and it must equal what the machine itself holds, which `ssh-keygen -lf` answers on the
// far side. Two programs, one answer; a parse that agreed only with itself would prove nothing.
func TestDiscoveryTheRootProbeHarvestsTheHopsOwnFingerprint(t *testing.T) {
	f := up(t, "basic")
	r := hostset.Probe(context.Background(), "hop", discoveryProbeTimeout, rootProbe(t, f))
	t.Logf("hop answered usable=%v version=%q took=%s fingerprints=%v",
		r.Usable, r.Version, r.Took.Round(time.Millisecond), r.Fingerprints)

	if len(r.Fingerprints) != 1 {
		t.Fatalf("the probe harvested %d fingerprints, want exactly 1: %v — `-v` is what puts the "+
			"server's host key on stderr, and an empty set reads as a handshake that never completed",
			len(r.Fingerprints), r.Fingerprints)
	}
	if own := f.Fingerprint(t, "hop"); r.Fingerprints[0] != own {
		t.Errorf("the probe read %s and the machine holds %s", r.Fingerprints[0], own)
	}
	// The hop runs Nushell as its login shell, and the payload is written to survive that: a QUOTED
	// program name is a parse error there AT rc=0, which is how a real host stayed invisible.
	if !r.Usable || r.Version != f.machine("hop").Tmux {
		t.Errorf("the hop answered usable=%v version=%q; the topology declares tmux %q — the probe "+
			"payload did not survive a non-POSIX login shell", r.Usable, r.Version, f.machine("hop").Tmux)
	}
}

// STEP 1b: the hop's own ssh config is ENUMERATED and each alias RESOLVED on the hop, `leaf` comes
// back diagnosed `Blocked`, and the remedy reaches the picker's screen.
//
// This is the whole path in one case, because that is what the plan asks for: what the operator ends
// up reading is a sentence composed from a file on another machine, and every layer between here and
// there is real.
func TestDiscoveryTheHopsConfigIsEnumeratedAndTheLeafIsBlockedWithARemedy(t *testing.T) {
	f := up(t, "basic")

	cands, err := hostset.RemoteCandidates(context.Background(), rootRemote(t, f), "hop")
	if err != nil {
		t.Fatalf("RemoteCandidates on the hop: %v", err)
	}
	for _, c := range cands {
		t.Logf("hop declares %q via=%q skip=%q recipe=%v", c.Alias, c.Via, c.Skip, c.Recipe)
	}
	if len(cands) == 0 {
		t.Fatal("the hop declared nothing; the topology says it declares leaf and hop")
	}

	var leaf *hostset.Candidate
	for i := range cands {
		if cands[i].Alias == "leaf" {
			leaf = &cands[i]
		}
	}
	if leaf == nil {
		t.Fatalf("`leaf` is absent from what the hop declares: %+v", cands)
	}
	if leaf.Via != "hop" {
		t.Errorf("leaf came back via %q, want the hop that declared it", leaf.Via)
	}
	// The RESOLVED recipe, from `ssh -G` run ON the hop — which is the only place the hop's own
	// stanza exists. The topology gives that edge the hop's key and not the root's.
	if got := leaf.Recipe["identityfile"]; !strings.Contains(got, "hop-only") {
		t.Errorf("the resolved recipe names identityfile %q, and the topology gives that edge the "+
			"hop's own key — without it there is nothing for the diagnosis to be about", got)
	}

	// The diagnosis, with the predicate the caller owns: nothing the hop names is on the machine
	// running this test, and the `~` in ssh's answer is expanded by the caller because `ssh -G`
	// does not expand it.
	state, reason := fleet.Diagnose(leaf.Recipe, func(string) bool { return false })
	t.Logf("leaf diagnosed %v: %s", state, reason)
	if state != fleet.Blocked {
		t.Fatalf("leaf is %v, want Blocked — its recipe names a credential the root does not hold", state)
	}
	for _, want := range []string{"hop-only", "ssh-copy-id"} {
		if !strings.Contains(reason, want) {
			t.Errorf("the reason %q does not name %q — a reason without a remedy is a complaint", reason, want)
		}
	}
	if strings.Contains(reason, "unreachable") {
		t.Error("`unreachable` is not a reason (spec §3.2 invariant 4): it names no act")
	}

	// And onto the SCREEN, at the 80 columns §16 commits to, through the same renderer the hub
	// paints with. A remedy nobody can read is not a remedy.
	rows := discoveredFrom(t, f, cands)
	drawn := strings.Join(ui.RenderDiscovered(rows, 80, 24), "\n")
	t.Logf("the picker's discovered section at 80 columns:\n%s", drawn)
	for _, want := range []string{"leaf", "hop", "blocked", "ssh-copy-id", "hop-only"} {
		if !strings.Contains(drawn, want) {
			t.Errorf("the drawn section does not carry %q:\n%s", want, drawn)
		}
	}
}

// discoveredFrom folds one hop's declarations into a graph and derives the picker's rows, going
// through ui.DiscoveredRowsFor rather than building rows by hand — a row whose state and reason the
// test assigned would assert only that assignment works.
func discoveredFrom(t *testing.T, f *harness, cands []hostset.Candidate) []ui.DiscoveredRow {
	t.Helper()
	g := fleet.New()
	for _, c := range cands {
		o := fleet.Observation{
			Observer: "hop", Label: c.Alias, Hostname: c.Recipe["hostname"],
			Recipe: c.Recipe, Reason: c.Skip,
		}
		if c.Skip == "" {
			o.State, o.Reason = fleet.Diagnose(c.Recipe, func(string) bool { return false })
		}
		g.Observe(o)
	}
	// The hop's own measured round trip is what bounds everything behind it, so the cache is keyed
	// on the root's alias for the hop — the same key production writes from its probe round.
	took := hostset.Probe(context.Background(), "hop", discoveryProbeTimeout, rootProbe(t, f)).Took
	facts := func(k fleetcache.Key) (fleetcache.Facts, bool) {
		if k.Alias == "hop" {
			return fleetcache.Facts{RTT: took}, true
		}
		return fleetcache.Facts{}, false
	}
	return ui.DiscoveredRowsFor(g.Nodes(), g.Candidates(), facts)
}

// STEP 2: two aliases for ONE machine fold to one node with two labels.
//
// Identity is a SET and observations MERGE (spec §2.3), and this is the case that could only be
// verified at hop 1 — which is why the topology puts `hop` and `hop-again` on the root's own network.
// A fixture with a baked host key would pass it by collapsing the whole fleet, so it is read here
// from two separate real handshakes.
func TestDiscoveryTwoAliasesForTheHopFoldToOneNodeWithTwoLabels(t *testing.T) {
	f := up(t, "basic")
	g := fleet.New()
	first := observeProbe(t, f, g, "hop")
	again := observeProbe(t, f, g, "hop-again")
	t.Logf("hop reported %v; hop-again reported %v", first.Fingerprints, again.Fingerprints)

	nodes := g.Nodes()
	if len(nodes) != 1 {
		t.Fatalf("two aliases for one machine gave %d nodes: %+v", len(nodes), nodes)
	}
	if got := len(nodes[0].Labels); got != 2 {
		t.Errorf("the node carries %d labels, want 2 — a merge must keep both names: %+v",
			got, nodes[0].Labels)
	}
	// The positive half of the merge, so "one node" cannot be satisfied by a probe that harvested
	// nothing at all: the identity set has to hold the fingerprint both handshakes reported.
	if len(nodes[0].Fingerprints) != 1 || nodes[0].Fingerprints[0] != f.Fingerprint(t, "hop") {
		t.Errorf("the merged node's identity is %v and the machine holds %s",
			nodes[0].Fingerprints, f.Fingerprint(t, "hop"))
	}
}

// STEP 3: two DIFFERENT machines under ONE hostname stay two nodes.
//
// Four peers answer to `web-ws` on the operator's own tailnet, which is why neither an alias nor a
// hostname can be the key. Both twins are on the root's network, so both really verify.
func TestDiscoveryTheTwinsStayTwoNodesUnderOneHostname(t *testing.T) {
	f := up(t, "basic")
	g := fleet.New()
	a := observeProbe(t, f, g, "twin-a")
	b := observeProbe(t, f, g, "twin-b")
	t.Logf("twin-a reported %v; twin-b reported %v", a.Fingerprints, b.Fingerprints)

	if len(g.Nodes()) != 2 {
		t.Fatalf("one hostname over two machines gave %d nodes: %+v", len(g.Nodes()), g.Nodes())
	}
	// The label really does collide in the running fleet — asserted as EQUALITY against the declared
	// hostname, because `Contains(out, "twin")` is satisfied by `twin-a` and `twin-b`, which is
	// exactly what a container reports when the collision is GONE.
	label := f.machine("twin-a").Hostname
	if label == "" || label != f.machine("twin-b").Hostname {
		t.Fatalf("the twins declare hostnames %q and %q; the fixture no longer collides",
			label, f.machine("twin-b").Hostname)
	}
	for _, m := range []string{"twin-a", "twin-b"} {
		out, rc := f.Exec(t, m, "hostname")
		if rc != 0 || strings.TrimSpace(out) != label {
			t.Errorf("%s calls itself %q (rc=%d) and the topology declares %q", m, strings.TrimSpace(out), rc, label)
		}
	}
}

// STEP 4: the cycle terminates, and each node contributes its edges once (spec §5 point 5).
//
// The topology has the hop declare `hop`, so the crawl walks back to a machine it has already seen.
// It terminates with no special case because what is marked visited is the IDENTITY and not the alias
// that led there — remove the visited check and this case does not finish.
func TestDiscoveryTheCycleTerminates(t *testing.T) {
	f := up(t, "basic")
	g := fleet.New()
	observeProbe(t, f, g, "hop")

	// The hop's own declaration of `hop` — a cycle in the topology — read from the hop's real config.
	cands, err := hostset.RemoteCandidates(context.Background(), rootRemote(t, f), "hop")
	if err != nil {
		t.Fatalf("RemoteCandidates on the hop: %v", err)
	}
	own := f.Fingerprint(t, "hop")
	var declaresItself bool
	for _, c := range cands {
		o := fleet.Observation{Observer: "hop", Label: c.Alias, Recipe: c.Recipe, Reason: c.Skip}
		if c.Alias == "hop" {
			declaresItself = true
			// THE EDGE HAS TO LAND ON A NODE, or the walk has no cycle to terminate — and the first
			// cut of this case had exactly that hole: it observed the hop's self-declaration
			// unverified, so the edge's target was empty, Walk skipped it, and "1 of 1 nodes visited"
			// passed against a traversal that could not loop.
			//
			// Attributing the root's own fingerprint here is sound rather than convenient: it IS the
			// same machine, and the ROOT completed that handshake itself under its own alias for it
			// (asserted above), which is invariant 3 satisfied and §2.3's set-intersection merge doing
			// the work it exists for — one machine, two observers' names for it, one node.
			o.Fingerprints, o.Verified = []string{own}, true
		}
		g.Observe(o)
	}
	if !declaresItself {
		t.Fatal("the hop does not declare itself, so there is no cycle in the running fleet and this " +
			"case would pass against a crawl that cannot terminate")
	}
	// One node with two observers' labels for it, and an edge from `hop` back to it: that pair IS the
	// cycle, and printing it is what says the walk below has something to terminate.
	if n := g.Nodes(); len(n) != 1 || len(n[0].Labels) != 2 {
		t.Fatalf("the fixture built %d nodes with labels %v; the cycle needs one node the hop's own "+
			"edge lands on", len(n), n)
	}
	var loops bool
	for _, e := range g.Edges() {
		if e.Observer == "hop" && e.Alias == "hop" && e.To != "" {
			loops = true
		}
	}
	if !loops {
		t.Fatal("no edge leaves `hop` for a machine the graph holds, so the walk has no cycle in it")
	}
	t.Logf("the graph holds 1 node under %d labels with a self-edge from hop", len(g.Nodes()[0].Labels))

	// The walk visits identities. A `done` channel bounds it, because a non-terminating walk would
	// otherwise hang the whole suite rather than fail this case.
	done := make(chan int, 1)
	go func() {
		seen := 0
		g.Walk(func(fleet.Node) { seen++ })
		done <- seen
	}()
	select {
	case seen := <-done:
		t.Logf("the walk visited %d of %d nodes over a fleet whose hop declares itself", seen, len(g.Nodes()))
		if seen != len(g.Nodes()) {
			t.Errorf("the walk visited %d of %d nodes — a cycle must not revisit and must not skip",
				seen, len(g.Nodes()))
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the walk did not finish over a fleet with a cycle in it")
	}
}

// STEP 5: stop the hop mid-run, and the leaf's reason names THE HOP rather than a tunnel.
//
// Attribution is the point. This repository has already shipped a status naming a mechanism the
// design did not contain, and the far machine's row is where that mistake is cheapest to make: the
// root never reaches the leaf either way, so a reason about the root's own transport would be true of
// a working fleet too.
func TestDiscoveryAStoppedHopIsNamedByWhatIsBehindIt(t *testing.T) {
	f := up(t, "basic")
	// It is stopped rather than paused so the failure is a refused connection with words on stderr,
	// which is what a hop that has gone away really looks like.
	if out, err := exec.Command("docker", "compose", "-f", f.compose, "stop", "hop").CombinedOutput(); err != nil {
		t.Fatalf("could not stop the hop: %v: %s", err, out)
	}

	_, err := hostset.RemoteCandidates(context.Background(), rootRemote(t, f), "hop")
	if err == nil {
		t.Fatal("reading a stopped hop's config succeeded — then a hop we could not ask is " +
			"indistinguishable from a hop with nothing to offer, which is the silent horizon " +
			"invariant 4 forbids")
	}
	t.Logf("a stopped hop answered: %v", err)
	if !strings.Contains(err.Error(), "hop") {
		t.Errorf("the failure %q does not name the hop it is about; a crawl reports several hops and "+
			"an unnamed one is not actionable", err)
	}

	for _, forbidden := range []string{"tunnel", "unreachable"} {
		if strings.Contains(strings.ToLower(err.Error()), forbidden) {
			t.Errorf("the failure %q names %q — a mechanism this design does not have, or a word that "+
				"names no act", err, forbidden)
		}
	}

	// And nothing is INVENTED in its place. A hop that could not be asked must contribute no rows at
	// all — a row for `leaf` here would be a machine the hub is claiming to know about on the strength
	// of a crawl that failed, which is worse than the omission because the operator would act on it.
	//
	// The note the screen shows for this is asserted in internal/ui, against the real model and the
	// real crawl (TestACrawlThatCouldNotAskAHopSaysWhichHop): from here the model is unexported, and
	// exporting a function so a test could reach it would be a surface the product does not need.
	if rows := ui.DiscoveredRowsFor(nil, nil, nil); len(rows) != 0 {
		t.Errorf("an empty graph produced %d discovered rows: %+v", len(rows), rows)
	}
}

// STEP 6: a budget cut is REPORTED with its count, over the aliases a real hop really declares.
//
// The cap this fleet ships is 32 and no hop here declares that many, so the cut is provoked with a
// smaller budget rather than with a fabricated config: the labels are the ones the hop's own file
// names, and the arithmetic is the graph's own.
func TestDiscoveryABudgetCutIsReportedWithItsCount(t *testing.T) {
	f := up(t, "basic")
	cands, err := hostset.RemoteCandidates(context.Background(), rootRemote(t, f), "hop")
	if err != nil {
		t.Fatalf("RemoteCandidates on the hop: %v", err)
	}
	var labels []string
	for _, c := range cands {
		labels = append(labels, c.Alias)
	}
	if len(labels) < 2 {
		t.Fatalf("the hop declares %v; a cut needs at least two aliases to have a tail", labels)
	}

	g := fleet.New()
	allowed := g.Allow(fleet.Budget{MaxDepth: 1, MaxPerObserver: 1}, "hop", 1, labels)
	cuts := g.Cuts()
	t.Logf("the hop declares %v; a breadth of 1 allowed %v and filed %d cuts", labels, allowed, len(cuts))
	if len(cuts) != 1 {
		t.Fatalf("%d cuts filed for %d skipped aliases", len(cuts), len(labels)-1)
	}
	if cuts[0].Skipped != len(labels)-1 {
		t.Errorf("the cut says %d skipped, want %d", cuts[0].Skipped, len(labels)-1)
	}
	// Space-delimited, because a bare Contains would also match the totals the same sentence carries.
	if !strings.Contains(cuts[0].Why, " "+itoa(cuts[0].Skipped)+" ") {
		t.Errorf("the cut sentence carries no count: %q", cuts[0].Why)
	}
	for _, want := range []string{"hop", "raise"} {
		if !strings.Contains(cuts[0].Why, want) {
			t.Errorf("the cut sentence %q does not name %q — a horizon with no observer or no knob is "+
				"indistinguishable from a finished crawl", cuts[0].Why, want)
		}
	}
	if strings.Contains(cuts[0].Why, "unreachable") {
		t.Errorf("the cut sentence says `unreachable`: %q", cuts[0].Why)
	}
}

// itoa keeps this file free of an import for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
