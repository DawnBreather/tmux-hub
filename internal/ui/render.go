// Package ui renders the dashboard. Layout thresholds and the tile width bound
// come from a prototype rendered against real pane content (docs/design.md §16
// and prototypes/layout.py), which corrected two assertions made without it.
package ui

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/DawnBreather/tmux-hub/internal/broadcast"
	"github.com/DawnBreather/tmux-hub/internal/history"
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/launch"
	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
)

type Layout int

const (
	InboxOnly Layout = iota
	InboxOneTile
	InboxGrid
)

func (l Layout) String() string {
	switch l {
	case InboxOnly:
		return "inbox-only"
	case InboxOneTile:
		return "inbox+tile"
	default:
		return "inbox+grid"
	}
}

// Grouping is what the inbox files rows under.
//
// It changes the HEADERS and the inline rows, never the ORDER: the dashboard exists to put what wants
// the operator first, and a view that gathered each project's rows together would bury a waiting
// session inside a quiet project. So a project's header comes round twice when the sort brings its
// rows round twice, marked `(cont.)` exactly as a session's header already is.
type Grouping int

const (
	// ByHost files a row under `HOST SESSION`, which is where the fleet lives.
	ByHost Grouping = iota
	// ByProject files it under the project §21 derives — the same vocabulary the project list and
	// the aliases use, so the two screens name the same things the same way.
	ByProject
)

// byHost and byProject are the package-internal names, so the model and its tests read as prose
// while the exported pair stays available to the harnesses that build a Frame by hand.
const (
	byHost    = ByHost
	byProject = ByProject
)

func (g Grouping) String() string {
	if g == ByProject {
		return "per-project"
	}
	return "per-host"
}

const (
	// InboxWidth is the inbox column when tiles share the screen.
	InboxWidth = 28
	// MaxTileWidth bounds a tile so surplus width becomes more columns rather
	// than a 130-column tile holding 30-column content.
	MaxTileWidth = 72
)

// LayoutFor picks a layout for a terminal width. 80x24 is the size to hold, not
// a degraded case.
func LayoutFor(width int) Layout {
	switch {
	case width < 100:
		return InboxOnly
	case width < 160:
		return InboxOneTile
	default:
		return InboxGrid
	}
}

// Nested reports whether the hub is running inside tmux, which changes what a
// user must press to leave a session they attached to from here.
func Nested() bool { return os.Getenv("TMUX") != "" }

// MarkKey is the identity used for selection: a pane id is unique only within
// one server, so the host is part of it.
func MarkKey(p registry.Pane) string { return p.Host + "\x00" + p.PaneID }

// RenderInbox renders the pane list. When inlineHostSession is set the host and
// session share the pane's row instead of taking a header row of their own.
// aliases resolves what each row is CALLED. It is threaded rather than looked up because
// §21.12 rule 6 gives one displayName and requires every surface to call it: a screen that
// resolved a name itself could show a different one from the next screen. Nothing derived is
// stored on the row — the alias key for a pane row IS its tmux session name, so writing a
// resolved name back onto Session would move the key the moment a row was named.
func RenderInbox(panes []registry.Pane, width, height, cursor int, marked map[string]bool, inlineHostSession bool, resurfaced, hiddenRows, favourites map[string]bool, aliases project.Aliases) []string {
	return renderInbox(panes, width, height, cursor, marked, inlineHostSession, resurfaced,
		hiddenRows, favourites, aliases, ByHost, nil, "")
}

// renderInbox is RenderInbox plus the view. The exported one keeps its shape because the scale
// harness and half this package's frames call it, and they are all about the host view.
// collidingLabels are the (host, name) pairs that MORE THAN ONE row draws — the only rows whose id
// tells the operator anything.
//
// Reported as "я не понимаю %1, %5, %3". The id was on every row on the argument that it is "the
// identity the history log records and the only way to tell two claude panes in one session apart".
// The second half is true and the first was doing no work on screen: the log is a different surface.
// Measured on the operator's own fleet — 60 rows, 54 sessions — five sessions put two or more rows
// up, so **49 of the 60 rows carried an id that distinguished nothing**. Of those five, three were
// the hub's own windows (now not rows at all, see ownFurniture) and two were pane-less rows whose id
// is an `agent:…` string rather than a `%N`, leaving exactly one session where a `%N` does any work.
//
// So the id appears where it disambiguates and nowhere else, which also means it comes BACK by
// itself the day two claude panes share a session — the case the old argument was really about, and
// the one where two rows would otherwise be identical byte for byte.
//
// Keyed on the DISPLAY NAME and not on the tmux session, which is the correction an adversarial
// review earned. `(host, session)` is a proxy for "would these two rows read the same", and it is
// wrong in the direction that hides a collision: measured on the operator's own fleet, two PANE-LESS
// rows read `local/20260818--cicd` — same host, same name, same state word, same kind word, different
// Claude uuids (`1b0cacf2`, `30f3382b`) — and neither carried an id, because an agent row's id was
// never drawn at all. Two sessions asking the operator for input, drawn byte for byte alike. Keying
// on what the row DRAWS answers the question the operator actually has.
//
// NOT keyed on the state word as well, though that would spare an id on a pair that differs by state:
// a state changes on a tick, and a row's SHAPE must not flicker with it. Erring toward showing the id
// is also the safe direction — the defect being fixed is one that hid it.
//
// Counted over every row the renderer was handed rather than the visible window, for the reason
// headerlessRows gives: scrolling must not change a row's shape.
func collidingLabels(panes []registry.Pane, aliases project.Aliases) map[[2]string]bool {
	seen := map[[2]string]int{}
	for _, p := range panes {
		name, _ := aliases.DisplayName(p)
		seen[[2]string{p.Host, name}]++
	}
	out := map[[2]string]bool{}
	for k, n := range seen {
		if n > 1 {
			out[k] = true
		}
	}
	return out
}

// rowIdentity is the id a ROW shows, and it is one function because three row shapes print it.
// The TILE is not one of them: it names one pane the operator has chosen, so it always shows the id.
//
// WHICH id depends on the kind, and both are the id that KEYS the operator's next command: a pane row
// carries tmux's `%N`, which `send-keys -t` and the history log take, and a pane-less row carries the
// short Claude id, which `claude logs`/`claude attach` take and which the door appends to the session
// it creates. The full uuid would be neither — measured in §22.6, `claude logs <full uuid>` answers
// `No job matching` while the short id resolves.
func rowIdentity(p registry.Pane, aliases project.Aliases, colliding map[[2]string]bool) string {
	name, _ := aliases.DisplayName(p)
	if !colliding[[2]string{p.Host, name}] {
		return ""
	}
	if p.Kind == registry.KindAgent {
		if p.AgentID != "" {
			return p.AgentID
		}
		// A LISTING RECORD WITH NO JOB ID STILL HAS A UUID, and without this fallback the rule is
		// half-blind for exactly the rows that need it. Measured on the operator's own fleet, through
		// the keyword filter, after the first version of this rule shipped:
		//
		//	>  ▸ idle   nuc/20260701-ops-schdev3-envoy-access-logs--compact
		//	   ✓ done   nuc/20260701-ops-schdev3-envoy-access-logs--compact  bce0212b
		//
		// One of a colliding pair carried its id and the other did not, because `claude agents`
		// reports no id for an `interactive` record — which is the same fact §22.11's fold rests on
		// ("the row worth keeping is the one with an ID"). `Claude()` and not SessionID, because which
		// field carries the uuid differs by kind and this repo has one place that answers that.
		return launch.ShortID(p.Claude())
	}
	return p.PaneID
}

// conversationPane is the pane a conversation is running, for the row that leads with its NAME.
//
// It is LAST and it is one function, for the reason the inline row shape already gives: the id is
// the least meaningful field to a person and therefore the right thing for truncation to eat
// first. A row whose label nothing else draws contributes nothing here (rowIdentity), so the shape
// does not move for most of this fleet.
//
// The COMMAND is deliberately absent. A door pane's command is the `sh` wrapping `claude attach`,
// so printing it tells the operator their Claude session is a shell.
func conversationPane(p registry.Pane, aliases project.Aliases, colliding map[[2]string]bool) string {
	if id := rowIdentity(p, aliases, colliding); id != "" {
		return "  " + id
	}
	return ""
}

// favouriteRow is the shape of a row the operator PINNED, and it leads with the NAME.
//
// An unpinned row leads with where it lives (`local/api`), which is right when the operator is
// scanning a fleet they did not choose. A pinned row is one they chose: they know what it is, so the
// question its row answers is not "what is this" but "which one" — the name first, then the address.
//
//	★⚑ needs  billing-cicd @local:~/lab/streams/orbits/billing-iac
//
// The PATH is on it, and the argument that keeps an id off an ordinary row (a field that
// distinguishes nothing is a column nobody can read) does not apply here for two measured reasons:
// there are SIX of these rows and not sixty, and the path is the row's ADDRESS — two favourites can
// carry one name in two checkouts, which is the case a person cannot resolve from the name alone.
//
// The path is the field that YIELDS, and it yields from the LEFT with `…`, because the last segments
// are what identify a checkout. Measured on the operator's own four favourites: 73–100 columns whole,
// 65–92 with HOME folded to `~`, so one of the four needs the cut at the size §16 commits to. The
// NAME never yields — it is the identity, and this repo has paid three times for a layout that kept
// a label and dropped the thing the operator acts on.
func favouriteRow(p registry.Pane, name string, own bool, home string, room int) string {
	if own {
		name = "» " + name
	}
	if p.Path == "" {
		// Nothing to address it by: fall back to what every headerless row says. A row that
		// invented a path would be worse than one that says only where it lives.
		return p.Host + "/" + name
	}
	// `~` for HOME, which is what the operator writes and what saves the eight columns that decide
	// whether the path fits at 80. Only a LEADING match, so a directory called `/opt/home/dev` is
	// left alone.
	//
	// HOME arrives as an ARGUMENT and is not read from the environment here: the first version called
	// os.UserHomeDir() inside the renderer, which puts an environment-dependent value in the one
	// function whose output this repo diffs byte for byte to prove a refactor moved nothing. The rule
	// already written down for the mockup generator — no time.Now(), no random, no absolute paths —
	// belongs to the renderer for the same reason.
	path := p.Path
	if home != "" && strings.HasPrefix(path, home) {
		path = "~" + strings.TrimPrefix(path, home)
	}
	head := name + " @" + p.Host + ":"
	// `room` is what the CALLER measured as left after the columns before the name, and it is passed
	// in rather than computed from a counted constant: the first version subtracted a hand-counted
	// eleven for the point, mark, star, glyph and state word, the real figure is twelve, and the row
	// came out one column too wide — so the OUTER truncate then cut the path from the right as well,
	// losing the last segment, which is the only part that names the checkout. A constant that counts
	// somebody else's columns drifts the moment they change; a measured prefix cannot.
	room -= lines.Width(head)
	if room < 8 {
		// No room to say anything true about the path: say nothing rather than a fragment that
		// names a directory the operator does not have.
		return head[:len(head)-1]
	}
	if lines.Width(path) > room {
		for lines.Width(path) > room-1 && path != "" {
			_, size := utf8.DecodeRuneInString(path)
			path = path[size:]
		}
		path = "…" + path
	}
	return head + path
}

// lonelyName is what a row says when no header says it for them.
//
// It carries everything the header would have carried and everything the grouped row shape would
// have: the name, then the pane id, then the command for anything that is not a conversation. So
// dropping the header for a group of one loses NOTHING — which is the whole argument for dropping
// it. A conversation's command is left out for the reason conversationPane gives: a door pane's
// command is the `sh` wrapping `claude attach`.
func lonelyName(p registry.Pane, name string, own bool, aliases project.Aliases,
	colliding map[[2]string]bool) string {
	if own {
		name = "» " + name
	}
	// THE HOST TOO, because the header this row replaces said `HOST SESSION` and the sort can drop this
	// row INSIDE another group's run. Reported as "I see duplicates now", and reproduced byte for byte:
	//
	//	NUC TMUX-HUB-DEMO
	//	>  ✱ quiet  %1   claude
	//	   ✱ quiet  tmp  %20  claude                 ← this row is on LOCAL
	//	   ✱ quiet  20260817-cicd-30f3382b  %19      ← so is this one
	//	NUC TMUX-HUB-DEMO  (cont.)
	//	   ✱ quiet  %0   bash
	//
	// Two rows of another host drawn under nuc's header, and the header coming back as `(cont.)` — which
	// reads as the same session twice. Nothing was duplicated; the header was claiming rows it does not
	// own. The list is ordered by ATTENTION and not grouped, so a group of one lands wherever its state
	// puts it and there is no ordering fix that keeps §21.11's promise.
	//
	// So the row says where it lives, in the shape the narrow band already uses for the same job
	// (`local/api %10`) — one convention for "this row carries its own location", not two.
	out := p.Host + "/" + name + conversationPane(p, aliases, colliding)
	if !p.IsConversation() && p.Command != "" {
		out += "  " + p.Command
	}
	return out
}

// sessionBreak is the row that says the rows below it are NOT in the group above.
//
// The list is ordered by ATTENTION and not grouped, so a row no header speaks for lands wherever its
// state puts it — including inside another session's run. Indentation under a header reads as
// membership, and it read that way to the operator: two local rows drawn under `NUC TMUX-HUB-DEMO`,
// with the header returning as `(cont.)`, reported as "I see duplicates now". The rows say where
// they live (lonelyName) and this says where the group ENDED, which is the half a row cannot say.
//
// Dashed and not solid: every tile line and the footer rule use `─`, and several tests separate the
// inbox from the other surfaces by exactly that character. It is deliberately SHORT rather than
// full-width — a full-width rule inside the list reads as the end of the list.
//
// Aligned with the state glyph at column 3, which is where the eye already runs down.
func sessionBreak(width int) string {
	return lines.Truncate("   ┄┄ other sessions ┄┄", width)
}

// groupLabelOf is the ONE place a row becomes a header string, so the header the renderer draws and
// the count that decides whether to draw it cannot disagree.
//
// The HOST is a label and stays upper-cased; the operator's OWN name is shown as they typed it,
// because shouting a name back at the person who chose its capitalisation contradicts the `»`
// marker that says it is theirs. A DERIVED name keeps the upper case it always had, so no unnamed
// row's frame moves.
func groupLabelOf(p registry.Pane, aliases project.Aliases, groupBy Grouping, groups map[string]string) string {
	if groupBy == ByProject {
		// The project's own label, upper-cased like a host header so the two views draw the same
		// SHAPE — a header that changed weight as well as content would read as a different
		// screen. A row whose project could not be derived gets its own header rather than
		// falling under the previous one, which would file it under somebody else's project.
		label := groups[MarkKey(p)]
		if label == "" {
			label = "unassigned"
		}
		return strings.ToUpper(label)
	}
	name, own := aliases.DisplayName(p)
	if own {
		return strings.ToUpper(p.Host) + " » " + name
	}
	return strings.ToUpper(p.Host) + " " + strings.ToUpper(name)
}

// headerlessRows are the rows whose group would hold exactly ONE row, so no header is drawn for
// them and each carries its own name instead.
//
// A header over a single row says nothing the row does not, and this fleet is now mostly such
// groups: the door gives every session it opens a tmux session of its own, so eight pinned
// conversations drew eight headers with a nameless `%3   sh` under each — reported as "они не
// выглядят переименованными, также они выглядят как отдельные проекты". The rule is about the
// GROUP and not about the row's kind, which is what makes it survive the door: a session that
// gains a pane changes kind, and the size of its group does not.
//
// Counted over EVERY row rather than the visible window, so scrolling cannot change a row's shape.
// Keyed on the header STRING, because an alias on one pane of a shared session gives that row a
// header of its own and the two rows are then two groups of one.
func headerlessRows(panes []registry.Pane, aliases project.Aliases, groupBy Grouping,
	groups map[string]string, favourites map[string]bool) map[string]bool {
	if groupBy == ByProject {
		// NOT in the project view. There the header answers the question the view was opened to
		// ask — which project — and the row never says it, so a project with one session would
		// lose its label entirely. In the HOST view the header answers "which tmux session", and
		// for a session the door made that is the row itself.
		return nil
	}
	sizes := map[string]int{}
	for _, p := range panes {
		if favourites[MarkKey(p)] {
			// A pinned row carries its own address (favouriteRow) and takes no header, so it must
			// not inflate a group and rob its unpinned sibling of one — the same reason an agent row
			// is skipped below.
			continue
		}
		if p.Kind == registry.KindAgent && groupBy != ByProject {
			// These never take a header at all — their name IS the row — so they must not
			// inflate a group and rob a real session of its header.
			continue
		}
		sizes[groupLabelOf(p, aliases, groupBy, groups)]++
	}
	alone := map[string]bool{}
	for label, n := range sizes {
		if n == 1 {
			alone[label] = true
		}
	}
	return alone
}

func renderInbox(panes []registry.Pane, width, height, cursor int, marked map[string]bool, inlineHostSession bool, resurfaced, hiddenRows, favourites map[string]bool, aliases project.Aliases, groupBy Grouping, groups map[string]string, home string) []string {
	// The list scrolls to keep the cursor on screen. Without this, pressing j
	// past the bottom row moved a cursor nobody could see — measured with 30
	// panes on a 24-row terminal, where the cursor simply left the screen.
	//
	// From inboxWindow, which is also what `A` reads: the two used to come from one ESTIMATE of
	// how many headers the list would spend rows on, and an estimate is a different number.
	first, _ := inboxWindow(panes, cursor, height, aliases, groupBy, groups, inlineHostSession,
		favourites)

	var rows []string
	lastGroup := ""
	// seenGroups is separate from lastGroup: lastGroup answers "is this a new run of rows",
	// and this answers "has this session had a header already" — the two differ exactly when
	// the sort brings a session round a second time.
	seenGroups := map[string]bool{}
	alone := headerlessRows(panes, aliases, groupBy, groups, favourites)
	colliding := collidingLabels(panes, aliases)
	for i, p := range panes {
		if i < first {
			// Keep group tracking honest so the first visible row still gets its
			// header when the list is scrolled into the middle of a session.
			lastGroup = p.Host + " " + p.Session
			continue
		}
		// A ROW AND ITS PREFIX FIT TOGETHER OR NEITHER IS DRAWN.
		//
		// The loop used to test `len(rows) >= height` here and then again after appending a
		// header or a break, which let the prefix take the last row of the screen and leave its
		// own row undrawn. Measured, three panes at 120 columns: at height 6 the last line was
		// `┄┄ other sessions ┄┄` with nothing under it, and at height 4 — with the group second —
		// it was `LOCAL PAIR`. A header or a break with no row below it announces sessions that
		// are not on the screen, which is the one thing this band exists to stop.
		//
		// The header half predates the break and was found by widening the same probe: the fix is
		// one rule rather than two guards, and it is the rule extraAbove already states — a row
		// costs itself plus whatever is drawn above it. Testing the WHOLE cost here is what makes
		// the renderer and the viewport agree by construction instead of by two matching bounds.
		if len(rows)+rowPrefixCost(p, lastGroup, len(rows), inlineHostSession, alone, aliases,
			groupBy, groups, favourites[MarkKey(p)])+1 > height {
			break
		}
		mark := " "
		if marked[MarkKey(p)] {
			mark = "◆"
		}
		point := " "
		if i == cursor {
			point = ">"
		}
		// A PINNED ROW HAS ITS OWN SHAPE, in every band and above every other branch.
		//
		// favouritesFirst lifts every pinned row above every other one, so the pinned band is a
		// contiguous run at the top of the list — which is what lets these rows be self-describing
		// with no header over them: `lastGroup` is empty for the first of them and stays empty, so
		// neither a header nor a break is drawn inside the band, and the first UNPINNED row after it
		// takes its own header. headerlessRows and rowPrefixCost are told the same thing, or the
		// viewport would count a header the renderer never draws.
		if favourites[MarkKey(p)] {
			name, own := aliases.DisplayName(p)
			// The prefix is MEASURED and its width handed on, so the path is sized against the room
			// that actually remains. Composing the row and letting the outer truncate sort it out is
			// what cut the path from both ends at once.
			prefix := fmt.Sprintf("%s%s%s%s %-6s ", point, mark, favColumn(p, favourites),
				p.State().Glyph(), stateWord(p))
			row := prefix + favouriteRow(p, name, own, home, width-lines.Width(prefix))
			row += rowMarkers(p, resurfaced, hiddenRows)
			rows = append(rows, lines.TruncateMarked(row, width))
			lastGroup = ""
			continue
		}
		if inlineHostSession {
			// Ordered by what a person needs first: state, then what is running,
			// then where it lives, then the id. The id is last because it is the
			// least meaningful to a human and so the right thing for truncation
			// to eat — but it stays on the row, because it is the identity the
			// history log records and the only way to tell two claude panes in
			// one session apart.
			// DisplayName here too. This branch is the ONLY shape below 100 columns — the
			// 80×24 §16 calls "the size to hold, not a degraded case" — and it used to print
			// the raw session while the tile beneath it printed the operator's chosen name,
			// so one screen carried TWO names for one row. §21.12 rule 6 forbids exactly
			// that in terms, and this function's own doc comment cites the rule as the
			// reason `aliases` is threaded in; the grouped branch below honoured it and this
			// one did not. Every naming test ran at 100 or wider, so the whole band was
			// untested for names.
			inlineName, inlineOwn := aliases.DisplayName(p)
			if inlineOwn {
				inlineName = "» " + inlineName
			}
			// WHERE the row lives, in the vocabulary the current view uses. Below 100 columns there
			// are no headers at all, so a view that only changed headers would do nothing at the
			// one size §16 commits to.
			where := p.Host + "/" + inlineName
			if groupBy == ByProject {
				where = groups[MarkKey(p)]
				if where == "" {
					where = "unassigned"
				}
			}
			row := fmt.Sprintf("%s%s%s%s %-6s %-8s %s",
				point, mark, favColumn(p, favourites), p.State().Glyph(), stateWord(p), p.Command,
				where)
			if p.Kind == registry.KindAgent {
				// Same column for the state as the pane row above, reached the same
				// way: the state leads and the name follows. Nothing is padded into
				// the command column, because an agent row has no command to name and
				// the name needs every column it can get.
				//
				// `where` and not the bare name, which is the same correction the grouped shape got:
				// below 100 columns NO row has a header speaking for it, so every row has to say
				// where it lives — and this was the one shape that did not, so `» ansible-ci-ops`
				// on a two-host fleet named no host at all.
				row = fmt.Sprintf("%s%s%s%s %-6s %s", point, mark, favColumn(p, favourites),
					p.State().Glyph(), stateWord(p), where)
			}
			// AFTER the agent branch, because that branch rebuilds the row from scratch and an id
			// appended before it was silently discarded — so a pane-less row could not be told from
			// its twin at 80 columns while it could at 120.
			if id := rowIdentity(p, aliases, colliding); id != "" {
				row += " " + id
			}
			row += rowMarkers(p, resurfaced, hiddenRows)
			rows = append(rows, lines.TruncateMarked(row, width))
			continue
		}
		// The separator, decided BEFORE either headerless branch draws, because both of them are
		// the same shape — a row no header speaks for — and a rule written twice is a rule that will
		// be got wrong once. extraAbove carries the identical test (`i > first` there is
		// `len(rows) > 0` here) so the viewport counts the row this draws; if the two disagree, `A`
		// selects a pane the operator cannot see.
		if lastGroup != "" && len(rows) > 0 &&
			(p.Kind == registry.KindAgent && groupBy != ByProject ||
				alone[groupLabelOf(p, aliases, groupBy, groups)]) {
			rows = append(rows, sessionBreak(width))
			lastGroup = ""
		}
		if p.Kind == registry.KindAgent && groupBy != ByProject {
			// A Claude session with no pane needs no session header in the HOST view: its NAME is
			// the row, and giving each one a header of its own made the list half
			// headers.
			//
			// The PROJECT view is the opposite case and falls through instead. There a header
			// gathers MANY rows, and pane-less agent rows are most of what this fleet has — 40 of
			// 43 on the machine this was built on — so skipping them would have left the view
			// grouping the three rows that have panes and nothing else.
			//
			// State first, then the name. The pane row below puts state in the same
			// place, which is what makes the column scannable — and the ALIGNMENT was
			// achieved by moving the pane row's state here rather than by giving this
			// row the pane row's id and command columns. That was measured: holding
			// those columns leaves 4 of the inbox's 29 for the name, and a session
			// called `s04` identifies nothing, while the name is the only thing that
			// identifies an agent to a person (§17) — the reason these rows carry no
			// header at all.
			// One displayName, and the marker says the name is the OPERATOR's: a screen
			// must not pass a derived name off as a chosen one (§21.12 rule 6).
			//
			// Through lonelyName, the SAME function the group-of-one row uses, because these
			// are ONE shape: a row that no header speaks for. Two copies is two chances to
			// forget the host, and that is exactly what happened — the group-of-one row was
			// fixed for the "I see duplicates" report and this one, drawn from the same run
			// of the same loop, would have gone on inheriting the header above it.
			name, own := aliases.DisplayName(p)
			row := fmt.Sprintf("%s%s%s%s %-6s %s", point, mark, favColumn(p, favourites),
				p.State().Glyph(), stateWord(p), lonelyName(p, name, own, aliases, colliding))
			row += rowMarkers(p, resurfaced, hiddenRows)
			rows = append(rows, lines.TruncateMarked(row, width))
			lastGroup = ""
			continue
		}
		name, own := aliases.DisplayName(p)
		group := groupLabelOf(p, aliases, groupBy, groups)
		// A group of ONE takes no header, and its row carries the name the header would have
		// held. A header over a single row says nothing the row does not — and since the door
		// gives every session it opens a tmux session of its own, this is now most of the
		// fleet: eight pinned conversations drew eight headers with a nameless `%3   sh`
		// under each.
		if alone[group] {
			row := fmt.Sprintf("%s%s%s%s %-6s %s", point, mark, favColumn(p, favourites),
				p.State().Glyph(), stateWord(p), lonelyName(p, name, own, aliases, colliding))
			row += rowMarkers(p, resurfaced, hiddenRows)
			rows = append(rows, lines.TruncateMarked(row, width))
			// The header is not drawn, so a LATER row of another group must still get its own.
			lastGroup = ""
			continue
		}
		if group != lastGroup {
			// A session's header can come round TWICE, because the list sorts by attention
			// across sessions rather than grouping by session: `LOCAL API`, then
			// `NUC DEPLOY`, then `LOCAL API` again when a quieter pane of the first session
			// arrives. The order is correct and is the whole point of the screen — what
			// read as a bug was the second header appearing with nothing saying why
			// (known-issues S2).
			//
			// So the repeat is LABELLED rather than removed or reordered. Grouping within
			// an attention band would reorder the waiting block, which now carries the
			// longest-wait-first rule (§21.11.1); dropping the repeat would put a session's
			// rows under another session's header, which is worse than redundant.
			// The mark OUTRANKS the name's tail. Appending it and then truncating the
			// whole label lost it entirely at the inbox's fixed 28 columns — so for any
			// `HOST SESSION` at or over 28 the repeated header rendered byte-identically
			// to the first and the fix was invisible, and between 17 and 27 it rendered as
			// the fragment `  (co`. lines.Fit is not the tool: it drops from the TAIL, so
			// it would drop the mark and truncate to the same width.
			label := lines.Truncate(group, width)
			if seenGroups[group] {
				const mark = "  (cont.)"
				label = lines.Truncate(group, width-lines.Width(mark)) + mark
			}
			seenGroups[group] = true
			rows = append(rows, label)
			lastGroup = group
		}
		// A CONVERSATION under a PROJECT header keeps its own shape — its NAME is the row —
		// because a project header says which project and nothing about which session, so a row
		// that gave its columns to a pane id and a command would leave the operator unable to
		// tell two of their own sessions apart under one header.
		//
		// Under a SESSION header it does not, and the view is the whole difference: there the
		// header has already said the name, and what the row is for is telling the operator which
		// of that session's panes this is. A conversation ALONE in its session never reaches here
		// — it took the headerless branch above and leads with its name.
		if p.IsConversation() && groupBy == ByProject {
			shown := name
			if own {
				shown = "» " + name
			}
			row := fmt.Sprintf("%s%s%s%s %-6s %s", point, mark, favColumn(p, favourites),
				p.State().Glyph(), stateWord(p), shown) + conversationPane(p, aliases, colliding)
			row += rowMarkers(p, resurfaced, hiddenRows)
			rows = append(rows, lines.TruncateMarked(row, width))
			continue
		}
		// State before the id and the command, so it sits in the same column as an
		// agent row's — the list is SORTED by attention and scanned by it, and it
		// used to put the word fourth here and second there, which left the eye
		// nothing to run down. The id and command keep their order after it.
		//
		// This shape is the one place the id is nearly always drawn, and by construction rather
		// than by rule: a header exists only over a group of two or more rows, so a row that HAS
		// a header is a row whose session put more than one up, which is exactly when rowPaneID
		// answers. The `%-4s` stays for the rare disagreement (an alias on one pane of a shared
		// session makes two groups of one) so the command column does not move between rows.
		row := fmt.Sprintf("%s%s%s%s %-6s %-4s %s",
			point, mark, favColumn(p, favourites), p.State().Glyph(), stateWord(p),
			rowIdentity(p, aliases, colliding), p.Command)
		row += rowMarkers(p, resurfaced, hiddenRows)
		rows = append(rows, lines.TruncateMarked(row, width))
	}
	for len(rows) < height {
		rows = append(rows, "")
	}
	return rows
}

// RenderTile renders one pane's content lines inside a box, bounded by
// MaxTileWidth.
func RenderTile(p registry.Pane, width, height int, aliases project.Aliases) []string {
	w := width
	if w > MaxTileWidth {
		w = MaxTileWidth
	}
	if w < 8 || height < 3 {
		return nil
	}
	inner := w - 2
	// The tile is a surface too (§21.12 rule 6), so it shows what the row is CALLED — with
	// the marker, because a tile that showed a chosen name unmarked would be the one screen
	// where the operator could not tell whose name they were reading.
	tileName, tileOwn := aliases.DisplayName(p)
	if tileOwn {
		tileName = "» " + tileName
	}
	head := fmt.Sprintf("─ %s %s %s%s ", p.Host, tileName, p.PaneID, staleAge(p))
	if p.Kind == registry.KindAgent {
		head = fmt.Sprintf("─ %s %s ", p.Host, tileName)
	}
	if lines.Width(head) > inner {
		// MARKED, unlike the body below: the head is the tile's own words about which row this is,
		// and at 200 columns it cuts a name the LIST ROW above shows in full — so the operator needs
		// to know they are reading a short form. The body is a copy of another program's screen and
		// gets no marker, because adding a character there would put words in its mouth.
		head = lines.TruncateMarked(head, inner)
	}
	top := "┌" + head + strings.Repeat("─", inner-lines.Width(head)) + "┐"

	body := p.Content
	if len(body) == 0 && p.Kind == registry.KindPane {
		// A pane whose capture is EMPTY — a shell at a cleared prompt, a `sleep`, a program that
		// draws nothing — left a three-row box holding one line, and that line only repeated the
		// header. Measured on a real fleet at 80x24, where 3 of the 24 rows the project commits to
		// went to a box with nothing in it.
		//
		// So the tile falls back to CONTEXT: the four things the operator is deciding between rows
		// on, and none of which the row itself carries. It is a FALLBACK and not a replacement,
		// because when there IS a capture it is the most valuable thing on the screen — the tile is
		// how the operator reads `Do you want to proceed? ❯ 1. Yes` without leaving the hub.
		if p.Path != "" {
			body = append(body, "path:   "+p.Path)
		}
		if p.Window != "" {
			body = append(body, fmt.Sprintf("window: %d %s", p.WindowIndex, p.Window))
		}
		if p.Width > 0 && p.Height > 0 {
			// The pane's own size, which decides what an attach will look like and is the one field
			// here that cannot be guessed from anything else on the screen.
			body = append(body, fmt.Sprintf("size:   %dx%d", p.Width, p.Height))
		}
		if p.Command != "" {
			body = append(body, "running: "+p.Command)
		}
		if p.Dead {
			body = append(body, fmt.Sprintf("exited: %d", p.DeadStatus))
		}
		if len(body) == 0 {
			// Nothing known at all: say THAT rather than drawing an empty box, because a blank box
			// and a box the hub could not fill read the same and mean different things.
			body = []string{"nothing captured yet"}
		}
	}
	if len(body) == 0 && p.Kind == registry.KindAgent {
		// A session with no pane has no screen to show, so the tile carries what
		// IS known rather than an empty box: Claude's own word for its state, the
		// kind, and how long it has been going.
		body = []string{
			"state: " + p.State().String(),
			"kind:  " + p.Command,
		}
		if !p.Activity.IsZero() {
			// `started:`, not `since:`. For an AGENT row Activity is the session's
			// start time (registry sets it from agents.Session.StartedAt), while for a
			// PANE row it is that pane's own last output — and §21.11.1 adds a third
			// clock, how long a row has been WAITING, which is what the inbox now
			// orders by. Three meanings cannot share one word on one screen, and the
			// vague one was this label.
			body = append(body, "started: "+p.Activity.Format("2006-01-02 15:04"))
		}
		// AgentID first, because it is the id the VERBS take: measured, `claude logs <full
		// uuid>` answers `No job matching` while the listing's short id resolves. The tile
		// printed the uuid, so the one string on this screen that looks like something to copy
		// was the one that fails. The uuid stays as the fallback — an interactive row carries no
		// short id (measured 0 of 13 across two hosts) and the uuid is still the identity an
		// operator can match against `claude agents` output.
		if id := p.AgentID; id != "" {
			body = append(body, "id:    "+id)
		} else if p.SessionID != "" {
			body = append(body, "id:    "+p.SessionID)
		}
	}
	// The tile's own two border rows, top and bottom — NOT the screen's header and
	// footer, which is what bodyHeight counts. A tile of a given height is the
	// same tile wherever it is drawn, so this does not follow the screen chrome.
	if n := height - 2; len(body) > n {
		body = body[len(body)-n:]
	}
	rows := []string{top}
	for _, l := range body {
		t := lines.Truncate(l, inner)
		rows = append(rows, "│"+lines.Pad(t, inner)+"│")
	}
	// No padding to the requested height: an empty box reads as a broken tile,
	// while trailing blank screen rows read as spare room.
	if len(rows) == 1 {
		rows = append(rows, "│"+strings.Repeat(" ", inner)+"│")
	}
	rows = append(rows, "└"+strings.Repeat("─", inner)+"┘")
	return rows
}

// Frame is everything every screen needs, and it is a STRUCT because the positional form
// had grown two adjacent pairs whose members are interchangeable to the compiler: `note,
// hint string` and `hiddenCount, blockedCount int`, plus `width, height, cursor int`. Swap
// either member of any pair and the program builds and draws the wrong screen — the kind of
// defect no test finds unless it happens to assert the exact field that moved.
//
// Twelve positional arguments was the state before this; naming them makes the wrong call
// impossible to write rather than merely unlikely.
type Frame struct {
	Panes  []registry.Pane
	Hosts  []hub.Host
	Width  int
	Height int
	Cursor int
	Marked map[string]bool
	// Note is the answer to something the operator just did; Hint is the standing reminder
	// of how to come back from `a`. They were adjacent strings.
	Note string
	Hint string
	// Aside is a SECOND message that outranks the fleet but not the note. The picker's
	// hosts.toml complaint is the one caller: it is true until the file is rewritten, so it
	// must not be replaced by a transient note, and it must not be pre-fitted either — the
	// picker used to run its own lines.Fit and hand the RESULT here, where the footer fits
	// again, producing `+1 +1` on one row and a marker that no longer counts anything. One
	// Fit owns the row, so there is one count of what is missing.
	Aside string
	// Home is the operator's home directory, folded to `~` on a pinned row's path. It is a FRAME
	// value rather than something the renderer reads from the environment, because this renderer's
	// output is diffed byte for byte to prove a refactor moved no frame — the same rule that keeps
	// time.Now() out of the mockup generator.
	Home string
	// OwnHidden is how many rows the fleet dropped because they were the hub's own windows
	// (ownFurniture). It is on the HEADER and not in the hidden tally, because it answers a
	// different question: the tally is about rows the OPERATOR marked and can bring back with
	// `X`, and these are rows that were never the fleet. A count the operator cannot reconcile
	// with `tmux ls` is the defect this would otherwise trade for the one it closes.
	OwnHidden int
	// HiddenCount is how many panes are hidden and BlockedCount how many of those are
	// waiting anyway. They were adjacent ints, and swapping them reads as a fleet where
	// more panes wait than exist.
	HiddenCount  int
	BlockedCount int
	// ShowingHidden is `X`: the filter is off, so every marked row is on the screen. It changes
	// the SENTENCE the footer builds from the two counts above, never the counts — the model used
	// to zero them here instead, which left no surface able to say the screen was unfiltered.
	ShowingHidden bool
	// HiddenRows and Resurfaced are the two halves of "this row is marked hidden": HiddenRows is
	// the rows the filter would keep off the screen, Resurfaced the ones that came back because
	// they are asking. hide.Set makes them disjoint, so a row carries at most one marker.
	HiddenRows map[string]bool
	Resurfaced map[string]bool
	// Screen is WHICH SCREEN an overlay is drawn over, and the fields below it are what that screen
	// needs to draw itself. One field per screen, because `backdrop` answers "what is underneath" in
	// one place and every overlay renderer asks it.
	//
	// It was a BOOLEAN naming the tree, and an adversarial review found the live cost within the hour:
	// the project list can raise the naming overlay too (`N` on a project), `raise` recorded
	// `modeProjects` correctly, and `backdrop` read every non-tree screen as the dashboard — so naming
	// a project replaced the list the operator was reading, including the row being named, with a list
	// of sessions, while `dismiss` afterwards returned them to the list. The picture and the return
	// disagreed. A boolean can only ever name one screen; a mode names all of them, and a third screen
	// adds a case rather than a second boolean.
	//
	// The zero value is `modeBrowse`, which is the dashboard — the right default for a frame nobody
	// has told anything.
	Screen     uiMode
	Tree       []treeLine
	TreeCursor int
	Projects   ProjectView
	// Favourites is what the operator pinned, keyed like Marked and Resurfaced. It decides the ORDER
	// (the model lifts these rows) and the row's own column; both come from ONE predicate, because a
	// screen whose marker and order disagreed would be worse than either alone.
	Favourites map[string]bool
	// GroupBy is what the inbox files rows under, and Groups is each row's project LABEL keyed the
	// way Marked is. The label is threaded rather than derived here for the reason `Aliases` is:
	// deriving it in the renderer would let this screen file a row under a different project from
	// the one the project list shows it in.
	GroupBy Grouping
	Groups  map[string]string
	Aliases project.Aliases
	// Filters is what `*` and `/` are doing to the list, and Searching is whether the keyword
	// field has focus (docs/design.md §21.17).
	//
	// The field is a footer CLAIMANT rather than a row of its own, deliberately: a new row would
	// have to come out of the body, and the body's height is shared by the renderer and
	// InboxViewport through one function — the last thing that took a row without telling both
	// made `A` select panes seven rows below the fold. A claimant costs no arithmetic and
	// degrades through the same Fit as everything else on that row.
	Filters   filterTally
	Searching bool
}

// withHeight returns the frame with a smaller body, which is what every overlay needs: it
// draws the dashboard shorter and puts itself underneath.
func (f Frame) withHeight(h int) Frame { f.Height = h; return f }

// withNote returns the frame with a different note, for a screen that has its own.
func (f Frame) withNote(n string) Frame { f.Note = n; return f }

// withAside returns the frame with a second footer claimant, which only the picker has.
func (f Frame) withAside(a string) Frame { f.Aside = a; return f }

// Render composes the whole screen.
func Render(f Frame) string {
	// Destructured once rather than threaded as f.X through every line: the bodies below are
	// unchanged from the positional version, so this refactor cannot alter a frame — which is
	// the property the whole mockup corpus is the guard for.
	panes, hosts, width, height, cursor := f.Panes, f.Hosts, f.Width, f.Height, f.Cursor
	marked, note, hint := f.Marked, f.Note, f.Hint
	hiddenCount, blockedCount := f.HiddenCount, f.BlockedCount
	resurfaced, aliases := f.Resurfaced, f.Aliases
	hiddenRows := f.HiddenRows

	if width < 20 || height < 4 {
		return "terminal too small"
	}
	layout := LayoutFor(width)
	bodyH := bodyHeight(height)

	var out []string
	// By PRIORITY, not by concatenation: the title and the session count are the identity
	// and the hint is the explanation, so a hint that does not fit is dropped WHOLE rather
	// than cut. Cutting it kept the label and lost the keystroke — the only actionable part
	// — which is the same class as the footer losing a down host (known-issues S3, L2).
	// FitQuiet, not Fit: a dropped hint is a reminder the operator does not need told about,
	// and `+1` next to the session count would read as "one session is missing".
	// The view is named on the header when it is NOT the default, because a mode that changes what a
	// screen MEANS has to say so — the operator would otherwise infer it from a different set of
	// headers, which is the defect `X` had: a mode whose only evidence was the thing it changed.
	// FitQuiet keeps the identity first and drops the tail unmarked, so a narrow terminal loses the
	// hint before the view and the view before nothing.
	view := ""
	if f.GroupBy != ByHost {
		view = f.GroupBy.String()
	}
	own := ""
	if f.OwnHidden > 0 {
		// AFTER the view, which changes what the screen means, and BEFORE the hint, which is a
		// reminder: this sentence is the only thing that lets the operator reconcile the count
		// above with what tmux reports.
		own = fmt.Sprintf("%s of its own not listed", plural(f.OwnHidden, "window", "windows"))
	}
	out = append(out, lines.FitQuiet(width, "   · ",
		"tmux-hub  "+plural(len(panes), "session", "sessions"), view, own, hint))

	// ONE vertical stack at every width: the inbox on top with the whole terminal to itself, the
	// details band pinned to the BOTTOM.
	//
	// It used to depend on the band. Below 100 columns the tile was stacked underneath and rows had
	// the full width; at 100 and above the inbox was pinned to InboxWidth (28) with tiles to the
	// RIGHT, which left 28 columns for a point, a mark, a glyph, a six-column state word and the
	// name — about fifteen for the name. Measured, the same fleet at two widths:
	//
	//	 80 cols  > ⚑ needs  20260809--рендеринг-карты
	//	100 cols  > ⚑ needs  20260809--рендери ┌─ local sess1 %1 ─────────────…
	//
	// So a WIDER terminal showed LESS of the one field that identifies a session to a person, while
	// the tile beside it held an almost empty box across seventy columns. Non-monotonic layout is a
	// class this project already pays attention to in the footer; this was the same class in the body,
	// and `TestWiderNeverShowsLessOfAName` is the assertion that keeps it closed.
	//
	// The band's height is a pure function of the terminal's, so its top edge does not move as the
	// focused row's content changes — that is what "pinned" buys the operator's eye. Surplus WIDTH
	// still becomes tile columns, which is what the old grid layout was for.
	bandH := detailsHeight(bodyH)
	band := renderTiles(panes, marked, cursor, width, bandH, aliases)
	// The INLINE shape is for the narrow band — and NOT for the project view, at any width. Measured
	// on a real fleet at 80x24: pressing `v` changed exactly one word in the header and nothing else,
	// because the inline shape draws no group headers and an agent row has no "where" column, so
	// rows of one project were not even adjacent. `v` there was a key that appeared to do nothing.
	// The grouped shape answers the view's own question with a header per project, and it fits 80
	// columns perfectly well now that the band is at the bottom and the list has the whole width.
	inline := layout == InboxOnly && f.GroupBy != ByProject
	list := trimBlank(renderInbox(panes, width, inboxHeight(height), cursor, marked,
		inline, resurfaced, hiddenRows, f.Favourites, aliases, f.GroupBy, f.Groups, f.Home))
	out = append(out, list...)
	// Pad so the band sits on the body's last rows, whatever the list did.
	for len(out) < 1+bodyH-len(band) {
		out = append(out, "")
	}
	out = append(out, band...)

	// A note SHARES the row with the host line rather than replacing it (known-issues M1).
	// The priority is unchanged and was always right — a note answers something the operator
	// just did, so it outranks ambient status — but `if note != "" { foot = note }` made the
	// one positive assertion about fleet health VANISH at the exact moment they were acting:
	// measured at 80×24, `local up · nuc degraded:format` was off screen entirely while the
	// note showed, so a host in degraded:format was invisible.
	//
	// Fit puts the note first and keeps as much of the fleet as fits, marking the loss with a
	// `+N` when it cannot. That is the same rule the project list and the picker now follow,
	// so no screen teaches a different habit — and it is the third place on this branch where
	// a correct priority had been written as a REPLACEMENT, which is invisible, instead of as
	// a list, which can say what it dropped.
	//
	// The width goes into hostLine because the footer decides what to DROP, and that decision
	// is impossible without knowing the room (known-issues S1).
	// The narrowings claim the same row, after the note and before the picker's complaint: a
	// filtered list that does not SAY it is filtered lies about the size of the fleet, and while
	// the field has focus it outranks even the note, because the operator is typing into it.
	out = append(out, hostLine(hosts, marked, hiddenTally{Marked: hiddenCount, Waiting: blockedCount,
		AllShown: f.ShowingHidden}, width, note, narrowingLine(f, width), f.Aside))
	return strings.Join(out, "\n")
}

// narrowingLine is what the footer says about the narrowings, and it is the FIELD while the field has
// focus.
//
// TWO screens claim this row now, so it is a function rather than two lines repeated. Measured on the
// filesystem view before it was: the footer drew the applied-filter sentence (`"nev" · 2 of 7`) while
// the operator was typing, so the CARET and the two keys that end the field (`search: nev▏ · enter:
// keep · esc: cancel`) existed on the dashboard and nowhere else — a field whose end you cannot see is
// a field you cannot type in, which is the ruling the naming overlay had to learn.
//
// The COUNT travels with the field, not only after enter. The first version showed the keyword being
// typed and nothing about what it had done, so the operator could not tell a keyword that had narrowed
// the fleet to one row from one that matched nothing until they committed it. The list narrows LIVE, so
// the number is already true on the screen above; saying it costs ten columns and the field's own key
// hint is what yields for them (searchLine drops the hint first).
func narrowingLine(f Frame, width int) string {
	if f.Searching {
		return searchLine(f.Filters.Query, f.Filters.Kept, f.Filters.Total, width, f.Filters.Loose)
	}
	return f.Filters.sentence()
}

// detailsHeight is the rows the details band gets at the bottom of the body.
//
// A pure function of the body's height, and that is the point: a band whose height followed its
// CONTENT would move its own top edge every time the focused row changed, so the list above it would
// shift under the operator's eye between one keystroke and the next. Bounds, each with a reason:
//
//   - never more than a third of the body, because the list is what the screen is for;
//   - at least 5 rows where they exist, since a box is two borders and a box with one content row
//     says less than the row it came from;
//   - never so much that the list drops below 3 rows — and the SEPARATOR row counts against the band
//     here, because it is the band that put it there: `bodyH-4`, not `bodyH-3`, which is the
//     difference between a 10-row terminal showing 3 sessions and showing 2;
//   - and 0 when even that will not fit, so a very short screen is all list and no band.
func detailsHeight(bodyH int) int {
	h := bodyH / 3
	if h < 5 {
		h = 5
	}
	if h > 12 {
		h = 12
	}
	if h > bodyH-4 {
		h = bodyH - 4
	}
	if h < 3 {
		return 0
	}
	return h
}

// renderTiles lays the selected panes out in as many columns as the width
// allows. A single full-width column was the earlier behaviour and it wasted the
// screen: at 160 columns it produced one 130-column tile holding 30-column
// content, which is why tile width is bounded and surplus width becomes columns.
func renderTiles(panes []registry.Pane, marked map[string]bool, cursor, width, height int, aliases project.Aliases) []string {
	sel := selected(panes, marked, cursor)
	if len(sel) == 0 || width < 12 || height < 3 {
		return nil
	}
	colW := min(MaxTileWidth, width)
	cols := max(1, (width+1)/(colW+1))
	if cols > len(sel) {
		cols = len(sel)
	}
	// Give each row of tiles an equal share of the height, at least 3 rows.
	rowsOfTiles := (len(sel) + cols - 1) / cols
	tileH := max(3, min(10, height/max(1, rowsOfTiles)))

	var out []string
	for start := 0; start < len(sel); start += cols {
		end := min(start+cols, len(sel))
		var block [][]string
		tallest := 0
		for _, p := range sel[start:end] {
			t := RenderTile(p, colW, tileH, aliases)
			block = append(block, t)
			tallest = max(tallest, len(t))
		}
		for row := 0; row < tallest; row++ {
			var line strings.Builder
			for i, t := range block {
				if i > 0 {
					line.WriteString(" ")
				}
				cell := ""
				if row < len(t) {
					cell = t[row]
				}
				line.WriteString(cell)
				if pad := colW - lines.Width(cell); pad > 0 && i < len(block)-1 {
					line.WriteString(strings.Repeat(" ", pad))
				}
			}
			out = append(out, line.String())
			if len(out) >= height {
				return out[:height]
			}
		}
	}
	return out
}

func selected(panes []registry.Pane, marked map[string]bool, cursor int) []registry.Pane {
	var sel []registry.Pane
	for _, p := range panes {
		if marked[MarkKey(p)] {
			sel = append(sel, p)
		}
	}
	if len(sel) == 0 && cursor < len(panes) {
		sel = append(sel, panes[cursor])
	}
	return sel
}

func hostLine(hosts []hub.Host, marked map[string]bool, hid hiddenTally, width int, lead ...string) string {
	return footerLine(hosts, len(marked), hid, width, lead...)
}

// footerLine assembles the status line by PRIORITY rather than by concatenation.
//
// The defect it closes: reasons were inline and the whole line was hard-truncated, so a
// verbose reason on one host ate the room and the host that was DOWN vanished — at 80
// columns, the size §16 commits to, `dead down` was gone entirely and the cut landed
// mid-word (known-issues S1, class L2). The one positive assertion about fleet health lost
// the failing host first.
//
// The order, and each step earns its place:
//
//  1. Every host's LABEL and STATUS. A status line that omits the broken host is worse than
//     one that omits why, so these are the identity and go in first.
//  2. The COUNTS. They are about the operator's own selection, and a footer that silently
//     drops "3 marked" makes the next enter a surprise.
//  3. REASONS, greedily, while there is room — and a host that is NOT up gets its reason
//     first, because that is the actionable half. Keeping the label and losing the action is
//     exactly what went wrong.
//
// If even the identities do not fit, lines.Fit drops hosts from the tail and COUNTS them, so
// the line degrades by design instead of by byte count.
// lead are the claimants that come BEFORE the fleet — the note, then the picker's aside. They
// are passed in rather than fitted separately because the fleet must be sized against the room
// that is actually left: composing it for the full width and handing Fit one long part meant a
// note could only drop the fleet WHOLE, so at 80 columns `sent to 2 panes +1` replaced
// `sent to 2 panes · local up · nuc degraded:format · dead down`. It also made the footer
// non-monotonic — a wider terminal could show fewer hosts — because the composition was sized
// for a width the row did not have.
func footerLine(hosts []hub.Host, marked int, hid hiddenTally, width int, lead ...string) string {
	// ONE priority list, and that is the fix rather than a tidy-up. The first version
	// reserved the counts' full width before any host was considered — `room := width -
	// Width(counts)` — so at 80 columns, the size §16 commits to, `dead down` vanished while
	// the three identities were only 42 of the 80. The counts had absolute priority, which
	// is the exact inversion of what S1 asks for, in the code that declares S1 fixed.
	//
	// So the counts are PARTS like any other, placed after the hosts, and lines.Fit drops
	// from the tail and counts what it dropped. The order within them matters too: "N of
	// them waiting for input" is meaningless without "N hidden", so it goes last and
	// therefore drops first.
	parts := make([]string, 0, len(hosts)+len(lead)+3)
	for _, l := range lead {
		if l != "" {
			// A footer is ONE line, and nothing that reaches it is allowed to change that.
			// Measured: a two-line reason short enough to fit produced a two-line footer at
			// every width, which pushes the frame past the terminal's height. Both claimants
			// that carry text from another program — a note built from an error, and a host's
			// reason below — go through firstLineOf.
			parts = append(parts, firstLineOf(l))
		}
	}
	nLead := len(parts)
	for _, h := range hosts {
		parts = append(parts, h.Label+" "+h.Status.String())
	}
	nHosts := len(parts) - nLead
	if marked > 0 {
		parts = append(parts, fmt.Sprintf("→ %d marked", marked))
	}
	parts = append(parts, hid.parts()...)

	fits := func(ps []string) bool { return lines.Width(strings.Join(ps, " · ")) <= width }
	if !fits(parts) {
		return lines.Fit(width, " · ", parts...)
	}

	// Everything fits, so spend what is left on REASONS — a failing host's first, because
	// that is the actionable half. Applied one at a time and reverted the moment one does
	// not fit, so a single verbose reason cannot cost a later host its own.
	//
	// Three tiers, and the middle one is why this list exists rather than one loop:
	//
	//  1. A host that is NOT up, with its transport reason. Actionable, and a listing that
	//     could not run on an unreachable host is a consequence rather than a second fault.
	//  2. Any host whose Claude LISTING failed. It was written by the poller, read only by
	//     the machine-readable report, and rendered nowhere — so a host whose panes appear
	//     normally was silently missing every pane-less row it should have had. A screen that
	//     looks complete and is not outranks an informational reason on a healthy host.
	//  3. An up host's own reason, which is informational (`degraded:format` and the like).
	type claim struct {
		host int
		text string
		// actionable marks tier 1: a host that is NOT up, whose reason is the only thing on the
		// screen about a host the operator cannot reach. Only those are shown in FRAGMENTS when
		// the whole will not fit — a piece of an up host's `degraded:format` is noise.
		actionable bool
	}
	order := make([]claim, 0, nHosts*2)
	for i, h := range hosts {
		if h.Reason != "" && h.Status != hub.Up {
			order = append(order, claim{i, firstLineOf(h.Reason), true})
		}
	}
	for i, h := range hosts {
		// Only where the transport reason has already ACCOUNTED for it: two parenthetical
		// clauses on one identity read as a run-on, and when the hub could not reach the
		// host the listing's failure is a consequence rather than news.
		//
		// The test is `Down || Connecting`, not `!= Up`. `!= Up` is also true for `UpEmpty`
		// (reachable, no tmux server) and `DegradedFormat` (a required format came back
		// empty) — hosts that ANSWERED, whose listing failure is an independent fault, and
		// suppressing it there hid the only thing that explained their missing rows.
		unreachable := h.Status == hub.Down || h.Status == hub.Connecting
		if h.AgentsReason != "" && !(h.Reason != "" && unreachable) {
			// No prefix: the producer's own strings already begin with `agents:`
			// (`agents.ErrNotInstalled`, `agents: deadline exceeded after …`,
			// `agents listing: …`), so adding one printed `agents: agents: …`. Measured on
			// the real strings — the first version of this line was written against an
			// invented fixture, which is how the doubling got past a green test.
			order = append(order, claim{i, firstLineOf(h.AgentsReason), false})
		}
	}
	for i, h := range hosts {
		if h.Reason != "" && h.Status == hub.Up {
			order = append(order, claim{i, firstLineOf(h.Reason), false})
		}
	}
	applied := make([]bool, len(order))
	for j, c := range order {
		// nLead, because the host at index i of `hosts` sits at i+nLead in `parts`.
		at := c.host + nLead
		was := parts[at]
		parts[at] = was + " (" + c.text + ")"
		if !fits(parts) {
			parts[at] = was
			continue
		}
		applied[j] = true
	}

	// SECOND pass, and it is second rather than folded into the first for a reason: an
	// actionable reason too long to fit is shown in part rather than dropped in silence — the
	// same contract lines.Fit keeps with its `+N` — but a fragment must never take the room a
	// LATER host's whole reason could have had. So every claim gets its whole-or-nothing chance
	// first, and only what is left over is spent on fragments.
	//
	// Measured before this existed: the real transport reason (a remedy plus ssh's own stderr,
	// about 320 columns) was reverted whole at 80, 120 AND 200 columns, so at no width did the
	// screen admit the hub had anything to say about why the host was down.
	//
	// The `…` cannot be a lie about a complete reason: `parts` only ever grows between the two
	// passes, so a claim the first pass could not fit whole cannot fit whole here either. The
	// control that says so is a reason that DOES fit, which carries no marker.
	const minReason = 20 // below this a fragment names neither the fault nor the remedy
	for j, c := range order {
		if applied[j] || !c.actionable {
			continue
		}
		at := c.host + nLead
		was := parts[at]
		room := width - lines.Width(strings.Join(parts, " · ")) - lines.Width(" (…)")
		if room < minReason {
			continue
		}
		parts[at] = was + " (" + lines.Truncate(c.text, room) + "…)"
		if !fits(parts) {
			parts[at] = was
		}
	}
	return strings.Join(parts, " · ")
}

// bodyHeight is the rows a terminal of `height` leaves for the body. The screen
// spends one row on the header (the "tmux-hub — N sessions" line, plus the nested
// hint) and one on the footer (the host line, or a note while one is showing);
// everything between them is the body.
//
// The chrome is counted here and nowhere else. Both the renderer and
// InboxViewport need this number, and if they ever answer it differently then `A`
// selects panes that are off screen — the one thing it exists to prevent, failing
// silently. So a second footer row or a status line is a change to this function,
// and both consumers follow it. TestRendererAndViewportAgreeOnTheBody pins them.
func bodyHeight(height int) int {
	const chrome = 2 // one header row + one footer row
	if height <= chrome {
		return 0
	}
	return height - chrome
}

// inboxHeight is the rows the LIST gets, and it is the second number this file owns for the reason
// bodyHeight gives: the renderer and InboxViewport both need it, and if they ever answer differently
// then `A` selects panes that are off screen — silently, which is the one thing it exists to prevent.
//
// It appeared when the details band moved to the bottom of the body. Before that the list had the
// whole body and this function would have been `bodyHeight`; the first version of the move changed the
// renderer alone and `TestSelectAllSelectsOnlyPanesOnScreen` caught it in the same run, reporting a
// pane seven rows below the fold.
//
// The band's own drawn height may be SHORTER than what is reserved (renderTiles caps a tile at ten
// rows), and the list's height is computed from the RESERVATION rather than from what was drawn, so
// the list is exactly as tall whatever the focused row holds.
func inboxHeight(height int) int {
	return listHeight(bodyHeight(height))
}

// listHeight is how many rows a LIST gets once the band has taken its share of a body.
//
// It takes the BODY and not the terminal height, which is what lets a second screen use it: the tree
// view's chrome is three rows (a title, a rule and the footer) where the dashboard's is two, so a
// screen that asked this function for the terminal height would claim one row too many and push its
// frame past the bottom. Splitting it out is the fix for a real trap rather than tidiness — there are
// seven independent answers to "how tall is my list" in this package and this is the only one that
// knows about the band.
func listHeight(bodyH int) int {
	band := detailsHeight(bodyH)
	if band == 0 {
		return bodyH
	}
	// One blank row separates the two surfaces.
	if h := bodyH - band - 1; h > 0 {
		return h
	}
	return 0
}

// extraAbove reports, for each pane index, how many EXTRA screen rows the list draws above it — a
// group header, or the separator that says the rows below are not in the group above. It is the
// renderer's own rule in ONE place, because the viewport has to COUNT the rows the renderer will
// draw: `A` selects what the viewport claims and the operator acts on what the renderer drew, so an
// estimate makes those two numbers different. Measured with 45 single-pane sessions on a 40-row
// terminal, the estimate claimed 12 of the 25 rows on screen.
//
// It returns counts and not booleans because the answer stopped being "is there a header": a
// headerless row that interrupts a standing header now costs a separator row instead. Two kinds of
// extra row, one arithmetic.
//
// It takes `first` because the renderer's own group tracking depends on it — a list scrolled into
// the middle of a session gives its first visible row a header it would not have had at the top.
func extraAbove(panes []registry.Pane, first int, aliases project.Aliases, groupBy Grouping,
	groups map[string]string, inline bool, favourites map[string]bool) []int {
	out := make([]int, len(panes))
	if inline {
		// Below 100 columns there are no headers at all, so every row costs one row.
		return out
	}
	alone := headerlessRows(panes, aliases, groupBy, groups, favourites)
	last := ""
	for i, p := range panes {
		if i < first {
			// The rows above the window still track the group, so the first visible row gets its
			// own header when the list is scrolled. This is the un-upper-cased `host session` the
			// renderer's own skip branch writes, which no group label can equal — so the first
			// visible row always takes a header, and it is written the same way here on purpose:
			// the two must agree, and agreeing on the renderer's shape is what makes them.
			last = p.Host + " " + p.Session
			continue
		}
		switch {
		case favourites[MarkKey(p)],
			p.Kind == registry.KindAgent && groupBy != ByProject,
			alone[groupLabelOf(p, aliases, groupBy, groups)]:
			// A row no header speaks for. If a header is STANDING above it, a separator row goes
			// between them, because indentation under a header reads as membership — the whole of
			// the "I see duplicates" report. Not at the very top of the visible list: there is no
			// header on the screen above it to be separated from, and the renderer's own `len(rows)`
			// test must say the same thing or `A` and the screen disagree by a row.
			if last != "" && i > first {
				out[i] = 1
			}
			last = ""
		default:
			if g := groupLabelOf(p, aliases, groupBy, groups); g != last {
				out[i] = 1
				last = g
			}
		}
	}
	return out
}

// rowPrefixCost is how many rows are drawn ABOVE pane p before p's own row: a group header, or the
// break that says p is not in the group above, or nothing.
//
// It is the renderer's own question, asked BEFORE anything is appended, so a prefix can never take
// the last row of the screen and leave its row undrawn. extraAbove answers the same question for the
// viewport walking the whole list; the two must agree, and they agree because each is written as the
// same three cases in the same order — `drawn` here is `i > first` there.
func rowPrefixCost(p registry.Pane, lastGroup string, drawn int, inline bool, alone map[string]bool,
	aliases project.Aliases, groupBy Grouping, groups map[string]string, pinned bool) int {
	if inline {
		// Below 100 columns there are no headers and therefore no breaks.
		return 0
	}
	if pinned || p.Kind == registry.KindAgent && groupBy != ByProject ||
		alone[groupLabelOf(p, aliases, groupBy, groups)] {
		if lastGroup != "" && drawn > 0 {
			return 1 // the break
		}
		return 0
	}
	if groupLabelOf(p, aliases, groupBy, groups) != lastGroup {
		return 1 // the header
	}
	return 0
}

// rowCost is the screen rows pane i takes: itself, plus whatever is drawn above it.
func rowCost(extra []int, i int) int {
	return 1 + extra[i]
}

// inboxWindow is which pane rows the list draws — the first index and how many — walked with each
// row's REAL cost rather than with an estimate of how many headers there will be. It is the single
// source of truth for the list's arithmetic: the renderer takes its scroll position from it and `A`
// takes its selection from it, so the screen and the selection cannot be two different sets.
//
// It replaced `inboxCapacity`, which assumed one header per two panes. That was deliberately
// conservative, on the argument that showing one row fewer is invisible while selecting a row
// nobody can see is not — true when it was written, and then §16's group-of-one rule made a header
// the exception rather than the rule, at which point the estimate was wrong by half on the
// commonest fleet there is.
func inboxWindow(panes []registry.Pane, cursor, height int, aliases project.Aliases,
	groupBy Grouping, groups map[string]string, inline bool,
	favourites map[string]bool) (first, count int) {
	n := len(panes)
	if n == 0 || height < 1 {
		return 0, 0
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= n {
		cursor = n - 1
	}
	// Where to start: walk BACK from the cursor while the budget allows, which puts the cursor on
	// the last drawn row while the list is being scrolled and at its own place once the whole list
	// fits. `windowStart` says the same thing by arithmetic and carries an end-of-list clamp
	// (`first > n - capacity`) as well; that clamp is unreachable for any cursor inside the list —
	// it needs `cursor >= n` — so nothing is lost by not having it here. What the estimate DID lose
	// is the top of the screen: it positioned the start for half the rows and then the renderer
	// drew twice that many, so the cursor landed mid-screen with the rows above it never drawn.
	back := extraAbove(panes, 0, aliases, groupBy, groups, inline, favourites)
	first = cursor
	spent := rowCost(back, cursor)
	for first > 0 && spent+rowCost(back, first-1) <= height {
		first--
		spent += rowCost(back, first)
	}
	// Then the count, with the headers re-derived from that start, because the forced header on the
	// first visible row is a row the walk above did not pay for. If it costs the CURSOR its place,
	// start one row later and count again: the alternative is a window that does not hold the
	// cursor, which is the defect the scroller exists to prevent.
	for {
		header := extraAbove(panes, first, aliases, groupBy, groups, inline, favourites)
		spent, count = 0, 0
		for i := first; i < n; i++ {
			if spent+rowCost(header, i) > height {
				break
			}
			spent += rowCost(header, i)
			count++
		}
		if count == 0 || cursor < first+count {
			return first, count
		}
		first++
	}
}

// InboxViewport is which panes the inbox is showing, from the SAME frame the renderer draws. It
// returns the start index and count, and it takes the whole Frame rather than a width and a count
// because the header rule reads the aliases and the grouping: a caller that passed only the
// dimensions would silently read a zero Grouping and answer for a screen nobody is looking at.
func InboxViewport(f Frame) (first, count int) {
	// inboxHeight, not bodyHeight: the details band is pinned to the bottom of the body and the
	// list gets what is left. Reading the whole body here made `A` select rows the screen did not
	// draw.
	bodyH := inboxHeight(f.Height)
	if bodyH < 1 {
		return 0, 0
	}
	inline := LayoutFor(f.Width) == InboxOnly
	return inboxWindow(f.Panes, f.Cursor, bodyH, f.Aliases, f.GroupBy, f.Groups, inline,
		f.Favourites)
}

// windowStart keeps the cursor inside a window of `capacity` rows and returns the index of
// the first visible row.
//
// It is the scroller for a list whose rows all cost ONE row — the project list and the history
// view. The dashboard has its own (`inboxWindow`) because a session header there takes a row that
// no pane occupies, so its window is a walk over per-row costs and not a division. Two scrollers is
// what §21.13.4 asked to avoid, and the reason this is not that: they are not two answers to one
// question, they are one answer each to two — uniform rows and variable rows — and the dashboard's
// former attempt to reuse this one by ESTIMATING its variable cost is exactly what drifted.
func windowStart(cursor, n, capacity int) int {
	if capacity < 1 || n == 0 {
		return 0
	}
	if cursor < capacity {
		return 0
	}
	first := cursor - capacity + 1
	if max := n - capacity; first > max && max > 0 {
		first = max
	}
	if first < 0 {
		first = 0
	}
	return first
}

// stateWord is the state, or "stale" when the host stopped answering. A pane
// whose host is gone must not keep reading `works` — observed when a killed
// tunnel left a row looking live indefinitely.
func stateWord(p registry.Pane) string {
	if p.Stale {
		return "stale"
	}
	if p.Dead {
		return fmt.Sprintf("exited %d", p.DeadStatus)
	}
	return p.State().String()
}

// The age is deliberately NOT on the row: "(last seen 15:04:05)" truncated to
// "(las" in the 28-column inbox, which reads as a rendering bug. The row says
// `stale`, the host line says which host is down and why, and the tile header
// carries the time.
func staleAge(p registry.Pane) string {
	if !p.Stale || p.SeenAt.IsZero() {
		return ""
	}
	return " last seen " + p.SeenAt.Format("15:04:05")
}

// appendNote adds a clause to a note instead of replacing it.
//
// It exists because one keystroke can now produce two answers on two different
// goroutines: `enter` saves — which may stop an ssh master — and then connects, and the
// connect's message landed on top of the save's. Measured on a mixed save,
// `1 host kept in hosts.toml · stopped the ssh master for eu` became `connecting to jj`,
// so the operator was never told a connection had been ended. Ending something the user
// cannot see is the act that most has to be reported; the same screen already guards this
// row against a probe landing in it.
func appendNote(note, clause string) string {
	if clause == "" {
		return note
	}
	if note == "" {
		return clause
	}
	return note + " · " + clause
}

// plural exists because "1 panes" is the first thing a user reads.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// trimBlank drops the padding RenderInbox adds to reach its requested height, so
// a caller can see how many rows the list actually needed.
func trimBlank(rows []string) []string {
	n := len(rows)
	for n > 0 && strings.TrimSpace(rows[n-1]) == "" {
		n--
	}
	return rows[:n]
}

// RenderHistory lists sends newest-first with the outcome WORD, because that is
// the column a person scans after broadcasting to six agents: which ones got it.
func RenderHistory(es []history.Entry, width, height, cursor int) []string {
	out := make([]string, 0, height)
	for i, e := range es {
		if len(out) >= height {
			break
		}
		point := " "
		if i == cursor {
			point = ">"
		}
		// Every outcome the log can hold has a glyph, or the column says `?` about something the
		// hub itself wrote. Two were missing: `launched` has been written by the launch door since
		// §19 and `woken` arrives with §22's, and both rendered as "unknown" in the one view that
		// exists to say what happened.
		glyph := "?"
		switch e.Outcome {
		case "delivered":
			glyph = "✓"
		case "sent-unwitnessed":
			glyph = "~"
		case "refused":
			glyph = "✗"
		case "launched":
			glyph = "+"
		case "woken":
			// The same arrow a resurfaced ROW carries, and for the same reason: something that was
			// not here came back.
			glyph = "↑"
		}
		out = append(out, lines.Truncate(fmt.Sprintf("%s%s %s %-10s %-6s %s",
			point, glyph, e.At.Format("15:04:05"), e.Host, e.PaneID,
			strings.ReplaceAll(firstLineOf(e.Text), "\n", " ")), width))
	}
	for len(out) < height {
		out = append(out, "")
	}
	return out
}

// firstLineOf is a one-line surface's view of text that may have more than one line, and the
// " …" is the whole point: a surface that silently shows only the first line of an error reads as
// the whole error.
//
// It cuts at a CARRIAGE RETURN as well as a newline, which is not pedantry on a terminal: a `\r`
// returns the cursor to column 0, so the tail of the string overwrites the row that was already
// drawn there. ssh writes `\r\n` when its stderr comes back over a pty, so this is the commoner
// half of the pair for exactly the strings that reach the footer.
func firstLineOf(s string) string {
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		return strings.TrimRight(s[:i], " ") + " …"
	}
	return s
}

// RenderCompose shows the dashboard with an input area overlay.
//
// The note goes to the BASE screen's footer, the same place browse mode puts it.
// Every mode has to carry it or a keystroke that answers with a note is a silent
// no-op — measured, three of the four modes dropped it, and `r` with an empty
// selection is a path that sets one without leaving the mode.
func RenderCompose(f Frame, text string) string {
	// Named locals for the fields this body reads, so the positional-to-struct
	// change altered no line of it — which is what makes the regenerated mockup an
	// honest check that no frame moved.
	width, height := f.Width, f.Height
	base := backdrop(f.withHeight(f.Height - 4))
	// Reserve 4 lines: separator, input area (wraps if needed, min 2 lines), hint line
	inputLines := wrapText(text, width-2, 2)

	out := strings.Split(base, "\n")
	out = append(out, separator(width))
	out = append(out, inputLines...)
	out = append(out, lines.Truncate("alt+enter: newline  ·  enter: send  ·  esc: keep draft and leave", width))
	return joinToHeight(out, height)
}

// ProjectView is what the project list needs to draw itself.
//
// Grouped rather than five loose Frame fields, because they travel together and are zero unless that
// screen is showing — and because `backdrop` needs exactly this set to paint the list under an overlay
// the list raised.
type ProjectView struct {
	Rows    []project.Summary
	Cursor  int
	Warn    string
	Pinned  map[string]bool
	FavNote string
}

// backdrop is the SCREEN an overlay is drawn over, and every overlay renderer starts here.
//
// They each called `Render` by name, which is the dashboard — correct while the dashboard was the
// only screen an overlay could be raised from, and a silent teleport the moment a second screen
// could raise one. The reduced height the caller passes is this screen's own, so the overlay's
// chrome costs the same rows whichever screen is underneath.
func backdrop(f Frame) string {
	switch f.Screen {
	case modeTree:
		return joinToHeight(RenderTreeScreen(f), f.Height)
	case modeProjects:
		return joinToHeight(RenderProjects(f), f.Height)
	}
	return Render(f)
}

// separator and joinToHeight are the two lines every overlay screen repeats. They
// are one function each so a change to the chrome cannot land in some screens and
// not others.
func separator(width int) string { return strings.Repeat("─", max(0, width)) }

// joinToHeight pads with blank rows and then cuts, so a screen is exactly as tall
// as the terminal however much it wanted to draw.
func joinToHeight(rows []string, height int) string {
	if height < 1 {
		return ""
	}
	for len(rows) < height {
		rows = append(rows, "")
	}
	return strings.Join(rows[:height], "\n")
}

// wrapText wraps text to fit width, returns at least minLines.
func wrapText(text string, width, minLines int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			out = append(out, "")
			continue
		}
		for len(line) > 0 {
			if lines.Width(line) <= width {
				out = append(out, line)
				break
			}
			// Find the cut point
			cut := 0
			w := 0
			for i, r := range line {
				rw := 1
				if r >= 0x1100 {
					rw = 2
				}
				if w+rw > width {
					break
				}
				w += rw
				cut = i + len(string(r))
			}
			if cut == 0 {
				cut = 1
			}
			out = append(out, line[:cut])
			line = line[cut:]
		}
	}
	// Ensure minimum lines
	for len(out) < minLines {
		out = append(out, "")
	}
	return out
}

// ConfirmView is everything the confirmation screen shows beyond the dashboard
// underneath it.
//
// It is a struct rather than four more positional parameters because the screen has
// to name the ACT, the PAYLOAD and the TARGETS, and it named none of them: it read
// "Confirm send to 1 target(s)" for an interrupt, and on the re-send path — where
// the user never passes through the input box — the last screen before writing into
// live agents showed no payload at all. A dialog that names the danger without
// naming what will land there cannot be acted on.
type ConfirmView struct {
	Action  string   // "send" | "interrupt with C-c"
	Payload string   // the text about to be written; empty for an interrupt
	Targets []string // one line per pane, as the user identified it
	Reasons []broadcast.Reason
	Warning string // why the hub could not check something it wanted to
	Note    string
}

// PayloadPreview is how many lines of the payload the dialog shows. Enough to
// recognise a prompt, not enough to push the reasons off the screen.
const PayloadPreview = 3

// TargetPreview is how many targets the dialog lists before summarising the rest.
const TargetPreview = 6

// RenderConfirm shows what is about to be written, where, and why the hub is asking.
func RenderConfirm(f Frame, v ConfirmView) string {
	// Named locals for the fields this body reads, so the positional-to-struct
	// change altered no line of it — which is what makes the regenerated mockup an
	// honest check that no frame moved.
	width, height := f.Width, f.Height
	body := confirmBody(width, v)
	base := backdrop(f.withHeight(f.Height - len(body)).withNote(v.Note))
	return joinToHeight(append(strings.Split(base, "\n"), body...), height)
}

// confirmBody is the dialog itself, so its height is known before the dashboard
// under it is sized.
func confirmBody(width int, v ConfirmView) []string {
	act := v.Action
	if act == "" {
		act = "write"
	}
	out := []string{separator(width),
		lines.Truncate(fmt.Sprintf("Confirm %s to %s:", act,
			plural(len(v.Targets), "target", "targets")), width)}
	for i, t := range v.Targets {
		if i == TargetPreview {
			out = append(out, lines.Truncate(fmt.Sprintf("    … and %d more", len(v.Targets)-i), width))
			break
		}
		out = append(out, lines.Truncate("    "+t, width))
	}
	if v.Payload != "" {
		for i, l := range strings.Split(v.Payload, "\n") {
			if i == PayloadPreview {
				out = append(out, lines.Truncate("  │ …", width))
				break
			}
			out = append(out, lines.Truncate("  │ "+l, width))
		}
	}
	// MARKED, because these are the lines that say why the hub is asking, and at 80 columns one of
	// them lost its last word: `• this pane does not accept pasted text — it will read the prompt as
	// keypresse`. A safety sentence whose object is missing reads as complete, which is the one
	// direction a warning must not fail in — so the cut says so now.
	for _, r := range v.Reasons {
		out = append(out, lines.TruncateMarked("  • "+string(r), width))
	}
	if v.Warning != "" {
		out = append(out, lines.TruncateMarked("  ! the hub could not check: "+v.Warning, width))
	}
	// The commit line NAMES THE ACT, and that is the whole point of the line: it is the sentence
	// directly above the operator's finger. It was a constant reading `enter: send anyway`, so `K`
	// and `!` both described a SEND on the one line that says which key acts — the operator read
	// `send` and pressed enter on a kill. ConfirmView's own doc comment records the older half of
	// this defect: the dialog used to read "Confirm send to 1 target" for an interrupt, and the fix
	// named the act on the HEADING and left this line behind.
	//
	// `anyway` stays, and belongs: the dialog is only up because something about the target changed,
	// so "kill anyway" is exactly the decision being asked for.
	return append(out, lines.Truncate("enter: "+act+" anyway  ·  any other key: cancel", width))
}

// RenderHistoryView shows the full-screen history view.
//
// It refuses a screen too small to draw, exactly as Render does: without the guard
// it emitted three chrome rows plus height-3 list rows, so on a two-row terminal it
// returned more lines than the screen had.
func RenderHistoryView(entries []history.Entry, width, height, cursor int, note string) string {
	if width < 20 || height < 4 {
		return "terminal too small"
	}
	out := []string{lines.Truncate("History (newest first)", width), separator(width)}

	foot := "r: re-send  ·  j/k: move  ·  h/esc/q: leave"
	if note != "" {
		// The note outranks the hints: it is the answer to something the user just
		// did, and in this mode there is nowhere else for it to go.
		foot = note
	}
	out = append(out, RenderHistory(entries, width, height-3, cursor)...)
	out = append(out, lines.Truncate(foot, width))
	return joinToHeight(out, height)
}

// RenderProjects draws the project list.
//
// It is a full screen rather than an overlay because it replaces the question being
// answered: not "who needs me across the fleet" but "what is the state of the thing I am
// working on" (docs/design.md §21.2). Every row carries the group's label and its
// attention cell, and nothing else — the cell IS the reason to look.
// RenderProjects draws the project list.
//
// THE ANSWER CHANNEL, which §21.14.3 left undecided: one row at the foot, three claimants, in
// this priority — a NOTE, then the FILE's warning, then the key line. Stated rather than
// implied, and each rank has a reason:
//
//   - A note answers something the operator just pressed. A screen that swallows an answer is
//     indistinguishable from a key that did nothing, so it outranks everything.
//   - The file's warning outranks the keys because it stays true until the file is edited,
//     where the key line is the same on every screen and can be learned once.
//   - The key line is the resting state: on a screen just opened, what to press is the most
//     useful thing there is to say.
//
// They share the row through lines.Fit, so a lower rank is not silently REPLACED — it is
// dropped with a `+N` that says something else is waiting to be read. That is the whole
// difference from picking one and discarding the rest.
func RenderProjects(f Frame) []string {
	rows, width, height := f.Projects.Rows, f.Width, f.Height
	cursor, note, warn := f.Projects.Cursor, f.Note, f.Projects.Warn
	pinned, pinnedNote := f.Projects.Pinned, f.Projects.FavNote
	labels := project.SummaryLabels(rows)
	out := []string{lines.Truncate(plural(len(rows), "project", "projects")+
		"  ·  enter narrows, esc goes back", width)}
	// The SAME separator every other screen draws, rather than a private copy capped at 80 —
	// which on a 200-column terminal read as a narrow screen with debris to the right of the
	// rule. One owner for the rule means the list cannot look like a different program.
	out = append(out, separator(width))

	if len(rows) == 0 {
		// A specified empty screen, not a blank box: §9 requires one, and the remedy is
		// the fact that there is nothing to group rather than anything about the file.
		out = append(out, "no sessions yet — n starts one, p adds a host")
	}
	// The window, so a fleet with more projects than rows does not lose its tail
	// SILENTLY — a row the operator cannot scroll to is a row they cannot act on, which
	// is the same failure as not showing it at all. Two rows go to the header and the
	// rule, one is kept for the warning line when there is one.
	avail := height - 2
	if warn != "" {
		avail -= 2
	}
	// The "… N more" line needs a row of its own, and only when there IS a tail — so the
	// capacity is computed twice rather than once. Reserving unconditionally would drop a
	// project on a screen where everything fits, and reserving never would trim the line
	// that says the tail exists, which is the whole point of drawing it.
	capacity := avail
	if len(rows) > avail {
		capacity = avail - 1
	}
	first := windowStart(cursor, len(rows), capacity)
	last := first + capacity
	if last > len(rows) || capacity < 1 {
		last = len(rows)
	}
	if first > len(rows) {
		first = len(rows)
	}
	shown := rows[first:last]
	for i, s := range shown {
		point := "  "
		if first+i == cursor {
			point = "❯ "
		}
		cell := project.Cell(s, project.CellWidth)
		label := labels[s.Group.ID]
		// The pinned column, in the same place the dashboard puts it: a mark the operator MADE
		// belongs next to the cursor, not on the end of a label that truncation eats first.
		star := " "
		if pinned[s.Group.ID] {
			star = favouriteMark
		}
		// The cell is right-aligned against a fixed budget, so the eye finds the counts
		// in one column whatever the names are.
		room := max(1, width-lines.Width(point)-lines.Width(star)-project.CellWidth-2)
		out = append(out, point+star+lines.Pad(lines.Truncate(label, room), room)+"  "+cell)
	}
	if hidden := len(rows) - len(shown); hidden > 0 {
		out = append(out, lines.Truncate(fmt.Sprintf("  … %d more, j scrolls", hidden), width))
	}
	// TWO STAGES, because the claimants on this row fail in two different ways and one Fit cannot
	// express both.
	//
	// The standing facts — a note, the file's warning, the pinned count — go through Fit, which MARKS
	// what it drops: §21.12's priority says a note outranks the warning and the warning outranks the
	// keys, and a warning that vanished silently would be a lie about a file the operator has to edit.
	//
	// The KEY LINE is appended only if it fits WHOLE, and its absence is unmarked. Measured at 80
	// columns on a fleet with five favourites, the one-Fit version produced `5 favourite sessions +1`
	// — a marker against a COUNT, which reads as "one favourite is missing" rather than "one claimant
	// did not fit"; the dashboard's header carries the same ruling for the same reason. Dropping the
	// keys unmarked is safe HERE and would not be elsewhere: this screen's two load-bearing keys are
	// on its own header (`N projects  ·  enter narrows, esc goes back`), so the way in and the way out
	// survive whatever the footer loses. What is lost is the discoverability of `f` and `a`.
	const projectKeys = "j/k: move  ·  enter: narrow  ·  f: pin  ·  a: go to what waits  ·  esc: back"
	foot := lines.Fit(width, "  ·  ", note, warn, pinnedNote)
	switch {
	case foot == "":
		// Nothing standing to say: the row IS the key line, which §21.12 rule 1 states in terms —
		// on a screen just opened, what to press outranks everything.
		foot = lines.Truncate(projectKeys, width)
	case lines.Width(foot)+lines.Width("  ·  ")+lines.Width(projectKeys) <= width:
		foot += "  ·  " + projectKeys
	}
	if foot != "" {
		out = append(out, "", foot)
	}
	return joinLinesToHeight(out, height)
}

// joinLinesToHeight pads or trims a screen to exactly height rows, so a mode cannot leave
// the terminal holding the previous screen's tail.
func joinLinesToHeight(rows []string, height int) []string {
	for len(rows) < height {
		rows = append(rows, "")
	}
	if height > 0 && len(rows) > height {
		rows = rows[:height]
	}
	return rows
}

// NamingRows is how tall the naming overlay is, and it is FIXED.
//
// Six rows always — separator, subject, `now:`, field, reason, keys — so nothing beneath it
// moves when a reason appears or goes. A variable overlay would reflow the dashboard under
// the operator's cursor mid-edit (docs/design.md §21.12 rule 1).
const NamingRows = 6

// RenderNaming draws the dashboard with the naming overlay at its foot.
func RenderNaming(f Frame, nf namingForm) string {
	// Named locals for the fields this body reads, so the positional-to-struct
	// change altered no line of it — which is what makes the regenerated mockup an
	// honest check that no frame moved.
	width, height, aliases := f.Width, f.Height, f.Aliases
	base := backdrop(f.withHeight(f.Height - NamingRows).withNote(""))

	// The PROJECT half of §21.12 rule 3: one overlay, and only the subject row differs. The
	// six rows, the field, the reason and the keys are all the same.
	if nf.namingAProject() {
		g := *nf.group
		// The provenance per KIND, the way the session half does through derivedFrom: a
		// bucket's label does not come from a directory, so telling the operator it is "the
		// last segment of its directory" would be false for the two kinds that cannot be
		// named at all — and those are exactly the kinds where the refusal that follows has
		// to make sense.
		nowVal := g.Label
		switch g.Kind {
		case project.Derived:
			nowVal += "  (not yours — the last segment of its directory)"
		case project.Pending:
			nowVal += "  (no path has been read for these rows yet)"
		case project.Unassigned:
			nowVal += "  (these rows have no path at all)"
		}
		subject := g.Label
		if g.Host != "" {
			subject = g.Host + " · " + g.Label
		}
		body := []string{
			separator(width),
			lines.Truncate("name this project: "+subject, width),
			lines.Truncate("now:  "+nowVal, width),
			"name: " + fieldTail(nf.input.Text(), width-len("name: ")),
			lines.Truncate(nf.reason, width),
			lines.Truncate("enter: save  ·  ctrl+u then enter: remove the name  ·  esc: cancel", width),
		}
		return joinToHeight(append(strings.Split(base, "\n"), body...), height)
	}

	shown, own := aliases.DisplayName(nf.subject)
	nowVal := shown
	if own {
		// SAID, not inferred. The line already explains a name the operator did NOT choose — "(not
		// yours — Claude's own name)" — and said nothing at all for one they did, so "this is mine"
		// had to be read from the ABSENCE of the other clause. The row's own `»` marker carries the
		// same fact, and a screen must not make the operator hold two conventions for one question.
		nowVal = shown + "  (yours)"
	}
	if !own {
		// `now:` says what the row is called TODAY and where that comes from, because the
		// field is empty for an unnamed row and an empty field beside an empty `now:` gives
		// the operator nothing to compare their typing against.
		nowVal = shown + "  (not yours — " + derivedFrom(nf.subject) + ")"
	}
	// The subject line carries the HOST too: a session name is not unique across the fleet,
	// and naming the wrong host's session is the mistake with no safety net.
	subject := nf.subject.Host + " · " + shown
	if nf.subject.Kind == registry.KindAgent {
		subject += "  (session, no pane)"
	}

	body := []string{
		separator(width),
		lines.Truncate("name this session: "+subject, width),
		lines.Truncate("now:  "+nowVal, width),
		"name: " + fieldTail(nf.input.Text(), width-len("name: ")),
		lines.Truncate(nf.reason, width),
		lines.Truncate("enter: save  ·  ctrl+u then enter: remove the name  ·  esc: cancel", width),
	}
	out := append(strings.Split(base, "\n"), body...)
	return joinToHeight(out, height)
}

// fieldTail renders a text field so the CURSOR is always visible, showing the tail and a
// leading ellipsis when what is typed is wider than the room.
//
// Truncating from the right is what a plain Truncate does, and it hides the very characters
// being typed — the exact defect §21.12 cites as the reason inline editing was ruled out, and
// the overlay reproduced it. A field you cannot see the end of is a field you cannot type in.
func fieldTail(text string, room int) string {
	const caret = "▏"
	room -= lines.Width(caret)
	if room < 1 {
		return caret
	}
	if lines.Width(text) <= room {
		return text + caret
	}
	// Walk back from the end until the tail fits beside the ellipsis, counting COLUMNS so a
	// wide glyph cannot push the caret off the row.
	const ell = "…"
	budget := room - lines.Width(ell)
	if budget < 1 {
		return caret
	}
	rs := []rune(text)
	i := len(rs)
	for i > 0 && lines.Width(string(rs[i-1:])) <= budget {
		i--
	}
	return ell + string(rs[i:]) + caret
}

// derivedFrom names WHERE a row's current name comes from when it is not the operator's, so
// `now:` is a fact about provenance rather than a bare string the operator cannot place.
func derivedFrom(p registry.Pane) string {
	switch {
	case p.Kind == registry.KindAgent:
		return "Claude's own name for the session"
	case p.AgentName != "":
		return "Claude's own name"
	case p.Session != "":
		return "the tmux session name"
	default:
		return "the pane id"
	}
}

// rowMarkers is the suffix a row carries after its columns: what is true of the row that its
// columns cannot say.
//
// One function for all three row shapes because each of them appended `[↑]` itself, so a second
// marker would have been a fourth, fifth and sixth place to forget it — and a marker that reaches
// two shapes of three is worse than none, since the shape it would miss is the one below 100
// columns that §16 calls the size to hold.
//
// The two markers are mutually exclusive by construction: hide.Set.Hidden is `marked && !waiting`
// and Resurfaced is `marked && waiting`. The switch states the precedence anyway, because `[↑]`
// says strictly more than `[x]` — hidden AND asking — and one marker per row is what makes either
// readable at a glance.
func rowMarkers(p registry.Pane, resurfaced, hiddenRows map[string]bool) string {
	switch k := MarkKey(p); {
	case resurfaced[k]:
		return " " + resurfacedMark
	case hiddenRows[k]:
		return " " + hiddenMark
	}
	return ""
}

// favColumn is the one column that says a row is PINNED, and it is a column rather than a suffix for
// two reasons. A list is scanned downward, so a marker the eye can follow has to be in a fixed place —
// and a suffix rides on the end of a NAME, the field most likely to be truncated, so on the narrow
// rows it would be the first thing to go.
//
// It sits beside the selection mark because the two are the same kind of fact: something the operator
// SAID about this row, as opposed to something the row says about itself.
func favColumn(p registry.Pane, favourites map[string]bool) string {
	if favourites[MarkKey(p)] {
		return favouriteMark
	}
	return " "
}

// hiddenTally is the footer's account of the hide filter, and it is a STRUCT for the same reason
// Frame is: `hiddenCount, blockedCount int` were adjacent interchangeable ints, and swapping them
// reads as a fleet where more panes are waiting than exist.
type hiddenTally struct {
	// Marked is how many rows the operator has hidden; Waiting how many of those are asking
	// anyway and so came back on their own (§18).
	Marked  int
	Waiting int
	// AllShown is `X`. It changes the sentence, not the numbers.
	AllShown bool
}

// parts are the tally's claimants on the footer, in the order lines.Fit may drop them from the
// tail: the least useful last.
func (h hiddenTally) parts() []string {
	if h.Marked == 0 {
		return nil
	}
	if h.AllShown {
		// The statement comes FIRST and the count second, because the fact the operator cannot
		// otherwise recover is that nothing is being kept off this screen. A bare number here
		// would read exactly like the filtered footer, which is the state it is the opposite of.
		//
		// The waiting count is dropped entirely: those rows are on the screen carrying their own
		// state word, so counting them again says nothing.
		return []string{"X shows all rows", fmt.Sprintf("%d marked hidden", h.Marked)}
	}
	// "N of them waiting for input" is meaningless without "N hidden", so it goes last and
	// therefore drops first.
	out := []string{fmt.Sprintf("%d hidden", h.Marked)}
	if h.Waiting > 0 {
		out = append(out, fmt.Sprintf("%d of them waiting for input", h.Waiting))
	}
	return out
}
