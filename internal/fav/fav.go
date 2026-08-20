// Package fav is the operator's favourites: sessions and whole projects that must sit above
// everything else on the dashboard.
//
// It is a separate set from `hide` rather than a flag on that one, because the two answer opposite
// questions and fail in opposite directions. A hidden set that cannot be read must fail toward
// VISIBLE — showing a noisy pane annoys, hiding a live agent loses work — while a favourites set that
// cannot be read fails toward the ORDINARY order, which is the order the operator would have had
// anyway. Folding them into one file would make one of those two choices for the other.
package fav

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/statedir"
)

// keyVersion is the on-disk shape. It starts at 1 and an absent `v` is refused, which is the lesson
// `hide` paid for: a change of CONTAINER is a different failure from a change of field, so the reader
// must say which shape it expected rather than reporting Go's own type error at the operator.
// Version 2 keys a session by its Claude UUID where there is one. Version 1 keyed every favourite by
// (kind, host, name), and that key does not survive a session GAINING A PANE: the door creates a tmux
// session named `<name>-<short id>` and the join then folds the pane-less row into it, so the row goes
// from `{agent, local, 20260817-cicd}` to `{pane, local, 20260817-cicd-30f3382b}` and the pin comes
// off — reported from real use as "after attaching to a favourite it stops being in the list", which
// is what falling out of the pinned band looks like on a 45-row fleet.
//
// A v1 file cannot be migrated: mapping `{agent, local, some-name}` to a uuid needs the fleet, and the
// fleet is not what a reader has. So v1 is refused with the sentence the version check already
// carries — pin them again — because a set half-translated would promote the wrong rows silently.
const keyVersion = 2

// Key is one favourite SESSION.
//
// It is the SESSION and not the pane, because that is what the operator marks: a session with four
// panes is one thing to them, and a favourite that followed a pane index would come off the moment a
// window was split.
//
// Which FIELDS identify a session depends on whether the hub knows its Claude uuid, and that is the
// whole substance of this type — see KeyOf. Where there is no uuid, `Kind` is in the key for the
// reason hide.Key carries one: without it a pane-less agent row and a real tmux session of the same
// name on the same host ARE the same key, and one press would mark the row beside the one under the
// cursor.
type Key struct {
	// Claude is the session's own UUID, and when it is set it is the WHOLE key — no kind, no host,
	// no name. Those three all change under a session that gains a pane, and the uuid does not: an
	// agent row carries it as SessionID and the pane that absorbs that row carries it as
	// ClaudeSession. It is also global, so a `~/.claude` shared between machines pins one session
	// rather than one session per host (docs/design.md §22.12).
	Claude string `json:"claude,omitempty"`
	// The rest key a pane that is not running a Claude session the hub knows the id of — a shell, a
	// `cat`, an agent the process walk found but never adopted.
	Kind    string `json:"kind,omitempty"`
	Host    string `json:"host,omitempty"`
	Session string `json:"session,omitempty"`
}

// KeyOf is the only place a row becomes a Key, so the writer and the reader cannot drift.
//
// An agent row's Session is the name Claude was given; a pane row's is the tmux session name. Both
// are what the operator sees on the row they pressed `f` on, which is what makes this the right key
// and an id the wrong one.
func KeyOf(p registry.Pane) Key {
	// Which FIELD carries the uuid depends on the row's kind, and `Pane.Claude` is the one place that
	// knows — the alias asks the same question one surface along, and two copies of it would be two
	// chances to key a pane on tmux's `$3`.
	if id := p.Claude(); id != "" {
		return Key{Claude: id}
	}
	return Key{Kind: p.Kind, Host: p.Host, Session: p.Session}
}

// Set is the persisted favourites.
type Set struct {
	mu       sync.Mutex
	path     string
	sessions map[Key]bool
	// projects holds project.Group.ID, which is machine-facing and namespaced by kind — a label a
	// person typed can collide with another, an id may not (§21.5's own argument for the split).
	projects map[string]bool
	warning  string
}

// DefaultPath is where the set lives, beside the hidden set and the send log.
func DefaultPath() string { return statedir.Path("favourites.json") }

// Open reads the set, and never fails because of its contents.
//
// A missing file is an empty set. A malformed one is an empty set plus a Warning the dashboard shows:
// fail toward the ORDINARY order, because an operator who has lost their favourites can see that
// they have, while a set half-read would silently promote the wrong rows.
func Open(path string) (*Set, error) {
	s := &Set{path: path, sessions: map[Key]bool{}, projects: map[string]bool{}}
	raw, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		return s, nil
	case err != nil:
		s.warning = fmt.Sprintf("favourites unreadable (%v) — nothing is pinned", err)
		return s, nil
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		s.warning = fmt.Sprintf("favourites malformed (%v) — nothing is pinned", err)
		return s, nil
	}
	if f.Version != keyVersion {
		s.warning = fmt.Sprintf("favourites are version %d and this hub writes %d — nothing is "+
			"pinned; press f again on what you want back", f.Version, keyVersion)
		return s, nil
	}
	for _, k := range f.Sessions {
		s.sessions[k] = true
	}
	for _, id := range f.Projects {
		s.projects[id] = true
	}
	return s, nil
}

type file struct {
	Version  int      `json:"v"`
	Sessions []Key    `json:"sessions"`
	Projects []string `json:"projects"`
}

// HasSession reports whether this row's session is a favourite.
func (s *Set) HasSession(p registry.Pane) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[KeyOf(p)]
}

// HasProject reports whether this project id is a favourite.
func (s *Set) HasProject(id string) bool {
	if s == nil || id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projects[id]
}

// ToggleSession marks or unmarks a row's session and writes the file.
func (s *Set) ToggleSession(p registry.Pane) error {
	return s.toggle(func() { flip(s.sessions, KeyOf(p)) })
}

// ToggleProject marks or unmarks a project and writes the file.
func (s *Set) ToggleProject(id string) error {
	if id == "" {
		return fmt.Errorf("that group has no id, so there is nothing to remember about it")
	}
	return s.toggle(func() { flip(s.projects, id) })
}

// toggle applies the change and persists, with the lock held across BOTH — so a second keystroke
// cannot write a file that describes a set the first one had already changed.
func (s *Set) toggle(apply func()) error {
	if s == nil {
		return fmt.Errorf("this hub keeps no favourites")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	apply()
	return writeAtomic(s.path, s.snapshot())
}

func flip[K comparable](m map[K]bool, k K) {
	if m[k] {
		delete(m, k)
		return
	}
	m[k] = true
}

// snapshot is the file's contents, SORTED, so two runs that marked the same things produce the same
// bytes and a diff of the file says what changed rather than that a map was walked.
func (s *Set) snapshot() file {
	out := file{Version: keyVersion}
	for k := range s.sessions {
		out.Sessions = append(out.Sessions, k)
	}
	sort.Slice(out.Sessions, func(i, j int) bool {
		a, b := out.Sessions[i], out.Sessions[j]
		if a.Claude != b.Claude {
			return a.Claude < b.Claude
		}
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		if a.Session != b.Session {
			return a.Session < b.Session
		}
		return a.Kind < b.Kind
	})
	for id := range s.projects {
		out.Projects = append(out.Projects, id)
	}
	slices.Sort(out.Projects)
	return out
}

// Count is how many of each the operator has marked, for a screen that wants to say so.
func (s *Set) Count() (sessions, projects int) {
	if s == nil {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions), len(s.projects)
}

// Warning is why the set is empty when it should not be, or "".
func (s *Set) Warning() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.warning
}

// writeAtomic writes through a temp file in the same directory, so a crash mid-write leaves the
// previous set rather than a truncated one.
func writeAtomic(path string, f file) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".favourites-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Describe is the one-line summary a screen shows, or "" when nothing is marked.
func Describe(sessions, projects int) string {
	switch {
	case sessions == 0 && projects == 0:
		return ""
	case projects == 0:
		return plural(sessions, "favourite session", "favourite sessions")
	case sessions == 0:
		return plural(projects, "favourite project", "favourite projects")
	}
	return plural(sessions, "favourite session", "favourite sessions") + " · " +
		plural(projects, "favourite project", "favourite projects")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, strings.TrimSpace(many))
}
