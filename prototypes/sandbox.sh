#!/usr/bin/env bash
# A scratch tmux server to try the hub against, where every key is safe to press.
#
#   ./sandbox.sh up     # create it and print what to run
#   ./sandbox.sh run    # create it if needed, then start the hub against it only
#   ./sandbox.sh down    # destroy it
#   ./sandbox.sh ls      # what is in it right now
#
# It is a PRIVATE socket, so `K` (kill), `i` (write), `R` (restart) and `a` (go to
# it) act on panes nobody depends on. The dev rule in §13 of the design says
# anything that mutates belongs on a socket like this one; the default socket —
# where your real work lives — is never named here.
#
# The hub is pointed at it with `--host sandbox=<socket>,local --no-local`:
#   local     marks the socket as a server on THIS machine, which is what turns
#             the identity walk on and therefore what makes the host writable
#   --no-local leaves your real default server out of the picture entirely
set -euo pipefail

SOCK="${TMUX_HUB_SANDBOX_SOCKET:-/tmp/tmux-hub-sandbox-$(id -u).sock}"
HUB="${TMUX_HUB_BIN:-tmux-hub}"

up() {
	if tmux -S "$SOCK" has-session -t noise 2>/dev/null; then
		echo "sandbox already up at $SOCK"
		return
	fi
	# -x/-y so the panes have a shape even with no client attached; without them a
	# detached server sizes windows to 80x24 anyway, but being explicit keeps the
	# tiles' content widths predictable while you are comparing screens.
	tmux -S "$SOCK" new-session -d -s noise -n logs -x 120 -y 30
	tmux -S "$SOCK" send-keys -t noise:logs \
		"while :; do printf '%s GET /healthz 200\\n' \"\$(date +%T)\"; sleep 3; done" Enter

	tmux -S "$SOCK" new-window -t noise -n build
	tmux -S "$SOCK" send-keys -t noise:build \
		"while :; do echo 'ok  internal/tmux  6.4s'; sleep 7; done" Enter

	tmux -S "$SOCK" new-session -d -s work -n shell
	tmux -S "$SOCK" new-window -t work -n asks
	# A pane that LOOKS like an agent waiting on you, so the inbox has something in
	# `needs` to sort to the top. It is a plain shell read, so answering it is
	# harmless and you can watch the state flip.
	tmux -S "$SOCK" send-keys -t work:asks \
		"printf 'Do you want to proceed?\\n  1. Yes\\n  2. No\\n'; read -r answer; echo \"you said: \$answer\"" Enter

	echo "sandbox up at $SOCK"
}

down() {
	tmux -S "$SOCK" kill-server 2>/dev/null || true
	rm -f "$SOCK"
	echo "sandbox gone"
}

ls_() {
	tmux -S "$SOCK" list-panes -a \
		-F '#{session_name}:#{window_name}.#{pane_index}  #{pane_id}  #{pane_current_command}' 2>/dev/null ||
		echo "no sandbox at $SOCK"
}

case "${1:-up}" in
up) up ;;
down) down ;;
ls) ls_ ;;
run)
	up
	echo
	echo "starting the hub against the sandbox only — q quits"
	exec "$HUB" --no-local --host "sandbox=$SOCK,local"
	;;
*)
	echo "usage: sandbox.sh [up|run|down|ls]" >&2
	exit 2
	;;
esac

if [ "${1:-up}" = up ]; then
	cat <<EOF

run the hub against it, and nothing else:

    $HUB --no-local --host sandbox=$SOCK,local

or in one step:  $0 run
EOF
fi
