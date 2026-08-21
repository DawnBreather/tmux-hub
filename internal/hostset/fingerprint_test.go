package hostset

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// serverKey is the fingerprint of the machine spec §2.2 calls `nuc`, and every fixture
// below that means "the server answered" carries this one. Re-measured 2026-08-20
// against live ssh: `ssh -v -o BatchMode=yes -o ConnectTimeout=6 nuc 'tmux -V; id -u'`
// puts it on stderr line 27 of 68.
const serverKey = "SHA256:Px9AwAvwtlm7dTELzGLm0WqaYPsFtA/7kMgsXQjfxr4"

// clientKey stands for the fingerprint of the OPERATOR's own private key, which the same
// transcript quotes four times. The value is neutralised (this repository is public) and
// only its SHAPE is load-bearing: it is a `SHA256:` fingerprint on a `debug1:` line that
// is not the server's. It is in the fixture because it is the hazard the marker anchor
// exists for — see fingerprint.go.
const clientKey = "SHA256:ThisIsTheCLIENTsOwnKeyNeutralised0000000000"

// The fixture is CAPTURED — these are the bytes real `ssh -v` printed on 2026-08-20, with
// the operator's home path, the host's FQDN and address, and the client key neutralised,
// since this repository is public. It is not hand-written, because this repository has
// twice shipped a defect from a fixture written from imagination — the `agents:` prefix
// and tmux's own quoting of `pane_start_command`.
//
// Two things it deliberately keeps that a hand-written one would not. The `loaded pubkey`
// line is the fourth-most-common shape in a real transcript and it quotes a `SHA256:`
// fingerprint that is NOT the server's, which is the whole reason the parse is anchored on
// ssh's marker; measured, the real transcript carries FOUR such lines (`loaded pubkey`,
// `Will attempt key`, `Offering public key`, `Server accepts key`) and one server line. And
// `tmux 3.2a` is absent: the version arrives on STDOUT, and putting it in a stderr fixture
// would be a small fiction about which stream carries what.
func TestParseHostKeysReadsWhatSSHActuallyPrints(t *testing.T) {
	const captured = `debug1: OpenSSH_10.3p1, OpenSSL 3.6.2 7 Apr 2026
debug1: Connecting to nuc.example.net [203.0.113.7] port 22.
debug1: loaded pubkey from /home/dev/.ssh/nuc: ED25519 ` + clientKey + `
debug1: Server host key: ssh-ed25519 ` + serverKey + `
debug1: Host 'nuc.example.net' is known and matches the ED25519 host key.
`
	got := ParseHostKeys(captured)
	if len(got) != 1 || got[0] != serverKey {
		t.Errorf("ParseHostKeys = %q, want %q", got, []string{serverKey})
	}
	// The anchor's whole job, asserted rather than argued: the operator's own key must
	// never be read as a machine's identity. It would be the SAME value for every host,
	// so under §2.3's set-intersection merging it fuses the entire fleet into one node.
	for _, fp := range got {
		if fp == clientKey {
			t.Errorf("ParseHostKeys returned the CLIENT's own key %q — that value is identical "+
				"on every host this operator probes, so every node would intersect with every "+
				"other and the graph would collapse to one vertex (spec §2.3)", fp)
		}
	}
}

// No handshake, no identity — this is the `cortex-web` case, measured: a proxy that never
// completed gives no fingerprint, and the machine must stay a candidate.
func TestNoHandshakeYieldsNoFingerprint(t *testing.T) {
	const failed = `debug1: Executing proxy command: exec docker exec -i tailscale-cortex nc 100.75.205.24 22
Connection timed out during banner exchange
`
	if got := ParseHostKeys(failed); len(got) != 0 {
		t.Errorf("ParseHostKeys invented %q from a failed handshake", got)
	}
}

// Identity is a SET (spec §2.3), so one machine named twice in one connection's output
// must contribute one member, not two. The line is the captured one repeated — a real
// line twice, rather than a second invented shape.
func TestParseHostKeysReportsEachFingerprintOnce(t *testing.T) {
	const line = "debug1: Server host key: ssh-ed25519 " + serverKey + "\n"
	if got := ParseHostKeys(line + line); len(got) != 1 {
		t.Errorf("ParseHostKeys = %q for one fingerprint seen twice, want one member", got)
	}
}

// A REFUSED connection still names the machine, and that is CORRECT. This test pins the
// consumer contract that makes it safe.
//
// Measured 2026-08-20, OpenSSH 10.3p1, a host absent from known_hosts under default
// strictness: rc=255, `debug1: Server host key: ssh-ed25519 SHA256:Px9Aw…` on stderr line
// 27 and ssh's own `Host key verification failed.` on line 34. The key exchange COMPLETED —
// that is precisely why the client can print the key it then declined to trust — so the
// transcript proves WHICH machine answered even though authentication never proceeded.
// Discarding it would throw away evidence the connection already paid for, and the machine
// would be unidentifiable for a reason that has nothing to do with its identity.
//
// So the contract is: `Usable` is the ONLY field that says a host may be used, and
// `Fingerprints` only ever says which machine said so. A consumer that reads "verified"
// off the presence of a fingerprint has read the wrong field (see Result.Fingerprints).
//
// Both halves are asserted in one test on purpose. A "fix" that suppressed the fingerprint
// on refusal would satisfy any test that checked `Usable` alone, and this repository has
// already paid for a mode implemented as *suppress the fact* rather than *say the fact
// differently* — the zero it returns is indistinguishable from the thing being absent.
func TestARefusedHandshakeNamesTheMachineAndIsStillNotUsable(t *testing.T) {
	const refused = `debug1: OpenSSH_10.3p1, OpenSSL 3.6.2 7 Apr 2026
debug1: SSH2_MSG_KEX_ECDH_REPLY received
debug1: Server host key: ssh-ed25519 ` + serverKey + `
debug1: load_hostkeys: fopen /home/dev/.ssh/known_hosts: No such file or directory
Host key verification failed.
`
	r := Probe(context.Background(), "h", time.Second,
		func(context.Context, string, ...string) (string, string, int) {
			return "", refused, 255
		})
	if r.Usable {
		t.Errorf("Usable = true for a host whose key ssh refused to trust (reason %q) — a "+
			"completed key exchange says which machine answered, never that it may be used",
			r.Reason)
	}
	if len(r.Fingerprints) != 1 || r.Fingerprints[0] != serverKey {
		t.Errorf("Result.Fingerprints = %q, want [%q] — the key exchange completed, so this "+
			"transcript proves which machine answered. Suppressing it because authentication "+
			"then failed discards evidence already paid for; `Usable` is what says the host is "+
			"unusable", r.Fingerprints, serverKey)
	}
}

// The probe is the only place the fingerprint is free, so `Probe` must publish it —
// and it must publish it for a host it is REFUSING as well. The handshake is what
// creates the node (spec §3.2 invariant 3); `tmux -V` answering nothing is a fact
// about the machine's software, not about which machine answered. A harvest gated on
// `Usable` would leave every tmux-less host without an identity, and those are exactly
// the candidates the picker has to name.
func TestProbePublishesTheFingerprintItsOwnHandshakeAlreadyRevealed(t *testing.T) {
	const fp = serverKey
	for _, tc := range []struct {
		name           string
		stdout, stderr string
		rc             int
		wantUsable     bool
	}{
		{
			name:   "a usable host",
			stdout: "tmux 3.2a\n1000\n",
			stderr: "debug1: Connecting to nuc port 22.\n" +
				"debug1: Server host key: ssh-ed25519 " + fp + "\n",
			rc:         0,
			wantUsable: true,
		},
		{
			// Measured on five of this machine's hosts: the shell's status belongs to
			// `id -u`, so tmux's own 127 is swallowed and rc is 0. The connection was
			// made all the same.
			name:   "a host with no tmux",
			stdout: "1000\n",
			stderr: "debug1: Server host key: ssh-ed25519 " + fp + "\n" +
				"zsh:1: command not found: tmux\n",
			rc:         0,
			wantUsable: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Probe(context.Background(), "h", time.Second,
				func(context.Context, string, ...string) (string, string, int) {
					return tc.stdout, tc.stderr, tc.rc
				})
			if r.Usable != tc.wantUsable {
				t.Fatalf("Usable = %v, want %v (reason %q)", r.Usable, tc.wantUsable, r.Reason)
			}
			if len(r.Fingerprints) != 1 || r.Fingerprints[0] != fp {
				t.Errorf("Result.Fingerprints = %q, want [%q] — identity comes free from the "+
					"handshake this probe already made", r.Fingerprints, fp)
			}
		})
	}
}

// A host that never answered must carry no identity, or the graph gains a node nobody
// has spoken to (spec §3.2 invariant 3).
func TestProbeInventsNoFingerprintForAHostThatNeverAnswered(t *testing.T) {
	r := Probe(context.Background(), "h", time.Second,
		func(context.Context, string, ...string) (string, string, int) {
			return "", "ssh: connect to host 20.127.207.74 port 22: Connection timed out\n", 255
		})
	if len(r.Fingerprints) != 0 {
		t.Errorf("Result.Fingerprints = %q for a host that never completed a handshake",
			r.Fingerprints)
	}
}

// `-v` bought identity, and it also put ssh's whole debug transcript on the stderr the
// REMEDY is built from. Both halves of the damage are measured, on 2026-08-20, through
// the production argv:
//
//	a HEALTHY host's 68-line transcript carries `debug1: Remote: …/authorized_keys:3:
//	key options: …` TWICE, and reasonFor's git-remote clause matches `remote:` — so a
//	host that answered ssh perfectly and merely lacks tmux would be told it is a git
//	remote and to leave it off;
//
//	the FIRST line of any verbose transcript is `debug1: OpenSSH_10.3p1, OpenSSL …`,
//	so every reason that quotes ssh's message would quote the version banner instead
//	of the failure — the label kept and the action lost, which is this repository's
//	oldest defect class.
//
// The fixtures are captured (the operator's home path neutralised, since this
// repository is public); the second case is github.com's real answer and it is the pole
// that proves the strip did not throw the true signal away with the noise.
func TestTheReasonIsBuiltFromSSHsOwnWordsNotItsDebugTranscript(t *testing.T) {
	// The preamble and the epilogue are the real transcript's shape, and the ORDER is
	// load-bearing: on a connection that completed, ssh's unprefixed `Authenticated to …`
	// note comes BEFORE whatever the remote said, and its byte-count summary after. A
	// fixture with only the `debug1:` lines is the trap this repository keeps paying for —
	// it would let a fix that strips only the debug prefix look finished while the reason
	// quotes ssh's progress note instead of the remote's complaint.
	const verbosePreamble = `debug1: OpenSSH_10.3p1, OpenSSL 3.6.2 7 Apr 2026
debug1: Reading configuration data /etc/ssh/ssh_config
debug1: Server host key: ssh-ed25519 ` + serverKey + `
debug1: Remote: /home/dev/.ssh/authorized_keys:3: key options: agent-forwarding port-forwarding pty user-rc x11-forwarding
Authenticated to a-host.example.net ([203.0.113.7]:22) using "publickey".
`
	const verboseEpilogue = `Transferred: sent 3572, received 3932 bytes, in 0.5 seconds
Bytes per second: sent 6916.2, received 7613.2
`
	// A FIRST connection carries a FOURTH unprefixed line, and it arrives BEFORE
	// `Authenticated to …` rather than after. Measured 2026-08-20, OpenSSH 10.3p1, an
	// empty UserKnownHostsFile with `StrictHostKeyChecking=accept-new`: line 31 of 62 is
	// this warning, `Authenticated to …` is line 48, and the two counters are 61 and 62.
	// So the note list is a claim about someone else's program and it was one line short.
	const firstConnectionPreamble = `debug1: OpenSSH_10.3p1, OpenSSL 3.6.2 7 Apr 2026
debug1: Server host key: ssh-ed25519 ` + serverKey + `
Warning: Permanently added 'a-host.example.net' (ED25519) to the list of known hosts.
Authenticated to a-host.example.net ([203.0.113.7]:22) using "publickey".
`
	for _, tc := range []struct {
		name   string
		stderr string
		rc     int
		want   string // the reason must contain this
		deny   []string
	}{
		{
			// A host that answered, has no tmux, and whose shell words the table does not
			// recognise — the Nushell machine's shape, and the one case where the false
			// `remote:` match is reachable, because `command not found` is tested first.
			name:   "a host whose refusal the table does not recognise",
			stderr: verbosePreamble + "Error: nu::parser::parse_mismatch\n" + verboseEpilogue,
			rc:     0,
			want:   "Error: nu::parser::parse_mismatch",
			deny:   []string{"git remote", "OpenSSH_", "debug1:", "Authenticated to", "Bytes per second"},
		},
		{
			// github.com's real answer, captured. Still a git remote after the strip.
			name:   "a real git remote",
			stderr: verbosePreamble + "Invalid command: tmux -V; id -u\n" + verboseEpilogue,
			rc:     1,
			want:   "not a shell host",
			deny:   []string{"OpenSSH_", "debug1:", "Authenticated to"},
		},
		{
			// Measured: ssh's own message is the last of five lines under -v, and the
			// connection never completed, so there is no `Authenticated to` note.
			name:   "a name that does not resolve",
			stderr: verbosePreamble + "ssh: Could not resolve hostname nope: Name or service not known\n",
			rc:     255,
			want:   "does not resolve",
			deny:   []string{"OpenSSH_", "debug1:"},
		},
		{
			// A completed connection whose only stderr is ssh's own verbose bookkeeping:
			// nothing is quotable, and the sentence must not trail off after its dash into
			// ssh's byte counts, which say nothing about why the host was refused.
			name:   "a completed connection that said nothing of its own",
			stderr: verbosePreamble + verboseEpilogue,
			rc:     0,
			want:   "no tmux version on stdout",
			deny:   []string{"OpenSSH_", "debug1:", "Authenticated to", "Transferred", "Bytes per second"},
		},
		{
			// The FIRST connection to a host, which is what fleet discovery spends its
			// whole life making. The remote's complaint is the only quotable thing here and
			// ssh's own note about updating known_hosts must not displace it — `firstLine`
			// takes the first survivor, so an unstripped note wins by position.
			name:   "a first connection whose remote complained",
			stderr: firstConnectionPreamble + "Error: nu::parser::parse_mismatch\n" + verboseEpilogue,
			rc:     0,
			want:   "Error: nu::parser::parse_mismatch",
			deny: []string{"Permanently added", "known hosts", "OpenSSH_", "debug1:",
				"Authenticated to", "Bytes per second"},
		},
		{
			// The same first connection with NOTHING of the remote's own: every line is
			// ssh's, so there is nothing to quote at all.
			name:   "a first connection that said nothing of its own",
			stderr: firstConnectionPreamble + verboseEpilogue,
			rc:     0,
			want:   "no tmux version on stdout",
			deny: []string{"Permanently added", "known hosts", "OpenSSH_", "debug1:",
				"Authenticated to", "Transferred", "Bytes per second"},
		},
		{
			// The OTHER label that quotes ssh, reached with nothing to quote. rc=255 is
			// ssh's own failure code, but it is also what ssh reports when the REMOTE
			// command exits 255 — and then the connection completed, so every stderr line
			// is ssh's bookkeeping and the strip leaves nothing. Both labels share one
			// function for exactly this reason; a second copy would drift.
			name:   "a remote command that exited 255 and said nothing",
			stderr: verbosePreamble + verboseEpilogue,
			rc:     255,
			want:   "ssh failed",
			deny: []string{"OpenSSH_", "debug1:", "Authenticated to", "Transferred",
				"Bytes per second"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Probe(context.Background(), "h", time.Second,
				func(context.Context, string, ...string) (string, string, int) {
					return "", tc.stderr, tc.rc
				})
			if !strings.Contains(r.Reason, tc.want) {
				t.Errorf("Reason = %q, want it to contain %q", r.Reason, tc.want)
			}
			for _, no := range tc.deny {
				if strings.Contains(r.Reason, no) {
					t.Errorf("Reason = %q quotes %q — that is ssh's debug transcript, not its "+
						"answer, and the operator cannot act on it", r.Reason, no)
				}
			}
			// A reason ending in a dash is a sentence whose claimant went missing: the
			// label promised a quote and the strip left nothing to quote. This repository
			// treats a refusal as a layout object, and the trailing ` — ` is the visible
			// tell. It is asserted for EVERY case rather than for the one that produces it,
			// because two labels build a sentence this way and both can reach it.
			if trimmed := strings.TrimRight(r.Reason, " "); strings.HasSuffix(trimmed, "—") {
				t.Errorf("Reason = %q trails off after its dash — the label was kept and the "+
					"clause it promised is missing, which is this repository's oldest defect "+
					"class", r.Reason)
			}
			// The transcript is still where identity comes from: the strip belongs to the
			// remedy alone, and stripping it for both would have thrown the fingerprint away.
			if len(r.Fingerprints) != 1 {
				t.Errorf("Fingerprints = %q — the debug transcript is what carries identity, "+
					"and only the reason may be built without it", r.Fingerprints)
			}
		})
	}
}

// Not a gate — a look at the real thing, in the shape probe_test.go already uses for
// the fleet, and it skips with a reason when there is no host to point it at.
//
// It earns its place because every fixture above is a claim about what ANOTHER program
// prints: ssh is free to reword `Server host key:` in any release, and the day it does,
// every test in this file still passes while the hub loses identity for the whole fleet.
// This is the one check that would notice. Run it as
// `HOSTSET_REAL_HOST=<alias> go test ./internal/hostset/ -run RealHandshake -v`.
//
// What it deliberately does NOT prove: that the production runner passes `-v`. It builds
// its own argv below, so it would stay green with the flag removed from `cmd/tmux-hub`'s
// probeArgs. That guard is `cmd/tmux-hub`'s TestTheProbeAsksSSHWhichMachineAnswered, and
// it has to live there because probeArgs is in package main and cannot be imported.
func TestParseHostKeysAgainstARealHandshake(t *testing.T) {
	alias := os.Getenv("HOSTSET_REAL_HOST")
	if alias == "" {
		t.Skip("HOSTSET_REAL_HOST unset — there is no host to hand a real handshake to")
	}
	// This argv is NOT a copy of production's and must not be read as one — production
	// builds `ConnectTimeout` from a constant in another package, and a test that claimed
	// to mirror it would silently stop doing so the day that constant moves. Only one
	// element here is load-bearing for what this test asserts: `-v`, which is what puts
	// the host key on stderr. BatchMode refuses a password prompt nobody can see, and the
	// timeout is this test's own bound on a machine that may be powered off.
	var out, errb bytes.Buffer
	cmd := exec.Command("ssh", "-v", "-o", "BatchMode=yes", "-o", "ConnectTimeout=6",
		alias, probePayload)
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()

	got := ParseHostKeys(errb.String())
	t.Logf("%s: rc-err=%v stdout=%q fingerprints=%v", alias, runErr, out.String(), got)
	if len(got) == 0 {
		t.Fatalf("a real handshake with %s yielded no fingerprint. ssh's own words were:\n%s\n"+
			"If the connection did not complete, that is the machine and not this parser; if it "+
			"did, ssh has reworded the line this parser is anchored on (%q) and the hub has just "+
			"lost identity for the whole fleet", alias, sshOwnWords(errb.String()), hostKeyMarker)
	}
	// The fingerprint is what it is, but its SHAPE is ssh's contract: a hash name, a
	// colon, and base64. A parser that returned the key TYPE would pass a len check.
	for _, fp := range got {
		if !strings.Contains(fp, ":") || strings.HasPrefix(fp, "ssh-") {
			t.Errorf("fingerprint %q looks like a key type rather than a hash — the field after "+
				"the marker is `<type> <fingerprint>` and the second one is the identity", fp)
		}
	}
}

// The payload must contain no quoted program name and no POSIX-only operator: a quoted
// program name is a parse error in Nushell AT rc=0, which is how a host went invisible.
func TestTheProbePayloadIsNotPOSIXOnly(t *testing.T) {
	for _, forbidden := range []string{"'tmux'", "&&", "2>&1", "$(", "`"} {
		if strings.Contains(probePayload, forbidden) {
			t.Errorf("the probe payload contains %q, which a non-POSIX login shell refuses", forbidden)
		}
	}
}
