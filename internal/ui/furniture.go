package ui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
)

// The hub's OWN windows are not part of the fleet, and until now they were rows in it.
//
// Reported as "что за LOCAL 0, LOCAL 15" — two sessions the operator did not recognise on their own
// screen. Measured on their server, and the answer is the hub itself:
//
//	session 0  | 2 windows | window 0 `tmux-hub` %0   window 1 `nuc/tmux-hub-demo` %24
//	session 15 | 3 windows | window 0 `tmux-hub` %21  window 1 `nuc/20260818--ansible-ci-ops…` %22
//	                                                  window 2 `nuc/envoy-ops` %23
//
// So five of the sixty rows were the hub watching itself and the doors it had opened, under headers
// named `0` and `15` because a bare `tmux` numbers its sessions. The `sh` rows are the worse half:
// an attach window is a VIEW of a session that is already a row of this same list, so those were the
// only true duplicates on the screen — and they are the ones a person cannot recognise, because the
// row shows the `sh` that wraps the ssh and not what it is looking at.
//
// Both keys are derived from the PANE and not from the process that made it, which is the rule a
// hub-created link has to obey — a link the hub remembers is a link that works for one run:
//
//   - the hub is a pane whose current command is this program's own name. Measured: those panes
//     report `tmux-hub` as `#{pane_current_command}` and an EMPTY `#{pane_start_command}`, because
//     the operator typed the name into a shell, so the start command cannot be the key.
//   - an attach window is one the hub NAMED, and §22's dedup already rests on that name being the
//     mark (a marker option written after `new-window` loses a race against its own payload, so the
//     name is the only mark that always exists). attachWindowName is the same function the dedup
//     calls, so the two cannot drift.
//
// The attach rule also demands the window share a tmux session with a hub pane, which is where the
// hub puts them by construction, AND that the pane say of itself that it is an attach.
//
// The third clause is what an adversarial review earned. With only the first two, this fleet loses a
// row: `nuc/api` is a real pane on nuc, so the hub WOULD name an attach window for it `nuc/api` — and
// an operator who has their own window called `nuc/api` inside the session the hub runs in loses an
// actionable Claude session, silently. A window name proves only that the hub COULD have chosen it.
//
// The reviewer's remedy — stamp created windows with a per-instance option — is the one this design
// has already measured and rejected: a command sent after `new-window` loses a race against its own
// payload (`false` survived 6 of 12 trials, §22), which is why the NAME is the mark at all. What the
// pane can be asked instead is what it was STARTED with, and that is decisive here: `pathWindow` is
// the only possession path that creates a window and it is defined as "the target on another server:
// today's ssh attach", so every window the hub makes runs `ssh … tmux attach` and nothing else does.
// If that command's shape ever changes this clause stops matching and the windows come BACK as rows,
// which is the safe direction — a row the operator cannot act on is noise, a row that vanishes is a
// session they think is gone.
//
// What remains unclosed, deliberately: an operator running a DIFFERENT program called `tmux-hub`
// loses that pane's row. There is no signal that separates two programs of one name, the cost is one
// curious row against `tmux ls`, and the header says how many rows are not listed. The false negative
// the reviewer paired it with does NOT exist — measured on both fleet versions (tmux 3.7b and 3.2a),
// `#{pane_current_command}` for a binary named `tmux-hub-enterprise-edition` reports all 27
// characters; the 15-character truncation is the KERNEL's `comm`, which tmux does not read.
func ownFurniture(panes []registry.Pane, aliases project.Aliases, self string) map[string]bool {
	own := map[string]bool{}
	if self == "" {
		// No name to compare against — every row stays, which is the safe direction: a row the
		// operator cannot act on is noise, and a row that vanishes is a session they think is gone.
		return own
	}
	hubSessions := map[[2]string]bool{}
	for _, p := range panes {
		if p.Kind == registry.KindPane && p.Command == self {
			own[MarkKey(p)] = true
			hubSessions[[2]string{p.Host, p.Session}] = true
		}
	}
	if len(hubSessions) == 0 {
		return own
	}
	// Every name the hub WOULD give an attach, built from the fleet itself rather than remembered.
	names := map[string]bool{}
	for _, p := range panes {
		if n := attachWindowName(p.Host, p, aliases); n != "" {
			names[n] = true
		}
	}
	for _, p := range panes {
		if p.Kind != registry.KindPane || own[MarkKey(p)] {
			continue
		}
		if hubSessions[[2]string{p.Host, p.Session}] && names[p.Window] &&
			looksLikeAnAttach(p.StartCommand) {
			own[MarkKey(p)] = true
		}
	}
	return own
}

// looksLikeAnAttach reports whether a pane was started by the command `a` builds for another server.
//
// The words are read as FIELDS after tmux's own quoting is stripped, because tmux word-quotes this
// value and the two words are therefore never adjacent in the raw string — a grep for `tmux attach`
// over it returns zero on a fleet with eight such panes, which this repo has already paid for once.
// The unquoting is the same three-character strip registry.attachedSessionID uses on the same field.
//
// `tmux` must come BEFORE `attach`: `ssh -S … -t nuc tmux attach -t '$0'` is the shape, and requiring
// the order keeps a pane whose PATH happens to contain the word `attach` from qualifying.
func looksLikeAnAttach(start string) bool {
	if start == "" {
		return false
	}
	bare := strings.NewReplacer("'", "", `"`, "", `\`, "").Replace(start)
	seenTmux := false
	for _, w := range strings.Fields(bare) {
		switch w {
		case "tmux":
			seenTmux = true
		case "attach":
			if seenTmux {
				return true
			}
		}
	}
	return false
}

// operatorHome is the home directory a pinned row's path is folded against, read once at startup.
//
// An empty answer is fine and means no folding: favouriteRow only folds a LEADING match, so a row
// whose path it cannot shorten simply shows the whole path. Reading it HERE rather than in the
// renderer keeps that function pure, which is what lets docs/ui-mockup.html be byte-reproducible.
func operatorHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// selfCommand is the name tmux will report for a pane running this program.
//
// `os.Args[0]` and not `os.Executable()`: the executable path resolves symlinks, and tmux reports
// the process name, so a hub reached through a shim would compare a resolved path against a comm
// name and never match. Under `go test` this is the test binary's name, which is why every test
// passes the name it means explicitly.
func selfCommand() string {
	if len(os.Args) == 0 {
		return ""
	}
	return filepath.Base(os.Args[0])
}
