package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Composer is the input box. It holds text and nothing else — no keys, no
// submission.
//
// That separation is the point. A newline here is a CHARACTER in a payload that
// will travel on stdin; a newline that reaches tmux as a keypress submits the
// first paragraph of the prompt, which is the measured accident that made
// `send-keys -l` unusable for multi-line text.
type Composer struct {
	runes []rune
}

func (c *Composer) Insert(s string) { c.runes = append(c.runes, []rune(s)...) }

// Newline appends a literal LF. Never a CR: `paste-buffer -r` exists to stop LF
// becoming CR on the way out, and putting one in here would defeat it.
func (c *Composer) Newline() { c.runes = append(c.runes, '\n') }

// Backspace removes one RUNE, not one byte. A byte-wise delete would split a
// multi-byte character and send invalid UTF-8 into someone's prompt.
func (c *Composer) Backspace() {
	if len(c.runes) > 0 {
		c.runes = c.runes[:len(c.runes)-1]
	}
}

func (c *Composer) Clear()       { c.runes = nil }
func (c *Composer) Text() string { return string(c.runes) }
func (c *Composer) Empty() bool  { return len(strings.TrimSpace(string(c.runes))) == 0 }

// typedText is what a key message contributes to a text field: the runes it carries, with a space
// spelled out. The second value is false for a key that is not text at all.
//
// It exists because bubbletea reports a space as KeySpace with Runes ALSO set, and three fields have
// now got that wrong in BOTH directions: folding the two together inserted the character twice (a
// search for `two words` became `two  words` and matched nothing), and handling only KeyRunes dropped
// it entirely (a directory called `with space` could not be typed into the launch form — measured
// through the interface, the field showed `withspace`). The count of copies of a rule is the count of
// future omissions, so there is one copy.
func typedText(msg tea.KeyMsg) (string, bool) {
	switch msg.Type {
	case tea.KeySpace:
		return " ", true
	case tea.KeyRunes:
		return string(msg.Runes), true
	}
	return "", false
}
