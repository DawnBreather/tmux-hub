# The harness: a declared topology, and the fleet that realises it

A container fleet the hub can be pointed at, declared in one TOML file and generated from it.
It exists so that "the graph you derived equals the topology I declared, minus what policy
excluded, and you named a reason for every exclusion" is a sentence a test can assert
(`docs/specs/2026-08-20-fleet-graph-and-harness-topology-design.md` §5).

```
go run ./harness/gen                                        # topology -> harness/.out/
docker compose -f harness/.out/compose.yaml up -d --build --wait
docker compose -f harness/.out/compose.yaml down -v         # and afterwards
```

`go test -tags e2e -run TestHarness ./internal/e2e/` does all of that itself, including the
teardown. Every case in `internal/e2e/harness_test.go` answers to that one pattern, and a case in
the same file refuses a new one that does not — the pattern used to name only one of seven, so a
reader who followed this line paid the whole image build for host-key distinctness and got none of
the delay, reachability, observer-agreement or hostname checks.

`harness/gen`'s own tests are *not* behind a tag: they are one of the `ok` lines of a plain
`go test ./...`. Only the cases that start containers need `-tags e2e`.

## What it cannot prove

This is the section to read first. **Green containers are not a green fleet.**

- **No macOS.** It cannot be containerised, and darwin support in this repository was found broken
  on 2026-08-20 by a release build rather than by any test. The cross-compile gates
  (`GOOS=darwin GOARCH=arm64 go vet ./...`) remain the only guard there, and `uid 501` — the
  figure that makes a remote uid load-bearing at all — belongs to a machine this harness has none
  of.
- **No real latency variance.** `netem delay 180ms` is a constant. The live fleet swung
  5.4 / 9.1 / 15.7 / 18.4 s between probes of one host, and that is the behaviour a latency-sorted
  list has to survive. The harness gives determinism, which is the opposite property; both are
  needed and only one is here.
- **No mesh-VPN semantics.** A docker network is not a tailnet: no ACLs, no key expiry, no
  MagicDNS, no `ProxyCommand` through another container's network namespace. The measured
  `cortex-web` case — a proxy that never completes the handshake, so there is no fingerprint and
  therefore no node — is representable as a machine on no shared network, but the *mechanism* that
  produced it is not reproduced.
- **Not the vendor CLI.** `claude` here is a shell script replaying two captured records. It cannot
  tell you what the real CLI does with a version bump, what an attach costs, or that an attach
  claims a pre-warmed process rather than starting one.
- **Not the hub's own tmux claims on 3.7b.** The two versions here are `3.2a` and `3.5a`, packaged.
  Anything specific to 3.7b — which is newer than any distribution — is still answered only by the
  live fleet.
- **One fleet at a time.** The compose project name is derived from the topology name, so two
  concurrent runs of the same topology are the same fleet. The generator's output directory is
  fixed for the same reason (`build.context` is relative to the compose file).

## The two properties the whole fixture rests on

**Host keys are generated per container, at start-up.** A key baked into the image would be copied
into every container, every machine would present one identity, and the derived graph would
collapse to a single node — the fixture would then encode the belief that a fleet is a fleet
(§2.3).

**The key does not arrive through a `COPY`.** Measured 2026-08-20 on `debian:13`: installing
`openssh-server` runs a postinst that calls `ssh-keygen -A`, so the image layer ends up holding
`ssh_host_{ecdsa,ed25519,rsa}_key` — three real keys with real fingerprints — and `ssh-keygen -A`
at start-up only creates keys that are *missing*, so it would leave them alone. Every container
built from one image would then present one identity, and no reading of the Dockerfile would have
found it. The image deletes them in the same `RUN` that installs the package.

Four things enforce the property, and the last two cannot be forgotten:

1. `harness/gen`'s tests require `ssh-keygen -A` in the generated start-up command.
2. A test reads `harness/image/Dockerfile` and fails on any `ssh_host_` mention, on any build-time
   `ssh-keygen -A`, and — the positive half — unless the packaged keys are removed exactly once.
   (The check the plan specified, `COPY ssh_host_` inside the *compose* file, cannot fail: a
   compose file has no `COPY` directives.)
3. The generated start-up **refuses to run** if a host key is already present when the container
   starts, naming the `rm` that fixes it. A fleet that will not come up is recoverable; a fleet
   that is secretly one machine is not.
4. The image has no working default command. sshd will not start without host keys, so an image run
   outside the generated compose refuses with a sentence naming the reason. There is no way to make
   this image work standalone except by baking a key, which is the thing forbidden.

And the self-test asserts the outcome rather than the mechanism: every declared machine reported a
distinct fingerprint, and what an observer's handshake reported equals what the machine itself
holds.

**Per-edge delay is really applied.** `tc qdisc … netem delay` needs `NET_ADMIN`, and the
generator grants it in the same branch that writes the qdisc, so a delay cannot be declared
without the right to apply it. Two fixture rules follow, and both are refusals rather than
guesses:

- The qdisc goes on the **far** end's interface, because netem acts on what leaves an interface:
  there it holds back exactly this edge's replies, so the declared figure is the round trip an
  observer measures. Near-end placement would instead hold back the observer's *own* requests, so
  `root -> hop`'s 5 ms would land on every link the root declares — the twins included, and they
  declare no delay at all. The far end is not immune to that in general (`hop`'s qdisc does slow
  `hop -> twin-a`, which nothing declares); what it buys is that the affected peers are the ones
  the *delayed* machine reaches rather than the ones the observer reaches, and the refusal below
  covers every affected link an edge actually declares.
- One delay per `(machine, network)`, and no edge inside somebody else's qdisc. Two edges into one
  machine over one network share one interface, so the second `tc qdisc replace` would silently
  replace the first — and **any other edge** with an endpoint on that interface is delayed by a
  figure it never declared, including one that declares no delay at all. Measured 2026-08-20 on the
  version that accepted it: `a -> c delay=180ms` plus a bare `b -> c` on one network gave `c` a
  single 180 ms qdisc, and b's traffic from c came back 180 ms late while `b -> c` declared nothing.
  Both are **refused**, in either file order. An edge whose endpoints share no network, or more than
  one, is refused too: reachability comes from the networks, and an edge only annotates a link that
  already exists.

## Facts it rests on, each measured rather than assumed

| fact | measured |
|---|---|
| `debian:trixie` packages tmux `3.5a-3` | 2026-08-20, sources.debian.org |
| `ubuntu:22.04` packages tmux `3.2a-4ubuntu0.2` | 2026-08-20, Launchpad |
| nushell is packaged by neither Debian nor Ubuntu | 2026-08-20, both APIs, with `fish` as the control that fires |
| nushell 0.115.0 answers `'tmux' '-V'` with `nu::parser::parse_mismatch` at **rc=1** | 2026-08-20, the pinned release binary |
| nushell 0.115.0 answers `tmux -V; id -u` correctly at rc=0 | same run — the bare program name is the fix |
| installing `openssh-server` generates host keys into the image layer | 2026-08-20, `debian:13`, three keys with fingerprints |
| Compose interpolates `$VAR` in a `command` string before the container sees it | 2026-08-20, Compose 5.1.4, both poles in one container |
| the real CLI's `--all` adds the ended-with-no-worker sessions (34 against 17) | 2026-08-20, `claude agents --json[ --all]` |
| docker does **not** name interfaces in the compose file's network order | 2026-08-20, the running fleet: on `hop`, `eth0` holds net-b and `eth1` holds net-a |
| the fleet comes up in 8–20 s on warm images, 5 distinct host keys | 2026-08-20, first real execution, Docker 29.5.2 / Compose 5.1.4 |
| `sch_netem` autoloads for a `NET_ADMIN` container with the module unloaded on the host | 2026-08-20, `lsmod` empty before, both qdiscs present after |
| root -> hop (5 ms) 299 ms against hop -> leaf (180 ms) 2.50 s over ssh | 2026-08-20, same run — the differential the delay case asserts |

The interface fact is load-bearing and it is why the generated start-up looks up its interface by
**address** (`ip -o -4 addr show | awk '$4 ~ /^10\.99\.1\.11\//'`) rather than by name: a start-up
written against `eth0` would have put `root -> hop`'s qdisc on the net-**b** interface, which is the
`hop -> leaf` link, and both delay assertions would have been wrong in a way that still looked like
a working fleet.

Two of those deserve their consequence spelled out.

**The plan asked the self-test to require the nushell parse failure at `rc=0`**, on the correct
grounds that a silent failure is why the class was invisible. On the version this harness installs
it is rc=1, so pinning the figure would redden a correct fixture. The self-test asserts instead
what does not depend on someone else's exit code and is the actual defect: the command **did not
run** — `tmux -V`'s output is absent while nushell's own error code is present — and it logs the
rc beside the measured figure.

**A lone `$` in the generated compose is eaten.** `tc qdisc replace dev "$iface" …` came out of
`docker compose config` as `dev ""`: the delay silently not applied, at **rc=0** with only a
warning on stderr. Every `$` is doubled at the single point where shell text enters the file, and
two tests guard it — one that every `$` in the generated file is part of a `$$`, and one that
`docker compose config` writes nothing to stderr at all.

## What is generated, and what is committed

Generated into `harness/.out/`, which `.gitignore` excludes:

- `compose.yaml` — a function of the topology, so committing it would give one question two
  answers. Every per-machine decision is *in* it as that service's start-up command, which is what
  makes a diff of it the review.
- `keys/` — one ed25519 keypair per machine plus one per edge that names a key. Never committed:
  this repository is public and a secret scanner should fire on a private key inside it. An
  existing keypair is reused, so regenerating does not invalidate the `authorized_keys` a running
  container already installed.
- `ssh/<machine>/config` — what `ssh -G` on that machine will answer. A machine that declares
  nothing gets **no file**, not an empty one: `nuc` measured, it has no `~/.ssh/config` at all, and
  "this hop offers nothing" has to be distinguishable from "this hop offers an empty list".
- `ssh/<machine>/hosts.toml` — for a `hub = true` machine. A hop's hub config ranks; it never
  creates (§3.2 invariant 6).
- `fleet.json` — the topology as the generator understood it, so a consumer can read what was
  built without importing `harness/gen` (it is `package main`) and without a second TOML reader.
  The self-test derives every vantage from it.

Committed: `topology/*.toml`, `image/Dockerfile`, `image/claude`, `image/claude-agents.json`.

## Quirks of the fixture, so they are not read as the product's

- **`StrictHostKeyChecking accept-new`** in every generated ssh config. Host keys do not exist
  until a container starts, so they cannot be pre-seeded into `known_hosts`; this is the direct
  consequence of the property above and is not how the hub's real hosts are configured.
- **`hostname: twin` on two containers**, plus a shared network alias `twin`. The name therefore
  resolves to *either* machine, non-deterministically — which is the point: four peers answer to
  `web-ws` on the live tailnet. Each twin is reached under its own alias; only the name they report
  for themselves collides.
- **Everything runs as `dev`, uid 1000**, because the remote uid decides both the tmux socket path
  and which `~/.claude` is read. Containers themselves run as root: `ssh-keygen -A`, `tc` and
  `chown` all need it.
- **A `#` after a value is part of that value.** `internal/conf` treats only a line that *begins*
  with `#` as a comment, deliberately, so that a `#` inside a value is safe. The topology therefore
  puts every comment on its own line, and the loader's refusal says so rather than complaining
  about quoting.

## Adding a case

A row of §4.1's table that the format cannot yet express is a change to `harness/gen`. Anything
the format already expresses is a change to a `.toml` file and nothing else — that is the
requirement §4.2 states as "adding a case must be one table row, or the harness becomes a second
product to maintain". `basic.toml` deliberately carries no over-budget delay and no machine stopped
mid-run; those get their own topologies, and each is one file.
