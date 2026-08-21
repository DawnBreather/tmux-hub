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
// EMPTY, and it has been emptied twice by the same mechanism working as designed.
//
// The first time was Task 7 of the picker plan: the picker consumed
// hostset.Candidate/Result/Entry, so internal/ui linked hostset and main linked ui.
// The second is task 6 of docs/plans/2026-08-20-fleet-discovery.md — the entry read
// "task 1 lands the graph model; task 6 wires it through internal/ui and the picker,
// and deletes this entry", and this is that deletion. `internal/ui/discovered.go`
// holds the graph behind a lock and draws it in the picker, and
// `internal/hostset/remote.go` now takes its per-hop cap from fleet.DefaultBreadth —
// so fleet is linked twice over and the loop above refused the entry as STALE the
// moment it was. The refusal is what makes an appointment an appointment rather than
// a permission: it arrived before any human noticed, and removing it is the proof the
// package is wired rather than a claim that it is.
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

		// THE FLEET GRAPH, THE REMEMBERED TIMINGS, AND THE MACHINES BEHIND THE HOPS. Twenty-five
		// commits added three packages and a picker section, and not one row here asked whether any of
		// it is REACHED — which is the state this file exists to catch, and it was caught by hand: a
		// verifier deleted the crawl's dispatch from `openPicker` and the whole default suite reported
		// `ok` at rc=0 for internal/ui and cmd/tmux-hub alike.
		//
		// The package-level floor above cannot see this. `internal/fleet` is linked because
		// `internal/hostset` takes its per-hop cap from it, `internal/fleetcache` because main opens a
		// cache — so both packages are in the binary with the whole crawl dead, which is precisely the
		// "linked because ONE symbol is used" case this second test was written for.
		{"fleet.New(", "internal/fleet", true,
			"nothing holds an observation, so a round has nowhere to fold what a hop declared", ""},
		{"fleet.DefaultBudget(", "internal/fleet", true,
			"the crawl runs with a zero budget, and fleet spec §3.3's report of what was cut then names " +
				"a knob nobody applied", ""},
		{"fleet.Diagnose(", "internal/fleet", true,
			"every discovered machine carries a state and no remedy, which invariant 4 forbids and which " +
				"is this repository's oldest defect class — keep the label, lose the action", ""},
		{"fleet.BreadthCut(", "internal/fleet", true,
			"a per-hop cut would word itself in hostset instead of in the one place that owns the " +
				"sentence, and two spellings of a cut are two counts a reader cannot compare", ""},
		// The receiver is IN the needle for these two, because the bare method name is also somebody
		// else's verb: `.Observe(` is the send log's (model.go: `m.log.Observe(m.panes, …)`) and
		// `.Allow(` is this feature's own wrapper. A needle satisfied by a different mechanism is a row
		// that cannot fail for the reason it names.
		{"g.Observe(", "internal/fleet", true,
			"the graph is made, locked and never written to, so the section draws an empty fleet forever", ""},
		{"g.Allow(", "internal/fleet", true,
			"the budget is a comment rather than a gate, which is the silent horizon §3.3 exists to prevent", ""},
		{".Nodes(", "internal/fleet", true,
			"a machine the root has verified never reaches the section's Mounted tier", ""},
		{".Candidates(", "internal/fleet", true,
			"the declared-and-unverified machines are the section's whole product today (§3.4)", ""},
		{".Cuts(", "internal/fleet", true,
			"a cut is filed and never read, so a truncated crawl reads exactly like a complete one", ""},
		{".Edges(", "internal/fleet", true,
			"the observer-to-machine edges",
			"built but unwired: the section is a list of nodes and candidates (fleet spec §3.4), and " +
				"nothing draws the graph as a graph yet"},

		{"fleetcache.Open(", "internal/fleetcache", true,
			"no cache is opened, so every opening of the picker orders by name until a probe has answered", ""},
		{"fleetcache.DefaultPath(", "internal/fleetcache", true,
			"the cache would live wherever a caller chose, and the next run would read a different file", ""},
		// KeysOfNode and not KeyOfNode, and the row moved rather than being deleted: the reader's
		// question changed from "the key" to "EVERY key this node's memory could be under", because a
		// node's fingerprint set grows when two machines turn out to be one and the memory filed under
		// the absorbed fingerprint would otherwise be unreachable. The invariant is unchanged — the
		// reader must not build a node's key by hand — so what this row protects is the same thing.
		{"fleetcache.KeysOfNode(", "internal/fleetcache", true,
			"a node's remembered figure would be keyed by hand at the reader, which is the drift the " +
				"KeyOf* pair exists to prevent — `fav.KeyOf` earned that rule twice on two surfaces", ""},
		{"fleetcache.KeyOfCandidate(", "internal/fleetcache", true,
			"the candidate half of the same rule",
			"unwired, and measured rather than assumed: a candidate INHERITS ITS HOP's figure, so " +
				"DiscoveredRowsFor keys on `{Observer: rootObserver, Alias: c.Observer}` — the root's " +
				"alias for the HOP — and never on the candidate's own pair. Nobody has timed a machine " +
				"the root cannot reach, so there is no fact to key that way yet"},
		// The two remembered-timing PORTS are method VALUES rather than calls, so the wiring is the
		// assignment and there is nothing with a paren to look for. The field name is in the needle
		// because `cache.Facts` on its own is a substring of the TYPE `fleetcache.Facts` (measured:
		// internal/ui/discovered.go names that type four times), which would make the row satisfied by
		// a signature.
		{"Facts: cache.Facts", "internal/fleetcache", true,
			"the section orders by remembered figures and would have none, so a probe's measurement " +
				"would be written each run and read never", ""},
		{"Learn: cache.Record", "internal/fleetcache", true,
			"nothing persists what a round measured, which is the whole reason the package exists", ""},

		{"hostset.RemoteCandidates(", "internal/hostset", true,
			"the Behind port has nothing behind it, so the picker says the hub was started without a way " +
				"to read a hop's own ssh config — on a hub that has one", ""},
		// BARE, and the package qualifier would have been a row that cannot pass: the only caller is
		// `Probe` in the same package, and an in-package caller writes `ParseHostKeys(errOut)`. The
		// convention this file already uses for that shape is `IdentifyAgent(` and `Snapshot()`.
		{"ParseHostKeys(", "internal/hostset", false,
			"a probe harvests no machine identity, so two aliases for one machine stay two nodes and a " +
				"remembered fact does not survive the alias it was learned under", ""},

		// The three wires inside internal/ui, which is where the crawl becomes a screen. `outside` is
		// false because a screen's dispatcher is legitimately its own package — the package-level floor
		// above proves internal/ui is in the binary, so a non-test caller here is reached.
		{".crawlBehind(", "internal/ui", false,
			"the picker opens and nothing ever asks a hop what it declares: `Behind your hops` is built, " +
				"ordered, drawn and permanently empty. MEASURED: deleting this one dispatch left every " +
				"default gate green, which is why this row exists", ""},
		{".learnFromProbe(", "internal/ui", false,
			"a probe round's figures are measured and forgotten, so the section cannot paint in a " +
				"remembered order and reorders under its reader instead", ""},
		{".discoveredArrived(", "internal/ui", false,
			"a finished round arrives as a message nothing folds in", ""},
		{"RenderDiscovered(", "internal/ui", false,
			"the section is built and never drawn, which is this repository's signature defect", ""},
		{"DiscoveredRowsFor(", "internal/ui", false,
			"the graph never becomes rows on the crawl's own path", ""},
		{".crawlRefusal(", "internal/ui", false,
			"the sentence the screen owes an operator who cannot look behind a hop at all",
			"NOT deliberately unwired — an APPOINTMENT, in `underConstruction`'s sense. Measured: its " +
				"only caller in the tree is internal/ui/discovered_test.go, so the two sentences it " +
				"words (`this hub was started without a way to read a hop's own ssh config`, `nothing " +
				"to look behind yet — keep a host with space`) are defined, tested and never drawn, and " +
				"an empty section reads as `there is nothing back there`. The staleness check below " +
				"deletes this entry the moment the picker draws it"},
		{"ui.WithDiscovered(", "internal/ui", true,
			"installs a section without a network, a hop or a container",
			"unwired, and its own doc comment overstates: it says a frame test and the published mockup " +
				"hold a section through it, and measured, NOTHING calls it — the frame tests assign " +
				"`m.discovered` directly. Kept because the option is the only way an out-of-package " +
				"caller could hold one"},
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
		// A STALE EXEMPTION IS THE DANGEROUS HALF, and until now only the package map above said so.
		// An exemption is a claim that a symbol is unwired; once it IS wired the claim is false, and a
		// false one here lowers the floor for that symbol forever with nobody looking. Measured before
		// adding this: all seven exempt rows are genuinely unwired in a clean `git archive HEAD` tree,
		// so it starts green — and it is what turns the `.crawlRefusal(` appointment above into an
		// appointment rather than a permission.
		if found != "" && c.exempt != "" {
			t.Errorf("%s IS called from %s, so its exemption is stale — delete the exempt string "+
				"(it says: %s)", c.symbol, found, c.exempt)
		}
	}
}

// calls reports whether a body CALLS the symbol, as opposed to declaring it or writing about it.
// Three rules, and the two new ones were each measured rather than reasoned.
//
// A DECLARATION does not count: `func Snapshot()` contains "Snapshot()", and counting it would let a
// symbol vouch for its own wiring. But the rule cannot be "skip the whole line", because a ONE-LINE
// FUNCTION carries its body on that line — `func crawlBudget() fleet.Budget { return
// fleet.DefaultBudget() }` is a real call site, and it is the ONLY call site `internal/ui` has for
// `fleet.DefaultBudget`; `fleet.New` is reached through exactly one more of the same shape. Both were
// invisible to this floor, so a row for either would have failed against correct code. On a `func`
// line the search therefore starts at the opening brace: a name in the SIGNATURE is a declaration, a
// name in the BODY is a call.
//
// A COMMENT does not count either. This repository's comments name the functions they are about
// constantly, so a row satisfied by prose would go green on the day the call is deleted and the
// sentence describing it survives — which is the wrong-comment shape CLAUDE.md already warns about,
// pointed at the floor itself. Measured before adding it: none of the 43 rows that existed then was
// satisfied by a comment line, so this narrows the floor without moving any row.
func calls(body, symbol string) bool {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.HasPrefix(trimmed, "func ") {
			brace := strings.Index(line, "{")
			if brace < 0 {
				continue
			}
			line = line[brace:]
		}
		if strings.Contains(line, symbol) {
			return true
		}
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
			// `.claude` holds WORKTREES, which are whole copies of this repository that git tracks
			// nothing of. A copy carries the same production lines, so a symbol unwired HERE is
			// vouched for by a file that used to wire it, and every row below quietly stops asking its
			// question. Measured: with three worktrees present and `hostset.RemoteCandidates` deleted
			// from the real `main.go`, this floor reported `ok` — the copies answered for it.
			//
			// It is also the second reason CLAUDE.md says to verify the COMMIT: `git archive HEAD`
			// contains no untracked file, so a gate run there never had this hole.
			case ".git", "docs", ".superpowers", "prototypes", ".claude":
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
		{Label: "eu", SSHDest: "eu", ControlPath: "/run/user/1000/cm-2-eu"},
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
