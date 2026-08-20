package ui

import (
	"fmt"
	"strings"

	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
	tea "github.com/charmbracelet/bubbletea"
)

// The filesystem view's SCREEN: its cursor, its keys, and the one place it is opened from.
//
// tree.go builds and draws; this decides what the keys mean. The split is the one the project list
// already has, and it is what keeps the drawing testable without a model.

// treeCursor names the LINE the cursor is on by IDENTITY, with the last position as the fallback.
//
// Not an index. The fleet re-sorts under every poll and a node opens or closes under the operator's own
// hand, so a stored index points at a stranger the moment either happens — the defect this repo removed
// from the dashboard's cursor and then found again in `clampCursor`'s four callers. The hint is what
// answers when the line has genuinely gone: a closed node's rows, or a session that ended.
type treeCursor struct {
	key  string
	hint int
}

// treeIndex resolves the cursor against the lines currently drawn.
//
// The key first, the hint second, and zero last. The hint is CLAMPED rather than trusted: closing a
// node can make the list shorter between two paints, and a hint past the end would put the cursor
// nowhere — which on this screen means every key silently doing nothing, the exact shape the agents
// poll produced before the dashboard's cursor was keyed.
func (m model) treeIndex(tls []treeLine) int {
	if m.treeCur.key != "" {
		for i, l := range tls {
			if l.Key == m.treeCur.key {
				return i
			}
		}
	}
	if m.treeCur.hint >= len(tls) {
		if len(tls) == 0 {
			return 0
		}
		return len(tls) - 1
	}
	if m.treeCur.hint < 0 {
		return 0
	}
	return m.treeCur.hint
}

// treeTo is the ONLY writer of the tree cursor, so a caller cannot set the key and forget the hint —
// which is exactly what happened to the dashboard's cursor the day a launch wrote it by hand.
func (m model) treeTo(tls []treeLine, i int) model {
	if i < 0 {
		i = 0
	}
	if i >= len(tls) {
		i = len(tls) - 1
	}
	if i < 0 {
		m.treeCur = treeCursor{}
		return m
	}
	m.treeCur = treeCursor{key: tls[i].Key, hint: i}
	return m
}

// treeShown is the lines the tree screen draws, from the same narrowed set the dashboard paints.
//
// rowsForScreen and not the whole fleet: every narrowing the operator turned on applies here too, and
// the footer says which — a view that quietly ignored the keyword would make `/` mean two things.
func (m model) treeShown() []treeLine {
	rows, _ := m.rowsForScreenLoose()
	return treeLines(rows, m.aliases, m.markedFavourites(), m.treeOpen, m.home, m.localLabel())
}

// markedFavourites is the pinned set in the shape the tree and the renderer both take.
func (m model) markedFavourites() map[string]bool {
	out := map[string]bool{}
	for _, p := range m.panes {
		if m.isFavourite(p) {
			out[MarkKey(p)] = true
		}
	}
	return out
}

// localLabel is which volume `~` may be folded on: the host whose tmux server is this machine's.
//
// Asked of the HOSTS rather than assumed to be called `local`, because the label is the operator's to
// choose in hosts.toml — and `Host.IsLocalServer` is the same question the launch form asks before it
// resolves a `~` at all.
func (m model) localLabel() string {
	for _, h := range m.hosts {
		if h.IsLocalServer() {
			return h.Label
		}
	}
	return ""
}

// treeKey is what the keys mean on the filesystem view.
//
// The set is deliberately small: move, open, close, go to what waits, leave. Everything that acts on a
// SESSION answers by naming what is missing rather than doing nothing, which is the rule the project
// list already follows — "nothing happened" is what a broken key looks like.
func (m model) treeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	tls := m.treeShown()
	at := m.treeIndex(tls)
	var here treeLine
	if at < len(tls) {
		here = tls[at]
	}
	switch msg.String() {
	case "j", "down":
		return m.treeTo(tls, at+1), nil
	case "k", "up":
		return m.treeTo(tls, at-1), nil
	case "enter", "l", "right":
		if here.IsRow {
			m.note = "this is a session — a goes to it, esc leaves the tree"
			return m, nil
		}
		if !here.Expandable {
			m.note = "nothing inside " + here.Label
			return m, nil
		}
		m = m.setTreeOpen(here.Key, !here.Open)
		return m, nil
	case "h", "left":
		if !here.IsRow && here.Open {
			return m.setTreeOpen(here.Key, false), nil
		}
		// Otherwise go OUT: to the nearest line above that is shallower, which is this line's
		// parent. A key that closed nothing and moved nowhere would read as broken.
		for i := at - 1; i >= 0; i-- {
			if tls[i].Depth < here.Depth {
				return m.treeTo(tls, i), nil
			}
		}
		return m, nil
	case "a":
		if here.IsRow {
			return m.goTo(here.Pane)
		}
		// On a NODE: go to the session under it that has waited longest, opening every directory on
		// the WAY — which is what `a` means on the project list, and what the head line promises on
		// every frame of this one.
		//
		// The target comes from the FLEET and the path is opened from the target, not the other way
		// round: the first version opened this node one level and scanned the drawn lines, which
		// worked only while every node was open by default. With the map open and the folders shut it
		// found nothing and the cursor did not move — the key silently doing nothing, on the promise
		// the screen makes most often.
		rows, _ := m.rowsForScreenLoose()
		for _, p := range panesUnder(here, rows, m.markedFavourites()) {
			if !waitsForOperator(p) {
				continue
			}
			m = m.openPathTo(p)
			opened := m.treeShown()
			for i, l := range opened {
				if l.IsRow && l.Key == MarkKey(p) {
					return m.treeTo(opened, i), nil
				}
			}
			break
		}
		m.note = "nothing under " + here.Label + " is waiting"
		return m, nil
	case "esc", "t":
		// The NOTE goes too, because every note on this screen is about THIS screen — four of them name
		// a key and say "enter opens it" — and a sentence about the tree read on the dashboard is false
		// the moment it is read. It was measured through the interface once, when `t` itself set a note
		// naming these keys and an e2e case waiting for the dashboard could not tell it had arrived;
		// that note has since gone (the head line is the legend), and the rule is the durable half.
		m.mode, m.note = modeBrowse, ""
		return m, nil
	case " ":
		// MARK, the same function the dashboard's `space` calls, which keeps `sel` and `atSelection`
		// in lockstep — the two halves live in `mark` so they cannot come apart.
		if !here.IsRow {
			m.note = notASession("space", "marks")
			return m, nil
		}
		m.mark(here.Pane)
		// The identify runs NOW rather than on the next tick, for the reason the dashboard's
		// `space` gives: a pane selected between two ticks holds no token yet, and the operator's
		// first send would otherwise wait for one.
		return m, m.identify()
	case "A":
		// SELECT WHAT IS ON SCREEN, from the same window arithmetic the paint uses. On the dashboard
		// this key reads `rowsOnScreen`, which bakes in the inbox's per-row cost walk and the cursor's
		// index into `rowsForScreen` — neither describes this screen, so the tree answers the same
		// question with its own two functions and they are the ones RenderTreeScreen calls.
		rows := m.treeRowsOnScreen()
		if len(rows) == 0 {
			m.note = "nothing on screen to select — open a directory first"
			return m, nil
		}
		for _, p := range rows {
			if !m.sel.Has(selKey(p)) {
				m.mark(p)
			}
		}
		return m, m.identify()
	case "C":
		m.sel.Clear()
		m.atSelection = map[SelectionKey]paneSnapshot{}
		m.note = "selection cleared"
		return m, nil
	case "x":
		// The SUBJECT rule is the dashboard's — the selection if there is one, otherwise the row under
		// the cursor — through the function that states it once.
		if !here.IsRow && m.sel.Len() == 0 {
			m.note = notASession("x", "hides")
			return m, nil
		}
		return m.hidePanes(m.hideSubjectsFrom(here.Pane, here.IsRow)), nil
	case "X":
		m.showHidden = !m.showHidden
		if m.showHidden {
			m.note = "showing every row, hidden ones marked"
		} else {
			m.note = "hidden rows are off the screen again"
		}
		return m, nil
	case "f":
		// Deliberately the CURSOR's row and never the selection, which is the one place this key
		// differs from `x` on purpose (§21 records the reason).
		if !here.IsRow {
			m.note = notASession("f", "pins")
			return m, nil
		}
		return m.toggleFavouriteSessionOf(here.Pane, true), nil
	case "N":
		if !here.IsRow {
			m.note = notASession("N", "names")
			return m, nil
		}
		return m.openNaming(here.Pane)
	case "n":
		// CREATE ONE HERE, which is the gesture the filesystem metaphor was asked for: on a directory
		// the form opens with that directory already in it, so making a session in a project is one
		// key rather than a typed path. On a session it means what it means on the dashboard — beside
		// that row — and the row's own cwd is the sensible default for a sibling.
		if here.IsRow {
			return m.openLaunchFormFor(here.Pane.Host, paneSessionTarget(here.Pane), here.Pane.Path), nil
		}
		// A VOLUME is a legitimate place to create one: it has no directory, but it is a HOST, and the
		// form's first field is the host. Refusing it (which this did at first) made the operator walk
		// into some directory they did not want just to change machine, and the form is editable
		// anyway — a pre-filled path is a default, not a decision. Only the favourites band, which has
		// no address at all, has nothing to offer the form.
		if here.Host == "" {
			m.note = "nothing to create here — the favourites band is a list, not a directory"
			return m, nil
		}
		return m.openLaunchFormFor(here.Host, "", here.Path), nil
	case "/":
		// The keyword field, over THIS screen: the tree already paints the narrowed set (the footer
		// says which), so the one thing missing was a way to start the search without leaving.
		m.searchBefore = m.search.Text()
		m.note = ""
		return m.openSearch(), nil
	case "q":
		// The hub QUITS from here, and that is not optional: this is the default screen, so a `q`
		// that did nothing would leave the operator with no way out of the program from the first
		// thing they see.
		return m, tea.Quit
	case "i", "!", "R", "K":
		// SELECTION keys, and they need no subject from this screen at all: each reads `m.sel` and
		// refuses an empty one with its own sentence. Handing them off unchanged is the whole reason
		// they were left alone — a second copy of "exactly one selected row" or of the mixed-selection
		// refusal is a second chance to get them wrong.
		return m.selectionKey(msg)
	}
	m.note = fmt.Sprintf("%s does nothing here — j/k move, enter opens, h closes, a goes to what "+
		"waits, esc leaves", keyName(msg))
	return m, nil
}

// notASession is what a session key answers with when the cursor is on a directory.
//
// FOUR keys need this sentence — `space`, `x`, `f`, `N` — and the fifth will, which is the tell that it
// is a function rather than a phrase: a repeated string is a place for the next author to write a
// slightly different one. It names the key, what the key is for, and the one key that changes the answer,
// because a refusal that does not carry its remedy is an apology.
func notASession(key, verb string) string {
	return key + " " + verb + " a session, and this is a directory — enter opens it"
}

// treeRowsOnScreen is the SESSIONS the tree is painting right now, which is what `A` selects.
//
// It asks treeListHeight and windowStart — the two functions RenderTreeScreen itself calls — so the
// selection and the paint cannot drift. Node lines are skipped: `A` selects sessions, and a directory
// is not one.
func (m model) treeRowsOnScreen() []registry.Pane {
	tls := m.treeShown()
	listH := treeListHeight(m.height)
	if listH < 1 || len(tls) == 0 {
		return nil
	}
	first := windowStart(m.treeIndex(tls), len(tls), listH)
	last := first + listH
	if last > len(tls) {
		last = len(tls)
	}
	var out []registry.Pane
	for _, l := range tls[first:last] {
		if l.IsRow {
			out = append(out, l.Pane)
		}
	}
	return out
}

// openPathTo opens every directory between a volume and one session, so that session's row is drawn.
//
// The keys are built in tree.go as `host:` for a volume and `parent + "/" + segment` below it, so the
// path's own prefixes ARE the ancestor keys and no tree walk is needed. A prefix that names no node —
// the intermediate steps of a COLLAPSED chain, whose line carries the deepest key — simply gains a map
// entry nobody reads, which is cheaper than teaching this function about the collapse.
func (m model) openPathTo(p registry.Pane) model {
	key := p.Host + ":"
	m = m.setTreeOpen(key, true)
	for _, seg := range strings.Split(strings.TrimPrefix(p.Path, "/"), "/") {
		if seg == "" {
			break
		}
		key += "/" + seg
		m = m.setTreeOpen(key, true)
	}
	return m
}

// setTreeOpen records ONE decision the operator made about ONE node.
//
// Nothing is seeded. An absent key means "whatever `openByDefault` says", so the map holds only what has
// actually been pressed and the first `h` closes one node rather than collapsing a tree nobody expanded.
//
// It used to SEED itself on first use, from `treeLines(m.panes, …)` — the whole fleet — while the screen
// draws `rowsForScreenLoose()`. A review found the divergence and it was measurable: with a filter on, the
// map held 8 nodes for a tree drawing 2. Nothing visibly wrong came out of it, and the fix is still to
// delete the seed rather than to align it, because two sources of the same answer is the coupling.
func (m model) setTreeOpen(key string, open bool) model {
	if m.treeOpen == nil {
		m.treeOpen = map[string]bool{}
	}
	m.treeOpen[key] = open
	return m
}

// waitsForOperator is the one predicate for "this row wants somebody", and it is the same pair the
// node counts roll up (Needs and Error) so a node that says `⚑ 2` cannot lead to a row this refuses.
func waitsForOperator(p registry.Pane) bool {
	return p.State() == state.Needs || p.State() == state.Error
}
