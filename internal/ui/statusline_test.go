package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/hub"

	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// The alias the operator typed reaches the session from INSIDE, so a name given on the dashboard is
// also what the attached session calls itself (docs/design.md §21.16).
//
// aliasPublications is the whole decision, and it is a PURE function of the rows and the aliases so
// the writes can be computed in Update — where nothing else is running — and handed to a tea.Cmd as
// a finished list. A `tea.Cmd` body runs concurrently with Update, so a command that reached into
// the model to work out what to write would be reading state another goroutine owns.

func aliasRow(sessionID, session, alias string) registry.Pane {
	return registry.Pane{Kind: registry.KindPane, Host: "local", PaneID: "%5",
		SessionID: sessionID, Session: session, ClaudeSession: "7ef2fe7e-c88d",
		Path: "/w/x", SessionAlias: alias, StatusLeft: tmux.DefaultStatusLeft,
		StatusLeftLength: "10"}
}

func aliasesFor(row registry.Pane, name string) project.Aliases {
	var a project.Aliases
	a.Set(project.AliasKeyOf(row), name)
	return a
}

// A named session whose server knows nothing yet gets all three options, and the length is part of
// it: tmux defaults `status-left-length` to TEN on both versions of this fleet, so a composition
// written without it draws `[billing-c` and stops.
func TestANamedSessionGetsTheAliasTheFormatAndTheRoom(t *testing.T) {
	row := aliasRow("$3", "20260817-cicd-30f3382b", "")
	got := aliasPublications([]registry.Pane{row}, aliasesFor(row, "billing-cicd"))

	want := map[string]string{
		tmux.AliasOption:      "billing-cicd",
		tmux.StatusLeftOption: tmux.AliasStatusLeft,
		// 40 written out rather than derived, so this asserts the WIDTH and not the formula:
		// `[billing-cicd (20260817-cicd-30f3382b)] ` is 1+12+2+22+3 columns, and it is the exact
		// string a real client was measured drawing.
		tmux.StatusLeftLengthOption: "40",
	}
	if len(got) != len(want) {
		t.Fatalf("%d writes, want %d: %+v", len(got), len(want), got)
	}
	for _, w := range got {
		if w.Session != "$3" {
			t.Errorf("write targets %q, want the tmux session id — a NAME is what this feature "+
				"changes, so it cannot also be the address", w.Session)
		}
		if w.Unset {
			t.Errorf("%s is being unset on a named session", w.Option)
		}
		if v, ok := want[w.Option]; !ok {
			t.Errorf("unexpected write of %q", w.Option)
		} else if w.Value != v {
			t.Errorf("%s = %q, want %q", w.Option, w.Value, v)
		}
	}
}

// The steady state costs NOTHING. This is what makes it safe to run on every poll, and it is why the
// options are read back from the server rather than remembered — a restarted hub also writes nothing,
// because the server already holds the answer.
func TestASessionTheServerAlreadyKnowsIsNotWrittenAgain(t *testing.T) {
	row := aliasRow("$3", "20260817-cicd-30f3382b", "billing-cicd")
	row.StatusLeft = tmux.AliasStatusLeft
	row.StatusLeftLength = "40"
	if got := aliasPublications([]registry.Pane{row}, aliasesFor(row, "billing-cicd")); len(got) != 0 {
		t.Errorf("%d writes for a session that is already right: %+v", len(got), got)
	}
}

// Un-naming UNSETS, and it unsets the format too — otherwise the hub would leave its own
// status-left behind on a session it no longer has anything to say about.
func TestRemovingTheNameUnsetsWhatTheHubWrote(t *testing.T) {
	row := aliasRow("$3", "20260817-cicd-30f3382b", "billing-cicd")
	row.StatusLeft = tmux.AliasStatusLeft
	row.StatusLeftLength = "40"

	got := aliasPublications([]registry.Pane{row}, project.Aliases{})
	if len(got) != 3 {
		t.Fatalf("%d writes to un-name, want 3 (the alias, the format and the room): %+v",
			len(got), got)
	}
	for _, w := range got {
		if !w.Unset {
			t.Errorf("%s = %q, want it UNSET so the session goes back to what it was",
				w.Option, w.Value)
		}
	}
}

// The GUARD: a status line the operator wrote is theirs. The hub still publishes the alias — that
// option collides with nothing — and leaves the drawing alone.
func TestTheOperatorsOwnStatusLineIsNotTouched(t *testing.T) {
	row := aliasRow("$3", "20260817-cicd-30f3382b", "")
	row.StatusLeft = "#[fg=green]MY OWN #S"
	row.StatusLeftLength = "40"

	got := aliasPublications([]registry.Pane{row}, aliasesFor(row, "billing-cicd"))
	if len(got) != 1 || got[0].Option != tmux.AliasOption {
		t.Fatalf("want exactly the alias published and the drawing left alone, got %+v", got)
	}
}

// And when the hub declines, it SAYS SO with the line to paste — a feature that silently does
// nothing is indistinguishable from one that is broken, and this repo's rule is that a refusal
// carries its remedy.
func TestTheRefusalNamesTheLineToAdd(t *testing.T) {
	row := aliasRow("$3", "20260817-cicd-30f3382b", "")
	row.StatusLeft = "#[fg=green]MY OWN #S"
	note := aliasNote([]registry.Pane{row}, aliasesFor(row, "billing-cicd"))
	if note == "" {
		t.Fatal("the hub declined to write and said nothing about it")
	}
	for _, want := range []string{tmux.StatusLeftOption, tmux.AliasOption} {
		if !strings.Contains(note, want) {
			t.Errorf("the note does not mention %q, so it does not say what to do: %q", want, note)
		}
	}
	// A session the hub CAN write needs no sentence.
	ok := aliasRow("$3", "20260817-cicd-30f3382b", "")
	if n := aliasNote([]registry.Pane{ok}, aliasesFor(ok, "billing-cicd")); n != "" {
		t.Errorf("a session the hub can write produced a note: %q", n)
	}
}

// A row with no tmux session of its own — a pane-less conversation — has nowhere to publish, and
// must not be mistaken for one that does. Its alias lives in projects.toml until the door gives it
// a pane.
func TestAPaneLessRowPublishesNothing(t *testing.T) {
	row := registry.Pane{Kind: registry.KindAgent, Host: "local", PaneID: "agent:7ef2fe7e",
		SessionID: "7ef2fe7e-c88d-46da-ac9d-0b57f170c3d8", Session: "20260817-cicd", Path: "/w/x"}
	if got := aliasPublications([]registry.Pane{row}, aliasesFor(row, "billing-cicd")); len(got) != 0 {
		t.Errorf("%d writes for a row with no tmux session: %+v", len(got), got)
	}
}

// One session, many panes, ONE set of writes. Every pane of a session reports the same options, so
// a publisher that keyed on the row would write the same three options once per pane.
func TestASessionWithManyPanesIsPublishedOnce(t *testing.T) {
	one := aliasRow("$3", "20260817-cicd-30f3382b", "")
	two := one
	two.PaneID = "%6"
	got := aliasPublications([]registry.Pane{one, two}, aliasesFor(one, "billing-cicd"))
	if len(got) != 3 {
		t.Errorf("%d writes for one session of two panes, want 3: %+v", len(got), got)
	}
}

// typed is a composer holding what the operator wrote, built the way they build it.
func typed(text string) Composer {
	var c Composer
	c.Insert(text)
	return c
}

// The name reaches tmux THROUGH THE GESTURE, not only through the pure function. This drives
// commitName — the model's own path — because a publisher wired to nothing passes every test of its
// arithmetic, which is this repo's signature defect one layer up.
func TestNamingASessionPublishesItToTmux(t *testing.T) {
	dir := t.TempDir()
	row := aliasRow("$3", "20260817-cicd-30f3382b", "")
	r := &recordingRunner{}
	m := model{
		run:          r,
		hosts:        []hub.Host{{Label: "local", Socket: "/tmp/test.sock"}},
		panes:        []registry.Pane{row},
		projectsPath: filepath.Join(dir, "projects.toml"),
		mode:         modeNaming,
		naming:       namingForm{subject: row, input: typed("billing-cicd")},
	}

	next, cmd := m.commitName()
	if cmd == nil {
		t.Fatal("naming a session issued no tmux write, so the session never learns its name")
	}
	if got := next.(model).note; got != "" {
		t.Errorf("a session the hub can write produced a note: %q", got)
	}
	// Run the command the way bubbletea would: ONCE, and then its children if it was a batch.
	// Calling cmd() in both arms of a type switch runs a single-command batch twice —
	// tea.Batch returns the command itself when there is only one — which is how this test
	// first reported two invocations for one write.
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			c()
		}
	}
	joined := strings.Join(r.calls, "\n")
	for _, want := range []string{
		"set -t $3 " + tmux.AliasOption + " billing-cicd",
		"set -t $3 " + tmux.StatusLeftOption,
		"set -t $3 " + tmux.StatusLeftLengthOption + " 40",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("tmux was never asked to %q:\n%s", want, joined)
		}
	}
	// ONE invocation for the three options: a poll that names three sessions must not pay nine
	// round trips.
	if len(r.calls) != 1 {
		t.Errorf("%d tmux invocations for one session, want 1:\n%s", len(r.calls), joined)
	}
}

// ONE unpublishable name must not cost every other session on that host its own.
//
// Every session on a host travels in ONE tmux invocation, and the seam refuses a literal `%` outside
// a `-t` value — measured, `set -t $2 @hub_alias 50% done` makes Validate refuse the whole argv. So
// a name like `50% done`, which is a perfectly ordinary thing to call a job, used to leave every
// other row on that host without its name, and the only sign was a note about a tmux error.
func TestAnUnpublishableNameDoesNotCostTheOthersTheirs(t *testing.T) {
	good := aliasRow("$1", "session-one", "")
	bad := aliasRow("$2", "session-two", "")
	bad.PaneID, bad.ClaudeSession = "%9", "aaaabbbb-cccc"

	var al project.Aliases
	al.Set(project.AliasKeyOf(good), "fine-name")
	al.Set(project.AliasKeyOf(bad), "50% done")
	rows := []registry.Pane{good, bad}

	ws := aliasPublications(rows, al)
	if len(ws) == 0 {
		t.Fatal("nothing published at all, so the bad name took the batch with it")
	}
	var forGood, forBad int
	for _, w := range ws {
		switch w.Session {
		case "$1":
			forGood++
		case "$2":
			if w.Option == tmux.AliasOption {
				forBad++
			}
		}
	}
	if forGood == 0 {
		t.Errorf("the session with a legal name got nothing: %+v", ws)
	}
	if forBad != 0 {
		t.Errorf("the unpublishable name was sent anyway (%d writes) — the whole argv would be "+
			"refused and neither session would be named", forBad)
	}
	// And the argv that comes out of it passes the seam, which is the property that matters.
	r := &fakeSessionRunner{}
	if err := tmux.SetSessionOptions(context.Background(), r, tmux.Target{Label: "local",
		Socket: "/tmp/t.sock"}, ws); err != nil {
		t.Fatalf("SetSessionOptions: %v", err)
	}
	if err := tmux.Validate(r.last); err != nil {
		t.Errorf("the argv the publisher builds is refused by the seam: %v\n%q", err, r.last)
	}
	// The operator is told, with the name bounded and the reason named.
	note := aliasNote(rows, al)
	if !strings.Contains(note, "50% done") || !strings.Contains(note, "%") {
		t.Errorf("the note does not name the unpublishable name and its reason: %q", note)
	}
}

// fakeSessionRunner records the last argv so a test can hand it to the seam.
type fakeSessionRunner struct{ last []string }

func (f *fakeSessionRunner) Run(_ context.Context, _ tmux.Target, args ...string) (tmux.Result, error) {
	f.last = args
	return tmux.Result{}, nil
}

// The door's window carries the name the DASHBOARD shows, so the operator's own session tree can be
// read against the hub.
//
// Measured on the operator's server before this: every door session listed one window called `sh`,
// because tmux renames a window to whatever is running unless it was created with `-n`. The SESSION
// name cannot carry the alias — it is the door's dedup key, and renaming it would make a second `a`
// create a second session (§22.3) — so the window is where a name becomes visible outside the hub.
func TestTheDoorWindowIsRenamedToTheNameTheDashboardShows(t *testing.T) {
	// A pane the door made: its start command is what says so, read from the pane rather than
	// remembered, so it still says so after the hub restarted.
	door := registry.Pane{Kind: registry.KindPane, Host: "local", PaneID: "%9", SessionID: "$3",
		WindowID: "@7", Session: "20260817-cicd-30f3382b", Window: "sh", Command: "claude",
		ClaudeSession: "30f3382b-f68c-4baf-98fd-68d4fd1c3da4",
		StartCommand:  `sh -c 'claude attach 30f3382b'`, ClassifiedState: state.Idle}
	// And one the OPERATOR made, which the hub must not touch.
	theirs := registry.Pane{Kind: registry.KindPane, Host: "local", PaneID: "%2", SessionID: "$1",
		WindowID: "@1", Session: "work", Window: "vim", Command: "vim", ClassifiedState: state.Idle}

	var al project.Aliases
	rows := []registry.Pane{door, theirs}

	ws := windowRenames(rows, al)
	if len(ws) != 1 {
		t.Fatalf("renames = %+v, want exactly one (the door's window)", ws)
	}
	if ws[0].WindowID != "@7" {
		t.Errorf("the rename names window %q, want the door's @7", ws[0].WindowID)
	}
	if ws[0].Name != "20260817-cicd-30f3382b" {
		t.Errorf("name = %q, want the name the dashboard shows", ws[0].Name)
	}
	if !ws[0].AutoOff {
		t.Error("automatic-rename is left ON, so tmux will undo the rename on the next command the " +
			"pane runs — a window made by an older hub was not created with -n")
	}

	// An ALIAS is what the dashboard shows once the operator has typed one, so it is what the window
	// gets. This is the case that has to keep working after the door has already opened.
	al.Set(project.AliasKeyOf(door), "прод-выкатка")
	ws = windowRenames(rows, al)
	if len(ws) != 1 || ws[0].Name != "прод-выкатка" {
		t.Errorf("renames = %+v, want the alias", ws)
	}

	// Already in step: nothing is sent. The publisher writes differences only, so a fleet whose
	// names agree costs no tmux commands at all.
	inStep := door
	inStep.Window = "прод-выкатка"
	if ws := windowRenames([]registry.Pane{inStep, theirs}, al); len(ws) != 0 {
		t.Errorf("renames = %+v on a fleet already in step, want none", ws)
	}
}

// A name the seam would refuse is skipped rather than sent, for the reason the alias publisher skips
// one: every rename on a host travels in ONE invocation, so the worst element decides whether the
// others land.
func TestAnUnrenamableNameDoesNotCostTheOtherWindowsTheirs(t *testing.T) {
	mk := func(pane, win, uuid, name string) registry.Pane {
		return registry.Pane{Kind: registry.KindPane, Host: "local", PaneID: pane, SessionID: "$3",
			WindowID: win, Session: name, Window: "sh", Command: "claude", ClaudeSession: uuid,
			StartCommand: `sh -c 'claude attach ` + uuid[:8] + `'`, ClassifiedState: state.Idle}
	}
	good := mk("%1", "@1", "aaaabbbb-cccc-dddd", "fine")
	bad := mk("%2", "@2", "eeeeffff-0000-1111", "unlucky")
	var al project.Aliases
	al.Set(project.AliasKeyOf(good), "fine-name")
	// A LEADING DASH is the hazard for a window name, and it is tmux's flag parser rather than the
	// hub's seam: measured, `rename-window -t @1 -wip` answers rc=1 `unknown flag -w` and leaves the
	// name alone, so one such name in a batched invocation costs every other window its own. A `%`
	// is not the hazard here — the sanitiser turns `50% done` into `50- done`, which tmux takes.
	al.Set(project.AliasKeyOf(bad), "-wip")

	ws := windowRenames([]registry.Pane{good, bad}, al)
	if len(ws) != 2 {
		t.Fatalf("renames = %+v, want both windows named", ws)
	}
	for _, w := range ws {
		if strings.HasPrefix(w.Name, "-") {
			t.Errorf("the namer produced a flag-shaped name %q, which tmux reads as a flag and "+
				"which would cost every other window in the batch its name", w.Name)
		}
	}
	r := &fakeSessionRunner{}
	if err := tmux.RenameWindows(context.Background(), r, tmux.Target{Label: "local",
		Socket: "/tmp/t.sock"}, ws); err != nil {
		t.Fatalf("RenameWindows: %v", err)
	}
	if err := tmux.Validate(r.last); err != nil {
		t.Errorf("the argv the publisher builds is refused by the seam: %v\n%q", err, r.last)
	}
}
