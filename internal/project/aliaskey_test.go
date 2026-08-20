package project

import (
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/registry"
)

// A name the operator gave must survive every transition the PRODUCT performs on the row it was
// given to. Reported from real use: "сессии, которые мы раньше называли (алиас) — теперь не
// показывают имена", and measured against the operator's own projects.toml — six aliases, of which
// two rows rendered Claude's name instead of theirs.
//
// Both failures were the key, not the file. It carried the HOST and the row's KIND, and the product
// changes both by itself:
//
//	the door       makes a tmux session called `<name>-<short id>` and the join folds the
//	               pane-less row into that pane, so the row goes agent → pane
//	the dedup      attributes a session a shared `~/.claude` reports on two hosts to the
//	               fleet-first one, so the row's Host moves under it (§22.12)
//
// This is the same defect the favourites paid for one surface along, so the fix is the same shape:
// key on the uuid, which is what neither transition touches — and it is global, so a shared
// `~/.claude` names one session rather than one per host.
const (
	aliasUUID = "7ef2fe7e-c88d-46da-ac9d-0b57f170c3d8"
	aliasCWD  = "/home/dev/lab/streams/experiments/xmap-reverse-engineering"
	aliasName = "20260813--gis-offline-maps-universal-reader-emulator"
)

// theAgentRow is the row before the door: a pane-less listing row.
func theAgentRow(host string) registry.Pane {
	return registry.Pane{Kind: registry.KindAgent, Host: host, PaneID: "agent:7ef2fe7e",
		SessionID: aliasUUID, Session: aliasName, Path: aliasCWD, AgentID: "7ef2fe7e"}
}

// theAbsorbedPane is the SAME session after the door: a real tmux pane, named by tmux's own rule,
// which the join has told which conversation it is running.
//
// SessionID is `$3` and that is the point of including it: on a pane row that field is TMUX's session
// id, while on an agent row it is the Claude uuid. A reader that asked for SessionID without checking
// the kind would key this row on `$3`, which changes when the tmux server restarts — so the fixture
// carries both fields, and the wrong one is the one that looks right.
func theAbsorbedPane(host string) registry.Pane {
	return registry.Pane{Kind: registry.KindPane, Host: host, PaneID: "%5", SessionID: "$3",
		Session: aliasName + "-7ef2fe7e", ClaudeSession: aliasUUID, AgentName: aliasName,
		Path: aliasCWD}
}

func TestAnAliasSurvivesEveryTransitionTheProductPerforms(t *testing.T) {
	for _, c := range []struct {
		what string
		then registry.Pane
	}{
		{"the door made a pane and the join folded the row into it", theAbsorbedPane("local")},
		{"the dedup moved the row to the fleet-first host", theAgentRow("local")},
		{"both at once", theAbsorbedPane("local")},
	} {
		var as Aliases
		// Named while the row was a pane-less agent row on the host that reported it.
		as.Set(AliasKeyOf(theAgentRow("side-desk")), "xmap-universal-reader")

		got, own := as.DisplayName(c.then)
		if got != "xmap-universal-reader" || !own {
			t.Errorf("%s: the row reads %q (own=%v) — the operator's name came off, which on a "+
				"45-row fleet reads as the name never having been given", c.what, got, own)
		}
	}
}

// The CONTROL for dropping the host, and it is why the two shapes keep DIFFERENT rules: a tmux
// session name is only unique per host, so two hosts running `work` are two sessions and must be
// nameable apart. A uuid is global and a shared `~/.claude` is one store, so the same uuid on two
// hosts is ONE session — §22.12 measured that, and it is what the dedup acts on.
func TestAPaneWithNoUUIDStillBelongsToItsHost(t *testing.T) {
	here := registry.Pane{Kind: registry.KindPane, Host: "local", Session: "work", PaneID: "%0"}
	far := registry.Pane{Kind: registry.KindPane, Host: "nuc", Session: "work", PaneID: "%1"}

	var as Aliases
	as.Set(AliasKeyOf(here), "the local one")
	as.Set(AliasKeyOf(far), "the far one")

	if got, _ := as.DisplayName(here); got != "the local one" {
		t.Errorf("local `work` reads %q", got)
	}
	if got, _ := as.DisplayName(far); got != "the far one" {
		t.Errorf("nuc `work` reads %q — two hosts' sessions of one name were folded together", got)
	}
}

// The CONTROL for keeping the cwd, and it is the measured N4 collision: one `sessionId` on `nuc`
// carried two different sessions in different directories. Naming one must not name the other,
// because §21.12 gives a wrong alias no safety net — a wrongly named session stays selectable and
// writable under the wrong name.
func TestTwoSessionsUnderOneUUIDKeepTheirOwnNames(t *testing.T) {
	one := registry.Pane{Kind: registry.KindAgent, Host: "nuc", SessionID: aliasUUID, Path: "/a"}
	two := registry.Pane{Kind: registry.KindAgent, Host: "nuc", SessionID: aliasUUID, Path: "/b"}

	var as Aliases
	as.Set(AliasKeyOf(one), "the first")
	if got, own := as.DisplayName(two); own {
		t.Errorf("the second session under the same uuid reads %q — one name landed on both, "+
			"silently", got)
	}
	as.Set(AliasKeyOf(two), "the second")
	if got, _ := as.DisplayName(one); got != "the first" {
		t.Errorf("naming the second overwrote the first: %q", got)
	}
}

// The operator's OWN FILE must keep working, unchanged, because it is the file that carries the six
// names this defect took off. It was written when the key had a host, so every agent record in it
// names one — and the reader must ignore that field rather than refusing the record, since a refusal
// costs the operator every name they have typed.
//
// This fixture is the first record of ~/.config/tmux-hub/projects.toml, byte for byte.
func TestTheOperatorsExistingFileStillResolves(t *testing.T) {
	const file = `[[alias]]
host = "side-desk"
id = "7ef2fe7e-c88d-46da-ac9d-0b57f170c3d8"
cwd = "/home/dev/lab/streams/experiments/xmap-reverse-engineering"
name = "xmap-universal-reader"
`
	_, as, err := ParseAll(file)
	if err != nil {
		t.Fatalf("the operator's own file no longer parses: %v", err)
	}
	if as.Len() != 1 {
		t.Fatalf("%d aliases parsed from one record", as.Len())
	}
	// The row is now on `local`, and it is now a pane. Both are the product's own doing.
	for _, p := range []registry.Pane{theAgentRow("local"), theAbsorbedPane("local")} {
		if got, own := as.DisplayName(p); got != "xmap-universal-reader" || !own {
			t.Errorf("%s row reads %q (own=%v)", p.Kind, got, own)
		}
	}
}

// And the writer stops emitting the field the reader ignores: a record whose `host` is read by
// nobody is a lie the next reader of this file would believe. The first `N` rewrites the file, so
// the stale field leaves on its own.
func TestAnAgentAliasIsWrittenWithoutAHost(t *testing.T) {
	var as Aliases
	as.Set(AliasKeyOf(theAgentRow("side-desk")), "xmap-universal-reader")
	as.Set(AliasKeyOf(registry.Pane{Kind: registry.KindPane, Host: "local", Session: "work"}),
		"the shell")

	out := RenderAll(nil, as)
	// The pane record still needs its host, so counting `host =` proves the difference rather than
	// its absence: one record has one, the other has none.
	if n := strings.Count(out, "host ="); n != 1 {
		t.Errorf("%d `host =` lines in\n%s\nwant exactly one — the pane's", n, out)
	}
	_, back, err := ParseAll(out)
	if err != nil {
		t.Fatalf("what we wrote does not parse: %v\n%s", err, out)
	}
	if got, _ := back.DisplayName(theAbsorbedPane("local")); got != "xmap-universal-reader" {
		t.Errorf("the agent alias did not survive the round trip: %q", got)
	}
	if got, _ := back.DisplayName(
		registry.Pane{Kind: registry.KindPane, Host: "local", Session: "work"}); got != "the shell" {
		t.Errorf("the pane alias did not survive the round trip: %q", got)
	}
}
