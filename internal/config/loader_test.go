package config

import (
	"os"
	"testing"
)

func TestExpandEnvVars(t *testing.T) {
	os.Setenv("TEST_VAR", "hello")
	defer os.Unsetenv("TEST_VAR")

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

func TestLoadFile(t *testing.T) {
	// Create a temp YAML file
	tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())

	content := `
server:
  host: "0.0.0.0"
  port: 9999
`
	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()

	var cfg Config
	if err := LoadFile(tmpFile.Name(), &cfg); err != nil {
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
	os.Setenv("TEST_PORT", "7777")
	defer os.Unsetenv("TEST_PORT")

	tmpFile, err := os.CreateTemp("", "test-config-env-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())

	content := `
server:
  host: "${TEST_HOST:127.0.0.1}"
  port: ${TEST_PORT}
`
	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()

	var cfg Config
	if err := LoadFile(tmpFile.Name(), &cfg); err != nil {
		t.Fatalf("LoadFile failed: %v", err)
	}

	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("expected host 127.0.0.1 (default), got %s", cfg.Server.Host)
	}
	if cfg.Server.Port != 7777 {
		t.Errorf("expected port 7777, got %d", cfg.Server.Port)
	}
}
