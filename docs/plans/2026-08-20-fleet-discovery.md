# Transitive fleet discovery — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: use superpowers:subagent-driven-development to implement
> this plan task by task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** the hub finds the machines behind the hosts it already reaches, identifies each one for what it
is, and tells the operator exactly what would make each mountable — without writing anything to a remote
machine and without a new transport.

**Architecture:** three new seams and one new test vehicle. `internal/fleet` holds the graph (nodes keyed
by an ssh host-key fingerprint SET, per-observer edges, five states, budgets that report what they cut).
`internal/hostset` grows two abilities it is already shaped for: harvesting the fingerprint from the probe
it already runs, and enumerating a HOP's `~/.ssh/config` resolved by `ssh -G` on that hop. `internal/ui`
surfaces candidates with a remedy each. A container harness declares a topology in TOML and is the oracle:
*derived graph == declared topology minus policy, with a reason per exclusion.*

**Tech stack:** Go 1.26.1, existing packages only; Docker + Compose + `tc netem` for the harness; TOML via
`internal/conf`.

**Spec:** `docs/specs/2026-08-20-fleet-graph-and-harness-topology-design.md` — the authority for this plan.
Where they disagree, the spec wins.

## Global Constraints

- **Never run `tmux` without an explicit `-L` or `-S`.** Harness containers included.
- **Never emit `#{client_activity}` or `#{client_created}`** — both segfault tmux 3.2a, which the harness
  runs on purpose. `internal/tmux`'s `Validate` is the guard; do not route around it.
- **Production UI strings are English** (`internal/ui/english_test.go` is the guard).
- **Nothing is ever written to a remote machine** — not its ssh config, not its hub config, not its
  favourites. Reads only.
- **Only the root's own completed handshake creates a node**; everything a hop merely declares is a
  candidate (spec §3.2 invariant 3).
- **Every candidate and every unmounted node carries a reason that names a remedy** (spec §3.2 invariant 4).
  `unreachable` is not a reason.
- **Every feature commit carries that feature's E2E cases.**
- **Count `ok` lines against the package count.** 19 today; this plan takes it to **22** —
  `internal/fleet` (task 1), `internal/fleetcache` (task 6) and `harness/gen` (task 3). A package that
  failed to build is not reported as a failure — it is not reported at all, which is why the count is the
  check and not the absence of a red line.
- **RULING (no dead package under `internal/`), corrected 2026-08-20 after reading the guard rather than
  assuming it.** `cmd/tmux-hub`'s `TestEveryPackageIsReachableFromMain` iterates **`internal/*` only** and
  asks `go list -deps ./cmd/tmux-hub` which of those the BINARY links. Two consequences, both measured:
  1. **A new `internal/` package must be linked, or carry an exemption.** The repo already has the slot:
     `var underConstruction = map[string]string{}` in `wiring_test.go`, documented as packages that exist
     before the task that wires them, each entry naming WHO removes it — and the loop REFUSES a stale
     entry once the package is linked, so the exemption cannot outlive its reason. Task 1 therefore lands
     `internal/fleet` with the entry
     `"fleet": "task 1 lands the graph model; task 6 wires it through internal/ui and the picker, and
     deletes this entry"`, and task 6 deletes it. This replaces an earlier ruling of mine that task 1
     could not merge alone: the mechanism the repo already built is better than the workaround I invented.
  2. **A top-level `main` package is out of the guard's scope**, measured: a tree containing
     `harness/gen/main.go` passes it. So the harness generator stays an ordinary `main` package, and an
     earlier ruling of mine that it had to move into `internal/e2e` behind a build tag is RETRACTED — it
     was drawn from an assumption about the guard's scope, and the guard says otherwise.
- **Gates, all of them, before any merge:** `gofmt -l .` empty; `go vet ./...`, `go vet -tags e2e
  ./internal/e2e/`, `go vet -tags mockup ./internal/ui/`; `GOOS=darwin GOARCH=arm64 go vet ./...` and
  `GOOS=darwin GOARCH=amd64`; `go test ./...` and `go test -race ./...`; the eight doc guards; and
  `go test -tags mockup -run TestGenerate ./internal/ui/` followed by `git diff --exit-code -- docs/`.
- **Verify the COMMIT, never the working tree**: `git archive HEAD | tar -x -C <scratch>` and run the gates
  there.
- **One owner per contested file.** Every task below names its files; no two concurrent tasks share one.
- **RULING (harness tmux versions):** the first topology uses **packaged** tmux only — `3.2a` from
  `ubuntu:22.04` and `3.5a` from `debian:trixie`. The requirement in spec §4.1 is version
  *heterogeneity*, and 3.7b is newer than any distro, so a source build would cost minutes per image for
  a property two packaged versions already provide. The live fleet remains the guard for 3.7b-specific
  claims. Adding a source-built image later changes one topology row.

---

## File Structure

| file | responsibility |
|---|---|
| `internal/fleet/fleet.go` | `Node`, `Edge`, `State`, `Graph`; identity merging; cycle-safe walk |
| `internal/fleet/diagnose.go` | `Ready` vs `Blocked` from a resolved stanza, with the remedy string |
| `internal/fleet/budget.go` | depth/breadth budgets and the report of what they cut |
| `internal/hostset/fingerprint.go` | parse ssh's `Server host key:` line; `Fingerprints` on `Result` |
| `internal/hostset/remote.go` | enumerate + resolve a HOP's ssh config over an existing master |
| `internal/ui/discovered.go` | the picker's "discovered" section: order, buckets, reasons |
| `internal/fleetcache/cache.go` | persisted per-node facts (RTT, version, last seen) so order is stable |
| `harness/topology/basic.toml` | the first declared topology (spec §4) |
| `harness/gen/{main,topology,compose}.go` | topology → compose + per-node build args + keys. NOT `internal/e2e/harnessgen.go`, as an earlier draft of this table said: the generator is a `package main` the harness runs, so it cannot sit behind `-tags e2e` in a test package, and the e2e suite reads its published `.out/fleet.json` instead |
| `harness/image/Dockerfile` | ONE base image, parameterised by build args |
| `internal/e2e/discovery_test.go` | the E2E cases, behind `-tags e2e` |

---

## Task 1: the graph model

**Files:**
- Create: `internal/fleet/fleet.go`, `internal/fleet/fleet_test.go`
- Create: `internal/fleet/budget.go`, `internal/fleet/budget_test.go`

**Interfaces:**
- Produces: `fleet.Node`, `fleet.Edge`, `fleet.State`, `fleet.Graph`, `(*Graph).Observe`, `(*Graph).Walk`,
  `fleet.Budget`, `fleet.Cut`.
- Consumes: nothing. No I/O, no network, no clock.

- [ ] **Step 1: write the failing tests**

```go
package fleet

import "testing"

// Identity is a SET and observations MERGE: one machine reached by two aliases is one node.
func TestTwoAliasesForOneMachineAreOneNode(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "root", Label: "hop",
		Fingerprints: []string{"SHA256:aaa"}, Verified: true})
	g.Observe(Observation{Observer: "root", Label: "hop-again",
		Fingerprints: []string{"SHA256:aaa"}, Verified: true})
	if got := len(g.Nodes()); got != 1 {
		t.Fatalf("two aliases for one fingerprint gave %d nodes, want 1", got)
	}
	if got := g.Nodes()[0].Labels; len(got) != 2 {
		t.Errorf("the node carries %d labels, want 2 — a merge must keep both names", len(got))
	}
}

// A machine that presents a second host key is still ONE node: identity is intersection, not equality.
func TestASecondHostKeyJoinsTheSetRatherThanForkingTheNode(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "root", Label: "h", Fingerprints: []string{"SHA256:aaa"}, Verified: true})
	g.Observe(Observation{Observer: "root", Label: "h", Fingerprints: []string{"SHA256:aaa", "SHA256:bbb"}, Verified: true})
	if got := len(g.Nodes()); got != 1 {
		t.Fatalf("an added fingerprint forked the node: %d nodes, want 1", got)
	}
}

// Two DIFFERENT machines answering to one hostname are two nodes — four `web-ws` on the live tailnet.
func TestOneHostnameTwoMachinesAreTwoNodes(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "root", Label: "twin", Hostname: "twin",
		Fingerprints: []string{"SHA256:aaa"}, Verified: true})
	g.Observe(Observation{Observer: "root", Label: "twin", Hostname: "twin",
		Fingerprints: []string{"SHA256:bbb"}, Verified: true})
	if got := len(g.Nodes()); got != 2 {
		t.Fatalf("one hostname over two fingerprints gave %d nodes, want 2", got)
	}
}

// Unverified is a CANDIDATE, never a node (spec §3.2 invariant 3).
func TestAnUnverifiedObservationIsACandidateNotANode(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "hop", Label: "leaf", Verified: false,
		Reason: "the hop's recipe names ~/.ssh/hop-only, which is not here"})
	if got := len(g.Nodes()); got != 0 {
		t.Fatalf("an unverified observation created %d nodes, want 0", got)
	}
	c := g.Candidates()
	if len(c) != 1 || c[0].Reason == "" {
		t.Fatalf("want one candidate carrying a reason, got %+v", c)
	}
}

// The walk visits identities, so a cycle terminates with no special case.
func TestTheWalkTerminatesOnACycle(t *testing.T) {
	g := New()
	g.Observe(Observation{Observer: "root", Label: "hop", Fingerprints: []string{"SHA256:aaa"}, Verified: true})
	g.Observe(Observation{Observer: "hop", Label: "root", Fingerprints: []string{"SHA256:root"}, Verified: true})
	g.Observe(Observation{Observer: "root", Label: "root", Fingerprints: []string{"SHA256:root"}, Verified: true})
	seen := 0
	g.Walk(func(n Node) { seen++ })
	if seen != len(g.Nodes()) {
		t.Fatalf("the walk visited %d of %d nodes — a cycle must not revisit", seen, len(g.Nodes()))
	}
}
```

- [ ] **Step 2: run them and watch them fail**

Run: `go test ./internal/fleet/`
Expected: build failure — the package does not exist.

- [ ] **Step 3: write the model**

`Observation` is what a probe learned; `Observe` folds it in. Identity: a node matches when the
observation's fingerprint set intersects the node's. Verified observations create or extend nodes;
unverified ones become candidates keyed by `(observer, label)`.

```go
// State is what the operator may do about a machine (spec §3.4).
type State int

const (
	Mounted State = iota // declared by the root, verified, polled
	Available            // declared and verified, not polled
	Ready                // verified by the root, not yet declared
	Blocked              // a hop declares it; its recipe names a credential the root lacks
	Candidate            // declared somewhere, unverified, undiagnosed
)
```

`Node` carries `Fingerprints []string`, `Labels []Label` (`{Observer, Alias}`), `Hostname`, `Recipe`,
`Shell`, `TmuxVersion`, `UID`, `Samples []time.Duration`, `State`, `Reason`.

- [ ] **Step 4: run the tests to green, then add the budget**

`Budget{MaxDepth, MaxPerObserver int}`; `Cut{Observer string, Skipped int, Why string}`. `Graph.Cuts()`
returns them. A budget that cuts silently is the defect — assert the count is reported.

- [ ] **Step 5: commit**

```bash
git add internal/fleet
git commit -m "feat(fleet): the fleet as a graph, keyed on identity rather than on a label" -- internal/fleet
```

---

## Task 2: harvest the fingerprint from the probe we already run

**Files:**
- Create: `internal/hostset/fingerprint.go`, `internal/hostset/fingerprint_test.go`
- Modify: `internal/hostset/probe.go` (add `Fingerprints []string` to `Result`; fill it from stderr)
- Modify: `cmd/tmux-hub/main.go` (`sshProbe`, ~line 538: add `-v`)

**Interfaces:**
- Consumes: nothing new.
- Produces: `hostset.Result.Fingerprints`, `hostset.ParseHostKeys(stderr string) []string`.

- [ ] **Step 1: write the failing test, from CAPTURED output**

The fixture is real: these two lines are what `ssh -v` printed on 2026-08-20. Do not hand-write a
variant — this repository has twice paid for an invented fixture.

```go
func TestParseHostKeysReadsWhatSSHActuallyPrints(t *testing.T) {
	const captured = `OpenSSH_10.0p2, OpenSSL 3.5.4
debug1: Connecting to nuc port 22.
debug1: Server host key: ssh-ed25519 SHA256:Px9AwAvwtlm7dTELzGLm0WqaYPsFtA/7kMgsXQjfxr4
debug1: Host 'nuc' is known and matches the ED25519 host key.
tmux 3.2a
`
	got := ParseHostKeys(captured)
	want := []string{"SHA256:Px9AwAvwtlm7dTELzGLm0WqaYPsFtA/7kMgsXQjfxr4"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("ParseHostKeys = %q, want %q", got, want)
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
```

- [ ] **Step 2: run, watch fail, implement**

`ParseHostKeys` scans lines for `Server host key: <type> <fingerprint>` and returns every distinct
fingerprint, in order. Tolerant: an unrecognised line is skipped, never an error.

- [ ] **Step 3: fill `Result.Fingerprints` in `Probe`, and add `-v` to the production runner**

`Probe` already receives stderr. One line: `r.Fingerprints = ParseHostKeys(errOut)`. `sshProbe` gains
`-v`. **Do not add `-vv`**: it multiplies output for no new fact.

- [ ] **Step 4: assert the probe's payload still works on a non-POSIX login shell**

Measured 2026-08-20 on a live macOS host whose login shell is Nushell: `tmux -V; id -u` answers
`tmux 3.7b` and `501`, because both are external commands and `;` is a separator there too. Pin it:

```go
func TestTheProbePayloadIsNotPOSIXOnly(t *testing.T) {
	// The payload must contain no quoted program name and no POSIX-only operator: a quoted
	// program name is a parse error in Nushell AT rc=0, which is how a host went invisible.
	for _, forbidden := range []string{"'tmux'", "&&", "2>&1", "$(", "`"} {
		if strings.Contains(probePayload, forbidden) {
			t.Errorf("the probe payload contains %q, which a non-POSIX login shell refuses", forbidden)
		}
	}
}
```

Extract the payload to a `const probePayload = "tmux -V; id -u"` so the test can read it.

- [ ] **Step 5: commit**

```bash
git commit -m "feat(hostset): identity comes free from the probe's own handshake" -- internal/hostset cmd/tmux-hub/main.go
```

---

## Task 3: the harness

**Files:**
- Create: `harness/topology/basic.toml`, `harness/gen/*.go`, `harness/image/Dockerfile`,
  `harness/README.md`, `harness/gen/gen_test.go`
- Create: `internal/e2e/harness_test.go` (the self-test, `-tags e2e`)

**Interfaces:**
- Produces: `harness/gen` writes `harness/.out/compose.yaml` plus a key directory; the self-test brings
  the fleet up and asserts the topology's own properties.
- Consumes: `internal/conf` for TOML.

- [ ] **Step 1: write the topology** — exactly spec §4's `basic.toml`, adapted to the packaged-tmux ruling
  (`3.2a` and `3.5a`).

- [ ] **Step 2: write the generator's test first**

```go
func TestGeneratedComposeGivesEveryMachineItsOwnHostKey(t *testing.T) {
	// A cloned key would collapse the whole fleet to ONE node (spec §2.3), which is the failure
	// mode a container fixture is most likely to have and least likely to show.
	out := Generate(loadTopology(t, "../topology/basic.toml"))
	if strings.Contains(out.Compose, "COPY ssh_host_") {
		t.Fatal("a host key is baked into the image: every machine would share one identity")
	}
	if !strings.Contains(out.Compose, "ssh-keygen -A") {
		t.Fatal("no per-container host-key generation in the compose entrypoint")
	}
}

func TestEveryDeclaredEdgeGetsItsDelay(t *testing.T) {
	out := Generate(loadTopology(t, "../topology/basic.toml"))
	if !strings.Contains(out.Compose, "netem delay 180ms") {
		t.Errorf("the hop->leaf delay is not in force; the mount policy would be untestable")
	}
}
```

- [ ] **Step 3: one base image, parameterised**

`harness/image/Dockerfile` takes `ARG BASE` (`ubuntu:22.04` | `debian:trixie`) and `ARG SHELL_KIND`
(`posix` | `nushell`), installs `openssh-server tmux`, installs `nu` when asked, sets the login shell,
and generates host keys in the entrypoint (`ssh-keygen -A`). `NET_ADMIN` is granted so the entrypoint can
apply `tc qdisc add dev eth0 root netem delay <d>`.

- [ ] **Step 4: the fake `claude`, from captured JSON**

Capture once from the real CLI (`claude agents --json --all`), strip it to two sessions, and have the
container's `claude` replay it. Never hand-write the shape.

- [ ] **Step 5: the self-test — the harness proves itself before any feature exists**

```go
func TestTheHarnessFleetIsAFleet(t *testing.T) {
	f := up(t, "basic")            // brings compose up, tears down via t.Cleanup
	fps := map[string]string{}
	for _, m := range f.Machines() {
		fp := f.Fingerprint(t, m)  // ssh -v ... | ParseHostKeys
		if prev, dup := fps[fp]; dup {
			t.Fatalf("%s and %s share host key %s — the fixture is one machine wearing five hats",
				m, prev, fp)
		}
		fps[fp] = m
	}
	// The Nushell node must fail a quoted program name AT rc=0 — that exit code is the whole
	// reason the class was invisible, and a node failing honestly with rc!=0 tests nothing.
	out, rc := f.Run(t, "hop", `'tmux' '-V'`)
	if rc != 0 || !strings.Contains(out, "parse") {
		t.Errorf("the nushell node answered rc=%d %q; want rc=0 and a parse error", rc, out)
	}
}
```

- [ ] **Step 6: `harness/README.md` states what it cannot prove** — no macOS, no real latency variance, no
  mesh-VPN ACLs, not the vendor CLI. Green containers are not a green fleet.

- [ ] **Step 7: commit**

```bash
git add harness internal/e2e/harness_test.go
git commit -m "test(harness): a declared topology, and the fleet that proves it is a fleet" -- harness internal/e2e/harness_test.go
```

---

## Task 4: enumerate and resolve a hop's ssh config

**Files:**
- Create: `internal/hostset/remote.go`, `internal/hostset/remote_test.go`
- Modify: `internal/hostset/sshconfig.go` — `Candidate` gains `Via string` and
  `Recipe map[string]string`. This task OWNS that file; no concurrent task may touch it.

**Interfaces:**
- Consumes: an existing master (`tmux.Target`-shaped: alias + control path), `ParseSSHConfig`.
- Produces: `hostset.RemoteCandidates(ctx, run RemoteRunner, hop string) ([]Candidate, error)`, where each
  `Candidate` gains `Via string` (the hop) and `Recipe map[string]string` (from `ssh -G`).

- [ ] **Step 1: the test, from measured reality**

Two cases, both real: a hop with **no** `~/.ssh/config` must contribute nothing and not error (measured:
`nuc` has none); and a hop whose config lists aliases must yield them with the resolved recipe.

```go
func TestAHopWithNoSSHConfigContributesNothingAndDoesNotFail(t *testing.T) {
	run := func(ctx context.Context, hop, payload string) (string, string, int) {
		return "", "cat: /home/dev/.ssh/config: No such file or directory", 1
	}
	got, err := RemoteCandidates(context.Background(), run, "hop")
	if err != nil {
		t.Fatalf("a hop with no ssh config is not an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("invented %d candidates from a hop with no config", len(got))
	}
}
```

- [ ] **Step 2: implement with ONE round trip per hop for enumeration**

Read the hop's config with a payload that is safe on a non-POSIX login shell: no quoted program name, no
`&&`, no `2>&1`. `cat ~/.ssh/config` is enough; a missing file is rc≠0 with an empty stdout, which is
"nothing to offer", not a failure.

- [ ] **Step 3: resolve each alias ON THE HOP with `ssh -G`**

`ssh -G <alias>` on the hop returns the effective recipe. Batch: one invocation listing every alias would
need shell looping, so cap it — resolve at most `Budget.MaxPerObserver` aliases per hop and report the
cut. `ssh -G` answers for ANY string (measured: `definitely-not-a-host-xyz` resolved), so it resolves and
never enumerates; enumeration stays the parse.

- [ ] **Step 4: E2E on the harness** — the hop declares `leaf`, and `RemoteCandidates` returns it with
  `Via: "hop"` and a recipe naming `hop-only`.

- [ ] **Step 5: commit.**

---

## Task 5: the Blocked diagnosis

**Files:**
- Create: `internal/fleet/diagnose.go`, `internal/fleet/diagnose_test.go`

**Interfaces:**
- Consumes: a resolved recipe (`map[string]string` from `ssh -G`) and a local-file predicate.
- Produces: `fleet.Diagnose(recipe map[string]string, haveLocal func(path string) bool) (State, string)`.

- [ ] **Step 1: the test — the remedy is the product**

```go
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
}

func TestARecipeWeCanSatisfyIsReady(t *testing.T) {
	recipe := map[string]string{"hostname": "leaf.internal", "identityfile": "~/.ssh/id_ed25519"}
	if state, _ := Diagnose(recipe, func(string) bool { return true }); state != Ready {
		t.Errorf("state = %v, want Ready — the key is here, so one tick declares it", state)
	}
}
```

- [ ] **Step 2: implement, then commit.**

---

## Task 6: surface it

**Files:**
- Create: `internal/ui/discovered.go`, `internal/ui/discovered_test.go`
- Create: `internal/fleetcache/cache.go`, `internal/fleetcache/cache_test.go`
- Modify: `internal/ui/picker.go` (owner: this task only)

**Interfaces:**
- Consumes: `fleet.Graph`, `fleetcache`.
- Produces: a picker section listing candidates with their reason, ordered by **bucketed** RTT.

- [ ] **Step 1: the ordering test — jitter is the defect**

Buckets, not raw RTT: measured, one host swung 5.4 / 9.1 / 15.7 / 18.4 s between probes, so a
latency-sorted list reorders between openings and the row you ticked moves. Bucket to
`<50ms / <250ms / <1s / slower`, stable by name inside a bucket, and take the value from the cache so the
list is instant and does not move while open.

```go
func TestOrderIsStableWhenLatencyJitters(t *testing.T) {
	a := order(rows(t, map[string]time.Duration{"alpha": 40 * time.Millisecond, "beta": 45 * time.Millisecond}))
	b := order(rows(t, map[string]time.Duration{"alpha": 45 * time.Millisecond, "beta": 40 * time.Millisecond}))
	if !reflect.DeepEqual(a, b) {
		t.Errorf("a 5 ms jitter reordered the list: %v then %v — the row the operator ticked moved", a, b)
	}
}
```

- [ ] **Step 2: three tiers** — ticked+mounted, ticked+available, then the rest by bucket.
- [ ] **Step 3: frame test** — the section is on the string `View()` returns, at 80 columns, and every row
  shows a reason or a tick box. A screen defined and never called is this repo's signature defect.
- [ ] **Step 4: regenerate the mockup** and add a scene for the discovered section.
- [ ] **Step 5: commit.**

---

## Task 7: the E2E cases

**Files:**
- Create: `internal/e2e/discovery_test.go`

- [ ] **Step 1** — the whole path on the harness: root probes `hop`, harvests its fingerprint, enumerates
  the hop's config, resolves `leaf`, diagnoses it `Blocked`, and the picker shows it with the remedy.
- [ ] **Step 2** — `hop-again` folds into `hop`: one node, two labels.
- [ ] **Step 3** — `twin-a` and `twin-b` stay two nodes under one hostname.
- [ ] **Step 4** — the cycle terminates: the crawl finishes and each node is visited once.
- [ ] **Step 5** — stop the hop mid-run; `leaf`'s reason names the hop, not a tunnel.
- [ ] **Step 6** — a budget cut is REPORTED with its count.
- [ ] **Step 7: commit.**

---

## Ordering and parallelism

Tasks **1, 2, 3** share no files and are independent — run them concurrently in worktrees.
Then **4** (needs 2's `Candidate` shape) and **5** (needs 1's `State`) run concurrently.
Then **6**, then **7**. Merge to `main` after each strand's gates pass, then run the full E2E suite
against merged main, then the review wave, then release.
