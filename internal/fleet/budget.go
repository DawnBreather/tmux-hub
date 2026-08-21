package fleet

import "strconv"

// Budget bounds the CRAWL, and it is not the latency budget (fleet spec §3.3).
//
// Latency bounds mounting: whether a machine is worth polling. It does not bound crawling at all, and
// the two must not be one number, because an unbounded chain over hosts that each declare eighteen
// aliases is combinatorial — and the operator's own machine declares eighteen.
//
// Both numbers are hard caps and a zero is a zero. `Budget{}` therefore crawls nothing, which is a
// deliberate choice between two bad zeroes: the other reading (0 means unbounded) makes a budget
// nobody filled in the very explosion this type exists to prevent, and it does it silently, while
// this one cuts everything and files a report saying exactly that. The numbers themselves belong to a
// later part, measured on the harness rather than chosen here.
type Budget struct {
	// MaxDepth is the greatest observer depth the crawl reads declarations FROM. The root is depth
	// 0, so the zero value still admits the root's own hosts.toml — today's flat fleet is the
	// smallest thing this type can express, not something it refuses.
	MaxDepth int
	// MaxPerObserver is the most aliases one observer may contribute.
	MaxPerObserver int
}

// The crawl's numbers, in ONE place, because two copies of a budget are two budgets.
//
// They are constants rather than a package-level Budget variable so nothing can reassign them at
// run time, and they are exported because `internal/hostset` spends the round trips this breadth
// bounds — its per-hop cap IS this number, and until this commit it was a second literal beside a
// comment promising they would be folded.
//
// DefaultBreadth is 32 because the largest real config on this fleet declares 21 probeable aliases
// (measured 2026-08-20 by hostset.ParseSSHConfig over the operator's own files), so this crawls
// every hop this fleet has WHOLE and still bounds one half again as large as any yet seen. It is
// CHOSEN rather than measured, and being wrong about it is visible rather than silent, which is the
// property that makes a chosen cap safe to ship.
//
// DefaultDepth is 1: today's crawl reads the declarations of the hops the root already polls and
// goes no further. Depth is separate from breadth (§3.3) and both are separate from the LATENCY
// budget, which bounds mounting and not crawling.
const (
	DefaultDepth   = 1
	DefaultBreadth = 32
)

// DefaultBudget is those numbers as a Budget. A function and not a variable, so a caller cannot
// change the crawl's bounds for every other caller.
func DefaultBudget() Budget {
	return Budget{MaxDepth: DefaultDepth, MaxPerObserver: DefaultBreadth}
}

// Cut is what a budget dropped, and it is the reason the budget is safe to have. A crawl that stops
// without saying where is indistinguishable from a crawl that finished, so every cut carries the
// observer it happened to, how many it skipped, and a sentence naming the knob to raise.
type Cut struct {
	Observer string
	Skipped  int
	Why      string
}

// BreadthCut is THE sentence a breadth cut carries, wherever the cut happened.
//
// One function, because two producers were building this sentence with two sets of words: this
// package's Allow, and `internal/hostset`'s per-hop cap on the round trips it spends. That is one
// decision in two places, and this repository has had a rule diverge that way three times in a
// single day — the pin key against the alias key, `beats` against the fold, the write boundary
// against the read boundary. The two callers ask the same question (this observer declares more than
// the crawl looked at) so they get the same answer, in the same words, and rewording it is one edit.
//
// It names the COUNT, because a crawl that stopped without saying where is indistinguishable from a
// crawl that finished, and the KNOB, because a reason without a remedy is a complaint (invariant 4).
// The count is space-delimited on both sides, deliberately: a caller's assertion of `Contains(why,
// "3")` would also match the 35 and the 32 in the same sentence, so the figure that matters has to be
// findable as a word.
func BreadthCut(observer string, declared, looked int) string {
	return observer + " declares " + strconv.Itoa(declared) + " machines and the crawl looked at " +
		strconv.Itoa(looked) + ", so " + strconv.Itoa(declared-looked) + " were skipped — raise the " +
		"per-host breadth to see the rest"
}

// Allow returns the aliases this observer may contribute, and FILES the report for whatever it
// dropped.
//
// It is a method on the graph rather than on the Budget on purpose: a `Budget.Allow` returning a
// shortened slice would let a caller take the truncation and leave the report behind, and then the
// only evidence the crawl stopped early would be a number nobody printed. Here there is no way to
// apply the budget without reporting — the cut is filed before the shortened list is returned.
//
// Depth is checked before breadth because it subsumes it: an observer the crawl will not read at all
// has skipped every alias it declared, and reporting a tail as well would count the same aliases
// twice in one report.
func (g *Graph) Allow(b Budget, observer string, depth int, labels []string) []string {
	if depth > b.MaxDepth {
		g.cut(Cut{
			Observer: observer,
			Skipped:  len(labels),
			Why: "the crawl stopped at " + observer + ", " + strconv.Itoa(depth) + " hops out, and " +
				"skipped all " + strconv.Itoa(len(labels)) + " of the machines it declares — raise the " +
				"crawl depth past " + strconv.Itoa(b.MaxDepth) + " to look further",
		})
		return nil
	}
	if len(labels) <= b.MaxPerObserver {
		return labels
	}
	// A NEGATIVE breadth is nonsense, and nonsense must not be a PANIC: `Budget` is exported, its
	// fields are plain ints, and `labels[:-1]` is a slice-bounds crash rather than a refusal. Zero is
	// already meaningful here — this package's own comment says the zero value must mean "nothing is
	// allowed" precisely so that an unfilled struct cannot open the crawl — so a negative clamps to
	// zero and reports every label as cut, which is the answer that keeps its promise: the caller
	// gets no labels and the reason says how many it lost.
	allowed := max(b.MaxPerObserver, 0)
	skipped := len(labels) - allowed
	g.cut(Cut{
		Observer: observer,
		Skipped:  skipped,
		Why:      BreadthCut(observer, len(labels), allowed),
	})
	// A copy, as every other answer this package hands out is: a window on the caller's own array
	// would let a later write into that array rewrite an answer already given, and the reason Allow
	// returns the labels rather than a count is that the caller keeps them.
	return append([]string(nil), labels[:allowed]...)
}

// cut files one report.
func (g *Graph) cut(c Cut) { g.cuts = append(g.cuts, c) }

// Cuts returns everything a budget dropped, in the order it was dropped. A snapshot, as Nodes is.
func (g *Graph) Cuts() []Cut { return append([]Cut(nil), g.cuts...) }
