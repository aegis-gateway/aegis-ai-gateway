# AEGIS AI Gateway - Testing Guide

This guide covers all aspects of testing the AEGIS AI Gateway, including unit tests, integration tests, and performance testing.

---

## Table of Contents

1. [Test Overview](#test-overview)
2. [Running Tests](#running-tests)
3. [Test Infrastructure](#test-infrastructure)
4. [Writing Tests](#writing-tests)
5. [Test Coverage](#test-coverage)
6. [Continuous Integration](#continuous-integration)

---

## Test Overview

### Test Types

**Unit Tests**:
- Test individual functions and components in isolation
- Fast execution (<1 second per test)
- No external dependencies (mocked)
- Run on every commit

**Integration Tests**:
- Test complete request lifecycle
- Real database and Redis (via testcontainers or local)
- Mock provider responses
- Run before deployment

**Performance Tests**:
- Load testing with concurrent requests
- Streaming performance under load
- Resource usage monitoring
- Run periodically and before releases

---

## Running Tests

### Prerequisites

```bash
# Install Go 1.21+
go version

# For integration tests, ensure PostgreSQL and Redis are running
# Option 1: Use Docker Compose
docker-compose -f deploy/docker-compose.test.yml up -d

# Option 2: Use local instances
# PostgreSQL on localhost:5432
# Redis on localhost:6379

# Set environment variables for test instances
export TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/aegis_test?sslmode=disable"
export TEST_REDIS_URL="localhost:6379"
```

### Running Unit Tests

```bash
# Run all unit tests
go test ./...

# Run tests for specific package
go test ./internal/gateway/

# Run with verbose output
go test -v ./internal/gateway/

# Run specific test
go test -run TestStreamMetricsTracking ./internal/gateway/

# Run with coverage
go test -cover ./...

# Generate coverage report
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out -o coverage.html
```

### Running Integration Tests

Integration tests require the `integration` build tag and running PostgreSQL/Redis instances.

```bash
# Run all integration tests
go test -tags=integration ./internal/gateway/

# Run with verbose output
go test -tags=integration -v ./internal/gateway/

# Run specific integration test
go test -tags=integration -run TestFullRequestLifecycle ./internal/gateway/

# Run with timeout (default 10m might be too short)
go test -tags=integration -timeout 30m ./internal/gateway/
```

### Running Specific Test Suites

```bash
# Streaming tests
go test -run TestStream ./internal/gateway/

# Request processor tests
go test -run TestRequest ./internal/gateway/

# Retry logic tests
go test -run TestRetry ./internal/retry/

# Validation tests
go test -run TestValidat ./internal/validation/

# Cost calculation tests
go test -run TestCost ./internal/cost/
```

### Quick Test Commands

```bash
# Fast: Run only unit tests (no integration)
make test-unit

# Full: Run all tests including integration
make test-all

# Coverage: Generate coverage report
make test-coverage

# Watch: Re-run tests on file changes (requires entr)
make test-watch
```

---

## Test Infrastructure

### Test Environment Setup

Integration tests use a test environment with all dependencies:

```go
// Setup test environment
env := SetupTestEnv(t)
defer env.Cleanup()

// Access components
db := env.DB              // PostgreSQL connection pool
redis := env.Redis        // Redis client
provider := env.MockProvider  // Mock provider server
handler := env.Handler    // Configured gateway handler
metrics := env.Metrics    // Prometheus metrics
```

### Mock Provider Server

The mock provider server simulates LLM provider APIs:

```go
// Configure mock provider
env.MockProvider.Response = &types.AegisResponse{
    Model: "gpt-4",
    Choices: []types.Choice{...},
    Usage: types.Usage{...},
}

// Configure streaming
env.MockProvider.StreamChunks = []string{
    `{"model":"gpt-4","choices":[{"delta":{"content":"Hello"}}]}`,
    `{"model":"gpt-4","choices":[{"delta":{"content":" world"}}]}`,
}

// Simulate failures
env.MockProvider.ShouldFail = true
env.MockProvider.StatusCode = http.StatusInternalServerError

// Add response delay
env.MockProvider.ResponseDelay = 500 * time.Millisecond
```

### Database and Redis

**Using Docker Compose**:

```yaml
# deploy/docker-compose.test.yml
version: '3.8'
services:
  postgres-test:
    image: postgres:16
    environment:
      POSTGRES_DB: aegis_test
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: postgres
    ports:
      - "5432:5432"

  redis-test:
    image: redis:7
    ports:
      - "6379:6379"
```

**Using Testcontainers** (Alternative):

```go
// Optional: Use testcontainers for fully isolated tests
import "github.com/testcontainers/testcontainers-go"

func SetupTestEnvWithContainers(t *testing.T) *TestEnv {
    // Start PostgreSQL container
    postgresContainer, err := testcontainers.GenericContainer(...)
    
    // Start Redis container
    redisContainer, err := testcontainers.GenericContainer(...)
    
    // ... rest of setup
}
```

---

## Writing Tests

### Unit Test Example

```go
func TestStreamMetricsTracking(t *testing.T) {
    // Arrange
    config := DefaultStreamingConfig()
    handler := &Handler{metrics: telemetry.NewMetrics()}
    streamingHandler := NewStreamingHandler(handler, config)

    // Act
    metrics := &StreamMetrics{}
    chunk := []byte(`{"model":"gpt-4","usage":{"prompt_tokens":100}}`)
    err := streamingHandler.extractTokensFromChunk(chunk, metrics)

    // Assert
    if err != nil {
        t.Fatalf("Unexpected error: %v", err)
    }
    if metrics.PromptTokens != 100 {
        t.Errorf("Expected 100 prompt tokens, got %d", metrics.PromptTokens)
    }
}
```

### Integration Test Example

```go
func TestFullRequestLifecycle(t *testing.T) {
    // Setup
    env := SetupTestEnv(t)
    defer env.Cleanup()

    // Create request
    req := httptest.NewRequest("POST", "/v1/chat/completions", body)
    authInfo := &auth.AuthInfo{...}
    ctx := auth.NewContextWithAuth(req.Context(), authInfo)
    req = req.WithContext(ctx)

    w := httptest.NewRecorder()

    // Execute
    env.Handler.ChatCompletions(w, req)

    // Assert
    if w.Code != http.StatusOK {
        t.Errorf("Expected 200, got %d: %s", w.Code, w.Body.String())
    }

    var response types.AegisResponse
    json.NewDecoder(w.Body).Decode(&response)

    if response.Model != "gpt-4" {
        t.Errorf("Expected model gpt-4, got %s", response.Model)
    }
}
```

### Table-Driven Tests

```go
func TestValidation(t *testing.T) {
    tests := []struct {
        name        string
        input       *types.AegisRequest
        expectError bool
        errorMsg    string
    }{
        {
            name:        "valid request",
            input:       &types.AegisRequest{Model: "gpt-4", Messages: [...]},
            expectError: false,
        },
        {
            name:        "missing model",
            input:       &types.AegisRequest{Messages: [...]},
            expectError: true,
            errorMsg:    "model is required",
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := validator.Validate(tt.input)
            
            if tt.expectError {
                if err == nil {
                    t.Fatal("Expected error, got nil")
                }
                if tt.errorMsg != "" && err.Error() != tt.errorMsg {
                    t.Errorf("Expected '%s', got '%s'", tt.errorMsg, err.Error())
                }
            } else {
                if err != nil {
                    t.Fatalf("Unexpected error: %v", err)
                }
            }
        })
    }
}
```

### Concurrent Testing

```go
func TestConcurrency(t *testing.T) {
    concurrency := 100
    done := make(chan bool, concurrency)

    for i := 0; i < concurrency; i++ {
        go func(id int) {
            defer func() { done <- true }()
            
            // Execute concurrent request
            result := processRequest(id)
            
            // Verify result
            if result.Error != nil {
                t.Errorf("Request %d failed: %v", id, result.Error)
            }
        }(i)
    }

    // Wait for all goroutines
    timeout := time.After(30 * time.Second)
    for i := 0; i < concurrency; i++ {
        select {
        case <-done:
            // Request completed
        case <-timeout:
            t.Fatal("Timeout waiting for concurrent requests")
        }
    }
}
```

---

## Test Coverage

### Generating Coverage Reports

```bash
# Generate coverage profile
go test -coverprofile=coverage.out ./...

# View coverage in terminal
go tool cover -func=coverage.out

# Generate HTML report
go tool cover -html=coverage.out -o coverage.html

# Open in browser
open coverage.html  # macOS
xdg-open coverage.html  # Linux
```

### Coverage Targets

**Overall Target**: >80% code coverage

**Component Targets**:
- `internal/gateway/`: >85%
- `internal/router/`: >80%
- `internal/auth/`: >90%
- `internal/validation/`: >95%
- `internal/cost/`: >90%
- `internal/retry/`: >85%
- `internal/filter/`: >75%

### Viewing Coverage by Package

```bash
# Show coverage by package
go test -cover ./...

# Example output:
ok      github.com/af-corp/aegis-gateway/internal/gateway    0.523s  coverage: 87.3% of statements
ok      github.com/af-corp/aegis-gateway/internal/auth       0.234s  coverage: 92.1% of statements
ok      github.com/af-corp/aegis-gateway/internal/validation 0.145s  coverage: 96.5% of statements
```

---

## Continuous Integration

### GitHub Actions Workflow

```yaml
# .github/workflows/test.yml
name: Tests

on: [push, pull_request]

jobs:
  unit-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v4
        with:
          go-version: '1.21'
      - run: go test -v -cover ./...

  integration-tests:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:16
        env:
          POSTGRES_DB: aegis_test
          POSTGRES_USER: postgres
          POSTGRES_PASSWORD: postgres
        ports:
          - 5432:5432
      redis:
        image: redis:7
        ports:
          - 6379:6379

    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v4
        with:
          go-version: '1.21'
      - run: go test -tags=integration -v ./internal/gateway/
        env:
          TEST_DATABASE_URL: postgres://postgres:postgres@localhost:5432/aegis_test?sslmode=disable
          TEST_REDIS_URL: localhost:6379
```

### Pre-commit Hooks

```bash
# .git/hooks/pre-commit
#!/bin/bash

# Run tests before committing
echo "Running tests..."
go test ./...

if [ $? -ne 0 ]; then
    echo "Tests failed! Commit aborted."
    exit 1
fi

echo "All tests passed!"
```

---

## Performance Testing

### Load Testing

```bash
# Install vegeta (HTTP load testing tool)
go install github.com/tsenart/vegeta@latest

# Run load test
echo "POST http://localhost:8080/v1/chat/completions" | vegeta attack \
  -duration=60s \
  -rate=100/s \
  -header="Authorization: Bearer sk-test-key" \
  -header="Content-Type: application/json" \
  -body='{"model":"gpt-4","messages":[{"role":"user","content":"Test"}]}' \
  | vegeta report

# Generate metrics
vegeta attack ... | vegeta report -type=text
vegeta attack ... | vegeta report -type=json > results.json
vegeta attack ... | vegeta plot > plot.html
```

### Benchmarking

```go
// Add benchmarks to test files
func BenchmarkStreamingHandler(b *testing.B) {
    env := SetupTestEnv(b)
    defer env.Cleanup()

    req := createTestRequest()
    
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        w := httptest.NewRecorder()
        env.Handler.ChatCompletions(w, req)
    }
}
```

Run benchmarks:

```bash
# Run all benchmarks
go test -bench=. ./...

# Run specific benchmark
go test -bench=BenchmarkStreaming ./internal/gateway/

# With memory profiling
go test -bench=. -benchmem ./...

# CPU profiling
go test -bench=. -cpuprofile=cpu.prof ./...
go tool pprof cpu.prof
```

---

## Troubleshooting

### Common Issues

**Issue**: Tests fail with "connection refused" to PostgreSQL  
**Solution**: Ensure PostgreSQL is running on localhost:5432 or set `TEST_DATABASE_URL`

**Issue**: Integration tests timeout  
**Solution**: Increase timeout with `-timeout 30m` flag

**Issue**: Race conditions detected  
**Solution**: Run with race detector: `go test -race ./...`

**Issue**: Tests pass locally but fail in CI  
**Solution**: Check environment variables and service availability in CI

### Debugging Tests

```bash
# Enable verbose logging
go test -v ./...

# Run with race detector
go test -race ./...

# Run single test with detailed output
go test -v -run TestSpecificTest ./package/

# Debug with delve
dlv test ./internal/gateway/ -- -test.run TestFullRequestLifecycle
```

---

## Best Practices

1. **Isolation**: Each test should be independent and not rely on other tests
2. **Cleanup**: Always defer cleanup functions to prevent resource leaks
3. **Determinism**: Tests should be deterministic and not rely on timing
4. **Fast Feedback**: Unit tests should run quickly (<1s per test)
5. **Clear Names**: Test names should describe what they test
6. **Table-Driven**: Use table-driven tests for multiple similar cases
7. **Mock Wisely**: Mock external dependencies but test real integration paths
8. **Coverage**: Aim for >80% coverage but prioritize critical paths
9. **CI Integration**: Run tests on every commit and before merge
10. **Documentation**: Document complex test setups and edge cases

---

## Resources

- [Go Testing Documentation](https://pkg.go.dev/testing)
- [Testcontainers Go](https://golang.testcontainers.org/)
- [Vegeta Load Testing](https://github.com/tsenart/vegeta)
- [Go Test Coverage](https://go.dev/blog/cover)
- [Table-Driven Tests in Go](https://dave.cheney.net/2019/05/07/prefer-table-driven-tests)

---

**Last Updated**: Sprint 7-10 (2026-03-22)  
**Maintainer**: AEGIS Team
