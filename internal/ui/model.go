package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/broadcast"
	"github.com/DawnBreather/tmux-hub/internal/fav"
	"github.com/DawnBreather/tmux-hub/internal/hide"
	"github.com/DawnBreather/tmux-hub/internal/history"
	"github.com/DawnBreather/tmux-hub/internal/hostset"
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/launch"
	"github.com/DawnBreather/tmux-hub/internal/proc"
	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// AgentInterval is how often Claude's own session listing is refreshed. Far
// slower than the tick because it costs 0.5-2.8 s over ssh and describes work
// that changes on a human timescale (docs/design.md §17).
const AgentInterval = 20 * time.Second

// PollInterval is the tick period. The floor exists because a tick costs one
// round trip per host; on the local socket a full poll is a couple of
// milliseconds.
const PollInterval = 1200 * time.Millisecond

// WalkTimeout bounds the process walk inside one round of identification. The
// remote half runs python over ssh — measured at 0.5-2.8 s for the comparable agent
// listing — so the bound is generous; it exists because a stalled forward makes the
// invocation hang forever and a hung walk would stop the hub ever re-stamping.
const WalkTimeout = 10 * time.Second

// IdentTimeout bounds ONE host's whole round: the walk, plus the stamps that follow
// it. It has to exceed WalkTimeout or a walk that is merely slow would read as a
// stall and the walk's own bound would be dead code; the margin is one tmux deadline,
// the cost of the single slowest stamp.
//
// The bound is what makes the token's meaning true. Without it a round could stay
// out forever — one 5-second stamp per selected pane after a stalled walk is over a
// minute for a host with `A` pressed — and all that time the last completed round is
// still the hub's answer, so a pane whose agent has exited reads as identified. A
// round that overruns this reports its host's panes UNIDENTIFIED instead, which is
// the answer that makes the confirmation dialog ask.
const IdentTimeout = WalkTimeout + 5*time.Second

// SweepTimeout bounds the shutdown sweep. It runs after the TUI is gone, so a
// stalled host must not keep the process alive.
const SweepTimeout = 5 * time.Second

// resurfacedMark is the marker shown on a resurfaced row. A resurfaced pane is
// one that was hidden but is now shown because it's waiting for the user.
const resurfacedMark = "[↑]"

// favouriteMark is the one column that says the operator pinned this row, or the project it belongs
// to. One glyph wide, so it costs every row exactly one column and reads as a column rather than as a
// decoration on the end of a name.
const favouriteMark = "★"

// hiddenMark is the marker on a row that is only on the screen because `X` is on.
//
// It names the KEY that hid the row, so the way to undo it is on the row itself. Without it `X`
// answered its own question with nothing: the hidden row and the row beside it rendered as the
// same string but for their pane id, so the gesture that exists to show the operator what they
// hid showed them everything and told them nothing.
const hiddenMark = "[x]"

type tickMsg struct {
	hosts []hub.Host
	panes []registry.Pane
}

type agentsMsg struct {
	hosts []hub.Host
	panes []registry.Pane
}

type agentsNow struct{}

// mode is what the keyboard means right now. Compose is a separate mode rather
// than a focused widget because in compose EVERY rune is text — including `q`,
// which in browse mode quits. A dashboard that quits when someone types "quit the
// server" into a prompt is worse than one with modes.
type uiMode int

const (
	modeBrowse uiMode = iota
	modeCompose
	modeConfirm
	modeHistory
	modeLaunch
	modePicker
	modeProjects
	// modeTree is the filesystem view (tree.go, treescreen.go): hosts as volumes, directories as
	// directories, sessions as files. A MODE and not a Grouping for the reason Grouping's own doc
	// comment gives — a grouping changes the headers and never the ORDER, and this changes the order
	// into a hierarchy.
	modeTree
	modeNaming
	// modeWake is the wake dialog. It is its own mode rather than a second shape of modeConfirm
	// because its subject is a ROW and not a selection, and its body is a list of costs rather than
	// of targets — and because a pending action must own its payload (docs/design.md §22.3).
	modeWake
	// modeSearch is the keyword field (docs/design.md §21.17). It is a MODE because every key
	// inside it is text: `j` types a `j` rather than moving the cursor, which is the rule this
	// repo wrote down after a mutant that made `q` quit inside the composer survived a test
	// that typed a whole word.
	modeSearch
)

// action is what a confirmation is confirming. It is RECORDED when the dialog
// opens and never inferred at enter: inferring it from the composer read `!` as a
// send whenever a draft was left in the box, which is the normal resting state —
// esc from compose deliberately keeps the draft, and so does a refused send. So the
// one key whose purpose is to stop a runaway agent wrote the draft into it instead.
type action int

const (
	actionNone action = iota
	actionSend
	actionInterrupt
	actionRestart
	actionKill
)

// paneSnapshot is one pane as it was AT THE MOMENT it was selected. §7's rule asks
// what changed since then, which has no answer unless the "then" is recorded at the
// keystroke.
//
// The session and window are IDs (`$N`, `@N`) and never names. A name is not
// identity — measured live, tmux's automatic-rename followed the pane's foreground
// process from `bash` to `claude` and the rule reported the pane as MOVED while it
// had not moved at all, which is the false alarm that teaches people to confirm
// without reading.
type paneSnapshot struct {
	identified bool
	session    string
	window     string
	epoch      string
}

type model struct {
	poller *hub.Poller
	reg    *registry.Registry

	hosts  []hub.Host
	panes  []registry.Pane
	cursor rowCursor
	sel    Selection // replaces `marked`: one answer to "what is selected"

	// atSelection and lastOutcome are the two halves of §7's rule that the pane
	// list cannot supply: what a target looked like when it was chosen, and what
	// the previous write to it did.
	atSelection map[SelectionKey]paneSnapshot
	lastOutcome map[SelectionKey]broadcast.Outcome

	// askedWindowName is the name the hub last ASKED a window to take, per window id, and it exists
	// because the hub is not the only program that writes a window name.
	//
	// Reported as a status line that "shimmers" on some sessions. Measured on the operator's own
	// server: three windows alternating between the operator's alias and the raw Claude session name
	// (`frontend-troubleshooting` ↔ `20260810--troubleshooting`) several times a second, with
	// `automatic-rename` OFF — so tmux was not doing it. Claude Code names the window after its own
	// session, the hub's differences-only rule then saw a name that had drifted and wrote the alias
	// back, and each side kept undoing the other at poll rate.
	//
	// A differences-only rule is not enough when there are two writers: it is the very thing that
	// makes the loop tight. So the hub asks ONCE for a given name and, if that name does not stick,
	// concedes the window — the alias is still what the dashboard shows, which is the surface the
	// operator asked for. A new alias is a new name and therefore gets one fresh attempt.
	//
	// It is a map so a value receiver can record into it, and it is touched only from Update
	// (`publishAliases` runs there and hands plain values to its commands), so it needs no lock.
	askedWindowName map[string]string

	mode uiMode
	// underlay is the SCREEN an overlay was raised over. Written only by `raise`, read only by
	// `dismiss`, zero-valued at the dashboard — which is where a hub with no overlay open is.
	underlay   uiMode
	composer   Composer
	pending    []broadcast.Reason // why the confirmation is up, empty when it is not
	pendingAct action             // WHICH act it is up for
	// pendingText is WHAT a pending send will send. It exists because the confirmation used to
	// read the composer at the moment enter was pressed, which made the composer the staging area
	// for anything that wanted to send: `r` on a history entry overwrote the operator's draft at
	// the KEYSTROKE, before the dialog it raised had been answered, and cancelling gave nothing
	// back — a prompt lost for having read one's own log and thought better of re-sending. The
	// dialog now carries its own subject, so the composer holds one thing only: the draft.
	pendingText string
	// pendingWake is the row the wake dialog is about, CAPTURED at the keystroke. It is a copy for
	// the reason the naming overlay's subject is: the list re-sorts under a poll, and a subject
	// re-derived at commit time would wake a row the operator was not looking at.
	pendingWake *wakeSubject
	history     []history.Entry // loaded on demand, newest first
	histCursor  int
	// fromHistory says the PENDING send's text came out of the log rather than out of the
	// composer, which is what makes §7's rule always ask. Its whole life is the dialog: `r` sets
	// it, and the dialog's resolution — sent or cancelled — clears it. composeKey used to clear it
	// on every keystroke as well, because `r` staged its text in the composer and the operator
	// could type over it; the text no longer goes there, so those four clearings became statements
	// about a mechanism that had gone.
	fromHistory bool
	launchForm  launchForm // the launch form state, when mode=modeLaunch

	// rules are the operator's project overrides. The zero value is valid and means
	// "no rules", which is a specified screen: every row then groups by its basename
	// fallback (docs/design.md §21.9.2).
	rules project.Rules
	// aliases are the operator's names for sessions, and projectsPath is where both they and
	// the project rules live. The path is a field rather than a call to
	// project.DefaultPath() so a test can point it somewhere private — the commit path
	// WRITES, and a test that wrote the operator's own file would be a defect of its own.
	aliases      project.Aliases
	projectsPath string
	naming       namingForm
	// rulesWarn is what is wrong with projects.toml. It is a RESTING message rather than
	// a note, for the same reason pickerWarn is: a file that cannot be parsed stays
	// unparseable until it is edited, so a message cleared by the next `j` would hide it.
	rulesWarn string
	// filter is which project the dashboard is narrowed to. It is a STRUCT and never a
	// bare string, because `"u:"` is the unassigned bucket's real id and `on == false` is
	// "no filter" — with one string, `enter` on that bucket would render as `esc` (§21.5).
	filter struct {
		on    bool
		group string
	}
	projCursor int

	// The picker's state, when mode=modePicker. `picker` is what the last probe
	// answered with the user's decisions folded in; `pickerKept` is what hosts.toml
	// said when the screen opened, and it is kept because a row carries only what the
	// screen shows — saving from rows alone would drop every tag the user wrote and
	// every entry for a host no longer in ~/.ssh/config.
	picker       []PickerRow
	pickerCursor int
	pickerKept   []hostset.Entry
	pickerPorts  PickerPorts
	// pickerReserved is the labels the fleet has already spoken for — this machine's own
	// server and every `--host` entry. main knows them and the screen cannot, and a
	// candidate that collides must be refused HERE rather than at the startup that then
	// exits 1 on a file the picker itself wrote.
	pickerReserved []string
	// pickerWarn is what is wrong with hosts.toml itself, and it is the picker's
	// RESTING footer rather than a note: a duplicate alias is a fact about the file
	// that stays true until the file is rewritten, so a message shown once and then
	// cleared by the next j would hide it. Set only by withKept.
	pickerWarn string
	// pickerBusy stops a second round starting while one is out there, for the same
	// reason identBusy does: two answers landing in either order would make the rows
	// depend on which ssh finished first.
	pickerBusy bool
	// pickerAutoProbe is the ONE path that probes without a keystroke: first run,
	// where hosts.toml is absent and nobody has asked yet. A probe on every start
	// would spend 7.65 s of ssh re-answering a question the file already answers.
	pickerAutoProbe bool

	run     tmux.Exec
	sender  *broadcast.Sender
	stamper *broadcast.Stamper
	keeper  *broadcast.Keeper
	hist    *history.Log
	// walk chooses the transport for a host's process walk. It is a field rather
	// than a direct call to walkerFor so a test can identify a pane without one
	// really being an agent — the same seam hub.Poller has for its Runner.
	walk func(hub.Host) proc.Walker

	// identBusy stops a second round of identification starting on a host while one
	// is still out there. Two rounds in flight can stamp one pane twice, and then the
	// hub's token and the pane's disagree until the next tick — which refuses a send
	// for a reason the user cannot act on.
	//
	// Keyed by host, and the key is the load-bearing part: one boolean for the fleet
	// meant a single host that stopped answering stopped re-identification on every
	// host, so the token silently degraded from "identified one tick ago" to
	// "identified at some point" for hosts that were answering perfectly well.
	identBusy map[string]bool
	// identErr is why identification is not answering, per host. Per host for the
	// same reason: with one string a healthy host's round cleared the reason a
	// stalled one gave, and the dialog then stopped mentioning the very host whose
	// panes it cannot vouch for.
	identErr map[string]string
	// identTimeout bounds one host's round. A field rather than the constant so a
	// test can prove the bound without waiting the real one out; build sets it to
	// IdentTimeout and nothing else writes it.
	identTimeout time.Duration

	// selfSession and selfEpoch are the hub's own coordinates: which session its
	// pane belongs to ($N, or "" outside tmux) and what its own tmux server says
	// about itself (#{pid}:#{start_time}). Both are read once at startup — the
	// hub's own pane cannot move between servers, and if the server restarts the
	// hub's pane is gone with it.
	selfSession string
	selfEpoch   string

	// treeOpen is which nodes of the filesystem view are expanded, keyed on treeLine.Key. NIL means
	// "everything open", which is how a first paint is useful before any key is pressed.
	treeOpen map[string]bool
	// treeCur is that view's cursor, keyed on the LINE and not on an index.
	treeCur treeCursor

	// home is the operator's home directory, read ONCE at startup and handed to the renderer through
	// the Frame. It is here rather than in the renderer because that function's output is diffed byte
	// for byte to prove a refactor moved no frame, and an environment read inside it would make the
	// published document depend on the machine that generated it.
	home string

	// self is the name tmux reports for a pane running this program, and ownHidden is how
	// many rows the last fleet dropped because they were the hub's own windows. The count
	// is kept rather than recomputed so the header can EXPLAIN its session count: a number
	// that silently disagrees with `tmux ls` is the defect this fix would otherwise trade
	// for the one it closes.
	self      string
	ownHidden int

	width, height int
	log           *hub.StateLog // nil unless --log-states was given
	note          string        // a one-line message for the user, shown until the next action
	ctx           context.Context
	hidden        *hide.Set
	showHidden    bool // when true, shows all panes including hidden ones
	// favs is what the operator pinned. A nil set is a working hub with no favourites, so nothing
	// here branches on its absence — every method answers for a nil receiver.
	favs *fav.Set
	// favouritesOnly and search are the operator's two NARROWINGS (docs/design.md §21.17), and
	// they are deliberately separate from `v`: `v` answers how rows are GROUPED and these answer
	// which rows are SHOWN, so folding them into one three-position cycle would make "only my
	// favourites, grouped by project" unreachable.
	//
	// The query has ONE source of truth — the field's own text — so "a filter is applied" cannot
	// disagree with what it filters by. Both are read in rowsForScreen and never in visibleRows,
	// because that function feeds sel.Prune and a narrowing there would drop a MARK the moment a
	// row stopped matching (§21.5).
	favouritesOnly bool
	search         Composer
	// searchBefore is what the field held when it was opened, so esc restores it. Cancelling
	// must be lossless: an operator who narrows, opens the field to refine and changes their
	// mind should be where they were, not on an unfiltered fleet.
	searchBefore string
	// groupBy is what the inbox files rows under. It is NOT persisted, and deliberately: it is a way
	// of looking rather than a decision about the fleet, one keystroke to change and named in the
	// header while it is on — the same standing as showHidden.
	groupBy Grouping
}

// attachedMsg comes back when the suspended attach has exited.
type attachedMsg struct{ err error }

// visibleRows is the ONE place the hidden set is applied.
//
// One owner because the alternative has already bitten this project: `A` was a
// logical no-op for a whole review cycle because two functions disagreed about
// what "visible" meant. If you need a filtered list, call this.
func (m model) visibleRows() []registry.Pane {
	if m.hidden == nil || m.showHidden {
		return m.panes
	}
	out := make([]registry.Pane, 0, len(m.panes))
	for _, p := range m.panes {
		if !m.hidden.Hidden(p) {
			out = append(out, p)
		}
	}
	return out
}

// namingForm is the state of the naming overlay (docs/design.md §21.12).
//
// The subject is CAPTURED at the keystroke rather than re-derived per frame: the list
// re-sorts under a probe, and a subject that moved between opening the overlay and pressing
// enter would name a row the operator was not looking at. Rule 2 is that the subject must be
// visible at the moment of commit, and that is only true if it cannot change.
type namingForm struct {
	subject registry.Pane
	// group is set instead of subject when the overlay was opened from the project LIST, where
	// what is being named is a project rather than a session. §21.12 rule 3 says one overlay
	// serves both and only the subject row differs; this is that difference, and the rest of
	// the form — the field, the reason, the six rows — is shared.
	group  *project.Group
	input  Composer
	reason string
}

// namingAProject reports which half of rule 3 this form is.
func (f namingForm) namingAProject() bool { return f.group != nil }

// openNaming raises the naming overlay over one ROW, remembering the screen it came from.
//
// The subject is captured HERE, at the keystroke, and the field opens with the operator's own name
// only — never a derived one, or an untouched enter would freeze Claude's word or a tmux session
// name into the file (§21.12 rule 5).
// openSearch opens the keyword field over whatever screen is showing.
//
// The field opens holding what is already applied, so narrowing further is editing rather than
// retyping; `searchBefore` is what esc restores, and the caller sets it because it is the caller
// that knows the field is being OPENED rather than re-entered.
func (m model) openSearch() model { return m.raise(modeSearch) }

// isOverlay reports whether a mode is drawn OVER a screen rather than being one.
//
// The distinction is what makes `raise` safe to nest: the composer's enter raises the confirm
// dialog, and the screen to return to afterwards is the one under the COMPOSER, not the composer.
func (mo uiMode) isOverlay() bool {
	switch mo {
	case modeCompose, modeConfirm, modeNaming, modeWake, modeSearch, modeLaunch, modePicker:
		return true
	}
	return false
}

// raise opens an overlay, remembering the screen it was raised over.
//
// It takes no `from` argument on purpose: the screen is `m.mode` at the moment of the keystroke, so
// there is nothing for a caller to pass and therefore nothing to pass wrongly. Every overlay used
// to exit to `modeBrowse` by name, which was right while the dashboard was the only screen that
// raised one — the filesystem view then made every one of those exits a silent teleport off the
// screen the operator was on.
func (m model) raise(overlay uiMode) model {
	if !m.mode.isOverlay() {
		m.underlay = m.mode
	}
	m.mode = overlay
	return m
}

// dismiss closes an overlay, returning to the screen it was raised over.
func (m model) dismiss() model {
	if m.underlay.isOverlay() {
		// Cannot happen through `raise`, and asserted by construction rather than trusted: an
		// overlay dismissed INTO an overlay is a screen with no way out, which is the one failure
		// this pair exists to prevent.
		m.underlay = modeBrowse
	}
	m.mode = m.underlay
	return m
}

func (m model) openNaming(p registry.Pane) (tea.Model, tea.Cmd) {
	m.naming = namingForm{subject: p}
	if cur, has := m.aliases.Get(project.AliasKeyOf(p)); has {
		m.naming.input.Insert(cur)
	}
	return m.raise(modeNaming), nil
}

// openNamingGroup raises the same overlay over a PROJECT — rule 3's other half.
//
// The group is COPIED rather than re-derived per frame: a group cannot be "the row under the
// cursor" while the list re-sorts under a probe (§21.12 rule 2).
func (m model) openNamingGroup(g project.Group) (tea.Model, tea.Cmd) {
	m.naming = namingForm{group: &g}
	if g.Kind == project.Named {
		m.naming.input.Insert(g.Label)
	}
	return m.raise(modeNaming), nil
}

// rowsForScreen is visibleRows narrowed by every NARROWING the operator turned on — the active
// project, `*` and a keyword — and it is what the renderer and `A` both use, so `A` still means
// "select what is on screen".
//
// It is deliberately NOT visibleRows. That function is the only input to sel.Prune, and putting a
// narrowing into it would make a re-resolution silently drop a mark: a pane the operator cd's in
// changes path, changes project, leaves the filter, and its mark disappears mid-compose. A narrowing
// decides what is SHOWN; the hidden set decides what EXISTS (§21.5, §21.17).
//
// ONE pass over the rows with the narrowings as a list of conditions, rather than one pass each.
// Three passes allocated three slices and walked the fleet three times per call — and this is called
// three or four times per frame — while `Rules.OfPane`, which normalises a path and matches it
// against every rule, ran TWICE per row whenever the project filter and a keyword were both on. The
// shape also makes the next narrowing a line in this list rather than a fourth loop somebody could
// write in the wrong function.
// setFleet is the ONE writer of m.panes, and it exists because there are two producers.
//
// The tmux tick and the agents poll both deliver a fleet, so a rule about what the fleet IS had two
// places to be forgotten — the shape this repo has paid for twice (a field the second producer did
// not copy). Anything that decides membership belongs here; `visibleRows` and `rowsForScreen` decide
// what is SHOWN, which is a different question and answers to a keystroke.
func (m *model) setFleet(panes []registry.Pane) {
	own := ownFurniture(panes, m.aliases, m.self)
	m.ownHidden = len(own)
	if len(own) == 0 {
		m.panes = panes
		return
	}
	kept := make([]registry.Pane, 0, len(panes)-len(own))
	for _, p := range panes {
		if !own[MarkKey(p)] {
			kept = append(kept, p)
		}
	}
	m.panes = kept
}

func (m model) rowsForScreen() []registry.Pane {
	rows, _ := m.rowsForScreenLoose()
	return rows
}

// rowsForScreenLoose is rowsForScreen plus whether the keyword had to be read LOOSELY to answer.
//
// The keyword matches by SUBSTRING, and falls back to a subsequence — fzf's matcher — only when the
// substring pass keeps nothing. That order is the whole design, and it is measured rather than
// argued: on the operator's own fleet of 64 rows, whose sessions are named after the prompt that
// started them, a bare subsequence floods the list — `sec` goes from 2 rows to 24, `ci` from 14 to
// 26, `test` from 4 to 17 — because almost any short subsequence exists somewhere in a sentence.
// Every one of those queries HAS substring hits, so the fallback cannot fire for them; it fires for
// `opssch`, which finds nothing by substring and four rows as a subsequence
// (`20260701-ops-schdev3-…`, `envoy-ops-svcdev4-…`). So the flood cases are excluded BY CONSTRUCTION
// rather than by a score threshold nobody could justify.
//
// The order is never touched. Ranking by match score is what fzf does instead, and it would fight the
// one property this screen exists for — the list is ordered by ATTENTION (§21.11), with the
// longest-waiting row first inside its band. A loose answer that reorders the fleet would trade the
// question "who needs me" for "what did I type".
//
// The bool is returned rather than stored because the footer has to SAY which matcher answered: two
// rows that contain `sec` and twenty-four that loosely resemble it are different facts, and a screen
// that shows the second while looking like the first is the defect this repo keeps finding.
func (m model) rowsForScreenLoose() ([]registry.Pane, bool) {
	rows := m.visibleRows()
	q := m.searchQuery()
	if m.filter.on || m.favouritesOnly || q != "" {
		narrow := func(match func(registry.Pane, project.Group) bool) []registry.Pane {
			// Only two of the three narrowings need a row's project, and deriving it for all of
			// them would make `*` alone — the cheapest narrowing — pay for a path normalisation
			// per row that nothing reads.
			needGroup := m.filter.on || q != ""
			out := make([]registry.Pane, 0, len(rows))
			for _, p := range rows {
				var group project.Group
				if needGroup {
					group = m.rules.OfPane(p)
				}
				switch {
				case m.filter.on && group.ID != m.filter.group:
				case m.favouritesOnly && !m.isFavourite(p):
				case q != "" && !match(p, group):
				default:
					out = append(out, p)
				}
			}
			return out
		}
		out := narrow(func(p registry.Pane, g project.Group) bool {
			return matchesQuery(p, m.aliases, g.Label, q)
		})
		if q != "" && len(out) == 0 && len(rows) > 0 {
			// Nothing CONTAINS the word. A second pass costs a frame only in the case where the
			// screen would otherwise be empty, and an empty screen is the one answer a fleet of
			// sixty sessions almost never deserves.
			loose := narrow(func(p registry.Pane, g project.Group) bool {
				return looselyMatchesQuery(p, m.aliases, g.Label, q)
			})
			if len(loose) > 0 {
				return m.favouritesFirst(loose), true
			}
		}
		rows = out
	}
	return m.favouritesFirst(rows), false
}

// favouritesFirst lifts every pinned row above every other one, and does it HERE because this is the
// single function that produces the painted set — the renderer, `A`, the cursor and the report all
// read it. A second sort anywhere else is how `A` and the renderer once came to disagree about what
// "visible" meant.
//
// STABLE, so the attention order survives inside each band: a pinned `needs` row still leads the
// pinned ones, and an unpinned `needs` row still leads the rest. Favourites do not reorder the fleet,
// they split it in two.
//
// It copies rather than sorting in place. `visibleRows` may hand back `m.panes` itself when nothing is
// hidden, and sorting that would reorder the model's own slice under the next reader — the mistake
// `Registry.Panes` was changed to make impossible.
func (m model) favouritesFirst(rows []registry.Pane) []registry.Pane {
	if m.favs == nil || len(rows) < 2 {
		return rows
	}
	pinned := 0
	for _, p := range rows {
		if m.isFavourite(p) {
			pinned++
		}
	}
	if pinned == 0 || pinned == len(rows) {
		// Nothing to lift, and no copy to pay for.
		return rows
	}
	out := make([]registry.Pane, 0, len(rows))
	for _, p := range rows {
		if m.isFavourite(p) {
			out = append(out, p)
		}
	}
	for _, p := range rows {
		if !m.isFavourite(p) {
			out = append(out, p)
		}
	}
	return out
}

// favouriteKeys is the pinned rows, keyed the way the renderer keys a row. It walks the SAME predicate
// the order does, so the star and the position can never disagree.
func (m model) favouriteKeys() map[string]bool {
	if m.favs == nil {
		return nil
	}
	keys := make(map[string]bool)
	for _, p := range m.panes {
		if m.isFavourite(p) {
			keys[MarkKey(p)] = true
		}
	}
	return keys
}

// pinnedProjects is the group ids the operator pinned, for the list that shows them. It asks the set
// directly rather than deriving from rows: a project can be pinned while every session in it is gone,
// and it must still read as pinned when they come back.
func (m model) pinnedProjects() map[string]bool {
	if m.favs == nil {
		return nil
	}
	out := make(map[string]bool)
	for _, r := range m.projectRows() {
		if m.favs.HasProject(r.Group.ID) {
			out[r.Group.ID] = true
		}
	}
	return out
}

// groupLabels is each row's project LABEL, keyed the way the renderer keys a row.
//
// It goes through `project.Labels` for the reason the project list does: two derived groups can share
// a basename across hosts, and disambiguating belongs to the one function that can see all of them at
// once. Nil in the host view, so nothing is computed for a screen that will not read it.
func (m model) groupLabels() map[string]string {
	if m.groupBy != byProject {
		return nil
	}
	rows := m.rowsForScreen()
	// UNIQUE groups, because `project.Labels` qualifies a label it sees more than once — that is how
	// two hosts' `billing-iac` are told apart. Handing it one entry per ROW made a single project
	// look like a collision with itself and every header read `BILLING-IAC @LOCAL`. Caught by a frame
	// assertion, which is the only place a wrong label is visible.
	unique := make(map[string]project.Group, len(rows))
	perRow := make([]project.Group, 0, len(rows))
	for _, p := range rows {
		g := m.rules.OfPane(p)
		unique[g.ID] = g
		perRow = append(perRow, g)
	}
	groups := make([]project.Group, 0, len(unique))
	for _, g := range unique {
		groups = append(groups, g)
	}
	labels := project.Labels(groups)
	out := make(map[string]string, len(rows))
	for i, p := range rows {
		if l := labels[perRow[i].ID]; l != "" {
			out[MarkKey(p)] = l
			continue
		}
		out[MarkKey(p)] = perRow[i].Label
	}
	return out
}

// isFavourite is the one predicate, and both halves of the operator's answer are in it: a row is
// pinned if THIS SESSION is pinned or if the PROJECT it belongs to is. Two callers would be two
// chances for a marker and an order to disagree.
func (m model) isFavourite(p registry.Pane) bool {
	if m.favs == nil {
		return false
	}
	return m.favs.HasSession(p) || m.favs.HasProject(m.rules.OfPane(p).ID)
}

// rowCursor is the row the operator is looking at, named by IDENTITY.
//
// key is the truth, and it has to be, because the list re-sorts under the operator's hand:
// §22.6 orders by last activity, so a working pane moves on nearly every tick. A cursor held
// as a POSITION then names whichever row the sort moved into that position — and every key
// reads the cursor through cursorRow, so `a` attached to a stranger, `x` hid one and `K`
// offered one for killing. Measured before this shape existed: holding `local/bravo %2`, one
// tick later the screen's own `>` sat on `nuc/charlie %3`.
//
// hint is where the row last was, and it is consulted ONLY when key is no longer in the list.
// A row that goes away — hidden, killed, cd'd out of the active project — must leave the
// cursor where it was on screen rather than sending it home to row 0, which is how an
// x-x-x pass loses its place.
//
// The index is DERIVED at every read rather than stored, and that is the part that closes the
// class instead of fixing one instance: there is no cached position for a future writer of
// m.panes to forget to re-clamp. There was such a writer — the agents poll (§22.1) assigned
// the pane list and never clamped, so a shorter listing left the cursor past the end and
// every key silently did nothing.
type rowCursor struct {
	key  SelectionKey
	hint int
}

// cursorIndex is where the cursor sits in the list that is ON SCREEN.
//
// rowsForScreen, not visibleRows: with a project filter on the two lists differ, and an index
// into the wrong one names a pane the operator cannot see (§21.5).
func (m model) cursorIndex() int {
	rows := m.rowsForScreen()
	if len(rows) == 0 {
		return 0
	}
	for i, p := range rows {
		if selKey(p) == m.cursor.key {
			return i
		}
	}
	// The row it named is not here. Stay at its position, clamped to the shorter list.
	switch {
	case m.cursor.hint >= len(rows):
		return len(rows) - 1
	case m.cursor.hint < 0:
		return 0
	}
	return m.cursor.hint
}

// cursorTo places the cursor on row i of the current screen, and it is the only writer of `m.cursor`
// that takes a POSITION — so clamping has one home and "which row is this" has one answer.
//
// There is exactly one other writer and it is deliberate: the launch names a pane that is not on the
// screen yet (see `launchedMsg`), which no index can express, and it carries the hint the fallback needs.
// The comment here used to claim to be the ONLY writer, which was false the day that one was added — and
// a wrong comment on a rule like this is worse than none, because it stops the next reader checking
// before they add a third.
func (m model) cursorTo(i int) model {
	rows := m.rowsForScreen()
	if len(rows) == 0 {
		m.cursor = rowCursor{}
		return m
	}
	switch {
	case i >= len(rows):
		i = len(rows) - 1
	case i < 0:
		i = 0
	}
	m.cursor = rowCursor{key: selKey(rows[i]), hint: i}
	return m
}

// cursorRow is the row under the cursor, and it is the ONLY way to get it.
//
// Eight places deriving the on-screen list themselves is eight chances for one of them to
// derive a different one — and with a project filter on, `visibleRows()[i]` names a DIFFERENT
// pane from the one the operator is looking at. `a` would attach to it. One accessor makes
// that impossible rather than fixed eight times.
func (m model) cursorRow() (registry.Pane, bool) {
	rows := m.rowsForScreen()
	if len(rows) == 0 {
		return registry.Pane{}, false
	}
	return rows[m.cursorIndex()], true
}

// selectedOutsideFilter counts marks the current screen cannot show. They still receive a
// send, so the screen has to say so rather than letting the operator believe the marked
// set is what they can see.
func (m model) selectedOutsideFilter() int {
	// ANY narrowing, not just the project one: `*` and a keyword hide rows exactly the same way,
	// and a mark the screen cannot show still receives the send (docs/design.md §21.17).
	if !m.narrowed() || m.sel.Len() == 0 {
		return 0
	}
	inside := map[SelectionKey]bool{}
	for _, p := range m.rowsForScreen() {
		inside[selKey(p)] = true
	}
	n := 0
	for _, k := range m.sel.Members() {
		if !inside[k] {
			n++
		}
	}
	return n
}

// projectRows is the list the project screen draws, recomputed per frame because nothing
// stores a row's project.
func (m model) projectRows() []project.Summary {
	return project.Summarise(m.rules, m.visibleRows())
}

// namingKey drives the naming overlay.
//
// Every rune is TEXT here, which is why naming is a mode rather than a focused widget: `q`
// in browse quits, and a screen that quit while someone typed a name would be worse than one
// with modes.
func (m model) namingKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		// Cancel keeps NOTHING of what was typed — a half-typed name applied by leaving would
		// be a name the operator never committed — but it keeps the SCREEN. It always returned
		// to the dashboard, so cancelling a project name threw the operator off the list while
		// a successful save returned them to it: cancelling cost strictly more than
		// committing, on a key row that calls both by name.
		m.naming = namingForm{}
		m = m.dismiss()
		return m, nil
	case tea.KeyCtrlU:
		// §21.12 rule 5: this plus enter is how a name is REMOVED.
		m.naming.input = Composer{}
		m.naming.reason = ""
		return m, nil
	case tea.KeyBackspace:
		m.naming.input.Backspace()
		m.naming.reason = ""
		return m, nil
	case tea.KeyEnter:
		return m.commitName()
	case tea.KeyRunes, tea.KeySpace:
		text, _ := typedText(msg)
		m.naming.input.Insert(text)
		// The reason goes the moment the field changes: a refusal about text that is no
		// longer there is worse than none.
		m.naming.reason = ""
		return m, nil
	}
	return m, nil
}

// commitName writes the name, or refuses it inside the overlay.
//
// The duplicate check runs against the file RE-READ, never against the copy this screen was
// drawn from (§21.12 rule 4): another hub, or a hand edit, may have taken the name since the
// overlay opened. Re-reading is also what keeps another writer's entries — a save built from
// the in-memory set alone would drop everything that arrived meanwhile.
func (m model) commitName() (tea.Model, tea.Cmd) {
	name := strings.TrimSpace(m.naming.input.Text())
	if m.naming.namingAProject() {
		return m.commitProjectName(name)
	}
	key := project.AliasKeyOf(m.naming.subject)
	if m.projectsPath == "" {
		// Only reachable from a model built without WithProjects. Refusing beats writing to
		// the empty path, which would land a file called `projects.toml` in whatever
		// directory the hub happened to start in.
		m.naming.reason = "no projects.toml path is configured, so a name cannot be saved"
		return m, nil
	}

	rules, onDisk, err := project.LoadAll(m.projectsPath)
	if err != nil {
		// A file that cannot be parsed must not be overwritten: it holds decisions, and the
		// operator can fix it by hand once told which line is wrong.
		m.naming.reason = "projects.toml: " + err.Error()
		return m, nil
	}
	if err := onDisk.Check(key, name); err != nil {
		m.naming.reason = err.Error()
		return m, nil
	}
	onDisk.Set(key, name)
	if err := project.Save(m.projectsPath, rules.Rules(), onDisk); err != nil {
		m.naming.reason = "cannot save: " + err.Error()
		return m, nil
	}
	// The in-memory set becomes what is on disk, so the screen and the file cannot disagree
	// — including about entries this hub did not write.
	m.aliases = onDisk
	m.naming = namingForm{}
	m = m.dismiss()
	// The name reaches the SESSION too, so an attached session says what the operator
	// called it (docs/design.md §12) — now rather than on the next tick, because this is
	// the gesture they just made. And if their own status line is in the way, the sentence
	// that says so carries the line to add.
	m.note = aliasNote(m.panes, m.aliases)
	return m, m.publishAliases()
}

// commitProjectName writes the project's name as a prefix RULE, or refuses inside the overlay.
//
// The refusal is the interesting half and it is a MEASUREMENT rather than an argument: a derived
// group is keyed on (host, last path segment), which is not a prefix, so project.RuleToName
// computes the ancestor of the group's own rows and then APPLIES the prospective rule to the
// whole fleet, refusing unless it captures exactly the rows it was asked to name. §21.12
// recorded this as the reason project naming was not built; the check is what closes it.
func (m model) commitProjectName(name string) (tea.Model, tea.Cmd) {
	if m.projectsPath == "" {
		m.naming.reason = "no projects.toml path is configured, so a name cannot be saved"
		return m, nil
	}
	onDiskRules, aliases, err := project.LoadAll(m.projectsPath)
	if err != nil {
		m.naming.reason = "projects.toml: " + err.Error()
		return m, nil
	}
	// m.panes, NOT visibleRows(): the check is about what the rule would capture ON DISK, and
	// the hidden set is a screen decision. Passing the visible rows let HIDING a row defeat the
	// check in the ACCEPT direction — measured, the rule was written and the hidden row silently
	// changed project — while both §21.12 and RuleToName's own comment say "applied to the whole
	// fleet". Unlike §18's hide, a wrong RULE has no self-repair: it persists in projects.toml.
	// The GROUP being named still comes from the visible rows via projectRows(), which is right.
	rule, err := onDiskRules.RuleToName(*m.naming.group, name, m.panes)
	if err != nil {
		m.naming.reason = err.Error()
		return m, nil
	}
	next := project.Replace(onDiskRules.Rules(), rule)
	// Parsed BEFORE it is written: a rule set the reader would refuse must never reach the
	// file, or the operator has to hand-edit TOML to recover from having typed a name.
	//
	// Through the SAME pair `Save` writes and `LoadAll` reads. It used to validate
	// `Parse(Render(next))` — the `[[project]]` records alone — while `Save` writes
	// `RenderAll(next, aliases)`, so the document that was checked was not the document that
	// landed. Measured over twelve hostile alias names, none makes `ParseAll` refuse (and `Set`
	// trims leading space before storing), so the gap was not reachable through a name today;
	// validating the bytes that are about to be written costs nothing and removes the question.
	// The values assigned below are the ones that ROUND-TRIPPED rather than the ones in hand.
	parsed, parsedAliases, err := project.ParseAll(project.RenderAll(next, aliases))
	if err != nil {
		m.naming.reason = "that name would make projects.toml unreadable: " + err.Error()
		return m, nil
	}
	if err := project.Save(m.projectsPath, next, aliases); err != nil {
		m.naming.reason = "cannot save: " + err.Error()
		return m, nil
	}
	m.rules, m.aliases = parsed, parsedAliases
	m.naming = namingForm{}
	m = m.dismiss()
	return m, nil
}

// selectionKey is the four keys whose subject is the SELECTION and not the cursor.
//
// They are shared rather than copied because the selection is the whole subject: each one reads
// `m.sel`, refuses an empty one with a sentence naming what is missing, and never asks a screen
// where the cursor is. That makes them screen-independent by construction — the filesystem view
// hands them straight over — and it keeps the refusals in one place, which is where a second copy
// would have drifted first (the two predicates that answered "is the list narrowed" with different
// clause lists cost this repo a whole surface).
func (m model) selectionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "i":
		if m.sel.Len() == 0 {
			m.note = "select a pane with space first — a prompt needs a target"
			return m, nil
		}
		m.note = ""
		return m.raise(modeCompose), nil
	case "!":
		// The same guard `i` has, for the same reason: without a target there is
		// nothing to interrupt, and a silent no-op reads as a broken key.
		if m.sel.Len() == 0 {
			m.note = "select a pane with space first — there is nothing to interrupt"
			return m, nil
		}
		// Interrupt is guarded by the same confirmation rule as a send: C-c into
		// the wrong pane kills whatever is actually running there.
		m.pending, m.pendingAct = broadcast.Needed(m.targetStates()), actionInterrupt
		if len(m.pending) == 0 {
			m.pendingAct = actionNone
			return m, m.interrupt("C-c")
		}
		m.note = ""
		return m.raise(modeConfirm), nil
	case "R":
		// Restart: respawn the selected pane with its session resumed
		return m.restart()
	case "K":
		// Kill: destroy the selected pane/window/session after confirmation
		return m.confirmKill()
	}
	return m, nil
}

// projectKey drives the project list.
//
// `enter` narrows and returns to the dashboard, so the list is a doorway rather than a
// place to live: every dashboard key keeps its meaning inside a project, which is what
// makes the narrowed screen the same screen (§21.4).
func (m model) projectKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.projectRows()
	switch msg.String() {
	case "esc", "P":
		// Leaving the list must not decide anything: a screen you can open is a screen
		// you can leave without narrowing the fleet.
		m.mode = modeBrowse
		return m, nil
	case "j", "down":
		if m.projCursor < len(rows)-1 {
			m.projCursor++
		}
		return m, nil
	case "k", "up":
		if m.projCursor > 0 {
			m.projCursor--
		}
		return m, nil
	case "enter":
		if m.projCursor < len(rows) {
			m = m.openProject(rows[m.projCursor].Group.ID, false)
		}
		m.mode = modeBrowse
		return m, nil
	case "ctrl+c":
		// Handled above the mode dispatch; named here so a reader of this switch does not
		// conclude the list swallows it.
		return m, nil
	case "a":
		// "Go to it", the same promise `a` makes on the dashboard: open the project with
		// the cursor ALREADY on the row that wants you. Narrowing and then hunting for
		// the waiting row is two steps for the one question the screen answers.
		if m.projCursor < len(rows) {
			m = m.openProject(rows[m.projCursor].Group.ID, true)
		}
		m.mode = modeBrowse
		return m, nil
	case "N":
		// Name the project. The subject is CAPTURED here rather than re-derived per frame,
		// and on this screen that is not merely tidy: a group cannot be "the row under the
		// cursor" while the list re-sorts under a probe, so §21.12 rule 2 is met by copying
		// it at the keystroke.
		if m.projCursor >= len(rows) {
			m.note = "nothing under the cursor to name"
			return m, nil
		}
		return m.openNamingGroup(rows[m.projCursor].Group)
	}
	// §21.14 asked what the OTHER dashboard keys do here, and the answer is a DIVISION rather
	// than a list, which is why it fits in two branches instead of twenty.
	switch msg.String() {
	case "h":
		// The subject is the FLEET — the send log, the host set, a new session — so the list
		// is no reason to refuse. Each goes through the SAME entry point the dashboard uses,
		// so there is one implementation per key rather than two that drift.
		//
		// The MODE is not set here, unlike the two below: openHistory is the one of the three
		// that does not set its own, so a refusal (no log, unreadable log) would have thrown
		// the operator off the list onto the dashboard — while the neighbouring pane-key
		// refusal keeps the screen. The historyMsg handler switches the mode when entries
		// actually arrive.
		return m.openHistory()
	case "p":
		// The MODE is not set here either. It used to be `modeBrowse` first, which was the only
		// coherent thing to do while every overlay exited to the dashboard by name — the operator was
		// going to land there anyway, so at least the transition matched. With `raise`/`dismiss` the
		// picker returns to the screen it was raised over, and that line became a teleport off the
		// list for no reason: after saving a host set, the list is where the operator was.
		return m.openPicker()
	case "v":
		// The dashboard's view key, answered rather than swallowed: this screen IS per project, so
		// there is nothing to switch — and the generic "does nothing here" would leave the operator
		// wondering whether the key had arrived at all.
		m.note = "this list is per project already — v switches the DASHBOARD between per-host and " +
			"per-project"
		return m, nil
	case "f":
		// The subject is the GROUP under the cursor, captured here rather than re-derived: the
		// list re-sorts under a poll, and §21.12 rule 2 is met by copying it at the keystroke.
		if m.projCursor >= len(rows) {
			m.note = "nothing under the cursor to pin"
			return m, nil
		}
		m = m.toggleFavouriteProject(rows[m.projCursor].Group)
		return m, nil
	case "n":
		// Same as `p` above: the form returns to this list now, and creating a session from a project
		// is a gesture whose answer belongs on the screen that shows the projects.
		return m.openLaunchForm(), nil
	case "i", "!", "R", "K", "x", "X", "A", "C", " ", "r":
		// The subject is a PANE, and a list of projects has none. Saying what is missing is
		// the whole point: "nothing happened" is what a broken key looks like, and this
		// answer names the way forward rather than only refusing.
		m.note = fmt.Sprintf("%s acts on a session — press enter to open a project first",
			keyName(msg))
		return m, nil
	}
	// Anything else answers too, naming itself.
	m.note = fmt.Sprintf("%s does nothing here — j/k move, enter narrows, a goes to what "+
		"waits, esc goes back", keyName(msg))
	return m, nil
}

// openLaunchForm opens the launch form, defaulting to the host under the cursor.
//
// A method rather than an inline case because the project LIST reaches it too: `n`'s subject
// is the fleet, so the list is no reason to refuse it, and two copies of "which host does a
// new session default to" is one too many.
// goTo is what `a` means on ANY screen: take me to this session.
//
// It was the dashboard's `a` case, and it is a method because a SECOND screen now presses the same key
// — the filesystem view. This repo has already paid for the other arrangement twice: when two paths
// create the same kind of thing through two functions, the weaker one is where the missing case lives
// (§22.3's door and `tmux.NewSession` differed in what their ERRORS could express, so a retry that
// looked missing was impossible). Every clause below is a decision with its own measurement, and
// re-typing them for the tree would have meant re-deciding them.
func (m model) goTo(p registry.Pane) (tea.Model, tea.Cmd) {
	h, ok := hostFor(m.hosts, p.Host)
	if !ok {
		m.note = "cannot attach: host " + p.Host + " is not in this hub's list"
		return m, nil
	}
	// A pane-less BACKGROUND row has a door (§22.3): the hub makes the pane the row is missing by
	// running `claude attach <id>` in a session on that row's host, and possession then takes over the
	// pane the create returns. Every other agent row is refused with the kind it carries and the
	// remedy for it (§22.8).
	if open, why := wakeable(p); open {
		// The tunnel is checked BEFORE any dial, because MarkHostStale deliberately leaves agent rows
		// live: a session on a host whose tmux tunnel is down is still running there, so the pane
		// path's socket message would describe a mechanism this row does not have.
		if h.Status == hub.Down || h.Status == hub.Connecting {
			m.note = fmt.Sprintf("%s's tunnel is down; %q is still running there — press p to "+
				"re-probe", p.Host, wakeSubjectName(p))
			return m, nil
		}
		// A wake that can COST something asks first, and what it costs is on the screen. A free one
		// acts immediately: a confirmation for a free action teaches the operator to press enter
		// without reading.
		if wakeCostsWork(p.AgentWord) {
			m.note = ""
			m.pendingWake = &wakeSubject{row: p, host: h}
			return m.raise(modeWake), nil
		}
		return m.wake(p, h)
	} else if why != "" {
		m.note = why
		return m, nil
	}
	return m.possess(p, h)
}

func (m model) openLaunchForm() model {
	cursorHost, cursorSession, cursorCWD := "", "", ""
	if p, ok := m.cursorRow(); ok {
		cursorHost = p.Host
		cursorCWD = p.Path
		// ONLY for a pane row. `SessionID` is dual-purpose — the design says so beside `Command` —
		// and on an AGENT row it holds Claude's own session uuid, not a tmux `$N`. Passing that to
		// `new-window -t` earns `-t value has the wrong shape: "1ff133f7-…" is not a session` from
		// the assert layer, which is the right refusal and the wrong question to ask. A pane-less
		// row has no session of the operator's to put a window beside, so the launch picks one.
		cursorSession = paneSessionTarget(p)
	}
	return m.openLaunchFormFor(cursorHost, cursorSession, cursorCWD)
}

// paneSessionTarget is the tmux session a new window may be put beside, or "" when there is none.
//
// ONLY for a pane row. `SessionID` is dual-purpose — the design says so beside `Command` — and on an
// AGENT row it holds Claude's own session uuid, not a tmux `$N`. Passing that to `new-window -t` earns
// `-t value has the wrong shape: "1ff133f7-…" is not a session` from the assert layer, which is the
// right refusal and the wrong question to ask. A pane-less row has no session of the operator's to put
// a window beside, so the launch picks one.
func paneSessionTarget(p registry.Pane) string {
	if p.Kind == registry.KindPane {
		return p.SessionID
	}
	return ""
}

// openLaunchFormFor opens the form on a STATED address, which is what a second screen needs.
//
// The dashboard derives all three from the row under its cursor; the filesystem view derives them
// from a DIRECTORY, which is the whole point of the metaphor — a session created inside a node lands
// in that node. One function so the form is built once: the pre-filled directory is a promise about
// where the session will run, and two builders are two chances to make it in one place only.
func (m model) openLaunchFormFor(host, session, cwd string) model {
	m.launchForm = newLaunchForm(m.hosts, host, session, cwd)
	return m.raise(modeLaunch)
}

// openProject narrows to a group and places the cursor.
//
// One function for both doorways, so `enter` and `a` cannot disagree about what "inside a
// project" means — they differ only in where the cursor lands.
func (m model) openProject(id string, onWaiting bool) model {
	m.filter.on, m.filter.group = true, id
	at := 0
	if onWaiting {
		// The FIRST waiting row, and the rows are already ordered by how long each has
		// waited (§21.11.1), so this is the one that has waited longest. A project where
		// nothing waits falls back to row 0 rather than refusing: "go to it" must not
		// become a key that sometimes does nothing.
		for i, p := range m.rowsForScreen() {
			if p.State() == state.Needs {
				at = i
				break
			}
		}
	}
	return m.cursorTo(at)
}

// nextProject moves the filter to the group after the current one, cycling at the end.
//
// Cycling rather than stopping (§21.14 left this open): a key that reads as "next" and
// silently does nothing at the boundary is indistinguishable from a key that is broken. With
// no filter on it opens the FIRST project, so `tab` has a defined first step too.
func (m model) nextProject() model {
	rows := m.projectRows()
	if len(rows) == 0 {
		return m
	}
	at := -1
	if m.filter.on {
		for i, s := range rows {
			if s.Group.ID == m.filter.group {
				at = i
				break
			}
		}
	}
	return m.openProject(rows[(at+1)%len(rows)].Group.ID, false)
}

// hiddenRowKeys returns the keys of every row the operator has marked hidden AND that the filter
// would keep off the screen.
//
// It is NOT conditional on showHidden, and that is what makes it correct rather than what makes it
// lax: while the filter is on those rows are not in the list the renderer is handed, so no marker
// can appear for them. The filter does the work, and the producer needs no branch that a future
// caller could get the wrong way round.
//
// A RESURFACED row is deliberately absent — hide.Set splits marked rows into `Hidden` (off-screen)
// and `Resurfaced` (asking, so back on screen), and the latter carries `[↑]`, which says strictly
// more than "hidden". One marker per row is what makes either of them readable.
func (m model) hiddenRowKeys() map[string]bool {
	keys := make(map[string]bool)
	if m.hidden == nil {
		return keys
	}
	for _, p := range m.panes {
		if m.hidden.Hidden(p) {
			keys[MarkKey(p)] = true
		}
	}
	return keys
}

// resurfacedKeys returns a map of pane keys that are resurfaced (marked as
// hidden but shown because they're waiting for the user).
func (m model) resurfacedKeys() map[string]bool {
	keys := make(map[string]bool)
	if m.hidden == nil {
		return keys
	}
	for _, p := range m.panes {
		if m.hidden.Resurfaced(p) {
			keys[MarkKey(p)] = true
		}
	}
	return keys
}

// toggleFavouriteSession pins or unpins the SESSION under the cursor.
//
// The subject is the row under the cursor and never the selection, which is the one place this key
// differs from `x` on purpose: `x` acts on the selection because hiding many rows at once is the
// common act, while pinning is a statement about ONE thing the operator keeps coming back to. A
// selection-wide pin would also be irreversible in one keystroke — `f` again would unpin whatever the
// selection had become.
// It takes its SUBJECT rather than reading the cursor, because a second screen now presses this key:
// the filesystem view's cursor indexes tree LINES, so `cursorRow()` there names a row the operator is
// not pointing at. The subject is still the row under the cursor and never the selection — that is the
// one place this key differs from `x` on purpose, and the difference belongs to the CALLER now.
func (m model) toggleFavouriteSessionOf(p registry.Pane, ok bool) model {
	if m.favs == nil {
		m.note = "this hub keeps no favourites"
		return m
	}
	if !ok {
		m.note = "nothing under the cursor to pin"
		return m
	}
	name := p.Session
	if name == "" {
		name = p.AgentID
	}
	if name == "" {
		// A row with no session name cannot be keyed, and a key of ("", host) would match every
		// other nameless row on that host — so it is refused rather than written.
		m.note = "that row has no session name, so there is nothing to pin it by"
		return m
	}
	was := m.favs.HasSession(p)
	if err := m.favs.ToggleSession(p); err != nil {
		m.note = "could not save favourites: " + err.Error()
		return m
	}
	// shortSubject, because this note is a footer claimant like any other and a session named after
	// its own prompt runs past the width — measured live at 100+ columns, where the clause that says
	// what pinning DOES was the part the fitter dropped.
	if was {
		m.note = "unpinned " + shortSubject(name)
	} else {
		m.note = "pinned " + shortSubject(name) + " — it stays above the rest"
	}
	return m
}

// toggleFavouriteProject pins or unpins a whole PROJECT, so every session in it rises.
func (m model) toggleFavouriteProject(g project.Group) model {
	if m.favs == nil {
		m.note = "this hub keeps no favourites"
		return m
	}
	was := m.favs.HasProject(g.ID)
	if err := m.favs.ToggleProject(g.ID); err != nil {
		m.note = "could not pin that project: " + err.Error()
		return m
	}
	if was {
		m.note = "unpinned " + g.Label
	} else {
		m.note = "pinned " + g.Label + " — every session in it stays above the rest"
	}
	return m
}

// hideSubject toggles hiding for the selection if there is one, else for the
// pane under the cursor.
//
// It does NOT clamp the cursor, and the comment that said it did was stale: the cursor names a ROW by
// identity, so a hidden row needs nothing done to the cursor — the fallback in `cursorIndex` answers.
func (m model) hideSubject() model {
	// If there's a selection, hide every selected pane. Otherwise hide the pane
	// under the cursor. This is the same rule the send path uses: the selection
	// is the user's stated subject.
	return m.hidePanes(m.hideSubjectsFrom(m.cursorRow()))
}

// hideSubjectsFrom is the SUBJECT rule — the selection if there is one, otherwise the row the caller
// points at — and it is a function because a second screen presses `x` now: the filesystem view's
// cursor indexes tree LINES, so `cursorRow()` there names a row the operator is not pointing at.
func (m model) hideSubjectsFrom(fallback registry.Pane, ok bool) []registry.Pane {
	var toHide []registry.Pane
	if m.sel.Len() > 0 {
		for _, sk := range m.sel.Members() {
			for _, p := range m.panes {
				if p.Host == sk.Host && p.PaneID == sk.PaneID {
					toHide = append(toHide, p)
					break
				}
			}
		}
		return toHide
	}
	if ok {
		toHide = append(toHide, fallback)
	}
	return toHide
}

// hidePanes toggles the hidden mark on a stated set.
func (m model) hidePanes(toHide []registry.Pane) model {
	if m.hidden == nil {
		m.note = "cannot hide: hidden set is not available"
		return m
	}
	if len(m.rowsForScreen()) == 0 {
		return m
	}

	// A row with no pane is REFUSED (§22.5), and the reason is LIFETIME: the mark would carry
	// nothing that expires, so it would outlive the session it was taken against and hide a
	// future row that happens to look the same.
	//
	// The first version of this guard gave a different reason — that the mark would land on a
	// DIFFERENT row — and that was true then and is not now. The key degenerated to (host, name)
	// for an agent row, so a real tmux pane at window 0, index 0 of a session with that name
	// produced the identical key and one press hid both. A review found the guard only covered
	// the agent SUBJECT, leaving the pane direction live, and the answer was not a second guard:
	// `Kind` went into `hide.Key` (v3), so the two keys can no longer be equal and neither
	// direction needs noticing. What is left is the policy, which is why this guard stays.
	//
	// ONE guard for both paths above rather than one per branch: the cursor path is the commonest
	// gesture and a guard in the selection branch alone would leave `x` live on it.
	//
	// §22.5 words the reason as `nothing about this row survives a restart to hide it by`, which
	// with the row's name is 93 columns against the 77 an 80-column footer leaves a shared
	// claimant. The shipped sentence says the same thing in 68.
	//
	// A MIXED selection refuses WHOLE, the rule `K` follows: hiding the pane rows and skipping the
	// rest is a partial action nobody asked for, and an operator who read only the tail would
	// believe the panes were hidden.
	for _, p := range toHide {
		if p.Kind != registry.KindAgent {
			continue
		}
		name := p.Session
		if name == "" {
			// A listing that reports no name still has to be nameable in a sentence, and the
			// short id is what `claude logs` takes.
			name = p.AgentID
		}
		if name == "" {
			name = "this row"
		}
		// Bounded through shortSubject: measured live, an 88-column session name pushed `a mark
		// would never expire` off the footer with a `+1`, so the operator was told the hub refused
		// and never why.
		m.note = fmt.Sprintf("nothing hidden — %s has no pane; a mark would never expire",
			shortSubject(name))
		return m
	}

	// Toggle each pane and collect errors.
	//
	// deferred counts the rows that were just MARKED while they are still asking for the
	// operator. Those are the ones where `x` looks like it did nothing: §18's resurface
	// rule keeps a waiting row on screen, so the mark is real but invisible, and a key
	// that reads as "not now" silently means "forever, once it goes quiet"
	// (docs/design.md §21.11.2). The behaviour is right and stays; the silence is what
	// is wrong.
	deferred := 0
	var deferredName string
	for _, p := range toHide {
		if err := m.hidden.Toggle(p); err != nil {
			// A hidden set that cannot be written is worth saying out loud, and
			// the error carries the fix (the path that could not be written).
			m.note = "cannot save hidden set: " + err.Error()
			return m
		}
		// Read the mark back rather than assuming the direction: Toggle flips, so the
		// same keypress un-marks, and a sentence that only knows one direction is worse
		// than none.
		if m.hidden.Resurfaced(p) {
			deferred++
			deferredName = p.Session
		}
		// If the pane is now hidden (not just marked — a resurfaced pane is
		// visible and should stay selected), remove it from the selection.
		// Hidden() is the one owner for "is this pane visible", and it returns
		// false for a marked pane in needs state, which is correct here.
		if m.hidden.Hidden(p) {
			sk := selKey(p)
			if m.sel.Has(sk) {
				m.sel.Toggle(sk) // Toggle removes when present
			}
			if m.atSelection != nil {
				delete(m.atSelection, sk)
			}
		}
	}

	// Say what was recorded, and say WHEN it takes effect — the row is still on screen,
	// so "hidden" on its own would read as a lie. A row that was not waiting vanished
	// the moment it was marked, which is its own feedback and needs no sentence in a
	// note channel four other things want.
	switch {
	case deferred == 1:
		m.note = fmt.Sprintf("hidden: %s stays while it is waiting, and goes when it "+
			"stops asking", deferredName)
	case deferred > 1:
		m.note = fmt.Sprintf("hidden %d rows: each stays while it is waiting, and goes "+
			"when it stops asking", deferred)
	}

	// No clamp: the cursor names a ROW, and cursorIndex derives its position from the list
	// as it is now. A row that just went away leaves the cursor at its position by the same
	// rule that a shorter list does, and there is no stored index to correct.
	return m
}

// Run starts the dashboard. The first paint happens before any poll completes,
// so a usable screen is on display immediately (docs/design.md §16).
//
// hist may be nil (--no-history); everything else is built here.
func Run(ctx context.Context, extra []hub.Host, local bool, log *hub.StateLog, hist *history.Log, hidden *hide.Set, opts ...Option) error {
	run := tmux.NewExec(5 * time.Second)
	m := build(ctx, run, extra, local, log, hist, hidden, opts...)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()

	// The shutdown half of the sweep, and it runs whatever the program returned —
	// including a panic bubbling out of a cmd, which is exactly the case that leaves
	// a payload behind as the user's most recent paste buffer (sweep.go). Its
	// context is a fresh one: the caller's may already be cancelled by the signal
	// that got us here.
	sctx, cancel := context.WithTimeout(context.Background(), SweepTimeout)
	defer cancel()
	for _, h := range m.hosts {
		if _, serr := broadcast.Sweep(sctx, m.run, h.Target()); serr != nil {
			// Logged and continued: one unreachable host must not stop the others
			// being cleaned, and this is the last chance to clean any of them.
			fmt.Fprintf(os.Stderr, "tmux-hub: could not sweep paste buffers on %s: %v\n",
				h.Label, serr)
		}
	}
	return err
}

// build assembles the model. Run is a thin shell over it so a test can build the
// model the SAME way with a fake tmux underneath — every ui test used to hand-build
// a `model{…}` with just the fields it needed, and a model whose sender could not
// send satisfied every one of them.
//
// The hub's own coordinates are read HERE and are not parameters, which is the
// same lesson one layer up. As parameters they were two same-typed strings that
// every test passed as `"", ""`: deleting the epoch read in Run left `selfEpoch`
// empty, so every local target silently reverted to the pre-§20 full-screen attach
// with the whole jump path dead, and swapping the two arguments at the one real
// call site compiled, stayed green, and made every `a` die at the seam with
// `-t value has the wrong shape`. Neither is expressible now: there is one reader
// per field, inside the constructor the wiring floor already exercises.
// Option is a piece of wiring main supplies that the constructor cannot read for
// itself. It is a function over the unexported model deliberately: only this package
// can write one, so the set of things main may reach into stays enumerable.
type Option func(*model)

// WithView chooses the screen the hub opens on: `tree` (the filesystem view) or `flat` (the
// attention-ordered list).
//
// The DEFAULT is the tree, which is what the operator asked for — "if positive, I would take this as
// the default view" — and the conditions that made it defensible are met: every node carries the
// attention rolled up beneath it, so a closed tree answers "who needs me" in a handful of lines where
// the flat list needs sixty rows on a screen that holds twenty-five; single-child chains collapse; and
// `a` on a node goes straight to the row that has waited longest inside it.
//
// It is a FLAG and not only a keystroke for two reasons. An operator who prefers the flat list should
// not press `t` on every start — a preference you have to re-enter is not a preference. And the e2e
// suite is two hundred cases written against the flat list: a flag lets each fixture say which screen
// it is about in one line, where a keystroke in every case would put the thing under test one paint
// further away from the thing that starts it.
//
// The PICKER still wins. §9 opens it at startup when there is no hosts.toml, and a first run has
// nothing to show in either view — so this only sets the mode when nothing else has claimed it.
func WithView(name string) Option {
	return func(m *model) {
		if name != "tree" {
			return
		}
		if m.mode.isOverlay() {
			// AN OVERLAY IS ALREADY OPEN — the first-run picker, which `WithPicker` raises before this
			// option runs — so the SCREEN to set is the one underneath it. Refusing here (which this
			// did at first) meant the picker's own base drew the flat list and dismissing it landed the
			// operator on the flat list, on the one run where they have never seen either screen.
			//
			// Between the two options the order no longer matters: whichever runs first, the picker
			// captures its underlay through `raise` and this writes the screen wherever it belongs.
			m.underlay = modeTree
			return
		}
		m.mode = modeTree
	}
}

// WithPicker wires the picker.
//
// It takes no rows: they come from the port and only from the port. Probing ten hosts
// took 7.65 s wall and §16 forbids that before the first paint, so main cannot have
// rows to hand over anyway — and one path in means the two rules that build a row (a
// Skip candidate is not a row, hosts.toml outranks the probe) cannot be applied
// differently by a caller than by the screen (review I6).
//
// `kept` is what hosts.toml said and the picker needs it WHOLE, not as a set of
// aliases: a row carries only what the screen shows, so saving from rows alone would
// drop every `tags` the user wrote. It is collapsed here — a duplicate or blank alias
// is the file's defect and this screen is where a person can be told about it.
//
// `reserved` is the labels the fleet has already spoken for — `hub.LocalLabel` and every
// `--host` entry. They are refused on the screen because `hostsFor` refuses them fatally at
// the NEXT startup, which exits before the TUI: without this the picker can write a file
// that stops the program starting, and the only remedy is to hand-edit TOML.
//
// `open` shows the screen at startup, which is what §9 asks for when hosts.toml is
// absent, and it makes Init ask once.
func WithPicker(ports PickerPorts, kept []hostset.Entry, reserved []string, open bool) Option {
	return func(m *model) {
		m.pickerPorts, m.pickerReserved = ports, reserved
		*m = m.withKept(kept)
		if open {
			// THROUGH `raise`, so the screen underneath is recorded rather than assumed. Setting the
			// mode by hand left `underlay` at the dashboard, and on a first run with the default view
			// that meant saving a host set dropped the operator onto the FLAT list — they never saw the
			// screen the hub opens on. `WithView` covers the other option order.
			m.pickerAutoProbe = true
			*m = m.raise(modePicker)
		}
	}
}

// WithFavourites wires the operator's pinned sessions and projects.
//
// An Option rather than an eighth positional argument to `build`: the seven it already takes are the
// shape a params struct exists to remove, and every test that builds a model would have had to name a
// set it does not use. A hub with no favourites is a working hub, which is exactly what an Option
// expresses.
func WithFavourites(f *fav.Set) Option {
	return func(m *model) {
		m.favs = f
		// The warning goes to the note for the reason the hidden set's does: an operator whose marks
		// are not being applied has to be told, or the screen is simply in the wrong order.
		if w := f.Warning(); w != "" && m.note == "" {
			m.note = w
		}
	}
}

// WithProjects wires the operator's project overrides.
//
// It takes the parsed rules and the warning separately because §21.11.3's whole reason for
// a second file is the FAILURE mode: an unparseable projects.toml must lose names and keep
// the fleet, where an unparseable hosts.toml stops the program. So main reads it, keeps
// whatever it got — an empty rule set is a working screen — and hands the reason down to
// be shown. A signature that returned only rules would make silence the only option.
func WithProjects(rules project.Rules, aliases project.Aliases, path, warn string) Option {
	return func(m *model) {
		m.rules, m.aliases, m.projectsPath, m.rulesWarn = rules, aliases, path, warn
	}
}

func build(ctx context.Context, r tmux.Exec, extra []hub.Host, local bool, log *hub.StateLog, hist *history.Log, hidden *hide.Set, opts ...Option) model {
	reg := registry.New()
	p := hub.NewPoller(r, reg)
	var hosts []hub.Host
	if local {
		hosts = append(hosts, p.AddLocal())
	}
	for _, h := range extra {
		p.Add(h)
		h.Status = hub.Connecting
		hosts = append(hosts, h)
	}

	// One instance id for this process, and the stamper and sender share it: the
	// option name the guard reads and the buffer names the sweep matches are both
	// derived from it, so two hubs on one host cannot touch each other's state.
	inst := broadcast.NewInstance()
	st := broadcast.NewStamper(r, inst)

	// The hub's own server identity, read once. A failure is not fatal: an unknown
	// epoch disables the jump path, and a local target falls back to the full-screen
	// attach while a remote target still gets a window (the only path that works for
	// remotes).
	selfSession := hub.SelfSessionID()
	selfEpoch := ""
	if s := hub.SelfSocket(); s != "" {
		if e, err := tmux.ServerEpoch(ctx, r, tmux.Target{Label: hub.LocalLabel, Socket: s}); err == nil {
			selfEpoch = e
		}
	}

	m := model{
		poller: p, reg: reg, ctx: ctx,
		hosts: hosts,
		width: 80, height: 24, log: log,
		run:             r,
		sender:          broadcast.NewSender(r, st, inst),
		stamper:         st,
		keeper:          broadcast.NewKeeper(st),
		hist:            hist,
		hidden:          hidden,
		walk:            walkerFor,
		atSelection:     map[SelectionKey]paneSnapshot{},
		lastOutcome:     map[SelectionKey]broadcast.Outcome{},
		askedWindowName: map[string]string{},
		identBusy:       map[string]bool{},
		identErr:        map[string]string{},
		identTimeout:    IdentTimeout,
		selfSession:     selfSession,
		selfEpoch:       selfEpoch,
		self:            selfCommand(),
		home:            operatorHome(),
	}
	// Surface the warning when the hidden set could not be used, so the user knows
	// their marks are not being applied.
	if hidden != nil {
		if w := hidden.Warning(); w != "" {
			m.note = w
		}
	}
	for _, opt := range opts {
		opt(&m)
	}
	return m
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.poll(), m.pollAgents(), m.sweep()}
	// First run: the picker is up and has nothing to show. The probe runs HERE rather
	// than before the program starts, because Init's commands run after the first
	// paint — ten hosts probed concurrently took 7.65 s wall and §16 promises a usable
	// screen in 50 ms.
	if m.pickerAutoProbe && m.pickerPorts.Probe != nil && len(m.picker) == 0 {
		cmds = append(cmds, m.probeHosts())
	}
	return tea.Batch(cmds...)
}

// sweep clears every hub paste buffer from every host at connect. It is not
// tidiness: a hub that died mid-send left its payload as the MOST RECENT buffer on
// that server, so the user's next `prefix ]` pastes someone's prompt (sweep.go).
//
// A failure is not reported here. The commonest reason a sweep fails is a host that
// is not answering, and the host line says that in its own words a tick later; an
// error banner on the first screen would name the sweep for a fault that is not
// about the sweep.
func (m model) sweep() tea.Cmd {
	if m.run == nil || len(m.hosts) == 0 {
		return nil
	}
	r, ctx, hosts := m.run, m.ctx, m.hosts
	return func() tea.Msg {
		var removed int
		for _, h := range hosts {
			got, _ := broadcast.Sweep(ctx, r, h.Target())
			removed += len(got)
		}
		return sweptMsg{removed: removed}
	}
}

// sweptMsg carries what the connect-time sweep found. A non-zero count is worth
// saying: it means a previous hub died holding a payload.
type sweptMsg struct{ removed int }

// pollAgents asks Claude for its own account of its sessions. Most of them have
// no tmux pane at all, so without this the inbox simply cannot see them — nine
// were `blocked`, i.e. waiting for the user, across three machines when this was
// measured.
func (m model) pollAgents() tea.Cmd {
	return func() tea.Msg {
		// The join goes FIRST, and this is the last point before the listing arrives where the
		// hub holds both halves of a pane's identity. UpdateAgents can only fold a listing row
		// into the pane running it if that pane already carries its Claude session id.
		m.joinAdoptedSessions()
		hosts := m.poller.TickAgents(m.ctx, time.Now())
		return agentsMsg{hosts: hosts, panes: m.reg.Panes()}
	}
}

// joinAdoptedSessions tells the registry which Claude session each hub-created pane is running, so
// a listing row for a session that HAS a pane updates that pane instead of adding a second row.
//
// It was the missing wire: `Registry.SetClaudeSession` and `proc.SessionID` both had zero callers
// outside their own tests, so the absorb branch in UpdateAgents could never fire. A session with a
// pane therefore appeared twice — once as a pane row, once as a pane-less one — §14's only
// supportable answer to quiet-versus-idle was not in effect, and the door would have duplicated
// every row it woke, since every pane `a` creates is hub-created and therefore Adopted.
//
// ONE reconciliation from the identity store rather than a `SetClaudeSession` call beside every
// place a session id is learned: a new producer of that fact then needs no second wire, and there
// is no call site for a future author to forget. Adopt covers hub-created panes today; a walk that
// learns a session id records it in the same store and arrives here for free.
//
// It runs on the tick's goroutine, so it takes a SNAPSHOT and applies it — the two locks are taken
// in sequence and never nested. A callback form would hold the Keeper's lock while taking the
// registry's, and the paint path takes those two in the opposite order.
func (m model) joinAdoptedSessions() {
	if m.reg == nil {
		return
	}
	for _, s := range m.keeper.SessionSnapshot() {
		m.reg.SetClaudeSession(s.Host, s.PaneID, s.SessionID)
	}
}

// identMsg says ONE HOST's round of identification has finished. It carries no
// answers: those live in the Keeper, which the confirmation rule reads directly.
// What it carries is the host — so the next round for that host can start without
// waiting for any other — and the failure, because "nothing is identified" and "I
// could not look" are the same screen otherwise.
type identMsg struct {
	host string
	err  string
}

// walkerFor picks the transport for a host's process walk. It mirrors hub's
// fetcherFor for the same reason: a host reached only by a forwarded socket has no
// shell to run the walk in, so the remote half of §7 needs the ssh master §8
// already requires for attach.
//
// A host with no walker identifies nothing, which makes it READ-ONLY rather than
// merely talkative: no pane on it is ever stamped, so the confirmation dialog names
// every one of its panes and the send that follows is refused for want of a token.
// Polling is unaffected — the forwarded socket carries that on its own.
func walkerFor(h hub.Host) proc.Walker {
	switch {
	case h.Remote() && h.ControlPath != "":
		return proc.OverSSH(h.ControlPath, h.SSHDest, WalkTimeout)
	case h.IsLocalServer():
		// IsLocalServer, not !Remote(). This is the line that gates every write, and
		// keying it on the absence of an ssh destination meant a forwarded socket handed
		// over without `ssh=` was answered from the LOCAL process table using REMOTE pane
		// pids. Measured: 97 of 3117 local pids report "an agent is at or under this pid",
		// pid 1 among them — so a colliding remote pane was marked identified, stamped,
		// and written to as a single fresh target with no dialog. Anything that is not
		// this machine's server now gets no walker, identifies nothing, and is read-only.
		return proc.Local()
	default:
		return nil
	}
}

// identJob is one host's share of a round of identification: the transport, the
// server, and the panes to answer for.
type identJob struct {
	walker proc.Walker
	target tmux.Target
	panes  []broadcast.PaneRef
}

// identityJobs works out what a round of identification would have to do, one entry
// per host worth walking (docs/design.md §7). A round runs on the poll tick and
// again the moment the selection changes: a pane selected between two ticks holds no
// token, and a send to it is refused for a reason the user cannot act on.
func (m model) identityJobs() []identJob {
	if m.keeper == nil || m.walk == nil {
		return nil
	}
	var jobs []identJob
	for _, h := range m.hosts {
		var refs []broadcast.PaneRef
		anySelected := false
		for _, p := range m.panes {
			// Agent rows have no pane and so no process to walk: their state comes
			// from Claude's own listing, and they are not writable targets.
			if p.Host != h.Label || p.Kind != registry.KindPane {
				continue
			}
			sel := m.sel.Has(selKey(p))
			anySelected = anySelected || sel
			refs = append(refs, broadcast.PaneRef{
				PaneID: p.PaneID, PanePID: p.PanePID, Stamp: sel,
			})
		}
		if len(refs) == 0 {
			continue
		}
		// A REMOTE walk costs a round trip, so it runs only while something on that
		// host is selected — which is the cost §7 budgeted for it. A local walk is
		// one /proc pass and runs always, and that is what makes "was this an agent
		// when you selected it" answerable at all: the answer has to predate the
		// selection.
		//
		// Measured, that pass costs 54 ms against 2920 processes (proc's
		// TestLocalWalkCost) — 4.5% of one core at this tick rate, on a machine with
		// an unusually large process table, in a goroutine that holds nothing up. Two
		// thirds of it is the stat pass, which is irreducible because a descendant's
		// parent chain can pass through any process; the lever, if it is ever needed,
		// is this cadence rather than the walk.
		if h.Remote() && !anySelected {
			continue
		}
		jobs = append(jobs, identJob{m.walk(h), h.Target(), refs})
	}
	return jobs
}

// identifyHost runs ONE host's round, under a deadline of its own, and reports for
// that host alone. Both halves of that sentence are the fix for a fail-open.
//
// Before, one round covered the fleet and did not return until every host's walk had
// finished, guarded by a single in-flight flag. So one host that stopped answering
// stopped re-identification on every host — and for the whole of that window
// Keeper.Identified keeps answering with the last COMPLETED round: a pane whose agent
// has exited still reads as identified, still carries its token, and a paste plus
// Enter lands at a shell prompt. That is the one direction this code must never fail
// in, so a round that overruns its deadline calls Forget and the host's panes read as
// unidentified — the answer that makes the confirmation dialog ask.
//
// The overrun round is left to finish in its own time, and it cannot do harm on the
// way out: its context is dead, so every tmux write in it fails, and Stamper records
// a token only after rc == 0 — so it cannot stamp a pane, and the most it can still
// do is report its panes unidentified a second time.
func (m model) identifyHost(j identJob) tea.Cmd {
	keeper, parent, label := m.keeper, m.ctx, j.target.Label
	bound := m.identTimeout
	if bound <= 0 {
		bound = IdentTimeout
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, bound)
		defer cancel()
		// Buffered, so the goroutine can always deliver its answer and exit even
		// when nothing is waiting for it any more.
		done := make(chan error, 1)
		go func() { done <- keeper.Refresh(ctx, j.walker, j.target, j.panes) }()
		select {
		case err := <-done:
			if err != nil {
				return identMsg{host: label, err: label + ": " + err.Error()}
			}
			return identMsg{host: label}
		case <-ctx.Done():
			keeper.Forget(label)
			if parent.Err() != nil {
				return identMsg{host: label, err: label + ": identification was cancelled"}
			}
			return identMsg{host: label, err: fmt.Sprintf(
				"%s: identification did not finish in %s — every pane there now reads as unidentified",
				label, bound)}
		}
	}
}

// identify starts a round on every host that has not got one out already.
//
// Hosts run CONCURRENTLY and report SEPARATELY, for the same reason the poll does: a
// remote walk is an ssh round trip, and one that never comes back must cost its own
// panes a confirmation dialog rather than costing every other host its freshness.
func (m *model) identify() tea.Cmd {
	var cmds []tea.Cmd
	for _, j := range m.identityJobs() {
		if m.identBusy[j.target.Label] {
			continue
		}
		if m.identBusy == nil {
			m.identBusy = map[string]bool{}
		}
		m.identBusy[j.target.Label] = true
		cmds = append(cmds, m.identifyHost(j))
	}
	// Batch of none is nil and batch of one is that one command, so a single-host
	// hub pays nothing for the fan-out.
	return tea.Batch(cmds...)
}

// identWarning is why identification is not answering, for the hosts where it is not.
// Sorted, because a line that reorders itself from tick to tick reads as changing
// when nothing has changed.
func (m model) identWarning() string {
	if len(m.identErr) == 0 {
		return ""
	}
	msgs := make([]string, 0, len(m.identErr))
	for _, v := range m.identErr {
		msgs = append(msgs, v)
	}
	sort.Strings(msgs)
	return strings.Join(msgs, "; ")
}

// mark toggles one pane's selection AND records what it looked like at that moment.
// Both halves live here so they cannot come apart: a selection with no snapshot
// makes every "changed since selection" clause read as changed, and a snapshot with
// no selection is dead weight.
func (m *model) mark(p registry.Pane) {
	k := selKey(p)
	if m.sel.Has(k) {
		m.sel.Toggle(k)
		delete(m.atSelection, k)
		return
	}
	m.sel.Toggle(k)
	if m.atSelection == nil {
		m.atSelection = map[SelectionKey]paneSnapshot{}
	}
	m.atSelection[k] = paneSnapshot{
		identified: m.keeper.Identified(p.Host, p.PaneID),
		session:    p.SessionID,
		window:     p.WindowID,
		epoch:      p.Epoch,
	}
}

// selKey turns a registry pane into a selection key. Two hosts both have a %0, so
// the host is part of it.
func selKey(p registry.Pane) SelectionKey {
	return SelectionKey{Host: p.Host, PaneID: p.PaneID}
}

// markedSet adapts the selection to what Render already expects, so the renderer
// does not have to change.
func (m model) markedSet() map[string]bool {
	out := make(map[string]bool, m.sel.Len())
	for _, p := range m.panes {
		if m.sel.Has(selKey(p)) {
			out[MarkKey(p)] = true
		}
	}
	return out
}

func (m model) poll() tea.Cmd {
	// Only panes whose tile is on screen get a full-screen capture; everything
	// else gets the cheap classification zone.
	want := m.markedSet()
	// The tile on screen is the CURSOR's, so the full capture follows the drawn list.
	if p, ok := m.cursorRow(); ok && len(want) == 0 {
		want[MarkKey(p)] = true
	}
	return func() tea.Msg {
		hosts := m.poller.Tick(m.ctx, time.Now(), want)
		return tickMsg{hosts: hosts, panes: m.reg.Panes()}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		m.hosts = msg.hosts
		m.setFleet(msg.panes)
		m.log.Observe(m.panes, time.Now())
		// No clamp: the cursor names a row, so a re-sorted or shorter list needs nothing
		// done to it here. This is the writer that made the old shape wrong — the sort ran
		// on every tick and the cursor's index stayed put, pointing at a stranger.
		// exists is deliberately NOT the narrowed list. The filter decides what is
		// SHOWN; this decides what still EXISTS, and pruning by the filter would drop a
		// mark whenever a pane cd's out of the active project — silently, mid-compose
		// (docs/design.md §21.5).
		exists := m.visibleRows()
		// Prune vanished panes so they stop being targets.
		alive := make(map[SelectionKey]bool, len(m.panes))
		for _, p := range m.panes {
			alive[selKey(p)] = true
		}
		// Build visibleKeys from visibleRows so the selection derives from visibility.
		// A pane that becomes hidden for any reason (not just the x keystroke) must
		// leave the selection, otherwise the write path reaches it with no dialog.
		visibleKeys := make(map[SelectionKey]bool, len(exists))
		for _, p := range exists {
			visibleKeys[selKey(p)] = true
		}
		m.sel.Prune(func(k SelectionKey) bool { return alive[k] && visibleKeys[k] })
		for k := range m.atSelection {
			// The snapshot goes with the selection it describes, or a pane
			// re-selected later inherits an hour-old "then".
			if !alive[k] || !visibleKeys[k] {
				delete(m.atSelection, k)
			}
		}
		// Identification rides the tick: the token means "identified no more than
		// one tick ago", so it has to be rewritten as often as the tick runs.
		//
		// The command is taken BEFORE the model is returned. identify records the
		// round as in flight on the model it is called on, and which of the two a
		// compiler evaluates first is not pinned by the spec — so written the short
		// way, the only guard in the identity path could be silently dead.
		ident := m.identify()
		return m, tea.Batch(
			tea.Tick(PollInterval, func(time.Time) tea.Msg { return pollNow{} }),
			ident,
			// The poll has just read what each session's server holds, so this is the
			// moment the differences are known. In the steady state it is nil.
			m.publishAliases(),
		)

	case pollNow:
		return m, m.poll()

	case identMsg:
		// Per host: another host's round may still be out, and clearing the fleet
		// would let a second round start on top of it.
		delete(m.identBusy, msg.host)
		if msg.err == "" {
			delete(m.identErr, msg.host)
			return m, nil
		}
		if m.identErr == nil {
			m.identErr = map[string]string{}
		}
		m.identErr[msg.host] = msg.err
		return m, nil

	case publishedMsg:
		// Silence on success: the operator asked for a name in a status line, not for a
		// report that one was written. A failure is worth a line, because the name they
		// see on the dashboard is then not the name the session knows.
		if msg.err != nil {
			m.note = fmt.Sprintf("could not publish the name to tmux: %v", msg.err)
		}
		return m, nil

	case sweptMsg:
		if msg.removed > 0 {
			m.note = fmt.Sprintf("cleared %s left by a previous hub",
				plural(msg.removed, "stale paste buffer", "stale paste buffers"))
		}
		return m, nil

	case agentsMsg:
		// Only the pane list is taken from here; host STATUS stays whatever the
		// tmux tick decided, because a missing `claude` is not a fault of the host.
		m.setFleet(msg.panes)
		for _, h := range msg.hosts {
			for i := range m.hosts {
				if m.hosts[i].Label == h.Label {
					m.hosts[i].AgentsReason = h.AgentsReason
				}
			}
		}
		m.log.Observe(m.panes, time.Now())
		return m, tea.Batch(
			tea.Tick(AgentInterval, func(time.Time) tea.Msg { return agentsNow{} }),
			// This poll is what folds a pane-less row into the pane the door made, which
			// is the moment a name first has a tmux session to be published to.
			m.publishAliases(),
		)

	case agentsNow:
		return m, m.pollAgents()

	case attachedMsg:
		// Whatever happened inside, the world moved on while the TUI was
		// suspended, so poll immediately rather than waiting out the interval.
		if msg.err != nil {
			m.note = "attach failed: " + msg.err.Error()
		}
		return m, m.poll()

	case wokenMsg:
		// The create landed and the jump has not been tried yet, so the two are reported
		// separately: "made X on Y for it, but could not go there" is the sentence §22.3 requires,
		// never "cannot go there" — which would hide a session the operator now owns and cannot
		// find.
		if msg.err != nil && msg.made.PaneID == "" {
			m.note = "could not open " + msg.name + " on " + msg.host + ": " + msg.err.Error()
			return m, nil
		}
		verb := "made"
		if msg.found {
			// The name is a pure function of the row, so a second `a` finds the first door rather
			// than making a second one. Saying WHICH it was matters: the operator pressed the same
			// key twice and the screen must not claim two sessions exist.
			verb = "went back to"
		}
		m.note = fmt.Sprintf("%s %s on %s for it", verb, msg.name, msg.host)
		if msg.err != nil {
			// The pane exists and something after it did not, which is a note rather than a
			// refusal: the operator is about to be standing in that pane.
			m.note += " — " + msg.err.Error()
		}
		h, ok := hostFor(m.hosts, msg.host)
		if !ok {
			m.note += ", but " + msg.host + " left this hub's list"
			return m, nil
		}
		// Possession takes over the pane the CREATE returned, with the epoch that came back in the
		// same invocation — which is what keeps a local wake off the full-screen path.
		made := registry.Pane{
			Kind: registry.KindPane, Host: msg.host, PaneID: msg.made.PaneID,
			SessionID: msg.made.SessionID, WindowID: msg.made.WindowID,
			Session: msg.name, Epoch: msg.made.Epoch,
		}
		note := m.note
		out, cmd := m.possess(made, h)
		m2 := out.(model)
		// possess clears the note on every path that goes somewhere, and what the door did is the
		// half the operator cannot see for themselves.
		if m2.note == "" {
			m2.note = note
		}
		return m2, cmd

	case possessedMsg:
		if msg.err != nil {
			// A `from` alongside an error means the move HALF landed, and saying
			// "cannot go there" would then be false in the direction that costs the
			// operator: they ARE somewhere new. One path can be in that state — a
			// jump whose select-window did not follow the switch — and the reason
			// travels inside the error rather than being named here, because naming
			// it made the note describe a select-window the window path never issues.
			// The window path lost its half-landed state when the window stopped
			// being held open by an option set after the operator was already in it
			// (WindowPayload): `new-window` either created the window and moved them,
			// or created nothing.
			if msg.from != "" {
				m.note = "moved into " + msg.from + ", but " + msg.err.Error()
				return m, nil
			}
			m.note = "cannot go there: " + msg.err.Error()
			return m, nil
		}
		if msg.reused {
			// "You are already there" and "nothing happened" look identical on a screen the hub
			// does not draw, so the note has to tell them apart.
			m.note = "already open — went to the window showing " + shortSubject(msg.from)
			return m, nil
		}
		if msg.from != "" {
			m.note = "back from " + msg.from
		}
		return m, nil

	case tea.KeyMsg:
		// ctrl+c is answered BEFORE the mode dispatch, and that placement is the whole
		// fix: it is not a shortcut but the universal "get me out", and a program that
		// swallows it reads as hung. Measured in a pty on the first-run picker — `q`
		// did nothing, `ctrl+c` did nothing, and the shell's `echo EXITED-rc=$?` never
		// printed, so the program could not be left from its own first screen.
		//
		// One place rather than five, because the alternative is a rule with a
		// per-screen table: four overlays that each have to remember, and a fifth one
		// added later that will not. Here a new mode inherits it by construction.
		//
		// It is also safe in the two text-entry modes: ctrl+c arrives as its own key
		// type and never as a rune, so nothing that types a literal `q` is affected —
		// which is why `q` stays where it is and this does not.
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		switch m.mode {
		case modeCompose:
			return m.composeKey(msg)
		case modeConfirm:
			return m.confirmKey(msg)
		case modeHistory:
			return m.historyKey(msg)
		case modeLaunch:
			return m.launchKey(msg)
		case modePicker:
			return m.pickerKey(msg)
		case modeProjects:
			return m.projectKey(msg)
		case modeTree:
			return m.treeKey(msg)
		case modeWake:
			return m.wakeKey(msg)
		case modeNaming:
			return m.namingKey(msg)
		case modeSearch:
			return m.searchKey(msg)
		}
		switch msg.String() {
		case "q":
			// ctrl+c used to be listed here too. It is handled above now, and leaving a
			// second unreachable arm for it would both mislead the next reader and let a
			// test pass on the dashboard while the overlays were broken — which is the
			// state this fix exists to end.
			return m, tea.Quit
		case "j", "down":
			m.note = ""
			m = m.cursorTo(m.cursorIndex() + 1)
		case "k", "up":
			m.note = ""
			m = m.cursorTo(m.cursorIndex() - 1)
		case "a":
			p, ok := m.cursorRow()
			if !ok {
				return m, nil
			}
			return m.goTo(p)
		case " ":
			if p, ok := m.cursorRow(); ok {
				m.mark(p)
				// A pane selected between two ticks holds no token yet, so identify
				// now rather than making the user's first send wait for the tick. The
				// command is taken first for the reason the tick's is.
				cmd := m.identify()
				return m, cmd
			}
		case "A":
			// VISIBLE panes only. Selecting something off-screen would break §7's
			// rule that a target is always a tile the user can see, and the whole
			// point of the rule is that nobody sends into a pane they are not
			// looking at. "Visible" means the panes currently shown in the inbox
			// viewport, from scrollTop through scrollTop + rows_that_fit.
			// rowsForScreen, so `A` cannot reach a row the active project excludes: the
			// renderer and `A` must read the SAME set or "select what is on screen"
			// stops being true the moment a filter is on (§21.5). Caught by a test that
			// selected 5 rows inside a 2-row project.
			visible := m.rowsOnScreen()
			if len(visible) == 0 {
				m.note = "nothing to select (terminal too small or no panes)"
				return m, nil
			}
			for _, p := range visible {
				if !m.sel.Has(selKey(p)) {
					m.mark(p)
				}
			}
			cmd := m.identify()
			return m, cmd
		case "C":
			m.sel.Clear()
			m.atSelection = map[SelectionKey]paneSnapshot{}
			m.note = "selection cleared"
		case "i", "!", "R", "K":
			// The four keys that act on the SELECTION, in one function because a second screen
			// now sends them here too — and every one of them refuses an empty selection with
			// its own sentence, which is exactly the code a copy would get subtly wrong.
			return m.selectionKey(msg)
		case "h":
			return m.openHistory()
		case "n":
			m = m.openLaunchForm()
		case "p":
			// The picker is a first-class screen, always reachable from the dashboard
			// (§9) — not only on the first run, because two of this machine's five
			// usable hosts answer differently minute to minute, so a hosts.toml written
			// once is a snapshot of a coin flip.
			return m.openPicker()
		case "N":
			// Name the row under the cursor. The subject is captured HERE, at the
			// keystroke, and the field opens with the operator's own name only — never a
			// derived one, or an untouched enter would freeze Claude's word or a tmux
			// session name into the file (§21.12 rule 5).
			p, ok := m.cursorRow()
			if !ok {
				m.note = "nothing under the cursor to name"
				break
			}
			return m.openNaming(p)
		case "tab":
			// The next project, without going back to the list — the walk the list exists
			// to start rather than to be lived in.
			m = m.nextProject()
		case "t":
			// The filesystem view. `t` for tree, and it was free — `v` was not the place for it,
			// because `v` cycles a GROUPING and this is a different question about the same fleet.
			// No note: the screen's own head line is its legend, and a note repeating it took the
			// fleet's health off the footer at 60 and 80 columns.
			m.mode, m.note = modeTree, ""
		case "P":
			// The project list. It is a MODE rather than an overlay because it replaces
			// the question the screen is answering: not "who needs me across the fleet"
			// but "what is the state of the thing I am working on" (§21).
			m.mode = modeProjects
			m.projCursor = 0
		case "esc":
			// esc means "widen": clear EVERY narrowing — the project filter, `*` and the
			// keyword — because an operator who pressed two of them should not have to
			// remember which one this key undoes (docs/design.md §21.17). In browse with
			// nothing narrowed it is a no-op rather than an error: a key that reads as
			// "back out" must not complain when there is nothing to back out of.
			if m.favouritesOnly || m.searchQuery() != "" {
				m.widen()
				m.note = ""
				return m, nil
			}
			if m.filter.on {
				// No clamp, and widening is where naming the row pays twice: the cursor
				// follows its own row out into the whole fleet, where an index would have
				// pointed at whatever row the fleet happens to hold at that position.
				m.filter.on, m.filter.group = false, ""
			}
		case "*":
			// Only what the operator pinned. It REFUSES on a fleet with nothing pinned and
			// says which key pins a row, because turning it on there would empty the screen —
			// and an empty list is indistinguishable from a fleet that went away, which is the
			// class this repo has paid for more than once.
			if !m.favouritesOnly {
				pinned := 0
				for _, p := range m.visibleRows() {
					if m.isFavourite(p) {
						pinned++
					}
				}
				if pinned == 0 {
					m.note = "nothing is pinned yet — f pins the row under the cursor, F its project"
					return m, nil
				}
			}
			m.favouritesOnly = !m.favouritesOnly
			m.note = ""
			return m, nil
		case "/":
			// The field opens holding what is already applied, so narrowing further is editing
			// rather than retyping. searchBefore is what esc restores: cancelling a search must
			// be lossless, exactly as cancelling a name is.
			m.searchBefore = m.search.Text()
			m.note = ""
			return m.openSearch(), nil
		case "x":
			m = m.hideSubject()
		case "X":
			m.showHidden = !m.showHidden
		case "f":
			m = m.toggleFavouriteSessionOf(m.cursorRow())
		case "v":
			if m.groupBy == byHost {
				m.groupBy = byProject
			} else {
				m.groupBy = byHost
			}
			m.note = "grouping " + m.groupBy.String()
		}

	case pickerProbedMsg:
		m.pickerBusy = false
		if msg.err != nil {
			// The rows already on screen stay: they are the last answers that were
			// true, and a screen emptied by a failed probe is indistinguishable from a
			// machine with no candidates.
			m.note = "cannot probe the candidates: " + msg.err.Error()
			return m, nil
		}
		// The rows are built HERE, from the candidates and the kept set this model is
		// already holding, so the two rules that make a row have one owner. The port
		// used to hand over finished rows, which put "hosts.toml outranks the probe" in
		// main's closure with a second half-guard on this side (review I6).
		fresh := PickerRowsFor(msg.cands, msg.results, m.pickerKept, m.pickerReserved, time.Now())
		// The cursor follows the HOST, not the index. If ~/.ssh/config loses a host
		// above it between probes, an index lands the cursor on a different host and
		// `space` then toggles something the user was not looking at — the rule §7
		// states for panes, and the same reason.
		var at string
		if m.pickerCursor < len(m.picker) {
			at = m.picker[m.pickerCursor].Alias
		}
		m.picker = pickerMerge(m.picker, fresh)
		for i, r := range m.picker {
			if r.Alias == at {
				m.pickerCursor = i
				break
			}
		}
		m = m.clampPickerCursor()
		// Only while the picker is up. A round still in flight when `enter` returned to
		// the dashboard would otherwise replace the save's note — and the part of that
		// note §9 says must be seen is which ssh master was stopped.
		if m.mode == modePicker {
			m.note = fmt.Sprintf("asked %s", plural(len(fresh), "candidate", "candidates"))
		}
		return m, nil

	case pickerSavedMsg:
		if msg.err != nil {
			m.note = "cannot save hosts.toml: " + msg.err.Error()
			return m, nil
		}
		// Recorded HERE and not when enter was pressed, because this field says what the
		// FILE holds. Assigning the intent early made a retry after a failed save
		// compute its "what did the user turn off" list from a state that already said
		// the host was off, so the master survived with nothing on screen about it
		// (review C1). It also re-collapses: the file has just been rewritten from the
		// normalised view, so a duplicate-alias complaint is now repaired.
		m = m.withKept(msg.kept)
		// A host the user turned off leaves the fleet, which is the mirror of the connect
		// below and was missing: stopping its master ended the connection and left the ROW,
		// so one tick — which replaces m.hosts wholesale from the poller — put it back as
		// `connecting (waiting for its ssh master)` and then, once it had ever answered,
		// `down` carrying a respawn command the operator must not run. Nothing would ever
		// spawn that master again, so the status could never resolve.
		//
		// Keyed on `off` rather than on `stopped`: a host whose master would not stop is
		// still a host the user disabled, and leaving its row is the worse of the two
		// failures.
		for _, alias := range msg.off {
			if m.poller != nil {
				m.poller.Remove(alias)
			}
			for i := range m.hosts {
				if m.hosts[i].Label == alias {
					m.hosts = append(m.hosts[:i], m.hosts[i+1:]...)
					break
				}
			}
		}
		note := fmt.Sprintf("%s kept in hosts.toml", plural(msg.enabled, "host", "hosts"))
		if len(msg.stopped) > 0 {
			note += " · stopped the ssh master for " + strings.Join(msg.stopped, ", ")
		}
		if msg.stopErr != "" {
			note += " · " + msg.stopErr
		}
		m.note = note
		// And the other half of `enter: save and connect`. Stop already ran inside the
		// save, so turning a host off took effect at once while turning one on did not —
		// that asymmetry was the whole defect (known-issues C1).
		return m, m.enableHosts(msg.kept)

	case pickerEnabledMsg:
		// The rows the file now enables, folded into the live fleet. TWO registrations,
		// and both are needed for different reasons: the poller is what actually polls,
		// and appending to m.hosts is what puts the row on screen before the next tick —
		// which matters because the operator just pressed a key. Without the poller the
		// row would also VANISH at the next tick, since tickMsg replaces m.hosts wholesale
		// with the poller's own list.
		var added []string
		for _, h := range msg.hosts {
			// Keyed on the LABEL against what is already polled, so a save that changes
			// nothing adds nothing. `--host` extras and the local server are in this list
			// too, and hostFor answers with the first match — a second copy of a host
			// would make a write aimed at one land on the other.
			if _, ok := hostFor(m.hosts, h.Label); ok {
				continue
			}
			h.Status = hub.Connecting
			if m.poller != nil {
				m.poller.Add(h)
			}
			m.hosts = append(m.hosts, h)
			added = append(added, h.Label)
		}
		if msg.err != nil {
			// Not "cannot connect": the file IS written, and a message that hid that would
			// send the user back to re-tick a list already saved. Whatever hosts did come
			// back are still folded in above.
			m.note = appendNote(m.note, "could not connect: "+msg.err.Error())
			return m, nil
		}
		if len(added) == 0 {
			return m, nil
		}
		m.note = appendNote(m.note, "connecting to "+strings.Join(added, ", "))
		// Poll at once rather than waiting for the timer: the row says `connecting`, and
		// the sooner it says something truer the better.
		return m, m.poll()

	case historyMsg:
		if msg.err != nil {
			m.note = "cannot read history: " + msg.err.Error()
			return m, nil
		}
		if len(msg.entries) == 0 {
			m.note = "history is empty"
			return m, nil
		}
		m.mode, m.history, m.histCursor = modeHistory, msg.entries, 0
		return m, nil

	case sentMsg:
		var clean = len(msg.results) > 0 && msg.dropped == 0
		for _, r := range msg.results {
			if r.Outcome != broadcast.Delivered {
				clean = false
			}
			// Remembered per pane, because §7 makes "the previous send to this pane
			// was never witnessed" a reason to confirm the next one.
			if m.lastOutcome == nil {
				m.lastOutcome = map[SelectionKey]broadcast.Outcome{}
			}
			m.lastOutcome[SelectionKey{Host: r.Target.Host, PaneID: r.Target.PaneID}] = r.Outcome
		}
		m.note = summarise(msg.act, msg.results, msg.dropped)
		// Captured before the flag is dropped: a re-send from the log sends text the composer
		// never held, so clearing "the draft" on its success would delete a prompt the operator
		// is still writing.
		wasTheDraft := !m.fromHistory
		m.fromHistory = false
		if clean && msg.act == actionSend && wasTheDraft {
			// Only a clean run clears the draft. Otherwise the text stays, because
			// retyping a prompt the tool failed to deliver is the tool's fault.
			m.composer.Clear()
		}
		return m, m.poll()

	case launchMsg:
		if msg.err != nil {
			m.note = "launch failed: " + msg.err.Error()
			return m, nil
		}
		// THE CURSOR GOES TO WHAT WAS JUST CREATED, and this is the whole difference between "it
		// worked" and "it is usable". Measured on a real fleet at 80x24: after enter the hub said
		// `launched: %1` and the row was not on the screen at all for one poll, then appeared TWELVE
		// rows below a cursor that had not moved — so the operator's next step was to hunt for the
		// thing they had just asked for, on a list sorted by attention where a fresh session sorts
		// low precisely because it is not asking for anything.
		//
		// The key is set even though the row has not arrived yet: the cursor is keyed on the ROW's
		// identity and falls back to its remembered position when the row is absent, so the moment
		// the poll brings the pane in, the cursor is already on it. `a` is then the next keystroke
		// rather than the twelfth.
		if msg.paneID != "" && msg.host != "" {
			// The HINT comes with the key, and leaving it at zero was a defect my own comment denied.
			// `cursorIndex` falls back to the hint when the key names no row on screen, so a launch
			// whose pane has not arrived yet — or one a narrowing hides — pointed the cursor at row
			// ZERO: the operator standing on row five pressed enter and their place jumped to the top,
			// and with a project filter on, `a` then acted on rows[0] instead of the pane they had
			// just made. Reproduced both ways. Carrying the position they were standing on makes the
			// fallback what the comment always claimed it was.
			m.cursor = rowCursor{key: SelectionKey{Host: msg.host, PaneID: msg.paneID},
				hint: m.cursorIndex()}
		}
		m.note = fmt.Sprintf("launched: %s — a goes there", msg.paneID)
		// AND SAY IT when a narrowing will hide what was just made, because the cursor then points at
		// a row that is not on the screen: `a` would act on whatever the fallback names, and "it went
		// somewhere I cannot see" is the one outcome the note must not leave silent.
		if m.narrowed() && msg.paneID != "" {
			shown := false
			for _, p := range m.rowsForScreen() {
				if p.Host == msg.host && p.PaneID == msg.paneID {
					shown = true
					break
				}
			}
			if !shown {
				m.note = fmt.Sprintf("launched: %s — the filter hides it, esc shows the whole fleet",
					msg.paneID)
			}
		}
		if msg.renamed {
			// NAME the session, because the operator asked for one named after their directory and
			// got a different one. Bounded like every interpolated name on this row.
			m.note = fmt.Sprintf("launched: %s in %s (that name was taken) — a goes there",
				msg.paneID, shortSubject(msg.session))
		}
		// Poll immediately to show the new pane
		return m, m.poll()

	case restartMsg:
		if msg.err != nil {
			m.note = "restart failed: " + msg.err.Error()
			return m, nil
		}
		m.note = fmt.Sprintf("restarted: %s", msg.paneID)
		// Poll immediately to see the restarted pane
		return m, m.poll()

	case killMsg:
		if msg.failed > 0 {
			m.note = fmt.Sprintf("killed %d, failed %d", msg.killed, msg.failed)
		} else {
			m.note = fmt.Sprintf("killed %d pane(s)", msg.killed)
		}
		// Clear selection after kill
		m.sel.Clear()
		m.atSelection = map[SelectionKey]paneSnapshot{}
		// Poll to update the pane list
		return m, m.poll()
	}
	return m, nil
}

type pollNow struct{}

// historyMsg carries entries read off disk, so the read never blocks the UI.
type historyMsg struct {
	entries []history.Entry
	err     error
}

// sentMsg carries the per-target outcomes back to the UI, plus the DENOMINATOR:
// dropped counts selected panes no target could be built for, and act says which
// write this was. Without both, a write that reached nothing reported "delivered".
type sentMsg struct {
	results []broadcast.Result
	dropped int
	act     action
}

// launchMsg reports that a launch finished, carrying either the new pane id or an error.
type launchMsg struct {
	paneID string
	// host is which server the pane was made on, and it is here so the cursor can be pointed at the
	// new row: a SelectionKey is (host, pane id), and a pane id alone names a different pane on every
	// server the hub watches.
	host string
	// session is the name the new session actually got, and `renamed` says it differs from the one
	// the directory asked for because that one was taken.
	session string
	renamed bool
	err     error
}

// composeKey turns keystrokes into text. Enter LEAVES compose and runs the
// confirmation rule; it never sends directly, because §7 requires a fresh check of
// every target at the moment of sending rather than at the moment of selecting.
func (m model) composeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// The text is KEPT. Losing a half-written prompt to a stray Esc is the kind
		// of thing that makes people stop using a tool.
		m.note = "draft kept — press i to go back to it"
		return m.dismiss(), nil
	case "enter":
		if m.composer.Empty() {
			m.note = "nothing to send"
			return m, nil
		}
		m.pending, m.pendingAct = broadcast.Needed(m.targetStates()), actionSend
		m.pendingText = m.composer.Text()
		if len(m.pending) == 0 {
			m.pendingAct = actionNone
			return m.dismiss(), m.send(m.pendingText)
		}
		return m.raise(modeConfirm), nil
	case "alt+enter", "ctrl+j":
		m.composer.Newline()
		return m, nil
	case "backspace":
		m.composer.Backspace()
		return m, nil
	}
	// Every other key is text, which is why compose is a mode.
	if msg.Type == tea.KeyRunes {
		m.composer.Insert(string(msg.Runes))
	} else if msg.String() == " " {
		m.composer.Insert(" ")
	}
	return m, nil
}

// confirmKey requires a SECOND enter. The reasons are on screen; anything other
// than enter cancels, because the safe default for "I do not understand this
// dialog" is to do nothing.
//
// WHICH act it confirms comes from pendingAct, recorded when the dialog opened.
// Inferring it here from the composer sent the draft when the user pressed `!`.
func (m model) confirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	act, text := m.pendingAct, m.pendingText
	m.pending, m.pendingAct, m.pendingText = nil, actionNone, ""
	m = m.dismiss()
	if msg.String() != "enter" {
		m.note = "cancelled"
		// Nothing was sent, so nothing about this dialog may outlive it — including the flag
		// that makes §7 always ask. A cancelled re-send used to leave it set, so the next
		// ordinary send inherited a reason to confirm from a send that never happened.
		m.fromHistory = false
		return m, nil
	}
	switch act {
	case actionInterrupt:
		return m, m.interrupt("C-c")
	case actionSend:
		return m, m.send(text)
	case actionRestart:
		return m, m.doRestart()
	case actionKill:
		return m, m.doKill()
	default:
		// No act was recorded, so there is nothing this enter can safely mean.
		// Doing nothing is the only answer that cannot write into a live agent.
		m.note = "internal: a confirmation with no action was dismissed"
		return m, nil
	}
}

// send writes to every selected pane, witnesses each one, presses Enter as its own
// act, and records the outcome. It runs as a tea.Cmd so the UI never blocks on a
// remote round trip.
func (m model) send(text string) tea.Cmd {
	targets, dropped := m.targets()
	sender := m.sender
	ctx := m.ctx
	hist := m.hist
	// Build a map for session/window lookup.
	paneInfo := make(map[string]registry.Pane)
	for _, p := range m.panes {
		key := p.Host + ":" + p.PaneID
		paneInfo[key] = p
	}
	return func() tea.Msg {
		results := make([]broadcast.Result, 0, len(targets))
		for _, tg := range targets {
			res, err := sender.Send(ctx, tg, text)
			if err != nil {
				res.Outcome, res.Reason = broadcast.Refused, err.Error()
			} else {
				res = sender.Witness(ctx, res, text)
				res = submit(ctx, sender, res)
			}
			results = append(results, res)
			if hist != nil {
				key := tg.Host + ":" + tg.PaneID
				p := paneInfo[key]
				e := history.Entry{
					At:          time.Now(),
					Host:        tg.Host,
					PaneID:      tg.PaneID,
					SessionName: p.Session,
					WindowName:  p.Window,
					Text:        text,
					Outcome:     string(res.Outcome),
					Reason:      res.Reason,
					Token:       res.Token,
					Submitted:   res.Submitted,
				}
				if err := hist.Append(e); err != nil {
					// Log failure but do not fail the send: the delivery succeeded
					// and silently losing history is better than preventing sends.
					res.Reason = "sent; history write failed: " + err.Error()
				}
			}
		}
		return sentMsg{results: results, dropped: dropped, act: actionSend}
	}
}

// submit presses Enter, which is what turns a pasted prompt into a running one. It
// is a separate act on purpose (docs/design.md §7): a newline inside the payload is
// what made send-keys execute paragraph one.
//
// A refused Enter DOWNGRADES the outcome. "Delivered" for a prompt sitting unsent
// in an agent's input box would be literally true and operationally wrong — the
// operator would wait on an answer nothing is computing.
func submit(ctx context.Context, sender *broadcast.Sender, res broadcast.Result) broadcast.Result {
	if res.Outcome == broadcast.Refused {
		return res
	}
	sub := sender.Submit(ctx, res.Target)
	res.Submitted = sub.Outcome == broadcast.Delivered
	if res.Submitted {
		return res
	}
	if res.Outcome == broadcast.Delivered {
		res.Outcome = broadcast.Unwitnessed
	}
	res.Reason = "pasted but not submitted: " + sub.Reason
	return res
}

// interrupt sends a control key to every selected pane.
//
// It is NOT recorded in history. The log exists so a prompt can be read back and
// re-sent, and a re-send of a "C-c" entry would paste those three characters as
// text into an agent's input box.
func (m model) interrupt(key string) tea.Cmd {
	targets, dropped := m.targets()
	sender, ctx := m.sender, m.ctx
	return func() tea.Msg {
		results := make([]broadcast.Result, 0, len(targets))
		for _, tg := range targets {
			results = append(results, sender.Interrupt(ctx, tg, key))
		}
		return sentMsg{results: results, dropped: dropped, act: actionInterrupt}
	}
}

// launch creates a new Claude Code session in a tmux pane. The hub generates the
// session uuid and passes `--session-id`, so both halves of its identity are known
// at birth and no process-tree walk is needed (docs/design.md §19).
func (m model) launch(spec launch.Spec) tea.Cmd {
	run := m.run
	ctx := m.ctx
	keeper := m.keeper
	stamper := m.stamper
	hist := m.hist

	return func() tea.Msg {
		// A model built by `build` always has a runner, so this is the same guard
		// sweep() carries and for the same reason: a model assembled field-by-field
		// has none, and a nil interface here is a panic inside a tea.Cmd — which
		// bubbletea surfaces as a dead TUI rather than as a message.
		if run == nil {
			return launchMsg{err: errors.New("this hub has no tmux runner, so nothing can be launched")}
		}
		// Step 1: Generate a fresh session ID
		id, err := launch.NewSessionID()
		if err != nil {
			return launchMsg{err: fmt.Errorf("generate session id: %w", err)}
		}

		// Step 2: Build the command
		plan, err := spec.Build(id)
		if err != nil {
			return launchMsg{err: fmt.Errorf("build launch plan: %w", err)}
		}

		// Step 3: Find the target host. The presence of the HOST is what decides here,
		// never the presence of a socket: a host out of hosts.toml has no socket at all
		// (§5 deleted the forward), so a socket-keyed check reported the one host the
		// user is most likely to have as "not found".
		h, ok := hostFor(m.hosts, spec.Host)
		if !ok {
			return launchMsg{err: fmt.Errorf("host %q not found", spec.Host)}
		}
		target := h.Target()
		// A host with neither a socket nor an ssh destination has no transport at all —
		// the shape a `--host` entry takes when the operator's forward is gone. tmux.build
		// would refuse it too, but its message is about sockets, and this one names the
		// host and the two ways to reach it.
		if target.Socket == "" && !target.Remote() {
			return launchMsg{err: fmt.Errorf("host %q has neither a socket nor an ssh destination, "+
				"so there is nowhere to create a window", spec.Host)}
		}

		// Step 4: Create the window or session
		//
		// usedName is the session name that actually landed and `renamed` says it is not the one the
		// directory asked for, so the note can name it: a session the operator did not name is a
		// session they cannot find, and silence here is what "it did not work" looks like from the
		// other side.
		var usedName string
		var renamed bool
		var paneID string
		if spec.NewSession {
			// The name the operator's directory asks for, and a SECOND name for when tmux says that
			// one is taken. `new-session -s <name>` is rc=1 `duplicate session: <name>` then, and
			// this path used to hand that sentence back — so a directory whose basename is already a
			// session name could never be launched into twice, which is every time for the operator
			// whose server holds a session called `tmux-hub`. The fallback carries the uuid this
			// launch already generated for `claude --session-id`, so it is unique by construction and
			// ONE retry always succeeds; §22.3's door names its sessions the same way, through the
			// same function.
			usedName = spec.SessionName
			paneID, err = tmux.NewSession(ctx, run, target, usedName, spec.CWD, plan.Command)
			if tmux.IsDuplicateSession(err) {
				usedName = launch.SessionNameWithID(spec.SessionName, id)
				paneID, err = tmux.NewSession(ctx, run, target, usedName, spec.CWD, plan.Command)
				renamed = err == nil
			}
		} else {
			// WHICH session the window goes in, resolved rather than assumed. `$0` was hard-coded
			// here, and `$0` is only the first session a server ever had — kill it and the id is
			// gone for good, so on any long-lived server this answered `can't find session: $0` and
			// created nothing. Reported from real use.
			//
			// First choice is the session the operator was looking at, which is what "a new window"
			// means. Failing that, any session on that host will do. Failing THAT, the host has no
			// session at all and a window has nowhere to go, so the refusal names the way out
			// rather than passing tmux's id back to someone who never typed it.
			sess := spec.SessionID
			if sess == "" {
				sess, err = tmux.FirstSessionID(ctx, run, target)
				if err != nil {
					return launchMsg{err: fmt.Errorf("ask %s which sessions it has: %w",
						spec.Host, err)}
				}
			}
			if sess == "" {
				return launchMsg{err: fmt.Errorf("%s has no tmux session to put a window in — "+
					"choose \"a new session\" instead", spec.Host)}
			}
			paneID, err = tmux.NewWindow(ctx, run, target, sess, spec.CWD, plan.Command)
		}
		if err != nil {
			return launchMsg{err: fmt.Errorf("create pane: %w", err)}
		}

		// Step 5: Get the window ID from the pane
		res, err := run.Run(ctx, target, "display", "-p", "-t", paneID, "#{window_id}")
		if err != nil {
			return launchMsg{paneID: paneID, host: spec.Host, session: usedName, renamed: renamed, err: fmt.Errorf("get window id: %w", err)}
		}
		if res.RC != 0 {
			return launchMsg{paneID: paneID, host: spec.Host, session: usedName, renamed: renamed, err: fmt.Errorf("get window id: tmux exited %d", res.RC)}
		}
		windowID := strings.TrimSpace(res.Stdout)
		if windowID == "" || !strings.HasPrefix(windowID, "@") {
			return launchMsg{paneID: paneID, host: spec.Host, session: usedName, renamed: renamed, err: fmt.Errorf("invalid window id: %q", windowID)}
		}

		// Step 6: Set remain-on-exit on THIS window only
		if err := tmux.SetWindowOption(ctx, run, target, windowID, "remain-on-exit", "on"); err != nil {
			return launchMsg{paneID: paneID, host: spec.Host, session: usedName, renamed: renamed, err: fmt.Errorf("set remain-on-exit: %w", err)}
		}

		// Step 7: Stamp the pane
		_, err = stamper.Stamp(ctx, target, paneID)
		if err != nil {
			// Stamping failed after creation. The pane exists but is not trusted.
			return launchMsg{paneID: paneID, host: spec.Host, err: fmt.Errorf("stamp failed (pane exists but not trusted): %w — run the hub's identification to recover", err)}
		}

		// Step 8: Adopt the pane — mark it as identified without a walk
		keeper.Adopt(spec.Host, paneID, id)

		// Step 9: Record in history
		if hist != nil {
			e := history.Entry{
				At:          time.Now(),
				Host:        spec.Host,
				PaneID:      paneID,
				SessionName: "", // will be filled by the next poll
				WindowName:  "", // will be filled by the next poll
				Text:        plan.Command,
				Outcome:     "launched",
				Reason:      "",
				Token:       "",
				Submitted:   false,
			}
			if err := hist.Append(e); err != nil {
				// Log failure but do not fail the launch: the pane was created successfully.
				return launchMsg{paneID: paneID, host: spec.Host, session: usedName, renamed: renamed}
			}
		}

		return launchMsg{paneID: paneID, host: spec.Host, session: usedName, renamed: renamed}
	}
}

// targets returns one broadcast target per selected pane, plus the number of
// selected panes it could NOT build one for.
//
// The count is returned rather than swallowed: a selected pane whose host has left
// the hub's list is silently skipped, and a send that skipped every one of them used
// to report "delivered" having written nothing.
func (m model) targets() ([]broadcast.Target, int) {
	var out []broadcast.Target
	dropped := 0
	for _, k := range m.sel.Members() {
		h, ok := hostFor(m.hosts, k.Host)
		if !ok {
			dropped++
			continue
		}
		out = append(out, broadcast.Target{
			Host:   k.Host,
			PaneID: k.PaneID,
			Tmux:   h.Target(),
		})
	}
	return out, dropped
}

// targetStates gathers what §7's confirmation rule looks at, for every selected
// pane. Every field it can answer is answered: the two poll snapshots the registry
// holds supply "now", the snapshot taken at the keystroke supplies "at selection",
// and the Keeper supplies identification. A field left at its zero value is not a
// neutral default — it is a clause that cannot fire, which is how four of the seven
// reasons came to be unreachable in the shipped binary.
//
// A pane that has VANISHED gets no live values, so its session and window read as
// changed and it reads as unidentified: the direction that asks rather than sends.
func (m model) targetStates() []broadcast.TargetState {
	live := make(map[SelectionKey]registry.Pane, len(m.panes))
	for _, p := range m.panes {
		live[selKey(p)] = p
	}
	var out []broadcast.TargetState
	for _, k := range m.sel.Members() {
		p := live[k]
		at := m.atSelection[k]
		out = append(out, broadcast.TargetState{
			Host:                  k.Host,
			PaneID:                k.PaneID,
			IdentifiedNow:         m.keeper.Identified(k.Host, k.PaneID),
			IdentifiedAtSelection: at.identified,
			SessionAtSelection:    at.session,
			SessionNow:            p.SessionID,
			WindowAtSelection:     at.window,
			WindowNow:             p.WindowID,
			EpochAtSelection:      at.epoch,
			EpochNow:              p.Epoch,
			LastOutcome:           m.lastOutcome[k],
			FromHistory:           m.fromHistory,
			Bracketed:             p.Bracketed,
		})
	}
	return out
}

// openHistory opens the history viewer and loads recent entries.
func (m model) openHistory() (tea.Model, tea.Cmd) {
	if m.hist == nil {
		m.note = "no history log — start the hub without --no-history"
		return m, nil
	}
	l := m.hist
	return m, func() tea.Msg {
		es, err := l.Recent(200)
		return historyMsg{entries: es, err: err}
	}
}

// historyKey handles keyboard input in history mode.
func (m model) historyKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "h":
		m.mode, m.history = modeBrowse, nil
		return m, nil
	case "j", "down":
		if m.histCursor < len(m.history)-1 {
			m.histCursor++
		}
		return m, nil
	case "k", "up":
		if m.histCursor > 0 {
			m.histCursor--
		}
		return m, nil
	case "r":
		if m.histCursor >= len(m.history) {
			return m, nil
		}
		if m.sel.Len() == 0 {
			m.note = "select the panes to re-send to first"
			return m, nil
		}
		// The entry's text becomes the pending send's subject and the send goes to the CURRENT
		// selection. Reusing the entry's own recorded targets would write into panes the user is
		// no longer looking at — an hour-old %3 on a host that has since restarted its server is
		// a different pane.
		//
		// It does NOT go through the composer. It used to, and that made a re-send destructive
		// before it was confirmed: the draft was overwritten at this keystroke, and the cancel
		// path had nothing to restore it from.
		m.pendingText = m.history[m.histCursor].Text
		m.mode = modeBrowse  // the re-send leaves the log behind: its subject is the SELECTION now
		m.fromHistory = true // which makes §7's rule always ask
		m.pending, m.pendingAct = broadcast.Needed(m.targetStates()), actionSend
		if len(m.pending) == 0 {
			// Cannot happen while fromHistory is set, and asserted rather than
			// assumed: a re-send that skipped the dialog would be the one case
			// where the user did not type the text they are about to send.
			m.note = "internal: a history re-send must always confirm"
			return m, nil
		}
		return m.raise(modeConfirm), nil
	}
	return m, nil
}

// launchKey handles keyboard input in launch form mode.
func (m model) launchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	f, cmd, out := m.launchForm.handleKey(msg)
	m.launchForm = f
	// The form SAYS which of the three things happened. It used to say only "closed", and this
	// gate inferred the rest from `f.err == "" && msg.Type == tea.KeyEnter` — a leftover from the
	// previous validation, read as a fact about this one. A correction made with Backspace alone
	// left that flag set, so a valid enter closed the form, returned to browse, launched nothing
	// and said nothing.
	switch out {
	case formSubmitted:
		return m.dismiss(), m.launch(f.spec)
	case formCancelled:
		m = m.dismiss()
	}
	return m, cmd
}

// VisiblePanes returns the panes currently shown in the inbox viewport.
// "Visible" means the slice from scrollTop through scrollTop + rows_that_fit,
// exactly matching what render.go draws. This is NOT the same as "which panes
// have tiles" — in both layouts the inbox lists every pane in a scrolled window.
//
// Uses InboxViewport so the renderer and select-all key cannot drift.
func VisiblePanes(f Frame) []registry.Pane {
	first, count := InboxViewport(f)
	if count == 0 {
		return nil
	}
	return f.Panes[first : first+count]
}

// rowsOnScreen is the rows the list is drawing right now, which is what `A` selects. It names the
// fields the list's arithmetic reads — the rows, the size, the cursor and the three inputs to the
// header rule — because the header rule decides how many rows fit, and a Frame built without them
// would answer for a screen nobody is looking at.
func (m model) rowsOnScreen() []registry.Pane {
	return VisiblePanes(Frame{Panes: m.rowsForScreen(), Width: m.width, Height: m.height,
		Cursor: m.cursorIndex(), GroupBy: m.groupBy, Groups: m.groupLabels(), Aliases: m.aliases})
}

// summarise is the one line the user reads after a write, and it carries the
// DENOMINATOR. Before it did, a write that resolved no target at all reported
// "delivered" — measured, summarise(nil) == "delivered" — which is the worst
// direction to be wrong in: the operator then waits on an agent nobody wrote to.
func summarise(act action, results []broadcast.Result, dropped int) string {
	verb := "sent"
	if act == actionInterrupt {
		verb = "interrupted"
	}
	if len(results) == 0 {
		s := "nothing was " + verb + " — no target resolved"
		switch {
		case dropped == 1:
			s += " (1 selected pane is on a host this hub no longer lists)"
		case dropped > 1:
			s += fmt.Sprintf(" (%d selected panes are on a host this hub no longer lists)", dropped)
		}
		return s
	}
	var delivered, unwitnessed, refused int
	for _, r := range results {
		switch r.Outcome {
		case broadcast.Delivered:
			delivered++
		case broadcast.Unwitnessed:
			unwitnessed++
		default:
			refused++
		}
	}
	var parts []string
	if delivered > 0 {
		parts = append(parts, fmt.Sprintf("%d delivered", delivered))
	}
	if unwitnessed > 0 {
		parts = append(parts, fmt.Sprintf("%d unwitnessed", unwitnessed))
	}
	if refused > 0 {
		parts = append(parts, fmt.Sprintf("%d refused", refused))
	}
	if dropped > 0 {
		parts = append(parts, fmt.Sprintf("%d unresolved", dropped))
	}
	return fmt.Sprintf("%s to %s: %s", verb,
		plural(len(results)+dropped, "target", "targets"), strings.Join(parts, ", "))
}

// confirmView is what the confirmation screen shows. The action, the payload and
// the targets are on it because the dialog used to name none of them: it read
// "Confirm send to 1 target(s)" for an interrupt, and on the re-send path the last
// screen before writing into live agents showed no payload at all.
func (m model) confirmView() ConfirmView {
	v := ConfirmView{Reasons: m.pending, Warning: m.identWarning(), Note: m.note}
	switch m.pendingAct {
	case actionInterrupt:
		v.Action, v.Payload = "interrupt with C-c", ""
	case actionKill:
		v.Action, v.Payload = "kill", ""
	case actionRestart:
		v.Action, v.Payload = "restart", ""
	default:
		// The PENDING text, not the composer's: those were the same thing until a re-send stopped
		// staging its payload in the composer, and reading the composer here would have shown the
		// operator their own draft while the dialog was about to write a log entry.
		v.Action, v.Payload = "send", m.pendingText
	}
	for _, k := range m.sel.Members() {
		// For kill, add pane info to make subjects clear
		if m.pendingAct == actionKill {
			var label string
			for _, p := range m.panes {
				if p.Host == k.Host && p.PaneID == k.PaneID {
					label = k.Host + " " + k.PaneID
					if p.Window != "" {
						label += " (" + p.Window
						if p.Command != "" {
							label += " " + p.Command
						}
						label += ")"
					}
					break
				}
			}
			if label == "" {
				label = k.Host + " " + k.PaneID
			}
			v.Targets = append(v.Targets, label)
		} else {
			v.Targets = append(v.Targets, k.Host+" "+k.PaneID)
		}
	}
	return v
}

// hiddenStats computes how many panes are marked hidden and how many of those are waiting anyway.
//
// It counts while `X` is ON as well, which is the fix: returning 0,0 there deleted the only fact
// the footer could have used to say the screen was unfiltered, so a screen with the toggle left on
// read exactly like a fleet with nothing hidden. What `X` changes is the SENTENCE the footer builds
// from these numbers (hiddenTally.AllShown), never the numbers.
func (m model) hiddenStats() (total, blocked int) {
	if m.hidden == nil {
		return 0, 0
	}
	for _, p := range m.panes {
		if m.hidden.Marked(hide.KeyOf(p)) {
			total++
			if m.hidden.Resurfaced(p) {
				blocked++
			}
		}
	}
	return total, blocked
}

func (m model) View() string {
	// rowsForScreen, not visibleRows: the renderer draws what the active project holds,
	// and `A` takes the same set, so "select what is on screen" stays true. sel.Prune is
	// the one thing that must keep reading visibleRows (§21.5).
	visible, loose := m.rowsForScreenLoose()
	// One answer for every mode's Frame: the cursor's position is DERIVED from the row it
	// names, and six modes each deriving it is six chances to derive a different one.
	cur := m.cursorIndex()
	hiddenTotal, hiddenBlocked := m.hiddenStats()
	resurfaced := m.resurfacedKeys()
	hiddenRows := m.hiddenRowKeys()
	favourites := m.favouriteKeys()
	hint := hintFor(m.pathForCursor())
	// A mark the screen cannot show still receives the send, so say so. Computed at paint
	// time rather than stored, because both the filter and the selection move under it —
	// a stored count would go stale on the tick that a pane cd's out of the group.
	note := m.note
	// A filter whose project no longer exists — every row cd'd out of it — must say so
	// rather than show an empty list. An empty screen is indistinguishable from a fleet
	// that went away, and the remedy (esc) is the one thing the operator needs to be told.
	// One walk for "how many rows EXIST", read by the empty-screen note below and by the footer's
	// tally: the two used to walk the fleet once each, on every frame.
	exists := len(m.visibleRows())
	if m.narrowed() && len(visible) == 0 && exists > 0 && note == "" {
		// WHICH narrowing emptied it, because the remedy differs and because an empty screen
		// with no sentence is indistinguishable from a fleet that went away. The keyword is
		// bounded for the reason every interpolated name on this row is (§21.17).
		switch {
		case m.searchQuery() != "":
			note = fmt.Sprintf("nothing matches %q — esc shows the whole fleet",
				shortSubject(m.searchQuery()))
		case m.favouritesOnly:
			note = "nothing pinned is on screen — esc shows the whole fleet"
		default:
			note = "no rows in this project any more — esc shows the whole fleet"
		}
	}
	if n := m.selectedOutsideFilter(); n > 0 {
		outside := fmt.Sprintf("%d selected row is not in this project — enter still sends to it", n)
		if n > 1 {
			outside = fmt.Sprintf("%d selected rows are not in this project — enter still sends to them", n)
		}
		if note == "" {
			note = outside
		} else {
			note += " · " + outside
		}
	}
	// ONE frame, built once, and the six modes below say only what DIFFERS. Every mode used to
	// hand-write all twelve fields — the exact form the struct was introduced to remove — and a
	// field added for one screen then had six places to be forgotten. Measured on this very
	// change: the hidden-row markers reached three of the six by hand before this was collapsed.
	// WHICH SCREEN is showing, which is not the same question as which MODE the model is in: an
	// overlay is drawn over a screen, and the screen is the one that raised it. The keyword field
	// has no renderer of its own at all — it is painted in the screen's own footer from
	// f.Searching — so for it the answer decides the whole frame.
	screen := m.mode
	if screen.isOverlay() {
		screen = m.underlay
	}
	// WHAT THAT SCREEN NEEDS TO DRAW ITSELF, built only for the screen that is showing: the tree's
	// lines, or the project list's rows. Both are walks over the fleet, so building both every frame
	// would pay for a screen nobody is looking at — and `backdrop` needs whichever one is under an
	// overlay, which is the same answer.
	var tls []treeLine
	treeCur := 0
	var projects ProjectView
	switch screen {
	case modeTree:
		// Built ONCE per frame. Both the paint and the band ask for it, and treeLines walks the
		// fleet, so a second call is a second walk for the same answer.
		tls = m.treeShown()
		treeCur = m.treeIndex(tls)
	case modeProjects:
		sessions, projectCount := m.favs.Count()
		projects = ProjectView{Rows: m.projectRows(), Cursor: m.projCursor, Warn: m.rulesWarn,
			Pinned: m.pinnedProjects(), FavNote: fav.Describe(sessions, projectCount)}
	}
	f := Frame{Panes: visible, Hosts: m.hosts, Width: m.width, Height: m.height, Cursor: cur,
		Screen: screen, Tree: tls, TreeCursor: treeCur, Projects: projects,
		Marked: m.markedSet(), Hint: hint, HiddenCount: hiddenTotal, BlockedCount: hiddenBlocked,
		OwnHidden:     m.ownHidden,
		Home:          m.home,
		ShowingHidden: m.showHidden, HiddenRows: hiddenRows, Resurfaced: resurfaced,
		Favourites: favourites, GroupBy: m.groupBy, Groups: m.groupLabels(),
		Filters: m.filters(visible, exists, loose), Searching: m.mode == modeSearch,
		Aliases: m.aliases}
	paint := m.mode
	if paint == modeSearch {
		paint = screen
	}
	switch paint {
	case modeCompose:
		return RenderCompose(f.withNote(note), m.composer.Text())
	case modeConfirm:
		return RenderConfirm(f, m.confirmView())
	case modeHistory:
		return RenderHistoryView(m.history, m.width, m.height, m.histCursor, note)
	case modeLaunch:
		return RenderLaunchForm(f, m.launchForm)
	case modeTree:
		return strings.Join(RenderTreeScreen(f.withNote(note)), "\n")
	case modeProjects:
		return strings.Join(RenderProjects(f.withNote(note)), "\n")
	case modeWake:
		return RenderWake(f, m.wakeViewOf(note))
	case modeNaming:
		return RenderNaming(f, m.naming)
	case modePicker:
		// The dashboard stays above the rule: the picker is an overlay, the same shape
		// as the launch form. Its note goes to the BASE footer, where every other mode
		// puts one — a mode that drops the note makes any keystroke answered by a note
		// a silent no-op.
		//
		// What is wrong with hosts.toml SHARES that row with the note rather than being
		// replaced by it — the same channel rule the project list states (§21.14.3), so the
		// two screens cannot teach different habits. The comment here used to claim the
		// precedence was right, and the precedence WAS: a file that says two contradictory
		// things about a host outranks whether the hosts are up. What was wrong is the
		// mechanism. `foot := note; if foot == "" { foot = warn }` REPLACES, so the warning —
		// which this comment itself says "must not vanish on the next `j`" — vanished the
		// moment any keystroke produced a note.
		//
		// It goes through Frame.Aside rather than being pre-fitted HERE: a local Fit produced a
		// string the base footer then fitted AGAIN, so two dropped parts printed `+1 +1` and
		// the marker stopped counting. One Fit owns the row.
		baseH, bodyH := pickerSplit(m.height)
		// backdrop and not `Render`: the picker is an overlay by this comment's own words, and it is
		// reachable from the project list and from the filesystem view as well as from the dashboard.
		// Drawing the dashboard above the rule for an operator who was on another screen is the defect
		// an adversarial review found in the naming overlay one screen over.
		out := strings.Split(backdrop(f.withHeight(baseH).withNote(note).withAside(m.pickerWarn)), "\n")
		out = append(out, RenderPicker(m.picker, m.width, bodyH, m.pickerCursor)...)
		return joinToHeight(out, m.height)
	default:
		return Render(f.withNote(note))
	}
}

// keyName is a key as a PERSON would say it, and it exists because a refusal that cannot name the
// key it refused is indistinguishable from a broken key.
//
// bubbletea's own `msg.String()` is right for nearly every key — `i`, `enter`, `esc` and `ctrl+c`
// all read correctly — and wrong for the ones whose spelling is not a printable glyph. Measured: a
// space's String() is a single space, so `space acts on a session` came out as `  acts on a session`
// with a hole where the key should be, on the key an operator presses most often.
//
// Both sites that print a key back to the operator go through here, so a second unprintable key is
// one case in one switch rather than another silent hole.
func keyName(msg tea.KeyMsg) string {
	switch s := msg.String(); s {
	case " ":
		return "space"
	case "":
		// Not reachable from any key bubbletea currently names, and still worth having: an empty
		// name would print the refusal with nothing at all in front of it.
		return "that key"
	default:
		return s
	}
}
