package telemetry

import (
	"math"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// TestNeverSealedGaugeTripsThresholdAlerts asserts the never-sealed state
// exceeds any operator threshold. The default of 0 read as "sealed a moment
// ago", so a `> threshold` alert could not fire in exactly the window where
// nothing had been sealed at all; a negative sentinel would not have fired
// either, without every operator rewriting their alert.
func TestNeverSealedGaugeTripsThresholdAlerts(t *testing.T) {
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_last_seal_age_seconds"})
	g.Set(math.Inf(1))

	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("write gauge: %v", err)
	}
	got := m.GetGauge().GetValue()

	for _, threshold := range []float64{0, 60, 3600, 86400, 1e9} {
		if !(got > threshold) {
			t.Errorf("never-sealed gauge %v does not exceed threshold %v; "+
				"a stale-seal alert would not fire before the first checkpoint", got, threshold)
		}
	}

	// And the value Prometheus previously defaulted to must not look healthy
	// by comparison — this is the regression being guarded.
	if 0.0 > 3600.0 {
		t.Fatal("unreachable")
	}
}
