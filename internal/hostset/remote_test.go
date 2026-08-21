package hostset

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/fleet"
)

// hopFixture is a hop that answers two kinds of payload: the one that reads its ssh config, and
// one `ssh -G` per alias. It records every payload it was handed, in order, which is what lets a
// case assert that an alias was NEVER sent — the round trips are the cost the budget bounds and
// the hazard the alias check exists for.
//
// It matches a payload by asking the production builder rather than by restating the payload's
// shape, so a change to the payload cannot make this harness silently answer nothing. The cost of
// that is exact: NO case in this file can see a payload change, because the product and the harness
// move together. What answers for the payload's text is
// TestTheRemotePayloadsAreTheOnesMeasuredAgainstTheFleet, which writes both literals out, and
// TestTheRemotePayloadsSurviveANonPOSIXLoginShell, which reads their characters against an
// allow-list. Measured before those existed: three one-line payload mutants survived the whole
// suite, and each one takes every remote hop dark.
type hopFixture struct {
	config  string            // what `cat ~/.ssh/config` puts on stdout; empty means the file is absent
	resolve map[string]string // alias -> what `ssh -G <alias>` puts on stdout
	calls   []string          // every payload, in order
}

func (h *hopFixture) run(_ context.Context, _, payload string) (string, string, int) {
	h.calls = append(h.calls, payload)
	if payload == remoteConfigPayload {
		if h.config == "" {
			// Measured shape of a missing file: rc!=0, nothing on stdout. `nuc` really has
			// no ~/.ssh/config, so this is the commonest hop, not an edge case.
			return "", "cat: /home/dev/.ssh/config: No such file or directory", 1
		}
		return h.config, "", 0
	}
	for alias, out := range h.resolve {
		if payload == remoteResolvePayload(alias) {
			// ssh -G writes `Pseudo-terminal will not be allocated because stdin is not a
			// terminal.` to STDERR at rc=0 — captured 2026-08-20, OpenSSH 10.3p1. A resolver
			// that read stderr as failure would refuse every recipe it got.
			return out, "Pseudo-terminal will not be allocated because stdin is not a terminal.\n", 0
		}
	}
	return "", "ssh: Could not resolve hostname " + payload, 255
}

func (h *hopFixture) sentAnythingAbout(alias string) bool {
	for _, c := range h.calls {
		if c != remoteConfigPayload && strings.Contains(c, alias) {
			return true
		}
	}
	return false
}

func (h *hopFixture) resolveCalls() int {
	n := 0
	for _, c := range h.calls {
		if c != remoteConfigPayload {
			n++
		}
	}
	return n
}

// The `ssh -G` fixtures below are CAPTURED from OpenSSH 10.3p1 on 2026-08-20, run against a
// hand-written config declaring exactly these two stanzas, so the key order, the lower-casing, the
// leading `host` line and the five default `identityfile` lines are ssh's and not mine. This
// repository has twice paid for a fixture written from imagination.
const (
	// A stanza that names its own key: exactly ONE identityfile line, because a configured
	// IdentityFile SUPPRESSES ssh's defaults. This is the `Blocked` case the fleet spec is
	// built around — the hop's own credential, which the root does not hold.
	resolvedLeaf = `host leaf
user dev
hostname leaf.internal
port 22
addressfamily any
batchmode no
identityfile ~/.ssh/hop-only
ipqos ef cs0
rekeylimit 0 0
`
	// A stanza with a ProxyJump and NO IdentityFile: ssh prints its five built-in defaults and
	// `proxyjump`. The proxyjump line is the load-bearing one (spec §2.2.1) — a fingerprint from
	// this connection belongs to `hop`, and attributing it to `behind` would fuse two machines
	// into one node under §2.3's set-intersection merging.
	resolvedBehind = `host behind
user dev
hostname behind.internal
port 22
identityfile ~/.ssh/id_rsa
identityfile ~/.ssh/id_ecdsa
identityfile ~/.ssh/id_ecdsa_sk
identityfile ~/.ssh/id_ed25519
identityfile ~/.ssh/id_ed25519_sk
proxyjump hop
`
)

func candidateFor(t *testing.T, got []Candidate, alias string) Candidate {
	t.Helper()
	for _, c := range got {
		if c.Alias == alias {
			return c
		}
	}
	t.Fatalf("no candidate for %q; got %+v", alias, got)
	return Candidate{}
}

// Measured: the operator's own `nuc` has no ~/.ssh/config at all, so a hop offering nothing is the
// commonest hop there is. Nothing to offer is not a failure.
func TestAHopWithNoSSHConfigContributesNothingAndDoesNotFail(t *testing.T) {
	h := &hopFixture{}
	got, err := RemoteCandidates(context.Background(), h.run, "hop")
	if err != nil {
		t.Fatalf("a hop with no ssh config is not an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("invented %d candidates from a hop with no config: %+v", len(got), got)
	}
	if h.resolveCalls() != 0 {
		t.Errorf("spent %d resolve round trips on a hop with nothing to resolve", h.resolveCalls())
	}
}

// The whole product of this task: an alias the hop declares, carried back with the hop's name on it
// and the recipe ssh resolved ON the hop.
func TestAHopsAliasArrivesWithTheHopsNameAndItsResolvedRecipe(t *testing.T) {
	h := &hopFixture{
		config:  "Host leaf\n  HostName leaf.internal\n  IdentityFile ~/.ssh/hop-only\n",
		resolve: map[string]string{"leaf": resolvedLeaf},
	}
	got, err := RemoteCandidates(context.Background(), h.run, "hop")
	if err != nil {
		t.Fatalf("RemoteCandidates: %v", err)
	}
	c := candidateFor(t, got, "leaf")
	if c.Skip != "" {
		t.Fatalf("leaf was skipped: %s", c.Skip)
	}
	if c.Via != "hop" {
		t.Errorf("Via = %q, want %q — a recipe is only meaningful on the machine holding the key it names", c.Via, "hop")
	}
	for key, want := range map[string]string{
		"hostname":     "leaf.internal",
		"user":         "dev",
		"port":         "22",
		"identityfile": "~/.ssh/hop-only",
	} {
		if c.Recipe[key] != want {
			t.Errorf("Recipe[%q] = %q, want %q — this is what the Blocked diagnosis reads", key, c.Recipe[key], want)
		}
	}
	// A recipe that carries ssh's whole answer would be sixty keys of noise; one that drops the
	// keys the diagnosis reads is worse. Nothing else is kept.
	for _, unwanted := range []string{"host", "addressfamily", "batchmode", "ipqos", "rekeylimit"} {
		if _, held := c.Recipe[unwanted]; held {
			t.Errorf("Recipe holds %q, which no consumer reads", unwanted)
		}
	}
}

// Spec §2.2.1, the sharpest hazard in the design: a proxied connection reports the JUMP host's key.
// The recipe is the only place the crawl can learn that, so if `proxyjump` does not survive into it,
// the destination silently inherits the jump's fingerprint and two machines collapse into one node.
func TestAProxiedRecipeCarriesTheProxyKeySoTheJumpsFingerprintIsNotAttributedToIt(t *testing.T) {
	h := &hopFixture{
		config:  "Host behind\n  ProxyJump hop\n",
		resolve: map[string]string{"behind": resolvedBehind},
	}
	got, err := RemoteCandidates(context.Background(), h.run, "hop")
	if err != nil {
		t.Fatalf("RemoteCandidates: %v", err)
	}
	c := candidateFor(t, got, "behind")
	if c.Recipe["proxyjump"] != "hop" {
		t.Errorf("Recipe[\"proxyjump\"] = %q, want %q — without it a fingerprint from this "+
			"connection would be attributed to behind rather than to the jump (spec §2.2.1)",
			c.Recipe["proxyjump"], "hop")
	}
	// `identityfile` carries EVERY line ssh printed, joined by newlines in ssh's try order, because
	// the key answers "which credentials would ssh offer" and ssh offers each in turn. This
	// assertion used to demand the FIRST line alone, and in doing so it pinned a live defect: on a
	// machine whose only key is `id_ed25519` — the operator's own, measured — the recipe named
	// `~/.ssh/id_rsa`, `fleet.Diagnose` found no key here, and the row advertised
	// `copy ~/.ssh/id_rsa to this machine` for a machine ssh would have reached on its fourth
	// default. The want is written out as a LITERAL rather than joined from a slice in the fixture,
	// so a producer that dropped a line cannot satisfy it by dropping the same line twice.
	const wantKeys = "~/.ssh/id_rsa\n~/.ssh/id_ecdsa\n~/.ssh/id_ecdsa_sk\n~/.ssh/id_ed25519\n~/.ssh/id_ed25519_sk"
	if c.Recipe["identityfile"] != wantKeys {
		t.Errorf("Recipe[\"identityfile\"] = %q, want all five ssh listed:\n%q",
			c.Recipe["identityfile"], wantKeys)
	}
}

// A ProxyCommand's value contains spaces, so a parse that split the whole line on whitespace would
// keep `docker` and lose the command. Measured on the live fleet, `cortex-web`'s recipe is
// `ProxyCommand docker exec -i tailscale-cortex nc %h %p`.
func TestAProxyCommandKeepsItsWholeValue(t *testing.T) {
	const want = "docker exec -i tailscale-cortex nc %h %p"
	h := &hopFixture{
		config:  "Host proxied\n",
		resolve: map[string]string{"proxied": "hostname proxied\nproxycommand " + want + "\n"},
	}
	got, err := RemoteCandidates(context.Background(), h.run, "hop")
	if err != nil {
		t.Fatalf("RemoteCandidates: %v", err)
	}
	if c := candidateFor(t, got, "proxied"); c.Recipe["proxycommand"] != want {
		t.Errorf("Recipe[\"proxycommand\"] = %q, want %q", c.Recipe["proxycommand"], want)
	}
}

// The payloads, as LITERALS. Every other case in this file matches a payload by asking the
// production builder, so a typo in a constant moves the product and the harness together and no
// case can see it: measured, `cat ~/.ssh/cfg-typo` passed the whole suite, and so did
// `cat /etc/ssh/ssh_config` and a `ssh -G` that lost its space — which OpenSSH 10.3p1 answers with a
// usage dump. In production each one makes every hop answer nothing, and a hop answering nothing is
// the case RemoteCandidates has to classify, so a payload typo would surface as "this hop could not
// be read" on every hop in the fleet at once.
//
// Written out rather than composed from the constants, because a test whose expected value comes
// from the code under test asserts only that assignment works.
func TestTheRemotePayloadsAreTheOnesMeasuredAgainstTheFleet(t *testing.T) {
	if got, want := remoteConfigPayload, "cat ~/.ssh/config"; got != want {
		t.Errorf("the enumeration payload is %q, want %q", got, want)
	}
	if got, want := remoteResolvePayload("leaf"), "ssh -G leaf"; got != want {
		t.Errorf("the resolve payload is %q, want %q", got, want)
	}
}

// Both payloads run in the hop's LOGIN shell, and one host on this fleet runs Nushell, where a
// quoted program name is a parse error AT rc=0 — which is how that host was invisible for weeks. An
// rc-keyed check cannot see that failure, so the guard is on the payload's own text.
//
// It is an ALLOW-LIST, for the reason unpasteableAlias states in production about aliases: a
// deny-list of shell metacharacters is a claim about every shell that will ever run this, and there
// is no such claim to be had. This case used to BE that deny-list — eight literals — and it let
// `>`, `2>`, `|`, `&`, `(` and `{` straight through: the mutant `cat ~/.ssh/config 2>/dev/null`
// survived it, and Nushell spells that redirection `e>` and refuses the line at rc=0, which is
// exactly the failure the case exists to prevent.
//
// The permitted set is not unpasteableAlias's set, and the difference is deliberate: a payload is a
// whole command, so it contains a space and a path, and an alias must contain neither. The last case
// below is what keeps the two from drifting apart — a payload built from the gnarliest alias the
// production rule ADMITS must still pass this rule.
func TestTheRemotePayloadsSurviveANonPOSIXLoginShell(t *testing.T) {
	permitted := func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return true
		case r == ' ', r == '-', r == '.', r == '/', r == '_', r == '~', r == '@', r == ':':
			return true
		}
		return false
	}
	// An alias made of every character class unpasteableAlias admits, so this case fails if the
	// two rules ever stop composing.
	const gnarliest = "Host-1.example_x@jump:2222"
	if why := unpasteableAlias(gnarliest); why != "" {
		t.Fatalf("the fixture is not an alias the production rule admits: %s", why)
	}
	payloads := map[string]string{
		"the config read":                 remoteConfigPayload,
		"the resolve":                     remoteResolvePayload("leaf"),
		"the resolve of the widest alias": remoteResolvePayload(gnarliest),
	}
	for name, payload := range payloads {
		// A floor that can actually fire, unlike a count over a literal map: an allow-list is
		// satisfied by NO characters at all, so an empty payload passes it while asking the hop
		// nothing.
		if strings.TrimSpace(payload) == "" {
			t.Errorf("the %s payload is empty, which asks the hop nothing", name)
		}
		for _, r := range payload {
			if !permitted(r) {
				t.Errorf("%s payload %q contains %q, which is not in the set every shell on this "+
					"fleet treats as an ordinary character", name, payload, r)
			}
		}
	}
}

// The cap's VALUE, written out. The case below is a test of the MECHANISM and holds for any cap: it
// builds its fixture from the constant and counts round trips against the constant, so it passes for
// a cap of 2 as readily as for 32 — measured, `MaxAliasesPerHop = 2` survived the whole suite while
// declaring 5, resolving 2 and cutting 3, with the cut reason still carrying its count.
//
// 32 is CHOSEN, not measured, and this is not a measurement — it is the number the comment beside the
// constant argues for, made unmutable without a deliberate edit. The one measurement in that argument
// is the floor it has to clear: the largest real config on this fleet declares 21 probeable aliases
// (2026-08-20, ParseSSHConfig over the operator's own files), so a cap below 21 would cut a hop this
// fleet already has, and the cut is per-hop so the arithmetic is not per-fleet.
func TestTheBudgetsCapIsTheNumberThatWasChosen(t *testing.T) {
	if MaxAliasesPerHop != 32 {
		t.Errorf("MaxAliasesPerHop = %d, want 32 — the number is a decision, and 21 is the largest "+
			"real config this fleet has measured, so a change to it is a change to whether a hop "+
			"this fleet ALREADY has gets crawled whole", MaxAliasesPerHop)
	}
}

// THE FOLD, and it is the half a wording change would otherwise slip through. Two producers used to
// build the "this observer declares more than the crawl looked at" sentence with two sets of words —
// fleet.Graph.Allow for a crawl the graph cut, and budgetCut here for a hop whose aliases this
// package would not resolve. One decision in two places is what this repository has paid for three
// times in a single day, so there is one builder now.
//
// It compares TWO PRODUCERS rather than a producer against a literal: an expectation copied from
// fleet.BreadthCut would pass against a mutant that blanked the sentence in both places, while this
// goes red the moment either side is reworded on its own. Both arms are production code.
func TestTheCutSentenceIsTheSameOneTheGraphFiles(t *testing.T) {
	labels := make([]string, MaxAliasesPerHop+3)
	for i := range labels {
		labels[i] = "m" + strconv.Itoa(i)
	}
	g := fleet.New()
	// MaxDepth is generous so the DEPTH arm cannot answer for the breadth arm: a depth cut is a
	// different fact with its own sentence, and it would satisfy a bare "some cut was filed".
	g.Allow(fleet.Budget{MaxDepth: 9, MaxPerObserver: MaxAliasesPerHop}, "hop", 1, labels)
	cuts := g.Cuts()
	if len(cuts) != 1 {
		t.Fatalf("the graph filed %d cuts for a list %d past its breadth", len(cuts), 3)
	}
	if got := budgetCut("hop", len(labels)); got != cuts[0].Why {
		t.Errorf("the two producers of one sentence have drifted:\n  hostset: %q\n  fleet:   %q", got, cuts[0].Why)
	}
}

// And the NUMBER is one number. Until this commit the per-hop cap was a second literal 32 beside a
// comment promising the fold; a change to one of two literals is invisible, and the whole point of
// folding them is that it cannot be.
func TestTheHopsCapIsTheGraphsOwnBreadthBudget(t *testing.T) {
	if MaxAliasesPerHop != fleet.DefaultBreadth {
		t.Errorf("MaxAliasesPerHop = %d and the graph's breadth budget is %d — two numbers for one "+
			"decision", MaxAliasesPerHop, fleet.DefaultBreadth)
	}
	// The budget a caller is handed carries the same figure, so a crawl configured from
	// fleet.DefaultBudget() and a hop capped here look at the same number of machines.
	if got := fleet.DefaultBudget().MaxPerObserver; got != MaxAliasesPerHop {
		t.Errorf("fleet.DefaultBudget() offers a breadth of %d against this package's %d", got, MaxAliasesPerHop)
	}
}

// A silent horizon reads as a finished crawl (spec §3.3), so a budget that cuts must say how many
// it cut. Coverage is the other half: what was cut is still RETURNED, carrying the reason, because
// spec §5 point 1 requires that nothing declared is absent from the result.
func TestABudgetCutIsReportedWithItsCountAndCutsNothingFromTheResult(t *testing.T) {
	const over = 3
	var lines []string
	declared := MaxAliasesPerHop + over
	// Every alias resolves, so the ONLY reason a row can carry is the cut. Left unresolvable they
	// all carry "ssh -G resolved no recipe" instead, and this case counted 35 cuts where there are
	// three — a fixture defect that reads exactly like a broken budget.
	resolve := map[string]string{}
	for i := 0; i < declared; i++ {
		alias := "m" + strconv.Itoa(i)
		lines = append(lines, "Host "+alias)
		resolve[alias] = "hostname " + alias + ".internal\nuser dev\nport 22\n"
	}
	h := &hopFixture{config: strings.Join(lines, "\n") + "\n", resolve: resolve}
	got, err := RemoteCandidates(context.Background(), h.run, "hop")
	if err != nil {
		t.Fatalf("RemoteCandidates: %v", err)
	}
	if len(got) != declared {
		t.Fatalf("got %d candidates for %d declared aliases — a cut must not remove a row", len(got), declared)
	}
	if h.resolveCalls() != MaxAliasesPerHop {
		t.Errorf("spent %d resolve round trips, want %d — the cap is what bounds the crawl",
			h.resolveCalls(), MaxAliasesPerHop)
	}
	cut := 0
	for _, c := range got {
		if c.Skip == "" {
			continue
		}
		cut++
		// Space-delimited, because a bare `Contains(skip, "3")` also matches the 35 and the 32
		// the same sentence carries, and would pass against a reason that named no count at all.
		if !strings.Contains(c.Skip, " "+strconv.Itoa(over)+" ") {
			t.Errorf("the cut reason does not carry its count %d: %q", over, c.Skip)
		}
		if strings.Contains(c.Skip, "unreachable") {
			t.Errorf("`unreachable` is not a reason (spec §3.2 invariant 4): %q", c.Skip)
		}
	}
	if cut != over {
		t.Errorf("%d candidates carry a cut reason, want %d", cut, over)
	}
}

// The alias comes off ANOTHER machine's config file and goes into a command that runs in that
// machine's login shell, where nothing can be quoted (see the Nushell case above). So an alias that
// is not safe to paste must be refused HERE, before the round trip, and must carry the refusal.
func TestAnAliasThatCannotBePastedIsRefusedBeforeItReachesTheHop(t *testing.T) {
	for _, tc := range []struct{ alias, why string }{
		{"leaf;id", "a command separator"},
		{"leaf$HOME", "a shell expansion"},
		{"-oProxyCommand=x", "a leading dash ssh would read as a flag"},
		{"leaf name", "a space, so ssh -G would receive two arguments"},
	} {
		h := &hopFixture{config: "Host " + tc.alias + "\n"}
		got, err := RemoteCandidates(context.Background(), h.run, "hop")
		if err != nil {
			t.Fatalf("%s: %v", tc.alias, err)
		}
		// `leaf name` is two names on one Host line, which is legal ssh — so the guard is not
		// that the candidate is absent, it is that nothing about it was sent to the hop.
		if h.sentAnythingAbout(tc.alias) {
			t.Errorf("%q (%s) was pasted into a command on the hop: %q", tc.alias, tc.why, h.calls)
		}
		for _, c := range got {
			if c.Skip == "" {
				t.Errorf("%q (%s) is offered for probing with no reason recorded", c.Alias, tc.why)
			}
		}
	}
}

// The allow-list's CONTENTS, both poles, because the case above pins four rejections and nothing
// else — measured, widening the rule with `~`, `!` and `#` survived the whole suite. This is the
// function whose entire argument is that the rule must be an allow-list, so what it ADMITS is the
// claim, and the admitted set is the one that has to be written down.
//
// The rejected column is chosen from what a shell does with each character rather than from a list
// of "dangerous" ones, since that ordering is the deny-list this function refuses to be: `~` is a
// home-directory expansion, `#` starts a comment in every shell here AND in an ssh config, `!` is
// history expansion in an interactive shell, and the rest change where a command's words go. Three
// of them (`!`, `*`, `%`) are also caught upstream by skipReason — asserted here anyway, because
// this function's contract is its own and a caller that stops running skipReason first must not
// silently widen it.
func TestTheAliasRuleAdmitsAnSSHAliasAndNothingElse(t *testing.T) {
	admitted := []struct{ alias, why string }{
		{"leaf", "lower case, the ordinary case"},
		{"LEAF", "upper case, which ssh config keys are matched case-insensitively but names are not"},
		{"leaf2", "a digit"},
		{"leaf.internal", "a dot, because an alias is very often a hostname"},
		{"web-01", "a dash inside the name, which is not the leading dash ssh reads as a flag"},
		{"db_2", "an underscore"},
		{"dev@leaf", "an at sign, the user@host shape ssh accepts as a target"},
		{"leaf:2222", "a colon, the host:port shape"},
	}
	for _, tc := range admitted {
		if why := unpasteableAlias(tc.alias); why != "" {
			t.Errorf("%q (%s) is refused, and it is an alias ssh accepts: %s", tc.alias, tc.why, why)
		}
	}
	refused := []struct{ alias, why string }{
		{"leaf~x", "a tilde, which every shell here expands"},
		{"leaf#x", "a hash, which starts a comment"},
		{"leaf!x", "a bang, history expansion"},
		{"leaf$x", "a dollar, a variable"},
		{"leaf;x", "a semicolon, a command separator"},
		{"leaf x", "a space, so the hop's shell would see two arguments"},
		{"leaf|x", "a pipe"},
		{"leaf>x", "a redirection"},
		{"leaf&x", "a background operator"},
		{"leaf(x", "a subshell, and in Nushell the start of a block"},
		{"leaf'x", "a quote, which is what these payloads cannot use"},
		{"leaf`x", "a backtick, command substitution"},
		{"leaf\\x", "a backslash, an escape"},
		{"leaf*x", "a glob"},
		{"leaf%x", "a percent, which tmux's own seam refuses too"},
		{"leaf\tx", "a tab"},
		{"leaf\nx", "a newline, which would make the payload two commands"},
	}
	for _, tc := range refused {
		why := unpasteableAlias(tc.alias)
		if why == "" {
			t.Errorf("%q (%s) would be pasted into a command on a hop's login shell", tc.alias, tc.why)
			continue
		}
		// The refusal has to name the character, because the operator is being asked to rename
		// something in a config on ANOTHER machine and a reason that does not say which character
		// is the complaint invariant 4 forbids.
		if !strings.Contains(why, strconv.QuoteRune([]rune(tc.alias)[4])) {
			t.Errorf("the refusal of %q does not name the character it refused: %s", tc.alias, why)
		}
	}
	// Floors, because both loops are matchers and a matcher over an empty table passes having
	// checked nothing. Each is a count of CLASSES, not of cells: five admitted classes (letters,
	// digits and the four punctuation marks) and one per rejected character.
	if len(admitted) < 8 || len(refused) < 17 {
		t.Fatalf("checked %d admitted and %d refused aliases", len(admitted), len(refused))
	}
}

// ParseSSHConfig already refuses a pattern, and a hop's config is read by the same rule — one
// predicate, not a second copy that drifts the day the first one changes.
func TestAPatternInAHopsConfigIsSkippedRatherThanResolved(t *testing.T) {
	h := &hopFixture{
		config:  "Host *.internal\n  ProxyJump hop\nHost leaf\n",
		resolve: map[string]string{"leaf": resolvedLeaf},
	}
	got, err := RemoteCandidates(context.Background(), h.run, "hop")
	if err != nil {
		t.Fatalf("RemoteCandidates: %v", err)
	}
	if c := candidateFor(t, got, "*.internal"); c.Skip == "" {
		t.Error("a pattern was offered for probing")
	}
	if h.sentAnythingAbout("*.internal") {
		t.Errorf("a pattern was resolved on the hop: %q", h.calls)
	}
	if h.resolveCalls() != 1 {
		t.Errorf("spent %d resolve round trips, want 1 (only `leaf` is a machine)", h.resolveCalls())
	}
}

// An `Include` in the hop's config names declarations the hub cannot see: following it would need a
// remote glob, and reads are all we do. Saying nothing about it is the defect spec §3.2 invariant 4
// names — a graph that silently omits is indistinguishable from one that finished.
func TestAHopsIncludeIsReportedRatherThanSilentlyDropped(t *testing.T) {
	h := &hopFixture{
		config:  "Include conf.d/*.conf\nHost leaf\n",
		resolve: map[string]string{"leaf": resolvedLeaf},
	}
	got, err := RemoteCandidates(context.Background(), h.run, "hop")
	if err != nil {
		t.Fatalf("RemoteCandidates: %v", err)
	}
	c := candidateFor(t, got, "conf.d/*.conf")
	if c.Skip == "" {
		t.Fatal("the include was carried as a machine rather than as a horizon")
	}
	if !strings.Contains(c.Skip, "conf.d/*.conf") {
		t.Errorf("the reason does not name what was not read: %q", c.Skip)
	}
	if c.Via != "hop" {
		t.Errorf("Via = %q, want %q — the horizon belongs to the hop that declared it", c.Via, "hop")
	}
	if h.sentAnythingAbout("conf.d") {
		t.Errorf("the hub tried to resolve an include glob as a host: %q", h.calls)
	}
}

// An empty answer because we could not ASK is not an empty answer because there was nothing to
// offer, and the two must not read alike: the first is the silent horizon, the second is `nuc`.
func TestAnEnumerationThatCouldNotBeMadeIsAnErrorRatherThanAnEmptyHop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := &hopFixture{}
	got, err := RemoteCandidates(ctx, h.run, "hop")
	if err == nil {
		t.Fatalf("a hop we could not ask answered like a hop with nothing to offer: %+v", got)
	}
	if !strings.Contains(err.Error(), "hop") {
		t.Errorf("the error does not name the hop it is about: %v", err)
	}
}

// A hop we could not ASK and a hop with NOTHING TO OFFER put the same thing on stdout — nothing —
// and this function's own doc comment promises to tell them apart. Only a cancelled context did:
// every cell below answered `nil, nil`, which is exactly what `nuc` answers, so a refused
// connection, a dead ssh master, a permission denial and a login shell refusing the payload at rc=0
// all read as "this hop declares no machines". That is the silent horizon wearing the commonest
// hop's clothes.
//
// The two hop-said-so cells are MEASURED, 2026-08-20 on GNU coreutils' `cat`: an ABSENT file is
// rc=1 with `cat: <path>: No such file or directory` on stderr, and an EMPTY file is rc=0 with both
// streams silent. Also measured on the same run, and why the predicate must not settle for the rc:
// `Is a directory` and `Permission denied` are rc=1 too, and both mean the declarations may be
// there and unread.
//
// The dead-master cell is the one that earns the second clause. ssh's own text for a dead control
// socket ENDS IN `No such file or directory`, and this repository already has a scar for matching
// that tail (2026-08-13: a socket taxonomy matched it and threw the remedy away), so a predicate
// keyed on those words alone would file the worst failure there is under the commonest success.
func TestAHopWeCouldNotAskIsDistinguishedFromAHopWithNothingToOffer(t *testing.T) {
	for _, tc := range []struct {
		name, stdout, stderr string
		rc                   int
		wantErr              bool
	}{
		{
			name:   "the config is absent, which is the commonest hop there is",
			stderr: "cat: /home/dev/.ssh/config: No such file or directory\n", rc: 1,
		},
		{
			name: "the config is there and empty", rc: 0,
		},
		{
			name:   "the connection was refused",
			stderr: "ssh: connect to host hop port 22: Connection refused\r\n", rc: 255,
			wantErr: true,
		},
		{
			name:   "the ssh master is dead, and ssh's words END IN the absent-file sentence",
			stderr: "Control socket connect(/run/user/1000/hop.sock): No such file or directory\r\n",
			rc:     255, wantErr: true,
		},
		{
			name:   "the login shell refused the payload AT rc=0, which is the Nushell class",
			stderr: "Error: nu::parser::parse_mismatch\n", rc: 0, wantErr: true,
		},
		{
			name:   "the config is there and unreadable",
			stderr: "cat: /home/dev/.ssh/config: Permission denied\n", rc: 1, wantErr: true,
		},
		{
			name:   "the hop answered, which is the pole that says the predicate is not just refusing",
			stdout: "Host leaf\n", rc: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := func(context.Context, string, string) (string, string, int) {
				return tc.stdout, tc.stderr, tc.rc
			}
			got, err := RemoteCandidates(context.Background(), run, "hop")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("a hop we could not ask answered like a hop with nothing to offer: %+v", got)
				}
				// The ENUMERATE step must quote the hop the way the RESOLVE step already does.
				// Without the words there is no remedy, and the operator is told only that a
				// machine list they cannot see is missing.
				if !strings.Contains(err.Error(), "hop") {
					t.Errorf("the error does not name the hop it is about: %v", err)
				}
				if first := strings.SplitN(strings.TrimSpace(tc.stderr), "\n", 2)[0]; !strings.Contains(err.Error(), strings.TrimSuffix(first, "\r")) {
					t.Errorf("the error does not quote what the hop said (%q): %v", tc.stderr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("a hop that answered honestly was reported as a failure: %v", err)
			}
			if tc.stdout == "" && len(got) != 0 {
				t.Errorf("invented %d candidates from a hop with nothing to offer: %+v", len(got), got)
			}
			if tc.stdout != "" && len(got) == 0 {
				t.Error("a hop that answered with a Host line contributed no candidate")
			}
		})
	}
}

// The context dying partway through leaves the rest unresolved, and those rows must say so rather
// than come back looking like machines with no recipe.
func TestResolutionStoppingEarlyIsReportedOnTheRowsItDidNotReach(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := &hopFixture{
		config:  "Host one\nHost two\nHost three\n",
		resolve: map[string]string{"one": resolvedLeaf, "two": resolvedLeaf, "three": resolvedLeaf},
	}
	// Die after the enumeration and the first resolve have been paid for.
	stop := func(c context.Context, hop, payload string) (string, string, int) {
		out, errOut, rc := h.run(c, hop, payload)
		if h.resolveCalls() == 1 {
			cancel()
		}
		return out, errOut, rc
	}
	got, err := RemoteCandidates(ctx, stop, "hop")
	if err != nil {
		t.Fatalf("a partial resolve is a partial answer, not an error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want 3", len(got))
	}
	unresolved := 0
	for _, c := range got {
		if len(c.Recipe) > 0 {
			continue
		}
		unresolved++
		if c.Skip == "" {
			t.Errorf("%q has no recipe and no reason — it reads as a machine we resolved to nothing", c.Alias)
		}
	}
	if unresolved != 2 {
		t.Errorf("%d rows went unresolved, want 2", unresolved)
	}
}

// ssh answers for ANY string (measured: `definitely-not-a-host-xyz` resolves), so a hop that
// answers nothing at all is a failure of the round trip, not a machine with an empty recipe — and
// the row must quote the hop's own words rather than come back looking resolved.
func TestAnAliasTheHopCouldNotResolveCarriesTheHopsOwnSentence(t *testing.T) {
	h := &hopFixture{config: "Host leaf\n"} // no resolve entry: the fixture refuses at rc=255
	got, err := RemoteCandidates(context.Background(), h.run, "hop")
	if err != nil {
		t.Fatalf("one unresolvable alias is not a failed hop: %v", err)
	}
	c := candidateFor(t, got, "leaf")
	if len(c.Recipe) != 0 {
		t.Errorf("Recipe = %+v, want none", c.Recipe)
	}
	if !strings.Contains(c.Skip, "Could not resolve hostname") {
		t.Errorf("the reason does not quote what the hop said: %q", c.Skip)
	}
}

// The other half of Via's contract, and the only half no hop can demonstrate: the ROOT's own
// candidates carry NO observer. There is exactly one root (fleet spec §3.1) and it has no name for
// itself in this vocabulary, so `Via == ""` IS the root and every consumer keying a candidate reads
// it that way.
//
// Without this the field has a one-sided test: nine cases assert a hop's candidate carries the hop,
// and a producer that stamped `Via: "local"` on the root's own would pass all nine while turning
// the root into a fourteenth observer — the same machine appearing under two identities, which is
// the collapse §2.1 and §3.2 invariant 1 exist to prevent.
func TestTheRootsOwnCandidatesCarryNoObserver(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "config")
	if err := os.WriteFile(user, []byte("Host leaf\n  HostName leaf.internal\nHost *.internal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ParseSSHConfig(user, filepath.Join(dir, "no-system-config"))
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2 — the fixture, not the rule, is what failed: %+v", len(got), got)
	}
	for _, c := range got {
		if c.Via != "" {
			t.Errorf("the root's own %q carries Via %q — an alias from the root's config has no "+
				"observer, and a name there makes the root an observer of itself", c.Alias, c.Via)
		}
	}
}

// A hop's config is written by somebody else, and ssh accepts three separators on a keyword line
// where this package read only one. MEASURED on OpenSSH 10.3p1, 2026-08-20, one config holding all
// three stanzas: `Host<TAB>tabbed`, `Host=equals` and `Host spaced` ALL resolve —
// `ssh -F cfg -G tabbed` answers `hostname tabbed.example`, and likewise for the other two.
//
// The failure this guards is the worst shape there is on this path: a hop whose Host lines use a
// tab enumerates to NOTHING, and `RemoteCandidates` then answers `nil, nil` — which is exactly what
// a hop with no config at all answers, so a whole machine list goes missing while reading as `nuc`.
// That is the silent horizon fleet spec §3.2 invariant 4 forbids, arriving through the parser
// instead of through the budget.
//
// Each case is asserted through RemoteCandidates rather than against hostNamesIn, because the
// operator's seam is the crawl and this repository has paid for confirming a composition that does
// not exist by measuring its halves in turn.
func TestAHopsHostLineIsReadWithEverySeparatorSSHAccepts(t *testing.T) {
	for _, tc := range []struct{ name, config string }{
		{"a tab", "Host\tleaf\n\tHostName leaf.internal\n"},
		{"an equals", "Host=leaf\n  HostName leaf.internal\n"},
		{"an equals with spaces around it", "Host = leaf\n  HostName leaf.internal\n"},
		{"a space, the shape already covered", "Host leaf\n  HostName leaf.internal\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &hopFixture{config: tc.config, resolve: map[string]string{"leaf": resolvedLeaf}}
			got, err := RemoteCandidates(context.Background(), h.run, "hop")
			if err != nil {
				t.Fatalf("RemoteCandidates: %v", err)
			}
			if len(got) == 0 {
				t.Fatalf("a hop declaring `%s` enumerated to nothing, which reads as a hop with "+
					"no config at all", strings.SplitN(tc.config, "\n", 2)[0])
			}
			// The EXACT set, because the separator has two failure directions and only one of
			// them is an absence. Measured before the fix: `Host = leaf` yielded `leaf` AND a
			// candidate literally named `=`, a machine that does not exist carrying a refusal
			// that blames the operator's config for a separator ssh accepts. A test that only
			// looked up `leaf` passed against it.
			var aliases []string
			for _, c := range got {
				aliases = append(aliases, c.Alias)
			}
			if len(aliases) != 1 || aliases[0] != "leaf" {
				t.Fatalf("candidates = %q, want exactly [leaf] — the separator became a machine", aliases)
			}
			c := candidateFor(t, got, "leaf")
			if c.Skip != "" {
				t.Fatalf("leaf was skipped: %s", c.Skip)
			}
			if c.Recipe["hostname"] != "leaf.internal" {
				t.Errorf("Recipe[%q] = %q, want %q — the alias was found but never resolved",
					"hostname", c.Recipe["hostname"], "leaf.internal")
			}
		})
	}
}

// ssh's config parser accepts DOUBLE QUOTES around a name, and this package split on whitespace, so
// one quoted alias became two names that are not aliases at all. It is the same failure direction as
// the `Host = leaf` separator bug: a row per garbage name, each carrying a refusal that blames the
// operator's config for syntax ssh accepts.
//
// MEASURED on OpenSSH 10.3p1, 2026-08-20, one config holding both stanzas:
//   - `Host "web"` → `ssh -F cfg -G web` answers that stanza's `hostname`, so ssh strips the quotes
//     and the alias is `web`. `ssh -G '"web"'` is rc=255 `hostname contains invalid characters`, so
//     keeping the quotes produced a name no probe could ever use.
//   - `Host "my host"` → `ssh -G 'my host'` is rc=255 with the same sentence, so a quoted name
//     carrying a space is unusable to ssh itself. One row refused for its space is therefore the
//     honest answer, and it is the answer the alias rule already gives.
func TestAQuotedHostLineIsOneAliasRatherThanTwoBrokenOnes(t *testing.T) {
	t.Run("a quoted name is the name without the quotes", func(t *testing.T) {
		h := &hopFixture{
			config:  "Host \"leaf\"\n  HostName leaf.internal\n",
			resolve: map[string]string{"leaf": resolvedLeaf},
		}
		got, err := RemoteCandidates(context.Background(), h.run, "hop")
		if err != nil {
			t.Fatalf("RemoteCandidates: %v", err)
		}
		var aliases []string
		for _, c := range got {
			aliases = append(aliases, c.Alias)
		}
		if len(aliases) != 1 || aliases[0] != "leaf" {
			t.Fatalf("candidates = %q, want exactly [leaf] — ssh resolves this stanza under `leaf`", aliases)
		}
		if c := candidateFor(t, got, "leaf"); c.Skip != "" {
			t.Errorf("the alias ssh accepts was refused: %s", c.Skip)
		}
	})

	// The splitter has TWO producers — a hop's config and the root's own — and a rule tested
	// through one of them is a rule half tested. On the root's path the quotes were not even
	// refused: skipReason has no opinion about them, so `"web"` was handed to the PROBE, and ssh
	// answers that name with rc=255 `hostname contains invalid characters`. The machine was
	// declared, present in the picker, and unprobeable.
	t.Run("and the root's own config is read by the same rule", func(t *testing.T) {
		dir := t.TempDir()
		user := filepath.Join(dir, "config")
		if err := os.WriteFile(user, []byte("Host \"web\"\n  HostName web.internal\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got := ParseSSHConfig(user, filepath.Join(dir, "no-system-config"))
		if len(got) != 1 || got[0].Alias != "web" {
			t.Fatalf("candidates = %+v, want exactly one named web", got)
		}
		if got[0].Skip != "" {
			t.Errorf("the alias ssh accepts was refused: %s", got[0].Skip)
		}
	})

	t.Run("a quoted name with a space is ONE refusal, not two garbage names", func(t *testing.T) {
		h := &hopFixture{config: "Host \"my host\"\n  HostName spaced.internal\n"}
		got, err := RemoteCandidates(context.Background(), h.run, "hop")
		if err != nil {
			t.Fatalf("RemoteCandidates: %v", err)
		}
		var aliases []string
		for _, c := range got {
			aliases = append(aliases, c.Alias)
		}
		if len(aliases) != 1 || aliases[0] != "my host" {
			t.Fatalf("candidates = %q, want exactly [\"my host\"] — the quotes became two names", aliases)
		}
		if c := candidateFor(t, got, "my host"); c.Skip == "" {
			t.Error("an alias ssh answers with `hostname contains invalid characters` was offered for probing")
		}
		if h.resolveCalls() != 0 {
			t.Errorf("spent %d round trips on a name ssh refuses: %q", h.resolveCalls(), h.calls)
		}
	})
}

// bufio.Scanner stops at a token longer than 64 KiB and reports it through Err(), which neither
// reader consulted — so an over-long line ended the scan and everything declared AFTER it came back
// as an absence. This branch is the first thing to point those readers at bytes read off ANOTHER
// machine, which is what turns a latent local defect into a foreign-data one.
//
// Measured against the pre-fix function: a hop config of one 70 000-character Host line followed by
// `Host leaf` and an `Include` answered ZERO candidates and a nil error — the same answer a hop with
// nothing to offer gives, with no row and no reason. That is the silent horizon again, arriving
// through the reader rather than through the parser or the budget.
//
// The fix REPORTS rather than recovers: bufio cannot resume after ErrTooLong, and raising the buffer
// would move the horizon rather than close it, so what the crawl owes is a row saying the read
// stopped and where. This case therefore asserts the horizon and NOT the absence of `leaf` — a later
// reader that manages to recover and name it is an improvement, and a test that pinned the absence
// would call that a regression.
func TestALineTooLongForTheReaderIsAHorizonRatherThanAShortConfig(t *testing.T) {
	// One byte over bufio's default token cap, so the fixture cannot pass by being small enough
	// while the cap moves underneath it.
	long := strings.Repeat("a", 64*1024+1)

	t.Run("on a hop", func(t *testing.T) {
		h := &hopFixture{
			config:  "Host " + long + "\nHost leaf\nInclude conf.d/*.conf\n",
			resolve: map[string]string{"leaf": resolvedLeaf},
		}
		got, err := RemoteCandidates(context.Background(), h.run, "hop")
		if err != nil {
			t.Fatalf("RemoteCandidates: %v", err)
		}
		horizon := ""
		for _, c := range got {
			if strings.Contains(c.Skip, "longer than") {
				horizon = c.Skip
			}
			if c.Alias == long {
				t.Error("the over-long line was carried as a machine, and it is longer than any name ssh accepts")
			}
		}
		if horizon == "" {
			t.Fatalf("a truncated read answered %d candidates and no horizon, which is what a hop "+
				"with nothing to offer answers: %+v", len(got), got)
		}
		if !strings.Contains(horizon, "hop") {
			t.Errorf("the horizon does not name whose config was cut short: %q", horizon)
		}
	})

	t.Run("on the root's own files, where the defect is older", func(t *testing.T) {
		dir := t.TempDir()
		user := filepath.Join(dir, "config")
		if err := os.WriteFile(user, []byte("Host "+long+"\nHost leaf\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got := ParseSSHConfig(user, filepath.Join(dir, "no-system-config"))
		for _, c := range got {
			if strings.Contains(c.Skip, "longer than") {
				if !strings.Contains(c.Skip, user) {
					t.Errorf("the horizon does not name the file it could not finish: %q", c.Skip)
				}
				return
			}
		}
		t.Fatalf("a truncated local config answered %d candidates and no horizon: %+v", len(got), got)
	})
}

// The same separator rule on `Include`, and the stakes are the same in the opposite direction: an
// include the parser cannot see is a horizon reported to nobody, so the machines behind it are
// absent from the result with no row and no reason.
func TestAHopsIncludeIsReadWithEverySeparatorSSHAccepts(t *testing.T) {
	for _, tc := range []struct{ name, config string }{
		{"a tab", "Include\tconf.d/*.conf\nHost leaf\n"},
		{"an equals", "Include=conf.d/*.conf\nHost leaf\n"},
		{"a space, the shape already covered", "Include conf.d/*.conf\nHost leaf\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &hopFixture{config: tc.config, resolve: map[string]string{"leaf": resolvedLeaf}}
			got, err := RemoteCandidates(context.Background(), h.run, "hop")
			if err != nil {
				t.Fatalf("RemoteCandidates: %v", err)
			}
			c := candidateFor(t, got, "conf.d/*.conf")
			if c.Skip == "" {
				t.Error("the include was carried as a machine rather than as a horizon")
			}
		})
	}
}

// TestTheRecipeAProducerMakesIsTheRecipeDiagnoseParses is the test that was missing, and its absence
// let a wrong remedy ship. Every existing case for `fleet.Diagnose`'s identity branch hands it a
// HAND-BUILT map; this one drives ssh's own bytes through the real producer and into the real
// consumer, which is the only shape that can see the two halves disagree.
//
// The bytes are ssh's, captured on 2026-08-20 from OpenSSH 10.3p1 for a name no config mentions —
// the commonest hop stanza, `Host leaf` with a HostName and nothing else. `held` is the operator's
// own machine as measured that day: `~/.ssh/id_ed25519` exists and the other four defaults do not,
// which is the arrangement that made the defect visible. Before the fix the producer kept the first
// line only, `heldHere` looked for `~/.ssh/id_rsa`, and the row read
// `blocked / copy ~/.ssh/id_rsa to this machine` for a machine ssh authenticates to on its FOURTH
// default. The two poles are here because "ready" must not be reachable by a predicate that always
// says yes: with none of the five present the same recipe must still be Blocked.
func TestTheRecipeAProducerMakesIsTheRecipeDiagnoseParses(t *testing.T) {
	const sshAnswer = `user dev
hostname leaf.internal
port 22
identityfile ~/.ssh/id_rsa
identityfile ~/.ssh/id_ecdsa
identityfile ~/.ssh/id_ecdsa_sk
identityfile ~/.ssh/id_ed25519
identityfile ~/.ssh/id_ed25519_sk
pubkeyauthentication yes
proxyusefdpass no
`
	recipe := resolvedRecipe(sshAnswer)

	// The fourth default is the one this machine holds, so a producer that kept any single line
	// other than the fourth would send Diagnose looking for a key that is not here.
	onlyEd25519 := func(path string) bool { return path == "~/.ssh/id_ed25519" }
	state, reason := fleet.Diagnose(recipe, onlyEd25519)
	if state != fleet.Ready {
		t.Errorf("a machine whose fourth default key is on this disk diagnosed %v: %s\nrecipe[identityfile] = %q",
			state, reason, recipe["identityfile"])
	}
	if reason != "" {
		t.Errorf("Ready carried a remedy, which means a row would print an act it does not need: %s", reason)
	}

	// The opposite pole, so Ready above cannot be the answer of a predicate that says yes to
	// everything: hold none of the five and the same recipe must be Blocked with an act.
	none := func(string) bool { return false }
	state, reason = fleet.Diagnose(recipe, none)
	if state != fleet.Blocked {
		t.Errorf("a machine none of whose five keys are here diagnosed %v rather than blocked", state)
	}
	if !strings.Contains(reason, "ssh-copy-id dev@leaf.internal") {
		t.Errorf("the remedy must name the resolved login, since the alias is the hop's vocabulary:\n%s", reason)
	}
	// And it must offer the whole set rather than ssh's first choice alone, because any ONE of them
	// brought here would do and naming only `id_rsa` sends the operator after a key they may not have.
	for _, want := range []string{"~/.ssh/id_rsa", "~/.ssh/id_ed25519"} {
		if !strings.Contains(reason, want) {
			t.Errorf("the remedy names no %s, so it offers less than ssh would try:\n%s", want, reason)
		}
	}
}

// A host that REFUSES public keys cannot be short of one, so `ssh-copy-id` would change nothing on
// it — and `fleet.Diagnose` knows that, in `offersPublicKeys`. The branch was dead: the producer's key
// filter did not carry `pubkeyauthentication`, so the consumer read an absent value, treated it as
// ssh's default of ON, and told the operator to copy a key that host will never be offered.
//
// Measured on OpenSSH 10.3p1 with a real config file, which is why this fixture is shaped the way it
// is: `PubkeyAuthentication no` resolves to `pubkeyauthentication false` AND ssh still prints all five
// `identityfile` defaults beside it. So the two keys must travel together; the file check alone cannot
// distinguish "no key here" from "keys are not accepted there".
//
// Both poles, because a fixture with only the refusing host passes against a producer that drops the
// key entirely — absent reads as ON, which is ssh's own default and the right reading of absence.
func TestAHostThatRefusesPublicKeysIsNotToldToCopyOne(t *testing.T) {
	const refuses = `user dev
hostname walled.internal
pubkeyauthentication false
identityfile ~/.ssh/id_rsa
identityfile ~/.ssh/id_ed25519
`
	const accepts = `user dev
hostname open.internal
pubkeyauthentication yes
identityfile ~/.ssh/id_rsa
identityfile ~/.ssh/id_ed25519
`
	none := func(string) bool { return false }

	state, reason := fleet.Diagnose(resolvedRecipe(refuses), none)
	if state != fleet.Ready {
		t.Errorf("a host that refuses public keys diagnosed %v: %s", state, reason)
	}
	if strings.Contains(reason, "ssh-copy-id") {
		t.Errorf("the remedy offers a key to a host that will not accept one:\n%s", reason)
	}

	// The opposite pole: the same shape with the refusal removed must still produce the key remedy,
	// or "no ssh-copy-id" above would be satisfied by a Diagnose that never offers one at all.
	state, reason = fleet.Diagnose(resolvedRecipe(accepts), none)
	if state != fleet.Blocked {
		t.Errorf("a host that accepts public keys and holds none of ours diagnosed %v", state)
	}
	if !strings.Contains(reason, "ssh-copy-id dev@open.internal") {
		t.Errorf("the remedy for a host that DOES accept keys must name the act:\n%s", reason)
	}
}
