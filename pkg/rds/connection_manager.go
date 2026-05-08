package rds

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"k8s.io/klog/v2"

	"git.srvlab.io/whiskey/nvmet-csi/pkg/observability"
)

// ConnectionManagerConfig holds configuration for ConnectionManager.
type ConnectionManagerConfig struct {
	// Client is the underlying RDSClient to manage (required)
	Client RDSClient

	// InitialInterval is the initial backoff interval (default: 1s)
	InitialInterval time.Duration

	// MaxInterval is the maximum backoff interval (default: 16s)
	MaxInterval time.Duration

	// MaxElapsedTime is the maximum time for backoff (default: 0 = never give up for background reconnection)
	MaxElapsedTime time.Duration

	// Multiplier is the backoff multiplier (default: 2.0)
	Multiplier float64

	// RandomizationFactor adds jitter to backoff intervals to prevent thundering herd (default: 0.1)
	RandomizationFactor float64

	// Metrics is optional Prometheus metrics recorder (may be nil)
	Metrics *observability.Metrics

	// OnReconnect is called after successful reconnection (optional, used to trigger reconciliation)
	OnReconnect func()
}

// ConnectionManager monitors RDS connection health and automatically reconnects with exponential backoff.
// It wraps an RDSClient and provides automatic reconnection in the background.
// The manager does NOT proxy RDSClient methods - callers use GetClient() and handle connection errors themselves.
type ConnectionManager struct {
	config    ConnectionManagerConfig
	client    RDSClient
	connected bool
	mu        sync.RWMutex
	stopCh    chan struct{}
	doneCh    chan struct{}
	metrics   *observability.Metrics
}

// NewConnectionManager creates a new ConnectionManager with the given configuration.
// Validates config and sets defaults for zero values.
func NewConnectionManager(config ConnectionManagerConfig) (*ConnectionManager, error) {
	if config.Client == nil {
		return nil, fmt.Errorf("client is required")
	}

	// Set defaults
	if config.InitialInterval == 0 {
		config.InitialInterval = 1 * time.Second
	}
	if config.MaxInterval == 0 {
		config.MaxInterval = 16 * time.Second
	}
	if config.MaxElapsedTime == 0 {
		config.MaxElapsedTime = 0 // 0 means never give up
	}
	if config.Multiplier == 0 {
		config.Multiplier = 2.0
	}
	if config.RandomizationFactor == 0 {
		config.RandomizationFactor = 0.1 // Jitter to prevent thundering herd
	}

	cm := &ConnectionManager{
		config:    config,
		client:    config.Client,
		connected: config.Client.IsConnected(),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
		metrics:   config.Metrics,
	}

	// Record initial connection state
	if cm.metrics != nil {
		cm.metrics.RecordConnectionState(cm.client.GetAddress(), cm.connected)
	}

	return cm, nil
}

// IsConnected returns the current connection state in a thread-safe manner.
func (cm *ConnectionManager) IsConnected() bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.connected
}

// GetClient returns the underlying RDSClient.
// Callers use this to perform RDS operations directly.
func (cm *ConnectionManager) GetClient() RDSClient {
	return cm.client
}

// StartMonitor starts the background connection monitoring goroutine.
// Polls connection health every 5 seconds and attempts reconnection when disconnected.
// Stops when ctx.Done() or Stop() is called.
func (cm *ConnectionManager) StartMonitor(ctx context.Context) {
	go cm.monitorLoop(ctx)
}

func (cm *ConnectionManager) monitorLoop(ctx context.Context) {
	defer close(cm.doneCh)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	klog.V(4).Infof("ConnectionManager: Starting monitoring for RDS %s", cm.client.GetAddress())

	for {
		select {
		case <-ctx.Done():
			klog.V(4).Infof("ConnectionManager: Context cancelled, stopping monitor")
			return
		case <-cm.stopCh:
			klog.V(4).Infof("ConnectionManager: Stop requested, stopping monitor")
			return
		case <-ticker.C:
			// Poll connection state
			isConnected := cm.client.IsConnected()

			cm.mu.Lock()
			wasConnected := cm.connected
			cm.connected = isConnected
			cm.mu.Unlock()

			// Detect disconnection
			if wasConnected && !isConnected {
				klog.Warningf("ConnectionManager: RDS connection lost to %s, starting reconnection", cm.client.GetAddress())
				if cm.metrics != nil {
					cm.metrics.RecordConnectionState(cm.client.GetAddress(), false)
				}

				// Start reconnection loop
				cm.attemptReconnection(ctx)
			}
		}
	}
}

func (cm *ConnectionManager) attemptReconnection(ctx context.Context) {
	// Create exponential backoff
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = cm.config.InitialInterval
	bo.MaxInterval = cm.config.MaxInterval
	bo.MaxElapsedTime = cm.config.MaxElapsedTime
	bo.Multiplier = cm.config.Multiplier
	bo.RandomizationFactor = cm.config.RandomizationFactor
	bo.Reset()

	attempt := 0
	startTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			klog.V(4).Infof("ConnectionManager: Context cancelled during reconnection")
			return
		case <-cm.stopCh:
			klog.V(4).Infof("ConnectionManager: Stop requested during reconnection")
			return
		default:
		}

		attempt++

		// Close old connection before reconnecting (avoid session leaks)
		if err := cm.client.Close(); err != nil {
			klog.V(4).Infof("ConnectionManager: Error closing connection: %v", err)
		}

		// Attempt reconnection
		klog.V(4).Infof("ConnectionManager: Reconnection attempt %d to %s", attempt, cm.client.GetAddress())
		err := cm.client.Connect()

		if err == nil {
			// Success!
			duration := time.Since(startTime)
			klog.Infof("ConnectionManager: Successfully reconnected to %s after %d attempts (%.2fs)", cm.client.GetAddress(), attempt, duration.Seconds())

			cm.mu.Lock()
			cm.connected = true
			cm.mu.Unlock()

			if cm.metrics != nil {
				cm.metrics.RecordConnectionState(cm.client.GetAddress(), true)
				cm.metrics.RecordReconnectAttempt("success", duration)
			}

			// Call OnReconnect callback if set
			if cm.config.OnReconnect != nil {
				go cm.config.OnReconnect()
			}

			return
		}

		// Failed - record failure metric
		klog.V(4).Infof("ConnectionManager: Reconnection attempt %d failed: %v", attempt, err)
		if cm.metrics != nil {
			cm.metrics.RecordReconnectAttempt("failure", 0)
		}

		// Calculate next backoff
		nextBackoff := bo.NextBackOff()
		if nextBackoff == backoff.Stop {
			// Max elapsed time reached - log and give up
			klog.Errorf("ConnectionManager: Max reconnection time exceeded for %s, giving up", cm.client.GetAddress())
			return
		}

		klog.V(4).Infof("ConnectionManager: Waiting %s before next reconnection attempt", nextBackoff)

		// Wait with context cancellation support
		select {
		case <-ctx.Done():
			return
		case <-cm.stopCh:
			return
		case <-time.After(nextBackoff):
			// Continue to next attempt
		}
	}
}

// Stop signals the monitor goroutine to stop and blocks until it's done.
func (cm *ConnectionManager) Stop() {
	close(cm.stopCh)
	<-cm.doneCh
	klog.V(4).Infof("ConnectionManager: Stopped monitoring for %s", cm.client.GetAddress())
}

// Reconnect manually closes and reconnects the RDS client.
// Used for startup or forced reconnection.
// Returns error if reconnection fails.
func (cm *ConnectionManager) Reconnect() error {
	klog.V(4).Infof("ConnectionManager: Manual reconnect requested for %s", cm.client.GetAddress())

	// Close existing connection
	if err := cm.client.Close(); err != nil {
		klog.V(4).Infof("ConnectionManager: Error closing connection during manual reconnect: %v", err)
	}

	// Attempt reconnection
	err := cm.client.Connect()

	cm.mu.Lock()
	cm.connected = (err == nil)
	cm.mu.Unlock()

	if cm.metrics != nil {
		cm.metrics.RecordConnectionState(cm.client.GetAddress(), cm.connected)
	}

	if err != nil {
		klog.Errorf("ConnectionManager: Manual reconnect failed: %v", err)
		return err
	}

	klog.V(4).Infof("ConnectionManager: Manual reconnect successful")
	return nil
}
