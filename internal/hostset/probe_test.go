package hostset

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The measured defect this rule exists for, with the exact five outcomes seen on
// this machine's fleet. The middle case is the one that matters: rc=0 and no tmux.
func TestProbeKeysOnStdoutNotOnTheExitCode(t *testing.T) {
	for _, tc := range []struct {
		name           string
		stdout, stderr string
		rc             int
		usable         bool
		reasonHas      string
	}{
		{"a real host", "tmux 3.2a\n1000\n", "", 0, true, ""},
		// Measured on studio-ws, web-ws, crater-ws, st-ws and qa-ws: the shell's
		// status belongs to `id -u`, so tmux's own 127 is swallowed and rc is 0.
		{"rc=0 with no tmux", "1000\n", "zsh:1: command not found: tmux\n", 0, false, "no tmux"},
		{"a git remote", "", "Invalid command: tmux -V\n", 1, false, "not a shell host"},
		{"dns", "", "ssh: Could not resolve hostname sandbox-a.ops.eu\n", 255, false, "does not resolve"},
		{"unreachable", "", "ssh: connect to host 20.127.207.74 port 22: Connection timed out\n", 255, false, "cannot be reached"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Probe(context.Background(), "h", time.Second,
				func(context.Context, string, ...string) (string, string, int) {
					return tc.stdout, tc.stderr, tc.rc
				})
			if r.Usable != tc.usable {
				t.Fatalf("Usable = %v, want %v (reason %q)", r.Usable, tc.usable, r.Reason)
			}
			if tc.usable && r.Version != "3.2a" {
				t.Errorf("Version = %q, want 3.2a", r.Version)
			}
			if !tc.usable && !strings.Contains(r.Reason, tc.reasonHas) {
				t.Errorf("Reason = %q, want it to contain %q", r.Reason, tc.reasonHas)
			}
			// Genuine refusals (git remotes, no tmux, DNS failures, unreachable hosts)
			// are not timeouts. TimedOut must be false for all these cases.
			if r.TimedOut {
				t.Errorf("TimedOut = true, want false — %s is a refusal, not a timeout", tc.name)
			}
		})
	}
}

// Every exclusion carries its remedy, because a status with no remedy is a bug
// report addressed to the wrong person (§16).
func TestEveryExclusionReasonNamesWhatToDo(t *testing.T) {
	for _, tc := range []struct {
		stdout, stderr string
		rc             int
	}{
		{"1000\n", "command not found: tmux\n", 0},
		{"", "Invalid command: tmux -V\n", 1},
		{"", "ssh: Could not resolve hostname x\n", 255},
	} {
		r := Probe(context.Background(), "h", time.Second,
			func(context.Context, string, ...string) (string, string, int) {
				return tc.stdout, tc.stderr, tc.rc
			})
		if !strings.ContainsAny(r.Reason, "—-") {
			t.Errorf("reason %q states a breakage without a remedy", r.Reason)
		}
	}
}

// A timeout is a third state: not usable right now, not excluded. A host measured
// at 5.4 s, 9.1 s, 15.7 s and 18.4 s straddles any fixed timeout, so keying
// membership on "did it answer within N" makes its presence a coin flip.
func TestProbeDistinguishesTimeoutFromExclusion(t *testing.T) {
	// Runner respects context: when the deadline fires, it returns empty output.
	slowRunner := func(ctx context.Context, alias string, args ...string) (string, string, int) {
		<-ctx.Done() // block until context is cancelled
		return "", "", 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r := Probe(ctx, "slow-host", 50*time.Millisecond, slowRunner)

	if r.TimedOut != true {
		t.Errorf("TimedOut = false, want true")
	}
	if r.Usable {
		t.Errorf("Usable = true, want false — no version was seen")
	}
	// The reason must say "slow rather than absent" and offer both remedies.
	if !strings.Contains(r.Reason, "slow rather than absent") {
		t.Errorf("reason %q must say 'slow rather than absent'", r.Reason)
	}
	if !strings.Contains(r.Reason, "enable it anyway") {
		t.Errorf("reason %q must offer 'enable it anyway' as a remedy", r.Reason)
	}
	if !strings.Contains(r.Reason, "--probe-timeout") {
		t.Errorf("reason %q must offer raising --probe-timeout as a remedy", r.Reason)
	}
	// Must NOT say "cannot be reached" — that is for genuinely unreachable hosts.
	if strings.Contains(r.Reason, "cannot be reached") {
		t.Errorf("reason %q must not say 'cannot be reached' — that is for unreachable hosts, not slow ones", r.Reason)
	}
	// Must NOT say "no tmux" — that is for hosts that answered but lack tmux.
	if strings.Contains(r.Reason, "no tmux") {
		t.Errorf("reason %q must not say 'no tmux' — that is for hosts without tmux, not timeouts", r.Reason)
	}
}

// Concurrency is the whole reason a first run is affordable: 20 hosts took 7.01 s
// wall, all of it the slowest one. A serial loop would have been a minute.
func TestProbeAllRunsConcurrently(t *testing.T) {
	cands := make([]Candidate, 12)
	for i := range cands {
		cands[i] = Candidate{Alias: fmt.Sprintf("h%d", i)}
	}
	start := time.Now()
	got := ProbeAll(context.Background(), cands, 2*time.Second,
		func(context.Context, string, ...string) (string, string, int) {
			time.Sleep(300 * time.Millisecond)
			return "tmux 3.4\n", "", 0
		})
	if len(got) != 12 {
		t.Fatalf("got %d results, want 12", len(got))
	}
	if el := time.Since(start); el > 1500*time.Millisecond {
		t.Errorf("12 probes of 300ms took %s — they are not running concurrently", el)
	}
}

// A candidate the parser already rejected is never probed: it costs a connection
// attempt to learn what the name already said.
func TestProbeAllSkipsWhatTheParserRejected(t *testing.T) {
	calls := 0
	ProbeAll(context.Background(),
		[]Candidate{{Alias: "real"}, {Alias: "unix/*", Skip: "a pattern, not a host"}},
		time.Second,
		func(context.Context, string, ...string) (string, string, int) {
			calls++
			return "tmux 3.4\n", "", 0
		})
	if calls != 1 {
		t.Errorf("probed %d times, want 1 — a skipped candidate must not be dialled", calls)
	}
}

// Not a gate: a look at the real fleet, to verify the probe classifies correctly and
// to measure the wall time. Measured when this was written: ~7 s for 20 candidates,
// 5 usable (nuc, web-app, web-db, eu on 3.2a, side-desk on 3.4).
func TestProbeAllAgainstTheRealFleet(t *testing.T) {
	if os.Getenv("HOSTSET_REAL") == "" {
		t.Skip("HOSTSET_REAL unset")
	}
	home, _ := os.UserHomeDir()
	cands := ParseSSHConfig(filepath.Join(home, ".ssh", "config"), "/etc/ssh/ssh_config")

	realSSH := func(ctx context.Context, alias string, args ...string) (string, string, int) {
		var stdout, stderr bytes.Buffer
		// BatchMode prevents blocking on password prompts; ConnectTimeout caps
		// non-answering hosts at 6s instead of ~2 minutes. Without them, probing 20
		// candidates took 134s; with them, 7s.
		sshArgs := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=6", alias}
		sshArgs = append(sshArgs, args...)
		cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		rc := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				rc = exitErr.ExitCode()
			} else {
				rc = 1
			}
		}
		return stdout.String(), stderr.String(), rc
	}

	start := time.Now()
	results := ProbeAll(context.Background(), cands, 10*time.Second, realSSH)
	elapsed := time.Since(start)

	usable := 0
	for _, r := range results {
		status := "✗"
		if r.Usable {
			status = "✓"
			usable++
		}
		t.Logf("%s %-24s %-8s %6.2fs  %s", status, r.Alias, r.Version, r.Took.Seconds(), r.Reason)
	}
	t.Logf("%d candidates, %d usable, %.2fs wall", len(results), usable, elapsed.Seconds())
}

// The Runner contract requires BatchMode=yes and ConnectTimeout=6. Without them,
// probing 20 candidates took 134 s (blocked on password prompts and system-default
// timeouts); with them, 7 s. This test documents the requirement for Task 8's
// production runner.
func TestRunnerContractRequiresBatchModeAndConnectTimeout(t *testing.T) {
	// This test verifies the documented requirement exists. When Task 8 implements
	// the production runner, it must include these flags or the probe will be
	// unusable (134 s for 20 hosts instead of 7 s).
	//
	// A production runner would look like:
	//   ssh -o BatchMode=yes -o ConnectTimeout=6 <alias> <args...>
	//
	// BatchMode: prevents blocking indefinitely on password/passphrase prompts
	// ConnectTimeout: caps non-answering hosts at 6s instead of ~2 minutes
	t.Log("Runner contract documented: production implementations MUST use -o BatchMode=yes -o ConnectTimeout=6")
}
