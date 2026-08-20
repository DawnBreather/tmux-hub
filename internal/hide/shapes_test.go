package hide

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every shape hidden.json can have on disk, and which of the two refusals each takes. This
// is the calibration the single v1 case could not give: a v1 file and a genuinely corrupt
// one both produce a warning and an empty set, so a test that only asserts "there is a
// warning" cannot tell which branch ran — and for a whole commit the answer was the wrong
// one.
func TestEveryFileShapeTakesTheRightRefusal(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		// The shape every shipped version wrote: a bare array, window as a NAME.
		{"v1 bare array", `[{"host":"h","session":"s","window":"w","index":1,"start":""}]`, "hide them again"},
		// An envelope whose "v" is absent reads as 0, which is the case the version check
		// was written for and the only one it used to reach.
		{"v2 envelope, no v", `{"keys":[]}`, "hide them again"},
		// And a file that is genuinely corrupt must still say so, or the migration message
		// would start covering for real damage.
		{"truncated json", `{"keys":`, "malformed"},
		{"not json at all", `hello`, "malformed"},
		{"an array of the wrong thing", `[1,2,3]`, "malformed"},
	} {
		p := filepath.Join(t.TempDir(), "hidden.json")
		if err := os.WriteFile(p, []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		s, _ := Open(p)
		if !strings.Contains(s.Warning(), c.want) {
			t.Errorf("%s: warning = %q, want it to contain %q", c.name, s.Warning(), c.want)
		}
		if s.Count() != 0 {
			t.Errorf("%s: Count = %d, want 0", c.name, s.Count())
		}
	}
}
