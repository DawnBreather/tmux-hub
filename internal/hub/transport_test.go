package hub

import (
	"context"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// targetSpy records the target every call was addressed to, and answers with a fixed
// result. It is deliberately not the real runner: what is under test here is which
// SERVER the poller decided to talk to, which the real runner would turn into a live
// ssh and hide behind a network failure.
type targetSpy struct {
	mu      sync.Mutex
	targets []tmux.Target
	res     tmux.Result
}

func (s *targetSpy) Run(_ context.Context, t tmux.Target, _ ...string) (tmux.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets = append(s.targets, t)
	return s.res, nil
}

func (s *targetSpy) first(t *testing.T) tmux.Target {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.targets) == 0 {
		t.Fatal("the poller issued no command at all, so nothing about its target is proved")
	}
	return s.targets[0]
}

// A host out of hosts.toml has NO socket: §5 deleted the forward, so its only
// transport is the ssh master. The poller built its target as
// `tmux.Target{Label, Socket}` and dropped both ssh fields, which meant every such
// host was addressed as a LOCAL tmux server on an empty socket path — refused by
// tmux.build with ErrNoSocket, i.e. a host configured through the file the whole
// plan is about could not be polled at all.
func TestASocketlessHostIsPolledOverItsMaster(t *testing.T) {
	spy := &targetSpy{res: tmux.Result{RC: 1, Stderr: "no server running on /tmp/tmux-1000/default"}}
	p := NewPoller(spy, registry.New())
	p.Add(Host{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/user/1000/cm-deadbeef-nuc"})

	p.Tick(context.Background(), time.Now(), nil)

	got := spy.first(t)
	if !got.Remote() {
		t.Fatalf("polled %+v — a host with no socket must be addressed over ssh", got)
	}
	if got.SSHDest != "nuc" || got.ControlPath != "/run/user/1000/cm-deadbeef-nuc" {
		t.Errorf("polled %+v — the target must carry the destination AND the control path, "+
			"because a remote run with no control path is refused rather than run bare", got)
	}
}

// A socket named on the command line is the operator's own forward, and it has
// carried the poll since `--host` existed. This is the compatibility half of
// Host.Target: preferring ssh would silently stop using a tunnel they built.
func TestAHostWithBothPrefersTheSocketItWasGiven(t *testing.T) {
	for _, c := range []struct {
		name string
		h    Host
		want tmux.Target
	}{
		{"hosts.toml: no socket exists", Host{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/cm-nuc"},
			tmux.Target{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/cm-nuc"}},
		{"the operator's own forward, plus a master for attach",
			Host{Label: "nuc", Socket: "/run/nuc.sock", SSHDest: "nuc", ControlPath: "/run/cm-nuc"},
			tmux.Target{Label: "nuc", Socket: "/run/nuc.sock"}},
		{"the local server", Host{Label: LocalLabel, Socket: "/tmp/tmux-1000/default", LocalProc: true},
			tmux.Target{Label: LocalLabel, Socket: "/tmp/tmux-1000/default"}},
		// The socket override (docs/design.md §9) travels on BOTH branches, because it
		// describes the SERVER rather than the transport that reaches it. Asserted here
		// rather than beside the seam because Target() is the only converter, so this
		// table is where a dropped field is visible at all.
		{"a socket override on a host from the file",
			Host{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/cm-nuc", TmuxArgs: []string{"-L", "work"}},
			tmux.Target{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/cm-nuc",
				TmuxArgs: []string{"-L", "work"}}},
		{"a socket override on a host with its own forward",
			Host{Label: "nuc", Socket: "/run/nuc.sock", TmuxArgs: []string{"-L", "work"}},
			tmux.Target{Label: "nuc", Socket: "/run/nuc.sock", TmuxArgs: []string{"-L", "work"}}},
	} {
		// DeepEqual rather than ==: Target carries a slice since it gained TmuxArgs, so
		// the struct is no longer comparable. That is a deliberate cost — the
		// alternative, a single socket string, cannot express `-L label` at all.
		if got := c.h.Target(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Target() = %+v, want %+v", c.name, got, c.want)
		}
	}
}

// `error connecting to /tmp/tmux-1000/default` from the FAR tmux means the host is
// reachable and has no server — the state design.md §5 measures as rc=1. Read through
// the local socket taxonomy it became "the tunnel is down or was never built", which
// names a mechanism this design does not have and sends the reader to look for a
// forward that does not exist.
func TestARemoteHostWithNoServerIsReachableRatherThanATunnelFault(t *testing.T) {
	spy := &targetSpy{res: tmux.Result{RC: 1,
		Stderr: "error connecting to /tmp/tmux-1000/default (No such file or directory)"}}
	p := NewPoller(spy, registry.New())
	p.Add(Host{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/user/1000/cm-deadbeef-nuc"})

	hosts := p.Tick(context.Background(), time.Now(), nil)
	if hosts[0].Status != UpEmpty {
		t.Fatalf("status = %v (%s), want UpEmpty", hosts[0].Status, hosts[0].Reason)
	}
	if strings.Contains(hosts[0].Reason, "tunnel") {
		t.Errorf("reason = %q — there is no tunnel in this design, so a reader sent to check "+
			"one has been sent to check nothing", hosts[0].Reason)
	}
}

// The whole point of ProxyCommand=false is that a dead master fails loudly, and the
// whole point of explainTransport is that the failure carries `ssh -N -M …`. Both are
// wasted if the host row rewrites the message on the way to the screen — and it did:
// ssh's own text ends in `Control socket connect(…): No such file or directory`, which
// the socket taxonomy matched as "socket is not there — the tunnel is down".
//
// It runs a real ssh, which cannot reach the network: ProxyCommand=false fails before
// any DNS lookup, so this is offline and fast.
//
// The host is arranged as one that has ANSWERED before and then lost its master, which is
// what makes `down` the right verdict and the respawn command the right words. That
// arrangement is not decoration: a host which has never answered is `connecting` with no
// remedy at all, because the hub is still spawning that master itself and telling the
// operator to respawn it would be telling them to fight their own tool. This test used to
// assert `down` for a never-answered host, which is the defect the first frame showed.
func TestADeadMasterKeepsItsRespawnCommandOnTheHostRow(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh not installed")
	}
	p := NewPoller(tmux.NewExec(10*time.Second), registry.New())
	p.Add(Host{Label: "nuc", SSHDest: "no-such-host.invalid",
		ControlPath: filepath.Join(t.TempDir(), "cm-not-a-master"), Seen: true})

	hosts := p.Tick(context.Background(), time.Now(), nil)
	if hosts[0].Status != Down {
		t.Fatalf("status = %v (%s), want Down", hosts[0].Status, hosts[0].Reason)
	}
	if !strings.Contains(hosts[0].Reason, "ssh -N -M") {
		t.Errorf("reason = %q — a dead master must reach the row with the command that "+
			"respawns it, which is the only action available to the person reading it",
			hosts[0].Reason)
	}
}

// The first frame of the program said a healthy host was DOWN. Measured in a real pty
// at 100x14 with one enabled host: the poll ticks at 250 ms, a master takes ~1.55 s to
// become checkable, so the first polls fail while the hub is still spawning that master
// ITSELF — and the operator's first screen read
// `nuc down (list-panes rc=255: … no live ssh master at` , truncated mid-sentence, with
// a remedy they must not run. §16 promises the opposite: hosts fold in as they answer.
//
// This runs a REAL ssh against a control path that is not a master, which is exactly the
// state of a host whose spawn has not finished. It needs no network: ProxyCommand=false
// fails before any DNS lookup.
func TestAHostThatHasNeverAnsweredIsConnectingRatherThanDown(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh not installed")
	}
	p := NewPoller(tmux.NewExec(10*time.Second), registry.New())
	p.Add(Host{Label: "nuc", SSHDest: "no-such-host.invalid",
		ControlPath: filepath.Join(t.TempDir(), "cm-not-yet-a-master")})

	hosts := p.Tick(context.Background(), time.Now(), nil)
	if hosts[0].Status != Connecting {
		t.Fatalf("status = %v (%s), want connecting — a host that has never answered, whose "+
			"master the hub is still spawning, is not down", hosts[0].Status, hosts[0].Reason)
	}
	if hosts[0].Reason != "waiting for its ssh master" {
		t.Errorf("reason = %q, want the short true one", hosts[0].Reason)
	}
	// The remedy must NOT be there: the hub is spawning that master, so telling the
	// operator to respawn it is telling them to fight their own tool. It is also what
	// made the line too long to fit the one row the footer gives a host.
	if strings.Contains(hosts[0].Reason, "ssh -N -M") {
		t.Errorf("reason = %q — this one carries a command the operator must not run",
			hosts[0].Reason)
	}
	if n := len(hosts[0].Reason); n > 40 {
		t.Errorf("reason is %d chars: at 80 columns the footer gives a host one line, and "+
			"known-issues L2 is this class of truncation", n)
	}
	// Stable rather than one-shot: the second tick must not decide it is down either,
	// because nothing has changed about what we know.
	again := p.Tick(context.Background(), time.Now(), nil)
	if again[0].Status != Connecting {
		t.Errorf("second tick = %v (%s), want connecting still", again[0].Status, again[0].Reason)
	}
}

// The other side of the same flag, and the reason it is a flag rather than a timer: once
// a host HAS answered, a transport failure is a real fault and must say so with the
// remedy. Without this arm, "always connecting" would pass the test above.
func TestOnceAHostHasAnsweredATransportFailureIsDownWithItsRemedy(t *testing.T) {
	// First the far tmux answers — no server over there, which is an ANSWER and proves
	// the master works. Then the transport breaks.
	spy := &targetSpy{res: tmux.Result{RC: 1,
		Stderr: "error connecting to /tmp/tmux-1000/default (No such file or directory)"}}
	p := NewPoller(spy, registry.New())
	p.Add(Host{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/user/1000/cm-deadbeef-nuc"})

	if hosts := p.Tick(context.Background(), time.Now(), nil); hosts[0].Status != UpEmpty {
		t.Fatalf("first tick = %v (%s), want up-empty — the far tmux answered",
			hosts[0].Status, hosts[0].Reason)
	}

	spy.mu.Lock()
	spy.res = tmux.Result{RC: 255, Stderr: `host "nuc" was not reached and nothing was sent: ` +
		"no live ssh master at /run/user/1000/cm-deadbeef-nuc — respawn it with " +
		"`ssh -N -M -S /run/user/1000/cm-deadbeef-nuc nuc`, then retry"}
	spy.mu.Unlock()

	hosts := p.Tick(context.Background(), time.Now(), nil)
	if hosts[0].Status != Down {
		t.Fatalf("status = %v (%s), want down — this host answered before and has stopped",
			hosts[0].Status, hosts[0].Reason)
	}
	if !strings.Contains(hosts[0].Reason, "ssh -N -M") {
		t.Errorf("reason = %q — a host that really lost its master must arrive with the "+
			"command that brings it back", hosts[0].Reason)
	}
	// And it arrives WITHOUT the send-shaped preamble. explainTransport writes `host "x" was not
	// reached and nothing was sent: ` because its commonest caller is a send; nothing was sent
	// here, this is a poll, and the footer already names the host and says it is down. The
	// preamble is 49 columns of the one line a host gets — measured, at 80 columns what survived
	// of this reason named neither the fault nor the remedy.
	if !strings.HasPrefix(hosts[0].Reason, "no live ssh master") {
		t.Errorf("reason = %q — a host reason must lead with the fault, not with a report about "+
			"a send that never happened", hosts[0].Reason)
	}
}
