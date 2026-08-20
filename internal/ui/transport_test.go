package ui

import (
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/broadcast"
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/proc"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// socketless is a host as hosts.toml produces one: an alias, an ssh destination and
// the control path the hub chose. There is no socket, because §5 deleted the forward.
func socketless() hub.Host {
	return hub.Host{Label: "nuc", SSHDest: "nuc",
		ControlPath: "/run/user/1000/cm-deadbeef-nuc", Status: hub.Up}
}

// Every seam in this package used to write `tmux.Target{Label: h.Label, Socket: h.Socket}`
// by hand, so a host with no socket was addressed as a LOCAL server on an empty path —
// refused by tmux.build with ErrNoSocket. The direction of that failure is what makes it
// worth a test: the pane is polled and rendered (the poller has its own target), the
// STAMP fails, and a pane that is never stamped is silently read-only. The dashboard
// then shows a host whose every send is refused for want of a token nobody could write.
func TestASocketlessHostIsAddressableAtEverySeam(t *testing.T) {
	h := socketless()
	hosts := []hub.Host{h}

	// The kill and restart paths know a host by LABEL only.
	if got := targetFor(hosts, h.Label); !got.Remote() || got.ControlPath != h.ControlPath {
		t.Errorf("targetFor = %+v, want the ssh coordinates — `K` and `R` would otherwise "+
			"be refused on every host out of hosts.toml", got)
	}
	// An unknown label must not borrow another host's transport.
	if got := targetFor(hosts, "nobody"); got.Remote() || got.Socket != "" {
		t.Errorf("targetFor(unknown) = %+v, want a bare target", got)
	}

	// The stamp path: this is the one that decides whether the host is writable at all.
	p := agentPane(h.Label, "api", "work", "%1", 0, state.Works)
	m := modelWith(t, p)
	m.hosts = hosts
	m.keeper = broadcast.NewKeeper(nil)
	m.walk = func(hub.Host) proc.Walker { return nil } // no walk needed to read the target
	m.mark(p)
	jobs := m.identityJobs()
	if len(jobs) != 1 {
		t.Fatalf("identityJobs = %d, want one for the selected remote host", len(jobs))
	}
	if !jobs[0].target.Remote() || jobs[0].target.ControlPath != h.ControlPath {
		t.Errorf("the identity round would stamp through %+v — a stamp that cannot be "+
			"written leaves the host read-only with no explanation", jobs[0].target)
	}

	// And the send path itself.
	tg, dropped := m.targets()
	if dropped != 0 || len(tg) != 1 {
		t.Fatalf("targets() = %+v, dropped %d, want the one selected pane", tg, dropped)
	}
	if !tg[0].Tmux.Remote() || tg[0].Tmux.ControlPath != h.ControlPath {
		t.Errorf("the send would go to %+v, which is not this host", tg[0].Tmux)
	}
}

// A pane on a host the model does not know must not be sent to at all — the shape a
// stale registry entry takes after a host is removed. It is asserted beside the case
// above because both go through the same lookup, and only one of them may produce a
// target.
func TestAPaneOnAnUnknownHostIsDroppedRatherThanAddressed(t *testing.T) {
	p := agentPane("ghost", "api", "work", "%1", 0, state.Works)
	m := modelWith(t, p)
	m.hosts = []hub.Host{socketless()}
	m.mark(p)
	tg, dropped := m.targets()
	if len(tg) != 0 || dropped != 1 {
		t.Fatalf("targets() = %+v, dropped %d, want nothing addressed and one dropped", tg, dropped)
	}
}

// The footer is where a host's status is visible at all, and it is one line per host, so
// this asserts the FRAME rather than the struct: at 100 columns — §16's commitment is 80 —
// a host whose master is still coming up must read `connecting` with its short reason, and
// the word `down` must not appear anywhere on that line.
//
// It is the screen half of internal/hub's TestAHostThatHasNeverAnswered…: that one pins
// the classification, this one pins that the classification survives to the glass. The
// first frame of the program said `nuc down (list-panes rc=255: … no live ssh master at`,
// truncated mid-sentence, about a host that was healthy 1.5 s later.
func TestAConnectingHostReadsAsConnectingInTheFrame(t *testing.T) {
	m := base(t, 100, 14, agentPane("nuc", "api", "work", "%1", 0, state.Works))
	m.hosts = []hub.Host{{
		Label: "nuc", SSHDest: "nuc", ControlPath: "/run/user/1000/cm-deadbeef-nuc",
		Status: hub.Connecting, Reason: "waiting for its ssh master",
	}}

	out := m.View()
	if !strings.Contains(out, "nuc connecting (waiting for its ssh master)") {
		t.Fatalf("the footer does not carry the host's own words:\n%s", out)
	}
	// The line must not also say down, and must not carry the respawn command — the two
	// halves of what the first frame used to show.
	for _, forbidden := range []string{"nuc down", "ssh -N -M", "rc=255"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("the frame still contains %q:\n%s", forbidden, out)
		}
	}
	// And it fits. A reason long enough to be cut mid-word is known-issues L2, and the
	// remedy this replaced was 120 characters on its own.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "connecting") && len([]rune(line)) > 100 {
			t.Errorf("the host line is %d columns wide at a width of 100:\n%s",
				len([]rune(line)), line)
		}
	}
}
