// Package launch specifies how to start a Claude Code session in a tmux pane.
//
// The hub GENERATES the session uuid and passes `claude --session-id <uuid>`,
// so the pane↔session binding is KNOWN at birth. This lets a hub-created agent
// skip the process-tree walk (where the project's one Critical defect lived).
package launch

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Spec describes a Claude Code launch.
type Spec struct {
	Host           string // which host to create on (label from hub.Host)
	CWD            string // working directory (must be absolute)
	Model          string // opus|sonnet|fable or full model name
	PermissionMode string // "default" means omit the flag; otherwise acceptEdits|auto|bypassPermissions|manual|dontAsk|plan
	NewSession     bool   // reserved for Task 12
	// SessionID is the `$N` a NEW WINDOW goes into, and it is the session the operator was looking
	// at when they pressed `n`. Empty means "any session on that host will do", which the launch
	// resolves by asking the host — because the alternative, a hard-coded `$0`, is only the first
	// session a server ever had: kill it and every launch into a new window fails.
	SessionID string
	// SessionName is what `tmux new-session -s` gets. Empty is a REAL name, not an absent one:
	// measured on 3.7b and 3.2a, `new-session -d -s ""` succeeds and the session's name is the
	// empty string, which draws as the pane id twice and cannot be attached to by name. Fill it
	// with SessionNameFor.
	SessionName string

	// Local says this launch targets THIS machine, so the CWD may be validated
	// against the local filesystem. NEVER inferred from Host being empty or any
	// other field — set explicitly by the caller that knows. See Host.LocalProc
	// in internal/hub/poll.go for the precedent: locality inference was the
	// project's one Critical defect (a forwarded socket walked against the local
	// process table; 97 of 3117 local pids falsely answered "agent here").
	Local bool

	// dirCheck is injectable for testing. If nil, uses defaultDirCheck.
	dirCheck dirCheckFunc
}

// Plan is a validated launch spec with a session ID assigned.
type Plan struct {
	SessionID string // the uuid the hub chose

	// Argv is the command as WORDS, which is what a pane is actually created with. The payload
	// builder needs the elements rather than the line: it wraps them in a login shell (a remote pane
	// inherits the ssh client's non-login PATH and cannot find `claude` otherwise) and refuses any
	// element a shell would reinterpret. Joining and re-splitting would put that decision in a
	// string.
	Argv []string

	// Command is the same words joined, for the surfaces that SHOW the launch: the history entry and
	// the note. It is not what runs — `ui.LoginPayload(Argv)` is — so it may be read but not sent.
	Command string
}

// Models is the set of known model aliases.
var Models = []string{"opus", "sonnet", "fable"}

// PermissionModes is the set of known permission modes.
// "default" is the hub's word for "omit the flag" — it is not a claude flag value.
var PermissionModes = []string{"default", "plan", "acceptEdits", "auto", "manual", "dontAsk", "bypassPermissions"}

// dirCheckFunc checks if a path exists and is a directory.
type dirCheckFunc func(path string) error

// defaultDirCheck checks if a path exists and is a directory.
func defaultDirCheck(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory: %s", path)
	}
	return nil
}

// SetDirCheck injects a custom directory check function. This is used by the
// UI layer to supply a remote check when ssh credentials are available.
func (s *Spec) SetDirCheck(check func(string) error) {
	s.dirCheck = check
}

// HasDirCheck reports whether a custom check has been injected.
func (s Spec) HasDirCheck() bool {
	return s.dirCheck != nil
}

// Validate checks that the spec is valid.
func (s Spec) Validate() error {
	// CWD must be non-empty and absolute.
	if s.CWD == "" {
		return errors.New("cwd must not be empty")
	}
	if !filepath.IsAbs(s.CWD) {
		return fmt.Errorf("cwd must be absolute, got %q", s.CWD)
	}

	// Directory check: local launches default to os.Stat, remote launches with
	// an injected check use it, and remote launches without credentials skip.
	// tmux new-window -c /nope returns rc=0 and creates the pane with cwd=$HOME,
	// no error. A typo'd directory would silently start an agent in the user's
	// HOME, outside any project.
	if s.Local {
		// Local launch: use the injected check, or default to os.Stat.
		check := s.dirCheck
		if check == nil {
			check = defaultDirCheck
		}
		if err := check(s.CWD); err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("directory does not exist: %s", s.CWD)
			}
			return fmt.Errorf("cwd is not a directory: %s", s.CWD)
		}
	} else if s.dirCheck != nil {
		// Remote launch with verification available: run the injected check.
		// This is the ssh master case — the UI layer built a check that asks
		// the remote host. When dirCheck is nil (no ssh credentials), we skip.
		if err := s.dirCheck(s.CWD); err != nil {
			return fmt.Errorf("remote directory check failed: %w", err)
		}
	}

	// Model validation: if set, must be a known alias or look like a full model name.
	if s.Model != "" {
		isKnownAlias := false
		for _, m := range Models {
			if s.Model == m {
				isKnownAlias = true
				break
			}
		}
		// A full model name contains a '-' (e.g., "claude-opus-5"). This catches
		// typo'd aliases like "opu" but NOT typo'd full names like "claude-opu-5" —
		// those are accepted here and only claude will reject them.
		looksLikeFullName := strings.Contains(s.Model, "-")
		if !isKnownAlias && !looksLikeFullName {
			return fmt.Errorf("unknown model %q; known aliases: %s, or use a full model name like claude-opus-5", s.Model, strings.Join(Models, ", "))
		}
	}

	// PermissionMode validation: if set and not "default", must be a known mode.
	if s.PermissionMode != "" && s.PermissionMode != "default" {
		known := false
		for _, pm := range PermissionModes {
			if s.PermissionMode == pm {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("unknown permission mode %q; known modes: %s", s.PermissionMode, strings.Join(PermissionModes, ", "))
		}
	}

	return nil
}

// Build assembles a Plan from a Spec.
// The id is the session uuid the hub chose.
// Build validates the spec first, so the safe path is the default.
func (s Spec) Build(id string) (Plan, error) {
	if err := s.Validate(); err != nil {
		return Plan{}, err
	}

	// The command as WORDS. Both forms come out of this one list, so the line the operator reads in
	// their history cannot say something different from the argv the pane was given.
	var parts []string
	parts = append(parts, "claude")
	parts = append(parts, "--session-id", id)

	if s.Model != "" {
		parts = append(parts, "--model", s.Model)
	}

	// "default" means omit the flag.
	if s.PermissionMode != "" && s.PermissionMode != "default" {
		parts = append(parts, "--permission-mode", s.PermissionMode)
	}

	return Plan{
		SessionID: id,
		Argv:      parts,
		Command:   strings.Join(parts, " "),
	}, nil
}

// NewSessionID generates a fresh v4 uuid for a session.
func NewSessionID() (string, error) {
	// Generate 16 random bytes.
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}

	// Set version (4) and variant (RFC 4122) bits.
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10

	// Format as a uuid string.
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// SessionNameFor is the session name a launch into a new session gets: the working directory's last
// segment, which is the same vocabulary the project list groups by, so the session lands under the
// name the operator already thinks of the work by.
//
// `.`, `:` and `%` are replaced because a name carrying one is a session nothing can address —
// the first two by tmux's own TARGET syntax, the third by this repo's seam. `%` is documented at
// the site that maps it. Measured:
// tmux accepts both in a name, and then `has-session -t my.app` answers `can't find pane: app` while
// `-t a:b` answers `can't find window: b` — so the session exists and nothing can address it. A
// newline is refused by tmux outright (`invalid session name`) and a directory may legally hold one.
// Spaces and non-ASCII are kept: measured, both are stored and addressable verbatim.
func SessionNameFor(cwd string) string {
	base := path.Base(strings.TrimRight(cwd, "/"))
	if base == "" || base == "/" || base == "." {
		return "tmux-hub"
	}
	return SessionNameFrom(base)
}

// SessionNameWithID is the name a session gets when the plain one is TAKEN: `<base>-<short id>`.
//
// It exists because `new-session -s <name>` is rc=1 `duplicate session: <name>` when the name is in
// use, and the launch had no answer for it — reported as "creating a new session does not work", and
// it was every time: a directory whose basename is already a session name can never be launched into
// twice. The operator's own server holds a session called `tmux-hub`, so the tmux-hub checkout was
// exactly that case.
//
// The suffix is the uuid the hub ALREADY generates for `claude --session-id`, shortened, so it is
// unique by construction and one retry always succeeds — no counter to bound and no name to probe
// for. It is also the shape §22.3's door uses (`<name>-<short id>`), and the door goes through this
// same function, so the two paths cannot drift into two conventions.
func SessionNameWithID(base, id string) string {
	id = ShortID(id)
	if id == "" {
		return SessionNameFrom(base)
	}
	if base == "" {
		return SessionNameFrom(id)
	}
	return SessionNameFrom(base + "-" + id)
}

// ShortID is how much of a uuid identifies a session to a person, and it is exported because a
// SECOND surface now needs the same answer: the dashboard prints it on a row whose label another row
// shares, and a pane-less row's only stable identity is its uuid. One truncation, so a name the door
// builds and an id a row shows cannot disagree about how much of a uuid a person reads.
func ShortID(id string) string {
	if len(id) > shortIDLen {
		return id[:shortIDLen]
	}
	return id
}

// shortIDLen is how much of a uuid identifies a session to a person, and it is what `claude agents`
// prints as the job id — so a name built with it matches what the vendor's own listing shows.
const shortIDLen = 8

// SessionNameFrom applies the addressability rule to any string a caller wants to name a session
// after — the door names one after an agent row (docs/design.md §22.3), so the rule has two callers
// and therefore belongs in one function. Everything the comment above says about `.`, `:` and
// newlines is enforced HERE; a second copy of that map is a second thing to keep in step.
func SessionNameFrom(raw string) string {
	out := strings.Map(func(r rune) rune {
		switch r {
		case '.', ':', '\n', '\r':
			return '-'
		case '%':
			// For a different reason from the three above, and the same consequence: tmux stores
			// it happily, and `internal/tmux`'s seam refuses a literal `%` anywhere but a `-t`
			// value, so the `new-session -s <name>` never runs. Measured, `make it 50% faster` →
			// `tmux: literal % outside a pane id: "50%"`. The door names a session after an agent
			// row and on this fleet an agent is named after the PROMPT that started it, so a
			// percentage in a prompt is the whole of the door refusing to open — the one key whose
			// failure has no remedy the operator could act on.
			return '-'
		}
		return r
	}, raw)
	if out == "" {
		return "tmux-hub"
	}
	return out
}
