package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/hostset"
)

// This file is about the crawl's TRANSPORT, and it exists because five comments across three packages
// asserted a property the argv did not have: that looking behind a hop travels over the ssh master the
// hub already holds for it. Nothing named a control path, so it did not — and measured on a mounted
// host, `echo HI` cost 2458 ms bare (one `Server host key` line, one `Authenticated to`) against
// 319 ms with `-S <the hub's control path>` (no host key, `mux_client_request_session`). A comment that
// is wrong is worse than none, because it stops the next reader checking; here it also carried an
// ARGUMENT — "the host already has a master, so no ConnectTimeout is needed" — that removed the one
// flag bounding a hop that has gone dark.
//
// Every expectation below is a LITERAL. A test that spelled the timeout as SSHConnectTimeout or the
// control path as hub.ControlPathFor would pass against a mutant that blanked either, which is the
// entire defect it exists to catch.

// crawlArgv runs the production RemoteRunner against an `ssh` shim on PATH and returns the argv the
// process was really spawned with, minus argv[0].
//
// It goes through the runner rather than reading remoteArgs, because the runner is where the control
// path is DERIVED and the derivation is half of what is under test. The shim helper is the one
// socketoverride_test.go built for the polling path — those two cases and these are the only
// assertions in this repository that read an argv off a real spawned process.
func crawlArgv(t *testing.T, runtimeDir, hop, payload string) []string {
	t.Helper()
	record := sshArgvShim(t)
	out, errOut, rc := sshRemoteVia(runtimeDir)(context.Background(), hop, payload)
	if rc != 0 {
		t.Fatalf("the shim exited %d (stdout %q, stderr %q)", rc, out, errOut)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the shim recorded no argv, so nothing was spawned: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// The whole argv, exactly, for a hop whose master is addressable.
//
// Exact and not a substring check, for the reason the socket-override cases give: `-S` in the wrong
// position, or a payload that stopped being the last positional, are both changes a `Contains` cannot
// see. The payload is the real one — hostset asks a hop for its own ssh config — and it stays a single
// positional argument because the far side hands it to a LOGIN shell, which on one host in this fleet
// is Nushell, where a quoted program name is a parse error at rc=0.
func TestTheCrawlNamesItsHopsControlPathAndBoundsItsConnect(t *testing.T) {
	got := remoteArgs("/run/user/1000/tmux-hub/cm-f6032288-nuc", "nuc", "cat ~/.ssh/config")
	assertSpawnedArgv(t, got, []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=6",
		"-S", "/run/user/1000/tmux-hub/cm-f6032288-nuc",
		"nuc", "cat ~/.ssh/config",
	})
}

// The derivation, asserted as an AGREEMENT between two production paths rather than against a value
// taken from the function that computes it.
//
// The crawl's hop is an alias out of hosts.toml, and hostsFor builds that host's ControlPath from the
// same two values — the runtime directory and the alias, which serves as label and ssh destination
// alike. If the crawl derives it any other way it names a socket nothing listens on, `-S` falls back to
// a full handshake at rc=0 (measured: an absent path 2013 ms, a stale one 2018 ms, one `Server host
// key` each), and the flag is then present, silent and worthless — the failure this test exists to make
// loud. The basename is also pinned as a LITERAL, captured from the operator's live runtime directory
// where a real master was listening at `cm-f6032288-nuc` for the alias `nuc`.
func TestTheCrawlsControlPathIsTheOneItsHopsMasterListensOn(t *testing.T) {
	rt := runtimeDir(t)
	hosts, err := hostsFor([]hostset.Entry{{Alias: "nuc", Enabled: true}}, nil)
	if err != nil {
		t.Fatalf("hostsFor: %v", err)
	}
	if len(hosts) != 1 || hosts[0].ControlPath == "" {
		t.Fatalf("the fixture is not one host with a master: %+v", hosts)
	}

	argv := crawlArgv(t, rt, "nuc", "cat ~/.ssh/config")
	at := -1
	for i, a := range argv {
		if a == "-S" {
			at = i
		}
	}
	if at < 0 || at+1 >= len(argv) {
		t.Fatalf("the crawl spawned %q, which names no control path — it cannot be riding the "+
			"master, whatever the comments say", argv)
	}
	if got := argv[at+1]; got != hosts[0].ControlPath {
		t.Errorf("the crawl connects to %q while the hop's master listens at %q — two spellings of "+
			"one control path mean the crawl handshakes afresh and the flag proves nothing",
			got, hosts[0].ControlPath)
	}
	// The NAME's shape, not a measured hash of it. This assertion used to demand the literal
	// `cm-f6032288-nuc`, taken from a live master on this machine — and that is a hash of the
	// operator's own runtime directory and host name, so it is two defects in one: it cannot survive
	// the publication rename (measured: it fails in the sanitised copy, where the same host resolves
	// to a different digest), and a contributor on any other machine would read a red gate for a
	// reason that has nothing to do with their change. The property worth pinning is the FORMAT —
	// `cm-<8 hex>-<alias>` — because the digest is what keeps two hosts' sockets apart and the alias
	// is what makes the file readable to a person; the assertion that the crawl and the master agree
	// on the whole path is above, and that is the one that catches a real divergence.
	base := filepath.Base(argv[at+1])
	if !regexp.MustCompile(`^cm-[0-9a-f]{8}-nuc$`).MatchString(base) {
		t.Errorf("the control socket is named %q, which is not `cm-<8 hex>-<alias>` — the digest is "+
			"what keeps two hosts apart and the alias is what makes the path readable", base)
	}
}

// The bound must hold on the path where it is actually needed, which is the one the master does NOT
// cover: with the socket absent or stale, ssh opens a real TCP connection at rc=0 rather than
// refusing, so a black-holed hop costs the kernel's whole SYN-retry window. Measured 2026-08-20
// against a black-holed address, 134.1 s bare against 6.0 s with the flag — and the crawl's whole
// round budget is 90 s, shared by every hop, so one dark hop otherwise starves the rest of the fleet.
func TestTheCrawlBoundsItsConnectWithOrWithoutAMaster(t *testing.T) {
	for _, c := range []struct{ name, controlPath string }{
		{"with a master to ride", "/run/user/1000/tmux-hub/cm-f6032288-nuc"},
		{"with none", ""},
	} {
		argv := strings.Join(remoteArgs(c.controlPath, "nuc", "cat ~/.ssh/config"), " ")
		if !strings.Contains(argv, "-o ConnectTimeout=6") {
			t.Errorf("%s: the argv is %q — nothing bounds the connect, so a hop that has gone dark "+
				"spends longer than the crawl's whole round on its own", c.name, argv)
		}
		// BatchMode is the other half and must not regress: without it a round trip can sit forever
		// on a password prompt nobody can see, which no timeout on the CONNECT would bound.
		if !strings.Contains(argv, "-o BatchMode=yes") {
			t.Errorf("%s: the argv is %q — a crawl that can be asked for a password hangs on a "+
				"prompt no screen shows", c.name, argv)
		}
		// No `-v`, ever: this connection harvests no identity, and its transcript would land in a
		// reason the operator reads.
		if strings.Contains(argv, " -v ") {
			t.Errorf("%s: the argv is %q — the verbose transcript ends up in a row's reason", c.name, argv)
		}
	}
}

// A hub with no runtime directory names NO control path, rather than a relative one.
//
// `hub.ControlPathFor("", alias)` is `cm-<hex>-<alias>` with no directory, and ssh would look for that
// master in whatever directory the hub was started from — a path the operator did not choose and
// another program could occupy. The crawl still runs there: it pays the handshake `-S` would have
// saved, which is the old behaviour plus the timeout, and not a refusal.
func TestACrawlWithNoRuntimeDirectoryNamesNoControlPathAtAll(t *testing.T) {
	argv := crawlArgv(t, "", "nuc", "cat ~/.ssh/config")
	for i, a := range argv {
		if a == "-S" {
			t.Fatalf("argv[%d] is `-S` with no runtime directory to derive a path from: %q", i, argv)
		}
		if strings.HasPrefix(a, "cm-") {
			t.Fatalf("argv[%d] is the relative control path %q — ssh would hunt for a master in the "+
				"working directory", i, a)
		}
	}
	assertSpawnedArgv(t, argv, []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=6",
		"nuc", "cat ~/.ssh/config",
	})
}

// `--version` had lived in the published mirror alone, which is why it gets a test as it lands here:
// a flag with no test is a flag the next refactor removes silently, and this one is the first question
// any bug report needs answered.
//
// The three install routes cannot all be exercised from a unit test — `go install …@v0.1.0` applies no
// ldflags at all, which is the case `versionString` exists for — so what is asserted is the FALLBACK
// LADDER, which is the part that was wrong before it existed: a stamped version wins, and an unstamped
// build must not answer `dev` when the toolchain knows better.
func TestTheVersionAnswersFromTheStampFirstAndTheBuildInfoSecond(t *testing.T) {
	originalVersion, originalReader := version, readBuildInfo
	t.Cleanup(func() { version, readBuildInfo = originalVersion, originalReader })

	fromToolchain := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			if v == "" {
				return nil, false
			}
			return &debug.BuildInfo{Main: debug.Module{Version: v}}, true
		}
	}

	// Four rungs, and the toolchain's answer is INJECTED because the environment cannot supply them:
	// under `go test` the real record carries no module version, so the first cut of this test could
	// not fail at all — a mutant that walked the rung and discarded its answer PASSED it, which is
	// how the seam came to exist.
	cases := []struct {
		name, stamp, toolchain, want string
	}{
		{"a stamped build wins outright, whatever the toolchain says", "v9.9.9", "v0.0.1", "v9.9.9"},
		{"unstamped, the toolchain's record answers — the `go install ...@v0.1.0` route applies no " +
			"ldflags and used to report `dev`, the one answer nobody can act on", "dev", "v0.1.0", "v0.1.0"},
		{"a `(devel)` record is no information, so `dev` stands", "dev", "(devel)", "dev"},
		{"no build info at all, and `dev` stands", "dev", "", "dev"},
	}
	for _, c := range cases {
		version, readBuildInfo = c.stamp, fromToolchain(c.toolchain)
		if got := versionString(); got != c.want {
			t.Errorf("%s: stamp %q + toolchain %q answered %q, want %q",
				c.name, c.stamp, c.toolchain, got, c.want)
		}
	}
}
