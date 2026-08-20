// Package project answers which project a row belongs to.
//
// A project is derived from a row's working directory, host-qualified, and overridden by
// rules the operator writes (docs/design.md §21.1). Nothing here is stored on a row:
// there is no `Pane.Project` field to go stale, no invalidation and no cache to be
// wrong — the answer is recomputed per frame by a pure function over the rules.
package project

import (
	"fmt"
	"sort"
	"strings"

	"github.com/DawnBreather/tmux-hub/internal/conf"
	"github.com/DawnBreather/tmux-hub/internal/configdir"
	"github.com/DawnBreather/tmux-hub/internal/registry"
)

// Kind says where a group's identity came from, and it is what the project list sorts
// and pins by.
type Kind int

const (
	// Named: a rule in projects.toml matched.
	Named Kind = iota
	// Derived: the last path segment, which is the fallback when no rule matches.
	Derived
	// Pending: a PANE whose path has not been read yet. It is counted, and its remedy is
	// "the path was unreadable, named per host" rather than "write projects.toml"
	// (§21.9.3).
	Pending
	// Unassigned: a row with no path at all. Pinned last whatever its size.
	Unassigned
)

func (k Kind) String() string {
	switch k {
	case Named:
		return "named"
	case Derived:
		return "derived"
	case Pending:
		return "pending"
	default:
		return "unassigned"
	}
}

// deletedSuffix is what tmux appends to the path of a pane whose directory was removed.
// Its LENGTH matches, so the label framing trusts the value and the stripping has to
// happen here (§21.7).
const deletedSuffix = " (deleted)"

// Group is one project as a screen shows it.
//
// ID is machine-facing and Label is what the operator reads, and they are separate
// because §21.5's filter compares ids for equality: a label a person typed can collide
// with another, while an id may not. The id is namespaced by kind so that `enter` on the
// unassigned bucket cannot render as `esc` — the bucket has a real id.
type Group struct {
	ID    string
	Label string
	Kind  Kind
	// Host is empty for a named group, which may span hosts by the operator's explicit
	// act, and set for a derived one, whose identity is (host, path) — measured:
	// `/home/dev/.claude-mem/observer-sessions` exists on two hosts and means different
	// things there.
	Host string
}

// Rule is one line of the operator's file.
type Rule struct {
	Name   string
	Prefix string
	// Host empty means any host. A host-qualified rule beats an any-host rule of the
	// same prefix: it is the more specific statement, and without that ordering the pair
	// would be a tie with no answer.
	Host string
}

// Rules is the parsed file. The zero value is valid and means "no rules", which is a
// specified screen rather than an error: every row then groups by its basename fallback
// (§21.9.2).
type Rules struct {
	rules []Rule
}

// Rules returns the parsed rules in file order.
func (r Rules) Rules() []Rule { return append([]Rule(nil), r.rules...) }

// DefaultPath is where the file lives: beside hosts.toml, in config rather than state,
// because these are the operator's decisions and not data the hub derived (§9).
func DefaultPath() string { return configdir.Path("projects.toml") }

// Parse reads the `[[project]]` records through the one config dialect.
func Parse(content string) (Rules, error) {
	var out []Rule
	// prefixLine is where each rule's `prefix =` was written, so a conflict between two
	// rules can point at the two lines the operator has to edit rather than at the
	// records that contain them. The file is hand-edited and "somewhere in the file" is
	// not a fix.
	var prefixLine []int
	var startLine []int
	var cur *Rule
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
		}
	}
	err := conf.Scan(content, "project",
		func() { flush(); cur = &Rule{}; prefixLine = append(prefixLine, 0); startLine = append(startLine, 0) },
		func(key, value string, line int) error {
			s, err := conf.String(value)
			if err != nil {
				return err
			}
			if startLine[len(startLine)-1] == 0 {
				startLine[len(startLine)-1] = line
			}
			switch key {
			case "name":
				cur.Name = s
			case "prefix":
				cur.Prefix = s
				prefixLine[len(prefixLine)-1] = line
			case "host":
				cur.Host = s
			default:
				return fmt.Errorf("unknown key %q", key)
			}
			return nil
		})
	if err != nil {
		return Rules{}, err
	}
	flush()

	for i, ru := range out {
		where := fmt.Sprintf("line %d", startLine[i])
		if ru.Name == "" {
			return Rules{}, fmt.Errorf("%s: a project needs a name — a nameless group "+
				"cannot be shown", where)
		}
		if ru.Prefix == "" {
			return Rules{}, fmt.Errorf("%s: project %q needs a prefix — without one it "+
				"would match every row", where, ru.Name)
		}
	}
	// The same prefix under the same host scope twice is a tie with no answer, and it is
	// refused HERE rather than per frame, where the operator cannot see it and the
	// renderer would have to guess. Two DIFFERENT prefixes of equal length can never both
	// match one path — a path has exactly one prefix of each length — so "equal length is
	// an error" reduces to this static check.
	seen := map[string]int{}
	for i, ru := range out {
		k := ru.Host + "\x00" + cleanPrefix(ru.Prefix)
		if j, dup := seen[k]; dup {
			a, b := prefixLine[j], prefixLine[i]
			scope := "any host"
			if ru.Host != "" {
				scope = "host " + ru.Host
			}
			return Rules{}, fmt.Errorf("prefix %q is claimed twice for %s, at line %d "+
				"and line %d — which one wins has no answer, so name one of them "+
				"differently or narrow one by host", ru.Prefix, scope, a, b)
		}
		seen[k] = i
	}
	return Rules{rules: out}, nil
}

// Render writes rules back out. It exists so the naming screen can add a rule without a
// second writer, and so a round-trip test can hold the pair to being inverses.
func Render(rules []Rule) string {
	var b strings.Builder
	for _, ru := range rules {
		b.WriteString("[[project]]\n")
		fmt.Fprintf(&b, "name = %s\n", conf.Quote(ru.Name))
		fmt.Fprintf(&b, "prefix = %s\n", conf.Quote(ru.Prefix))
		if ru.Host != "" {
			fmt.Fprintf(&b, "host = %s\n", conf.Quote(ru.Host))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// OfPane is the one place a ROW becomes a group, so the mapping from a row's kind to the
// meaning of an empty path lives in exactly one place.
//
// The path comes from two different fields depending on the kind — `pane_current_path`
// for a pane, Claude's `cwd` for an agent — and both land in `registry.Pane.Path`. What
// the KIND still decides is what an EMPTY one means: a pane's label may simply not have
// arrived yet, which is Pending, while an agent's cwd is reported once at session start,
// so its absence is final and the row is Unassigned.
func (r Rules) OfPane(p registry.Pane) Group {
	// An EMPTY path, not a blank one: a directory named "  " is a directory, so trimming
	// here would report a real pane as having no path yet.
	if p.Path == "" && p.Kind == registry.KindPane {
		return Group{ID: "p:", Label: "path not read yet", Kind: Pending}
	}
	return r.Of(p.Host, p.Path)
}

// Of is the derivation, and it is pure.
func (r Rules) Of(host, path string) Group {
	path = normalise(path)
	if path == "" {
		return Group{ID: "u:", Label: "unassigned", Kind: Unassigned}
	}
	if ru, ok := r.match(host, path); ok {
		// A named group has no host in its identity: the operator may deliberately point
		// two hosts' prefixes at one name, and that is the explicit act §21.1 requires
		// for a merge.
		return Group{ID: "n:" + ru.Name, Label: ru.Name, Kind: Named}
	}
	base := path
	if i := strings.LastIndex(base, "/"); i >= 0 && len(base) > 1 {
		base = base[i+1:]
	}
	if base == "" {
		base = "/"
	}
	// Host-qualified, because the identity is (host, path): the same directory name on
	// two hosts means different things, measured. The LABEL stays bare and `Labels`
	// disambiguates only where two of them collide, so a distinction that is not there
	// costs no columns.
	return Group{ID: "d:" + host + "\x00" + base, Label: base, Kind: Derived, Host: host}
}

// match finds the winning rule: the longest prefix, with a host-qualified rule beating an
// any-host rule of the same length.
func (r Rules) match(host, path string) (Rule, bool) {
	best := -1
	bestLen := -1
	bestScoped := false
	for i, ru := range r.rules {
		if ru.Host != "" && ru.Host != host {
			continue
		}
		p := cleanPrefix(ru.Prefix)
		if !underPrefix(path, p) {
			continue
		}
		scoped := ru.Host != ""
		switch {
		case len(p) > bestLen, len(p) == bestLen && scoped && !bestScoped:
			best, bestLen, bestScoped = i, len(p), scoped
		}
	}
	if best < 0 {
		return Rule{}, false
	}
	return r.rules[best], true
}

// underPrefix is a PATH-BOUNDARY test, not a string one. `/home/dev/lab/st` must not
// match `/home/dev/lab/streams`, or a rule would silently sweep a neighbouring directory
// into someone else's project.
func underPrefix(path, prefix string) bool {
	if prefix == "/" {
		return strings.HasPrefix(path, "/")
	}
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// cleanPrefix drops a trailing slash so `/w` and `/w/` are one rule rather than two that
// look identical in the file and collide by surprise.
func cleanPrefix(p string) string {
	for len(p) > 1 && strings.HasSuffix(p, "/") {
		p = p[:len(p)-1]
	}
	return p
}

// normalise strips what tmux adds and what the shell leaves.
//
// The ` (deleted)` marker is stripped UNCONDITIONALLY, which is a trade rather than a
// clean answer: a directory whose name really ends that way is folded into its neighbour,
// and tmux gives the reader no way to tell the two apart. The alternative is worse — a
// project named `st (deleted)` appearing beside `st` the moment anyone removes a
// directory, splitting a live project in two.
func normalise(path string) string {
	// NOT TrimSpace. `mkdir "st "` succeeds, so `/w/st` and `/w/st ` are two directories,
	// and folding them together is exactly the silent merge §21.7 refused
	// `#{=N:pane_current_path}` to avoid — the derivation must not do by trimming what the
	// wire format was chosen not to do. A path of only spaces is likewise a real path and
	// not an absent one.
	path = strings.TrimSuffix(path, deletedSuffix)
	for len(path) > 1 && strings.HasSuffix(path, "/") {
		path = path[:len(path)-1]
	}
	return path
}

// Labels resolves display labels over a WHOLE SET, because a collision is a property of
// the set and not of one group (§21.13.2): two groups with the same label must be
// distinguishable on screen, and only those two should pay for it.
//
// Returned by id, so a caller cannot accidentally key on the ambiguous thing.
func Labels(groups []Group) map[string]string {
	byLabel := map[string][]Group{}
	for _, g := range groups {
		byLabel[g.Label] = append(byLabel[g.Label], g)
	}
	out := make(map[string]string, len(groups))
	for label, st := range byLabel {
		if len(st) < 2 {
			for _, g := range st {
				out[g.ID] = label
			}
			continue
		}
		// Sorted so the qualified labels come out in a stable order whatever order the
		// rows arrived in.
		sort.Slice(st, func(i, j int) bool { return st[i].ID < st[j].ID })
		for _, g := range st {
			if g.Host != "" {
				out[g.ID] = label + " @" + g.Host
				continue
			}
			// Two NAMED groups cannot collide — a name is the id — and a bucket is
			// unique, so this is unreachable today. Left as a label rather than a panic
			// because an ambiguous heading is a cosmetic fault and a crash is not.
			out[g.ID] = label
		}
	}
	return out
}
