// Package proc answers one question: does this pane run an agent?
//
// `pane_active` is measured actively wrong — in a session where the user had split
// off a shell, the active pane WAS the shell, and a broadcast produced
// `bash: please: command not found` from a prompt beginning "please refactor…".
//
// `pane_current_command` is not the key either. An earlier draft said it reports
// `bash` for both the agent pane and the user's shell; measured, it reports `claude`
// in both launch shapes. It is still unusable because it names the FOREGROUND
// process — becoming `bash`/`git`/`npm` whenever the agent shells out for a tool —
// and because an interactive agent and `claude bg-pty-host` are both `claude`.
//
// So identification is positive and structural: look for the agent AT or under
// #{pane_pid}. "At" matters — when the pane's command is claude, pane_pid is the
// agent itself.
package proc

import (
	"path/filepath"
	"strconv"
	"strings"
)

// Proc is one process, reduced to what identification needs.
type Proc struct {
	PID  int
	PPID int
	Argv []string
	Comm string
}

// agentName is the program that makes a pane an agent pane.
const agentName = "claude"

// daemonRoles are argv[1] values that mark a claude process which is NOT an
// interactive agent. Measured on this machine: `claude bg-pty-host --bg-pty-host
// /tmp/cc-daemon-1000` and `claude bg-spare`. Stamping a pane that holds one of
// these would let a prompt be sent to a process that cannot read it.
var daemonRoles = map[string]bool{
	"bg-pty-host":   true,
	"bg-spare":      true,
	"--bg-pty-host": true,
	"--bg-spare":    true,
	"mcp":           true,
}

// isAgent reports whether one process is an interactive agent.
//
// It keys on argv[0]'s BASENAME and never on Comm. Comm is measured unreliable
// here: Node overwrites it with a thread name, so the same population contains
// `MainThread`, `node-MainThread` and — least guessable of all — `2.1.226`, the
// version string. Keying on comm would both miss real agents and match arbitrary
// helpers.
func isAgent(p Proc) bool {
	if len(p.Argv) == 0 {
		return false
	}
	if filepath.Base(p.Argv[0]) != agentName {
		return false
	}
	if len(p.Argv) > 1 && daemonRoles[p.Argv[1]] {
		return false
	}
	return true
}

// Descendants returns every process under root, root itself excluded.
//
// The visited set is not defensive clutter: a cyclic parent chain would spin
// forever, and a hub frozen in a process walk looks exactly like a hung tunnel,
// which is the most expensive symptom to diagnose.
func Descendants(all []Proc, root int) []Proc {
	children := make(map[int][]Proc, len(all))
	for _, p := range all {
		children[p.PPID] = append(children[p.PPID], p)
	}
	var out []Proc
	visited := map[int]bool{root: true}
	queue := []int{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range children[cur] {
			if visited[c.PID] {
				continue
			}
			visited[c.PID] = true
			out = append(out, c)
			queue = append(queue, c.PID)
		}
	}
	return out
}

// IdentifyAgent returns the agent process at or under a pane, if any.
//
// "At" is load-bearing and was measured the hard way: there are TWO launch shapes,
// and a walk over descendants alone is blind to one of them.
//
//   - the pane's command IS claude (`tmux new-window claude`), so `pane_pid` is the
//     claude process itself. Against a live session, a descendants-only walk
//     reported "not identified" for a pane plainly running an agent.
//   - the pane runs a shell and claude is a child of it, possibly through a
//     wrapper.
//
// The root is therefore tested first, and a plain `sleep` is still refused by both
// forms — the fix widens what is found without widening what is accepted.
//
// The answer must be recomputed rather than cached: in the shell shape `pane_pid`
// is the shell and does not change as the agent comes and goes, so an unchanged
// `pane_pid` is not evidence the agent is still there.
func IdentifyAgent(all []Proc, panePID int) (int, bool) {
	for _, p := range all {
		if p.PID == panePID && isAgent(p) {
			return p.PID, true
		}
	}
	for _, p := range Descendants(all, panePID) {
		if isAgent(p) {
			return p.PID, true
		}
	}
	return 0, false
}

// RemoteWalkScript builds ONE command that answers for every selected pane at
// once. One ssh per pane would add a round trip per target to every tick; this
// adds one, and only while something is selected.
//
// It prints `<panePID> <agentPID|0>` per line, and it is deliberately written to
// print nothing rather than something wrong when the far side lacks python3 —
// ParseRemoteWalk then identifies no agent, and an unidentified target is one the
// confirmation step will ask about.
func RemoteWalkScript(panePIDs []int) string {
	ids := make([]string, 0, len(panePIDs))
	for _, p := range panePIDs {
		ids = append(ids, strconv.Itoa(p))
	}
	return `python3 -c '
import os,glob,sys
ps={}
for d in glob.glob("/proc/[0-9]*"):
    try:
        pid=int(os.path.basename(d))
        st=open(d+"/stat").read()
        ppid=int(st.rsplit(")",1)[1].split()[1])
        argv=[c for c in open(d+"/cmdline","rb").read().split(b"\x00") if c]
    except Exception: continue
    ps[pid]=(ppid,[a.decode("utf-8","replace") for a in argv])
kids={}
for pid,(ppid,_) in ps.items(): kids.setdefault(ppid,[]).append(pid)
ROLES={"bg-pty-host","bg-spare","--bg-pty-host","--bg-spare","mcp"}
def agent(root):
    # the root FIRST: when the pane command is claude, pane_pid is claude itself,
    # and a descendants-only walk reports nothing for a live agent pane
    ra=ps.get(root,(0,[]))[1]
    if ra and os.path.basename(ra[0])=="claude" and not (len(ra)>1 and ra[1] in ROLES):
        return root
    seen={root}; q=[root]
    while q:
        cur=q.pop(0)
        for k in kids.get(cur,[]):
            if k in seen: continue
            seen.add(k); q.append(k)
            argv=ps[k][1]
            if argv and os.path.basename(argv[0])=="claude" and not (len(argv)>1 and argv[1] in ROLES):
                return k
    return 0
for a in sys.argv[1:]:
    print(a, agent(int(a)))
' ` + strings.Join(ids, " ")
}

// ParseRemoteWalk reads the script's output. Anything unparseable yields no
// entry, which reads downstream as "not identified" — the safe direction, since an
// unidentified target triggers confirmation rather than a silent send.
func ParseRemoteWalk(stdout string) map[int]int {
	out := map[int]int{}
	for _, line := range strings.Split(stdout, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		pane, err1 := strconv.Atoi(f[0])
		agent, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		out[pane] = agent
	}
	return out
}
