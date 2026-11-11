package uptime

import "testing"

// 500 hanging targets must all fit one tick: 5 worst-case probes per
// worker inside the budget (SCL-8).
func TestWorkersFor(t *testing.T) {
	for _, c := range []struct{ n, want int }{{0, 16}, {80, 16}, {81, 17}, {500, 100}, {2560, 512}, {5000, 512}} {
		if got := workersFor(c.n); got != c.want {
			t.Errorf("workersFor(%d) = %d, want %d", c.n, got, c.want)
		}
	}
}
