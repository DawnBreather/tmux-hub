//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// The whole §21 chain against a real server: tmux's #{pane_current_path} -> the length-framed
// label format -> registry.Pane.Path -> project.Summarise -> the groups a screen draws.
//
// Every unit test for this hand-builds Pane.Path, so all of them would pass with the label
// unfetched and the field permanently empty — which is this repository's own recorded
// defect: #{pane_start_command} was added to the label table, nothing read the table, and
// registry.Pane.StartCommand was "" on every real poll while the suite stayed green.
func TestE2EAProjectIsDerivedFromRealPaneDirectories(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	base := t.TempDir()
	// Two projects, and one of them carries both hazards a directory name can hold: a `|`,
	// which the old line-cutting reader used as its delimiter, and a raw NEWLINE, which is
	// the only value tmux emits unescaped. If either reaches the grouping mangled, two
	// panes merge into one project or one goes missing.
	alpha := filepath.Join(base, "alpha")
	beta := filepath.Join(base, "be|ta\nsecond")
	for _, d := range []string{alpha, beta} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", d, err)
		}
	}

	sock := filepath.Join(base, "tmux.sock")
	must := func(args ...string) {
		t.Helper()
		full := append([]string{"-S", sock, "-f", "/dev/null"}, args...)
		if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })

	must("new-session", "-d", "-s", "one", "-c", alpha, "sleep", "300")
	must("split-window", "-t", "one", "-d", "-c", alpha, "sleep", "300")
	must("new-session", "-d", "-s", "two", "-c", beta, "sleep", "300")

	ctx := context.Background()
	r := tmux.NewExec(10 * time.Second)
	tgt := tmux.Target{Label: "test", Socket: sock}

	ds, err := tmux.FetchDeltas(ctx, r, tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	snap, err := tmux.FetchSnapshot(ctx, r, tgt, ds, nil)
	if err != nil {
		t.Fatalf("FetchSnapshot: %v", err)
	}
	reg := registry.New()
	reg.Update("test", ds, snap.Labels, snap.Zones, snap.Fulls, time.Now(), time.Second)
	rows := reg.Panes()
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}

	// The field is populated by the REAL poll, byte for byte.
	byPath := map[string]int{}
	for _, p := range rows {
		if p.Path == "" {
			t.Fatalf("pane %s has an empty Path after a real poll — the label is fetched "+
				"and not carried, which every unit test would still pass through", p.PaneID)
		}
		byPath[p.Path]++
	}
	if byPath[alpha] != 2 {
		t.Errorf("alpha holds %d panes, want 2: %v", byPath[alpha], byPath)
	}
	if byPath[beta] != 1 {
		t.Errorf("the hostile directory did not survive the wire: paths are %v\nwanted %q",
			byPath, beta)
	}

	// And the grouping the screen draws.
	got := project.Summarise(project.Rules{}, rows)
	if len(got) != 2 {
		var labels []string
		for _, s := range got {
			labels = append(labels, s.Group.Label)
		}
		t.Fatalf("got %d groups %v, want 2 — a mangled path splits or merges a project",
			len(got), labels)
	}
	sum := 0
	for _, s := range got {
		sum += s.Total
	}
	// §21.6's invariant, end to end: every row in exactly one group and the sum is the
	// population. Two screens one keystroke apart must not disagree about the total.
	if sum != len(rows) {
		t.Errorf("the groups hold %d rows and the fleet has %d", sum, len(rows))
	}
	// The hostile name reaches the LABEL too, which is what an operator reads.
	var labels []string
	for _, s := range got {
		labels = append(labels, s.Group.Label)
	}
	joined := strings.Join(labels, " / ")
	if !strings.Contains(joined, "alpha") {
		t.Errorf("labels %q do not include alpha", joined)
	}
	if !strings.Contains(joined, "second") {
		t.Errorf("labels %q lost the hostile directory's last segment", joined)
	}
}

// A rule from a real projects.toml, through the real reader, over real panes. The unit
// tests parse strings; this proves the file the README documents produces the grouping the
// README implies.
func TestE2EARealProjectsFileGroupsRealPanes(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	base := t.TempDir()
	// THE DIRECTION MATTERS, and my first version of this fixture had it backwards. The
	// rule must be on the SHORTER name (`st`) and the outsider must be the one that
	// EXTENDS it (`streams/st`) — a plain strings.HasPrefix answers "is /work/st a prefix
	// of /work/streams/st" with YES, which is the swallow this asserts against. With the
	// rule on `streams` instead, the string prefix is false anyway and the case cannot
	// fail in the dimension it claims. Verified by mutation: replacing the boundary test
	// with strings.HasPrefix now makes this test fail, and did not before.
	inRule := filepath.Join(base, "work", "st")
	outside := filepath.Join(base, "work", "streams", "st")
	for _, d := range []string{inRule, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// THE PRODUCTION PAIR, and the test used to miss it: it wrote with `project.Render` (the
	// `[[project]]` records alone) and read with `project.Load`, which refused any file carrying
	// an `[[alias]]` record — i.e. every file written after the operator presses `N` once. So a
	// case named for "a real projects.toml through the real reader" exercised a path the binary
	// never takes. It writes with `Save` and reads with `LoadAll` now, and the fixture carries an
	// ALIAS so the file has the shape the old reader choked on.
	file := filepath.Join(base, "projects.toml")
	named := project.Aliases{}
	named.Set(project.AliasKey{Kind: "pane", Host: "test", Session: "out"}, "the outsider")
	rules0 := []project.Rule{{Name: "the st work", Prefix: inRule}}
	if err := project.Save(file, rules0, named); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rules, aliases, err := project.LoadAll(file)
	if err != nil {
		body, _ := os.ReadFile(file)
		t.Fatalf("LoadAll: %v\n%s", err, body)
	}
	if aliases.Len() != 1 {
		t.Fatalf("the alias did not survive the file: %d stored", aliases.Len())
	}

	sock := filepath.Join(base, "tmux.sock")
	must := func(args ...string) {
		t.Helper()
		full := append([]string{"-S", sock, "-f", "/dev/null"}, args...)
		if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })
	must("new-session", "-d", "-s", "in", "-c", inRule, "sleep", "300")
	must("new-session", "-d", "-s", "out", "-c", outside, "sleep", "300")

	ctx := context.Background()
	r := tmux.NewExec(10 * time.Second)
	tgt := tmux.Target{Label: "test", Socket: sock}
	ds, err := tmux.FetchDeltas(ctx, r, tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	snap, err := tmux.FetchSnapshot(ctx, r, tgt, ds, nil)
	if err != nil {
		t.Fatalf("FetchSnapshot: %v", err)
	}
	reg := registry.New()
	reg.Update("test", ds, snap.Labels, snap.Zones, snap.Fulls, time.Now(), time.Second)

	byLabel := map[string]int{}
	for _, s := range project.Summarise(rules, reg.Panes()) {
		byLabel[s.Group.Label] = s.Total
	}
	if byLabel["the st work"] != 1 {
		t.Errorf("the named rule holds %d rows, want 1: %v", byLabel["the st work"], byLabel)
	}
	// `streams/st` must NOT have been swallowed by the `st` rule — it extends the prefix
	// as a STRING while sharing no path boundary with it — so it falls back to its own
	// last segment.
	if byLabel["st"] != 1 {
		t.Errorf("the boundary case did not stay out of the rule: %v — a plain string "+
			"prefix would have taken it", byLabel)
	}
}
