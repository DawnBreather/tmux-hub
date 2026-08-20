package hostset

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DawnBreather/tmux-hub/internal/conf"
	"github.com/DawnBreather/tmux-hub/internal/configdir"
)

// Entry is one host the user has decided to keep. Enabled is the user's decision —
// a host that times out during probe stays enabled, because membership is persistent
// (Task 5) and a slow host is not an absent host. The probe's TimedOut tells status
// from membership; this file holds membership only.
type Entry struct {
	Alias    string
	Enabled  bool
	Tags     []string
	TmuxArgs []string
}

// DefaultPath is where the picker writes the set of hosts the user kept, and the one
// place that answer is derived — the reader and the writer both take this path from
// the same `-hosts` flag, so a generated file cannot land where the reader will not
// look. That is what makes §9's "generated, therefore cannot drift" true.
//
// It is CONFIG, not state: the file holds the user's own decisions and §9 makes it
// hand-editable. It read from statedir until this commit, i.e. from
// $XDG_STATE_HOME/tmux-hub/hosts.toml, while the README and §9 both documented
// ~/.config/tmux-hub/hosts.toml — so a user who followed the documentation wrote a
// file nothing read. Nothing complained, either, because an absent hosts.toml is a
// legitimate empty fleet by design (§16's zero configuration), which is exactly the
// shape of failure that has no symptom.
func DefaultPath() string {
	return configdir.Path("hosts.toml")
}

// LoadHosts reads the user's kept hosts. An absent file is an empty set (first run).
// Malformed content is an error — the file holds user decisions, and a corrupted file
// must not silently discard them (§18).
func LoadHosts(path string) ([]Entry, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil // absent file is the common case on first run
	}
	if err != nil {
		return nil, err
	}
	return parseTOML(string(raw))
}

// SaveHosts writes the set atomically, so a crash mid-write leaves the previous file
// rather than a truncated one.
func SaveHosts(path string, entries []Entry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	var b strings.Builder
	for _, e := range entries {
		b.WriteString("[[host]]\n")
		fmt.Fprintf(&b, "alias = %s\n", conf.Quote(e.Alias))
		fmt.Fprintf(&b, "enabled = %t\n", e.Enabled)
		if len(e.Tags) > 0 {
			b.WriteString("tags = [")
			for i, tag := range e.Tags {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(conf.Quote(tag))
			}
			b.WriteString("]\n")
		}
		if len(e.TmuxArgs) > 0 {
			b.WriteString("tmux_args = [")
			for i, arg := range e.TmuxArgs {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(conf.Quote(arg))
			}
			b.WriteString("]\n")
		}
		b.WriteString("\n")
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".hosts-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// parseTOML reads the `[[host]]` records. The dialect — the framing, the value parsers
// and their inverse relationship with the writer — lives in internal/conf, so the
// projects file cannot grow a second one that drifts (docs/design.md §21.11.3).
func parseTOML(content string) ([]Entry, error) {
	var entries []Entry
	var current *Entry
	flush := func() {
		if current != nil {
			entries = append(entries, *current)
		}
	}
	err := conf.Scan(content, "host",
		func() { flush(); current = &Entry{} },
		func(key, value string, _ int) error {
			switch key {
			case "alias":
				s, err := conf.String(value)
				if err != nil {
					return err
				}
				current.Alias = s
			case "enabled":
				b, err := conf.Bool(value)
				if err != nil {
					return err
				}
				current.Enabled = b
			case "tags":
				arr, err := conf.StringArray(value)
				if err != nil {
					return err
				}
				current.Tags = arr
			case "tmux_args":
				arr, err := conf.StringArray(value)
				if err != nil {
					return err
				}
				current.TmuxArgs = arr
			default:
				return fmt.Errorf("unknown key %q", key)
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	flush()
	return entries, nil
}
