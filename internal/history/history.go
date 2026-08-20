// Package history records what the hub sent, and lets it be read and re-sent.
//
// A write-only log is not a feature: the reason to keep this is that after
// broadcasting to six agents the operator needs to know which ones got it, and
// then to send the same thing again to the ones that did not.
package history

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/statedir"
)

// Entry is one send to one target.
//
// Outcome is the WORD — delivered, sent-unwitnessed, refused — and never a
// boolean, because a confirmation fires whether or not any bytes arrived, so
// `ok: true` cannot distinguish a delivery from a confirmation over nothing.
type Entry struct {
	At          time.Time `json:"at"`
	Host        string    `json:"host"`
	PaneID      string    `json:"pane_id"`
	SessionName string    `json:"session_name"`
	WindowName  string    `json:"window_name"`
	Text        string    `json:"text"`
	Outcome     string    `json:"outcome"`
	Reason      string    `json:"reason,omitempty"`
	Token       string    `json:"token,omitempty"`
	// Submitted records whether the Enter that follows a paste was confirmed. The
	// outcome word cannot carry it: a prompt sitting unsent in an agent's input box
	// was delivered and will do nothing, and a reader asking "why did nothing
	// happen" needs the difference.
	Submitted bool `json:"submitted"`
}

// Log is an append-only JSONL file with size-based rotation.
type Log struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	f        *os.File
}

// Open creates the log and its directory. A missing file is an empty history, not
// an error.
func Open(path string, maxBytes int64) (*Log, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &Log{path: path, maxBytes: maxBytes, f: f}, nil
}

// Append writes one entry. JSON encoding is what keeps a multi-line prompt on one
// line: the newline is escaped, so a reader splitting on newlines cannot tear the
// entry in half.
func (l *Log) Append(e Entry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, err := l.f.Write(append(b, '\n')); err != nil {
		return err
	}
	return l.rotateLocked()
}

// rotateLocked keeps the newest half when the file outgrows its limit. Truncating
// from the front rather than deleting the file is what preserves the entries a
// re-send would use.
func (l *Log) rotateLocked() error {
	fi, err := l.f.Stat()
	if err != nil || fi.Size() <= l.maxBytes {
		return err
	}
	raw, err := os.ReadFile(l.path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	keep := lines[len(lines)/2:]

	tmp := l.path + ".rotating"
	if err := os.WriteFile(tmp, []byte(strings.Join(keep, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	if err := l.f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, l.path); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	l.f = f
	return nil
}

// Recent returns up to n entries, newest first.
//
// A line that will not parse is SKIPPED rather than fatal: a hub killed mid-write
// leaves a torn last line, and one bad shutdown must not cost the user their
// history.
func (l *Log) Recent(n int) ([]Entry, error) {
	f, err := os.Open(l.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var all []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		all = append(all, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	out := make([]Entry, 0, n)
	for i := len(all) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, all[i])
	}
	return out, nil
}

func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// DefaultPath is $XDG_STATE_HOME/tmux-hub/history.jsonl, falling back to
// ~/.local/state — state rather than cache, because a re-send needs it.
func DefaultPath() string { return statedir.Path("history.jsonl") }
