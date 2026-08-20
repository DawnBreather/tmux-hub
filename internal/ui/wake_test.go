package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/agents"
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// The wake dialog, and the one thing §22.3 got wrong.
//
// §22.3 gated it on the listing word `failed`, on the grounds that "waking a live, blocked, working
// or settled row costs nothing measured". Measured 2026-08-17 against the real CLI: waking a row the
// listing called `done` made it WORK within the minute, because its record carries
// `--reply-on-resume` — 18 of 26 records on this fleet do, and 10 of the 15 reading `done`. So the
// settled half of that sentence is false and `done` is in the gate.

// costLines are the dialog's own bullets, sliced off the screen rather than searched for across it:
// a `Contains` over a whole screen cannot tell one surface's line from another's, which is the
// mistake §22.3's test rule 1 exists to forbid.
func costLines(screen string) []string {
	var out []string
	for _, ln := range strings.Split(screen, "\n") {
		trimmed := strings.TrimRight(ln, " ")
		switch {
		case strings.HasPrefix(trimmed, "  • "):
			out = append(out, strings.TrimSpace(trimmed))
		case len(out) > 0 && strings.HasPrefix(trimmed, "    ") && strings.TrimSpace(trimmed) != "":
			// A cost is WRAPPED at 80 columns, and its continuation carries the half of the
			// sentence that names the consequence. Collecting bullets alone counted right and read
			// wrong — measured, `spend tokens` sat on a continuation line and a content assertion
			// against it failed on a dialog that was perfectly correct.
			out[len(out)-1] += " " + strings.TrimSpace(trimmed)
		}
	}
	return out
}

func wakeDialog(t *testing.T, word string, status hub.Status, cols, rows int) (model, string) {
	t.Helper()
	d := &doorTmux{}
	m := doorModel(t, d, wakeRow("30f3382b", "cicd", "background", word))
	m.hosts = []hub.Host{{Label: "local", Socket: "/tmp/tmux-1000/default", Status: status}}
	m.width, m.height = cols, rows
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if cmd != nil {
		t.Fatalf("word %q: `a` acted immediately instead of asking", word)
	}
	m2 := out.(model)
	if m2.mode != modeWake {
		t.Fatalf("word %q: mode = %v, want the wake dialog", word, m2.mode)
	}
	return m2, m2.View()
}

// The gate reads the LISTING's word, not the state: state.FromWord folds `failed` and `error` onto
// one value, so a gate written against the state fires on a session whose WORK errored.
func TestTheWakeDialogAsksExactlyWhenAWakeCanCost(t *testing.T) {
	for _, c := range []struct {
		word string
		asks bool
	}{
		{"failed", true},    // the worker is gone; the turn was abandoned
		{"done", true},      // finished, and most such rows reply when resumed
		{"completed", true}, // the same thing under 2.1.224's vocabulary
		{"blocked", false},  // waiting for a person; waking IS the answer
		{"working", false},  // it keeps working whether a pane watches or not
		{"busy", false},     // 2.1.224's word for working
		{"idle", false},     // a prompt is up and nothing is running
		{"somethingnew", false},
	} {
		d := &doorTmux{}
		m := doorModel(t, d, wakeRow("30f3382b", "cicd", "background", c.word))
		out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
		asked := out.(model).mode == modeWake
		if asked != c.asks {
			t.Errorf("word %q: asked = %v, want %v", c.word, asked, c.asks)
		}
		if !c.asks && cmd == nil {
			t.Errorf("word %q: neither asked nor acted", c.word)
		}
	}

	// And it is the WORD, not the folded state: `error` reaches state.Error just as `failed` does,
	// so a row whose work errored must not be gated by a sentence about a missing worker.
	if wakeCostsWork("error") {
		t.Error("`error` is in the gate, so a session whose WORK errored is told its worker is gone")
	}
	// The fold is in `agents.Attention`, which is what UpdateAgents feeds to state.FromWord — not
	// in FromWord itself, where `failed` is simply unknown. Asserted on the real function so this
	// case cannot outlive the premise it rests on.
	if (agents.Session{State: "failed"}).Attention() != (agents.Session{State: "error"}).Attention() {
		t.Error("the fixture's premise is gone: `failed` and `error` no longer fold together, so a " +
			"gate on the folded state would now be distinguishable and this case says otherwise")
	}
	if state.FromWord((agents.Session{State: "failed"}).Attention()) != state.Error {
		t.Error("a `failed` listing no longer reaches state.Error")
	}
}

// The costs are asserted BY COUNT and BY CONTENT on the dialog's own surface, at every width §16
// commits to (§22.3 test rule 1).
func TestTheWakeDialogsCostsAtEveryPromisedWidth(t *testing.T) {
	for _, cols := range []int{80, 100, 160, 200} {
		m, screen := wakeDialog(t, "failed", hub.Up, cols, 40)
		_ = m
		costs := costLines(screen)
		if len(costs) != 3 {
			t.Errorf("%d cols: %d cost bullets, want 3 (abandoned turn, stray process, replay):\n%s",
				cols, len(costs), strings.Join(costs, "\n"))
		}
		joined := strings.Join(strings.Split(screen, "\n"), " ")
		for _, want := range []string{
			"wake cicd on local?",           // the subject and where
			"the listing says failed",       // the word, unfolded
			"abandoned",                     // cost 1
			"may still be running on local", // cost 2, the class and the host, never a pid
			"spend tokens",                  // cost 3, the one §22.3 did not have
			"enter wakes it",                // and how to answer
			"any other key",
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("%d cols: the dialog does not say %q", cols, want)
			}
		}
		// Nothing may run past the terminal, at any width.
		for _, ln := range strings.Split(screen, "\n") {
			if lines.Width(ln) > cols {
				t.Errorf("%d cols: a line is %d wide: %q", cols, lines.Width(ln), ln)
			}
		}
		// A pid is never named: no source the hub reads carries a usable one.
		if strings.Contains(joined, "pid ") {
			t.Errorf("%d cols: the dialog names a pid it cannot have", cols)
		}
	}
}

// A settled row is a DIFFERENT dialog: nothing was abandoned and no stray process was left, so
// naming either would be a cost the hub has not measured for this row.
func TestASettledRowsDialogNamesOnlyTheCostItHas(t *testing.T) {
	_, screen := wakeDialog(t, "done", hub.Up, 100, 40)
	costs := costLines(screen)
	if len(costs) != 1 {
		t.Errorf("%d cost bullets for a settled row, want 1:\n%s", len(costs), strings.Join(costs, "\n"))
	}
	joined := strings.Join(costs, " ")
	if !strings.Contains(joined, "spend tokens") {
		t.Errorf("the one cost a settled wake has is missing: %s", joined)
	}
	for _, forbidden := range []string{"abandoned", "still be running"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("a settled row was charged %q, which was measured on a REAPED row: %s",
				forbidden, joined)
		}
	}
}

// A host with no tmux server gains one from the keypress, which is a cost of every press rather
// than of a confirmed one — so it is named.
func TestAHostWithNoServerIsToldItWillGainOne(t *testing.T) {
	_, screen := wakeDialog(t, "done", hub.UpEmpty, 100, 40)
	joined := strings.Join(costLines(screen), " ")
	if !strings.Contains(joined, "no tmux server") {
		t.Errorf("the dialog does not say the host gains a server: %s", joined)
	}
	// And a host that HAS one is not told it does not.
	_, screen = wakeDialog(t, "done", hub.Up, 100, 40)
	if strings.Contains(strings.Join(costLines(screen), " "), "no tmux server") {
		t.Error("a host with a server was told it has none")
	}
}

// enter wakes, anything else leaves the row alone AND SAYS WHICH — a dialog that vanishes with only
// `cancelled` makes the operator check whether something happened.
func TestTheWakeDialogActsOnEnterAndOnNothingElse(t *testing.T) {
	m, _ := wakeDialog(t, "failed", hub.Up, 100, 40)
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	after := out.(model)
	if cmd != nil {
		t.Error("a key that is not enter woke the session")
	}
	if after.mode != modeBrowse {
		t.Errorf("the dialog stayed up: %v", after.mode)
	}
	if after.pendingWake != nil {
		t.Error("the dialog outlived its own dismissal")
	}
	if !strings.Contains(after.note, "left cicd alone") {
		t.Errorf("the note does not say which row was left: %q", after.note)
	}

	m2, _ := wakeDialog(t, "failed", hub.Up, 100, 40)
	out2, cmd2 := m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd2 == nil {
		t.Fatal("enter did not wake it")
	}
	if _, ok := cmd2().(wokenMsg); !ok {
		t.Error("enter answered with something other than a wake")
	}
	if out2.(model).pendingWake != nil {
		t.Error("the subject survived the commit")
	}
}

// The subject is CAPTURED at the keystroke. The list re-sorts under every poll, so a dialog that
// re-derived "the row under the cursor" at enter would wake a row the operator was not looking at —
// which is the defect the history re-send paid for, one dialog over.
func TestTheWakeDialogWakesTheRowItWasOpenedOn(t *testing.T) {
	d := &doorTmux{}
	m := doorModel(t, d, wakeRow("30f3382b", "cicd", "background", "failed"))
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = out.(model)
	if m.mode != modeWake {
		t.Fatalf("mode = %v", m.mode)
	}

	// The fleet moves under the dialog: another row arrives and the one it was opened on is gone
	// from the list entirely, which is what a poll can do between the keystroke and the answer.
	m.panes = []registry.Pane{wakeRow("99999999", "someone-else", "background", "failed")}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter did nothing")
	}
	woken := cmd().(wokenMsg)
	if woken.name != "cicd-30f3382b" {
		t.Errorf("the dialog woke %q, not the row it was opened on", woken.name)
	}
	if create := d.argv("new-session"); create != nil {
		if got := strings.Join(create, " "); strings.Contains(got, "99999999") {
			t.Errorf("the create names the row that arrived AFTER the dialog:\n  %s", got)
		}
	}
}
