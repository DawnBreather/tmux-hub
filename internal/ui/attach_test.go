package ui

import (
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/registry"

	"github.com/DawnBreather/tmux-hub/internal/project"
)

func TestAttachCmdTargetsTheSessionOnTheRightSocket(t *testing.T) {
	p := registry.Pane{Host: "local", PaneID: "%3", Session: "live1", SessionID: "$2"}
	c, err := AttachCmd(p, hub.Host{Label: "local", Socket: "/run/user/1000/tmux-hub/local.sock"})
	if err != nil {
		t.Fatalf("AttachCmd: %v", err)
	}
	got := strings.Join(c.Args, " ")
	want := "tmux -S /run/user/1000/tmux-hub/local.sock attach -t $2"
	if got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

// With $TMUX set, plain `tmux attach` is refused — so a hub started from inside
// tmux could never attach to anything. The variable must be gone from the child.
func TestAttachCmdStripsTMUX(t *testing.T) {
	p := registry.Pane{Host: "local", PaneID: "%0", Session: "s"}
	c, err := AttachCmd(p, hub.Host{Label: "local", Socket: "/tmp/x.sock"})
	if err != nil {
		t.Fatalf("AttachCmd: %v", err)
	}
	for _, e := range c.Env {
		if strings.HasPrefix(e, "TMUX=") {
			t.Fatalf("child environment still carries %q", e)
		}
	}
}

func TestWithoutTMUXKeepsEverythingElse(t *testing.T) {
	in := []string{"PATH=/bin", "TMUX=/tmp/s,123,0", "TMUX_PANE=%4", "TERM=xterm"}
	got := withoutTMUX(in)
	if len(got) != 3 {
		t.Fatalf("got %v, want the three non-TMUX entries", got)
	}
	// TMUX_PANE is informational and tmux does not gate on it, so it stays —
	// the hub uses it to recognise its own pane.
	joined := strings.Join(got, " ")
	for _, want := range []string{"PATH=/bin", "TMUX_PANE=%4", "TERM=xterm"} {
		if !strings.Contains(joined, want) {
			t.Errorf("lost %q", want)
		}
	}
}

func TestAttachCmdRefusesWhatItCannotDo(t *testing.T) {
	full := registry.Pane{Host: "h", PaneID: "%1", Session: "s"}
	if _, err := AttachCmd(full, hub.Host{Label: "h"}); err == nil {
		t.Error("no socket: want an error naming the host")
	}
	noSess := registry.Pane{Host: "h", PaneID: "%1"}
	if _, err := AttachCmd(noSess, hub.Host{Label: "h", Socket: "/tmp/s.sock"}); err == nil {
		t.Error("no session name: want an error naming the pane")
	}
	remoteNoCtl := registry.Pane{Host: "nuc", PaneID: "%1", Session: "s"}
	if _, err := AttachCmd(remoteNoCtl, hub.Host{Label: "nuc", Socket: "/tmp/s.sock", SSHDest: "nuc"}); err == nil {
		t.Error("remote host with no control path: want an error")
	}
}

func TestHostForFindsTheHost(t *testing.T) {
	hosts := []hub.Host{{Label: "local", Socket: "/a.sock"}, {Label: "nuc", Socket: "/b.sock"}}
	if h, ok := hostFor(hosts, "nuc"); !ok || h.Socket != "/b.sock" {
		t.Errorf("hostFor(nuc) = %+v, %v", h, ok)
	}
	if _, ok := hostFor(hosts, "nope"); ok {
		t.Error("hostFor(unknown) should not be found")
	}
}

// A forwarded socket cannot carry an attach: measured, it fails "open terminal
// failed: not a terminal" even from a real pty, because the terminal fd travels
// over SCM_RIGHTS and a forward drops ancillary data. A remote attach must go
// through the ssh master, and this was broken for every remote pane.
func TestRemoteAttachGoesThroughTheSSHMaster(t *testing.T) {
	p := registry.Pane{Host: "nuc", PaneID: "%0", Session: "trainings", SessionID: "$7"}
	h := hub.Host{Label: "nuc", Socket: "/run/user/1000/tmux-hub/nuc.sock",
		SSHDest: "nuc", ControlPath: "/run/user/1000/tmux-hub/nuc.ctl"}
	c, err := AttachCmd(p, h)
	if err != nil {
		t.Fatalf("AttachCmd: %v", err)
	}
	got := strings.Join(c.Args, " ")
	// The target is quoted for the REMOTE shell; the test above is where that lives.
	want := "ssh -S /run/user/1000/tmux-hub/nuc.ctl -t nuc tmux attach -t '$7'"
	if got != want {
		t.Fatalf("args = %q\nwant   %q", got, want)
	}
	if strings.Contains(got, h.Socket) {
		t.Error("the forwarded socket must not appear in a remote attach")
	}
}

// The remote path has TWO shells and only the near one was ever handled. ssh joins
// its command arguments into ONE string and hands that to the REMOTE user's shell,
// so a session id reaches tmux expanded: measured over a live control master,
// `ssh -S <ctl> nuc 'echo -t $0'` printed `-t bash`, and end to end the argv this
// function used to build failed `can't find session: bash` (rc=1, 0 clients) while
// the same argv with the target quoted attached, 1 client, remote status line on
// screen. Remote attach had therefore never worked for any session.
//
// The two levels are different shells and both are needed. shellJoin protects the
// payload from the shell TMUX runs it through on THIS machine (§20's window path);
// this quoting protects the target from the shell on the FAR side, which the
// full-screen path reaches with no local shell involved at all.
//
// The literals are written out by hand. Deriving either from the code under test is
// what would let a one-sided fix ship: `shellQuote(x) == shellQuote(x)` cannot fail.
//
// LOCAL must stay bare, and that is the half a careless fix breaks: exec runs the
// local form with no shell at all, so a quoted target makes tmux look for a session
// literally NAMED `'$0'`.
//
// No e2e arm exists for this, and cannot: it needs a live ssh control master to a
// real host, which the suite must not assume. The measurements quoted above are the
// end-to-end evidence, and they are the design.md §12 limitation coming due.
func TestRemoteAttachQuotesTheTargetForTheRemoteShellAndLocalDoesNot(t *testing.T) {
	p := registry.Pane{Host: "nuc", PaneID: "%0", Session: "dev", SessionID: "$0"}
	remote := hub.Host{Label: "nuc", Socket: "/run/user/1000/tmux-hub/nuc.sock",
		SSHDest: "nuc", ControlPath: "/run/user/1000/tmux-hub/nuc.ctl"}
	c, err := AttachCmd(p, remote)
	if err != nil {
		t.Fatalf("AttachCmd: %v", err)
	}
	if got, want := c.Args[len(c.Args)-1], `'$0'`; got != want {
		t.Errorf("remote target = %q, want %q — the remote shell expands a bare $0", got, want)
	}
	// The rest of the argv is not quoted: ssh's own flags are ssh's, not the remote
	// shell's, and quoting them would send the quotes to the far side as text.
	if got, want := strings.Join(c.Args, " "),
		`ssh -S /run/user/1000/tmux-hub/nuc.ctl -t nuc tmux attach -t '$0'`; got != want {
		t.Errorf("remote argv = %q\n              want %q", got, want)
	}

	local := hub.Host{Label: "local", Socket: "/run/user/1000/tmux-hub/local.sock"}
	lp := registry.Pane{Host: "local", PaneID: "%0", Session: "dev", SessionID: "$0"}
	lc, err := AttachCmd(lp, local)
	if err != nil {
		t.Fatalf("AttachCmd(local): %v", err)
	}
	if got, want := lc.Args[len(lc.Args)-1], `$0`; got != want {
		t.Errorf("local target = %q, want %q — exec passes it to tmux with no shell in between", got, want)
	}
}

// A session NAME is the fallback target, and it is the one that can carry a quote of
// its own: `tmux rename-session "it's mine"` is legal. Quoting has to survive that,
// or the fix trades an expansion defect for an unterminated string on the far side.
func TestTheRemoteTargetSurvivesAQuoteInASessionName(t *testing.T) {
	p := registry.Pane{Host: "nuc", PaneID: "%0", Session: `it's mine`}
	h := hub.Host{Label: "nuc", SSHDest: "nuc", ControlPath: "/tmp/ctl"}
	c, err := AttachCmd(p, h)
	if err != nil {
		t.Fatalf("AttachCmd: %v", err)
	}
	// Single quotes cannot carry a single quote, so it is closed, escaped, reopened.
	if got, want := c.Args[len(c.Args)-1], `'it'\''s mine'`; got != want {
		t.Fatalf("remote target = %q, want %q", got, want)
	}
}

// A failure to attach must be said out loud, not swallowed: the user pressed a
// key and nothing happened otherwise.
//
// It used to assert the note REPLACED the host line, which is the behaviour known-issues M1
// records as a defect with "fix when touched" — the one positive assertion about fleet health
// vanished at the moment the operator was acting. The note still outranks the fleet, which was
// always the right priority; what changed is that the two now SHARE the row, so this asserts
// the note is FIRST rather than alone.
func TestANoteOutranksTheHostLineWithoutErasingIt(t *testing.T) {
	out := Render(Frame{Panes: samplePanes(), Hosts: []hub.Host{{Label: "local", Status: hub.Up}}, Width: 80, Height: 24, Cursor: 0, Marked: nil, Note: "cannot attach: no socket known for host \"nuc\"", Hint: "", HiddenCount: 0, BlockedCount: 0, Resurfaced: nil, Aliases: project.Aliases{}})
	if !strings.Contains(out, "cannot attach") {
		t.Fatalf("the note is not on screen:\n%s", out)
	}
	foot := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			foot = line
		}
	}
	if i, j := strings.Index(foot, "cannot attach"), strings.Index(foot, "local"); j >= 0 && i > j {
		t.Fatalf("the note is not first on the footer: %q", foot)
	}
	// And the fleet is either beside it or marked as dropped — never gone with no sign.
	if !strings.Contains(foot, "local") && !strings.Contains(foot, "+") {
		t.Fatalf("the host line vanished behind the note with no sign it exists: %q", foot)
	}
}

// A session NAME does not survive a rename — measured, `has-session -t <old>`
// fails rc=1 right after one — so attach targets the id and only falls back to
// the name when the id is not known yet.
func TestAttachPrefersTheSessionIDOverItsName(t *testing.T) {
	withID := registry.Pane{Host: "local", PaneID: "%1", Session: "will-be-renamed", SessionID: "$4"}
	c, err := AttachCmd(withID, hub.Host{Label: "local", Socket: "/s.sock"})
	if err != nil {
		t.Fatalf("AttachCmd: %v", err)
	}
	if got := strings.Join(c.Args, " "); !strings.HasSuffix(got, "attach -t $4") {
		t.Fatalf("args = %q, want the session id as the target", got)
	}
	nameOnly := registry.Pane{Host: "local", PaneID: "%1", Session: "only-a-name"}
	c2, err := AttachCmd(nameOnly, hub.Host{Label: "local", Socket: "/s.sock"})
	if err != nil {
		t.Fatalf("AttachCmd: %v", err)
	}
	if got := strings.Join(c2.Args, " "); !strings.HasSuffix(got, "attach -t only-a-name") {
		t.Fatalf("args = %q, want the name as a fallback", got)
	}
}

// An agent row's SessionID is Claude's own UUID, not a tmux `$N`. Nothing checked
// which, so `a` on an agent row ran `tmux attach -t <uuid>` and failed with a tmux
// error that says nothing about the real problem. Most agent sessions have no pane
// at all, so this is the common case rather than an edge one.
func TestAttachRefusesAnAgentRowWithAUsefulReason(t *testing.T) {
	p := registry.Pane{
		Host: "local", Kind: registry.KindAgent,
		PaneID:    "agent:1ff133f7",
		Session:   "dockerfile goldens across the fleet",
		SessionID: "1ff133f7-c34a-4c60-91e5-b0048842cc66",
	}
	_, err := AttachCmd(p, hub.Host{Label: "local", Socket: "/tmp/x"})
	if err == nil {
		t.Fatal("an agent row was accepted for attach")
	}
	// The message must name the thing and say why, not just refuse: a bare
	// "cannot attach" trains the user to stop reading.
	for _, want := range []string{"Claude session", "nothing to attach"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if !strings.Contains(err.Error(), p.Session) {
		t.Errorf("error %q does not name the session", err)
	}
}

// A real pane row must still attach — the fix must not refuse the normal case.
func TestAttachStillAcceptsAPaneRow(t *testing.T) {
	p := registry.Pane{Host: "local", Kind: registry.KindPane,
		PaneID: "%3", Session: "work", SessionID: "$0"}
	c, err := AttachCmd(p, hub.Host{Label: "local", Socket: "/tmp/x"})
	if err != nil {
		t.Fatalf("a pane row was refused: %v", err)
	}
	if !strings.Contains(strings.Join(c.Args, " "), "attach -t $0") {
		t.Errorf("args = %v", c.Args)
	}
}
