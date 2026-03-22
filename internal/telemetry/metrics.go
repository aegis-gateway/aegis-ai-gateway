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
	RateLimitHitTotal *prometheus.CounterVec
	DBPoolConns       *prometheus.GaugeVec
	DBPoolWaitDuration *prometheus.HistogramVec
	
	// Retry metrics
	RetryAttemptTotal *prometheus.CounterVec
	RetrySuccessTotal *prometheus.CounterVec
	RetryFailureTotal *prometheus.CounterVec
	
	// Context cancellation metrics
	CancellationTotal *prometheus.CounterVec
	
	// Validation metrics
	ValidationFailureTotal *prometheus.CounterVec
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

		RateLimitHitTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_rate_limit_hit_total",
			Help: "Total rate limit hits.",
		}, []string{"dimension", "id"}),

		DBPoolConns: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "aegis_db_pool_conns",
			Help: "Database connection pool statistics.",
		}, []string{"state"}),

		DBPoolWaitDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "aegis_db_pool_wait_duration_ms",
			Help:    "Time spent waiting for a database connection in milliseconds.",
			Buckets: []float64{1, 2, 5, 10, 25, 50, 100, 250, 500, 1000},
		}, []string{}),
		
		RetryAttemptTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_retry_attempt_total",
			Help: "Total number of retry attempts.",
		}, []string{"provider", "attempt"}),
		
		RetrySuccessTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_retry_success_total",
			Help: "Total number of successful retries.",
		}, []string{"provider", "attempt"}),
		
		RetryFailureTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_retry_failure_total",
			Help: "Total number of failed retries.",
		}, []string{"provider", "reason"}),
		
		CancellationTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_cancellation_total",
			Help: "Total number of cancelled requests.",
		}, []string{"provider", "stage"}),
		
		ValidationFailureTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_validation_failure_total",
			Help: "Total number of validation failures.",
		}, []string{"field"}),
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

// RecordRateLimitHit records a rate limit hit.
func (m *Metrics) RecordRateLimitHit(dimension, id string) {
	m.RateLimitHitTotal.WithLabelValues(dimension, id).Inc()
}

// RecordFilterAction records a filter action metric.
func (m *Metrics) RecordFilterAction(filter, action string) {
	m.FilterActionTotal.WithLabelValues(filter, action).Inc()
}

// RecordDBPoolStats records database pool statistics.
func (m *Metrics) RecordDBPoolStats(acquiredConns, idleConns, maxConns, totalConns int32) {
	m.DBPoolConns.WithLabelValues("acquired").Set(float64(acquiredConns))
	m.DBPoolConns.WithLabelValues("idle").Set(float64(idleConns))
	m.DBPoolConns.WithLabelValues("max").Set(float64(maxConns))
	m.DBPoolConns.WithLabelValues("total").Set(float64(totalConns))
}

// RecordRetryAttempt records a retry attempt.
func (m *Metrics) RecordRetryAttempt(provider string, attempt int) {
	m.RetryAttemptTotal.WithLabelValues(provider, itoa(attempt)).Inc()
}

// RecordRetrySuccess records a successful retry.
func (m *Metrics) RecordRetrySuccess(provider string, attempt int) {
	m.RetrySuccessTotal.WithLabelValues(provider, itoa(attempt)).Inc()
}

// RecordRetryFailure records a failed retry.
func (m *Metrics) RecordRetryFailure(provider string, attempt int, reason string) {
	m.RetryFailureTotal.WithLabelValues(provider, reason).Inc()
}

// RecordCancellation records a cancelled request.
func (m *Metrics) RecordCancellation(provider, stage string) {
	m.CancellationTotal.WithLabelValues(provider, stage).Inc()
}

// RecordValidationFailure records a validation failure.
func (m *Metrics) RecordValidationFailure(field string) {
	m.ValidationFailureTotal.WithLabelValues(field).Inc()
}

// itoa converts an integer to a string (simple implementation for metrics).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	negative := i < 0
	if negative {
		i = -i
	}
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	if negative {
		s = "-" + s
	}
	return s
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
