package rds

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// mockRDSClient implements RDSClient for testing
type mockRDSClient struct {
	connected bool
	closed    bool
	mu        sync.Mutex
}

func (m *mockRDSClient) Connect() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = true
	return nil
}

func (m *mockRDSClient) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	m.connected = false
	return nil
}

func (m *mockRDSClient) IsConnected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connected && !m.closed
}

func (m *mockRDSClient) GetAddress() string {
	return "198.51.100.20"
}

func (m *mockRDSClient) CreateVolume(opts CreateVolumeOptions) error {
	return nil
}

func (m *mockRDSClient) DeleteVolume(slot string) error {
	return nil
}

func (m *mockRDSClient) ResizeVolume(slot string, newSizeBytes int64) error {
	return nil
}

func (m *mockRDSClient) GetVolume(slot string) (*VolumeInfo, error) {
	return nil, nil
}

func (m *mockRDSClient) VerifyVolumeExists(slot string) error {
	return nil
}

func (m *mockRDSClient) ListVolumes() ([]VolumeInfo, error) {
	return nil, nil
}

func (m *mockRDSClient) ListFiles(path string) ([]FileInfo, error) {
	return nil, nil
}

func (m *mockRDSClient) DeleteFile(path string) error {
	return nil
}

func (m *mockRDSClient) GetCapacity(basePath string) (*CapacityInfo, error) {
	return nil, nil
}

func (m *mockRDSClient) CreateSnapshot(opts CreateSnapshotOptions) (*SnapshotInfo, error) {
	return nil, nil
}

func (m *mockRDSClient) DeleteSnapshot(snapshotID string) error {
	return nil
}

func (m *mockRDSClient) GetSnapshot(snapshotID string) (*SnapshotInfo, error) {
	return nil, nil
}

func (m *mockRDSClient) ListSnapshots() ([]SnapshotInfo, error) {
	return nil, nil
}

func (m *mockRDSClient) RestoreSnapshot(snapshotID string, newVolumeOpts CreateVolumeOptions) error {
	return nil
}

func (m *mockRDSClient) GetDiskMetrics(slot string) (*DiskMetrics, error) {
	return &DiskMetrics{Slot: slot}, nil
}

func (m *mockRDSClient) GetHardwareHealth(snmpHost string, snmpCommunity string) (*HardwareHealthMetrics, error) {
	return &HardwareHealthMetrics{}, nil
}

func TestNewConnectionPool(t *testing.T) {
	tests := []struct {
		name        string
		config      PoolConfig
		expectError bool
	}{
		{
			name: "valid config",
			config: PoolConfig{
				Factory:   func() (RDSClient, error) { return &mockRDSClient{}, nil },
				MaxSize:   10,
				MaxIdle:   5,
				RateLimit: 10.0,
			},
			expectError: false,
		},
		{
			name: "default values",
			config: PoolConfig{
				Factory: func() (RDSClient, error) { return &mockRDSClient{}, nil },
			},
			expectError: false,
		},
		{
			name: "missing factory",
			config: PoolConfig{
				MaxSize: 10,
			},
			expectError: true,
		},
		{
			name: "maxIdle > maxSize gets adjusted",
			config: PoolConfig{
				Factory: func() (RDSClient, error) { return &mockRDSClient{}, nil },
				MaxSize: 5,
				MaxIdle: 10,
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool, err := NewConnectionPool(tt.config)
			if tt.expectError && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if pool != nil {
				defer func() { _ = pool.Close() }()
			}
		})
	}
}

func TestPoolGetPut(t *testing.T) {
	pool, err := NewConnectionPool(PoolConfig{
		Factory: func() (RDSClient, error) {
			client := &mockRDSClient{}
			_ = client.Connect()
			return client, nil
		},
		MaxSize:   5,
		MaxIdle:   3,
		RateLimit: 100.0,
	})
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}
	defer func() { _ = pool.Close() }()

	ctx := context.Background()

	// Get a connection
	client1, err := pool.Get(ctx)
	if err != nil {
		t.Fatalf("Failed to get connection: %v", err)
	}
	if client1 == nil {
		t.Fatal("Got nil client")
	}

	// Check metrics
	metrics := pool.GetMetrics()
	if metrics.activeConnections != 1 {
		t.Errorf("Expected 1 active connection, got %d", metrics.activeConnections)
	}

	// Return connection
	if err := pool.Put(client1); err != nil {
		t.Errorf("Failed to put connection: %v", err)
	}

	// Check metrics after put
	metrics = pool.GetMetrics()
	if metrics.activeConnections != 0 {
		t.Errorf("Expected 0 active connections after put, got %d", metrics.activeConnections)
	}
	if metrics.idleConnections != 1 {
		t.Errorf("Expected 1 idle connection after put, got %d", metrics.idleConnections)
	}

	// Get again - should reuse idle connection
	client2, err := pool.Get(ctx)
	if err != nil {
		t.Fatalf("Failed to get connection second time: %v", err)
	}
	if client2 == nil {
		t.Fatal("Got nil client on second get")
	}

	// Should be the same client
	if client1 != client2 {
		t.Error("Expected to reuse same connection but got different one")
	}

	_ = pool.Put(client2)
}

func TestPoolMaxSize(t *testing.T) {
	maxSize := 3
	pool, err := NewConnectionPool(PoolConfig{
		Factory: func() (RDSClient, error) {
			client := &mockRDSClient{}
			_ = client.Connect()
			return client, nil
		},
		MaxSize:   maxSize,
		MaxIdle:   1,
		RateLimit: 100.0,
	})
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}
	defer func() { _ = pool.Close() }()

	ctx := context.Background()

	// Get maxSize connections
	clients := make([]RDSClient, 0, maxSize)
	for i := 0; i < maxSize; i++ {
		client, err := pool.Get(ctx)
		if err != nil {
			t.Fatalf("Failed to get connection %d: %v", i, err)
		}
		clients = append(clients, client)
	}

	// Try to get one more - should fail
	_, err = pool.Get(ctx)
	if !errors.Is(err, ErrPoolExhausted) {
		t.Errorf("Expected ErrPoolExhausted, got: %v", err)
	}

	// Return one connection
	if err := pool.Put(clients[0]); err != nil {
		t.Errorf("Failed to put connection: %v", err)
	}

	// Now we should be able to get one again
	_, err = pool.Get(ctx)
	if err != nil {
		t.Errorf("Failed to get connection after put: %v", err)
	}

	// Clean up
	for i := 1; i < len(clients); i++ {
		_ = pool.Put(clients[i])
	}
}

func TestPoolRateLimit(t *testing.T) {
	rateLimit := 10.0 // 10 per second
	pool, err := NewConnectionPool(PoolConfig{
		Factory: func() (RDSClient, error) {
			client := &mockRDSClient{}
			_ = client.Connect()
			return client, nil
		},
		MaxSize:   100,
		MaxIdle:   10,
		RateLimit: rateLimit,
		RateBurst: 1,
	})
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}
	defer func() { _ = pool.Close() }()

	ctx := context.Background()

	// Get connections rapidly
	start := time.Now()
	count := 15
	for i := 0; i < count; i++ {
		client, err := pool.Get(ctx)
		if err != nil {
			t.Fatalf("Failed to get connection %d: %v", i, err)
		}
		_ = pool.Put(client)
	}
	elapsed := time.Since(start)

	// Should take at least (count-burst)/rate seconds
	minDuration := time.Duration(float64(count-1)/rateLimit) * time.Second
	if elapsed < minDuration/2 { // Allow some tolerance
		t.Errorf("Rate limiting not working: got %v connections in %v (expected at least %v)",
			count, elapsed, minDuration)
	}
}

func TestPoolCircuitBreaker(t *testing.T) {
	failureCount := 0
	threshold := 3

	pool, err := NewConnectionPool(PoolConfig{
		Factory: func() (RDSClient, error) {
			failureCount++
			if failureCount <= threshold {
				return nil, errors.New("connection failed")
			}
			client := &mockRDSClient{}
			_ = client.Connect()
			return client, nil
		},
		MaxSize:                 10,
		MaxIdle:                 5,
		RateLimit:               100.0,
		CircuitBreakerThreshold: threshold,
		CircuitBreakerTimeout:   1 * time.Second,
	})
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}
	defer func() { _ = pool.Close() }()

	ctx := context.Background()

	// Trigger failures to open circuit
	for i := 0; i < threshold; i++ {
		_, err := pool.Get(ctx)
		if err == nil {
			t.Error("Expected connection error")
		}
	}

	// Circuit should be open now
	metrics := pool.GetMetrics()
	if metrics.connectionErrors != int64(threshold) {
		t.Errorf("Expected %d errors, got %d", threshold, metrics.connectionErrors)
	}

	// Next request should be rejected by circuit breaker
	_, err = pool.Get(ctx)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("Expected ErrCircuitOpen, got: %v", err)
	}

	// Wait for circuit to transition to half-open
	time.Sleep(1100 * time.Millisecond)

	// Should allow request through now and succeed
	client, err := pool.Get(ctx)
	if err != nil {
		t.Errorf("Failed to get connection after circuit timeout: %v", err)
	}
	if client == nil {
		t.Fatal("Got nil client after circuit recovery")
	}
	_ = pool.Put(client)
}

func TestPoolConcurrency(t *testing.T) {
	pool, err := NewConnectionPool(PoolConfig{
		Factory: func() (RDSClient, error) {
			client := &mockRDSClient{}
			_ = client.Connect()
			return client, nil
		},
		MaxSize:   10,
		MaxIdle:   5,
		RateLimit: 100.0,
	})
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}
	defer func() { _ = pool.Close() }()

	ctx := context.Background()

	// Run multiple goroutines getting and putting connections
	var wg sync.WaitGroup
	concurrency := 20
	iterations := 50

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				client, err := pool.Get(ctx)
				if err != nil && !errors.Is(err, ErrPoolExhausted) {
					t.Errorf("Failed to get connection: %v", err)
					return
				}
				if client != nil {
					// Simulate some work
					time.Sleep(1 * time.Millisecond)
					if err := pool.Put(client); err != nil {
						t.Errorf("Failed to put connection: %v", err)
					}
				}
			}
		}()
	}

	wg.Wait()

	// Check final metrics
	metrics := pool.GetMetrics()
	if metrics.activeConnections != 0 {
		t.Errorf("Expected 0 active connections at end, got %d", metrics.activeConnections)
	}
}

func TestPoolClose(t *testing.T) {
	pool, err := NewConnectionPool(PoolConfig{
		Factory: func() (RDSClient, error) {
			client := &mockRDSClient{}
			_ = client.Connect()
			return client, nil
		},
		MaxSize:   5,
		MaxIdle:   3,
		RateLimit: 100.0,
	})
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}

	ctx := context.Background()

	// Get some connections and return them
	client1, _ := pool.Get(ctx)
	client2, _ := pool.Get(ctx)
	_ = pool.Put(client1)
	_ = pool.Put(client2)

	// Close pool
	if err := pool.Close(); err != nil {
		t.Errorf("Failed to close pool: %v", err)
	}

	// Verify idle connections were closed
	if mock, ok := client1.(*mockRDSClient); ok {
		if !mock.closed {
			t.Error("Idle connection 1 was not closed")
		}
	}
	if mock, ok := client2.(*mockRDSClient); ok {
		if !mock.closed {
			t.Error("Idle connection 2 was not closed")
		}
	}

	// Try to get connection from closed pool
	_, err = pool.Get(ctx)
	if !errors.Is(err, ErrPoolClosed) {
		t.Errorf("Expected ErrPoolClosed, got: %v", err)
	}
}

func TestPoolIdleTimeout(t *testing.T) {
	idleTimeout := 100 * time.Millisecond
	pool, err := NewConnectionPool(PoolConfig{
		Factory: func() (RDSClient, error) {
			client := &mockRDSClient{}
			_ = client.Connect()
			return client, nil
		},
		MaxSize:     5,
		MaxIdle:     3,
		IdleTimeout: idleTimeout,
		RateLimit:   100.0,
	})
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}
	defer func() { _ = pool.Close() }()

	ctx := context.Background()

	// Get and return a connection
	client1, err := pool.Get(ctx)
	if err != nil {
		t.Fatalf("Failed to get connection: %v", err)
	}
	_ = pool.Put(client1)

	// Wait for idle timeout
	time.Sleep(idleTimeout + 50*time.Millisecond)

	// Get another connection - should create new one since old one timed out
	client2, err := pool.Get(ctx)
	if err != nil {
		t.Fatalf("Failed to get connection after timeout: %v", err)
	}

	// Should be a new connection (old one was closed)
	if mock, ok := client1.(*mockRDSClient); ok {
		if !mock.closed {
			t.Error("Idle connection should have been closed due to timeout")
		}
	}

	_ = pool.Put(client2)
}

func TestPoolDisconnectedConnection(t *testing.T) {
	pool, err := NewConnectionPool(PoolConfig{
		Factory: func() (RDSClient, error) {
			client := &mockRDSClient{}
			_ = client.Connect()
			return client, nil
		},
		MaxSize:   5,
		MaxIdle:   3,
		RateLimit: 100.0,
	})
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}
	defer func() { _ = pool.Close() }()

	ctx := context.Background()

	// Get a connection
	client, err := pool.Get(ctx)
	if err != nil {
		t.Fatalf("Failed to get connection: %v", err)
	}

	// Disconnect it
	if mock, ok := client.(*mockRDSClient); ok {
		_ = mock.Close()
	}

	// Return disconnected connection
	_ = pool.Put(client)

	// Should not be in idle pool
	metrics := pool.GetMetrics()
	if metrics.idleConnections != 0 {
		t.Errorf("Expected 0 idle connections (disconnected), got %d", metrics.idleConnections)
	}
}

func TestCircuitBreakerStates(t *testing.T) {
	cb := NewCircuitBreaker(3, 100*time.Millisecond)

	// Initially closed
	if cb.GetState() != CircuitClosed {
		t.Error("Circuit should start in Closed state")
	}
	if !cb.Allow() {
		t.Error("Closed circuit should allow requests")
	}

	// Record failures to open circuit
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordFailure()

	if cb.GetState() != CircuitOpen {
		t.Error("Circuit should be Open after threshold failures")
	}
	if cb.Allow() {
		t.Error("Open circuit should not allow requests")
	}

	// Wait for timeout
	time.Sleep(150 * time.Millisecond)

	if !cb.Allow() {
		t.Error("Circuit should allow request after timeout (half-open)")
	}
	if cb.GetState() != CircuitHalfOpen {
		t.Error("Circuit should be in HalfOpen state after timeout")
	}

	// Record success to close circuit
	cb.RecordSuccess()
	if cb.GetState() != CircuitClosed {
		t.Error("Circuit should close after successful test")
	}
}

func TestPoolMetricsString(t *testing.T) {
	metrics := &PoolMetrics{
		totalConnections:  10,
		activeConnections: 2,
		idleConnections:   3,
		connectionErrors:  1,
		circuitBreaks:     0,
		rateLimitHits:     5,
		waitTimeTotal:     100 * time.Millisecond,
		waitCount:         10,
	}

	str := metrics.String()
	if str == "" {
		t.Error("Metrics string should not be empty")
	}

	// Check that it contains key information
	expectedSubstrings := []string{"total=10", "active=2", "idle=3", "Errors=1", "RateLimitHits=5"}
	for _, substr := range expectedSubstrings {
		if !containsString(str, substr) {
			t.Errorf("Metrics string should contain %q, got: %s", substr, str)
		}
	}
}
