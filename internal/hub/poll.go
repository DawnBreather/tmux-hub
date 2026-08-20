// Package hub drives the poll loop.
//
// Everything here is read-only. A pane is never written to, no option is set,
// and no server is started — including as a side effect of probing, which in an
// earlier design started a LOCAL tmux server on the forward socket and answered
// as if it were the remote (docs/design.md §5).
package hub

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/agents"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

type Status int

const (
	Connecting Status = iota
	Up
	UpEmpty        // reachable, no tmux server
	DegradedFormat // a required format field came back empty
	Down
)

func (s Status) String() string {
	switch s {
	case Connecting:
		return "connecting"
	case Up:
		return "up"
	case UpEmpty:
		return "up-empty"
	case DegradedFormat:
		return "degraded:format"
	default:
		return "down"
	}
}

// Host is one tmux server the hub watches, plus its status. Status is always a
// positive assertion with a reason; absence of an error is never a status.
type Host struct {
	Label  string
	Socket string

	// SSHDest and ControlPath are set for a remote host. They are how ATTACH
	// reaches it: a forwarded socket cannot carry an attach at all — measured,
	// `tmux -S <forwarded> attach` fails `open terminal failed: not a terminal`
	// even with a real pty, because the client passes its terminal fd over
	// SCM_RIGHTS and a forward does not carry ancillary data. Polling uses the
	// socket; attaching uses the master.
	SSHDest     string
	ControlPath string

	// TmuxArgs are this host's extra global tmux options, straight out of its
	// `tmux_args` in hosts.toml — docs/design.md §9's socket override, for a server
	// that lives on `-L <label>` rather than the default socket. Target() is what
	// carries them to the seam; nothing else reads them.
	TmuxArgs []string

	Status  Status
	Reason  string
	Version string

	// LastOK is when this host last answered, so the next tick knows how long the
	// gap actually was. A freshness window derived from a constant is wrong in the
	// direction that matters: a slow cycle made a busy pane read idle.
	LastOK time.Time

	// Seen says this host has answered AT LEAST ONCE, ever, and it is what separates
	// `down` from `connecting`.
	//
	// It exists because the first frame of the program said a healthy host was DOWN.
	// Measured in a real pty at 100x14 with one enabled host: the poll ticks at 250 ms
	// and a master takes ~1.55 s to become checkable, so the first two or three polls
	// fail while the hub is still spawning the master ITSELF — and explainFailure
	// classified that as `down`, with a truncated ssh error recommending a respawn the
	// operator must not run. §16 promises the opposite: "remote hosts fold in as they
	// answer, connecting until then".
	//
	// It is a fact about the host rather than a window of time on purpose. Any timing
	// rule here — delay the first tick, coordinate with the spawn, wait N ticks — has
	// to encode how long a master takes, which is a number measured on one machine and
	// one network. Whether the host has ever spoken cannot rot.
	//
	// It is NOT LastOK, which is the freshness clock for the attention model: LastOK is
	// set only by a healthy poll, while Seen is set by any ANSWER — including "reachable,
	// no tmux server" and "tmux answered but a format came back empty", both of which
	// prove the transport works and therefore that a later failure is a real fault.
	Seen bool

	// AgentsReason records why the Claude-session listing did not work for this
	// host, if it did not. A host can be perfectly up as a tmux server and have no
	// claude installed, which is not an error about the host.
	AgentsReason string

	// LocalProc says this host's panes' pids live in THIS machine's process table, so
	// the identity walk may use it. Set by AddLocal, or explicitly by the operator for a
	// local server on a non-default socket. Never inferred — see IsLocalServer.
	LocalProc bool

	// Peer is what SO_PEERCRED said about the other end of this host's SOCKET, and it
	// is filled only for a host that has one — i.e. the local server, or a `--host`
	// entry naming a forwarded socket. It is empty for every host reached over an ssh
	// master, and that is not a gap: §5 deleted the forward, so there is no local
	// socket to have a peer, and explainFailure returns before dialling rather than
	// asking the kernel about a path that does not exist.
	//
	// It is written on FAILURE only, which is the same asymmetry: the dial runs from
	// explainFailure because proving a socket "live" costs a full read timeout (a live
	// tmux server says nothing until spoken to), so it is a diagnostic and never part
	// of a healthy tick. An empty Peer therefore means "not asked" far more often than
	// it means "nobody there".
	//
	// The comment this replaces said a remote host's peer "should be the ssh process
	// the hub spawned" — true of the forward design, where the hub dialled a local
	// socket that ssh was serving, and unreachable now.
	Peer PeerInfo
}

// Remote reports whether this host is reached over ssh.
func (h Host) Remote() bool { return h.SSHDest != "" }

// Target is how a tmux command about this host is addressed. It is the ONE place a
// Host becomes a tmux.Target, because there is more than one right answer and
// picking it per call site is how they drift: every site used to write
// `tmux.Target{Label: h.Label, Socket: h.Socket}`, which for a host out of
// hosts.toml — no socket at all, §5 deleted the forward — is refused by build with
// ErrNoSocket. So the poll could not reach such a host, and nothing about a stamp,
// a kill or a sweep could either.
//
// A named SOCKET wins over ssh, and that ordering is the compatibility half. The
// only way to have both today is `--host label=/path,ssh=dest,ctl=path`, i.e. the
// operator's own forward plus a master for attach — and that entry has polled over
// the socket since it existed. Preferring ssh here would silently stop using the
// forward they built and asked for. A host with no socket has nothing to prefer,
// which is the new shape and the reason this method exists.
//
// TmuxArgs travels on every answer, local or remote: it is a property of the SERVER
// this host names, not of the transport that reaches it, so a socket override has to
// survive both branches. It is also the reason this method must stay the only
// converter — a field added here reaches every call site, while a field added to a
// hand-built Target reaches exactly one.
func (h Host) Target() tmux.Target {
	t := tmux.Target{Label: h.Label, Socket: h.Socket, TmuxArgs: h.TmuxArgs}
	if h.Socket == "" {
		t.SSHDest, t.ControlPath = h.SSHDest, h.ControlPath
	}
	return t
}

// IsLocalServer reports whether this host's panes run on THIS machine — which is what
// decides whether their pids may be looked up in the local process table.
//
// It is an explicit FLAG rather than anything derived, and that is the point. The
// obvious derivations are both wrong:
//
//   - `!Remote()` — the absence of an ssh destination — was the original rule and it was
//     a hole in the guard. A forwarded socket handed over as
//     `--host label=/run/user/1000/nuc.sock` with no `ssh=` answers no to `Remote()`
//     while being somebody else's machine entirely, so the identity walk that gates
//     every write answered from the LOCAL process table using REMOTE pane pids. Measured
//     on this machine: 97 of 3117 live local pids report "an agent is at or under this
//     pid", pid 1 among them — so a remote pane whose pane_pid collided with one of those
//     was marked identified, stamped, and written to as a single fresh target with NO
//     confirmation dialog.
//   - `Socket == tmux.LocalSocket()` — only the default socket counts — is wrong the
//     other way: `tmux -S /tmp/whatever` is still this machine, and that rule made every
//     non-default local server read-only.
//
// And the hub cannot tell the two apart by looking: a forwarded unix socket and a local
// tmux socket are both just paths. `SO_PEERCRED` would answer, but the dial that reads it
// runs only on failure, by design, because it costs a full read timeout per host per tick.
//
// So the hub does not guess. Whoever adds the host says. `AddLocal` sets it, because that
// is the one place this machine's own server enters; a `--host` entry sets it only when
// the user marks the socket as local. Default false means unknown, unknown means no local
// process lookup, and no lookup means the host identifies nothing and is read-only —
// which is exactly what the README promises for a host the hub cannot walk.
func (h Host) IsLocalServer() bool { return h.LocalProc }

type Poller struct {
	run tmux.Runner
	reg *registry.Registry

	// mu guards BOTH fields below, and the host slice is the half that was missing.
	//
	// Add is called from bubbletea's Update goroutine (the picker's save connects the
	// hosts it just enabled) while a tick is in flight in a tea.Cmd goroutine. Unguarded,
	// `append` reallocates and the tick's per-host goroutines then write their Status and
	// Reason into the ABANDONED backing array while the tick copies out of the new one —
	// so the writes are silently discarded, and the visible symptom is a host snapping
	// back to `connecting` after a save. `go test -race ./...` was 16/16 green with that
	// present, because no test called Add against a live Tick.
	//
	// The lock is never held across a tick: a remote tick is ~1.4 s and Add runs on the
	// UI goroutine, so holding it would freeze the dashboard for the length of a poll.
	// Instead each tick SNAPSHOTS under the lock, works on its own copy, and merges the
	// fields it owns back under the lock — which is also what stops Tick and TickAgents
	// clobbering each other, the race docs/known-issues recorded separately.
	mu     sync.Mutex
	hosts  []Host
	probed map[string]bool // label -> the field assertion has passed

	selfID     string // the hub's own pane, excluded from its own server's results
	selfSocket string // the socket of the server that pane lives on
}

func NewPoller(r tmux.Runner, reg *registry.Registry) *Poller {
	return &Poller{run: r, reg: reg, probed: map[string]bool{},
		selfID: SelfPaneID(), selfSocket: SelfSocket()}
}

// SelfPaneID is the pane the hub is running in, or "" when it is not inside
// tmux. It comes from the environment rather than from tmux, because asking tmux
// "which pane am I" over a socket has no answer: the hub is not an attached
// client.
func SelfPaneID() string { return os.Getenv("TMUX_PANE") }

// SelfSocket is the socket of the tmux server the hub is running on, or "" when
// it is not inside tmux. Exclusion keys on the SOCKET rather than on a host
// label, because the server the hub lives on is not necessarily registered as
// "local" — pointed at its own server under another label, the hub logged and
// rendered its own pane again.
func SelfSocket() string {
	v := os.Getenv("TMUX")
	if v == "" {
		return ""
	}
	if i := strings.IndexByte(v, ','); i > 0 {
		return v[:i]
	}
	return v
}

// SelfSessionID is the session the hub's own pane belongs to, as a tmux session
// target (`$N`), or "" when the hub is not inside tmux.
//
// $TMUX is `socket,server_pid,session_id` and the third field is a BARE number,
// while every session target needs the $ sigil — so `0` must become `$0` or the
// command would address a session NAMED 0. Task 1's shape for switch-client
// refuses the bare form at the seam, but the conversion belongs here, where the
// field is read.
//
// A malformed or non-numeric third field returns "" rather than a guess: an empty
// answer disables possession and falls back to today's full-screen attach, which
// is the safe direction.
func SelfSessionID() string {
	v := os.Getenv("TMUX")
	if v == "" {
		return ""
	}
	parts := strings.Split(v, ",")
	if len(parts) < 3 || parts[2] == "" {
		return ""
	}
	for _, c := range parts[2] {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return "$" + parts[2]
}

// excludeSelf drops the hub's own pane. Without it the hub polls the pane it
// lives in, captures its own screen, renders that screen in a tile, and the tile
// then contains its own screen — an infinite mirror, observed live. It also
// spends a full-screen capture per tick on itself.
//
// It applies to the hub's OWN server only, identified by socket. Pane ids are
// per-server, so $TMUX_PANE names a pane on that server and nowhere else —
// applying it everywhere silently deleted a remote host's %0, observed against a
// real host where the dropped pane was the one asking a question.
func (p *Poller) excludeSelf(socket string, ds []tmux.Delta) []tmux.Delta {
	if p.selfID == "" || p.selfSocket == "" || socket != p.selfSocket {
		return ds
	}
	out := ds[:0:0]
	for _, d := range ds {
		if d.PaneID == p.selfID {
			continue
		}
		out = append(out, d)
	}
	return out
}

// Add registers a host to poll. Safe to call while a tick is in flight — see the note on
// Poller.mu for what happened when it was not.
func (p *Poller) Add(h Host) {
	h.Status = Connecting
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hosts = append(p.hosts, h)
}

// Remove takes a host out of the fleet and reports whether it was there. It is Add's
// mirror, and the reason it exists is that without it a host the user turns OFF keeps its
// row forever.
//
// Measured before it did: after untick + enter, with the master confirmed stopped, one tick
// — which replaces the caller's fleet wholesale from this list — put the host back reading
// `connecting (waiting for its ssh master)`, and in a session where it had answered before,
// `down` carrying the full `respawn it with ssh -N -M -S …` sentence that explainTransport
// itself calls a remedy the operator must NOT act on. Permanently, because the only two
// things that spawn a master are startup and the picker's connect, and neither will ever
// include a disabled host. tickHost states the principle: a status that can never resolve
// is worse than a wrong one.
//
// Safe against a tick in flight for the same reason Add is: the merge matches by label, so
// a poll that started before the host left does not resurrect it.
func (p *Poller) Remove(label string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.hosts {
		if p.hosts[i].Label != label {
			continue
		}
		p.hosts = append(p.hosts[:i], p.hosts[i+1:]...)
		return true
	}
	return false
}

// LocalLabel is the host label for the machine the hub runs on.
const LocalLabel = "local"

// AddLocal registers the local server, whose socket is derived rather than
// discovered so that even the first call carries an explicit -S.
func (p *Poller) AddLocal() Host {
	h := Host{Label: LocalLabel, Socket: tmux.LocalSocket(), Status: Connecting, LocalProc: true}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hosts = append(p.hosts, h)
	return h
}

// Tick polls every host once and returns their statuses. wantFull holds
// "host\x00paneID" identities whose tile is on screen; only those get a
// full-screen capture, because that is the only source of tile content and it
// costs about 3.6 KB per pane against the zone's 650 B.
//
// Hosts are polled CONCURRENTLY. A remote tick is ~1.4 s against a local one's
// ~5 ms, so a serial loop made the local dashboard update at the speed of the
// slowest host — and at eight hosts it would be unusable.
func (p *Poller) Tick(ctx context.Context, now time.Time, wantFull map[string]bool) []Host {
	// A private copy, so the per-host goroutines below write into an array nothing else
	// can reach. Handing out `&p.hosts[i]` is what made a concurrent Add corrupt a tick.
	hosts := p.snapshot()
	var wg sync.WaitGroup
	for i := range hosts {
		wg.Add(1)
		go func(h *Host) {
			defer wg.Done()
			p.tickHost(ctx, h, now, wantFull)
		}(&hosts[i])
	}
	wg.Wait()
	return p.mergeTransport(hosts)
}

// TickAgents asks each host for Claude's own account of its sessions. It is
// SEPARATE from Tick and meant for a slower timer: measured, the listing costs
// ~200 ms locally but 0.5-2.8 s over ssh, so putting it in the tick would drag
// the dashboard down to the speed of the slowest `claude` invocation for data
// that changes on a human timescale (docs/design.md §17).
func (p *Poller) TickAgents(ctx context.Context, now time.Time) []Host {
	hosts := p.snapshot()
	// The registry attributes a session that SEVERAL hosts report — what a shared `~/.claude`
	// produces — to one of them, and the tiebreak is this order. It has to be told rather than
	// inferred, because the fan-out below is concurrent: measured on the real fleet, the order
	// UpdateAgents arrived in gave a remote host all 26 shared rows while `local` was polled first.
	labels := make([]string, 0, len(hosts))
	for _, h := range hosts {
		labels = append(labels, h.Label)
	}
	p.reg.SetHostOrder(labels)
	var wg sync.WaitGroup
	for i := range hosts {
		wg.Add(1)
		go func(h *Host) {
			defer wg.Done()
			f := p.fetcherFor(*h)
			if f == nil {
				return
			}
			ss, err := f.Fetch(ctx)
			if err != nil {
				// Not having claude is not a fault of the host, so this never
				// changes Status — it only explains an empty agent list.
				h.AgentsReason = err.Error()
				return
			}
			h.AgentsReason = ""
			p.reg.UpdateAgents(h.Label, ss, now)
		}(&hosts[i])
	}
	wg.Wait()
	return p.mergeAgents(hosts)
}

// snapshot copies the fleet under the lock. The copy is what each tick then works on, so
// the lock is held for a memcpy rather than for a poll.
func (p *Poller) snapshot() []Host {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.copyLocked()
}

// copyLocked returns the fleet as callers see it. mu must be held.
func (p *Poller) copyLocked() []Host {
	out := make([]Host, len(p.hosts))
	copy(out, p.hosts)
	return out
}

// mergeTransport writes back what a poll learns and leaves AgentsReason alone, because
// TickAgents owns that field and runs on its own 20 s timer.
//
// FIELD ownership rather than a whole-struct write is the point, and it is what closes the
// race docs/known-issues recorded separately: the two ticks each used to copy the whole
// struct out while the other wrote a field into it, so either could lose the other's work.
//
// Matched by LABEL, never by index, so the fleet changing under a tick is harmless: a host
// added mid-tick keeps its `connecting` until the next one, and a host REMOVED mid-tick is
// not resurrected by a poll that started before it left.
func (p *Poller) mergeTransport(fresh []Host) []Host {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, f := range fresh {
		for i := range p.hosts {
			if p.hosts[i].Label != f.Label {
				continue
			}
			agents := p.hosts[i].AgentsReason
			p.hosts[i] = f
			p.hosts[i].AgentsReason = agents
			break
		}
	}
	return p.copyLocked()
}

// mergeAgents writes back the ONE field TickAgents owns.
func (p *Poller) mergeAgents(fresh []Host) []Host {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, f := range fresh {
		for i := range p.hosts {
			if p.hosts[i].Label == f.Label {
				p.hosts[i].AgentsReason = f.AgentsReason
				break
			}
		}
	}
	return p.copyLocked()
}

// fetcherFor returns the listing command for a host, or nil when there is no way
// to run one: a host reached only by a forwarded socket has no shell to run
// `claude` in, which is why §17's second producer needs the ssh master that §8
// already requires for attach.
func (p *Poller) fetcherFor(h Host) agents.Fetcher {
	switch {
	case h.Remote() && h.ControlPath != "":
		return agents.OverSSH(h.ControlPath, h.SSHDest, 30*time.Second)
	case h.IsLocalServer():
		// IsLocalServer, not !Remote(): a forwarded socket with no ssh= is somebody
		// else's machine, and running the listing HERE attributed this machine's Claude
		// sessions to that host's label — measured, 13 local agent rows appeared under a
		// remote label in `--status`.
		return agents.Local(30 * time.Second)
	default:
		return nil
	}
}

func (p *Poller) tickHost(ctx context.Context, h *Host, now time.Time, wantFull map[string]bool) {
	tgt := h.Target()
	down := func(err error) {
		h.Status, h.Reason = Down, reasonFor(err)
		p.reg.MarkHostStale(h.Label, now)
	}

	ds, err := tmux.FetchDeltas(ctx, p.run, tgt)
	if err != nil {
		// tmux could not answer. Only now ask the kernel WHY, because classifying
		// a live socket costs a full read timeout — the dial can only prove
		// "live" by waiting to NOT get an EOF, so putting it on the happy path
		// added seconds per host per tick and starved the poll loop. As a
		// diagnostic it costs nothing when nothing is wrong.
		p.explainFailure(h, err, now)
		return
	}
	// tmux ANSWERED, whatever it went on to say. Seen is set here rather than at each
	// of the five outcomes below because every one of them is an answer — an empty
	// server, a missing format field and a full snapshot all prove the transport works,
	// and that is the whole content of the flag.
	h.Seen = true
	// NOTE the ordering: the hub's own pane is dropped AFTER the batch, not
	// before. The batch's label queries are `list-panes -a`, which returns every
	// pane, so a filtered delta list made the reader expect one line fewer per
	// label block and the next block's first line was misread as a frame —
	// observed as `bad frame line "%1|zsh"` with the hub inside tmux. The cost of
	// including it is one zone capture; a full capture is never requested for it.
	if len(ds) == 0 {
		h.Status = UpEmpty
		h.Reason = "no tmux server here — start one, or leave this host off"
		return
	}
	if len(p.excludeSelf(h.Socket, ds)) == 0 {
		h.Status = UpEmpty
		h.Reason = "nothing here but this hub — start a session, or leave this host off"
		return
	}

	p.mu.Lock()
	done := p.probed[h.Label]
	p.mu.Unlock()
	if !done {
		rep, err := tmux.AssertFields(ctx, p.run, tgt, ds[0].PaneID)
		if err != nil {
			down(err)
			return
		}
		if len(rep.Missing) > 0 {
			h.Status = DegradedFormat
			h.Reason = fmt.Sprintf("tmux %s returned nothing for: %s",
				rep.Version, strings.Join(rep.Missing, ", "))
			return
		}
		h.Version = rep.Version
		p.mu.Lock()
		p.probed[h.Label] = true
		p.mu.Unlock()
	}

	// One batch for labels, every pane's zone, and full captures of the on-screen
	// tiles. Measured over ssh: each separate invocation costs a round trip
	// (~500 ms), so the earlier per-query, per-capture shape made a tick with six
	// tiles take about four and a half seconds.
	snap, err := tmux.FetchSnapshot(ctx, p.run, tgt, ds, wantFull)
	if err != nil {
		down(err)
		return
	}

	sinceLast := time.Duration(0)
	if !h.LastOK.IsZero() {
		sinceLast = now.Sub(h.LastOK)
	}
	ds = p.excludeSelf(h.Socket, snap.Deltas)
	p.reg.Update(h.Label, ds, snap.Labels, filterZones(snap.Zones, ds), snap.Fulls, now, sinceLast)
	h.Status, h.Reason, h.LastOK = Up, "", now
}

// filterZones keeps only the captures whose pane survived exclusion, preserving
// order so the registry pairs them with the right delta.
func filterZones(zones []tmux.Capture, keep []tmux.Delta) []tmux.Capture {
	want := make(map[string]bool, len(keep))
	for _, d := range keep {
		want[d.PaneID] = true
	}
	out := zones[:0:0]
	for _, z := range zones {
		if want[z.PaneID] {
			out = append(out, z)
		}
	}
	return out
}

// explainFailure asks the kernel what tmux could not tell us. tmux answers
// `server exited unexpectedly` both for a reachable host with no server and for
// several genuinely broken cases, so the socket's own state is what separates
// "there is nothing running over there" from "the tunnel is gone" — measured
// against a real host with tmux installed and not started: the dial is accepted
// and the first read returns EOF, where a live server holds the connection.
func (p *Poller) explainFailure(h *Host, err error, now time.Time) {
	defer p.reg.MarkHostStale(h.Label, now)

	// A host reached over ssh has NO socket to dial (§5 deleted the forward), so the
	// only evidence is what ssh and the far tmux said — and Run has already rewritten
	// a dead master into a sentence naming the respawn command. Dialling "" here would
	// report `cannot reach its socket: dial unix: missing address` about a host that
	// is designed not to have one, which is the message hardest to act on: it names a
	// mechanism this host does not use.
	if h.Target().Remote() {
		s := err.Error()
		switch {
		case strings.Contains(s, "no server running"), strings.Contains(s, "error connecting"):
			// The far tmux answered and said there is no server there. Measured over a
			// real master (docs/design.md §5): rc=1 with
			// `error connecting to /tmp/tmux-1000/default (No such file or directory)`.
			// That path is on the FAR machine, so the local socket taxonomy would call
			// a perfectly reachable host's missing server "the tunnel is down".
			//
			// It is an ANSWER, so it sets Seen: a later transport failure on this host is
			// a real fault rather than a master that has not come up yet.
			h.Seen = true
			h.Status = UpEmpty
			h.Reason = "reachable, but no tmux server is running there — start one, or leave this host off"
		case !h.Seen:
			// Never answered, and the hub is still bringing this host's master up ITSELF.
			// `down` would be a claim we have no basis for — down means it answered before
			// and stopped, or we know it is gone — and this is the state §16 calls
			// `connecting`: "remote hosts fold in as they answer".
			//
			// The reason is deliberately SHORT and carries no remedy. What ssh says here
			// is `no live ssh master at … — respawn it with ssh -N -M -S …`, which is a
			// remedy the operator must NOT act on (the hub is spawning that master) and
			// which is long enough to be truncated mid-word on the one line the footer
			// gives a host.
			h.Status = Connecting
			h.Reason = "waiting for its ssh master"
		default:
			h.Status, h.Reason = Down, reasonFor(err)
		}
		return
	}
	// The local path deliberately has no `connecting` case. Nothing is being spawned for
	// a socket the operator named, so a first failure there is the answer rather than an
	// interval before one — and a status that can never resolve is worse than a wrong one.

	tr, peer, derr := Dial(h.Socket, 300*time.Millisecond)
	h.Peer = peer
	switch {
	case derr != nil:
		h.Status, h.Reason = Down, "cannot reach its socket: "+derr.Error()
	case tr == TransportAbsent:
		h.Status = Down
		h.Reason = "nothing is listening on its socket — the tunnel is down or was never built"
	case tr == TransportEmpty:
		// Something is listening, so the socket answered: Seen, for the same reason as
		// the remote branch above.
		h.Seen = true
		h.Status = UpEmpty
		h.Reason = "reachable, but no tmux server is running there — start one, or leave this host off"
	default:
		// Something is listening and holding the connection, so the socket is
		// fine and tmux itself refused. Report what tmux said.
		h.Status, h.Reason = Down, reasonFor(err)
	}
}

// reasonFor turns a failure into a sentence naming the remedy. The strings tmux
// and the OS produce are matched loosely on purpose: the classification must not
// depend on an exact message, and an unrecognised failure falls through with its
// own text rather than being flattened.
func reasonFor(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "no live ssh master"):
		// Already a remedy: internal/tmux's explainTransport names the host, the
		// control path and the `ssh -N -M` that fixes it. It must be tested FIRST,
		// because ssh's own text is kept after that remedy and it contains
		// `Control socket connect(…): No such file or directory` — which the socket
		// case below would rewrite into "the tunnel is down", a mechanism this
		// design does not have.
		//
		// The send-shaped PREAMBLE is dropped. explainTransport writes `host "x" was not
		// reached and nothing was sent: ` because its commonest caller IS a send, and nothing
		// was being sent here — this is a poll, and the footer already says which host and that
		// it is down. It also cost the 49 columns the fault and its remedy need: at 80 columns
		// what survived of this reason named neither.
		if i := strings.Index(s, "no live ssh master"); i > 0 {
			return s[i:]
		}
		return s
	case strings.Contains(s, "no server running"), strings.Contains(s, "server exited unexpectedly"):
		return "no tmux server on that socket — start one, or leave this host off"
	case strings.Contains(s, "error connecting"), strings.Contains(s, "No such file"):
		return "socket is not there — the tunnel is down or was never built"
	case strings.Contains(s, "deadline exceeded"):
		return "tmux did not answer in time — the link is stalled, retrying"
	case strings.Contains(s, "protocol version mismatch"):
		return "tmux version mismatch — this host needs the per-call fallback"
	default:
		return s
	}
}
