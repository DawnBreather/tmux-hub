package ui

import "sort"

// SelectionKey names one pane. The host is part of the key because a pane id is
// unique only within a server: two hosts both have a %0, and merging them would
// send to whichever the map happened to hold.
type SelectionKey struct {
	Host   string
	PaneID string
}

// Selection is the set of panes the user has explicitly chosen.
//
// It holds PANES and never groups. Tags select; a tag is never itself a target —
// expanding a tag adds its members at the moment of selection, so a pane that
// joins the tag afterwards does not silently become a target nobody chose.
type Selection struct {
	members map[SelectionKey]bool
	order   []SelectionKey // insertion order, so the confirmation list is stable
}

func (s *Selection) Toggle(k SelectionKey) {
	if s.members == nil {
		s.members = map[SelectionKey]bool{}
	}
	if s.members[k] {
		delete(s.members, k)
		for i, o := range s.order {
			if o == k {
				s.order = append(s.order[:i], s.order[i+1:]...)
				break
			}
		}
		return
	}
	s.members[k] = true
	s.order = append(s.order, k)
}

func (s *Selection) Has(k SelectionKey) bool { return s.members[k] }
func (s *Selection) Len() int                { return len(s.members) }

func (s *Selection) Clear() {
	s.members = nil
	s.order = nil
}

// Members returns the selection in a stable order. The confirmation dialog lists
// them, and a list that reshuffles between one look and the next is a list nobody
// reads.
func (s *Selection) Members() []SelectionKey {
	out := make([]SelectionKey, 0, len(s.members))
	for _, k := range s.order {
		if s.members[k] {
			out = append(out, k)
		}
	}
	// Insertion order is the primary sort; anything the order slice lost is
	// appended deterministically rather than dropped.
	if len(out) < len(s.members) {
		var extra []SelectionKey
		seen := map[SelectionKey]bool{}
		for _, k := range out {
			seen[k] = true
		}
		for k := range s.members {
			if !seen[k] {
				extra = append(extra, k)
			}
		}
		sort.Slice(extra, func(i, j int) bool {
			if extra[i].Host != extra[j].Host {
				return extra[i].Host < extra[j].Host
			}
			return extra[i].PaneID < extra[j].PaneID
		})
		out = append(out, extra...)
	}
	return out
}

// Prune drops every member the predicate rejects. A pane that vanished must leave
// the selection: otherwise it stays a target forever and every send has to ask
// about it, which trains the user to confirm without reading.
func (s *Selection) Prune(alive func(SelectionKey) bool) {
	for k := range s.members {
		if !alive(k) {
			delete(s.members, k)
		}
	}
	kept := s.order[:0]
	for _, k := range s.order {
		if s.members[k] {
			kept = append(kept, k)
		}
	}
	s.order = kept
}
