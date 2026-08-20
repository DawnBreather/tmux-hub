package hide

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/registry"
)

// §18's promise is "never show me this again", not "not right now". The key held the
// window's NAME, and tmux ships `automatic-rename on`, so the name follows whatever is
// running: measured on a private socket, one window went `zsh` → `sleep` → `tail` across
// three commands while `window_index` stayed 0 and `window_id` stayed `@0`. So `x` on a
// shell pane held only until the operator ran something in it.
//
// §18's approved key is `session:window.pane` — INDICES — so the name was a drift from
// the design rather than the design.
func TestAHiddenPaneStaysHiddenWhenItsWindowIsRenamed(t *testing.T) {
	before := registry.Pane{Host: "local", Session: "work", Window: "zsh",
		WindowIndex: 0, Index: 1, StartCommand: ""}
	// The same pane, one command later. Only the NAME moved.
	after := before
	after.Window = "tail"

	if KeyOf(before) != KeyOf(after) {
		t.Errorf("a rename changed the key:\n before %+v\n after  %+v\n"+
			"so `x` un-hides itself as soon as the operator runs something",
			KeyOf(before), KeyOf(after))
	}
	// And the key must still tell two different WINDOWS apart, or hiding one pane
	// would hide its counterpart in every window of the session.
	other := before
	other.WindowIndex = 1
	if KeyOf(before) == KeyOf(other) {
		t.Errorf("panes in window 0 and window 1 share a key: %+v", KeyOf(before))
	}
}

// The persisted file changes shape, and the dangerous direction is not losing marks —
// it is GAINING them. An old record has no window index, so it would unmarshal with
// WindowIndex 0, and 0 is a REAL window: the first run after the upgrade could hide a
// pane the operator never hid. So a file that is not this version is refused entirely,
// which shows everything and says why — the path Open already has for a malformed file.
func TestAFileFromBeforeTheKeyChangedHidesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hidden.json")
	// Exactly what the previous version wrote: a bare array, window as a NAME.
	old := `[
  {
    "host": "local",
    "session": "work",
    "window": "zsh",
    "index": 1,
    "start": ""
  }
]`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := s.Count(); got != 0 {
		t.Errorf("Count = %d, want 0 — an old record must not become a new one, because "+
			"its missing window index would read as window 0 and hide a pane nobody hid", got)
	}
	// The MESSAGE, not merely its presence. A non-empty check passed while the file took
	// the pre-existing "malformed" branch and told the operator their file was corrupt,
	// quoting a Go type name — so the assertion measured the wrong branch and could not
	// see it. What the operator needs is the remedy.
	w := s.Warning()
	if w == "" {
		t.Fatal("Warning is empty — an upgrade that silently un-hides everything is a " +
			"surprise, and the operator has to be told to hide them again")
	}
	for _, want := range []string{"hide them again", "window"} {
		if !strings.Contains(w, want) {
			t.Errorf("Warning = %q, want it to mention %q", w, want)
		}
	}
	if strings.Contains(w, "cannot unmarshal") || strings.Contains(w, "hide.file") {
		t.Errorf("Warning = %q — that is the malformed-file branch quoting a Go type, "+
			"not the migration branch. A v1 file is a bare ARRAY, so it cannot unmarshal "+
			"into the envelope at all and the version check is never reached", w)
	}
	// The specific pane the old record named must be visible.
	p := registry.Pane{Host: "local", Session: "work", Window: "zsh", WindowIndex: 0, Index: 1}
	if s.Hidden(p) {
		t.Error("the pane from the old record is still hidden, so the file was half-read")
	}
}

// What this version writes must be what this version reads. A round trip is the only
// thing that proves the envelope and the parser agree.
func TestTheSetRoundTripsThroughItsOwnFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hidden.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := registry.Pane{Host: "nuc", Session: "work", Window: "tail", WindowIndex: 2,
		Index: 1, StartCommand: `sh -c "tail -f log | grep boom"`}
	if err := s.Toggle(p); err != nil {
		t.Fatalf("Toggle: %v", err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Warning() != "" {
		t.Errorf("reopening the file this version just wrote produced a warning: %q",
			again.Warning())
	}
	if !again.Marked(KeyOf(p)) {
		raw, _ := os.ReadFile(path)
		t.Errorf("the mark did not survive its own file; the file holds:\n%s", raw)
	}
	// A rename must not lose it across the restart either — the whole point.
	renamed := p
	renamed.Window = "htop"
	if !again.Marked(KeyOf(renamed)) {
		t.Error("the mark did not survive a rename ACROSS a restart")
	}
	if !strings.Contains(string(mustRead(t, path)), `"v"`) {
		t.Error("the file carries no version, so the next key change cannot be detected")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
