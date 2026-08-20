package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exemptFiles are the files that legitimately NAME what the rest of the tree may
// not use: run.go defines the forbidden list and documents why, and the three test
// files exercise it.
//
// Keyed on the path from the scan root, never on the base name. A base name
// exempts every file that happens to share it: `internal/e2e/guard_test.go`
// already exists and was silently exempt from both bans, and any future `run.go`
// in any package would inherit the same pass. An exemption is a decision about
// ONE file.
var exemptFiles = map[string]bool{
	"internal/tmux/guard_test.go":       true,
	"internal/tmux/run_test.go":         true,
	"internal/tmux/run.go":              true,
	"internal/tmux/adversarial_test.go": true,
}

// rawDoorFiles are the production files allowed to call RunRaw, and it is a SECOND
// list on purpose: an exemption is a decision about one file AND ONE BAN. Task 6
// first took the whole-file exemption above for master.go, which would also have
// stopped scanning it for the forbidden formats that segfault a 3.2a server — a
// property that has nothing to do with the raw door.
//
// The master spawn is the legitimate use and the reason RunRaw exists unvalidated:
// `ssh -O check` and `ssh -N -M …` carry no tmux argv at all, so Validate would
// refuse them (internal/hub/master.go:52,62,83). run.go needs no entry here; it is
// covered by the whole-file exemption above, being the door's own definition.
var rawDoorFiles = map[string]bool{
	"internal/hub/master.go": true,
}

// scanTree reports every banned construct under root, one line per violation.
//
// It takes a root rather than walking the repo directly so it can be calibrated:
// a scan that only ever runs against a clean tree is indistinguishable from a
// scan that looks at nothing, and this repo has shipped that shape before.
func scanTree(root string) []string {
	var out []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if exemptFiles[filepath.ToSlash(rel)] {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s := string(b)
		for _, f := range forbiddenVars {
			// The violation is a FORMAT reference, not a mention: the name inside
			// #{...} is what would reach tmux.
			if strings.Contains(s, "#{"+f) {
				out = append(out, path+": #{"+f+"} segfaults a tmux 3.2a server")
			}
		}
		// The QUOTED verb, which is what it looks like when it can reach argv.
		// Scanning for the bare word fails on the tree as it stands: measured,
		// internal/ui/flows_test.go carries "link-window" twice in the prose that
		// explains why it is banned, and the doc comment on AttachWindow (Task 3)
		// carries it a third time. A ban whose first act is to fail on the
		// documentation of itself gets deleted, not obeyed.
		if strings.Contains(s, `"link-window"`) {
			out = append(out, path+": link-window in argv — kill-window on a linked "+
				"window kills the pane's process in every session (docs/design.md §20)")
		}
		// RunRaw is the raw-process door: it does NOT validate, because the ssh
		// master (`ssh -N -M`) is not a tmux command and Validate would refuse it.
		// Its rule was prose until this ban — and §5's own "the factor of two is
		// available and not taken" names the temptation exactly: merging a tick's
		// four invocations into one ssh command line is naturally written by
		// assembling an ssh argv by hand and handing it to RunRaw, which takes the
		// seam's whole safety property with it and goes green.
		//
		// Three scoping decisions, each of which the ban is wrong without:
		//
		//   - The CALL is banned, not the word: a doc comment or a test name saying
		//     RunRaw cannot become a call. Same discrimination as "link-window".
		//   - PRODUCTION files only. A test legitimately drives the raw door — the
		//     deadline is only testable through it, and the master supervisor's tests
		//     spawn `ssh -N -M` with it — and a test is not the write path. Measured,
		//     not assumed: this ban shipped tree-wide for one run and went red on
		//     `internal/hub/master_test.go`, which is exactly a correct use.
		//   - A production file that genuinely IS the raw door adds its path to
		//     exemptFiles, which is why the message says so: the bypass becomes a
		//     decision someone reviews rather than one nobody sees.
		if !strings.HasSuffix(path, "_test.go") && !rawDoorFiles[filepath.ToSlash(rel)] &&
			strings.Contains(s, "RunRaw(") {
			out = append(out, path+": RunRaw( bypasses Validate — a tmux argv must go "+
				"through Run or RunInput; if this file IS the raw-process door on purpose "+
				"(the master spawn), add its path to exemptFiles in internal/tmux/guard_test.go")
		}
		return nil
	})
	return out
}

func TestTheTreeNamesNoBannedConstruct(t *testing.T) {
	if v := scanTree(filepath.Join("..", "..")); len(v) != 0 {
		t.Errorf("banned constructs found:\n%s", strings.Join(v, "\n"))
	}
}

// The calibration the old test could not have: the scanner must go red on a
// planted violation, and it must not fire on a mention that cannot become argv.
func TestTheScannerCatchesAPlantedViolation(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("linker.go", "package x\nfunc f() { run(\"link-window\", \"-s\", \"@1\") }\n")
	write("crasher.go", "package x\nconst F = \"#{client_activity}\"\n")
	// The innocent file NAMES the ban in prose, unquoted — the shape the real tree
	// already has. A scan on the bare word fires here, which is the calibration
	// that matters; a file saying only "we never link windows" would not test it.
	write("innocent.go", "package x\n// a link-window'ed copy kills the agent; we never do it\n")

	// The exemption is one FILE, not one base name. Both of these are named
	// `guard_test.go` and carry the same violation: the exempt one is the path in
	// the list, the other is a namesake in another package — which the repo
	// already has. Against a base-name key the namesake is silently exempt and
	// this arm goes red.
	writeAt := func(dir2, name, body string) {
		if err := os.MkdirAll(filepath.Join(dir, dir2), 0o755); err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(dir2, name), body)
	}
	const bothBans = "package x\nconst F = \"#{client_activity}\"\nvar v = \"link-window\"\n"
	writeAt("internal/tmux", "guard_test.go", bothBans)
	writeAt("internal/e2e", "guard_test.go", bothBans)

	// The RunRaw ban, calibrated on both poles the same way. The caller is written the
	// way §5's "four invocations can become one" will actually be written — an ssh
	// argv assembled by hand — because that is the bypass the ban exists for.
	write("merger.go", "package x\nfunc f(r *execRunner) {\n"+
		"\tr.RunRaw(ctx, \"ssh\", \"-S\", cp, host, \"tmux list-panes; tmux capture-pane\")\n}\n")
	// Prose cannot become a call, and the real tree has exactly this shape: run.go's
	// own comment and three test lines name RunRaw without calling it.
	write("innocent_raw.go", "package x\n// RunRaw is the raw door; Run and RunInput are the ones that validate\n")
	// A TEST may drive the raw door — the deadline is only testable through it, and
	// the master supervisor's own tests spawn `ssh -N -M` with it. Byte-identical to
	// merger.go apart from the name, so this pole isolates the file's KIND and nothing
	// else. It is also the arm that would have caught the tree-wide version of this
	// ban, which went red on internal/hub/master_test.go.
	write("merger_test.go", "package x\nfunc f(r *execRunner) {\n"+
		"\tr.RunRaw(ctx, \"ssh\", \"-S\", cp, host, \"tmux list-panes; tmux capture-pane\")\n}\n")
	// And the door's own definition site must not fire, or the ban would forbid the
	// implementation it is protecting.
	writeAt("internal/tmux", "run.go", "package tmux\nfunc (r *execRunner) RunRaw() {}\nvar _ = r.RunRaw(ctx)\n")
	// The per-ban exemption, planted as a PRODUCTION file so it can only pass through
	// rawDoorFiles: the master spawn is not a tmux command and must reach the raw door.
	// Its format ban stays live, which is the difference from a whole-file exemption —
	// the arm below proves that by planting a forbidden format in it too.
	writeAt("internal/hub", "master.go", "package hub\nfunc f(r R) {\n"+
		"\tr.RunRaw(ctx, \"ssh\", \"-N\", \"-M\", \"-S\", cp, alias)\n}\n"+
		"const bad = \"#{client_activity}\"\n")

	v := scanTree(dir)
	// 2 planted at the root + both bans in the namesake + the hand-assembled RunRaw
	// call + the format inside the raw-door file, and nothing from either exempt path.
	// An exact count, because "at least one" cannot see an over-exemption.
	if len(v) != 6 {
		t.Fatalf("scanTree found %d violations, want 6:\n%s", len(v), strings.Join(v, "\n"))
	}
	joined := strings.Join(v, "\n")
	// Every assertion is anchored on the PLANTED path followed by the ":" that starts
	// the reason, because a violation line is `<path>: <reason>` and a reason may name
	// a file too: the RunRaw ban's own message points at internal/tmux/run.go, which
	// made the un-anchored exemption check read a message as a violation and fail
	// against a correct scanner. Measured, not imagined — it failed exactly that way.
	linesFor := func(rel ...string) []string {
		prefix := filepath.Join(append([]string{dir}, rel...)...) + ":"
		var out []string
		for _, line := range v {
			if strings.HasPrefix(line, prefix) {
				out = append(out, line)
			}
		}
		return out
	}
	for _, want := range [][]string{{"linker.go"}, {"crasher.go"}, {"merger.go"}, {"internal", "e2e", "guard_test.go"}} {
		if len(linesFor(want...)) == 0 {
			t.Errorf("scanTree missed %v:\n%s", want, joined)
		}
	}
	for _, unwanted := range [][]string{{"innocent.go"}, {"innocent_raw.go"}, {"merger_test.go"}} {
		if got := linesFor(unwanted...); len(got) != 0 {
			t.Errorf("scanTree fired on something that cannot become argv (%v): %q", unwanted, got)
		}
	}
	for _, exempt := range [][]string{{"internal", "tmux", "guard_test.go"}, {"internal", "tmux", "run.go"}} {
		if got := linesFor(exempt...); len(got) != 0 {
			t.Errorf("scanTree ignored its own exemption (%v): %q", exempt, got)
		}
	}
	// The per-ban exemption, and the reason it is worth a second map: master.go is
	// forgiven the raw door and still scanned for the format. One ban off, one on, in
	// the same file — which a whole-file exemption cannot express.
	master := linesFor("internal", "hub", "master.go")
	if len(master) != 1 {
		t.Fatalf("the raw-door file produced %d violations, want exactly the format one: %q", len(master), master)
	}
	if !strings.Contains(master[0], "client_activity") {
		t.Errorf("the format ban must still reach a raw-door file: %q", master[0])
	}
	if strings.Contains(master[0], "RunRaw(") {
		t.Errorf("the raw-door exemption did not apply: %q", master[0])
	}
}
