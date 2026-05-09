# Connection Pooling and Rate Limiting

## Overview

The nvmet-csi driver implements connection pooling with rate limiting and circuit breaker patterns to improve performance, reliability, and resource utilization when managing volumes on the RDS server.

## Features

### 1. Connection Pooling

The connection pool maintains a set of reusable SSH connections to the RDS server:

- **Connection Reuse**: Idle connections are kept alive and reused for subsequent requests
- **Configurable Pool Size**: Maximum number of concurrent connections (default: 10)
- **Idle Connection Management**: Unused connections are closed after timeout (default: 5 minutes)
- **Automatic Cleanup**: Stale or disconnected connections are detected and removed

### 2. Rate Limiting

Built-in rate limiting prevents overwhelming the RDS server:

- **Token Bucket Algorithm**: Uses `golang.org/x/time/rate` for fair rate limiting
- **Configurable Rate**: Requests per second limit (default: 10 req/s)
- **Burst Handling**: Allows temporary bursts above the steady-state rate
- **Graceful Queueing**: Requests wait for available tokens rather than failing

### 3. Circuit Breaker

Circuit breaker pattern protects against cascading failures:

- **Failure Detection**: Tracks consecutive connection failures
- **Circuit States**:
  - **Closed**: Normal operation, all requests allowed
  - **Open**: Too many failures, rejecting requests to allow recovery
  - **Half-Open**: Testing if service recovered with limited requests
- **Automatic Recovery**: Circuit closes after successful test request
- **Configurable Thresholds**: Failure count and timeout duration

### 4. Metrics and Monitoring

The pool tracks comprehensive metrics:

- Total connections created
- Active and idle connection counts
- Connection errors
- Circuit breaker activations
- Rate limit hits
- Average wait times

## Configuration

### Pool Configuration

```go
import "gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/rds"

poolConfig := rds.PoolConfig{
    // Required: Factory function to create new clients
    Factory: func() (rds.RDSClient, error) {
        return rds.NewClient(rds.ClientConfig{
            Protocol:   "ssh",
            Address:    "10.42.68.1",
            Port:       22,
            User:       "admin",
            PrivateKey: privateKeyBytes,
            Timeout:    10 * time.Second,
        })
    },

    // Optional: Pool sizing (defaults shown)
    MaxSize:   10,                  // Maximum total connections
    MaxIdle:   5,                   // Maximum idle connections
    IdleTimeout: 5 * time.Minute,   // Idle connection lifetime

    // Optional: Rate limiting (defaults shown)
    RateLimit: 10.0,                // Requests per second
    RateBurst: 20,                  // Burst capacity

    // Optional: Circuit breaker (defaults shown)
    CircuitBreakerThreshold: 5,              // Failures before opening
    CircuitBreakerTimeout:   30 * time.Second, // Recovery attempt interval
}

pool, err := rds.NewConnectionPool(poolConfig)
if err != nil {
    log.Fatalf("Failed to create pool: %v", err)
}
defer pool.Close()
```

## Usage

### Basic Usage

```go
ctx := context.Background()

// Get a connection from the pool
client, err := pool.Get(ctx)
if err != nil {
    return fmt.Errorf("failed to get connection: %w", err)
}

// Use the client
err = client.CreateVolume(rds.CreateVolumeOptions{
    Slot:     "pvc-123",
    FilePath: "/storage-pool/volumes/pvc-123.img",
    FileSize: 10737418240, // 10 GiB
})

// Return connection to pool
if err := pool.Put(client); err != nil {
    log.Warningf("Failed to return connection: %v", err)
}

// Handle operation error
if err != nil {
    return fmt.Errorf("failed to create volume: %w", err)
}
```

### Context-Based Timeouts

```go
// Create context with timeout
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

// Rate limiter will respect context cancellation
client, err := pool.Get(ctx)
if err != nil {
    if errors.Is(err, context.DeadlineExceeded) {
        return fmt.Errorf("timeout waiting for connection")
    }
    return err
}
defer pool.Put(client)

// Use client...
```

### Error Handling

```go
client, err := pool.Get(ctx)
if err != nil {
    switch {
    case errors.Is(err, rds.ErrPoolClosed):
        return fmt.Errorf("connection pool is closed")
    case errors.Is(err, rds.ErrPoolExhausted):
        return fmt.Errorf("all connections in use, try again later")
    case errors.Is(err, rds.ErrCircuitOpen):
        return fmt.Errorf("circuit breaker is open, RDS server may be unavailable")
    default:
        return fmt.Errorf("failed to get connection: %w", err)
    }
}
defer pool.Put(client)
```

### Monitoring Metrics

```go
// Get current metrics
metrics := pool.GetMetrics()

log.Infof("Pool metrics: %s", metrics.String())
// Output: Connections(total=42, active=3, idle=2) Errors=1 CircuitBreaks=0 RateLimitHits=5 AvgWait=12ms

// Access individual metrics
log.Infof("Active connections: %d", metrics.activeConnections)
log.Infof("Total errors: %d", metrics.connectionErrors)
```

## Best Practices

### 1. Pool Sizing

- **MaxSize**: Set based on expected concurrent volume operations
  - Controller: 5-10 connections typically sufficient
  - High-volume environments: 20-50 connections
- **MaxIdle**: Keep 30-50% of MaxSize idle for quick reuse
- **IdleTimeout**: Balance between connection reuse and resource consumption
  - Short timeout (1-2 min): More aggressive cleanup
  - Long timeout (10-15 min): Better performance for bursty workloads

### 2. Rate Limiting

- **Match RDS capacity**: Don't exceed what the RDS server can handle
  - Start conservative (5-10 req/s)
  - Monitor RDS CPU/memory and adjust upward
- **Allow bursts**: Set RateBurst to 2-3x RateLimit for pod scheduling spikes
- **Consider workload patterns**: Higher rate during business hours if needed

### 3. Circuit Breaker

- **Threshold**: 3-5 failures prevents unnecessary retry storms
- **Timeout**: 20-60 seconds gives RDS time to recover
- **Monitor circuit breaks**: Frequent opens indicate RDS issues

### 4. Connection Management

- **Always return connections**: Use `defer pool.Put(client)` immediately after `Get()`
- **Handle Put errors**: Log warnings but don't fail operations
- **Context timeouts**: Use context for long operations to prevent hung requests
- **Graceful shutdown**: Call `pool.Close()` during cleanup

### 5. Error Handling

- **Circuit open errors**: Don't retry immediately, respect backoff
- **Pool exhausted**: Implement application-level queuing or backoff
- **Connection errors**: Let circuit breaker handle, avoid manual retries

## Performance Considerations

### Connection Reuse Benefits

- **Reduced latency**: Eliminate SSH handshake overhead (~50-100ms)
- **Lower CPU usage**: Both on driver and RDS server
- **Better throughput**: Handle 2-5x more operations per second

### Rate Limiting Impact

- **Prevents overload**: Protects RDS from request floods
- **Fair resource sharing**: Ensures all requests get processed eventually
- **Predictable performance**: Smooths out traffic spikes

### Circuit Breaker Protection

- **Fast failure**: Immediately reject requests when RDS is down
- **Reduced load**: Gives RDS server time to recover
- **Automatic recovery**: Resumes traffic when service healthy

## Troubleshooting

### Pool Exhausted Errors

**Symptom**: Frequent `ErrPoolExhausted` errors

**Causes**:
- MaxSize too small for workload
- Connections not being returned (missing Put() calls)
- Slow operations blocking connections
- Rate limit too restrictive

**Solutions**:
```go
// Increase pool size
MaxSize: 20,

// Verify all Get() calls have matching Put()
defer pool.Put(client)

// Add operation timeouts
ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
defer cancel()
```

### Circuit Breaker Opens Frequently

**Symptom**: `ErrCircuitOpen` errors, circuit state oscillating

**Causes**:
- RDS server connectivity issues
- SSH authentication failures
- RDS server overloaded
- Network instability

**Solutions**:
```go
// Increase threshold for transient issues
CircuitBreakerThreshold: 10,

// Longer timeout for recovery
CircuitBreakerTimeout: 60 * time.Second,

// Check RDS server health
// Verify network connectivity
// Review SSH authentication
```

### High Wait Times

**Symptom**: Metrics show high average wait times

**Causes**:
- Rate limit too restrictive
- Pool size too small
- Burst capacity insufficient

**Solutions**:
```go
// Increase rate limit
RateLimit: 20.0,
RateBurst: 40,

// Increase pool size
MaxSize: 20,
MaxIdle: 10,
```

### Stale Connections

**Symptom**: Connection errors after idle periods

**Causes**:
- SSH timeout on RDS server
- Network issues
- IdleTimeout too long

**Solutions**:
```go
// Shorter idle timeout
IdleTimeout: 2 * time.Minute,

// The pool automatically detects and removes stale connections
// No additional action needed
```

## Integration with CSI Driver

The connection pool is designed to be integrated into the CSI controller service:

```go
// In controller initialization
poolConfig := rds.PoolConfig{
    Factory: func() (rds.RDSClient, error) {
        return rds.NewClient(controllerConfig.RDSClientConfig)
    },
    MaxSize:                 controllerConfig.PoolMaxSize,
    MaxIdle:                 controllerConfig.PoolMaxIdle,
    RateLimit:               controllerConfig.RateLimit,
    CircuitBreakerThreshold: 5,
    CircuitBreakerTimeout:   30 * time.Second,
}

pool, err := rds.NewConnectionPool(poolConfig)
if err != nil {
    return fmt.Errorf("failed to create connection pool: %w", err)
}

// Store pool in controller
cs.connectionPool = pool

// In CreateVolume
client, err := cs.connectionPool.Get(ctx)
if err != nil {
    return nil, status.Errorf(codes.Unavailable, "failed to get RDS connection: %v", err)
}
defer cs.connectionPool.Put(client)

// Use client for volume operations...
```

## See Also

- [Architecture Documentation](architecture.md) - Overall system design
- [RDS Commands](rds-commands.md) - RouterOS CLI reference
- [Performance Tuning](performance-tuning.md) - Optimization guidelines
