package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/registry"
)

// An alias names a SESSION, never a pane: the operator names the work, and the work outlives
// any one pane (docs/design.md §21.12). So the key is what identifies a session, and that
// differs by row KIND — which is a decision §21.14 left to the plan and this makes.
//
//	pane row   (host, tmux session name)
//	agent row  (host, Claude session id, cwd)
//
// The agent shape carries the cwd because the id alone is NOT unique: measured, one
// `sessionId` on `nuc` carried two different sessions in different directories (N4). An
// alias keyed on the id alone would land on both, silently, and §21.12 says a wrong alias
// has no safety net — §18's hide has one, since a wrongly hidden pane that starts waiting
// comes back, while a wrongly named session stays selectable and writable under the wrong
// name.
func TestAnAliasKeyIdentifiesTheSessionAndNotThePane(t *testing.T) {
	// Two panes of ONE tmux session get one key: the alias follows the session.
	a := registry.Pane{Kind: registry.KindPane, Host: "local", Session: "work", PaneID: "%0"}
	b := registry.Pane{Kind: registry.KindPane, Host: "local", Session: "work", PaneID: "%7"}
	if AliasKeyOf(a) != AliasKeyOf(b) {
		t.Errorf("two panes of one session got different keys: %+v vs %+v",
			AliasKeyOf(a), AliasKeyOf(b))
	}
	// A session of the same name on ANOTHER host is another session.
	far := a
	far.Host = "nuc"
	if AliasKeyOf(a) == AliasKeyOf(far) {
		t.Error("two hosts' sessions of the same name share a key")
	}
	// Two agent sessions sharing an id but not a cwd are two sessions — the measured N4
	// collision, and the reason the cwd is in the key.
	x := registry.Pane{Kind: registry.KindAgent, Host: "nuc",
		SessionID: "5a485bc4-4f01-4690-bbd4-29d42779a154", Path: "/a"}
	y := x
	y.Path = "/b"
	if AliasKeyOf(x) == AliasKeyOf(y) {
		t.Errorf("two sessions under one id share the key %+v — an alias would land on "+
			"both, silently", AliasKeyOf(x))
	}
	// And a pane row can never collide with an agent row, whatever their fields.
	clash := registry.Pane{Kind: registry.KindPane, Host: "nuc", Session: "5a485bc4"}
	if AliasKeyOf(clash) == AliasKeyOf(x) {
		t.Error("a pane row and an agent row produced the same key")
	}
}

// displayName is the ONE answer to "what is this row called", so no screen can show a
// different name from another (§21.12 rule 6). Precedence: the operator's alias, then
// Claude's own name, then the tmux session name.
func TestDisplayNamePrefersTheOperatorsOwnName(t *testing.T) {
	agent := registry.Pane{Kind: registry.KindAgent, Host: "nuc", SessionID: "id-1",
		Path: "/w", Session: "claude's own name"}
	pane := registry.Pane{Kind: registry.KindPane, Host: "local", Session: "tmux-name"}

	var none Aliases
	if got, own := none.DisplayName(agent); got != "claude's own name" || own {
		t.Errorf("with no alias, agent = (%q, own=%v), want Claude's name and own=false", got, own)
	}
	if got, own := none.DisplayName(pane); got != "tmux-name" || own {
		t.Errorf("with no alias, pane = (%q, own=%v), want the tmux name", got, own)
	}

	named := Aliases{}
	named.Set(AliasKeyOf(agent), "the DR plan")
	if got, own := named.DisplayName(agent); got != "the DR plan" || !own {
		t.Errorf("named agent = (%q, own=%v), want the alias and own=true", got, own)
	}
	// The flag is what lets a screen mark the name as the operator's own, and it must be
	// FALSE for a derived one — otherwise the marker stops meaning anything.
	if _, own := named.DisplayName(pane); own {
		t.Error("an unnamed row reported its name as the operator's own")
	}
}

// A row with nothing to call it must not read as empty: a nameless row cannot be spoken
// about, and the fallback has to be something the operator can match to a screen.
func TestDisplayNameNeverReturnsEmpty(t *testing.T) {
	var none Aliases
	for _, p := range []registry.Pane{
		{Kind: registry.KindPane, Host: "local", PaneID: "%3"},
		{Kind: registry.KindAgent, Host: "nuc", SessionID: "id-9"},
	} {
		if got, _ := none.DisplayName(p); strings.TrimSpace(got) == "" {
			t.Errorf("%v row has no name at all", p.Kind)
		}
	}
}

// Names are checked for duplicates FLEET-WIDE and CASE-FOLDED, at commit (§21.12 rule 4) —
// because two rows reading the same on one screen is exactly the confusion a name is meant
// to remove.
func TestADuplicateNameIsRefusedCaseFolded(t *testing.T) {
	a := registry.Pane{Kind: registry.KindPane, Host: "local", Session: "one"}
	b := registry.Pane{Kind: registry.KindPane, Host: "nuc", Session: "two"}
	as := Aliases{}
	as.Set(AliasKeyOf(a), "The DR Plan")

	for _, name := range []string{"The DR Plan", "the dr plan", "  THE DR PLAN  "} {
		if err := as.Check(AliasKeyOf(b), name); err == nil {
			t.Errorf("%q was accepted although %q exists — two rows would read the same",
				name, "The DR Plan")
		}
	}
	// The SAME row may keep its own name: renaming a row to what it already is is not a
	// duplicate, and refusing it would make `N`, enter a dead end.
	if err := as.Check(AliasKeyOf(a), "the dr plan"); err != nil {
		t.Errorf("a row was refused its own name: %v", err)
	}
	// And a distinct name is fine.
	if err := as.Check(AliasKeyOf(b), "something else"); err != nil {
		t.Errorf("a distinct name was refused: %v", err)
	}
}

// An empty name is how a name is REMOVED (§21.12 rule 5: N, ctrl+u, enter), so it must be
// accepted and must delete rather than store a blank.
func TestAnEmptyNameRemovesTheAlias(t *testing.T) {
	p := registry.Pane{Kind: registry.KindPane, Host: "local", Session: "one"}
	as := Aliases{}
	as.Set(AliasKeyOf(p), "named")
	if err := as.Check(AliasKeyOf(p), ""); err != nil {
		t.Fatalf("un-naming was refused: %v", err)
	}
	as.Set(AliasKeyOf(p), "")
	if got, own := as.DisplayName(p); own || got != "one" {
		t.Errorf("after un-naming: (%q, own=%v), want the tmux name and own=false", got, own)
	}
	if as.Len() != 0 {
		t.Errorf("un-naming left %d entries, want 0 — a blank alias stored is a row named "+
			"nothing", as.Len())
	}
}

// The store round-trips through its own file, in the one dialect, and a name a person typed
// survives it — which is the whole reason the dialect was fixed (N5).
func TestAliasesRoundTripThroughTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.toml")
	as := Aliases{}
	agent := registry.Pane{Kind: registry.KindAgent, Host: "nuc", SessionID: "id-1", Path: "/w/st"}
	pane := registry.Pane{Kind: registry.KindPane, Host: "local", Session: "wat\"ever"}
	as.Set(AliasKeyOf(agent), `say "yes"`)
	as.Set(AliasKeyOf(pane), "two\nlines")

	// Written beside the project rules, in ONE file — §21.11.3 puts both in projects.toml.
	rules, _ := Parse("[[project]]\nname = \"st\"\nprefix = \"/w/st\"\n")
	if err := Save(path, rules.Rules(), as); err != nil {
		t.Fatalf("Save: %v", err)
	}
	gotRules, gotAliases, err := LoadAll(path)
	if err != nil {
		t.Fatalf("LoadAll: %v\n%s", err, mustReadFile(t, path))
	}
	if len(gotRules.Rules()) != 1 || gotRules.Rules()[0].Name != "st" {
		t.Errorf("the project rules did not survive: %#v", gotRules.Rules())
	}
	if got, own := gotAliases.DisplayName(agent); got != `say "yes"` || !own {
		t.Errorf("agent alias = (%q, %v)\nfile:\n%s", got, own, mustReadFile(t, path))
	}
	// TWO PROPERTIES, and they are not the same one. The FILE must carry the value verbatim — that is
	// what this round trip is for, and a format that mangles an odd value would lose a name somebody
	// typed. The SCREEN must not: a name with a newline in it adds a ROW to the dashboard (measured,
	// 26 lines on a 24-row terminal), so DisplayName cuts at the first line and marks the loss. Check
	// refuses such a name at the write boundary now; this file was written before that existed, which
	// is exactly the case the read side has to survive.
	if raw := mustReadFile(t, path); !strings.Contains(raw, "two\\nlines") {
		t.Errorf("the file did not carry the two-line name verbatim:\n%s", raw)
	}
	if got, _ := gotAliases.DisplayName(pane); got != "two …" {
		t.Errorf("pane alias on screen = %q, want it cut at the first line and marked — a name that "+
			"draws two rows breaks the frame's one-row-per-row invariant", got)
	}
}

// A projects.toml holding only aliases, or only rules, must load — neither section is
// mandatory, and a first `N` writes the file into existence.
func TestEitherSectionMayBeAbsent(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct{ name, body string }{
		{"only rules", "[[project]]\nname = \"a\"\nprefix = \"/w\"\n"},
		{"only aliases", "[[alias]]\nhost = \"local\"\nsession = \"one\"\nname = \"n\"\n"},
		{"empty", ""},
	} {
		p := filepath.Join(dir, c.name+".toml")
		if err := os.WriteFile(p, []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadAll(p); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// An alias record the writer could never have produced is refused, naming the line — the
// file is hand-editable and that is where these errors come from.
func TestAMalformedAliasRecordIsRefusedNamingTheLine(t *testing.T) {
	for _, c := range []struct{ name, body string }{
		{"no name", "[[alias]]\nhost = \"local\"\nsession = \"one\"\n"},
		{"no host", "[[alias]]\nsession = \"one\"\nname = \"n\"\n"},
		{"neither session nor id", "[[alias]]\nhost = \"local\"\nname = \"n\"\n"},
		{"both session and id", "[[alias]]\nhost = \"h\"\nsession = \"s\"\nid = \"i\"\nname = \"n\"\n"},
		{"unknown key", "[[alias]]\nhost = \"h\"\nsession = \"s\"\nname = \"n\"\nwat = \"x\"\n"},
	} {
		_, _, err := ParseAll(c.body)
		if err == nil {
			t.Errorf("%s: parsed without error", c.name)
			continue
		}
		if !strings.Contains(err.Error(), "line") {
			t.Errorf("%s: error %q does not name a line", c.name, err)
		}
	}
}

func mustReadFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A NAME IS ONE LINE, refused on the way in and cut on the way out.
//
// Measured before this: `Check` accepted a name holding a newline, `DisplayName` returned it whole,
// and the dashboard drew TWENTY-SIX lines on a twenty-four-row terminal — the name's tail became a
// row of its own — which breaks the frame's one-screen-row-per-fleet-row invariant. A name is the one
// field on that row that comes from OUTSIDE the program: an operator can paste one, and projects.toml
// is a file they may edit by hand. A carriage return is worse than a newline, because it returns the
// cursor to column 0 and the row overwrites itself — the defect this repo already paid for in the
// footer's host reasons.
func TestANameIsOneLineOnTheWayInAndOnTheWayOut(t *testing.T) {
	var as Aliases
	k := AliasKey{Kind: "agent", ID: "aaaabbbb"}
	for _, bad := range []string{"two" + LF + "names", "carriage" + CR + "return", "a" + TAB + "b"} {
		if err := as.Check(k, bad); err == nil {
			t.Errorf("Check accepted %q, which draws more than one row", bad)
		} else if !strings.Contains(err.Error(), "one line") {
			t.Errorf("the refusal of %q does not say why: %v", bad, err)
		}
	}
	// An ordinary name with spaces and non-ASCII is untouched — the rule is about line breaks, not
	// about anything unfamiliar.
	for _, good := range []string{"прод выкатка", "two words", "50% done", "中文 名前"} {
		if err := as.Check(k, good); err != nil {
			t.Errorf("Check refused an ordinary name %q: %v", good, err)
		}
	}
	// THE READ SIDE, for a file written before the refusal existed: cut at the first line and marked,
	// because a name silently shortened cannot be matched against what the operator typed.
	as.Set(k, "two"+LF+"names")
	row := registry.Pane{Kind: registry.KindAgent, Host: "local", SessionID: "aaaabbbb",
		Session: "sess"}
	got, own := as.DisplayName(row)
	if strings.ContainsAny(got, LF+CR) {
		t.Errorf("DisplayName returned %q, which draws two rows", got)
	}
	if !own {
		t.Errorf("the name stopped being the operator's: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("DisplayName = %q, want the loss marked", got)
	}
}

// The three characters this rule is about, spelled out so the test source stays one line per line.
const (
	LF  = "\n"
	CR  = "\r"
	TAB = "\t"
)

// The write boundary and the read boundary refuse the SAME characters, through one predicate.
//
// They disagreed: `Check` refused a tab and every control character while `firstLine` cut only at a
// newline or a carriage return — so a tab in a hand-edited projects.toml, or one written by a build
// older than the refusal, reached the row and expanded to a variable width, misaligning the column the
// eye runs down. Two copies of one rule, and the older copy was the one on the path a file takes.
func TestTheWriteAndReadBoundariesRefuseTheSameCharacters(t *testing.T) {
	var as Aliases
	k := AliasKey{Kind: "agent", ID: "aaaabbbb"}
	row := registry.Pane{Kind: registry.KindAgent, Host: "local", SessionID: "aaaabbbb", Session: "sess"}

	for _, bad := range []string{"a" + TAB + "b", "a" + LF + "b", "a" + CR + "b", "a\x01b", "a\x1fb"} {
		if err := as.Check(k, bad); err == nil {
			t.Errorf("Check accepted %q", bad)
		}
		// The read side must not pass it through either: this is the file nobody validated.
		as.Set(k, bad)
		got, _ := as.DisplayName(row)
		if strings.ContainsAny(got, LF+CR+TAB) || strings.ContainsRune(got, 0x01) ||
			strings.ContainsRune(got, 0x1f) {
			t.Errorf("DisplayName returned %q for a stored %q — the character reached the row", got, bad)
		}
		if !strings.HasSuffix(got, "…") {
			t.Errorf("DisplayName = %q for a stored %q, want the loss marked", got, bad)
		}
	}
}
