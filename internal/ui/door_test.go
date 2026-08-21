package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/agents"
	"github.com/DawnBreather/tmux-hub/internal/history"
	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// The door is `a` on a pane-less BACKGROUND row: the hub creates a session on that row's host running
// `claude attach <short id>`, and possession takes over the pane the create returns
// (docs/design.md §22.3).
//
// The gate is Kind == KindAgent AND the row's own kind word `background`, never a non-empty id:
// `agents.Parse` back-fills ID from SessionID[:8], so an interactive row carries a plausible short id
// with no daemon behind it, and a gate written `ID != ""` reads correctly and fails at runtime.

// wakeRow is render_test's agentRow plus the two things the door reads: the RAW listing word, which
// is what the gate and the dialog are written against, and a cwd, which is where the created session
// starts. Composed rather than copied, so a row shape lives in one place.
//
// The state goes through `Attention()` FIRST, exactly as UpdateAgents does — `state.FromWord` does
// not know the word `failed` at all, so a fixture that skipped the fold gave every reaped row the
// state `unknown` and drew `? unknown` where production draws `✗ error`. A fixture that does not
// pass through the producer's own fold is a fixture testing a screen the product cannot show.
func wakeRow(id, name, kind, word string) registry.Pane {
	p := agentRow(id, name, kind, state.FromWord((agents.Session{State: word}).Attention()))
	p.AgentWord = word
	p.SessionID = id + "-f68c-4baf-98fd-68d4fd1c3da4"
	p.Path = "/w/iac"
	return p
}

func TestOnlyABackgroundAgentRowHasADoor(t *testing.T) {
	for _, c := range []struct {
		name string
		row  registry.Pane
		open bool
		// says is whether the refusal must carry a sentence. An AGENT row the door turns down is
		// the operator pressing `a` on something that looks reachable, so it has to be told why and
		// what to do; a PANE row is not the door's business at all — it goes down the ordinary
		// possession path — so a sentence there would be a refusal of something never refused.
		says bool
	}{
		{"a background row", wakeRow("30f3382b", "cicd", "background", "blocked"), true, false},
		{"an interactive row", wakeRow("30f3382b", "cicd", "interactive", "idle"), false, true},
		{"a row whose kind is empty", wakeRow("30f3382b", "cicd", "", ""), false, true},
		{"a row whose kind is a word this fleet has not shown",
			wakeRow("30f3382b", "cicd", "somethingnew", "idle"), false, true},
		{"a pane row", registry.Pane{Kind: registry.KindPane, Host: "local", PaneID: "%1"}, false, false},
	} {
		got, why := wakeable(c.row)
		if got != c.open {
			t.Errorf("%s: wakeable = %v, want %v (%s)", c.name, got, c.open, why)
		}
		if c.says && why == "" {
			t.Errorf("%s: refused with no sentence — a key that does nothing and says nothing is "+
				"indistinguishable from a broken one", c.name)
		}
		if !c.says && why != "" {
			t.Errorf("%s: answered with a refusal it should not own: %s", c.name, why)
		}
	}
}

// The refusal carries the FIX, not just the breakage, and it quotes the kind off the row rather than
// describing it: a row whose kind is empty or a future word must not be told it is "interactive",
// which is a claim the hub never measured (§22.8).
func TestTheRefusalNamesTheKindAndTheRemedy(t *testing.T) {
	_, why := wakeable(wakeRow("30f3382b", "cicd", "interactive", "idle"))
	for _, want := range []string{`"interactive"`, "cicd", "/background"} {
		if !strings.Contains(why, want) {
			t.Errorf("the refusal does not mention %q: %s", want, why)
		}
	}
	if strings.Contains(why, "the hub has no id") {
		t.Error("the refusal blames the hub, when it is the ROW that carries no background id")
	}
	// It has to FIT the size §16 commits to, remedy included. Measured before this bound existed: the
	// 190-character form specified in §22.8 was cut after "so there is nothing for the hub to", so
	// the operator read the complaint and never the fix.
	if lines.Width(why) > 78 {
		t.Errorf("the refusal is %d columns, so at 80 the remedy is what gets cut: %s",
			lines.Width(why), why)
	}
	// A kind the fleet has not shown is quoted verbatim and never renamed.
	_, why = wakeable(wakeRow("30f3382b", "cicd", "somethingnew", "idle"))
	if !strings.Contains(why, `"somethingnew"`) || strings.Contains(why, "interactive") {
		t.Errorf("a future kind was described rather than quoted: %s", why)
	}
}

// The created session is named `<name>-<short id>`, because the name answers "where am I" and the
// short id is the only field guaranteed unique among background rows — two rows on this fleet share
// a name AND a cwd.
func TestTheDoorNamesTheSessionAfterTheRowAndItsID(t *testing.T) {
	if got := wakeName(wakeRow("30f3382b", "cicd", "background", "blocked")); got != "cicd-30f3382b" {
		t.Errorf("name = %q", got)
	}
	// A name tmux cannot address is a session nobody can reach: `.` and `:` are stored and then
	// make `-t` look for a window or a pane. Measured on both fleet versions.
	got := wakeName(wakeRow("30f3382b", "20260817-cicd:v2.1", "background", "blocked"))
	if strings.ContainsAny(got, ".:") {
		t.Errorf("name %q keeps a character tmux's own -t syntax reads as a window or pane", got)
	}
	if !strings.Contains(got, "30f3382b") {
		t.Errorf("name %q dropped the one field that is unique among background rows", got)
	}
	// A row with no name at all still gets an addressable one.
	if got := wakeName(wakeRow("30f3382b", "", "background", "blocked")); got == "" ||
		!strings.Contains(got, "30f3382b") {
		t.Errorf("an unnamed row produced %q", got)
	}
}

// The payload runs the verb under a LOGIN shell, so `claude` is on the path on a host that only puts
// it there for a login command, and it holds the pane on failure, so a `claude` that exits 1 with one
// stderr line leaves the message on screen instead of evaporating with the pane.
//
// And it carries NO `--debug-file`. Measured 2026-08-17 against the real CLI: `claude attach
// --debug-file /tmp/x deadbeef` answers rc=1 `unknown option '--debug-file'` with
// `Usage: claude attach <id>` — §22.3 left that flag UNVERIFIED and the payload rested on it.
func TestTheDoorsPayloadWrapsTheVerbAndCarriesNoDebugFile(t *testing.T) {
	got, err := wakePayload("30f3382b")
	if err != nil {
		t.Fatalf("wakePayload refused a plain short id: %v", err)
	}
	if !strings.Contains(got, "claude") || !strings.Contains(got, "attach") ||
		!strings.Contains(got, "30f3382b") {
		t.Errorf("payload does not run the verb: %s", got)
	}
	if strings.Contains(got, "--debug-file") {
		t.Errorf("the payload carries a flag `claude attach` REJECTS, so every wake would exit 1 "+
			"before reaching the daemon: %s", got)
	}
	// `-l`, not just `sh`. A pane inherits the ssh CLIENT's environment, so on dev-air (login shell
	// nushell) the non-login PATH does not contain `claude` and the door created a session that died
	// before the hub could read its window id. The old assertion was `Contains(got, "sh")`, which the
	// broken shape satisfied — it is inside the word `attach`.
	if !strings.Contains(got, "sh -lc") {
		t.Errorf("the payload does not run under a login shell, so `claude` may not be on the "+
			"pane's path at all: %s", got)
	}
	if !strings.Contains(got, "read") {
		t.Errorf("nothing holds the pane open on failure: %s", got)
	}
	// The verb takes the SHORT id. Measured: the full uuid answers `No job matching`.
	if strings.Contains(got, "f68c-4baf") {
		t.Errorf("the payload passes a uuid, which the verb does not resolve: %s", got)
	}
}

// Every outcome the hub WRITES has a glyph in the one view that exists to say what happened. Two did
// not: `launched` has been written since §19 and `woken` arrives with the door, and both rendered as
// `?` — the column's own word for "the hub does not know".
func TestEveryOutcomeTheHubWritesHasAGlyph(t *testing.T) {
	at := time.Unix(1786450000, 0)
	var es []history.Entry
	for _, w := range []string{"delivered", "sent-unwitnessed", "refused", "launched", "woken"} {
		es = append(es, history.Entry{At: at, Host: "local", PaneID: "%1", Text: w, Outcome: w})
	}
	rows := RenderHistory(es, 100, len(es), 0)
	for i, w := range []string{"delivered", "sent-unwitnessed", "refused", "launched", "woken"} {
		if strings.Contains(rows[i], "?") {
			t.Errorf("outcome %q renders as unknown: %q", w, rows[i])
		}
	}
	// And an outcome nobody writes still falls back rather than rendering blank, because a future
	// word must not silently lose its row.
	odd := RenderHistory([]history.Entry{{At: at, Host: "local", PaneID: "%1", Outcome: "wat"}},
		100, 1, 0)
	if !strings.Contains(odd[0], "?") {
		t.Errorf("an unknown outcome lost its fallback: %q", odd[0])
	}
}

// §22.8 asks for the refusal to be pinned by a frame assertion on the string View() returns at 80
// columns, PAIRED with an accept pole — because today's tree already refused every agent row with a
// sentence of the same shape, so a refusal test alone was green before the door existed. The pole is
// TestAOnABackgroundRowRunsTheVerbInASessionItMakes and, end to end,
// TestE2EUIDoorAOnABackgroundRowMakesTheSessionAndRunsTheVerb.
func TestTheRefusalReachesTheScreenAtTheCommittedSize(t *testing.T) {
	d := &doorTmux{}
	m := doorModel(t, d, wakeRow("30f3382b", "cicd", "interactive", "idle"))
	m.width, m.height = 80, 24

	before := len(d.calls)
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if cmd != nil {
		t.Error("a row with no door returned a command")
	}
	if got := len(d.calls) - before; got != 0 {
		t.Errorf("the refusal still talked to tmux %d times", got)
	}
	screen := out.(model).View()
	for _, want := range []string{"cicd", `"interactive"`, "/background"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the screen at 80 columns does not carry %q:\n%s", want, screen)
		}
	}
	// Nothing may run past the terminal at the size §16 calls the one to hold.
	for _, ln := range strings.Split(screen, "\n") {
		if lines.Width(ln) > 80 {
			t.Errorf("a line is %d wide: %q", lines.Width(ln), ln)
		}
	}
}

// `K` on a pane-less row refuses, and the REMEDY has to survive the width — including when the
// session is named after the prompt that started it, which is what the operator's own fleet looks
// like.
//
// Measured live before the name was bounded: `nothing killed — 20260803--store-online-takes-too-long-
// to-ci-cd-troubleshooting ⑂ давай раскатаем Dockerfile goldens  +2`, where the `+2` is lines.Fit
// saying it had dropped the remedy and the host. The template had been sized for 80 columns; the
// variable in it had not.
func TestTheKillRefusalKeepsItsRemedyWhateverTheSessionIsCalled(t *testing.T) {
	long := "20260803--store-online-takes-too-long-to-ci-cd-troubleshooting ⑂ давай раскатаем " +
		"Dockerfile goldens - по всему флоту"
	for _, c := range []struct {
		name, kind string
		st         state.State
		want       string
	}{
		{"a background job", "background", state.Needs, "claude stop"},
		{"a finished job", "background", state.Done, "claude logs"},
		{"an interactive session", "interactive", state.Idle, "end it in its own terminal"},
	} {
		row := wakeRow("30f3382b", long, c.kind, "")
		row.ClassifiedState = c.st
		m := doorModel(t, &doorTmux{}, row)
		m.sel.Toggle(SelectionKey{Host: row.Host, PaneID: row.PaneID})

		out, _ := m.confirmKill()
		note := out.(model).note
		if !strings.Contains(note, "nothing killed") {
			t.Errorf("%s: the refusal does not say the whole act was declined: %q", c.name, note)
		}
		if !strings.Contains(note, c.want) {
			t.Errorf("%s: the remedy %q is gone: %q", c.name, c.want, note)
		}
		// 80 columns, the size §16 commits to. At exactly 80 the fleet beside it is dropped with a
		// `+N`, which is a marked loss the operator can act on; the remedy going is not.
		if lines.Width(note) > 80 {
			t.Errorf("%s: the refusal is %d columns, so the footer will drop the remedy: %q",
				c.name, lines.Width(note), note)
		}
	}
}

// `x` on a pane-less row refuses, and its REASON has to survive the width for the same reason the
// kill refusal's remedy does. Measured live: `nothing hidden — 20260803--store-online-takes-too-long-
// to-ci-cd-troubleshooting ⑂ давай раскатаем Dockerfile goldens  +1`, where the `+1` is lines.Fit
// reporting that `a mark would never expire` had gone.
func TestTheHideRefusalKeepsItsReasonWhateverTheSessionIsCalled(t *testing.T) {
	long := "20260803--store-online-takes-too-long-to-ci-cd-troubleshooting ⑂ давай раскатаем " +
		"Dockerfile goldens - по всему флоту"
	row := wakeRow("30f3382b", long, "background", "blocked")
	// base(), not doorModel(): the hide refusal needs a hidden SET, and doorModel builds a hub with
	// none — its answer would then be "cannot hide: hidden set is not available", which is a different
	// refusal about a different thing.
	m := base(t, 100, 24, row)
	m.sel.Toggle(SelectionKey{Host: row.Host, PaneID: row.PaneID})

	after := m.hideSubject()
	if !strings.Contains(after.note, "nothing hidden") {
		t.Errorf("the refusal does not say the act was declined: %q", after.note)
	}
	if !strings.Contains(after.note, "never expire") {
		t.Errorf("the REASON is gone: %q", after.note)
	}
	if lines.Width(after.note) > 80 {
		t.Errorf("the refusal is %d columns, so the footer will drop the reason: %q",
			lines.Width(after.note), after.note)
	}
}

// One owner for the bound, so a fourth refusal cannot be written without it.
func TestShortSubjectBoundsAndMarks(t *testing.T) {
	if got := shortSubject("api"); got != "api" {
		t.Errorf("a short name was changed: %q", got)
	}
	long := shortSubject("20260803--store-online-takes-too-long-to-ci-cd-troubleshooting")
	if lines.Width(long) > 21 {
		t.Errorf("the bound does not hold: %d columns, %q", lines.Width(long), long)
	}
	if !strings.HasSuffix(long, "…") {
		t.Errorf("a cut name does not say it was cut: %q", long)
	}
	// A name of double-width glyphs is bounded by COLUMNS, not runes: `Truncate` is display-width
	// aware and a rune count would let a CJK name run to twice the room.
	if got := shortSubject(strings.Repeat("中", 30)); lines.Width(got) > 21 {
		t.Errorf("a double-width name is %d columns: %q", lines.Width(got), got)
	}
}

// The door tells tmux what to call the window, through the ARGV rather than through a pure function:
// a namer that returns the right string and a create that never passes it are the same screen.
//
// Without `-n` tmux renames the window to whatever is running, which is why every door session on the
// operator's own server read `sh` under a session named after the row (§21.18). Measured on both fleet
// versions, `-n` also turns `automatic-rename` off by itself, so no second command is needed — and a
// second command after a create is the shape this repo already refuses on a measurement.
func TestTheDoorTellsTmuxWhatToCallTheWindow(t *testing.T) {
	rec := &possessionRecorder{}
	m := newTestModel(t, rec)
	row := wakeRow("30f3382b", "20260817-cicd", "background", "blocked")
	m.panes = []registry.Pane{row}

	_, cmd := m.wake(row, localHost())
	if cmd == nil {
		t.Fatal("wake returned no command")
	}
	cmd()

	create := callOf(t, rec, "new-session")
	var name string
	for i, a := range create {
		if a == "-n" && i+1 < len(create) {
			name = create[i+1]
		}
	}
	if name == "" {
		t.Fatalf("the create passes no -n, so tmux will rename the window to the wrapper shell "+
			"and the session tree will read `sh`: %q", create)
	}
	if name != "20260817-cicd" {
		t.Errorf("-n %q, want the name the dashboard shows", name)
	}
	if err := tmux.Validate(create); err != nil {
		t.Errorf("the argv the door builds is refused by the seam: %v (%q)", err, create)
	}
}

// A background row with NO SHORT ID is refused, and the refusal is the only honest answer.
//
// Measured on such a row before this: `wakePayload("")` builds `claude attach ”` — the verb with an
// empty argument — and `wakeName` returns the row's plain name, which is the SAME name for every row
// of that name, so two of them share one door and the second `a` walks the operator into the first
// one's session. The id is the only field guaranteed unique among background rows, so without it there
// is nothing to make the name unique with.
//
// `agents.Session.ID` is documented as absent on some versions of the vendor's listing, so this is a
// row the fleet can really produce, not a hypothetical.
func TestABackgroundRowWithNoShortIDIsRefusedRatherThanWoken(t *testing.T) {
	row := wakeRow("", "myproject", "background", "blocked")
	row.AgentID = ""

	ok, why := wakeable(row)
	if ok {
		t.Fatalf("a row with no short id is wakeable, so `a` would run `claude attach ''` and two "+
			"such rows would share one door: %+v", row)
	}
	if !strings.Contains(why, "myproject") {
		t.Errorf("the refusal does not name the row: %q", why)
	}
	if !strings.Contains(why, "claude agents") {
		t.Errorf("the refusal carries no remedy: %q", why)
	}
	// A refusal is a layout object: it has to fit the size §16 commits to, beside the fleet.
	m := base(t, 80, 24, row)
	m = m.cursorTo(0)
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	note := out.(model).note
	if lines.Width(note) > 80 {
		t.Errorf("the refusal is %d columns, so the footer drops its tail: %q", lines.Width(note), note)
	}
	if !strings.Contains(note, "claude agents") {
		t.Errorf("the remedy did not reach the screen: %q", note)
	}
	// And the row with an id is still wakeable, or the guard would have closed the door for everyone.
	if ok, why := wakeable(wakeRow("30f3382b", "cicd", "background", "blocked")); !ok {
		t.Errorf("a normal background row is no longer wakeable: %q", why)
	}
}
