package project

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/conf"
	"github.com/DawnBreather/tmux-hub/internal/registry"
)

// With no rules at all every row groups by the LAST PATH SEGMENT. Measured on the 21-row
// fleet: grouping by git root gives 15 groups with 10 singletons — a grouping that does
// not pay for itself — while six prefix rules give 6 groups and leave 2 rows, and those
// two reach a group through this fallback (docs/design.md §21.1).
func TestWithNoRulesTheLastPathSegmentIsTheGroup(t *testing.T) {
	var r Rules
	for _, c := range []struct{ path, want string }{
		{"/home/dev/lab/streams/st", "st"},
		{"/home/dev/lab/streams/st/", "st"},
		{"/home/dev/.claude-mem/observer-sessions", "observer-sessions"},
		{"/", "/"},
		{"relative/dir", "dir"},
	} {
		g := r.Of("local", c.path)
		if g.Label != c.want {
			t.Errorf("Of(%q).Label = %q, want %q", c.path, g.Label, c.want)
		}
		if g.Kind != Derived {
			t.Errorf("Of(%q).Kind = %v, want Derived", c.path, g.Kind)
		}
	}
}

// The identity is (host, path). Measured: `/home/dev/.claude-mem/observer-sessions`
// exists on BOTH local and nuc and means different things there, which is the whole
// reason §21.1 host-qualifies. So two rows with the same basename on different hosts are
// two groups — merging across hosts is an explicit act in the file, never inferred.
//
// Note this differs from the §21 draft's arithmetic, which counted those two rows as one
// "7th group". That count and the identity rule cannot both hold; the rule has a
// measurement behind it and the count does not.
func TestTheSameDirectoryNameOnTwoHostsIsTwoGroups(t *testing.T) {
	var r Rules
	a := r.Of("local", "/home/dev/.claude-mem/observer-sessions")
	b := r.Of("nuc", "/home/dev/.claude-mem/observer-sessions")
	if a.ID == b.ID {
		t.Errorf("both hosts produced the id %q; they mean different things there", a.ID)
	}
	if a.Label != b.Label {
		t.Errorf("the LABELS should still both read %q — it is the id that separates "+
			"them, and the screen disambiguates (got %q and %q)",
			"observer-sessions", a.Label, b.Label)
	}
}

// A label collision must be visible on screen, which is a property of the SET rather than
// of one group — so it is computed once over the whole set (§21.13.2).
func TestLabelsDisambiguateOnlyWhereTheyCollide(t *testing.T) {
	var r Rules
	groups := []Group{
		r.Of("local", "/a/observer-sessions"),
		r.Of("nuc", "/b/observer-sessions"),
		r.Of("local", "/a/unique"),
	}
	got := Labels(groups)
	if got[groups[0].ID] == got[groups[1].ID] {
		t.Errorf("the two colliding groups still read the same: %q", got[groups[0].ID])
	}
	for _, id := range []string{groups[0].ID, groups[1].ID} {
		if got[id] == "observer-sessions" {
			t.Errorf("a colliding label was left bare: %q", got[id])
		}
	}
	// The one that does NOT collide must be left alone: qualifying every label would
	// spend columns on a distinction that is not there.
	if got[groups[2].ID] != "unique" {
		t.Errorf("a label with no collision was qualified: %q", got[groups[2].ID])
	}
}

// A prefix matches on a PATH BOUNDARY. `/home/dev/lab/st` must not match
// `/home/dev/lab/streams` — string prefixing would silently sweep a neighbouring
// directory into someone else's project (§21.13.1).
func TestAPrefixMatchesOnAPathBoundary(t *testing.T) {
	r, err := Parse("[[project]]\nname = \"st\"\nprefix = \"/home/dev/lab/st\"\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if g := r.Of("local", "/home/dev/lab/streams/st"); g.Kind == Named {
		t.Errorf("the rule for /home/dev/lab/st captured /home/dev/lab/streams/st as %q",
			g.Label)
	}
	// The prefix itself, and anything under it, do match.
	for _, p := range []string{"/home/dev/lab/st", "/home/dev/lab/st/", "/home/dev/lab/st/deep/er"} {
		g := r.Of("local", p)
		if g.Kind != Named || g.Label != "st" {
			t.Errorf("Of(%q) = %+v, want the named group st", p, g)
		}
	}
}

// Longest prefix wins, so a rule for a subtree can carve itself out of its parent.
func TestTheLongestMatchingPrefixWins(t *testing.T) {
	r, err := Parse(`[[project]]
name = "lab"
prefix = "/home/dev/lab"

[[project]]
name = "streams"
prefix = "/home/dev/lab/streams"
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if g := r.Of("local", "/home/dev/lab/streams/st"); g.Label != "streams" {
		t.Errorf("got %q, want streams — the longer prefix must win", g.Label)
	}
	if g := r.Of("local", "/home/dev/lab/other"); g.Label != "lab" {
		t.Errorf("got %q, want lab", g.Label)
	}
}

// A host-qualified rule beats an any-host rule of the same prefix: it is the more
// specific statement, and without that ordering the pair would be an unresolvable tie.
func TestAHostQualifiedRuleBeatsAnAnyHostRule(t *testing.T) {
	r, err := Parse(`[[project]]
name = "everywhere"
prefix = "/w"

[[project]]
name = "just nuc"
prefix = "/w"
host = "nuc"
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if g := r.Of("nuc", "/w/x"); g.Label != "just nuc" {
		t.Errorf("on nuc got %q, want the host-qualified rule", g.Label)
	}
	if g := r.Of("local", "/w/x"); g.Label != "everywhere" {
		t.Errorf("on local got %q, want the any-host rule", g.Label)
	}
}

// Two rules that could both match with the SAME prefix and the same host scope are an
// unresolvable tie, and it is refused at PARSE time naming both lines — not per frame,
// where the operator cannot see it and the renderer would have to guess.
//
// Two DIFFERENT prefixes of equal length can never both match one path, so "equal length
// is an error" reduces to "the same prefix twice", which is a static conflict.
func TestTheSamePrefixTwiceIsRefusedNamingBothLines(t *testing.T) {
	_, err := Parse(`[[project]]
name = "one"
prefix = "/w"

[[project]]
name = "two"
prefix = "/w"
`)
	if err == nil {
		t.Fatal("two rules for /w parsed without error; the tie has no answer")
	}
	// Lines 3 and 7 are the two `prefix =` lines — the ones to edit, not the records
	// that contain them.
	for _, want := range []string{"line 3", "line 7"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name line %s — the operator has to find both", err, want)
		}
	}
	// The same prefix under DIFFERENT host scopes is not a conflict: one is more
	// specific than the other and the precedence is decided.
	if _, err := Parse("[[project]]\nname = \"a\"\nprefix = \"/w\"\n\n[[project]]\nname = \"b\"\nprefix = \"/w\"\nhost = \"nuc\"\n"); err != nil {
		t.Errorf("the same prefix under two host scopes was refused: %v", err)
	}
}

// A pane whose path has not been read yet is PENDING, and an agent whose cwd is genuinely
// absent is UNASSIGNED. Both are the empty string in the row, so the row's KIND is what
// separates them — and the difference matters because §21.9 gives them different remedies:
// "the path was unreadable, named per host" against "write projects.toml".
func TestAnEmptyPathMeansPendingForAPaneAndUnassignedForAnAgent(t *testing.T) {
	var r Rules
	pane := registry.Pane{Kind: registry.KindPane, Host: "local"}
	if g := r.OfPane(pane); g.Kind != Pending {
		t.Errorf("a pane with no path yet = %v, want Pending — its label has not arrived", g.Kind)
	}
	agent := registry.Pane{Kind: registry.KindAgent, Host: "local"}
	if g := r.OfPane(agent); g.Kind != Unassigned {
		t.Errorf("an agent with no cwd = %v, want Unassigned — Claude reported none", g.Kind)
	}
	// And a row WITH a path goes through the ordinary derivation whatever its kind.
	agent.Path = "/home/dev/lab/streams/st"
	if g := r.OfPane(agent); g.Kind != Derived || g.Label != "st" {
		t.Errorf("an agent with a cwd = %+v, want the derived group st", g)
	}
}

// tmux appends ` (deleted)` to the path of a pane whose directory was removed, and its
// LENGTH matches, so the framing trusts it and the reader has to handle it (§21.7).
// Without this the group would be `st (deleted)` — a project of one row that appears the
// moment someone removes a directory.
func TestATrailingDeletedMarkerIsNotPartOfTheName(t *testing.T) {
	var r Rules
	g := r.Of("local", "/home/dev/lab/streams/st (deleted)")
	if g.Label != "st" {
		t.Errorf("Label = %q, want st", g.Label)
	}
	// It must group with the live pane of the same directory, not beside it.
	if live := r.Of("local", "/home/dev/lab/streams/st"); live.ID != g.ID {
		t.Errorf("the deleted pane got id %q and the live one %q", g.ID, live.ID)
	}
	// A directory whose name really ends that way is not the same thing, and there is
	// nothing to do about it: tmux gives the reader no way to tell them apart. Recorded
	// as a test so the choice is on record.
	if r.Of("local", "/a/b (deleted)").Label != "b" {
		t.Error("the stripping is unconditional, which is the documented trade")
	}
}

// The unassigned bucket has a REAL id, because §21.5's filter is
// `struct{on bool; group string}` and `enter` on that bucket must not render as `esc`.
func TestEveryKindHasAStableID(t *testing.T) {
	var r Rules
	seen := map[string]bool{}
	for _, g := range []Group{
		r.Of("local", ""),
		r.OfPane(registry.Pane{Kind: registry.KindPane, Host: "local"}),
		r.Of("local", "/a/b"),
	} {
		if g.ID == "" {
			t.Errorf("%v has an empty id", g.Kind)
		}
		if seen[g.ID] {
			t.Errorf("id %q is used by two kinds", g.ID)
		}
		seen[g.ID] = true
	}
}

// A name a person typed must survive being put in an id and read back out, because the
// filter compares ids for equality and a mangled id silently matches nothing.
func TestAnIDSurvivesANameAPersonTyped(t *testing.T) {
	for _, name := range []string{`say "yes"`, `C:\work`, "с кириллицей", "with a|pipe", "  "} {
		r, err := Parse("[[project]]\nname = " + quote(name) + "\nprefix = \"/w\"\n")
		if err != nil {
			t.Errorf("Parse(%q): %v", name, err)
			continue
		}
		g := r.Of("local", "/w/x")
		if g.Label != name {
			t.Errorf("Label = %q, want %q", g.Label, name)
		}
		if g.ID != r.Of("local", "/w/y").ID {
			t.Errorf("two rows under one rule got different ids")
		}
	}
}

// An absent file is an empty rule set and NOT an error: §21.9.2 is a specified screen —
// every row groups by its basename fallback and the remedy names the file.
func TestAnAbsentFileIsAnEmptyRuleSet(t *testing.T) {
	// LoadAll, the reader production uses: `Load` read only the `[[project]]` records and
	// refused any file carrying an `[[alias]]` one — i.e. every file written after the operator
	// has pressed `N` once — so a test through it asserted a path the binary never takes.
	r, _, err := LoadAll(filepath.Join(t.TempDir(), "projects.toml"))
	if err != nil {
		t.Fatalf("LoadAll of an absent file: %v", err)
	}
	if g := r.Of("local", "/a/b"); g.Kind != Derived {
		t.Errorf("with no file a row must still group: %+v", g)
	}
}

// An unparseable projects.toml must LOSE NAMES AND KEEP THE FLEET — the opposite of
// hosts.toml, which must stop the program. That asymmetry is §21.11.3's whole reason for
// two files, so it is asserted here rather than left to the caller's discretion.
func TestAnUnparseableFileYieldsAWarningAndAWorkingFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.toml")
	if err := os.WriteFile(path, []byte("[[project]]\nname = nonsense\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _, err := LoadAll(path)
	if err == nil {
		t.Fatal("a malformed file parsed without error; the operator must be told")
	}
	if g := r.Of("local", "/a/b"); g.Kind != Derived || g.Label != "b" {
		t.Errorf("the returned rules do not fall back cleanly: %+v — an unparseable "+
			"projects.toml must lose names and keep the fleet", g)
	}
}

// A rule with no name, or no prefix, is refused: a nameless group cannot be shown and a
// prefixless one would match everything.
func TestARuleMissingAFieldIsRefused(t *testing.T) {
	for _, body := range []string{
		"[[project]]\nprefix = \"/w\"\n",
		"[[project]]\nname = \"n\"\n",
		"[[project]]\nname = \"\"\nprefix = \"/w\"\n",
		"[[project]]\nname = \"n\"\nprefix = \"\"\n",
		"[[project]]\nname = \"n\"\nprefix = \"/w\"\nwat = \"x\"\n",
	} {
		if _, err := Parse(body); err == nil {
			t.Errorf("parsed without error: %q", body)
		}
	}
}

func TestRoundTripThroughTheWriter(t *testing.T) {
	want := []Rule{
		{Name: `say "yes"`, Prefix: "/home/dev/lab/streams"},
		{Name: "nuc only", Prefix: "/w", Host: "nuc"},
	}
	r, err := Parse(Render(want))
	if err != nil {
		t.Fatalf("Parse(Render(...)): %v\n%s", err, Render(want))
	}
	if !reflect.DeepEqual(r.Rules(), want) {
		t.Errorf("round trip changed the rules\n got: %#v\nwant: %#v", r.Rules(), want)
	}
}

// quote goes through the dialect's own writer, so a fixture cannot disagree with it.
func quote(v string) string { return conf.Quote(v) }

// Two directories that differ ONLY by a trailing space are two directories, and mkdir will
// make both. Collapsing them into one group is precisely the silent merge §21.7 refused
// `#{=N:pane_current_path}` to avoid — so the derivation must not do by TrimSpace what the
// wire format was chosen not to do.
func TestTwoDirectoriesDifferingOnlyByWhitespaceStayApart(t *testing.T) {
	var r Rules
	for _, pair := range [][2]string{
		{"/w/st", "/w/st "},
		{"/w/st", "/w/st\t"},
		{"/w/a b", "/w/a b "},
	} {
		x, y := r.Of("local", pair[0]), r.Of("local", pair[1])
		if x.ID == y.ID {
			t.Errorf("%q and %q share the group id %q — two real directories merged into "+
				"one project, which is the failure the wire format was chosen to prevent",
				pair[0], pair[1], x.ID)
		}
	}
	// A directory whose name is only spaces is still a directory, so it is NOT Pending.
	if g := r.Of("local", "  "); g.Kind != Derived {
		t.Errorf(`Of("  ") = %v, want Derived — mkdir "  " succeeds`, g.Kind)
	}
	// And a PANE row with such a path must not be read as "the label has not arrived".
	if g := r.OfPane(registry.Pane{Kind: registry.KindPane, Host: "local", Path: "  "}); g.Kind == Pending {
		t.Error(`a pane in a directory named "  " was read as having no path yet`)
	}
	// The trailing-slash and (deleted) rules still hold, and they are about tmux's own
	// output rather than about whitespace.
	if r.Of("local", "/w/st/").ID != r.Of("local", "/w/st").ID {
		t.Error("a trailing slash split a group")
	}
	if r.Of("local", "/w/st (deleted)").ID != r.Of("local", "/w/st").ID {
		t.Error("the deleted marker split a group")
	}
}
