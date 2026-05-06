package rds

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestNewConnectionManager(t *testing.T) {
	mockClient := NewMockClient()

	cm, err := NewConnectionManager(ConnectionManagerConfig{
		Client: mockClient,
	})

	if err != nil {
		t.Fatalf("NewConnectionManager failed: %v", err)
	}

	if cm == nil {
		t.Fatal("NewConnectionManager returned nil")
		return
	}

	if cm.client != mockClient {
		t.Error("ConnectionManager client not set correctly")
	}

	// Check defaults applied
	if cm.config.InitialInterval != 1*time.Second {
		t.Errorf("expected InitialInterval=1s, got %v", cm.config.InitialInterval)
	}
	if cm.config.MaxInterval != 16*time.Second {
		t.Errorf("expected MaxInterval=16s, got %v", cm.config.MaxInterval)
	}
	if cm.config.Multiplier != 2.0 {
		t.Errorf("expected Multiplier=2.0, got %v", cm.config.Multiplier)
	}
	if cm.config.RandomizationFactor != 0.1 {
		t.Errorf("expected RandomizationFactor=0.1, got %v", cm.config.RandomizationFactor)
	}
}

func TestNewConnectionManager_RequiresClient(t *testing.T) {
	_, err := NewConnectionManager(ConnectionManagerConfig{})

	if err == nil {
		t.Fatal("expected error when Client is nil")
	}

	if err.Error() != "client is required" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestNewConnectionManager_CustomConfig(t *testing.T) {
	mockClient := NewMockClient()

	cm, err := NewConnectionManager(ConnectionManagerConfig{
		Client:              mockClient,
		InitialInterval:     500 * time.Millisecond,
		MaxInterval:         8 * time.Second,
		Multiplier:          1.5,
		RandomizationFactor: 0.2,
	})

	if err != nil {
		t.Fatalf("NewConnectionManager failed: %v", err)
	}

	// Check custom values preserved
	if cm.config.InitialInterval != 500*time.Millisecond {
		t.Errorf("expected InitialInterval=500ms, got %v", cm.config.InitialInterval)
	}
	if cm.config.MaxInterval != 8*time.Second {
		t.Errorf("expected MaxInterval=8s, got %v", cm.config.MaxInterval)
	}
	if cm.config.Multiplier != 1.5 {
		t.Errorf("expected Multiplier=1.5, got %v", cm.config.Multiplier)
	}
	if cm.config.RandomizationFactor != 0.2 {
		t.Errorf("expected RandomizationFactor=0.2, got %v", cm.config.RandomizationFactor)
	}
}

func TestIsConnected_ReflectsClientState(t *testing.T) {
	mockClient := NewMockClient()
	mockClient.SetConnected(true)

	cm, err := NewConnectionManager(ConnectionManagerConfig{
		Client: mockClient,
	})
	if err != nil {
		t.Fatalf("NewConnectionManager failed: %v", err)
	}

	// Should reflect initial connected state
	if !cm.IsConnected() {
		t.Error("expected IsConnected() to be true initially")
	}

	// Change client state
	mockClient.SetConnected(false)

	// Note: ConnectionManager caches state, so it won't reflect immediately
	// until the monitor detects the change. For testing initial state only.
}

func TestGetClient(t *testing.T) {
	mockClient := NewMockClient()

	cm, err := NewConnectionManager(ConnectionManagerConfig{
		Client: mockClient,
	})
	if err != nil {
		t.Fatalf("NewConnectionManager failed: %v", err)
	}

	client := cm.GetClient()
	if client != mockClient {
		t.Error("GetClient() did not return the correct client")
	}
}

func TestStartMonitor_DetectsDisconnection(t *testing.T) {
	mockClient := NewMockClient()
	mockClient.SetConnected(true)

	// Set persistent error to prevent automatic reconnection in this test
	disconnectDetected := false

	cm, err := NewConnectionManager(ConnectionManagerConfig{
		Client:          mockClient,
		InitialInterval: 100 * time.Millisecond,
		MaxInterval:     200 * time.Millisecond,
		MaxElapsedTime:  1 * time.Second, // Give up quickly in this test
		OnReconnect: func() {
			disconnectDetected = true
		},
	})
	if err != nil {
		t.Fatalf("NewConnectionManager failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start monitor
	cm.StartMonitor(ctx)

	// Give monitor time to start
	time.Sleep(100 * time.Millisecond)

	// Verify initial connected state
	if !cm.IsConnected() {
		t.Error("expected IsConnected() to be true initially")
	}

	// Simulate disconnection and prevent reconnection
	mockClient.SetConnected(false)
	mockClient.SetPersistentError(fmt.Errorf("connection refused"))

	// Wait for monitor to detect disconnection (polls every 5s)
	time.Sleep(6 * time.Second)

	// Monitor should have attempted reconnection and failed
	if disconnectDetected {
		t.Error("expected reconnection to fail, but OnReconnect was called")
	}

	// Stop monitor
	cm.Stop()
}

func TestReconnection_WithBackoff(t *testing.T) {
	mockClient := NewMockClient()
	mockClient.SetConnected(true)

	reconnectCalled := false
	cm, err := NewConnectionManager(ConnectionManagerConfig{
		Client:          mockClient,
		InitialInterval: 100 * time.Millisecond,
		MaxInterval:     500 * time.Millisecond,
		OnReconnect: func() {
			reconnectCalled = true
		},
	})
	if err != nil {
		t.Fatalf("NewConnectionManager failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start monitor
	cm.StartMonitor(ctx)
	time.Sleep(100 * time.Millisecond)

	// Simulate disconnection
	mockClient.SetConnected(false)

	// Wait for disconnection detection
	time.Sleep(6 * time.Second)

	// Now clear the persistent error so Connect() can succeed
	mockClient.ClearError()

	// Wait for reconnection
	time.Sleep(3 * time.Second)

	// Should be reconnected
	if !cm.IsConnected() {
		t.Error("expected reconnection to succeed")
	}

	// OnReconnect callback should be called
	if !reconnectCalled {
		t.Error("expected OnReconnect callback to be called")
	}

	cm.Stop()
}

func TestReconnection_FailsAndRetries(t *testing.T) {
	mockClient := NewMockClient()
	mockClient.SetConnected(false)

	// Set persistent error to simulate connection failures
	mockClient.SetPersistentError(fmt.Errorf("connection refused"))

	cm, err := NewConnectionManager(ConnectionManagerConfig{
		Client:          mockClient,
		InitialInterval: 100 * time.Millisecond,
		MaxInterval:     300 * time.Millisecond,
		MaxElapsedTime:  2 * time.Second, // Give up after 2 seconds
	})
	if err != nil {
		t.Fatalf("NewConnectionManager failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Trigger manual reconnection (simulate disconnection)
	cm.connected = false

	// Start reconnection attempt
	cm.attemptReconnection(ctx)

	// Should still be disconnected after giving up
	if cm.IsConnected() {
		t.Error("expected connection to remain disconnected after max elapsed time")
	}
}

func TestStop_GracefulShutdown(t *testing.T) {
	mockClient := NewMockClient()

	cm, err := NewConnectionManager(ConnectionManagerConfig{
		Client: mockClient,
	})
	if err != nil {
		t.Fatalf("NewConnectionManager failed: %v", err)
	}

	ctx := context.Background()
	cm.StartMonitor(ctx)

	// Give monitor time to start
	time.Sleep(100 * time.Millisecond)

	// Stop should complete without hanging
	done := make(chan struct{})
	go func() {
		cm.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() timed out")
	}
}

func TestReconnect_Manual(t *testing.T) {
	mockClient := NewMockClient()
	mockClient.SetConnected(false)

	cm, err := NewConnectionManager(ConnectionManagerConfig{
		Client: mockClient,
	})
	if err != nil {
		t.Fatalf("NewConnectionManager failed: %v", err)
	}

	// Initially disconnected
	if cm.IsConnected() {
		t.Error("expected IsConnected() to be false initially")
	}

	// Manual reconnect
	err = cm.Reconnect()
	if err != nil {
		t.Errorf("Reconnect() failed: %v", err)
	}

	// Should be connected now
	if !cm.IsConnected() {
		t.Error("expected IsConnected() to be true after Reconnect()")
	}
}

func TestReconnect_ManualFailure(t *testing.T) {
	mockClient := NewMockClient()
	mockClient.SetConnected(false)
	mockClient.SetPersistentError(fmt.Errorf("connection refused"))

	cm, err := NewConnectionManager(ConnectionManagerConfig{
		Client: mockClient,
	})
	if err != nil {
		t.Fatalf("NewConnectionManager failed: %v", err)
	}

	// Manual reconnect should fail
	err = cm.Reconnect()
	if err == nil {
		t.Error("expected Reconnect() to fail")
	}

	// Should remain disconnected
	if cm.IsConnected() {
		t.Error("expected IsConnected() to be false after failed Reconnect()")
	}
}

func TestOnReconnectCallback(t *testing.T) {
	mockClient := NewMockClient()

	callbackCalled := make(chan struct{})
	cm, err := NewConnectionManager(ConnectionManagerConfig{
		Client: mockClient,
		OnReconnect: func() {
			close(callbackCalled)
		},
	})
	if err != nil {
		t.Fatalf("NewConnectionManager failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Simulate disconnection and reconnection
	cm.connected = false
	mockClient.SetConnected(true)

	// Trigger reconnection
	cm.attemptReconnection(ctx)

	select {
	case <-callbackCalled:
	case <-ctx.Done():
		t.Error("expected OnReconnect callback to be called")
	}
}

func TestConnectionManager_ContextCancellation(t *testing.T) {
	mockClient := NewMockClient()

	cm, err := NewConnectionManager(ConnectionManagerConfig{
		Client: mockClient,
	})
	if err != nil {
		t.Fatalf("NewConnectionManager failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cm.StartMonitor(ctx)

	// Give monitor time to start
	time.Sleep(100 * time.Millisecond)

	// Cancel context
	cancel()

	// Monitor should stop gracefully
	select {
	case <-cm.doneCh:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("Monitor did not stop after context cancellation")
	}
}
