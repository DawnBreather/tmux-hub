package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/hostset"
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

func TestHostFlagParsing(t *testing.T) {
	var h hostFlags
	if err := h.Set("nuc=/run/s.sock,ssh=nuc,ctl=/run/s.ctl"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got := h[0]
	if got.Label != "nuc" || got.Socket != "/run/s.sock" ||
		got.SSHDest != "nuc" || got.ControlPath != "/run/s.ctl" {
		t.Fatalf("parsed %+v", got)
	}
	if !got.Remote() {
		t.Error("a host with ssh= must read as remote")
	}

	var local hostFlags
	if err := local.Set("local=/tmp/x.sock"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if local[0].Remote() {
		t.Error("a host with no ssh= must not read as remote")
	}

	for _, bad := range []string{
		"", "nuc", "=/run/s.sock", "nuc=", "nuc=/s.sock,bogus=1",
		"nuc=/s.sock,ssh=", "nuc=/s.sock,ctl=/c.sock",
	} {
		var f hostFlags
		if err := f.Set(bad); err == nil {
			t.Errorf("Set(%q) accepted, want an error", bad)
		}
	}
}

// `local` marks a socket as this machine's own server, which is what turns the
// identity walk on for it — and without a way to say so, a local server on a
// non-default socket was permanently read-only. Host.IsLocalServer's comment has
// promised this flag since it was written ("a `--host` entry sets it only when the
// user marks the socket as local") and the parser had no such key, so the
// commonest first thing a person does — point the hub at a scratch server and try
// the write path where it cannot hurt anything — was impossible.
//
// It is a BARE key, not key=value: there is no value that could mean "somewhat
// local", and `local=false` would invite one.
func TestHostFlagLocalMarksTheSocketAsThisMachine(t *testing.T) {
	var h hostFlags
	if err := h.Set("sandbox=/tmp/hub-sandbox.sock,local"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !h[0].IsLocalServer() {
		t.Error("`local` must set LocalProc, or the identity walk stays off and the host is read-only")
	}
	if h[0].Remote() {
		t.Error("a host marked local must not read as remote")
	}

	// Without it, nothing changes: unknown means unknown means read-only.
	var plain hostFlags
	if err := plain.Set("sandbox=/tmp/hub-sandbox.sock"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if plain[0].IsLocalServer() {
		t.Error("a socket with no `local` must stay unknown — the guard is opt-in by design")
	}

	// `ssh=` and `local` together is a contradiction, and it is exactly the hole
	// IsLocalServer exists to close: a forwarded socket handed over as a local one
	// let the identity walk answer from THIS machine's process table using REMOTE
	// pane pids. Refuse it rather than pick a winner.
	for _, bad := range []string{
		"nuc=/run/s.sock,ssh=nuc,local",
		"nuc=/run/s.sock,local,ssh=nuc",
		"sandbox=/tmp/x.sock,local=yes",
	} {
		var f hostFlags
		if err := f.Set(bad); err == nil {
			t.Errorf("Set(%q) accepted, want an error", bad)
		}
	}
}

// runtimeDir gives the test its own XDG_RUNTIME_DIR and answers with `$RT` — the hub's
// own directory under it, which is what every control path is built from. hostsFrom
// reads the environment because a host from the file is addressed by a control path and
// nothing else, and taking the ambient one would make the answer depend on the machine.
//
// `$RT` is asked of hub.RuntimeDir rather than joined here, so this helper cannot
// disagree with the program about where the sockets are. That does not weaken anything:
// the JOIN is pinned by a literal in internal/hub's own TestTheRuntimeDirIsTheHubsOwn…,
// which is where a mutation that drops the subdirectory goes red.
func runtimeDir(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	rt, err := hub.RuntimeDir()
	if err != nil {
		t.Fatalf("RuntimeDir: %v", err)
	}
	return rt
}

// The point of the whole plan: hosts come from the file, and --host is the escape
// hatch rather than the interface.
func TestHostsComeFromTheFileWhenNoFlagIsGiven(t *testing.T) {
	rt := runtimeDir(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts.toml")
	if err := hostset.SaveHosts(path, []hostset.Entry{
		{Alias: "nuc", Enabled: true},
		{Alias: "eu", Enabled: false},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := hostsFrom(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Label != "nuc" {
		t.Fatalf("got %+v, want only the enabled host", got)
	}
	if got[0].SSHDest != "nuc" || got[0].ControlPath == "" {
		t.Errorf("an enabled host must carry what the transport needs: %+v", got[0])
	}
	if got[0].Socket != "" {
		t.Error("a remote host has no socket in this design — that was the forward (§5)")
	}
	// The control path is the one the master lifecycle will address, or the hub adopts
	// nothing and spawns a second master beside the one it already has.
	if want := hub.ControlPathFor(rt, "nuc"); got[0].ControlPath != want {
		t.Errorf("ControlPath = %q, want %q — Ensure, Stop and the startup reconcile all "+
			"derive it the same way, so a different answer here adopts nothing",
			got[0].ControlPath, want)
	}
	// A host reached over ssh is NOT this machine, and the identity walk must stay off
	// it: measured, 97 of 3117 local pids answer "an agent is at or under this pid".
	if got[0].IsLocalServer() {
		t.Error("a host from the file must not be marked as this machine's own server")
	}
}

// --host still works, and still wins, because it is what an unusual setup uses.
func TestAnExplicitHostFlagIsAddedToTheFile(t *testing.T) {
	runtimeDir(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts.toml")
	if err := hostset.SaveHosts(path, []hostset.Entry{{Alias: "nuc", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	got, err := hostsFrom(path, []hub.Host{{Label: "odd", Socket: "/tmp/odd.sock"}})
	if err != nil {
		t.Fatalf("hostsFrom: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v, want the file's host and the flag's", got)
	}
	if got[0].Label != "nuc" || got[1].Label != "odd" {
		t.Errorf("got %+v, want the file's host first and the flag's appended", got)
	}
}

// An absent file is the first run, not an error: §16 promises that zero configuration
// is a working configuration.
func TestAnAbsentHostsFileIsAnEmptyFleetAndNotAFailure(t *testing.T) {
	runtimeDir(t)
	got, err := hostsFrom(filepath.Join(t.TempDir(), "hosts.toml"), nil)
	if err != nil {
		t.Fatalf("hostsFrom on an absent file: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want nothing", got)
	}
}

// A file that cannot be parsed STOPS the program, and this is the decision that call
// site exists to make. The two alternatives are worse in ways that are hard to see:
//
//   - treated as empty, a broken file is indistinguishable from a first run, so the
//     picker opens and the first save OVERWRITES every decision the reader could not
//     parse — the user's host list is destroyed by the act of reporting nothing;
//   - and an empty fleet is exactly what hub.ReconcileMasters refuses to be handed,
//     because "I have no configured hosts" and "stop everything" are different
//     intents and one of them ends the operator's masters.
func TestAMalformedHostsFileStopsTheProgramRatherThanReadingAsEmpty(t *testing.T) {
	runtimeDir(t)
	path := filepath.Join(t.TempDir(), "hosts.toml")
	if err := os.WriteFile(path, []byte("[[host]]\nalias = \"nuc\"\nenabled = maybe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := hostsFrom(path, nil)
	if err == nil {
		t.Fatalf("hostsFrom accepted a malformed file and returned %+v — a host list that "+
			"reads as empty is one the next save deletes", got)
	}
	// The message must carry the file and the way out, or the person holding a broken
	// file has only "it did not start".
	for _, want := range []string{path, "maybe"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// `local` is the label this machine's own server already uses, and two hosts under one
// label share a pane namespace: hostFor answers with the FIRST match, so a write aimed
// at a remote pane %1 would be addressed to the local server's %1. The registry keys
// panes on the label too, so their rows merge before anything even asks.
func TestTheFileMayNotTakeTheLabelOfThisMachinesOwnServer(t *testing.T) {
	runtimeDir(t)
	path := filepath.Join(t.TempDir(), "hosts.toml")
	if err := hostset.SaveHosts(path, []hostset.Entry{{Alias: hub.LocalLabel, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := hostsFrom(path, nil); err == nil {
		t.Fatal("a file host called `local` was accepted, so a remote pane can be written " +
			"to on this machine's server")
	}
	// And the same hole from the other side: two entries, one label.
	dup := filepath.Join(t.TempDir(), "hosts.toml")
	if err := hostset.SaveHosts(dup, []hostset.Entry{{Alias: "nuc", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := hostsFrom(dup, []hub.Host{{Label: "nuc", Socket: "/tmp/other.sock"}}); err == nil {
		t.Fatal("two hosts labelled `nuc` were accepted — one of them is unreachable and " +
			"which one is decided by slice order")
	}
}

// The two options that decide whether a first run takes 7 seconds or 134. Measured on
// the host that set the wall time: `ssh metrics-engine 'tmux -V; id -u'` takes 133.3 s
// bare against 6.1 s with them, and ProbeAll is concurrent, so the wall IS the slowest
// probe. hostset.Runner's own doc comment states the requirement; this is what holds
// the production implementation to it.
func TestTheProbeCarriesTheOptionsThatBoundIt(t *testing.T) {
	got := strings.Join(probeArgs("nuc", []string{"tmux -V; id -u"}), " ")
	for _, want := range []string{"-o BatchMode=yes", "-o ConnectTimeout=6"} {
		if !strings.Contains(got, want) {
			t.Errorf("probe argv %q is missing %q — without both, one unanswering host "+
				"costs the system default (~2 minutes) and a password prompt nobody can "+
				"see blocks forever", got, want)
		}
	}
	// The alias must precede the command, or ssh reads the command as the destination.
	ai, ci := strings.Index(got, " nuc "), strings.Index(got, "tmux -V")
	if ai < 0 || ci < 0 || ai > ci {
		t.Errorf("probe argv %q must name the host before the command", got)
	}
}

// recordingRaw stands in for ssh at the raw door, recording every argv and answering
// rc=0. The master sweep is the one path in this program whose effect is to KILL
// something, so what it was asked to kill has to be observable.
type recordingRaw struct {
	mu   sync.Mutex
	argv [][]string
}

func (r *recordingRaw) RunRaw(_ context.Context, name string, args ...string) (tmux.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.argv = append(r.argv, append([]string{name}, args...))
	return tmux.Result{}, nil
}

func (r *recordingRaw) calls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]string, len(r.argv))
	copy(out, r.argv)
	return out
}

// listenAt makes a real unix socket at path, because the sweep refuses to stop
// anything that is not one — it lstats every candidate and skips regular files,
// directories and symlinks. A test that wrote plain files would pass against a sweep
// that does nothing.
func listenAt(t *testing.T, path string) {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	t.Cleanup(func() { _ = l.Close() })
}

// The startup sweep is the BACKSTOP for a leak the picker can produce: untick a host,
// have the hosts.toml write fail, fix the permission and press enter again — the file
// is then correct and the disabled host's master was never stopped. A master is
// reparented to pid 1 and outlives the hub, so without this it survives every restart,
// forever, holding a connection to a host the user said no to.
//
// It only works if the aliases handed to ReconcileMasters are the ENABLED ones. Pass
// every alias the file mentions and the unticked host is still "configured", its
// control path is spared, and the leak is permanent — which is invisible without this
// test, because the sweep does exactly the right thing for every host that is on.
func TestOnlyAnEnabledHostsMasterSurvivesTheStartupSweep(t *testing.T) {
	rt := runtimeDir(t)
	path := filepath.Join(t.TempDir(), "hosts.toml")
	if err := hostset.SaveHosts(path, []hostset.Entry{
		{Alias: "nuc", Enabled: true},
		{Alias: "eu", Enabled: false}, // unticked, and its master may have escaped the stop
	}); err != nil {
		t.Fatal(err)
	}
	keep, drop := hub.ControlPathFor(rt, "nuc"), hub.ControlPathFor(rt, "eu")
	listenAt(t, keep)
	listenAt(t, drop)

	all, err := hostsFrom(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := &recordingRaw{}
	reconcileMasters(context.Background(), all, rt, liveMasterOps(raw))

	// Only the EXITS are the victims. A sweep asks `ssh -O check` first — that is what
	// tells a live master from a leftover socket, and counting both verbs would report
	// every path twice.
	var stopped []string
	for _, argv := range raw.calls() {
		if len(argv) < 5 || argv[0] != "ssh" || argv[1] != "-O" {
			t.Errorf("the sweep ran something other than a master stop: %v", argv)
			continue
		}
		if argv[2] == "check" {
			continue
		}
		if argv[2] != "exit" {
			t.Errorf("unexpected ssh control verb: %v", argv)
			continue
		}
		stopped = append(stopped, argv[4]) // ssh -O exit -S <path> <host>
	}
	if len(stopped) != 1 || stopped[0] != drop {
		t.Fatalf("stopped %v, want exactly [%s] — the disabled host's master is the whole "+
			"point, and stopping nothing leaves it running until --stop-masters", stopped, drop)
	}
	// The other direction, which is the one that would break a working fleet: an enabled
	// host's master must be spared, or every start kills the connection it just adopted
	// and pays 1.55 s to spawn it again.
	for _, s := range stopped {
		if s == keep {
			t.Errorf("the sweep stopped %s, whose host is enabled", keep)
		}
	}
	// And the sockets themselves are untouched: the sweep asks ssh to exit, it never
	// unlinks. Measured elsewhere in this design — unlinking a live socket does not stop
	// its ssh, it only makes the path undialable while the process runs on.
	for _, p := range []string{keep, drop} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("the sweep removed %s itself: %v", p, err)
		}
	}
}

// A master belongs to a host only when BOTH halves are there: ssh needs a destination
// and the per-command line needs `-S <path>`. Turning the `||` into `&&` builds a
// hub.Master with an empty ControlPath, which `Ensure` then hands to
// `ssh -O check -S "" <alias>` — and the alias is the string ssh dials, not the label.
func TestAMasterNeedsBothADestinationAndAControlPath(t *testing.T) {
	for _, c := range []struct {
		name  string
		h     hub.Host
		alias string // "" means: this host has no master at all
	}{
		{"a host out of hosts.toml", hub.Host{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/cm-nuc"}, "nuc"},
		{"a --host entry whose label is not its destination",
			hub.Host{Label: "far", SSHDest: "far-dest", ControlPath: "/run/cm-far"}, "far-dest"},
		{"an ssh destination with nowhere to put the socket",
			hub.Host{Label: "nuc", SSHDest: "nuc"}, ""},
		{"a control path with no destination to dial",
			hub.Host{Label: "nuc", ControlPath: "/run/cm-nuc"}, ""},
		{"the local server", hub.Host{Label: "local", Socket: "/tmp/tmux-1000/default", LocalProc: true}, ""},
		{"the operator's own forward, master included",
			hub.Host{Label: "nuc", Socket: "/run/nuc.sock", SSHDest: "nuc", ControlPath: "/run/cm-nuc"}, "nuc"},
	} {
		m := masterFor(c.h)
		switch {
		case c.alias == "" && m != nil:
			t.Errorf("%s: got a master %+v, want none — a half-filled pair would be dialled "+
				"as `ssh -O check -S %q %s`", c.name, m, m.ControlPath, m.Alias)
		case c.alias != "" && m == nil:
			t.Errorf("%s: got no master, so this host is never reached", c.name)
		case c.alias != "" && m.Alias != c.alias:
			t.Errorf("%s: master alias = %q, want %q — the alias is what ssh dials and what "+
				"ControlPathFor hashes, so a label here names a master that does not exist",
				c.name, m.Alias, c.alias)
		}
	}
}

// `status` must not sweep. It is a READ, and its host list is whatever that one
// invocation was given — so `tmux-hub status --hosts other.toml` would `ssh -O exit`
// every master under $RT that the other file does not enable, including the ones the
// dashboard running beside it is polling through. ReconcileMasters spares only the
// aliases it is handed, and the empty-list guard is no help: one enabled host in the
// other file arms the sweep.
//
// It DOES ensure masters, in the foreground, and that half is asserted too: without a
// master every remote host reports the same missing one, which is a report about the
// hub's own startup rather than about the fleet.
func TestStatusEnsuresMastersAndNeverSweeps(t *testing.T) {
	// runStatus writes the report to stdout; the test wants its verdict, not its JSON.
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devnull.Close() })
	saved := os.Stdout
	os.Stdout = devnull
	t.Cleanup(func() { os.Stdout = saved })

	swept := make(chan []string, 1)
	ensured := make(chan string, 4)
	ops := masterOps{
		ensure: func(_ context.Context, m *hub.Master) error {
			ensured <- m.Alias
			return nil
		},
		reconcile: func(_ context.Context, _ string, aliases []string) error {
			swept <- aliases
			return nil
		},
	}
	// A destination that cannot resolve, so the two poll cycles and the agent listing
	// fail fast rather than waiting on a network: with ProxyCommand=false and no live
	// master, ssh gives up before any DNS lookup.
	hosts := []hub.Host{{Label: "nuc", SSHDest: "no-such-host.invalid",
		ControlPath: filepath.Join(t.TempDir(), "cm-not-a-master")}}

	if err := runStatus(hosts, false, ops); err != nil {
		t.Fatalf("runStatus: %v", err)
	}

	select {
	case aliases := <-swept:
		t.Fatalf("status swept with %v configured — a read-only report just stopped the "+
			"masters of whatever else is running against this runtime directory", aliases)
	default:
	}
	select {
	case alias := <-ensured:
		if alias != "no-such-host.invalid" {
			t.Errorf("ensured %q, want the host's ssh destination", alias)
		}
	default:
		t.Error("status ensured no master, so every remote host in the report says its master " +
			"is missing — a report about the hub rather than about the fleet")
	}
}
