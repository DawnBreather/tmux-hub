package hub

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

func liveTarget(t *testing.T) tmux.Target {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := filepath.Join(t.TempDir(), "tmux.sock")
	cmd := exec.Command("tmux", "-S", sock, "-f", "/dev/null",
		"new-session", "-d", "-s", "one", "-x", "80", "-y", "24")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })
	return tmux.Target{Label: "test", Socket: sock}
}

func TestTickPopulatesTheRegistry(t *testing.T) {
	tgt := liveTarget(t)
	reg := registry.New()
	p := NewPoller(tmux.NewExec(10*time.Second), reg)
	p.Add(Host{Label: tgt.Label, Socket: tgt.Socket})

	hosts := p.Tick(context.Background(), time.Now(), nil)
	if len(hosts) != 1 {
		t.Fatalf("len(hosts) = %d, want 1", len(hosts))
	}
	if hosts[0].Status != Up {
		t.Fatalf("status = %v, reason = %q, want Up", hosts[0].Status, hosts[0].Reason)
	}
	if hosts[0].Version == "" {
		t.Fatal("Version is empty; the field assertion did not run")
	}
	if len(reg.Panes()) != 1 {
		t.Fatalf("registry has %d panes, want 1", len(reg.Panes()))
	}
}

// A socket with no server must report Down with a reason a person can act on,
// never an empty status and never a crash.
func TestTickOnAbsentServerIsDownWithAReason(t *testing.T) {
	reg := registry.New()
	p := NewPoller(tmux.NewExec(5*time.Second), reg)
	p.Add(Host{Label: "ghost", Socket: filepath.Join(t.TempDir(), "nope.sock")})

	hosts := p.Tick(context.Background(), time.Now(), nil)
	if hosts[0].Status != Down {
		t.Fatalf("status = %v, want Down", hosts[0].Status)
	}
	if hosts[0].Reason == "" {
		t.Fatal("Down with no reason: a status without a remedy is a bug report to the wrong person")
	}
}

// The poll path must never mutate. This asserts it at the argument level: no
// command the poller issues may be one of tmux's mutating verbs.
func TestPollPathIsPure(t *testing.T) {
	tgt := liveTarget(t)
	rec := &recordingRunner{inner: tmux.NewExec(10 * time.Second)}
	reg := registry.New()
	p := NewPoller(rec, reg)
	p.Add(Host{Label: tgt.Label, Socket: tgt.Socket})
	p.Tick(context.Background(), time.Now(), nil)

	mutating := map[string]bool{
		"send-keys": true, "set": true, "set-option": true, "resize-window": true,
		"resize-pane": true, "new-session": true, "start-server": true,
		"kill-session": true, "kill-pane": true, "kill-server": true,
		"load-buffer": true, "paste-buffer": true, "set-buffer": true,
		"rename-session": true, "rename-window": true, "set-hook": true,
	}
	if len(rec.calls) == 0 {
		t.Fatal("no calls recorded")
	}
	for _, call := range rec.calls {
		for _, a := range call {
			if mutating[a] {
				t.Errorf("poll path issued a mutating command %q in %v", a, call)
			}
		}
	}
}

type recordingRunner struct {
	inner tmux.Runner
	calls [][]string
}

func (r *recordingRunner) Run(ctx context.Context, t tmux.Target, args ...string) (tmux.Result, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	return r.inner.Run(ctx, t, args...)
}

// Without this the hub polls the pane it runs in, captures its own screen, and
// renders that screen in a tile whose content is its own screen — an infinite
// mirror, observed live before the fix.
func TestSelfPaneIsExcluded(t *testing.T) {
	ds := []tmux.Delta{
		{PaneID: "%0", PaneHeight: 24},
		{PaneID: "%7", PaneHeight: 24},
	}
	p := &Poller{selfID: "%7", selfSocket: "/tmp/mine.sock"}
	got := p.excludeSelf("/tmp/mine.sock", ds)
	if len(got) != 1 || got[0].PaneID != "%0" {
		t.Fatalf("excludeSelf = %+v, want only %%0", got)
	}
}

func TestNoSelfPaneOutsideTmuxKeepsEverything(t *testing.T) {
	ds := []tmux.Delta{{PaneID: "%0"}, {PaneID: "%1"}}
	p := &Poller{selfID: "", selfSocket: "/tmp/mine.sock"}
	if got := p.excludeSelf("/tmp/mine.sock", ds); len(got) != 2 {
		t.Fatalf("outside tmux nothing should be excluded, got %d", len(got))
	}
}

// A host whose only pane is the hub itself is up-with-nothing-to-show, not down.
func TestHostWithOnlyTheHubPaneIsUpEmpty(t *testing.T) {
	tgt := liveTarget(t)
	ids := paneIDs(t, tgt)
	reg := registry.New()
	p := NewPoller(tmux.NewExec(10*time.Second), reg)
	p.selfID = ids[0] // pretend the hub lives in the only pane there is
	p.selfSocket = tgt.Socket
	p.Add(Host{Label: tgt.Label, Socket: tgt.Socket})

	hosts := p.Tick(context.Background(), time.Now(), nil)
	if hosts[0].Status != UpEmpty {
		t.Fatalf("status = %v (%s), want UpEmpty", hosts[0].Status, hosts[0].Reason)
	}
	if len(reg.Panes()) != 0 {
		t.Fatalf("registry has %d panes, want none", len(reg.Panes()))
	}
}

func paneIDs(t *testing.T, tgt tmux.Target) []string {
	t.Helper()
	r := tmux.NewExec(10 * time.Second)
	ds, err := tmux.FetchDeltas(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.PaneID)
	}
	return out
}

// Pane ids are per-server, so the hub's own $TMUX_PANE says nothing about a
// remote host. Applying it everywhere silently deleted a real remote host's %0 —
// which was the pane asking a question.
func TestSelfExclusionDoesNotReachOtherHosts(t *testing.T) {
	ds := []tmux.Delta{{PaneID: "%0"}, {PaneID: "%1"}}
	p := &Poller{selfID: "%0", selfSocket: "/tmp/mine.sock"}
	if got := p.excludeSelf("/tmp/other.sock", ds); len(got) != 2 {
		t.Fatalf("another server lost a pane to this hub's self-exclusion: %+v", got)
	}
	if got := p.excludeSelf("/tmp/mine.sock", ds); len(got) != 1 {
		t.Fatalf("the hub's own server should drop its own pane: %+v", got)
	}
	// And the socket, not the label, is what decides: the hub's own server may be
	// registered under any label at all.
	q := &Poller{selfID: "%0", selfSocket: "/tmp/mine.sock"}
	if got := q.excludeSelf("/tmp/mine.sock", ds); len(got) != 1 {
		t.Fatalf("exclusion must key on the socket whatever the label is: %+v", got)
	}
}

// Classifying a socket as live can only be done by waiting to NOT get an EOF, so
// the dial must never be on the happy path: it added seconds per host per tick
// and starved the poll loop. This asserts the happy path issues no dial by
// timing it against a live local server.
func TestHappyPathDoesNotPayForADial(t *testing.T) {
	tgt := liveTarget(t)
	reg := registry.New()
	p := NewPoller(tmux.NewExec(10*time.Second), reg)
	p.Add(Host{Label: tgt.Label, Socket: tgt.Socket})

	p.Tick(context.Background(), time.Now(), nil) // first tick also probes fields
	start := time.Now()
	hosts := p.Tick(context.Background(), time.Now(), nil)
	elapsed := time.Since(start)

	if hosts[0].Status != Up {
		t.Fatalf("status = %v (%s)", hosts[0].Status, hosts[0].Reason)
	}
	// A dial's live-classification timeout is 300ms; a local tick is single-digit
	// milliseconds. Anything near or above the timeout means a dial happened.
	if elapsed > 250*time.Millisecond {
		t.Fatalf("a healthy local tick took %v — the dial is on the happy path", elapsed)
	}
}

// The batch's label queries are `list-panes -a`, which returns EVERY pane, so
// dropping the hub's own pane from the delta list BEFORE the batch made the
// reader expect one line fewer per label block and misread the next block's
// first line as a frame — seen live as `bad frame line "%1|zsh"`. This drives
// the real path with self-exclusion active.
func TestSelfExclusionDoesNotBreakTheBatchDemux(t *testing.T) {
	tgt := liveTarget(t)
	// a second pane, so there is something left after the hub's own is dropped
	if out, err := exec.Command("tmux", "-S", tgt.Socket, "new-window", "-d", "-t", "one",
		"sh -c 'echo VISIBLE; sleep 300'").CombinedOutput(); err != nil {
		t.Fatalf("new-window: %v: %s", err, out)
	}
	time.Sleep(1200 * time.Millisecond)

	ids := paneIDs(t, tgt)
	if len(ids) < 2 {
		t.Fatalf("want 2 panes, got %v", ids)
	}
	reg := registry.New()
	p := NewPoller(tmux.NewExec(15*time.Second), reg)
	p.selfID, p.selfSocket = ids[0], tgt.Socket // pretend the hub lives in the first
	p.Add(Host{Label: tgt.Label, Socket: tgt.Socket})

	hosts := p.Tick(context.Background(), time.Now(), nil)
	if hosts[0].Status != Up {
		t.Fatalf("status = %v, reason = %q", hosts[0].Status, hosts[0].Reason)
	}
	got := reg.Panes()
	if len(got) != len(ids)-1 {
		t.Fatalf("registry has %d panes, want %d (all but the hub's own)", len(got), len(ids)-1)
	}
	for _, pane := range got {
		if pane.PaneID == ids[0] {
			t.Errorf("the hub's own pane %s is in the registry", pane.PaneID)
		}
		if pane.Session == "" {
			t.Errorf("pane %s has no session label, so the label blocks mis-aligned", pane.PaneID)
		}
	}
}

// The listing is a separate, slower producer: measured at ~200ms locally but
// 0.5-2.8s over ssh, so it must not be in the tick.
func TestFetcherForPicksTheRightCommand(t *testing.T) {
	p := &Poller{}
	// The fixture carries the real local SOCKET, which is what AddLocal sets and what
	// makes a host local. The bare `Host{Label: LocalLabel}` this used to pass is not a
	// host the program ever builds, and it was only accepted because the old rule keyed
	// on the absence of an ssh destination rather than on the socket.
	if f := p.fetcherFor(Host{Label: LocalLabel, Socket: tmux.LocalSocket(), LocalProc: true}); f == nil {
		t.Error("the local host must get a local fetcher")
	}
	remote := Host{Label: "nuc", Socket: "/s.sock", SSHDest: "nuc", ControlPath: "/s.ctl"}
	if f := p.fetcherFor(remote); f == nil {
		t.Error("a remote host with a control path must get an ssh fetcher")
	}
	// A host reached only by a forwarded socket has no shell to run claude in.
	socketOnly := Host{Label: "nuc", Socket: "/s.sock", SSHDest: "nuc"}
	if f := p.fetcherFor(socketOnly); f != nil {
		t.Error("without a control path there is no way to run the listing")
	}
	// And the hole this used to have: a forwarded socket with NO ssh destination is
	// somebody else's machine. Running the listing here attributed this machine's Claude
	// sessions to that host's label — measured, 13 local agent rows under a remote label.
	forwardedOnly := Host{Label: "nuc", Socket: "/run/user/1000/nuc.sock"}
	if f := p.fetcherFor(forwardedOnly); f != nil {
		t.Error("a forwarded socket with no ssh destination must not be listed LOCALLY — " +
			"that reports this machine's agents as if they were the remote host's")
	}
}

// A host is LOCAL only when its socket is this machine's tmux server. Before this,
// `walkerFor` and `fetcherFor` keyed on `!Remote()` — i.e. on the ABSENCE of an ssh
// destination — so a forwarded socket given as `--host label=/run/user/1000/nuc.sock`
// with no `ssh=` was treated as local.
//
// That is not a cosmetic mislabel. The identity walk that gates every write would then
// answer from the LOCAL process table using REMOTE pane pids: measured on this machine,
// 97 of 3117 live local pids report "an agent is at or under this pid" (3.11%), pid 1
// among them. A remote pane whose pane_pid collides with any of those is marked
// identified, gets stamped, and — as a single fresh identified target — is written to
// with NO confirmation dialog. The safety property was not enforced, only accidentally
// satisfied.
func TestAForwardedSocketWithoutSSHIsNotLocal(t *testing.T) {
	for _, c := range []struct {
		name string
		h    Host
		want bool
	}{
		{"what AddLocal builds", (&Poller{}).AddLocal(), true},
		{"a forwarded socket, no ssh dest", Host{Label: "nuc", Socket: "/run/user/1000/nuc.sock"}, false},
		{"a forwarded socket with an ssh dest", Host{Label: "nuc", Socket: "/run/user/1000/nuc.sock", SSHDest: "nuc"}, false},
		{"a local server on a non-default socket, marked", Host{Label: "t", Socket: "/tmp/x.sock", LocalProc: true}, true},
		{"the same socket unmarked — unknown means read-only", Host{Label: "t", Socket: "/tmp/x.sock"}, false},
	} {
		if got := c.h.IsLocalServer(); got != c.want {
			t.Errorf("%s: IsLocalServer() = %v, want %v — a host that is not this "+
				"machine's server must never be answered from this machine's /proc",
				c.name, got, c.want)
		}
	}
}

// $TMUX is socket,server_pid,session_id, and the third field is a BARE number
// while every session target needs the $ sigil. The bare form would name a
// session CALLED 0, which is a different session that may well exist.
func TestSelfSessionIDAddsTheSigil(t *testing.T) {
	for _, tc := range []struct{ env, want string }{
		{"/tmp/tmux-1000/default,4242,0", "$0"},
		{"/tmp/tmux-1000/default,4242,17", "$17"},
		{"", ""},
		{"/tmp/tmux-1000/default", ""},          // no session field at all
		{"/tmp/tmux-1000/default,4242", ""},     // still no session field
		{"/tmp/tmux-1000/default,4242,", ""},    // present but empty
		{"/tmp/tmux-1000/default,4242,abc", ""}, // not a number: refuse rather than guess
	} {
		t.Setenv("TMUX", tc.env)
		if got := SelfSessionID(); got != tc.want {
			t.Errorf("TMUX=%q: SelfSessionID() = %q, want %q", tc.env, got, tc.want)
		}
	}
}

// slowRunner blocks every call until it is released, so a tick is reliably IN FLIGHT while
// the test does something else. A sleep would make the window a guess; this makes it a
// fact, which matters because the defect below is invisible when the two do not overlap.
type slowRunner struct {
	entered chan struct{} // one token per call that has arrived
	release chan struct{} // closed to let them all finish
}

func newSlowRunner(n int) *slowRunner {
	return &slowRunner{entered: make(chan struct{}, n), release: make(chan struct{})}
}

func (r *slowRunner) Run(ctx context.Context, t tmux.Target, args ...string) (tmux.Result, error) {
	r.entered <- struct{}{}
	<-r.release
	return tmux.Result{}, fmt.Errorf("no server")
}

func (r *slowRunner) RunInput(ctx context.Context, t tmux.Target, stdin []byte, args ...string) (tmux.Result, error) {
	return r.Run(ctx, t, args...)
}

// Add is called from bubbletea's Update goroutine — the picker's save connects the hosts it
// just enabled — while a tick is in flight in a tea.Cmd goroutine. Nothing tested that
// pairing, which is why `go test -race ./...` was 16/16 green while the host slice was
// guarded by nothing.
//
// Two failures, and the second is the one that costs the operator something. `append`
// reallocating is a data race on the slice header; and once it has reallocated, the tick's
// per-host goroutines write Status and Reason into the ABANDONED array while the tick
// copies out of the new one, so those writes are silently discarded — a host snapping back
// to `connecting` after a save.
//
// This test is calibrated by the race detector itself: remove the lock from Add and it
// fails under `-race`.
func TestAddDuringATickIsSafe(t *testing.T) {
	r := newSlowRunner(4)
	p := NewPoller(r, registry.New())
	// Two hosts to start, so the tick holds pointers into a slice that a third will grow.
	// One is enough for the header race; more makes the abandoned-array write likelier.
	p.Add(Host{Label: "a", Socket: "/tmp/a.sock"})
	p.Add(Host{Label: "b", Socket: "/tmp/b.sock"})

	done := make(chan []Host, 1)
	go func() { done <- p.Tick(context.Background(), time.Now(), nil) }()

	// Both host goroutines are now inside Run and cannot proceed: the tick is provably in
	// flight rather than probably.
	<-r.entered
	<-r.entered

	p.Add(Host{Label: "c", Socket: "/tmp/c.sock"})
	close(r.release)
	got := <-done

	// The tick reports the fleet as it stands when it finishes, so a host added while it
	// ran is already in the result rather than missing for one cycle. That matters to the
	// caller: the UI assigns m.hosts from this wholesale, so a tick that dropped `c` would
	// make the row the user just created flicker out and back.
	if len(got) != 3 {
		t.Fatalf("the tick returned %d hosts, want the 3 the fleet now holds: %+v", len(got), got)
	}
	c, ok := hostByLabel(got, "c")
	if !ok {
		t.Fatalf("the host added during the tick is missing from the result: %+v", got)
	}
	// And it is not given a verdict by a poll that never asked it.
	if c.Status != Connecting {
		t.Errorf("c reads %v, want connecting — the tick started before it existed", c.Status)
	}
	next := p.snapshot()
	if len(next) != 3 {
		t.Fatalf("the fleet holds %d hosts after Add, want 3: %+v", len(next), next)
	}
	// And the tick's own work survived the append, which is the half a race detector alone
	// would not tell you: both original hosts must carry the status the poll gave them,
	// not the `connecting` they were added with.
	for _, label := range []string{"a", "b"} {
		h, ok := hostByLabel(next, label)
		if !ok {
			t.Fatalf("%s vanished from the fleet", label)
		}
		if h.Status == Connecting {
			t.Errorf("%s is still `connecting`, so the tick's status write was discarded — "+
				"the append reallocated and the goroutine wrote into the abandoned array", label)
		}
	}
}

func hostByLabel(hosts []Host, label string) (Host, bool) {
	for _, h := range hosts {
		if h.Label == label {
			return h, true
		}
	}
	return Host{}, false
}

// Add racing the fleet's readers, hammered. The deterministic test above parks the tick
// and then calls Add, which is the right shape for asserting no WRITE was lost — but it
// leaves the two provably NOT overlapping, so it cannot see an unlocked `append` at all.
// Overlap is what the detector needs, and overlap is what this arranges.
//
// Calibrated by the detector: take the lock off Add and this fails under `-race`.
func TestAddAndTickOverlapSafely(t *testing.T) {
	p := NewPoller(errRunner{}, registry.New())
	p.Add(Host{Label: "seed", Socket: "/tmp/seed.sock"})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			p.Add(Host{Label: fmt.Sprintf("h%03d", i), Socket: "/tmp/h.sock"})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			p.Tick(context.Background(), time.Now(), nil)
		}
	}()
	wg.Wait()

	if n := len(p.snapshot()); n != 201 {
		t.Errorf("the fleet holds %d hosts, want 201 — an append was lost", n)
	}
}

// errRunner answers immediately and unhappily. The ticks above are for overlap, not for
// what they learn.
type errRunner struct{}

func (errRunner) Run(context.Context, tmux.Target, ...string) (tmux.Result, error) {
	return tmux.Result{}, fmt.Errorf("no server")
}

func (errRunner) RunInput(context.Context, tmux.Target, []byte, ...string) (tmux.Result, error) {
	return tmux.Result{}, fmt.Errorf("no server")
}

// A transport merge must leave AgentsReason alone, because TickAgents owns it and runs on
// its own 20 s timer. Recorded in known-issues as the two ticks racing on the host struct:
// both handed out `&p.hosts[i]` and then copied the whole struct out, so either could lose
// the other's write.
//
// Asserted on the merge directly rather than by running both ticks. Two real ticks cannot
// reach it: TickAgents has no fetcher for a socket-only host, so it writes nothing, and a
// whole-struct merge then preserves the field by accident — which is exactly how my first
// version of this test passed against the defect.
func TestATransportMergeLeavesTheAgentFieldAlone(t *testing.T) {
	p := NewPoller(errRunner{}, registry.New())
	p.Add(Host{Label: "a", Socket: "/tmp/a.sock"})

	// What a tick took at its start: no agent reason yet.
	fresh := p.snapshot()
	fresh[0].Status, fresh[0].Reason = Up, ""

	// What TickAgents learned WHILE that tick was running, merged before it finished.
	p.mergeAgents([]Host{{Label: "a", AgentsReason: "claude is not installed there"}})

	// The transport merge lands last and must not carry its stale copy of the field back.
	got := p.mergeTransport(fresh)
	h, ok := hostByLabel(got, "a")
	if !ok {
		t.Fatal("the host vanished from the merge")
	}
	if h.AgentsReason != "claude is not installed there" {
		t.Errorf("AgentsReason = %q — the transport merge overwrote a field it does not own",
			h.AgentsReason)
	}
	if h.Status != Up {
		t.Errorf("status = %v, want up — the merge dropped the field it DOES own", h.Status)
	}
}

// The merge matches by label, never by index, and the case that separates them is a fleet
// that SHRANK while a tick ran. With index matching the survivor inherits the departed
// host's verdict — a status belonging to a host that is gone, attached to one that is not.
func TestTheMergeMatchesByLabelNotByIndex(t *testing.T) {
	p := NewPoller(errRunner{}, registry.New())
	p.Add(Host{Label: "a", Socket: "/tmp/a.sock"})
	p.Add(Host{Label: "b", Socket: "/tmp/b.sock"})

	fresh := p.snapshot()
	fresh[0].Status, fresh[0].Reason = Down, "a is down"
	fresh[1].Status, fresh[1].Reason = Up, ""

	// `a` leaves mid-tick, so `b` is now index 0 while the tick's results still start at a.
	p.mu.Lock()
	p.hosts = p.hosts[1:]
	p.mu.Unlock()

	got := p.mergeTransport(fresh)
	if len(got) != 1 || got[0].Label != "b" {
		t.Fatalf("the merge resurrected a departed host: %+v", got)
	}
	if got[0].Status != Up || got[0].Reason != "" {
		t.Errorf("b reads %v %q — it inherited the verdict of the host that left",
			got[0].Status, got[0].Reason)
	}
}
