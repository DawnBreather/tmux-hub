// Command tmux-hub is a control panel over tmux sessions, local and remote, built
// for orchestrating many Claude Code sessions at once.
//
// It reads every pane and every Claude session, attaches to one with `a`, and
// broadcasts a typed prompt into the panes the user selected — guarded, per target,
// by the pane's own identity token (docs/design.md §7).
//
// Which hosts it watches comes from `hosts.toml`, which the picker writes (§9): an
// enabled entry is polled over an ssh master the hub spawns and adopts, with no
// forwarded socket anywhere (§5). `--host label=/path/to/socket` is still here, and
// is now what it should always have been — the escape hatch for a setup the file
// cannot describe, ADDED to the file's hosts rather than replacing them.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/fav"
	"github.com/DawnBreather/tmux-hub/internal/hide"
	"github.com/DawnBreather/tmux-hub/internal/history"
	"github.com/DawnBreather/tmux-hub/internal/hostset"
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
	"github.com/DawnBreather/tmux-hub/internal/ui"
)

// HistoryMaxBytes is where the send log rotates, keeping the newest half. Big
// enough to hold a long working day of broadcasts, small enough that the reader
// stays instant.
const HistoryMaxBytes = 4 << 20

// version is stamped at link time by the release build (`-ldflags "-X main.version=v1.2.3"`).
// Unstamped it is `dev`, and `versionString` is what a reader should call.
var version = "dev"

// versionString answers for all three install routes, which is more than the ldflag can do.
//
// `go install …@v0.1.0` applies no ldflags at all, so a RELEASED version installed that way reported
// `dev` — the one answer nobody can act on, because it names no source. The toolchain already
// records the module version in the binary's build info, so the fallback is a read rather than a
// guess. Measured on this program, all three routes:
//
//	go build (working tree at a tag, modified)  →  v0.1.0+dirty
//	go build -ldflags -X main.version=v9.9.9    →  v9.9.9
//	go install …@latest                         →  the tag, or a pseudo-version
//
// `dev` therefore survives only where build info has nothing either — a build with no VCS
// information at all, which reports `(devel)`.
func versionString() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}

const (
	// TmuxTimeout is the per-call deadline for tmux work, local or over a master.
	TmuxTimeout = 5 * time.Second

	// MasterTimeout is the deadline for the RAW door, and it is longer than
	// TmuxTimeout for one measured reason: a spawned master takes 1.55 s to become
	// checkable (1530/1551/1606 ms over three trials) and Ensure polls for up to
	// 10 s, so a 5 s deadline would kill the wait for a master that was on its way up.
	MasterTimeout = 20 * time.Second

	// StatusTimeout bounds one `status` report. It is generous because that path
	// ensures the masters first — a one-shot report has no first-paint commitment,
	// and a report of "no live ssh master" on every remote host would be about the
	// hub's own startup rather than about the fleet.
	StatusTimeout = 45 * time.Second

	// DefaultProbeTimeout is how long one host gets to answer `tmux -V` in the
	// picker. It is a display decision rather than a membership one: measured over
	// three probes each, `eu` answered `tmux 3.2a` at 5.4, 9.1, 15.7 and 18.4 s and
	// `web-app` at 4.4, 7.4 and 19.6 s, so no value admits every usable host. A host
	// that runs out of time keeps its tick box and its reason says slow rather than
	// absent (§9), which is what makes a fourfold swing survivable.
	DefaultProbeTimeout = 10 * time.Second

	// ProbeConnectTimeout is ssh's own connect budget per probe, in seconds. See
	// probeArgs for what it is worth.
	ProbeConnectTimeout = "6"
)

// hostFlags collects --host label=/path/to/socket, repeatable.
//
// This is the escape hatch, not the interface: `hosts.toml` is where hosts come from
// (§9), and this flag exists for the setup the file cannot describe — a second local
// server on its own socket, or an operator who is already running their own forward
//
//	ssh -N -M -S ~/.ssh/cm-nuc -L /run/user/1000/nuc.sock:/tmp/tmux-1000/default nuc
//
// and wants the hub to use it. A host given a socket is polled over that socket;
// everything above it is the same code as for the local server, which is the claim
// §5 makes.
type hostFlags []hub.Host

// fail prints one diagnosis with the program's name and stops, which six call sites did by hand.
// The prefix is here so a change to how this program identifies itself is one edit, and the exit
// code travels with it: the pairs it replaces were `Fprintln` then `os.Exit(1)`, and a pair is
// exactly the shape where one half gets forgotten.
func fail(err error) {
	fmt.Fprintln(os.Stderr, "tmux-hub:", err)
	os.Exit(1)
}

func (h *hostFlags) String() string { return fmt.Sprint(*h) }

// Set parses label=/path/to/socket[,ssh=destination][,ctl=/path/to/controlsocket][,local]
//
// ssh and ctl are what ATTACH needs for a remote host: a forwarded socket cannot
// carry an attach at all, so pressing `a` on a remote pane has to go through the
// ssh master. Without them the host still polls fine and attach says why it
// cannot.
//
// `local` says the socket is a server on THIS machine, which is what lets the
// identity walk read its panes' pids out of the local process table — and
// therefore what makes the host writable at all (Host.IsLocalServer). Default is
// unknown, unknown means no walk, and no walk means read-only, so this is opt-in
// by design. It is a bare key: no value could mean "somewhat local", and
// `local=false` would invite one.
func (h *hostFlags) Set(v string) error {
	parts := strings.Split(v, ",")
	label, socket, ok := strings.Cut(parts[0], "=")
	if !ok || label == "" || socket == "" {
		return fmt.Errorf("want label=/path/to/socket[,ssh=dest][,ctl=path][,local], got %q", v)
	}
	host := hub.Host{Label: label, Socket: socket}
	for _, kv := range parts[1:] {
		if kv == "local" {
			host.LocalProc = true
			continue
		}
		k, val, ok := strings.Cut(kv, "=")
		if !ok || val == "" {
			return fmt.Errorf("want key=value, got %q in %q", kv, v)
		}
		switch k {
		case "ssh":
			host.SSHDest = val
		case "ctl":
			host.ControlPath = val
		default:
			return fmt.Errorf("unknown key %q in %q (want ssh=, ctl= or local)", k, v)
		}
	}
	if host.SSHDest == "" && host.ControlPath != "" {
		return fmt.Errorf("%q gives ctl= without ssh=, so attach has no destination", v)
	}
	// A forwarded socket handed over as a local one is the exact hole
	// Host.IsLocalServer exists to close: the identity walk would answer from THIS
	// machine's process table using REMOTE pane pids, and measured, 97 of 3117 live
	// local pids report "an agent is at or under this pid" — pid 1 among them. So a
	// contradiction is refused rather than resolved in either direction.
	if host.SSHDest != "" && host.LocalProc {
		return fmt.Errorf("%q says both ssh= and local: a socket reached over ssh is not this machine, "+
			"and treating it as one would let pane pids be looked up in the wrong process table", v)
	}
	*h = append(*h, host)
	return nil
}

func main() {
	var hosts hostFlags
	flag.Var(&hosts, "host", "label=/path/to/socket[,ssh=dest][,ctl=path][,local] — a tmux socket to watch (repeatable); "+
		"`local` marks it as a server on this machine, which is what makes it writable")
	hostsPath := flag.String("hosts", hostset.DefaultPath(),
		"the file of hosts the picker writes, and where the fleet comes from")
	probeTimeout := flag.Duration("probe-timeout", DefaultProbeTimeout,
		"how long one host gets to answer `tmux -V` when the picker probes; a host that runs "+
			"out of time keeps its tick box and is shown as slow rather than absent")
	stopMasters := flag.Bool("stop-masters", false,
		"stop every ssh master under $XDG_RUNTIME_DIR/tmux-hub and exit — the way out of "+
			"the adoption design, since a master deliberately outlives the hub")
	noLocal := flag.Bool("no-local", false, "do not watch the local tmux server")
	logStates := flag.String("log-states", "",
		"append every state transition to this JSONL file, to ground the timing thresholds")
	status := flag.Bool("status", false, "print one poll cycle as JSON and exit")
	historyPath := flag.String("history", history.DefaultPath(),
		"append every send to this JSONL file, which the history view reads")
	noHistory := flag.Bool("no-history", false,
		"do not record sends, which also disables the history view and re-send")
	hiddenPath := flag.String("hidden", hide.DefaultPath(),
		"the persisted set of panes to keep out of the dashboard")
	noHide := flag.Bool("no-hide", false, "show every pane, ignoring the hidden set")
	favPath := flag.String("favourites", fav.DefaultPath(),
		"the persisted set of sessions and projects to keep above the rest")
	view := flag.String("view", "tree",
		"which screen the hub opens on: `tree` (the fleet as a filesystem) or `flat` (the "+
			"attention-ordered list). `t` switches at any time")
	noFav := flag.Bool("no-favourites", false, "ignore the favourites and use the attention order alone")
	showVersion := flag.Bool("version", false, "print the version and exit")

	// The subcommand is consumed BEFORE parsing, not after. Go's flag package
	// stops at the first non-flag argument, so `tmux-hub status --host x=/path`
	// used to leave every flag after `status` unparsed — measured against the real
	// binary, which then polled the LOCAL server and reported it under the label
	// `local` while the user had asked about another host. The only tell was that
	// label, in JSON nobody reads closely.
	rest, wantStatus := splitSubcommand(os.Args[1:])
	if err := flag.CommandLine.Parse(rest); err != nil {
		os.Exit(2) // flag.CommandLine already printed the reason and the usage
	}
	if wantStatus {
		*status = true
	}
	// A leftover positional is refused rather than ignored, so `tmux-hub statuss`
	// says so instead of silently starting the dashboard.
	if n := flag.NArg(); n > 0 {
		fmt.Fprintf(os.Stderr, "tmux-hub: unexpected argument %q — the only subcommand is `status`, and flags may go on either side of it\n", flag.Arg(0))
		os.Exit(2)
	}
	// Before anything reads a socket or a file: a person who installed a released binary and is
	// reporting a defect needs to be able to say WHICH binary, and the answer must not depend on
	// there being a tmux server to reach.
	if *showVersion {
		fmt.Println("tmux-hub", versionString())
		return
	}

	// An unknown --view is REFUSED, not ignored. `WithView` acts on one word and does nothing for
	// any other, so a typo would silently hand the operator the screen they did not ask for — and the
	// two screens differ enough that "why is this the old list" is a question they would take to the
	// wrong place. The refusal names both words, which is the whole remedy.
	if *view != "tree" && *view != "flat" {
		fmt.Fprintf(os.Stderr, "tmux-hub: unknown --view %q — it is `tree` (the fleet as a "+
			"filesystem) or `flat` (the attention-ordered list)\n", *view)
		os.Exit(2)
	}

	raw := tmux.NewRawRunner(MasterTimeout)
	ops := liveMasterOps(raw)

	// --stop-masters answers a question the rest of the program cannot: it needs no
	// host file, no dashboard and no fleet, because its subject is every master under
	// the runtime directory whatever its alias. That is the explicit intent
	// hub.StopAllMasters exists for, as opposed to the startup reconcile's "stop what
	// is not configured".
	if *stopMasters {
		if err := stopAllMasters(raw, os.Stdout); err != nil {
			fail(err)
		}
		return
	}

	// `status` wants the fleet and nothing else, so it takes the whole read in one
	// call. A broken host list stops it too — a monitor that silently reports on
	// fewer hosts than it was configured with is worse than one that fails.
	if *status {
		all, err := hostsFrom(*hostsPath, hosts)
		if err == nil {
			err = runStatus(all, !*noLocal, ops)
		}
		if err != nil {
			fail(err)
		}
		return
	}

	// The dashboard needs the ENTRIES as well as the fleet — the picker merges into
	// them, and that is what keeps the `tags` a row cannot show alive across a save —
	// so it reads the file once here and derives both from that one read. A failure is
	// fatal and says so; see loadKept for why an unreadable host list must not be
	// treated as an empty one.
	kept, err := loadKept(*hostsPath)
	if err != nil {
		fail(err)
	}
	all, err := hostsFor(kept, hosts)
	if err != nil {
		fail(err)
	}
	var log *hub.StateLog
	if *logStates != "" {
		l, err := hub.OpenStateLog(*logStates)
		if err != nil {
			fail(err)
		}
		log = l
		defer log.Close()
	}

	// The send log is opened HERE, before the TUI starts, so a path that cannot be
	// written is a startup error with a message rather than a surprise at the first
	// send. --no-history leaves it nil, which the dashboard reports when `h` is
	// pressed instead of showing an empty view.
	var hist *history.Log
	if !*noHistory {
		h, err := history.Open(*historyPath, HistoryMaxBytes)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tmux-hub: cannot open the send log:", err)
			os.Exit(1)
		}
		hist = h
		defer hist.Close()
	}

	// Opened HERE, before the TUI starts, for the same reason the send log is: a
	// path that cannot be read is a startup message rather than a surprise later.
	// Open never fails on CONTENT — a malformed set is an empty set plus a warning
	// (docs/design.md §18) — so an error here is a real filesystem problem.
	var hidden *hide.Set
	if !*noHide {
		h, err := hide.Open(*hiddenPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tmux-hub: cannot open the hidden set:", err)
			os.Exit(1)
		}
		hidden = h
	}

	// Same shape, opposite failure direction: an unreadable favourites file leaves the ORDINARY
	// order and says so, where an unreadable hidden set leaves everything VISIBLE. Both are
	// warnings rather than exits; an error here is a real filesystem problem.
	var favs *fav.Set
	if !*noFav {
		f, err := fav.Open(*favPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tmux-hub: cannot open the favourites:", err)
			os.Exit(1)
		}
		favs = f
	}

	ctx := context.Background()
	// The runtime directory is needed by the picker's Stop port and by the orphan
	// sweep. An error here is not fatal, and cannot be: hostsFor already refused to
	// build a host that needs a control path without it, so reaching this line with an
	// error means nothing configured needs one. The picker says why it cannot stop a
	// master rather than the program refusing to start.
	runtimeDir, rtErr := hub.RuntimeDir()

	// The candidates are read here and NOT probed: parsing two ssh configs costs a
	// fraction of a millisecond, while probing the 20 real candidates on this machine
	// costs about 7 s (§9) — so the picker probes from Init, after the first paint.
	cands := hostset.ParseSSHConfig(sshConfigPaths())
	ports := ui.PickerPorts{
		// The candidates travel back beside the results, unfinished: the two rules that
		// decide what a row says — a Skip candidate is not a row, and hosts.toml outranks
		// the probe — have one owner inside internal/ui, and main is not it.
		Probe: func() ([]hostset.Candidate, []hostset.Result, error) {
			pctx, cancel := context.WithTimeout(ctx, *probeTimeout+5*time.Second)
			defer cancel()
			return cands, hostset.ProbeAll(pctx, cands, *probeTimeout, sshProbe), nil
		},
		Save: func(entries []hostset.Entry) error {
			return hostset.SaveHosts(*hostsPath, entries)
		},
		// Untick a host and its master ends with it. Leaving it is a real leak rather
		// than untidiness: measured, an `ssh -N -M` is reparented to pid 1 and outlives
		// the hub, so a host nobody enabled would hold a connection open indefinitely.
		Stop: func(alias string) error {
			if rtErr != nil {
				return fmt.Errorf("cannot address %s's master: %w", alias, rtErr)
			}
			sctx, cancel := context.WithTimeout(ctx, MasterTimeout)
			defer cancel()
			m := &hub.Master{Alias: alias, ControlPath: hub.ControlPathFor(runtimeDir, alias)}
			return m.Stop(sctx, raw)
		},
		// Stop's other half, and the reason `enter: save and connect` now does both. It
		// reuses the two functions startup already uses for exactly this — hostsFor to
		// derive the label, ssh destination and control path, ensureMasters to adopt or
		// spawn each one concurrently — so a host enabled from the picker is built the
		// same way as one read from the file at startup. Two builders would drift, and
		// the thing that would drift is a control path: two spellings of it mean the hub
		// adopts nothing and spawns a second master per host.
		//
		// The extras go in because hostsFor takes them and the model skips every label it
		// already polls; leaving them out would make this the one caller whose list
		// disagrees with startup's.
		Enable: func(entries []hostset.Entry) ([]hub.Host, error) {
			built, err := hostsFor(entries, hosts)
			if err != nil {
				return nil, err
			}
			// Called from a tea.Cmd, so this blocks a background goroutine and never the
			// paint (§16). It returns when every master has answered, which is what makes
			// the first poll after it worth running.
			ensureMasters(ctx, built, ops)
			return built, nil
		},
	}

	// The labels the fleet has already spoken for. The picker refuses a candidate that
	// collides with one, because hostsFor refuses both shapes FATALLY and main exits 1 on
	// them — at a startup that happens before the TUI, so a file the picker wrote could stop
	// the program starting with no way back but hand-editing TOML.
	reserved := []string{hub.LocalLabel}
	for _, h := range hosts {
		reserved = append(reserved, h.Label)
	}

	// The project overrides. Read here rather than in the UI so the one process that can
	// exit does the reading — but note the ASYMMETRY with hosts.toml, which is §21.11.3's
	// whole reason for two files: an unparseable hosts.toml stops the program, because an
	// empty host list is indistinguishable from a first run and the next save would
	// overwrite it, while an unparseable projects.toml must LOSE NAMES AND KEEP THE FLEET.
	// So this failure is carried into the UI to be shown, never returned.
	projectsPath := project.DefaultPath()
	rules, aliases, rulesErr := project.LoadAll(projectsPath)
	rulesWarn := ""
	if rulesErr != nil {
		rulesWarn = "projects.toml: " + rulesErr.Error() + " — grouping by directory name"
	}

	err = startDashboard(ctx, all, runtimeDir, ops, func() error {
		return ui.Run(ctx, all, !*noLocal, log, hist, hidden,
			ui.WithPicker(ports, kept, reserved, pickerOpensAtStartup(kept)),
			ui.WithProjects(rules, aliases, projectsPath, rulesWarn),
			ui.WithFavourites(favs),
			ui.WithView(*view))
	})
	if err != nil {
		fail(err)
	}
}

// pickerOpensAtStartup is §9's rule: the picker shows itself when the host list has
// decided nothing yet, which is what makes zero configuration a working configuration
// (§16). The dashboard is behind it with the local server already polling, and `esc`
// reaches it.
//
// It is a named function rather than an expression inline in main because it is a
// DECISION and main is not reachable from a test. The two ways to get it wrong are
// opposite and both silent: never opening leaves a first-run operator with an empty
// dashboard and no way to know the picker exists, while always opening puts a screen
// over the dashboard on every start of a configured hub.
//
// An entry the file DISABLED still counts as a decision. The user has been to the
// picker and said no to that host; showing it again would be asking a question they
// have answered.
func pickerOpensAtStartup(kept []hostset.Entry) bool { return len(kept) == 0 }

// loadKept reads the host list, and REFUSES to start on a file it cannot parse.
//
// That refusal is the decision this program has to make, and both alternatives are
// worse in ways that are hard to see afterwards. Read as empty, a broken file is
// indistinguishable from a first run: the picker opens with nothing ticked and the
// first save writes over every decision the reader could not parse, so reporting the
// problem is what destroys the data. And an empty fleet is exactly what
// hub.ReconcileMasters refuses to be handed, because "I have no configured hosts" and
// "stop everything" are different intents and one of them ends the masters of a hub
// the operator is still using.
//
// An ABSENT file is not a failure — hostset.LoadHosts answers with an empty set, and
// §16 promises that zero configuration is a working configuration.
func loadKept(path string) ([]hostset.Entry, error) {
	kept, err := hostset.LoadHosts(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read the host list %s: %w\n"+
			"\tfix the line it names, or move the file aside and let the picker write a new one.\n"+
			"\tRefusing to start rather than treat your host list as empty, because the next save "+
			"would then overwrite it", path, err)
	}
	return kept, nil
}

// hostsFrom is the whole fleet: what hosts.toml enabled, plus what --host added. It is
// what a caller that needs only the fleet asks for — `status` — while the dashboard
// keeps the entries too, because the picker merges into them.
func hostsFrom(path string, extra []hub.Host) ([]hub.Host, error) {
	kept, err := loadKept(path)
	if err != nil {
		return nil, err
	}
	return hostsFor(kept, extra)
}

// hostsFor turns the file's entries into hosts to poll, then appends the flag's.
//
// An enabled entry becomes a host with NO socket: §5 deleted the forward, so its
// transport is the ssh master at the control path hub.ControlPathFor names — the same
// path Ensure, Stop and the startup reconcile derive, which is what lets the next run
// adopt the master this one left (3.6 ms against 1550 to spawn).
//
// LocalProc stays false, which makes such a host read-only until its master answers a
// process walk: it is not this machine, and the walk that gates every write must not
// be answered from this machine's process table.
func hostsFor(kept []hostset.Entry, extra []hub.Host) ([]hub.Host, error) {
	var out []hub.Host
	dir := ""
	for _, e := range kept {
		if !e.Enabled {
			continue
		}
		if e.Alias == hub.LocalLabel {
			return nil, fmt.Errorf("the host list enables an alias called %q, which is the label "+
				"this machine's own server already uses: two hosts under one label share a pane "+
				"namespace, so a write aimed at one can land on the other. Rename it in "+
				"~/.ssh/config, or disable it and give it another label with --host", hub.LocalLabel)
		}
		if dir == "" {
			d, err := hub.RuntimeDir()
			if err != nil {
				return nil, fmt.Errorf("host %q is reached over an ssh master, and its control "+
					"socket needs a runtime directory: %w", e.Alias, err)
			}
			dir = d
		}
		out = append(out, hub.Host{
			Label:       e.Alias,
			SSHDest:     e.Alias,
			ControlPath: hub.ControlPathFor(dir, e.Alias),
			// The socket override (docs/design.md §9). This line is the whole wire: the
			// field was parsed, written and round-trip tested, and copying it here is
			// what makes a hand-edited `tmux_args` reach tmux instead of being a value
			// the file remembers and nothing acts on.
			TmuxArgs: e.TmuxArgs,
		})
	}
	out = append(out, extra...)

	// One label, one host. The registry keys panes on the label and hostFor answers
	// with the FIRST match, so a duplicate does not merely confuse a heading: a write
	// aimed at the second host's pane %1 is addressed to the first host's server.
	seen := make(map[string]bool, len(out))
	for _, h := range out {
		if seen[h.Label] {
			return nil, fmt.Errorf("two hosts are labelled %q, so one of them is unreachable and "+
				"which one is decided by the order they were read in — rename one with --host, or "+
				"disable it in the host list", h.Label)
		}
		seen[h.Label] = true
	}
	return out, nil
}

// sshConfigPaths names the two files ParseSSHConfig reads. An unreadable path
// contributes nothing rather than failing: a machine with no ~/.ssh/config has no
// candidates, which is a first run and not an error.
func sshConfigPaths() (userPath, systemPath string) {
	const system = "/etc/ssh/ssh_config"
	home, err := os.UserHomeDir()
	if err != nil {
		return "", system
	}
	return filepath.Join(home, ".ssh", "config"), system
}

// probeArgs is the ssh argv for one probe, and the two options are not tuning.
//
// Measured on the host that set the wall time, `ssh reports-engine 'tmux -V; id -u'`
// takes 133.3 s bare against 6.1 s with them: BatchMode refuses to sit forever on a
// password prompt nobody can see, and ConnectTimeout caps a host that is powered off
// at 6 s instead of the system default's ~2 minutes. hostset.ProbeAll is concurrent,
// so the wall IS the slowest probe — those two options are the difference between the
// 7 s §16's promise rests on and 134 s.
func probeArgs(alias string, args []string) []string {
	return append([]string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=" + ProbeConnectTimeout,
		alias,
	}, args...)
}

// sshProbe is the production hostset.Runner.
//
// It runs ssh directly rather than through tmux.RawRunner, and that is a rule rather
// than a preference: the raw door exists for the ssh MASTER's own lifecycle and is
// banned everywhere else (internal/tmux/guard_test.go), because an argv assembled by
// hand and handed to it is how a tmux command bypasses Validate. A probe is neither —
// it carries no tmux argv of the hub's own making and no control path.
func sshProbe(ctx context.Context, alias string, args ...string) (string, string, int) {
	cmd := exec.CommandContext(ctx, "ssh", probeArgs(alias, args)...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()

	stderr := strings.TrimSpace(errb.String())
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		return out.String(), stderr, ee.ExitCode()
	case err != nil:
		// ssh could not be started, or the deadline killed it. Report it as ssh's own
		// failure code so hostset's reason table treats it as one; a timeout is
		// recognised before that by the probe's own context.
		return out.String(), strings.TrimSpace(stderr + " " + err.Error()), 255
	}
	return out.String(), stderr, 0
}

// masterOps are the ssh-master calls the startup makes, as one seam.
//
// They are a struct of functions so the ordering test can hand in a spawn that BLOCKS:
// the property that matters is that none of this sits on the first-paint path, and a
// test that could only assert it against real ssh would not be written.
type masterOps struct {
	ensure    func(ctx context.Context, m *hub.Master) error
	reconcile func(ctx context.Context, runtimeDir string, aliases []string) error
}

func liveMasterOps(raw tmux.RawRunner) masterOps {
	return masterOps{
		ensure: func(ctx context.Context, m *hub.Master) error { return m.Ensure(ctx, raw) },
		reconcile: func(ctx context.Context, dir string, aliases []string) error {
			// The report is dropped HERE and only here: this runs while the TUI owns the
			// screen, so a line about a leftover socket would garble a frame. The same
			// classification still applies — a stale socket is removed and is not a
			// failure — because both sweeps go through one judgement rather than two.
			_, err := hub.ReconcileMasters(ctx, raw, dir, aliases)
			return err
		},
	}
}

// startDashboard runs the startup in the order §16 requires: the screen first, ssh
// afterwards.
//
// A master spawn takes 1.55 s to become checkable and Ensure polls for up to 10 s,
// while §16 promises a usable dashboard in under 50 ms — five enabled hosts spawned
// before the program starts would miss that by thirty times, and the user would watch
// a blank terminal on every run. So every ssh call goes into its own goroutine and the
// UI is entered while they are still in flight. A host whose master is not up yet
// renders `connecting`, which §9 specifies, and its panes appear on the tick after it
// comes up.
//
// TestTheMasterSpawnIsNotOnTheFirstPaintPath asserts that order rather than trusting
// it, with a spawn that blocks until the test releases it.
func startDashboard(ctx context.Context, hosts []hub.Host, runtimeDir string, ops masterOps, runUI func() error) error {
	go func() {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); reconcileMasters(ctx, hosts, runtimeDir, ops) }()
		go func() { defer wg.Done(); ensureMasters(ctx, hosts, ops) }()
		wg.Wait()
	}()
	return runUI()
}

// ensureMasters adopts or spawns one master per host that has one, concurrently, and
// returns when they have all answered. The dashboard calls it off the paint path;
// `status` calls it in the foreground, because a report has nothing to paint.
//
// A failure is deliberately not reported here, and it has a better reporter: the host's
// own row says so within one tick and carries the command that respawns it — internal
// tmux's explainTransport writes that sentence and hub's remote classification keeps
// it. A message printed from here would arrive after the TUI owns the screen, where it
// would garble the frame and name the wrong subject.
func ensureMasters(ctx context.Context, hosts []hub.Host, ops masterOps) {
	if ops.ensure == nil {
		return
	}
	var wg sync.WaitGroup
	for _, h := range hosts {
		m := masterFor(h)
		if m == nil {
			continue
		}
		wg.Add(1)
		go func(m *hub.Master) {
			defer wg.Done()
			_ = ops.ensure(ctx, m)
		}(m)
	}
	wg.Wait()
}

// reconcileMasters stops the masters of hosts nobody enabled any more. It is the third
// of the three exits the adoption design requires (§5): the picker stops a host's
// master when it is unticked, `--stop-masters` stops every one, and this stops what is
// left over from a previous configuration.
//
// It runs ONLY when something is configured, and that is the decision this call site
// exists to make. hub.ReconcileMasters refuses an empty alias list on purpose, because
// "I have no configured hosts" and "stop everything" are different intents — and here
// they would look identical for a hub started with `--hosts` pointing at another file,
// or with only a local server. A routine startup must never sweep the operator's
// masters; `--stop-masters` is how that is asked for.
//
// A failure leaves an orphan ssh running and nothing else, which is why it is not
// reported either: `--stop-masters` is its remedy and it costs nothing until then.
func reconcileMasters(ctx context.Context, hosts []hub.Host, runtimeDir string, ops masterOps) {
	if ops.reconcile == nil || runtimeDir == "" {
		return
	}
	aliases := masterAliases(hosts)
	if len(aliases) == 0 {
		return
	}
	_ = ops.reconcile(ctx, runtimeDir, aliases)
}

// stopAllMasters is `--stop-masters`: every master under the runtime directory, whether
// this hub configured it or not.
//
// It is mandatory rather than a convenience. A master is deliberately left running when
// the hub exits — measured, `ssh -N -M` is reparented to pid 1 and survives, which is
// what makes the next start free (3.6 ms to adopt against 1550 ms to spawn) — and
// deliberately leaving processes behind requires a way to end them.
// It reports per path, and a LEFTOVER socket is a success with a line of its own.
// Measured before this said so: with one stale socket present — the residue of a master
// that was killed rather than asked to exit — the command printed
// `failed to stop 1 master(s)` and exited 1, i.e. it reported failure precisely when its
// own intent was already satisfied, naming neither the path nor anything to do about it.
// Silence would be wrong too: a leftover socket is worth knowing about, and it is now
// removed, so the operator should be told which one and that it is gone.
func stopAllMasters(raw tmux.RawRunner, out io.Writer) error {
	dir, err := hub.RuntimeDir()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), MasterTimeout)
	defer cancel()

	rep, err := hub.StopAllMasters(ctx, raw, dir)
	for _, p := range rep.Paths(hub.MasterStale) {
		fmt.Fprintf(out, "no master was listening at %s — removed the leftover socket\n", p)
	}
	if n := rep.Count(hub.MasterStopped); n > 0 {
		fmt.Fprintf(out, "stopped %d master(s)\n", n)
	}
	// A sweep that found nothing says so. "Exit 0 and print nothing" reads the same as
	// "did not run", which is the shape this repo has shipped before.
	if len(rep.Events) == 0 {
		fmt.Fprintf(out, "no masters under %s\n", dir)
	}
	return err
}

// masterFor is the ssh master of a host, or nil for a host that has none: the local
// server, and a `--host` entry that gave a socket but no ssh coordinates.
//
// The alias is the ssh DESTINATION rather than the label, because that is what ssh is
// handed and what hub.ControlPathFor hashes. For a host out of hosts.toml they are the
// same string; for a `--host` entry they need not be.
func masterFor(h hub.Host) *hub.Master {
	if h.SSHDest == "" || h.ControlPath == "" {
		return nil
	}
	return &hub.Master{Alias: h.SSHDest, ControlPath: h.ControlPath}
}

// masterAliases is the set the orphan sweep compares against: every alias this hub may
// legitimately hold a master for.
func masterAliases(hosts []hub.Host) []string {
	var out []string
	for _, h := range hosts {
		if m := masterFor(h); m != nil {
			out = append(out, m.Alias)
		}
	}
	return out
}

func runStatus(hosts []hub.Host, local bool, ops masterOps) error {
	ctx, cancel := context.WithTimeout(context.Background(), StatusTimeout)
	defer cancel()

	// The masters are ensured BEFORE the poll, and in the foreground: a one-shot report
	// has no first-paint commitment, and without a master every remote host would report
	// the same missing one — a report about the hub's own startup rather than about the
	// fleet. Adoption makes the common case free.
	//
	// The orphan sweep is deliberately NOT run here. `status` is a read, and its host
	// list is whatever this invocation was given: sweeping from it would let
	// `tmux-hub status --host one=...` end the masters of the dashboard running beside it.
	ensureMasters(ctx, hosts, ops)

	reg := registry.New()
	p := hub.NewPoller(tmux.NewExec(TmuxTimeout), reg)
	if local {
		p.AddLocal()
	}
	for _, h := range hosts {
		p.Add(h)
	}

	// First tick discovers the panes; the second asks for a full capture of each
	// so the report carries content. Two round trips is free for a one-shot
	// command, and a monitor wants the content.
	p.Tick(ctx, time.Now(), nil)
	want := map[string]bool{}
	for _, pn := range reg.Panes() {
		want[pn.Host+"\x00"+pn.PaneID] = true
	}
	polled := p.Tick(ctx, time.Now(), want)
	// Claude's own listing too: most of the sessions it reports have no tmux pane,
	// so a report without it is incomplete by construction (docs/design.md §17).
	p.TickAgents(ctx, time.Now())
	rep := hub.BuildReport(polled, reg.Panes())

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

// splitSubcommand consumes a leading or trailing `status` subcommand, returning
// the arguments that should reach flag parsing and whether it was present.
//
// It exists because Go's flag package stops parsing at the first non-flag
// argument: with the subcommand read from flag.Arg(0) AFTER flag.Parse(), every
// flag written after `status` was silently dropped. Both orders must mean the
// same thing, because both are natural to type.
func splitSubcommand(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	found := false
	for _, a := range args {
		if a == "status" && !found {
			found = true
			continue
		}
		out = append(out, a)
	}
	return out, found
}
