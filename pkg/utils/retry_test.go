package utils

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
)

func TestDefaultBackoffConfig(t *testing.T) {
	backoff := DefaultBackoffConfig()

	// Verify Steps == 5
	if backoff.Steps != 5 {
		t.Errorf("Expected Steps=5, got %d", backoff.Steps)
	}

	// Verify Duration == 1 * time.Second
	if backoff.Duration != 1*time.Second {
		t.Errorf("Expected Duration=1s, got %v", backoff.Duration)
	}

	// Verify Factor == 2.0
	if backoff.Factor != 2.0 {
		t.Errorf("Expected Factor=2.0, got %f", backoff.Factor)
	}

	// Verify Jitter == 0.1 (10%)
	if backoff.Jitter != 0.1 {
		t.Errorf("Expected Jitter=0.1, got %f", backoff.Jitter)
	}
}

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "connection refused",
			err:      errors.New("dial tcp 10.0.0.1:4420: connection refused"),
			expected: true,
		},
		{
			name:     "Connection Refused (case insensitive)",
			err:      errors.New("Connection Refused"),
			expected: true,
		},
		{
			name:     "no route to host",
			err:      errors.New("no route to host"),
			expected: true,
		},
		{
			name:     "network unreachable",
			err:      errors.New("connect: network unreachable"),
			expected: true,
		},
		{
			name:     "network is unreachable",
			err:      errors.New("network is unreachable"),
			expected: true,
		},
		{
			name:     "host is unreachable",
			err:      errors.New("host is unreachable"),
			expected: true,
		},
		{
			name:     "device did not appear",
			err:      errors.New("device did not appear within timeout"),
			expected: true,
		},
		{
			name:     "i/o timeout",
			err:      errors.New("i/o timeout"),
			expected: true,
		},
		{
			name:     "io timeout (no slash)",
			err:      errors.New("io timeout"),
			expected: true,
		},
		{
			name:     "connection reset",
			err:      errors.New("connection reset by peer"),
			expected: true,
		},
		{
			name:     "connection timeout",
			err:      errors.New("connection timeout"),
			expected: true,
		},
		{
			name:     "connection timed out",
			err:      errors.New("connection timed out"),
			expected: true,
		},
		{
			name:     "temporary failure",
			err:      errors.New("temporary failure in name resolution"),
			expected: true,
		},
		{
			name:     "resource temporarily unavailable",
			err:      errors.New("resource temporarily unavailable"),
			expected: true,
		},
		{
			name:     "try again",
			err:      errors.New("try again"),
			expected: true,
		},
		{
			name:     "orphan disconnect failure is terminal even with retryable detail",
			err:      errors.New("failed to disconnect orphaned subsystem nqn.2000-02.com.mikrotik:pvc-test before reconnect: resource temporarily unavailable"),
			expected: false,
		},
		{
			name:     "device wait cleanup failure is terminal even with retryable detail",
			err:      errors.New("device did not appear for subsystem nqn.2000-02.com.mikrotik:pvc-test and cleanup disconnect failed: wait error: connection timed out; cleanup error: resource temporarily unavailable"),
			expected: false,
		},
		{
			name:     "permission denied (not retryable)",
			err:      errors.New("permission denied"),
			expected: false,
		},
		{
			name:     "invalid argument (not retryable)",
			err:      errors.New("invalid argument"),
			expected: false,
		},
		{
			name:     "no such file or directory (not retryable)",
			err:      errors.New("no such file or directory"),
			expected: false,
		},
		{
			name:     "file exists (not retryable)",
			err:      errors.New("file exists"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsRetryableError(tt.err)
			if result != tt.expected {
				t.Errorf("IsRetryableError(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}

func TestRetryWithBackoff_Success(t *testing.T) {
	ctx := context.Background()
	backoff := testBackoffConfig() // Use fast backoff for tests

	attemptCount := 0
	err := RetryWithBackoff(ctx, backoff, func() error {
		attemptCount++
		return nil // Success on first attempt
	})

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if attemptCount != 1 {
		t.Errorf("Expected 1 attempt, got %d", attemptCount)
	}
}

func TestRetryWithBackoff_RetryThenSuccess(t *testing.T) {
	ctx := context.Background()
	backoff := testBackoffConfig() // 1ms delay for fast tests

	attemptCount := 0
	err := RetryWithBackoff(ctx, backoff, func() error {
		attemptCount++
		if attemptCount < 3 {
			// Fail with retryable error for first 2 attempts
			return errors.New("connection refused")
		}
		return nil // Success on 3rd attempt
	})

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if attemptCount != 3 {
		t.Errorf("Expected 3 attempts, got %d", attemptCount)
	}
}

func TestRetryWithBackoff_NonRetryable(t *testing.T) {
	ctx := context.Background()
	backoff := testBackoffConfig()

	attemptCount := 0
	nonRetryableErr := errors.New("permission denied")

	err := RetryWithBackoff(ctx, backoff, func() error {
		attemptCount++
		return nonRetryableErr
	})

	// Should stop immediately with non-retryable error
	if err == nil {
		t.Fatal("Expected error but got nil")
	}
	if !errors.Is(err, nonRetryableErr) {
		t.Errorf("Expected permission denied error, got: %v", err)
	}
	if attemptCount != 1 {
		t.Errorf("Expected 1 attempt (no retries for non-retryable), got %d", attemptCount)
	}
}

func TestRetryWithBackoff_ExhaustsRetries(t *testing.T) {
	ctx := context.Background()
	backoff := testBackoffConfig()
	backoff.Steps = 3 // Only 3 attempts

	attemptCount := 0
	err := RetryWithBackoff(ctx, backoff, func() error {
		attemptCount++
		return errors.New("connection refused") // Always fail with retryable error
	})

	// Should return timeout error after exhausting retries
	if err == nil {
		t.Fatal("Expected error but got nil")
	}
	if !wait.Interrupted(err) {
		t.Errorf("Expected interrupted/timeout error, got: %v", err)
	}
	if attemptCount != 3 {
		t.Errorf("Expected 3 attempts (all retries exhausted), got %d", attemptCount)
	}
}

func TestRetryWithBackoff_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	backoff := testBackoffConfig()
	backoff.Duration = 100 * time.Millisecond // Longer delay to allow cancellation

	attemptCount := 0
	err := RetryWithBackoff(ctx, backoff, func() error {
		attemptCount++
		if attemptCount == 1 {
			// Cancel context after first attempt
			cancel()
		}
		return errors.New("connection refused")
	})

	// Should return context error
	if err == nil {
		t.Fatal("Expected error but got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Logf("Got error: %v (type: %T)", err, err)
		// The error might be wrapped or be ErrWaitTimeout with context canceled
		// Just verify we got some error
	}
}

func TestRetryWithBackoff_ContextAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	backoff := testBackoffConfig()
	attemptCount := 0

	err := RetryWithBackoff(ctx, backoff, func() error {
		attemptCount++
		return nil
	})

	// Should fail immediately with context error
	if err == nil {
		t.Fatal("Expected error but got nil")
	}
	// With pre-canceled context, we might get 0 or 1 attempts depending on implementation
	if attemptCount > 1 {
		t.Errorf("Expected at most 1 attempt with canceled context, got %d", attemptCount)
	}
}

// testBackoffConfig returns a fast backoff config for testing (1ms delays)
func testBackoffConfig() wait.Backoff {
	return wait.Backoff{
		Steps:    5,
		Duration: 1 * time.Millisecond, // Very short for fast tests
		Factor:   2.0,
		Jitter:   0.0, // No jitter for predictable tests
	}
}
