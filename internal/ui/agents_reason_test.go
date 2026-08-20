package ui

import (
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/agents"
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/lines"
)

// A host whose Claude listing failed said NOTHING on any screen.
//
// `AgentsReason` was written by the poller and read by `internal/hub/report.go:46`, so it reached
// the machine-readable `--status` JSON and no TUI surface. The host's tmux panes render normally,
// which is what makes the silence dangerous: the screen looks complete while every pane-less row
// for that host is missing. §22.6 binds this to the same commit as `--all`; `--all` shipped alone.
//
// A remote host with no `claude` at all sets no reason by design — the payload short-circuits with
// an empty listing — so the set this covers is a timeout, a non-zero exit from a claude that IS
// installed, an ssh failure, a decode failure, and locally `ErrNotInstalled`.

func hostsWithAgentsReason() []hub.Host {
	return []hub.Host{
		{Label: "local", Status: hub.Up, Version: "3.7b", LocalProc: true},
		// The reason comes from the PRODUCER, not from intuition: every string the fetcher
		// can store already begins with `agents:`, and a hand-written fixture that did not
		// is how a doubled `agents: agents:` prefix passed a green test.
		{Label: "nuc", Status: hub.Up, Version: "3.2a", SSHDest: "nuc",
			AgentsReason: agents.ErrNotInstalled.Error()},
	}
}

// It is a PART of the footer's Fit list, not a replacement for anything: the host identities and the
// operator's own counts outrank it, and it degrades by being dropped with a marker rather than by
// pushing something else off the row. That is the rule three footer defects on this branch were paid
// for.
func TestTheFooterSaysWhyAHostsListingIsMissing(t *testing.T) {
	got := hostLine(hostsWithAgentsReason(), nil, hiddenTally{}, 160)

	if !strings.Contains(got, "nuc up") {
		t.Fatalf("the host identity is gone, which is the one thing that must never drop: %q", got)
	}
	if !strings.Contains(got, agents.ErrNotInstalled.Error()) {
		t.Errorf("the footer does not carry the producer's own reason: %q", got)
	}
	// The producer's strings already begin with `agents:`, which is the word that says WHAT
	// could not be listed — so the renderer must not add a second one.
	if strings.Contains(got, "agents: agents:") {
		t.Errorf("the reason is prefixed twice: %q", got)
	}
	// And the host that is fine must not acquire a reason.
	if n := strings.Count(got, "claude is not installed"); n != 1 {
		t.Errorf("the reason is on %d hosts, want exactly the failing one: %q", n, got)
	}
}

// A host that is DOWN keeps priority: its transport reason is the actionable half, and a listing
// that could not run is a consequence of the host being unreachable rather than a second fault.
func TestATransportReasonOutranksAListingReason(t *testing.T) {
	hosts := []hub.Host{
		{Label: "dead", Status: hub.Down, Reason: "no live ssh master at /run/x",
			AgentsReason: "ssh: connect failed"},
		{Label: "local", Status: hub.Up, LocalProc: true},
	}
	// Wide enough for the identities and ONE reason, narrow enough that both cannot fit.
	got := hostLine(hosts, nil, hiddenTally{}, 62)

	if !strings.Contains(got, "dead down") {
		t.Fatalf("the failing host's identity is gone: %q", got)
	}
	if !strings.Contains(got, "no live ssh master") {
		t.Errorf("the transport reason lost to the listing reason: %q", got)
	}
}

// The narrow band the project commits to: at 80 columns the identities and the counts come first,
// and a reason that does not fit is dropped rather than truncating the row. Nothing may exceed the
// width — the defect this whole footer exists for was a hard truncation mid-word.
func TestTheListingReasonNeverOverflowsTheCommittedWidth(t *testing.T) {
	hosts := hostsWithAgentsReason()
	hosts[1].AgentsReason = "agents: deadline exceeded after 30s waiting for the far side to answer"
	for _, w := range []int{80, 100, 160, 200} {
		got := hostLine(hosts, nil, hiddenTally{Marked: 3, Waiting: 1}, w)
		if lines.Width(got) > w {
			t.Errorf("at %d columns the footer is %d wide: %q", w, lines.Width(got), got)
		}
		if !strings.Contains(got, "local up") || !strings.Contains(got, "nuc up") {
			t.Errorf("at %d columns a host identity was dropped for a reason: %q", w, got)
		}
	}
}

// The tier-2 suppression asks whether the TRANSPORT is the cause, not whether the status differs
// from `Up`.
//
// `Status != Up` is true for `UpEmpty` (reachable, no tmux server) and `DegradedFormat` (a required
// format field came back empty) — both of which mean the host ANSWERED. A listing failure on such a
// host is an independent fault and belongs on the row; suppressing it hid the only thing that
// explained the missing pane-less rows. Only `Down` and `Connecting` mean the hub could not get
// there, and only then is the listing's failure a consequence rather than news.
func TestAHostThatAnsweredStillShowsItsListingFailure(t *testing.T) {
	for _, c := range []struct {
		name    string
		status  hub.Status
		reason  string
		wantSay bool
	}{
		{"up, no tmux server", hub.UpEmpty, "no tmux server on its socket", true},
		{"up, a format came back empty", hub.DegradedFormat, "window_activity was empty", true},
		{"unreachable", hub.Down, "no live ssh master at /run/x", false},
		{"still connecting", hub.Connecting, "waiting for its ssh master", false},
	} {
		hosts := []hub.Host{
			{Label: "nuc", Status: c.status, Reason: c.reason,
				AgentsReason: agents.ErrNotInstalled.Error()},
		}
		got := hostLine(hosts, nil, hiddenTally{}, 200)
		said := strings.Contains(got, "claude is not installed")
		if said != c.wantSay {
			verb := "did not say"
			if said {
				verb = "said"
			}
			t.Errorf("%s: the footer %s why the listing failed: %q", c.name, verb, got)
		}
		// The transport reason is never lost either way — it is tier 1.
		if !strings.Contains(got, c.reason) {
			t.Errorf("%s: the transport reason is gone: %q", c.name, got)
		}
	}
}
