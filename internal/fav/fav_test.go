package fav

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/registry"
)

// paneRow is a pane the hub knows no Claude session id for — a shell, a `cat`, an agent a walk found
// and never adopted. That is the population keyed on (kind, host, name).
func paneRow(host, session string) registry.Pane {
	return registry.Pane{Kind: registry.KindPane, Host: host, Session: session, PaneID: "%1"}
}

// agentRow is a pane-less listing row. Every one of them carries a uuid — agents.Parse skips a record
// without a sessionId, so there is no such row — which is what makes the uuid usable as the key.
func agentRow(host, session string) registry.Pane {
	return registry.Pane{Kind: registry.KindAgent, Host: host, Session: session,
		PaneID: "agent:30f3382b@ee42d26c", AgentID: "30f3382b",
		SessionID: "30f3382b-f68c-4baf-98fd-68d4fd1c3da4"}
}

// absorbedRow is that same session AFTER it gains a pane: a pane row carrying the uuid in
// ClaudeSession, and named after the tmux session the door created rather than after the Claude
// session. Three of the four fields the v1 key used have changed.
func absorbedRow(host, claudeName string) registry.Pane {
	return registry.Pane{Kind: registry.KindPane, Host: host, PaneID: "%9",
		Session: claudeName + "-30f3382b", ClaudeSession: "30f3382b-f68c-4baf-98fd-68d4fd1c3da4"}
}

func openIn(t *testing.T, dir string) *Set {
	t.Helper()
	s, err := Open(filepath.Join(dir, "favourites.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// A favourite SURVIVES the process, because a preference the operator has to re-state on every start
// is not a preference the tool keeps.
func TestAFavouriteSurvivesAReopen(t *testing.T) {
	dir := t.TempDir()
	s := openIn(t, dir)
	if err := s.ToggleSession(paneRow("local", "api")); err != nil {
		t.Fatal(err)
	}
	if err := s.ToggleProject("d:local\x00billing-iac"); err != nil {
		t.Fatal(err)
	}

	again := openIn(t, dir)
	if !again.HasSession(paneRow("local", "api")) {
		t.Error("the favourite session did not survive")
	}
	if !again.HasProject("d:local\x00billing-iac") {
		t.Error("the favourite project did not survive")
	}
	if sess, proj := again.Count(); sess != 1 || proj != 1 {
		t.Errorf("Count = %d sessions, %d projects", sess, proj)
	}
	if w := again.Warning(); w != "" {
		t.Errorf("a set that round-tripped warns: %q", w)
	}
}

// The same key toggles OFF, and the file says so — an unmark that only lived in memory would come
// back on the next start.
func TestASecondPressUnmarksAndPersists(t *testing.T) {
	dir := t.TempDir()
	s := openIn(t, dir)
	row := paneRow("local", "api")
	for i := 0; i < 2; i++ {
		if err := s.ToggleSession(row); err != nil {
			t.Fatal(err)
		}
	}
	if s.HasSession(row) {
		t.Error("two presses left it marked")
	}
	if openIn(t, dir).HasSession(row) {
		t.Error("the unmark did not reach the file")
	}
}

// A pane-less AGENT row and a tmux session of the same name on the same host are DIFFERENT things,
// and one press must not mark both. This is the defect hide.Key paid for: with the kind out of the
// key, the two degenerate to (host, name).
func TestAnAgentRowAndAPaneOfTheSameNameAreNotOneFavourite(t *testing.T) {
	s := openIn(t, t.TempDir())
	if err := s.ToggleSession(agentRow("local", "cicd")); err != nil {
		t.Fatal(err)
	}
	if !s.HasSession(agentRow("local", "cicd")) {
		t.Fatal("the agent row is not marked")
	}
	if s.HasSession(paneRow("local", "cicd")) {
		t.Error("marking the agent row also marked a tmux session of the same name")
	}
}

// The same name on two hosts is two sessions, measured on the real fleet for the project key and true
// here for the same reason.
func TestTheSameNameOnTwoHostsIsTwoFavourites(t *testing.T) {
	s := openIn(t, t.TempDir())
	if err := s.ToggleSession(paneRow("local", "api")); err != nil {
		t.Fatal(err)
	}
	if s.HasSession(paneRow("nuc", "api")) {
		t.Error("a favourite on one host marked the other host's session of the same name")
	}
}

// A favourite is the SESSION, so every pane of it is favourite — a mark that followed a pane index
// would come off the moment a window was split.
func TestEveryPaneOfAFavouriteSessionIsFavourite(t *testing.T) {
	s := openIn(t, t.TempDir())
	first := paneRow("local", "api")
	if err := s.ToggleSession(first); err != nil {
		t.Fatal(err)
	}
	other := first
	other.PaneID, other.Index, other.WindowIndex = "%9", 3, 2
	if !s.HasSession(other) {
		t.Error("a second pane of the favourite session is not favourite")
	}
}

// A missing file is an empty set with NO warning: nothing is wrong with never having marked anything.
func TestAMissingFileIsAnEmptySetAndSaysNothing(t *testing.T) {
	s := openIn(t, t.TempDir())
	if sess, proj := s.Count(); sess != 0 || proj != 0 {
		t.Errorf("a fresh set holds %d/%d", sess, proj)
	}
	if w := s.Warning(); w != "" {
		t.Errorf("a fresh set warns: %q", w)
	}
}

// A file the reader cannot make sense of leaves NOTHING pinned and SAYS SO. The direction is the
// opposite of `hide`'s on purpose: a favourites set that failed toward "everything is pinned" would
// reorder the whole dashboard on a corrupt file.
func TestABrokenFileLeavesNothingPinnedAndSaysWhichWay(t *testing.T) {
	for _, c := range []struct{ name, body string }{
		{"not json at all", "{{{"},
		{"the wrong container", `["local"]`},
		{"a version this hub does not write", `{"v":99,"sessions":[]}`},
		{"no version at all", `{"sessions":[{"kind":"pane","host":"local","session":"api"}]}`},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "favourites.json")
		if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		s, err := Open(path)
		if err != nil {
			t.Fatalf("%s: Open returned an error instead of a warning: %v", c.name, err)
		}
		if sess, proj := s.Count(); sess != 0 || proj != 0 {
			t.Errorf("%s: %d/%d survived a file the reader refused", c.name, sess, proj)
		}
		if w := s.Warning(); w == "" {
			t.Errorf("%s: nothing is pinned and the screen is told nothing", c.name)
		} else if !strings.Contains(w, "nothing is pinned") {
			t.Errorf("%s: the warning does not say what it did: %q", c.name, w)
		}
	}
}

// The file is SORTED, so the same marks produce the same bytes and a diff says what changed rather
// than that a map was walked.
func TestTheFileIsStableAcrossRuns(t *testing.T) {
	write := func(order []string) string {
		dir := t.TempDir()
		s := openIn(t, dir)
		for _, n := range order {
			if err := s.ToggleSession(paneRow("local", n)); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.ToggleProject("d:local\x00b"); err != nil {
			t.Fatal(err)
		}
		if err := s.ToggleProject("d:local\x00a"); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "favourites.json"))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if a, b := write([]string{"one", "two", "three"}), write([]string{"three", "one", "two"}); a != b {
		t.Errorf("the same marks in a different order wrote different files:\n%s\n---\n%s", a, b)
	}
}

// A nil Set is a real state — every model built field by field has one — and no caller should need a
// branch for it.
func TestANilSetAnswersEverythingWithoutPanicking(t *testing.T) {
	var s *Set
	if s.HasSession(paneRow("local", "api")) || s.HasProject("d:x") {
		t.Error("a nil set claims a favourite")
	}
	if sess, proj := s.Count(); sess != 0 || proj != 0 {
		t.Error("a nil set counts something")
	}
	if s.Warning() != "" {
		t.Error("a nil set warns")
	}
	if err := s.ToggleSession(paneRow("local", "api")); err == nil {
		t.Error("a nil set accepted a mark, so the keystroke reported success and changed nothing")
	}
}

// A group with no id cannot be remembered, and the refusal says that rather than writing an empty
// string into the file — where it would match every row whose group could not be derived.
func TestAProjectWithNoIDIsRefused(t *testing.T) {
	s := openIn(t, t.TempDir())
	if err := s.ToggleProject(""); err == nil {
		t.Error("an empty project id was accepted")
	}
	if s.HasProject("") {
		t.Error("the empty id is marked, so every group with no id reads as a favourite")
	}
}

func TestDescribeCountsBothAndSaysNothingWhenThereIsNothing(t *testing.T) {
	for _, c := range []struct {
		sessions, projects int
		want               string
	}{
		{0, 0, ""},
		{1, 0, "1 favourite session"},
		{2, 0, "2 favourite sessions"},
		{0, 1, "1 favourite project"},
		{3, 2, "3 favourite sessions · 2 favourite projects"},
	} {
		if got := Describe(c.sessions, c.projects); got != c.want {
			t.Errorf("Describe(%d,%d) = %q, want %q", c.sessions, c.projects, got, c.want)
		}
	}
}

// A PIN SURVIVES THE SESSION GAINING A PANE. Reported from real use: "after attaching to a favourite
// it stops being in the list", which is what falling out of the pinned band looks like on a 45-row
// fleet.
//
// The v1 key was (kind, host, name) and all three change at once. The door creates a tmux session
// named `<name>-<short id>`, the join folds the pane-less row into that pane, and the row goes from
//
//	{agent, local, 20260817-cicd}  ->  {pane, local, 20260817-cicd-30f3382b}
//
// so the pin came off, the star went, and the row dropped back among forty others.
func TestAPinSurvivesASessionGainingAPane(t *testing.T) {
	s := openIn(t, t.TempDir())
	before := agentRow("local", "20260817-cicd")
	if err := s.ToggleSession(before); err != nil {
		t.Fatal(err)
	}
	after := absorbedRow("local", "20260817-cicd")
	if !s.HasSession(after) {
		t.Errorf("the pin did not survive the pane:\n  before %+v\n  after  %+v",
			KeyOf(before), KeyOf(after))
	}
	// And unpinning from EITHER shape takes it off, because it is one key.
	if err := s.ToggleSession(after); err != nil {
		t.Fatal(err)
	}
	if s.HasSession(before) {
		t.Error("unpinning the pane left the pane-less row pinned — two keys for one session")
	}
}

// The uuid is GLOBAL, so a `~/.claude` shared between machines pins one session rather than one per
// host (§22.12). That is the opposite of the no-uuid case, where the same name on two hosts is two
// sessions — and both are right, because the uuid identifies a session while a name identifies a
// session ON a host.
func TestAUUIDPinIsTheSameSessionOnEveryHost(t *testing.T) {
	s := openIn(t, t.TempDir())
	if err := s.ToggleSession(agentRow("local", "20260817-cicd")); err != nil {
		t.Fatal(err)
	}
	if !s.HasSession(agentRow("side-desk", "20260817-cicd")) {
		t.Error("the same session listed by a second host reads as unpinned — a shared store would " +
			"then need a pin per host for one job")
	}
}

// A v1 file is REFUSED rather than half-translated: mapping `{agent, local, name}` to a uuid needs the
// fleet, which a reader does not have.
func TestAVersionOneFileIsRefusedWithAWayBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "favourites.json")
	v1 := `{"v":1,"sessions":[{"kind":"agent","host":"local","session":"20260817-cicd"}],"projects":["d:local\u0000iac"]}`
	if err := os.WriteFile(path, []byte(v1), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if sess, proj := s.Count(); sess != 0 || proj != 0 {
		t.Errorf("a v1 file was read as %d/%d — its session keys mean something else now", sess, proj)
	}
	w := s.Warning()
	for _, want := range []string{"version 1", "nothing is pinned", "press f again"} {
		if !strings.Contains(w, want) {
			t.Errorf("the warning does not say %q: %q", want, w)
		}
	}
}
