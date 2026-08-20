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
