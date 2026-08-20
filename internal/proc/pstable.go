package proc

import (
	"strconv"
	"strings"
)

// parsePSTable reads the output of `ps -o pid=,ppid=,args=` into the same shape `/proc` gives.
//
// It lives in an untagged file, and that is deliberate: it is the only part of the non-Linux walk
// that contains a decision, and a parser that can only be exercised on the platform it serves is a
// parser nobody has run. The tests beside it use output captured from the real command.
//
// TWO honest approximations, both of which `IdentifyAgent` is insensitive to:
//
//   - Argv is recovered by splitting on whitespace, so an argument that CONTAINS a space arrives
//     as several. Identification keys on `filepath.Base(Argv[0])` and on `Argv[1]` against a set of
//     known daemon roles, none of which has a space, so the split cannot change an answer. `/proc`
//     is exact because it stores the NUL-separated vector; `ps` renders it already joined, and the
//     joining is not reversible.
//   - Comm is left EMPTY rather than filled from argv[0]. `isAgent` documents Comm as measured
//     unreliable and never reads it (Node overwrites it with a thread name or a version string), so
//     inventing a value here would be a field that looks authoritative and is not.
//
// A line whose first two fields are not numbers is skipped, which covers a header if one is ever
// printed and a truncated last line.
func parsePSTable(out string) []Proc {
	lines := strings.Split(out, "\n")
	procs := make([]Proc, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		procs = append(procs, Proc{PID: pid, PPID: ppid, Argv: fields[2:]})
	}
	return procs
}
