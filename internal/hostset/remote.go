package hostset

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/DawnBreather/tmux-hub/internal/fleet"
)

// RemoteRunner runs ONE command on a hop the hub already reaches, and answers with the command's
// own streams. It is a seam so the tests need no network and no keys.
//
// It is a second type beside Runner rather than a reuse of it, because the two have different
// contracts and folding them would give one of them the other's argv. Runner PROBES a candidate the
// hub has never spoken to, so its production implementation must carry `-v -o BatchMode=yes -o
// ConnectTimeout=6` — the `-v` is where a fingerprint comes from and the other two are what keep
// twenty candidates to seven seconds. A RemoteRunner speaks to a hop that is already verified and
// already has a master, so it wants neither the verbose transcript nor a connect timeout of its own.
//
// A production implementation MUST pass STDERR through, and that is not decoration: an enumeration
// that comes back empty is either a hop with no ssh config or a hop we could not reach, and the words
// on stderr are the only thing that tells those apart (theHopSaidThereIsNoConfig). A runner that
// returns "" there makes every unreachable hop read as a hop with nothing to offer — the same shape
// of silent omission `Runner`'s missing `-v` produces for fingerprints.
//
// Nothing here writes to the hop. Every payload this file builds is a read, which is fleet spec
// §3.2 invariant 5 (nothing is ever written outward) enforced by there being no other kind of
// payload rather than by a rule somebody has to remember.
type RemoteRunner func(ctx context.Context, hop, payload string) (stdout, stderr string, rc int)

// remoteConfigPayload enumerates: it is the whole of what the hub asks a hop about its own fleet.
//
// Every character is a decision, and they are the same decisions probePayload made, for the same
// measured reason. The program name is BARE and nothing is quoted: one host on this fleet runs
// Nushell as its login shell, where a QUOTED PROGRAM NAME is a parse error **at rc=0**, and that is
// how the host was invisible for weeks. `~` is expanded by Nushell as well as by any POSIX shell,
// and `cat` is an external command in both.
//
// What keeps it that way is an ALLOW-LIST over its characters in remote_test.go, not a list of
// constructs to avoid: the same argument unpasteableAlias makes below about aliases applies to the
// payload, since "no shell will read this as syntax" is a claim about every shell that will ever run
// it. Both payloads are also pinned there as literals, because every other case in that file matches
// a payload by asking THIS builder, so a typo moves the product and the harness together.
//
// A missing file is rc!=0 with an EMPTY stdout, and that is the commonest answer there is — the
// operator's own `nuc` has no ~/.ssh/config at all. So it means "nothing to offer", never a failure —
// but only when the hop's own words SAY that, which is what theHopSaidThereIsNoConfig decides.
//
// The path is a constant of its own because two things need it: the payload asks for it, and the
// predicate that recognises "that file is not there" has to know which file was asked about. Two
// copies of the path would let the predicate go on matching a file the payload stopped reading.
const remoteConfigPath = "~/.ssh/config"

const remoteConfigPayload = "cat " + remoteConfigPath

// remoteResolvePayload resolves ONE alias on the hop.
//
// `ssh -G` RESOLVES and never ENUMERATES: measured 2026-08-20, `ssh -G definitely-not-a-host-xyz`
// answers with a hostname, a user, a port and five default identity files for a name declared
// nowhere. So there is no listing to be had from it, enumeration stays the parse of Host lines, and
// this payload is asked once per alias — which is what the budget below exists to bound.
//
// One alias per invocation rather than a loop over all of them, because a loop is shell syntax and
// the shell on the far side may not be a POSIX one. The cost of that choice is a round trip per
// alias over a master that is already open, and the budget is what keeps it finite.
func remoteResolvePayload(alias string) string { return "ssh -G " + alias }

// MaxAliasesPerHop is how many of a hop's aliases the crawl will resolve, and it is a hard cap that
// REPORTS what it cut: an alias past it comes back carrying the count that was skipped and the name
// of the knob to raise, because a silent horizon reads as a finished crawl (fleet spec §3.3).
//
// The number is 32 because the largest real config on this fleet declares 21 probeable aliases —
// measured 2026-08-20 by ParseSSHConfig over the operator's own files: 31 entries, 10 of them
// patterns. So this resolves every hop this fleet has WHOLE, and still bounds a hop whose config is
// half again as large as any yet seen. Being wrong about it is visible rather than silent, which is
// the property that makes a guessed cap safe to ship.
//
// IT IS NOW THE GRAPH'S OWN BREADTH BUDGET, and this is the commit the earlier version of this
// comment promised. It used to be a second literal 32 here, with the reason written down: importing
// internal/fleet from this package makes fleet linked into the binary, and until the picker consumed
// the graph `TestEveryPackageIsReachableFromMain` would have failed fleet's under-construction
// exemption as STALE. Task 6 wires the graph through the picker and deletes that exemption, so the
// two numbers are one — `fleet.DefaultBreadth` — and TestTheHopsCapIsTheGraphsOwnBreadthBudget is
// what keeps them from drifting apart again.
const MaxAliasesPerHop = fleet.DefaultBreadth

// recipeKeys are the keys any consumer reads out of `ssh -G`, and the reason the recipe is not just
// ssh's whole answer: that answer is 79 lines on OpenSSH 10.3p1 (measured), sixty of which no part
// of the hub looks at, and a candidate is a thing the picker holds one of per machine per observer.
//
// The proxy pair is the load-bearing entry. Fleet spec §2.2.1: a proxied connection reports the
// JUMP host's key, so a fingerprint from one must never be attributed to the destination — and this
// map is the only place the crawl can learn that the connection would not be direct.
var recipeKeys = map[string]bool{
	"hostname": true,
	"user":     true,
	"port":     true,
	// `identityfile` and `pubkeyauthentication` are BOTH here because the consumer's question is
	// "would a key fix this machine", and it takes both to answer: a host that refuses public keys
	// cannot be short of one, so `ssh-copy-id` would change nothing on it. Carrying only the first
	// left `fleet.Diagnose`'s `offersPublicKeys` reading an absent value, which it treats as ssh's
	// default of ON — so the branch was dead in production and such a host was told to copy a key it
	// will never be offered. Measured 2026-08-20, OpenSSH 10.3p1: `PubkeyAuthentication no` resolves
	// to `pubkeyauthentication false` AND the five `identityfile` lines stay beside it, which is why
	// the file check alone cannot see the refusal.
	"identityfile":         true,
	"pubkeyauthentication": true,
	"proxyjump":            true,
	"proxycommand":         true,
}

// recipeSetKeys are the keys whose ANSWER IS A SET, so a repeat extends the value instead of being
// discarded. `identityfile` is the only one, and it is one because of what the key MEANS: it answers
// "which credentials would ssh offer here", ssh tries each in turn, and one held key is enough — so
// keeping a single line is keeping the wrong answer to a question about a set.
//
// This cost a wrong diagnosis on the operator's own machine before it was fixed, and the numbers are
// worth keeping: measured 2026-08-20 on OpenSSH 10.3p1, an unconfigured name resolves to FIVE
// `identityfile` lines in the order ssh will try them — `~/.ssh/id_rsa`, `id_ecdsa`, `id_ecdsa_sk`,
// `id_ed25519`, `id_ed25519_sk` — and on that machine only the FOURTH exists. Keeping the first made
// `fleet.Diagnose` read "the recipe names no key that is here" and print `run ssh-copy-id …, or copy
// ~/.ssh/id_rsa to this machine` for a machine ssh would have authenticated to on its fourth default.
// A stanza that configures two `IdentityFile` directives prints two lines and lost its second the
// same way, which is why the fix is the KEY's plurality and not a special case for the default set.
//
// The joiner is a newline because that is what the consumer parses (`fleet.Diagnose`'s
// `identityFiles`), and a newline is the one separator a path cannot contain — a space would break a
// key path that has one, which is a real shape on macOS.
var recipeSetKeys = map[string]bool{"identityfile": true}

// RemoteCandidates asks one hop what machines it declares, and resolves each one's transport on
// that hop.
//
// It costs one round trip to enumerate plus one per resolved alias, and it answers with EVERY alias
// the hop declared — including the ones it refused to resolve, each carrying its reason in Skip.
// That is fleet spec §5 point 1: nothing declared is absent from the result. A candidate dropped
// silently is indistinguishable from a hop that declares less than it does, and the three ways this
// function declines to resolve something (a pattern, an alias it will not paste, the budget) are
// exactly the three that would otherwise be invisible.
//
// A hop with no ssh config contributes nothing and is not an error. A hop we could not ASK is an
// error carrying the hop's own words, because those two must not read alike: the first is `nuc`, and
// the second is the silent horizon wearing the first one's clothes. Both put the same thing on
// stdout — nothing — so the distinction is made by theHopSaidThereIsNoConfig, which is where the
// evidence for it is written down.
//
// Nothing here creates a node. Every alias comes back a CANDIDATE (fleet spec §3.2 invariant 3) —
// only the root's own completed handshake makes a node, and this function makes no handshake with
// anything but the hop.
func RemoteCandidates(ctx context.Context, run RemoteRunner, hop string) ([]Candidate, error) {
	out, errOut, rc := run(ctx, hop, remoteConfigPayload)

	// The clock first, and before anything about the hop: a run the hub itself cut short must not
	// be reported as a sentence the hop said.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("could not read %s's ssh config: %w", hop, err)
	}
	text := strings.TrimSpace(out)
	if text == "" {
		// The rc alone answers neither question, for the reason Probe gives about its own
		// stdout: a remote refusal can arrive AT rc=0 (a quoted program name in Nushell did
		// exactly that), and a missing file arrives at rc!=0 with nothing on stdout. So the
		// hop's WORDS decide, and everything they do not account for is a failure — quoted, so
		// the operator has the only remedy there is, and named, so `nuc` is not what a dead
		// master looks like.
		if theHopSaidThereIsNoConfig(errOut, rc) {
			return nil, nil
		}
		return nil, errors.New(because("could not read "+hop+"'s ssh config", errOut))
	}

	cands := declaredBy(hop, text)

	// Which candidates would cost a round trip, counted BEFORE any is spent, so the cut's own
	// number is the truth rather than however far the loop happened to get.
	var resolvable []int
	for i := range cands {
		if cands[i].Skip == "" {
			resolvable = append(resolvable, i)
		}
	}
	for n, i := range resolvable {
		if n >= MaxAliasesPerHop {
			cands[i].Skip = budgetCut(hop, len(resolvable))
			continue
		}
		// Out of time rather than out of budget. The rows this loop never reaches must say so:
		// a row with no recipe and no reason reads as a machine that resolved to nothing, which
		// is a claim about the machine when the truth is a claim about the crawl.
		if err := ctx.Err(); err != nil {
			for _, rest := range resolvable[n:] {
				cands[rest].Skip = ranOutOfTime(hop, cands[rest].Alias, err)
			}
			break
		}
		gotOut, gotErr, _ := run(ctx, hop, remoteResolvePayload(cands[i].Alias))

		// An answer already in hand outranks the clock: this round trip was paid for and it
		// succeeded, so dropping it because the deadline fired a moment later would discard
		// evidence and put a complaint in its place. The next iteration reports the remainder.
		if recipe := resolvedRecipe(gotOut); len(recipe) > 0 {
			cands[i].Recipe = recipe
			continue
		}
		if err := ctx.Err(); err != nil {
			// This run was itself cut short, so its stderr is about the kill and not about the
			// host — quoting it would blame the machine for the hub's own deadline.
			cands[i].Skip = ranOutOfTime(hop, cands[i].Alias, err)
			continue
		}
		// ssh -G answers for ANY string, so an empty answer is a failed round trip and not a
		// machine with an empty recipe. Quote the hop's own words: they are all the remedy there
		// is, and this candidate's next move is that sentence (fleet spec §3.4).
		cands[i].Skip = because("ssh -G on "+hop+" resolved no recipe for "+cands[i].Alias, gotErr)
	}
	return cands, nil
}

// theHopSaidThereIsNoConfig recognises the ONE empty answer that is not a failure, and it is the
// whole of the distinction RemoteCandidates promises.
//
// Two forms, both measured 2026-08-20 against GNU coreutils' `cat`: an ABSENT file is rc=1 with
// `cat: <path>: No such file or directory` on stderr, and an EMPTY file is rc=0 with both streams
// silent. `nuc` is the first form, and it is the commonest hop there is.
//
// It takes TWO clauses rather than one because ssh's own message for a dead control socket also ENDS
// IN `No such file or directory`, and this repository has already paid for matching that tail (a
// socket taxonomy matched it and threw the remedy away). So the sentence must ALSO name the file the
// payload asked about, which ssh's message never does. The residual overlap is a control socket
// living at a path containing `/.ssh/config`, which nothing in this fleet does.
//
// Everything it does not recognise is a failure, INCLUDING a sentence in a locale it cannot read.
// The two mistakes are not symmetric: calling a real absence a failure is noise the operator can
// see, and calling a failure an absence is the silent horizon fleet spec §3.2 invariant 4 forbids.
// A `cat` that says `Is a directory` or `Permission denied` is the second kind — both rc=1, both
// meaning the declarations may be there and unread.
func theHopSaidThereIsNoConfig(stderr string, rc int) bool {
	if strings.TrimSpace(stderr) == "" {
		// Nothing on either stream and the command succeeded: the file is there and empty. A
		// runner that DROPS stderr looks exactly the same from here, which is why RemoteRunner's
		// doc makes passing it through a MUST — these two fields are the only evidence there is.
		return rc == 0
	}
	said := strings.ToLower(stderr)
	return strings.Contains(said, strings.TrimPrefix(remoteConfigPath, "~")) &&
		strings.Contains(said, "no such file")
}

// ranOutOfTime is the sentence an unresolved row carries when the deadline, rather than the hop, is
// what stopped it. One function and not two literals, because it is reached from both sides of a
// round trip — before one is spent, and after one was cut short — and two copies of a reason drift
// the day one of them is reworded.
func ranOutOfTime(hop, alias string, err error) string {
	return "the crawl of " + hop + " ran out of time before resolving " + alias + " (" +
		err.Error() + ") — raise the probe timeout, or lower the per-hop breadth so fewer " +
		"aliases share it"
}

// declaredBy turns a hop's config text into candidates, in the order the config names them, with
// the Include horizons appended.
//
// Candidacy is decided by the same skipReason the root's own config goes through — one predicate,
// because two copies of "what counts as a machine" drift the day one of them changes, and the copy
// on the remote path is the one nobody would notice drifting.
func declaredBy(hop, text string) []Candidate {
	var out []Candidate
	seen := map[string]bool{}
	names, nameErr := hostNamesIn(strings.NewReader(text))
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		skip := skipReason(name)
		if skip == "" {
			skip = unpasteableAlias(name)
		}
		out = append(out, Candidate{Alias: name, Skip: skip, Via: hop})
	}

	// An Include names declarations the hub cannot read: following it needs a glob on the hop's
	// filesystem, and the crawl reads a hop's config once. Saying nothing would leave a machine
	// declared only there invisible with no trace, which is the omission fleet spec §3.2
	// invariant 4 forbids — so the horizon is reported in the same shape a pattern is, a row
	// with a reason and no probe.
	globs, globErr := includeGlobsIn(strings.NewReader(text))
	for _, glob := range globs {
		if seen[glob] {
			continue
		}
		seen[glob] = true
		out = append(out, Candidate{
			Alias: glob,
			Via:   hop,
			Skip: "the hub does not read a hop's included files, so a machine declared only in " +
				glob + " on " + hop + " is invisible from here — declare it in that host's own " +
				"~/.ssh/config, or in the root's",
		})
	}

	// A reader that STOPPED is the second kind of horizon, and it must be a row for the same reason
	// an Include is: without one, a config the reader could not finish answers exactly what a hop
	// with nothing to offer answers. Both readers scan the same text, so they fail together and one
	// row is the whole truth — the source is named because a crawl reports several hops and "a line
	// was too long" without a machine's name is not actionable.
	if nameErr != nil || globErr != nil {
		err := nameErr
		if err == nil {
			err = globErr
		}
		source := hop + ":" + remoteConfigPath
		out = append(out, Candidate{Alias: source, Via: hop, Skip: unreadPast(source, err)})
	}
	return out
}

// budgetCut is the sentence a cut alias carries, and it is the GRAPH's sentence.
//
// It used to be a second wording of the same fact — `fleet.Graph.Allow` composed one for a crawl the
// budget cut, this composed another for a hop whose aliases it would not resolve — and the two answer
// the same question: this observer declares more than the crawl looked at. One decision in two places
// is what this repository has paid for three times in one day, so there is one builder now and
// rewording it is one edit. It still names the count and the knob, because a horizon with no count
// reads as a finished crawl and a reason with no remedy is a complaint (fleet spec §3.2 invariant 4).
func budgetCut(hop string, resolvable int) string {
	return fleet.BreadthCut(hop, resolvable, MaxAliasesPerHop)
}

// unpasteableAlias refuses an alias that cannot safely become part of a command, and it is a
// REMOTE-ONLY rule.
//
// An alias from the root's own config is handed to ssh as argv and never touches a shell, so
// refusing it there would drop a machine the hub can perfectly well probe. An alias from a HOP's
// config is a string read off another machine's file that this package pastes into a command run by
// that machine's login shell — and it cannot be quoted, because a quoted program name is a parse
// error at rc=0 in Nushell, which is the whole reason these payloads are bare. With quoting off the
// table the only safe move is to refuse what would change the command's meaning.
//
// So the rule is an allow-list of what an ssh alias legitimately contains, not a deny-list of what
// is dangerous: a deny-list of shell metacharacters is a claim about every shell that will ever run
// this, and there is no such claim to be had.
//
// The leading dash is separate and is not about shells at all — ssh would read `-oProxyCommand=x` as
// a flag, and `--` is not an escape here (measured on this repository's own seam: `set -t p @x --
// -wip` is rc=1 `too many arguments`).
func unpasteableAlias(alias string) string {
	if strings.HasPrefix(alias, "-") {
		return "starts with a dash, so ssh would read it as a flag rather than as a host — rename " +
			"it in that host's ssh config, or declare the machine in the root's own ~/.ssh/config"
	}
	for _, r := range alias {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == '_', r == '@', r == ':':
		default:
			return "contains " + strconv.QuoteRune(r) + ", which the hub will not paste into a " +
				"command on a remote login shell — rename it in that host's ssh config, or " +
				"declare the machine in the root's own ~/.ssh/config"
		}
	}
	return ""
}

// resolvedRecipe reads the keys a consumer needs out of `ssh -G`'s stdout.
//
// The shape is captured, not imagined — on 2026-08-20 OpenSSH 10.3p1 answered 79 lines of
// `key value`, lower-cased, one pair per line, and a value may itself contain spaces (`ipqos ef
// cs0`, `rekeylimit 0 0`, and a ProxyCommand of `docker exec -i tailscale-cortex nc %h %p`). So the
// cut is at the FIRST space and the rest is the value whole.
//
// A REPEATED KEY KEEPS ITS FIRST VALUE unless it is in `recipeSetKeys`, where the repeats are JOINED
// BY NEWLINES in ssh's own order. `identityfile` is the only key that repeats — measured over the
// whole answer, both for a stanza that configures a key and for a name declared nowhere — and it is
// also the only key whose answer is a set, so see `recipeSetKeys` for why the two facts are the same
// fact. Keeping the first was a defect, not a simplification: it made the one consumer of this key
// name a credential ssh would never have needed.
//
// It is deliberately tolerant: an unrecognised line is skipped and never an error, because this is
// another program's output and stderr may hold noise at rc=0 (`Pseudo-terminal will not be allocated
// because stdin is not a terminal.`, captured from the same runs). TrimSpace before the cut, so a
// `\r` from a pty does not travel into a value — this repository has already had one go a surface
// further than it should.
func resolvedRecipe(stdout string) map[string]string {
	var recipe map[string]string
	for _, line := range strings.Split(stdout, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		key = strings.ToLower(key)
		if !recipeKeys[key] {
			continue
		}
		value = strings.TrimSpace(value)
		if had, already := recipe[key]; already {
			if !recipeSetKeys[key] {
				continue
			}
			recipe[key] = had + "\n" + value
			continue
		}
		if recipe == nil {
			recipe = map[string]string{}
		}
		recipe[key] = value
	}
	return recipe
}
