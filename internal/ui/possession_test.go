package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/broadcast"
	"github.com/DawnBreather/tmux-hub/internal/hide"
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

func localHost() hub.Host {
	return hub.Host{Label: "local", Socket: "/tmp/tmux-1000/default", LocalProc: true}
}

func remoteHost() hub.Host {
	return hub.Host{Label: "nuc", Socket: "/run/user/1000/nuc.sock",
		SSHDest: "nuc", ControlPath: "/home/dev/.ssh/cm-nuc"}
}

func testPane(host, epoch string) registry.Pane {
	return registry.Pane{Kind: registry.KindPane, Host: host, Session: "api", Window: "review",
		PaneID: "%0", SessionID: "$1", WindowID: "@4", Epoch: epoch}
}

// The hub's own server: a jump, which is the case §20 exists for.
func TestSameEpochIsAJump(t *testing.T) {
	got, note := decidePossession(testPane("local", "999:111"), localHost(), "$0", "999:111")
	if got != pathJump {
		t.Fatalf("path = %v, want pathJump (note %q)", got, note)
	}
}

// Another server: a window holding the existing attach, never a jump. A
// switch-client against a pane id from a different server would either fail or,
// worse, find an unrelated session with that id.
func TestADifferentEpochIsAWindow(t *testing.T) {
	got, _ := decidePossession(testPane("nuc", "222:333"), remoteHost(), "$0", "999:111")
	if got != pathWindow {
		t.Fatalf("path = %v, want pathWindow", got)
	}
}

// §20's `$TMUX` empty row. Outside tmux there is no client to switch and no
// session to hold a window, so both new paths are impossible and the honest
// answer is today's behaviour rather than a refusal.
func TestOutsideTmuxEveryPathFallsBackToTheFullScreenAttach(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    registry.Pane
		h    hub.Host
	}{
		{"local target", testPane("local", "999:111"), localHost()},
		{"remote target", testPane("nuc", "222:333"), remoteHost()},
	} {
		got, note := decidePossession(tc.p, tc.h, "", "999:111")
		if got != pathFullScreen {
			t.Errorf("%s: path = %v, want pathFullScreen (note %q)", tc.name, got, note)
		}
	}
}

// An unknown own-server identity must not read as "same server". Two empty
// epochs compare equal, which would send switch-client at a remote pane.
func TestAnEmptySelfEpochIsNeverASameServerMatch(t *testing.T) {
	got, _ := decidePossession(testPane("local", ""), localHost(), "$0", "")
	if got == pathJump {
		t.Fatal("an unknown epoch matched itself and produced a jump")
	}
}

// An unknown epoch for a local non-remote host must fall back to pathFullScreen,
// because the window path does not strip $TMUX and tmux refuses same-socket
// nesting. A remote host with an unknown epoch still uses pathWindow.
func TestUnknownEpochFallbackDependsOnLocality(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    hub.Host
		want possessionPath
	}{
		{"local host unknown epoch", localHost(), pathFullScreen},
		{"remote host unknown epoch", remoteHost(), pathWindow},
	} {
		got, note := decidePossession(testPane(tc.h.Label, ""), tc.h, "$0", "")
		if got != tc.want {
			t.Errorf("%s: path = %v, want %v (note %q)", tc.name, got, tc.want, note)
		}
	}
}

// The two refusals already exist and already carry their fix. §20 does not
// reword them; it routes to them.
func TestTheRefusalsKeepNamingTheMissingThing(t *testing.T) {
	noCtl := remoteHost()
	noCtl.ControlPath = ""
	got, note := decidePossession(testPane("nuc", "222:333"), noCtl, "$0", "999:111")
	if got != pathRefuse {
		t.Fatalf("path = %v, want pathRefuse", got)
	}
	if !strings.Contains(note, "has no ssh control path") {
		t.Errorf("note = %q, want it to name the missing field", note)
	}

	agent := registry.Pane{Kind: registry.KindAgent, Host: "local", Session: "api"}
	got2, note2 := decidePossession(agent, localHost(), "$0", "999:111")
	if got2 != pathRefuse {
		t.Fatalf("path = %v, want pathRefuse", got2)
	}
	if !strings.Contains(note2, "nothing to attach to until it runs in one") {
		t.Errorf("note = %q, want the agent-row explanation", note2)
	}
}

// possessionRecorder records every argv the model sends, in order. failVerb, when
// set, is the one verb that answers rc=1 with tmux's own message — the shape of a
// target that vanished between the poll and the keypress.
//
// It answers `new-window` with nothing, which is what a real one prints: the window
// path used to read an id back with `-P -F '#{window_id}'` to set remain-on-exit on
// the window it had just made, and that ordering was the race this file's window
// tests now guard against. Nothing consumes an id any more.
type possessionRecorder struct {
	calls    [][]string
	failVerb string
	// windows is what the server answers for `list-windows`: the fixture's own session tree. Empty
	// means no window is showing anything, which is a first `a`.
	windows string
}

func (r *possessionRecorder) Run(ctx context.Context, t tmux.Target, args ...string) (tmux.Result, error) {
	argv := make([]string, len(args))
	copy(argv, args)
	r.calls = append(r.calls, argv)
	if r.failVerb != "" && len(args) > 0 && args[0] == r.failVerb {
		return tmux.Result{RC: 1, Stderr: "can't find " + args[0] + " target " + args[len(args)-1]}, nil
	}
	switch args[0] {
	case "list-windows":
		if r.windows != "" {
			return tmux.Result{Stdout: r.windows}, nil
		}
		// No window is showing this target: the default fixture is a first `a`.
		return tmux.Result{Stdout: "@4|$0|zsh\n"}, nil
	}
	return tmux.Result{Stdout: "999:111\n"}, nil
}

func (r *possessionRecorder) RunInput(ctx context.Context, t tmux.Target, stdin []byte, args ...string) (tmux.Result, error) {
	return r.Run(ctx, t, args...)
}

func assertCall(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q (full %q)", i, got[i], want[i], got)
		}
	}
}

// newTestModel is a model with the two hosts the possession tests use and a
// runner that records. It sets $TMUX so the hub counts as being inside tmux.
func newTestModel(t *testing.T, r tmux.Exec) model {
	t.Helper()
	t.Setenv("TMUX", "/tmp/tmux-1000/default,4242,0")
	s, err := hide.Open(filepath.Join(t.TempDir(), "hidden.json"))
	if err != nil {
		t.Fatal(err)
	}
	return model{
		panes:       []registry.Pane{testPane("local", "999:111"), testPane("nuc", "222:333")},
		hosts:       []hub.Host{localHost(), remoteHost()},
		hidden:      s,
		width:       120,
		height:      24,
		run:         r,
		ctx:         context.Background(),
		selfSession: hub.SelfSessionID(),
		atSelection: map[SelectionKey]paneSnapshot{},
		lastOutcome: map[SelectionKey]broadcast.Outcome{},
	}
}

// The jump must send the two commands §20 specifies, by ID, in that order.
// Order is not cosmetic: select-window addresses a window in the session the
// client is now displaying.
func TestTheJumpSendsSwitchClientThenSelectWindow(t *testing.T) {
	rec := &possessionRecorder{}
	m := newTestModel(t, rec)
	m.selfEpoch = "999:111"
	p := testPane("local", "999:111")
	_, cmd := m.possess(p, localHost())
	if cmd == nil {
		t.Fatal("possess returned no command for a same-server target")
	}
	msg := cmd()
	if got, ok := msg.(possessedMsg); !ok || got.err != nil {
		t.Fatalf("msg = %#v, want a clean possessedMsg", msg)
	}
	if len(rec.calls) != 2 {
		t.Fatalf("sent %d commands, want 2: %q", len(rec.calls), rec.calls)
	}
	assertCall(t, rec.calls[0], "switch-client", "-t", "$1")
	assertCall(t, rec.calls[1], "select-window", "-t", "@4")
}

// attachArgvQuoted is AttachCmd's argv, quoted element for element, written out by
// hand. Deriving it from AttachCmd is what let the unquoted join ship:
// `strings.Join(x, " ") == strings.Join(x, " ")` cannot fail, so no defect in how
// the argv becomes a payload could make the old test red.
//
// Every element is single-quoted because tmux hands a trailing argument to
// `$SHELL -c`. Unquoted, this pane's `$1` is eaten as a positional parameter.
//
// The last element is quoted TWICE over, and the two quotings answer to two
// different shells. AttachCmd's own target is already `'$1'`, because ssh joins its
// command arguments into one string for the shell on the FAR side; this file's outer
// layer then protects that whole element from the shell on THIS side. So the element
// below reads as an empty quote pair, an escaped quote, the id, an escaped quote and
// another empty pair — spelled out in prose because gofmt rewrites two adjacent
// single quotes in a DOC comment into a typographic quote (see shapeFor, which says
// the same about itself). A local shell re-splits that into exactly `'$1'`, which is
// what ssh must receive for the remote shell to hand tmux a bare `$1`.
const attachArgvQuoted = `'ssh' '-S' '/home/dev/.ssh/cm-nuc' '-t' 'nuc' 'tmux' 'attach' '-t' ''\''$1'\'''`

// keepOpenTail is what turns that argv into a payload the window cannot outlive
// on failure, also written out by hand: the status the payload exited with, and
// when non-zero a `read` that keeps a live shell in the pane until the operator
// presses enter. On success the window closes silently. This is the operator-facing
// text, so a reworded line is a change this test should see.
const keepOpenTail = `; s=$?; [ "$s" -eq 0 ] || { printf '\n[tmux-hub] the attach exited %s — press enter to close this window\n' "$s"; read _; }`

// windowPayload is the ONE argument the window path hands to new-window: the script
// above, quoted a second time, inside `sh -c`.
//
// The escaping is spelled out here rather than borrowed from the product, because a
// test that calls shellJoin to predict shellJoin's output asserts only that a
// function equals itself. A single quote is the one character single quotes cannot
// carry, so it is closed, escaped and reopened; everything else passes through.
var windowPayload = `'sh' '-c' '` +
	strings.ReplaceAll(attachArgvQuoted+keepOpenTail, `'`, `'\''`) + `'`

// The remote path reuses AttachCmd's argv element for element and only changes its
// container. If this test has to be edited to accommodate a reworded ssh command,
// the change went further than §20 allows.
//
// ONE command, and that is the fix rather than a simplification. The second command
// used to be `set -w remain-on-exit on`, which is what made the window survive a
// payload that died — except that `new-window` STARTS the payload, so the option
// arrives after the fact and loses the race whenever the payload dies first
// (measured on 3.7b over a private socket: a payload of `false` lost 6 of 12
// trials, and the remote failures this path exists to show are the fast ones).
// Wrapping the payload in a shell that outlives it removes the race instead of
// timing it, and internal/e2e proves that against a real server.
func TestTheWindowPathWrapsTheExistingAttachUnchanged(t *testing.T) {
	rec := &possessionRecorder{}
	m := newTestModel(t, rec)
	m.selfEpoch = "999:111"
	h := remoteHost()
	p := testPane("nuc", "222:333")

	_, cmd := m.possess(p, h)
	if cmd == nil {
		t.Fatal("possess returned no command for a remote target")
	}
	if msg, ok := cmd().(possessedMsg); !ok || msg.err != nil {
		t.Fatalf("msg = %#v, want a clean possessedMsg", msg)
	}
	// Three now, and each one is load-bearing: ASK whether a window is already showing this
	// target, create, then CLAIM what was created. The middle one is the only one that existed,
	// and pressing `a` twice therefore opened two windows onto one session — measured on the
	// operator's own server as five.
	// Two, and both are load-bearing: ASK whether a window is already showing this target, then
	// create. Only the create existed, so pressing `a` twice opened two windows onto one session —
	// measured on the operator's own server as five. There is deliberately no THIRD call: a marker
	// option would have to be written after `new-window`, which is the shape this file already
	// refuses on a measurement, so the window's NAME is the key.
	if len(rec.calls) != 2 {
		t.Fatalf("sent %d commands, want 2 (look up, create): %q", len(rec.calls), rec.calls)
	}
	assertCall(t, rec.calls[0], "list-windows", "-a", "-F",
		"#{window_id}|#{session_id}|#{window_name}")
	// The window is named after the ROW, not after the host: `C-b w` used to list one entry per
	// attach, all called `nuc`, matching nothing on the dashboard.
	assertCall(t, rec.calls[1], "new-window", "-t", "$0", "-n", "nuc/api", windowPayload)
}

// And when the window cannot be created at all, nothing moved: the note must deny
// the move, and it must NOT carry a `from` that a later "back from …" would read.
//
// This is the direction the old code got wrong for a different reason. Then, the
// failure that reached here was a window that HAD opened and would not take
// remain-on-exit, so the note had to name where the operator now was; now the only
// failure is a refused `new-window`, and the operator is still in the hub. The
// negative halves are the discriminators: no session name in the note, no `from` in
// the message.
func TestARefusedWindowDeniesTheMoveThatDidNotHappen(t *testing.T) {
	rec := &possessionRecorder{failVerb: "new-window"}
	m := newTestModel(t, rec)
	m.selfEpoch = "999:111"

	_, cmd := m.possess(testPane("nuc", "222:333"), remoteHost())
	if cmd == nil {
		t.Fatal("possess returned no command for a remote target")
	}
	msg, ok := cmd().(possessedMsg)
	if !ok || msg.err == nil {
		t.Fatalf("msg = %#v, want a possessedMsg carrying the refusal", msg)
	}
	if msg.from != "" {
		t.Errorf("from = %q, want empty — no window was created, so nobody moved", msg.from)
	}
	m2, _ := m.Update(msg)
	note := m2.(model).note
	if !strings.Contains(note, "cannot go there") || !strings.Contains(note, "can't find new-window") {
		t.Errorf("note = %q, want a refusal carrying tmux's own message", note)
	}
	if strings.Contains(note, "api:review") {
		t.Errorf("note = %q claims a move that did not happen", note)
	}
}

// The payload's meaning is whatever a SHELL makes of it, so this asks a real shell —
// now TWICE, because the wrapper adds a second one. tmux's default-shell re-splits
// the outer `sh -c <script>`, and that `sh` re-splits the script into the attach
// argv, so a quoting defect at either level is a payload that reaches ssh altered.
//
// Unquoted the shell eats the id — measured on tmux 3.7b, `… attach -t $3` reaches
// ssh as `… attach -t`, `-t $0` as the shell's own name, and `-t $10` as a bare
// `0`, which attaches to whatever session is NAMED 0. That last one is the defect
// shapeFor's `^\$[0-9]+$` rule exists to prevent, manufactured past the seam.
func TestNoShellCanAlterTheWindowPathPayload(t *testing.T) {
	rec := &possessionRecorder{}
	m := newTestModel(t, rec)
	m.selfEpoch = "999:111"
	p := testPane("nuc", "222:333")

	_, cmd := m.possess(p, remoteHost())
	if cmd == nil {
		t.Fatal("possess returned no command for a remote target")
	}
	cmd()
	// The CREATE's argv, found by verb rather than by index: the window path asks the server for an
	// existing window first now, so a positional read here was silently testing a format string.
	create := callOf(t, rec, "new-window")
	payload := create[len(create)-1]
	if !strings.Contains(payload, "$1") {
		t.Fatalf("the session id is not in the payload at all: %q", payload)
	}

	// The OUTER level. printf reuses its format once per argument, so the output
	// names the argv the shell actually built: three elements, with the whole script
	// intact as the third. The want is a literal too.
	out, err := exec.Command("sh", "-c", `printf '[%s] ' `+payload).CombinedOutput()
	if err != nil {
		t.Fatalf("sh failed: %v (%s)", err, out)
	}
	want := `[sh] [-c] [` + attachArgvQuoted + keepOpenTail + `] `
	if string(out) != want {
		t.Fatalf("a shell re-parsed the outer payload:\n got %q\nwant %q", out, want)
	}

	// The INNER level, asked of the far side rather than of a string comparison: run
	// the real payload through real shells with an `ssh` on PATH that prints its own
	// argv. What ssh receives is what the operator's attach receives, and the `$1`
	// inside it is the one element a broken level of quoting would eat.
	//
	// It doubles as the wrapper's own proof: the status printed is the shim's, so a
	// `$?` read after the wrong command would be visible here, and `read` consuming
	// EOF from an empty stdin is what lets the script finish at all — in a pane it is
	// a terminal, and the shell sits there until the operator presses enter.
	dir := t.TempDir()
	shim := "#!/bin/sh\nprintf '[%s] ' \"$@\"\nprintf '\\n'\nexit 255\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sh", "-c", payload)
	c.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	c.Stdin = strings.NewReader("")
	// The script's own status is `read`'s, which is non-zero on EOF, so the error is
	// not evidence of anything and the output is.
	ran, _ := c.CombinedOutput()
	// The target arrives at ssh STILL QUOTED, and that is the assertion that matters
	// after both local shells have had their turn. ssh joins its command arguments
	// into one string for the remote user's shell, so `$1` bare there is expanded
	// before tmux sees it — measured over a live master, `ssh -S <ctl> nuc 'echo -t
	// $0'` printed `-t bash`, and `tmux attach -t $0` failed `can't find session:
	// bash` while the quoted form attached (1 client). The local shells must therefore
	// deliver the quotes rather than consume them, which is the composition this arm
	// exists to prove: `''\''$1'\'''` in the payload, `'$1'` in ssh's argv.
	const wantArgv = `[-S] [/home/dev/.ssh/cm-nuc] [-t] [nuc] [tmux] [attach] [-t] ['$1'] `
	if !strings.Contains(string(ran), wantArgv) {
		t.Fatalf("ssh received a different argv:\n got %q\nwant it to contain %q", ran, wantArgv)
	}
	// The negative half, because the defect this fixes is precisely a target that
	// arrives bare: an unquoted `[$1]` element means one of the two local shells ate
	// the quotes and the far side will expand the id.
	if strings.Contains(string(ran), `[-t] [$1] `) {
		t.Fatalf("the target reached ssh unquoted — the remote shell will expand it:\n%s", ran)
	}
	if !strings.Contains(string(ran), "the attach exited 255") {
		t.Fatalf("the payload's own exit status did not reach the pane:\n%s", ran)
	}
}

// `from` is the whole memory, so its VALUE has to be asserted on what the real
// closure returns — not on a literal hand-injected into Update, which only proves
// that Update renders what it was given.
//
// It must be the session and window NAMES, which is the one thing a reader of the
// note can recognise. Nothing else nearby would fail this: the pane's ids are
// `$1:@4`, its pane id is `%0`, and the two paths are given DIFFERENT names so
// that returning the other path's value is red too.
func TestTheMessageNamesWhereTheOperatorWasSent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		p       registry.Pane
		h       hub.Host
		session string
		window  string
		want    string
	}{
		{"jump", testPane("local", "999:111"), localHost(), "work", "agent", "work:agent"},
		{"window", testPane("nuc", "222:333"), remoteHost(), "api", "review", "api:review"},
	} {
		rec := &possessionRecorder{}
		m := newTestModel(t, rec)
		m.selfEpoch = "999:111"
		p := tc.p
		p.Session, p.Window = tc.session, tc.window

		_, cmd := m.possess(p, tc.h)
		if cmd == nil {
			t.Fatalf("%s: possess returned no command", tc.name)
		}
		got, ok := cmd().(possessedMsg)
		if !ok {
			t.Fatalf("%s: message is not a possessedMsg", tc.name)
		}
		if got.err != nil {
			t.Fatalf("%s: %v", tc.name, got.err)
		}
		if got.from != tc.want {
			t.Errorf("%s: from = %q, want %q", tc.name, got.from, tc.want)
		}
	}
}

// A jump that half-landed must not deny the move. switch-client succeeds,
// select-window is refused because the window was killed between the poll and the
// keypress — the ordinary race this codebase guards everywhere else — and the
// client IS now displaying the target session, on some other window. "cannot go
// there" is wrong in the one direction that matters, and the operator's next
// `C-b L` would return them from a place the hub said they never reached.
//
// The second arm is the negative that keeps the first honest: when the switch
// itself fails nothing moved, so the refusal must stay a refusal and carry no
// `from` to produce a "back from" note later.
func TestAHalfLandedJumpSaysSoInsteadOfDenyingTheMove(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failVerb   string
		wantFrom   string
		wantInNote []string
		wantNotIn  string
	}{
		{
			name: "select-window refused after the client moved", failVerb: "select-window",
			wantFrom:   "api:review",
			wantInNote: []string{"api:review", "can't find select-window target @4"},
			wantNotIn:  "cannot go there",
		},
		{
			name: "switch-client refused, so nothing moved", failVerb: "switch-client",
			wantFrom:   "",
			wantInNote: []string{"cannot go there", "can't find switch-client target $1"},
			wantNotIn:  "api:review",
		},
	} {
		rec := &possessionRecorder{failVerb: tc.failVerb}
		m := newTestModel(t, rec)
		m.selfEpoch = "999:111"

		_, cmd := m.possess(testPane("local", "999:111"), localHost())
		if cmd == nil {
			t.Fatalf("%s: possess returned no command", tc.name)
		}
		msg, ok := cmd().(possessedMsg)
		if !ok || msg.err == nil {
			t.Fatalf("%s: msg = %#v, want a possessedMsg carrying tmux's refusal", tc.name, msg)
		}
		if msg.from != tc.wantFrom {
			t.Errorf("%s: from = %q, want %q", tc.name, msg.from, tc.wantFrom)
		}
		m2, _ := m.Update(msg)
		note := m2.(model).note
		for _, want := range tc.wantInNote {
			if !strings.Contains(note, want) {
				t.Errorf("%s: note = %q, want it to contain %q", tc.name, note, want)
			}
		}
		if strings.Contains(note, tc.wantNotIn) {
			t.Errorf("%s: note = %q, must not contain %q", tc.name, note, tc.wantNotIn)
		}
	}
}

// The hub's only state: where it sent the operator, so it can say so on return.
func TestReturningNamesWhereTheOperatorWas(t *testing.T) {
	m := newTestModel(t, &possessionRecorder{})
	m.selfEpoch = "999:111"
	m.possess(testPane("local", "999:111"), localHost())
	m2, _ := m.Update(possessedMsg{from: "api:review"})
	view := m2.(model).View()
	if !strings.Contains(view, "back from api:review") {
		t.Fatalf("View() does not say where the operator came back from:\n%s", view)
	}
}

// callOf is the recorded argv for one verb, and it fails rather than returning nothing: a test that
// silently read the wrong invocation is what this replaced.
func callOf(t *testing.T, rec *possessionRecorder, verb string) []string {
	t.Helper()
	for _, c := range rec.calls {
		if len(c) > 0 && c[0] == verb {
			return c
		}
	}
	t.Fatalf("no %q among the recorded calls: %q", verb, rec.calls)
	return nil
}

// A SECOND `a` on the same remote row goes to the window the first one opened, and opens nothing.
//
// This is the operator's report, and it was measured on their own server: five presses had left five
// windows all named `nuc`, all ssh-attached to one remote session (`attached=5`). It is not only
// clutter in `C-b w` — `window-size` is `latest` on both ends, so each new attach RESIZED the shared
// session, and the older windows were left drawing a session wider than their own terminal.
func TestASecondAGoesToTheWindowTheFirstOneOpened(t *testing.T) {
	rec := &possessionRecorder{}
	// The server already holds a window of the hub's session showing this target, named the way the
	// hub names one. The ANSWER comes from the server rather than from the hub's memory, which is
	// what makes a restarted hub find the window a previous one opened.
	rec.windows = "@4|$0|zsh\n@9|$0|nuc/api\n"
	m := newTestModel(t, rec)
	m.selfEpoch = "999:111"

	_, cmd := m.possess(testPane("nuc", "222:333"), remoteHost())
	if cmd == nil {
		t.Fatal("possess returned no command for a remote target")
	}
	msg, ok := cmd().(possessedMsg)
	if !ok || msg.err != nil {
		t.Fatalf("msg = %#v, want a clean possessedMsg", msg)
	}
	if !msg.reused {
		t.Error("the message does not say the window was reused, so the note will read as a fresh " +
			"attach and the operator cannot tell 'you are already there' from 'nothing happened'")
	}
	for _, c := range rec.calls {
		if len(c) > 0 && c[0] == "new-window" {
			t.Fatalf("a second window was opened onto a session already on screen: %q", c)
		}
	}
	assertCall(t, callOf(t, rec, "select-window"), "select-window", "-t", "@9")
}

// A window of ANOTHER session, or one showing another target, is not this row's window. Without both
// halves the lookup would send the operator into somebody else's window — which is worse than the
// duplicate it exists to prevent.
func TestTheLookupMatchesTheSessionAndTheNameTogether(t *testing.T) {
	for _, c := range []struct {
		name    string
		windows string
		reuse   bool
	}{
		{"the hub's session, the right name", "@9|$0|nuc/api\n", true},
		{"another session, the right name", "@9|$7|nuc/api\n", false},
		{"the hub's session, another host", "@9|$0|dev/api\n", false},
		{"the hub's session, another row", "@9|$0|nuc/deploy\n", false},
		{"a name that merely contains it", "@9|$0|xnuc/apix\n", false},
		{"nothing at all", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := &possessionRecorder{windows: c.windows}
			m := newTestModel(t, rec)
			m.selfEpoch = "999:111"
			_, cmd := m.possess(testPane("nuc", "222:333"), remoteHost())
			msg, _ := cmd().(possessedMsg)
			if msg.reused != c.reuse {
				t.Errorf("reused = %v, want %v (windows %q)", msg.reused, c.reuse, c.windows)
			}
		})
	}
}

// The name the hub gives the window is the name the hub SHOWS for that row, so the session tree and
// the dashboard can be read against each other. It used to be the host label alone.
func TestTheAttachWindowIsNamedAfterTheRow(t *testing.T) {
	p := testPane("nuc", "222:333")
	var al project.Aliases
	if got := attachWindowName("nuc", p, al); got != "nuc/api" {
		t.Errorf("unnamed row: %q, want nuc/api", got)
	}
	// An alias wins, because it is what the dashboard shows.
	al.Set(project.AliasKeyOf(p), "прод-выкатка")
	if got := attachWindowName("nuc", p, al); got != "nuc/прод-выкатка" {
		t.Errorf("aliased row: %q, want the alias", got)
	}
	// And the sanitiser applies: tmux stores `.` and `:` and then cannot address the window, and the
	// hub's own seam refuses a literal `%` outside a -t value, so an alias with a percentage in it
	// would make new-window fail rather than open.
	al.Set(project.AliasKeyOf(p), "50% a.b:c")
	got := attachWindowName("nuc", p, al)
	for _, bad := range []string{"%", ".", ":"} {
		if strings.Contains(got, bad) {
			t.Errorf("the window name still carries %q: %q", bad, got)
		}
	}
	if err := tmux.Validate([]string{"new-window", "-t", "$0", "-n", got, "payload"}); err != nil {
		t.Errorf("the argv the hub would build is refused by the seam: %v (%q)", err, got)
	}
}
