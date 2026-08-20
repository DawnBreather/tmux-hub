package broadcast

import (
	"context"
	"strings"

	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// Sweep removes every hub paste buffer from a server, whichever instance left it.
//
// It runs at connect and at shutdown, and it is not tidiness. A batch that aborts
// skips its own cleanup — measured, one missing pane left
// `tmux-hub-2: 42 bytes: "secret prompt…"` as the MOST RECENT buffer, ahead of the
// user's own, so their next `prefix ]` in any session on that host pastes the hub's
// prompt. A vanished pane is an expected condition, so the leak is routine rather
// than exotic, and a crashed hub cannot clean up after itself at all.
//
// It matches every instance's prefix rather than only ours for exactly that
// reason, and it touches nothing else: a buffer the user named is theirs.
func Sweep(ctx context.Context, r tmux.Runner, t tmux.Target) ([]string, error) {
	res, err := r.Run(ctx, t, "list-buffers", "-F", "#{buffer_name}")
	if err != nil {
		return nil, err
	}
	// A server with no buffers answers rc=1. That is an empty list, not a failure.
	if res.RC != 0 {
		return nil, nil
	}

	prefix := strings.TrimSuffix(BufferGlob, "*")
	var removed []string
	for _, name := range strings.Split(res.Stdout, "\n") {
		name = strings.TrimSpace(name)
		if name == "" || !strings.HasPrefix(name, prefix) {
			continue
		}
		if _, derr := r.Run(ctx, t, "delete-buffer", "-b", name); derr != nil {
			return removed, derr
		}
		removed = append(removed, name)
	}
	return removed, nil
}
