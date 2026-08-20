// Package hide keeps the set of panes the user never wants to see.
//
// A host accumulates panes that are not agents and never will be: a log tail, a
// htop, a build watcher. They are permanent noise, so hiding is persistent —
// "never show me this again", not "not right now".
package hide

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/statedir"
)

// Key identifies a hidden pane across hub restarts.
//
// NOT the pane id. `%12` is monotonic and never reused within one tmux server's
// life (measured: kill %3, the next pane is %4), which makes it an exact key
// while the hub runs and the WRONG key on disk — a restarted server numbers from
// %0 again, so a persisted %3 names a different pane in the next generation.
//
// Start is #{pane_start_command}, the corroborator. It is STABLE, not immutable —
// this comment used to claim immutable and that is measured false: `respawn-pane`
// rewrites it with the pane id unchanged (a pane started `tail -f /dev/null`
// reported `"sleep 6777"` afterwards). So a respawn drops a mark, which fails
// toward VISIBLE and is the safe direction, but a reader must not be told the
// field cannot move. #{pane_current_command} is not usable at all: it walks
// zsh → claude → zsh, so a mark taken while it read `zsh` would stop matching
// the moment the agent started.
//
// TWO honest limits, both measured. Start is the EMPTY string for a pane created
// with no command — the plain shell, which is the commonest thing anyone hides —
// so those keys carry no corroboration and rest on position alone. And an
// interactive agent's start command is `"claude --resume <uuid>"`, which is
// unique to one launch, so a mark on an agent pane cannot match a future one.
// Neither is a defect to fix here: the resurface rule in Hidden is the safety
// net, and it does not depend on the corroborator at all.
//
// The window is its INDEX, not its name. §18's approved key is `session:window.pane`
// and the name was a drift from that: tmux ships `automatic-rename on`, so the name
// follows the running command — measured on a private socket, one window went
// `zsh` → `sleep` → `tail` across three commands while `window_index` stayed 0 and
// `window_id` stayed `@0`. So a name-keyed mark un-hid itself the moment the operator
// ran something in the pane, which broke §18's promise of "never show me this again".
// `window_id` is not the answer either: `@N` does not survive a server restart, and
// surviving one is the whole reason this key exists rather than a pane id.
type Key struct {
	// Kind is KindPane or KindAgent, and it is in the key because without it a pane-less row
	// and a real tmux pane could BE the same key. An agent row has no window, no index and no
	// start command, so three of the five components below were zero-valued and the key
	// degenerated to (host, name) — which a pane at window 0, index 0 of a session with that
	// name produces exactly. Measured: one press of `x` on either took a two-row screen to
	// `0 sessions · 2 hidden`, hiding the row nobody had marked. A guard on one side of that
	// was written first and could only ever see one direction; the kind closes both, because
	// the two keys are no longer equal.
	Kind        string `json:"kind"`
	Host        string `json:"host"`
	Session     string `json:"session"`
	WindowIndex int    `json:"window_index"`
	PaneIndex   int    `json:"pane_index"`
	Start       string `json:"start"`
}

// KeyOf is the only place a pane becomes a Key, so the fields that make up the
// key cannot drift between the writer and the reader.
func KeyOf(p registry.Pane) Key {
	return Key{Kind: p.Kind, Host: p.Host, Session: p.Session, WindowIndex: p.WindowIndex,
		PaneIndex: p.Index, Start: p.StartCommand}
}

// Set is the persisted hidden set.
type Set struct {
	mu      sync.Mutex
	path    string
	marked  map[Key]bool
	warning string
}

// DefaultPath is where the set lives, beside the send log.
func DefaultPath() string { return statedir.Path("hidden.json") }

// Open reads the set, and never fails because of its contents.
//
// A missing file is an empty set. A malformed or unreadable one is an empty set
// plus a Warning the dashboard shows: fail toward VISIBLE, because showing a
// noisy pane annoys and hiding a live agent loses work.
func Open(path string) (*Set, error) {
	s := &Set{path: path, marked: map[Key]bool{}}
	raw, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		return s, nil
	case err != nil:
		s.warning = fmt.Sprintf("hidden set unreadable (%v) — showing everything", err)
		return s, nil
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		// A v1 file is a BARE ARRAY, so it cannot unmarshal into the envelope at all —
		// which means the version check below was unreachable for every file any shipped
		// version wrote, and the one upgrade this code exists to handle told the operator
		// their file was corrupt, quoting a Go type name. Recognising the old shape here
		// is what makes the migration branch reachable.
		var legacy []Key
		if json.Unmarshal(raw, &legacy) == nil {
			f = file{Version: 1}
		} else {
			s.warning = fmt.Sprintf("hidden set malformed (%v) — showing everything", err)
			return s, nil
		}
	}
	if f.Version != keyVersion {
		// The version is what makes the key change SAFE, and the danger runs the
		// opposite way from the obvious one. Losing marks merely annoys; GAINING one
		// hides a pane nobody hid. An older record has no window index at all, so
		// unmarshalling it into the current Key leaves WindowIndex 0 — and 0 is a real
		// window, the first one — so a silent upgrade could hide the operator's first
		// window on its first run. Refusing the whole file un-hides everything once,
		// visibly, which is the direction this package fails in everywhere else.
		s.warning = fmt.Sprintf("hidden set is from before the key changed "+
			"(v%d, this hub writes v%d) — showing everything; hide them again and a mark "+
			"will no longer follow a window rename or land on the wrong row", f.Version,
			keyVersion)
		return s, nil
	}
	for _, k := range f.Keys {
		s.marked[k] = true
	}
	return s, nil
}

// keyVersion is the shape of Key on disk. Bump it whenever a FIELD of Key changes,
// because a record written under one shape cannot be read under another: a field the
// old record lacks does not arrive as "absent", it arrives as the zero value, which
// for an index is indistinguishable from a real one.
//
//	v1  host, session, window NAME, pane index, start   (un-hid itself on a rename)
//	v2  host, session, window INDEX, pane index, start
//	v3  KIND, host, session, window index, pane index, start
//
// v3 exists because v2 could not tell a pane-less row from a pane: an agent row's three
// zero-valued components made its key equal to that of a pane at window 0, index 0 with no start
// command, so `x` on either hid both. An absent `kind` reads as "", which no row produces, so an
// old record would in fact match nothing — but this package refuses rather than reasoning about
// what a zero value happens to mean, because the argument runs the other way for every other
// field, and being right by accident is how the v1 branch came to be unreachable.
const keyVersion = 3

// file is hidden.json.
//
// v1 was a BARE ARRAY, and that is not the same thing as "an envelope with no v". An
// earlier version of this comment claimed the absent "v" would read as 0 and be refused by
// the version check; it never got that far, because unmarshalling an array into a struct is
// a TYPE error, so every real v1 file took the malformed branch instead. Open therefore
// tries the array shape explicitly and calls it v1. A v2-shaped envelope whose "v" is
// absent DOES read as 0 and is refused, which is the case the old comment described.
type file struct {
	Version int   `json:"v"`
	Keys    []Key `json:"keys"`
}

// Marked is the raw answer: did the user hide this pane?
func (s *Set) Marked(k Key) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.marked[k]
}

// Hidden is the EFFECTIVE answer, and the one the dashboard uses.
//
// A marked pane that is waiting on the user is shown anyway. This is not a
// courtesy — it is what makes a wrong key match safe. A key can only mis-match a
// pane that shares a host, session, window, index AND start command with the one
// the user hid; even then the mis-matched pane cannot be hidden while it is
// BLOCKED, which is the only state where hiding loses work. Do not remove this
// without reading docs/design.md §18.
func (s *Set) Hidden(p registry.Pane) bool { return s.Marked(KeyOf(p)) && !waiting(p) }

// Resurfaced is the other half of the same rule, and it is here so the SENTENCE that announces the
// behaviour and the GATE that performs it cannot read different fields.
//
// Four sites used to re-derive "is this marked row waiting", and they split: this gate read
// `p.ClassifiedState` while the note eight lines from its caller read `p.State()`. While nothing
// wired the pane-to-session join the two were identical and the split was invisible; wiring it made
// them differ, so the hub could print "stays while it is waiting" and hide the row in one keystroke.
func (s *Set) Resurfaced(p registry.Pane) bool { return s.Marked(KeyOf(p)) && waiting(p) }

// waiting is the ONE definition of the state where hiding loses work.
//
// It reads `State()`, the EFFECTIVE state, not the classification. For a pane the listing has spoken
// about recently the listing is the fresher fact, and for a row with no pane it is the only fact
// there is — a gate whose failure mode is losing the operator's work should take the fresher answer.
// `State()` also refuses to let a listing word override a pane whose pixels show a prompt, so this
// cannot be talked out of `needs` by a stale word.
func waiting(p registry.Pane) bool { return p.State() == state.Needs }

// Toggle flips a pane's mark and writes the set to disk immediately, so a hub
// killed without a clean exit does not lose the user's last decision.
func (s *Set) Toggle(p registry.Pane) error {
	k := KeyOf(p)
	s.mu.Lock()
	if s.marked[k] {
		delete(s.marked, k)
	} else {
		s.marked[k] = true
	}
	keys := make([]Key, 0, len(s.marked))
	for k := range s.marked {
		keys = append(keys, k)
	}
	path := s.path
	s.mu.Unlock()

	sortKeys(keys) // stable file: a diff of hidden.json should show only real changes
	return writeAtomic(path, keys)
}

// Count is how many marks exist, for the footer.
func (s *Set) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.marked)
}

// Warning is non-empty when the set on disk could not be used.
func (s *Set) Warning() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.warning
}

// sortKeys orders keys by their five fields in declaration order, for stable file output.
func sortKeys(keys []Key) {
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		if a.Session != b.Session {
			return a.Session < b.Session
		}
		if a.WindowIndex != b.WindowIndex {
			return a.WindowIndex < b.WindowIndex
		}
		if a.PaneIndex != b.PaneIndex {
			return a.PaneIndex < b.PaneIndex
		}
		return a.Start < b.Start
	})
}

// writeAtomic writes through a temp file in the same directory, so a crash
// mid-write leaves the previous set rather than a truncated one.
func writeAtomic(path string, keys []Key) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(file{Version: keyVersion, Keys: keys}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hidden-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
