package telemetry

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestNewMetrics(t *testing.T) {
	m := NewMetrics()

	if m.RequestTotal == nil {
		t.Error("RequestTotal should not be nil")
	}
	if m.RequestDurationMs == nil {
		t.Error("RequestDurationMs should not be nil")
	}
	if m.GatewayOverheadMs == nil {
		t.Error("GatewayOverheadMs should not be nil")
	}
	if m.TokensTotal == nil {
		t.Error("TokensTotal should not be nil")
	}
	if m.CostUSDTotal == nil {
		t.Error("CostUSDTotal should not be nil")
	}
	if m.FilterActionTotal == nil {
		t.Error("FilterActionTotal should not be nil")
	}
}

func TestRecordRequest(t *testing.T) {
	// Use a fresh registry to avoid polluting the default one
	reg := prometheus.NewRegistry()

	requestTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "test_aegis_request_total",
		Help: "Test counter",
	}, []string{"org", "team", "model", "provider", "status", "classification"})

	tokensTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "test_aegis_tokens_total",
		Help: "Test counter",
	}, []string{"org", "team", "model", "direction"})

	durationMs := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "test_aegis_request_duration_ms",
		Help:    "Test histogram",
		Buckets: []float64{100, 500, 1000},
	}, []string{"model", "provider"})

	overheadMs := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "test_aegis_gateway_overhead_ms",
		Help:    "Test histogram",
		Buckets: []float64{5, 10, 50},
	}, []string{"org"})

	costTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "test_aegis_cost_usd_total",
		Help: "Test counter",
	}, []string{"org", "team", "model", "provider"})

	filterTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "test_aegis_filter_action_total",
		Help: "Test counter",
	}, []string{"filter", "action"})

	reg.MustRegister(requestTotal, tokensTotal, durationMs, overheadMs, costTotal, filterTotal)

	m := &Metrics{
		RequestTotal:      requestTotal,
		RequestDurationMs: durationMs,
		GatewayOverheadMs: overheadMs,
		TokensTotal:       tokensTotal,
		CostUSDTotal:      costTotal,
		FilterActionTotal: filterTotal,
	}

	m.RecordRequest(RequestLabels{
		Org:              "org-1",
		Team:             "team-1",
		Model:            "gpt-4o",
		Provider:         "openai",
		Status:           "200",
		Classification:   "INTERNAL",
		DurationMs:       150,
		OverheadMs:       5,
		PromptTokens:     100,
		CompletionTokens: 50,
		CostUSD:          0.005,
	})

	// Verify request counter incremented
	counter, err := requestTotal.GetMetricWithLabelValues("org-1", "team-1", "gpt-4o", "openai", "200", "INTERNAL")
	if err != nil {
		t.Fatalf("failed to get metric: %v", err)
	}
	var metric dto.Metric
	counter.Write(&metric)
	if *metric.Counter.Value != 1 {
		t.Errorf("expected request count 1, got %v", *metric.Counter.Value)
	}

	// Verify tokens recorded
	promptCounter, _ := tokensTotal.GetMetricWithLabelValues("org-1", "team-1", "gpt-4o", "prompt")
	promptCounter.Write(&metric)
	if *metric.Counter.Value != 100 {
		t.Errorf("expected 100 prompt tokens, got %v", *metric.Counter.Value)
	}
}

func TestRecordFilterAction(t *testing.T) {
	filterTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "test_filter_action",
		Help: "Test",
	}, []string{"filter", "action"})

	m := &Metrics{FilterActionTotal: filterTotal}
	m.RecordFilterAction("secrets", "block")

	counter, _ := filterTotal.GetMetricWithLabelValues("secrets", "block")
	var metric dto.Metric
	counter.Write(&metric)
	if *metric.Counter.Value != 1 {
		t.Errorf("expected filter action count 1, got %v", *metric.Counter.Value)
	}
}
