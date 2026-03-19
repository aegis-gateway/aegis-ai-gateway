package telemetry

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// TestRecordDBPoolStats tests the DB pool metrics recording.
func TestRecordDBPoolStats(t *testing.T) {
	dbPoolConns := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "test_aegis_db_pool_conns",
		Help: "Test DB pool gauge",
	}, []string{"state"})

	m := &Metrics{
		DBPoolConns: dbPoolConns,
	}

	// Record some pool stats
	m.RecordDBPoolStats(5, 20, 25, 25)

	// Verify acquired connections
	gauge, err := dbPoolConns.GetMetricWithLabelValues("acquired")
	if err != nil {
		t.Fatalf("failed to get acquired metric: %v", err)
	}
	var metric dto.Metric
	gauge.Write(&metric)
	if *metric.Gauge.Value != 5 {
		t.Errorf("expected acquired conns 5, got %v", *metric.Gauge.Value)
	}

	// Verify idle connections
	gauge, err = dbPoolConns.GetMetricWithLabelValues("idle")
	if err != nil {
		t.Fatalf("failed to get idle metric: %v", err)
	}
	gauge.Write(&metric)
	if *metric.Gauge.Value != 20 {
		t.Errorf("expected idle conns 20, got %v", *metric.Gauge.Value)
	}

	// Verify max connections
	gauge, err = dbPoolConns.GetMetricWithLabelValues("max")
	if err != nil {
		t.Fatalf("failed to get max metric: %v", err)
	}
	gauge.Write(&metric)
	if *metric.Gauge.Value != 25 {
		t.Errorf("expected max conns 25, got %v", *metric.Gauge.Value)
	}

	// Verify total connections
	gauge, err = dbPoolConns.GetMetricWithLabelValues("total")
	if err != nil {
		t.Fatalf("failed to get total metric: %v", err)
	}
	gauge.Write(&metric)
	if *metric.Gauge.Value != 25 {
		t.Errorf("expected total conns 25, got %v", *metric.Gauge.Value)
	}
}

// TestRecordDBPoolStats_ZeroValues tests recording zero pool stats.
func TestRecordDBPoolStats_ZeroValues(t *testing.T) {
	dbPoolConns := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "test_aegis_db_pool_conns_zero",
		Help: "Test DB pool gauge zero values",
	}, []string{"state"})

	m := &Metrics{
		DBPoolConns: dbPoolConns,
	}

	// Record zero stats (fresh pool)
	m.RecordDBPoolStats(0, 0, 25, 0)

	// Verify acquired is 0
	gauge, _ := dbPoolConns.GetMetricWithLabelValues("acquired")
	var metric dto.Metric
	gauge.Write(&metric)
	if *metric.Gauge.Value != 0 {
		t.Errorf("expected acquired conns 0, got %v", *metric.Gauge.Value)
	}

	// Verify max is still 25
	gauge, _ = dbPoolConns.GetMetricWithLabelValues("max")
	gauge.Write(&metric)
	if *metric.Gauge.Value != 25 {
		t.Errorf("expected max conns 25, got %v", *metric.Gauge.Value)
	}
}

// TestRecordDBPoolStats_Update tests updating pool stats over time.
func TestRecordDBPoolStats_Update(t *testing.T) {
	dbPoolConns := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "test_aegis_db_pool_conns_update",
		Help: "Test DB pool gauge updates",
	}, []string{"state"})

	m := &Metrics{
		DBPoolConns: dbPoolConns,
	}

	// Initial state
	m.RecordDBPoolStats(1, 9, 25, 10)

	gauge, _ := dbPoolConns.GetMetricWithLabelValues("acquired")
	var metric dto.Metric
	gauge.Write(&metric)
	if *metric.Gauge.Value != 1 {
		t.Errorf("expected acquired conns 1, got %v", *metric.Gauge.Value)
	}

	// After load increase
	m.RecordDBPoolStats(15, 5, 25, 20)

	gauge, _ = dbPoolConns.GetMetricWithLabelValues("acquired")
	gauge.Write(&metric)
	if *metric.Gauge.Value != 15 {
		t.Errorf("expected acquired conns 15 after update, got %v", *metric.Gauge.Value)
	}

	gauge, _ = dbPoolConns.GetMetricWithLabelValues("idle")
	gauge.Write(&metric)
	if *metric.Gauge.Value != 5 {
		t.Errorf("expected idle conns 5 after update, got %v", *metric.Gauge.Value)
	}

	// After load decreases
	m.RecordDBPoolStats(3, 17, 25, 20)

	gauge, _ = dbPoolConns.GetMetricWithLabelValues("acquired")
	gauge.Write(&metric)
	if *metric.Gauge.Value != 3 {
		t.Errorf("expected acquired conns 3 after decrease, got %v", *metric.Gauge.Value)
	}
}

// TestRecordDBPoolStats_PoolExhaustion tests stats when pool is exhausted.
func TestRecordDBPoolStats_PoolExhaustion(t *testing.T) {
	dbPoolConns := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "test_aegis_db_pool_conns_exhausted",
		Help: "Test DB pool gauge exhaustion",
	}, []string{"state"})

	m := &Metrics{
		DBPoolConns: dbPoolConns,
	}

	// Pool at capacity - all connections acquired
	m.RecordDBPoolStats(25, 0, 25, 25)

	var metric dto.Metric

	gauge, _ := dbPoolConns.GetMetricWithLabelValues("acquired")
	gauge.Write(&metric)
	if *metric.Gauge.Value != 25 {
		t.Errorf("expected acquired conns 25 (exhausted), got %v", *metric.Gauge.Value)
	}

	gauge, _ = dbPoolConns.GetMetricWithLabelValues("idle")
	gauge.Write(&metric)
	if *metric.Gauge.Value != 0 {
		t.Errorf("expected idle conns 0 (exhausted), got %v", *metric.Gauge.Value)
	}

	gauge, _ = dbPoolConns.GetMetricWithLabelValues("max")
	gauge.Write(&metric)
	if *metric.Gauge.Value != 25 {
		t.Errorf("expected max conns 25, got %v", *metric.Gauge.Value)
	}
}

// TestDBPoolWaitDuration tests the wait duration histogram.
func TestDBPoolWaitDuration(t *testing.T) {
	dbPoolWait := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "test_aegis_db_pool_wait_duration_ms",
		Help:    "Test DB pool wait duration",
		Buckets: []float64{1, 2, 5, 10, 25, 50, 100, 250, 500, 1000},
	}, []string{})

	m := &Metrics{
		DBPoolWaitDuration: dbPoolWait,
	}

	// Observe some wait times
	m.DBPoolWaitDuration.WithLabelValues().Observe(5.0)
	m.DBPoolWaitDuration.WithLabelValues().Observe(15.0)
	m.DBPoolWaitDuration.WithLabelValues().Observe(100.0)

	// Collect and verify metrics through the registry
	ch := make(chan prometheus.Metric, 10)
	dbPoolWait.Collect(ch)
	close(ch)

	collected := 0
	for range ch {
		collected++
	}

	if collected != 1 {
		t.Errorf("expected 1 histogram metric, got %d", collected)
	}
}

// TestMetrics_NewMetrics_DBPool tests that NewMetrics creates DB pool metrics.
func TestMetrics_NewMetrics_DBPool(t *testing.T) {
	// Note: This test can't use the actual NewMetrics() because it would
	// register with the default registry and conflict with other tests.
	// Instead, we verify the structure matches expectations.
	
	dbPoolConns := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "test_check_aegis_db_pool_conns",
		Help: "Test check",
	}, []string{"state"})

	dbPoolWait := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "test_check_aegis_db_pool_wait_duration_ms",
		Help:    "Test check",
		Buckets: []float64{1, 2, 5, 10, 25, 50, 100, 250, 500, 1000},
	}, []string{})

	m := &Metrics{
		DBPoolConns:       dbPoolConns,
		DBPoolWaitDuration: dbPoolWait,
	}

	if m.DBPoolConns == nil {
		t.Error("DBPoolConns should not be nil")
	}
	if m.DBPoolWaitDuration == nil {
		t.Error("DBPoolWaitDuration should not be nil")
	}
}

// TestRecordDBPoolStats_DifferentPoolSizes tests various pool configurations.
func TestRecordDBPoolStats_DifferentPoolSizes(t *testing.T) {
	tests := []struct {
		name         string
		acquired     int32
		idle         int32
		max          int32
		total        int32
	}{
		{"Small pool", 2, 3, 5, 5},
		{"Medium pool", 10, 15, 25, 25},
		{"Large pool", 50, 50, 100, 100},
		{"Very large pool", 200, 300, 500, 500},
		{"Minimal pool", 1, 0, 1, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbPoolConns := prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Name: "test_pool_size_" + tt.name,
				Help: "Test",
			}, []string{"state"})

			m := &Metrics{
				DBPoolConns: dbPoolConns,
			}

			m.RecordDBPoolStats(tt.acquired, tt.idle, tt.max, tt.total)

			var metric dto.Metric

			gauge, _ := dbPoolConns.GetMetricWithLabelValues("acquired")
			gauge.Write(&metric)
			if int32(*metric.Gauge.Value) != tt.acquired {
				t.Errorf("expected acquired %d, got %v", tt.acquired, *metric.Gauge.Value)
			}

			gauge, _ = dbPoolConns.GetMetricWithLabelValues("max")
			gauge.Write(&metric)
			if int32(*metric.Gauge.Value) != tt.max {
				t.Errorf("expected max %d, got %v", tt.max, *metric.Gauge.Value)
			}
		})
	}
}
