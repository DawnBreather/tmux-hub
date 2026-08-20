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

// THE HOST PICKER, driven through the real binary in a real terminal: `p`, `space`, `r`, `enter`,
// `esc`, and what hosts.toml holds afterwards.
//
// `internal/ui/picker_test.go` covers the screen's rules in process against injected ports. What it
// structurally cannot cover is the wiring those ports hide, and that wiring is where this screen's
// history lives: the CANDIDATES come from a parser reading a file at a path derived from the
// environment, the PROBE is a real ssh subprocess whose stderr a table classifies, the SAVE is a real
// atomic write, and the CONNECT is a real ssh master. Four separate programs; a fake for any of them
// answers the question the fake was written to answer.
//
// FOUR FACTS ABOUT THIS HARNESS, each measured here and each one that makes a case pass or fail for a
// reason that has nothing to do with the product.
//
// 1. OPENSSH IGNORES $HOME. It takes `~/.ssh/config` from the passwd entry (`pw->pw_dir`), not from
// the environment, so an isolated HOME isolates the picker's CANDIDATE LIST — hostset's parser reads
// `os.UserHomeDir()`, which is $HOME — and does NOT isolate the config ssh itself consults. Measured
// on OpenSSH_10.3p1: `env HOME=<isolated> ssh -G hub-e2e-slow` reports `hostname hub-e2e-slow` with
// no ProxyCommand, i.e. exactly what it reports under the real HOME, while the isolated config
// declared one. Two consequences the cases below are built on. Every alias written here must be one
// the operator's own config does not name, because that is what makes the probe unable to reach any
// of their machines: ssh has no entry for `hub-e2e-*`, so it tries to resolve the alias as a hostname
// and fails. And no ssh_config OPTION written in the isolated config has any effect at all, so the
// probe's outcome has to be steered some other way — see fact 3.
//
// 2. UNDER AN ISOLATED HOME, `tmux` ITSELF CAN STOP WORKING, and the whole fleet then reads `down`
// for a reason that is not the hub's. Measured here: `tmux` on PATH is a mise shim whose data
// directory lives under HOME, so with HOME redirected the hub's own poll answers
// `list-panes rc=1: mise ERROR tmux is not a valid shim`, `--status` reports the host `down`, and the
// dashboard shows `0 sessions` forever. pickerHub therefore puts a directory of its own at the front
// of the pane's PATH holding a symlink to a tmux that has been PROVEN to answer `tmux -V` under the
// isolated HOME before the hub is started — and it waits for the watched pane id on the first frame,
// so a case can never assert against a dark fleet.
//
// 3. `--probe-timeout 1ms` IS HOW A TIMED-OUT ROW IS MADE DETERMINISTIC. §9's third probe outcome —
// answered nothing YET, which keeps its tick box because a timeout is slow rather than absent — is
// the only outcome these cases can produce without a reachable host, and it is the one that makes
// `space` and `enter` testable at all. With the per-probe deadline already expired, ssh does no
// network I/O and every candidate lands there. The alternative was a real DNS failure racing a short
// timeout: measured 3.48 s, 3.48 s, 3.83 s for a name that cannot exist, so a 1 s timeout "works" on
// the strength of the resolver's latency, which is not a property of this program.
//
// 4. NOTHING OF THE OPERATOR'S IS TOUCHED, and each guard is separate. HOME is a temp dir, so
// hosts.toml and the ssh config the picker reads are the case's own. XDG_RUNTIME_DIR is a temp dir,
// so an ssh master's control socket lands there rather than beside the operator's — which also means
// the startup reconcile, whose victim set is that directory, can only ever see this case's own files.
// Both tmux servers are private `-S` sockets killed by their own Cleanup. `--view=flat --no-history` and an
// explicit `--hidden` path keep the send log and the hidden set out of it.

// pickerFixture is one running hub and the paths a case has to read to check it.
type pickerFixture struct {
	ui        string // the socket the hub is DISPLAYED on
	target    string // the socket the hub WATCHES
	work      string
	home      string
	hostsPath string // the --hosts file, which `enter` writes and `esc` must not
	pane      string // the watched pane's id, which is how "the fleet is live" is asserted
}

// pickerOpts is the fixture a picker case needs. hostsTOML of "" writes no file at all, which is the
// first run — the state that opens the picker without a keystroke.
type pickerOpts struct {
	cols, rows int
	sshConfig  string
	hostsTOML  string
	// view is which screen the hub opens on, and it defaults to `flat` because every case in this file
	// is about the picker over the attention-ordered list. The filesystem view's own first-run case sets
	// it to `tree`: the two options interact (the picker is raised at startup and the view decides the
	// screen underneath it), and that interaction was wrong until it was measured.
	view         string
	probeTimeout string
}

// pickerHub starts the hub with an isolated HOME carrying a written ssh config, so the picker has
// candidates that are the case's own and cannot reach anything.
func pickerHub(t *testing.T, o pickerOpts) pickerFixture {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	bin := buildBinary(t)
	work := t.TempDir()
	f := pickerFixture{
		ui:        filepath.Join(work, "ui.sock"),
		target:    filepath.Join(work, "target.sock"),
		work:      work,
		home:      filepath.Join(work, "home"),
		hostsPath: filepath.Join(work, "hosts.toml"),
	}

	if err := os.MkdirAll(filepath.Join(f.home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	// 0600, because ssh refuses a group-readable config and the hub's own parser does not care —
	// so the stricter of the two readers sets the mode.
	if err := os.WriteFile(filepath.Join(f.home, ".ssh", "config"), []byte(o.sshConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if o.hostsTOML != "" {
		if err := os.WriteFile(f.hostsPath, []byte(o.hostsTOML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runDir := filepath.Join(work, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Fact 2: a tmux the hub can still run once HOME has moved.
	binDir := filepath.Join(work, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(pickerTmuxForIsolatedHome(t, f.home), filepath.Join(binDir, "tmux")); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", f.target, "kill-server").Run()
		_ = exec.Command("tmux", "-S", f.ui, "kill-server").Run()
	})

	// One pane on the watched server, so the fleet is not empty and the first frame has something
	// to prove itself with.
	out, err := exec.Command("tmux", "-S", f.target, "-f", "/dev/null", "new-session", "-d",
		"-s", "watched", "-c", work, "-P", "-F", "#{pane_id}", "cat").CombinedOutput()
	if err != nil {
		t.Fatalf("the watched server: %v: %s", err, out)
	}
	f.pane = strings.TrimSpace(string(out))

	view := o.view
	if view == "" {
		view = "flat"
	}
	launch := fmt.Sprintf("PATH=%s:$PATH HOME=%s XDG_RUNTIME_DIR=%s %s "+
		"--hosts %s --no-local --host scratch=%s,local --hidden %s --view=%s --no-history --probe-timeout %s; "+
		"echo EXITED-rc=$?; sleep 90",
		binDir, f.home, runDir, bin, f.hostsPath, f.target,
		filepath.Join(work, "hidden.json"), view, o.probeTimeout)
	if out, err := exec.Command("tmux", "-S", f.ui, "-f", "/dev/null", "new-session", "-d", "-s", "ui",
		"-x", fmt.Sprint(o.cols), "-y", fmt.Sprint(o.rows), "-c", work, launch).CombinedOutput(); err != nil {
		t.Fatalf("start the hub: %v: %s", err, out)
	}

	// Wait for the FLEET, not for the header. §16 paints a usable screen before any poll completes, so
	// a harness that waits for the header reads the pre-poll frame — and the fleet carries a second
	// claim: it is the only evidence the PATH in fact 2 did its job, since a hub whose tmux cannot run
	// paints the same header and never a pane.
	//
	// WHICH SIGN depends on the view, because the two screens draw the fleet differently: the flat list
	// prints the pane's id, and the filesystem view deliberately does not — it prints the volume the
	// pane lives on, since the host is a line of its own there. A single needle would make one of the
	// two views wait thirty seconds for a screen that is working.
	sign, what := f.pane, "the watched pane "+f.pane
	if view == "tree" {
		sign, what = "scratch/", "the watched host's volume line"
	}
	waitUntil(t, what+" to reach the screen (a hub whose tmux cannot run under the isolated HOME "+
		"paints the header and never a pane)", 30*time.Second, func() bool {
		s, err := paneScreen(t, f.ui, "ui")
		return err == nil && strings.Contains(s, sign)
	})
	return f
}

// pickerTmuxForIsolatedHome answers with a tmux binary that still works once HOME has been
// redirected, and refuses to guess: every candidate is RUN before it is chosen.
//
// It exists for fact 2 above. A version-manager shim on PATH resolves through HOME, so the obvious
// `exec.LookPath("tmux")` can name a binary that fails for every command the hub issues — and the
// symptom is a dashboard with no panes, which reads exactly like a broken poll.
func pickerTmuxForIsolatedHome(t *testing.T, home string) string {
	t.Helper()
	var tried []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		tried = append(tried, p)
	}
	if p, err := exec.LookPath("tmux"); err == nil {
		add(p)
		if real, err := filepath.EvalSymlinks(p); err == nil {
			add(real)
		}
	}
	add("/usr/bin/tmux")
	add("/usr/local/bin/tmux")
	add("/bin/tmux")

	var why []string
	for _, p := range tried {
		cmd := exec.Command(p, "-V")
		cmd.Env = pickerEnvWithHome(home)
		out, err := cmd.CombinedOutput()
		if err == nil && strings.HasPrefix(string(out), "tmux ") {
			return p
		}
		why = append(why, fmt.Sprintf("%s: %v %s", p, err, strings.TrimSpace(string(out))))
	}
	// Not a product fact, and it is said in full: every candidate was executed and none answered.
	t.Skipf("no tmux binary answers `tmux -V` with HOME redirected to a temp dir, so the picker's "+
		"own fleet could not be polled and no assertion about it would mean anything. Tried:\n\t%s",
		strings.Join(why, "\n\t"))
	return ""
}

// pickerEnvWithHome is the environment with HOME replaced rather than shadowed: glibc's getenv
// answers with the FIRST match, so appending a second HOME= would leave the original in force.
func pickerEnvWithHome(home string) []string {
	out := []string{"HOME=" + home}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HOME=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// ── reading the overlay ────────────────────────────────────────────────────────────────────────────

// pickerBodyLines is the overlay's own lines: everything below the full-width rule RenderPicker draws
// as its first line. The dashboard stays above it — the picker is an overlay, not a takeover — so a
// case that read the whole screen would find a row's alias in the footer note as readily as in the
// list, which is the `Contains` over two surfaces this repo has already been bitten by.
func pickerBodyLines(t *testing.T, ui string) []string {
	t.Helper()
	lines := strings.Split(capturePane(t, ui, "ui"), "\n")
	rule := -1
	for i, l := range lines {
		if trimmed := strings.TrimRight(l, " "); trimmed != "" && strings.Trim(trimmed, "─") == "" {
			rule = i // the LAST such line: the overlay's rule is below anything the dashboard drew
		}
	}
	if rule < 0 {
		return nil
	}
	return lines[rule+1:]
}

// pickerSqueezed is the overlay joined into one line with runs of spaces collapsed.
//
// A candidate's reason WRAPS — at 120 columns the timeout remedy takes two lines and the `(asked …)`
// stamp a third — so a per-line assertion can only ever check the first fragment, and that is the
// half that carries the complaint rather than the FIX. Squeezed, a row reads as
// `[ ] hub-e2e-keep no answer in 1ms — … raise --probe-timeout (asked 06:23:29)`, so one assertion
// can require the alias, its tick box and the whole sentence including the remedy.
func pickerSqueezed(t *testing.T, ui string) string {
	t.Helper()
	joined := strings.Join(pickerBodyLines(t, ui), " ")
	return strings.Join(strings.Fields(joined), " ")
}

// pickerBodyHas waits for the overlay to say something, and prints the overlay when it does not.
func pickerBodyHas(t *testing.T, ui, want, why string) {
	t.Helper()
	var last string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		last = pickerSqueezed(t, ui)
		if strings.Contains(last, want) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the picker never said %q — %s\nwhat it said:\n%s\nthe whole screen:\n%s",
		want, why, last, capturePane(t, ui, "ui"))
}

// pickerCursorLine is the overlay row the cursor is on. The picker's own marker is `›`; the
// dashboard's is `>` and the dashboard is on the same screen, so the two markers must not be
// confused — cursorRow (the shared helper) answers with the dashboard's row here.
func pickerCursorLine(t *testing.T, ui string) string {
	t.Helper()
	for _, l := range pickerBodyLines(t, ui) {
		if strings.HasPrefix(l, "›") {
			return strings.TrimRight(l, " ")
		}
	}
	return ""
}

// pickerWalkTo moves the picker's cursor with `j` until it rests on the named candidate.
//
// It walks rather than assuming, because clampPickerCursor moves the cursor off any row where no key
// would act — so where it starts is a property of the probe's answers, not of the file's order.
func pickerWalkTo(t *testing.T, ui, alias string) {
	t.Helper()
	for i := 0; i < 30; i++ {
		if strings.Contains(pickerCursorLine(t, ui), alias) {
			return
		}
		send(t, ui, "j")
		time.Sleep(120 * time.Millisecond)
	}
	t.Fatalf("the picker's cursor never reached %q in 30 presses, so no key could be aimed at it\n%s",
		alias, capturePane(t, ui, "ui"))
}

// pickerOpen presses `p` and waits for the round to LAND, not for the screen to appear.
//
// The two are seconds apart and the difference is visible: the overlay opens on
// `Hosts — nothing to show yet` and the probe replaces it with the count line. Asserting against the
// first of those reads a screen that has no rows and no boxes yet.
func pickerOpen(t *testing.T, ui string) {
	t.Helper()
	send(t, ui, "p")
	screenHas(t, ui, "Hosts —", "`p` must open the host picker")
	pickerBodyHas(t, ui, "answer with tmux",
		"the picker must replace `nothing to show yet` with the round's own count once the probe "+
			"lands; a screen still saying that has asked nobody")
}

// pickerAskedStamp is the `(asked HH:MM:SS)` a timed-out row carries. It is the only thing on this
// screen that says a NEW round ran: `r`'s note and the row's reason read the same either way.
func pickerAskedStamp(t *testing.T, ui string) string {
	t.Helper()
	body := pickerSqueezed(t, ui)
	const marker = "(asked "
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	rest := body[i+len(marker):]
	j := strings.Index(rest, ")")
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// pickerTwoCandidates is the config most cases take: two aliases the operator's own ssh config does
// not name (so no probe can reach anything), plus the two shapes hostset's parser must refuse — a
// wildcard, and systemd's dot-prefixed local alias.
const pickerTwoCandidates = "Host hub-e2e-keep\nHost hub-e2e-two\nHost hub-e2e-*\nHost .hub-e2e-dot\n"

// pickerOneDisabledEntry is a hosts.toml that has DECIDED something, which is what keeps the picker
// from opening at startup so `p` is the thing under test. The tag is a field no row can show, so it
// is also the probe for whether a save merges or rebuilds.
const pickerOneDisabledEntry = "[[host]]\nalias = \"hub-e2e-untouched\"\nenabled = false\ntags = [\"keep-me\"]\n"

// ── what the list shows ────────────────────────────────────────────────────────────────────────────

// The picker lists the candidates it found — and a PATTERN is not one of them.
//
// Both halves are the same rule and both matter. `~/.ssh/config` on a systemd machine declares ten
// entries that are patterns rather than machines (§9), so a picker that offered them would send
// someone looking for hosts that do not exist and would point an ssh probe at `machine/*`. A picker
// that dropped a real candidate instead would hide a machine the operator can see in their own file.
func TestE2EUIPickerListsTheCandidatesItFoundAndNeverAPattern(t *testing.T) {
	f := pickerHub(t, pickerOpts{
		cols: 120, rows: 40, probeTimeout: "1ms",
		sshConfig: pickerTwoCandidates,
		hostsTOML: pickerOneDisabledEntry,
	})
	pickerOpen(t, f.ui)

	body := pickerSqueezed(t, f.ui)
	if !strings.Contains(body, "2 candidates in ~/.ssh/config") {
		t.Errorf("the count line does not say the config's two real candidates were found, so the "+
			"operator cannot tell a short list from a complete one:\n%s", body)
	}
	for _, alias := range []string{"hub-e2e-keep", "hub-e2e-two"} {
		if !strings.Contains(body, alias) {
			t.Errorf("candidate %q is in ~/.ssh/config and not on the picker — a machine the "+
				"operator can see in their own file is missing from the screen that offers it:\n%s",
				alias, body)
		}
	}
	for _, skipped := range []string{"hub-e2e-*", ".hub-e2e-dot"} {
		if strings.Contains(body, skipped) {
			t.Errorf("%q is a pattern, not a host, and the picker is offering it — ticking it would "+
				"write a hosts.toml entry nothing can reach, and probing it points ssh at a "+
				"pattern:\n%s", skipped, body)
		}
	}
}

// §9's load-bearing rule, on the screen: a host that ran out of time KEEPS ITS TICK BOX and its
// reason says slow rather than absent.
//
// It is the rule the design spends the most words on because it was got wrong twice by two readers:
// measured across repeats, `eu` answered `tmux 3.2a` at 5.4 s, 9.1 s, 15.7 s and 18.4 s, so 40% of
// that fleet straddles any fixed timeout. A row that lost its box on a timeout would make those
// hosts unreachable from the only screen that can enable them, and the box is the whole difference.
func TestE2EUIPickerATimedOutCandidateKeepsItsBoxAndSaysSlowRatherThanAbsent(t *testing.T) {
	f := pickerHub(t, pickerOpts{
		cols: 120, rows: 40, probeTimeout: "1ms",
		sshConfig: pickerTwoCandidates,
		hostsTOML: pickerOneDisabledEntry,
	})
	pickerOpen(t, f.ui)

	body := pickerSqueezed(t, f.ui)
	if !strings.Contains(body, "[ ] hub-e2e-keep") {
		t.Errorf("the timed-out candidate has no tick box, so the one screen that can enable it "+
			"refuses to — and a host that answers `tmux` in 15 s is a host, not an absence:\n%s", body)
	}
	// The whole sentence, remedy included. The complaint half alone is what a truncating renderer
	// leaves behind, and the flag to raise is the only actionable part.
	const remedy = "no answer in 1ms — this host is slow rather than absent; " +
		"enable it anyway, or raise --probe-timeout"
	if !strings.Contains(body, remedy) {
		t.Errorf("the timed-out row does not carry its remedy in full; the operator is told the "+
			"host is quiet and not what to do about it. wanted %q in:\n%s", remedy, body)
	}
	// And it says WHEN, because that is the one answer on this screen that depends on the moment.
	if pickerAskedStamp(t, f.ui) == "" {
		t.Errorf("the timed-out row does not say when it was asked, so a stale answer is "+
			"indistinguishable from a fresh one:\n%s", body)
	}
}

// A candidate the fleet has already spoken for is REFUSED WITH ITS REASON rather than silently
// dropped, has no tick box, and `space` on it answers instead of doing nothing.
//
// The label here collides with a `--host` entry. Refusing it on this screen is the whole point:
// hostsFor treats a duplicate label as fatal and main exits 1 on it BEFORE the TUI exists, so a
// hosts.toml the picker wrote could stop the program starting with no way back but hand-editing TOML.
func TestE2EUIPickerRefusesACandidateWhoseLabelTheFleetOwnsAndSaysWhy(t *testing.T) {
	f := pickerHub(t, pickerOpts{
		cols: 120, rows: 40, probeTimeout: "1ms",
		// `scratch` is the label the fixture gives the watched server with --host.
		sshConfig: "Host hub-e2e-keep\nHost scratch\n",
		hostsTOML: pickerOneDisabledEntry,
	})
	pickerOpen(t, f.ui)

	body := pickerSqueezed(t, f.ui)
	const reason = "scratch this label is already given to a --host entry — " +
		"rename one of them, or drop the --host"
	if !strings.Contains(body, reason) {
		t.Errorf("the colliding candidate is not refused with its reason on screen. Silently absent, "+
			"it reads as a machine the picker cannot see; with no remedy, the operator cannot act. "+
			"wanted %q in:\n%s", reason, body)
	}
	if strings.Contains(body, "[ ] scratch") || strings.Contains(body, "[x] scratch") {
		t.Errorf("the colliding candidate has a tick box, so the picker will write a hosts.toml that "+
			"the NEXT startup refuses with exit 1 — from the screen that created it:\n%s", body)
	}

	// The key still has to answer. A keystroke that changes nothing and says nothing reads as a
	// broken key, and this row is one a first-run cursor can rest beside.
	pickerWalkTo(t, f.ui, "scratch")
	send(t, f.ui, "space")
	screenHas(t, f.ui, "scratch cannot be enabled",
		"`space` on a row it cannot enable must say so; silence here is indistinguishable from a "+
			"dead key, and the operator's next move is to press it again")
}

// A REAL probe failure is classified into a remedy, and this is the case that runs one: an alias
// nothing can resolve, with a probe timeout long enough for ssh to answer rather than be cut off.
//
// The classification lives in hostset.reasonFor over ssh's own stderr, so it is only exercised by a
// real subprocess — an injected Runner tests the table against strings the test itself wrote.
// Measured three times on this machine: a name that cannot exist fails at 3.48 s, 3.48 s and 3.83 s,
// which is why the timeout here is 15 s and not the 1 ms every other case uses.
func TestE2EUIPickerAStaleConfigEntryIsNamedAsDNSWithWhatToDoAboutIt(t *testing.T) {
	f := pickerHub(t, pickerOpts{
		cols: 120, rows: 40, probeTimeout: "15s",
		sshConfig: "Host hub-e2e-stale\n",
		hostsTOML: pickerOneDisabledEntry,
	})
	pickerOpen(t, f.ui)

	const reason = "hub-e2e-stale DNS does not resolve — a stale ssh config entry? fix or remove it"
	pickerBodyHas(t, f.ui, reason,
		"a candidate whose name does not resolve must be named as a stale config entry, with the "+
			"edit that fixes it — ssh's own `Could not resolve hostname` is a sentence about ssh")
	if body := pickerSqueezed(t, f.ui); strings.Contains(body, "[ ] hub-e2e-stale") {
		t.Errorf("a host whose name does not resolve keeps a tick box, so the picker will enable a "+
			"host that will read the same tomorrow — that is the hub inventing a state nobody can "+
			"use:\n%s", body)
	}
}

// ── the decision, and where it lands ───────────────────────────────────────────────────────────────

// `space` ticks a candidate and `enter` COMMITS: hosts.toml on disk says so, and the fleet on the
// dashboard behind the overlay gains the host.
//
// Both halves are asserted because either one alone has shipped broken. A save that wrote the file
// and connected nothing is known-issues C1 — measured in a pty, three candidates ticked, the file
// correct, and 17.5 s later zero master spawns and zero polls, so the running hub never heard the
// decision. And a save that rebuilt the file from the ROWS would drop every field a row cannot show,
// which is why the fixture's surviving entry carries a `tags` line.
func TestE2EUIPickerSpaceThenEnterWritesTheFileAndTheFleetGainsTheHost(t *testing.T) {
	f := pickerHub(t, pickerOpts{
		cols: 160, rows: 40, probeTimeout: "1ms",
		sshConfig: pickerTwoCandidates,
		hostsTOML: pickerOneDisabledEntry,
	})
	pickerOpen(t, f.ui)

	pickerWalkTo(t, f.ui, "hub-e2e-keep")
	send(t, f.ui, "space")
	pickerBodyHas(t, f.ui, "[x] hub-e2e-keep",
		"`space` must tick the row under the cursor, and the box is the only place the screen says "+
			"what `enter` is about to write")

	send(t, f.ui, "Enter")
	screenHas(t, f.ui, "1 host kept in hosts.toml",
		"`enter` must report what it committed; a save that says nothing is indistinguishable from "+
			"a swallowed key, and the next move is to press it again")

	// THE FILE. Waited for rather than read once: the write happens in a tea.Cmd, so the note and
	// the bytes are not the same instant.
	var got string
	waitUntil(t, "hosts.toml to hold the ticked host", 20*time.Second, func() bool {
		b, err := os.ReadFile(f.hostsPath)
		if err != nil {
			return false
		}
		got = string(b)
		return strings.Contains(got, "hub-e2e-keep")
	})
	if !strings.Contains(got, "alias = \"hub-e2e-keep\"\nenabled = true\n") {
		t.Errorf("hosts.toml does not enable the host that was ticked, so the decision survives "+
			"only until the hub exits:\n%s", got)
	}
	// The merge, not a rebuild: the entry the screen never showed keeps its own hand-written field.
	if !strings.Contains(got, "alias = \"hub-e2e-untouched\"") || !strings.Contains(got, "tags = [\"keep-me\"]") {
		t.Errorf("saving from the picker dropped an entry the screen did not show, or the tags on "+
			"it — a generated file makes that loss invisible and the operator hand-wrote those "+
			"lines:\n%s", got)
	}

	// THE FLEET. `connecting` or `down` — either is a transport verdict about a host that is now in
	// the fleet, and the alias beside a status can only come from the footer's host line, never from
	// the save's note.
	waitUntil(t, "the new host to reach the dashboard's fleet line (a save that writes the file and "+
		"connects nothing leaves the running hub polling the old set — known-issues C1)",
		60*time.Second, func() bool {
			s, err := paneScreen(t, f.ui, "ui")
			return err == nil && (strings.Contains(s, "hub-e2e-keep connecting") ||
				strings.Contains(s, "hub-e2e-keep down"))
		})
}

// `esc` leaves the picker having written NOTHING, and the file is the assertion.
//
// A tick is made first on purpose: without one, "esc did not write" is true of a screen that cannot
// write at all. The file carries a `tags` line so a rewrite that happened to preserve the aliases
// would still be caught — the failure to fear is not an empty file but a regenerated one.
func TestE2EUIPickerEscLeavesTheFileByteForByteAsItWas(t *testing.T) {
	f := pickerHub(t, pickerOpts{
		cols: 120, rows: 40, probeTimeout: "1ms",
		sshConfig: pickerTwoCandidates,
		hostsTOML: pickerOneDisabledEntry,
	})
	before, err := os.ReadFile(f.hostsPath)
	if err != nil {
		t.Fatal(err)
	}
	pickerOpen(t, f.ui)

	pickerWalkTo(t, f.ui, "hub-e2e-keep")
	send(t, f.ui, "space")
	pickerBodyHas(t, f.ui, "[x] hub-e2e-keep",
		"the tick has to land before `esc`, or this case proves only that a screen with no decision "+
			"on it writes nothing")

	send(t, f.ui, "Escape")
	screenHas(t, f.ui, "hosts unchanged",
		"`esc` must say it changed nothing; a silent exit leaves the operator unsure whether the "+
			"tick was committed")

	// A moment for a write that should not exist to land, then the bytes.
	time.Sleep(1500 * time.Millisecond)
	after, err := os.ReadFile(f.hostsPath)
	if err != nil {
		t.Fatalf("hosts.toml is unreadable after esc: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("`esc` wrote hosts.toml, so looking at the list and committing to it are the same "+
			"gesture — a ticked box the operator changed their mind about is now their configuration."+
			"\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// `r` asks every candidate again and KEEPS an unsaved tick.
//
// The tick is the property. A probe answers what a host IS; whether the operator wants it is not a
// question the probe gets to re-open, and a re-probe that cleared the ticks would silently undo the
// work of a screen whose whole job is collecting them. The proof that a NEW round really landed is
// the `(asked …)` stamp: `r`'s note and the row's reason read identically either way.
func TestE2EUIPickerRAsksAgainAndKeepsAnUnsavedTick(t *testing.T) {
	f := pickerHub(t, pickerOpts{
		cols: 120, rows: 40, probeTimeout: "1ms",
		sshConfig: "Host hub-e2e-keep\n",
		hostsTOML: pickerOneDisabledEntry,
	})
	pickerOpen(t, f.ui)

	pickerWalkTo(t, f.ui, "hub-e2e-keep")
	send(t, f.ui, "space")
	pickerBodyHas(t, f.ui, "[x] hub-e2e-keep", "`space` must tick the row under the cursor")
	first := pickerAskedStamp(t, f.ui)
	if first == "" {
		t.Fatalf("no `(asked …)` stamp on the row, so nothing here can tell one round from the "+
			"next:\n%s", capturePane(t, f.ui, "ui"))
	}

	// Past the second boundary, because the stamp has second resolution and a re-probe that lands
	// inside the same second would be indistinguishable from one that never ran.
	time.Sleep(1200 * time.Millisecond)
	send(t, f.ui, "r")
	waitUntil(t, "a second round to land, which the `(asked …)` stamp is the only witness to",
		30*time.Second, func() bool {
			s := pickerAskedStamp(t, f.ui)
			return s != "" && s != first
		})

	if body := pickerSqueezed(t, f.ui); !strings.Contains(body, "[x] hub-e2e-keep") {
		t.Errorf("`r` cleared a tick the operator had made and not yet saved, so probing again "+
			"silently discards the decisions this screen exists to collect:\n%s", body)
	}
}

// ── the file's own complaint ───────────────────────────────────────────────────────────────────────

// What is wrong with hosts.toml is a RESTING message: it survives a keystroke that produces a note,
// and it survives the next `j`.
//
// A duplicate alias is a fact about the FILE, true until the file is rewritten, so a message cleared
// by the next keypress would hide it from everyone who moves the cursor — which is everyone. This is
// the mechanism the picker's own comment once described backwards: it argued the precedence while the
// code beneath it read `foot := note; if foot == "" { foot = warn }`, which REPLACES, so the warning
// vanished the instant any key produced a note rather than on the next `j`.
func TestE2EUIPickerTheMalformedHostsFileComplaintRestsThroughAKeystroke(t *testing.T) {
	f := pickerHub(t, pickerOpts{
		cols: 120, rows: 40, probeTimeout: "1ms",
		sshConfig: pickerTwoCandidates,
		// Both entries DISABLED so the duplicate is the only thing wrong: two ENABLED entries under
		// one alias make hostsFor fatal and the hub exits 1 before any screen exists.
		hostsTOML: "[[host]]\nalias = \"hub-e2e-dup\"\nenabled = false\n\n" +
			"[[host]]\nalias = \"hub-e2e-dup\"\nenabled = false\n",
	})
	pickerOpen(t, f.ui)

	const complaint = "hosts.toml names hub-e2e-dup twice — enter rewrites it"
	screenHas(t, f.ui, complaint,
		"a hosts.toml naming one host twice must say so on the screen that can rewrite it; "+
			"unreported, the file keeps two contradictory answers about one machine")

	// It shares the row with a NOTE rather than being replaced by one. `pickerOpen` has just left
	// `asked N candidates` there, so this frame holds both — which is exactly what the replacing
	// mechanism could not do.
	screen := capturePane(t, f.ui, "ui")
	if !strings.Contains(screen, "asked ") {
		t.Fatalf("no note on screen, so this frame cannot show whether the complaint SHARES the "+
			"footer or merely has it to itself:\n%s", screen)
	}
	if !strings.Contains(screen, complaint) {
		t.Errorf("the file's complaint disappeared while a note was on screen, so any keystroke "+
			"hides a fact about the file that stays true until the file is edited:\n%s", screen)
	}

	// And the next `j`, which is the gesture the comment named. The cursor is required to MOVE:
	// otherwise a swallowed key would pass this as though the message had rested.
	before := pickerCursorLine(t, f.ui)
	send(t, f.ui, "j")
	waitUntil(t, "the picker's cursor to move, so the `j` was really delivered", 15*time.Second,
		func() bool {
			now := pickerCursorLine(t, f.ui)
			return now != "" && now != before
		})
	if screen := capturePane(t, f.ui, "ui"); !strings.Contains(screen, complaint) {
		t.Errorf("the file's complaint vanished on the next `j`, so it is visible only to an "+
			"operator who never moves the cursor:\n%s", screen)
	}
}
