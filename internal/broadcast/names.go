// Package broadcast is tmux-hub's write path: it puts text into panes the user
// selected, and reports per target whether it arrived.
//
// Everything here is built around one measured asymmetry: a tmux command that
// fails to deliver still succeeds. `send-keys -H <hex>` delivered nothing at rc=0
// with an empty stderr; a batch that aborts leaves the payload as the user's most
// recent paste buffer; an empty `-t` delivers to whichever pane they last touched;
// and a `display -p` confirmation fires whether or not any bytes arrived. So no
// step here treats a zero exit code as evidence.
package broadcast

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
)

// Instance names one running hub. It is in every name the hub writes on a remote
// server, because two hubs pointed at one host is a normal setup — a laptop and a
// desktop — and the flock that makes one hub authoritative is per-machine.
type Instance string

// NewInstance returns a fresh id. Lowercase hex only: it becomes part of a tmux
// option name and a buffer name, and neither syntax should ever have to quote it.
func NewInstance() Instance {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A hub without randomness cannot namespace its own state, and sharing a
		// name with another instance is the failure this type exists to prevent.
		panic("broadcast: no randomness for an instance id: " + err.Error())
	}
	return Instance(hex.EncodeToString(b[:]))
}

// Option is the per-pane user option holding the identity token.
func (i Instance) Option() string { return "@hub_" + string(i) }

// BufferPrefix is what this instance's buffers start with.
func (i Instance) BufferPrefix() string { return "tmux-hub-" + string(i) + "-" }

// Buffer names the buffer for one send. The sequence makes a concurrent second
// send impossible to confuse with the first.
func (i Instance) Buffer(seq uint64) string {
	return i.BufferPrefix() + strconv.FormatUint(seq, 10)
}

// BufferGlob matches EVERY instance's buffers, not just ours. The connect and
// shutdown sweeps use it deliberately: a hub that crashed mid-send left its
// payload as the most recent buffer on that server, and refusing to clean up
// after a previous run leaves the user's `prefix ]` pasting someone's prompt.
const BufferGlob = "tmux-hub-*"

// NewToken returns a per-pane identity token. It has to be unguessable rather
// than merely unique: the confirmation echoes the token back, and that is what
// makes a reply from the wrong pane impossible to mistake for the right one.
func NewToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("broadcast: no randomness for a token: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
