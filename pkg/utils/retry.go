package utils

import (
	"context"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

// DefaultBackoffConfig returns the recommended exponential backoff configuration
// with 10% jitter to prevent thundering herd problems
func DefaultBackoffConfig() wait.Backoff {
	return wait.Backoff{
		Steps:    5,               // Maximum 5 attempts
		Duration: 1 * time.Second, // Initial delay: 1 second
		Factor:   2.0,             // Double each time: 1s, 2s, 4s, 8s, 16s
		Jitter:   0.1,             // 10% jitter to prevent thundering herd
	}
}

// RetryWithBackoff retries an operation with exponential backoff until success or exhaustion
// The function respects context cancellation and distinguishes retryable from fatal errors
//
// Parameters:
//   - ctx: Context for cancellation/timeout
//   - backoff: Backoff configuration (use DefaultBackoffConfig() for defaults)
//   - fn: Function to retry, returns nil on success or error on failure
//
// Returns:
//   - nil if fn() succeeds
//   - wait.ErrWaitTimeout if all retries exhausted with retryable errors
//   - The actual error if fn() returns a non-retryable error
//   - context.Canceled or context.DeadlineExceeded if context is cancelled
func RetryWithBackoff(ctx context.Context, backoff wait.Backoff, fn func() error) error {
	var lastErr error
	attempt := 0

	err := wait.ExponentialBackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
		attempt++
		lastErr = fn()

		if lastErr == nil {
			// Success - stop retrying
			klog.V(4).Infof("Operation succeeded on attempt %d", attempt)
			return true, nil
		}

		// Check if error is retryable
		if IsRetryableError(lastErr) {
			klog.V(4).Infof("Attempt %d failed with retryable error: %v", attempt, lastErr)
			// Return false, nil to signal "retry"
			return false, nil
		}

		// Non-retryable error - stop immediately
		klog.V(4).Infof("Attempt %d failed with non-retryable error: %v", attempt, lastErr)
		return false, lastErr
	})

	// If wait.ExponentialBackoffWithContext returned due to exhaustion,
	// err will be an interrupted error and we should log the last error
	if wait.Interrupted(err) && lastErr != nil {
		klog.V(2).Infof("All %d retry attempts exhausted, last error: %v", attempt, lastErr)
	}

	return err
}

// IsRetryableError determines if an error is transient and worth retrying
// Returns true for network-related errors that may succeed on retry
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}

	errStr := strings.ToLower(err.Error())

	// Non-retryable patterns must be checked first. These errors can wrap lower-level
	// transient text, but retrying them would repeat cleanup chatter.
	nonRetryablePatterns := []string{
		"failed to disconnect orphaned subsystem",
	}

	for _, pattern := range nonRetryablePatterns {
		if strings.Contains(errStr, pattern) {
			return false
		}
	}

	// Retryable patterns - transient network and device issues
	retryablePatterns := []string{
		"connection refused",
		"connection reset",
		"connection timeout",
		"connection timed out",
		"no route to host",
		"network unreachable",
		"network is unreachable",
		"host is unreachable",
		"device did not appear",
		"i/o timeout",
		"io timeout",
		"temporary failure",
		"resource temporarily unavailable",
		"try again",
	}

	for _, pattern := range retryablePatterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}

	return false
}
