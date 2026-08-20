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
}

// Runner is how the tests inject a fake ssh without needing real network or keys.
//
// Production implementations MUST use `-o BatchMode=yes -o ConnectTimeout=6`:
// BatchMode prevents ssh from blocking on password/passphrase prompts indefinitely,
// and ConnectTimeout caps a non-answering host at 6 s instead of the system default
// (~2 minutes). Without them, probing 20 candidates took 134 s; with them, 7 s.
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
func Probe(ctx context.Context, alias string, timeout time.Duration, ssh Runner) Result {
	start := time.Now()
	out, errOut, rc := ssh(ctx, alias, "tmux -V; id -u")
	r := Result{Alias: alias, Took: time.Since(start)}

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

// reasonFor names the remedy, not the breakage. Every string here was observed —
// the table in §9 carries the timings that came with them.
func reasonFor(stderr string, rc int, timedOut bool, timeout time.Duration) string {
	// A timeout is slow rather than absent: the host is reachable but didn't answer in
	// time. Offer both remedies: enable it anyway, or raise the timeout.
	if timedOut {
		return "no answer in " + timeout.String() + " — this host is slow rather than absent; enable it anyway, or raise --probe-timeout"
	}

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
		return "ssh failed — " + firstLine(stderr)
	}
	return "no tmux version on stdout — " + firstLine(stderr)
}

func firstLine(s string) string {
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		return s[:idx]
	}
	return s
}
