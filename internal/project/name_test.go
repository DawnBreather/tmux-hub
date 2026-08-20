package project

import (
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/registry"
)

func at(host, path string) registry.Pane {
	return registry.Pane{Kind: registry.KindPane, Host: host, Path: path}
}

// Naming a PROJECT has to become a prefix RULE, and a rule needs a PREFIX — while a derived
// group is keyed on (host, last path segment), which is not one. §21.12 left this open for
// exactly that reason. The answer is the longest common ancestor of the group's own rows, and
// the safety is not an argument about it: the prospective rule is APPLIED to the whole fleet
// and refused unless it captures exactly the rows it was asked to name.
func TestNamingAGroupTakesTheAncestorOfItsOwnRows(t *testing.T) {
	// A group with several rows means several PANES IN ONE DIRECTORY — the derived group is
	// the last path segment, so `/x/st/one` and `/x/st/two` are two groups (`one` and `two`),
	// not one. My first fixture here made that mistake and the test measured it.
	fleet := []registry.Pane{
		at("local", "/home/dev/lab/streams/st"),
		at("local", "/home/dev/lab/streams/st"),
		// A SIBLING that must not be captured, and a row under the same name elsewhere, which
		// the host qualifier and the path boundary each have to keep out.
		at("local", "/home/dev/lab/streams/maps"),
		at("nuc", "/home/dev/lab/streams/st"),
	}
	var r Rules
	g := r.OfPane(fleet[0])
	rule, err := r.RuleToName(g, "the st work", fleet)
	if err != nil {
		t.Fatalf("RuleToName: %v", err)
	}
	if rule.Name != "the st work" {
		t.Errorf("Name = %q", rule.Name)
	}
	// The ancestor of the two rows, not one row's own path and not their parent's parent.
	if rule.Prefix != "/home/dev/lab/streams/st" {
		t.Errorf("Prefix = %q, want the two rows' common ancestor", rule.Prefix)
	}
	// Host-qualified, because a derived group's identity is (host, path).
	if rule.Host != "local" {
		t.Errorf("Host = %q, want the group's host", rule.Host)
	}
	// And applying it captures exactly those two rows.
	withRule, err := Parse(Render([]Rule{rule}))
	if err != nil {
		t.Fatal(err)
	}
	var named int
	for _, p := range fleet {
		if withRule.OfPane(p).Label == "the st work" {
			named++
		}
	}
	if named != 2 {
		t.Errorf("the rule captured %d rows, want the group's 2", named)
	}
	// The same directory on ANOTHER host stays out, because the rule is host-qualified.
	if withRule.OfPane(fleet[3]).Label == "the st work" {
		t.Error("the rule reached another host's identically named directory")
	}
	// And the sibling stays out, which is the path boundary doing its job.
	if withRule.OfPane(fleet[2]).Label == "the st work" {
		t.Error("the rule swallowed the sibling directory")
	}
}

// THE SAFETY, and it is a measurement rather than an argument: a group whose rows share no
// useful ancestor would need a prefix that swallows rows it was not asked to name, so naming it
// is REFUSED and the refusal says what would have been taken.
func TestNamingAGroupThatWouldSwallowOtherRowsIsRefused(t *testing.T) {
	// Two rows with the same last segment under different parents: the group is (local, st),
	// and their only common ancestor is /home/dev/lab, which also holds `other`.
	fleet := []registry.Pane{
		at("local", "/home/dev/lab/a/st"),
		at("local", "/home/dev/lab/b/st"),
		at("local", "/home/dev/lab/other"),
	}
	var r Rules
	g := r.OfPane(fleet[0])
	if g.Kind != Derived {
		t.Fatalf("kind = %v", g.Kind)
	}
	_, err := r.RuleToName(g, "st", fleet)
	if err == nil {
		t.Fatal("naming a group with no exclusive ancestor was allowed — the rule would have " +
			"swallowed a row nobody asked to name")
	}
	// The refusal has to name what it would have taken, or the operator cannot act on it.
	if !strings.Contains(err.Error(), "other") {
		t.Errorf("the refusal does not name what it would have swallowed: %v", err)
	}
}

// A group of ONE row is nameable: its own path is the prefix, and it captures nothing else.
func TestASingleRowGroupIsNameable(t *testing.T) {
	fleet := []registry.Pane{
		at("local", "/home/dev/lab/streams/st"),
		at("local", "/home/dev/lab/streams/maps"),
	}
	var r Rules
	rule, err := r.RuleToName(r.OfPane(fleet[0]), "st", fleet)
	if err != nil {
		t.Fatalf("RuleToName: %v", err)
	}
	if rule.Prefix != "/home/dev/lab/streams/st" {
		t.Errorf("Prefix = %q", rule.Prefix)
	}
	// And it must NOT capture the sibling, which the path-boundary rule is what guarantees.
	withRule, _ := Parse(Render([]Rule{rule}))
	if withRule.OfPane(fleet[1]).Label == "st" {
		t.Error("the rule swallowed a sibling directory")
	}
}

// A group that is ALREADY named came from a rule, so naming it again RENAMES that rule rather
// than adding a second one — two rules for one prefix is the tie Parse refuses outright.
func TestRenamingANamedGroupEditsItsOwnRule(t *testing.T) {
	r, err := Parse("[[project]]\nname = \"old\"\nprefix = \"/w/st\"\n")
	if err != nil {
		t.Fatal(err)
	}
	fleet := []registry.Pane{at("local", "/w/st/one")}
	rule, err := r.RuleToName(r.OfPane(fleet[0]), "new", fleet)
	if err != nil {
		t.Fatalf("RuleToName: %v", err)
	}
	if rule.Prefix != "/w/st" {
		t.Errorf("Prefix = %q, want the existing rule's prefix", rule.Prefix)
	}
	// Applying it must leave ONE rule, not two — and Parse would refuse two for one prefix.
	next := Replace(r.Rules(), rule)
	if len(next) != 1 {
		t.Fatalf("got %d rules, want 1: %#v", len(next), next)
	}
	if _, err := Parse(Render(next)); err != nil {
		t.Errorf("the rewritten file does not parse: %v", err)
	}
	after, _ := Parse(Render(next))
	if got := after.OfPane(fleet[0]).Label; got != "new" {
		t.Errorf("label = %q, want new", got)
	}
}

// The buckets are not directories and cannot become a rule, so naming one is refused with the
// reason rather than writing a prefix that means nothing.
func TestTheBucketsCannotBeNamed(t *testing.T) {
	var r Rules
	for _, g := range []Group{
		{ID: "u:", Kind: Unassigned, Label: "unassigned"},
		{ID: "p:", Kind: Pending, Label: "path not read yet"},
	} {
		if _, err := r.RuleToName(g, "whatever", nil); err == nil {
			t.Errorf("%v was nameable", g.Kind)
		}
	}
}

// An empty name would write a rule that names nothing; removing a project's name is deleting
// its rule, which Replace does when the name is blank.
func TestAnEmptyNameRemovesTheRule(t *testing.T) {
	r, err := Parse("[[project]]\nname = \"old\"\nprefix = \"/w/st\"\n")
	if err != nil {
		t.Fatal(err)
	}
	next := Replace(r.Rules(), Rule{Name: "", Prefix: "/w/st"})
	if len(next) != 0 {
		t.Errorf("got %d rules, want none: %#v", len(next), next)
	}
}
