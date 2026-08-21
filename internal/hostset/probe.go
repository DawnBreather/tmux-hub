package hostset

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Result is what one probe learns about a candidate.
type Result struct {
	Alias    string
	Version  string
	Reason   string
	Usable   bool
	TimedOut bool
	Took     time.Duration

	// Fingerprints names WHICH MACHINE answered, and nothing else. A consumer derives
	// "this host is verified and may be used" from Usable — NEVER from a fingerprint
	// being present.
	//
	// The distinction is not theoretical: a connection whose host key ssh then refused to
	// trust fills this field. Measured 2026-08-20 on OpenSSH 10.3p1 against a host absent
	// from known_hosts, `debug1: Server host key: ssh-ed25519 SHA256:Px9Aw…` lands on
	// stderr line 27 and ssh's `Host key verification failed.` on line 34, at rc=255. The
	// key exchange COMPLETED — that is why the client can print the key it declined — so
	// the machine really did identify itself, and throwing that away would discard
	// evidence the round trip already paid for. What must not happen is reading it as
	// permission.
	//
	// It is filled unconditionally on Usable for the same reason: a machine with no tmux
	// is still a machine the root has spoken to, and those are exactly the candidates the
	// picker has to name.
	//
	// It is a SET rather than a scalar because one machine may hold several host keys and
	// which is presented depends on negotiation (spec §2.3). It is EMPTY when no handshake
	// completed, which is what makes "no fingerprint, therefore no node" sound — see
	// ParseHostKeys for the one case where a NON-empty set must still be discarded.
	Fingerprints []string
}

// probePayload is what the probe asks every host to run, and every character of it is
// a decision.
//
// `;` rather than `&&`: the point of the second command is to learn the remote uid
// even from a host with no tmux, and `&&` would drop it exactly there.
//
// The program names are BARE. Measured 2026-08-20: a live macOS host whose login shell
// is Nushell answered a quoted program name with `Error: nu::parser::parse_mismatch`
// **at rc=0**, so the poll read as a successful poll of a host with no panes and the
// whole machine was invisible. Both `tmux` and `id` are external commands and `;`
// separates in Nushell too, so this payload answers `tmux 3.7b` and `501` there.
const probePayload = "tmux -V; id -u"

// Runner is how the tests inject a fake ssh without needing real network or keys.
//
// Production implementations MUST use `-v -o BatchMode=yes -o ConnectTimeout=6`.
// BatchMode prevents ssh from blocking on password/passphrase prompts indefinitely,
// and ConnectTimeout caps a non-answering host at 6 s instead of the system default
// (~2 minutes). Without them, probing 20 candidates took 134 s; with them, 7 s.
//
// `-v` is what puts `Server host key: …` on stderr, and that is the only place a
// Result's identity can come from. A runner without it answers every probe with an
// empty Fingerprints — which is the same answer a machine that never completed a
// handshake gives, so the omission is silent and reads as an unreachable fleet.
type Runner func(ctx context.Context, alias string, args ...string) (stdout, stderr string, rc int)

var tmuxVersion = regexp.MustCompile(`^tmux (\S+)`)

// Probe asks one host whether it can host the hub, and answers with a remedy when
// it cannot.
//
// Membership keys on STDOUT. Measured on five of this machine's hosts at once,
// `ssh host 'tmux -V; id -u'` returns rc=0 with no tmux installed, because a
// shell's status belongs to its last command and `tmux -V`'s own 127 is swallowed.
// An rc-keyed probe admits ten hosts where five are usable, and the other five fail
// mysteriously later, which is the worst direction for this to be wrong in.
//
// It also reports WHICH MACHINE answered, and that costs no round trip: ssh prints the
// server's host key fingerprint on the stderr this function already reads to build a
// remedy, so identity is a by-product of a connection the picker was making anyway
// (spec §2.2). The harvest is unconditional on `Usable` on purpose — a machine with no
// tmux is still a machine the root has spoken to, and those are exactly the candidates
// the picker has to name. It needs `-v` on the runner; without it the field is empty,
// which reads as "nobody answered".
func Probe(ctx context.Context, alias string, timeout time.Duration, ssh Runner) Result {
	start := time.Now()
	out, errOut, rc := ssh(ctx, alias, probePayload)
	r := Result{Alias: alias, Took: time.Since(start), Fingerprints: ParseHostKeys(errOut)}

	// Detect timeout: the context deadline fired rather than ssh answering. A host
	// measured at 5.4 s, 9.1 s, 15.7 s and 18.4 s (3.4× swing) straddles any fixed
	// timeout, so keying membership on "did it answer within N" makes its presence a
	// coin flip. Timeouts are a third state: not usable right now, not excluded.
	timedOut := ctx.Err() == context.DeadlineExceeded
	r.TimedOut = timedOut

	if m := tmuxVersion.FindStringSubmatch(strings.TrimSpace(out)); m != nil {
		r.Usable, r.Version = true, m[1]
		return r
	}
	r.Reason = reasonFor(errOut, rc, timedOut, timeout)
	return r
}

// ProbeAll runs one goroutine per candidate whose Skip is empty, collecting results
// in input order. Each probe gets its own timeout.
func ProbeAll(ctx context.Context, cands []Candidate, timeout time.Duration, ssh Runner) []Result {
	results := make([]Result, len(cands))
	var wg sync.WaitGroup
	for i, c := range cands {
		if c.Skip != "" {
			results[i] = Result{Alias: c.Alias, Reason: c.Skip}
			continue
		}
		wg.Add(1)
		go func(i int, alias string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			results[i] = Probe(ctx, alias, timeout, ssh)
		}(i, c.Alias)
	}
	wg.Wait()
	return results
}

var debugLine = regexp.MustCompile(`^debug[0-9]+: `)

// verboseNotes are the lines ssh prints ABOUT ITSELF at `-v` without a `debug` prefix,
// so the prefix rule alone does not reach them. Measured 2026-08-20 on OpenSSH 10.3p1
// through the production argv: on a host already in known_hosts a completed connection's
// unprefixed stderr is `Authenticated to …` (line 48 of 68), which arrives BEFORE
// anything the remote said, and the two counters after — which is why quoting "the first
// line ssh wrote" quoted ssh's progress note and dropped the remote's own complaint.
//
// The FIRST connection to a host adds a fourth, and it arrives before all three.
// Re-measured on the same version with an empty UserKnownHostsFile and
// `StrictHostKeyChecking=accept-new`: `Warning: Permanently added '…' (ED25519) to the
// list of known hosts.` is line 31 of 62, `Authenticated to …` line 48, the counters 61
// and 62. That one matters more than it looks, because fleet discovery's whole job is
// probing config entries nobody has connected to yet — the case this list was one line
// short for is the case the feature exists to serve.
//
// It is a measured list rather than a rule because ssh gives these no marker. The cost
// of a phrase ssh adds later is bounded: no entry in reasonFor's table matches any of
// them, so it can never cause a MISCLASSIFICATION, only a noisier quote — and `because`
// keeps the fully-stripped outcome from being a sentence with a missing clause.
var verboseNotes = []string{
	"Authenticated to ",
	"Transferred: sent ",
	"Bytes per second: sent ",
	"Warning: Permanently added ",
}

// sshOwnWords drops ssh's verbose transcript and keeps what ssh said to the operator.
//
// The probe runs with `-v` so the handshake reveals which machine answered, and that
// makes every line of ssh's internal narration arrive on the same stderr the REMEDY is
// built from. Measured 2026-08-20 through the production argv, that costs the remedy
// twice over: a healthy host's 68-line transcript carries `debug1: Remote:
// …/authorized_keys:3: key options: …` twice, which the git-remote clause matches, so a
// machine that merely lacks tmux is told to leave it off as a git remote — and it beats
// the DNS clause too, since `remote:` is tested first, so an unresolvable name got the
// same wrong sentence. The first line of any verbose transcript is the version banner,
// so quoting "ssh's message" quoted `debug1: OpenSSH_10.3p1, OpenSSL …`.
//
// Two rules, because one is not enough: the `debugN: ` prefix covers the transcript,
// and verboseNotes covers the three unprefixed lines ssh writes about itself. The
// prefix rule alone left the reason quoting `Authenticated to …` in place of the
// remote's own complaint, and a fixture carrying only `debug1:` lines hid that.
// Calibrated both ways: github.com's real `Invalid command: tmux -V; id -u` and ssh's
// own `Could not resolve hostname` are unprefixed, match no note, and survive — that is
// the pole which says the strip keeps the signal rather than the silence.
//
// It applies to the REASON only. Identity is read from the raw transcript by Probe,
// because the fingerprint lives on a `debug1:` line — sanitising once for both would
// have thrown away the thing this flag was added for.
func sshOwnWords(stderr string) string {
	lines := strings.Split(stderr, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if debugLine.MatchString(line) || isVerboseNote(line) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func isVerboseNote(line string) bool {
	for _, note := range verboseNotes {
		if strings.HasPrefix(line, note) {
			return true
		}
	}
	return false
}

// reasonFor names the remedy, not the breakage. Every string here was observed —
// the table in §9 carries the timings that came with them.
func reasonFor(stderr string, rc int, timedOut bool, timeout time.Duration) string {
	// A timeout is slow rather than absent: the host is reachable but didn't answer in
	// time. Offer both remedies: enable it anyway, or raise the timeout.
	if timedOut {
		return "no answer in " + timeout.String() + " — this host is slow rather than absent; enable it anyway, or raise --probe-timeout"
	}

	// Inside rather than at the one call site, so a second caller cannot classify a
	// verbose transcript by accident.
	stderr = sshOwnWords(stderr)
	s := strings.ToLower(stderr)
	switch {
	case strings.Contains(s, "command not found") || strings.Contains(s, "not found: tmux"):
		return "no tmux — install it there, or leave this host off"
	case strings.Contains(s, "invalid command") || strings.Contains(s, "remote:"):
		return "not a shell host — this is a git remote, so leave it off"
	case strings.Contains(s, "could not resolve hostname"):
		return "DNS does not resolve — a stale ssh config entry? fix or remove it"
	case strings.Contains(s, "connection timed out") || strings.Contains(s, "banner exchange"):
		return "cannot be reached — powered off, or behind a VPN that is not up"
	case strings.Contains(s, "permission denied"):
		return "ssh refused the key — add one, or leave this host off"
	case rc == 255:
		return because("ssh failed", stderr)
	}
	return because("no tmux version on stdout", stderr)
}

// because joins a label to ssh's own words, and refuses to leave the label trailing off
// after a dash.
//
// `sshOwnWords` can legitimately strip a transcript to nothing — a connection that
// completed and whose every stderr line was ssh's own bookkeeping — and `firstLine("")`
// is `""`, so the two `label + " — " + quote` sites produced `no tmux version on
// stdout — ` (30 bytes ending in dash-space) and `ssh failed — `. A refusal is a layout
// object in this repository and a dangling ` — ` is the visible tell that a claimant went
// missing, which reads as a bug in the hub rather than as silence from the host.
//
// It is one function and not two fixes because the shape had TWO copies, and a rule that
// can be got wrong in two places has already been got wrong in both.
func because(label, stderr string) string {
	quote := firstLine(stderr)
	if quote == "" {
		return label + ", and ssh's stderr said nothing of its own"
	}
	return label + " — " + quote
}

func firstLine(s string) string {
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		return s[:idx]
	}
	return s
}
