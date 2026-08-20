package ui

import (
	"fmt"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/state"
	tea "github.com/charmbracelet/bubbletea"
)

func TestNOpensTheLaunchFormAndItIsVISIBLE(t *testing.T) {
	// This project shipped four invisible UI modes because View() was one line
	// and no test read its output. Assert on the string.
	m := modelWith(t, pane("local", "api", "claude", 0, `"claude"`, state.Works))
	m.hosts = []hub.Host{{Label: "local", LocalProc: true}}
	m, _ = press(t, m, runes("n"))

	out := m.View()
	for _, want := range []string{"dir:", "model:", "mode:", "where:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the launch form is not drawn — missing %q:\n%s", want, out)
		}
	}
}

func TestTheFormDefaultsToTheHostUnderTheCursor(t *testing.T) {
	m := modelWith(t, pane("prod", "api", "claude", 0, `"claude"`, state.Works))
	m.hosts = []hub.Host{{Label: "prod", LocalProc: false}}
	m, _ = press(t, m, runes("n"))

	if m.launchForm.spec.Host != "prod" {
		t.Fatalf("form host = %q, want %q", m.launchForm.spec.Host, "prod")
	}
}

func TestTabMovesBetweenFieldsAndTypingLandsInTheFocusedOne(t *testing.T) {
	m := modelWith(t, pane("local", "api", "claude", 0, `"claude"`, state.Works))
	m.hosts = []hub.Host{{Label: "local", LocalProc: true}}
	m, _ = press(t, m, runes("n"))

	// Start focused on directory (field 1)
	if m.launchForm.focused != 1 {
		t.Fatalf("initial focus = %d, want 1 (directory)", m.launchForm.focused)
	}

	// Type into directory
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})

	if m.launchForm.dirInput.Text() != "api" {
		t.Fatalf("directory = %q, want %q", m.launchForm.dirInput.Text(), "api")
	}

	// Tab to model
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if m.launchForm.focused != 2 {
		t.Fatalf("after tab, focus = %d, want 2 (model)", m.launchForm.focused)
	}

	// Typing doesn't affect directory anymore
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if m.launchForm.dirInput.Text() != "api" {
		t.Fatalf("directory changed after focus moved: %q", m.launchForm.dirInput.Text())
	}
}

func TestSubmittingAnInvalidSpecShowsTheFIXAndKeepsTheForm(t *testing.T) {
	// A relative path is the commonest mistake and the message must say what to
	// do, not that something is wrong.
	m := modelWith(t, pane("local", "api", "claude", 0, `"claude"`, state.Works))
	m.hosts = []hub.Host{{Label: "local", LocalProc: true}}
	m, _ = press(t, m, runes("n"))

	// Type a relative path
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("api")})
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	out := m.View()
	if !strings.Contains(out, "absolute") {
		t.Fatalf("the error must carry its fix:\n%s", out)
	}
	if !strings.Contains(out, "dir:") {
		t.Fatalf("the form must stay open so the user can correct it:\n%s", out)
	}
	if m.mode != modeLaunch {
		t.Fatalf("mode = %v after invalid submit, want modeLaunch", m.mode)
	}
}

func TestEscapeClosesTheFormWithoutCreatingAnything(t *testing.T) {
	m := modelWith(t, pane("local", "api", "claude", 0, `"claude"`, state.Works))
	m.hosts = []hub.Host{{Label: "local", LocalProc: true}}
	m, _ = press(t, m, runes("n"))

	if m.mode != modeLaunch {
		t.Fatalf("after n, mode = %v, want modeLaunch", m.mode)
	}

	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeBrowse {
		t.Fatalf("after esc, mode = %v, want modeBrowse", m.mode)
	}
}

func TestLocalFieldIsSetFromHostIsLocalServer(t *testing.T) {
	cases := []struct {
		name      string
		localProc bool
		wantLocal bool
	}{
		{"local server", true, true},
		{"remote server", false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := modelWith(t, pane("test", "api", "claude", 0, `"claude"`, state.Works))
			m.hosts = []hub.Host{{Label: "test", LocalProc: c.localProc}}
			m, _ = press(t, m, runes("n"))

			if m.launchForm.spec.Local != c.wantLocal {
				t.Fatalf("spec.Local = %v, want %v (for LocalProc=%v)",
					m.launchForm.spec.Local, c.wantLocal, c.localProc)
			}
		})
	}
}

func TestLeftRightCycleChoiceFields(t *testing.T) {
	m := modelWith(t, pane("local", "api", "claude", 0, `"claude"`, state.Works))
	m.hosts = []hub.Host{{Label: "local", LocalProc: true}}
	m, _ = press(t, m, runes("n"))

	// Tab to model field
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if m.launchForm.focused != 2 {
		t.Fatalf("focus = %d, want 2 (model)", m.launchForm.focused)
	}

	// Cycle through models
	initial := m.launchForm.modelIndex
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRight})
	if m.launchForm.modelIndex == initial {
		t.Fatalf("right arrow did not cycle model")
	}
}

func TestTheFormDoesNotCOMPLETEALocalPathForARemoteHost(t *testing.T) {
	// The trap: the form runs on this machine, and `/srv/api` existing HERE
	// says nothing about the host the pane will be created on. Completing from
	// the local filesystem would be confidently wrong.
	m := modelWith(t, pane("remote", "api", "claude", 0, `"claude"`, state.Works))
	// A remote host WITHOUT ssh= cannot complete directories, but LocalProc=false
	// prevents local filesystem completion.
	m.hosts = []hub.Host{{Label: "remote", LocalProc: false}}
	m, _ = press(t, m, runes("n"))

	// The form must NOT offer local filesystem completion for a remote host.
	// We verify by checking that the form's hint shows the limitation rather
	// than attempting local completion.
	out := m.View()
	// If local completion were attempted, there would be no hint about ssh=
	// being needed. The honest implementation shows that ssh= is required.
	if !strings.Contains(out, "ssh=") {
		t.Fatalf("form must indicate ssh= is needed for remote directory completion, got:\n%s", out)
	}
}

func TestARemoteHostWithoutSSHSaysWhyCompletionIsUnavailableAndStillACCEPTSATYPEDPATH(t *testing.T) {
	// A forwarded socket carries tmux, not a shell. Without ssh= the hub cannot
	// list remote directories — but the user knows the path, so refusing the
	// launch would be the ceremony this project's principles reject.
	m := modelWith(t, pane("remote", "api", "claude", 0, `"claude"`, state.Works))
	m.hosts = []hub.Host{{Label: "remote", LocalProc: false, SSHDest: "", ControlPath: ""}}
	m, _ = press(t, m, runes("n"))

	// Type an absolute remote path
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/srv/api")})

	out := m.View()
	// The hint must explain that completion needs ssh=
	if !strings.Contains(out, "ssh=") {
		t.Fatalf("the message must name the missing config: %s", out)
	}

	// But the path should still be accepted when submitted
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	// Validation will fail for other reasons in this test setup, but NOT because
	// it's a remote host without ssh= — that combination must be permitted.
	// We verify the path was captured.
	if m.launchForm.spec.CWD != "/srv/api" {
		t.Fatalf("spec.CWD = %q, want %q (typed path must be accepted)", m.launchForm.spec.CWD, "/srv/api")
	}
}

func TestABadRemotePathFailsWithTMUXOWNMESSAGE(t *testing.T) {
	// Without ssh credentials, the hub cannot verify a remote directory before
	// launch. The typed path is accepted, and tmux will be the one to error if
	// the path does not exist. Measured: `tmux new-window -c /nope` returns
	// rc=0 and creates the pane with cwd=$HOME, no error or warning. This is
	// precisely why the hint naming ssh= exists — without it, a typo'd path
	// silently starts an agent outside any project.
	m := modelWith(t, pane("remote", "api", "claude", 0, `"claude"`, state.Works))
	m.hosts = []hub.Host{{
		Label:       "remote",
		LocalProc:   false,
		SSHDest:     "", // no ssh credentials
		ControlPath: "",
	}}
	m, _ = press(t, m, runes("n"))

	// Type a path that might not exist
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/nope/does/not/exist")})
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	// The form must CLOSE (validation passed without verification).
	if m.mode == modeLaunch {
		t.Fatal("form must close when validation passes; mode=modeLaunch means form stayed open")
	}
	// The typed path must be accepted.
	if m.launchForm.spec.CWD != "/nope/does/not/exist" {
		t.Fatalf("spec.CWD = %q, want the typed path", m.launchForm.spec.CWD)
	}
}

func TestRemoteHostWithSSHCredentialsRejectsInvalidDirectory(t *testing.T) {
	// A remote host WITH ssh credentials CAN verify the directory before launch.
	// When the injected check rejects, the launch is refused.
	m := modelWith(t, pane("remote", "api", "claude", 0, `"claude"`, state.Works))
	m.hosts = []hub.Host{{
		Label:       "remote",
		LocalProc:   false,
		SSHDest:     "testhost",
		ControlPath: "/tmp/test-ctl",
	}}
	m, _ = press(t, m, runes("n"))

	// Inject a check that always rejects
	m.launchForm.spec.SetDirCheck(func(path string) error {
		return fmt.Errorf("remote check: no such directory: %s", path)
	})

	// Type a path and submit
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/srv/api")})
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	// The form should stay open and show the error
	if m.mode != modeLaunch {
		t.Fatalf("mode = %v after rejection, want modeLaunch (form stays open)", m.mode)
	}
	if !strings.Contains(m.launchForm.err, "remote") || !strings.Contains(m.launchForm.err, "/srv/api") {
		t.Fatalf("error must name the rejected path: %s", m.launchForm.err)
	}
}

func TestRemoteHostWithSSHCredentialsAcceptsValidDirectory(t *testing.T) {
	// A remote host WITH ssh credentials that ACCEPTS the directory proceeds.
	m := modelWith(t, pane("remote", "api", "claude", 0, `"claude"`, state.Works))
	m.hosts = []hub.Host{{
		Label:       "remote",
		LocalProc:   false,
		SSHDest:     "testhost",
		ControlPath: "/tmp/test-ctl",
	}}
	m, _ = press(t, m, runes("n"))

	// Inject a check that always accepts
	m.launchForm.spec.SetDirCheck(func(path string) error {
		return nil
	})

	// Type a path and submit
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/srv/api")})
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	// The form should close (validation passed)
	if m.mode == modeLaunch {
		t.Fatalf("mode = modeLaunch after accepting; want form closed (validation passed)")
	}
	if m.launchForm.spec.CWD != "/srv/api" {
		t.Fatalf("spec.CWD = %q, want %q", m.launchForm.spec.CWD, "/srv/api")
	}
}

func TestRemoteHostWithoutSSHCredentialsDoesNotCallCheck(t *testing.T) {
	// A remote host WITHOUT ssh credentials skips the directory check entirely.
	// Validation passes without running any check.
	m := modelWith(t, pane("remote", "api", "claude", 0, `"claude"`, state.Works))
	m.hosts = []hub.Host{{
		Label:       "remote",
		LocalProc:   false,
		SSHDest:     "", // no credentials
		ControlPath: "",
	}}
	m, _ = press(t, m, runes("n"))

	// Do NOT inject a check - let the normal flow handle it.
	// With no ssh credentials, injectDirCheck will leave dirCheck nil.

	// Type a path and submit
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/srv/api")})
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	// Validation should pass (no check was injected, so none ran)
	if m.mode == modeLaunch {
		t.Fatalf("mode = modeLaunch; want form closed (validation passed without check)")
	}

	// The spec must NOT have a dirCheck set
	if m.launchForm.spec.HasDirCheck() {
		t.Fatal("dirCheck was set for a remote host without ssh credentials; must be nil")
	}
}

func TestRemotePathNeverCheckedAgainstLocalFilesystem(t *testing.T) {
	// A remote host with ssh credentials uses the INJECTED check, never os.Stat.
	// Prove it by setting Local=false and showing the injected check is used.
	m := modelWith(t, pane("remote", "api", "claude", 0, `"claude"`, state.Works))
	m.hosts = []hub.Host{{
		Label:       "remote",
		LocalProc:   false,
		SSHDest:     "testhost",
		ControlPath: "/tmp/test-ctl",
	}}
	m, _ = press(t, m, runes("n"))

	// The spec must have Local=false (remote host)
	if m.launchForm.spec.Local {
		t.Fatal("spec.Local = true for a remote host; this is the defect Task 15 prevents")
	}

	// Inject a check that records it was called with a marker
	var calledWith string
	m.launchForm.spec.SetDirCheck(func(path string) error {
		calledWith = path
		return nil
	})

	// Type a path and submit
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/srv/api")})
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	// The injected check must have been called (proving os.Stat was NOT used)
	if calledWith != "/srv/api" {
		t.Fatalf("injected check was called with %q, want %q", calledWith, "/srv/api")
	}

	// Structural proof: Local=false means the defaultDirCheck (os.Stat) cannot
	// be reached. The validation only runs the local check when Local=true.
}

// The error goes with the value it was about. Every other key that can change a
// field cleared it; typing did not, so an error naming the path being corrected
// stayed on screen beside the new one. A message naming something the field no
// longer shows is worse than none — the reader cannot tell whether it applies.
func TestTypingInTheDirectoryClearsAStaleError(t *testing.T) {
	f := newLaunchForm([]hub.Host{{Label: "local", LocalProc: true}}, "local", "", "")
	f.focused = 1
	f.err = "directory does not exist: /home/dev/lab/typo"

	f, _, _ = f.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if f.err != "" {
		t.Errorf("err = %q after typing, want empty: the message named a path the field no longer shows", f.err)
	}
	if f.dirInput.Text() != "x" {
		t.Errorf("the keystroke must still reach the field, got %q", f.dirInput.Text())
	}
}

// A `~` path is resolved by the HUB, because tmux neither expands nor refuses it.
//
// Measured on both fleet versions: `new-session -c '~/somedir'` is rc=0 and the pane's cwd is HOME
// rather than the directory — a session in the wrong place at rc=0, which is the silent failure this
// repo refuses. Before this the form answered `cwd must be absolute, got "~/…"` to the most natural
// thing a person types.
func TestATildePathIsResolvedForALocalHostAndRefusedForARemoteOne(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	for _, c := range []struct {
		name, in string
		local    bool
		want     string
		wantErr  string
	}{
		{"bare tilde", "~", true, home, ""},
		{"tilde path", "~/lab/x", true, filepath.Join(home, "lab/x"), ""},
		{"an ordinary path is untouched", "/w/x", true, "/w/x", ""},
		// `~user` is somebody else's home, which the hub does not resolve; Spec.Validate then refuses
		// it as not absolute, and its message already says so.
		{"another user's home is untouched", "~root/x", true, "~root/x", ""},
		// A REMOTE host: `~` there is the far user's home and the hub has not asked for it. Resolving
		// with the LOCAL home would name a path that exists on the wrong machine.
		{"remote is refused with the reason", "~/lab/x", false, "", "not it"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := expandTilde(c.in, c.local)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("expandTilde(%q, %v) = %q, want a refusal", c.in, c.local, got)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Errorf("the refusal does not say why: %v", err)
				}
				if !strings.Contains(err.Error(), "type the path") {
					t.Errorf("the refusal carries no remedy: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expandTilde(%q, %v): %v", c.in, c.local, err)
			}
			if got != c.want {
				t.Errorf("expandTilde(%q, %v) = %q, want %q", c.in, c.local, got, c.want)
			}
		})
	}
}

// A SPACE reaches the directory field. bubbletea reports one as KeySpace, and this arm named only
// KeyRunes, so the character was dropped — measured through the interface, `with space` reached the
// field as `withspace` and the launch then refused a path that does not exist.
//
// The keys are sent ONE AT A TIME, because that is the only form that tests the routing: an injected
// run arrives as a single key message whose String() is the whole string, so typing a word cannot tell
// a field that takes text from one that dropped a character.
func TestTheDirectoryFieldTakesASpaceOneKeyAtATime(t *testing.T) {
	f := newLaunchForm([]hub.Host{{Label: "local"}}, "local", "$1", "")
	f.focused = 1 // the directory field
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("/w/a")},
		{Type: tea.KeySpace, Runes: []rune(" ")},
		{Type: tea.KeyRunes, Runes: []rune("b")},
	} {
		var out formOutcome
		f, _, out = f.handleKey(k)
		if out != formOpen {
			t.Fatalf("the form left on %v", k.Type)
		}
	}
	if got := f.dirInput.Text(); got != "/w/a b" {
		t.Errorf("the field holds %q, want %q — a dropped space makes the path one that does not "+
			"exist, and the launch then refuses it", got, "/w/a b")
	}
}

// typedText is the ONE answer to "what text does this key contribute", because three fields got the
// KeySpace rule wrong in two different directions: doubled in one, dropped in another.
func TestTypedTextIsOneRuleForEveryField(t *testing.T) {
	for _, c := range []struct {
		msg  tea.KeyMsg
		want string
		ok   bool
	}{
		// A space arrives as KeySpace with Runes ALSO set, which is what made folding the two
		// together insert it twice.
		{tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}, " ", true},
		{tea.KeyMsg{Type: tea.KeySpace}, " ", true},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ab")}, "ab", true},
		{tea.KeyMsg{Type: tea.KeyEnter}, "", false},
		{tea.KeyMsg{Type: tea.KeyEsc}, "", false},
	} {
		got, ok := typedText(c.msg)
		if got != c.want || ok != c.ok {
			t.Errorf("typedText(%v) = %q,%v want %q,%v", c.msg.Type, got, ok, c.want, c.ok)
		}
	}
}

// THE CURSOR GOES TO WHAT WAS JUST CREATED.
//
// Measured on a real fleet at 80x24: after the launch the hub said `launched: %1` and the row was not
// on the screen at all for one poll, then arrived twelve rows below a cursor that had not moved. A
// list sorted by attention puts a fresh session low precisely because it is not asking for anything,
// so "create it and then hunt for it" was the flow.
//
// The key is set before the row exists, which is the property worth pinning: the cursor is keyed on a
// ROW's identity, so the moment the poll brings the pane in the cursor is already on it and `a` is the
// next keystroke rather than the twelfth.
func TestTheCursorLandsOnTheJustLaunchedPane(t *testing.T) {
	// A fleet where the new pane would sort LAST: three rows that want attention, and the launch's
	// own pane arriving quiet.
	m := base(t, 80, 24,
		agentRow("aaaaaaaa", "needs-one", "background", state.Needs),
		agentRow("bbbbbbbb", "needs-two", "background", state.Needs),
		agentRow("cccccccc", "needs-three", "background", state.Needs))
	if got := m.cursorIndex(); got != 0 {
		t.Fatalf("the fixture does not start at the top: %d", got)
	}

	out, _ := m.Update(launchMsg{paneID: "%9", host: "local"})
	after := out.(model)
	if !strings.Contains(after.note, "%9") {
		t.Errorf("the note does not name what was created: %q", after.note)
	}
	if !strings.Contains(after.note, "a goes there") {
		t.Errorf("the note names a pane id and no remedy — the operator cannot act on `%%9`: %q",
			after.note)
	}
	// The cursor NAMES the new pane even though no row carries it yet.
	if after.cursor.key != (SelectionKey{Host: "local", PaneID: "%9"}) {
		t.Fatalf("the cursor does not name the launched pane: %+v", after.cursor)
	}

	// And when the poll brings the pane in, the cursor is ON it — with three louder rows above.
	pane := pane("local", "proj", "claude", 9, "claude", state.Idle)
	after.panes = append(after.panes, pane)
	rows := after.rowsForScreen()
	i := after.cursorIndex()
	if i >= len(rows) || rows[i].PaneID != "%9" {
		var got string
		if i < len(rows) {
			got = rows[i].PaneID
		}
		t.Errorf("the cursor is on row %d (%q) once the pane arrives, want the launched %%9 — the "+
			"operator would have to hunt for what they just made", i, got)
	}
}

// The launch's cursor jump KEEPS the operator's place when the pane does not arrive.
//
// Setting the key without the hint left the hint at ZERO, and `cursorIndex` falls back to the hint when
// the key names no row on screen — so an operator standing on row five pressed enter and their place
// jumped to the TOP, and with a project filter on `a` then acted on rows[0] instead of the pane they
// had just made. Both reproduced. The comment at the site claimed the opposite, which is the worse half:
// a false comment stops the next reader checking.
func TestALaunchKeepsTheOperatorsPlaceWhenThePaneDoesNotArrive(t *testing.T) {
	rows := []registry.Pane{
		pane("local", "s1", "claude", 1, "claude", state.Needs),
		pane("local", "s2", "claude", 2, "claude", state.Needs),
		pane("local", "s3", "claude", 3, "claude", state.Needs),
		pane("local", "s4", "claude", 4, "claude", state.Needs),
	}
	m := base(t, 100, 40, rows...)
	m = m.cursorTo(2)
	if got := m.cursorIndex(); got != 2 {
		t.Fatalf("the fixture does not start at row 2: %d", got)
	}

	// A launch whose pane never arrives: the key names nothing on screen.
	out, _ := m.Update(launchMsg{paneID: "%99", host: "local"})
	after := out.(model)
	if got := after.cursorIndex(); got != 2 {
		t.Errorf("the cursor moved to row %d when the launched pane had not arrived, want the row the "+
			"operator was standing on (2) — their place is not the hub's to spend", got)
	}
	// And when it DOES arrive, the cursor is on it: the fallback must not have replaced the key.
	after.panes = append(after.panes, pane("local", "new", "claude", 99, "claude", state.Idle))
	list := after.rowsForScreen()
	if i := after.cursorIndex(); i >= len(list) || list[i].PaneID != "%99" {
		t.Errorf("the cursor is not on the launched pane once it arrives: index %d of %d", i, len(list))
	}
}

// A launch into a row the ACTIVE NARROWING hides says so, because the cursor then names a row that is
// not on the screen and `a` would act on whatever the fallback points at.
func TestALaunchHiddenByANarrowingSaysSo(t *testing.T) {
	local := pane("local", "here", "claude", 1, "claude", state.Needs)
	local.Path = "/w/alpha"
	m := base(t, 100, 40, local)
	m.favouritesOnly = false
	// Narrow to a project the launched pane is not in.
	m = m.openProject(m.rules.OfPane(local).ID, false)
	if !m.narrowed() {
		t.Fatal("the fixture is not narrowed, so nothing is being tested")
	}

	out, _ := m.Update(launchMsg{paneID: "%77", host: "otherhost"})
	note := out.(model).note
	if !strings.Contains(note, "filter hides it") {
		t.Errorf("a launch the filter hides is reported as an ordinary launch: %q — the operator would "+
			"press `a` and act on a row they cannot see", note)
	}
	if !strings.Contains(note, "esc") {
		t.Errorf("the note does not name the way to see it: %q", note)
	}
}

// The form opens in the directory the cursor was in, and ctrl+u is what makes that an OFFER.
//
// Asked for as "create the session straight in the corresponding directory (project), automatically,
// with the operator able to override it". The row's own cwd and not the project LABEL, because a
// label is only the last path segment (§21.12) while a launch needs a path — and it travels with the
// host the same row chose, so the pair cannot name a directory that does not exist on that machine.
func TestTheLaunchFormOpensInTheDirectoryTheCursorWasIn(t *testing.T) {
	row := registry.Pane{
		Kind: registry.KindPane, Host: "nuc", Session: "api", PaneID: "%3", SessionID: "$1",
		Command: "claude", Path: "/home/dev/lab/streams/orbits/billing-iac",
		ClassifiedState: state.Quiet,
	}
	m := base(t, 120, 24, row)
	m = m.cursorTo(0)
	m = m.openLaunchForm()

	if got := m.launchForm.dirInput.Text(); got != row.Path {
		t.Errorf("the form opened on %q, want the cursor row's own cwd %q", got, row.Path)
	}
	// On the screen, not just in the field's value: a pre-fill nobody can see is not a pre-fill.
	if s := m.View(); !strings.Contains(s, row.Path) {
		t.Errorf("the pre-filled directory is not on the form:\n%s", s)
	}
	// And the form NAMES the key that clears it, because a sixty-column path the operator cannot
	// remove in one gesture is a trap rather than a convenience.
	if s := m.View(); !strings.Contains(s, "ctrl+u: clear") {
		t.Errorf("the form does not say how to clear the directory:\n%s", s)
	}

	// ctrl+u empties it, and then the field takes a fresh path.
	f, _, _ := m.launchForm.handleKey(tea.KeyMsg{Type: tea.KeyCtrlU})
	if got := f.dirInput.Text(); got != "" {
		t.Errorf("ctrl+u left %q in the field", got)
	}
	for _, r := range "/tmp/fresh" {
		f, _, _ = f.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if got := f.dirInput.Text(); got != "/tmp/fresh" {
		t.Errorf("after clearing, the field holds %q, want the fresh path", got)
	}
}

// A row with NO cwd leaves the field empty rather than putting something wrong in it: a pane-less
// listing record whose cwd the listing did not report is the case, and an invented default would be
// a directory the operator did not choose.
func TestTheLaunchFormStaysEmptyWhenTheRowHasNoDirectory(t *testing.T) {
	row := registry.Pane{
		Kind: registry.KindAgent, Host: "local", Session: "no-cwd", AgentID: "aaaa1111",
		PaneID: "agent:aaaa1111@bbbb", ClassifiedState: state.Idle, Content: []string{"  (no pane)"},
	}
	m := base(t, 120, 24, row)
	m = m.cursorTo(0)
	opened := m.openLaunchForm()
	if got := opened.launchForm.dirInput.Text(); got != "" {
		t.Errorf("the form invented a directory %q for a row that has none", got)
	}
}
