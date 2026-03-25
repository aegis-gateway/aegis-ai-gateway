package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExpandEnvVars(t *testing.T) {
	t.Setenv("TEST_VAR", "hello")

	tests := []struct {
		input    string
		expected string
	}{
		{"${TEST_VAR}", "hello"},
		{"${TEST_VAR:default}", "hello"},
		{"${UNSET_VAR:fallback}", "fallback"},
		{"${UNSET_VAR}", ""},
		{"no vars here", "no vars here"},
		{"prefix-${TEST_VAR}-suffix", "prefix-hello-suffix"},
	}

	for _, tt := range tests {
		got := expandEnvVars(tt.input)
		if got != tt.expected {
			t.Errorf("expandEnvVars(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "config.yaml", `
server:
  host: "0.0.0.0"
  port: 9999
`)

	var cfg Config
	if err := LoadFile(path, &cfg); err != nil {
		t.Fatalf("LoadFile failed: %v", err)
	}

	if cfg.Server.Port != 9999 {
		t.Errorf("expected port 9999, got %d", cfg.Server.Port)
	}
	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("expected host 0.0.0.0, got %s", cfg.Server.Host)
	}
}

func TestLoadFile_WithEnvVars(t *testing.T) {
	t.Setenv("TEST_PORT", "7777")

	dir := t.TempDir()
	path := writeTestFile(t, dir, "config.yaml", `
server:
  host: "${TEST_HOST:127.0.0.1}"
  port: ${TEST_PORT}
`)

	var cfg Config
	if err := LoadFile(path, &cfg); err != nil {
		t.Fatalf("LoadFile failed: %v", err)
	}

	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("expected host 127.0.0.1 (default), got %s", cfg.Server.Host)
	}
	if cfg.Server.Port != 7777 {
		t.Errorf("expected port 7777, got %d", cfg.Server.Port)
	}
}

func TestLoadFile_FileNotFound(t *testing.T) {
	var cfg Config
	err := LoadFile("/nonexistent/path.yaml", &cfg)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadFile_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "bad.yaml", "{{{{not valid yaml")

	var cfg Config
	if err := LoadFile(path, &cfg); err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Server.Port != 8080 {
		t.Errorf("expected default port 8080, got %d", cfg.Server.Port)
	}
	if cfg.Server.ReadTimeout != 30*time.Second {
		t.Errorf("expected ReadTimeout 30s, got %v", cfg.Server.ReadTimeout)
	}
	if cfg.Database.MaxOpenConns != 25 {
		t.Errorf("expected MaxOpenConns 25, got %d", cfg.Database.MaxOpenConns)
	}
	if cfg.Routing.MaxRetries != 2 {
		t.Errorf("expected MaxRetries 2, got %d", cfg.Routing.MaxRetries)
	}
	if !cfg.Filter.Secrets.Enabled {
		t.Error("expected secrets filter enabled by default")
	}
}

func TestDatabaseConfig_DSN(t *testing.T) {
	db := DatabaseConfig{
		Host:     "db.example.com",
		Port:     5432,
		Name:     "mydb",
		User:     "admin",
		Password: "secret",
	}
	expected := "postgres://admin:secret@db.example.com:5432/mydb?sslmode=disable"
	if got := db.DSN(); got != expected {
		t.Errorf("DSN() = %q, want %q", got, expected)
	}
}

func TestLoader_LoadAndAccessors(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "gateway.yaml", "server:\n  host: \"0.0.0.0\"\n  port: 3000\n")
	writeTestFile(t, dir, "models.yaml", "models:\n  gpt-4o:\n    primary:\n      provider: openai\n      model: gpt-4o\n")
	writeTestFile(t, dir, "providers.yaml", "providers:\n  openai:\n    name: openai\n    base_url: https://api.openai.com/v1\n")

	logger := slog.Default()
	loader := NewLoader(dir, logger)

	if err := loader.Load(); err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	cfg := loader.Config()
	if cfg == nil {
		t.Fatal("Config() returned nil")
	}
	if cfg.Server.Port != 3000 {
		t.Errorf("expected port 3000, got %d", cfg.Server.Port)
	}

	if loader.Models() == nil {
		t.Fatal("Models() returned nil")
	}
	if loader.Providers() == nil {
		t.Fatal("Providers() returned nil")
	}
}

func TestLoader_OnReload(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "gateway.yaml", "server:\n  port: 1111\n")
	writeTestFile(t, dir, "models.yaml", "models: {}\n")
	writeTestFile(t, dir, "providers.yaml", "providers: {}\n")

	logger := slog.Default()
	loader := NewLoader(dir, logger)
	if err := loader.Load(); err != nil {
		t.Fatalf("initial Load() failed: %v", err)
	}

	called := false
	loader.OnReload(func() { called = true })

	// Manually trigger reload
	if err := loader.Load(); err != nil {
		t.Fatalf("reload Load() failed: %v", err)
	}
	for _, fn := range loader.watchers {
		fn()
	}

	if !called {
		t.Error("OnReload callback was not invoked")
	}
}

func TestLoader_LoadMissingFile(t *testing.T) {
	logger := slog.Default()
	loader := NewLoader("/nonexistent/dir", logger)
	err := loader.Load()
	if err == nil {
		t.Fatal("expected error for missing config directory")
	}
}
