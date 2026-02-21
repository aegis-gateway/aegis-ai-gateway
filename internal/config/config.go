package config

import "time"

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Database  DatabaseConfig  `yaml:"database"`
	Redis     RedisConfig     `yaml:"redis"`
	Telemetry TelemetryConfig `yaml:"telemetry"`
	Filter    FilterConfig    `yaml:"filter"`
	Routing   RoutingConfig   `yaml:"routing"`
}

type ServerConfig struct {
	Host             string        `yaml:"host"`
	Port             int           `yaml:"port"`
	ReadTimeout      time.Duration `yaml:"read_timeout"`
	WriteTimeout     time.Duration `yaml:"write_timeout"`
	IdleTimeout      time.Duration `yaml:"idle_timeout"`
	GracefulShutdown time.Duration `yaml:"graceful_shutdown"`
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
	PIIService PIIServiceConfig       `yaml:"pii_service"`
	Secrets    SecretsFilterConfig    `yaml:"secrets"`
	Injection  InjectionFilterConfig  `yaml:"injection"`
	Policy     PolicyFilterConfig     `yaml:"policy"`
}

type PIIServiceConfig struct {
	Address    string        `yaml:"address"`
	Timeout    time.Duration `yaml:"timeout"`
	MaxRetries int           `yaml:"max_retries"`
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
	DefaultTimeout          time.Duration      `yaml:"default_timeout"`
	StreamFirstChunkTimeout time.Duration      `yaml:"stream_first_chunk_timeout"`
	StreamChunkTimeout      time.Duration      `yaml:"stream_chunk_timeout"`
	MaxRetries              int                `yaml:"max_retries"`
	CircuitBreaker          CircuitBreakerConfig `yaml:"circuit_breaker"`
	HealthCheckInterval     time.Duration      `yaml:"health_check_interval"`
}

type CircuitBreakerConfig struct {
	FailureThreshold      int           `yaml:"failure_threshold"`
	ErrorRateThreshold    float64       `yaml:"error_rate_threshold"`
	ErrorRateWindow       time.Duration `yaml:"error_rate_window"`
	RecoveryProbeInterval time.Duration `yaml:"recovery_probe_interval"`
}

func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Host:             "0.0.0.0",
			Port:             8080,
			ReadTimeout:      30 * time.Second,
			WriteTimeout:     120 * time.Second,
			IdleTimeout:      120 * time.Second,
			GracefulShutdown: 30 * time.Second,
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
	}
}
