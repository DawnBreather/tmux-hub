package hub

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDialAbsentSocket(t *testing.T) {
	tr, _, err := Dial(filepath.Join(t.TempDir(), "nope.sock"), time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if tr != TransportAbsent {
		t.Fatalf("transport = %v, want absent", tr)
	}
}

// A peer that accepts and closes at once is what sshd does when the far-side
// socket is missing — measured against a real host with tmux installed and no
// server running. It must NOT read as a broken host.
func TestDialAcceptThenEOFIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "eof.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	tr, peer, err := Dial(path, 2*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if tr != TransportEmpty {
		t.Fatalf("transport = %v, want empty", tr)
	}
	if peer.PID != os.Getpid() {
		t.Errorf("peer pid = %d, want this process %d", peer.PID, os.Getpid())
	}
	if peer.UID != os.Geteuid() {
		t.Errorf("peer uid = %d, want %d", peer.UID, os.Geteuid())
	}
}

// A listener that accepts and says nothing is a live tmux server: it waits to be
// spoken to.
func TestDialAcceptAndHoldIsLive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()
	held := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			held <- c // hold it open
		}
	}()
	tr, _, err := Dial(path, 400*time.Millisecond)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if tr != TransportLive {
		t.Fatalf("transport = %v, want live", tr)
	}
	select {
	case c := <-held:
		c.Close()
	default:
	}
}

// And against the real local tmux server, which is live by definition.
func TestDialAgainstARealTmuxServer(t *testing.T) {
	tgt := liveTarget(t)
	tr, peer, err := Dial(tgt.Socket, 2*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if tr != TransportLive {
		t.Fatalf("transport = %v, want live", tr)
	}
	if peer.PID == 0 {
		t.Error("no peer credentials from a real server")
	}
}
