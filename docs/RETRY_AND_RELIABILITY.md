# Retry Logic and Reliability Features

This document describes the retry logic, context cancellation, and input validation features implemented in Sprint 5-6.

## Table of Contents

1. [Context Cancellation](#context-cancellation)
2. [Retry Logic](#retry-logic)
3. [Input Validation](#input-validation)
4. [Configuration](#configuration)
5. [Metrics](#metrics)
6. [Troubleshooting](#troubleshooting)

---

## Context Cancellation

### Overview

Context cancellation ensures that when a client disconnects or cancels their request, the gateway immediately stops processing and cancels any ongoing upstream API calls. This prevents:

- **Wasted API costs** from completing requests that the client no longer needs
- **Zombie requests** that continue processing after the client has gone
- **Resource leaks** from goroutines that don't properly clean up

### How It Works

1. **Context Propagation**: The HTTP request context is propagated through the entire request chain:
   - From the HTTP handler
   - Through the retry executor
   - To the provider adapter
   - Into the upstream HTTP request

2. **Cancellation Monitoring**: A context monitor watches for cancellation and logs when it occurs:
   ```go
   cleanup := h.contextMonitor.Watch(r.Context(), requestID, provider)
   defer cleanup()
   ```

3. **Automatic Cleanup**: When context is cancelled:
   - The retry executor stops immediately (no more retry attempts)
   - The upstream HTTP request is cancelled
   - Resources are cleaned up via defer statements
   - Metrics are recorded for observability

### Metrics

- `aegis_cancellation_total{provider,stage}`: Total cancelled requests
  - `stage="before_attempt"`: Cancelled before starting a retry attempt
  - `stage="during_backoff"`: Cancelled while waiting between retries
  - `stage="client_disconnect"`: Cancelled due to client disconnection

### Testing

To test context cancellation:

```bash
# Start a request and immediately cancel it (Ctrl+C)
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer YOUR_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}' &
PID=$!
sleep 0.1
kill $PID

# Check metrics
curl http://localhost:9090/metrics | grep aegis_cancellation_total
```

---

## Retry Logic

### Overview

The retry system automatically retries failed requests using **exponential backoff with jitter**. This improves reliability against transient failures while preventing thundering herd problems.

### Retry Behavior

#### Retryable Errors

The following errors are automatically retried:

- **5xx HTTP errors**: 500, 502, 503, 504
- **429 Too Many Requests**: Rate limit errors
- **408 Request Timeout**
- **Network errors**: Connection refused, connection reset, timeouts
- **Syscall errors**: `ECONNREFUSED`, `ECONNRESET`, `ETIMEDOUT`

#### Non-Retryable Errors

These errors fail immediately without retrying:

- **4xx HTTP errors**: 400, 401, 403, 404 (client errors)
- **Context cancellation**: Request was cancelled by the client
- **Circuit breaker open**: Provider circuit breaker is open

### Backoff Algorithm

The retry executor uses **exponential backoff with jitter**:

```
backoff = initialBackoff * (multiplier ^ attempt)
backoff = min(backoff, maxBackoff)
backoff = backoff ± (jitter * backoff)
```

**Default values:**
- Initial backoff: 100ms
- Backoff multiplier: 2.0
- Max backoff: 5 seconds
- Jitter fraction: 0.1 (10%)

**Example backoff sequence:**
- Attempt 1: ~100ms (90-110ms with jitter)
- Attempt 2: ~200ms (180-220ms)
- Attempt 3: ~400ms (360-440ms)
- Attempt 4: ~800ms (720-880ms)
- Attempt 5: ~1600ms (1440-1760ms)
- Attempt 6+: ~5000ms (4500-5500ms, capped)

### Circuit Breaker Integration

The retry logic integrates with the circuit breaker:

- **Circuit closed**: Normal retry behavior
- **Circuit open**: Requests fail immediately with `ErrCircuitOpen` (no retries)
- **Circuit half-open**: First request is attempted as a probe

This prevents retry storms when a provider is down.

### Metrics

- `aegis_retry_attempt_total{provider,attempt}`: Total retry attempts
- `aegis_retry_success_total{provider,attempt}`: Successful retries (request succeeded after N retries)
- `aegis_retry_failure_total{provider,reason}`: Failed retries
  - `reason="exhausted"`: All retries exhausted
  - `reason="non_retryable"`: Error was not retryable

### Example Flow

```
Request arrives
  ↓
Attempt 1: Fails with 503
  ↓
Wait ~100ms (with jitter)
  ↓
Attempt 2: Fails with 503
  ↓
Wait ~200ms (with jitter)
  ↓
Attempt 3: Succeeds with 200
  ↓
Return response to client
(Total attempts: 3, retry_success_total incremented)
```

---

## Input Validation

### Overview

Input validation checks all incoming requests for:

- **Required fields**: Ensures critical fields are present
- **Length limits**: Prevents resource exhaustion from oversized payloads
- **Valid ranges**: Ensures numeric parameters are within acceptable bounds
- **Injection prevention**: Blocks dangerous control characters

### Validation Rules

#### Model

- **Required**: Yes
- **Max length**: 256 characters
- **Valid characters**: `a-z`, `A-Z`, `0-9`, `-`, `_`, `.`, `:`
- **Examples**:
  - ✅ `gpt-4`
  - ✅ `gpt-4-0125-preview`
  - ✅ `azure:gpt-4`
  - ❌ `model<script>` (invalid characters)

#### Messages

- **Required**: Yes, must be non-empty array
- **Max messages**: 1,000 messages per request
- **Max message length**: 100,000 characters per message
- **Max total content**: 1,000,000 characters total
- **Valid roles**: `system`, `user`, `assistant`, `function`
- **Content validation**:
  - No null bytes (`\x00`)
  - No dangerous control characters (except `\n`, `\t`, `\r`)

#### Temperature

- **Required**: No (optional)
- **Range**: 0.0 to 2.0
- **Default**: Provider-specific

#### Max Tokens

- **Required**: No (optional)
- **Range**: 1 to 128,000
- **Note**: Adjust per model (some models support higher limits)

#### Top P

- **Required**: No (optional)
- **Range**: 0.0 to 1.0

#### Stop Sequences

- **Max sequences**: 4
- **Max length per sequence**: 256 characters

### Error Responses

When validation fails, the gateway returns a **400 Bad Request** with a detailed error message:

```json
{
  "error": {
    "message": "model: model name contains invalid characters (allowed: a-z, A-Z, 0-9, -, _, ., :); messages[0].role: invalid role 'admin' (allowed: system, user, assistant, function)",
    "type": "invalid_request_error",
    "code": "bad_request"
  }
}
```

### Metrics

- `aegis_validation_failure_total{field}`: Total validation failures by field
  - `field="model"`: Model validation failures
  - `field="messages"`: Messages validation failures
  - `field="temperature"`: Temperature validation failures
  - `field="max_tokens"`: Max tokens validation failures
  - `field="top_p"`: Top P validation failures
  - `field="stop"`: Stop sequences validation failures

### Example Valid Request

```json
{
  "model": "gpt-4",
  "messages": [
    {
      "role": "system",
      "content": "You are a helpful assistant."
    },
    {
      "role": "user",
      "content": "Hello!"
    }
  ],
  "temperature": 0.7,
  "max_tokens": 1000,
  "top_p": 0.9,
  "stop": ["\n", "END"]
}
```

### Bypassing Validation

For internal or admin requests, validation can be bypassed by not initializing the validator:

```go
// In main.go, don't pass validator to NewHandler:
handler := gateway.NewHandler(..., nil, nil, nil) // No retry, monitor, or validator
```

⚠️ **Warning**: Only bypass validation for trusted internal requests.

---

## Configuration

### Gateway Config (`configs/gateway.yaml`)

```yaml
routing:
  # Retry configuration
  max_retries: 2                      # Maximum retry attempts per request
  default_timeout: "30s"              # Default request timeout
  stream_first_chunk_timeout: "60s"   # Timeout for first streaming chunk
  stream_chunk_timeout: "10s"         # Timeout between streaming chunks
  
  # Circuit breaker configuration
  circuit_breaker:
    failure_threshold: 5              # Failures before circuit opens
    error_rate_threshold: 0.5         # Error rate (50%) before circuit opens
    error_rate_window: "30s"          # Window for error rate calculation
    recovery_probe_interval: "15s"    # Interval between recovery probes
```

### Retry Configuration in Code

To customize retry behavior:

```go
retryConfig := retry.Config{
    MaxRetries:        3,                    // 3 retry attempts
    InitialBackoff:    50 * time.Millisecond, // Start at 50ms
    MaxBackoff:        10 * time.Second,     // Cap at 10s
    BackoffMultiplier: 2.0,                  // Double each time
    JitterFraction:    0.2,                  // 20% randomness
}
retryExecutor := retry.NewExecutor(retryConfig, metrics)
```

### Validation Limits

To customize validation limits:

```go
limits := validation.Limits{
    MaxModelNameLength:    128,      // Shorter model names
    MaxMessagesCount:      100,      // Fewer messages
    MaxMessageLength:      50000,    // Shorter messages
    MaxTotalContentLength: 500000,   // Less total content
    MaxTokens:             64000,    // Lower token limit
    MinTemperature:        0.0,
    MaxTemperature:        1.0,      // Restrict temperature range
    MinTopP:               0.0,
    MaxTopP:               1.0,
    MaxStopSequences:      2,        // Fewer stop sequences
    MaxStopSequenceLength: 128,
}
validator := validation.NewValidator(limits, metrics)
```

---

## Metrics

### Prometheus Metrics

All retry, cancellation, and validation metrics are exposed at `:9090/metrics`:

```bash
# Retry metrics
curl -s http://localhost:9090/metrics | grep aegis_retry

# Cancellation metrics
curl -s http://localhost:9090/metrics | grep aegis_cancellation

# Validation metrics
curl -s http://localhost:9090/metrics | grep aegis_validation
```

### Grafana Dashboard Queries

#### Retry Success Rate

```promql
rate(aegis_retry_success_total[5m]) /
rate(aegis_retry_attempt_total[5m])
```

#### Cancellation Rate

```promql
rate(aegis_cancellation_total[5m])
```

#### Validation Failure Rate

```promql
rate(aegis_validation_failure_total[5m])
```

#### Average Retries Per Request

```promql
sum(rate(aegis_retry_attempt_total[5m])) by (provider) /
sum(rate(aegis_request_total[5m])) by (provider)
```

---

## Troubleshooting

### High Retry Rate

**Symptoms**: `aegis_retry_attempt_total` is high

**Possible causes**:
1. **Provider instability**: Upstream API is returning 5xx errors
2. **Network issues**: Connection timeouts or network errors
3. **Rate limiting**: Provider is rate limiting requests

**Solutions**:
- Check provider health: `curl http://localhost:8080/health`
- Review provider logs for error patterns
- Consider increasing timeouts if requests are slow but successful
- Implement request throttling if hitting rate limits

### High Cancellation Rate

**Symptoms**: `aegis_cancellation_total` is high

**Possible causes**:
1. **Client timeouts**: Clients are timing out before requests complete
2. **Slow responses**: Requests take too long, clients give up
3. **Client application issues**: Client code is cancelling prematurely

**Solutions**:
- Check `aegis_request_duration_ms` to see if requests are slow
- Increase client-side timeouts
- Optimize prompts to reduce token counts
- Consider using streaming for long responses

### High Validation Failure Rate

**Symptoms**: `aegis_validation_failure_total` is high

**Possible causes**:
1. **Client SDK issues**: Client is sending malformed requests
2. **Integration bugs**: New integration has incorrect request format
3. **Limit too restrictive**: Validation limits are too tight for use case

**Solutions**:
- Check logs for validation error messages
- Review client code for request construction
- Adjust validation limits if they're too restrictive:
  ```go
  limits := validation.DefaultLimits()
  limits.MaxMessageLength = 200000  // Increase if needed
  ```

### Debugging Retry Behavior

Enable debug logging to see retry attempts:

```bash
# Check logs for retry messages
tail -f /var/log/aegis/gateway.log | grep -i retry

# Look for patterns like:
# {"level":"debug","msg":"retrying request","provider":"openai","attempt":1,"backoff_ms":100}
```

### Testing Retry Logic

Simulate transient failures:

```bash
# Test with a provider that randomly fails
# (requires test endpoint or mock server)

for i in {1..10}; do
  curl -X POST http://localhost:8080/v1/chat/completions \
    -H "Authorization: Bearer YOUR_KEY" \
    -H "Content-Type: application/json" \
    -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}'
  echo "Request $i complete"
done

# Check retry metrics
curl -s http://localhost:9090/metrics | grep aegis_retry_attempt_total
```

---

## Best Practices

### 1. Monitor Retry Metrics

Set up alerts for:
- Retry rate > 10%: `rate(aegis_retry_attempt_total[5m]) > 0.1`
- Retry exhaustion rate > 1%: `rate(aegis_retry_failure_total{reason="exhausted"}[5m]) > 0.01`

### 2. Tune Retry Configuration

- **High-traffic services**: Lower `MaxRetries` (1-2) to fail fast
- **Background jobs**: Higher `MaxRetries` (3-5) for better reliability
- **Critical requests**: Adjust timeouts, not retries

### 3. Implement Client-Side Timeouts

Always set client-side timeouts that are higher than gateway timeouts:

```go
client := &http.Client{
    Timeout: 60 * time.Second, // Higher than gateway's 30s default
}
```

### 4. Use Validation Limits Wisely

- **Development**: Use lenient limits for testing
- **Production**: Use strict limits to prevent abuse
- **Per-tenant**: Consider different limits for different API keys

### 5. Log Validation Errors

When developing integrations, enable verbose logging to catch validation issues early:

```go
if err := validator.Validate(req); err != nil {
    log.Printf("Validation failed: %v", err)
    // Return error to client
}
```

---

## See Also

- [Circuit Breaker Documentation](CIRCUIT_BREAKER.md)
- [Metrics and Monitoring](METRICS.md)
- [Configuration Reference](CONFIG_REFERENCE.md)
