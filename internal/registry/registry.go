// Package registry merges every host's panes into one ordered view.
//
// It performs no I/O: Update takes what the tmux package fetched and returns a
// sorted snapshot, so the whole attention model is table-testable.
package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/agents"

	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// ContentLines is how many content-bearing lines a tile keeps. It is a cap, not
// a target: the tile draws only as many as it has. Slicing a capture the hub
// already holds is free, and a tall terminal with few panes has room for far
// more than a screenful of list.
const ContentLines = 60

// KindPane and KindAgent say where a row came from. Most rows are panes; a
// background agent has no pane at all and reports its own state as a fact
// (docs/design.md §17), so the two provenances share one list and the row says
// which. The type keeps the name Pane because that is what the overwhelming
// majority are and renaming it would touch every call site for no behaviour.
const (
	KindPane  = "pane"
	KindAgent = "agent"
)

// Pane is one row as the UI sees it: a tmux pane, or a Claude session with no
// pane.
type Pane struct {
	Kind      string // KindPane | KindAgent
	Host      string
	PaneID    string
	SessionID string // `$N`; what attach targets, because a name does not survive a rename
	// AgentID is the listing's SHORT id for a pane-less row, and it is a different string from
	// SessionID: measured, `claude logs <full uuid>` answers `No job matching` while the short id
	// resolves, so every `claude attach|logs|stop` argument has to come from here. Empty for a pane.
	AgentID string
	// AlsoOn names the OTHER hosts whose listing reported this same session, empty for all but a
	// shared store. It is a fact about the world rather than a nicety: `~/.claude` can be synced
	// between machines, and then every host reports every session, so `Host` is the one the hub
	// chose to act through and these are the ones it could have chosen instead.
	AlsoOn   []string
	WindowID string // `@N`; what "has this pane moved" is asked about, for the same reason
	Session  string
	Window   string
	Command  string

	ClassifiedState state.State // derived from pixels; prefers AgentState when fresh
	Zone            []string    // the captured tail, as captured
	Content         []string    // chrome stripped, newest last

	// Activity is the last tick at which THIS pane's own output changed — not
	// `#{window_activity}`, which is what it used to be and which is per-WINDOW.
	// markActivity says why that mattered enough to move the meaning of a field.
	// For an agent row it is the session's start time, because a row with no pane
	// has no screen to have changed.
	Activity time.Time
	// HistorySize and CursorY are kept only to be compared against the NEXT tick:
	// they are two of the three per-pane signals markActivity watches. Nothing
	// renders them.
	HistorySize int
	CursorY     int
	Height      int
	Width       int
	Alt         bool
	SeenAt      time.Time // the last tick this pane was present

	// PanePID, Bracketed and Epoch are what the write path's pre-flight reads, and
	// they are kept here so asking "may this pane be written to" costs no tmux
	// call: PanePID is the root of the process walk that identifies an agent,
	// Bracketed says whether a paste would be read as text or as keypresses, and
	// Epoch is the server's identity, which invalidates every pane id it hands out
	// when it changes (docs/design.md §7).
	PanePID   int
	Bracketed bool
	Epoch     string

	// Index is the pane's 0-based index within its window.
	Index int
	// WindowIndex is the window's 0-based position in its session, and it is the
	// window component of a persisted hide key: the window's NAME is not identity,
	// because `automatic-rename on` makes it follow the running command
	// (docs/design.md §18, known-issues H1).
	WindowIndex int
	// Path is the row's working directory, and it has TWO sources because a row has two
	// shapes: `#{pane_current_path}` for a pane, and Claude's own `cwd` for a session with
	// no pane. Naming only the first here is what let UpdateAgents ship without copying
	// the second, which put every pane-less row in the bucket the project list pins last
	// (docs/design.md §21.1).
	//
	// It is carried on the row rather than looked up on demand because a row's own cwd is
	// the only thing that says which project it belongs to — tmux knows nothing about
	// projects, and Claude's listing is fetched on the slow sweep.
	Path string
	// StartCommand is #{pane_start_command}: the command the pane was created with,
	// quoted by tmux. It is STABLE rather than immutable: respawn-pane rewrites it
	// with the pane id unchanged (measured), which is why the hide key treats it as a
	// corroborator that may drop a mark and never as identity.
	StartCommand string
	// Dead is true when the pane's command has exited and remain-on-exit is on.
	Dead bool
	// DeadStatus is the exit code of the command in a dead pane.
	DeadStatus int

	// StateSince is the tick this row ENTERED the state it is in, and it is what the
	// waiting block sorts by: among rows that want the operator, the one that has waited
	// longest is the one to answer first (docs/design.md §21.11.1). Before it, five
	// waiting rows came out alphabetically and a row blocked for three hours sat below
	// one blocked for twenty seconds.
	//
	// It tracks the row's OWN state — ClassifiedState, which both kinds of row set —
	// rather than the effective State(), and the reason is DETERMINISM: State() reads the
	// wall clock, and a value computed from `now` must not be tracked. That is the whole
	// argument now. It used to rest on a second one — "the two differ only when a fresh
	// agent fact overrides a pane's pixels, which needs SetClaudeSession, and that has no
	// production caller" — and the join is wired since `joinAdoptedSessions`, so the two
	// DO differ in production and only the determinism argument survives. The same wiring
	// is why sortByAttentionAt snapshots the state instead of calling State() per comparison.
	StateSince time.Time
	// lastState is StateSince's companion: the state the row was in at the previous
	// tick, so "did it change" is a comparison rather than a guess. It is unexported
	// because it is bookkeeping, and the zero State is Needs — which is why entry is
	// detected from firstSight rather than from a zero value.
	lastState state.State

	// Stale means the host stopped answering. The pane and its last screen stay
	// on display, because a vanished host must not make its sessions disappear,
	// but the row says so rather than looking live.
	Stale      bool
	StaleSince time.Time

	// ClaudeSession is the session id this pane's agent belongs to, which joins a
	// pane row to a `claude agents --json` row. When set, AgentState wins over
	// ClassifiedState while fresh.
	ClaudeSession string
	AgentState    state.State
	AgentSeenAt   time.Time
	AgentName     string

	// AgentWord is the listing's own word, UNFOLDED, and it is here because nothing downstream can
	// recover it: `state.FromWord(s.Attention())` maps both `failed` and `error` onto state.Error,
	// so a row whose worker vanished is indistinguishable from a row whose work errored — and the
	// two need different answers. Two things gate on this string: the wake dialog (docs/design.md
	// §22.3) and the shared-store dedup, which reads `failed` as "this host can see no worker" and
	// must not read it as "the session errored".
	//
	// TWO producers write it, and both are named because forgetting one is this repo's signature
	// defect: UpdateAgents' agent branch, for a row with no pane, and the absorb branch above it,
	// for a listing row that folds into a pane the hub polls. Each has a test that goes THROUGH the
	// producer rather than building a Pane by hand.
	AgentWord string
	// AgentPID is the pid the LISTING gave for this session, and its only job is to say whether the
	// host that reported the word can SEE the worker.
	//
	// Measured across the operator's fleet: on the machine that runs them, every `working` session
	// carries a pid (6 of 6) and every finished one carries none; on the host that shares `~/.claude`
	// and runs nothing, NO record has a pid at all (31 of 31) — and that host reported `blocked` or
	// `failed` for six sessions the owner reported `working`. So a record without a pid is a claim
	// about the STORE, and one with a pid is a claim about a process.
	AgentPID int
	// AgentClaimAt is when the round's BEST CLAIM was recorded, and it is separate from AgentSeenAt on
	// purpose: a claim can arrive whose word this hub cannot use (a future version renames a state), and
	// such a word must not move the row's state — but it must still stop a host that cannot see the
	// worker from speaking in its place. Stamping AgentSeenAt for an unusable word is what made a row
	// read `? unknown` for ten minutes, which is why the two timestamps cannot be one.
	AgentClaimAt time.Time

	// SessionAlias, StatusLeft and StatusLeftLength are this pane's SESSION options as
	// the tmux server currently holds them, and they are here so the status-line
	// publisher can compare rather than remember (docs/design.md §21.16): what the hub
	// would write against what is already there, which makes the steady state cost
	// nothing and a restarted hub need no cache.
	SessionAlias     string
	StatusLeft       string
	StatusLeftLength string
}

// Claude is the uuid of the conversation this row is, or the empty string for a row that is not one.
//
// It exists because the field it comes from DEPENDS ON THE ROW'S KIND, and getting that wrong is
// silent: on an agent row the uuid is SessionID, while on a pane row SessionID is tmux's own session
// id (`$3`) and the uuid — if the hub knows it — is ClaudeSession. A reader that asked for SessionID
// without checking the kind would key a pane on `$3`, which changes when tmux restarts.
//
// Everything that must follow a conversation across the transitions the product performs asks this:
// the door renames the tmux session and the join folds the pane-less row into the pane, so kind, host
// and name all move, and the uuid does not. Two persisted marks are keyed on it — the favourite
// (internal/fav) and the operator's alias (internal/project) — and both were once keyed on what the
// operator READS, which came off the moment the product renamed the thing.
func (p Pane) Claude() string {
	if p.Kind == KindAgent {
		return p.SessionID
	}
	return p.ClaudeSession
}

// IsConversation says whether this row IS a Claude session, whatever shape it currently has.
//
// It is deliberately wider than `Claude() != ""`: an agent row is a conversation even when the
// listing gave it no uuid, because there is nothing else it could be. A pane is one only when the
// hub knows which conversation it is running.
//
// The renderer asks this, and asking the KIND instead is what made a conversation change shape the
// moment the door gave it a pane — it inherited the pane row's shape (a session header, then
// `paneID command`), which is right for a shell and wrong for a session whose name is the only
// thing that identifies it to a person.
func (p Pane) IsConversation() bool {
	return p.Kind == KindAgent || p.ClaudeSession != ""
}

// wordFailed is the listing's word for "the store says this session was running and I can see no
// worker for it". It is machine-local by nature — the check is against `roster.json`'s
// `workers[<id>].pid`, and a pid means nothing on another machine — which is what makes it the one
// word a shared store does not make identical everywhere.
const wordFailed = "failed"

// betterClaim reports whether claim A about ONE SHARED SESSION beats claim B.
//
// THE HOST THAT CAN SEE THE WORKER WINS, and the listing says which host that is: the record carries a
// `pid`. A record without one is a claim about the STORE — what `roster.json` last recorded — while one
// with a pid is a claim about a process this machine can see.
//
// MEASURED on the operator's fleet, and the numbers are the whole argument. `~/.claude` is shared with
// `side-desk`, which runs no workers: NOT ONE of its 31 records carries a pid, and it reported `failed`
// or `blocked` for six sessions that the owning machine reported `working`. On the owner, every
// `working` session carries a pid (6 of 6) and every finished one carries none. So "has a pid" is
// exactly "can see the worker", and it needs no threshold and no version check.
//
// The first version of this rule only refused `failed`, which fixed the instance in front of it and not
// the class: the very next report showed `blocked` from the same pid-less host burying a live `working`,
// so a session the operator had just answered still read `needs`. Any word from a host that cannot see
// the process is a claim about the store.
//
// The `failed` clause stays UNDER the pid one for the case where no host has a pid — a finished session,
// where the two claims differ only in whether the store was read before or after the worker went away.
func betterClaim(aWord string, aPID int, bWord string, bPID int) bool {
	if (aPID > 0) != (bPID > 0) {
		return aPID > 0
	}
	return bWord == wordFailed && aWord != wordFailed
}

// Key identifies a pane across ticks. A pane id is unique only within one
// server's lifetime, so the host is part of the key.
type Key struct {
	Host   string
	PaneID string
}

// agentStateFreshness is how long an agent state from `claude agents --json`
// remains authoritative. A fact older than this yields to the live pixel
// classification, because the CLI was measured 30 minutes stale (§17): a fact
// that old is worse than a fresh guess.
const agentStateFreshness = 10 * time.Minute

// State returns the pane's effective state, preferring the agent's own report
// when it is fresh.
//
// The screen cannot separate a finished agent from a working one (Claude renders
// its input box at all times), so `quiet` from the classifier is a guess where
// `done` from the CLI is not. But freshness matters: the CLI was measured 30
// minutes behind, and a fact that stale is worse than a live classification.
func (p *Pane) State() state.State { return p.stateAt(time.Now()) }

// stateAt is State at a GIVEN instant, and it exists so a comparator can fix the instant once.
// See sortByAttentionAt: a comparator that re-reads the clock is not a strict weak ordering.
func (p *Pane) stateAt(now time.Time) state.State {
	// A pane asking a permission question is NEVER overridden, and this guard is on the READ
	// side because that is the side the ordering can defeat. The listing runs every 20 s and the
	// dialog appears whenever the agent reaches it, so the normal order is: the sweep records
	// `working` while the pane really is working, and the prompt arrives five seconds later. A
	// guard on the WRITE cannot help there — nothing is being written at the moment the pixels
	// change — so the row rendered the recorded word for up to ten minutes: measured, `works` at
	// rank 4 against `needs` at rank 0, sorting the one row that wants the operator below every
	// row that wants nothing, with the glyph and word to match.
	//
	// Only the pixels can see a prompt. The listing has no word for it on the population this
	// join reaches: measured across both hosts, `state` is null on all 9 interactive rows, and
	// `blocked` appears only on background rows (5/5 local, 2/2 nuc) — which is exactly why a
	// recorded word can only ever DEMOTE such a row, and why the read has to refuse.
	if p.ClassifiedState == state.Needs {
		return state.Needs
	}
	// "Have we heard from the CLI about this pane" is a time question: AgentSeenAt
	// is set exactly when the CLI reports a session, and remains zero otherwise.
	if !p.AgentSeenAt.IsZero() && now.Sub(p.AgentSeenAt) < agentStateFreshness {
		return p.AgentState
	}
	return p.ClassifiedState
}

type Registry struct {
	// Hosts are polled concurrently — a remote tick is ~1.4 s and a serial loop
	// made every host wait for the slowest — so the map is guarded.
	mu     sync.Mutex
	panes  map[Key]*Pane
	sorted []Pane
	// hostRank is the FLEET order — the operator's own preference, local first — used to attribute
	// a session that several hosts report to one of them. It is set by the poller rather than
	// learned here, because the hosts are polled CONCURRENTLY: taking the order in which
	// UpdateAgents happened to arrive made the winner nondeterministic, and measured on the real
	// fleet a remote host won all 26 shared rows while the local machine sat at rank 0 of nothing.
	hostRank map[string]int
}

func New() *Registry {
	return &Registry{panes: map[Key]*Pane{}}
}

// SetClaudeSession records which Claude session a pane's agent belongs to, which
// joins the pane to the session so UpdateAgents can fold the CLI's state report
// into that row.
func (r *Registry) SetClaudeSession(host, paneID, sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.panes[Key{Host: host, PaneID: paneID}]; p != nil {
		p.ClaudeSession = sessionID
	}
}

// agentRowID is the row identity for a Claude session with no pane.
//
// Unique by construction rather than repaired on collision: a detect-and-suffix scheme would make
// a row's identity depend on the ORDER the listing arrived in, and the key is not private to the
// registry — a hide mark, an alias, the state log and the selection are all keyed on it, so every
// one of those artefacts would move under the operator for reasons nobody can see.
//
// `Kind` is in the hash because `--all` makes a stated residue live: measured 2026-08-15, 31 rows
// collapsed to 30 keys and one row was lost in silence, the pair being a `background` row and an
// `interactive` continuation of the same conversation in the same cwd (docs/design.md §22.11).
// Kind is fixed for the life of a session, so it cannot move the key between polls.
func agentRowID(s agents.Session) string {
	sum := sha256.Sum256([]byte(s.Kind + "\x00" + s.CWD))
	// The short id is the listing's own where there is one. A version that reports none still
	// needs a stable string here, and the uuid's first 8 characters are it — that fallback lives
	// HERE rather than in the producer, because a key only has to be stable while `Session.ID`
	// has to be usable as a verb's argument, and the two are different jobs.
	short := s.ID
	if short == "" && len(s.SessionID) >= 8 {
		short = s.SessionID[:8]
	}
	return "agent:" + short + "@" + hex.EncodeToString(sum[:4])
}

// UpdateAgents folds Claude's own account of its sessions into the registry.
//
// A session that is running in a pane the hub can see updates THAT row rather than
// adding one: before this, such a session appeared twice — once as the pane the
// poll found and once as the agent the CLI reported — and the two rows disagreed
// about its state, because one came from pixels and the other from a fact.
//
// The fact wins for those rows. §14 explains why: the screen cannot separate a
// finished agent from a working one (Claude renders its input box at all times), so
// `quiet` from the classifier is a guess where `done` from the CLI is not.
func (r *Registry) UpdateAgents(host string, ss []agents.Session, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Build a map from session ID to pane for sessions that have a pane.
	//
	// A pane tmux has RETIRED is not a join target. It keeps its row and its last screen on
	// purpose (a vanished pane must not make its session disappear), but folding a live listing
	// row into it hands the corpse a fresh fact: the dead pane then reads `works` for the whole
	// freshness window while the session — which is alive, and is the row with a door — never
	// appears at all. A stale host is the same case one level up: its panes are last-known, not
	// current.
	bySession := make(map[string]*Pane, len(r.panes))
	// byAttach is the same join recovered from the SERVER instead of from memory: a pane the door
	// created is running `claude attach <short id>`, and `#{pane_start_command}` still says so after
	// the hub has restarted — which the Keeper's adoption does not.
	//
	// Measured on the real fleet: session 7ef2fe7e appeared THREE times, and one of the three was the
	// door's own pane sitting beside the listing row it was made for, because the hub had been
	// restarted since the keystroke and nothing left in memory linked them. The uuid join below covers
	// the run that pressed `a`; this covers every run after it.
	byAttach := make(map[string]*Pane, len(r.panes))
	for _, p := range r.panes {
		if p.Kind != KindPane || p.ClassifiedState == state.Gone || p.Stale {
			continue
		}
		if p.ClaudeSession != "" {
			bySession[p.ClaudeSession] = p
		}
		if id := attachedSessionID(p.StartCommand); id != "" {
			byAttach[id] = p
		}
	}

	// One conversation is one row. The listing can report it TWICE — once as the background job and
	// once as the interactive session in front of it — with the same sessionId, cwd and name, and
	// `agentRowID` puts the KIND in the key so neither is lost (§22.11). That is right for two records
	// that are two subjects and wrong for two that are one, which is what the operator sees:
	// `xmap-universal-reader` twice on one screen. So the records are collapsed first, and the key
	// keeps doing its job for everything that survives.
	ss = oneRecordPerConversation(ss)

	present := make(map[Key]bool, len(ss))
	for _, s := range ss {
		// If this session has a pane, update the pane rather than creating a
		// second row. Two ways to know: the uuid the hub itself adopted, and the door's own payload
		// still on the pane — the second is what a restarted hub has.
		paneRow, ok := bySession[s.SessionID]
		if !ok && s.ID != "" {
			if q, viaAttach := byAttach[s.ID]; viaAttach {
				// Learned from the server, so write it down: everything else keyed on the uuid — a
				// favourite, a restart, the history — asks this field.
				q.ClaudeSession = s.SessionID
				paneRow, ok = q, true
			}
		}
		if ok {
			// The NAME is always worth taking: it is what the operator called the session and
			// it makes no claim about state.
			paneRow.AgentName = s.Name
			// The WORD is taken only when it says something, and only when it cannot bury the
			// one thing the pixels see and the listing cannot.
			//
			// An empty word carries no information — measured, 2 of 21 real sessions report
			// neither `state` nor `status` — and stamping AgentSeenAt for it made `State()`
			// prefer a fact that says nothing, so the row read `? unknown` for ten minutes.
			//
			// A `needs` pane is never demoted — but that rule lives on the READ side now
			// (`stateAt`), because the ordering defeats a write-side guard: the sweep records a
			// word while the pane is working and the prompt arrives afterwards, with nothing
			// being written at the moment the pixels change. Keeping the clause here as well
			// would only stop a `blocked` listing from PROMOTING a row and stop AgentSeenAt
			// being refreshed, so it is gone. The layering rule §14 argues for is "the listing
			// knows `done` where pixels only guess `quiet`", which says nothing about a prompt.
			// WHO MAY SPEAK THIS ROUND is decided before anything is written, and on the CLAIM rather
			// than on the word — because a word this version cannot interpret is still a claim by the
			// machine that owns the process. Measured as a hole in the first version of this rule: with
			// the owner reporting a word the hub does not know, `Attention()` returned "" and the fold
			// skipped it entirely, so the pid-less host's `blocked` dev and the row read `needs` on a
			// working session. A property test over the whole vocabulary — every owner word against
			// every sharer word, both poll orders — is what found it.
			if paneRow.AgentClaimAt.Equal(now) &&
				betterClaim(paneRow.AgentWord, paneRow.AgentPID, s.State, s.PID) {
				continue
			}
			paneRow.AgentPID, paneRow.AgentClaimAt = s.PID, now
			// The WORD is kept only when there is one: a listing that reports neither `state` nor
			// `status` — measured, 2 of 21 sessions — must not erase the last word that did, because
			// `AgentWord` is what `K` and the wake dialog read to say whether the worker is gone. The
			// CLAIM above is recorded either way, since a pid says who was entitled to speak even when
			// the word says nothing.
			if s.State != "" {
				paneRow.AgentWord = s.State
			}
			// AND THE ROUND'S BEST CLAIM RETRACTS a state written EARLIER IN THE SAME ROUND by a claim
			// that was not entitled to speak. Without this the rule held in one poll order and not the
			// other: the pid-less host answered first, wrote its word, and the owner's unusable word
			// then replaced the claim while leaving that state standing — so the row still read the
			// sharer's answer. Falling back to the pixels is the honest outcome when the only host that
			// may speak has said nothing this hub can read.
			//
			// Same-round only (`AgentSeenAt == now`): a word this host itself reported a second ago is
			// not retracted by a later listing that reports none, which is the rule the empty-word case
			// beside it already fixed.
			if s.Attention() == "" && paneRow.AgentSeenAt.Equal(now) {
				paneRow.AgentState, paneRow.AgentSeenAt = state.Unknown, time.Time{}
			}
			if w := s.Attention(); w != "" {
				// AND A `failed` WORD MAY NOT BURY A LIVE ONE FROM ANOTHER HOST IN THE SAME ROUND.
				//
				// `UpdateAgents` runs once per host and the agent polls fan out concurrently, so
				// without this the LAST host to answer decided the row — a race whose outcome the
				// operator reads as fact. With `~/.claude` shared, the host that does not run the
				// process reports `failed` (it can see no pid), so a session with a workflow running
				// read `error` while its own screen said `Waiting for 1 dynamic workflow to finish`
				// and its own machine said `working`. Measured 15 times out of 15 on the live fleet,
				// and `works` in the same code with only the local host polled.
				//
				// Scoped to THIS round (`AgentSeenAt == now`) so nothing can get stuck: when the
				// session really does end, every host says `failed` in the next round and the row
				// follows. The row dedup has had this rule since the shared store was measured; this
				// is the same rule on the path that reaches a pane, through the same predicate.
				paneRow.AgentState, paneRow.AgentSeenAt = state.FromWord(w), now
			}
			continue
		}

		// No pane: create or update an agent row.
		//
		// The id alone is NOT a key. Measured on a real host: `claude agents --json`
		// reported two different sessions under one `sessionId` — same pid, same
		// startedAt, different cwd and name, and neither carrying an `id` field, so
		// `agents.Parse` back-filled both from `SessionID[:8]`. Keyed on that, both
		// records landed on one *Pane and the second overwrote the first's Session,
		// Command, SessionID, Activity and ClassifiedState: 12 sessions in, 11 rows
		// out, one of the operator's sessions invisible on the dashboard.
		//
		// The truncation is not the cause — the FULL ids are identical too — so the
		// discriminator has to come from outside the id, and cwd is the one that
		// works: `(host, sessionId, cwd)` was unique for 21 of 21 rows where
		// `(host, sessionId)` was unique for 20 — a 2026-08-13 census under bare
		// `--json`. Under the `--all` call §22.6 mandates, the same key gives 31 rows
		// and 30 distinct keys, so one row is lost silently and `Kind` has to join the
		// key (docs/design.md §22.11). It is also stable, because an
		// agent's cwd is fixed at session start, so the key does not move between
		// polls.
		//
		// Unique by construction rather than repaired on collision: a
		// detect-and-suffix scheme would make the id depend on the order the listing
		// arrived in. Residue worth stating: two sessions sharing an id, a cwd AND a
		// kind would still merge. That has NOT been observed. The weaker residue — id
		// and cwd alone — DID occur under `--all` on 2026-08-15, 31 rows collapsing to
		// 30 keys with one of the operator's rows lost in silence, the pair being a
		// background row and an interactive continuation of the same conversation in
		// the same cwd; that is why `Kind` is in the hash (docs/design.md §22.11). The
		// name cannot help either — two rows here share a name and a cwd while
		// differing in id.
		//
		// Safe to lengthen because an agent row's PaneID is identity only: the
		// renderer draws `p.Session` for these rows and never the id
		// (`internal/ui/render.go:104-110`).
		k := Key{Host: host, PaneID: agentRowID(s)}
		present[k] = true
		p := r.panes[k]
		firstAgentSight := p == nil
		if firstAgentSight {
			p = &Pane{Host: host, PaneID: k.PaneID, Kind: KindAgent}
			r.panes[k] = p
		}
		p.Kind = KindAgent
		p.Session = s.Name
		p.Command = s.Kind
		p.AgentID = s.ID
		// The row's PROJECT comes from here, and the cwd was already in hand three lines
		// up — agentRowID hashes it. Forgetting to copy it made every pane-less row
		// Unassigned, which is the bucket the project list pins LAST: 32 of 45 rows under
		// `--all` on 2026-08-15, i.e. exactly the rows §21 exists to surface
		// (docs/design.md §21.1, §22.1).
		p.Path = s.CWD
		p.SessionID = s.SessionID
		p.ClassifiedState = state.FromWord(s.Attention())
		p.AgentWord, p.AgentPID = s.State, s.PID
		markStateEntry(p, now, firstAgentSight)
		p.Activity = s.StartedAt
		p.SeenAt = now
		p.Stale, p.StaleSince = false, time.Time{}
	}
	// A session that has left the listing is done with, not stale: the listing is
	// the whole truth about its own population, unlike a pane list that a broken
	// tunnel can silently empty.
	for k, p := range r.panes {
		if k.Host == host && p.Kind == KindAgent && !present[k] {
			delete(r.panes, k)
		}
	}
	r.resort()
}

// doorPayload matches the start command the door leaves on a pane it creates: §20's `sh -c` wrapper
// with `claude attach <id>` as the FIRST command inside it.
//
// Anchored at the start, which is the whole precision of it. A pane that merely MENTIONS the verb — a
// shell where somebody typed it, a log being tailed, `claude logs` on the same id — must not swallow a
// listing row, and an unanchored search claims all three.
//
// Quotes and backslashes are stripped first, INCLUDING double quotes: tmux reports the payload as it
// was quoted for two shells and adds a pair of its own around anything containing a space. The first
// version stripped only `'` and the backslash, so the bare string still began with a double quote and
// the anchor never matched — it passed a unit test whose fixture I had quoted by hand and changed
// nothing on the fleet. The fixture now carries the string read off the operator's own server.
var doorPayload = regexp.MustCompile(`^sh -c claude attach ([A-Za-z0-9_-]+)`)

// attachedSessionID is the short id a door pane is attached to, or "".
// AttachedSessionID is exported for the one caller outside this package that needs the same
// question answered: the window namer, which may only rename a window the HUB made (§21.18). The
// answer is derived from the pane rather than remembered, so it survives a hub restart — the
// argument §22.3 makes for the join this same function serves.
func AttachedSessionID(startCommand string) string { return attachedSessionID(startCommand) }

func attachedSessionID(start string) string {
	if start == "" {
		return ""
	}
	bare := strings.NewReplacer("'", "", `"`, "", `\`, "").Replace(start)
	bare = strings.Join(strings.Fields(bare), " ")
	if m := doorPayload.FindStringSubmatch(bare); m != nil {
		return m[1]
	}
	return ""
}

// oneRecordPerConversation folds listing records that are the SAME conversation seen under two kinds.
//
// The pair is measured: `claude agents --json --all` reported session
// `7ef2fe7e-c88d-46da-ac9d-0b57f170c3d8` twice on one host, once `background` with an id and once
// `interactive` with none, at the same cwd and under the same name. To the operator that is one thing,
// and the row worth keeping is the one with an ID: it carries the argument every `claude` verb takes,
// a state word, and a door. The interactive twin carries none of the three.
//
// The key is (sessionId, cwd, name) and NOT the sessionId alone, because §22.11 measured the other
// shape too — two records under one sessionId with DIFFERENT cwd and name, which are genuinely two
// sessions. Collapsing those lost one of the operator's rows in silence, 31 rows into 30 keys.
func oneRecordPerConversation(ss []agents.Session) []agents.Session {
	type key struct{ uuid, cwd, name string }
	first := make(map[key]int, len(ss))
	out := make([]agents.Session, 0, len(ss))
	for _, s := range ss {
		if s.SessionID == "" {
			out = append(out, s)
			continue
		}
		k := key{s.SessionID, s.CWD, s.Name}
		at, seen := first[k]
		if !seen {
			first[k] = len(out)
			out = append(out, s)
			continue
		}
		// The one with an id wins; where both have one (or neither does) the first stays, so the
		// answer does not depend on the order the listing arrived in.
		if out[at].ID == "" && s.ID != "" {
			out[at] = s
		}
	}
	return out
}

// markStateEntry records WHEN a row entered the state it is now in.
//
// Called with the row's own freshly computed state, after it is assigned. On first sight
// the state is an entry by definition — and it has to be said that way rather than
// inferred, because state.Needs is the zero State, so a fresh row's unset field already
// reads as "waiting".
func markStateEntry(p *Pane, now time.Time, firstSight bool) {
	if firstSight || p.ClassifiedState != p.lastState {
		p.StateSince = now
	}
	p.lastState = p.ClassifiedState
}

// markActivity sets p.Activity to now if this pane produced anything since the last
// tick, and leaves it alone otherwise.
//
// It exists because `#{window_activity}` — the only activity timestamp tmux offers —
// is per-WINDOW, and Classify returns Works from the activity age before it tests
// anything else. So a pane sharing a window with a sibling that prints more often than
// FreshFor could never reach `error`, `quiet` or `idle`: it was pinned at `works`,
// rank 4, the bottom of the inbox, and `needs` survived only because it is tested
// first. That is the core attention model, not a corner of it.
//
// Measured on a private socket, tmux 3.7b, two panes in one window with only the
// second one writing:
//
//	window_activity   %0=1786827863  %1=1786827863   ← the SILENT pane reports its
//	window_activity   %0=1786827865  %1=1786827865      sibling's timestamp
//	history_size      %0=1           %1=9            ← per PANE, and it moves
//	history_size      %0=1           %1=19              only for the one that wrote
//
// tmux has no per-pane timestamp at all, so the hub keeps one. Three signals, because
// no one of them is sufficient:
//
//   - history_size grows only when a line leaves the TOP, so a pane that redraws in
//     place — which is exactly what Claude Code does — looks silent by that measure
//     alone.
//   - cursor_y catches movement within the screen.
//   - the ZONE catches a redraw that moves neither: a spinner advancing one glyph is
//     this pane's own output and nothing else's. The capture is already fetched, so
//     comparing it is free.
//
// FIRST SIGHT takes the window's timestamp, which is the best prior available and
// keeps the cold answer defined: a fleet of long-idle panes must read idle on the
// first frame rather than all reading `works` because the hub has no history yet.
// From the second tick on, the pane's own evidence decides.
//
// A STALE capture leaves the zone unchanged (the range that came back is not the one
// that was asked for), so it cannot forge activity — it just costs one tick of
// evidence, which is the same trade the zone assignment already makes.
//
// What this deliberately does NOT do: reorder Classify so the error pattern beats
// works-by-age. With the age now per-pane, `error`, `quiet` and `idle` are all
// reachable without touching the order — and reordering would call a `go test` that
// has printed its first `--- FAIL` and is still running `error` rather than `works`,
// which is a live pane the operator does not need yet.
func markActivity(p *Pane, d tmux.Delta, zone []string, now time.Time, firstSight bool) {
	if firstSight {
		p.Activity = time.Unix(d.Activity, 0)
		p.HistorySize, p.CursorY = d.HistorySize, d.CursorY
		return
	}
	if d.HistorySize != p.HistorySize || d.CursorY != p.CursorY || !slices.Equal(zone, p.Zone) {
		p.Activity = now
	}
	p.HistorySize, p.CursorY = d.HistorySize, d.CursorY
}

// Update replaces one host's panes.
//
// zones are the cheap classification captures, taken for every pane. fulls are
// whole-screen captures, taken only for panes whose tile is on screen — they are
// the ONLY source of Content, because the zone is a pane's tail and on an idle
// Claude pane the tail is chrome by construction (rule, empty prompt, rule,
// footer, footer, blank), so stripping chrome from it yields nothing. A pane with
// no full capture this tick keeps the Content it had.
//
// Panes that were present before and are absent now become Gone and keep their
// last screen: tmux destroys a pane and its scrollback together, so the hub's own
// cache is the only remaining evidence of why it died.
func (r *Registry) Update(host string, ds []tmux.Delta, ls map[string]tmux.Labels, zones []tmux.Capture, fulls map[string]tmux.Capture, now time.Time, sinceLast time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	byID := make(map[string]tmux.Capture, len(zones))
	for _, c := range zones {
		byID[c.PaneID] = c
	}

	present := make(map[Key]bool, len(ds))
	for _, d := range ds {
		k := Key{Host: host, PaneID: d.PaneID}
		present[k] = true

		p := r.panes[k]
		firstSight := p == nil
		if firstSight {
			p = &Pane{Host: host, PaneID: d.PaneID, Kind: KindPane}
			r.panes[k] = p
		}
		p.Kind = KindPane
		l := ls[d.PaneID]
		p.Stale, p.StaleSince = false, time.Time{}
		p.SessionID, p.WindowID = d.SessionID, d.WindowID
		p.Session, p.Window, p.Command = l.Session, l.Window, l.Command
		p.Path = l.Path
		// The session's own options, so the status-line publisher can write only what
		// differs (docs/design.md §21.16). They come from the same fetch as the labels.
		p.SessionAlias, p.StatusLeft, p.StatusLeftLength = l.SessionAlias, l.StatusLeft, l.StatusLeftLength
		p.Height, p.Width, p.Alt = d.PaneHeight, d.WindowWidth, d.Alt
		p.PanePID, p.Bracketed, p.Epoch = d.PanePID, d.Bracketed, d.Epoch
		p.Index, p.StartCommand, p.Dead, p.DeadStatus = d.Index, l.StartCommand, d.Dead, d.DeadStatus
		p.WindowIndex = d.WindowIndex
		p.SeenAt = now

		// The zone is resolved before Activity, because whether the SCREEN changed is
		// one of the three things that decide whether this pane wrote anything.
		zone := p.Zone
		if c, ok := byID[d.PaneID]; ok && !c.Stale {
			zone = c.Lines
		}
		markActivity(p, d, zone, now, firstSight)
		p.Zone = zone
		// A Stale capture means the pane was resized between the delta and the
		// batch, so the range that was asked for is not the range that came back.
		// Keeping the previous zone costs one tick of freshness; trusting a
		// mis-ranged one would classify from the wrong rows. The flag was computed
		// and discarded before this.
		if c, ok := fulls[d.PaneID]; ok {
			p.Content = lines.ContentTail(c.Lines, ContentLines)
		}
		p.ClassifiedState = state.Classify(state.Input{
			Zone:         p.Zone,
			ActivityAge:  now.Sub(p.Activity),
			PollInterval: sinceLast,
			Dead:         d.Dead,
			Alt:          d.Alt,
			Command:      p.Command,
		})
		markStateEntry(p, now, firstSight)
	}

	for k, p := range r.panes {
		if k.Host == host && p.Kind != KindAgent && !present[k] {
			p.ClassifiedState = state.Gone
		}
	}
	r.resort()
}

// SortByAttention puts the panes in the order the dashboard shows them: what
// needs the user first, then host, session and pane id so the sequence is
// stable between ticks.
//
// It is exported because the registry is not the only thing that has to produce
// this order. Anything that builds a pane list for the renderer without going
// through Update — the mockup generator, a fixture — shows rows in whatever
// order it happened to construct, and attention ordering is the one property the
// dashboard exists for. One comparator, so a fixture cannot drift from
// production.
// lastKnownChange is the most recent thing the registry knows about a row, and it BRANCHES ON KIND
// because the two kinds fill their time fields differently. An earlier attempt took the later of the
// two fields to avoid the branch; that was wrong, and the producer test caught it — a pane-less row
// born after another row's state change wins on birth, which is not a change at all.
//
//   - a PANE row's Activity moves on nearly every tick: markActivity stamps it from HistorySize,
//     CursorY and the classification zone. It is the recent event, and StateSince is the fallback
//     for a pane the registry has not yet dated.
//   - a PANE-LESS row's Activity is its session's START time and never moves — UpdateAgents
//     re-assigns the same `s.StartedAt` on every poll and markActivity is never called for it — so
//     it must be ignored here entirely. What moves is StateSince, stamped by markStateEntry when
//     the listing's word changes, which is exactly what §22.6's rule needs: a row that FAILED two
//     hours ago sinks below one that failed two minutes ago.
//
// Both fields are monotonic, so neither branch can move a row backwards within its own history.
func lastKnownChange(p Pane) time.Time {
	if p.Kind == KindAgent {
		return p.StateSince
	}
	if !p.Activity.IsZero() {
		return p.Activity
	}
	return p.StateSince
}

func SortByAttention(out []Pane) { sortByAttentionAt(out, time.Now()) }

// sortByAttentionAt is the sort with its instant fixed, and the fixing is the point.
//
// `State()` reads the wall clock — a fresh agent fact overrides a pane's pixels for
// agentStateFreshness — so a comparator calling it can see a row cross that boundary BETWEEN two
// comparisons. `sort.Slice` with a comparator that changes its mind is not merely wrongly ordered:
// it is undefined, free to panic or to scramble the slice. StateSince's own comment states the rule
// this obeys — a value computed from `now` must not be tracked — and until the pane-to-session join
// was wired, AgentSeenAt was always zero and the comparator was deterministic BY ACCIDENT. Wiring
// the join removed the accident, so the snapshot lands in the same commit.
//
// The snapshot is keyed on (Host, PaneID), which is the registry's own pane key and therefore
// unique. Two rows that somehow shared a key would read the same state, tie, and fall through to the
// stable tiebreak — the safe direction.
func sortByAttentionAt(out []Pane, now time.Time) {
	st := make(map[Key]state.State, len(out))
	for i := range out {
		st[Key{Host: out[i].Host, PaneID: out[i].PaneID}] = out[i].stateAt(now)
	}
	rank := func(p Pane) int { return st[Key{Host: p.Host, PaneID: p.PaneID}].Rank() }
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if rank(a) != rank(b) {
			return rank(a) < rank(b)
		}
		// Within the WAITING block only, the longest wait comes first: that is the
		// order the question "who needs me" is actually asked in (§21.11.1). Scoped to
		// Needs on purpose: every other rank is ordered by recency below (§22.6), with
		// host/session/pane id remaining the tiebreak WITHIN one recency bucket, which
		// is what still makes the list stable between ticks.
		//
		// A zero StateSince is UNKNOWN, not the beginning of time. Sorting it as the
		// oldest would let a row the registry has never dated outrank every real wait,
		// so it falls through to the stable tiebreak instead.
		if rank(a) == state.Needs.Rank() {
			az, bz := a.StateSince.IsZero(), b.StateSince.IsZero()
			switch {
			case !az && !bz && !a.StateSince.Equal(b.StateSince):
				return a.StateSince.Before(b.StateSince)
			case az != bz:
				return bz // a known entry time outranks an unknown one
			}
		}
		// Recency for every rank EXCEPT Needs, and the exclusion is explicit because the block
		// above only RETURNS when the two entry times differ. Two rows that entered `needs` on the
		// same tick fall through it, and that is the whole first frame rather than a corner case:
		// UpdateAgents stamps every first-sight row with one `now`. Without this guard the waiting
		// block would be reordered newest-first, the exact inverse of longest-wait-first.
		//
		// Bucketed to the MINUTE, and the bucket is not tidiness: the key moves on nearly every
		// tick for a working pane, so a per-second order would re-sort the list under the eye
		// while it is being read. host/session/pane id below is the tiebreak WITHIN a bucket,
		// which is what keeps the list stable between ticks (docs/design.md §22.6).
		//
		// It used to be justified by the CURSOR — an index into the on-screen list, which a
		// reorder made name a different row. That is now closed on the UI side (rowCursor names
		// the row itself), so this bucket is about reading the screen and not about the
		// keyboard, and narrowing it would cost nothing but legibility.
		//
		// A zero key is UNKNOWN, not the beginning of time, so a stamped row outranks an unstamped
		// one — the same rule the Needs block applies to StateSince.
		if rank(a) != state.Needs.Rank() {
			ac, bc := lastKnownChange(a), lastKnownChange(b)
			az, bz := ac.IsZero(), bc.IsZero()
			switch {
			case az != bz:
				return bz // a known time outranks an unknown one
			case !az && !bz:
				am, bm := ac.Truncate(time.Minute), bc.Truncate(time.Minute)
				if !am.Equal(bm) {
					return am.After(bm) // newest first
				}
			}
		}
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		if a.Session != b.Session {
			return a.Session < b.Session
		}
		return a.PaneID < b.PaneID
	})
}

// SetHostOrder records the fleet order, which is the tiebreak for which host a shared session is
// attributed to: the local machine leads, then hosts.toml, because that is the operator's own
// statement of preference and the door should knock next door before it crosses an ssh leg.
//
// The poller calls this before it fans out. It cannot be learned from the order UpdateAgents is
// called in, because that fan-out is concurrent — and a rank that shuffles per tick would migrate
// rows between hosts under the operator's cursor, taking StateSince and the selection with them.
func (r *Registry) SetHostOrder(labels []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rank := make(map[string]int, len(labels))
	for i, l := range labels {
		if _, dup := rank[l]; !dup {
			rank[l] = i
		}
	}
	r.hostRank = rank
}

// rankFor is a host's place in the fleet. A host the fleet does not name — one left over from an
// earlier fleet, or one that answered before the first SetHostOrder — ranks behind every named one,
// and all of them tie; `beats` breaks that tie on the label so the answer never depends on map
// order.
func (r *Registry) rankFor(host string) int {
	if i, ok := r.hostRank[host]; ok {
		return i
	}
	return len(r.hostRank)
}

func (r *Registry) resort() {
	out := make([]Pane, 0, len(r.panes))
	for _, p := range r.panes {
		out = append(out, *p)
	}
	out = r.oneRowPerSession(out)
	SortByAttention(out)
	r.sorted = out
}

// oneRowPerSession collapses the agent rows that are the SAME session seen from several hosts.
//
// `~/.claude` can be shared between machines — measured on this fleet, 2026-08-17: `roster.json`
// and `jobs/<id>/state.json` are byte-identical on `local` and `side-desk`, so `claude agents
// --json --all` returns the same 26 sessions on each. Keyed per host that made 52 rows out of 26
// sessions, and a live one appeared twice with contradictory states.
//
// The grouping is not a heuristic and needs no new identity: `agentRowID` hashes the short id,
// Claude's kind and the cwd and mentions no host, so two AGENT rows sharing a PaneID are the same
// session by construction — which is also why a background job and its interactive continuation in
// one cwd stay two rows, and why two sessions differing only in cwd do.
//
// PANE rows are excluded, and that is the load-bearing half: a pane id is unique only within one
// tmux server, so `%1` exists on every host, and grouping panes by PaneID would collapse the whole
// dashboard onto one machine.
//
// Which host wins decides where the door knocks. A `failed` claim is the one word a host produces
// about its OWN ignorance — it means "the store says this ran and I can see no worker for it",
// computed from `roster.json`'s machine-local `workers[<id>].pid` — while every other word is read
// out of the shared store and is therefore identical on every host. So `failed` loses to any host
// that can see the session alive; among claims that agree, the lowest rank wins. Attributing the row
// to the failed host would send a wake to a machine with no worker while another machine runs one,
// which is two workers against one transcript.
//
// The cost of that rule, measured the same day and stated rather than hidden: a shared store is
// consistent only EVENTUALLY. Right after `claude stop`, `local` said `failed` (it had re-read the
// roster) while `side-desk` still said `working` from its not-yet-synced copy, so the row read
// `works` for a session that was already gone. The trade is deliberate — a stale `works` corrects
// itself on the next sync, while preferring `failed` hides live work for as long as the work runs,
// which is the defect the operator reported.
func (r *Registry) oneRowPerSession(rows []Pane) []Pane {
	first := make(map[string]int, len(rows))
	out := make([]Pane, 0, len(rows))
	for _, p := range rows {
		if p.Kind != KindAgent {
			out = append(out, p)
			continue
		}
		at, seen := first[p.PaneID]
		if !seen {
			first[p.PaneID] = len(out)
			out = append(out, p)
			continue
		}
		if r.beats(p, out[at]) {
			p.AlsoOn = append(p.AlsoOn, out[at].Host)
			p.AlsoOn = append(p.AlsoOn, out[at].AlsoOn...)
			out[at] = p
			continue
		}
		out[at].AlsoOn = append(out[at].AlsoOn, p.Host)
	}
	// The claimants are named in fleet order rather than in map order, because `r.panes` is a map
	// and an unsorted list would reshuffle on every tick.
	for i := range out {
		if len(out[i].AlsoOn) > 1 {
			slices.SortFunc(out[i].AlsoOn, func(a, b string) int {
				if d := r.rankFor(a) - r.rankFor(b); d != 0 {
					return d
				}
				return strings.Compare(a, b)
			})
		}
	}
	return out
}

// beats reports whether challenger is the better claim to one shared session than holder.
//
// Total and order-independent, because `r.panes` is a map: two claims are compared on whether they
// admit ignorance, then on fleet rank, then on the label. Without that last clause two unranked
// hosts would decide the row by map iteration order, which is a different answer per tick.
func (r *Registry) beats(challenger, holder Pane) bool {
	if betterClaim(challenger.AgentWord, challenger.AgentPID, holder.AgentWord, holder.AgentPID) {
		return true
	}
	if betterClaim(holder.AgentWord, holder.AgentPID, challenger.AgentWord, challenger.AgentPID) {
		return false
	}
	if cr, hr := r.rankFor(challenger.Host), r.rankFor(holder.Host); cr != hr {
		return cr < hr
	}
	return challenger.Host < holder.Host
}

// Panes returns the current snapshot, sorted by attention then host, session,
// pane id.
//
// It returns a COPY, and the reason is the mistake it makes impossible rather than any
// defect it fixed. Returning `r.sorted` handed the caller a reference into the
// registry's own published state: `SortByAttention` is exported and sorts IN PLACE, so
// `SortByAttention(reg.Panes())` compiles, reads like the obvious thing, and would
// reorder the live snapshot outside the lock while other goroutines read it — and
// `panes[i].Stale = true` would be seen by every later reader. Neither had a caller,
// which is what "latent" means: the cost of the copy is tens of structs per tick, and
// the cost of the class is what the sibling package just paid for handing out
// `&p.hosts[i]`.
//
// What the copy does NOT buy, said out loud so the next reader does not assume it: a
// Pane carries `Zone` and `Content` as slices, so the copy shares their backing arrays.
// That is safe because nothing ever writes them in place — `Update` REPLACES both, and
// `lines.ContentTail` builds a fresh slice rather than sub-slicing its input, which
// TestAPublishedPanesContentIsNeverWrittenInPlace holds to.
func (r *Registry) Panes() []Pane {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.sorted)
}

// MarkHostStale marks every pane of a host as unreachable, keeping it in the list
// with its last known screen. A host that goes away must never make its sessions
// silently vanish, and it must not leave them looking live either — observed
// when a killed tunnel left a pane still reading `works`.
func (r *Registry) MarkHostStale(host string, since time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, p := range r.panes {
		// Pane rows only. An agent row's liveness has nothing to do with the tmux
		// tunnel — a dropped forward says nothing about whether Claude's own
		// sessions are running, and they have their own producer and their own
		// failure to report (Host.AgentsReason).
		if k.Host == host && p.Kind != KindAgent {
			p.Stale = true
			p.StaleSince = since
		}
	}
	r.resort()
}
