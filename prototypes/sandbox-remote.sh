#!/usr/bin/env bash
# Set up a REMOTE host to try the hub against, end to end.
#
#   ./sandbox-remote.sh up nuc      # session on nuc + ssh master + forwarded socket
#   ./sandbox-remote.sh run nuc     # the above, then start the hub against it only
#   ./sandbox-remote.sh down nuc    # remove all of it, here and there
#   ./sandbox-remote.sh ls nuc      # what is up right now
#
# The hub does not spawn or supervise ssh (docs/design.md §5): the transport is
# your own forward, and the hub is pointed at the socket it produces. So there are
# three moving parts and this script owns all three:
#
#   1. a tmux session ON the remote, named tmux-hub-demo
#   2. an ssh MASTER here, so `a` has a control socket to reach the remote through —
#      a forwarded socket cannot carry an attach at all (measured: it fails
#      "open terminal failed: not a terminal" even from a real pty, because the
#      client passes its terminal fd over SCM_RIGHTS and a forward drops ancillary
#      data). Polling uses the socket; attaching uses the master.
#   3. a unix-socket FORWARD from the remote tmux socket to a local path
#
# The remote session is created on the remote's DEFAULT socket on purpose. §12
# records the limitation: the attach the hub builds is `tmux attach -t <session>`
# against the default socket, so a session on some other remote socket polls fine
# and `a` would reach the wrong server.
set -euo pipefail

HOST="${2:-}"
if [ -z "$HOST" ]; then
	echo "usage: sandbox-remote.sh [up|run|down|ls] <ssh-host>" >&2
	exit 2
fi

SESSION=tmux-hub-demo
CTL="$HOME/.ssh/cm-hubdemo-$HOST"
LOCAL_SOCK="/tmp/tmux-hub-remote-$HOST-$(id -u).sock"
HUB="${TMUX_HUB_BIN:-tmux-hub}"

remote_socket() {
	# Ask the remote where its default socket is rather than assuming /tmp/tmux-UID.
	ssh -o BatchMode=yes "$HOST" \
		"tmux display -p '#{socket_path}' 2>/dev/null || echo /tmp/tmux-\$(id -u)/default"
}

up() {
	echo "1/3  a tmux session on $HOST"
	# -d so nothing attaches; || true so re-running is harmless.
	ssh -o BatchMode=yes "$HOST" "
		tmux has-session -t $SESSION 2>/dev/null && exit 0
		tmux new-session -d -s $SESSION -n asks -x 120 -y 30
		tmux send-keys -t $SESSION:asks \
			\"printf 'Deploy to production?\\\\n  1. Yes\\\\n  2. No\\\\n'; read -r a; echo \\\"you said: \\\$a\\\"\" Enter
		tmux new-window -t $SESSION -n logs
		tmux send-keys -t $SESSION:logs \
			'while :; do printf \"%s worker tick\\\\n\" \"\$(date +%T)\"; sleep 4; done' Enter
	" || true

	RSOCK=$(remote_socket)
	echo "     remote tmux socket: $RSOCK"

	echo "2/3  ssh master + socket forward"
	rm -f "$LOCAL_SOCK"
	if ssh -O check -S "$CTL" "$HOST" 2>/dev/null; then
		ssh -O exit -S "$CTL" "$HOST" 2>/dev/null || true
	fi
	# -f backgrounds ssh ITSELF, so the master is owned by ssh and not by this
	# shell — a master started with a plain `&` dies when the caller's shell exits.
	ssh -f -N -M -S "$CTL" -L "$LOCAL_SOCK:$RSOCK" "$HOST"

	echo "3/3  waiting for the socket to appear"
	for _ in $(seq 1 20); do
		[ -S "$LOCAL_SOCK" ] && break
		sleep 0.25
	done
	if [ ! -S "$LOCAL_SOCK" ]; then
		echo "the forward did not produce $LOCAL_SOCK — is the remote socket path right?" >&2
		exit 1
	fi
	# Prove the forwarded socket really answers before handing over a command that
	# assumes it does. A forward that connected to nothing looks identical until the
	# hub polls it.
	if ! tmux -S "$LOCAL_SOCK" list-sessions >/dev/null 2>&1; then
		echo "the socket exists but no tmux answers on it" >&2
		exit 1
	fi
	echo "     ok: $(tmux -S "$LOCAL_SOCK" list-sessions | tr '\n' '|')"
}

down() {
	ssh -o BatchMode=yes "$HOST" "tmux kill-session -t $SESSION 2>/dev/null || true" || true
	ssh -O exit -S "$CTL" "$HOST" 2>/dev/null || true
	rm -f "$LOCAL_SOCK" "$CTL"
	echo "removed: the $SESSION session on $HOST, the ssh master, and $LOCAL_SOCK"
}

ls_() {
	printf 'ssh master: '
	ssh -O check -S "$CTL" "$HOST" 2>&1 | head -1 || true
	printf 'local socket: '
	[ -S "$LOCAL_SOCK" ] && echo "$LOCAL_SOCK" || echo "absent"
	printf 'remote panes: '
	tmux -S "$LOCAL_SOCK" list-panes -a \
		-F '#{session_name}:#{window_name} #{pane_id} #{pane_current_command}' 2>/dev/null |
		tr '\n' '|' || echo "unreadable"
	echo
}

case "${1:-up}" in
up)
	up
	cat <<EOF

run the hub against $HOST and nothing else:

    $HUB --no-local --host "$HOST=$LOCAL_SOCK,ssh=$HOST,ctl=$CTL"

  ssh= and ctl= are what \`a\` needs. Without them the host still polls and \`a\`
  tells you which field is missing instead of failing obscurely.

tear it all down:  $0 down $HOST
EOF
	;;
run)
	up
	echo
	echo "starting the hub against $HOST only — q quits"
	exec "$HUB" --no-local --host "$HOST=$LOCAL_SOCK,ssh=$HOST,ctl=$CTL"
	;;
down) down ;;
ls) ls_ ;;
*)
	echo "usage: sandbox-remote.sh [up|run|down|ls] <ssh-host>" >&2
	exit 2
	;;
esac
