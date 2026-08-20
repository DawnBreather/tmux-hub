package proc

import (
	"context"
	"os"
	"testing"
	"time"
)

// The identity walk runs on every tick for local hosts, so what it costs is a number
// the design owes rather than an assumption. Measured on this machine: 2920
// processes, 54 ms per walk, of which the /proc/<pid>/stat pass — which every
// process needs, because the parent chain of a descendant can pass through any of
// them — is 33 ms and the cmdline pass the other 21 ms.
//
// Reported and not asserted: a threshold here would fail on a loaded machine and
// prove nothing. It is a test so the number is re-measured rather than remembered.
func TestLocalWalkCost(t *testing.T) {
	pids := []int{os.Getpid(), 1, 2, 3, 4, 5, 6, 7, 8, 9}
	w := Local()
	if _, err := w.Walk(context.Background(), pids); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	const n = 20
	start := time.Now()
	for i := 0; i < n; i++ {
		if _, err := w.Walk(context.Background(), pids); err != nil {
			t.Fatalf("Walk: %v", err)
		}
	}
	all, _ := Snapshot()
	t.Logf("%d processes, %d panes: one walk = %.1f ms (mean of %d)",
		len(all), len(pids), float64(time.Since(start).Microseconds())/1000.0/n, n)
}
