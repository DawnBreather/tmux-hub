package ui

import (
	"context"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// The ACCEPT pole, which §22.3 asks for by name: a case that presses `a` on a background row and
// checks the verb was RUN.
//
// Without it a suite of refusals is green against a tree with no feature at all — today's tree
// refused every agent row with a sentence of the same shape, so every refusal case would have passed
// before the door existed.

// fakeCreateEpoch is the two halves of fakeEpoch as a create prints them, so the pane the door makes
// is on the hub's OWN server and possession must jump rather than take the terminal.
const fakeCreateEpoch = "4242|1786510794"

// doorTmux answers a create the way a real server does and records every argv.
type doorTmux struct {
	mu        sync.Mutex
	calls     [][]string
	createRC  int
	createErr string
}

func (d *doorTmux) Run(ctx context.Context, t tmux.Target, args ...string) (tmux.Result, error) {
	// The real seam validates before running, and a fake that skipped it would not notice a format
	// production can never send.
	if err := tmux.Validate(args); err != nil {
		return tmux.Result{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, args)
	switch args[0] {
	case "new-session":
		if d.createRC != 0 {
			return tmux.Result{RC: d.createRC, Stderr: d.createErr}, nil
		}
		return tmux.Result{Stdout: "$3|@7|%9|" + fakeCreateEpoch + "\n"}, nil
	case "list-sessions":
		return tmux.Result{Stdout: "$3|@7|%9|" + fakeCreateEpoch + "|cicd-30f3382b\n"}, nil
	case "display":
		// build() asks this ONE question to learn the hub's own server identity, and the answer
		// has to be that identity — not the create's five fields. Answering the wrong shape made
		// selfEpoch garbage, which silently sent every wake down the WINDOW path instead of the
		// jump, and the test that noticed was the one asserting switch-client.
		return tmux.Result{Stdout: fakeEpoch + "\n"}, nil
	case "switch-client", "select-window", "set":
		return tmux.Result{}, nil
	}
	return tmux.Result{}, nil
}

func (d *doorTmux) RunInput(ctx context.Context, t tmux.Target, stdin []byte, args ...string) (tmux.Result, error) {
	return d.Run(ctx, t, args...)
}

func (d *doorTmux) argv(verb string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.calls {
		if len(c) > 0 && c[0] == verb {
			return c
		}
	}
	return nil
}

// doorModel is a model with a runner and one background row on the hub's own server.
func doorModel(t *testing.T, d *doorTmux, row registry.Pane) model {
	t.Helper()
	inTmux(t)
	m := build(context.Background(), d, nil, true, nil, nil, nil)
	m.hosts = []hub.Host{{Label: "local", Socket: "/tmp/tmux-1000/default", Status: hub.Up}}
	m.panes = []registry.Pane{row}
	m.width, m.height = 120, 40
	return m
}

func TestAOnABackgroundRowRunsTheVerbInASessionItMakes(t *testing.T) {
	d := &doorTmux{}
	m := doorModel(t, d, wakeRow("30f3382b", "cicd", "background", "blocked"))

	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if cmd == nil {
		t.Fatalf("`a` on a background row returned no command, so nothing was run. note: %q",
			out.(model).note)
	}
	msg := cmd()
	woken, ok := msg.(wokenMsg)
	if !ok {
		t.Fatalf("`a` answered with %T, want wokenMsg", msg)
	}
	if woken.err != nil {
		t.Fatalf("the wake failed: %v", woken.err)
	}

	create := d.argv("new-session")
	if create == nil {
		t.Fatalf("no session was created; calls: %v", d.calls)
	}
	joined := strings.Join(create, " ")
	for _, want := range []string{
		"-d",               // detached: the hub keeps its terminal
		"-s cicd-30f3382b", // named after the row and its id
		"-c /w/iac",        // in the session's own cwd
		"#{session_id}",    // the five-field read-back...
		"#{pane_id}",       // ...whose epoch keeps a local wake off the full-screen path
		"#{start_time}",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the create does not carry %q:\n  %s", want, joined)
		}
	}
	// The payload is ONE argv element and the assertion is an equality, not a substring: tmux hands
	// a trailing argument to `$SHELL -c`, so the verb arrives QUOTED — `''\''claude''\'' ''\''attach''\''` —
	// and a substring check for `claude attach` would fail on a payload that is perfectly correct.
	if got := create[len(create)-1]; got != wakePayload("30f3382b") {
		t.Errorf("the payload is not the wrapped verb:\n  got  %s\n  want %s", got,
			wakePayload("30f3382b"))
	}
	if strings.Contains(joined, "--debug-file") {
		t.Errorf("the payload carries a flag `claude attach` rejects:\n  %s", joined)
	}

	// The pane the create returned is what possession is aimed at, and the epoch came back with it.
	if woken.made.PaneID != "%9" || woken.made.Epoch != fakeEpoch {
		t.Errorf("the door did not read back what it made: %+v", woken.made)
	}

	// And the hub owns the new pane's identity at birth: no process walk decides whether it is an
	// agent, because the hub made it.
	if id, ok := m.keeper.Session("local", "%9"); !ok || !strings.HasPrefix(id, "30f3382b") {
		t.Errorf("the created pane was not adopted: %q %v", id, ok)
	}
}

// The pane the door made is on the hub's OWN server, so possession must JUMP rather than take the
// terminal. That is the whole reason the create reads five fields instead of one: an empty epoch on a
// local host falls through to the full-screen attach, which blocks the hub.
func TestTheDoorJumpsIntoThePaneItMadeRatherThanTakingTheTerminal(t *testing.T) {
	d := &doorTmux{}
	m := doorModel(t, d, wakeRow("30f3382b", "cicd", "background", "blocked"))

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	woken := cmd().(wokenMsg)
	out, cmd2 := m.Update(woken)
	if cmd2 == nil {
		t.Fatalf("nothing followed the create, so the operator was left where they were. note: %q",
			out.(model).note)
	}
	if _, ok := cmd2().(possessedMsg); !ok {
		t.Fatalf("the door did not hand over to possession")
	}
	if sw := d.argv("switch-client"); sw == nil {
		t.Errorf("no switch-client: the door took the terminal instead of jumping. calls: %v", d.calls)
	} else if !strings.Contains(strings.Join(sw, " "), "$3") {
		t.Errorf("the jump aimed at %v, not at the session the create returned", sw)
	}
	if note := out.(model).note; !strings.Contains(note, "made cicd-30f3382b on local") {
		t.Errorf("the note does not say what was made: %q", note)
	}
}

// A second `a` finds the door the first one made, because the name is a pure function of the row.
// tmux's OWN words are what say so — rc=1 alone also means a missing server, a dead ssh master or a
// wrong socket, and each of those has to be reported as itself.
func TestASecondPressFindsTheDoorInsteadOfMakingASecondOne(t *testing.T) {
	d := &doorTmux{createRC: 1, createErr: "duplicate session: cicd-30f3382b"}
	m := doorModel(t, d, wakeRow("30f3382b", "cicd", "background", "blocked"))

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	woken := cmd().(wokenMsg)
	if woken.err != nil {
		t.Fatalf("a duplicate name was reported as a failure: %v", woken.err)
	}
	if !woken.found {
		t.Error("the door did not say it FOUND the session rather than making it")
	}
	if woken.made.PaneID != "%9" {
		t.Errorf("the found session's pane was not read back: %+v", woken.made)
	}
	out, _ := m.Update(woken)
	if note := out.(model).note; !strings.Contains(note, "went back to") {
		t.Errorf("the note claims a second session was made: %q", note)
	}
}

// Every other rc=1 is reported as ITSELF, with tmux's own sentence, and no session is entered.
func TestAnyOtherRefusalIsReportedAsItself(t *testing.T) {
	for _, c := range []struct{ name, stderr string }{
		{"no server", "no server running on /tmp/tmux-1000/default"},
		{"a dead master", "no live ssh master at /run/user/1000/cm-x — respawn it with ssh -N -M"},
		{"a missing directory", "can't find directory /w/iac"},
	} {
		d := &doorTmux{createRC: 1, createErr: c.stderr}
		m := doorModel(t, d, wakeRow("30f3382b", "cicd", "background", "blocked"))
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
		woken := cmd().(wokenMsg)
		if woken.err == nil {
			t.Fatalf("%s: rc=1 was treated as success", c.name)
		}
		if !strings.Contains(woken.err.Error(), c.stderr) {
			t.Errorf("%s: tmux's own words were dropped: %v", c.name, woken.err)
		}
		out, cmd2 := m.Update(woken)
		if cmd2 != nil {
			t.Errorf("%s: the hub tried to go somewhere it never made", c.name)
		}
		if note := out.(model).note; !strings.Contains(note, "could not open") {
			t.Errorf("%s: the note is %q", c.name, note)
		}
	}
}

// A host whose tunnel is down still runs the session — MarkHostStale deliberately leaves agent rows
// live — so the refusal must name that mechanism and not a socket, and it must not dial.
func TestADownHostsRowSaysTheTunnelIsDownWithoutDialling(t *testing.T) {
	d := &doorTmux{}
	m := doorModel(t, d, wakeRow("30f3382b", "cicd", "background", "blocked"))
	m.hosts = []hub.Host{{Label: "local", Socket: "/tmp/tmux-1000/default", Status: hub.Down,
		Reason: "no live ssh master"}}

	// build() has already asked this server for its own identity, so the count that matters is what
	// the KEYPRESS added — a total would be measuring the fixture.
	before := len(d.calls)
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if cmd != nil {
		t.Error("the hub returned a command for a host it knows is down")
	}
	note := out.(model).note
	for _, want := range []string{"tunnel is down", "cicd", "still running there", "press p"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note does not say %q: %s", want, note)
		}
	}
	if got := len(d.calls) - before; got != 0 {
		t.Errorf("a down host was dialled %d times by the keypress: %v", got, d.calls[before:])
	}
}
