//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// THE DESTRUCTIVE KEYS, driven through the real interface: `!` (interrupt), `K` (kill), `R` (restart).
//
// These are the three keys that cannot be undone, so every case here asserts on the PRIVATE TMUX
// SERVER rather than on the hub's sentence about it. The distinction is the whole point: the hub
// prints "killed 1 pane(s)" from a counter, and `interrupted to 1 target: 1 delivered` from a guard
// that never sees the process — so "did the pane die" and "did C-c arrive" are questions only the
// watched server can answer.
//
// §7's argument is that a destructive act must be impossible to trigger with ONE keystroke. That is
// asserted here as a property of the WORLD, not of the screen: after the single key, the pane is
// still running. A dialog that appears while the act has already fired would satisfy a screen check
// and fail this one.

// destructiveInterruptMark is what the watched pane prints when SIGINT reaches it. It is the only
// evidence in this file that a C-c really landed: `send-keys C-c` returns rc=0 whether or not the
// pane's process is in a state to receive it, and the hub's own guard can only report that the key
// went to the pane it still identifies (internal/broadcast/send.go's keystroke says so in words).
const destructiveInterruptMark = "E2E-INTERRUPT-ARRIVED"

// destructiveHub starts the hub over a private server holding `panes` cat panes.
//
// It keeps the operator's REAL home, and that decision cost an hour, so it is written down: an
// isolated home would be the safer-looking choice here, and on this machine it makes the hub report
// `scratch down` with an empty fleet. Measured A/B — one pane, identical flags, only HOME differing —
// the footer reads `scratch down (list-panes rc=1: mise ERROR tmux is not a valid shim …)` because
// `tmux` on this PATH is a mise shim that resolves the real binary through `$HOME`. The same binary
// with the same isolated home answers `"status": "up"` under `--status`, which is why the harness's
// own note calls the pane list "late" rather than absent: `--status` is what was measured, and it
// spawns no tmux at all for the hosts it already knows.
//
// The safety property the isolated home was wanted for is kept STRUCTURALLY instead, and it does not
// depend on the fleet's contents. `walkTo` fails rather than settling for a row it did not find, and
// every destructive key acts on the SELECTION rather than on the cursor — so `K`, `!` and `R` here
// can only reach a pane id this test created on its own private server.
func destructiveHub(t *testing.T, panes int) (ui, target string, ids []string, work string) {
	t.Helper()
	ui, target, ids, work = hubWith(t, 120, 40, panes, "cat")
	for _, id := range ids {
		want := id
		waitUntil(t, "the watched pane "+want+" to reach the dashboard", 30*time.Second, func() bool {
			s, err := paneScreen(t, ui, "ui")
			return err == nil && strings.Contains(s, want)
		})
	}
	return ui, target, ids, work
}

// destructiveLivePanes is the private server's own answer about what still exists.
func destructiveLivePanes(t *testing.T, target string) []string {
	t.Helper()
	out, err := exec.Command("tmux", "-S", target, "list-panes", "-a", "-F", "#{pane_id}").Output()
	if err != nil {
		// A server with no sessions left exits, and `list-panes` then fails. That is an answer
		// about the world ("nothing is alive"), not a broken harness.
		return nil
	}
	var ids []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			ids = append(ids, l)
		}
	}
	return ids
}

func destructivePaneAlive(t *testing.T, target, paneID string) bool {
	t.Helper()
	for _, id := range destructiveLivePanes(t, target) {
		if id == paneID {
			return true
		}
	}
	return false
}

// destructivePanePID is how a respawn is detected from outside. `respawn-pane -k` keeps `pane_id`
// and the `@hub_*` stamp and CHANGES `pane_pid` (docs/design.md §19 measured 222976 → 222983), so
// the pid is the only field that says whether the process behind the row was replaced.
func destructivePanePID(t *testing.T, target, paneID string) string {
	t.Helper()
	out, err := exec.Command("tmux", "-S", target, "list-panes", "-a", "-F",
		"#{pane_id} #{pane_pid}").Output()
	if err != nil {
		t.Fatalf("list-panes on the watched server: %v", err)
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && f[0] == paneID {
			return f[1]
		}
	}
	return ""
}

// destructiveSelect puts the cursor on a pane and marks it, then waits for the hub to SAY it is
// marked.
//
// The wait is not decoration: `!` and `K` both answer an empty selection with a note instead of a
// dialog, and "select a pane with space first" reads on screen exactly like a dialog that never
// opened. Waiting for the footer's own count separates the two.
// destructiveSelect walks to a row and marks it. Its argument is what the ROW SHOWS, which for a
// single-pane fixture is not the pane id — see hubRowNeedle.
func destructiveSelect(t *testing.T, ui, needle string) {
	t.Helper()
	walkTo(t, ui, needle)
	send(t, ui, "space")
	waitUntil(t, "the footer to report one marked pane", 15*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && strings.Contains(s, "1 marked")
	})
}

// destructiveAgentPane opens a window the hub's identity walk will VOUCH for, and it does so through
// the walk's own rule rather than around it.
//
// internal/proc keys identification on the BASENAME of argv[0] being `claude`, with argv[1] not one
// of the daemon roles — comm is measured unreliable there, because Node overwrites it. So a symlink
// named `claude` pointing at bash, running a script, satisfies exactly the predicate the product
// applies, and the fixture is asserted against /proc rather than assumed: if the rule ever moves,
// this case says so instead of quietly testing the refusal path twice.
//
// The script traps INT and PRINTS, which is what makes an arriving C-c observable on the server. A
// pane running `cat` would die on the same signal — indistinguishable from a kill, and gone before
// it can be read.
func destructiveAgentPane(t *testing.T, target, work string) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed, so no pane can be built that both survives SIGINT and " +
			"reports receiving it")
	}
	dir := filepath.Join(work, "fakebin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "claude")
	_ = os.Remove(link)
	if err := os.Symlink(bash, link); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(work, "trap-int.sh")
	body := "trap 'echo " + destructiveInterruptMark + "' INT\nwhile :; do sleep 0.2; done\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("tmux", "-S", target, "new-window", "-t", "watched", "-c", work,
		"-P", "-F", "#{pane_id}", link, script).CombinedOutput()
	if err != nil {
		t.Fatalf("open a window the identity walk will vouch for: %v: %s", err, out)
	}
	id := strings.TrimSpace(string(out))

	// The fixture's own contract, checked against /proc: argv[0]'s basename must be `claude`, or the
	// walk will not vouch and this case silently becomes a second copy of the refusal case.
	pid := destructivePanePID(t, target, id)
	if pid == "" {
		t.Fatalf("the pane %s the harness just opened has no pane_pid", id)
	}
	raw, err := os.ReadFile(filepath.Join("/proc", pid, "cmdline"))
	if err != nil {
		t.Skipf("cannot read /proc/%s/cmdline, so the fixture cannot be proved to satisfy the "+
			"identity walk's rule: %v", pid, err)
	}
	argv := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	if len(argv) == 0 || filepath.Base(argv[0]) != "claude" {
		t.Fatalf("the fixture no longer satisfies the identity walk's rule (argv[0] basename must "+
			"be \"claude\"): argv=%q", argv)
	}
	return id
}

// destructiveInboxLines is the INBOX COLUMN alone, which is the surface the row lives on.
//
// It exists because a captured line is not a row. Above 100 columns the dashboard puts the inbox in a
// 28-column left column and a tile beside it, so one captured line holds one row's text AND a slice
// of an unrelated pane's tile: the first version of the check below read
// `  · works  20260813--gis-off ┌─ scratch watched %1 ──┐` and accused the hub of drawing a killed
// pane as live, when the two halves belong to different panes. Any per-line rule about a row must
// slice the surface first.
func destructiveInboxLines(t *testing.T, ui string) []string {
	t.Helper()
	const inboxWidth = 28 // internal/ui.InboxWidth
	var out []string
	for _, l := range strings.Split(capturePane(t, ui, "ui"), "\n") {
		r := []rune(l)
		if len(r) > inboxWidth {
			r = r[:inboxWidth]
		}
		out = append(out, string(r))
	}
	return out
}

// destructiveWaitScreen waits for the dashboard to satisfy pred and DUMPS the screen when it never
// does. waitUntil names the fact and not the frame, and every question in this file is answered by
// looking at what the operator is looking at.
func destructiveWaitScreen(t *testing.T, ui, what string, timeout time.Duration, pred func(string) bool) {
	t.Helper()
	last := ""
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s, err := paneScreen(t, ui, "ui"); err == nil {
			last = s
			if pred(s) {
				return
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s\n%s", timeout, what, last)
}

// destructiveDialogLines returns the confirmation dialog's OWN lines: everything from the line that
// opens with `head` down to the key line that closes it.
//
// It exists because the dashboard is still drawn underneath. A pane id appears in the inbox row, in
// the focused tile's border and in the dialog, so a `Contains` over the whole capture is satisfied by
// a dialog that names nothing — which is precisely the "Kill this?" with no subject that §7 forbids.
func destructiveDialogLines(t *testing.T, ui, head string) []string {
	t.Helper()
	var out []string
	in := false
	for _, l := range strings.Split(capturePane(t, ui, "ui"), "\n") {
		if strings.Contains(l, head) {
			in = true
			continue
		}
		if !in {
			continue
		}
		if strings.Contains(l, "any other key: cancel") {
			break
		}
		out = append(out, l)
	}
	return out
}

// destructiveOpenVouchedInterrupt presses `!` until the dialog stops calling the pane unidentified,
// and returns the dialog it settled on.
//
// The retry exists because the dialog's reason list is the ONLY signal outside the process that the
// identity walk has run and the stamp has landed — no row, no footer and no header says so. A single
// look races the walk, and a raced look does not fail: it reports the refusal path and reads exactly
// like a hub that cannot vouch for anything.
func destructiveOpenVouchedInterrupt(t *testing.T, ui string) string {
	t.Helper()
	last := ""
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		sendLiteral(t, ui, "!")
		screenHas(t, ui, "Confirm interrupt with C-c",
			"`!` must ask before it writes C-c into a live process")
		last = capturePane(t, ui, "ui")
		if !strings.Contains(last, "cannot be identified as an agent") {
			return last
		}
		send(t, ui, "Escape")
		time.Sleep(700 * time.Millisecond)
	}
	t.Fatalf("the hub never vouched for the agent pane in 30s, so an interrupt could only be "+
		"refused — the operator's `!` would never reach a real agent:\n%s", last)
	return last
}

// ── §7's argument: one keystroke destroys nothing ─────────────────────────────────────────────────

// A single press of a destructive key leaves the world exactly as it was.
//
// This is asserted on the SERVER and not on the screen, and that is the whole case: a dialog painted
// after the act had already fired would satisfy "the dialog appears" and still have killed the pane.
// The watched pane runs `cat`, which dies on SIGINT and disappears with its window, so "the pane is
// still there" is one assertion that covers both keys — nothing was killed and no C-c arrived.
func TestE2EUIDestructiveOneKeystrokeDestroysNothing(t *testing.T) {
	for _, c := range []struct {
		name string
		key  string
		want string // what the dialog this key opens must say
	}{
		{"kill", "K", "Confirm kill to 1 target:"},
		{"interrupt", "!", "Confirm interrupt with C-c to 1 target:"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ui, target, ids, _ := destructiveHub(t, 2)
			victim := ids[1]
			destructiveSelect(t, ui, victim)

			sendLiteral(t, ui, c.key)
			screenHas(t, ui, c.want,
				"`"+c.key+"` must ASK — a destructive act on one keystroke is what §7 forbids")
			screenHas(t, ui, "any other key: cancel",
				"the dialog must name the way out, or the operator's only exit is to guess")

			// Give an act that fired anyway time to land. Without this the assertion below could
			// pass simply by reading the server before the damage.
			time.Sleep(1500 * time.Millisecond)
			if !destructivePaneAlive(t, target, victim) {
				t.Fatalf("one press of `%s` destroyed pane %s: it is gone from the watched server "+
					"while the dialog asking about it was still on screen\n%s",
					c.key, victim, capturePane(t, ui, "ui"))
			}
			if s := capturePane(t, ui, "ui"); strings.Contains(s, "killed") ||
				strings.Contains(s, "interrupted to") {
				t.Errorf("`%s` reported acting while it was still only asking:\n%s", c.key, s)
			}
		})
	}
}

// ── K: the dialog names the subject, and enter is what kills ──────────────────────────────────────

// `K` names WHAT it is about to destroy, and only `enter` destroys it. Then the pane is gone from the
// private server AND the hub's own row stops reading as live — two claims, because a hub that keeps
// drawing a dead row as `idle` sends the operator to a pane that no longer exists.
func TestE2EUIDestructiveKillNamesTheSubjectThenEnterKillsThePane(t *testing.T) {
	ui, target, ids, _ := destructiveHub(t, 2)
	victim, survivor := ids[1], ids[0]
	destructiveSelect(t, ui, victim)

	sendLiteral(t, ui, "K")
	screenHas(t, ui, "Confirm kill to 1 target:", "`K` must confirm before it kills")

	// The dialog's subject line must carry the pane AND what is running in it. "Kill this?" with no
	// subject is the failure §7 and docs/design.md §10 both name: it cannot tell the operator which
	// window they are about to lose.
	//
	// The search is over the DIALOG's own lines and not over the screen, because the pane id is on
	// the screen three times over — the inbox row, the focused tile's border and the dialog — so a
	// `Contains` on the whole capture would pass with the dialog listing nothing at all.
	subject := ""
	for _, l := range destructiveDialogLines(t, ui, "Confirm kill to") {
		if strings.Contains(l, victim) {
			subject = strings.TrimSpace(l)
		}
	}
	if subject == "" {
		t.Fatalf("the kill dialog does not list the pane it is about to destroy (%s):\n%s",
			victim, capturePane(t, ui, "ui"))
	}
	if !strings.Contains(subject, "cat") {
		t.Errorf("the kill dialog names the pane but not what is RUNNING in it: %q — the operator "+
			"is asked to confirm destroying a window without being told what is in it", subject)
	}
	// And the reason, which is what "naming what is running" means for identity rather than command.
	screenHas(t, ui, "this pane cannot be identified as an agent",
		"the kill dialog must say what the hub knows about the pane, so a wrong window is refusable")

	send(t, ui, "Enter")

	// The SERVER is the assertion. `killed 1 pane(s)` is a counter, and a counter cannot tell the
	// operator whether the window went away.
	waitUntil(t, "the killed pane to be gone from the watched server", 20*time.Second, func() bool {
		return !destructivePaneAlive(t, target, victim)
	})
	// The hub's own row must stop reading as LIVE. It does not leave the screen, and that is the
	// product being deliberate rather than slow: measured here, the row becomes `✝ gone   %1   cat`
	// and sorts to the bottom, because tmux destroys a pane and its scrollback together, so the
	// hub's cache is the only remaining evidence of why it died (internal/registry/registry.go's
	// Update says so). What must not survive is the row reading `idle` — an operator who selects and
	// sends to that row gets a refusal about a token instead of the fact that the pane is gone.
	destructiveWaitScreen(t, ui, "the killed pane's row to be marked gone", 20*time.Second,
		func(string) bool {
			for _, l := range destructiveInboxLines(t, ui) {
				if strings.Contains(l, victim+" ") && strings.Contains(l, "gone") {
					return true
				}
			}
			return false
		})
	for _, l := range destructiveInboxLines(t, ui) {
		if !strings.Contains(l, victim+" ") || strings.Contains(l, "gone") {
			continue
		}
		for _, live := range []string{"idle", "works", "needs", "quiet"} {
			if strings.Contains(l, live) {
				t.Errorf("the killed pane still has a row reading %q: %q — the operator sends to a "+
					"pane that no longer exists and is answered about a token", live, l)
			}
		}
	}
	if s := capturePane(t, ui, "ui"); !strings.Contains(s, "killed 1 pane") {
		t.Errorf("the hub killed a pane and did not say so — a destructive act with no answer is "+
			"indistinguishable from a key that did nothing:\n%s", s)
	}
	// The neighbour is untouched, on the server and on the screen. A kill that takes the pane next
	// to the one named is the worst outcome this dialog exists to prevent.
	if !destructivePaneAlive(t, target, survivor) {
		t.Errorf("killing %s took %s with it — the operator loses a window they never named",
			victim, survivor)
	}
	// There is deliberately NO assertion that the survivor is on the screen, and the reason is worth
	// the space. This case runs against the operator's own fleet, which has more rows than a 40-row
	// terminal draws now that the details band holds the bottom of the body — so a `Contains` over one
	// capture was really asserting "the survivor is among the first rows by attention", which is not a
	// property of anything and was true only by luck. Walking to it is no better: `walkTo` only moves
	// DOWN, and by this point the cursor has already been driven past the row.
	//
	// What matters is asserted above and is real state on a real socket: the pane the operator did not
	// name is still running. The hub cannot hide a pane that exists — a row leaving the list would be
	// the registry losing it, which its own tests cover.
}

// Any key that is not `enter` cancels the kill, and the pane is still running afterwards.
//
// The two keys are chosen for how their leak would show. `x` hides a row, so a leak is SILENT — the
// operator sees a cancelled kill and a row that vanished for no stated reason. `q` quits, so a leak
// takes the whole hub down mid-decision. Both must reach the dashboard as nothing at all.
func TestE2EUIDestructiveAKeyThatIsNotEnterCancelsTheKill(t *testing.T) {
	for _, c := range []struct {
		name string
		key  string
	}{
		{"a key that hides a row on the dashboard", "x"},
		{"a key that quits the dashboard", "q"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ui, target, ids, _ := destructiveHub(t, 2)
			victim := ids[1]
			destructiveSelect(t, ui, victim)

			sendLiteral(t, ui, "K")
			screenHas(t, ui, "Confirm kill to 1 target:", "`K` must confirm before it kills")

			sendLiteral(t, ui, c.key)
			screenHas(t, ui, "cancelled",
				"a key that is not enter must cancel and SAY so — a dialog that closes silently "+
					"leaves the operator unsure whether the pane died")

			time.Sleep(1500 * time.Millisecond)
			if !destructivePaneAlive(t, target, victim) {
				t.Fatalf("`%s` on the kill dialog destroyed pane %s instead of cancelling", c.key, victim)
			}
			s := capturePane(t, ui, "ui")
			if strings.Contains(s, "EXITED-rc=") {
				t.Fatalf("`%s` leaked past the kill dialog and quit the hub:\n%s", c.key, s)
			}
			if strings.Contains(s, "hidden") {
				t.Errorf("`%s` leaked past the kill dialog to the dashboard and hid a row — the "+
					"operator cancelled a kill and silently lost the row instead:\n%s", c.key, s)
			}
			if !strings.Contains(s, victim) {
				t.Errorf("the cancelled pane %s is no longer on the dashboard:\n%s", victim, s)
			}
		})
	}
}

// `K` on a row that has NO pane refuses without opening a dialog at all.
//
// The two halves are one property and both matter. A dialog that offers a target the hub is going to
// refuse anyway teaches the operator to press enter through dialogs, which is the habit §7's whole
// mechanism depends on not forming — and internal/ui/lifecycle.go records that this is exactly what
// shipped: `K` confirmed and then issued `kill-pane -t agent:<shortid>@<hash>`, which tmux reads as a
// session named `agent`, and the failure was counted into a `failed` tally with no reason shown.
//
// It is safe to run against the operator's own listing: every row here carries the watched host's
// label, so any tmux command the hub builds is addressed to this test's private socket.
func TestE2EUIDestructiveKillRefusesAPaneLessRowWithoutOpeningADialog(t *testing.T) {
	ui, _, _, _ := destructiveHub(t, 1)
	if !waitForAgentRow(t, ui) {
		t.Skip("no pane-less Claude rows appeared in 20s, so there is no pane-less row to refuse")
	}
	walkToAgentRow(t, ui)
	send(t, ui, "space")
	waitUntil(t, "the footer to report one marked row", 15*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && strings.Contains(s, "1 marked")
	})

	sendLiteral(t, ui, "K")
	screenHas(t, ui, "nothing killed",
		"`K` on a row with no pane must say NOTHING was killed — the refusal covers the whole "+
			"action, and an operator who read only a tail would assume the row was destroyed")

	s := capturePane(t, ui, "ui")
	if strings.Contains(s, "Confirm kill to") {
		t.Errorf("`K` opened a kill dialog for a row it cannot kill — confirming becomes a reflex "+
			"when the dialog offers targets the hub will refuse:\n%s", s)
	}
	// And the refusal names the way out. Three sentences are correct, because one is wrong for half
	// the population: a background job has an id `claude stop` takes, a finished one has output to
	// read, and an interactive session carries no id at all.
	remedies := []string{"claude stop", "claude logs", "end it in its own terminal"}
	found := false
	for _, r := range remedies {
		if strings.Contains(s, r) {
			found = true
		}
	}
	if !found {
		t.Errorf("the refusal names no way out (looked for %v) — the operator is told the hub will "+
			"not act and not what to do instead:\n%s", remedies, s)
	}
}

// The key that COMMITS a destructive act must name that act.
//
// The dialog's heading gets this right (`Confirm kill to 1 target:`) and its last line — the only
// line that says which key does the thing — is a constant reading `enter: send anyway`. So the
// sentence directly above the operator's finger describes a send while the act is a kill, and
// nothing on screen says `enter: kill`.
//
// FIXED: the commit line is built from the act now. It was a constant, and the frame it produced at
// 120x40 with one cat pane marked was:
//
//	Confirm kill to 1 target:
//	    scratch %1 (cat cat)
//	  • this pane cannot be identified as an agent
//	enter: send anyway  ·  any other key: cancel
//
// It was not cosmetic, for the same reason
// ConfirmView exists at all: its own doc comment says the dialog used to read "Confirm send to 1
// target(s)" for an interrupt, and the fix named the act on the heading and left the commit line
// behind. §7 makes confirming a DECISION, and a decision needs the verb on the line that carries the
// key.
func TestE2EUIDestructiveTheCommitKeyNamesTheActItPerforms(t *testing.T) {
	for _, c := range []struct {
		name string
		key  string
		head string
		verb string
	}{
		{"kill", "K", "Confirm kill to", "kill"},
		{"interrupt", "!", "Confirm interrupt with C-c to", "interrupt"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ui, _, ids, _ := destructiveHub(t, 2)
			destructiveSelect(t, ui, ids[1])
			sendLiteral(t, ui, c.key)
			screenHas(t, ui, c.head, "`"+c.key+"` must open its dialog")

			commit := ""
			for _, l := range strings.Split(capturePane(t, ui, "ui"), "\n") {
				if strings.Contains(l, "any other key: cancel") {
					commit = strings.TrimSpace(l)
				}
			}
			if commit == "" {
				t.Fatalf("the %s dialog has no line naming the key that commits:\n%s",
					c.name, capturePane(t, ui, "ui"))
			}
			if !strings.Contains(commit, c.verb) {
				t.Errorf("the %s dialog's commit line is %q — it never names %q, so the operator "+
					"reads one act and performs another", c.name, commit, c.verb)
			}
		})
	}
}

// ── !: the interrupt, asserted where it lands ─────────────────────────────────────────────────────

// `!` confirmed on a pane the hub vouches for really delivers C-c, and the PANE says so.
//
// This is the only case in the suite that reads an arriving keystroke on the far side. The hub cannot
// do it for itself and says so in internal/broadcast/send.go: a keypress leaves no text to witness,
// so `delivered` there means "the key went to the pane the hub still identifies". The pane's own
// INT trap is the missing half — and it also proves the interrupt stopped the COMMAND rather than
// the pane, since the process is still alive after it.
func TestE2EUIDestructiveInterruptDeliversCtrlCToTheWatchedPane(t *testing.T) {
	ui, target, _, work := destructiveHub(t, 1)
	agent := destructiveAgentPane(t, target, work)
	waitUntil(t, "the agent pane to reach the dashboard", 30*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && strings.Contains(s, agent)
	})
	destructiveSelect(t, ui, agent)

	// Nothing has arrived yet, which is the baseline that makes the assertion after `enter` mean
	// something. Without it, a mark left over from any earlier keystroke would read as delivery.
	if s := capturePane(t, target, agent); strings.Contains(s, destructiveInterruptMark) {
		t.Fatalf("the watched pane reports an interrupt before one was sent:\n%s", s)
	}

	dialog := destructiveOpenVouchedInterrupt(t, ui)
	if !strings.Contains(dialog, "Confirm interrupt with C-c to 1 target:") {
		t.Fatalf("the interrupt dialog does not name the act and the count:\n%s", dialog)
	}

	send(t, ui, "Enter")

	// The PANE's own words. Everything else in this flow is the hub talking about itself.
	waitUntil(t, "C-c to arrive at the watched pane", 20*time.Second, func() bool {
		s, err := paneScreen(t, target, agent)
		return err == nil && strings.Contains(s, destructiveInterruptMark)
	})
	if s := capturePane(t, ui, "ui"); !strings.Contains(s, "1 delivered") {
		t.Errorf("C-c reached the pane and the hub did not report it delivered — an operator who "+
			"reads the footer would send it again:\n%s", s)
	}
	// The pane survives its own interrupt. `!` stops what is running; it is not a kill, and a `!`
	// that closed the window would destroy a transcript the operator meant to keep.
	if !destructivePaneAlive(t, target, agent) {
		t.Errorf("`!` destroyed pane %s instead of interrupting what was running in it", agent)
	}
}

// `!` confirmed on a pane the hub CANNOT vouch for is refused at the write path, and no C-c arrives.
//
// The pane runs `cat`, which dies on SIGINT and takes its window with it, so the pane still being
// there is a positive statement that the signal never reached it. This is the assertion that
// separates a refusal from a message about one: the hub prints `1 refused` from the same code path
// whether or not the bytes went out.
func TestE2EUIDestructiveInterruptIsRefusedWhenTheHubCannotVouchAndNoCtrlCArrives(t *testing.T) {
	ui, target, ids, _ := destructiveHub(t, 2)
	victim := ids[1]
	destructiveSelect(t, ui, victim)

	sendLiteral(t, ui, "!")
	screenHas(t, ui, "Confirm interrupt with C-c to 1 target:", "`!` must ask before it writes C-c")
	screenHas(t, ui, "cannot be identified as an agent",
		"the dialog must say WHY it is asking, or confirming is a reflex rather than a decision")

	send(t, ui, "Enter")
	screenHas(t, ui, "1 refused",
		"an interrupt into a pane the hub cannot vouch for must be refused at the write path — "+
			"C-c into the wrong pane kills whatever is really running there")

	time.Sleep(1500 * time.Millisecond)
	if !destructivePaneAlive(t, target, victim) {
		t.Fatalf("the refused interrupt still reached pane %s: it ran `cat`, which SIGINT kills, "+
			"and it is gone from the watched server", victim)
	}
}

// ── R: a pane the hub did not create ──────────────────────────────────────────────────────────────

// `R` on a pane the hub did not create REFUSES, in one sentence, and respawns nothing.
//
// Which of "refuse" and "ask" it does is the thing this case pins, and it refuses: docs/design.md
// §10 gives `R` to hub-launched agents only, because a restart is `claude --resume <uuid>` and the
// hub has no uuid for a pane it did not launch. §22.1 measured why guessing is not an option — a
// second `--resume` of a live session returns rc=0 with no lock and appends into the transcript the
// live process holds open.
//
// The two subtests differ in ONE thing, and it is the one an operator would confuse: the second pane
// IS identified as an agent by the same walk that gates every write. If `R` keyed on identification
// rather than on a known session, the second row would be restarted, so this pair says which
// question the refusal is answering.
func TestE2EUIDestructiveRestartRefusesAPaneTheHubDidNotCreate(t *testing.T) {
	for _, c := range []struct {
		name  string
		agent bool
	}{
		{"a pane the walk does not vouch for", false},
		{"a pane the walk DOES vouch for, launched outside the hub", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ui, target, ids, work := destructiveHub(t, 1)
			victim := ids[0]
			// The pane TARGET and the row NEEDLE are two different things, and folding them into one
			// variable worked only while every row carried its pane id. `victim` is what tmux is
			// asked about (`pane_pid`); `needle` is what the screen shows.
			needle := hubRowNeedle(victim, ids)
			if c.agent {
				victim = destructiveAgentPane(t, target, work)
				// This pane joins the `watched` session, so that session now puts TWO rows on the
				// screen and the ids are drawn again — which is why the needle is the id here.
				needle = victim
				waitUntil(t, "the agent pane to reach the dashboard", 30*time.Second, func() bool {
					s, err := paneScreen(t, ui, "ui")
					return err == nil && strings.Contains(s, victim)
				})
			}
			destructiveSelect(t, ui, needle)
			if c.agent {
				// Wait until the hub really does vouch for it, or this subtest is the other one.
				dialog := destructiveOpenVouchedInterrupt(t, ui)
				if strings.Contains(dialog, "cannot be identified as an agent") {
					t.Fatalf("the hub does not vouch for the agent pane, so this case cannot say "+
						"whether R keys on identification:\n%s", dialog)
				}
				send(t, ui, "Escape")
				screenHas(t, ui, "cancelled", "esc must leave the interrupt dialog")
			}
			before := destructivePanePID(t, target, victim)
			if before == "" {
				t.Fatalf("pane %s has no pane_pid before the restart", victim)
			}

			sendLiteral(t, ui, "R")
			screenHas(t, ui, "only hub-launched agents can be restarted",
				"`R` on a pane the hub did not create must say WHY it will not restart it — a key "+
					"that answers nothing is indistinguishable from a broken key")

			// It refuses rather than asking, so no dialog may appear: a confirmation the hub would
			// then refuse anyway teaches the operator to press enter through dialogs.
			if s := capturePane(t, ui, "ui"); strings.Contains(s, "Confirm restart") {
				t.Errorf("`R` opened a confirmation for an act it cannot perform:\n%s", s)
			}

			// And nothing was respawned. `respawn-pane -k` keeps pane_id and changes pane_pid, so
			// the pid is what says whether the process behind the row was replaced (§19).
			time.Sleep(1500 * time.Millisecond)
			after := destructivePanePID(t, target, victim)
			if after != before {
				t.Fatalf("`R` respawned pane %s despite refusing: pane_pid %s → %s — whatever was "+
					"running there was killed with nothing resumed in its place", victim, before, after)
			}
			if !destructivePaneAlive(t, target, victim) {
				t.Fatalf("`R` destroyed pane %s while refusing to restart it", victim)
			}
		})
	}
}

// `R` with a selection of more than one pane refuses and names the rule, and respawns neither.
//
// The multi-select case is separate because it is the one where a partial act is possible: a restart
// that took "the first selected pane" would respawn one process the operator did not single out. The
// note has to name the rule rather than the failure, so the way out is on screen.
func TestE2EUIDestructiveRestartRefusesMoreThanOnePaneAndRespawnsNeither(t *testing.T) {
	ui, target, ids, _ := destructiveHub(t, 2)
	before := map[string]string{}
	for _, id := range ids {
		walkTo(t, ui, id)
		send(t, ui, "space")
		time.Sleep(300 * time.Millisecond)
		before[id] = destructivePanePID(t, target, id)
	}
	// The COUNT, from the footer, because the whole case is about a selection larger than one and a
	// second `space` that toggled a mark OFF would leave exactly the selection R accepts.
	waitUntil(t, "the footer to report both panes marked", 15*time.Second, func() bool {
		s, err := paneScreen(t, ui, "ui")
		return err == nil && strings.Contains(s, "2 marked")
	})

	sendLiteral(t, ui, "R")
	screenHas(t, ui, "restart requires exactly one pane",
		"`R` on a multi-pane selection must name the rule, not merely decline")

	time.Sleep(1500 * time.Millisecond)
	for _, id := range ids {
		if after := destructivePanePID(t, target, id); after != before[id] {
			t.Errorf("`R` respawned pane %s from a selection it refused: pane_pid %s → %s",
				id, before[id], after)
		}
	}
}
