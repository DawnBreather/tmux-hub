package tmux

import "strings"

// ShellQuote makes one argument survive a shell verbatim.
//
// Single quotes because they suspend every expansion a shell would otherwise do —
// `$`, whitespace, `*`, `~`, `;`, `&`, backticks. An embedded single quote is the
// only character they cannot carry, so it is closed, escaped and reopened.
//
// It lives in this package rather than in the caller because two consumers need the
// SAME rule for different reasons: the seam wraps a validated tmux argv into an ssh
// command line (§5), and the attach path quotes a session id so the REMOTE login
// shell does not expand it (§20). Both were shipped wrong once, in opposite halves,
// and two copies of a quoting rule is how one of them drifts.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ShellJoin turns an argv into one string a shell re-splits into exactly that argv.
func ShellJoin(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = ShellQuote(a)
	}
	return strings.Join(parts, " ")
}

// ShellJoinCommand is ShellJoin for a command LINE: the program name is left bare.
//
// A QUOTED PROGRAM NAME IS LEGAL POSIX AND IS REFUSED BY A SHELL THAT IS NOT POSIX, and that made a
// whole host invisible. Measured on a live macOS host whose login shell is Nushell: the payload this
// seam used to build,
//
//	'tmux' 'list-panes' '-a' '-F' '#{pane_id}'
//
// answers `Error: nu::parser::parse_mismatch` — a quoted string is a string there, not a command —
// and it answers at rc=0, so the hub read a successful poll containing no panes. With the name bare,
// the same shell runs it and tmux answers `no server running on /private/tmp/tmux-501/default`, which
// is a sentence the status taxonomy already knows what to do with.
//
// Leaving it bare costs nothing: the name is a literal this program chooses (`tmux`), never operator
// data, so there is no expansion or glob in it to suspend. The ARGUMENTS still go through ShellQuote,
// which is where that risk actually lives.
//
// A name that is not a bare word is quoted anyway. That direction merely fails on a non-POSIX shell;
// passing an unquoted name with a space or a `$` in it through would be an injection, and of the two
// wrong answers only one of them is dangerous.
func ShellJoinCommand(name string, args []string) string {
	quoted := ShellJoin(args)
	if !bareWord(name) {
		name = ShellQuote(name)
	}
	if quoted == "" {
		return name
	}
	return name + " " + quoted
}

// bareWord reports whether a string needs no quoting to survive any shell as a command name.
//
// The set is deliberately narrow — letters, digits and the four characters a program path can hold —
// because the question is not "what does POSIX allow" but "what is safe unquoted in a shell whose
// grammar we do not know".
func bareWord(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.', r == '/':
		default:
			return false
		}
	}
	return true
}
