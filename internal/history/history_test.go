package history

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func entry(text string, outcome string) Entry {
	return Entry{
		At: time.Unix(1786487832, 0).UTC(), Host: "nuc", PaneID: "%3",
		SessionName: "work", WindowName: "agent", Text: text,
		Outcome: outcome, Token: "abc123",
	}
}

func TestAppendAndReadBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	l, err := Open(path, 1<<20)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	for i, txt := range []string{"first prompt", "second prompt", "third prompt"} {
		e := entry(txt, "delivered")
		if i == 1 {
			e.Outcome = "sent-unwitnessed"
		}
		if err := l.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got, err := l.Recent(10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Recent returned %d entries", len(got))
	}
	// Newest first: a history view is read from the top.
	if got[0].Text != "third prompt" {
		t.Errorf("Recent[0] = %q, want the newest", got[0].Text)
	}
	if got[1].Outcome != "sent-unwitnessed" {
		t.Errorf("the outcome word was not preserved: %q", got[1].Outcome)
	}
}

// The outcome is one of three words, never a boolean. A log that says `ok: true`
// cannot distinguish a delivery from a confirmation that fired over nothing.
func TestOutcomeIsStoredAsAWord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	l, _ := Open(path, 1<<20)
	defer l.Close()
	for _, w := range []string{"delivered", "sent-unwitnessed", "refused"} {
		if err := l.Append(entry("x", w)); err != nil {
			t.Fatalf("Append(%s): %v", w, err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"delivered", "sent-unwitnessed", "refused"} {
		if !strings.Contains(string(raw), w) {
			t.Errorf("%q is not in the file", w)
		}
	}
	if strings.Contains(string(raw), `"ok":`) {
		t.Error("a boolean crept in")
	}
}

// A multi-line prompt must survive as one entry — JSONL means the newline is
// escaped, and a reader that split on raw newlines would tear the entry apart.
func TestMultiLineTextStaysOneEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	l, _ := Open(path, 1<<20)
	defer l.Close()
	text := "line one\nline two\nline three"
	if err := l.Append(entry(text, "delivered")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := l.Recent(5)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("a multi-line prompt became %d entries", len(got))
	}
	if got[0].Text != text {
		t.Errorf("Text = %q, want %q", got[0].Text, text)
	}
}

// Rotation must bound the file and keep the NEWEST entries, which are the ones a
// re-send uses. Asserting only that the last-written entry is still there does not
// test that: by the end of a long loop the file is small again, so the final append
// triggers no rotation at all and the assertion holds whichever half rotation keeps
// — measured, the mutation that keeps the OLDEST half left that version green.
//
// So the assertion is on the SPAN: the newest entry present, the oldest gone.
func TestRotationKeepsTheNewestAndDropsTheOldest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	l, err := Open(path, 4096) // small on purpose, so rotation fires repeatedly
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	const n = 400
	for i := 0; i < n; i++ {
		e := entry(fmt.Sprintf("entry-%04d %s", i, strings.Repeat("x", 48)), "delivered")
		if err := l.Append(e); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > 4*4096 {
		t.Errorf("the file grew to %d bytes despite a 4096 limit", fi.Size())
	}

	got, err := l.Recent(10000)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("rotation emptied the log")
	}
	if len(got) >= n {
		t.Errorf("rotation never fired: %d entries survived out of %d", len(got), n)
	}
	var texts []string
	for _, e := range got {
		texts = append(texts, e.Text)
	}
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, fmt.Sprintf("entry-%04d", n-1)) {
		t.Errorf("rotation lost the NEWEST entry; newest kept is %q", texts[0])
	}
	if strings.Contains(joined, "entry-0000") {
		t.Error("rotation kept the OLDEST entry, so it is discarding the wrong half")
	}
}

// A corrupt line — a hub killed mid-write — must not make the whole log
// unreadable, or one bad shutdown costs the user their history.
func TestACorruptLineIsSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	l, _ := Open(path, 1<<20)
	if err := l.Append(entry("good one", "delivered")); err != nil {
		t.Fatal(err)
	}
	l.Close()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("{\"At\":\"broken\n")
	f.Close()

	l2, err := Open(path, 1<<20)
	if err != nil {
		t.Fatalf("Open on a log with a torn line: %v", err)
	}
	defer l2.Close()
	got, err := l2.Recent(10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 1 || got[0].Text != "good one" {
		t.Errorf("got %+v, want the one intact entry", got)
	}
}

func TestRecentOnAMissingFile(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "sub", "history.jsonl"), 1<<20)
	if err != nil {
		t.Fatalf("Open must create its directory: %v", err)
	}
	defer l.Close()
	got, err := l.Recent(5)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v from a fresh log", got)
	}
}
