// Package fleet holds the hub's fleet as a GRAPH: the machines it has spoken to, keyed on what they
// are rather than on what anybody calls them.
//
// Identity is the SET of ssh host-key fingerprints the machine presented, and two observations are
// the same machine when their sets INTERSECT. That rule is the reason this package exists as its own
// seam, and both of the failure modes it answers are measured rather than imagined:
//
//   - two machines legitimately share a NAME — four peers on the operator's own tailnet answer to
//     one hostname — so neither an alias nor a hostname can be the key;
//   - cloned machines legitimately share a host KEY, so equality of a single fingerprint cannot be
//     the key either, and a container fixture built from one image with a baked key would collapse a
//     whole fleet to one vertex.
//
// It is also the third surface in this repository to pay for the same lesson from the other end:
// the favourites pin and the project alias were both keyed on `(kind, host, name)` and both came off
// when the product renamed the thing under the operator. A fingerprint is not vocabulary; nothing
// the hub does renames it.
//
// Authority: docs/specs/2026-08-20-fleet-graph-and-harness-topology-design.md, cited below as "the
// fleet spec". Section numbers in this package are ITS numbers, not docs/design.md's.
//
// There is no I/O here, no clock and no network. A probe elsewhere produces an Observation; this
// package folds it in, and every decision the fold makes is made from that observation's own fields.
//
// A Graph is NOT safe for concurrent use. A caller that shares one between bubbletea's Update and a
// tea.Cmd must hold a lock and must ship the concurrent test that proves it in the same commit — an
// unguarded append to a shared slice has already cost this repository a whole poll's status writes,
// silently, with `go test -race` green throughout (CLAUDE.md).
package fleet

import "time"

// State is what the operator may do about a machine (fleet spec §3.4). Three states are nodes and
// two are candidates, which is invariant 3 — only the root's own completed handshake makes a node —
// made visible in the vocabulary.
//
// The constants run LEAST-privileged first so that the zero value claims nothing. The spec's table
// lists them in the other order and the plan's sketch wrote `Mounted State = iota`; neither fixes an
// integer value, and a zero value meaning `Mounted` would have every unfilled struct in every later
// part claim "declared, verified and polled". This repository has paid for that shape twice (an
// accessor returning 0, 0 while a mode was on, read as "nothing is hidden"), so the order is pinned
// by a test rather than by this comment.
type State int

const (
	// Candidate — declared somewhere, unverified, undiagnosed. The operator's next move is the
	// failure's own sentence.
	Candidate State = iota
	// Blocked — a hop declares it and its resolved recipe names a credential the root does not
	// hold. A DIAGNOSIS from the hop's stanza, never a verification: a fingerprint for a machine
	// the root cannot reach could only come from running ssh on the hop, which this part does not
	// do. The operator's next move is the printed remedy.
	Blocked
	// Ready — verified by the root, not yet declared by the root. One tick declares it.
	Ready
	// Available — declared and verified, not polled: over the latency budget, or unticked.
	Available
	// Mounted — declared by the root, verified, polled. The only state with no next move, and
	// therefore the only one that needs no remedy.
	Mounted
)

func (s State) String() string {
	switch s {
	case Blocked:
		return "blocked"
	case Ready:
		return "ready"
	case Available:
		return "available"
	case Mounted:
		return "mounted"
	default:
		return "candidate"
	}
}

// node reports whether this state belongs to a machine the root has spoken to. The split is the
// whole content of invariant 3, so it is one predicate rather than a list repeated at each caller.
func (s State) node() bool { return s >= Ready }

// ShellFamily is the login shell family of a machine.
//
// It is a field rather than a footnote because a non-POSIX login shell made a whole host invisible
// on 2026-08-20: ssh hands the remote command to the LOGIN shell, a quoted program name is legal
// POSIX and a parse error in Nushell, and that parse error comes back at **rc=0** — so the poll read
// as a host with no panes and the footer blamed an ssh master that was alive the whole time.
//
// The zero value is ShellOther, which is the safe direction the spec asks for (§3.1: a node whose
// family is unproven is `other`). Nothing here needs the distinction to be right in order to work —
// the payload is written to survive both families — so an unproven family costs nothing while a
// wrong `posix` costs a host.
type ShellFamily int

const (
	ShellOther ShellFamily = iota
	ShellPOSIX
)

func (f ShellFamily) String() string {
	if f == ShellPOSIX {
		return "posix"
	}
	return "other"
}

// Label is one observer's name for a machine. An alias is per-observer vocabulary — `web` here and
// `web` there are different machines — so a name never travels without the observer that used it.
type Label struct {
	Observer string
	Alias    string
}

// RootObserver is the observer name of the machine the hub runs on, and it is EMPTY.
//
// That is the producer's own convention rather than a choice made here: `hostset.Candidate.Via` is
// empty for the root's own declarations, because the root has no name for itself in this vocabulary
// and there is exactly one root (fleet spec §3.1).
//
// It is EXPORTED so that there is one of it. It briefly existed twice — here and as an unexported
// copy in `internal/ui` — which is this repository's signature defect in its smallest form: one fact
// in two places, and the second one drifts the day the first changes. This package is the right home
// because it owns the vocabulary and CANNOT import hostset (hostset imports it), while every other
// reader can import this.
//
// What it decides is not cosmetic. An alias is addressable from HERE only when the root itself
// declared it, so this is the predicate that says whether a remedy may put the alias in a command —
// see candidateReason.
const RootObserver = ""

// Node is a machine the root has spoken to (fleet spec §3.1).
type Node struct {
	// Fingerprints is the identity SET, in the order first seen. A newly observed fingerprint
	// joins it; it never forks the node (§2.3).
	Fingerprints []string
	// Labels is every alias any observer uses for this machine, with the observer.
	Labels []Label
	// Hostname is what a recipe resolved to. It does NOT identify: four peers on the operator's
	// tailnet answer to one hostname, which is why it is a field and not a key.
	Hostname string
	// Recipe is the resolved transport that reached it, as `ssh -G` reports it.
	Recipe map[string]string
	// Shell decides how a remote command may be quoted; see ShellFamily.
	Shell ShellFamily
	// TmuxVersion is what `tmux -V` answered, empty when it answered nothing. Every tmux claim in
	// docs/design.md is a per-version claim, so the version is a property of the machine.
	TmuxVersion string
	// UID is the remote uid the recipe lands as, empty when unknown. A string rather than an int
	// because uid 0 is root: an int could not tell "root" from "we never asked".
	UID string
	// Samples holds round-trip observations, newest first, bounded by maxSamples. A merge shares the
	// window between the identities it folded rather than appending one list to the other: a sample
	// carries no timestamp, so order is the only recency information there is, and a concatenation
	// would assert an ordering across the two lists that neither of them contains.
	Samples []time.Duration
	// State and Reason: every state but Mounted carries a remedy (§7 decision 6), and this
	// package refuses to store a node or candidate whose reason is silent.
	State  State
	Reason string
}

// Edge is "observer O reaches machine M by recipe R" (fleet spec §3.1). Directed and per-observer,
// because a recipe is only meaningful on the machine that holds the key it names.
type Edge struct {
	Observer string
	Alias    string
	Recipe   map[string]string
	// To is one fingerprint of the machine this edge lands on, and any member of the set
	// identifies the node because two intersecting sets ARE one node. It is empty while the far
	// machine is only a candidate, which is what makes an edge's target absent rather than wrong.
	To string
}

// Unverified is a machine some observer declares that the root has not verified: no completed
// handshake, therefore no identity, therefore no node (invariant 3).
//
// The fleet spec calls this noun a "candidate", and so does the accessor that returns them. The TYPE
// cannot carry that word because `Candidate` is the State these rows default to, and one identifier
// cannot be both. Two spellings of one noun, not two nouns — do not "tidy" this into a second
// vocabulary.
type Unverified struct {
	Observer string
	Alias    string
	Hostname string
	Recipe   map[string]string
	// State is Candidate or Blocked. Nothing else is reachable: a node state on a row nobody
	// verified is a contradiction, and Observe refuses it rather than storing it.
	State  State
	Reason string
}

// Observation is what one probe learned. A zero field means "this probe learned nothing about it",
// never "this fact is now absent": the producers differ in what they measure — the picker's probe
// answers `tmux -V; id -u`, a candidate-resolution pass answers `ssh -G` and nothing else — so a
// fold that let a silent field erase a measured one would lose facts as a function of which producer
// spoke last.
type Observation struct {
	// Observer is the machine whose ssh config named this one; Label is that observer's alias for
	// it. The root observer is the machine the hub runs on, and there is exactly one root.
	Observer string
	Label    string
	Hostname string
	// Fingerprints is what the handshake reported. Empty means no handshake completed.
	Fingerprints []string
	// Verified is true only when a handshake COMPLETED and these fingerprints came out of it.
	// Invariant 3 — only the ROOT's own handshake creates a node — has a half this package cannot
	// check: it cannot tell whose ssh ran. The producer owns that half, and this flag is the seam
	// where it is asserted. What the graph does enforce is mechanical and total: no fingerprint,
	// no node, whatever the flag says.
	Verified bool
	// Reason is why this machine is not mountable, in a sentence naming a remedy. `unreachable`
	// is not a reason (invariant 4). When a producer names none, the graph supplies one rather
	// than storing the silence.
	Reason string
	Recipe map[string]string
	Shell  ShellFamily
	// TmuxVersion, UID: see the same fields on Node.
	TmuxVersion string
	UID         string
	// Sample is this probe's own round trip, if it timed one.
	Sample time.Duration
	// State is what a DIAGNOSIS decided (fleet.Diagnose, a later task). Leave it zero and the row
	// reads Candidate — declared, unverified, undiagnosed — which is what an undiagnosed
	// declaration is. Observe refuses a state that contradicts Verified in either direction.
	State State
}

// maxSamples bounds the round-trip window a node keeps.
//
// The bound is not a policy threshold — the latency budget and its buckets belong to a later part —
// it exists because the fold has no other bound: the hub polls about four times a second for as long
// as the operator leaves it open, so an unbounded slice reaches six figures per node in one working
// day. Eight is long enough for a middle value to mean something and short enough that a machine
// which has become slow is not defended by an hour-old fast sample. A later part that needs a
// different window changes this constant and nothing else.
const maxSamples = 8

// Graph is the derived fleet.
//
// Nodes live in a slice in the order first seen, and a fingerprint is resolved by scanning it. There
// is deliberately no index: a second structure keyed on the same fingerprints would have to be kept
// consistent through every merge, and this repository's costliest defects are two copies of one rule
// drifting apart. The fleet is tens of machines with a few keys each, folded a few times a second,
// so the scan is not measurable against a frame.
type Graph struct {
	nodes []*Node
	edges []Edge
	cands []Unverified
	cuts  []Cut
}

// New returns an empty graph.
func New() *Graph { return &Graph{} }

// Observe folds one observation into the graph.
//
// A verified observation creates or EXTENDS a node: it is matched against every node whose identity
// set it intersects, and when it intersects more than one those nodes are merged, because a machine
// that presented `aaa` on one call and `bbb` on another was always one machine. An unverified one
// becomes a candidate keyed by (observer, alias), which is the same declaration seen again rather
// than a new row.
func (g *Graph) Observe(o Observation) {
	if o.Verified && len(o.Fingerprints) > 0 {
		g.verify(o)
		return
	}
	g.declare(o)
}

// verify folds an observation the root's own handshake produced.
func (g *Graph) verify(o Observation) {
	n := g.absorb(o.Fingerprints)
	if n == nil {
		n = &Node{}
		g.nodes = append(g.nodes, n)
	}
	for _, fp := range o.Fingerprints {
		n.addFingerprint(fp)
	}
	n.addLabel(Label{Observer: o.Observer, Alias: o.Label})

	// Facts: what the observation carries replaces what we knew; what it does not carry is left
	// alone. See Observation's own comment for why silence cannot erase.
	if o.Hostname != "" {
		n.Hostname = o.Hostname
	}
	if len(o.Recipe) > 0 {
		n.Recipe = cloneMap(o.Recipe)
	}
	if o.Shell != ShellOther {
		n.Shell = o.Shell
	}
	if o.TmuxVersion != "" {
		n.TmuxVersion = o.TmuxVersion
	}
	if o.UID != "" {
		n.UID = o.UID
	}
	if o.Sample > 0 {
		n.Samples = trim(append([]time.Duration{o.Sample}, n.Samples...))
	}

	n.State, n.Reason = stateOf(o)
	// The machine is a node now, so it is not also a row waiting to be diagnosed. Leaving the
	// candidate behind would draw one machine twice — the defect this repository has been reported
	// three times, each time as "I think I am seeing duplicates".
	g.dropCandidate(o.Observer, o.Label)
	g.record(Edge{Observer: o.Observer, Alias: o.Label, Recipe: cloneMap(o.Recipe), To: o.Fingerprints[0]})
}

// declare folds an observation with no completed handshake behind it.
func (g *Graph) declare(o Observation) {
	// The edge is recorded whatever becomes of the row: an observer named an alias, which is what
	// an edge IS, and its target is simply absent while nobody has shaken hands with it.
	defer g.record(Edge{Observer: o.Observer, Alias: o.Label, Recipe: cloneMap(o.Recipe)})
	// A machine the root has ALREADY spoken to does not become a candidate again because one probe
	// failed: the handshake happened, so the identity stands, and what changed is only what the hub
	// can say about it. Liveness is not one of the five states — the poller owns that — so the
	// reason lands on the node and no second row is created. This is the mirror of the drop in
	// verify(), and a rule that can be got wrong in two directions has already been got wrong in
	// both.
	//
	// What lands is the FAILURE's own sentence, and only that. The candidate reason `stateOf` would
	// synthesise below is the next move for `Candidate` — a machine nobody has shaken hands with —
	// so on a node it would name an act that does not apply, and on a Mounted node it would invent a
	// remedy where §3.4 says there is no next move. A probe that names nothing has learned nothing
	// this graph can phrase, and overwriting a true remedy with an invented one is the state/reason
	// mismatch every other path here refuses.
	//
	// Invariant 4 is not weakened by staying quiet, because verify() is the only maker of a node and
	// stateOf gives every state but Mounted a reason there: a node reached here with no remedy and no
	// Mounted state cannot exist, so a fallback for it would be a branch nothing can enter.
	if n := g.nodeLabelled(o.Observer, o.Label); n != nil {
		if o.Reason != "" {
			n.Reason = o.Reason
		}
		return
	}
	state, reason := stateOf(o)
	u := Unverified{
		Observer: o.Observer,
		Alias:    o.Label,
		Hostname: o.Hostname,
		Recipe:   cloneMap(o.Recipe),
		State:    state,
		Reason:   reason,
	}
	for i, have := range g.cands {
		if have.Observer == u.Observer && have.Alias == u.Alias {
			g.cands[i] = u
			return
		}
	}
	g.cands = append(g.cands, u)
}

// stateOf decides the state and the reason together, because the two are one statement: a reason is
// the remedy for a state, and a state whose reason belongs to a different state is the defect this
// repository has already shipped once — a status naming a mechanism the design did not contain.
//
// It also refuses the two contradictions the vocabulary allows to be written down. A handshake
// completed, so `Candidate` and `Blocked` are both refuted — and so is whatever remedy justified
// them, which is why the reason is dropped with the state rather than surviving it. In the other
// direction, nothing anybody merely declared may claim a node state.
func stateOf(o Observation) (State, string) {
	verified := o.Verified && len(o.Fingerprints) > 0
	state, reason := o.State, o.Reason
	switch {
	case verified && !state.node():
		state, reason = Ready, ""
	case !verified && state.node():
		state = Candidate
	}
	if reason == "" && state != Mounted {
		reason = defaultReason(state, o)
	}
	return state, reason
}

// defaultReason is what the graph says when a producer named no reason. Invariant 4 — every
// candidate and every unmounted node carries a reason that names a remedy — is enforced here rather
// than asserted in a comment, because a graph that silently omits is indistinguishable from a graph
// that finished. None of these sentences says `unreachable`, which invariant 4 forbids: it names no
// act the operator can perform.
//
// It takes the whole observation and not the alias alone, because a REMEDY IS ONLY A REMEDY IF THE
// OPERATOR CAN TYPE IT HERE, and whether the alias may appear in a command depends on who declared
// it — see candidateReason. The three sentences that need nothing but the alias still take nothing
// but the alias.
func defaultReason(s State, o Observation) string {
	switch s {
	case Blocked:
		return o.Label + " needs a credential this machine does not hold — resolve it on the hop with `ssh -G " + o.Label + "` to see which key it names"
	case Ready:
		return o.Label + " answered but is not in hosts.toml — tick it to mount it"
	case Available:
		return o.Label + " is verified and not polled — tick it to mount it"
	default:
		return candidateReason(o)
	}
}

// candidateReason is `Candidate`'s next move: §3.4 calls it "the failure's own sentence", and this is
// what the graph says when nothing supplied one.
//
// THE ALIAS IS NOT ALWAYS ADDRESSABLE FROM HERE, and that is the whole content of this function. A
// crawled row's alias comes out of the HOP's ~/.ssh/config — `internal/ui`'s crawl folds it with
// `Observer: hop` — so `ssh -v <alias>` typed on this machine resolves in the ROOT's config, which
// by definition does not hold that stanza. That is the very reason the row is a candidate, so the one
// command it offered could not work as printed. `internal/fleet/diagnose.go`'s `loginIn` had already
// made this decision for the Blocked remedy and its reasoning is quoted there; this is the second
// surface to need it, which is why it CALLS that function rather than composing a login of its own.
//
// Three sentences, because there are three cases and they differ in what is true:
//
//   - the ROOT declared it: the alias is this machine's own vocabulary, so it goes in the command
//     unchanged. This is the case the old sentence was right about, and its COMMAND is byte for byte
//     the same one; what moved is where in the sentence it sits, for the reason measured below.
//   - a HOP declared it and its recipe resolved: the resolved `user@hostname` (with `-p <port>` only
//     when the port is not 22) is the only form that addresses the machine from here, and the
//     sentence says whose name the alias was so the operator is not left wondering why it changed.
//     A DIRECT route is what it aims at deliberately: §2.2.1 says a proxied handshake reports the
//     jump's key, so a route through the hop could never identify the machine and could not make the
//     row mountable — which is what §3.4 says the act is for.
//   - a HOP declared it and there is no recipe to resolve from: then the hop is the only machine that
//     name means anything on, so the act is asked THERE. Saying "unreachable" instead is what
//     invariant 4 forbids, and inventing a login out of an alias would be worse than saying nothing —
//     it would name a machine nobody mentioned.
//
// The ACT COMES FIRST in all three, and that ordering is measured rather than preferred:
// `internal/ui`'s `discoveredBlock` wraps a reason at `width - discoveredIndent` — 74 columns at the
// 80 §16 commits to — and its COMPACT form keeps only the first of those lines, so an act that starts
// past column 74 is an act the operator never reads. Complaint-first, which is how this sentence used
// to read, is worse than that measurement makes it sound: the command ENDED at column 80 for a
// four-column alias and at 112 for the operator's widest one, so on the compact line it was cut in
// EVERY case, not just for long names. That is this repository's oldest defect class (keep the label,
// lose the action) and it is the same ordering `Diagnose`'s own sentence was reordered to avoid.
//
// Measured, act-first, on the real vocabulary — the operator's 21 concrete aliases run to 20 columns
// (`gitlab.storefront.eu`) and their resolved logins to 38 (`admin1@web-app.orchardpet.DawnBreather.net`):
// the act ENDS at 33 + the alias or login it carries, so 53 for the widest root case, 71 for the
// widest resolved one and 69 for the widest unresolved one. All three clear 74, and clear the 78 that
// `diagnose_test.go` holds a Blocked act to. Past that the name itself is wider than the room, which
// is the case that file calls pathological and holds to the VERB alone; here too the verb is in
// column 1, so what a 120-column alias loses is its own tail and never the instruction.
func candidateReason(o Observation) string {
	// One clause, composed once: every branch ends in the same complaint, and it is the complaint
	// `TestEachStatesRemedyNamesItsOwnNextMove` reads to tell this state's sentence from the others'.
	said := " — nothing has said why " + o.Label + " did not answer"
	if o.Observer == RootObserver {
		return "probe it from here with `ssh -v " + o.Label + "`" + said +
			", and the handshake is the only thing that will"
	}
	if login := loginIn(o.Recipe); login != "" {
		return "probe it from here with `ssh -v " + login + "`" + said +
			", and " + o.Label + " is " + o.Observer + "'s own name for it, which does not resolve here"
	}
	return "resolve it on " + o.Observer + " with `ssh -G " + o.Label + "`" + said +
		", and " + o.Observer + " is the only machine that name resolves on"
}

// absorb returns the node these fingerprints belong to, merging every node they intersect.
//
// Merging is not an edge case: the second and later matches mean two identities the graph was
// holding apart have just been shown to be one machine, and leaving them apart would let one
// fingerprint resolve to two different nodes depending on which one the scan reached first.
func (g *Graph) absorb(fps []string) *Node {
	var keep *Node
	out := make([]*Node, 0, len(g.nodes))
	for _, n := range g.nodes {
		switch {
		case !n.intersects(fps):
			out = append(out, n)
		case keep == nil:
			keep = n
			out = append(out, n)
		default:
			keep.fold(n)
		}
	}
	g.nodes = out
	return keep
}

// nodeFor returns the node holding this fingerprint, or nil.
func (g *Graph) nodeFor(fp string) *Node {
	for _, n := range g.nodes {
		if n.intersects([]string{fp}) {
			return n
		}
	}
	return nil
}

// nodeLabelled returns the node this observer already calls by this alias, or nil.
func (g *Graph) nodeLabelled(observer, alias string) *Node {
	for _, n := range g.nodes {
		for _, l := range n.Labels {
			if l.Observer == observer && l.Alias == alias {
				return n
			}
		}
	}
	return nil
}

// dropCandidate removes one observer's declaration of one alias.
//
// It matches the observer too, and not the alias alone: an alias is per-observer vocabulary, so the
// root verifying its own `leaf` says nothing about the machine a hop calls `leaf`. Dropping both
// would hide a candidate that is a different machine, in the direction that looks like success.
func (g *Graph) dropCandidate(observer, alias string) {
	out := g.cands[:0]
	for _, c := range g.cands {
		if c.Observer == observer && c.Alias == alias {
			continue
		}
		out = append(out, c)
	}
	g.cands = out
}

// record stores an edge, replacing the earlier declaration by the same observer of the same alias.
// One observer naming one alias is ONE edge however many times it is polled; appending instead would
// grow the list for as long as the hub is open.
//
// A target already named is not un-learned by an observation that carries none — the same rule the
// node's own facts follow, for the same reason: a probe that failed this tick learned nothing about
// the identity, and it did not disprove the handshake that named it. Blanking the target would make
// Walk stop following an edge to a machine the root has spoken to, so the traversal would get quietly
// smaller with every failed poll.
func (g *Graph) record(e Edge) {
	for i, have := range g.edges {
		if have.Observer != e.Observer || have.Alias != e.Alias {
			continue
		}
		if e.To == "" {
			e.To = have.To
		}
		g.edges[i] = e
		return
	}
	g.edges = append(g.edges, e)
}

// Nodes returns every machine the root has spoken to, in the order first seen.
//
// It is a SNAPSHOT, copied down to the slices and the map. The caller that shares a graph across
// bubbletea's Update and a tea.Cmd works on a private copy and merges back by key and by field —
// never by index — and a returned slice that aliased the graph's own memory would make that
// impossible to do correctly (CLAUDE.md).
func (g *Graph) Nodes() []Node {
	out := make([]Node, 0, len(g.nodes))
	for _, n := range g.nodes {
		out = append(out, n.clone())
	}
	return out
}

// Edges returns every declaration, in the order first seen. A snapshot, as Nodes is.
func (g *Graph) Edges() []Edge {
	out := make([]Edge, 0, len(g.edges))
	for _, e := range g.edges {
		e.Recipe = cloneMap(e.Recipe)
		out = append(out, e)
	}
	return out
}

// Candidates returns every declaration the root has not verified, in the order first seen. A
// snapshot, as Nodes is.
func (g *Graph) Candidates() []Unverified {
	out := make([]Unverified, 0, len(g.cands))
	for _, c := range g.cands {
		c.Recipe = cloneMap(c.Recipe)
		out = append(out, c)
	}
	return out
}

// Walk visits every node exactly once, following each node's outgoing edges before falling back to
// any node no edge reached.
//
// A node's outgoing edges are the ones an observer of its own name declared, so a machine that
// declares the machine that declared it is a cycle in the traversal — and it terminates with no
// special case because what is marked visited is the IDENTITY, not the alias that led there
// (invariant 2). Remove the visited check and TestTheWalkTerminatesOnACycle does not finish.
//
// The callback must not mutate the graph: a merge during a walk removes a node the traversal is
// still holding. Collect what you learn and Observe it afterwards.
func (g *Graph) Walk(visit func(Node)) {
	seen := make(map[*Node]bool, len(g.nodes))
	var follow func(n *Node)
	follow = func(n *Node) {
		if seen[n] {
			return
		}
		seen[n] = true
		visit(n.clone())
		for _, e := range g.edges {
			if e.To == "" || !n.answersTo(e.Observer) {
				continue
			}
			if to := g.nodeFor(e.To); to != nil {
				follow(to)
			}
		}
	}
	for _, n := range g.nodes {
		follow(n)
	}
}

// intersects reports whether this node's identity set shares a fingerprint with fps.
func (n *Node) intersects(fps []string) bool {
	for _, have := range n.Fingerprints {
		for _, fp := range fps {
			if have == fp {
				return true
			}
		}
	}
	return false
}

// answersTo reports whether some observer calls this node by that name. It is how an observer's name
// resolves to a machine: a topology writes one identifier per machine and every observer is named by
// the alias its declarer used, so an edge from "hop" leaves the node somebody calls `hop`.
func (n *Node) answersTo(observer string) bool {
	for _, l := range n.Labels {
		if l.Alias == observer {
			return true
		}
	}
	return false
}

func (n *Node) addFingerprint(fp string) {
	if fp == "" || n.intersects([]string{fp}) {
		return
	}
	n.Fingerprints = append(n.Fingerprints, fp)
}

func (n *Node) addLabel(l Label) {
	for _, have := range n.Labels {
		if have == l {
			return
		}
	}
	n.Labels = append(n.Labels, l)
}

// fold merges another node's facts into this one. It is called when an observation proves two
// identities were one machine; this node is the earlier of the two, so it keeps its position.
//
// State and Reason are deliberately NOT merged. The only caller is absorb, from verify, which decides
// both from the observation that proved the merge — so a state chosen here could never be read, and a
// line whose effect nothing can observe is a rule that reads as enforced and is not. A future second
// caller of fold owns that decision itself.
func (n *Node) fold(other *Node) {
	for _, fp := range other.Fingerprints {
		n.addFingerprint(fp)
	}
	for _, l := range other.Labels {
		n.addLabel(l)
	}
	if n.Hostname == "" {
		n.Hostname = other.Hostname
	}
	if len(n.Recipe) == 0 {
		n.Recipe = cloneMap(other.Recipe)
	}
	if other.Shell != ShellOther {
		n.Shell = other.Shell
	}
	if n.TmuxVersion == "" {
		n.TmuxVersion = other.TmuxVersion
	}
	if n.UID == "" {
		n.UID = other.UID
	}
	n.Samples = mergeSamples(n.Samples, other.Samples)
}

func (n *Node) clone() Node {
	out := *n
	out.Fingerprints = append([]string(nil), n.Fingerprints...)
	out.Labels = append([]Label(nil), n.Labels...)
	out.Samples = append([]time.Duration(nil), n.Samples...)
	out.Recipe = cloneMap(n.Recipe)
	return out
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// mergeSamples shares one window between two newest-first lists, taking each list's newest in turn.
//
// A sample carries no timestamp — deliberately, since this package holds no clock — so ORDER is the
// only recency information that exists, and no ordering ACROSS the two lists is knowable. That rules
// out concatenation twice over: it asserts an order neither list contains, and with `trim` keeping the
// newest end it hands the whole window to whichever alias happened to be observed first, discarding
// every round trip measured through the other. Alternating keeps the head a genuinely newest sample,
// keeps both identities' newest half, and depends on nothing about which alias arrived first —
// maxSamples' own reasoning is that a machine which has become slow must not be defended by an old
// fast sample, and a merge that dropped one identity's samples entirely would do exactly that.
func mergeSamples(a, b []time.Duration) []time.Duration {
	if len(b) == 0 {
		return trim(a)
	}
	out := make([]time.Duration, 0, min(len(a)+len(b), maxSamples))
	for i := 0; i < len(a) || i < len(b); i++ {
		if i < len(a) {
			out = append(out, a[i])
		}
		if i < len(b) {
			out = append(out, b[i])
		}
	}
	return trim(out)
}

func trim(s []time.Duration) []time.Duration {
	if len(s) <= maxSamples {
		return s
	}
	return s[:maxSamples]
}
