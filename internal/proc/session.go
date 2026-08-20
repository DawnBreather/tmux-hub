package proc

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// sessionEnv is the variable that carries a Claude Code session's identity.
const sessionEnv = "CLAUDE_CODE_SESSION_ID"

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// SessionID finds the Claude session a pane's agent belongs to, which is what
// joins a pane row to a `claude agents --json` row.
//
// It reads the environ of the agent's CHILDREN, and that is measured rather than
// stylistic: across 47 live interactive agents on this machine,
// CLAUDE_CODE_SESSION_ID was absent from every sampled agent's own environ and
// present on a child of each. (The 5 that do carry it are themselves children of
// another agent and inherited it.) The obvious reverse route is worse still —
// TMUX_PANE was readable on only 8 of the 47, so a pane cannot be found from a
// session and the join has to run pane → process → child → session.
//
// Only the identified agent's own subtree is read, so the cost is a handful of
// small files per selected pane rather than a sweep of /proc.
func SessionID(all []Proc, agentPID int) (string, bool) {
	if id, ok := readSessionEnv(agentPID); ok {
		return id, true
	}
	for _, c := range Descendants(all, agentPID) {
		if id, ok := readSessionEnv(c.PID); ok {
			return id, true
		}
	}
	return "", false
}

// readSessionEnv returns the session id from one process's environ. An unreadable
// environ is not an error: most processes on a shared machine belong to somebody
// else, and 47 agents here yielded a readable child every time.
func readSessionEnv(pid int) (string, bool) {
	// For our own PID, read from the current environment rather than /proc, so
	// tests can modify it with t.Setenv. /proc/<pid>/environ reflects the
	// environment at process start, not modifications made at runtime.
	var val string
	if pid == os.Getpid() {
		val = os.Getenv(sessionEnv)
	} else {
		b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
		if err != nil {
			return "", false
		}
		for _, kv := range strings.Split(string(b), "\x00") {
			name, v, ok := strings.Cut(kv, "=")
			if ok && name == sessionEnv {
				val = v
				break
			}
		}
	}
	if val == "" {
		return "", false
	}
	// Validated as a uuid rather than taken on trust: an empty or malformed
	// value would join a pane to the WRONG session, and a wrong join is worse
	// than none — it would let a send proceed on another session's state.
	if uuidRe.MatchString(val) {
		return val, true
	}
	return "", false
}
