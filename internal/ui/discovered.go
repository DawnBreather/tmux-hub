package ui

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/fleet"
	"github.com/DawnBreather/tmux-hub/internal/fleetcache"
	"github.com/DawnBreather/tmux-hub/internal/hostset"
	"github.com/DawnBreather/tmux-hub/internal/lines"
)

// The picker's DISCOVERED section: the machines behind the hosts the hub already reaches, each with
// the one command that would make it mountable.
//
// It shows only what a HOP declared, never what the root's own ~/.ssh/config declares. Those already
// have a row with a tick box a few lines above, and drawing one machine twice is the defect this
// repository has been reported three times, each time in the words "I think I am seeing duplicates".
// A machine both a hop and the root declare is therefore here under the HOP's name for it, which is
// the fact the operator does not otherwise have.
//
// What it can say is bounded by fleet spec §3.4, and the bound is stated there rather than being a
// shortcoming of this file: with ssh-config-only discovery and no nested ssh, discovery adds no new
// NODES. Only the root's own completed handshake makes a node (invariant 3), and a fingerprint taken
// through a proxy belongs to the JUMP rather than to the destination (§2.2.1), so a machine reachable
// only through a hop cannot be identified by this part at all. Its product is a set of diagnosed
// CANDIDATES — "here are the machines behind your hops, and here is the one command that makes each
// mountable" — which is what §3.4 chose deliberately over drawing rows that cannot be opened. The
// Mounted and Available tiers below are therefore reachable from the graph and not from today's
// production crawl; they are the ordering this section will need the day a node arrives from a
// handshake made on a hop (part 7), and the E2E cases exercise them against real containers.

// rootObserver is `fleet.RootObserver` under a local name, and it is an ALIAS rather than a copy: the
// value lives in one place, so a filter written here cannot disagree with what the crawl wrote or
// with the remedy `fleet` composes from the same predicate. It was briefly a second `const ""`, which
// is one fact in two packages — the shape this repository keeps paying for.
const rootObserver = fleet.RootObserver

// crawlTimeout bounds one whole round of looking behind the hops.
//
// It is generous on purpose. The round travels over masters that are already open, so the cost is a
// round trip per alias rather than a handshake — but a hop declaring twenty machines pays twenty of
// them, and one edge on the container harness is 180 ms by declaration while the live fleet has
// swung to 18.4 s. Nothing waits on this: it runs in a tea.Cmd and the screen is live throughout, so
// the cost of being too generous is a stale section and the cost of being too tight is a row that
// says the crawl ran out of time when the machine was simply far away.
const crawlTimeout = 90 * time.Second

// crawlBudget is the graph's own budget for this round: depth 1, because today's crawl reads the
// declarations of the hops the root already polls and goes no further, and the breadth
// `internal/hostset` already enforces per hop.
//
// It is applied even though neither number can bite today, and that is the point: the gate is on the
// path rather than in a comment, so a producer that enumerates a hop without hostset's own cap is
// bounded, and a cut is REPORTED with its count instead of being a silent horizon (fleet spec §3.3).
// It is a function call rather than a literal so the two numbers live in ONE place: `internal/hostset`
// spends the round trips this breadth bounds, and until this commit its cap was a second literal.
func crawlBudget() fleet.Budget { return fleet.DefaultBudget() }

// fleetStore is the graph, and the LOCK around it.
//
// `fleet.Graph` says in its own package comment that it is not safe for concurrent use, and this is
// the seam that makes it so. The crawl runs in a tea.Cmd body and bubbletea's Update reads the same
// graph to draw, so the two run concurrently — and this repository has already paid for exactly that
// shape once: `Poller.Add` appended to a shared slice while a tick held a pointer into it, growslice
// reallocated, and a whole poll's status writes were discarded silently with `go test -race` green
// throughout, because no test called the two together.
//
// So two rules, and both are structural rather than remembered. Every method takes the lock. And NO
// CALLER EVER HOLDS A POINTER INTO SHARED STATE: the graph's own accessors already hand out deep
// snapshots, and Snapshot below is the only way out of this type, so a reader works on a private copy
// and can never be reading an array a writer is about to reallocate.
//
// TestEveryObservationMadeWhileTheScreenWasReadingSurvives asserts the VALUE and not the silence,
// which is the half that matters: a copy-on-write "fix" that discarded the writes would keep `-race`
// quiet while losing every machine the crawl found — worse than the bug, because it removes the
// evidence and keeps the symptom.
type fleetStore struct {
	mu sync.Mutex
	g  *fleet.Graph
}

func newFleetStore() *fleetStore { return &fleetStore{g: fleet.New()} }

// fleetSnapshot is one consistent reading of the graph. A struct rather than three calls, because
// three calls take the lock three times and could straddle a write — the section would then draw a
// candidate that had already become a node, which is one machine twice.
type fleetSnapshot struct {
	Nodes      []fleet.Node
	Candidates []fleet.Unverified
	Cuts       []fleet.Cut
}

// Snapshot is the only way to read the graph, and it is nil-safe: a model built by a fixture never
// went through the option that makes a store, and every screen still has to draw.
func (s *fleetStore) Snapshot() fleetSnapshot {
	if s == nil {
		return fleetSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return fleetSnapshot{
		Nodes:      s.g.Nodes(),
		Candidates: s.g.Candidates(),
		Cuts:       s.g.Cuts(),
	}
}

// Observe folds observations in, under the lock. Variadic so one round is one critical section: a
// per-observation lock would let a snapshot land in the middle of one hop's list, and a section drawn
// from it would report a hop as declaring fewer machines than it does.
func (s *fleetStore) Observe(obs ...fleet.Observation) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range obs {
		s.g.Observe(o)
	}
}

// Allow applies the crawl budget and files whatever it cut, under the lock. It is a pass-through on
// purpose — the graph's own Allow is a method rather than a Budget function precisely so a caller
// cannot take the truncation and leave the report behind, and wrapping it here must not undo that.
func (s *fleetStore) Allow(b fleet.Budget, observer string, depth int, labels []string) []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.g.Allow(b, observer, depth, labels)
}

// cutFor is the sentence the most recent cut against this observer carries, or "".
//
// The LAST one, because a re-crawl of the same hop files another, and the freshest is the one whose
// count matches the list the caller is holding.
func (s *fleetStore) cutFor(observer string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	why := ""
	for _, c := range s.g.Cuts() {
		if c.Observer == observer {
			why = c.Why
		}
	}
	return why
}

// DiscoveredRow is one machine a hop declares, as this section shows it.
type DiscoveredRow struct {
	// Label is the alias the OBSERVER uses, and Observer is that machine. They travel together
	// because an alias is per-observer vocabulary: `web` here and `web` on a hop are different
	// machines (fleet spec §2.1), so a bare label is not an identity and two hops' lists cannot be
	// merged by name.
	Label    string
	Observer string
	State    fleet.State
	// Reason is the remedy. It is empty only for Mounted, which is the one state with no next move
	// (fleet spec §3.4), and the graph itself refuses to store a silent reason for any other.
	Reason string
	// Version is what `tmux -V` answered for this machine, when anything did.
	Version string
	// RTT and Timed are the ordering figure and whether it is a measurement at all. Timed=false is
	// NOT "0 ms": a zero would sort every machine nobody has timed into the fastest bucket, which is
	// the one direction a remembered order must not fail in.
	RTT   time.Duration
	Timed bool
}

// DiscoveredRowsFor turns the graph into the section's rows, ordered.
//
// `facts` is the remembered measurement — fleetcache — passed as a lookup rather than as the cache
// itself so this function needs no filesystem and no clock. A nil lookup is a hub that remembers
// nothing, which orders every row by name inside one bucket rather than refusing to draw.
//
// A CANDIDATE INHERITS ITS HOP'S ROUND TRIP, and that is a claim worth stating: nobody has timed a
// machine the root cannot reach, so the only measurement that bounds it is the hop's own — a machine
// behind a hop cannot be nearer than the hop. The row prints the hop beside the label, so the figure
// is never read as the machine's own.
func DiscoveredRowsFor(nodes []fleet.Node, cands []fleet.Unverified,
	facts func(fleetcache.Key) (fleetcache.Facts, bool)) []DiscoveredRow {

	look := facts
	if look == nil {
		look = func(fleetcache.Key) (fleetcache.Facts, bool) { return fleetcache.Facts{}, false }
	}

	var out []DiscoveredRow
	for _, n := range nodes {
		// EVERY key the node's memory could be under, not just its first fingerprint. A node's
		// fingerprint set GROWS: two machines seen separately are two nodes, and one later
		// observation carrying both fingerprints merges them into one whose key is the keeper's
		// first — so everything remembered under the absorbed fingerprint becomes unreachable, and
		// the row falls back to `no timing` and re-sorts into a slower bucket than the machine
		// deserves. Measured: a node with a 42 ms RTT filed under its own fingerprint found nothing
		// after the merge. `fleetcache.KeysOfNode` owns the order; this loop owns "first hit wins".
		var known fleetcache.Facts
		var timed bool
		for _, k := range fleetcache.KeysOfNode(n) {
			if f, ok := look(k); ok {
				known, timed = f, true
				break
			}
		}
		for _, l := range n.Labels {
			if l.Observer == rootObserver {
				continue
			}
			out = append(out, DiscoveredRow{
				Label: l.Alias, Observer: l.Observer, State: n.State, Reason: n.Reason,
				Version: n.TmuxVersion, RTT: known.RTT, Timed: timed,
			})
		}
	}
	for _, c := range cands {
		if c.Observer == rootObserver {
			continue
		}
		// The HOP's figure, keyed on the root's own alias for it, because that is the connection
		// the probe timed. See this function's own comment for why the far machine inherits it.
		known, timed := look(fleetcache.Key{Observer: rootObserver, Alias: c.Observer})
		out = append(out, DiscoveredRow{
			Label: c.Alias, Observer: c.Observer, State: c.State, Reason: c.Reason,
			RTT: known.RTT, Timed: timed,
		})
	}
	return orderDiscovered(out)
}

// The buckets. Measured on the live fleet (docs/design.md §9), one host answered at 5.4 s, 9.1 s,
// 15.7 s and 18.4 s across four probes — so an order taken from the raw figure reorders between two
// openings of the same screen, and the row the operator ticked is a different machine when they come
// back to it. Bucketing makes that impossible rather than unlikely, and it is the same remedy the
// dashboard's recency tiebreak takes for the same reason.
//
// The last bucket is NOT "slower". A machine nobody has timed is an absence, and folding it in with
// the slow ones would make the screen unable to tell a measurement from its lack — the shape this
// repository has shipped twice, where a zero was indistinguishable from the thing being missing.
const (
	bucketNear = iota
	bucketClose
	bucketFar
	bucketSlower
	bucketUntimed
)

func discoveredBucket(r DiscoveredRow) int {
	if !r.Timed {
		return bucketUntimed
	}
	switch {
	case r.RTT < 50*time.Millisecond:
		return bucketNear
	case r.RTT < 250*time.Millisecond:
		return bucketClose
	case r.RTT < time.Second:
		return bucketFar
	default:
		return bucketSlower
	}
}

// discoveredBucketName is what the row prints. Short, because it shares a row with a label nothing
// bounds, and it is a POSITIVE statement in every case including the absence — `no timing` says
// which fact is missing, where a blank would leave a hole a reader has to guess at.
func discoveredBucketName(r DiscoveredRow) string {
	switch discoveredBucket(r) {
	case bucketNear:
		return "<50ms"
	case bucketClose:
		return "<250ms"
	case bucketFar:
		return "<1s"
	case bucketSlower:
		return "slower"
	default:
		return "no timing"
	}
}

// discoveredTier is the operator's own decision, and it outranks the bucket.
//
// Three tiers: what the root already polls, what it has verified and could poll, and everything
// else. The question the top of this section answers is "is there anything for me to do", and a
// machine already mounted needs nothing done however slow its link — so a fast candidate must not
// push it down the list.
func discoveredTier(r DiscoveredRow) int {
	switch r.State {
	case fleet.Mounted:
		return 0
	case fleet.Available:
		return 1
	default:
		return 2
	}
}

// orderDiscovered sorts by tier, then by bucket, then by name — and by the OBSERVER last, so two
// hops declaring the same alias have a defined order rather than whichever the crawl reached first.
//
// It is a stable sort over a total key, which means the answer does not depend on the input order at
// all. That is what makes the section paint the same way twice.
func orderDiscovered(rows []DiscoveredRow) []DiscoveredRow {
	out := append([]DiscoveredRow(nil), rows...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ta, tb := discoveredTier(a), discoveredTier(b); ta != tb {
			return ta < tb
		}
		if ba, bb := discoveredBucket(a), discoveredBucket(b); ba != bb {
			return ba < bb
		}
		if a.Label != b.Label {
			return a.Label < b.Label
		}
		return a.Observer < b.Observer
	})
	return out
}

const (
	// discoveredStateColumn is the width of the state word. `candidate` is the longest of the five
	// (fleet.State.String), so the column is its width and every row's label starts in one place.
	discoveredStateColumn = 9
	// discoveredIDColumn is the room the `<label> @<observer>` cell gets. Both halves come from
	// another machine's config and nothing bounds either, so the cell is bounded HERE — the same
	// rule shortSubject applies to a footer's subject, for the same reason: the row is on the screen
	// and the name is what yields.
	discoveredIDColumn = 30
	// discoveredIndent is where a wrapped remedy resumes. It is deliberately far left of the
	// label column: the remedy is a COMMAND, and giving it the width of the terminal is what keeps
	// it whole at the 80 columns §16 commits to.
	discoveredIndent = 6
)

// RenderDiscovered draws the section: a heading that carries the total, then a block per machine,
// cut at `height` with what was cut COUNTED.
//
// The heading holds the total whatever the height, which is what makes cutting the list safe — this
// repository's oldest defect class is a line assembled without asking whether the parts fit, cut so
// that the label survives and the number or the action does not.
func RenderDiscovered(rows []DiscoveredRow, width, height int) []string {
	if len(rows) == 0 || height < 1 || width < 1 {
		return nil
	}
	// WHOLE remedies if one machine's worth fits, and the ACT ALONE if not.
	//
	// The fallback is not a compromise on "every error carries its fix" — it rests on a property
	// fleet.Diagnose engineered on purpose and measured: the ACT COMES FIRST in every reason it
	// builds. `run \`ssh-copy-id dev@host\`` ends by column 35 (the key list, which is unbounded,
	// was deliberately put last for exactly this reason), and the proxy sentence's verb phrase
	// `give this machine a direct route` is 31 columns. So one line always carries something the
	// operator can do, and the ` …` says the sentence continues.
	//
	// It is needed because the whole-remedy form does not fit at the size §16 commits to, which was
	// measured rather than reasoned: at 80×24 the picker's overlay has seven rows for content, and
	// the proxy diagnosis alone is a head line plus four wrapped lines. Without this fallback the
	// section YIELDED WHOLE at that size — the operator saw nothing at all about what is behind
	// their hops, and which machine sorted first decided whether they did. Silent, and dependent on
	// another sentence's length.
	// THE FORM IS CHOSEN BY ASKING, not by arithmetic about the marker's row. An earlier cut compared
	// `len(blocks[0])` against `height-1` while the fit below runs against `height-2` — the "N not
	// shown" row is reserved — so at a first block exactly `height-1` tall the whole-remedy form was
	// kept, nothing fitted, the marker was correctly suppressed, and the section printed its heading
	// over an empty list. The compact form would have named a machine at that very height. Measured at
	// 60 columns with the sentence fleet.Diagnose really builds: height 4 named `leaf0`, height 5
	// named nobody — a TALLER terminal saying LESS, which is the one thing docs/design.md §9 asserts
	// this section never does. Two arithmetics answering one question is how that happened, so there
	// is one now: build it, and if it named nobody, build it again from the shorter form.
	blocks := discoveredBlocks(rows, width, 0)
	shown, marker := discoveredShown(blocks, len(rows), height)
	if shown == 0 {
		blocks = discoveredBlocks(rows, width, 1)
		shown, marker = discoveredShown(blocks, len(rows), height)
	}

	out := []string{lines.Truncate(discoveredHeading(len(rows)), width)}
	for i := 0; i < shown; i++ {
		out = append(out, blocks[i]...)
	}
	// The "N not shown" line only earns its row once a machine IS named. With nothing shown it
	// restates the heading, which already carries the TOTAL — so at a height too small for one
	// machine's block the honest output is the heading alone, and it is one line rather than two.
	// Measured by a verifier: at height 1 this returned TWO lines, one past the contract stated
	// above, and at heights 2 and 3 it spent the second row saying nothing the first had not said.
	if marker && shown > 0 && shown < len(rows) {
		out = append(out, lines.Truncate(discoveredMore(len(rows)-shown), width))
	}
	// NO CAP HERE, deliberately, and this is a reversal worth reading. A `len(out) > height` cut was
	// added when the two arithmetics above disagreed, and once `discoveredShown` became the single
	// answer the cut was unreachable: measured by deleting it, and no assertion in the suite changed.
	// Leaving it would be worse than dead code — it would TRUNCATE a future arithmetic mistake into
	// something that fits, so the sweep in TestTheSectionsHeightContractHoldsAtEveryWidthAndHeight
	// would go green while an operator silently lost the tail of a remedy. The overrun is prevented by
	// construction and asserted by that sweep at five widths and thirty heights; a cut would only hide
	// it. Same reasoning this repository applies to a `Truncate` that swallows its own defect.
	return out
}

// discoveredShown is how many machines the section names in `height` rows, and it owns the marker's
// arithmetic so that no caller repeats it.
//
// The order of the two questions is the whole content. Ask FIRST whether everything fits with no
// marker at all, because the marker is only true when something is left out — the earlier form asked
// with the row reserved and then re-asked when the answer was already "all of them", which is a
// no-op, and left the interesting case unhandled: a section that could name every machine using the
// marker's row instead reported `1 machine not shown` above a row it had spare. Asking in this order
// also removes the case where a taller terminal names fewer, because the room only grows.
func discoveredShown(blocks [][]string, total, height int) (int, bool) {
	room := height - 1 // the heading owns one row, always
	if all := discoveredFit(blocks, room); all == total {
		return all, false
	}
	if some := discoveredFit(blocks, room-1); some > 0 {
		// Something is left out and a machine is named, so the marker is both true and worth its row:
		// it is the only thing that says a taller terminal shows more.
		return some, true
	}
	// The marker's own row is the last thing standing between the operator and a machine. The machine
	// takes it: the heading already carries the TOTAL, so a reader can see that not every machine is
	// drawn, while a section that names nobody carries no remedy — and the remedy is this section's
	// entire product. Measured at 60 columns, where a compact block is two rows: at height 3 the
	// reserved row left `fit(1) == 0` and the section printed its heading over nothing, when it could
	// have printed `blocked leaf0 @hop` and the `ssh-copy-id` that fixes it.
	return discoveredFit(blocks, room), false
}

// discoveredFit counts the whole blocks that fit. WHOLE, because a machine's remedy cut in half is
// the same defect as a machine that is not shown at all — and worse, because it looks complete.
func discoveredFit(blocks [][]string, room int) int {
	used, shown := 0, 0
	for _, b := range blocks {
		if used+len(b) > room {
			break
		}
		used += len(b)
		shown++
	}
	return shown
}

// discoveredHeading names what the section is and how many machines it is about. The TOTAL, not what
// fits: this line is what makes cutting the list below it safe, so it must not be the thing that
// yields.
func discoveredHeading(total int) string {
	return "Behind your hops — " + plural(total, "machine", "machines") + " your hosts declare"
}

// discoveredMore is the cut marker. It names the count and the reason it is short, because a section
// that quietly stopped listing is indistinguishable from a fleet with nothing more behind it.
func discoveredMore(left int) string {
	return "  ↓ " + plural(left, "machine", "machines") + " not shown — a taller terminal shows more"
}

// discoveredBlock is one machine's lines: the row, then its remedy wrapped under it.
//
// It wraps rather than cutting for the reason §16 gives: 80×24 is the size to hold, and every error
// has to carry its FIX. The remedy here is literally a command — `ssh-copy-id <login>` — and a
// command with its tail cut off is not a command anyone can type.
// discoveredBlocks builds every machine's block at one reason budget, so the caller chooses the form
// once rather than per row: a section whose rows were compressed one at a time would put a full
// remedy above a cut one, and the reader could not tell the cut from the short.
func discoveredBlocks(rows []DiscoveredRow, width, reasonLines int) [][]string {
	out := make([][]string, len(rows))
	for i, r := range rows {
		out[i] = discoveredBlock(r, width, reasonLines)
	}
	return out
}

func discoveredBlock(r DiscoveredRow, width, reasonLines int) []string {
	id := r.Label
	if r.Observer != rootObserver {
		id += " @" + r.Observer
	}
	head := "  " + lines.Pad(r.State.String(), discoveredStateColumn) + " " +
		lines.Pad(lines.Truncate(id, discoveredIDColumn), discoveredIDColumn) + " " +
		discoveredBucketName(r)
	// The version LAST, and only when it is known. It is the same fact the candidate rows above print
	// (`tmux 3.5a`), and printing it here answers the question a machine in this section raises — can
	// this thing be polled at all — with the machine's own answer instead of a promise. It goes after
	// the bucket because it is the narrowest claimant and the one a 60-column terminal can lose: the
	// state, the name and the timing all say something about EVERY row, while the version is known only
	// for a machine some observer has actually shaken hands with. An empty value is the commonest case
	// for a hop-declared candidate and prints nothing rather than a gap.
	if r.Version != "" {
		head += "   tmux " + r.Version
	}

	out := []string{strings.TrimRight(head, " ")}
	// firstLineOf, because a reason can carry another program's stderr and `lines.Width` counts a
	// newline as a character rather than as a line break — a `\r` in a row would return the cursor
	// to column 0 and overwrite the line already drawn there.
	pieces := wrapWords(firstLineOf(r.Reason), max(1, width-discoveredIndent))
	if reasonLines > 0 && len(pieces) > reasonLines {
		// MARKED, never silent. The convention is this repository's own: a dropped part says so, and
		// `…` on a complete sentence would be a lie, which is why it is appended only here.
		pieces = append(pieces[:reasonLines:reasonLines], nil...)
		pieces[len(pieces)-1] += " …"
	}
	for _, piece := range pieces {
		out = append(out, strings.Repeat(" ", discoveredIndent)+piece)
	}
	// ONE bound for every line, whatever branch built it: a line wider than the terminal SOFT-WRAPS,
	// so a section of exactly N lines would occupy more than N visual rows and every frame below it
	// desynchronises.
	for i := range out {
		out[i] = lines.Truncate(out[i], width)
	}
	return out
}

// discoveredNeed is the fewest rows that show a MACHINE: the heading plus the first whole block.
//
// It exists because half the body is not enough at the one size §16 commits to, and that was
// measured rather than reasoned. At 80×24 the picker's body has seven rows for content, half of it is
// three, and one machine's block is three lines on its own — so the section drew
// `Behind your hops — 1 machine your hosts declare` followed by `↓ 1 machine not shown` and NEVER
// NAMED THE MACHINE. That is this repository's oldest defect class in a new coat: keep the label,
// lose the subject and the action.
//
// It asks the drawing function for the height rather than computing one, so a row that gains a field
// cannot make this arithmetic wrong.
func discoveredNeed(rows []DiscoveredRow, width int) int {
	if len(rows) == 0 {
		return 0
	}
	// The COMPACT form and the marker line, because that is the smallest section that names a
	// machine — asking for the whole-remedy height here is what let the section be squeezed out of
	// an 80×24 screen entirely.
	need := 1 + len(discoveredBlock(rows[0], width, 1))
	if len(rows) > 1 {
		need++
	}
	return need
}

// discoveredRoom is how much of the picker's body the section may take.
//
// Three claimants and one list, in the order their loss costs the most: ONE WHOLE MACHINE (or the
// section says nothing it cannot substantiate), then two rows for the candidate list, then half the
// body as the ordinary share. The candidate list is the screen's subject — the tick boxes are there —
// so it keeps a floor of its own, and it also SCROLLS, which the section does not: a candidate the
// list cannot show is one `j` away, while a machine the section drops is gone until the terminal
// grows. That asymmetry is why the section may exceed half the body to show its first machine and may
// never exceed it to show a second.
// `candidatesWant` is what the candidate list would take if nothing shared the body with it, and it
// is what stops a tall terminal showing LESS than it could: the list scrolls, so on a fleet with
// twenty candidates it wants everything and the section takes its half — but on a fleet with two it
// wants two rows, and leaving the other eleven blank while the section says `1 machine not shown` is
// the non-monotonic shape this repository already has a rule against.
func discoveredRoom(want, need, budget, candidatesWant int) int {
	const candidateFloor = 2
	room := budget / 2
	if spare := budget - candidatesWant; spare > room {
		room = spare
	}
	if room < need {
		room = need
	}
	if cap := budget - candidateFloor; room > cap {
		room = cap
	}
	if room < need {
		// Not even one machine fits beside a usable candidate list. The section yields WHOLE rather
		// than spending two rows on a heading and a note about a machine it cannot name.
		return 0
	}
	if room > want {
		room = want
	}
	return room
}

// keyHeldHere answers whether a key ssh named is on THIS machine, and it is the caller's half of
// fleet.Diagnose's contract.
//
// THE TILDE IS THE WHOLE POINT, and it fails in the direction that looks like success. Measured:
// `ssh -G` does NOT expand `~`, so a resolved recipe carries `~/.ssh/id_ed25519` verbatim.
// fleet.Diagnose is handed the path exactly as ssh reported it, because expanding it inside that
// function would make a package with no I/O and no environment read read HOME — so the CALLER owns
// HOME, and this is that caller. Hand `os.Stat` the unexpanded path and every key on every host
// reads absent, the whole fleet comes back Blocked, and every row advertises an `ssh-copy-id` remedy
// nobody needs. TestTheCrawlExpandsTheTildeThatSSHDoesNot is what keeps it expanded, with the
// opposite pole beside it so "expanded" cannot be satisfied by a predicate that answers yes.
//
// `home` is a PARAMETER and never `os.UserHomeDir()`, because this package's own renderers are
// diffed byte for byte against a published document: HOME enters the model once at startup
// (model.home) and travels from there.
func keyHeldHere(home string) func(path string) bool {
	return func(path string) bool {
		path = strings.TrimSpace(path)
		if path == "" {
			return false
		}
		switch {
		case path == "~":
			path = home
		case strings.HasPrefix(path, "~/"):
			path = filepath.Join(home, path[2:])
		}
		// A `~user/` form is left alone deliberately: resolving another account's home needs a
		// passwd lookup, and answering "not here" for it is the safe direction — a wrong Blocked
		// costs one row a remedy it did not need until the next probe, while a wrong Ready costs the
		// operator the one command that would have fixed the machine (fleet.Diagnose says so too).
		_, err := os.Stat(path)
		return err == nil
	}
}

// crawled turns one hop's declarations into observations, diagnosing each one.
//
// Every observation is UNVERIFIED, and that is invariant 3 rather than a limitation of this
// function: only the root's own completed handshake creates a node, and the root has shaken hands
// with the hop and with nothing behind it. A candidate the enumeration itself declined — a pattern,
// an alias it would not paste, its own budget — carries that sentence instead of a diagnosis,
// because the hop's list is reported WHOLE (fleet spec §5 point 1) and a row dropped silently is
// indistinguishable from a hop that declares less than it does.
func crawled(hop string, cands []hostset.Candidate, home string) []fleet.Observation {
	held := keyHeldHere(home)
	out := make([]fleet.Observation, 0, len(cands))
	for _, c := range cands {
		o := fleet.Observation{
			Observer: hop,
			Label:    c.Alias,
			Hostname: c.Recipe["hostname"],
			Recipe:   c.Recipe,
			Reason:   c.Skip,
		}
		if c.Skip == "" {
			o.State, o.Reason = fleet.Diagnose(c.Recipe, held)
		}
		out = append(out, o)
	}
	return out
}

// discoveredMsg is what one crawl round learned. It carries finished ROWS because the round already
// holds the lock the graph needs and the cache lookup it orders by; rebuilding them in Update would
// be a second reader of both.
type discoveredMsg struct {
	rows []DiscoveredRow
	// note is why the round is incomplete, in the words of whatever refused. Empty when nothing did.
	note string
}

// mountedHops is the machines this crawl may travel over: the ones hosts.toml ENABLES.
//
// Not every candidate. A host the operator turned off has no master to travel over, so asking it
// would spend a whole connect timeout per alias on a machine nobody wants — and a host they never
// kept was never verified, so invariant 3 gives the hub nothing to ask it with.
func (m model) mountedHops() []string {
	var out []string
	for _, e := range m.pickerKept {
		if e.Enabled && strings.TrimSpace(e.Alias) != "" {
			out = append(out, e.Alias)
		}
	}
	return out
}

// crawlRefusal is what the screen says when it cannot look behind a hop at all. A section that was
// simply empty would read as "there is nothing back there", which is a different fact.
func (m model) crawlRefusal() string {
	switch {
	case m.pickerPorts.Behind == nil || m.store == nil:
		return "cannot look behind these hosts: this hub was started without a way to read a hop's " +
			"own ssh config"
	case len(m.mountedHops()) == 0:
		return "nothing to look behind yet — keep a host with space, and the hub reads what it declares"
	default:
		return ""
	}
}

// crawlBehind asks every mounted hop what it declares, diagnoses each answer, and folds it into the
// shared graph.
//
// It is a COMMAND and never inline: a hop declaring twenty machines pays a round trip each, and §16
// promises a live screen throughout. That is also why the graph needs a lock rather than care — this
// body runs concurrently with Update, which reads the same store to draw (CLAUDE.md), and the store
// hands out snapshots so no goroutine ever holds a pointer into shared state.
func (m model) crawlBehind() tea.Cmd {
	behind, store := m.pickerPorts.Behind, m.store
	hops := m.mountedHops()
	if behind == nil || store == nil || len(hops) == 0 {
		return nil
	}
	facts, home := m.pickerPorts.Facts, m.home
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), crawlTimeout)
		defer cancel()

		var trouble []string
		for _, hop := range hops {
			cands, err := behind(ctx, hop)
			if err != nil {
				// The hop's own words, named. A round that could not ask is not a hop with
				// nothing to offer, and those two must not read alike.
				trouble = append(trouble, "could not read what "+hop+" declares: "+err.Error())
				continue
			}
			labels := make([]string, 0, len(cands))
			for _, c := range cands {
				labels = append(labels, c.Alias)
			}
			allowed := store.Allow(crawlBudget(), hop, 1, labels)
			store.Observe(crawled(hop, budgeted(cands, allowed, store.cutFor(hop)), home)...)
		}
		snap := store.Snapshot()
		return discoveredMsg{
			rows: DiscoveredRowsFor(snap.Nodes, snap.Candidates, facts),
			note: firstLineOf(strings.Join(trouble, " · ")),
		}
	}
}

// budgeted marks what the graph's budget cut, keeping the row.
//
// Coverage is the rule (fleet spec §5 point 1): nothing declared is absent from the result, so a cut
// alias comes back carrying the cut's own sentence — which names the count and the knob — rather
// than disappearing. A budget that removed rows would be the silent horizon the budget exists to
// prevent.
func budgeted(cands []hostset.Candidate, allowed []string, why string) []hostset.Candidate {
	if len(allowed) == len(cands) {
		return cands
	}
	ok := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		ok[a] = true
	}
	out := make([]hostset.Candidate, 0, len(cands))
	for _, c := range cands {
		if !ok[c.Alias] && c.Skip == "" {
			c.Skip = why
		}
		out = append(out, c)
	}
	return out
}

// discoveredArrived folds one round's rows into the screen.
func (m model) discoveredArrived(msg discoveredMsg) (tea.Model, tea.Cmd) {
	m.discovered = msg.rows
	if msg.note != "" {
		m.note = msg.note
	}
	return m, nil
}

// learnFromProbe remembers what the round measured, so the next opening of this screen paints in the
// same order without waiting for a probe.
//
// The key is the ROOT's own alias and not a fingerprint, deliberately: a fingerprint identifies a
// machine only when the connection that produced it was DIRECT (fleet spec §2.2.1, measured — a
// proxied handshake reports the JUMP host's key, so attributing it to the destination would fuse two
// machines into one node), and the picker's probe has no resolved recipe with which to tell. The
// alias is the root's own vocabulary about its own config, which is sound whatever the transport,
// and it is the key the hop rows read to inherit their bound.
func (m model) learnFromProbe(results []hostset.Result, now time.Time) model {
	learn := m.pickerPorts.Learn
	if learn == nil || len(results) == 0 {
		return m
	}
	learned := make(map[fleetcache.Key]fleetcache.Facts, len(results))
	for _, r := range results {
		// Only a round trip that actually completed. A timed-out probe's `Took` is the timeout, not
		// the machine — remembering it would file the operator's own deadline as a measurement.
		if r.TimedOut || r.Took <= 0 {
			continue
		}
		learned[fleetcache.Key{Observer: rootObserver, Alias: r.Alias}] = fleetcache.Facts{
			RTT: r.Took, TmuxVersion: r.Version, LastSeen: now,
		}
	}
	if len(learned) == 0 {
		return m
	}
	if err := learn(learned); err != nil {
		// Worth saying and not worth failing over: the order is the thing at stake, and its
		// fallback is the ordinary one.
		m.note = "could not remember this round's timings: " + firstLineOf(err.Error())
	}
	// THE SECTION IS NOT REORDERED HERE, and that omission is the feature.
	//
	// A round's figures are WRITTEN and the list the operator is reading is left alone: the crawl
	// runs on every opening of this screen and reads the cache then, so a fresh measurement reaches
	// the order at the next opening. Rebuilding now would let a host cross a bucket boundary while
	// somebody is looking at the list, which is the thing bucketing exists to prevent — one host's
	// probe answered at 5.4 s, 9.1 s, 15.7 s and 18.4 s, and 900 ms against 1.1 s is the same
	// crossing at a smaller scale. Reading a remembered figure is what makes the list instant AND
	// still, and a screen that reordered under its reader would have thrown away the second half.
	return m
}

// WithDiscovered installs a section directly, for the frames that have to prove it is drawn.
//
// It exists so a frame test and the published mockup can hold a section without a network, a hop or
// a container — the same reason PickerPorts is callbacks rather than direct calls. Production fills
// this from a crawl round; nothing here reaches the world.
func WithDiscovered(rows []DiscoveredRow) Option {
	return func(m *model) { m.discovered = orderDiscovered(rows) }
}
