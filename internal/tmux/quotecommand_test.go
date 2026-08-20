package tmux

import "testing"

// A QUOTED PROGRAM NAME IS LEGAL POSIX AND A PARSE ERROR IN A SHELL THAT IS NOT, so the seam quotes
// arguments and leaves the name bare.
//
// This has a live measurement behind it. A new host on the fleet was a MacBook whose login shell is
// Nushell; ssh hands a remote command to the LOGIN shell, and the payload the seam used to build,
// `'tmux' 'list-panes' '-a' '-F' '#{pane_id}'`, came back as
// `Error: nu::parser::parse_mismatch` — at rc=0, so the hub read a poll that had succeeded and found
// no panes, and the host sat at `connecting` for as long as it was enabled. With the name bare the
// same shell runs it and tmux answers `no server running on /private/tmp/tmux-501/default`, which the
// status taxonomy already turns into "reachable, but no tmux server is running there".
func TestShellJoinCommandLeavesTheProgramNameBare(t *testing.T) {
	for _, c := range []struct {
		name string
		prog string
		args []string
		want string
	}{
		{
			name: "the poll's own command line",
			prog: "tmux",
			args: []string{"list-panes", "-a", "-F", "#{pane_id}"},
			want: `tmux 'list-panes' '-a' '-F' '#{pane_id}'`,
		},
		{
			// The arguments are where the expansion risk lives, and they stay quoted: an unquoted
			// `#{pane_id}` is globbed and an unquoted `$0` is expanded by the far shell.
			name: "a format and a session id still survive quoting",
			prog: "tmux",
			args: []string{"display", "-p", "-t", "$0", "#{session_name}"},
			want: `tmux 'display' '-p' '-t' '$0' '#{session_name}'`,
		},
		{
			name: "an absolute path is a bare word too",
			prog: "/usr/local/bin/tmux",
			args: []string{"-V"},
			want: `/usr/local/bin/tmux '-V'`,
		},
		{
			// The other direction, and the reason the predicate is narrow rather than permissive:
			// passing this through bare would be an injection, while quoting it merely fails on a
			// shell that cannot parse a quoted command — of the two wrong answers, only one is
			// dangerous.
			name: "a name that is not a bare word is quoted anyway",
			prog: "tmux; rm -rf /",
			args: []string{"-V"},
			want: `'tmux; rm -rf /' '-V'`,
		},
		{
			name: "no arguments leaves no trailing space",
			prog: "tmux",
			args: nil,
			want: `tmux`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := ShellJoinCommand(c.prog, c.args); got != c.want {
				t.Errorf("ShellJoinCommand(%q, %q)\n got %q\nwant %q", c.prog, c.args, got, c.want)
			}
		})
	}
}

// bareWord decides which of the two directions above a name takes, so its edges are worth pinning
// rather than inferring from the joins.
func TestBareWord(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"tmux", true},
		{"/usr/bin/tmux", true},
		{"tmux-hub", true},
		{"tmux_2.x", true},
		{"", false},          // nothing is not a command
		{"tmux ", false},     // a trailing space would split into two words
		{"$TMUX", false},     // expansion
		{"tm*x", false},      // glob
		{"tmux;ls", false},   // a second command
		{"tmux'", false},     // an unbalanced quote
		{"echo`id`", false},  // substitution
		{"a&&b", false},      // control operator
		{"кириллица", false}, // outside the set on purpose: not a name this program chooses
	} {
		if got := bareWord(c.in); got != c.want {
			t.Errorf("bareWord(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
