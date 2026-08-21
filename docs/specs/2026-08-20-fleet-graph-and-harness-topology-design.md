# The fleet as a graph, and the topology that tests it — design

**Status:** approved for planning (2026-08-20)
**Scope:** part 1 of 7 of the transitive-fleet landscape. This document decides the DATA MODEL and the
TEST VOCABULARY, and nothing else.
**Authority it answers to:** `docs/design.md`. Where the two disagree, design.md wins and this document
is wrong.
**Not published:** this lives in `docs/specs/` as a build artefact, beside `docs/plans/`. It is NOT in
`docs/superpowers/specs/` — that path is excluded by this machine's GLOBAL gitignore
(`~/.config/git/ignore: **/docs/superpowers/`), and this repository has already paid for a design whose
only copy sat in an ignored directory while the authority document cited it 77 times. `docs/design.md` is the document
the public repository carries; a spec for unbuilt work is not.

---

## 1. What this decides, and what it deliberately does not

The hub's fleet is a flat list today: `hosts.toml` names aliases, each resolves through the operator's
`~/.ssh/config`, and every host is one hop away. The agreed landscape adds hosts reachable only
*through* another host, an unbounded chain, a latency-based mount policy, and intermediate hosts drawn
as directories.

Six of the seven parts are mechanism. They all rest on two artefacts, and both are here because **they
are one vocabulary**:

- **the graph** the hub derives — what a node is, what identifies it, what an edge means;
- **the topology** a test declares — the same nouns, written by hand, so a test can say *"the graph you
  derived equals the topology I declared, minus what policy excluded, and you named a reason for every
  exclusion."*

Two schemas would put a translation in every test, and this repository paid three times in one day for a
rule that lived in two places (the pin key, the alias key, the `beats`/fold predicate). So: one set of
nouns, one file format, two readers.

**Deferred, and named so this document cannot be read as covering it:** discovery (part 3), latency
measurement and the mount policy's thresholds (part 4), presentation (part 5), the on-disk config schema
(part 6), and transport for the second hop and beyond (part 7). This document defines the *states* a
policy assigns and the *fields* it reads; it chooses no threshold.

---

## 2. Node identity: the ssh host key, taken from the handshake we already make

A node is a machine the hub has opened an ssh connection to. This section settles what makes two
observations the same machine, because every duplicate-row defect this repository has fixed came from
keying on something the product itself renames.

### 2.1 Why not the alias, the hostname, or the address

An alias is per-observer vocabulary: `web` here and `web` there are different machines, so merging two
hosts' configs by alias merges two machines. A hostname is worse than it looks — measured on the
operator's own tailnet, **four peers answer to `web-ws`**. An address is a lease.

### 2.2 What we use

**The SHA256 fingerprint of the server's host key, as ssh reports it during the connection.** Measured
2026-08-20 on the live fleet:

```
nuc       debug1: Server host key: ssh-ed25519 SHA256:Px9AwAvwtlm7dTELzGLm0WqaYPsFtA/7kMgsXQjfxr4
dev-air   debug1: Server host key: ssh-ed25519 SHA256:204cDUD+GOLPKWji+xcLfVkXGCOk5VMrZwMutymV4uA
```

Three properties, checked rather than assumed:

1. **It is free.** It arrives in the verbose output of the connection the probe already makes. The
   picker probes every candidate with `tmux -V`; the fingerprint is a by-product of that handshake.
2. **It distinguishes.** Two hosts, two fingerprints. Two machines answering to one hostname get two
   identities, which is what the alias and the hostname both fail to give.
3. **It is stable across calls.** Two consecutive connections to `nuc` reported an identical line.

And one property that is a FEATURE rather than a limit: **through a proxy it arrives only if the
connection completes.** Measured on `cortex-web`, whose recipe is
`ProxyCommand docker exec -i tailscale-cortex nc %h %p`, the attempt ended in `Connection timed out
during banner exchange` — no fingerprint, therefore no node. Correct: an unreachable machine is a
candidate, and the graph must contain no node nobody has spoken to.

### 2.2.1 A PROXIED CONNECTION REPORTS THE JUMP HOST'S KEY, AND THAT WOULD FUSE TWO MACHINES

This is the sharpest hazard in this document and it is measured, not reasoned. A review of the first
implementation raised it as *"several hops may report several fingerprints, so a set could span two
machines"*. The truth is worse and simpler. Measured on OpenSSH 10.x, 2026-08-20:

| connection | `debug1: Server host key` lines | whose key |
|---|---|---|
| `ssh -v nuc` | 1 | `nuc` — `SHA256:Px9Aw…` |
| `ssh -v dev-air` | 1 | `dev-air` — `SHA256:204cD…` |
| `ssh -v -J nuc dev-air` | 1 | **`nuc`** — the JUMP host's |
| the same at `-vv` and `-vvv` | 1 | still the jump host's |

One earlier run of the jumped case reported **zero** such lines, so the count is not stable either: the
jump's handshake happens in a child process whose verbose output races the parent's.

Under §2.3's set-intersection merging that is not a missing fact but a WRONG one — the destination would
inherit the jump host's fingerprint, the sets would intersect, and two machines would collapse into one
node. That is exactly the failure §2.3 warns about for cloned container keys, arriving by a different
road and without any container.

**The rule, therefore:**

> A fingerprint is evidence of a machine's identity ONLY when the connection that produced it was DIRECT.
> If the resolved recipe names `proxyjump` or `proxycommand`, the fingerprint from that connection is
> discarded — it belongs to the jump — and the machine stays a CANDIDATE with that as its reason.

The condition is cheap to detect because we already resolve the recipe: `ssh -G -J nuc dev-air` reports
`proxyjump nuc`, and a direct `ssh -G dev-air` reports neither key. So the check is a lookup in a map the
discovery path already holds, not a new probe.

The consequence is honest and worth stating plainly: **a machine reachable only through a hop cannot be
identified by v1 at all.** It is a candidate with a remedy, which is the same place §3.4's `Blocked`
leaves it. Verifying such a machine needs a handshake made FROM the hop — part 7.

The parse is of ssh's own verbose line and must be calibrated against real output, not invented — the
two samples above are captured, and this repository has twice paid for a fixture written from
imagination.

### 2.3 Identity is a SET, not a scalar

A host may hold several host keys (ed25519, rsa, ecdsa) and which is presented depends on negotiation,
so two connections made with different `HostKeyAlgorithms` can report different fingerprints for one
machine. Therefore:

> A node's identity is the SET of fingerprints observed for it. Two observations are the same node when
> their sets intersect. A newly observed fingerprint for a known node joins the set; it never forks the
> node.

The inverse hazard is real, and the harness is where it bites: **cloned machines share a host key.** A
container fleet built from one image with a baked key would be ONE node and the graph would collapse to
a single vertex. So:

> The harness MUST generate host keys per container at start-up, and its self-test MUST assert that
> every declared machine reported a distinct fingerprint.

That assertion is the check that the fixture has not quietly encoded the belief that a fleet is a fleet.

### 2.4 What identity is not for

Not authentication and not authorisation: it says which machine answered, and `known_hosts` remains what
decides whether that machine is trusted. Nor is it the tmux server's identity — a server is
`#{pid}:#{start_time}` (design.md §3) and is an ATTRIBUTE of a node, since a node can run several
servers and a server cannot outlive its machine.

---

## 3. The graph

### 3.1 Nouns

**Node** — a machine we have spoken to. Carries:

| field | meaning | source |
|---|---|---|
| `Fingerprints` | the identity set (§2.3) | the handshake |
| `Labels` | every alias any observer uses for it, with the observer | per-observer ssh config |
| `Recipe` | the resolved transport that reached it | `ssh -G`, per observer |
| `Shell` | `posix` or `other` | measured per node |
| `TmuxVersion` | what `tmux -V` answered, or absent | the probe |
| `UID` | the remote uid the recipe lands as | the probe |
| `Samples` | round-trip observations, newest first | the probe |
| `State` | §3.4 | derived |

`Shell` is a field rather than a footnote because a non-POSIX login shell made a whole host invisible on
2026-08-20: a quoted program name is legal POSIX and a parse error in Nushell, **returned at rc=0**, so
the poll looked like a host with no panes. A node whose family is unproven is `other`, which is the safe
direction.

`UID` is on the node because it decides *whose* sessions are visible: 1000 on `nuc` and **501** on
`dev-air`, and both the tmux socket path and `~/.claude` follow the uid.

**Edge** — *"observer O reaches machine M by recipe R"*. Directed, per-observer, because a recipe is
only meaningful on the machine holding the key it names. The hub's own machine is the root observer;
there is exactly one root.

**Candidate** — a machine some observer declares that the ROOT has not verified: no fingerprint, hence
no node. Candidates are first-class — the picker shows them, with a reason each — and they are not
vertices.

### 3.2 Invariants

1. **Nodes are keyed by identity, never by label.** A label collision is a display question (part 5), not
   a graph question.
2. **The crawl visits identities.** A node already in the graph contributes its outgoing edges exactly
   once, which terminates a cycle without a special case.
3. **Only the root's own completed handshake creates a node.** Everything a hop merely *declares* is a
   candidate until the root has spoken to it. This is what keeps the graph free of machines nobody has
   reached, and it is the invariant that makes part 7 (reaching a machine with a hop's credentials) a
   real feature rather than a refinement.
4. **Every candidate and every unmounted node carries a reason that names a remedy.** A graph that
   silently omits is indistinguishable from a graph that finished.
5. **The root's declaration wins, and nothing is ever written outward.** Not a remote ssh config, not a
   remote hub config, not remote favourites.
6. **A hop's hub config ranks; it never creates.** `hosts.toml` and `favourites.json` read from a hop
   supply priority and defaults. Existence and transport come only from that hop's ssh config.

### 3.3 Budgets, and why they are not the latency budget

Latency bounds *mounting*. It does not bound *crawling*: an unbounded chain over hosts that each declare
18 aliases is combinatorial, and the operator's own machine declares 18. So the crawl carries its own two
numbers — a maximum depth, and a maximum number of candidates verified per observer — and invariant 4
applies to both: what a budget cut is reported, with its count.

The numbers belong to part 3, measured on the harness rather than chosen here. This document requires
only that they exist, that they are separate from the latency budget, and that exceeding one is a
reported outcome rather than a silent horizon.

### 3.4 The five states

Three are nodes; two are candidates. That split is invariant 3 made visible.

| state | node? | meaning | the operator's next move |
|---|---|---|---|
| `Mounted` | yes | declared by the root, verified, polled | none |
| `Available` | yes | declared and verified, not polled — over the latency budget, or unticked | mount it |
| `Ready` | yes | verified by the root, not yet declared by the root | one tick declares it |
| `Blocked` | no | a hop declares it, and its resolved recipe names a credential the root does not hold | the printed remedy |
| `Candidate` | no | declared somewhere, unverified, undiagnosed | the failure's own sentence |

**`Blocked` is a DIAGNOSIS, not a verification**, and getting that wrong is the mistake this document's
own review caught. A fingerprint for a machine the root cannot reach could only come from running ssh
*on the hop* — which is part 7. So `Blocked` is derived from evidence the root already has: the hop's
stanza, resolved with `ssh -G` on the hop, names an `IdentityFile`; the root checks whether that file
exists locally, and whether its own key is plausibly accepted. When it is not, the state is `Blocked`
and the reason is a specific command (`ssh-copy-id …`, or "copy `~/.ssh/<name>` here"), never the word
`unreachable`.

The honest consequence, stated because it sizes part 3: **with ssh-config-only discovery and no nested
ssh, discovery adds no new NODES beyond what the root can already reach.** Its product is a set of
diagnosed candidates — "here are the machines behind your hops, and here is the one command that makes
each mountable". That is the answer we chose deliberately over showing rows that cannot be opened.

---

## 4. The topology file

One file, TOML, read by two consumers: the harness generator that builds the fleet, and the test that
uses it as the oracle. TOML because `internal/conf` exists precisely so a second config dialect could
not grow, and because a Go test then loads the same bytes the generator did.

```toml
# internal/e2e/topology/basic.toml
[fleet]
name = "basic"

# The root observer: the machine the hub runs on.
[[machine]]
id       = "root"
shell    = "posix"
tmux     = "3.7b"
networks = ["net-a"]
# Two aliases for ONE machine, so identity merging is exercised at hop 1 — where v1 can
# actually verify it (§3.4). `hop-again` must fold into `hop`, not become a second node.
declares = ["hop", "hop-again", "twin-a", "twin-b"]

[[machine]]
id       = "hop"
aliases  = ["hop", "hop-again"]
shell    = "nushell"            # the measured failure mode, present from the first topology
tmux     = "3.2a"
networks = ["net-a", "net-b"]
hub      = true                 # carries hosts.toml + favourites.json, so ranking has an input
declares = ["leaf", "hop"]      # `hop` again: a cycle, to prove the crawl terminates

# Two DIFFERENT machines answering to one hostname: the label-collision test. Both are on the
# root's network, so both verify, and the graph must hold two nodes with one label.
[[machine]]
id       = "twin-a"
hostname = "twin"
networks = ["net-a"]

[[machine]]
id       = "twin-b"
hostname = "twin"
networks = ["net-a"]

# Off the root's network entirely: the root cannot reach it, and the hop's recipe for it names
# the hop's own key -> `Blocked`, with a remedy.
[[machine]]
id       = "leaf"
shell    = "posix"
tmux     = "3.2a"
networks = ["net-b"]

[[edge]]
from = "root"
to   = "hop"
delay = "5ms"

[[edge]]
from = "hop"
to   = "leaf"
delay = "180ms"
key  = "hop-only"               # the leaf accepts the HOP's key, not the root's
```

### 4.1 What a topology must be able to express

Each row is earned by a real defect or a stated requirement. This is what the FORMAT must express — one
file need not contain every row, and `basic.toml` above deliberately does not (it carries no
over-budget delay and no mid-run failure; those get their own topologies).

| expressible | earned by |
|---|---|
| depth ≥ 3 | "unbounded" must not be two special-cased |
| a machine off the root's network | the whole point: the root cannot reach it directly |
| per-edge delay | the mount policy, deterministic where the live fleet swings fourfold |
| `shell = "nushell"` | the host that was invisible at rc=0 on 2026-08-20 |
| a machine with no ssh config | `nuc` measured: it has none, so a hop may offer nothing |
| `hub = true` | "take the hop's list and favourites into account" |
| two tmux versions | every tmux claim in design.md is dual-version; `client_activity` segfaults 3.2a |
| two aliases, one machine | identity merging, verifiable at hop 1 |
| two machines, one hostname | four `web-ws` on the live tailnet |
| a cycle | termination |
| a machine stopped mid-run | attribution: the far node's reason must name the hop |
| `key = <owner>` per edge | the `Ready` / `Blocked` distinction |

### 4.2 What the harness generates rather than carries

- **Host keys, per container, at start-up** (§2.3). Never baked, never committed.
- **User keys, into a temp directory**, never committed: the repository is public, and a secret scanner
  should fire on a key inside it.
- **The compose file**, from this topology. One base image with build arguments beats one Dockerfile per
  machine; adding a case must be one table row, or the harness becomes a second product to maintain.
- **The fake `claude`**, replaying JSON *captured from the real CLI* — this repository has twice paid for
  a fixture written from imagination.

### 4.3 What the harness cannot prove, stated in its own README

macOS cannot be containerised, and darwin support was found broken on 2026-08-20 by a release build
rather than by any test; the cross-compile CI job remains the only guard there. Nor can it prove real
latency variance, a mesh VPN's ACL semantics, or the vendor CLI's behaviour. It proves mechanism, not
ecology: green containers are not a green fleet, and the live fleet stays the final check.

---

## 5. The oracle

For a topology `T` and a derived graph `G`:

1. **Coverage.** Every machine in `T` that any declaration names appears in the result — as a node if the
   root verified it, as a candidate otherwise. Nothing declared is absent.
2. **Identity.** `|nodes(G)|` equals the number of distinct machines the ROOT verified — so `hop` and
   `hop-again` are one node, `twin-a` and `twin-b` are two, and the cycle adds none.
3. **States.** Each state equals what `T` plus the policy predicts: `leaf` is `Blocked` because its edge
   names the hop's key; a machine whose cumulative delay exceeds the budget is `Available`, not `Mounted`.
4. **Reasons.** Every candidate and every unmounted node carries a non-empty reason, and no reason names
   a mechanism the topology does not contain — the failure this repository has already shipped once was a
   status naming a tunnel in a design that has no tunnels.
5. **Termination.** The crawl finishes, and each node contributes its edges once.

Point 4 is the one to write tests for first, because it is the one a passing suite can hide: a reason
string is easy to produce and hard to keep true.

---

## 6. Testing

- **The model is a data structure.** Identity merging, cycle termination, budget reporting and state
  derivation are unit tests with no network and no containers. They come first and they are cheap.
- **The oracle is the harness's own self-test**, standing before any hub feature exists: bring the fleet
  up; assert every machine reported a distinct fingerprint (§2.3); assert the delays are in force; assert
  the Nushell machine really answers a POSIX-quoted command with a parse error **at rc=0** — that exit
  code is the whole reason the class was invisible, and a machine that fails honestly with rc≠0 would
  test nothing.
- **Every later part ships its E2E case in the same commit as its feature.** The harness is what makes
  that possible for anything transitive.

---

## 7. Decisions taken here, for the record

1. Identity is the ssh host-key fingerprint SET, read from the handshake the probe already makes —
   and ONLY from a DIRECT connection: a proxied one reports the jump host's key (§2.2.1).
2. Only the root's completed handshake creates a node; everything else is a candidate.
3. Nodes are keyed by identity; labels are display.
4. Edges are per-observer and carry the resolved recipe.
5. Crawl budgets (depth, breadth) are separate from the latency budget, and both report what they cut.
6. Five states — three nodes, two candidates — and every state but `Mounted` carries a remedy.
7. `Blocked` is a diagnosis from the hop's resolved stanza, not a verification, so v1 needs no nested ssh.
8. One TOML vocabulary for graph and topology, read by `internal/conf`.
9. The harness generates host keys, user keys and its compose file, and commits none of them.
10. A hop's ssh config supplies existence and transport; its hub config supplies ranking only.
11. Nothing is ever written to a remote machine.
