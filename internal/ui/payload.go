package ui

import (
	"fmt"
	"strings"
)

// LoginPayload is the ONE argument a tmux create hands to `$SHELL -c` when the thing to run is the
// OPERATOR'S OWN PROGRAM on the row's host: the argv under a LOGIN shell, inside a wrapper that
// outlives a failing payload.
//
// It exists because a pane created on a remote host could not find `claude` at all, and the hub
// reported that as `invalid window id: ""` — a sentence about tmux, three layers away from the cause.
// Measured on dev-air (Darwin arm64, tmux 3.7b, login shell `/opt/homebrew/bin/nu`) and nuc:
//
//   - A PANE INHERITS THE TMUX CLIENT'S ENVIRONMENT, and the hub's client is `ssh <host> tmux …`.
//     So a remote pane's PATH is whatever the far login shell gives a NON-INTERACTIVE command, which
//     on dev-air is `/opt/homebrew/bin:…:/usr/bin:/bin` — and `claude` lives in
//     `/Users/temporary/.local/bin`, which only a LOGIN shell puts on the path. The payload therefore
//     failed with the shell's own "command not found", nothing held the pane, tmux destroyed pane →
//     window → session, and `display -p -t <gone pane> '#{window_id}'` answers **rc=0 with an empty
//     string** — hence the empty window id rather than an error naming the pane.
//   - `-l` is what fixes it, and it has to be `-l` rather than an absolute path or a PATH= prefix:
//     the hub does not know where the operator keeps their tools on a machine it has never run on,
//     and the login shell is the one thing that does.
//   - THE STRING CARRIES NO `'`, `$`, backtick or backslash, and that is a requirement rather than a
//     style. `internal/tmux`'s transport quotes each argument POSIX-style, so an embedded `'` becomes
//     close-quote, backslash-quote, reopen-quote — legal POSIX and a PARSE ERROR in nushell, which closes its string at the bare `'` and
//     reads the rest as nu code. Measured: the old payload shape answered
//     `Error: nu::parser::parse_mismatch` at **rc=0**, so the hub saw a successful create with no
//     pane. `payload_test.go` asserts both poles of that.
//
// The wrapper is §20's rule, one measured fact behind it: a `set -w remain-on-exit on` that FOLLOWS
// the create can only win on time (a payload of `false` lost 6 of 12 trials on 3.7b), and the
// failures worth showing are the fast ones. A shell that stays in the foreground after the payload
// returns cannot lose that race. Success closes the pane silently — a keypress on the commonest case
// is ceremony — while a failure keeps the far shell's own sentence on screen and waits.
//
// It is `sh -lc` and not `$SHELL`: only the QUOTING has to survive the operator's default-shell, and
// `||`/`{ … }`/`read` must not be handed to a shell whose grammar is somebody else's. Measured, `sh`
// exists and is POSIX on both hosts; nu happily execs it.
//
// The cost, checked before it was accepted: `#{pane_current_command}` reads the wrapper shell (`sh`
// on nuc, `nu` on dev-air) instead of `claude`. No product code keys on that field being `claude` —
// `registry.Pane.IsConversation` answers from `Kind` and `ClaudeSession` — and the door's panes have
// carried an `sh` wrapper since §20, which `registry.attachedSessionID` already parses.
func LoginPayload(argv []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("a pane needs a command to run, and this one is empty")
	}
	for i, a := range argv {
		if a == "" {
			return "", fmt.Errorf("argument %d of the command is empty, and an empty word vanishes "+
				"when the payload is joined — every argument after it would shift one place left", i+1)
		}
		if bad, ok := unquotableRune(a); ok {
			return "", fmt.Errorf("argument %d of the command (%q) holds %q, which no login shell "+
				"can be given without quoting — pass a plain word, or run it from a wrapper script "+
				"whose name is one", i+1, a, string(bad))
		}
	}
	return `sh -lc "` + strings.Join(argv, " ") + ` || { echo; echo ` + payloadFailureLine +
		`; read x; }"`, nil
}

// payloadFailureLine is what a failed payload leaves on the pane, above the shell's own message.
//
// Unquoted on purpose: it goes inside the payload's own double quotes, so it may hold no `'`, `$`,
// backtick or backslash either — and it is the one operator-facing string in this file, which is why
// the payload is built here rather than in `internal/tmux` (that package puts words on no screen).
const payloadFailureLine = "tmux-hub: the command above failed — press enter to close this pane"

// unquotableRune is the guard behind LoginPayload: the first rune of s that the payload cannot carry,
// and whether there was one. An EMPTY s has no such rune and is refused by the caller, which is the
// only place that can say which argument it was.
//
// A WHITELIST, because the question is not "which characters are dangerous" but "which characters
// need no quoting anywhere", and the second list is short and cannot grow by accident. It admits
// exactly what the three callers pass — a program name, `--flags`, a uuid, a short id, a model name,
// a permission mode — and refuses at DECLARATION time what would otherwise fail on a remote machine
// at three in the morning.
//
// `%` is refused with everything else and deliberately: `internal/tmux`'s seam refuses a literal `%`
// outside a `-t` value, so a payload carrying one is rejected by the transport with a message about
// pane ids. Refusing it here names the argument instead.
func unquotableRune(s string) (rune, bool) {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '.' || r == ':' || r == '/' || r == '@' || r == '=' ||
			r == '+' || r == ',' || r == '-':
		default:
			return r, true
		}
	}
	return 0, false
}
