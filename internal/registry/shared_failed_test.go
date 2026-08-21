package registry

import (
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/agents"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// A `failed` WORD FROM A HOST THAT DOES NOT RUN THE PROCESS MUST NOT BURY A LIVE WORD FROM THE HOST
// THAT DOES — on a PANE row, which is where the rule was missing.
//
// Measured on the operator's fleet, and the numbers are what makes this a rule rather than a taste.
// `~/.claude` is shared with `side-desk`, so that host's listing carries every session — including the
// ones whose worker runs on `local` — and `failed` is decided by looking for a pid in `roster.json`,
// which means nothing on another machine. It called ALL THREE live sessions `failed` while agreeing with
// local on all 26 finished ones: 3 of 3 against 0 of 26.
//
// What the operator saw: `seedtool-development`, whose own screen read `✻ Waiting for 1 dynamic workflow
// to finish` and whose own machine reported `working`, rendered as `error` — fifteen times out of
// fifteen with the remote host in the poll, and `works` in the same code with only the local host. The
// row dedup has had this rule since the shared store was first measured; the fold that puts a word on a
// pane row did not, so a session with a workflow running read as a failure on the one screen the
// operator watches.
//
// BOTH ORDERS are asserted because the agent polls fan out CONCURRENTLY: without the rule the last host
// to answer decides, so a test that fixes one order proves nothing about the race.
func TestASharedFailedWordDoesNotBuryALiveOneOnAPaneRow(t *testing.T) {
	now := time.Unix(1787194000, 0)
	// The real screen of the session in question: a Claude pane waiting on a workflow. It carries no
	// "esc to interrupt", which is why the pixels alone cannot rescue this row.
	zone := []string{
		"  Дальше по цели: планы 2–6, затем /simplify, /codex:review",
		"✻ Waiting for 1 dynamic workflow to finish",
		"                                        100% context used · ◎ /goal active (14m)",
		"─────────────────────────────────────────────── seedtool-development ─",
		"❯ ",
	}
	const uuid = "77ef6f5e-a719-4cf9-8dbd-722e986f2604"
	// The PIDS are what the real listings carry: the machine that runs the worker reports one (6 of 6
	// working sessions on the operator's fleet), and the host that only shares `~/.claude` reports none
	// at all (0 of 31 records).
	local := agents.Session{ID: "77ef6f5e", SessionID: uuid, Kind: "background", PID: 237624,
		Name: "seedtool-development", State: "working", StartedAt: now.Add(-time.Hour)}
	remote := agents.Session{ID: "77ef6f5e", SessionID: uuid, Kind: "background",
		Name: "seedtool-development", State: "failed", StartedAt: now.Add(-time.Hour)}

	seed := func(r *Registry) {
		r.Update("local",
			[]tmux.Delta{{PaneID: "%28", Activity: now.Unix(), PaneHeight: 43, WindowWidth: 120,
				PanePID: 1059855, CursorY: len(zone) - 1, SessionID: "$9", WindowID: "@9"}},
			map[string]tmux.Labels{"%28": {Session: "seedtool-development-77ef6f5e",
				Window: "seedtool-development", Command: "sh"}},
			[]tmux.Capture{{PaneID: "%28", Height: 43, Lines: zone}},
			map[string]tmux.Capture{"%28": {PaneID: "%28", Height: 43, Lines: zone}},
			now, time.Second)
		r.SetClaudeSession("local", "%28", uuid)
	}

	for _, tc := range []struct {
		name  string
		first agents.Session
		hosts [2]string
		last  agents.Session
	}{
		{"the live host answers first", local, [2]string{"local", "side-desk"}, remote},
		{"the stale host answers first", remote, [2]string{"side-desk", "local"}, local},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := New()
			seed(r)
			r.UpdateAgents(tc.hosts[0], []agents.Session{tc.first}, now)
			r.UpdateAgents(tc.hosts[1], []agents.Session{tc.last}, now)

			rows := r.Panes()
			if len(rows) != 1 {
				t.Fatalf("one session with one pane produced %d rows", len(rows))
			}
			p := rows[0]
			if got := p.stateAt(now); got != state.Works {
				t.Errorf("the row reads %v, want works — a host that cannot see the process called it "+
					"%q and buried the live word (held word is %q)", got, remote.State, p.AgentWord)
			}
			if p.AgentWord == "failed" {
				t.Errorf("the row is carrying the word %q, so `K` and the wake dialog would tell the "+
					"operator the worker is gone while it is running", p.AgentWord)
			}
		})
	}

	// AND THE ROW STILL FOLLOWS A REAL FAILURE. When the session genuinely ends, every host says
	// `failed` in the NEXT round — the guard is scoped to one round precisely so nothing can get stuck.
	t.Run("a real failure still lands", func(t *testing.T) {
		r := New()
		seed(r)
		r.UpdateAgents("local", []agents.Session{local}, now)
		r.UpdateAgents("side-desk", []agents.Session{remote}, now)
		if got := r.Panes()[0].stateAt(now); got != state.Works {
			t.Fatalf("the first round reads %v, want works", got)
		}
		// The worker is gone: the owning machine no longer reports a pid either, so no claim has one
		// and the words decide — which is the case the `failed` clause is still there for.
		later := now.Add(20 * time.Second)
		ended := remote
		r.UpdateAgents("local", []agents.Session{ended}, later)
		r.UpdateAgents("side-desk", []agents.Session{remote}, later)
		if got := r.Panes()[0].stateAt(later); got != state.Error {
			t.Errorf("with every host reporting failed the row reads %v, want error — the guard has to "+
				"yield to agreement or a finished session would read as working forever", got)
		}
	})
}

// ANY word from a host that cannot see the worker is a claim about the STORE, not about the session —
// and `blocked` is the one that reached the operator.
//
// The first version of this rule refused only `failed`, which fixed the instance in front of it. The very
// next report was `billing-cicd`: the operator had just answered it, its own machine said `working` with a
// pid, and the pid-less host still had `blocked` from before the answer — so the row read `needs` while
// the session worked. Same shape, different word, and the fix that only knew one word could not see it.
//
// Measured on the live fleet at that moment: local `state=working status=busy pid=1060612`, `side-desk`
// `state=blocked pid=None`, and NOT ONE of that host's 31 records carried a pid.
func TestAnyWordFromAHostWithNoWorkerLosesToTheOwner(t *testing.T) {
	now := time.Unix(1787196000, 0)
	// A Claude pane doing work: its own spinner, and no "esc to interrupt" — this version prints
	// `(ctrl+b to run in background)` and `· Concocting…`, which is why the pixels cannot rescue the row
	// either. Captured from the operator's own screen.
	zone := []string{
		"     (ctrl+b to run in background)",
		"",
		"· Concocting… (4m 5s · ↓ 5.8k tokens)",
		"",
		"───────────────────────────────────────────── 20260818--cicd ─",
		"❯ ",
	}
	const uuid = "30f3382b-f68c-4baf-98fd-68d4fd1c3da4"
	owner := agents.Session{ID: "30f3382b", SessionID: uuid, Kind: "background", PID: 1060612,
		Name: "20260818--cicd", State: "working", StartedAt: now.Add(-30 * time.Minute)}
	sharer := agents.Session{ID: "30f3382b", SessionID: uuid, Kind: "background",
		Name: "20260818--cicd", State: "blocked", StartedAt: now.Add(-30 * time.Minute)}

	for _, order := range [][2]string{{"local", "side-desk"}, {"side-desk", "local"}} {
		t.Run(order[0]+" answers first", func(t *testing.T) {
			r := New()
			r.Update("local",
				[]tmux.Delta{{PaneID: "%31", Activity: now.Unix(), PaneHeight: 42, WindowWidth: 120,
					PanePID: 1060700, CursorY: len(zone) - 1, SessionID: "$8", WindowID: "@8", Alt: true}},
				map[string]tmux.Labels{"%31": {Session: "20260818--cicd-30f3382b",
					Window: "20260818--cicd", Command: "sh"}},
				[]tmux.Capture{{PaneID: "%31", Height: 42, Lines: zone}},
				map[string]tmux.Capture{"%31": {PaneID: "%31", Height: 42, Lines: zone}},
				now, time.Second)
			r.SetClaudeSession("local", "%31", uuid)
			byHost := map[string]agents.Session{"local": owner, "side-desk": sharer}
			for _, h := range order {
				r.UpdateAgents(h, []agents.Session{byHost[h]}, now)
			}
			p := r.Panes()[0]
			if got := p.stateAt(now); got != state.Works {
				t.Errorf("the row reads %v, want works — the host with no worker said %q and won "+
					"(held word %q, pid %d)", got, sharer.State, p.AgentWord, p.AgentPID)
			}
			if p.AgentPID != owner.PID {
				t.Errorf("the row kept the claim of a host that cannot see the worker: pid %d, want %d",
					p.AgentPID, owner.PID)
			}
		})
	}

	// AND `blocked` FROM THE OWNER STILL MEANS `needs`, which is the whole point of the word: the guard
	// is about WHO speaks, never about which word is convenient.
	t.Run("the owner saying blocked is believed", func(t *testing.T) {
		r := New()
		r.Update("local",
			[]tmux.Delta{{PaneID: "%31", Activity: now.Unix(), PaneHeight: 42, WindowWidth: 120,
				PanePID: 1060700, CursorY: len(zone) - 1, SessionID: "$8", WindowID: "@8", Alt: true}},
			map[string]tmux.Labels{"%31": {Session: "20260818--cicd-30f3382b",
				Window: "20260818--cicd", Command: "sh"}},
			[]tmux.Capture{{PaneID: "%31", Height: 42, Lines: zone}},
			map[string]tmux.Capture{"%31": {PaneID: "%31", Height: 42, Lines: zone}},
			now, time.Second)
		r.SetClaudeSession("local", "%31", uuid)
		blockedOwner := owner
		blockedOwner.State, blockedOwner.PID = "blocked", 1060612
		r.UpdateAgents("local", []agents.Session{blockedOwner}, now)
		r.UpdateAgents("side-desk", []agents.Session{sharer}, now)
		if got := r.Panes()[0].stateAt(now); got != state.Needs {
			t.Errorf("the row reads %v, want needs — the machine that runs the session said blocked", got)
		}
	})
}

// THE PROPERTY, OVER THE WHOLE VOCABULARY: while a claim with a pid exists in the round, NO word from a
// claim without one may change what the row says.
//
// Two instances of this class reached the operator on the same day, one word apart — `failed` burying
// `working` (the row read `error` on a session running a workflow) and `blocked` burying `working` (the row
// read `needs` on a session they had just answered). Two example-shaped tests would have caught the two
// words and left the other seven; this asserts the RULE across every pair the listing can produce, which
// is what makes the third instance impossible rather than merely unobserved.
//
// The vocabulary is `agents.Attention`'s own switch: blocked, working, running, busy, done, completed,
// stopped, idle, failed, error — plus a word it does not know, which must also not win.
func TestNoPidlessClaimCanMoveARowWhileTheOwnerSpeaks(t *testing.T) {
	words := []string{"blocked", "working", "running", "busy", "done", "completed", "stopped",
		"idle", "failed", "error", "something-a-future-version-invents"}
	now := time.Unix(1787200000, 0)
	const uuid = "c0ffee00-0000-0000-0000-00000000beef"
	zone := []string{"● output", "", "· Working… (1m)", "", "──────── sess ─", "❯ "}

	seed := func(r *Registry) {
		r.Update("local",
			[]tmux.Delta{{PaneID: "%7", Activity: now.Unix(), PaneHeight: 40, WindowWidth: 120,
				PanePID: 4242, CursorY: len(zone) - 1, SessionID: "$1", WindowID: "@1", Alt: true}},
			map[string]tmux.Labels{"%7": {Session: "sess-c0ffee00", Window: "sess", Command: "sh"}},
			[]tmux.Capture{{PaneID: "%7", Height: 40, Lines: zone}},
			map[string]tmux.Capture{"%7": {PaneID: "%7", Height: 40, Lines: zone}},
			now, time.Second)
		r.SetClaudeSession("local", "%7", uuid)
	}
	sess := func(word string, pid int) agents.Session {
		return agents.Session{ID: "c0ffee00", SessionID: uuid, Kind: "background", PID: pid,
			Name: "sess", State: word, StartedAt: now.Add(-time.Hour)}
	}

	checked := 0
	for _, ownerWord := range words {
		// What the row says with the OWNER alone is the answer every combination must reproduce.
		alone := New()
		seed(alone)
		alone.UpdateAgents("local", []agents.Session{sess(ownerWord, 4242)}, now)
		want := alone.Panes()[0].stateAt(now)

		for _, sharerWord := range words {
			for _, order := range [][2]string{{"local", "sharer"}, {"sharer", "local"}} {
				r := New()
				seed(r)
				byHost := map[string]agents.Session{
					"local":  sess(ownerWord, 4242),
					"sharer": sess(sharerWord, 0), // no pid: it cannot see the worker
				}
				for _, h := range order {
					r.UpdateAgents(h, []agents.Session{byHost[h]}, now)
				}
				checked++
				if got := r.Panes()[0].stateAt(now); got != want {
					t.Errorf("owner=%q sharer=%q order=%v: the row reads %v, want %v — a claim with no "+
						"pid changed the answer", ownerWord, sharerWord, order, got, want)
				}
			}
		}
	}
	// A FLOOR, because a loop that built nothing would pass having compared nothing.
	if checked != len(words)*len(words)*2 {
		t.Errorf("compared %d combinations, want %d", checked, len(words)*len(words)*2)
	}
	t.Logf("%d combinations compared over %d words, both poll orders", checked, len(words))
}
