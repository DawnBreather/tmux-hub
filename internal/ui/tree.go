package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// The filesystem view: hosts are volumes, directories are directories, sessions are files.
//
// Asked for in those words — "I would treat Projects as Directories, sessions as files, Hosts as
// volumes" — and it is more expressive than what the hub had, not just a different arrangement. Today
// a "project" is the LAST PATH SEGMENT of a row's cwd (§21.12), so the whole hierarchy above it is
// thrown away: measured on the operator's fleet, `st-edgebox`, `frontend` and `tundra-security-server`
// are three unrelated labels that are one family — `~/lab/streams/st`, holding 14 sessions of which 5
// want the operator. That node does not exist in the flat model at all.
//
// WHAT THIS FILE IS AND IS NOT. It builds the FLATTENED tree — the lines a renderer would draw, in
// order — and nothing else. No keys, no cursor, no screen. That split is deliberate: the arithmetic
// this repo has paid for three times (a viewport that must agree with `A` to the row) becomes trivial
// once every drawn line is exactly one thing, so the tree is built as a flat list of lines and the
// window over it is a window over a slice. A tree row can never carry a "prefix" the way a grouped row
// carries a header, which is the whole class of orphan-header and orphan-break defects.
//
// THE ORDER INSIDE A NODE IS ATTENTION, not the alphabet, and that is the one judgement this design
// makes against the metaphor: files in a directory do not reorder themselves, and this screen exists
// to put what wants the operator first (§21.11). A node's children come in the order the PROJECT LIST
// already uses (project.MoreUrgent), and its own sessions come in the order the dashboard already
// uses — so all three surfaces answer "which of these first" the same way, by calling the same
// functions rather than by three comments agreeing.

// treeLine is one drawn line of the flattened view: a NODE or a ROW, never both.
//
// A node carries the attention rolled up over everything beneath it, as a project.Summary — the same
// struct and the same roll-up rule the project list uses, so `waiting` cannot come to mean two things
// on two screens. A row carries its pane and nothing else; the renderer already knows how to draw one.
type treeLine struct {
	// Depth is how far to indent, counted in nodes above this line.
	Depth int
	// Key is the line's stable identity: `host:/collapsed/path` for a node, and for a row the same
	// SelectionKey the rest of the product uses. Expansion state and the cursor are keyed on it, so
	// neither moves when the fleet re-sorts under a poll — the defect a stored INDEX has.
	Key string
	// Label is what a NODE says. Empty on a row line.
	Label string
	// Sum is a NODE's rolled-up attention. Zero on a row line.
	Sum project.Summary
	// Pane is a ROW's session. Zero on a node line.
	Pane registry.Pane
	// IsRow separates the two shapes without reading another field, because "Label is empty" would
	// also be true of a node whose label the fleet failed to report.
	IsRow bool
	// Pinned marks the favourites band and THE ROWS UNDER IT.
	//
	// On the node it says "this is not a directory". On a row it says "nothing above this line tells
	// you where it lives", which is the whole reason a pinned row carries its own address: the band is
	// not a place, so a pinned row under it has no volume and no directory over it to read the host and
	// the path from. Two favourites of one name in two checkouts are otherwise the same line twice —
	// which is the duplicate report this screen was built from.
	Pinned bool
	// Expandable says the line has children that are currently hidden or shown, so the renderer can
	// draw the ▸/▾ and a key can act on it. A node with no children under it is not expandable.
	Expandable bool
	// Open is whether those children are currently shown.
	Open bool
	// Host and Path are a NODE's address: the volume it is on and the absolute directory it names.
	// They are what `n` launches into, which is the gesture the whole metaphor was asked for — "when
	// creating a new session, create it in the corresponding directory". Empty on a row line, which
	// carries its pane and answers from that; empty on the favourites node, which is not a directory.
	Host string
	Path string
}

// nodeAddress splits a node's Key back into the volume and the directory it names.
//
// The inverse of the two lines that build it — `host + ":"` for a volume and `parent.key + "/" + seg`
// below it — and not a guess about anybody else's format: both halves live in this file. The `~` fold
// relabels a node and never rekeys it, so this returns the REAL directory, which is the one a launch
// needs. A key that is not of this shape (the favourites band) answers with two empty strings.
func nodeAddress(key string) (host, path string) {
	i := strings.Index(key, ":")
	if i < 0 {
		return "", ""
	}
	return key[:i], key[i+1:]
}

// favouritesKey is the pinned node's identity. It is not a path, so it cannot collide with one: a
// directory called `★` would still be `host:/★`.
const favouritesKey = "\x00favourites"

// treeNode is the intermediate trie this file builds and then flattens. It is not exported and not
// returned: everything outside sees the flat lines.
type treeNode struct {
	label string
	key   string
	kids  map[string]*treeNode
	order []string // insertion order, so the sort below is stable against map iteration
	rows  []registry.Pane
	// sum memoises rollUp for the life of ONE build, which is all a trie lives for: `flatten` asks each
	// node once for its line, and the child sort asks twice per comparison, so the recursion ran over
	// the same subtrees O(n log n) times. Measured on a 54-session fleet shaped like the operator's —
	// three volumes, six levels, twenty directories — `treeLines` went from 137 µs to 103 µs.
	//
	// There is no staleness class to worry about: the node is created inside `treeLines` and thrown away
	// when it returns, so nothing can change under the memo.
	sum *project.Summary
}

func (n *treeNode) child(seg, key string) *treeNode {
	if n.kids == nil {
		n.kids = map[string]*treeNode{}
	}
	if c := n.kids[seg]; c != nil {
		return c
	}
	c := &treeNode{label: seg, key: key}
	n.kids[seg] = c
	n.order = append(n.order, seg)
	return c
}

// rollUp is the attention beneath a node, counted by the SAME rule the project list uses: Waiting is
// Needs, Broken is Error, Unknown is Unknown, and Gone folds into Total because §6 keeps a vanished
// pane with its last screen and it is not something to act on.
//
// It restates that mapping rather than calling project.Summarise, because Summarise groups by the
// project RULES and this groups by the path trie — the counting is the shared part and the grouping is
// not. The state names are the shared vocabulary, so a new state has one place to be added on each
// side and the doc comment on each names the other.
func (n *treeNode) rollUp() project.Summary {
	if n.sum != nil {
		return *n.sum
	}
	var s project.Summary
	for _, p := range n.rows {
		s.Total++
		switch p.State() {
		case state.Needs:
			s.Waiting++
		case state.Error:
			s.Broken++
		case state.Unknown:
			s.Unknown++
		}
	}
	for _, seg := range n.order {
		c := n.kids[seg].rollUp()
		s.Total += c.Total
		s.Waiting += c.Waiting
		s.Broken += c.Broken
		s.Unknown += c.Unknown
	}
	n.sum = &s
	return s
}

// collapse merges a node that has ONE child and no sessions of its own into that child, so a chain
// with no branching costs one line instead of four.
//
// Measured on the operator's fleet: `/home/dev/lab/streams` is four levels before anything branches,
// and 17 of their 30 projects hold exactly one session — without this the tree would spend more than
// half its lines on ceremony over a single leaf, which is the objection that nearly ruled the whole
// view out as a default.
func (n *treeNode) collapse() {
	for _, seg := range n.order {
		n.kids[seg].collapse()
	}
	for len(n.rows) == 0 && len(n.order) == 1 {
		only := n.kids[n.order[0]]
		n.label = strings.TrimPrefix(n.label+"/"+only.label, "/")
		n.key, n.kids, n.order, n.rows = only.key, only.kids, only.order, only.rows
	}
}

// treeLines is the flattened view: the favourites node, then one volume per host, then the path trie.
//
// `expanded` is keyed on treeLine.Key and says which nodes are OPEN. A nil map means everything is
// closed except what alwaysOpen decides, which is how a first paint can be useful without the operator
// pressing anything.
func treeLines(panes []registry.Pane, aliases project.Aliases, favourites map[string]bool,
	expanded map[string]bool, home string, localHost string) []treeLine {
	// The pinned band first, because that is what the operator asked for and because it is the one
	// group whose membership they chose by hand.
	var out []treeLine
	var pinned []registry.Pane
	for _, p := range panes {
		if favourites[MarkKey(p)] {
			pinned = append(pinned, p)
		}
	}
	if len(pinned) > 0 {
		node := &treeNode{label: "FAVOURITES", key: favouritesKey, rows: pinned}
		// ALWAYS OPEN on the first paint, deliberately, and NOT through openByDefault: the pinned band
		// is the one group the operator chose by hand, and they asked for it "straight away" while
		// everything else is "by navigation". A band of finished favourites is still the answer to
		// "which sessions do I care about", where a directory of finished work is not.
		open := expanded == nil || expanded[favouritesKey]
		out = append(out, treeLine{Key: favouritesKey, Label: "FAVOURITES", Sum: node.rollUp(),
			Pinned: true, Expandable: true, Open: open})
		if open {
			for _, p := range pinned {
				out = append(out, treeLine{Depth: 1, Key: MarkKey(p), Pane: p, IsRow: true,
					Pinned: true})
			}
		}
	}

	// One volume per host, in the order the fleet reports them, so the tree does not invent a host
	// ordering the rest of the product does not have.
	hosts := map[string]*treeNode{}
	var hostOrder []string
	for _, p := range panes {
		if favourites[MarkKey(p)] {
			// Already shown above. A row in two places at once is the duplicate the operator
			// reported on the flat list, and it would be worse here: two lines, one session, and
			// two cursor positions that act on the same thing.
			continue
		}
		h := hosts[p.Host]
		if h == nil {
			h = &treeNode{label: p.Host, key: p.Host + ":"}
			hosts[p.Host] = h
			hostOrder = append(hostOrder, p.Host)
		}
		node := h
		for _, seg := range strings.Split(strings.TrimPrefix(p.Path, "/"), "/") {
			if seg == "" {
				break // no path at all: the row hangs off its volume, like a loose file
			}
			node = node.child(seg, node.key+"/"+seg)
		}
		node.rows = append(node.rows, p)
	}
	for _, h := range hostOrder {
		if home != "" && h == localHost {
			// HOME folds to `~`, as it does on a pinned row — and only on the LOCAL volume, because
			// `~` on another machine is THAT user's home and the hub does not know it. The launch
			// form makes the same distinction for the same reason: it resolves `~` for a local host
			// and refuses it for a remote one.
			foldHome(hosts[h], home)
		}
		// The host's CHILDREN collapse, never the host itself: a volume is not a directory, and
		// letting it merge produced `nuc/home/dev/lab/streams/qa/ansible-20260818` as one line — the
		// machine and the path in one label, which is exactly the confusion `local/tmp` versus
		// `nuc/tmp` cost the operator on the flat list.
		for _, seg := range hosts[h].order {
			hosts[h].kids[seg].collapse()
		}
		out = append(out, flatten(hosts[h], 0, expanded, aliases)...)
	}
	return out
}

// foldHome rewrites the one child chain that spells the home directory out into `~`.
//
// It works on the TRIE and before the collapse, so the fold is a relabelling of existing nodes rather
// than string surgery on a label that may already be a join of several — and a directory called
// `/opt/home/dev` is untouched, because the walk only follows the home path's own segments.
func foldHome(host *treeNode, home string) {
	segs := strings.Split(strings.TrimPrefix(home, "/"), "/")
	if len(segs) == 0 || segs[0] == "" {
		return
	}
	// Every node ON THE WAY must be a pure link — no sessions of its own and exactly one child —
	// or folding would hide something. Two things this guard got wrong on the first try, both found
	// by looking at the printed tree rather than by reading: it tested the rows of the HOST, where a
	// session with no path legitimately hangs (`tmp-1e`), so the fold never fired at all; and it
	// replaced the host's WHOLE child map, which would have dropped every other tree on that volume.
	node := host
	for i, seg := range segs {
		next := node.kids[seg]
		if next == nil {
			return // the fleet does not live under home on this volume
		}
		if i > 0 && (len(node.rows) > 0 || len(node.order) > 1) {
			return // a session or a sibling sits on the way, and folding would hide it
		}
		node = next
	}
	// Re-hang the home node under the volume as `~`, replacing ONLY the first segment's child.
	node.label = "~"
	delete(host.kids, segs[0])
	host.kids["~"] = node
	for i, seg := range host.order {
		if seg == segs[0] {
			host.order[i] = "~"
		}
	}
}

// openByDefault is which nodes the first paint shows the inside of: THE MAP IS OPEN, THE FOLDERS ARE
// SHUT. A node with child directories is open, so the whole shape of the fleet is on the screen; a leaf
// — a directory holding sessions and no directories — is closed, so its sessions are one key away.
//
// The operator's own words for what they wanted: the pinned band "straight away", and "everything else
// by navigation". Measured on their fleet, everything-open drew 54 session rows under twenty nodes and
// six levels of indentation, so a 40-row screen showed a quarter of the fleet and neither of the other
// two volumes; this rule draws every volume and every directory in about twenty lines, with the counts
// saying where the work is.
//
// Opening on "whatever has waiting work" was tried first and is a weaker version of the same idea: it
// opened the leaf that held two asking sessions AND the six finished ones beside them, which is the
// screen this rule exists to avoid. `a` is the fast path either way — on a closed node it opens it and
// lands on the longest-waiting session inside — so nothing is further away than one key.
func openByDefault(n *treeNode) bool { return len(n.order) > 0 }

// flatten walks one node into lines, children before rows: a directory's subdirectories are its
// structure and its sessions are its contents, and a reader looking for structure should not have to
// scroll past twenty files to find the next folder.
func flatten(n *treeNode, depth int, expanded map[string]bool, aliases project.Aliases) []treeLine {
	sum := n.rollUp()
	hasKids := len(n.order) > 0 || len(n.rows) > 0
	// ABSENT means "whatever the default says", not "closed" — see openByDefault. Everything-open was
	// the first rule and it does not survive the real fleet: 54 sessions drew 54 rows plus twenty nodes
	// under six levels of indentation, so the screen the hub opens on showed a quarter of the fleet, and
	// the operator's own words for what they wanted were "favourites straight away, and everything else
	// by navigation".
	//
	// The map therefore holds only what the OPERATOR has decided, and every other node follows the rule.
	// That is what removes a whole coupling: the map used to be SEEDED on first use so that the first
	// press would move one node instead of collapsing a tree nobody had expanded, and the seed was built
	// from the WHOLE fleet while the screen draws the narrowed rows — 8 entries for a tree drawing 2
	// nodes, with a filter on. A review found that divergence; making absence mean the default deletes
	// the seed rather than fixing it, and no filter can make the two disagree because there is only one
	// source left.
	//
	// The volume was ALSO forced open by a `depth == 0` clause here, and that made `enter` and `h` on a
	// volume do nothing at all — a key that silently fails is what a broken key looks like, and this
	// screen answers every other key it does not act on.
	open := openByDefault(n)
	if v, decided := expanded[n.key]; decided {
		open = v
	}
	host, path := nodeAddress(n.key)
	line := treeLine{Depth: depth, Key: n.key, Label: n.label, Sum: sum,
		Expandable: hasKids, Open: open && hasKids, Host: host, Path: path}
	out := []treeLine{line}
	if !line.Open {
		return out
	}
	kids := make([]*treeNode, 0, len(n.order))
	for _, seg := range n.order {
		kids = append(kids, n.kids[seg])
	}
	// project.MoreUrgent, so the tree, the project list and the dashboard answer "which of these
	// first" by calling one function rather than by three comments agreeing.
	sort.SliceStable(kids, func(i, j int) bool {
		a, b := kids[i].rollUp(), kids[j].rollUp()
		a.Group = project.Group{Label: kids[i].label}
		b.Group = project.Group{Label: kids[j].label}
		return project.MoreUrgent(a, b)
	})
	for _, c := range kids {
		out = append(out, flatten(c, depth+1, expanded, aliases)...)
	}
	rows := make([]registry.Pane, len(n.rows))
	copy(rows, n.rows)
	// The dashboard's own order, by the same function the fleet is sorted with, so a session that has
	// waited longest leads inside its directory exactly as it leads inside the flat list (§21.11.1).
	registry.SortByAttention(rows)
	for _, p := range rows {
		out = append(out, treeLine{Depth: depth + 1, Key: MarkKey(p), Pane: p, IsRow: true})
	}
	return out
}

// renderTree draws the flattened view.
//
// Every drawn line is one treeLine, so the window is `lines[first:first+count]` and `A` selecting the
// ROWS in that window cannot disagree with what was painted — the property the grouped list needs
// `extraAbove`, `rowPrefixCost` and two calibrated guards to keep.
//
// A ROW inside the tree is the shortest shape in the product: `state name`, with no host (the volume
// above says it) and no path (the directory above says it). That is the tree paying for its own
// indentation. The pane id appears under the same rule as everywhere else (rowIdentity), so two
// sessions that draw one label inside one directory are still told apart.
//
// The state word is NOT in one column here, and that is a departure from §21.11's rule for the flat
// list — there the eye runs down the state, here it runs down the structure, and the aligned column
// this screen offers instead is the node's fact cell at the right edge.
func renderTree(tls []treeLine, width, height, cursor int, aliases project.Aliases,
	colliding map[[2]string]bool, home string) []string {
	if width < 20 || height < 1 {
		return nil
	}
	// The fact cell is the one aligned column, and it is the project list's own cell so the two
	// screens report attention in one vocabulary.
	cell := treeCellWidth
	if width < 60 {
		cell = 10
	}
	out := make([]string, 0, height)
	for i := 0; i < len(tls) && len(out) < height; i++ {
		l := tls[i]
		point := " "
		if i == cursor {
			point = ">"
		}
		indent := strings.Repeat("  ", l.Depth)
		if l.IsRow {
			name, own := aliases.DisplayName(l.Pane)
			if l.Pinned {
				// THE PINNED SHAPE, asked for in the operator's own words: "the record under
				// FAVOURITES special/short — {status} name @host:path". Here it is not a flourish but
				// the only thing that says where the row lives: the band above it is a list, not a
				// place, so unlike every other row on this screen there is no volume and no directory
				// over it to read. `favouriteRow` is the flat list's own function, so the two screens
				// draw one shape, and `room` is MEASURED from the prefix rather than counted — a
				// hand-counted constant is what made this row one column too wide the first time.
				head := fmt.Sprintf("%s %s%s %-6s ", point, indent, l.Pane.State().Glyph(),
					stateWord(l.Pane))
				out = append(out, lines.TruncateMarked(head+favouriteRow(l.Pane, name, own, home,
					width-lines.Width(head)), width))
				continue
			}
			if own {
				name = "» " + name
			}
			row := fmt.Sprintf("%s %s%s %-6s %s", point, indent, l.Pane.State().Glyph(),
				stateWord(l.Pane), name)
			if id := rowIdentity(l.Pane, aliases, colliding); id != "" {
				row += "  " + id
			}
			out = append(out, lines.TruncateMarked(row, width))
			continue
		}
		arrow := " "
		if l.Expandable {
			arrow = "▸"
			if l.Open {
				arrow = "▾"
			}
		}
		// A TRAILING SLASH, which is `ls -F`'s convention and here it is a disambiguator as well: the
		// state glyph for `idle` is `▸`, the same character a CLOSED node uses, so `▸ idle tmp-1e` and
		// `▸ ~/lab/streams` began at the same two characters. The slash says "this line is a place"
		// where the arrow only says "this line has children". The favourites node is not a directory
		// and does not take one.
		label := l.Label
		if !l.Pinned {
			label += "/"
		}
		head := fmt.Sprintf("%s %s%s %s", point, indent, arrow, label)
		// The cell is right-aligned, and the LABEL is what yields when the two meet: a node's label is
		// a directory name the operator can shorten by looking one line up, and the cell is the answer
		// they came for.
		room := width - cell - 2
		if room < 8 {
			out = append(out, lines.TruncateMarked(head, width))
			continue
		}
		head = lines.Truncate(head, room)
		pad := room - lines.Width(head)
		if pad < 0 {
			pad = 0
		}
		out = append(out, head+strings.Repeat(" ", pad)+"  "+project.Cell(l.Sum, cell))
	}
	return out
}

// treeBodyHeight and treeListHeight are how tall this screen's body and list are, and they are
// FUNCTIONS because `A` has to ask the same question the paint asks.
//
// `A` selects what is on screen and the operator acts on what was drawn, so the two numbers must come
// from one place — the defect this repo has fixed twice, once when an estimate claimed twelve of
// twenty-five rows and once when the details band took a row without telling the viewport. The chrome
// here is THREE rows (a title, the rule, the footer) where the dashboard's is two, which is why
// `bodyHeight` and `inboxHeight` cannot be reused directly; `listHeight` can, and is.
func treeBodyHeight(height int) int {
	const chrome = 3 // title + rule + footer
	if height <= chrome {
		return 0
	}
	return height - chrome
}

func treeListHeight(height int) int {
	bodyH := treeBodyHeight(height)
	if bodyH < 1 {
		return 0
	}
	if h := listHeight(bodyH); h > 0 {
		return h
	}
	return bodyH
}

// treeBandSubjects is which sessions the band shows: every MARKED row, or the row under the cursor
// alone — the rule `selected` states for the dashboard, restated over tree LINES because the tree's
// cursor indexes lines and not panes.
//
// A NODE under the cursor yields nothing, and that is right rather than a gap: a directory has no
// screen to show, and inventing one would mean picking a session the operator did not point at. The
// band then draws nothing and the pad keeps the frame exactly as tall as the terminal.
func treeBandSubjects(tls []treeLine, cursor int, marked map[string]bool) []registry.Pane {
	var sel []registry.Pane
	for _, l := range tls {
		if l.IsRow && marked[MarkKey(l.Pane)] {
			sel = append(sel, l.Pane)
		}
	}
	if len(sel) == 0 && cursor >= 0 && cursor < len(tls) && tls[cursor].IsRow {
		sel = append(sel, tls[cursor].Pane)
	}
	return sel
}

// nodeTile is the band's tile for a DIRECTORY, and it exists because the cursor is on a directory for
// most of this screen's lines.
//
// Without it the band was BLANK whenever the cursor was not on a session — measured at the committed
// 80x24, three of twenty-four rows saying nothing on the screen the hub opens on, and the operator's eye
// losing the anchor the dashboard's band gives it. The list's height never changed (treeListHeight
// subtracts the band whether or not it draws), so this is not the moving-top-edge defect; it is five
// columns of blank in the place the screen answers "what am I pointing at".
//
// It is not RenderTile and must not be: that tile's body is a copy of another program's screen and its
// head names a pane. A directory has neither. What it has is an ADDRESS and a ROLL-UP, which is the pair
// the operator is deciding on, and the address is worth a line of its own because the label above is
// folded (`~`) and collapsed (`a/b/c` for three nodes) while a launch needs the real path. The box
// grammar is the same, and a test pins the two tiles to one width so they cannot drift apart.
func nodeTile(l treeLine, inside []registry.Pane, aliases project.Aliases,
	colliding map[[2]string]bool, width, height int) []string {
	w := min(width, MaxTileWidth)
	if w < 12 || height < 3 {
		return nil
	}
	inner := w - 2
	what := l.Label
	switch {
	case l.Pinned:
		what = l.Label + " (what you pinned)"
	case l.Host != "" && l.Label == l.Host:
		what = l.Label + " (volume)"
	case l.Host != "":
		what = l.Host + " " + l.Label
	}
	head := "─ " + what + " "
	if lines.Width(head) > inner {
		// MARKED, for the reason RenderTile's head is: this is the tile's own words about which line
		// the cursor is on, and a silently shortened path is one the operator would read as complete.
		head = lines.TruncateMarked(head, inner)
	}
	out := []string{"┌" + head + strings.Repeat("─", max(0, inner-lines.Width(head))) + "┐"}

	// ONE body line at the band's usual height, so it is a priority list rather than a sentence that
	// gets cut: the census first — it is the reason to open the directory at all — then the real path.
	var parts []string
	parts = append(parts, plural(l.Sum.Total, "session", "sessions"))
	if l.Sum.Waiting > 0 {
		parts = append(parts, fmt.Sprintf("%d asking", l.Sum.Waiting))
	}
	if l.Sum.Broken > 0 {
		parts = append(parts, fmt.Sprintf("%d broken", l.Sum.Broken))
	}
	if l.Path != "" {
		parts = append(parts, l.Path)
	}
	if l.Pinned {
		parts = append(parts, "f pins a session, and it moves here")
	}
	rows := []string{lines.Fit(inner-2, " · ", parts...)}
	// THE REST OF THE BOX NAMES WHAT IS INSIDE, which is the whole reason a closed node is allowed to
	// be closed: the counts say how much wants you and these say which sessions they are, so opening
	// the directory is a choice rather than the only way to find out. The rows arrive in the fleet's
	// own attention order, so the first ones are the longest-waiting — and they are the sessions under
	// this node whether it is open or closed, because they come from the FLEET and not from the drawn
	// lines (a closed node contributes none of those).
	for _, p := range inside {
		if len(rows) >= height-2 {
			// The last row is REPLACED by the tally, so the session that was on it is no longer
			// listed and has to be counted among the remainder: `len(rows) - 2` is the census line
			// plus the row being given up. Measured before this was right: six sessions listed three
			// and claimed `… and 2 more`, which is five.
			if listed := len(rows) - 2; len(inside) > listed {
				rows[len(rows)-1] = lines.Truncate(fmt.Sprintf("… and %d more",
					len(inside)-listed), inner-2)
			}
			break
		}
		name, _ := aliases.DisplayName(p)
		// THE SAME COLLISION RULE THE LIST ROWS USE. Measured on the operator's own fleet the day the
		// tile landed: the pinned band's tile listed `ansible-ci-ops` twice and `envoy-ops` twice, two
		// rows apiece with nothing to tell them apart — which is the report this whole screen came
		// from ("I think I am seeing duplicates"). rowIdentity is the one place that decides when an
		// id earns its columns, so the tile asks it rather than restating it.
		row := fmt.Sprintf("%s %-6s %s", p.State().Glyph(), p.State().String(), name)
		if id := rowIdentity(p, aliases, colliding); id != "" {
			row += "  " + id
		}
		rows = append(rows, lines.Truncate(row, inner-2))
	}
	for _, r := range rows {
		out = append(out, "│ "+r+strings.Repeat(" ", max(0, inner-1-lines.Width(r)))+"│")
	}
	for len(out) < height-1 {
		out = append(out, "│"+strings.Repeat(" ", inner)+"│")
	}
	return append(out, "└"+strings.Repeat("─", inner)+"┘")
}

// panesUnder is the sessions inside a node, in the FLEET's own order.
//
// Two callers: the directory tile, which names them, and `a`, which goes to the first waiting one. ONE
// function because "inside this node" is one question — a second copy would be a second answer, and the
// screen would then name sessions the key refuses to reach.
//
// From the fleet and not from the drawn lines, because a CLOSED node contributes no lines at all and a
// closed node is exactly where "what is in here" is worth saying. The fleet arrives in attention order,
// so the first ones are the longest-waiting — the same order the node's own children are sorted by.
//
// The path test is a PREFIX at a boundary, not `strings.HasPrefix` alone: `/a/st` is not inside
// `/a/streams`, and that pair is a real directory on this machine.
func panesUnder(l treeLine, panes []registry.Pane, favourites map[string]bool) []registry.Pane {
	var out []registry.Pane
	for _, p := range panes {
		if l.Pinned {
			if favourites[MarkKey(p)] {
				out = append(out, p)
			}
			continue
		}
		if favourites[MarkKey(p)] {
			continue // drawn in the pinned band, and counted there too
		}
		if p.Host != l.Host {
			continue
		}
		if l.Path == "" || p.Path == l.Path ||
			(len(p.Path) > len(l.Path) && strings.HasPrefix(p.Path, l.Path) && p.Path[len(l.Path)] == '/') {
			out = append(out, p)
		}
	}
	return out
}

// treeCellWidth is what a node's fact cell gets, and it is the project list's own figure so a node and
// a project row report the same counts in the same columns.
const treeCellWidth = 16

// RenderTreeScreen is the whole screen: a title that says where you are and how to leave, the tree,
// and the footer the dashboard already builds.
//
// It is a MODE and not a Grouping, which is the shape the project list has for the same reason: a
// Grouping changes the headers and never the ORDER (its own doc comment says so), and this changes the
// order into a hierarchy. Reusing `v` for it would have made one key mean two different kinds of thing.
//
// The window is windowStart over the flat lines — the same helper the project list uses — because every
// drawn line costs exactly one row here. That is the payoff of building the tree flat: no per-row cost
// walk, no prefix rows, and `A` selecting the ROWS of the window cannot disagree with the paint.
func RenderTreeScreen(f Frame) []string {
	tls, cursor := f.Tree, f.TreeCursor
	width, height := f.Width, f.Height
	if width < 20 || height < 4 {
		return []string{"terminal too small"}
	}
	// THE CENSUS IS THE FLEET'S, not the screen's. Counted over the panes this frame was given —
	// narrowed, since that is what every surface counts — and NOT over the drawn lines: closing a
	// directory then took the head from `7 sessions · 4 asking` to `3 sessions · 1 asking`, so the one
	// line that says how much work exists reported the operator's own fold as sessions going away. A
	// closed node is the point of this screen, and a count that shrinks when you close one is a count
	// nobody can navigate by.
	waiting, total := 0, 0
	for _, p := range f.Panes {
		total++
		if waitsForOperator(p) {
			waiting++
		}
	}
	// THE LEGEND IS A PRIORITY LIST, not one sentence truncated.
	//
	// Truncated, at 60 columns it read `enter opens, a goes to what wait` — cut mid-word, with no
	// mark, which is this repo's oldest defect class (keeping the label and losing the action). The
	// order is what must survive first: the census, then the two keys that get the operator OUT (esc)
	// and IN (enter), then the rest, and `lines.Fit` says `+N` for what it dropped.
	//
	// It is also the ONLY legend on this screen. `t` used to leave a note saying the same sentence,
	// which cost the footer its fleet health at 60 and 80 columns — the M1 defect, paid to repeat a
	// line already on the screen.
	// The separator is the footer's own `" · "` and not a wider one: measured at the 80 columns §16
	// commits to, five columns per gap costs this line the whole `a goes to what waits` clause.
	head := lines.Fit(width, " · ",
		plural(total, "session", "sessions"),
		fmt.Sprintf("%d asking", waiting),
		// The order the eye wants, and both fit at every width §16 commits to. Measured: 60 columns
		// carries both, and only at 50 — below anything this project promises — does `lines.Fit` have
		// room for one, which is then the way IN. That is the trade taken deliberately: reading order
		// at the sizes the operator has against a way out at a size the screen was never sized for,
		// where `esc` and `q` are the habits anyway.
		"enter opens",
		"esc leaves",
		"a goes to what waits",
		"n new session here",
		"/ finds",
		"q quits")
	out := []string{head, separator(width)}

	// THE BODY IS WHAT IS LEFT AFTER THIS SCREEN'S OWN CHROME, which is three rows and not the two
	// `bodyHeight` assumes — a title, the rule, and the footer. Asking `inboxHeight` for the terminal
	// height would claim one row too many and push the frame past the bottom, which is why the shared
	// arithmetic is `listHeight(bodyH)` and takes the body.
	bodyH := treeBodyHeight(height)
	if bodyH < 1 {
		return out
	}
	// The band is the dashboard's, through the same three functions, so the tile a session gets here is
	// the tile it gets there — including the fallbacks for a pane with nothing captured and for a row
	// with no pane at all (§17). `selected` semantics: the marked rows, or the cursor's row alone.
	// ONE map for the whole frame. Both the band's tile and the list ask which labels collide, and each
	// was building it — the same walk over the same panes twice per paint. Cheap either way at sixty
	// sessions, and it is one ANSWER rather than two: the id a row shows and the id the tile shows must
	// come from the same verdict, or the two surfaces disagree about which rows need telling apart.
	colliding := collidingLabels(f.Panes, f.Aliases)
	bandH := detailsHeight(bodyH)
	var band []string
	if bandH > 0 {
		band = renderTiles(treeBandSubjects(tls, cursor, f.Marked), nil, 0, width, bandH, f.Aliases)
		if len(band) == 0 && cursor >= 0 && cursor < len(tls) && !tls[cursor].IsRow {
			// The cursor is on a DIRECTORY and nothing is marked, which is the commonest state of
			// this screen. The pane tiles win when there are any, because they are the send's
			// targets and §7 requires the operator to be looking at what they are sending to.
			band = nodeTile(tls[cursor], panesUnder(tls[cursor], f.Panes, f.Favourites), f.Aliases,
				colliding, min(width, MaxTileWidth), bandH)
		}
	}
	listH := treeListHeight(height)
	first := windowStart(cursor, len(tls), listH)
	last := first + listH
	if last > len(tls) {
		last = len(tls)
	}
	out = append(out, renderTree(tls[first:last], width, listH, cursor-first, f.Aliases,
		colliding, f.Home)...)
	// Pad so the band sits on the body's LAST rows whatever the list did — the property
	// details_test.go pins for the dashboard, and the reason the top edge does not move with the
	// list's length.
	for len(out) < height-1-len(band) {
		out = append(out, "")
	}
	out = append(out, band...)
	for len(out) < height-1 {
		out = append(out, "")
	}
	// The same footer claimants as the dashboard, so the fleet's health and the narrowings do not
	// disappear because the operator changed view.
	// hostLine, the same claimant list the dashboard's footer uses, with the note and the narrowings
	// leading it: a view change must not cost the operator the one positive statement about fleet
	// health, which is the defect known-issues M1 records for the dashboard's own footer.
	var lead []string
	if f.Note != "" {
		lead = append(lead, f.Note)
	}
	// narrowingLine and not `Filters.sentence()`: while the keyword field has focus this row belongs
	// to the field, caret and all. A copy of the dashboard's two lines is what left this screen drawing
	// the applied-filter sentence under a cursor the operator could not see.
	if s := narrowingLine(f, width); s != "" {
		lead = append(lead, s)
	}
	out = append(out, hostLine(f.Hosts, f.Marked,
		hiddenTally{Marked: f.HiddenCount, Waiting: f.BlockedCount, AllShown: f.ShowingHidden},
		width, lead...))
	return out
}
