package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/registry"
)

// wakeCostsWork reports whether waking this row can COST something, which is the gate for the
// dialog. It reads the listing's own word unfolded, never the state: `state.FromWord` maps `failed`
// and `error` onto one value, so a gate written against the state fires on a session whose WORK
// errored as readily as on one whose worker is gone.
//
// docs/design.md §22.3 gated this on `failed` alone, on the grounds that "waking a live, blocked,
// working or settled row costs nothing measured". MEASURED 2026-08-17, and the settled half of that
// is false: `claude attach <id>` on a row the listing called `done` woke it, and because its record
// carried `respawnFlags: ["--reply-on-resume", …]` it began working within the minute —
// `state.json` went `done` → `working` with a fresh `detail`, spending tokens on a turn nobody
// asked for. Over the whole local store, 18 of 26 records carry that flag and 10 of the 15 reading
// `done` do, so it is two rows in three rather than a corner.
//
// The hub cannot tell WHICH: `respawnFlags` lives in `~/.claude/jobs/<id>/state.json` on the row's
// own host, a file §22.5 rules the hub does not read. So `done` joins the gate rather than the
// dialog naming a certainty it does not have.
//
// `blocked` and `working` stay outside it: a blocked session is waiting for a person, and waking it
// is how the person answers — the whole point of the door — while a working one keeps working
// whether a pane watches it or not. A confirmation for a free action teaches the operator to press
// enter without reading.
func wakeCostsWork(word string) bool {
	switch word {
	case wordFailed, "done", "completed":
		return true
	}
	return false
}

// wordFailed is the listing's word for "the store says this ran and I see no worker". It is spelled
// here as well as in the registry because this package must not import that constant to compare a
// string it reads off a row it was handed.
const wordFailed = "failed"

// WakeView is the wake dialog's own subject, and it carries its payload rather than reading it back
// out of the model at commit time — the rule the history re-send defect paid for: a pending action
// that reads a shared surface makes every raiser of that action a writer of it.
type WakeView struct {
	Name  string   // what to call the session
	Host  string   // where the door will knock
	Word  string   // the listing's own word, unfolded
	Costs []string // one per cost, each one measured
	Note  string
}

// wakeView builds the dialog for one row. Every line in it is a fact the hub HOLDS: the far host's
// own `state.json` carries the two stamps §22.3's sketch wanted (`reapedMidWorkAt`,
// `firstTerminalAt`) and no code reads that file, so the dialog says what the hub knows and stops.
func wakeView(p registry.Pane, h hub.Host, note string) WakeView {
	v := WakeView{Name: wakeSubjectName(p), Host: p.Host, Word: p.AgentWord, Note: note}
	if v.Word == "" {
		v.Word = "nothing at all"
	}
	// Cost 1 is about work that was ABANDONED, so it belongs to the row whose worker went away and
	// not to one that finished. Measured (§22.3): the transcript kept a dangling `tool_use` with no
	// result and then `No response requested.`, and the row settled `done` — so a killed turn can
	// report success.
	if p.AgentWord == wordFailed {
		v.Costs = append(v.Costs,
			"the turn it was running was abandoned, and a killed turn can still report success — "+
				"so anything it claimed to finish may be unfinished")
		// Cost 2, measured on the same reap: the `sleep 300` from the killed tool call survived,
		// reparented to init. The class and the host, never a pid — no source the hub reads carries
		// a usable one.
		v.Costs = append(v.Costs, fmt.Sprintf(
			"a process its last tool call started may still be running on %s — the hub cannot see "+
				"it and will not kill it", p.Host))
	}
	// Cost 3 is the one §22.3 did not have, and it is why `done` is in the gate at all. Measured
	// 2026-08-17: waking a row the listing called `done` made it work within the minute, because
	// its record carries `--reply-on-resume` — 18 of 26 records on this fleet do, 10 of the 15
	// reading `done`. The flag lives in a file on the row's own host that the hub does not read, so
	// this says MAY and cannot say will.
	v.Costs = append(v.Costs,
		"waking it replays the transcript, and most background sessions are set to reply when they "+
			"resume — so it may pick the work up again and spend tokens")
	// Cost 4, measured on 3.2a: one `new-session -d -P -F` against a socket with no server returned
	// all five fields at rc=0 and started the server. A cost of the keypress, so it is named.
	if h.Status == hub.UpEmpty {
		v.Costs = append(v.Costs,
			fmt.Sprintf("%s has no tmux server; making a pane starts one", p.Host))
	}
	return v
}

// RenderWake shows what waking this row costs, over the dashboard it came from.
func RenderWake(f Frame, v WakeView) string {
	body := wakeBody(f.Width, v)
	base := backdrop(f.withHeight(f.Height - len(body)).withNote(v.Note))
	return joinToHeight(append(strings.Split(base, "\n"), body...), f.Height)
}

// wakeBody is the dialog itself, so its height is known before the dashboard under it is sized —
// never the other way round, because subtracting a fixed height is how a screen loses a row it
// promised (§22.9).
//
// The costs are WRAPPED rather than truncated. Each one is a sentence the operator has to finish
// reading to make the decision, and `lines.Truncate` emits no ellipsis — so a cut cost would read
// as a complete one, which is the direction that matters here.
func wakeBody(width int, v WakeView) []string {
	out := []string{separator(width)}
	out = append(out, lines.Truncate(fmt.Sprintf("wake %s on %s?  the listing says %s",
		v.Name, v.Host, v.Word), width))
	for _, c := range v.Costs {
		// The bullet is what makes the costs countable on the dialog's own surface, which is how
		// they are asserted: a `Contains` over the whole screen cannot tell one surface's line from
		// another's.
		wrapped := wrapWords(c, max(1, width-4))
		for i, ln := range wrapped {
			lead := "  • "
			if i > 0 {
				lead = "    "
			}
			out = append(out, lines.Truncate(lead+ln, width))
		}
	}
	// `any other key`, not `esc`: any key that is not enter cancels, and naming only one of them
	// would be a half-truth on the line the operator's finger is on.
	return append(out, lines.Truncate("enter wakes it  ·  any other key leaves it alone", width))
}

// wakeSubject is what the dialog is about, captured at the keystroke.
type wakeSubject struct {
	row  registry.Pane
	host hub.Host
}

// wakeViewOf is the dialog for whatever the model is currently asking about. A model in modeWake
// with no subject is a defect rather than a state, and it draws a dialog that says so instead of an
// empty one — an empty dialog with an `enter` on it is the worst of the three outcomes.
func (m model) wakeViewOf(note string) WakeView {
	if m.pendingWake == nil {
		return WakeView{Name: "nothing", Host: "nowhere", Word: "nothing at all", Note: note,
			Costs: []string{"internal: the wake dialog has no subject, so enter will do nothing"}}
	}
	return wakeView(m.pendingWake.row, m.pendingWake.host, note)
}

// wakeKey requires enter. Anything else leaves the row alone, which is the safe default for "I do
// not understand this dialog" — and it says which row it left, because a dialog that vanishes with
// only `cancelled` makes the operator check whether something happened.
func (m model) wakeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	subject := m.pendingWake
	m.pendingWake = nil
	m = m.dismiss()
	if subject == nil {
		m.note = "internal: a wake dialog with no subject was dismissed"
		return m, nil
	}
	if msg.String() != "enter" {
		m.note = "left " + wakeSubjectName(subject.row) + " alone"
		return m, nil
	}
	return m.wake(subject.row, subject.host)
}
