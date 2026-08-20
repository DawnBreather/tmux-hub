package hub

import (
	"errors"
	"net"
	"os"
	"syscall"
	"time"
)

// Transport is what the kernel says about a socket, as opposed to what tmux says
// about the server behind it. tmux cannot tell these apart on its own: a host
// with tmux installed but no server running answers `server exited unexpectedly`,
// which reads as a broken host when nothing is wrong.
type Transport int

const (
	// TransportAbsent: nothing is listening. The tunnel is down or was never built.
	TransportAbsent Transport = iota
	// TransportEmpty: something accepted and closed at once. For a forwarded
	// socket that is sshd finding no socket on the far side — the host is
	// reachable and has no tmux server.
	TransportEmpty
	// TransportLive: the connection was accepted and held.
	TransportLive
)

func (t Transport) String() string {
	switch t {
	case TransportAbsent:
		return "absent"
	case TransportEmpty:
		return "empty"
	default:
		return "live"
	}
}

// PeerInfo is what the kernel knows about the other end.
type PeerInfo struct {
	PID int
	UID int
}

// Dial classifies a socket by connecting to it, and reports who the peer is.
//
// Measured against a real host whose tmux server was not running: the dial is
// ACCEPTED, SO_PEERCRED names the ssh process the hub spawned, and the first read
// returns 0 bytes — EOF — because sshd could not reach a socket on the far side.
// A live server holds the connection instead. That distinction is the difference
// between telling the user "the tunnel is down" and "there is no tmux running
// over there", and tmux's own error text cannot make it.
func Dial(path string, wait time.Duration) (Transport, PeerInfo, error) {
	c, err := net.DialTimeout("unix", path, wait)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return TransportAbsent, PeerInfo{}, nil
		}
		return TransportAbsent, PeerInfo{}, err
	}
	defer c.Close()

	var peer PeerInfo
	if uc, ok := c.(*net.UnixConn); ok {
		if raw, err := uc.SyscallConn(); err == nil {
			// The socket option that answers this differs per kernel (`peer_*.go`), and on a
			// platform with neither it answers nothing. It has always been best-effort: a zero
			// peer costs the transport classification below nothing, so a platform without the
			// option loses a corroboration and not a feature.
			_ = raw.Control(func(fd uintptr) {
				if p, ok := peerOf(fd); ok {
					peer = p
				}
			})
		}
	}

	// A live tmux server says nothing until spoken to, so a read that blocks is
	// the signal for "live" and an immediate EOF is the signal for "empty".
	_ = c.SetReadDeadline(time.Now().Add(wait))
	buf := make([]byte, 1)
	n, err := c.Read(buf)
	switch {
	case n == 0 && (errors.Is(err, os.ErrDeadlineExceeded) || isTimeout(err)):
		return TransportLive, peer, nil
	case n == 0:
		return TransportEmpty, peer, nil
	default:
		return TransportLive, peer, nil
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
