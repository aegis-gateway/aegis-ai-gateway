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

package config

import "time"

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Database  DatabaseConfig  `yaml:"database"`
	Redis     RedisConfig     `yaml:"redis"`
	Telemetry TelemetryConfig `yaml:"telemetry"`
	Filter    FilterConfig    `yaml:"filter"`
	Routing   RoutingConfig   `yaml:"routing"`
	Cost      CostConfig      `yaml:"cost"`
	Audit     AuditConfig     `yaml:"audit"`
	// AnthropicMessages configures the /v1/messages ingress.
	AnthropicMessages AnthropicMessagesConfig `yaml:"anthropic_messages"`
}

// AnthropicMessagesConfig governs what the /v1/messages ingress forwards beyond
// the request body.
type AnthropicMessagesConfig struct {
	// AllowedBetas are anthropic-beta flags forwarded without comment.
	AllowedBetas []string `yaml:"allowed_betas"`
	// DeniedBetas are flags always refused, and sealed as feature_denied. An
	// entry ending in * matches by prefix, so "files-api-*" covers every
	// dated release of that feature.
	DeniedBetas []string `yaml:"denied_betas"`
	// UnlistedBetas decides a well-formed flag on neither list: "forward"
	// forwards it, logs it by name and counts it in
	// aegis_anthropic_beta_unlisted_total; "refuse" refuses it as
	// feature_denied. Any other value is treated as "refuse".
	//
	// Forward is the default because Claude Code's flags vary by release,
	// mode, command and install, so an exact list breaks it on every
	// release. It is safe as a default because a beta mostly switches on
	// request constructs (block types, tool types, fields), and the
	// /v1/messages decoder refuses every construct it does not allowlist
	// whatever flags are set. The flags that move data or execution to the
	// provider are refused by family in DeniedBetas.
	UnlistedBetas string `yaml:"unlisted_betas"`
	// StreamTotalTimeout bounds one streamed response. A coding agent's turn
	// can think and write for many minutes, far past the chat default.
	StreamTotalTimeout time.Duration `yaml:"stream_total_timeout"`
	// StreamIdleTimeout bounds the gap between two lines of the provider's
	// event stream. The provider sends ping events while it works, so a
	// healthy stream is never idle this long.
	StreamIdleTimeout time.Duration `yaml:"stream_idle_timeout"`
}

// Values of UnlistedBetas.
const (
	UnlistedBetasForward = "forward"
	UnlistedBetasRefuse  = "refuse"
)

// DefaultAnthropicBetas are the flags Claude Code 2.1.150 was observed sending,
// across print and interactive mode, the session-title request, /init, and
// installs with different server-side feature flags. context-1m changes the
// price of a request over 200K input tokens; it is priced through the
// long-context rates in configs/pricing.yaml.
var DefaultAnthropicBetas = []string{
	"claude-code-20250219",
	"context-1m-2025-08-07",
	"interleaved-thinking-2025-05-14",
	"context-management-2025-06-27",
	"prompt-caching-scope-2026-01-05",
	"effort-2025-11-24",
	"structured-outputs-2025-12-15",
	"redact-thinking-2026-02-12",
	"thinking-token-count-2026-05-13",
	"advanced-tool-use-2025-11-20",
	"thinking-display-updates-2026-08-18",
	"advisor-tool-2026-03-01",
}

// DefaultDeniedBetas are the feature families that move data or execution to
// the provider: uploaded files, provider-side MCP, code execution, web search
// and fetch, and skills containers. Their request constructs are refused by
// the decoder already; refusing the flag too makes the refusal explicit and
// sealed rather than a decode error.
var DefaultDeniedBetas = []string{
	"files-api-*",
	"mcp-client-*",
	"code-execution-*",
	"web-search-*",
	"web-fetch-*",
	"skills-*",
}

// CostConfig controls how the gateway handles pricing.
type CostConfig struct {
	// OnMissingPricing controls behaviour when a routed model has no pricing entry.
	// "deny"  — reject with 402 and write an audit log entry (default, safest).
	// "flag"  — proceed but emit an audit log entry and increment aegis_requests_unpriced_total.
	// "allow" — silent pass-through; preserved for dev/test environments only.
	OnMissingPricing string `yaml:"on_missing_pricing"`
	// PricingStalenessThresholdDays is how many days after pricing.yaml's verified_at
	// before a boot-time warning is logged. Default: 90.
	PricingStalenessThresholdDays int `yaml:"pricing_staleness_threshold_days"`
}

// AuditConfig controls audit log retention policy.
type AuditConfig struct {
	// RetentionDays is the minimum number of days to keep audit_logs and
	// audit_events rows. The purge subcommand enforces this floor; rows
	// newer than RetentionDays are never eligible for purge.
	RetentionDays int `yaml:"retention_days"`
}

type ServerConfig struct {
	Host             string        `yaml:"host"`
	Port             int           `yaml:"port"`
	ReadTimeout      time.Duration `yaml:"read_timeout"`
	WriteTimeout     time.Duration `yaml:"write_timeout"`
	IdleTimeout      time.Duration `yaml:"idle_timeout"`
	GracefulShutdown time.Duration `yaml:"graceful_shutdown"`
	// MaxRequestBytes caps a chat completion request body. A larger body is a
	// 413 before any of it is parsed. Zero means DefaultMaxRequestBytes. It is
	// sized for image parts, which arrive as base64 inside the JSON body: a
	// few phone photos resent with a conversation reach tens of megabytes.
	MaxRequestBytes int64 `yaml:"max_request_bytes"`
}

// DefaultMaxRequestBytes is the request body cap when none is configured.
const DefaultMaxRequestBytes int64 = 32 << 20

// RequestBodyLimit returns the configured body cap, or the default.
func (s ServerConfig) RequestBodyLimit() int64 {
	if s.MaxRequestBytes > 0 {
		return s.MaxRequestBytes
	}
	return DefaultMaxRequestBytes
}

type DatabaseConfig struct {
	Host            string        `yaml:"host"`
	Port            int           `yaml:"port"`
	Name            string        `yaml:"name"`
	User            string        `yaml:"user"`
	Password        string        `yaml:"password"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
}

func (d DatabaseConfig) DSN() string {
	return "postgres://" + d.User + ":" + d.Password + "@" + d.Host + ":" + itoa(d.Port) + "/" + d.Name + "?sslmode=disable"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

type RedisConfig struct {
	Addresses []string `yaml:"addresses"`
	Password  string   `yaml:"password"`
	DB        int      `yaml:"db"`
	PoolSize  int      `yaml:"pool_size"`
}

type TelemetryConfig struct {
	LogLevel        string  `yaml:"log_level"`
	LogFormat       string  `yaml:"log_format"`
	MetricsPort     int     `yaml:"metrics_port"`
	OTLPEndpoint    string  `yaml:"otlp_endpoint"`
	TraceSampleRate float64 `yaml:"trace_sample_rate"`
}

type FilterConfig struct {
	PIIService PIIServiceConfig      `yaml:"pii_service"`
	Secrets    SecretsFilterConfig   `yaml:"secrets"`
	Injection  InjectionFilterConfig `yaml:"injection"`
	Policy     PolicyFilterConfig    `yaml:"policy"`
}

type PIIServiceConfig struct {
	Enabled    bool          `yaml:"enabled"`
	Address    string        `yaml:"address"`
	Timeout    time.Duration `yaml:"timeout"`
	MaxRetries int           `yaml:"max_retries"`
	FailOpen   bool          `yaml:"fail_open"`
}

type SecretsFilterConfig struct {
	Enabled bool `yaml:"enabled"`
}

type InjectionFilterConfig struct {
	Enabled        bool    `yaml:"enabled"`
	BlockThreshold float64 `yaml:"block_threshold"`
	FlagThreshold  float64 `yaml:"flag_threshold"`
}

type PolicyFilterConfig struct {
	Enabled           bool          `yaml:"enabled"`
	BundlePath        string        `yaml:"bundle_path"`
	EvaluationTimeout time.Duration `yaml:"evaluation_timeout"`
}

type RoutingConfig struct {
	DefaultTimeout          time.Duration        `yaml:"default_timeout"`
	StreamFirstChunkTimeout time.Duration        `yaml:"stream_first_chunk_timeout"`
	StreamChunkTimeout      time.Duration        `yaml:"stream_chunk_timeout"`
	MaxRetries              int                  `yaml:"max_retries"`
	CircuitBreaker          CircuitBreakerConfig `yaml:"circuit_breaker"`
	HealthCheckInterval     time.Duration        `yaml:"health_check_interval"`
}

type CircuitBreakerConfig struct {
	FailureThreshold      int           `yaml:"failure_threshold"`
	ErrorRateThreshold    float64       `yaml:"error_rate_threshold"`
	ErrorRateWindow       time.Duration `yaml:"error_rate_window"`
	RecoveryProbeInterval time.Duration `yaml:"recovery_probe_interval"`
}

func DefaultConfig() *Config {
	return &Config{
		AnthropicMessages: AnthropicMessagesConfig{
			AllowedBetas:       append([]string(nil), DefaultAnthropicBetas...),
			DeniedBetas:        append([]string(nil), DefaultDeniedBetas...),
			UnlistedBetas:      UnlistedBetasForward,
			StreamTotalTimeout: 30 * time.Minute,
			StreamIdleTimeout:  60 * time.Second,
		},
		Server: ServerConfig{
			Host:             "0.0.0.0",
			Port:             8080,
			ReadTimeout:      30 * time.Second,
			WriteTimeout:     120 * time.Second,
			IdleTimeout:      120 * time.Second,
			GracefulShutdown: 30 * time.Second,
			MaxRequestBytes:  DefaultMaxRequestBytes,
		},
		Database: DatabaseConfig{
			Host:            "localhost",
			Port:            5432,
			Name:            "aegis",
			User:            "aegis",
			MaxOpenConns:    25,
			MaxIdleConns:    10,
			ConnMaxLifetime: 5 * time.Minute,
		},
		Redis: RedisConfig{
			Addresses: []string{"localhost:6379"},
			DB:        0,
			PoolSize:  50,
		},
		Telemetry: TelemetryConfig{
			LogLevel:        "info",
			LogFormat:       "json",
			MetricsPort:     9090,
			TraceSampleRate: 0.1,
		},
		Filter: FilterConfig{
			PIIService: PIIServiceConfig{
				Address:    "aegis-filter-nlp:50051",
				Timeout:    5 * time.Second,
				MaxRetries: 1,
			},
			Secrets: SecretsFilterConfig{Enabled: true},
			Injection: InjectionFilterConfig{
				Enabled:        true,
				BlockThreshold: 0.9,
				FlagThreshold:  0.7,
			},
			Policy: PolicyFilterConfig{
				Enabled:           true,
				BundlePath:        "/etc/aegis/policies",
				EvaluationTimeout: 100 * time.Millisecond,
			},
		},
		Routing: RoutingConfig{
			DefaultTimeout:          30 * time.Second,
			StreamFirstChunkTimeout: 60 * time.Second,
			StreamChunkTimeout:      10 * time.Second,
			MaxRetries:              2,
			CircuitBreaker: CircuitBreakerConfig{
				FailureThreshold:      5,
				ErrorRateThreshold:    0.5,
				ErrorRateWindow:       30 * time.Second,
				RecoveryProbeInterval: 15 * time.Second,
			},
			HealthCheckInterval: 10 * time.Second,
		},
		Cost: CostConfig{
			OnMissingPricing:              "deny",
			PricingStalenessThresholdDays: 90,
		},
		Audit: AuditConfig{
			RetentionDays: 365,
		},
	}
}
