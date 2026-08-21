package fleet

import (
	"strings"
	"testing"
)

// noKeysHere is the predicate a machine holding none of the recipe's keys answers with, and
// keysAreHere its opposite. Both are named rather than written inline at each call, because which one
// a case passes decides whether the row is Blocked or Candidate and that is the distinction these
// tests are about.
func noKeysHere(string) bool  { return false }
func keysAreHere(string) bool { return true }

// candidateFor drives the SEAM the crawl uses, and not `candidateReason` directly.
//
// `internal/ui`'s `crawled` builds exactly this: it diagnoses the hop's resolved recipe, puts the
// answer on an Observation carrying the HOP as observer, and folds it in. Three things stand between
// Diagnose and the stored sentence — the Ready-with-no-reason return, `stateOf`'s clamp of a node
// state claimed by an unverified declaration, and `defaultReason` — and "A does X" and "B does Y" say
// nothing about a chain with a third thing in it. Asserting on the chain is also what makes these
// cases about the DOMINANT production path: a hop row whose recipe names no obstacle arrives here as
// `Candidate` with no reason of its own, which is precisely when the default sentence is printed.
func candidateFor(t *testing.T, observer, alias string, recipe map[string]string, held func(string) bool) Unverified {
	t.Helper()
	state, reason := Diagnose(recipe, held)
	g := New()
	g.Observe(Observation{Observer: observer, Label: alias, Hostname: recipe["hostname"],
		Recipe: recipe, State: state, Reason: reason})
	c := g.Candidates()
	if len(c) != 1 {
		t.Fatalf("want one candidate for %s@%s, got %+v", alias, observer, c)
	}
	if c[0].State != Candidate {
		t.Fatalf("%s reads %v, and this file is about the Candidate sentence", alias, c[0].State)
	}
	return c[0]
}

// A HOP's alias is not addressable from HERE, so it must not be the argument of a command the operator
// is told to run on this machine.
//
// This is the defect: `ssh -v <alias>` resolves in the ROOT's ssh config, which by definition does not
// hold the hop's stanza — that is the whole reason the row is a candidate — so the one command the row
// offered could not work as printed. The expected command is written as a LITERAL and composed from
// nothing: a test that asked `loginIn` for its own expectation would pass against a version that had
// stopped calling it.
func TestACandidateBehindAHopIsProbedByItsResolvedLoginAndNotByTheHopsAlias(t *testing.T) {
	// A recipe naming a key this machine DOES hold, so Diagnose finds no obstacle and answers
	// `Ready` with nothing to say — the path that reaches the default sentence.
	recipe := map[string]string{"hostname": "vault-b.internal", "user": "dev",
		"identityfile": "~/.ssh/id_ed25519"}
	got := candidateFor(t, "depot-a", "vault-b", recipe, keysAreHere).Reason

	if !strings.Contains(got, "`ssh -v dev@vault-b.internal`") {
		t.Errorf("the remedy is %q — it does not carry the resolved login, which is the only form "+
			"that addresses the machine from here", got)
	}
	if strings.Contains(got, "ssh -v vault-b") {
		t.Errorf("the remedy is %q — it tells the operator to run `ssh -v vault-b`, and `vault-b` is "+
			"depot-a's vocabulary: that name does not resolve in this machine's ssh config, which is "+
			"why the row is a candidate at all", got)
	}
	// The alias still has to be NAMED, or the sentence is about a machine the operator cannot find on
	// their screen — the row is drawn under that name.
	if !strings.Contains(got, "vault-b") || !strings.Contains(got, "depot-a") {
		t.Errorf("the remedy is %q — it must name both the alias it is about and whose name that is", got)
	}
	if strings.Contains(got, "unreachable") {
		t.Errorf("the remedy is %q — `unreachable` names no act (spec §3.2 invariant 4)", got)
	}
}

// The OTHER POLE, and without it the fix above could be "never put the alias in a command", which
// would be wrong for every candidate the root itself declared: those aliases are this machine's own
// vocabulary, `ssh -v <alias>` is exactly right, and a resolved login there would be a worse sentence
// than the alias the operator wrote in their own config.
//
// The root observer is the EMPTY string, which is `hostset.Candidate.Via`'s convention and spec §3.1's
// "there is exactly one root". Spelled as a literal `""` here on purpose: `rootObserver` is the thing
// under test.
func TestACandidateTheRootItselfDeclaredIsProbedByItsOwnAlias(t *testing.T) {
	got := candidateFor(t, "", "leaf", map[string]string{"hostname": "leaf.internal", "user": "dev",
		"identityfile": "~/.ssh/id_ed25519"}, keysAreHere).Reason

	if !strings.Contains(got, "`ssh -v leaf`") {
		t.Errorf("the remedy is %q — for an alias out of THIS machine's ssh config the alias is the "+
			"argument, and substituting a resolved login discards the name the operator chose", got)
	}
	if strings.Contains(got, "dev@leaf.internal") {
		t.Errorf("the remedy is %q — it resolved an alias that already resolves here", got)
	}
}

// A machine declared but never resolved has no login to name, and the honest act is the one the
// operator can perform: ask the machine where that name means something. Inventing a login out of the
// alias would name a host nobody mentioned, and saying `unreachable` is what invariant 4 forbids.
func TestACandidateWithNoResolvedRecipeIsSentToTheOnlyMachineItsNameResolvesOn(t *testing.T) {
	got := candidateFor(t, "depot-a", "vault-b", nil, noKeysHere).Reason

	if !strings.Contains(got, "`ssh -G vault-b`") || !strings.Contains(got, "depot-a") {
		t.Errorf("the remedy is %q — with no recipe the only act left is to resolve the name on the "+
			"machine that holds it, and the sentence must say which machine that is", got)
	}
	if strings.Contains(got, "ssh -v vault-b") {
		t.Errorf("the remedy is %q — it aims `ssh -v` at this machine using a name that does not "+
			"resolve here", got)
	}
	if strings.Contains(got, "@") {
		t.Errorf("the remedy is %q — it composed a login out of an alias, naming a host no recipe "+
			"reported", got)
	}
	if strings.Contains(got, "unreachable") {
		t.Errorf("the remedy is %q — `unreachable` names no act (spec §3.2 invariant 4)", got)
	}
}

// A port that is not 22 has to reach the command or the command goes to 22 and fails for a reason the
// operator cannot see; 22 must NOT be spelled out, since `ssh -G` always reports one and `-p 22` reads
// as a special case where there is none. The same pair `diagnose_test.go` asserts for `ssh-copy-id`,
// asserted here because this is a SECOND caller of `loginIn` and a fix that stopped calling it would
// otherwise lose the port silently.
func TestTheProbeCommandCarriesANonDefaultPortAndNotTheDefaultOne(t *testing.T) {
	with := func(port string) string {
		return candidateFor(t, "depot-a", "vault-b", map[string]string{"hostname": "vault-b.internal",
			"user": "dev", "port": port, "identityfile": "~/.ssh/id_ed25519"}, keysAreHere).Reason
	}
	if got := with("2222"); !strings.Contains(got, "`ssh -v -p 2222 dev@vault-b.internal`") {
		t.Errorf("the remedy %q does not carry the port — the command would go to 22", got)
	}
	if got := with("22"); strings.Contains(got, "-p ") {
		t.Errorf("the remedy %q spells out the default port", got)
	}
}

// MECHANICAL, over every shape the candidate sentence can take: THE ACT MUST END WHERE THE OPERATOR
// CAN STILL READ IT.
//
// The number is 74 and it is arithmetic, not taste: `internal/ui`'s `discoveredBlock` wraps a reason at
// `width - discoveredIndent`, the indent is 6, §16 commits to 80 columns, and the COMPACT form of the
// section keeps only the first of those wrapped lines. So an act that ends past column 74 is an act
// that is not on the screen at the one size this project promises. Measured, the sentence this replaces
// ended at column 80 for a four-column alias and at 112 for the operator's widest one — cut in every
// case, which is this repository's oldest defect class (keep the label, lose the action).
//
// ORDINARY values come from the real vocabulary rather than from imagination: the operator's own
// ~/.ssh/config declares 21 concrete aliases, the widest is `gitlab.storefront.eu` at 20 columns and
// the widest resolved login is 38 (`admin1@web-app.orchardpet.DawnBreather.net`). A value wider than the
// terminal cannot fit any complete sentence, so — following the convention `diagnose_test.go` writes
// down for the same reason — such a row is held to the VERB only, and the flag is per row rather than
// derived from a width, because a threshold nobody can justify is usually a check nobody ran.
func TestEveryCandidateActEndsWhereTheOperatorCanReadIt(t *testing.T) {
	const room = 74
	wide := strings.Repeat("w", 120)
	cases := []struct {
		name         string
		observer     string
		alias        string
		recipe       map[string]string
		pathological bool
	}{
		{name: "the root's own alias", observer: "", alias: "leaf"},
		{name: "the root's widest real alias", observer: "", alias: "gitlab.storefront.eu"},
		{name: "a hop's alias, resolved", observer: "depot-a", alias: "vault-b",
			recipe: map[string]string{"hostname": "vault-b.internal", "user": "dev"}},
		// The widest login this sentence has to carry, at 38 columns. It is INVENTED at that width
		// rather than copied from the operator's fleet, and the reason is measured: the real one was
		// `admin1@web-app.<two private labels>.net`, and the publication rename is not length-preserving
		// for the author's name, so the same fixture measured 42 columns in the public tree and this
		// case failed there for a reason that has nothing to do with the sentence. A width fixture must
		// state its width and own it.
		{name: "a hop's alias, widest real login", observer: "depot-a", alias: "gitlab.storefront.example",
			recipe: map[string]string{"hostname": "web-app.orchard-pet.example.net", "user": "admin1"}},
		{name: "a hop's alias, login with a port", observer: "depot-a", alias: "vault-b",
			recipe: map[string]string{"hostname": "vault-b.internal", "user": "dev", "port": "2222"}},
		{name: "a hop's alias, unresolved", observer: "depot-a", alias: "vault-b"},
		{name: "a hop's alias, unresolved, both names widest", observer: "gitlab.storefront.eu",
			alias: "gitlab.storefront.eu"},
		{name: "a hostname wider than the terminal", observer: "depot-a", alias: "vault-b",
			recipe: map[string]string{"hostname": wide, "user": "dev"}, pathological: true},
		{name: "an alias wider than the terminal", observer: "", alias: wide, pathological: true},
	}
	// Every act this sentence may offer, spelled as the verb the operator reads.
	verbs := []string{"probe it from here", "resolve it on "}
	for _, c := range cases {
		recipe := c.recipe
		if recipe == nil {
			recipe = map[string]string{}
		}
		got := candidateFor(t, c.observer, c.alias, recipe, keysAreHere).Reason
		if got == "" {
			t.Errorf("%s: a candidate with no remedy (spec §3.2 invariant 4)", c.name)
			continue
		}
		if strings.Contains(got, "  ") {
			t.Errorf("%s: %q has a gap where an absent field was interpolated", c.name, got)
		}
		// Runes are columns here: every fixture is ASCII.
		named := false
		for _, verb := range verbs {
			if strings.HasPrefix(got, verb) {
				named = true
			}
		}
		if !named {
			t.Errorf("%s: %q does not START with an act — a sentence that opens with the complaint "+
				"puts its command past the fold, which is what this check exists to prevent", c.name, got)
		}
		if c.pathological {
			continue
		}
		// The act is everything before the ` — ` that introduces the explanation, which is the same
		// shape `Diagnose`'s reasons use and the same cut `diagnose_test.go` makes.
		act, _, found := strings.Cut(got, " — ")
		if !found {
			t.Errorf("%s: %q has no ` — `, so nothing separates the act from the explanation", c.name, got)
			continue
		}
		if end := len([]rune(act)); end > room {
			t.Errorf("%s: the act ENDS at column %d, past the %d a wrapped reason has at the 80 "+
				"columns this project commits to — the operator reads a truncated command and cannot "+
				"run it. The act is %q", c.name, end, room, act)
		}
	}
}

// The three sentences that need nothing but the alias must not have acquired the observer, and the
// state each names must still be its OWN next move. `TestEachStatesRemedyNamesItsOwnNextMove` covers
// the needles; this covers the thing that changed — `defaultReason` now sees the whole observation, so
// a hop's name could leak into a sentence that is not about a hop at all.
func TestOnlyTheCandidateSentenceReadsTheObserver(t *testing.T) {
	o := Observation{Observer: "depot-a", Label: "vault-b"}
	for _, c := range []struct{ state State }{{Blocked}, {Ready}, {Available}} {
		got := defaultReason(c.state, o)
		if strings.Contains(got, "depot-a") {
			t.Errorf("%s's remedy is %q — it names the observer, and this sentence is about the "+
				"machine rather than about who declared it", c.state, got)
		}
		if !strings.Contains(got, "vault-b") {
			t.Errorf("%s's remedy %q does not name the machine it is about", c.state, got)
		}
	}
}

// A NEGATIVE breadth used to CRASH, and an exported function that takes a plain int must not: found
// by an adversarial reviewer, reproduced as `labels[:-1]` — a slice-bounds panic, not a refusal.
//
// Zero is the pole that matters beside it, because this package's zero value is deliberately the
// least-privileged one ("nothing is allowed", so an unfilled Budget cannot open a crawl). A negative
// therefore clamps to zero rather than to one: it must behave like the most restrictive setting, not
// like a slightly smaller one, and the cut it files must still name what was lost.
func TestANonsensicalBreadthIsRefusedRatherThanCrashing(t *testing.T) {
	for _, breadth := range []int{-1, 0, -99} {
		g := &Graph{}
		var got []string
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("MaxPerObserver=%d panicked: %v", breadth, r)
				}
			}()
			got = g.Allow(Budget{MaxDepth: 1, MaxPerObserver: breadth}, "hop", 0, []string{"leaf", "twin"})
		}()
		if len(got) != 0 {
			t.Errorf("MaxPerObserver=%d allowed %v, want nothing at all", breadth, got)
		}
		cuts := g.Cuts()
		if len(cuts) != 1 {
			t.Fatalf("MaxPerObserver=%d filed %d cuts, want exactly one — a crawl that drops "+
				"machines silently is indistinguishable from a fleet that ends there", breadth, len(cuts))
		}
		if cuts[0].Skipped != 2 {
			t.Errorf("MaxPerObserver=%d reported %d skipped, want 2 — the count is what makes the "+
				"cut actionable", breadth, cuts[0].Skipped)
		}
	}

	// And the opposite pole, so "returns nothing" above is not satisfied by an Allow that never
	// returns anything: a positive breadth still passes that many through.
	g := &Graph{}
	if got := g.Allow(Budget{MaxDepth: 1, MaxPerObserver: 1}, "hop", 0, []string{"leaf", "twin"}); len(got) != 1 {
		t.Errorf("MaxPerObserver=1 allowed %v, want exactly one label", got)
	}
}
