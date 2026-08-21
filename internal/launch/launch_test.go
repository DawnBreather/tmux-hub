package launch

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

func TestBuildDictatesTheSessionID(t *testing.T) {
	// The whole point: the hub chooses the id, so pane↔session is KNOWN at
	// birth and no process-tree walk is needed (docs/design.md §19).
	p, err := Spec{CWD: "/srv/api", Model: "opus"}.Build("7007b23f-1599-4efa-81c5-4195621cc273")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Command, "--session-id 7007b23f-1599-4efa-81c5-4195621cc273") {
		t.Fatalf("command must dictate the session id: %q", p.Command)
	}
	if !strings.Contains(p.Command, "--model opus") {
		t.Fatalf("command must carry the model: %q", p.Command)
	}
}

func TestBuildOmitsWhatTheUserDidNotChoose(t *testing.T) {
	// An empty model must not become `--model ""`, which claude rejects.
	p, err := Spec{CWD: "/srv/api"}.Build("abc")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.Command, "--model") || strings.Contains(p.Command, "--permission-mode") {
		t.Fatalf("unset fields must be absent, not empty: %q", p.Command)
	}
}

func TestDefaultPermissionModeIsNotPassedAtAll(t *testing.T) {
	// "default" is the hub's word for "do not pass the flag". claude's own
	// choices are acceptEdits|auto|bypassPermissions|manual|dontAsk|plan —
	// `--permission-mode default` is not one of them and exits non-zero.
	p, _ := Spec{CWD: "/x", PermissionMode: "default"}.Build("abc")
	if strings.Contains(p.Command, "--permission-mode") {
		t.Fatalf("`default` must mean the flag is omitted: %q", p.Command)
	}
}

func TestValidateRefusesARelativeCWD(t *testing.T) {
	// tmux -c resolves relative to the SERVER's cwd, which on a remote host is
	// not anywhere the user is thinking about. Refuse it with the fix in the
	// message.
	err := Spec{CWD: "api"}.Validate()
	if err == nil {
		t.Fatal("a relative cwd must be refused")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("the error must carry its fix, got %q", err)
	}
}

func TestValidateRefusesAnEmptyCWD(t *testing.T) {
	err := Spec{CWD: ""}.Validate()
	if err == nil {
		t.Fatal("an empty cwd must be refused")
	}
	if !strings.Contains(err.Error(), "cwd") && !strings.Contains(err.Error(), "CWD") {
		t.Fatalf("the error must name the field, got %q", err)
	}
}

func TestValidateRefusesAModelClaudeDoesNotKnowUnlessItLooksLikeAFullName(t *testing.T) {
	// Aliases are a closed set (opus|sonnet|fable); a full model name is not,
	// so anything containing a '-' is passed through and claude judges it.
	// Inject a dirCheck that passes to isolate the model validation.
	passingCheck := func(path string) error { return nil }

	s1 := Spec{CWD: "/x", Model: "opu"}
	s1.dirCheck = passingCheck
	if err := s1.Validate(); err == nil {
		t.Fatal("a typo'd alias must be refused before a pane is created")
	}

	s2 := Spec{CWD: "/x", Model: "claude-opus-5"}
	s2.dirCheck = passingCheck
	if err := s2.Validate(); err != nil {
		t.Fatalf("a full model name must pass through: %v", err)
	}
}

func TestNewSessionIDIsAFreshUUIDEveryTime(t *testing.T) {
	// Reuse exits 1 with `Session ID <uuid> is already in use.` (measured), so
	// a duplicate here would surface as a pane that dies immediately.
	a, err := NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two launches must not share a session id")
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(a) {
		t.Fatalf("not a v4 uuid: %q", a)
	}
}

// Filesystem validation tests — required by the task message.
// For LOCAL launches, tmux new-window -c /nope silently creates the pane
// with cwd=$HOME, no error. So we must validate that the directory exists.

func TestValidateAcceptsAnExistingDirectory(t *testing.T) {
	// A local launch with an existing directory must pass.
	// We inject a fake checker that says "exists and is a directory".
	s := Spec{CWD: "/srv/api", Local: true}
	s.dirCheck = func(path string) error {
		if path != "/srv/api" {
			t.Fatalf("dirCheck called with wrong path: %q", path)
		}
		return nil // exists and is a directory
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("an existing directory must pass: %v", err)
	}
}

func TestValidateRefusesANonexistentDirectory(t *testing.T) {
	// A local launch with a nonexistent absolute path must be refused.
	s := Spec{CWD: "/does/not/exist", Local: true}
	s.dirCheck = func(path string) error {
		return os.ErrNotExist
	}
	err := s.Validate()
	if err == nil {
		t.Fatal("a nonexistent directory must be refused")
	}
	if !strings.Contains(err.Error(), "/does/not/exist") {
		t.Fatalf("the error must name the path, got %q", err)
	}
}

func TestValidateRefusesAFileNotADirectory(t *testing.T) {
	// A local launch where the path exists but is a FILE must be refused.
	s := Spec{CWD: "/etc/passwd", Local: true}
	s.dirCheck = func(path string) error {
		return errors.New("not a directory")
	}
	err := s.Validate()
	if err == nil {
		t.Fatal("a file (not directory) must be refused")
	}
	if !strings.Contains(err.Error(), "directory") && !strings.Contains(err.Error(), "/etc/passwd") {
		t.Fatalf("the error must say why, got %q", err)
	}
}

func TestValidateSkipsFilesystemCheckForNonLocal(t *testing.T) {
	// For a non-local launch WITHOUT an injected dirCheck, validation skips
	// the directory check entirely. This is the "no ssh credentials" case.
	s := Spec{Host: "remote.example.com", CWD: "/srv/api", Local: false}
	// Do NOT inject a dirCheck - leave it nil to prove it's skipped
	if err := s.Validate(); err != nil {
		t.Fatalf("non-local validation without dirCheck should pass: %v", err)
	}
}

func TestValidateRefusesNonexistentDirectoryOnlyWhenLocal(t *testing.T) {
	// A spec with Local: false and no injected dirCheck must skip validation.
	// This proves the check is skipped entirely, not just that it passes.
	s := Spec{CWD: "/does/not/exist", Local: false}
	// Do NOT inject a dirCheck - this is the "no ssh credentials" case
	if err := s.Validate(); err != nil {
		t.Fatalf("non-local spec without dirCheck should pass: %v", err)
	}
}

func TestValidateRunsInjectedCheckForNonLocal(t *testing.T) {
	// When a dirCheck IS injected for a non-local spec (the "has ssh credentials"
	// case), validation DOES run the check. This is Task 15's remote verification.
	s := Spec{Host: "remote.example.com", CWD: "/srv/api", Local: false}
	called := false
	s.dirCheck = func(path string) error {
		called = true
		return nil // accept
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("non-local validation with accepting dirCheck should pass: %v", err)
	}
	if !called {
		t.Fatal("injected dirCheck must be called for non-local spec")
	}
}

func TestValidateRejectsNonexistentRemoteDirectory(t *testing.T) {
	// When a remote dirCheck rejects, validation fails.
	s := Spec{Host: "remote.example.com", CWD: "/does/not/exist", Local: false}
	s.dirCheck = func(path string) error {
		return errors.New("remote check: no such directory")
	}
	if err := s.Validate(); err == nil {
		t.Fatal("validation should fail when remote dirCheck rejects")
	} else if !strings.Contains(err.Error(), "remote") {
		t.Fatalf("error should indicate remote check failed: %v", err)
	}
}

func TestBuildValidatesTheSpec(t *testing.T) {
	// Build must validate before building. A caller who forgets to call Validate()
	// should not get a well-formed command that starts an agent in $HOME.
	// Prove: Build on an invalid spec returns a non-nil error AND a zero Plan.
	s := Spec{CWD: "/does/not/exist", Local: true}
	s.dirCheck = func(path string) error {
		return os.ErrNotExist
	}
	plan, err := s.Build("test-uuid")
	if err == nil {
		t.Fatal("Build on invalid spec must return an error")
	}
	if !strings.Contains(err.Error(), "/does/not/exist") {
		t.Fatalf("error must name the invalid directory, got %q", err)
	}
	if plan.SessionID != "" || plan.Command != "" {
		t.Fatalf("Build on invalid spec must return a zero Plan, got %+v", plan)
	}
}

// A `%` in a name is replaced for the same consequence as `.` and `:` — a session nothing can
// address — and a different cause: tmux stores it, and this repo's own seam refuses a literal `%`
// anywhere but a `-t` value, so the `new-session` never runs.
//
// The door names a session after an agent row, and on this fleet an agent is named after the prompt
// that started it, so this is a percentage in a prompt stopping `a` from opening at all. Asserted
// through the SEAM rather than against a rune, because the seam is what refuses and a test that
// restated its rule would pass while the rule moved.
func TestAPercentIsReplacedBecauseTheSeamRefusesItInArgv(t *testing.T) {
	for _, c := range []struct{ raw, want string }{
		{"make it 50% faster", "make it 50- faster"},
		{"100%", "100-"},
		{"20260817-cicd", "20260817-cicd"},
	} {
		got := SessionNameFrom(c.raw)
		if got != c.want {
			t.Errorf("SessionNameFrom(%q) = %q, want %q", c.raw, got, c.want)
		}
		argv := []string{"new-session", "-d", "-s", got, "-c", "/w/x", "-P", "-F", "#{pane_id}"}
		if err := tmux.Validate(argv); err != nil {
			t.Errorf("the argv the door builds for %q is refused by the seam: %v", c.raw, err)
		}
	}
}

// SessionNameWithID is the name a session gets when the plain one is TAKEN, and its shape is the
// door's: `<base>-<short id>`.
//
// It exists because `new-session -s <name>` answers rc=1 `duplicate session: <name>`, and the launch
// had no second choice — so a directory whose basename was already a session name could never be
// launched into twice. Reported as "creating a new session does not work", and it was, every time.
func TestSessionNameWithIDIsTheDoorsShape(t *testing.T) {
	for _, c := range []struct{ base, id, want string }{
		// A full uuid is cut to the 8 characters `claude agents` prints as the job id, so the name
		// matches what the vendor's own listing shows.
		{"st", "aacb47c8-1234-4321-9999-000000000000", "st-aacb47c8"},
		// An ALREADY short id is idempotent, which is what lets the door pass its AgentID straight in.
		{"cicd", "30f3382b", "cicd-30f3382b"},
		// The sanitiser still applies: tmux stores `.` and `:` and then cannot address the session.
		{"a.b:c", "30f3382b", "a-b-c-30f3382b"},
		// Either half missing is the other half alone rather than a name with a dangling dash.
		{"", "30f3382b", "30f3382b"},
		{"st", "", "st"},
	} {
		if got := SessionNameWithID(c.base, c.id); got != c.want {
			t.Errorf("SessionNameWithID(%q, %q) = %q, want %q", c.base, c.id, got, c.want)
		}
	}
}

// disagreement reports how a Plan's two views of one command differ, or "" when they cannot.
//
// A function rather than four inline comparisons, because the pole below needs the same question asked
// of a Plan that Build did NOT make — a check whose only input is Build's output cannot distinguish
// "the views agree" from "there is one producer today".
func disagreement(p Plan) string {
	if joined := strings.Join(p.Argv, " "); p.Command != joined {
		return "Command is " + p.Command + ", the argv joined is " + joined
	}
	// The round trip is the half that is not arithmetic: a Command that splits back into a different
	// number of words is a line the operator reads as N arguments and the pane runs as M. It is also
	// where an element carrying whitespace shows up, which `Build` does not refuse for the one field it
	// does not constrain — the session id — and `ui.LoginPayload` does, naming the argument.
	fields := strings.Fields(p.Command)
	if len(fields) != len(p.Argv) {
		return "Command splits into " + strings.Join(fields, "|") +
			" but the argv is " + strings.Join(p.Argv, "|")
	}
	for i := range fields {
		if fields[i] != p.Argv[i] {
			return "word " + fields[i] + " is argv element " + p.Argv[i]
		}
	}
	return ""
}

// `Argv` is what a pane RUNS and `Command` is what the history log and the note SHOW, so a plan whose
// two views disagree is a hub that records something other than what it did. They come out of one list
// today; this pins that they must, over every flag combination the form can produce, because the form
// is where a fourth field would be added and the second producer is what would be forgotten.
func TestThePlansTwoViewsOfTheCommandCannotDisagree(t *testing.T) {
	for _, s := range []Spec{
		{CWD: "/srv/api"},
		{CWD: "/srv/api", Model: "opus"},
		{CWD: "/srv/api", PermissionMode: "bypassPermissions"},
		{CWD: "/srv/api", Model: "sonnet", PermissionMode: "plan"},
		{CWD: "/srv/api", Model: "claude-opus-5", PermissionMode: "default"},
		{CWD: "/srv/api", NewSession: true, SessionName: "api", Model: "fable"},
	} {
		p, err := s.Build("7007b23f-1599-4efa-81c5-4195621cc273")
		if err != nil {
			t.Fatalf("Build(%+v): %v", s, err)
		}
		if why := disagreement(p); why != "" {
			t.Errorf("the plan for %+v shows one command and runs another: %s", s, why)
		}
	}
	// The pole. Without it the check above passes against any Plan at all, since a single producer
	// makes agreement a property of the code rather than an assertion about it.
	hand := Plan{
		SessionID: "abc",
		Argv:      []string{"claude", "--session-id", "abc", "--model", "opus"},
		Command:   "claude --session-id abc",
	}
	if why := disagreement(hand); why == "" {
		t.Error("this test cannot fail: a Command missing two of its argv's words was called agreement")
	}
	// And the shape the round trip exists for: one element, two words on screen.
	split := Plan{Argv: []string{"claude", "--model", "my model"}, Command: "claude --model my model"}
	if why := disagreement(split); why == "" {
		t.Error("an argument holding a space read as one word, so the join is not a reversible view")
	}
}
