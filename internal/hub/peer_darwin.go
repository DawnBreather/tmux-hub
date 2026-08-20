package hub

import "golang.org/x/sys/unix"

// peerOf reads the peer's credentials from a connected unix socket.
//
// Darwin has no `SO_PEERCRED`; it splits the same answer across two options at `SOL_LOCAL`, and
// they are not interchangeable — `LOCAL_PEERCRED` returns an `xucred`, which carries the uid and
// NOT the pid, while `LOCAL_PEERPID` returns the pid alone. The pid is the one the transport
// argument uses, so it is fetched first and a missing uid does not discard it.
func peerOf(fd uintptr) (PeerInfo, bool) {
	var peer PeerInfo
	var got bool
	if pid, err := unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID); err == nil {
		peer.PID, got = pid, true
	}
	if cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED); err == nil {
		peer.UID, got = int(cred.Uid), true
	}
	return peer, got
}
