package statedir

import "testing"

func TestPathHonoursXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg")
	if got, want := Path("hidden.json"), "/xdg/tmux-hub/hidden.json"; got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestPathFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/u")
	if got, want := Path("history.jsonl"), "/home/u/.local/state/tmux-hub/history.jsonl"; got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestPathReturnsBareNameWhenHomeIsUnreadable(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	// With no XDG_STATE_HOME and no HOME, os.UserHomeDir() fails and the
	// function returns the bare name. Consequence if this ever breaks: state
	// files land in the process's cwd instead of the state dir.
	if got, want := Path("hidden.json"), "hidden.json"; got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}
