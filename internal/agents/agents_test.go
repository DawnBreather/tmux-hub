package agents

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The three shapes below are verbatim from real machines, not invented: the
// schema and the population moved twice in three patch releases (docs/design.md
// §17), so the parser is tested against what each version actually emits.

const v227 = `[
  {"id":"1ff133f7","cwd":"/home/dev/lab/streams/st/st-edgebox","kind":"background",
   "startedAt":1786205864163,"sessionId":"1ff133f7-c34a-4c60-91e5-b0048842cc66",
   "name":"dockerfile goldens across the fleet","state":"blocked"},
  {"id":"4ca5ffa9","cwd":"/home/dev/lab/streams/crater/erp-system","kind":"background",
   "startedAt":1786229184987,"sessionId":"4ca5ffa9-e6ed-45f2-aa6c-3dd4a76946d8",
   "name":"Access Miro board specification","state":"working"}
]`

// 2.1.224: interactive entries, a `status` field instead of `state`, and a pid.
const v224 = `[
  {"cwd":"/home/dev/lab/jira-tickets/OPSPROJ-496","kind":"interactive","pid":42277,
   "startedAt":1785766911922,"sessionId":"5a485bc4-4f01-4690-bbd4-29d42779a154",
   "name":"visualized-explanation","status":"working"},
  {"id":"84dc5a2e","cwd":"/home/dev/lab","kind":"background","pid":991,
   "startedAt":1785766911922,"sessionId":"84dc5a2e-ce23-44f0-a9d0-beb96b7f5f26",
   "name":"envoy hotfix","state":"done","status":"done"}
]`

func TestParseVersion227(t *testing.T) {
	got, err := Parse([]byte(v227))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2", len(got))
	}
	if got[0].State != "blocked" || got[0].Attention() != "needs" {
		t.Errorf("first = %+v, want blocked/needs", got[0])
	}
	if got[1].Attention() != "works" {
		t.Errorf("second attention = %q, want works", got[1].Attention())
	}
	if got[0].StartedAt.IsZero() {
		t.Error("startedAt was not parsed")
	}
	if got[0].PID != 0 {
		t.Errorf("this version reports no pid, got %d", got[0].PID)
	}
}

// The 2.1.224 shape is the one that would break a parser written against the
// newer schema: no `state`, no `id` on interactive entries, and a pid.
func TestParseVersion224(t *testing.T) {
	got, err := Parse([]byte(v224))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2", len(got))
	}
	inter := got[0]
	if inter.Kind != "interactive" {
		t.Errorf("kind = %q, want interactive", inter.Kind)
	}
	if inter.State != "working" || inter.Attention() != "works" {
		t.Errorf("status was not read as state: %+v", inter)
	}
	if inter.PID != 42277 {
		t.Errorf("pid = %d, want 42277 — it is the cheapest pane join available", inter.PID)
	}
	// NO back-fill: `ID` is the listing's own short id or nothing. 2.1.224 gives none for an
	// interactive session, and inventing one here put an 8-character look-alike in front of every
	// consumer — the report, the tile and `K`'s refusal each print it, and `claude attach
	// <manufactured>` answers `No job matching`. Measured on the live fleet: 57 background rows
	// with a real id, 8 interactive rows with an invented one.
	//
	// The registry's row key still needs a stable string and derives its own from the uuid; see
	// `agentRowID` and internal/registry's key tests. A key has to be stable, an id has to be
	// usable, and those are different jobs.
	if inter.ID != "" {
		t.Errorf("id = %q, want empty — this version reports none and a manufactured one is "+
			"refused by every verb that takes it", inter.ID)
	}
}

// A version that reports neither word is a real possibility, and unknown must
// stay unknown rather than defaulting to any state.
func TestUnknownStateStaysUnknown(t *testing.T) {
	got, err := Parse([]byte(`[{"sessionId":"abcdef12-0000-0000-0000-000000000000","kind":"background","name":"x"}]`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].State != "" || got[0].Attention() != "" {
		t.Errorf("want unknown, got state=%q attention=%q", got[0].State, got[0].Attention())
	}
}

func TestEmptyAndBlankListings(t *testing.T) {
	for _, in := range []string{"[]", "  ", "\n[]\n"} {
		got, err := Parse([]byte(in))
		if err != nil {
			t.Errorf("Parse(%q) = %v", in, err)
		}
		if len(got) != 0 {
			t.Errorf("Parse(%q) returned %d sessions", in, len(got))
		}
	}
}

func TestEntryWithoutASessionIDIsSkipped(t *testing.T) {
	got, err := Parse([]byte(`[{"kind":"background","name":"no id"},
	                           {"sessionId":"aaaaaaaa-0000-0000-0000-000000000000","name":"ok"}]`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("got %+v, want only the entry that can be keyed", got)
	}
}

func TestParseRejectsNonJSON(t *testing.T) {
	if _, err := Parse([]byte("command not found: claude")); err == nil {
		t.Fatal("want an error for non-JSON output")
	}
}

// Against the real CLI, if it is installed. This is the only test that can catch
// a schema change in a future version.
func TestFetchLocalAgainstTheRealCLI(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed")
	}
	got, err := Local(30 * time.Second).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	for _, s := range got {
		if s.SessionID == "" {
			t.Errorf("a session came back with no id: %+v", s)
		}
		if s.Kind == "" {
			t.Errorf("session %s has no kind", s.ID)
		}
	}
	t.Logf("the real CLI reported %d sessions", len(got))
}

func TestFetchHonoursItsDeadline(t *testing.T) {
	f := &cmdFetcher{name: "sleep", args: []string{"10"}, timeout: 300 * time.Millisecond}
	start := time.Now()
	_, err := f.Fetch(context.Background())
	if err == nil {
		t.Fatal("want a deadline error")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("deadline not enforced: took %v", d)
	}
}

// `--all` on BOTH fetchers, and the figures are per host because they are three different machines:
// locally bare `--json` returned 13 against `--all`'s 31, so it hid 18; on §17's third machine
// (2.1.226) bare returned 4 against 25, hiding 21. Every hidden row is terminal, and the `failed`
// ones are the half an operator can act on. On `nuc` — 2.1.224, the OLDEST version in the fleet —
// `--all` is accepted and changes that host's own answer in the same run, 11 against 14, so the flag
// is supported there rather than silently tolerated.
func TestBothFetchersAskForEverySession(t *testing.T) {
	local, ok := Local(time.Second).(*cmdFetcher)
	if !ok {
		t.Fatal("Local no longer returns a *cmdFetcher; rewrite this test, do not delete it")
	}
	if !slices.Contains(local.args, "--all") {
		t.Errorf("Local omits --all: %q — bare --json hides every terminal row", local.args)
	}

	remote, ok := OverSSH("/tmp/ctl", "host", time.Second).(*cmdFetcher)
	if !ok {
		t.Fatal("OverSSH no longer returns a *cmdFetcher; rewrite this test, do not delete it")
	}
	payload := strings.Join(remote.args, " ")
	if !strings.Contains(payload, "--all") {
		t.Errorf("OverSSH omits --all: %q", payload)
	}
}

// A CENSUS rather than a list of branches. Every word the fleet reports must survive the
// producer now that nothing is filtered, and the one that matters most is `failed`: all 4 rows
// carrying `reapedMidWorkAt` report it, in both the listing and the file, so losing that word
// loses the reaped-mid-work case §22.5 exists to announce.
func TestEveryReportedWordSurvivesTheProducer(t *testing.T) {
	words := []string{"done", "completed", "failed", "blocked", "working", "busy", "idle", ""}
	var b strings.Builder
	b.WriteString("[")
	for i, w := range words {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"sessionId":"aaaa%04d-bbbb-cccc-dddd-eeeeffff0000","kind":"background","state":%q}`, i, w)
	}
	b.WriteString("]")

	got, err := Parse([]byte(b.String()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != len(words) {
		t.Fatalf("%d rows in, %d out — the producer drops a word", len(words), len(got))
	}
	seen := map[string]bool{}
	for _, s := range got {
		seen[s.State] = true
	}
	for _, w := range words {
		if !seen[w] {
			t.Errorf("state %q did not survive Parse", w)
		}
	}
}

// The ssh payload must not convert a FAILING listing into an empty one. Shell semantics make this easy
// to get wrong: `command -v claude && claude agents … || echo '[]'` runs the `||` branch when the CALL
// fails, not only when claude is missing, so the shell exits 0 with `[]` and the host reads as "claude is
// installed and has no sessions". §22.6 requires the listing to be judged by its exit code alone, and
// this repository has already shipped one silent-empty defect that took every remote host dark.
//
// Asserted through a REAL shell rather than by reading the string, because the bug is in the shell's
// precedence rather than in the words.
func TestTheSSHPayloadDoesNotTurnAFailedListingIntoAnEmptyOne(t *testing.T) {
	f, ok := OverSSH("/tmp/ctl", "host", time.Second).(*cmdFetcher)
	if !ok {
		t.Fatal("OverSSH no longer returns a *cmdFetcher; rewrite this test, do not delete it")
	}
	payload := f.args[len(f.args)-1]

	// The idiom must be sh's problem, not the account's. ssh hands its command line to the login
	// shell, which is whatever the operator's account uses, so a brace group with `exit` is not
	// safe to write there — §20 makes the same ruling for the window path. All the login shell has
	// to parse is `sh -c '…'`.
	if !strings.HasPrefix(payload, "sh -c ") {
		t.Errorf("the payload asks the account's login shell to parse the idiom: %q", payload)
	}

	// sh's own directory is on BOTH cases' PATH: the payload invokes `sh` by name, so a PATH holding
	// only the stub makes the shell itself missing and the case then passes at exit 127 — on the
	// absence of a shell rather than on the failing listing, which would let the silent-empty bug
	// back in unnoticed. Measured: without shDir the first case exits 127 with
	// `sh: command not found`, and with it exits 1 with `error: unknown option '--all'`.
	shDir := "/bin"
	if p, err := exec.LookPath("sh"); err == nil {
		shDir = filepath.Dir(p)
	}

	dir := t.TempDir()
	// A claude that is present and FAILS, the shape a version rejecting --all produces.
	stub := filepath.Join(dir, "claude")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho \"error: unknown option '--all'\" >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", payload)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+shDir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Errorf("a failing `claude agents` exited 0 with %q — the host will read as empty rather than broken", out)
	}

	// And the property that must NOT regress: a host with no claude at all is not an error (§9).
	//
	empty := t.TempDir()
	cmd = exec.Command("sh", "-c", payload)
	cmd.Env = append(os.Environ(), "PATH="+empty+string(os.PathListSeparator)+shDir)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Errorf("a host without claude must not be an error: %v (%q)", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "[]" {
		t.Errorf("a host without claude must answer an empty listing, got %q", got)
	}
}
