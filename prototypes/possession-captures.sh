#!/usr/bin/env bash
# Capture the status lines §20 claims the operator reads, from a real nested tmux.
#
#   ./possession-captures.sh <outdir>
#
# §20's whole UX argument is that the hub draws nothing because tmux already says
# where you are: the status line's left segment goes [hub] -> [work]. That claim
# is only worth putting in a review document as the bytes tmux actually printed,
# so this builds the arrangement and captures it.
#
# The arrangement is a tmux inside a tmux, because a status line cannot be
# captured directly: `capture-pane` returns a PANE, and the status bar is not in
# one. Running the inner server inside an outer pane makes the inner server's
# whole screen -- status bar included -- the outer pane's content.
#
#   outer server (-L hubflows, status off, one 100x24 pane)
#     inner server (-L hubinner)          <- the "hub's own server"
#     third server (-L hubother)          <- stands in for another machine
#
# Both extra servers are private sockets created here and killed here. Nothing
# touches the default socket: the dev rule in §13 of the design.
set -euo pipefail

out="${1:?usage: possession-captures.sh <outdir>}"
mkdir -p "$out"

OUTER=hubflows
INNER=hubinner
OTHER=hubother

cleanup() {
	for s in "$OUTER" "$INNER" "$OTHER"; do
		tmux -L "$s" kill-server 2>/dev/null || true
	done
}
trap cleanup EXIT
cleanup

# --- the outer viewport ------------------------------------------------------
tmux -L "$OUTER" new-session -d -s view -x 100 -y 24
tmux -L "$OUTER" set -g status off

snap() { # snap <file> -- the outer pane, i.e. the inner server's whole screen
	tmux -L "$OUTER" capture-pane -p -t view | sed -e 's/[[:space:]]*$//'  >"$out/$1"
	printf '%-28s %s lines\n' "$1" "$(wc -l <"$out/$1")"
}

# --- the hub's own server, displaying the hub's session ----------------------
tmux -L "$INNER" new-session -d -s hub -x 100 -y 24
tmux -L "$INNER" send-keys -t hub "clear; printf 'tmux-hub  2 sessions\\n> %s claude   needs  local/api %s\\n' '⚑' '%%0'" Enter
tmux -L "$OUTER" send-keys -t view "clear; TERM=xterm-256color tmux -L $INNER attach -t hub" Enter
sleep 2
snap cap-01-hub.txt

# --- the target the operator jumps to, on the SAME server -------------------
tmux -L "$INNER" new-session -d -s work -n agent
tmux -L "$INNER" send-keys -t work:agent \
	"clear; printf 'Do you want to proceed?\\n  1. Yes\\n  2. No\\n> '" Enter
sleep 0.5

# switch-client needs a client name when it is not run from inside one, and the
# session is targeted by ID for the reason §7 gives: a name does not survive a
# rename. This is the pair of commands §20 specifies.
CLIENT=$(tmux -L "$INNER" list-clients -F '#{client_name}' | head -1)
WORK_SESSION=$(tmux -L "$INNER" display -p -t work -F '#{session_id}')
WORK_WINDOW=$(tmux -L "$INNER" display -p -t work:agent -F '#{window_id}')
echo "same-server jump: switch-client -c $CLIENT -t $WORK_SESSION ; select-window -t $WORK_WINDOW"
tmux -L "$INNER" switch-client -c "$CLIENT" -t "$WORK_SESSION"
tmux -L "$INNER" select-window -t "$WORK_WINDOW"
sleep 1
snap cap-02-work.txt

# --- back, the way the operator does it: last-session -----------------------
tmux -L "$INNER" switch-client -c "$CLIENT" -l
sleep 1
snap cap-03-back.txt

# --- another server, reached the way §20 says: a new window with the attach --
# A different socket IS a different tmux server -- the case switch-client cannot
# reach -- so this exercises the real mechanism. What it cannot show is a remote
# HOSTNAME in the inner status line; here #{host} is this machine.
tmux -L "$OTHER" new-session -d -s ag -n sh -x 100 -y 24
tmux -L "$OTHER" set -g status-right ' "#{host}" '
tmux -L "$OTHER" send-keys -t ag:sh "clear; printf 'agent on another server\\n> '" Enter
sleep 0.5
# The client is already back on hub after cap-03. Switching -l again would flip it
# to work, and the capture would show the wrong session while looking plausible --
# it did, on the first run of this script.
HUB_SESSION=$(tmux -L "$INNER" display -p -t hub -F '#{session_id}')
tmux -L "$INNER" new-window -t hub -n other -- \
	env TERM=xterm-256color tmux -L "$OTHER" attach -t ag
tmux -L "$INNER" switch-client -c "$CLIENT" -t "$HUB_SESSION"
tmux -L "$INNER" select-window -t hub:other
sleep 2
snap cap-04-other-server.txt

# --- closing that window leaves the other server's pane alive ---------------
# The asymmetry §20 rests on: this is safe, and the same keystroke on a
# link-window'ed copy kills the agent.
BEFORE=$(tmux -L "$OTHER" list-panes -t ag -F '#{pane_pid}' | head -1)
tmux -L "$INNER" kill-window -t hub:other
sleep 1
AFTER=$(tmux -L "$OTHER" list-panes -t ag -F '#{pane_pid}' 2>/dev/null | head -1 || true)
CLIENTS=$(tmux -L "$OTHER" list-clients -t ag 2>/dev/null | wc -l)
echo "other server pane_pid before=$BEFORE after=${AFTER:-GONE} clients_now=$CLIENTS"
snap cap-05-after-close.txt

# --- a payload that DIES leaves its message on screen, in a LIVE pane -------
# Before any of this, a window whose ssh died vanished and the hub still said
# "back from api:review", so a broken jump read as a completed one. The first fix
# was `set -w remain-on-exit on` after the create, and it lost a race it could
# only win on time — new-window is what STARTS the payload (measured: a payload of
# `false` was lost in 6 of 12 trials) — and when it won, it left a DEAD pane,
# whose visible screen carries only tmux's banner while ssh's own line goes into
# the scrollback. So NO option is set here, and the payload holds the window
# itself: this is the argument AttachWindow now sends, built the way internal/ui's
# WindowPayload builds it, with a control socket that is not there.
ATTACH_ARGV="'ssh' '-S' '/nonexistent/control/socket' '-t' 'nosuchhost.invalid' 'tmux' 'attach' '-t' '\$0'"
SCRIPT="$ATTACH_ARGV; s=\$?; printf '\\n[tmux-hub] the attach exited %s — press enter to close this window\\n' \"\$s\"; read _"
# The second level of quoting, by shellJoin's rule: a single quote is the one
# character single quotes cannot carry, so it is closed, escaped and reopened.
PAYLOAD="'sh' '-c' '$(printf '%s' "$SCRIPT" | sed "s/'/'\\\\''/g")'"
tmux -L "$INNER" new-window -t hub -n broken "$PAYLOAD"
sleep 2
tmux -L "$INNER" switch-client -c "$CLIENT" -t "$HUB_SESSION"
tmux -L "$INNER" select-window -t hub:broken
sleep 1
DEAD=$(tmux -L "$INNER" display -p -t hub:broken -F '#{pane_dead}:#{pane_dead_status}')
echo "broken payload: pane_dead:status = $DEAD (want 0: — a LIVE shell holding the window) (window still listed: $(tmux -L "$INNER" list-windows -t hub -F '#{window_name}' | tr '\n' ' '))"
snap cap-06-payload-died.txt

echo "captures in $out"
