package hub

import "syscall"

// peerOf reads the peer's credentials from a connected unix socket.
//
// `SO_PEERCRED` gives pid, uid and gid in one call. This is the measurement §5's transport rests
// on: dialling a forwarded socket returns EXACTLY the pid of the ssh process the hub spawned, which
// is what lets the hub tell "my own tunnel answered" from "something else is listening there".
func peerOf(fd uintptr) (PeerInfo, bool) {
	cred, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	if err != nil {
		return PeerInfo{}, false
	}
	return PeerInfo{PID: int(cred.Pid), UID: int(cred.Uid)}, true
}
