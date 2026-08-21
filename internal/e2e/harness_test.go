//go:build e2e

package e2e

// The harness proves itself, before any hub feature reads it.
//
// A container fixture for a FLEET has one failure mode that is both the likeliest and the least
// visible: every machine sharing one identity, because a host key was baked into the image. The
// derived graph would then collapse to a single node and the fixture would silently encode the
// belief that a fleet is a fleet (spec §2.3). So the first assertion here is distinctness, and
// the second is that the per-edge delays are actually in force — without them the mount policy
// has nothing deterministic to be tested against (spec §6).
//
// This is the only file in the repository that starts containers. It is slow (two base images,
// an 80 MB nushell release) and it skips with a reason when docker cannot be used, which is the
// precondition rather than an opt-in: a leg that runs only when someone remembers an environment
// variable is a leg that has not run.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The declared topology as the generator published it, read from `.out/fleet.json`.
//
// `harness/gen` is `package main` and cannot be imported, so this is a wire format rather than a
// shared type. Reading it beats restating the topology here: a restated fixture stops matching
// the moment a machine is added, and it would be the second place one rule lives.
type harnessFleet struct {
	Name     string           `json:"name"`
	Machines []harnessMachine `json:"machines"`
	Edges    []harnessEdge    `json:"edges"`
	Networks []string         `json:"networks"`
}

type harnessMachine struct {
	ID       string   `json:"id"`
	Aliases  []string `json:"aliases"`
	Hostname string   `json:"hostname"`
	Shell    string   `json:"shell"`
	Tmux     string   `json:"tmux"`
	Networks []string `json:"networks"`
	Hub      bool     `json:"hub"`
	Declares []string `json:"declares"`
}

type harnessEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Delay string `json:"delay"`
	Key   string `json:"key"`
}

type harness struct {
	compose string
	decl    harnessFleet
}

// generateHarness runs the generator and reads back what it published. It does NOT start
// anything, so the cheap structural checks can use it too.
func generateHarness(t *testing.T, topology string) *harness {
	t.Helper()
	root := repoRoot(t)
	cmd := exec.Command("go", "run", "./harness/gen",
		"-topology", filepath.Join("harness", "topology", topology+".toml"))
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("harness/gen: %v: %s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(root, "harness", ".out", "fleet.json"))
	if err != nil {
		t.Fatalf("the generator published no fleet description: %v", err)
	}
	h := &harness{compose: filepath.Join(root, "harness", ".out", "compose.yaml")}
	if err := json.Unmarshal(raw, &h.decl); err != nil {
		t.Fatalf("fleet.json: %v", err)
	}
	if len(h.decl.Machines) == 0 {
		t.Fatalf("the topology %q declares no machines", topology)
	}
	return h
}

// requireDocker skips with the reason, naming the command that failed. A skip decided by one
// look is indistinguishable from a machine with nothing to test, so the reason has to be the
// precondition and not a preference.
func requireDocker(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").
		CombinedOutput(); err != nil {
		t.Skipf("docker is not usable here, so the container harness cannot run: %v: %s",
			err, strings.TrimSpace(string(out)))
	}
}

// up brings the declared fleet up and tears it down afterwards.
func up(t *testing.T, topology string) *harness {
	t.Helper()
	requireDocker(t)
	h := generateHarness(t, topology)

	// Two base images and an 80 MB nushell release: minutes on a cold cache, seconds on a warm
	// one. The elapsed time is logged rather than assumed, because "the harness is slow" is a
	// claim the merged run has to be able to check.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	started := time.Now()
	out, err := exec.CommandContext(ctx, "docker", "compose", "-f", h.compose,
		"up", "-d", "--build", "--wait").CombinedOutput()
	t.Logf("the fleet came up in %s", time.Since(started).Round(time.Second))
	t.Cleanup(func() {
		down, derr := exec.Command("docker", "compose", "-f", h.compose,
			"down", "-v", "--remove-orphans").CombinedOutput()
		if derr != nil {
			t.Errorf("the fleet did not come down, so the next run starts dirty: %v: %s", derr, down)
		}
	})
	if err != nil {
		t.Fatalf("docker compose up: %v: %s", err, out)
	}
	return h
}

func (h *harness) Machines() []string {
	out := make([]string, 0, len(h.decl.Machines))
	for _, m := range h.decl.Machines {
		out = append(out, m.ID)
	}
	return out
}

func (h *harness) machine(id string) harnessMachine {
	for _, m := range h.decl.Machines {
		if m.ID == id {
			return m
		}
	}
	return harnessMachine{}
}

func (h *harness) resolve(alias string) string {
	for _, m := range h.decl.Machines {
		for _, a := range m.Aliases {
			if a == alias {
				return m.ID
			}
		}
	}
	return ""
}

// observerOf answers which machine can open a connection to this one, and under which alias.
//
// It is derived from the declared topology rather than written down: a machine is observable
// from any machine that declares an alias for it AND shares a network with it, which is exactly
// the pair of conditions the topology exists to express. The ROOT has no observer — nothing
// declares it — and that is a fact about the fixture, so the caller is told rather than given a
// silent fallback.
func (h *harness) observerOf(id string) (observer, alias string, ok bool) {
	target := h.machine(id)
	for _, d := range h.decl.Machines {
		for _, a := range d.Declares {
			if h.resolve(a) != id {
				continue
			}
			for _, dn := range d.Networks {
				for _, tn := range target.Networks {
					if dn == tn {
						return d.ID, a, true
					}
				}
			}
		}
	}
	return "", "", false
}

// Exec runs a command IN a container, as the unprivileged account every ssh session lands as.
func (h *harness) Exec(t *testing.T, machine string, argv ...string) (string, int) {
	t.Helper()
	return h.exec(t, machine, "dev", argv...)
}

// ExecRoot is for reading what only root can read — `tc qdisc show`, `/etc/ssh`.
func (h *harness) ExecRoot(t *testing.T, machine string, argv ...string) (string, int) {
	t.Helper()
	return h.exec(t, machine, "root", argv...)
}

func (h *harness) exec(t *testing.T, machine, user string, argv ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	full := append([]string{"compose", "-f", h.compose, "exec", "-T", "-u", user, machine}, argv...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	out, err := cmd.CombinedOutput()
	rc := 0
	if err != nil {
		rc = cmd.ProcessState.ExitCode()
		if rc < 0 {
			t.Fatalf("docker compose exec %s %v did not run at all: %v: %s", machine, argv, err, out)
		}
	}
	return string(out), rc
}

// Run sends a command to a machine's LOGIN SHELL over ssh, from an observer that can reach it.
//
// The login shell is the whole point and it is why this is not `Exec`: ssh hands a remote
// command to the far account's login shell, which is where a non-POSIX shell turns a legal POSIX
// command into a parse failure — the mechanism that made a real host invisible on 2026-08-20.
// `docker compose exec` runs the program directly and would never see it.
func (h *harness) Run(t *testing.T, machine, command string) (string, int) {
	t.Helper()
	observer, alias, ok := h.observerOf(machine)
	if !ok {
		t.Fatalf("no machine in %q declares %s on a network it shares, so nothing can ssh to it",
			h.decl.Name, machine)
	}
	return h.Exec(t, observer, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", alias, command)
}

// Fingerprint is the machine's identity, read from the machine itself.
//
// Every machine has one, including the root, which no observer can reach. `Observed` below is
// what an OBSERVER's handshake reported, and the two are asserted equal for every machine that
// has an observer — that equality is what makes this cheap read trustworthy as the identity.
func (h *harness) Fingerprint(t *testing.T, machine string) string {
	t.Helper()
	out, rc := h.ExecRoot(t, machine, "ssh-keygen", "-lf", "/etc/ssh/ssh_host_ed25519_key.pub")
	if rc != 0 {
		t.Fatalf("%s has no ed25519 host key (rc=%d): %s — the start-up did not run ssh-keygen -A",
			machine, rc, out)
	}
	fp := sha256Field(out)
	if fp == "" {
		t.Fatalf("%s: no SHA256 fingerprint in %q", machine, out)
	}
	return fp
}

// Observed is the fingerprint an observer's own handshake reported: the identity as the product
// would learn it, from the connection it already makes.
func (h *harness) Observed(t *testing.T, machine string) string {
	t.Helper()
	observer, alias, ok := h.observerOf(machine)
	if !ok {
		t.Fatalf("%s has no observer, so nothing can report its host key", machine)
	}
	out, rc := h.Exec(t, observer, "ssh", "-v", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		alias, "true")
	if rc != 0 {
		t.Fatalf("%s could not reach %s as %q (rc=%d): %s", observer, machine, alias, rc, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "Server host key:") {
			continue
		}
		if fp := sha256Field(line); fp != "" {
			return fp
		}
	}
	t.Fatalf("%s reported no host key for %s: %s", observer, machine, out)
	return ""
}

// sha256Field picks the SHA256 token out of a line.
//
// This is a FIELD PICK, not a parser: `internal/hostset.ParseHostKeys` is the production parser
// for ssh's verbose output and it belongs to its own task. The assertion that makes this pick
// trustworthy is `TestHarnessObserverSeesTheMachinesOwnHostKey` below, which requires the token
// taken out of ssh's handshake line to equal the one `ssh-keygen -lf` prints for the same
// machine — two different programs, one answer.
func sha256Field(s string) string {
	for _, field := range strings.Fields(s) {
		if strings.HasPrefix(field, "SHA256:") {
			return field
		}
	}
	return ""
}

// `harness/README.md` tells a reader to run this file with ONE `go test -run` pattern, and the
// seven cases here had no common prefix — so `-run TestTheHarness` matched exactly one of them
// and a reader who followed the README paid the whole image build for host-key distinctness and
// none of the delay, reachability, observer-agreement or hostname checks. Renaming is the fix;
// this is what stops it drifting back, since the next case added here is the one that would be
// silently outside the pattern.
//
// It reads its own source rather than the test registry, because Go offers no way to enumerate
// the cases in one FILE — and a pattern is a claim about a file, which is what the README names.
func TestHarnessEveryCaseHereAnswersToOnePattern(t *testing.T) {
	const prefix = "func TestHarness"
	raw, err := os.ReadFile("harness_test.go")
	if err != nil {
		t.Fatalf("read this suite's own source: %v", err)
	}
	var cases int
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "func Test") {
			continue
		}
		cases++
		if !strings.HasPrefix(line, prefix) {
			name, _, _ := strings.Cut(strings.TrimPrefix(line, "func "), "(")
			t.Errorf("%s is outside the pattern harness/README.md documents (`-run TestHarness`), so following the README would not run it",
				name)
		}
	}
	// A floor, and it is deliberately well below the eight cases here rather than equal to them: it
	// exists to tell DID NOT LOOK from NOTHING TO CHECK — a renamed file, or a cwd that is not the
	// package directory, reads as zero — and a floor pinned to today's count would redden on
	// removing a case, with a message naming the wrong cause.
	if cases < 5 {
		t.Fatalf("found %d cases in this file, so it was not the source that was read", cases)
	}
}

// This one needs no containers and no images: `docker compose config` parses and interpolates
// without building anything, which makes it the cheapest possible guard on the generated file.
//
// It checks stderr as well as the exit code, and that is the point. Measured on Compose 5.1.4: a
// start-up script containing `tc qdisc replace dev "$iface" …` came through as `dev ""` — the
// per-edge delay silently not applied, the mount policy silently untestable — and `config`
// answered rc=0 with only a warning. A gate reading the exit code alone would have passed it.
func TestHarnessComposeParsesWithNothingInterpolatedAway(t *testing.T) {
	requireDocker(t)
	h := generateHarness(t, "basic")

	cmd := exec.Command("docker", "compose", "-f", h.compose, "config")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker compose config: %v: %s", err, stderr.String())
	}
	if noise := strings.TrimSpace(stderr.String()); noise != "" {
		t.Errorf("docker compose config warned while answering rc=0, which is how a delay goes missing:\n%s",
			noise)
	}
	// The delay must still be there AFTER interpolation, and so must the variable it needs.
	for _, want := range []string{"netem delay 180ms", "netem delay 5ms", "iface"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the parsed compose lost %q", want)
		}
	}
}

// netemDelay parses another program's output, so it gets a table rather than a belief. Both rows
// are real iproute2 renderings of ONE qdisc: newer versions print an integral figure, older ones
// use `%.1f`, and `ubuntu:22.04` — four of this fleet's five machines — ships an older one. This
// case needs no containers, so it runs wherever the e2e tag is set.
func TestHarnessNetemDelayReadsEitherFormatIPRoute2Prints(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		want   time.Duration
		found  bool
	}{
		{
			name:   "an integral figure, as newer iproute2 prints it",
			output: "qdisc noqueue 0: dev lo root refcnt 2\nqdisc netem 8001: dev eth0 root refcnt 2 limit 1000 delay 180ms\n",
			want:   180 * time.Millisecond, found: true,
		},
		{
			name:   "one decimal place, as iproute2 5.15 prints it",
			output: "qdisc noqueue 0: dev lo root refcnt 2\nqdisc netem 8001: dev eth0 root refcnt 2 limit 1000 delay 180.0ms\n",
			want:   180 * time.Millisecond, found: true,
		},
		{
			name:   "no netem at all, which is the failure the caller reports",
			output: "qdisc noqueue 0: dev lo root refcnt 2\nqdisc noqueue 0: dev eth0 root refcnt 2\n",
			want:   0, found: false,
		},
		{
			// The word `delay` appearing outside a netem line must not answer for one: the caller
			// asks whether THIS edge's qdisc is in force.
			name:   "a delay on a qdisc that is not netem",
			output: "qdisc tbf 8002: dev eth0 root refcnt 2 delay 90ms\n",
			want:   0, found: false,
		},
	} {
		got, found := netemDelay(tc.output)
		if found != tc.found || got != tc.want {
			t.Errorf("%s: netemDelay = %v,%v want %v,%v", tc.name, got, found, tc.want, tc.found)
		}
	}
}

func TestHarnessFleetIsAFleet(t *testing.T) {
	f := up(t, "basic")
	fps := map[string]string{}
	for _, m := range f.Machines() {
		fp := f.Fingerprint(t, m)
		if prev, dup := fps[fp]; dup {
			t.Fatalf("%s and %s share host key %s — the fixture is one machine wearing five hats",
				m, prev, fp)
		}
		fps[fp] = m
	}
	if len(fps) != len(f.Machines()) {
		t.Fatalf("%d distinct identities for %d machines", len(fps), len(f.Machines()))
	}
	t.Logf("%d machines, %d distinct host keys", len(f.Machines()), len(fps))

	// The nushell machine must fail a POSIX-quoted program name. The plan asked for that failure
	// AT rc=0, on the grounds that the exit code is why the class was invisible — and measured on
	// the version this harness installs (nushell 0.115.0, 2026-08-20) it is rc=1:
	// `nu -c "'tmux' '-V'"` answers 1 with `Error: nu::parser::parse_mismatch` on stderr. The
	// rc=0 figure belongs to the nushell on the macOS host where the class was found, so pinning
	// it here would redden a correct fixture.
	//
	// What is asserted instead is the part that does not depend on someone else's exit code and
	// is the actual defect: the command DID NOT RUN. That is invisibility, whatever the rc.
	out, rc := f.Run(t, "hop", `'tmux' '-V'`)
	t.Logf("a quoted program name on the nushell machine answered rc=%d (0.115.0 measured rc=1)", rc)
	if strings.Contains(out, "tmux 3.2a") {
		t.Errorf("the nushell machine RAN a POSIX-quoted program name: %q — the invisible-host class cannot be reproduced on this fleet", out)
	}
	if !strings.Contains(out, "parse_mismatch") {
		t.Errorf("the nushell machine answered %q; want its own parse error, or the refusal is some other failure", out)
	}

	// And the other half, which is the one with product value: the payload the hub actually sends
	// leaves the program name BARE, and that shape works on the same shell.
	out, rc = f.Run(t, "hop", "tmux -V; id -u")
	if rc != 0 {
		t.Errorf("the hub's own probe payload failed on the nushell machine (rc=%d): %s", rc, out)
	}
	for _, want := range []string{"tmux 3.2a", "1000"} {
		if !strings.Contains(out, want) {
			t.Errorf("the probe payload answered %q, which does not contain %q", out, want)
		}
	}
}

// A field pick out of ssh's verbose output is only as good as its agreement with the machine's
// own key. Two programs, one answer — and it is also the assertion that catches a fleet where
// ssh negotiated a different host key type from the one read back.
func TestHarnessObserverSeesTheMachinesOwnHostKey(t *testing.T) {
	f := up(t, "basic")
	var checked int
	for _, m := range f.Machines() {
		if _, _, ok := f.observerOf(m); !ok {
			continue
		}
		checked++
		if observed, own := f.Observed(t, m), f.Fingerprint(t, m); observed != own {
			t.Errorf("%s: the handshake reported %s and the machine holds %s", m, observed, own)
		}
	}
	// A floor, because a loop that skipped every machine passes having checked nothing — and the
	// root legitimately has no observer, so "some are skipped" is the normal case.
	if checked < 4 {
		t.Fatalf("checked %d machines against their own keys; the topology offers 4", checked)
	}
}

// Oracle point 2 (spec §5), at the level the fixture can answer: two aliases for one machine are
// one identity, and two machines under one hostname are two.
func TestHarnessTwoAliasesAreOneIdentityAndOneHostnameCanBeTwo(t *testing.T) {
	f := up(t, "basic")

	hop, again := f.Observed(t, "hop"), ""
	out, rc := f.Exec(t, "root", "ssh", "-v", "-o", "BatchMode=yes", "hop-again", "true")
	if rc != 0 {
		t.Fatalf("the root could not reach hop-again (rc=%d): %s", rc, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Server host key:") {
			again = sha256Field(line)
		}
	}
	if again == "" {
		t.Fatalf("hop-again reported no host key: %s", out)
	}
	if hop != again {
		t.Errorf("hop is %s and hop-again is %s — two aliases for one machine must be one identity, or nothing downstream has a merge to make",
			hop, again)
	}

	a, b := f.Fingerprint(t, "twin-a"), f.Fingerprint(t, "twin-b")
	if a == b {
		t.Errorf("both twins are %s — two different machines under one hostname must stay two", a)
	}
	// The collided label, asserted as EQUALITY against one declared hostname rather than as a
	// substring. `Contains(out, "twin")` could not fail in the direction it names: `twin-a` and
	// `twin-b` — which is exactly what a container reports when the collision is GONE — both
	// contain `twin`, so the predicate was satisfied by the defect. Measured 2026-08-20: making
	// the generator write `hostname: <id>` removes the whole row from the fleet and that check,
	// plus every case in `harness/gen`, stayed green.
	label := f.machine("twin-a").Hostname
	if label == "" || label != f.machine("twin-b").Hostname {
		t.Fatalf("the twins declare hostnames %q and %q; the topology no longer puts two machines under one label",
			label, f.machine("twin-b").Hostname)
	}
	for _, m := range []string{"twin-a", "twin-b"} {
		if label == m {
			t.Fatalf("the collided hostname is %q, which is %s's own id — each twin would merely be naming itself",
				label, m)
		}
	}
	for _, m := range []string{"twin-a", "twin-b"} {
		out, rc := f.Exec(t, m, "hostname")
		if rc != 0 {
			t.Fatalf("%s could not report its own hostname (rc=%d): %s", m, rc, out)
		}
		if got := strings.TrimSpace(out); got != label {
			t.Errorf("%s calls itself %q and the topology declares %q; the label collision this row exists for is not in the running fleet",
				m, got, label)
		}
	}
}

// The whole point of the topology: a machine the root cannot reach, which some hop can. Without
// this the `Blocked` diagnosis has nothing to diagnose and `leaf` would simply be `Ready`.
func TestHarnessRootCannotReachTheLeafAndTheHopCan(t *testing.T) {
	f := up(t, "basic")

	out, rc := f.Exec(t, "root", "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "leaf", "true")
	if rc == 0 {
		t.Errorf("the root reached the leaf directly: %s — then nothing here is Blocked", out)
	}
	out, rc = f.Exec(t, "hop", "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "leaf", "true")
	if rc != 0 {
		t.Errorf("the hop could not reach the leaf either (rc=%d): %s — the leaf is then unreachable rather than Blocked, which is a different fixture from the one declared",
			rc, out)
	}
}

// Spec §6: "assert the delays are in force". Both halves, because either alone can lie — a qdisc
// can be installed on the wrong interface, and a slow link can be slow for another reason.
func TestHarnessDeclaredDelaysAreInForce(t *testing.T) {
	f := up(t, "basic")

	var checked int
	for _, e := range f.decl.Edges {
		if e.Delay == "" {
			continue
		}
		out, rc := f.ExecRoot(t, e.To, "tc", "qdisc", "show")
		if rc != 0 {
			t.Fatalf("tc qdisc show on %s: rc=%d: %s", e.To, rc, out)
		}
		want, err := time.ParseDuration(e.Delay)
		if err != nil {
			t.Fatalf("the topology declares delay %q on %s -> %s: %v", e.Delay, e.From, e.To, err)
		}
		checked++
		got, found := netemDelay(out)
		if !found {
			t.Errorf("%s carries no netem qdisc for the %s -> %s edge:\n%s", e.To, e.From, e.To, out)
			continue
		}
		if got != want {
			t.Errorf("%s carries netem delay %v for the %s -> %s edge, which declares %v",
				e.To, got, e.From, e.To, want)
		}
	}

	// A floor, because the loop above `continue`s on an undelayed edge: a `fleet.json` that
	// published no edges at all would check nothing and this case would still report PASS on the
	// strength of the timing differential below. Every other loop in this file carries one.
	if checked < 2 {
		t.Fatalf("checked %d declared delays against their qdiscs; the topology declares 2", checked)
	}

	// The differential, so docker's own exec overhead cancels: an ssh handshake is several round
	// trips, so a 180 ms link must cost far more than a 5 ms one. Absolute figures would be a claim
	// about this machine's load; the difference is a claim about the fixture — but only if neither
	// sample carries the load, which is why each link is measured `delaySamples` times and the
	// MINIMUM is taken. Contention can only inflate a handshake, never shorten one, so the minimum
	// is the robust estimate of the link's floor while any single sample is a lottery. Measured, and
	// this is the failure that bought the change: in a full e2e run on a machine holding 150 other
	// containers the 5 ms link's one sample took 1m3.6s — the Linux SYN-retry sum — against 2.9s for
	// the 180 ms link, so the guard reported that the qdisc was not in the path. It was; the control
	// measurement was contended. Alone on the same tree the same links measured 727ms and 2.79s.
	fast := minSSH(t, f, "root", "hop")
	slow := minSSH(t, f, "hop", "leaf")
	t.Logf("root -> hop (5ms declared) took %s; hop -> leaf (180ms declared) took %s (best of %d)",
		fast, slow, delaySamples)
	if ok, why := delayVerdict(fast, slow); !ok {
		t.Error(why)
	}
}

// delaySamples is how many handshakes each link is timed over. Three, because the estimator is the
// minimum and the thing it defends against is a single stalled connect: two samples make one stall
// fatal half the time, and more than three costs the case seconds for a floor it already has.
const delaySamples = 3

// contendedHandshake is the point past which the FAST link's own timing stops being evidence about
// the fixture. It is deliberately far above anything a qdisc on that link can explain — the link
// declares 5 ms, an ssh handshake is a handful of round trips, and the measured figure on an idle
// machine is 727 ms — so a second beyond this is the machine and not the topology.
const contendedHandshake = 5 * time.Second

// delayVerdict judges the two timings, and it exists as a FUNCTION so both of its refusals can be
// asserted without containers (TestHarnessTheDelayVerdictNamesTheRightCause).
//
// The two refusals must not be confused, because they send the reader to different places: "the
// qdisc is not in the path" is a statement about the fixture, and a guard that prints it when the
// control sample was contended is a guard that blames the fixture for the machine. That happened —
// see the comment at the call site — and this repository's own journal records the class: a refusal
// whose message names a mechanism that is not at fault costs the next reader the whole diagnosis.
//
// The qdisc's VALUE is checked structurally above, against `tc qdisc show`. This half checks only
// that it is in the PATH, which is why a margin rather than a ratio: the claim is "measurably
// slower", and 500 ms is far larger than the 175 ms the declarations differ by per round trip.
func delayVerdict(fast, slow time.Duration) (bool, string) {
	if fast > contendedHandshake {
		return false, fmt.Sprintf(
			"the 5 ms link's own handshake took %s, past the %s no qdisc on it can explain — this "+
				"machine was contended, so the differential measures the load rather than the fixture; "+
				"the qdiscs themselves were verified against tc qdisc show above", fast, contendedHandshake)
	}
	if slow < fast+500*time.Millisecond {
		return false, fmt.Sprintf(
			"the 180 ms link (%s) is not measurably slower than the 5 ms one (%s) — the qdisc is "+
				"installed and not in the path", slow, fast)
	}
	return true, ""
}

// netemDelay reads the figure out of `tc qdisc show` as a DURATION rather than as a string.
//
// iproute2 does not print one format. Older versions render the delay with `%.1fms`, so a correct
// 180 ms qdisc reads `delay 180.0ms` — and `ubuntu:22.04`, which four of the five machines are
// built from, ships one of those. An assertion spelled `Contains(out, "delay 180ms")` would
// therefore have reddened a fixture that was doing exactly what it was told. Both spellings parse
// to the same `time.Duration`, verified with Go's own parser, so the comparison is of figures and
// not of anybody's formatting — and it checks the VALUE, which a substring search over the whole
// output does not.
func netemDelay(tcOutput string) (time.Duration, bool) {
	for _, line := range strings.Split(tcOutput, "\n") {
		if !strings.Contains(line, "netem") {
			continue
		}
		fields := strings.Fields(line)
		for i, f := range fields {
			if f != "delay" || i+1 >= len(fields) {
				continue
			}
			if d, err := time.ParseDuration(fields[i+1]); err == nil {
				return d, true
			}
		}
	}
	return 0, false
}

// minSSH times one link `delaySamples` times and returns the fastest. See `delaySamples` for why
// the minimum rather than the mean: the noise here is one-sided.
func minSSH(t *testing.T, f *harness, from, to string) time.Duration {
	t.Helper()
	best := time.Duration(0)
	for i := 0; i < delaySamples; i++ {
		if got := timeSSH(t, f, from, to); best == 0 || got < best {
			best = got
		}
	}
	return best
}

func timeSSH(t *testing.T, f *harness, from, to string) time.Duration {
	t.Helper()
	started := time.Now()
	out, rc := f.Exec(t, from, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=20", to, "true")
	elapsed := time.Since(started)
	if rc != 0 {
		t.Fatalf("%s could not reach %s (rc=%d): %s", from, to, rc, out)
	}
	return elapsed
}

// TestHarnessTheDelayVerdictNamesTheRightCause pins the one thing the containers cannot cheaply assert: that
// the two refusals stay distinct. It runs with no docker at all, which is the point — a guard whose
// wording is only exercised when the fixture is broken is a guard whose wording nobody has read.
//
// The contended cell carries the REAL numbers from the run that found this: 1m3.62s against 2.93s in a
// full e2e pass on a machine holding 150 other containers. Under the old single-sample form that pair
// printed "the qdisc is installed and not in the path", sending the reader to the topology generator
// for a defect that was in the load. The idle cell carries the real numbers too — 727ms and 2.79s,
// measured on the same tree minutes later — so the passing pole is a measurement rather than a guess.
func TestHarnessTheDelayVerdictNamesTheRightCause(t *testing.T) {
	cases := []struct {
		name       string
		fast, slow time.Duration
		ok         bool
		says       string
	}{
		{"the fixture as measured on an idle machine",
			727 * time.Millisecond, 2790 * time.Millisecond, true, ""},
		{"the contended run that bought this function",
			63620 * time.Millisecond, 2930 * time.Millisecond, false, "contended"},
		{"a qdisc installed but not in the path",
			700 * time.Millisecond, 900 * time.Millisecond, false, "not in the path"},
		{"both links equally slow is still the qdisc, not the load, below the contention bound",
			4 * time.Second, 4 * time.Second, false, "not in the path"},
	}
	for _, c := range cases {
		ok, why := delayVerdict(c.fast, c.slow)
		if ok != c.ok {
			t.Errorf("%s: delayVerdict(%s, %s) ok=%t, want %t (%s)", c.name, c.fast, c.slow, ok, c.ok, why)
			continue
		}
		if c.says == "" {
			if why != "" {
				t.Errorf("%s: a passing verdict still said %q", c.name, why)
			}
			continue
		}
		if !strings.Contains(why, c.says) {
			t.Errorf("%s: the refusal must name %q so the reader looks in the right place, and it said:\n%s",
				c.name, c.says, why)
		}
		// A refusal that names the other cause as well is a refusal that names neither.
		other := "not in the path"
		if c.says == "not in the path" {
			other = "contended"
		}
		if strings.Contains(why, other) {
			t.Errorf("%s: the refusal names BOTH causes, so it identifies neither:\n%s", c.name, why)
		}
	}
}
