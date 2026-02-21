package telemetry

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all Prometheus metrics for the AEGIS gateway.
type Metrics struct {
	RequestTotal      *prometheus.CounterVec
	RequestDurationMs *prometheus.HistogramVec
	GatewayOverheadMs *prometheus.HistogramVec
	TokensTotal       *prometheus.CounterVec
	CostUSDTotal      *prometheus.CounterVec
	FilterActionTotal *prometheus.CounterVec
}

// NewMetrics creates and registers all Prometheus metrics.
func NewMetrics() *Metrics {
	return &Metrics{
		RequestTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_request_total",
			Help: "Total number of requests processed by the gateway.",
		}, []string{"org", "team", "model", "provider", "status", "classification"}),

		RequestDurationMs: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "aegis_request_duration_ms",
			Help:    "Total request duration in milliseconds (including provider latency).",
			Buckets: []float64{50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000},
		}, []string{"model", "provider"}),

		GatewayOverheadMs: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "aegis_gateway_overhead_ms",
			Help:    "Gateway processing overhead in milliseconds (excluding provider latency).",
			Buckets: []float64{1, 2, 5, 10, 25, 50, 100, 250},
		}, []string{"org"}),

		TokensTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_tokens_total",
			Help: "Total tokens processed.",
		}, []string{"org", "team", "model", "direction"}),

		CostUSDTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_cost_usd_total",
			Help: "Estimated total cost in USD.",
		}, []string{"org", "team", "model", "provider"}),

		FilterActionTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_filter_action_total",
			Help: "Total filter actions taken.",
		}, []string{"filter", "action"}),
	}
}

// RecordRequest records metrics for a completed request.
func (m *Metrics) RecordRequest(labels RequestLabels) {
	m.RequestTotal.WithLabelValues(
		labels.Org, labels.Team, labels.Model, labels.Provider,
		labels.Status, labels.Classification,
	).Inc()

	m.RequestDurationMs.WithLabelValues(
		labels.Model, labels.Provider,
	).Observe(labels.DurationMs)

	m.GatewayOverheadMs.WithLabelValues(
		labels.Org,
	).Observe(labels.OverheadMs)

	if labels.PromptTokens > 0 {
		m.TokensTotal.WithLabelValues(
			labels.Org, labels.Team, labels.Model, "prompt",
		).Add(float64(labels.PromptTokens))
	}

	if labels.CompletionTokens > 0 {
		m.TokensTotal.WithLabelValues(
			labels.Org, labels.Team, labels.Model, "completion",
		).Add(float64(labels.CompletionTokens))
	}

	if labels.CostUSD > 0 {
		m.CostUSDTotal.WithLabelValues(
			labels.Org, labels.Team, labels.Model, labels.Provider,
		).Add(labels.CostUSD)
	}
}

// RecordFilterAction records a filter action metric.
func (m *Metrics) RecordFilterAction(filter, action string) {
	m.FilterActionTotal.WithLabelValues(filter, action).Inc()
}

// RequestLabels holds the label values for recording a request.
type RequestLabels struct {
	Org              string
	Team             string
	Model            string
	Provider         string
	Status           string
	Classification   string
	DurationMs       float64
	OverheadMs       float64
	PromptTokens     int
	CompletionTokens int
	CostUSD          float64
}
