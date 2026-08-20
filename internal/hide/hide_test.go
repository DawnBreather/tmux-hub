package hide

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

func pane(host, session, window string, index int, start string, st state.State) registry.Pane {
	return registry.Pane{
		Kind:            registry.KindPane,
		Host:            host,
		Session:         session,
		Window:          window,
		Index:           index,
		StartCommand:    start,
		ClassifiedState: st,
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestMarkedSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hidden.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := pane("local", "logs", "tail", 0, `"tail -f app.log"`, state.Works)
	if err := s.Toggle(p); err != nil {
		t.Fatal(err)
	}

	// Reopened from disk, with no help from the first Set.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !s2.Marked(KeyOf(p)) {
		t.Fatal("the mark did not survive a reopen")
	}
}

func TestABlockedPaneIsShownEvenWhenMarked(t *testing.T) {
	// The resurface rule, and it is load-bearing: it is what makes a wrong key
	// match unable to lose work (docs/design.md §18). Removing it must go red.
	s := must(Open(filepath.Join(t.TempDir(), "h.json")))
	p := pane("local", "logs", "tail", 0, `"tail -f app.log"`, state.Works)
	if err := s.Toggle(p); err != nil {
		t.Fatal(err)
	}
	if !s.Hidden(p) {
		t.Fatal("a marked working pane should be hidden")
	}

	blocked := p
	blocked.ClassifiedState = state.Needs
	if s.Hidden(blocked) {
		t.Fatal("a marked pane WAITING ON THE USER must be shown")
	}
	if !s.Marked(KeyOf(blocked)) {
		t.Fatal("resurfacing must not clear the mark — it is temporary, not an unhide")
	}
}

func TestAMarkDoesNotMatchADifferentStartCommand(t *testing.T) {
	// The corroborator earning its place: same host, session, window and index,
	// different start command, therefore a different pane.
	s := must(Open(filepath.Join(t.TempDir(), "h.json")))
	noise := pane("local", "logs", "tail", 0, `"tail -f app.log"`, state.Works)
	if err := s.Toggle(noise); err != nil {
		t.Fatal(err)
	}

	agent := noise
	agent.StartCommand = `"claude --session-id 7007b23f"`
	if s.Hidden(agent) {
		t.Fatal("a pane that only shares the PATH must not inherit the mark")
	}
}

func TestAMalformedFileYieldsAnEmptySetNotAnError(t *testing.T) {
	// Fail toward visible: an unreadable set shows everything, which is
	// annoying. Refusing to start, or guessing, is worse.
	path := filepath.Join(t.TempDir(), "hidden.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a malformed file must not fail: %v", err)
	}
	if s.Count() != 0 {
		t.Fatalf("Count = %d, want 0", s.Count())
	}
	if s.Warning() == "" {
		t.Fatal("a dropped set must say so — a silent empty set is indistinguishable from an empty one")
	}
}

func TestToggleTwiceUnhides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hidden.json")
	s := must(Open(path))
	p := pane("local", "logs", "tail", 0, `"tail -f app.log"`, state.Works)
	if err := s.Toggle(p); err != nil {
		t.Fatal(err)
	}
	if err := s.Toggle(p); err != nil {
		t.Fatal(err)
	}
	if s.Marked(KeyOf(p)) {
		t.Fatal("the second toggle must unhide")
	}
	s2 := must(Open(path))
	if s2.Marked(KeyOf(p)) {
		t.Fatal("the unhide must persist too")
	}
}
