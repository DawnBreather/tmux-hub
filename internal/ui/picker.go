package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/fleetcache"
	"github.com/DawnBreather/tmux-hub/internal/hostset"
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/lines"
)

// The picker is the screen a person meets on their first run: the candidates from
// ~/.ssh/config, what each one answered, and a tick box for the ones they can keep.
//
// Its one non-obvious rule is that a probe has THREE outcomes, not two. Measured
// across three consecutive probes each (docs/design.md §9): `eu` answered
// `tmux 3.2a` at 5.4 s, 9.1 s, 15.7 s and 18.4 s, `web-app` at 4.4 s, 7.4 s and
// 19.6 s, while `web-db` and `nuc` were steady at 2–3 s. So two of five usable hosts
// swing about fourfold and straddle any fixed timeout — and both were read as "the
// host is gone" on first encounter, by two different readers. A timed-out host
// therefore keeps its tick box and its reason says slow rather than absent; only a
// host that answered something positively ELSE (a git remote, no tmux, DNS that does
// not resolve) loses the box, because that answer will be the same tomorrow.
//
// The second rule is that MEMBERSHIP is the user's and STATUS is the probe's, and it
// cuts both ways. A host they kept is never un-enabled by a later probe, and it is
// also never made unturnoffable by one: whatever the probe now answers, a host that
// is on can be switched off HERE, because this is the screen §9 calls the place a
// person decides.

// PickerRow is one candidate as the picker shows it.
//
// Four booleans, and they are four separate facts rather than one state:
//
//	Usable    the host named a tmux version
//	TimedOut  the host named nothing yet — see above, this is not "absent"
//	Kept      hosts.toml had it ENABLED when the screen opened
//	Enabled   what the screen says NOW, i.e. the user's decision in progress
type PickerRow struct {
	Alias   string
	Reason  string
	Version string
	Enabled bool
	Usable  bool

	// TimedOut is the third probe outcome. It is not a Reason string to be
	// pattern-matched: the difference between "slow" and "absent" decides whether the
	// row offers a tick box at all, and a screen cannot key that on prose.
	TimedOut bool

	// Kept is what hosts.toml said when the screen opened, and it is separate from
	// Enabled — which is what the screen says now. It exists because the box has to
	// survive the user turning the host OFF: with the box derived from Enabled alone,
	// it vanished the moment they unticked a kept host whose probe now disagrees, and
	// the row then wrote nothing, leaving `enabled = true` in the file. So the
	// question the box asks is "was this ever yours", not "is it on".
	Kept bool

	// Asked is when the probe behind this row ran. It reaches the screen only for a
	// timed-out row, because that is the only answer that depends on the moment —
	// `not a shell host` will read the same in an hour. Zero means unrecorded.
	Asked time.Time
}

// The rule about what a row lets the user do is ASYMMETRIC, so it is three predicates
// and not one. A single `decidable()` covering both directions was the first version of
// this fix, and it was too generous: it let the picker CREATE `enabled = true` for a
// host the probe says cannot work, rather than only preserving what the file already
// said.
//
//	canEnable   only a live probe answer  — a version, or a timeout, which is not absence
//	canDisable  a row that is ON          — membership is the user's, always theirs to drop
//	writable    either, or Kept           — whose `enabled` reaches hosts.toml

// canEnable reports whether this host can be switched ON here. Only the probe grants
// that: `no tmux` will read the same in an hour, so turning such a host on would be the
// hub inventing a state nobody can use. A TIMEOUT does grant it — measured, `eu`
// answered `tmux 3.2a` at 5.4 s, 9.1 s, 15.7 s and 18.4 s, so a timeout is slow rather
// than absent and §9's table says the user may enable it anyway.
func (r PickerRow) canEnable() bool { return r.Usable || r.TimedOut }

// canDisable reports whether this host can be switched OFF here. Any row that is on
// can, whatever the probe now answers.
//
// This is the half that was broken (review I4), measured: `studio-ws` enabled in
// hosts.toml with the probe answering `no tmux` had no box at all, so no key could turn
// it off, and `space` replied "cannot be enabled" — the opposite of what the user was
// doing. The only remedy left was hand-editing the file, on the screen §9 calls the
// place a person decides.
func (r PickerRow) canDisable() bool { return r.Enabled }

// writable reports whether the picker showed a decision about this host, so its
// `enabled` reaches hosts.toml. It is deliberately WIDER than either key predicate: a
// kept host the user has just turned off can no longer be enabled and no longer shows a
// box, and its `false` still has to reach the file — otherwise turning it off would be
// a gesture the file never hears, which is the same shape as review C1.
func (r PickerRow) writable() bool { return r.canEnable() || r.Kept }

// disputed reports a host the user kept whose probe now answers something else. It is
// not the same as "off" and not the same as "on and fine", so it gets its own box.
func (r PickerRow) disputed() bool { return r.Kept && !r.canEnable() }

// actionable reports whether a key would DO something on this row. The box is drawn
// exactly here and the cursor rests exactly here, from one predicate, because the two
// drifting apart is what put the cursor on a row that refuses every key.
func (r PickerRow) actionable() bool { return r.Enabled || r.canEnable() }

// box is the tick box, or the three columns of nothing that say there is nothing to do
// on this row right now.
//
// "Right now" is the whole rule: a box appears exactly where a key would act. So a kept
// host whose probe now disagrees carries one while it is on, and once the user turns it
// off it reads like `github.com` — because that is what it now is, off and not
// enable-able. Undo is `esc`, which is named on screen.
func (r PickerRow) box() string {
	switch {
	case !r.actionable():
		return "   "
	case r.Enabled && r.disputed():
		// Kept, and the probe now disagrees. A distinct glyph because `[x]` beside a
		// reason reading `no tmux` looks like a rendering bug rather than the state it
		// is — and it is still a BOX, because this row can be turned off.
		return "[!]"
	case r.Enabled:
		return "[x]"
	default:
		return "[ ]"
	}
}

// status is what the row says to the right of the alias: the version when the host
// named one, otherwise the reason — which always carries the remedy, never just the
// breakage (§16).
func (r PickerRow) status() string {
	if r.Usable {
		return "tmux " + r.Version
	}
	if r.TimedOut && !r.Asked.IsZero() {
		return r.Reason + " (asked " + r.Asked.Format("15:04:05") + ")"
	}
	return r.Reason
}

// PickerPorts are the picker's three doors to the world outside internal/ui.
//
// They are callbacks rather than direct calls for two reasons. Layering: what a probe
// costs, where hosts.toml lives and how an ssh master is addressed are main's
// business, not a screen's. And testability: a test that had to spawn ssh to prove a
// tick box would not get written, so every assertion about this screen runs with no
// network and no filesystem.
//
// A nil field is not a crash. The key that needs it says why it cannot act, because a
// key that does nothing and says nothing reads as a broken key.
type PickerPorts struct {
	// Probe asks every candidate again. It returns the CANDIDATES beside the results
	// rather than finished rows, and that is deliberate: the two rules that decide
	// what a row says — a Skip candidate is not a row, and hosts.toml outranks the
	// probe — then have exactly ONE owner, inside this package, holding the `kept` it
	// already has. With the port returning rows, both rules lived in main's closure
	// while the screen kept a second half-guard of its own, and which one covered a
	// given case depended on whether a row happened to be on screen (review I6).
	//
	// It runs inside a tea.Cmd, so it may block for the whole probe timeout — ten
	// hosts probed concurrently took 7.65 s — while the screen stays live.
	Probe func() ([]hostset.Candidate, []hostset.Result, error)

	// Save records the user's decisions. In production: hostset.SaveHosts.
	Save func([]hostset.Entry) error

	// Stop ends the ssh master of a host the user just turned off. Leaving it is a
	// real leak, not untidiness: measured, an `ssh -N -M` is reparented to pid 1 and
	// outlives the hub, so a host nobody enabled would hold a connection open
	// indefinitely. The user said no; nothing of theirs should keep running.
	Stop func(alias string) error

	// Enable is Stop's missing other half: it builds the hosts the file now enables
	// and connects them, so `enter: save and connect` does the second half too.
	//
	// The asymmetry it fixes was the tell. Stop ran inside the save, so turning a host
	// OFF took effect at once — while turning one ON only wrote the file, and the
	// running hub polled nothing new until it was restarted. Measured end to end in a
	// pty: three candidates ticked, the file correct, and 17.5 s later the transport
	// had been asked for five probes and nothing else — zero `ssh -O check`, zero
	// master spawns, zero polls. Same family as review C1, one layer out: a decision
	// the user made that the running program never hears.
	//
	// main both BUILDS and ENSURES behind this one door, because deriving a control
	// path and adopting a master are ssh concerns and a screen must not hold them. The
	// hosts come back ready to poll; the model only decides which of them are new.
	Enable func(kept []hostset.Entry) ([]hub.Host, error)

	// Behind asks one hop what machines its OWN ssh config declares, resolved on that
	// hop. In production: hostset.RemoteCandidates over the master the host already
	// has.
	//
	// It is a port and not a call because reading another machine's file is ssh's
	// business and a screen must not hold an argv — and because a test that had to
	// bring up two machines to prove a remedy string would not get written. It takes a
	// context, unlike the three above, because a round costs one round trip per alias
	// and the crawl bounds the whole round rather than each leg.
	//
	// NOTHING IS EVER WRITTEN OUTWARD (fleet spec §3.2 invariant 5): every payload
	// behind this door is a read, which is enforced by there being no other kind of
	// payload rather than by a rule somebody has to remember.
	//
	// A nil port is a hub that cannot look behind a hop, and the screen SAYS so
	// (crawlRefusal) rather than showing an empty section — "there is nothing back
	// there" and "nobody asked" are different facts.
	Behind func(ctx context.Context, hop string) ([]hostset.Candidate, error)

	// Facts and Learn are the remembered per-machine measurements — internal/fleetcache
	// — as a lookup and a writer. In production both close over one open cache.
	//
	// They are TWO ports rather than the cache itself so this package neither opens a
	// file nor holds a path, which is the same layering the three doors above keep. And
	// they are the reason the discovered list paints instantly and DOES NOT MOVE while
	// it is open: measured on the live fleet, one host's probe answered at 5.4 s, 9.1 s,
	// 15.7 s and 18.4 s, so an order taken from a live figure reorders between two
	// openings of the same screen.
	//
	// Both nil is a hub that remembers nothing, which orders the section by name inside
	// one bucket — the ordinary order, which is the order the operator would have had
	// anyway.
	Facts func(fleetcache.Key) (fleetcache.Facts, bool)
	Learn func(map[fleetcache.Key]fleetcache.Facts) error
}

// PickerRowsFor turns one probe round into the rows the picker shows. It is the ONE
// owner of both rules below; the model calls it, main does not.
//
// A candidate the parser marked Skip never becomes a row. ParseSSHConfig answers 30
// candidates on this machine and ten of them are systemd's ssh-proxy patterns
// (`machine/.host`, `*`): "30 candidates" would send someone looking for ten hosts
// that do not exist, and giving them a tick box would point a probe at a pattern.
//
// `kept` is what hosts.toml said, and it OUTRANKS the probe: a host the user enabled
// stays enabled even when this round timed out, because a timeout changes a host's
// status and not its membership (§9). A candidate the file says nothing about takes
// the probe's answer as its default, which is what makes zero configuration a working
// configuration.
//
// `reserved` holds the labels the fleet has already spoken for — this machine's own server
// and every `--host` entry. A candidate whose alias collides with one gets a row and a
// remedy but never a tick box, whatever the probe answered, because the alternative is a
// file the picker wrote and the next startup REFUSES: `hostsFor` treats both collisions as
// fatal and main exits 1 on them, before the TUI exists, so the screen that created the
// state cannot undo it. Refusing here is the same rule one step earlier, where a person can
// be told. It is a parameter rather than a later pass so a caller cannot forget it.
func PickerRowsFor(cands []hostset.Candidate, results []hostset.Result, kept []hostset.Entry, reserved []string, asked time.Time) []PickerRow {
	taken := make(map[string]bool, len(reserved))
	for _, r := range reserved {
		taken[r] = true
	}
	answered := make(map[string]hostset.Result, len(results))
	for _, r := range results {
		answered[r.Alias] = r
	}
	decided := make(map[string]bool, len(kept))
	for _, e := range kept {
		decided[e.Alias] = e.Enabled
	}

	var out []PickerRow
	for _, c := range cands {
		if c.Skip != "" {
			continue
		}
		r := answered[c.Alias]
		row := PickerRow{
			Alias: c.Alias, Version: r.Version, Reason: r.Reason,
			Usable: r.Usable, TimedOut: r.TimedOut, Asked: asked,
		}
		row.Enabled = r.Usable
		if on, ok := decided[c.Alias]; ok {
			row.Enabled, row.Kept = on, on
		}
		if taken[c.Alias] {
			// Whatever the probe said. A host answering `tmux 3.2a` under a label the
			// fleet has already given away is still unusable, and the row has to say
			// which conflict it is so the remedy is actionable.
			row.Usable, row.TimedOut, row.Enabled, row.Kept = false, false, false, false
			row.Version = ""
			if c.Alias == hub.LocalLabel {
				row.Reason = "this label is taken by this machine's own server — " +
					"rename it in ~/.ssh/config, or give it another label with --host"
			} else {
				row.Reason = "this label is already given to a --host entry — " +
					"rename one of them, or drop the --host"
			}
		}
		out = append(out, row)
	}
	return out
}

// normaliseKept collapses hosts.toml's entries into the ONE reading the picker acts
// on, and returns what it had to change so the screen can say it.
//
// Both defects it handles were left by hosts.toml's reader deliberately — it appends
// per `[[host]]` with no dedup and accepts `alias = ""` — because validation belongs
// where a person can be told, and this screen is that place (§9). Measured before this
// existed: a file naming `eu` twice made the picker write BOTH entries, one `true` and
// one `false`, run `ssh -O exit` twice for one host, and report "1 host kept" when the
// user's decision was to keep none; and an empty alias survived every save while being
// counted as a kept host, invisible on a screen that said "2 hosts kept" on a machine
// with one.
//
// A duplicate keeps the FIRST, which is what a first-match consumer of the file would
// read, and its tags with it. The next successful save rewrites the file from this
// collapsed view, so the complaint repairs itself rather than needing a hand edit.
func normaliseKept(kept []hostset.Entry) ([]hostset.Entry, string) {
	var out []hostset.Entry
	seen := map[string]bool{}
	var blank int
	dupes := map[string]bool{}
	var dupeOrder []string
	for _, e := range kept {
		if strings.TrimSpace(e.Alias) == "" {
			blank++
			continue
		}
		if seen[e.Alias] {
			if !dupes[e.Alias] {
				dupes[e.Alias], dupeOrder = true, append(dupeOrder, e.Alias)
			}
			continue
		}
		seen[e.Alias] = true
		out = append(out, e)
	}
	return out, keptComplaint(dupeOrder, blank)
}

// KeptComplaintWidth is the column budget the file's complaint must fit in. It lands in
// the base screen's footer, which Render cuts at the terminal width — and Render is not
// this screen's to change, because the other three callers of that line are screens whose
// frames are this branch's calibration targets. So the complaint has to fit 80 by
// CONSTRUCTION rather than by wrapping.
//
// Measured before it did (review N2): the two complaints joined ran to 165 columns, so a
// file carrying BOTH defects reported only the first one at 80 and the reader had no way
// to learn about the second. What was dropped to make room is the EXPLANATION — which half
// of a duplicate was kept, and that the entries were dropped — both of which are visible
// in the rows and in the rewritten file anyway. What stays is EVERY defect and the action,
// which is known-issues L2's rule.
const KeptComplaintWidth = 76

// keptComplaint names every defect the file has, in one line that fits.
//
// A single duplicate is named, because that is the common case and the alias is what the
// reader needs; several are counted, because a list of aliases has no bound and this line
// does. TestTheFileComplaintAlwaysFitsItsBudget pins that against adversarial input.
func keptComplaint(dupes []string, blank int) string {
	var parts []string
	switch {
	case len(dupes) == 1 && blank == 0:
		parts = append(parts, "names "+lines.Truncate(dupes[0], 24)+" twice")
	case len(dupes) == 1:
		parts = append(parts, "names 1 host twice")
	case len(dupes) > 1:
		parts = append(parts, fmt.Sprintf("names %d hosts twice", len(dupes)))
	}
	if blank > 0 {
		parts = append(parts, fmt.Sprintf("has %d with no alias", blank))
	}
	if len(parts) == 0 {
		return ""
	}
	return "hosts.toml " + strings.Join(parts, " and ") + " — enter rewrites it"
}

// pickerEntries merges the screen's decisions into the file's entries.
//
// It MERGES rather than rebuilds, and that is the load-bearing part. A row carries
// only what the picker shows, so rebuilding hosts.toml from rows alone would silently
// drop every `tags` and `tmux_args` the user wrote, and an entry for a host no longer
// in ~/.ssh/config would vanish with it — the kind of loss a generated file makes
// invisible.
//
// `kept` must already be through normaliseKept, which is why the alias index below can
// assume one entry per alias; built over a raw file it left only the LAST duplicate
// reachable and wrote the other one back unchanged.
//
// Only a WRITABLE row writes anything. A host that answered `not a shell host` and was
// never the user's was offered no decision, so the picker records none about it either
// way — and a host they DID keep stays writable however it answers now and whichever way
// they leave it, so unticking one always reaches the file even though the box has by then
// gone away.
func pickerEntries(rows []PickerRow, kept []hostset.Entry) []hostset.Entry {
	out := make([]hostset.Entry, len(kept))
	copy(out, kept)
	at := make(map[string]int, len(out))
	for i, e := range out {
		at[e.Alias] = i
	}
	for _, r := range rows {
		if !r.writable() {
			continue
		}
		if i, ok := at[r.Alias]; ok {
			out[i].Enabled = r.Enabled
			continue
		}
		at[r.Alias] = len(out)
		out = append(out, hostset.Entry{Alias: r.Alias, Enabled: r.Enabled})
	}
	return out
}

// pickerMerge carries the user's UNSAVED decisions across a re-probe — the ticks they
// made on this screen and have not committed yet.
//
// It is one of two layers and only one: the file's decisions are applied by
// PickerRowsFor before this runs, so a host the user kept is already enabled when these
// rows arrive. This layer exists for the narrower case of a tick made, then `r` pressed
// before `enter`. A probe answers what a host IS; whether the user wants it is not a
// question the probe gets to re-open.
func pickerMerge(old, fresh []PickerRow) []PickerRow {
	decided := make(map[string]bool, len(old))
	for _, r := range old {
		decided[r.Alias] = r.Enabled
	}
	out := make([]PickerRow, len(fresh))
	copy(out, fresh)
	for i := range out {
		if on, ok := decided[out[i].Alias]; ok {
			out[i].Enabled = on
		}
	}
	return out
}

const (
	// pickerBody is how many rows the overlay takes on a 24-row screen: twelve, the
	// launch form's height, so the dashboard above does not jump when one overlay
	// replaces the other.
	pickerBody = 12
	// pickerChrome is the rows of the body that are not candidate rows: the rule, the
	// count line and the blank under it, plus the blank and the key line at the foot.
	// It is counted here and nowhere else, the same rule bodyHeight follows — two
	// readers of this number would let the window and the cursor disagree, which is
	// the §16 defect the scroll exists to prevent.
	pickerChrome = 5
	// pickerGutter is where a candidate's status column starts: cursor, box, and the
	// alias padded so the versions line up. A wrapped remedy is indented to it.
	pickerGutter = 20
)

// pickerSplit divides a terminal between the dashboard and the overlay. Both halves
// are at least one row, so a short screen degrades instead of asking Render for a
// negative height.
func pickerSplit(height int) (baseH, bodyH int) {
	bodyH = pickerBody
	// A tall screen spends the surplus on hosts: twenty candidates are readable on
	// forty rows and are not on twelve.
	if half := height / 2; half > bodyH {
		bodyH = half
	}
	if bodyH > height-1 {
		bodyH = height - 1
	}
	if bodyH < 1 {
		bodyH = 1
	}
	baseH = height - bodyH
	if baseH < 1 {
		baseH = 1
	}
	return baseH, bodyH
}

// pickerBlock is one candidate's lines: the row, plus an indented continuation for
// each piece of a remedy too long to sit beside the alias.
//
// aliasColumn is how wide the alias column is. Named because the padding and the
// does-it-overflow test must agree: two literals would drift, and the drift is invisible
// until an alias lands exactly on the boundary.
const aliasColumn = 13

// It wraps rather than cutting because §16 names 80×24 "the size to hold, not a
// degraded case" and every error has to carry its FIX. Measured at 80 columns before
// this wrapped: `no answer in 10s — this host is slow rather than absent; ena` — the
// remedy gone from the single most important row on the screen (§9: 40% of this fleet
// straddles the timeout), leaving a bare complaint. The approved target frame is 120
// wide, so the frame diff structurally could not see it.
func pickerBlock(r PickerRow, width int, atCursor bool) []string {
	point := " "
	if atCursor {
		point = "›"
	}
	// lines.Pad, not `%-13s`. The verb pads by RUNE count — measured, `%-13s` on `путь`
	// (4 runes, 8 bytes) and on `abcd` produce the same visible width — so an earlier
	// version of this comment claiming it pads by BYTES was wrong, and the Cyrillic test
	// written to guard it could not fail. What the verb cannot do is DISPLAY width: `中文`
	// is 2 runes and 4 columns, so `%-13s` pads it to 15 columns and every row after it
	// inherits the shift. Pad counts columns.
	head := fmt.Sprintf("%s %s %s ", point, r.box(), lines.Pad(r.Alias, aliasColumn))
	status := r.status()

	// An alias WIDER than the column left exactly one space before its reason, so a long
	// one read as a run-on beside the padded short ones (known-issues L3). It takes the
	// same treatment the narrow-terminal branch below already gives every row: its own
	// line, with the reason wrapped under it at a six-column indent. That keeps the
	// aligned column intact for every ordinary row — moving it would shift every frame in
	// the corpus for the sake of the exceptional row.
	if lines.Width(r.Alias) > aliasColumn {
		out := []string{strings.TrimRight(head, " ")}
		for _, p := range wrapWords(status, max(1, width-6)) {
			out = append(out, strings.Repeat(" ", 6)+p)
		}
		// Through the SAME width bound the ordinary path takes. Returning early skipped it,
		// so this branch — the one an over-long alias reaches, i.e. exactly the case that is
		// already too wide — could emit a line past the terminal.
		for i := range out {
			out[i] = lines.Truncate(out[i], width)
		}
		return out
	}

	// Below about 32 columns the gutter that lines the versions up leaves too little to
	// wrap into, and the padding is the part worth losing: known-issues L2 records this
	// whole family — a line assembled without asking whether the parts fit, cut so that
	// the LABEL survives and the ACTION does not. So the alias keeps its own row and the
	// remedy wraps under it at a six-column indent, which costs a line and drops nothing.
	// Dropping the explanation instead would need this code to know which half of
	// `<why> — <remedy>` is which, and it is prose.
	indent := lines.Width(head)
	var out []string
	switch {
	case width-indent < 12:
		out = append(out, strings.TrimRight(head, " "))
		for _, p := range wrapWords(status, max(1, width-6)) {
			out = append(out, strings.Repeat(" ", 6)+p)
		}
	default:
		pieces := wrapWords(status, width-indent)
		if len(pieces) == 0 {
			out = append(out, strings.TrimRight(head, " "))
			break
		}
		out = append(out, head+pieces[0])
		for _, p := range pieces[1:] {
			out = append(out, strings.Repeat(" ", indent)+p)
		}
	}

	// ONE bound for every line this returns, whatever branch built it. wrapWords now
	// hard-breaks a token too long for any column, so nothing should reach this — it is
	// here to make "a candidate row is never wider than the terminal" a property of the
	// function rather than a consequence of wrapWords being right.
	//
	// It matters more than the truncation it replaced (review N1): a terminal SOFT-WRAPS
	// an over-wide line, so a View() of exactly 24 lines would occupy 25 visual rows on a
	// 24-row terminal and the frame desynchronises — which is the whole purpose of
	// joinToHeight and of this body's padding. Truncating at least kept the layout.
	for i := range out {
		out[i] = lines.Truncate(out[i], width)
	}
	return out
}

// wrapWords breaks a line on spaces at `cols`.
//
// A word that FITS the column is never split: `--probe-timeout` broken across two rows is
// not a flag anyone can type. A word too long for ANY column is a different case and is
// hard-broken, because the alternative is losing its tail — and these are the strings that
// reach it: `hostset.reasonFor`'s two fall-through arms embed `firstLine(stderr)` verbatim
// and `firstLine` bounds nothing but the newline, so a real
// `unix_listener: cannot bind to path …` measured 97 columns and a real
// `Warning: Identity file …` measured 90. The path IS the part the reader needs.
func wrapWords(s string, cols int) []string {
	if s == "" || cols < 1 {
		return nil
	}
	var out []string
	cur := ""
	for _, w := range strings.Fields(s) {
		if lines.Width(w) > cols {
			// Hard-break it, and keep the tail in `cur` so the words after it can share
			// that row rather than each starting a new one.
			if cur != "" {
				out = append(out, cur)
			}
			parts := breakWord(w, cols)
			out = append(out, parts[:len(parts)-1]...)
			cur = parts[len(parts)-1]
			continue
		}
		switch {
		case cur == "":
			cur = w
		case lines.Width(cur)+1+lines.Width(w) <= cols:
			cur += " " + w
		default:
			out = append(out, cur)
			cur = w
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// breakWord splits one token into pieces no wider than cols. It counts DISPLAY width per
// rune rather than bytes, because every string on this screen carries em dashes. It always
// returns at least one piece.
func breakWord(s string, cols int) []string {
	var out []string
	cur := ""
	for _, r := range s {
		w := lines.Width(string(r))
		if cur != "" && lines.Width(cur)+w > cols {
			out = append(out, cur)
			cur = ""
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	if len(out) == 0 {
		out = append(out, "")
	}
	return out
}

// pickerLayout decides which candidates are on screen when a row can take more than
// one line. It walks BACK from the cursor, taking whole blocks until the budget is
// spent, then extends downward with whatever is left.
//
// Whole blocks, because the cursor's own remedy half off the bottom is the same defect
// as a cursor off the bottom. For uniform one-line rows it is behaviour-identical to
// the fixed-height arithmetic it replaces (verified against both: cursor 2 of 20 gives
// rows 0–5, cursor 17 gives 12–17, cursor 19 gives 14–19).
func pickerLayout(blocks [][]string, cursor, budget int) (first, count int) {
	if len(blocks) == 0 || budget < 1 {
		return 0, 0
	}
	if cursor < 0 || cursor >= len(blocks) {
		cursor = 0
	}
	used := len(blocks[cursor])
	if used > budget {
		// One block taller than the whole overlay: show it alone and let the body cut.
		return cursor, 1
	}
	first, count = cursor, 1
	for first > 0 && used+len(blocks[first-1]) <= budget {
		first--
		used += len(blocks[first])
		count++
	}
	for first+count < len(blocks) && used+len(blocks[first+count]) <= budget {
		used += len(blocks[first+count])
		count++
	}
	return first, count
}

// RenderPicker draws the picker's body: everything from the rule down. The dashboard
// above it is drawn by Render, because the picker is an overlay and not a takeover.
//
// `height` is the rows the OVERLAY has, not the terminal's — pickerSplit decides that
// — and the body is exactly that many lines, with the key line on the last of them.
// It is padded rather than left short because a footer that floats is a footer nobody
// finds: measured before, four candidates put the key line on row 21 of 24 with three
// blank rows under it.
func RenderPicker(rows []PickerRow, discovered []DiscoveredRow, width, height, cursor int) []string {
	head := []string{separator(width), lines.Truncate(pickerCount(rows), width), ""}
	foot := []string{"", lines.Truncate(
		"space: keep this host · enter: save and connect · esc: cancel · r: probe again", width)}
	budget := height - len(head) - len(foot)
	if budget < 1 {
		return joinBlocks(head, nil, foot, height)
	}

	blocks := make([][]string, len(rows))
	wanted := 0
	for i, r := range rows {
		blocks[i] = pickerBlock(r, width, i == cursor)
		wanted += len(blocks[i])
	}

	// The discovered section is sized against what the CANDIDATE LIST actually needs, and then the
	// list gets what is left. Both halves of that order are deliberate.
	//
	// The section is sized by asking the one function that draws it, twice, rather than by arithmetic
	// about its shape: a second copy of "how tall is a machine's block" would drift the day a row
	// gains a field. And `wanted` is passed in because the two claimants yield differently — a
	// candidate the list cannot show is one `j` away, while a machine the section drops is gone until
	// the terminal grows — so on a screen where the candidates do not need their half, the section may
	// have the rest instead of leaving it blank. Measured before it did: at 120×40 with two candidates
	// the section reported `1 machine not shown` above seven empty rows.
	full := RenderDiscovered(discovered, width, budget)
	section := full
	if room := discoveredRoom(len(full), discoveredNeed(discovered, width), budget, wanted); room < len(full) {
		section = RenderDiscovered(discovered, width, room)
	}
	budget -= len(section)
	if budget < 1 {
		return joinBlocks(head, section, foot, height)
	}

	// The "how much is left" line costs a row, so the budget is asked twice: once with
	// it reserved, and again without when it turns out everything fits. At 120x24 the
	// overlay leaves six rows for candidates and this machine offers twenty, so the
	// list SCROLLS — and it has to say so, or the cursor walks off the screen exactly
	// as it did in the inbox before §16's fix.
	first, count := pickerLayout(blocks, cursor, budget-1)
	marker := true
	switch {
	case count == 0:
		// The reserved row was the ONLY row. A body holding nothing but `↓ 12 more · j/k to move`
		// names no candidate and tells the operator to press `j` on a list showing none, while the row
		// the cursor stands on is what `space` and `enter` act on — so the row goes to the candidate
		// and the marker goes with it, the heading already carrying the total. Measured at 80x6, where
		// head and foot leave the body exactly one line.
		first, count = pickerLayout(blocks, cursor, budget)
		marker = false
	case count == len(rows):
		// Everything fits, so there is nothing to mark and the reserved row returns to the list.
		first, count = pickerLayout(blocks, cursor, budget)
	}

	var body []string
	for i := first; i < first+count; i++ {
		body = append(body, blocks[i]...)
	}
	above, below := first, len(rows)-(first+count)
	// The scroll marker earns its row only once a candidate is drawn beside it. With `count == 0`
	// it restates the heading — which already says how many candidates there are — and names none of
	// them, which is the same defect the discovered section had at height 1 and the same class this
	// repository records as "keep the label, lose the action". Found by the height sweep in
	// TestThePickerSharesItsBodyWithTheSectionAndKeepsTheCursor: at 80x6 the body has exactly one row
	// and it went to `↓ 12 more · j/k to move` — a direction to press `j` on a list showing nothing.
	switch {
	case !marker || count == 0:
	case above > 0 && below > 0:
		body = append(body, fmt.Sprintf("  ↑ %d above · ↓ %d more · j/k to move", above, below))
	case below > 0:
		body = append(body, fmt.Sprintf("  ↓ %d more · j/k to move", below))
	case above > 0:
		body = append(body, fmt.Sprintf("  ↑ %d more above · j/k to move", above))
	}
	// The section goes UNDER the candidates: the tick boxes are what the operator came here for, and
	// a block that pushed them down the screen would put the subject of the screen below the fold.
	body = append(body, section...)
	return joinBlocks(head, body, foot, height)
}

// joinBlocks pads the body so the key line lands on the overlay's last row.
func joinBlocks(head, body, foot []string, height int) []string {
	out := append([]string{}, head...)
	out = append(out, body...)
	for len(out) < height-len(foot) {
		out = append(out, "")
	}
	out = append(out, foot...)
	if len(out) > height {
		out = out[:height]
	}
	return out
}

// pickerCount is the line a person reads to know what they are looking at. The
// candidate count is the count worth PROBING (see PickerRowsFor), and a round with
// hosts that never answered says so rather than letting them read as excluded.
func pickerCount(rows []PickerRow) string {
	if len(rows) == 0 {
		return "Hosts — nothing to show yet; r asks every candidate in ~/.ssh/config"
	}
	var usable, timedOut int
	for _, r := range rows {
		if r.Usable {
			usable++
			continue
		}
		if r.TimedOut {
			timedOut++
		}
	}
	s := fmt.Sprintf("Hosts — %s in ~/.ssh/config, %d answer with tmux",
		plural(len(rows), "candidate", "candidates"), usable)
	if timedOut > 0 {
		// Short deliberately: with the longer phrasing this line was 82 columns for two
		// candidates and `lines.Truncate` cut it mid-word at the 80 §16 promises. What a
		// timeout MEANS belongs on the row, which carries the remedy; this line is a
		// tally.
		s += fmt.Sprintf(", %d timed out", timedOut)
	}
	return s
}

// pickerProbedMsg carries one round's answer back to the screen. It carries the
// CANDIDATES and the RESULTS rather than rows, so the rules that turn them into rows
// have one owner — see PickerPorts.Probe.
type pickerProbedMsg struct {
	cands   []hostset.Candidate
	results []hostset.Result
	err     error
}

// pickerEnabledMsg carries the hosts main built and connected for the set that was just
// saved. `hosts` is every host the file enables, not only the new ones — deciding which
// are new is the model's job, because only it knows what is already being polled.
type pickerEnabledMsg struct {
	hosts []hub.Host
	err   error
}

// enableHosts asks main to build and connect the hosts the file now enables.
//
// A COMMAND and never inline, for the reason §16 states: a master takes about 1.55 s to
// become checkable and the operator has just pressed a key. So the save's note lands
// immediately, the new rows arrive as `connecting`, and they resolve on their own.
func (m model) enableHosts(kept []hostset.Entry) tea.Cmd {
	enable := m.pickerPorts.Enable
	if enable == nil {
		return nil
	}
	return func() tea.Msg {
		hosts, err := enable(kept)
		return pickerEnabledMsg{hosts: hosts, err: err}
	}
}

// pickerSavedMsg is what enter did. It carries the entries because the model must not
// record them until the write has actually landed — see the model's handler.
type pickerSavedMsg struct {
	kept    []hostset.Entry
	enabled int
	// off is every host the user turned OFF, and `stopped` is the subset whose master
	// actually ended. They are separate because the fleet must lose a host the user
	// disabled even when its master would not stop: leaving the row behind gives it a
	// status no poll can ever resolve, which is worse than a wrong one.
	off     []string
	stopped []string
	err     error
	stopErr string
}

// withKept installs what hosts.toml said, collapsed to the one reading the picker acts
// on, and keeps the complaint so the screen can carry it. One owner, called from
// WithPicker and from the save handler, because a second reader of a raw entry list is
// how the duplicate-alias defect got in.
func (m model) withKept(kept []hostset.Entry) model {
	m.pickerKept, m.pickerWarn = normaliseKept(kept)
	return m
}

// openPicker shows the picker. It probes when it has nothing to show, because a
// screen listing zero candidates on a machine that offers twenty is worse than a wait
// it names.
func (m model) openPicker() (tea.Model, tea.Cmd) {
	m.note = ""
	m = m.raise(modePicker)
	m = m.clampPickerCursor()

	// The crawl behind the hops runs on every opening, and it is BATCHED beside the probe rather than
	// chained after it: the two ask different machines different questions, and a crawl that waited
	// for a ten-host probe round would arrive minutes after the screen it belongs to. Neither gates
	// the paint (§16 promises a first paint in 50 ms).
	var cmds []tea.Cmd
	if len(m.picker) == 0 && m.pickerPorts.Probe != nil && !m.pickerBusy {
		m.pickerBusy = true
		m.note = "asking every candidate in ~/.ssh/config — this takes as long as the probe timeout"
		cmds = append(cmds, m.probeHosts())
	}
	if crawl := m.crawlBehind(); crawl != nil {
		cmds = append(cmds, crawl)
	}
	if len(cmds) == 0 {
		return m, nil
	}
	return m, tea.Batch(cmds...)
}

// probeHosts runs one round off the UI goroutine. Probing can never gate the screen:
// ten hosts took 7.65 s wall and §16 promises a first paint in 50 ms.
func (m model) probeHosts() tea.Cmd {
	probe := m.pickerPorts.Probe
	return func() tea.Msg {
		cands, results, err := probe()
		return pickerProbedMsg{cands: cands, results: results, err: err}
	}
}

// clampPickerCursor keeps the cursor inside the list, and off a row where no key would
// act.
//
// The second half is about the first screen a person ever sees. Verified live at 110×40
// against this machine's real ~/.ssh/config: the first candidate is `orbits.github.com`,
// a git remote, so the picker opened with the cursor on a row that refuses every key —
// and the first thing a new user does is press space and be told "cannot be enabled".
//
// It moves ONLY a cursor resting where nothing can happen, so it cannot take the user
// off a host they chose; that is the same promise the alias re-anchoring across a
// re-probe makes, and this is deliberately the weaker sibling of it. Searching forward
// before wrapping to the top keeps it as literally as possible: the nearest row that can
// act, never a jump home. The one visible interaction is that re-probing while parked on
// an excluded row moves the cursor to the nearest actionable one — which is a position
// no keystroke could have used anyway.
func (m model) clampPickerCursor() model {
	if len(m.picker) == 0 {
		m.pickerCursor = 0
		return m
	}
	if m.pickerCursor >= len(m.picker) {
		m.pickerCursor = len(m.picker) - 1
	}
	if m.pickerCursor < 0 {
		m.pickerCursor = 0
	}
	if m.picker[m.pickerCursor].actionable() {
		return m
	}
	for i := m.pickerCursor; i < len(m.picker); i++ {
		if m.picker[i].actionable() {
			m.pickerCursor = i
			return m
		}
	}
	for i := 0; i < m.pickerCursor; i++ {
		if m.picker[i].actionable() {
			m.pickerCursor = i
			return m
		}
	}
	// Every candidate is excluded. That is a real state §9 names as a specified screen,
	// not a failure, and row 0 is where the cursor belongs because there is nowhere
	// better — the rows still carry their reasons, and `r` and `esc` still work.
	return m
}

// pickerKey is what the keyboard means while the picker is up.
func (m model) pickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// Nothing is written on the way out. A picker that saved when it closed would
		// make "look at the list" and "commit to it" the same gesture.
		m.note = "hosts unchanged"
		m = m.dismiss()
		return m, nil

	case "j", "down":
		m.note = ""
		if m.pickerCursor < len(m.picker)-1 {
			m.pickerCursor++
		}
		return m, nil

	case "k", "up":
		m.note = ""
		if m.pickerCursor > 0 {
			m.pickerCursor--
		}
		return m, nil

	case " ":
		return m.pickerToggle()

	case "enter":
		return m.pickerCommit()

	case "r":
		if m.pickerPorts.Probe == nil {
			m.note = "cannot probe: this hub was started without a prober"
			return m, nil
		}
		if m.pickerBusy {
			m.note = "already asking — a probe takes as long as the probe timeout"
			return m, nil
		}
		m.pickerBusy = true
		m.note = "asking every candidate again — this takes as long as the probe timeout"
		return m, m.probeHosts()
	}
	return m, nil
}

// pickerToggle keeps or drops the host under the cursor.
func (m model) pickerToggle() (tea.Model, tea.Cmd) {
	if m.pickerCursor < 0 || m.pickerCursor >= len(m.picker) {
		return m, nil
	}
	// Asymmetric, and the direction is read off the row's own state: a row that is on is
	// being turned OFF, which is always allowed. Only switching one ON needs the probe's
	// blessing — so the refusal below reaches exactly the case where "cannot be enabled"
	// is a true sentence, which is what it was not before (review I4).
	if r := m.picker[m.pickerCursor]; !r.canDisable() && !r.canEnable() {
		// The row already carries its remedy, but the KEY still has to answer: a
		// keystroke that changes nothing and says nothing reads as a broken key.
		m.note = r.Alias + " cannot be enabled — " + r.Reason
		return m, nil
	}
	// Copied, not written through: m.picker is a slice header on a value receiver, so
	// assigning into it would reach back into the model bubbletea is replacing.
	rows := make([]PickerRow, len(m.picker))
	copy(rows, m.picker)
	rows[m.pickerCursor].Enabled = !rows[m.pickerCursor].Enabled
	m.picker, m.note = rows, ""
	return m, nil
}

// pickerCommit writes the decisions and ends what the user turned off.
func (m model) pickerCommit() (tea.Model, tea.Cmd) {
	if m.pickerPorts.Save == nil {
		m.note = "cannot save hosts: this hub was started without a hosts.toml writer"
		return m, nil
	}
	entries := pickerEntries(m.picker, m.pickerKept)

	// Every host the FILE said was on and the screen now says is off. The file is the
	// right side of that comparison because it is what the hub connected to at
	// startup, so it is what has a master to stop.
	var off []string
	for _, e := range m.pickerKept {
		if !e.Enabled {
			continue
		}
		for _, r := range m.picker {
			if r.Alias == e.Alias && r.writable() && !r.Enabled {
				off = append(off, e.Alias)
				break
			}
		}
	}

	save, stop := m.pickerPorts.Save, m.pickerPorts.Stop
	m.note = ""
	m = m.dismiss()
	// m.pickerKept is deliberately NOT assigned here. It records what the FILE holds,
	// and the file has not been written yet: assigning the intent made the retry after
	// a failed save compute its `off` list from a state that already said the host was
	// off, so `enter` → permission denied → fix it → `enter` left the master alive with
	// the screen never mentioning it (review C1). The success branch of the handler
	// records it.
	return m, func() tea.Msg {
		// The write comes first, and nothing is stopped if it fails: a master ended
		// while the file still enables its host leaves the hub disagreeing with its
		// own configuration, which is worse than a leaked connection.
		if err := save(entries); err != nil {
			return pickerSavedMsg{err: err}
		}
		var enabled int
		for _, e := range entries {
			if e.Enabled {
				enabled++
			}
		}
		msg := pickerSavedMsg{kept: entries, enabled: enabled, off: off}
		for _, alias := range off {
			if stop == nil {
				msg.stopErr = "could not stop the ssh master for " + alias +
					": this hub was started without a way to stop one"
				continue
			}
			if err := stop(alias); err != nil {
				msg.stopErr = "could not stop the ssh master for " + alias + ": " + err.Error()
				continue
			}
			msg.stopped = append(msg.stopped, alias)
		}
		return msg
	}
}
