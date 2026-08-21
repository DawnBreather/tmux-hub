package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// loadTopology reads a declared topology the way the generator's own main does, so a file
// that the CLI would refuse cannot pass a test here.
func loadTopology(t *testing.T, path string) Topology {
	t.Helper()
	top, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile(%s): %v", path, err)
	}
	return top
}

// refused loads a topology written inline and requires a refusal naming want.
func refused(t *testing.T, content, want string) {
	t.Helper()
	_, err := Load(content)
	if err == nil {
		t.Fatalf("the topology was accepted; want a refusal naming %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal is %q, which does not name %q — a refusal a reader cannot act on",
			err, want)
	}
}

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

// The check above cannot fail in the direction it names: a compose file has no COPY
// directives, so `COPY ssh_host_` is absent from every compose this generator could write,
// correct or not. The place a baked key can appear is the IMAGE, and that is a file rather
// than a generated string — so this is where the assertion can fire.
func TestTheImageBakesNoHostKeyAndCannotStartWithout(t *testing.T) {
	raw, err := os.ReadFile("../image/Dockerfile")
	if err != nil {
		t.Fatalf("read the base image: %v", err)
	}
	var directives, removals int
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		directives++
		if strings.Contains(line, "rm -f /etc/ssh/ssh_host_") {
			removals++
			continue
		}
		if strings.Contains(line, "ssh_host_") {
			t.Errorf("the image names a host key file: %q — every container would share one identity", line)
		}
		if strings.Contains(line, "ssh-keygen -A") {
			t.Errorf("the image generates host keys at BUILD time: %q — that is one identity for the whole fleet", line)
		}
	}
	// The POSITIVE half, and it is the one that matters, because the keys do not arrive through a
	// directive anyone would read as suspicious. Measured 2026-08-20 on debian:13: installing
	// `openssh-server` runs a postinst that generates ssh_host_{ecdsa,ed25519,rsa}_key into the
	// image layer, and `ssh-keygen -A` at start-up only creates keys that are MISSING — so without
	// this removal every container from one image presents one identity, silently.
	if removals != 1 {
		t.Errorf("the image removes the packaged host keys %d times, want once: installing openssh-server generates them into the layer, and the start-up's ssh-keygen -A will not replace what is already there",
			removals)
	}
	// A floor, because a matcher that read nothing is green for the wrong reason.
	if directives < 10 {
		t.Fatalf("read %d directives from the Dockerfile; it cannot be the real one", directives)
	}
}

// The removal above is in a file the generator does not write, so a second guard lives in the
// artefact it does write: the start-up refuses to run when a host key came from the image. A
// fleet that will not come up is recoverable; a fleet that is secretly one machine is not.
func TestTheStartUpRefusesAHostKeyThatCameFromTheImage(t *testing.T) {
	blocks := serviceBlocks(t, Generate(loadTopology(t, "../topology/basic.toml")).Compose)
	if len(blocks) < 5 {
		t.Fatalf("read %d services; the topology declares 5", len(blocks))
	}
	for id, body := range blocks {
		if !strings.Contains(body, "if [ -e /etc/ssh/ssh_host_ed25519_key ]; then") {
			t.Errorf("%s starts up without checking whether its host key came from the image:\n%s", id, body)
		}
	}
}

// Each row of spec §4.1 that `basic.toml` is required to carry, asserted on the loaded
// topology rather than on the file's bytes — a comment in the TOML is not a property.
func TestTheFirstTopologyCarriesEveryPropertyItIsRequiredTo(t *testing.T) {
	top := loadTopology(t, "../topology/basic.toml")

	byID := map[string]Machine{}
	for _, m := range top.Machines {
		byID[m.ID] = m
	}
	if len(byID) != 5 {
		t.Fatalf("the topology declares %d machines, want 5 (root, hop, twin-a, twin-b, leaf)", len(byID))
	}

	// Two aliases, one machine — identity merging, verifiable at hop 1.
	if got := byID["hop"].Aliases; len(got) != 2 {
		t.Errorf("hop carries %v; want two aliases so a merge has something to merge", got)
	}
	// Two machines, one hostname — four `web-ws` on the live tailnet.
	if a, b := byID["twin-a"].Hostname, byID["twin-b"].Hostname; a == "" || a != b {
		t.Errorf("the twins report hostnames %q and %q; want one hostname on two machines", a, b)
	}
	// A cycle, to prove the crawl terminates.
	var cycles bool
	for _, d := range byID["hop"].Declares {
		if d == "hop" {
			cycles = true
		}
	}
	if !cycles {
		t.Errorf("hop declares %v and none of them is itself; the crawl has no cycle to terminate on",
			byID["hop"].Declares)
	}
	// The measured non-POSIX login shell, present from the first topology.
	if byID["hop"].Shell != "nushell" {
		t.Errorf("hop's shell is %q; the rc=0 parse failure has nothing to happen on", byID["hop"].Shell)
	}
	// A hop that carries hub config, so ranking has an input.
	if !byID["hop"].Hub {
		t.Error("no machine carries hub config; a hop's hosts.toml has nothing to rank with")
	}
	// A machine with NO ssh config: `nuc` measured, so a hop may offer nothing.
	if got := byID["leaf"].Declares; len(got) != 0 {
		t.Errorf("leaf declares %v; the harness then has no machine that offers nothing", got)
	}
	// Two tmux versions, because every tmux claim in design.md is dual-version.
	versions := map[string]bool{}
	for _, m := range top.Machines {
		if m.Tmux != "" {
			versions[m.Tmux] = true
		}
	}
	if len(versions) < 2 {
		t.Errorf("the fleet runs %v; one version cannot carry a dual-version claim", versions)
	}
	// A machine off the root's network entirely: the whole point.
	root, leaf := byID["root"], byID["leaf"]
	for _, rn := range root.Networks {
		for _, ln := range leaf.Networks {
			if rn == ln {
				t.Errorf("root and leaf share %q; the root could reach the leaf directly", rn)
			}
		}
	}
	// `key = <owner>` per edge: the Ready / Blocked distinction.
	var keyed int
	for _, e := range top.Edges {
		if e.Key != "" {
			keyed++
		}
	}
	if keyed != 1 {
		t.Errorf("%d edges name a key; want exactly one, or Blocked has no cause", keyed)
	}
}

// The generated `hostname:` line is what makes two DIFFERENT machines answer to one label — four
// `web-ws` on the live tailnet. Nothing here read it: the topology case above asserts the loaded
// FIELD, and the shared network alias is written from a separate branch, so a generator that
// emitted `hostname: <id>` for every service dropped the whole collision and stayed green —
// measured, all 20 `ok` lines, and the container case that names the row passed too because it
// searched for the substring `twin` in `twin-a`.
func TestTheComposeGivesEachMachineItsDeclaredHostname(t *testing.T) {
	top := loadTopology(t, "../topology/basic.toml")
	blocks := serviceBlocks(t, Generate(top).Compose)

	var checked int
	shares := map[string][]string{}
	for _, m := range top.Machines {
		want := m.Hostname
		if want == "" {
			want = m.ID
		}
		got := serviceField(blocks[m.ID], "hostname: ")
		if got != want {
			t.Errorf("%s is given hostname %q; the topology declares %q", m.ID, got, want)
			continue
		}
		checked++
		shares[got] = append(shares[got], m.ID)
	}
	var collisions int
	for name, ids := range shares {
		if len(ids) < 2 {
			continue
		}
		collisions++
		for _, id := range ids {
			if name == id {
				t.Errorf("%v share hostname %q, which is also %s's own id — that is one machine named twice, not two machines under one label",
					ids, name, id)
			}
		}
	}
	if checked != len(top.Machines) {
		t.Fatalf("read %d hostnames for %d machines", checked, len(top.Machines))
	}
	// A floor in the other direction: the collision is the property this topology exists to carry,
	// so its absence is a fixture that no longer exercises the label-collision row at all.
	if collisions != 1 {
		t.Fatalf("%d hostnames are shared by more than one machine, want exactly 1", collisions)
	}
}

// serviceField picks a service's own top-level field out of its block. A whole-block `Contains`
// would be satisfied by the same text appearing inside that service's start-up script.
func serviceField(serviceBody, prefix string) string {
	for _, line := range strings.Split(serviceBody, "\n") {
		if rest, ok := strings.CutPrefix(line, "    "+prefix); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func TestEveryMachineIsAServiceWithADistinctAddress(t *testing.T) {
	top := loadTopology(t, "../topology/basic.toml")
	out := Generate(top)

	var wantAddresses int
	for _, m := range top.Machines {
		if !strings.Contains(out.Compose, "\n  "+m.ID+":\n") {
			t.Errorf("machine %q has no service in the compose file", m.ID)
		}
		wantAddresses += len(m.Networks)
	}

	seen := map[string]string{}
	var count int
	for _, line := range strings.Split(out.Compose, "\n") {
		_, addr, ok := strings.Cut(strings.TrimSpace(line), "ipv4_address: ")
		if !ok {
			continue
		}
		count++
		if prev, dup := seen[addr]; dup {
			t.Errorf("%s is assigned twice (already %s) — two machines at one address", addr, prev)
		}
		seen[addr] = addr
	}
	if count != wantAddresses {
		t.Fatalf("the compose pins %d addresses, want %d — one per machine per network",
			count, wantAddresses)
	}
}

// Compose interpolates `$VAR` in a command string before the container sees it, so a shell
// variable written plainly arrives EMPTY. Measured on Compose 5.1.4: the generated
// `tc qdisc replace dev "$iface" …` came out of `docker compose config` as `dev ""`, which is
// the delay silently not applied — and `config` answered rc=0, so no gate would have said so.
// Every `$` the generator writes must therefore be doubled.
func TestEveryShellVariableInTheComposeIsEscapedFromCompose(t *testing.T) {
	composeText := Generate(loadTopology(t, "../topology/basic.toml")).Compose

	var pairs int
	for i := 0; i < len(composeText); i++ {
		if composeText[i] != '$' {
			continue
		}
		if i+1 < len(composeText) && composeText[i+1] == '$' {
			pairs++
			i++
			continue
		}
		line := composeText[strings.LastIndex(composeText[:i], "\n")+1:]
		if end := strings.Index(line, "\n"); end >= 0 {
			line = line[:end]
		}
		t.Errorf("a lone $ survives into the compose file, so compose will replace it with nothing: %q",
			strings.TrimSpace(line))
	}
	// A floor: a compose file with no `$` at all would satisfy the loop above having proved
	// nothing, and it is exactly what a generator that dropped the whole tc block would produce.
	if pairs < 4 {
		t.Fatalf("only %d escaped variables in the compose file; the interface lookup is missing", pairs)
	}
}

// tc refuses without the capability, so a service told to apply a delay and not given
// NET_ADMIN dies at start-up. The two must come from one decision: every service carrying a
// qdisc carries the capability, and no other service carries it.
func TestTheCapabilityLandsExactlyWhereAQdiscDoes(t *testing.T) {
	blocks := serviceBlocks(t, Generate(loadTopology(t, "../topology/basic.toml")).Compose)
	for id, body := range blocks {
		qdisc := strings.Contains(body, "tc qdisc replace ")
		capable := strings.Contains(body, "NET_ADMIN")
		if qdisc != capable {
			t.Errorf("%s: qdisc=%v NET_ADMIN=%v — a delay without the capability dies at start-up, and the capability without a delay is a right nobody asked for",
				id, qdisc, capable)
		}
	}
}

// A delay is realised as netem on ONE interface, so it must land on exactly the machines an
// edge points at. A blanket qdisc on every container would satisfy the plan's check above
// and would make the latency policy untestable in the other direction.
func TestOnlyTheMachinesAnEdgePointsAtCarryAQdisc(t *testing.T) {
	blocks := serviceBlocks(t, Generate(loadTopology(t, "../topology/basic.toml")).Compose)

	carries := map[string]int{}
	for id, body := range blocks {
		// Anchored on the line that ACTS, not on the word: the generated start-up explains
		// itself in a comment beside the command, and a substring match on `netem delay`
		// counted the prose as a second qdisc. An assertion that matches its own rationale is
		// this repository's recorded false-alarm shape.
		carries[id] = strings.Count(body, "tc qdisc replace ")
	}
	want := map[string]int{"hop": 1, "leaf": 1}
	for id, n := range carries {
		if want[id] != n {
			t.Errorf("%s carries %d qdiscs, want %d", id, n, want[id])
		}
	}
	for id, n := range want {
		if carries[id] != n {
			t.Errorf("%s carries %d qdiscs, want %d — its edge's delay is not in force", id, carries[id], n)
		}
	}
}

// Two edges into one machine over one network would both be applied to that machine's single
// interface, so the later one would silently win. Refuse instead of mis-applying.
func TestTwoDelaysOnOneInterfaceAreRefused(t *testing.T) {
	refused(t, `
[fleet]
name = "clash"

[[machine]]
id = "a"
networks = ["n"]

[[machine]]
id = "b"
networks = ["n"]

[[machine]]
id = "c"
networks = ["n"]

[[edge]]
from = "a"
to = "c"
delay = "5ms"

[[edge]]
from = "b"
to = "c"
delay = "50ms"
`, "one interface")
}

// The mirror of the case above, and it was accepted until 2026-08-20: a qdisc holds back
// everything that leaves an interface, so an edge sharing that interface is delayed whether it
// declares a delay or not. Measured on the accepted version — `a -> c delay=180ms` plus a bare
// `b -> c` on one network gave `c` one 180 ms qdisc, and b's traffic from c came back 180 ms late
// while `b -> c` declared nothing.
func TestAnUndelayedEdgeSharingADelayedInterfaceIsRefused(t *testing.T) {
	const shape = `
[fleet]
name = "leak"

[[machine]]
id = "a"
networks = ["n"]

[[machine]]
id = "b"
networks = ["n"]

[[machine]]
id = "c"
networks = ["n"]

[[edge]]
%s

[[edge]]
%s
`
	delayed := "from = \"a\"\nto = \"c\"\ndelay = \"180ms\""
	bare := "from = \"b\"\nto = \"c\""
	// BOTH orders, because the affected edge may come first in the file and a one-pass check sees
	// only the order it was written in.
	refused(t, fmt.Sprintf(shape, delayed, bare), "in this edge's path")
	refused(t, fmt.Sprintf(shape, bare, delayed), "in this edge's path")

	// The NEAR end too: an edge whose own requests leave through somebody else's qdisc carries a
	// figure it does not declare just as surely as one whose replies do.
	refused(t, fmt.Sprintf(shape,
		"from = \"a\"\nto = \"b\"\ndelay = \"180ms\"",
		"from = \"b\"\nto = \"c\"\ndelay = \"5ms\""), "in this edge's path")

	// And the CONTROL, or the refusal above is indistinguishable from a check that refuses
	// everything: two delays that do not share an interface are accepted, which is the shape
	// `basic.toml` itself is.
	if _, err := Load(`
[fleet]
name = "apart"

[[machine]]
id = "a"
networks = ["x"]

[[machine]]
id = "b"
networks = ["x", "y"]

[[machine]]
id = "c"
networks = ["y"]

[[edge]]
from = "a"
to = "b"
delay = "5ms"

[[edge]]
from = "b"
to = "c"
delay = "180ms"
`); err != nil {
		t.Errorf("two delays on separate networks were refused: %v — that is basic.toml's own shape", err)
	}
}

func TestAnEdgeBetweenMachinesThatShareNoNetworkIsRefused(t *testing.T) {
	refused(t, `
[fleet]
name = "apart"

[[machine]]
id = "a"
networks = ["n"]

[[machine]]
id = "b"
networks = ["m"]

[[edge]]
from = "a"
to = "b"
delay = "5ms"
`, "share no network")
}

func TestAnEdgeNamingAnUnknownMachineIsRefused(t *testing.T) {
	refused(t, `
[fleet]
name = "typo"

[[machine]]
id = "a"
networks = ["n"]

[[edge]]
from = "a"
to = "bb"
`, "bb")
}

func TestAnUnknownKeyIsRefused(t *testing.T) {
	refused(t, `
[fleet]
name = "future"

[[machine]]
id = "a"
networks = ["n"]
latency = "5ms"
`, "latency")
}

func TestADeclarationNamingNoMachineIsRefused(t *testing.T) {
	refused(t, `
[fleet]
name = "dangling"

[[machine]]
id = "a"
networks = ["n"]
declares = ["ghost"]
`, "ghost")
}

// The `Blocked` diagnosis rests entirely on this file pair: the hop holds a key the root does
// not, and the leaf authorises only that key. If the root's key reached the leaf the state
// would be `Ready`, and nothing downstream could tell the difference.
func TestTheLeafAuthorisesOnlyTheKeyTheEdgeNames(t *testing.T) {
	out := Generate(loadTopology(t, "../topology/basic.toml"))
	blocks := serviceBlocks(t, out.Compose)

	if !strings.Contains(blocks["leaf"], "keys/hop-only.pub >> ") {
		t.Errorf("the leaf does not authorise hop-only, so nothing can reach it:\n%s", blocks["leaf"])
	}
	if strings.Contains(blocks["leaf"], "keys/root.pub >> ") {
		t.Error("the leaf authorises the ROOT's key, so it would verify and never be Blocked")
	}
	if !strings.Contains(blocks["hop"], "cp /harness/keys/hop-only ") {
		t.Errorf("the hop does not install hop-only, so no observer can reach the leaf at all:\n%s", blocks["hop"])
	}
	if strings.Contains(blocks["root"], "keys/hop-only ") {
		t.Error("the ROOT installs hop-only — the very credential the diagnosis says it lacks")
	}
	// The keypair still has to be asked for, or every copy above is of a file that is not there.
	if !containsString(out.Keys, "hop-only") {
		t.Errorf("the generator asks for keypairs %v and hop-only is not among them", out.Keys)
	}
}

// A syntax error in the generated start-up is five containers dying at once, and nothing else
// here would find it: the generator writes shell as strings, `docker compose config` parses YAML
// and not shell, and the first reader of the script is `/bin/sh` inside a container. `sh -n`
// parses without executing, which is exactly the check that is missing.
func TestEveryGeneratedStartUpIsValidShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no POSIX sh to parse the generated scripts with: %v", err)
	}
	blocks := serviceBlocks(t, Generate(loadTopology(t, "../topology/basic.toml")).Compose)
	dir := t.TempDir()
	var parsed int
	for id, body := range blocks {
		script := startupScript(body)
		if script == "" {
			t.Errorf("%s has no start-up script at all:\n%s", id, body)
			continue
		}
		path := filepath.Join(dir, id+".sh")
		if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		if out, err := exec.Command(sh, "-n", path).CombinedOutput(); err != nil {
			t.Errorf("%s's start-up is not valid shell: %v: %s\n%s", id, err, out, script)
			continue
		}
		parsed++
	}
	if parsed < 5 {
		t.Fatalf("parsed %d start-up scripts, want one per machine (5)", parsed)
	}
}

// startupScript recovers the shell a container will actually receive: the lines of the block
// scalar, with compose's `$$` escape undone, which is precisely the transformation compose
// performs on the way in.
func startupScript(serviceBody string) string {
	_, after, ok := strings.Cut(serviceBody, "      - |\n")
	if !ok {
		return ""
	}
	var lines []string
	for _, line := range strings.Split(after, "\n") {
		if !strings.HasPrefix(line, "        ") {
			break
		}
		lines = append(lines, strings.ReplaceAll(line[8:], "$$", "$"))
	}
	return strings.Join(lines, "\n") + "\n"
}

// serviceBlocks cuts the compose into one text per service, so an assertion about a machine
// cannot be satisfied by a different machine's line. A whole-file `Contains` is what this
// repository already paid for twice: a screen holds a name twice and the check cannot say which
// surface carried it.
func serviceBlocks(t *testing.T, composeText string) map[string]string {
	t.Helper()
	_, body, ok := strings.Cut(composeText, "\nservices:\n")
	if !ok {
		t.Fatalf("the compose file has no services section:\n%s", composeText)
	}
	blocks := map[string]string{}
	var current string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") && strings.HasSuffix(line, ":") {
			current = strings.TrimSuffix(strings.TrimSpace(line), ":")
			continue
		}
		blocks[current] += line + "\n"
	}
	if len(blocks) < 2 {
		t.Fatalf("read %d services from the compose file; it cannot be the real one", len(blocks))
	}
	return blocks
}

func containsString(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// This stanza is what Task 4 reads over the hop and Task 5 diagnoses. Its shape is the
// contract between the harness and both of them.
func TestTheHopsSSHConfigNamesTheLeafAndTheKeyOnlyItHolds(t *testing.T) {
	out := Generate(loadTopology(t, "../topology/basic.toml"))

	cfg, ok := out.Files["ssh/hop/config"]
	if !ok {
		t.Fatalf("the hop has no ssh config; generated %v", sortedKeys(out.Files))
	}
	for _, want := range []string{"Host leaf", "IdentityFile ~/.ssh/hop-only"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("the hop's ssh config does not carry %q:\n%s", want, cfg)
		}
	}
}

// `nuc` has no ~/.ssh/config, measured, so a hop may offer nothing. An EMPTY config is a
// different thing from an absent one — a reader that treats them alike cannot report which.
func TestAMachineThatDeclaresNothingGetsNoSSHConfigAtAll(t *testing.T) {
	out := Generate(loadTopology(t, "../topology/basic.toml"))
	if body, ok := out.Files["ssh/leaf/config"]; ok {
		t.Errorf("the leaf was given an ssh config it does not declare: %q", body)
	}
}

// The fake `claude` replays JSON captured from the real CLI on 2026-08-20. The reduction to
// two sessions must keep the one distinction this repository has paid for twice: a record
// carries `pid` and `status` exactly when the host that answered can see a worker.
func TestTheFakeClaudeReplaysTwoCapturedSessionsThatDifferInPresence(t *testing.T) {
	raw, err := os.ReadFile("../image/claude-agents.json")
	if err != nil {
		t.Fatalf("read the captured fixture: %v", err)
	}
	var records []map[string]any
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatalf("the captured fixture is not the CLI's own array shape: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("the fixture holds %d sessions, want 2", len(records))
	}
	live, done := records[0], records[1]
	for _, field := range []string{"pid", "status", "state", "sessionId", "id", "cwd", "kind", "name", "startedAt"} {
		if _, ok := live[field]; !ok {
			t.Errorf("the live session lost %q from the capture", field)
		}
	}
	for _, absent := range []string{"pid", "status"} {
		if _, ok := done[absent]; ok {
			t.Errorf("the finished session carries %q; the presence distinction is gone", absent)
		}
	}
	if got := done["state"]; got != "done" {
		t.Errorf("the finished session's state is %v, want done", got)
	}
}

// The start-up copies the private key AND appends the public one, so a `keys/` directory holding
// only one half is a container that dies under `set -e` for a reason no message names. Reuse
// therefore has to be decided from BOTH halves: ssh-keygen writes two files and a run interrupted
// between them leaves one, which no amount of re-running the generator repaired.
func TestARegenerationRepairsAKeypairThatLostItsPublicHalf(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skipf("no ssh-keygen to make a keypair with: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "keys")

	if made, err := keypairs(dir, []string{"solo"}); err != nil || made != 1 {
		t.Fatalf("the first pass made %d keypairs (err %v), want 1", made, err)
	}
	// A whole pair is REUSED — the property that keeps a running container's authorized_keys valid
	// across a regeneration, and the control that says the repair below is not just "always remake".
	before, err := os.ReadFile(filepath.Join(dir, "solo"))
	if err != nil {
		t.Fatalf("read the private half: %v", err)
	}
	if made, err := keypairs(dir, []string{"solo"}); err != nil || made != 0 {
		t.Fatalf("the second pass made %d keypairs (err %v), want 0 — a fresh key every run is a different fleet every run", made, err)
	}
	if again, _ := os.ReadFile(filepath.Join(dir, "solo")); string(again) != string(before) {
		t.Error("the private key changed although both halves were present")
	}

	// Now the half-pair, which is the case with no message.
	if err := os.Remove(filepath.Join(dir, "solo.pub")); err != nil {
		t.Fatalf("remove the public half: %v", err)
	}
	made, err := keypairs(dir, []string{"solo"})
	if err != nil {
		t.Fatalf("regenerating over a half-pair: %v", err)
	}
	if made != 1 {
		t.Errorf("the pass over a half-pair made %d keypairs, want 1 — the container's start-up would die at `cat /harness/keys/solo.pub`", made)
	}
	for _, half := range []string{"solo", "solo.pub"} {
		if _, err := os.Stat(filepath.Join(dir, half)); err != nil {
			t.Errorf("%s is still missing after a regeneration: %v", half, err)
		}
	}
}

// 86 lines of shell shipped into every container, and nothing ran them: the only reader of
// anything under `image/` was the fixture case above, which reads the JSON as data. A syntax error
// or a jq typo in that file is five containers with a broken `claude` and no gate that says so —
// and the instrument was already here, since `TestEveryGeneratedStartUpIsValidShell` does exactly
// this for the GENERATED start-ups and was never pointed at the shipped one.
//
// `HARNESS_CLAUDE_FIXTURE` is the hook that makes it runnable out of a container. The `attach
// <known id>` arm is deliberately not exercised: on success it `exec sleep 86400`, which is the
// point of it (a door pane that exits destroys its own session inside 200 ms), so only its refusal
// is testable in process.
func TestTheFakeClaudeAnswersTheInvocationsTheHubMakes(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no POSIX sh to run the shipped script with: %v", err)
	}
	// The script is READ HERE and run from a copy, and that is not tidiness. `go test` caches a
	// package's result and invalidates it from the files the TEST PROCESS opened — a subprocess's
	// reads are invisible to it. Measured: with `sh -n ../image/claude` the planted syntax error
	// was reported as `ok (cached)`, because nothing in this package had ever opened that file,
	// and only `-count=1` caught it. `os.ReadFile` registers the dependency, so an edit to the
	// shipped script now invalidates this package's cached result.
	script, err := os.ReadFile("../image/claude")
	if err != nil {
		t.Fatalf("read the shipped claude: %v", err)
	}
	if len(script) < 500 {
		t.Fatalf("the shipped claude is %d bytes; it cannot be the real one", len(script))
	}
	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, script, 0o755); err != nil {
		t.Fatalf("stage the shipped claude: %v", err)
	}

	// The syntax check must always run — it needs only `sh`, not `jq`. Separating them means a
	// machine without jq still catches syntax errors in the 86-line script.
	if out, err := exec.Command(sh, "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("the shipped claude is not valid shell: %v: %s", err, out)
	}

	// The jq-dependent assertions may skip, but the syntax check above must not.
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skipf("the shipped claude needs jq to answer --json, skipping functional tests: %v", err)
	}

	fixture, err := filepath.Abs("../image/claude-agents.json")
	if err != nil {
		t.Fatalf("locate the capture: %v", err)
	}
	run := func(argv ...string) (string, int) {
		cmd := exec.Command(sh, append([]string{path}, argv...)...)
		cmd.Env = append(os.Environ(), "HARNESS_CLAUDE_FIXTURE="+fixture)
		out, err := cmd.CombinedOutput()
		rc := 0
		if err != nil {
			rc = cmd.ProcessState.ExitCode()
		}
		return string(out), rc
	}
	records := func(t *testing.T, out string) []map[string]any {
		t.Helper()
		var got []map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("the answer is not the CLI's own array shape: %v: %s", err, out)
		}
		return got
	}

	// `--all` is not optional: the measured difference is that bare `--json` omits every session
	// that ENDED with no worker left, and this branch is what lets `internal/agents`' one-line
	// claim about it be tested rather than believed.
	all, rc := run("agents", "--json", "--all")
	if rc != 0 {
		t.Fatalf("agents --json --all answered rc=%d: %s", rc, all)
	}
	if got := records(t, all); len(got) != 2 {
		t.Errorf("--all replayed %d records, want the capture's 2", len(got))
	}
	bare, rc := run("agents", "--json")
	if rc != 0 {
		t.Fatalf("agents --json answered rc=%d: %s", rc, bare)
	}
	kept := records(t, bare)
	if len(kept) != 1 {
		t.Fatalf("bare --json kept %d records, want 1 — the terminal-with-no-worker row must be dropped, or --all is indistinguishable from it: %s",
			len(kept), bare)
	}
	// And the survivor is the LIVE one, not just any one: a filter that dropped the wrong record
	// would satisfy the count.
	if _, ok := kept[0]["pid"]; !ok {
		t.Errorf("bare --json kept %v, which carries no pid — it dropped the live session instead of the finished one",
			kept[0])
	}

	if out, rc := run("--version"); rc != 0 || !strings.Contains(out, "Claude Code") {
		t.Errorf("--version answered rc=%d %q; a consumer reading the front-end's version gets nothing", rc, out)
	}

	// Every refusal names the fix rather than only the breakage, and each is a separate branch of
	// the script, so an unreachable one is a branch nobody has run.
	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{"no verb at all", nil, "agents --json --all"},
		{"agents without --json", []string{"agents"}, "only speaks --json"},
		{"an agents flag it does not know", []string{"agents", "--json", "--wat"}, "--wat"},
		{"a verb it does not know", []string{"logs"}, "'logs'"},
		{"attach with no id", []string{"attach"}, "attach needs a session id"},
		// The real CLI's own sentence, measured on 2.1.224 and 2.1.233 alike. It is the only text a
		// caller can match on, so it is quoted rather than paraphrased.
		{"attach an id nobody has", []string{"attach", "no-such-session"},
			"No job matching 'no-such-session'. Run 'claude agents' to list running sessions."},
	} {
		out, rc := run(tc.argv...)
		if rc == 0 {
			t.Errorf("%s was ACCEPTED (rc=0): %q", tc.name, out)
			continue
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("%s was refused with %q, which does not name %q", tc.name, strings.TrimSpace(out), tc.want)
		}
	}
}

// `fleet.json` is how a consumer that cannot import this command — the self-test, and later the
// oracle — learns what was built. It has no other reader here, so without this test it could go
// missing and only a container run would notice.
func TestTheGeneratorPublishesWhatItBuilt(t *testing.T) {
	top := loadTopology(t, "../topology/basic.toml")
	out := Generate(top)

	raw, ok := out.Files["fleet.json"]
	if !ok {
		t.Fatalf("nothing published; generated %v", sortedKeys(out.Files))
	}
	var back Topology
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("what was published does not read back: %v", err)
	}
	if back.Name != top.Name || len(back.Machines) != len(top.Machines) || len(back.Edges) != len(top.Edges) {
		t.Fatalf("published %q with %d machines and %d edges; declared %q with %d and %d",
			back.Name, len(back.Machines), len(back.Edges),
			top.Name, len(top.Machines), len(top.Edges))
	}
	// The fields the self-test actually reads, since a round trip that lost one of them would
	// leave it choosing the wrong vantage and reporting that as a product failure.
	for i, m := range back.Machines {
		want := top.Machines[i]
		if m.ID != want.ID || m.Shell != want.Shell || len(m.Networks) != len(want.Networks) ||
			len(m.Declares) != len(want.Declares) || len(m.Aliases) != len(want.Aliases) {
			t.Errorf("machine %d published as %+v, declared as %+v", i, m, want)
		}
	}
}

// sortedKeys feeds two failure messages, so it has to SORT: Go's map iteration is randomised, and
// a message whose word order changes between runs is a message nobody can grep for — which is the
// property `topology.go`'s own `keysOf` documents itself as existing to avoid.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
