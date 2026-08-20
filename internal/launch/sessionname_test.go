package launch

import "testing"

// A launch into "a new session" has to produce a session the operator can FIND.
//
// `SessionName` was reserved for a later task and never filled, so the hub ran
// `tmux new-session -d -s ""` — measured on both halves of the fleet (3.7b and 3.2a), that succeeds
// and creates a session whose name is the empty string. The row then draws as `nuc/%0 %0`: the pane
// id twice, no human-readable name anywhere, and `tmux attach -t <name>` has nothing to take.
//
// The name is the cwd's last segment, which is the same vocabulary the project grouping already uses,
// so a session lands under the name the operator already thinks of the work by.
//
// `.` and `:` are REPLACED, and that is measured rather than defensive: tmux accepts them in a name
// but its target syntax uses them for windows and panes, so `has-session -t my.app` answers
// `can't find pane: app` and `-t a:b` answers `can't find window: b`. A directory called `my.app` is
// ordinary, and a session nobody can address by name is the defect this function exists to avoid.
func TestSessionNameForIsFindableAndAddressable(t *testing.T) {
	for _, c := range []struct{ cwd, want, why string }{
		{"/home/dev/lab/streams/st", "st", "the last segment, which is what the project list shows"},
		{"/w/my.app", "my-app", "a dot makes tmux read the tail as a PANE: `can't find pane: app`"},
		{"/w/a:b", "a-b", "a colon makes tmux read the tail as a WINDOW: `can't find window: b`"},
		{"/w/dir with spaces", "dir with spaces", "a space is addressable, measured, so it stays"},
		{"/w/слово", "слово", "non-ASCII is stored and addressable verbatim, measured"},
		{"/", "tmux-hub", "the root has no segment to name a session by"},
		{"", "tmux-hub", "no directory at all still needs a name"},
		{"/w/trailing/", "trailing", "a trailing separator is not a segment"},
	} {
		if got := SessionNameFor(c.cwd); got != c.want {
			t.Errorf("SessionNameFor(%q) = %q, want %q — %s", c.cwd, got, c.want, c.why)
		}
	}
}

// A newline is the one thing tmux REFUSES outright (`invalid session name: a\nb`), and a directory
// can legally contain one, so it cannot reach the command.
func TestSessionNameForRefusesWhatTmuxRefuses(t *testing.T) {
	got := SessionNameFor("/w/a\nb")
	if got == "a\nb" {
		t.Error("a newline reached the session name; tmux answers `invalid session name`")
	}
	if got != "a-b" {
		t.Errorf("SessionNameFor with a newline = %q, want %q", got, "a-b")
	}
}
