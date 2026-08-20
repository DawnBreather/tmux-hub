package tmux

import (
	"os/exec"
	"strings"
	"testing"
)

// The property, asserted through a REAL shell rather than by comparing to a
// hand-written expectation: whatever a shell does to the joined string, it must
// split back into the argv element for element. `printf '[%s] '` reuses its format
// once per argument, so the output names the argv the shell actually built.
func TestShellJoinSurvivesARealShell(t *testing.T) {
	for _, argv := range [][]string{
		{"tmux", "attach", "-t", "$0"},
		{"tmux", "attach", "-t", "$10"},
		{"tmux", "send-keys", "-t", "%3", "-l", "it's a $HOME; rm -rf *"},
		{"ssh", "-S", "/home/w/.ssh/cm-%h-%p-%r", "-t", "nuc", "tmux", "attach", "-t", "$3"},
		{"tmux", "display", "-p", "-F", "#{pane_id}|#{window_activity}"},
		{"a b", "", "~", "&", ";", "back\\slash", "new\nline", "$(id)", "`id`"},
	} {
		out, err := exec.Command("sh", "-c", `printf '[%s] ' `+ShellJoin(argv)).CombinedOutput()
		if err != nil {
			t.Fatalf("sh: %v (%s)", err, out)
		}
		var want strings.Builder
		for _, a := range argv {
			want.WriteString("[" + a + "] ")
		}
		if string(out) != want.String() {
			t.Errorf("argv %q\n  shell built %q\n  want        %q", argv, out, want.String())
		}
	}
}

// Two levels, which is the remote path: tmux hands one argument to `$SHELL -c`
// here, and ssh hands its command to a login shell there. A rule that survives one
// pass and not two is the defect this project shipped twice.
func TestShellJoinSurvivesTwoShells(t *testing.T) {
	argv := []string{"tmux", "attach", "-t", "$0"}
	inner := ShellJoin(argv)
	outer := ShellJoin([]string{"sh", "-c", `printf '[%s] ' ` + inner})
	out, err := exec.Command("sh", "-c", outer).CombinedOutput()
	if err != nil {
		t.Fatalf("sh: %v (%s)", err, out)
	}
	if got, want := string(out), "[tmux] [attach] [-t] [$0] "; got != want {
		t.Errorf("through two shells got %q, want %q", got, want)
	}
}
