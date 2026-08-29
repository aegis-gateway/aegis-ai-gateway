// Copyright 2026 Atlantic Frontier Corporations LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package telemetry

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all Prometheus metrics for the AEGIS gateway.
type Metrics struct {
	RequestTotal       *prometheus.CounterVec
	RequestDurationMs  *prometheus.HistogramVec
	GatewayOverheadMs  *prometheus.HistogramVec
	TokensTotal        *prometheus.CounterVec
	CostUSDTotal       *prometheus.CounterVec
	FilterActionTotal  *prometheus.CounterVec
	RateLimitHitTotal  *prometheus.CounterVec
	DBPoolConns        *prometheus.GaugeVec
	DBPoolWaitDuration *prometheus.HistogramVec

	// Retry metrics
	RetryAttemptTotal *prometheus.CounterVec
	RetrySuccessTotal *prometheus.CounterVec
	RetryFailureTotal *prometheus.CounterVec

	// Context cancellation metrics
	CancellationTotal *prometheus.CounterVec

	// Validation metrics
	ValidationFailureTotal *prometheus.CounterVec

	// Policy reload metrics
	PolicyReloadTotal *prometheus.CounterVec

	// Unpriced-model metrics
	UnpricedRequestsTotal *prometheus.CounterVec

	// Requests refused because the routed provider cannot carry tools.
	ToolRequestsRefusedTotal *prometheus.CounterVec

	// Requests refused at decode for carrying an unsupported field.
	UnsupportedFieldTotal *prometheus.CounterVec

	// Pricing freshness gauge (set once at startup).
	PricingAgeDays prometheus.Gauge

	// Streaming metrics
	StreamingChunkTotal       *prometheus.CounterVec
	StreamingTimeToFirstToken *prometheus.HistogramVec
	StreamingTokensPerSecond  *prometheus.HistogramVec
	StreamingDurationMs       *prometheus.HistogramVec
	StreamingErrorTotal       *prometheus.CounterVec

	// Audit integrity metrics (refreshed every 5 minutes by the gateway).
	// AuditLastSealAgeSeconds is seconds since the most recent checkpoint's sealed_at.
	// AuditUnsealedEvents is the count of audit_events not yet covered by any checkpoint.
	AuditLastSealAgeSeconds prometheus.Gauge
	AuditUnsealedEvents     prometheus.Gauge

	// AuditOldestEventAgeDays is the age in days of the oldest row in audit_events.
	// A large value on a deployment with retention configured indicates a purge is overdue.
	// Set to -1 when the table is empty.
	AuditOldestEventAgeDays prometheus.Gauge

	// AuditWriteFailureTotal counts audit events that were not persisted,
	// labelled by event type.
	//
	// This is the only signal that the decision record is incomplete. A dropped
	// audit write leaves no row and no gap in the id sequence, because the
	// sequence only advances on a successful insert, so the sealer cannot
	// detect it: it seals a contiguous range that is missing an event it never
	// saw. Nothing downstream can reconstruct the loss either.
	//
	// Any non-zero value means the trail has holes and the completeness claim
	// does not hold for the affected window. Alert on any increase.
	AuditWriteFailureTotal *prometheus.CounterVec
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

		PolicyReloadTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_policy_reload_total",
			Help: "Total number of policy reload attempts.",
		}, []string{"status"}),

		UnpricedRequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_requests_unpriced_total",
			Help: "Total requests routed to a model with no pricing configuration. Incremented under deny and flag modes.",
		}, []string{"provider", "model", "mode"}),

		ToolRequestsRefusedTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_tool_requests_refused_total",
			Help: "Total tool-bearing requests refused because the routed provider's adapter cannot carry tools.",
		}, []string{"provider", "adapter"}),

		UnsupportedFieldTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_unsupported_field_total",
			Help: "Total requests refused at decode for carrying a request field AEGIS does not support, labelled by field name.",
		}, []string{"field"}),

		PricingAgeDays: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "aegis_pricing_age_days",
			Help: "Age in days of the pricing snapshot (computed from configs/pricing.yaml verified_at).",
		}),

		StreamingChunkTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_streaming_chunk_total",
			Help: "Total number of streaming chunks sent.",
		}, []string{"provider", "model"}),

		StreamingTimeToFirstToken: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "aegis_streaming_time_to_first_token_ms",
			Help:    "Time to first token in milliseconds for streaming requests.",
			Buckets: []float64{50, 100, 250, 500, 1000, 2000, 5000, 10000},
		}, []string{"provider", "model"}),

		StreamingTokensPerSecond: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "aegis_streaming_tokens_per_second",
			Help:    "Tokens per second during streaming.",
			Buckets: []float64{1, 5, 10, 20, 50, 100, 200, 500, 1000},
		}, []string{"provider", "model"}),

		StreamingDurationMs: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "aegis_streaming_duration_ms",
			Help:    "Total duration of streaming requests in milliseconds.",
			Buckets: []float64{1000, 5000, 10000, 30000, 60000, 120000, 300000},
		}, []string{"provider", "model"}),

		StreamingErrorTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_streaming_error_total",
			Help: "Total number of streaming errors.",
		}, []string{"provider", "error_type"}),

		AuditLastSealAgeSeconds: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "aegis_audit_last_seal_age_seconds",
			Help: "Seconds since the most recent audit checkpoint was written. " +
				"A large value indicates the sealer is not running; +Inf means " +
				"nothing has ever been sealed. Alert on a threshold — both " +
				"states exceed it.",
		}),

		AuditUnsealedEvents: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "aegis_audit_unsealed_events",
			Help: "Count of audit_events rows not yet covered by any checkpoint.",
		}),

		AuditOldestEventAgeDays: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "aegis_audit_oldest_event_age_days",
			Help: "Age in days of the oldest row in audit_events. " +
				"A large value on a deployment with retention configured indicates a purge is overdue. " +
				"Set to -1 when the table is empty.",
		}),

		AuditWriteFailureTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_audit_write_failure_total",
			Help: "Audit events that could not be written, by event type. " +
				"A dropped write leaves no row and no id gap, so the sealer " +
				"cannot detect it: any non-zero value means the decision " +
				"record is incomplete for that window. Alert on any increase.",
		}, []string{"event_type"}),
	}
}

// RecordAuditWriteFailure records an audit event that was not persisted.
//
// Nil-safe on the receiver: the audit logger is constructed in contexts that
// have no metrics registry, and losing the counter must not also lose the write
// attempt that was trying to report a loss.
func (m *Metrics) RecordAuditWriteFailure(eventType string) {
	if m == nil || m.AuditWriteFailureTotal == nil {
		return
	}
	m.AuditWriteFailureTotal.WithLabelValues(eventType).Inc()
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

// RecordToolRequestRefused records a tool-bearing request refused because the
// routed provider cannot express tools.
func (m *Metrics) RecordToolRequestRefused(provider, adapter string) {
	m.ToolRequestsRefusedTotal.WithLabelValues(provider, adapter).Inc()
}

// RecordUnsupportedField records a request refused at decode for an
// unsupported field.
//
// The caller must pass either a name AEGIS recognises or the literal "other".
// The refused field name comes from client input, and a metric label taken
// straight from client input is an unbounded-cardinality hole a caller can push
// on deliberately.
func (m *Metrics) RecordUnsupportedField(field string) {
	m.UnsupportedFieldTotal.WithLabelValues(field).Inc()
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

// StreamingLabels holds the label values for recording streaming metrics.
type StreamingLabels struct {
	Provider           string
	Model              string
	ChunkCount         int
	TimeToFirstTokenMs float64
	TokensPerSecond    float64
	StreamDurationMs   float64
}

// RecordStreamingMetrics records metrics for a completed streaming request.
func (m *Metrics) RecordStreamingMetrics(labels StreamingLabels) {
	m.StreamingChunkTotal.WithLabelValues(
		labels.Provider, labels.Model,
	).Add(float64(labels.ChunkCount))

	m.StreamingTimeToFirstToken.WithLabelValues(
		labels.Provider, labels.Model,
	).Observe(labels.TimeToFirstTokenMs)

	m.StreamingTokensPerSecond.WithLabelValues(
		labels.Provider, labels.Model,
	).Observe(labels.TokensPerSecond)

	m.StreamingDurationMs.WithLabelValues(
		labels.Provider, labels.Model,
	).Observe(labels.StreamDurationMs)
}

// RecordPolicyReload records a policy reload attempt.
func (m *Metrics) RecordPolicyReload(success bool) {
	if success {
		m.PolicyReloadTotal.WithLabelValues("success").Inc()
	} else {
		m.PolicyReloadTotal.WithLabelValues("error").Inc()
	}
}

// RecordStreamingError records a streaming error.
func (m *Metrics) RecordStreamingError(provider, errorType string) {
	m.StreamingErrorTotal.WithLabelValues(provider, errorType).Inc()
}
