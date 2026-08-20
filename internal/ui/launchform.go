package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/launch"
	"github.com/DawnBreather/tmux-hub/internal/lines"
	tea "github.com/charmbracelet/bubbletea"
)

// launchForm is the state of the launch form. It holds a spec under construction
// and tracks which field is focused.
type launchForm struct {
	spec launch.Spec

	// Field indices:
	// 0: host
	// 1: directory (text input)
	// 2: model
	// 3: permission mode
	// 4: destination
	focused int

	dirInput Composer // reuse the existing text input for directory

	// Choice field indices
	// cursorHost and cursorSession are where the operator was standing when they pressed `n`. The
	// session is what "a new window" means — beside the thing I am working on — and it is only valid
	// while the chosen host is still the cursor's host, because a `$N` belongs to one server.
	cursorHost    string
	cursorSession string

	hostIndex  int
	modelIndex int
	permIndex  int
	destIndex  int // 0=new window, 1=new session

	hosts []hub.Host // available hosts to cycle through

	err string // validation error shown to user
}

// newLaunchForm creates a form defaulting to the host under the cursor.
func newLaunchForm(hosts []hub.Host, cursorHost, cursorSession, cursorCWD string) launchForm {
	f := launchForm{
		hosts:   hosts,
		focused: 1, // start on directory, the only mandatory field
	}

	// THE DIRECTORY THE CURSOR IS IN, pre-filled. Asked for as "create the session straight in the
	// corresponding directory (project), automatically" — and it is the same answer the host field
	// already gives: a new session is nearly always beside the one being looked at, and the operator
	// who wants elsewhere says so. It is the row's own cwd rather than the project LABEL, because the
	// label is only the last path segment (§21.12) and a launch needs a path.
	//
	// It travels with cursorHost, which is set from the same row, so the pair cannot disagree — a path
	// from a remote row with the local host selected would be a directory that does not exist here,
	// and `Spec.Validate` would refuse it with a sentence about the wrong thing.
	//
	// Overridable, and ctrl+u is what makes that true: without a clear key a sixty-column path can
	// only be removed one backspace at a time, which turns a convenience into a trap.
	f.cursorHost, f.cursorSession = cursorHost, cursorSession
	if cursorCWD != "" {
		f.dirInput.Insert(cursorCWD)
	}
	// Default to cursor's host
	for i, h := range hosts {
		if h.Label == cursorHost {
			f.hostIndex = i
			f.spec.Host = h.Label
			f.spec.Local = h.IsLocalServer()
			break
		}
	}
	if f.spec.Host == "" && len(hosts) > 0 {
		f.spec.Host = hosts[0].Label
		f.spec.Local = hosts[0].IsLocalServer()
	}

	// Default model and permission mode
	f.spec.Model = launch.Models[0]                   // opus
	f.spec.PermissionMode = launch.PermissionModes[0] // default

	return f
}

// formOutcome is what a keystroke DID to the form, and it exists because a bool could not say.
//
// handleKey used to report only `closed`, which is true for both Esc and a successful Enter, so the
// caller had to tell them apart by another means — and the means it chose was `f.err == ""`, a value
// that means "the LAST validation failed" used as though it meant "this one did". After an Enter
// that failed, a correction made with Backspace ALONE (the one key that did not clear the error)
// left the flag set, so the next valid Enter closed the form, went back to browse, launched nothing
// and said nothing. Naming the outcome removes the inference rather than clearing the flag, so a
// later caller cannot get it wrong either.
type formOutcome int

const (
	formOpen formOutcome = iota // the form stays up
	formCancelled
	formSubmitted
)

// handleKey processes keyboard input for the launch form.
func (f launchForm) handleKey(msg tea.KeyMsg) (launchForm, tea.Cmd, formOutcome) {
	switch msg.Type {
	case tea.KeyEsc:
		return f, nil, formCancelled

	case tea.KeyEnter:
		// Validate and submit
		cwd, err := expandTilde(f.dirInput.Text(), f.chosenHostIsLocal())
		if err != nil {
			f.err = err.Error()
			return f, nil, formOpen
		}
		if cwd != f.dirInput.Text() {
			// SHOW what will be used. A path the hub resolved silently is a path the operator cannot
			// check, and the field is the only place they can read it.
			f.dirInput = Composer{}
			f.dirInput.Insert(cwd)
		}
		f.spec.CWD = cwd
		f.spec.NewSession = f.destIndex == 1
		// The session's NAME, derived from the directory. Without it the spec carried an empty
		// name and `tmux new-session -s ""` succeeds — measured on 3.7b and 3.2a — so the row
		// drew as the pane id twice and `tmux attach -t <name>` had nothing to take.
		f.spec.SessionName = launch.SessionNameFor(f.spec.CWD)
		// A new WINDOW goes beside what the operator was looking at — but a `$N` belongs to one
		// server, so it is only carried while the chosen host is still the cursor's. Changing the
		// host in the form drops it, and the launch then asks that host for a session instead.
		f.spec.SessionID = ""
		if f.spec.Host == f.cursorHost {
			f.spec.SessionID = f.cursorSession
		}

		// Inject remote directory check if the host has ssh credentials.
		// This must happen BEFORE validation so the check runs.
		f = f.injectDirCheck()

		if err := f.spec.Validate(); err != nil {
			f.err = err.Error()
			return f, nil, formOpen
		}
		// The error goes with the value it was about, and this spec has just validated — so
		// nothing that follows can read a message about a path this form no longer holds.
		f.err = ""
		return f, nil, formSubmitted

	case tea.KeyTab:
		f.focused = (f.focused + 1) % 5
		f.err = "" // clear error when moving
		return f, nil, formOpen

	case tea.KeyShiftTab:
		f.focused--
		if f.focused < 0 {
			f.focused = 4
		}
		f.err = "" // clear error when moving
		return f, nil, formOpen

	case tea.KeyLeft, tea.KeyRight:
		delta := 1
		if msg.Type == tea.KeyLeft {
			delta = -1
		}
		f = f.cycle(delta)
		f.err = "" // clear error when cycling
		return f, nil, formOpen

	case tea.KeyCtrlU:
		// Clears the whole field, which is what makes the pre-filled directory an OFFER rather than
		// something to delete by hand. Same key the naming overlay and the keyword field use, so the
		// gesture is one gesture across every field in this product.
		if f.focused == 1 {
			f.dirInput.Clear()
			f.err = ""
		}
		return f, nil, formOpen

	case tea.KeyBackspace:
		if f.focused == 1 {
			f.dirInput.Backspace()
			// The same rule the typing path states below: a message naming a path the field
			// no longer shows is worse than none, because the reader cannot tell whether it
			// still applies. This was the one editing key that did not clear it.
			f.err = ""
		}
		return f, nil, formOpen

	case tea.KeyRunes, tea.KeySpace:
		if f.focused == 1 {
			// Through typedText, because a SPACE arrives as KeySpace and this arm used to name only
			// KeyRunes — so a directory with a space in its name could not be typed at all: measured
			// through the interface, `with space` reached the field as `withspace` and the launch
			// then refused a path that does not exist.
			text, _ := typedText(msg)
			f.dirInput.Insert(text)
			// The error goes with the value it was about. Every other key that can
			// change a field clears it; typing could not, so an error naming the
			// path the user is in the middle of correcting stayed on screen beside
			// the new one — and a message that names something the field no longer
			// shows is worse than none, because the reader cannot tell whether it
			// still applies.
			f.err = ""
		}
		return f, nil, formOpen
	}

	return f, nil, formOpen
}

// cycle changes a choice field's value by delta.
func (f launchForm) cycle(delta int) launchForm {
	switch f.focused {
	case 0: // host
		f.hostIndex = (f.hostIndex + delta + len(f.hosts)) % len(f.hosts)
		if f.hostIndex < len(f.hosts) {
			f.spec.Host = f.hosts[f.hostIndex].Label
			f.spec.Local = f.hosts[f.hostIndex].IsLocalServer()
		}

	case 2: // model
		f.modelIndex = (f.modelIndex + delta + len(launch.Models)) % len(launch.Models)
		f.spec.Model = launch.Models[f.modelIndex]

	case 3: // permission mode
		f.permIndex = (f.permIndex + delta + len(launch.PermissionModes)) % len(launch.PermissionModes)
		f.spec.PermissionMode = launch.PermissionModes[f.permIndex]

	case 4: // destination
		f.destIndex = (f.destIndex + delta + 2) % 2
		f.spec.NewSession = f.destIndex == 1
	}
	return f
}

// injectDirCheck sets the appropriate directory check based on the host.
// For remote hosts with ssh credentials, it builds a check that asks the remote
// host over the existing ssh master. For local hosts or remote hosts without
// credentials, it leaves the default behavior.
//
// If a check is already set (e.g., by a test), this does not override it.
func (f launchForm) injectDirCheck() launchForm {
	// Don't override a check that's already been injected (e.g., by tests)
	if f.spec.HasDirCheck() {
		return f
	}

	// Find the current host
	var currentHost *hub.Host
	for i := range f.hosts {
		if f.hosts[i].Label == f.spec.Host {
			currentHost = &f.hosts[i]
			break
		}
	}
	if currentHost == nil {
		return f
	}

	// Local hosts use the default check (os.Stat), so no injection needed.
	if currentHost.LocalProc {
		return f
	}

	// Remote host: if we have ssh credentials, build a remote check.
	// If not, leave dirCheck nil so validation skips the check.
	if currentHost.SSHDest != "" && currentHost.ControlPath != "" {
		f.spec.SetDirCheck(buildRemoteDirCheck(currentHost.SSHDest, currentHost.ControlPath))
	}

	return f
}

// buildRemoteDirCheck returns a dirCheck that verifies a path on a remote host
// using the existing ssh master. It runs `test -d <path>` over the master and
// returns an error if the directory does not exist or is not accessible.
func buildRemoteDirCheck(sshDest, controlPath string) func(string) error {
	return func(path string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Run `test -d <path>` over the ssh master. Exit code 0 means it's a directory.
		cmd := exec.CommandContext(ctx, "ssh", "-S", controlPath, sshDest, "--", "test", "-d", path)
		if err := cmd.Run(); err != nil {
			// test -d exits 1 if the path doesn't exist or isn't a directory.
			return fmt.Errorf("directory does not exist or is not accessible: %s", path)
		}
		return nil
	}
}

// RenderLaunchForm renders the launch form overlay.
func RenderLaunchForm(fr Frame, f launchForm) string {
	// Named locals for the fields this body reads, so the positional-to-struct
	// change altered no line of it — which is what makes the regenerated mockup an
	// honest check that no frame moved.
	width, height := fr.Width, fr.Height
	// The frame, shortened — not rebuilt. A hand-written twelve-field literal can still
	// drop or mis-assign a field, which is the form the struct exists to remove.
	base := backdrop(fr.withHeight(fr.Height - 12).withNote(""))

	// Form body
	var body []string
	body = append(body, separator(width))
	body = append(body, "Start a Claude Code session")
	body = append(body, "")

	// Field 0: host
	hostVal := f.spec.Host
	if hostVal == "" {
		hostVal = "(none)"
	}
	marker := " "
	if f.focused == 0 {
		marker = "›"
	}
	body = append(body, fmt.Sprintf("%s host:  %s", marker, hostVal))

	// Field 1: directory
	dirVal := f.dirInput.Text()
	if dirVal == "" {
		dirVal = "(path to the project)"
	}
	marker = " "
	if f.focused == 1 {
		marker = "›"
	}
	body = append(body, fmt.Sprintf("%s dir:   %s", marker, dirVal))

	// Hint: remote host without ssh= cannot complete or verify
	var currentHost *hub.Host
	for i := range f.hosts {
		if f.hosts[i].Label == f.spec.Host {
			currentHost = &f.hosts[i]
			break
		}
	}
	if currentHost != nil && !currentHost.LocalProc && (currentHost.SSHDest == "" || currentHost.ControlPath == "") {
		body = append(body, "         (completing a directory needs ssh= in the host's config)")
	}

	// Field 2: model
	marker = " "
	if f.focused == 2 {
		marker = "›"
	}
	body = append(body, fmt.Sprintf("%s model: %s", marker, f.spec.Model))

	// Field 3: permission mode
	marker = " "
	if f.focused == 3 {
		marker = "›"
	}
	body = append(body, fmt.Sprintf("%s mode:  %s", marker, f.spec.PermissionMode))

	// Field 4: destination
	destVal := "a new window"
	if f.spec.NewSession {
		destVal = "a new session"
	}
	marker = " "
	if f.focused == 4 {
		marker = "›"
	}
	body = append(body, fmt.Sprintf("%s where: %s", marker, destVal))

	body = append(body, "")
	if f.err != "" {
		body = append(body, "⚠ "+f.err)
	}
	// A PRIORITY LIST, in the order of what the operator cannot do without — and it is a list because
	// adding `ctrl+u` to the old fixed string pushed the line past 80 columns, where it truncated and
	// took `esc: cancel` with it: the way OUT of the form, lost to make room for a convenience. That
	// is this repo's oldest defect class (keeping the label and losing the action, known-issues S1),
	// and the second time today I have walked into it by adding a claimant to a shared row.
	//
	// `enter` first because it is the only key that commits, then `esc` because a screen you cannot
	// leave is worse than one you cannot fully use, then `ctrl+u` because the pre-filled directory
	// makes it necessary rather than optional, then the two navigation keys, which `tab` teaches by
	// being tried. All five fit at 80 with this separator; the order decides what goes below it.
	body = append(body, lines.Fit(width, " · ",
		"enter: create", "esc: cancel", "ctrl+u: clear", "tab/shift+tab: move",
		"left/right: change"))

	out := strings.Split(base, "\n")
	out = append(out, body...)
	return joinToHeight(out, height)
}

// expandTilde resolves a leading `~` against the hub's own HOME, and only for a LOCAL host.
//
// tmux will neither expand it nor refuse it. Measured on BOTH fleet versions (3.7b and 3.2a):
// `new-session -c '~/somedir'` is **rc=0** and the pane's `#{pane_current_path}` is `/home/dev` — the
// HOME, not the directory. So passing the path through would put a session in the wrong place at rc=0,
// which is the silent failure this repo refuses; the hub resolves it where it can.
//
// For a REMOTE host it cannot: `~` there is the far user's home, and the hub has not asked for it.
// Resolving with the LOCAL home would produce a path that exists on the wrong machine, so the refusal
// names the reason and what to type instead. A relative path is left to `Spec.Validate`, whose message
// already says what is wrong with it — "relative to what" has no answer the hub can give either.
func expandTilde(path string, local bool) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	if !local {
		return "", fmt.Errorf("~ is the home directory on THIS machine, and this host is not it — " +
			"type the path as it is on that host")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("cannot resolve ~ (no home directory) — type the absolute path")
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// chosenHostIsLocal reports whether the host the form is pointed at is on this machine. An unknown
// host answers false, so nothing is resolved against the wrong home while the launch's own refusal
// names the host that does not exist.
func (f launchForm) chosenHostIsLocal() bool {
	for i := range f.hosts {
		if f.hosts[i].Label == f.spec.Host {
			return !f.hosts[i].Remote()
		}
	}
	return false
}
