package runner

import "testing"

func TestAutoThrottleTarget(t *testing.T) {
	const mbps = 1_000_000.0 / 8 // bytes/sec per Mbps

	cases := []struct {
		name        string
		linkMbps    int
		floorMbps   int
		externalBps float64
		want        float64
	}{
		{"idle link gets capacity minus headroom", 40, 5, 0, 36 * mbps},
		{"busy link yields to external traffic", 40, 5, 20 * mbps, 16 * mbps},
		{"never below the floor", 40, 5, 40 * mbps, 5 * mbps},
		{"floor zero clamps to 1 Mbps so limiter never disables", 40, 0, 40 * mbps, 1 * mbps},
		{"floor above capacity clamps to capacity", 40, 100, 0, 40 * mbps},
		{"external burst above capacity clamps at floor", 40, 5, 100 * mbps, 5 * mbps},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := autoThrottleTarget(tc.linkMbps, tc.floorMbps, tc.externalBps)
			if diff := got - tc.want; diff > 1 || diff < -1 {
				t.Fatalf("autoThrottleTarget(%d, %d, %.0f) = %.0f, want %.0f",
					tc.linkMbps, tc.floorMbps, tc.externalBps, got, tc.want)
			}
		})
	}
}
