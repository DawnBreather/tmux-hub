package main

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/hostset"
	"github.com/DawnBreather/tmux-hub/internal/hub"
)

// This file is the floor under every other test in the repository: it asks whether
// the code they exercise is REACHABLE FROM THIS BINARY.
//
// Nothing asked that before, and the answer was no. An entire package
// (internal/proc) and six exported constructors had no importer outside their own
// tests: the write path was built, reviewed, tested and never connected, so the
// binary shipped a dashboard whose first send crashed it. Every unit test passed
// throughout, and structurally none of them could see it.

// moduleRoot walks up to the directory holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

// TestEveryPackageIsReachableFromMain asks the toolchain, which cannot be fooled by
// a test-only import: `go list -deps` on the command reports exactly the packages
// the BINARY links.
func TestEveryPackageIsReachableFromMain(t *testing.T) {
	// A package with no non-test Go files is legitimately unreachable from main: it is
	// test-only by construction. internal/e2e is exactly that — a build-tagged suite that
	// drives the binary from outside — and demanding it be linked in would be demanding
	// that the product import its own tests.
	//
	// The floor still does its job: any package with real code must be reachable, which is
	// the defect this test exists for (an entire package once shipped built, tested and
	// wired to nothing).
	testOnly := func(dir string) bool {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return false
		}
		for _, e := range ents {
			n := e.Name()
			if strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
				return false
			}
		}
		return true
	}

	root := moduleRoot(t)
	cmd := exec.Command("go", "list", "-deps", "./cmd/tmux-hub")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	linked := map[string]bool{}
	for _, l := range strings.Split(string(out), "\n") {
		linked[strings.TrimSpace(l)] = true
	}

	// Every internal package must be in the binary. A package that is not is either
	// dead or unwired, and the two are indistinguishable from inside its own tests.
	entries, err := os.ReadDir(filepath.Join(root, "internal"))
	if err != nil {
		t.Fatalf("reading internal/: %v", err)
	}
	if len(entries) < 5 {
		t.Fatalf("found %d packages under internal/, which is too few to be the whole tree",
			len(entries))
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pkg := "github.com/DawnBreather/tmux-hub/internal/" + e.Name()
		if testOnly(filepath.Join(root, "internal", e.Name())) {
			continue
		}
		if why, exempt := underConstruction[e.Name()]; exempt {
			// A stale exemption is the dangerous half. Once the package IS linked the
			// exemption has done its job and must go, or the floor is permanently
			// lowered for it and nobody notices.
			if linked[pkg] {
				t.Errorf("%s is linked, so its under-construction exemption is stale — remove it "+
					"(it says: %s)", pkg, why)
			}
			continue
		}
		if !linked[pkg] {
			t.Errorf("%s is not reachable from cmd/tmux-hub — it is built, tested and not wired in", pkg)
		}
	}
}

// underConstruction names packages that exist before the task that wires them, with
// WHO removes the entry. It exists because a plan can legitimately create a package
// several tasks before its consumer, and for those tasks the floor cannot tell that
// state from the defect it was built for — an entire package once shipped built,
// tested and wired to nothing.
//
// Two things keep it from becoming the hole it looks like. An entry must name the task
// that deletes it, so it is an appointment rather than a permission; and the loop above
// fails when an exempt package turns out to BE linked, so an entry cannot outlive its
// reason quietly. Task 10 of the plan below asserts this map is empty before the branch
// closes.
// Emptied by Task 7: the picker consumes hostset.Candidate/Result/Entry, so
// internal/ui links hostset and main links ui. The appointment was written for Task
// 8, which wires main's own probe round and hosts.toml read — that work is still
// outstanding, but the LINKAGE this floor measures arrived a task early, and the loop
// above is right to refuse a stale entry rather than wait for its named task.
var underConstruction = map[string]string{}

// TestTheWritePathHasProductionCallSites asks the sharper question: a package can be
// linked because ONE symbol in it is used while the rest is dead. These are the
// symbols that make a send possible at all, and the defect this catches is precisely
// the one that shipped — every one of them had call sites in _test.go files ONLY.
//
// `outside` says the call site must also be in another package, which is the right
// bar for anything the WIRING is supposed to call. It is false for the few symbols
// whose production caller legitimately lives beside them (the Keeper calls Stamp;
// proc.Local calls Snapshot), and that relaxation rests on a positive fact rather
// than on convenience: TestEveryPackageIsReachableFromMain proves the calling
// package is linked into the binary, so its non-test caller is reached.
func TestTheWritePathHasProductionCallSites(t *testing.T) {
	root := moduleRoot(t)
	files := nonTestSources(t, root)
	if len(files) < 15 {
		t.Fatalf("found %d non-test source files, which is too few to be the whole tree",
			len(files))
	}

	// Exemptions are deliberately unwired symbols with documented reasons. The floor
	// still asks the question — these entries remain in the list — but the answer is
	// "intentionally absent, see docs/known-issues.md" instead of an error. An empty
	// `exempt` string means the symbol must have a production call site.
	for _, c := range []struct {
		symbol    string // what a call site looks like
		definedIn string // the package that defines it
		outside   bool   // the call site must be in a different package
		why       string
		exempt    string // if non-empty, the symbol is intentionally unwired (see docs/known-issues.md)
	}{
		{"broadcast.NewInstance(", "internal/broadcast", true,
			"without an instance id the hub's option and buffer names collide with another hub's", ""},
		{"broadcast.NewStamper(", "internal/broadcast", true,
			"without a stamper no pane is ever identified and every send is refused", ""},
		{"broadcast.NewSender(", "internal/broadcast", true,
			"a zero-value Sender holds a nil Stamper, so the first send panics", ""},
		{"broadcast.NewKeeper(", "internal/broadcast", true,
			"nothing would re-stamp a pane per tick, which is what the guard means by 'recently'", ""},
		{"broadcast.Sweep(", "internal/broadcast", true,
			"a crashed hub's payload stays as the user's most recent paste buffer", ""},
		{".Stamp(", "internal/broadcast", false,
			"a token that is never written cannot be checked", ""},
		{".Unstamp(", "internal/broadcast", false,
			"a pane whose agent exited would keep its token and accept a prompt at a shell prompt", ""},
		{".Submit(", "internal/broadcast", true,
			"a pasted prompt nobody submits sits in the input box while the hub reports delivered", ""},
		{".Interrupt(", "internal/broadcast", true,
			"the key that stops a runaway agent", ""},
		{"proc.Local(", "internal/proc", true,
			"identification of local panes", ""},
		{"proc.OverSSH(", "internal/proc", true,
			"identification of remote panes, which is the half that needs the ssh master", ""},
		{"IdentifyAgent(", "internal/proc", false,
			"the process walk itself", ""},
		{"Snapshot()", "internal/proc", false,
			"the /proc pass the walk reads", ""},
		{"history.Open(", "internal/history", true,
			"a Log with no path returns 'invalid argument' from every Append", ""},
		{"history.DefaultPath(", "internal/history", true,
			"where the send log lives", ""},
		{".Recent(", "internal/history", true,
			"the reader, without which the log is write-only", ""},
		{"hide.Open(", "internal/hide", true,
			"the hidden set reader", ""},
		{"hide.DefaultPath(", "internal/hide", true,
			"where the hidden set lives", ""},
		{"launch.NewSessionID(", "internal/launch", true,
			"generates the session uuid that namespaces a Claude session's state directory", ""},
		{"tmux.NewWindow(", "internal/tmux", true,
			"creates a window, which is what 'n' does", ""},
		{"tmux.NewSession(", "internal/tmux", true,
			"creates a session, which is what 'n' does when no session exists", ""},
		{"tmux.RespawnPane(", "internal/tmux", true,
			"restarts a pane, which is what 'R' does", ""},
		{"tmux.KillPane(", "internal/tmux", true,
			"kills a pane, which is what 'K' does", ""},
		{"tmux.SetWindowOption(", "internal/tmux", true,
			"configures remain-on-exit, which is what prevents respawn from auto-closing the window", ""},
		{"tmux.KillWindow(", "internal/tmux", true,
			"kills a window, which nothing in the hub currently does",
			"kept alongside KillPane but unwired: the hub's unit is a pane, not a window"},
		{"tmux.KillSession(", "internal/tmux", true,
			"kills a session (§19 lists this verb but K kills panes only)",
			"built but unwired: §19 overpromises, or the feature is scoped as future work"},
		{"tmux.PaneAlive(", "internal/tmux", true,
			"checks whether a pane still exists before resuming into it",
			"deliberately kept for a FUTURE path that resumes into a different pane (Task 14 comment explains)"},
		{".Adopt(", "internal/broadcast", false,
			"maps a Claude session uuid to a pane, which is what resume needs to stamp the new pane", ""},

		// The pane-to-session join. All three were built, covered and unwired — the exact
		// state this file exists to catch, and it went unnoticed because the registry's
		// absorb branch HAD tests: a join can be tested and dead at the same time. Unwired,
		// a session with a pane appeared twice, §14's answer to quiet-versus-idle was not in
		// effect, and the door would have duplicated every row it woke.
		{".SetClaudeSession(", "internal/registry", true,
			"the pane never learns which Claude session runs in it, so UpdateAgents adds a second row for a session that already has one", ""},
		{".SessionSnapshot(", "internal/broadcast", true,
			"the identity store's half of the join, and the form that lets it be applied without holding the Keeper's lock", ""},
		{".joinAdoptedSessions(", "internal/ui", false,
			"the wire itself: without a call on the agents poll the two halves above never meet", ""},
		{"proc.SessionID(", "internal/proc", true,
			"the GENERAL half of the join — a session id for a pane the operator started by hand, which Adopt cannot know",
			"deliberately unwired: Adopt covers every pane the hub creates, which is the door's whole population, and this costs a /proc subtree read per pane. Recorded in docs/known-issues.md"},

		// The projects screen's file pair. The package-level floor covers internal/project
		// because main links it, but no symbol row asked whether the READ and the WRITE are
		// both reachable — and the read has two entry points, one of which is dead.
		{"project.LoadAll(", "internal/project", true,
			"the operator's project rules and aliases would never be read, so every row would group by its basename fallback", ""},
		{".Save(", "internal/project", true,
			"pressing N would change the screen and forget the name at exit", ""},

		// The host set and its transport. Every one of these was built, covered and
		// unwired for six tasks of the plan that added it, which is the state this file
		// exists to catch — and the state the package-level floor above cannot see now
		// that internal/ui links hostset for the picker's types alone.
		{"hostset.LoadHosts(", "internal/hostset", true,
			"without it hosts.toml is never read and --host is the only way to name a host", ""},
		{"hostset.SaveHosts(", "internal/hostset", true,
			"the picker's tick boxes would be forgotten the moment it closes", ""},
		{"hostset.ParseSSHConfig(", "internal/hostset", true,
			"the picker would have no candidates to show, on the screen a person meets first", ""},
		{"hostset.ProbeAll(", "internal/hostset", true,
			"no candidate would ever be told apart from a git remote, so none could be kept", ""},
		{"ui.WithPicker(", "internal/ui", true,
			"the picker screen and its three ports would be unreachable from the binary", ""},
		{"hub.ControlPathFor(", "internal/hub", true,
			"a master would be addressed at a path nothing else derives, so none is ever adopted", ""},
		{".Ensure(", "internal/hub", true,
			"no ssh master is ever spawned, so every host from hosts.toml reports a dead one", ""},
		{"hub.ReconcileMasters(", "internal/hub", true,
			"a master whose host is no longer enabled would run until --stop-masters", ""},
		{"hub.StopAllMasters(", "internal/hub", true,
			"--stop-masters is the only way out of a design where masters outlive the hub", ""},
	} {
		found := ""
		for _, f := range files {
			if c.outside && strings.HasPrefix(f.rel, c.definedIn) {
				continue
			}
			if calls(f.body, c.symbol) {
				found = f.rel
				break
			}
		}
		if found == "" && c.exempt == "" {
			where := "in any non-test file"
			if c.outside {
				where = "outside " + c.definedIn
			}
			t.Errorf("%s has no production call site %s — %s", c.symbol, where, c.why)
		}
	}
}

// calls reports whether a body CALLS the symbol, as opposed to declaring it. A
// declaration matches every plausible pattern — `func Snapshot()` contains
// "Snapshot()" — and counting one would let a symbol vouch for its own wiring.
func calls(body, symbol string) bool {
	for _, line := range strings.Split(body, "\n") {
		i := strings.Index(line, symbol)
		if i < 0 {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "func ") {
			continue
		}
		return true
	}
	return false
}

type source struct {
	rel  string
	body string
}

func nonTestSources(t *testing.T, root string) []source {
	t.Helper()
	var out []source
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "docs", ".superpowers", "prototypes":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		out = append(out, source{rel: rel, body: string(b)})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
	return out
}

// §16 promises a usable dashboard in under 50 ms, and a master spawn is measured at
// 1550 ms (1530/1551/1606 over three trials, ~28 failed `ssh -O check`s at 50 ms
// apart) with Ensure polling for up to 10 s. Five enabled hosts spawned before the
// program starts would miss that commitment by thirty times, and the user would watch
// a blank terminal for a second and a half on every start.
//
// So this asserts the ORDER rather than trusting it, in the shape internal/ui's
// TestAStalledHostDoesNotStopIdentificationEverywhere already uses: a dependency that
// blocks, plus a release channel. Both master calls block here, and the UI must
// already be running while they do.
//
// The positive control is the half that makes it a test: without it, a startup that
// never spawns anything at all passes trivially.
func TestTheMasterSpawnIsNotOnTheFirstPaintPath(t *testing.T) {
	var once sync.Once
	release := make(chan struct{})
	releaseAll := func() { once.Do(func() { close(release) }) }
	t.Cleanup(releaseAll)

	spawned := make(chan string, 8)
	reconciled := make(chan []string, 1)
	ops := masterOps{
		ensure: func(_ context.Context, m *hub.Master) error {
			spawned <- m.Alias
			<-release
			return nil
		},
		reconcile: func(_ context.Context, _ string, aliases []string) error {
			reconciled <- aliases
			<-release
			return nil
		},
	}

	hosts := []hub.Host{
		{Label: "local", Socket: "/tmp/tmux-1000/default", LocalProc: true},
		{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/user/1000/cm-1-nuc"},
		{Label: "eu", SSHDest: "eu", ControlPath: "/run/user/1000/cm-2-kg"},
		// A --host entry whose label is not its ssh destination, which is the one shape
		// that can tell "keyed on the destination" from "keyed on the label". ssh is
		// handed the destination and ControlPathFor hashes it, so a label here would
		// name a master that does not exist.
		{Label: "far", SSHDest: "far-dest", ControlPath: "/run/user/1000/cm-3-far"},
	}

	painted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- startDashboard(context.Background(), hosts, "/run/user/1000", ops,
			func() error { close(painted); return nil })
	}()

	select {
	case <-painted:
	case <-time.After(5 * time.Second):
		t.Fatal("the UI was never started while the master calls were in flight — the spawn " +
			"is ON the first-paint path, which costs 1.55 s per host before anything is drawn")
	}

	// The positive control: the spawns must really be happening, concurrently, for
	// exactly the hosts that have a master. A local server has none. Spawning is keyed on
	// the ssh DESTINATION for the same reason the sweep is — that is the string ssh gets.
	got := map[string]bool{}
	for range 3 {
		select {
		case alias := <-spawned:
			got[alias] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("only %v were spawned; a startup that spawns nothing would satisfy the "+
				"ordering assertion above for the wrong reason", got)
		}
	}
	if want := map[string]bool{"nuc": true, "eu": true, "far-dest": true}; !maps.Equal(got, want) {
		t.Errorf("spawned %v, want %v", got, want)
	}
	select {
	case alias := <-spawned:
		t.Errorf("%q got a master: a host with no ssh destination has none to spawn", alias)
	default:
	}

	// The orphan sweep runs too, and it is handed the configured aliases — never an
	// empty list, which hub.ReconcileMasters refuses precisely because "no configured
	// hosts" would otherwise mean "stop everything the operator is running".
	//
	// The SET, not the count. A count cannot see a swap, a collision, or a drift: the
	// sweep rebuilds its spare-list by re-deriving ControlPathFor from these strings, so
	// one wrong alias means the hub's own live master is not in `safe` and the sweep
	// stops the master ensureMasters spawned moments earlier, in the same goroutine
	// group. `far`'s ssh destination differs from its label on purpose — keying this on
	// the label instead of the destination is the same defect and is invisible when the
	// two strings are equal.
	select {
	case aliases := <-reconciled:
		got := map[string]bool{}
		for _, a := range aliases {
			got[a] = true
		}
		want := map[string]bool{"nuc": true, "eu": true, "far-dest": true}
		if len(aliases) != len(want) || !maps.Equal(got, want) {
			t.Errorf("reconcile got %v, want the three configured ssh destinations %v — an "+
				"alias that is not exactly what ControlPathFor was given makes the hub's own "+
				"master look like an orphan", aliases, want)
		}
	case <-time.After(5 * time.Second):
		t.Error("the startup never reconciled orphaned masters, so a master whose host is " +
			"no longer enabled runs until --stop-masters")
	}

	releaseAll()
	if err := <-done; err != nil {
		t.Fatalf("startDashboard: %v", err)
	}
}

// The other half of the same decision: with nothing configured, the sweep must not run
// at all. hub.ReconcileMasters refuses an empty alias list, and this is the call site
// where a failed hosts.toml load and an empty file would otherwise look identical — a
// routine start must never end the masters of a hub started with another host file.
func TestNothingConfiguredMeansNoSweepRatherThanSweepEverything(t *testing.T) {
	swept := make(chan []string, 1)
	ops := masterOps{
		ensure: func(context.Context, *hub.Master) error { return nil },
		reconcile: func(_ context.Context, _ string, aliases []string) error {
			swept <- aliases
			return nil
		},
	}
	local := []hub.Host{{Label: "local", Socket: "/tmp/tmux-1000/default", LocalProc: true}}
	if err := startDashboard(context.Background(), local, "/run/user/1000", ops,
		func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case aliases := <-swept:
		t.Fatalf("the sweep ran with %v configured — every master under the runtime "+
			"directory would be stopped by a hub that simply has no hosts of its own", aliases)
	case <-time.After(300 * time.Millisecond):
	}
}

// §9's rule about the one screen a person meets first, and the two ways to get it wrong
// are opposite and both silent: never opening leaves a first-run operator looking at an
// empty dashboard with no way to know the picker exists, while always opening puts a
// screen over the dashboard on every start of a hub that is already configured.
//
// This is the DECISION half of the picker's wiring. Its three EFFECTS are asserted in
// internal/ui, against the real WithPicker applied to a real model — a call-site floor
// row proves `ui.WithPicker(` is invoked and can never prove it does anything, and those
// are different questions.
func TestThePickerOpensOnlyWhenTheFileHasDecidedNothing(t *testing.T) {
	for _, c := range []struct {
		name string
		kept []hostset.Entry
		want bool
	}{
		{"first run: no file at all", nil, true},
		{"a file that parsed and holds nothing", []hostset.Entry{}, true},
		{"one enabled host", []hostset.Entry{{Alias: "nuc", Enabled: true}}, false},
		// A DISABLED entry is still a decision: the user went to the picker and said no
		// to that host. Opening again asks a question they have answered.
		{"every host disabled", []hostset.Entry{{Alias: "eu", Enabled: false}}, false},
		{"a mixed fleet", []hostset.Entry{
			{Alias: "nuc", Enabled: true}, {Alias: "eu", Enabled: false}}, false},
	} {
		if got := pickerOpensAtStartup(c.kept); got != c.want {
			t.Errorf("%s: pickerOpensAtStartup = %v, want %v", c.name, got, c.want)
		}
	}
}
