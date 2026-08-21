package fleet

import "strings"

// Diagnose reads a resolved recipe and answers what the root may do about the machine it names
// (fleet spec §3.4, §7 decision 7).
//
// THE REMEDY IS THE PRODUCT. `unreachable` is not a reason — it names no act — and invariant 4 wants
// every candidate to carry one, so this function returns either `Blocked` with a sentence the operator
// can act on or `Ready` with nothing to say. It is a DIAGNOSIS and not a verification: a fingerprint
// for a machine the root cannot reach could only come from running ssh on the hop, which is part 7, so
// everything here is read from evidence the root already holds.
//
// It names exactly TWO obstacles, and guesses at nothing else:
//
//   - a PROXIED recipe (§2.2.1). A proxied handshake reports the JUMP host's key — measured on
//     OpenSSH 10.x, `ssh -v -J nuc dev-air` prints one `Server host key` line and it is nuc's, at every
//     verbosity — so under §2.3's set-intersection merging the destination would inherit the jump's
//     identity and two machines would collapse into one node. v1 therefore cannot identify such a
//     machine at all, which is a fact about the transport and not a failure of the probe.
//   - NO NAMED IDENTITY held here. The recipe names the keys ssh would offer; if none of them is on
//     this machine, the root holds no credential for it.
//
// Ready means only "the recipe names no obstacle", never "it answers". `stateOf` clamps a node state
// claimed by an unverified declaration, so a Ready diagnosis on a row nobody has shaken hands with
// reads as `Candidate` and the probe's own failure sentence is what lands. In the other direction
// `stateOf` DROPS a Blocked remedy the moment a handshake refutes it
// (TestAStateThatContradictsVerificationIsRefused), which is why erring toward Blocked is the cheap
// direction: a wrong Blocked costs one row a remedy it did not need until the next successful probe,
// while a wrong Ready costs the operator the one command that would have fixed the machine.
//
// Ready carries NO reason on purpose. The remedies for the four unmounted states live in
// `defaultReason` and nowhere else, they are phrased around the ALIAS, and this function is never
// given one — it sees a recipe. `Observe` fills the sentence in from that one place; a second copy of
// it here is how three rules in this repository diverged in a single day.
//
// There is no I/O, no clock and no environment read. The file question is the caller's predicate, and
// the path is handed over exactly as ssh reported it — measured, `ssh -G` does not expand `~` — so the
// caller owns HOME. A nil predicate is a caller that cannot answer, which is not the same as `no`: the
// identity half is then skipped rather than dereferenced, and the row stays undiagnosed.
//
// The keys read here are the ones `ssh -G` prints, lowercase, which is what `Node.Recipe` promises to
// carry. Two measurements are load-bearing and both were taken on OpenSSH_10.3p1, 2026-08-20:
// `proxyusefdpass no` is on EVERY direct recipe and is the only key beginning with `proxy` there, so
// the proxy question is an exact lookup of two keys and never a prefix scan; and `ProxyJump none` /
// `ProxyCommand none` — the idiom that cancels a wildcard stanza — produce no line at all, ssh having
// resolved the cancellation itself, so there is no `none` token to special-case and a branch for one
// would be a rule that can never run.
func Diagnose(recipe map[string]string, haveLocal func(path string) bool) (State, string) {
	if via := proxiedThrough(recipe); via != "" {
		machine := machineIn(recipe)
		// The act names no destination, and that is deliberate: a hostname is unbounded, the row this
		// reason is printed on already says which machine it is about, and a destination inside the act
		// pushes the act itself past the width the terminal has. So the verb phrase is 31 columns and
		// always readable; the two names follow it.
		return Blocked, "give this machine a direct route — " + via + " stands between it and " +
			machine + ", and a proxied handshake reports the proxy's host key rather than " + machine +
			"'s, so the hub cannot tell which machine answered"
	}
	keys := identityFiles(recipe)
	if len(keys) == 0 || !offersPublicKeys(recipe) || heldHere(keys, haveLocal) {
		return Ready, ""
	}
	// Two remedies, because the hub cannot know which the operator prefers: put this machine's own key
	// on the far side, or bring the key the recipe names here. Both are §3.4's own wording, which names
	// them as `ssh-copy-id …` and "copy `~/.ssh/<name>` here" — a bare command and a bare instruction,
	// with no clause explaining either. That is deliberate and it is measured: an earlier cut of this
	// sentence appended "to authorise this machine's own key", which made the act 79 columns for a host
	// on a non-default port — one column past the 78 a whole act may occupy, so at the 80 columns §16
	// commits to the operator read a truncated command. The command names what it does; the sentence's
	// own tail says why it is needed; the clause bought nothing and cost the act its margin. Dropping it
	// leaves 25 columns of headroom — measured by sweeping the clause length, where 25 survives and 26
	// breaks — and the BINDING row is `a tailnet-scale hostname` at 53 columns, not `a port` at 43.
	//
	// The ORDER is measured rather than chosen, and the measurement is POSITIONAL rather than about
	// length: every claimant here is bounded except the key list, and with the five default paths the
	// sentence is 198 columns either way. What the order decides is WHERE the command sits — command
	// first it ENDS at column 35, key list first it STARTS at column 125, so at the 80 columns §16
	// commits to the operator reads a list of files they do not have and never the command. That is this
	// repository's oldest defect class (keep the label, lose the action) reappearing inside a sentence
	// written to avoid it, which is why the list goes LAST.
	var acts []string
	if login := loginIn(recipe); login != "" {
		acts = append(acts, "run `ssh-copy-id "+login+"`")
	}
	acts = append(acts, "copy "+oneOf(keys)+" to this machine")
	return Blocked, strings.Join(acts, ", or ") + " — the recipe names no key that is here"
}

// proxiedThrough names what stands between the root and the machine, or returns "" for a direct
// recipe. The phrase is composed here rather than at the call site because the two forms differ in
// what they can say: a `ProxyJump` names a host the operator knows, while a `ProxyCommand` is a
// command they would edit — measured on the live fleet, `ProxyCommand docker exec -i tailscale-cortex
// nc %h %p`, reported by `ssh -G` with `%h %p` unexpanded.
//
// Both are returned as a SUBJECT, so one sentence serves both and neither needs the reader to know
// which mechanism produced it. `ssh -J a,b` resolves to `proxyjump a,b`, which is a chain and is
// printed as the operator wrote it: this function names the obstacle, and does not pretend to know
// which link of a chain the operator should act on.
func proxiedThrough(recipe map[string]string) string {
	if jump := strings.TrimSpace(recipe["proxyjump"]); jump != "" {
		return jump
	}
	if cmd := strings.TrimSpace(recipe["proxycommand"]); cmd != "" {
		return "the proxy command `" + cmd + "`"
	}
	return ""
}

// identityFiles is the set of keys the recipe names, one to a line.
//
// The framing is not a choice: `ssh -G` prints one `identityfile` line PER KEY — five for a host with
// no configuration at all (`~/.ssh/id_rsa`, `id_ecdsa`, `id_ecdsa_sk`, `id_ed25519`, `id_ed25519_sk`),
// and only the configured ones once `IdentityFile` is set — so a `map[string]string` can carry the set
// only by keeping the newline. Splitting on whitespace instead would break a path that contains a
// space; not splitting at all would read five named keys as one absent file.
func identityFiles(recipe map[string]string) []string {
	var out []string
	for _, line := range strings.Split(recipe["identityfile"], "\n") {
		if path := strings.TrimSpace(line); path != "" {
			out = append(out, path)
		}
	}
	return out
}

// offersPublicKeys reports whether a key file can be the obstacle at all. A host that refuses public
// keys cannot be short of one, and `ssh-copy-id` would change nothing on it. Measured:
// `PubkeyAuthentication no` is reported as `pubkeyauthentication false`, and the `identityfile` lines
// stay in the output beside it, so the file check alone would name a remedy that does not apply.
//
// Absent reads as ON, which is ssh's own default: a producer that did not carry the field must not
// switch the whole diagnosis off, and anything that is not a refusal leaves public keys in play.
func offersPublicKeys(recipe map[string]string) bool {
	switch strings.ToLower(strings.TrimSpace(recipe["pubkeyauthentication"])) {
	case "false", "no":
		return false
	}
	return true
}

// heldHere reports whether any ONE of these paths is on this machine, which is the question ssh itself
// asks: it offers each identity in turn, so one is enough. Demanding all of them would report the
// whole fleet Blocked, since the five default paths are named for every host and almost nobody holds
// every one.
//
// A nil predicate answers yes — not because the keys are here, but because a caller that cannot look
// has produced no obstacle, and a diagnosis nobody could check is not a diagnosis.
func heldHere(paths []string, haveLocal func(path string) bool) bool {
	if haveLocal == nil {
		return true
	}
	for _, path := range paths {
		if haveLocal(path) {
			return true
		}
	}
	return false
}

// oneOf names the keys, saying plainly that any one of them would do. Naming only the first would be a
// lie by omission when ssh would have offered five.
func oneOf(paths []string) string {
	if len(paths) == 1 {
		return paths[0]
	}
	return "one of " + strings.Join(paths, ", ")
}

// loginIn builds the argument `ssh-copy-id` needs, or "" when the recipe cannot name one.
//
// The resolved `user@hostname` is the only form that works from HERE: the ALIAS belongs to the hop's
// ssh config, so `ssh-copy-id <alias>` would resolve in the root's own config, which by definition
// does not hold that stanza — that is the whole reason the machine is a candidate. The port is spelled
// out only when it is not 22, since `ssh -G` always reports one and a remedy carrying `-p 22` reads as
// a special case where there is none.
func loginIn(recipe map[string]string) string {
	host := strings.TrimSpace(recipe["hostname"])
	if host == "" {
		return ""
	}
	if user := strings.TrimSpace(recipe["user"]); user != "" {
		host = user + "@" + host
	}
	if port := strings.TrimSpace(recipe["port"]); port != "" && port != "22" {
		host = "-p " + port + " " + host
	}
	return host
}

// machineIn names the machine a sentence is about. `ssh -G` always reports a hostname — measured, even
// for a host no config mentions — so the fallback is for a hand-built recipe, and it keeps the
// sentence readable instead of leaving a gap where a field should have been.
func machineIn(recipe map[string]string) string {
	if host := strings.TrimSpace(recipe["hostname"]); host != "" {
		return host
	}
	return "it"
}
