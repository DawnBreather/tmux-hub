//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The launch form, driven through the INTERFACE. Every case here starts at `n` on the dashboard,
// because the report these were written for — "creating a new session does not work" — is about what
// the keystroke does end to end, and the defect it found was three layers below the form: tmux
// answering `duplicate session:` to a name the launch had no second choice for.
//
// The fixture watches a server on another socket, except where a case says otherwise: the operator's
// own shape is a hub watching the server it RUNS IN, and until this file nothing covered that.

// launchFixture is a hub, a watched server, and a scratch directory to launch into.
type launchFixture struct {
	sock   string // the hub's own tmux server
	target string // the server the hub watches
	work   string
}

// startLaunch brings up the hub. `selfWatched` points the watched host at the hub's OWN server, which
// is the operator's shape. seed names are sessions created on the watched server before the hub runs,
// so a case can make a name collide.
func startLaunch(t *testing.T, cols, rows int, selfWatched bool, seed ...string) launchFixture {
	t.Helper()
	if _, err := exec.LookPath("claude"); err != nil {
		// The launch runs the real `claude`: the argv three layers below the spec is what these
		// cases are about, so a substituted command would test the substitution. Without it the pane
		// exits at once and the session is gone before it can be read.
		t.Skip("claude not installed, so a launch cannot produce a session to look at")
	}
	bin := buildBinary(t)
	work := t.TempDir()
	sock := filepath.Join(work, "ui.sock")
	target := filepath.Join(work, "target.sock")
	if selfWatched {
		target = sock
	}
	run := func(s string, args ...string) (string, error) {
		full := append([]string{"-S", s, "-f", "/dev/null"}, args...)
		out, err := exec.Command("tmux", full...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", target, "kill-server").Run()
		_ = exec.Command("tmux", "-S", sock, "kill-server").Run()
	})

	// One pane on the watched server, so the fleet is not empty and the harness can wait for a
	// signal that the poll has landed rather than for the header, which paints before any poll.
	if out, err := run(target, "new-session", "-d", "-s", "work", "-c", work, "sleep", "300"); err != nil {
		t.Fatalf("seed the watched server: %v: %s", err, out)
	}
	for _, name := range seed {
		if out, err := run(target, "new-session", "-d", "-s", name, "-c", work,
			"sleep", "300"); err != nil {
			t.Fatalf("seed session %q: %v: %s", name, err, out)
		}
	}
	hosts := filepath.Join(work, "hosts.toml")
	if err := os.WriteFile(hosts, []byte("[[host]]\nalias = \"nothing\"\nenabled = false\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	cmd := fmt.Sprintf("%s --hosts %s --no-local --host scratch=%s,local --hidden %s --view=flat --no-history; "+
		"echo EXITED-rc=$?; sleep 60", bin, hosts, target, filepath.Join(work, "hidden.json"))
	if out, err := run(sock, "new-session", "-d", "-s", "ui", "-x", fmt.Sprint(cols),
		"-y", fmt.Sprint(rows), "-c", work, cmd); err != nil {
		t.Fatalf("start the hub: %v: %s", err, out)
	}
	f := launchFixture{sock: sock, target: target, work: work}
	waitUntil(t, "the watched server's pane to reach the screen", 30*time.Second, func() bool {
		s, err := paneScreen(t, sock, "ui:0")
		return err == nil && strings.Contains(s, "sleep")
	})
	return f
}

// keys names the hub's WINDOW rather than its session: a launch or an attach can move the client, and
// a key sent to the session would then land somewhere else.
func (f launchFixture) keys(t *testing.T, keys ...string) {
	t.Helper()
	args := append([]string{"-S", f.sock, "send-keys", "-t", "ui:0"}, keys...)
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		t.Fatalf("send-keys %v: %v: %s", keys, err, out)
	}
	time.Sleep(300 * time.Millisecond)
}

func (f launchFixture) typeText(t *testing.T, text string) {
	t.Helper()
	if out, err := exec.Command("tmux", "-S", f.sock, "send-keys", "-t", "ui:0", "-l",
		text).CombinedOutput(); err != nil {
		t.Fatalf("type %q: %v: %s", text, err, out)
	}
	time.Sleep(300 * time.Millisecond)
}

func (f launchFixture) screen(t *testing.T) string { return capturePane(t, f.sock, "ui:0") }

// sessionNames is every session on the watched server.
func (f launchFixture) sessionNames(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("tmux", "-S", f.target, "list-sessions", "-F",
		"#{session_name}").Output()
	if err != nil {
		t.Fatalf("list-sessions: %v", err)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			got = append(got, l)
		}
	}
	return got
}

// mkdir makes a directory under the fixture's work tree and returns its path.
func (f launchFixture) mkdir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(f.work, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// openForm presses `n` and asserts the form is on screen. The whole capture is read, because a form
// can paint below the fold and reading the first rows once reported `n` as doing nothing.
func (f launchFixture) openForm(t *testing.T) {
	t.Helper()
	f.keys(t, "n")
	waitUntil(t, "the launch form to open", 10*time.Second, func() bool {
		s, err := paneScreen(t, f.sock, "ui:0")
		return err == nil && strings.Contains(s, "enter: create")
	})
	// ctrl+u FIRST, because the directory field now opens holding the cwd of the row the cursor was
	// on. An operator who wants a different directory clears it; a test that types without clearing
	// appends to a real path and launches into nonsense.
	f.keys(t, "C-u")
}

// fillNewSession types the directory and moves the destination to "a new session". The three tabs are
// the form's own order — dir, model, mode, where — so a field added in the middle fails here rather
// than silently changing which value the arrow moves.
func (f launchFixture) fillNewSession(t *testing.T, dir string) {
	t.Helper()
	f.typeText(t, dir)
	if s := f.screen(t); !strings.Contains(s, dir) {
		t.Fatalf("what was typed did not reach the field:\n%s", s)
	}
	f.keys(t, "Tab", "Tab", "Tab")
	f.keys(t, "Right")
	if s := f.screen(t); !strings.Contains(s, "a new session") {
		t.Fatalf("the destination is not `a new session`:\n%s", s)
	}
}

// THE REPORTED BUG. A directory whose basename is already a session name on that server used to make
// the launch fail — every time, with tmux's own sentence and no remedy:
//
//	launch failed: create pane: new-session: duplicate session: st
//
// Measured through this interface before the fix. It is not an edge case on the operator's fleet:
// their own server holds a session called `tmux-hub`, so the tmux-hub checkout could never be
// launched into, which is exactly "creating a new session does not work".
func TestE2EUILaunchIntoANewSessionWhoseNameIsTaken(t *testing.T) {
	f := startLaunch(t, 120, 40, false, "st")
	dir := f.mkdir(t, "st")

	f.openForm(t)
	f.fillNewSession(t, dir)
	f.keys(t, "Enter")

	// A session IS created, and it is a SECOND one — the seed keeps its own.
	waitUntil(t, "a second session for that directory", 25*time.Second, func() bool {
		var made int
		for _, n := range f.sessionNames(t) {
			if strings.HasPrefix(n, "st") {
				made++
			}
		}
		return made == 2
	})
	var extra string
	for _, n := range f.sessionNames(t) {
		if strings.HasPrefix(n, "st-") {
			extra = n
		}
	}
	if extra == "" {
		t.Fatalf("no session carries the fallback name: %v", f.sessionNames(t))
	}
	// The fallback is `<dir>-<short id>`: the uuid the launch already generates for
	// `claude --session-id`, so it is unique by construction and one retry always succeeds. A
	// counter would need a bound and a probe; this needs neither.
	if len(extra) != len("st-")+8 {
		t.Errorf("the fallback name is %q, want st- plus an 8-character id", extra)
	}

	// And the operator is TOLD, because a session they did not name is a session they cannot find.
	s := f.screen(t)
	if !strings.Contains(s, "launched:") {
		t.Errorf("the launch is not reported at all:\n%s", s)
	}
	if !strings.Contains(s, extra) {
		t.Errorf("the note does not name the session that was created (%s):\n%s", extra, s)
	}
	if !strings.Contains(s, "taken") {
		t.Errorf("the note does not say WHY the name differs from the directory's:\n%s", s)
	}
	if strings.Contains(s, "launch failed") {
		t.Errorf("the launch reported a failure:\n%s", s)
	}
}

// The operator's own shape: a hub watching the server it is RUNNING IN. Every other launch case runs
// against a server on another socket, so this path — the commonest one there is — had no coverage.
func TestE2EUILaunchIntoANewSessionOnTheHubsOwnServer(t *testing.T) {
	f := startLaunch(t, 120, 40, true)
	dir := f.mkdir(t, "selfproj")

	f.openForm(t)
	f.fillNewSession(t, dir)
	f.keys(t, "Enter")

	waitUntil(t, "the session to exist on the hub's own server", 25*time.Second, func() bool {
		for _, n := range f.sessionNames(t) {
			if n == "selfproj" {
				return true
			}
		}
		return false
	})
	if s := f.screen(t); !strings.Contains(s, "launched:") {
		t.Errorf("the launch is not reported:\n%s", s)
	}
	// The hub's own session is still there, which is the thing a self-watched launch could break.
	var sawUI bool
	for _, n := range f.sessionNames(t) {
		if n == "ui" {
			sawUI = true
		}
	}
	if !sawUI {
		t.Errorf("the hub's own session is gone after launching into its server: %v",
			f.sessionNames(t))
	}
}

// A launched session has to REACH THE DASHBOARD, which is the other reading of "it does not work":
// created and invisible is indistinguishable from not created, and the operator only has the screen.
func TestE2EUILaunchTheNewSessionAppearsOnTheDashboard(t *testing.T) {
	f := startLaunch(t, 120, 40, false)
	dir := f.mkdir(t, "visible-proj")

	f.openForm(t)
	f.fillNewSession(t, dir)
	f.keys(t, "Enter")

	waitUntil(t, "the launched session to appear on the dashboard", 30*time.Second, func() bool {
		s, err := paneScreen(t, f.sock, "ui:0")
		return err == nil && strings.Contains(s, "visible-proj")
	})
}

// A directory that does not exist is refused IN THE FORM, with the path named and nothing created —
// and the form stays open, so the operator can correct the path instead of retyping it.
func TestE2EUILaunchIntoAMissingDirRefusesAndKeepsTheForm(t *testing.T) {
	f := startLaunch(t, 120, 40, false)
	missing := filepath.Join(f.work, "nope-not-here")
	before := f.sessionNames(t)

	f.openForm(t)
	f.fillNewSession(t, missing)
	f.keys(t, "Enter")
	time.Sleep(1500 * time.Millisecond)

	s := f.screen(t)
	if !strings.Contains(s, "does not exist") {
		t.Errorf("the refusal does not say what is wrong:\n%s", s)
	}
	if !strings.Contains(s, missing) {
		t.Errorf("the refusal does not name the path it refused:\n%s", s)
	}
	if !strings.Contains(s, "enter: create") {
		t.Errorf("the form closed on a refusal, so the operator has to retype the path:\n%s", s)
	}
	if got := f.sessionNames(t); len(got) != len(before) {
		t.Errorf("sessions changed on a refused launch: %v -> %v", before, got)
	}
}

// `esc` leaves the form and creates nothing.
func TestE2EUILaunchEscCancelsAndCreatesNothing(t *testing.T) {
	f := startLaunch(t, 120, 40, false)
	dir := f.mkdir(t, "cancelled")
	before := f.sessionNames(t)

	f.openForm(t)
	f.fillNewSession(t, dir)
	f.keys(t, "Escape")
	waitUntil(t, "the form to close", 10*time.Second, func() bool {
		s, err := paneScreen(t, f.sock, "ui:0")
		return err == nil && !strings.Contains(s, "enter: create")
	})
	time.Sleep(1500 * time.Millisecond)
	if got := f.sessionNames(t); len(got) != len(before) {
		t.Errorf("esc created something: %v -> %v", before, got)
	}
}

// The form at the ONE size the project commits to. §16 calls 80x24 the size to hold, not a degraded
// case, and a form can paint BELOW THE FOLD — which is how `n` once read as doing nothing.
func TestE2EUILaunchTheFormIsUsableAtEightyColumns(t *testing.T) {
	f := startLaunch(t, 80, 24, false)
	dir := f.mkdir(t, "narrow")

	f.openForm(t)
	s := f.screen(t)
	for _, want := range []string{"host:", "dir:", "model:", "mode:", "where:", "enter: create",
		"esc: cancel"} {
		if !strings.Contains(s, want) {
			t.Errorf("the form at 80x24 is missing %q:\n%s", want, s)
		}
	}
	// Nothing may run over the edge: a row wider than the terminal is what tmux wraps, and a wrapped
	// form row costs a line the fold then eats.
	for i, l := range strings.Split(s, "\n") {
		if len([]rune(strings.TrimRight(l, " "))) > 80 {
			t.Errorf("line %d is wider than the 80 columns the project commits to: %q", i, l)
		}
	}
	// And it LAUNCHES from there, which is the half a frame test cannot answer.
	f.fillNewSession(t, dir)
	f.keys(t, "Enter")
	waitUntil(t, "the session to exist", 25*time.Second, func() bool {
		for _, n := range f.sessionNames(t) {
			if n == "narrow" {
				return true
			}
		}
		return false
	})
}

// A `~` path is RESOLVED, and the field shows what will be used.
//
// tmux neither expands nor refuses it: measured on both fleet versions, `new-session -c '~/somedir'`
// is rc=0 with the pane's cwd at HOME rather than the directory — a session in the wrong place at
// rc=0. Before this the form refused with `cwd must be absolute, got "~/…"`, which is the most
// natural thing a person types.
func TestE2EUILaunchResolvesATildePath(t *testing.T) {
	f := startLaunch(t, 120, 40, false)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(home, "hube2e-tilde-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	base := filepath.Base(dir)

	f.openForm(t)
	f.fillNewSession(t, "~/"+base)
	f.keys(t, "Enter")

	waitUntil(t, "the session to exist under the resolved path", 25*time.Second, func() bool {
		for _, n := range f.sessionNames(t) {
			if n == base {
				return true
			}
		}
		return false
	})
	s := f.screen(t)
	if strings.Contains(s, "must be absolute") {
		t.Errorf("the form still refuses a ~ path:\n%s", s)
	}
	// The pane really is in that directory, which is the half rc=0 cannot answer — tmux answers
	// rc=0 for the unresolved form too and lands in HOME.
	out, err := exec.Command("tmux", "-S", f.target, "display", "-p", "-t", base,
		"#{pane_current_path}").Output()
	if err != nil {
		t.Fatalf("read the pane's path: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != dir {
		t.Errorf("the pane's cwd is %q, want %q — an unresolved ~ lands in HOME at rc=0", got, dir)
	}
}

// A directory with a SPACE in its name can be typed at all.
//
// It could not: bubbletea reports a space as KeySpace, the form's typing arm named only KeyRunes, and
// the character was dropped — measured through this interface, `with space` reached the field as
// `withspace` and the launch then refused a path that does not exist. tmux itself keeps a space in a
// session name, so nothing below the form ever had a problem with it.
func TestE2EUILaunchIntoADirectoryWithASpaceInItsName(t *testing.T) {
	f := startLaunch(t, 120, 40, false)
	dir := f.mkdir(t, "with space")

	f.openForm(t)
	f.typeText(t, dir)
	if s := f.screen(t); !strings.Contains(s, "with space") {
		t.Fatalf("the space was dropped on the way to the field:\n%s", s)
	}
	f.keys(t, "Tab", "Tab", "Tab", "Right")
	f.keys(t, "Enter")

	waitUntil(t, "the session to exist", 25*time.Second, func() bool {
		for _, n := range f.sessionNames(t) {
			if n == "with space" {
				return true
			}
		}
		return false
	})
	if s := f.screen(t); strings.Contains(s, "does not exist") {
		t.Errorf("the launch refused the path:\n%s", s)
	}
}

// Every OTHER shape a person types wrong is refused IN THE FORM, with its own sentence and nothing
// created. One case, because the property is the same for all three and the discriminator is the
// sentence.
func TestE2EUILaunchRefusesTheOtherWrongPathsInTheForm(t *testing.T) {
	f := startLaunch(t, 120, 40, false)
	file := filepath.Join(f.work, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.mkdir(t, "relproj")
	before := len(f.sessionNames(t))

	for _, c := range []struct{ typed, want string }{
		{"relproj", "must be absolute"},
		{"", "must not be empty"},
		{file, "not a directory"},
	} {
		f.openForm(t)
		if c.typed != "" {
			f.typeText(t, c.typed)
		}
		f.keys(t, "Tab", "Tab", "Tab", "Right")
		f.keys(t, "Enter")
		time.Sleep(1200 * time.Millisecond)
		s := f.screen(t)
		if !strings.Contains(s, c.want) {
			t.Errorf("typing %q was not refused with %q:\n%s", c.typed, c.want, s)
		}
		if !strings.Contains(s, "enter: create") {
			t.Errorf("the form closed on a refusal of %q, so the path has to be retyped:\n%s",
				c.typed, s)
		}
		f.keys(t, "Escape")
		time.Sleep(400 * time.Millisecond)
	}
	if got := len(f.sessionNames(t)); got != before {
		t.Errorf("a refused launch created something: %d sessions, want %d", got, before)
	}
}
