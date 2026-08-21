package telemetry

import (
	"math"
	"testing"
	"time"
)

// TestSealAgeSeconds exercises the function the production path actually calls,
// rather than a throwaway gauge. An earlier version of this test built its own
// gauge, set +Inf itself, and asserted infinity exceeds finite numbers — which
// tested Go's float comparison, not this package, and would have stayed green
// if the never-sealed path were reverted to 0.
func TestSealAgeSeconds(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	t.Run("never sealed exceeds any alert threshold", func(t *testing.T) {
		got := sealAgeSeconds(nil, now)

		// The property operators depend on: the documented `> threshold` alert
		// fires for a deployment that has never sealed. 0 (the Prometheus
		// default) and a negative sentinel both fail this.
		for _, threshold := range []float64{0, 60, 3600, 86400, 1e9} {
			if !(got > threshold) {
				t.Errorf("never-sealed age %v does not exceed threshold %v; a stale-seal "+
					"alert would not fire before the first checkpoint", got, threshold)
			}
		}
		if !math.IsInf(got, 1) {
			t.Errorf("never-sealed age = %v, want +Inf as documented in the metric help "+
				"and docs/AUDIT-INTEGRITY.md", got)
		}
	})

	t.Run("sealed reports elapsed seconds", func(t *testing.T) {
		sealed := now.Add(-90 * time.Second)
		if got := sealAgeSeconds(&sealed, now); got != 90 {
			t.Errorf("age = %v, want 90", got)
		}
	})

	t.Run("just sealed is near zero and below thresholds", func(t *testing.T) {
		sealed := now
		got := sealAgeSeconds(&sealed, now)
		if got != 0 {
			t.Errorf("age = %v, want 0", got)
		}
		// The distinction the never-sealed case must not collide with.
		if got > 3600 {
			t.Error("a fresh seal must not trip a stale-seal alert")
		}
	})
}
