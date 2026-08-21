package fleet

import (
	"strings"
	"testing"
)

// The remedy IS the product (fleet spec §3.4): `Blocked` is a diagnosis, and a diagnosis whose
// sentence names no act is the "keep the label, lose the action" class this repository has carried as
// a known issue since its first week.
func TestARecipeNamingTheHopsKeyIsBlockedWithACommand(t *testing.T) {
	recipe := map[string]string{"hostname": "leaf.internal", "user": "dev",
		"identityfile": "~/.ssh/hop-only"}
	state, reason := Diagnose(recipe, func(string) bool { return false })
	if state != Blocked {
		t.Fatalf("state = %v, want Blocked", state)
	}
	for _, want := range []string{"hop-only", "ssh-copy-id"} {
		if !strings.Contains(reason, want) {
			t.Errorf("the reason %q does not name %q — a reason without a remedy is a complaint", reason, want)
		}
	}
	if strings.Contains(reason, "unreachable") {
		t.Error("`unreachable` is not a reason (spec §3.2 invariant 4)")
	}
	// The command has to be one the operator can paste. The recipe's resolved `user@hostname` is the
	// only thing that works from HERE: the alias belongs to the HOP's ssh config, so `ssh-copy-id
	// <alias>` would resolve in the root's config, which by definition does not hold that stanza.
	if !strings.Contains(reason, "dev@leaf.internal") {
		t.Errorf("the reason %q does not name the login the command needs — the alias is the hop's "+
			"vocabulary and would not resolve here", reason)
	}
}

// The other pole of the same question.
func TestARecipeWeCanSatisfyIsReady(t *testing.T) {
	recipe := map[string]string{"hostname": "leaf.internal", "identityfile": "~/.ssh/id_ed25519"}
	if state, _ := Diagnose(recipe, func(string) bool { return true }); state != Ready {
		t.Errorf("state = %v, want Ready — the key is here, so one tick declares it", state)
	}
}

// CALIBRATION ON THE HEALTHY POLE, and it is the reason this function asks "is ANY of them here"
// rather than "are they all". Measured 2026-08-20, OpenSSH_10.3p1: `ssh -G` prints one
// `identityfile` line PER KEY, and for a host with no configuration at all it prints these five.
// Almost nobody holds all five, so a rule demanding every named key would report the whole fleet
// `Blocked` — a guard that reddens on everything is a guard measuring the wrong thing.
func TestOneKeyPresentAmongTheDefaultFiveIsNotBlocked(t *testing.T) {
	defaults := []string{"~/.ssh/id_rsa", "~/.ssh/id_ecdsa", "~/.ssh/id_ecdsa_sk",
		"~/.ssh/id_ed25519", "~/.ssh/id_ed25519_sk"}
	recipe := map[string]string{"hostname": "leaf.internal",
		"identityfile": strings.Join(defaults, "\n")}
	asked := map[string]bool{}
	state, reason := Diagnose(recipe, func(p string) bool {
		asked[p] = true
		return p == "~/.ssh/id_ed25519"
	})
	if state != Ready {
		t.Errorf("state = %v (%q), want Ready — four of the five default identity files are absent "+
			"on a perfectly ordinary machine", state, reason)
	}
	// The predicate is handed the path exactly as ssh reported it, unexpanded: measured, `ssh -G`
	// does not expand `~`, and expanding it here would make this function read HOME.
	if !asked["~/.ssh/id_ed25519"] {
		t.Errorf("the paths offered to the predicate were %v — want the recipe's own spelling, "+
			"tilde and all, since the caller owns HOME", keysOf(asked))
	}
}

// The fold the producer must use, and the reason it is a newline: `ssh -G` frames one path per LINE,
// so a `map[string]string` can carry the set only by keeping that framing. Splitting on spaces would
// break a path that contains one; splitting on nothing would read five keys as one absent file.
func TestSeveralIdentityFilesArriveOneToALine(t *testing.T) {
	recipe := map[string]string{"hostname": "leaf.internal", "user": "dev",
		"identityfile": "~/.ssh/first\n~/.ssh/second"}
	if state, reason := Diagnose(recipe, func(p string) bool { return p == "~/.ssh/second" }); state != Ready {
		t.Errorf("state = %v (%q), want Ready — the second of two named keys is here", state, reason)
	}
	state, reason := Diagnose(recipe, func(string) bool { return false })
	if state != Blocked {
		t.Fatalf("state = %v, want Blocked — neither named key is here", state)
	}
	for _, want := range []string{"~/.ssh/first", "~/.ssh/second"} {
		if !strings.Contains(reason, want) {
			t.Errorf("the reason %q drops %q — either key would have done, so naming one of them "+
				"would be a lie by omission", reason, want)
		}
	}
}

// Spec §2.2.1, the sharpest hazard in that document: a proxied handshake reports the JUMP host's key.
// Measured on OpenSSH 10.x — `ssh -v -J nuc dev-air` prints ONE `Server host key` line and it is
// nuc's, at every verbosity — so v1 cannot identify a machine behind a hop at all.
//
// This case holds the KEY the recipe names, which is what makes it about ORDER: a recipe we could
// authenticate to is still unverifiable when the handshake would describe the wrong machine. A ruling
// of the form "harmless because X is also available" is only true if the code consults X first.
func TestAProxiedRecipeIsBlockedEvenWhenWeHoldItsKey(t *testing.T) {
	recipe := map[string]string{"hostname": "leaf.internal", "user": "dev",
		"identityfile": "~/.ssh/id_ed25519", "proxyjump": "hop"}
	state, reason := Diagnose(recipe, func(string) bool { return true })
	if state != Blocked {
		t.Fatalf("state = %v (%q), want Blocked — spec §2.2.1: the fingerprint would be the jump's, "+
			"and under set-intersection merging that fuses two machines", state, reason)
	}
	if !strings.Contains(reason, "hop") {
		t.Errorf("the reason %q does not name what stands between — the operator cannot act on a "+
			"hop they are not told about", reason)
	}
	if !strings.Contains(reason, "leaf.internal") {
		t.Errorf("the reason %q does not name the machine the route is wanted TO", reason)
	}
}

// A `ProxyCommand` is the same hazard without a hop name. Measured on the live fleet: `cortex-web`
// resolves to `ProxyCommand docker exec -i tailscale-cortex nc %h %p`, and `ssh -G` reports the
// command with `%h %p` unexpanded — so the string is the thing the operator would edit, and the
// reason quotes it.
func TestAProxyCommandIsBlockedAndNamesTheCommand(t *testing.T) {
	cmd := "docker exec -i tailscale-x nc %h %p"
	recipe := map[string]string{"hostname": "leaf.internal", "proxycommand": cmd}
	state, reason := Diagnose(recipe, func(string) bool { return true })
	if state != Blocked {
		t.Fatalf("state = %v (%q), want Blocked", state, reason)
	}
	if !strings.Contains(reason, cmd) {
		t.Errorf("the reason %q does not quote the proxy command — that string is the thing the "+
			"operator would change", reason)
	}
}

// THE FALSE-POSITIVE POLE, measured and not imagined: `ssh -G` reports `proxyusefdpass no` for EVERY
// host, direct ones included, and it is the only key beginning with `proxy` on a direct recipe. A
// prefix or substring scan over the recipe's keys would therefore call the whole fleet `Blocked` —
// this repository has already shipped a substring check over prose that reported five false hits.
func TestProxyUseFdPassIsNotAProxy(t *testing.T) {
	recipe := map[string]string{"hostname": "leaf.internal", "identityfile": "~/.ssh/id_ed25519",
		"proxyusefdpass": "no", "canonicalizehostname": "false", "identitiesonly": "no"}
	if state, reason := Diagnose(recipe, func(string) bool { return true }); state != Ready {
		t.Errorf("state = %v (%q), want Ready — `proxyusefdpass` is on every direct recipe, so a "+
			"prefix scan for `proxy` blocks everything", state, reason)
	}
}

// Also measured, and it is why there is no `none` branch anywhere in this file: `ProxyJump none` and
// `ProxyCommand none` — the idiom that cancels a wildcard stanza — produce NO line at all. ssh
// resolves the cancellation itself, so a `none` token cannot reach a recipe that came from `ssh -G`,
// and a branch for it would be a rule that reads as enforced and can never run.
func TestACancelledProxyIsAbsentFromTheRecipeRatherThanPresentAsNone(t *testing.T) {
	recipe := map[string]string{"hostname": "leaf.internal", "identityfile": "~/.ssh/id_ed25519"}
	if state, _ := Diagnose(recipe, func(string) bool { return true }); state != Ready {
		t.Errorf("state = %v, want Ready — a cancelled proxy leaves no key behind", state)
	}
}

// A recipe that names no credential names no obstacle, so there is nothing to diagnose. `Ready` here
// does not claim the machine answers — `stateOf` clamps it to `Candidate` for a row nobody verified,
// and the probe's own failure sentence is what lands. Inventing a `Blocked` remedy for a recipe that
// carries no complaint would be a status naming a mechanism the design does not have.
func TestARecipeThatNamesNoKeyHasNoDiagnosableObstacle(t *testing.T) {
	for name, recipe := range map[string]map[string]string{
		"empty":       {},
		"nil":         nil,
		"no identity": {"hostname": "leaf.internal", "user": "dev", "port": "22"},
	} {
		state, reason := Diagnose(recipe, func(string) bool { return false })
		if state != Ready || reason != "" {
			t.Errorf("%s recipe diagnosed as %v (%q), want Ready with nothing to say", name, state, reason)
		}
	}
}

// A host that refuses public keys cannot be short of one. Measured: `PubkeyAuthentication no` is
// reported by `ssh -G` as `pubkeyauthentication false`, and the identity files stay in the output —
// so the file check alone would name a remedy (`ssh-copy-id`) that changes nothing on a host which
// will only ever ask for a password.
func TestAHostThatRefusesPublicKeysIsNotBlockedForWantOfAKeyFile(t *testing.T) {
	for _, off := range []string{"false", "no"} {
		recipe := map[string]string{"hostname": "leaf.internal", "user": "dev",
			"identityfile": "~/.ssh/hop-only", "pubkeyauthentication": off}
		if state, reason := Diagnose(recipe, func(string) bool { return false }); state != Ready {
			t.Errorf("pubkeyauthentication=%s diagnosed as %v (%q), want Ready — no key file can be "+
				"the obstacle where no key is offered", off, state, reason)
		}
	}
	// The default is ON, and an absent key must read as the default rather than as `off`: a producer
	// that did not carry the field would otherwise switch the whole diagnosis off silently.
	for _, on := range []string{"", "true", "yes", "TRUE"} {
		recipe := map[string]string{"hostname": "leaf.internal", "user": "dev",
			"identityfile": "~/.ssh/hop-only"}
		if on != "" {
			recipe["pubkeyauthentication"] = on
		}
		if state, _ := Diagnose(recipe, func(string) bool { return false }); state != Blocked {
			t.Errorf("pubkeyauthentication=%q diagnosed as %v, want Blocked — anything that is not a "+
				"refusal leaves public keys in play", on, state)
		}
	}
}

// A nil predicate is the caller saying it cannot answer, which is not the same as `no`. It leaves the
// row UNDIAGNOSED — `stateOf` reads a `Ready` claim on an unverified declaration as `Candidate`, so
// the operator gets the probe's own sentence instead of a remedy the hub cannot justify. The one
// thing it must not do is dereference: this runs inside a poll.
func TestANilPredicateLeavesTheRowUndiagnosed(t *testing.T) {
	recipe := map[string]string{"hostname": "leaf.internal", "identityfile": "~/.ssh/hop-only"}
	state, reason := Diagnose(recipe, nil)
	if state != Ready || reason != "" {
		t.Errorf("a nil predicate gave %v (%q), want Ready with nothing to say — a diagnosis nobody "+
			"could check is not a diagnosis", state, reason)
	}
}

// MECHANICAL, over every reason this function can produce, because a reason string is easy to produce
// and hard to keep true (spec §5 point 4).
//
// The width checks are earned by damage: a specified refusal longer than the terminal the project
// commits to loses its TAIL, and the tail is where a complaint-first sentence keeps its remedy. There
// are TWO of them, and the difference is the interesting part.
//
//   - Every reason must name an act in its first 78 columns. That holds even for a 120-column
//     hostname, because the act's VERB is what has to survive; the live fleet already names sessions
//     88 columns wide, so the pathological rows are not hypothetical.
//   - Every reason built from ORDINARY values must fit a WHOLE act in those columns. This is the
//     sharper one, and it has now caught TWO defects in this file rather than one, which is why it is
//     worth its length. FIRST: with the unbounded key list printed before the command, the five default
//     identity paths put the entire `ssh-copy-id …` past the fold — the sentence is 198 columns either
//     way, but command-first it ENDS at column 35 while key-list-first it STARTS at column 125. A
//     verb-only check passes that version, since `copy ` sat in column 1. SECOND, and it is why an act
//     carries no explanatory clause: appending "to authorise this machine's own key" made the act 79
//     columns for a host on a non-default port — over by exactly one — so the operator read a truncated
//     command. Both are the oldest defect class in this repository (keep the label, lose the action)
//     reappearing inside a sentence written to avoid it. Spec §3.4 names the two acts as `ssh-copy-id …`
//     and "copy `~/.ssh/<name>` here", with no clause on either; that wording is what fits.
func TestEveryBlockedReasonNamesAnActInItsFirstColumns(t *testing.T) {
	long := strings.Repeat("l", 120)
	// The measured worst ORDINARY case for the key list: `ssh -G` names these five for any host with
	// no `IdentityFile` of its own, and a machine holding none of them is a fresh machine.
	defaults := "~/.ssh/id_rsa\n~/.ssh/id_ecdsa\n~/.ssh/id_ecdsa_sk\n~/.ssh/id_ed25519\n~/.ssh/id_ed25519_sk"
	cases := []struct {
		name   string
		recipe map[string]string
		// pathological marks a row carrying a value wider than the whole terminal. No sentence can put
		// a complete act inside 78 columns once one of its own names is 120 wide, so such a row is held
		// to the verb check only. Note what is NOT here: `the five default keys` is ordinary in every
		// value and still runs the sentence to 234 columns, which is the row that makes the sharper
		// check bite. The flag is written down per row rather than derived from a width, because a
		// threshold nobody can justify is usually a check nobody ran.
		pathological bool
	}{
		{name: "the hop's key", recipe: map[string]string{"hostname": "leaf.internal", "user": "dev", "identityfile": "~/.ssh/hop-only"}},
		{name: "several keys", recipe: map[string]string{"hostname": "leaf.internal", "user": "dev", "identityfile": "~/.ssh/a\n~/.ssh/b"}},
		{name: "the five default keys", recipe: map[string]string{"hostname": "leaf.internal", "user": "dev", "identityfile": defaults}},
		{name: "no user", recipe: map[string]string{"hostname": "leaf.internal", "identityfile": "~/.ssh/hop-only"}},
		{name: "a port", recipe: map[string]string{"hostname": "leaf.internal", "user": "dev", "port": "2222", "identityfile": "~/.ssh/hop-only"}},
		// A REALISTIC long hostname, and it is ORDINARY rather than pathological: the table otherwise
		// jumps from 13 columns to 120, so the band a real fleet actually occupies went untested. A
		// tailnet peer's resolved name is this shape, and it is what says the act still has headroom now
		// that the explanatory clause is off it — 53 columns against the 78 an act may occupy.
		{name: "a tailnet-scale hostname", recipe: map[string]string{"hostname": "some-machine.tailnet1234.ts.net", "user": "won", "identityfile": "~/.ssh/hop-only"}},
		{name: "no hostname", recipe: map[string]string{"identityfile": "~/.ssh/hop-only"}},
		{name: "a jump", recipe: map[string]string{"hostname": "leaf.internal", "proxyjump": "hop"}},
		{name: "a proxy command", recipe: map[string]string{"hostname": "leaf.internal", "proxycommand": "nc %h %p"}},
		{name: "a long hostname", recipe: map[string]string{"hostname": long, "user": "dev", "identityfile": "~/.ssh/hop-only"}, pathological: true},
		{name: "a long key path", recipe: map[string]string{"hostname": "leaf.internal", "user": "dev", "identityfile": "~/.ssh/" + long}, pathological: true},
		{name: "a long hostname AND key path", recipe: map[string]string{"hostname": long, "user": "dev", "identityfile": "~/.ssh/" + long}, pathological: true},
		{name: "a long jump", recipe: map[string]string{"hostname": long, "proxyjump": long}, pathological: true},
		{name: "a long proxy command", recipe: map[string]string{"hostname": long, "proxycommand": long}, pathological: true},
	}
	// Every act this function may offer, spelled as the verb the operator reads.
	acts := []string{"copy ", "ssh-copy-id", "give this machine a direct route"}
	for _, c := range cases {
		state, reason := Diagnose(c.recipe, func(string) bool { return false })
		if state != Blocked {
			t.Errorf("%s: state = %v, want Blocked — this table exists to read the reasons", c.name, state)
			continue
		}
		if reason == "" {
			t.Errorf("%s: Blocked with no remedy (spec §3.2 invariant 4)", c.name)
			continue
		}
		if strings.Contains(reason, "unreachable") {
			t.Errorf("%s: says %q — `unreachable` names no act the operator can perform", c.name, reason)
		}
		if strings.Contains(reason, "  ") {
			t.Errorf("%s: %q has a gap where an absent field was interpolated", c.name, reason)
		}
		// Runes are columns here: every fixture is ASCII.
		head := reason
		if r := []rune(head); len(r) > 78 {
			head = string(r[:78])
		}
		named := false
		for _, act := range acts {
			if strings.Contains(head, act) {
				named = true
			}
		}
		if !named {
			t.Errorf("%s: the first 78 columns are %q, which names no act — at 80 columns the "+
				"operator reads the complaint and never the fix", c.name, head)
		}
		if c.pathological {
			continue
		}
		// A whole act, end to end, and ENDING inside the first 78 columns. The position is the whole
		// point: the first cut of this check asked only whether some clause was short enough, which a
		// clause beginning at column 80 satisfies while being entirely off screen — the mutation that
		// restores the original ordering defect SURVIVED that version. The remedy is everything before
		// the ` — ` that introduces the explanation, and the acts within it are separated by `, or `.
		remedy, _, _ := strings.Cut(reason, " — ")
		const sep = ", or "
		whole, at := "", 0
		for _, act := range strings.Split(remedy, sep) {
			if end := at + len([]rune(act)); end <= 78 && whole == "" {
				whole = act
			}
			at += len([]rune(act)) + len(sep)
		}
		if whole == "" {
			t.Errorf("%s: no whole act ENDS inside 78 columns — the remedy is %q, so at the width this "+
				"project commits to the operator sees a truncated command and cannot run it", c.name, remedy)
		}
	}
}

// A port that is not 22 has to reach the command, or the command fails for a reason the operator
// cannot see. `ssh -G` always reports a port, so 22 must NOT produce a flag: a remedy carrying
// `-p 22` reads as a special case where there is none.
func TestANonDefaultPortReachesTheCommandAndTheDefaultDoesNot(t *testing.T) {
	base := map[string]string{"hostname": "leaf.internal", "user": "dev", "identityfile": "~/.ssh/hop-only"}
	with := func(port string) string {
		recipe := map[string]string{"port": port}
		for k, v := range base {
			recipe[k] = v
		}
		_, reason := Diagnose(recipe, func(string) bool { return false })
		return reason
	}
	if got := with("2222"); !strings.Contains(got, "ssh-copy-id -p 2222 dev@leaf.internal") {
		t.Errorf("the reason %q does not carry the port — the command would go to 22 and fail", got)
	}
	if got := with("22"); strings.Contains(got, "-p ") {
		t.Errorf("the reason %q spells out the default port", got)
	}
}

// The claim the task-1 author left in writing: `Diagnose`'s output drops straight into an
// `Observation`. Asserted through that seam rather than on the two functions separately — "A does X"
// and "B does Y" say nothing about the chain when a third thing stands between them, and `stateOf`
// stands between these two.
//
// The Ready half is the load-bearing one. Diagnose returns NO reason there on purpose: the remedies
// for the four unmounted states live in `defaultReason` alone, they need the ALIAS, and Diagnose is
// never given one. A second copy of those sentences is how three rules in this repository diverged in
// a single day.
func TestADiagnosisDropsStraightIntoAnObservation(t *testing.T) {
	state, reason := Diagnose(map[string]string{"hostname": "leaf.internal", "user": "dev",
		"identityfile": "~/.ssh/hop-only"}, func(string) bool { return false })
	g := New()
	g.Observe(Observation{Observer: "hop", Label: "leaf", State: state, Reason: reason})
	c := g.Candidates()
	if len(c) != 1 {
		t.Fatalf("want one candidate, got %+v", c)
	}
	if c[0].State != Blocked {
		t.Errorf("the row reads %v, want Blocked", c[0].State)
	}
	if c[0].Reason != reason {
		t.Errorf("the graph stored %q, not the diagnosis's own %q — a specific remedy was replaced "+
			"by a general one", c[0].Reason, reason)
	}

	state, reason = Diagnose(map[string]string{"hostname": "leaf.internal",
		"identityfile": "~/.ssh/id_ed25519"}, func(string) bool { return true })
	if reason != "" {
		t.Fatalf("Diagnose composed its own Ready remedy (%q) — that sentence lives in one place and "+
			"needs the alias, which Diagnose never sees", reason)
	}
	g = New()
	g.Observe(Observation{Observer: "root", Label: "leaf", State: state, Reason: reason,
		Fingerprints: []string{"SHA256:aaa"}, Verified: true})
	n := g.Nodes()[0]
	if n.State != Ready {
		t.Errorf("a Ready diagnosis reached the graph as %v", n.State)
	}
	if !strings.Contains(n.Reason, "leaf") {
		t.Errorf("the node's remedy is %q — the one place that holds it names the alias, and this "+
			"row would carry no next move at all (spec §7 decision 6)", n.Reason)
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// THE CLAIM `identityFiles` MAKES ABOUT A PATH WITH A SPACE IN IT, ASSERTED.
//
// Its doc comment says splitting on whitespace "would break a path that contains a space", and an
// independent verifier measured that no test held that claim: the mutant replacing `strings.Split(…,
// "\n")` with `strings.Fields` survived the whole suite. A documented guarantee nothing asserts is the
// shape this repository treats as worse than an undocumented one, because it stops the next reader
// checking.
//
// The `~` is deliberately unexpanded, which is how `ssh -G` reports it (measured, five default paths
// for a host with no configuration), and the predicate below is handed exactly that spelling.
func TestAKeyPathWithASpaceStaysOnePath(t *testing.T) {
	const spaced = "~/.ssh/my key"
	recipe := map[string]string{
		"hostname":     "leaf.internal",
		"user":         "dev",
		"identityfile": spaced + "\n~/.ssh/id_ed25519",
	}
	var offered []string
	state, reason := Diagnose(recipe, func(p string) bool {
		offered = append(offered, p)
		return false
	})
	if len(offered) != 2 {
		t.Fatalf("the predicate was offered %d paths (%q), want 2 — a space split one key into two",
			len(offered), offered)
	}
	if offered[0] != spaced {
		t.Errorf("first path = %q, want %q — the space must not split it", offered[0], spaced)
	}
	if state != Blocked {
		t.Errorf("state = %v, want Blocked — neither key is present", state)
	}
	if !strings.Contains(reason, "my key") {
		t.Errorf("the remedy %q does not name the key it wants, so the operator cannot act on it", reason)
	}
}
