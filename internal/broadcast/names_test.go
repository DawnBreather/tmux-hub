package broadcast

import (
	"regexp"
	"strings"
	"testing"
)

// A laptop and a desktop pointed at one host is a normal setup, and flock is
// per-machine, so nothing but the name keeps two hubs' remote state apart.
func TestInstancesDiffer(t *testing.T) {
	a, b := NewInstance(), NewInstance()
	if a == b {
		t.Fatal("two instances got the same id")
	}
	if a == "" {
		t.Fatal("empty instance id")
	}
}

// The instance id ends up inside a tmux option NAME and a buffer NAME, so it must
// contain nothing that either syntax treats specially.
func TestInstanceIDIsNameSafe(t *testing.T) {
	safe := regexp.MustCompile(`^[a-z0-9]+$`)
	for i := 0; i < 32; i++ {
		id := string(NewInstance())
		if !safe.MatchString(id) {
			t.Fatalf("instance id %q is not name-safe", id)
		}
		if len(id) < 6 || len(id) > 16 {
			t.Errorf("instance id %q has an awkward length %d", id, len(id))
		}
	}
}

func TestOptionAndBufferNames(t *testing.T) {
	i := Instance("ab12cd")
	if got := i.Option(); got != "@hub_ab12cd" {
		t.Errorf("Option() = %q", got)
	}
	if got := i.Buffer(7); got != "tmux-hub-ab12cd-7" {
		t.Errorf("Buffer(7) = %q", got)
	}
	if !strings.HasPrefix(i.Buffer(7), i.BufferPrefix()) {
		t.Error("Buffer must sit under BufferPrefix, or the sweep misses it")
	}
	// The sweep at connect and shutdown must match another instance's leftovers
	// too — a hub that crashed is exactly the case worth cleaning.
	if !strings.HasPrefix(i.BufferPrefix(), strings.TrimSuffix(BufferGlob, "*")) {
		t.Errorf("BufferGlob %q does not cover %q", BufferGlob, i.BufferPrefix())
	}
}

// A token proves identity only if a wrong pane cannot produce one, so it must be
// unguessable and never repeat.
func TestTokensAreUniqueAndOpaque(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 256; i++ {
		tok := NewToken()
		if seen[tok] {
			t.Fatalf("token repeated: %q", tok)
		}
		if len(tok) < 24 {
			t.Fatalf("token %q is too short to be unguessable", tok)
		}
		if strings.ContainsAny(tok, " ;'\"%$") {
			t.Fatalf("token %q contains something a tmux format would treat specially", tok)
		}
		seen[tok] = true
	}
}
