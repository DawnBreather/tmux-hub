// Package agents reads Claude Code's own account of its sessions.
//
// It exists because the hub was blind to most of them. `claude agents --json`
// reports state as a FACT — blocked, working, done, failed — where §6 derives it
// from pixels, and the sessions it lists mostly have no tmux pane at all: across
// three machines nine were `blocked`, i.e. waiting for the user, and the
// dashboard could show none of them (docs/design.md §17).
//
// The schema is version-dependent and moved twice in three patch releases, so
// everything here is written to tolerate rather than to assume. Measured:
//
//	2.1.227  background only, `state`, no pid
//	2.1.226  background only, `state`, no pid
//	2.1.224  background AND interactive, `status`, with pid
package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Session is one Claude Code session as its own CLI reports it.
type Session struct {
	ID        string // short id; absent on some versions
	SessionID string // the full uuid, always present
	Kind      string // "background" | "interactive" | whatever a future version says
	Name      string
	CWD       string
	StartedAt time.Time
	State     string // normalised from `state` or `status`; "" when neither is given
	PID       int    // 0 when the version does not report one

	// Status is the listing's `status` field kept RAW, and it is not a duplicate of State.
	//
	// This field was read as the older half of a version pair — `state` on 2.1.226+, `status` on
	// 2.1.224, the same fact under two names — and that is no longer what the fleet reports. Measured
	// on 34 live records: 5 carry `state=working` WITH `status=busy`, and one carries `state=working`
	// with `status=idle`. So they arrive together and they are not the same question: `state` is the
	// session's lifecycle word and `status` is whether its worker is presently doing anything.
	//
	// The one disagreeing record was the session the operator reported as "not working, shown as
	// works". Keeping the raw value is what lets Attention refine on it and what lets `--status`
	// publish the premise its answer came from.
	Status string
}

// Attention maps Claude's own word to what the hub sorts by. An unknown or
// missing word maps to "", which the caller must treat as unknown rather than as
// any particular state — a version that reports neither `state` nor `status` is
// a real, measured case.
func (s Session) Attention() string {
	// The vocabulary is a VERSION PAIR, and only half of it was here. 2.1.226+ reports
	// `working`/`done` in `state`; 2.1.224 reports `busy`/`idle` in `status`, and where a version
	// sends only one of the two the constructor above folds it into `State` — so both halves arrive
	// through this switch and only one was named.
	//
	// A version that sends BOTH is a different case, and reading it as the pair is what made a parked
	// session read `works`: there `status` is not another name for `state`, it says whether the worker
	// is presently occupied. That refinement is in the `working` branch, with its measurement.
	//
	// Measured over 21 real sessions on two hosts: blocked 5, idle 7, working 3,
	// busy 2, done 2, neither 2. Without `busy` and `idle` this returned "" for ELEVEN
	// of the 21, and the product's own JSON showed `kind=agent state=unknown` for 7 of
	// 11 agent rows on a live run — rows that render `?` on the dashboard. A test that
	// fed only `working` and `done` could not see it, which is why the test beside this
	// is a CENSUS of every word the fleet reports rather than a list of branches.
	switch strings.ToLower(s.State) {
	case "blocked":
		return "needs"
	case "working", "running", "busy":
		// `status` REFINES this, it does not repeat it. Measured on 34 live records: `working` comes
		// with `busy` five times and with `idle` once — and that once was the session the operator
		// reported as not working while the row read `works`. Its pid pointed at a `claude bg-spare`,
		// a pre-warmed process parked on a claim socket, so the pid says this host can SEE a process
		// and never that the process is doing anything.
		//
		// Narrow on purpose. Only `working` is demoted, and only to `idle` — a LIVE session with a
		// prompt waiting, which is exactly what a parked worker is. `blocked` keeps `needs` even
		// beside `status=idle` (1 of 34 records), because burying a session that is waiting for the
		// operator is the one direction this repo refuses; `done` keeps `done` (2 of 34) because a
		// finished job is more specific than an unoccupied one.
		//
		// On the version that reports only `status`, State was filled FROM it, so `idle` there took
		// the `idle` case below and never reaches this one.
		if strings.EqualFold(s.Status, "idle") {
			return "idle"
		}
		return "works"
	case "done", "completed", "stopped":
		// NOT "idle". All three mean the job ENDED, and idle in the hub's vocabulary means a
		// LIVE session with a prompt waiting — so the fold printed the same row for a finished
		// background job and a session asking to be typed into, and gave them one rank.
		//
		// `stopped` is the operator's own `claude stop`, measured on the live fleet 2026-08-17
		// as 1 of 28 records. It was in no branch at all until then, so it fell to `default`
		// and drew `? unknown`, which tells the operator the hub does not know rather than that
		// the job is over. The REASON is not lost by folding: registry.Pane.AgentWord keeps the
		// listing's word unfolded, which is what the wake dialog and `K` read.
		return "done"
	case "idle":
		// 2.1.224's word for a LIVE interactive session, measured on both versions.
		return "idle"
	case "failed", "error":
		return "error"
	default:
		return ""
	}
}

// raw mirrors every field shape seen across versions. Numbers arrive as JSON
// numbers, so json.Number keeps both integer millis and any future string form
// parseable.
type raw struct {
	ID        string      `json:"id"`
	SessionID string      `json:"sessionId"`
	Kind      string      `json:"kind"`
	Name      string      `json:"name"`
	CWD       string      `json:"cwd"`
	StartedAt json.Number `json:"startedAt"`
	State     string      `json:"state"`  // 2.1.226+
	Status    string      `json:"status"` // 2.1.224
	PID       json.Number `json:"pid"`
}

// Parse reads the listing. An empty array is valid and common — a machine with
// no live sessions answers `[]` with rc 0.
func Parse(b []byte) ([]Session, error) {
	txt := strings.TrimSpace(string(b))
	if txt == "" {
		return nil, nil
	}
	var rs []raw
	if err := json.Unmarshal([]byte(txt), &rs); err != nil {
		return nil, fmt.Errorf("agents listing: %w", err)
	}
	out := make([]Session, 0, len(rs))
	for _, r := range rs {
		if r.SessionID == "" {
			// Without a session id there is nothing to key on, and every measured
			// version supplies one. Skipping is safer than inventing a key.
			continue
		}
		s := Session{ID: r.ID, SessionID: r.SessionID, Kind: r.Kind,
			Name: r.Name, CWD: r.CWD}
		// NO back-fill. `ID` is the listing's own short id or nothing, because it is the only
		// string `claude attach|logs|stop` accepts — a manufactured one answers `No job
		// matching`. Filling it here put an 8-character look-alike in front of every consumer:
		// measured on the live fleet, 57 background rows carried a real id and 8 interactive
		// rows carried an invented one, and the report, the tile and `K`'s refusal would each
		// have handed the operator a command that fails.
		//
		// The back-fill existed only so the registry's row KEY had a stable string. That is the
		// key builder's business and it does it there (`agentRowID`).
		// `state` on 2.1.226+, `status` on 2.1.224. Prefer whichever is there — and keep the raw
		// `status` as well, because a version that sends BOTH is sending two different facts (see
		// Session.Status). The fallback stays for the version that sends only one.
		s.State = r.State
		s.Status = r.Status
		if s.State == "" {
			s.State = r.Status
		}
		if ms, err := strconv.ParseInt(r.StartedAt.String(), 10, 64); err == nil && ms > 0 {
			s.StartedAt = time.UnixMilli(ms)
		}
		if pid, err := strconv.Atoi(r.PID.String()); err == nil {
			s.PID = pid
		}
		out = append(out, s)
	}
	return out, nil
}

// Fetcher runs the listing somewhere. Local and remote are the same command with
// a different prefix, which is the same shape §5 uses for tmux.
type Fetcher interface {
	Fetch(ctx context.Context) ([]Session, error)
}

type cmdFetcher struct {
	name    string
	args    []string
	timeout time.Duration
}

// Local lists sessions on this machine.
//
// `--all` is not optional: bare `--json` omits every terminal row, so a background job that
// FAILED is invisible. Measured on one snapshot across three hosts — 65 rows against a bare
// call's 28 — and accepted on 2.1.224, the oldest version in the fleet (docs/design.md §22.6).
func Local(timeout time.Duration) Fetcher {
	return &cmdFetcher{name: "claude", args: []string{"agents", "--json", "--all"}, timeout: timeout}
}

// OverSSH lists sessions on a remote host through an existing ControlMaster.
// Measured cost: 0.5 s to 2.8 s depending on the host, so this belongs on a slow
// sweep rather than in the tick.
func OverSSH(controlPath, dest string, timeout time.Duration) Fetcher {
	return &cmdFetcher{name: "ssh", timeout: timeout, args: []string{
		"-S", controlPath, dest,
		// Two properties, and the obvious one-liner only has the first. A host
		// WITHOUT claude must not be an error — §9's positive-probe rule, and the
		// reason the absent case answers an empty listing at rc=0. But a host WHERE
		// claude is installed and the call FAILS must be an error, because §22.6
		// judges this listing by its exit code alone.
		//
		// `A && B || C` gives only the first: the `||` branch runs when B fails too,
		// so a version rejecting `--all` printed its error to stderr and the shell
		// still exited 0 with `[]` — the host read as "claude is here and has no
		// sessions" instead of "the call is broken", which is the silent-empty shape
		// that has taken every remote host dark once already in this project.
		//
		// Separated: absent claude short-circuits with its own exit 0, and otherwise
		// the listing's exit code IS the shell's.
		// Delegated to `sh -c` rather than appended to the payload, for the reason
		// §20 already gives for the window path: ssh hands its command line to the
		// ACCOUNT's login shell, which is whatever the account uses, and a brace
		// group with `exit` is not something every shell parses. The login shell only
		// has to understand `sh -c '…'`; the idiom inside is POSIX sh's problem.
		// The script carries no single quote, so one level of quoting is enough.
		`sh -c 'command -v claude >/dev/null 2>&1 || { echo "[]"; exit 0; }; ` +
			`claude agents --json --all'`,
	}}
}

var ErrNotInstalled = errors.New("agents: claude is not installed here")

func (c *cmdFetcher) Fetch(ctx context.Context) ([]Session, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.name, c.args...)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()

	if ctx.Err() != nil {
		return nil, fmt.Errorf("agents: deadline exceeded after %s", c.timeout)
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("agents: %s exited %d: %s",
				c.name, ee.ExitCode(), strings.TrimSpace(errb.String()))
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrNotInstalled
		}
		return nil, err
	}
	return Parse([]byte(out.String()))
}
