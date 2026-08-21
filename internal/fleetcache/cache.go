// Package fleetcache remembers what the hub measured about each machine, so the discovered list
// paints instantly and — the part that matters — DOES NOT MOVE while the operator is reading it.
//
// The order is the whole reason this file exists. Measured on the live fleet (docs/design.md §9),
// one host's probe answered at 5.4 s, 9.1 s, 15.7 s and 18.4 s: a list sorted on a live round trip
// therefore reorders between two openings of the same screen, and the row somebody ticked is a
// different machine when they come back to it. So the figure a screen sorts on is taken from HERE,
// written once per round, and bucketed by the reader — the same argument the dashboard's recency
// tiebreak makes for bucketing to the minute.
//
// It fails toward FORGETTING, never toward a wrong order. An unreadable or foreign-shaped file is
// "no facts yet" plus a Warning a screen can show: a fleet in its default order is the order the
// operator would have had anyway, while half a cache silently sorts some machines on a remembered
// figure and the rest on nothing. That is the same choice `internal/fav` makes against
// `internal/hide`, in the same words, and the two packages are separate because they fail in
// opposite directions.
package fleetcache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/fleet"
	"github.com/DawnBreather/tmux-hub/internal/statedir"
)

// fileVersion is the on-disk shape, and an absent `v` reads as 0 and is REFUSED.
//
// That is `hide`'s lesson rather than a habit: a change of CONTAINER is a different failure from a
// change of field, so the reader has to say which shape it expected instead of reporting Go's own
// type error at the operator. There is nothing to migrate here and there never will be — every fact
// in this file is re-measured by the next probe — so a refusal costs one round's ordering, which is
// the cheapest possible refusal in this repository.
const fileVersion = 1

// Key names the machine a fact is about, and it has two forms for the same reason `fav.Key` does.
//
// A fingerprint IS the machine (fleet spec §2.2): nothing the hub or the operator does renames it,
// so a fact learned under one alias is still that machine's fact under another — which is exactly
// what makes the order survive `hop` and `hop-again` being the same host.
//
// Where there is no fingerprint there is no identity, and the honest key is then the OBSERVER
// together with its alias. Not the alias alone: `web` here and `web` on a hop are different machines
// (fleet spec §2.1), so an alias-only key would let one hop's measurement decide another hop's
// order. Both fields are needed and the empty observer is the root's own vocabulary, which is
// hostset.Candidate.Via's convention.
type Key struct {
	Fingerprint string `json:"fp,omitempty"`
	Observer    string `json:"observer,omitempty"`
	Alias       string `json:"alias,omitempty"`
}

// Facts is what one round measured about one machine.
//
// `LastSeen` is stored beside the figures rather than being inferred from the file's mtime, because
// one file holds every machine and an mtime would date all of them by the most recent write.
type Facts struct {
	// RTT is the round trip the probe timed. It is only a fact when the probe TIMED one, and the
	// absence is reported by Facts' second return rather than by a zero — a zero here would sort an
	// unmeasured machine into the fastest bucket, which is the one direction a remembered order must
	// not fail in.
	RTT time.Duration
	// TmuxVersion is what `tmux -V` answered. Every tmux claim in docs/design.md is a per-version
	// claim, so this is a property of the machine and worth remembering between runs.
	TmuxVersion string
	// LastSeen dates the two above. A screen that wants to say how old a figure is reads it here.
	LastSeen time.Time
}

// Cache is the remembered facts.
type Cache struct {
	mu      sync.Mutex
	path    string
	facts   map[Key]Facts
	warning string
}

// DefaultPath is where the cache lives: beside the hidden set, the favourites and the send log.
//
// STATE and not config, which is why it is statedir rather than configdir: nothing here is the
// operator's decision, every figure is re-measured, and deleting the file costs one round's
// ordering. `hosts.toml` went the other way for the opposite reason.
func DefaultPath() string { return statedir.Path("fleet-cache.json") }

// Open reads the cache. It never fails because of malformed CONTENT (version mismatch, bad JSON),
// but it does fail when the file cannot be read (permissions, disk full) — those are not content
// problems the operator can fix by deleting the file.
func Open(path string) (*Cache, error) {
	c := &Cache{path: path, facts: map[Key]Facts{}}
	raw, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		// A first run. Deliberately silent: an absent cache and a refused one must not read alike,
		// and this is the commonest state the hub is ever in.
		return c, nil
	case err != nil:
		// Permission denied, disk full, or another I/O failure that is not about the cache's
		// content. The caller must report it.
		return c, fmt.Errorf("read %s: %w", path, err)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		c.warning = fmt.Sprintf("the fleet cache is malformed (%v) — the discovered list keeps its "+
			"ordinary order until the next probe measures one", err)
		return c, nil
	}
	if f.Version != fileVersion {
		c.warning = fmt.Sprintf("the fleet cache is version %d and this hub writes %d — the "+
			"discovered list keeps its ordinary order until the next probe measures one",
			f.Version, fileVersion)
		return c, nil
	}
	for _, n := range f.Nodes {
		c.facts[n.Key] = Facts{
			RTT:         time.Duration(n.RTTms) * time.Millisecond,
			TmuxVersion: n.TmuxVersion,
			LastSeen:    n.LastSeen,
		}
	}
	return c, nil
}

type file struct {
	Version int      `json:"v"`
	Nodes   []record `json:"nodes"`
}

// record is one machine on disk. The key is INLINE rather than nested, so the file reads as one line
// per machine and a person editing it by hand — which they will, to forget a stale figure — is not
// navigating two levels of object to find an alias.
type record struct {
	Key
	RTTms       int64     `json:"rtt_ms,omitempty"`
	TmuxVersion string    `json:"tmux,omitempty"`
	LastSeen    time.Time `json:"seen,omitempty"`
}

// Facts answers what is remembered about this machine, and whether anything is.
//
// The second return is the whole contract: a caller must be able to tell "measured at 0 ms" from
// "never measured", and this repository has already shipped an accessor whose zero was
// indistinguishable from the absence of the thing.
func (c *Cache) Facts(k Key) (Facts, bool) {
	if c == nil {
		return Facts{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.facts[k]
	return f, ok
}

// Len is how many machines are remembered, for a screen or a test that wants to say so.
func (c *Cache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.facts)
}

// Warning is why the cache is empty when it should not be, or "".
func (c *Cache) Warning() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.warning
}

// Record folds one round's measurements in and writes the file.
//
// It takes a MAP rather than one fact at a time because a round measures the whole fleet at once:
// one write per round instead of one per machine, and no second entry point that could apply a
// change without persisting it. The lock is held across both the fold and the write, so a second
// round cannot publish a file describing a set the first one had already changed — the shape
// `fav.toggle` uses, for the same reason.
//
// It is called from a tea.Cmd body while bubbletea's Update reads through Facts, which is what makes
// the lock a requirement rather than care (CLAUDE.md).
func (c *Cache) Record(learned map[Key]Facts) error {
	if c == nil {
		return fmt.Errorf("this hub remembers no fleet facts, so the discovered order will be the " +
			"ordinary one on every run")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, f := range learned {
		c.facts[k] = f
	}
	return writeAtomic(c.path, c.snapshot())
}

// snapshot is the file's contents, SORTED, so two runs that learned the same things produce the same
// bytes.
func (c *Cache) snapshot() file {
	out := file{Version: fileVersion}
	for k, f := range c.facts {
		out.Nodes = append(out.Nodes, record{
			Key:         k,
			RTTms:       f.RTT.Milliseconds(),
			TmuxVersion: f.TmuxVersion,
			LastSeen:    f.LastSeen,
		})
	}
	sort.Slice(out.Nodes, func(i, j int) bool {
		a, b := out.Nodes[i].Key, out.Nodes[j].Key
		if a.Fingerprint != b.Fingerprint {
			return a.Fingerprint < b.Fingerprint
		}
		if a.Observer != b.Observer {
			return a.Observer < b.Observer
		}
		return a.Alias < b.Alias
	})
	return out
}

// writeAtomic writes through a temp file in the same directory, so a crash mid-write leaves the
// previous facts rather than a truncated file the next Open would refuse whole.
func writeAtomic(path string, f file) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".fleet-cache-*")
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

// KeyOfNode is the ONE place a verified node becomes a cache key, so the writer (Record) and the
// reader (Facts) cannot drift — `fav.KeyOf` is the sibling that earned this rule, twice, on two
// surfaces.
//
// A node's key is its FIRST fingerprint and never its labels. `fleet.Node` holds the identity set in
// the order first seen and a merge only ever APPENDS to it (fleet spec §2.3: a new fingerprint joins
// the set, it never forks the node), so the first member is stable across every merge — which is the
// property that makes a fact learned under `hop` still that machine's fact under `hop-again`.
//
// The cache does not store facts about unverified candidates — only verified nodes that have been
// probed. When displaying candidates, the UI keys on the hop that reported them, not the candidate
// itself, so there is no KeyOfCandidate and that function was dead code.
func KeyOfNode(n fleet.Node) Key {
	if len(n.Fingerprints) > 0 {
		return Key{Fingerprint: n.Fingerprints[0]}
	}
	// A node with no fingerprint cannot be built by fleet.Observe — no fingerprint, no node — so
	// this is for a hand-made one, and it falls back to (observer, alias) rather than answering
	// with an empty key that every other unidentified machine would share.
	if len(n.Labels) > 0 {
		return Key{Observer: n.Labels[0].Observer, Alias: n.Labels[0].Alias}
	}
	return Key{}
}

// KeysOfNode is EVERY key a node's remembered facts could be under, in the order to try them.
//
// It exists because `KeyOfNode` answers with the FIRST fingerprint and a node's fingerprint set
// GROWS: two machines seen separately are two nodes with two keys, and one later observation carrying
// both fingerprints merges them into one node whose key is the keeper's first. Everything remembered
// under the absorbed fingerprint then becomes unreachable — measured, a node that had a 42 ms RTT and
// `tmux 3.5a` under `SHA256:bbb` looked up `SHA256:aaa` after the merge and found nothing, so the row
// fell back to `no timing` and re-sorted into a slower bucket than the machine deserves.
//
// The alternative was to MIGRATE the file's entries on merge, and this is better for a reason worth
// stating: a migration has to happen at exactly the right moment in a process that may not be the one
// holding the cache, while a reader that tries every key is correct whenever it runs and needs nobody
// to have remembered anything. The write path is untouched — `Record` still keys on `KeyOfNode`, so a
// merged node's next round writes one entry and the stale one ages out.
func KeysOfNode(n fleet.Node) []Key {
	if len(n.Fingerprints) == 0 {
		return []Key{KeyOfNode(n)}
	}
	out := make([]Key, 0, len(n.Fingerprints))
	for _, f := range n.Fingerprints {
		out = append(out, Key{Fingerprint: f})
	}
	return out
}
