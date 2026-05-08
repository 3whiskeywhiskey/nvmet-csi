package circuitbreaker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/sony/gobreaker"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

const (
	// DefaultConsecutiveFailures is the number of failures before circuit opens
	DefaultConsecutiveFailures = 3

	// DefaultTimeout is how long circuit stays open before allowing a retry
	DefaultTimeout = 5 * time.Minute

	// DefaultInterval is the cyclic period of closed state to clear failure counts
	DefaultInterval = 1 * time.Minute

	// ResetAnnotation is the PV annotation to reset circuit breaker
	ResetAnnotation = "nvmet.csi.whiskey.works/reset-circuit-breaker"
)

// VolumeCircuitBreaker manages per-volume circuit breakers to prevent retry storms
type VolumeCircuitBreaker struct {
	breakers map[string]*gobreaker.CircuitBreaker
	mu       sync.RWMutex
}

// NewVolumeCircuitBreaker creates a new per-volume circuit breaker manager
func NewVolumeCircuitBreaker() *VolumeCircuitBreaker {
	return &VolumeCircuitBreaker{
		breakers: make(map[string]*gobreaker.CircuitBreaker),
	}
}

// getBreaker returns or creates a circuit breaker for the given volume
func (vcb *VolumeCircuitBreaker) getBreaker(volumeID string) *gobreaker.CircuitBreaker {
	vcb.mu.RLock()
	cb, exists := vcb.breakers[volumeID]
	vcb.mu.RUnlock()

	if exists {
		return cb
	}

	vcb.mu.Lock()
	defer vcb.mu.Unlock()

	// Double-check after acquiring write lock
	if cb, exists := vcb.breakers[volumeID]; exists {
		return cb
	}

	settings := gobreaker.Settings{
		Name:        volumeID,
		MaxRequests: 1, // Only 1 request allowed in half-open state
		Interval:    DefaultInterval,
		Timeout:     DefaultTimeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= DefaultConsecutiveFailures
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			klog.Infof("Circuit breaker for volume %s: %s -> %s", name, from, to)
		},
	}

	cb = gobreaker.NewCircuitBreaker(settings)
	vcb.breakers[volumeID] = cb
	klog.V(4).Infof("Created circuit breaker for volume %s", volumeID)
	return cb
}

// Execute runs the given function with circuit breaker protection.
// Returns gRPC Unavailable error if circuit is open.
func (vcb *VolumeCircuitBreaker) Execute(ctx context.Context, volumeID string, fn func() error) error {
	cb := vcb.getBreaker(volumeID)

	_, err := cb.Execute(func() (interface{}, error) {
		return nil, fn()
	})

	if errors.Is(err, gobreaker.ErrOpenState) {
		return status.Errorf(codes.Unavailable,
			"Volume %s circuit breaker is OPEN due to %d consecutive failures. "+
				"Filesystem may be corrupted. To retry: add annotation '%s=true' to the PV "+
				"and delete the pod. The circuit will reset on next mount attempt.",
			volumeID, DefaultConsecutiveFailures, ResetAnnotation)
	}

	if errors.Is(err, gobreaker.ErrTooManyRequests) {
		return status.Errorf(codes.Unavailable,
			"Volume %s circuit breaker is HALF-OPEN and already has a request in progress. "+
				"Wait for the current request to complete.",
			volumeID)
	}

	return err
}

// CheckReset checks if the circuit breaker should be reset based on PV annotations.
// If the reset annotation is present and true, removes the breaker to allow fresh start.
func (vcb *VolumeCircuitBreaker) CheckReset(volumeID string, annotations map[string]string) bool {
	if annotations == nil {
		return false
	}

	if annotations[ResetAnnotation] == "true" {
		vcb.mu.Lock()
		defer vcb.mu.Unlock()

		if _, exists := vcb.breakers[volumeID]; exists {
			delete(vcb.breakers, volumeID)
			klog.Infof("Circuit breaker reset for volume %s via annotation", volumeID)
			return true
		}
	}
	return false
}

// State returns the current state of the circuit breaker for a volume.
// Returns "closed" if no breaker exists (default safe state).
func (vcb *VolumeCircuitBreaker) State(volumeID string) string {
	vcb.mu.RLock()
	cb, exists := vcb.breakers[volumeID]
	vcb.mu.RUnlock()

	if !exists {
		return "closed"
	}

	return cb.State().String()
}
