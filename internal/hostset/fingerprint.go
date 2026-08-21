package hostset

import "strings"

// hostKeyMarker is ssh's own wording, and the parse is anchored on it rather than on
// `SHA256:` because most of the fingerprints in a verbose transcript are the CLIENT's.
//
// Measured 2026-08-20, OpenSSH 10.3p1, one ordinary probe of one host: five lines quote a
// `SHA256:` fingerprint and exactly ONE of them is the server's.
//
//	debug1: loaded pubkey from …/.ssh/nuc: ED25519 SHA256:9AU7cw…      the operator's key
//	debug1: Server host key: ssh-ed25519 SHA256:Px9Aw…                 the machine's
//	debug1: Will attempt key: …/.ssh/nuc ED25519 SHA256:9AU7cw… …      the operator's key
//	debug1: Offering public key: …/.ssh/nuc ED25519 SHA256:9AU7cw… …   the operator's key
//	debug1: Server accepts key: …/.ssh/nuc ED25519 SHA256:9AU7cw… …    the operator's key
//
// A `SHA256:` anchor would therefore harvest the operator's own key — the SAME value on
// every host they probe — so under §2.3's set-intersection merging every node would
// intersect every other and the whole fleet would collapse to one vertex.
//
// It is NOT anchored here to keep a refusal from yielding identity, and an earlier version
// of this comment said so wrongly. Measured on the same version: a host absent from
// known_hosts prints this marker on line 27 and `Host key verification failed.` on line 34
// at rc=255, so a refusal DOES carry the marker. That is correct and deliberate — the key
// exchange completed, so the machine did identify itself — and the safeguard is the
// consumer contract on Result.Fingerprints, not a filter here.
const hostKeyMarker = "Server host key: "

// ParseHostKeys reads the machine identities out of ssh's verbose stderr, in the order
// ssh reported them, each one once.
//
// The line it reads is captured, not imagined — on 2026-08-20 `ssh -v` printed
//
//	debug1: Server host key: ssh-ed25519 SHA256:Px9AwAvwtlm7dTELzGLm0WqaYPsFtA/7kMgsXQjfxr4
//
// so the shape after the marker is `<type> <fingerprint>` and the fingerprint is the
// SECOND field. The fingerprint is taken verbatim rather than being checked against
// `SHA256:`: `FingerprintHash md5` is a legal client setting and the hash ssh chose is
// ssh's business, while a filter here would silently answer "no identity" for a
// perfectly good handshake.
//
// It is deliberately tolerant — an unrecognised line is skipped and never an error —
// because this input is another program's debug output and the caller is a probe whose
// job is to report a remedy, not to fail. No marker means no identity, which is the
// answer a proxy that timed out during banner exchange deserves.
//
// A PROXIED CONNECTION'S FINGERPRINT BELONGS TO THE JUMP, AND THIS FUNCTION CANNOT TELL.
// It sees one stderr and no recipe, so the caller owns the distinction. Measured on
// OpenSSH 10.x, 2026-08-20 (spec §2.2.1):
//
//	ssh -v nuc             1 `Server host key` line   nuc's
//	ssh -v dev-air         1                          dev-air's
//	ssh -v -J nuc dev-air  1                          **nuc's** — the JUMP host's
//	the same at -vv, -vvv  1                          still the jump host's
//
// and one run of the jumped case reported ZERO, because the jump's handshake happens in a
// child process whose verbose output races the parent's. So the hazard is not "several
// hops give several fingerprints" — it is that a proxied connection gives ONE fingerprint
// and it is the wrong machine's. Under §2.3's set-intersection merging that is not a
// missing fact but a WRONG one: the destination inherits the jump's key, the sets
// intersect, and two machines collapse into one node.
//
// THE RULE: a fingerprint is evidence of identity only from a DIRECT connection. If the
// resolved recipe names `proxyjump` or `proxycommand`, the fingerprint from that
// connection is DISCARDED and the machine stays a candidate with that as its reason.
// Detecting it costs nothing — `ssh -G -J nuc dev-air` reports `proxyjump nuc` while a
// direct `ssh -G dev-air` reports neither key — but it needs the resolved recipe, which
// this function does not have. The hub's own probe argv is direct, and
// `cmd/tmux-hub`'s TestTheProbeArgvIsADirectConnection is what keeps it so; gating on
// config-supplied proxying lands with the resolved recipes.
func ParseHostKeys(stderr string) []string {
	var found []string
	seen := map[string]bool{}
	for _, line := range strings.Split(stderr, "\n") {
		at := strings.Index(line, hostKeyMarker)
		if at < 0 {
			continue
		}
		// Fields, not Split(" "), so a `\r\n` ending contributes no phantom field: ssh's
		// stderr arrives with CRLF endings when it came through a pty, and this
		// repository has already had a `\r` travel one surface further than it should.
		f := strings.Fields(line[at+len(hostKeyMarker):])
		if len(f) < 2 {
			continue
		}
		if fp := f[1]; !seen[fp] {
			seen[fp] = true
			found = append(found, fp)
		}
	}
	return found
}
