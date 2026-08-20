package project

import (
	"fmt"
	"sort"
	"strings"

	"github.com/DawnBreather/tmux-hub/internal/registry"
)

// RuleToName is the rule that would give a group the operator's name, or the reason it cannot
// have one.
//
// §21.12 rule 3 says the naming overlay serves a PROJECT as well as a session, and §21.12 then
// records why the project half was not built: a project name has to become a prefix RULE, and a
// rule needs a PREFIX — while a derived group is keyed on `(host, last path segment)`, which is
// not one. Its rows may sit under different ancestors (`/a/st` and `/b/st`), so there may be
// nothing safe to write.
//
// The answer is the longest common ancestor of the group's OWN rows, and the safety is a
// MEASUREMENT rather than an argument about it: the prospective rule is applied to the whole
// fleet, and it is refused unless it captures exactly the rows it was asked to name. That
// closes the case the section was worried about without needing to reason about how shallow an
// ancestor is too shallow — a prefix that would swallow a neighbour is rejected by trying it,
// and the refusal NAMES what it would have taken so the operator can act on it.
//
// A group that is ALREADY named came from a rule, so its own prefix is reused: two rules for one
// prefix is the tie Parse refuses outright, and rewriting beats accumulating.
func (r Rules) RuleToName(g Group, name string, fleet []registry.Pane) (Rule, error) {
	switch g.Kind {
	case Unassigned:
		return Rule{}, fmt.Errorf("the unassigned bucket is not a directory, so it cannot "+
			"become a rule — a row lands there because it has no path at all, and %q would "+
			"name nothing", name)
	case Pending:
		return Rule{}, fmt.Errorf("these rows have no path YET, so there is nothing to write a " +
			"rule about — they will join a project as soon as their host answers")
	case Named:
		// Reuse the matching rule's own prefix, so naming twice rewrites rather than stacks.
		for _, ru := range r.rules {
			if ru.Name == g.Label {
				return Rule{Name: name, Prefix: ru.Prefix, Host: ru.Host}, nil
			}
		}
		return Rule{}, fmt.Errorf("no rule in projects.toml is named %q any more — reopen the "+
			"list and try again", g.Label)
	}

	// A DERIVED group: gather the paths it actually holds, on its own host.
	var mine []string
	for _, p := range fleet {
		if r.OfPane(p).ID == g.ID {
			mine = append(mine, normalise(p.Path))
		}
	}
	if len(mine) == 0 {
		return Rule{}, fmt.Errorf("that project has no rows any more — reopen the list")
	}
	prefix := commonAncestor(mine)
	if prefix == "" || prefix == "/" {
		return Rule{}, fmt.Errorf("the %d directories in this project share no common parent "+
			"below the filesystem root, so no prefix can name them together — name them "+
			"separately, or move them under one directory", len(mine))
	}

	cand := Rule{Name: name, Prefix: prefix, Host: g.Host}
	// THE CHECK: apply it and see. Anything captured that is not in this group is a row the
	// operator did not ask to name, and naming it would be silent.
	probe := Rules{rules: append(append([]Rule(nil), r.rules...), cand)}
	var swallowed []string
	for _, p := range fleet {
		if r.OfPane(p).ID == g.ID {
			continue
		}
		if probe.OfPane(p).Label == name {
			swallowed = append(swallowed, normalise(p.Path))
		}
	}
	if len(swallowed) > 0 {
		sort.Strings(swallowed)
		if len(swallowed) > 3 {
			swallowed = append(swallowed[:3], fmt.Sprintf("and %d more", len(swallowed)-3))
		}
		return Rule{}, fmt.Errorf("naming this project would need the prefix %s, which also "+
			"takes %s — those rows are not in it, so name a narrower directory instead",
			prefix, strings.Join(swallowed, ", "))
	}
	return cand, nil
}

// commonAncestor is the longest path prefix every path shares, cut at a path BOUNDARY — the
// same rule matching uses, so a computed prefix cannot mean something a match would not.
//
// The character-wise answer is wrong here in a way that is easy to miss: `/a/streams` and
// `/a/st` share the characters `/a/st`, which is a real directory that holds neither.
func commonAncestor(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	split := func(p string) []string {
		return strings.Split(strings.TrimPrefix(p, "/"), "/")
	}
	rooted := strings.HasPrefix(paths[0], "/")
	best := split(paths[0])
	for _, p := range paths[1:] {
		if strings.HasPrefix(p, "/") != rooted {
			// A relative path and an absolute one share nothing that can be written down.
			return ""
		}
		segs := split(p)
		n := 0
		for n < len(best) && n < len(segs) && best[n] == segs[n] {
			n++
		}
		best = best[:n]
	}
	out := strings.Join(best, "/")
	if rooted {
		out = "/" + out
	}
	return cleanPrefix(out)
}

// Replace puts a rule into a set by its PREFIX, or removes it when the name is blank.
//
// By prefix rather than by name, because the prefix is the identity — Parse refuses two rules
// for one prefix, so a rename that added a second would produce a file the reader rejects, and
// the operator would have to hand-edit TOML to recover from having typed a name.
func Replace(rules []Rule, ru Rule) []Rule {
	out := make([]Rule, 0, len(rules)+1)
	replaced := false
	for _, old := range rules {
		if cleanPrefix(old.Prefix) == cleanPrefix(ru.Prefix) && old.Host == ru.Host {
			if ru.Name == "" {
				replaced = true
				continue // removing a name removes the rule; a nameless rule names nothing
			}
			out = append(out, ru)
			replaced = true
			continue
		}
		out = append(out, old)
	}
	if !replaced && ru.Name != "" {
		out = append(out, ru)
	}
	return out
}
