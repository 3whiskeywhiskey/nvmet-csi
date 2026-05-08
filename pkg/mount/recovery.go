package mount

import (
	"context"
	"fmt"
	"time"

	"k8s.io/klog/v2"

	"git.srvlab.io/whiskey/nvmet-csi/pkg/nvme"
	"git.srvlab.io/whiskey/nvmet-csi/pkg/observability"
)

// RecoveryConfig holds recovery configuration
type RecoveryConfig struct {
	MaxAttempts       int           // Default: 3
	InitialBackoff    time.Duration // Default: 1s
	BackoffMultiplier float64       // Default: 2.0
	NormalUnmountWait time.Duration // Default: 10s (from CONTEXT.md)
}

// DefaultRecoveryConfig returns sensible defaults
func DefaultRecoveryConfig() RecoveryConfig {
	return RecoveryConfig{
		MaxAttempts:       3,
		InitialBackoff:    1 * time.Second,
		BackoffMultiplier: 2.0,
		NormalUnmountWait: 10 * time.Second,
	}
}

// RecoveryResult contains details about a recovery attempt
type RecoveryResult struct {
	Recovered  bool
	Attempts   int
	FinalError error
	OldDevice  string
	NewDevice  string
}

// MountRecoverer handles automatic mount recovery
type MountRecoverer struct {
	config   RecoveryConfig
	mounter  Mounter
	checker  *StaleMountChecker
	resolver *nvme.DeviceResolver
	metrics  *observability.Metrics
}

// NewMountRecoverer creates a new mount recoverer
func NewMountRecoverer(config RecoveryConfig, mounter Mounter, checker *StaleMountChecker, resolver *nvme.DeviceResolver) *MountRecoverer {
	return &MountRecoverer{
		config:   config,
		mounter:  mounter,
		checker:  checker,
		resolver: resolver,
	}
}

// SetMetrics sets the Prometheus metrics instance for recording recovery operations
func (r *MountRecoverer) SetMetrics(metrics *observability.Metrics) {
	r.metrics = metrics
}

// Recover attempts to recover a stale mount by unmounting and remounting with the correct device
// Returns a RecoveryResult with details about the recovery attempt
//
// Recovery process:
//  1. Resolve current device from NQN
//  2. For each attempt (up to MaxAttempts):
//     a. Try ForceUnmount with NormalUnmountWait timeout
//     b. If unmount fails with "in use" error: return error (don't retry)
//     c. If unmount succeeds: resolve new device path and mount
//     d. If mount succeeds: return success
//     e. If mount fails: log warning, sleep with exponential backoff, continue
//  3. If all attempts fail: return result with FinalError
func (r *MountRecoverer) Recover(ctx context.Context, mountPath string, nqn string, fsType string, mountOptions []string) (*RecoveryResult, error) {
	klog.V(2).Infof("Starting mount recovery for %s (NQN: %s)", mountPath, nqn)

	// If resolver is nil (test environment), we can't do recovery
	if r.resolver == nil {
		klog.V(4).Infof("Recovery skipped for %s: resolver not configured (test mode)", mountPath)
		return &RecoveryResult{
			Recovered:  false,
			Attempts:   0,
			FinalError: fmt.Errorf("recovery not available in test mode"),
		}, fmt.Errorf("recovery not available in test mode")
	}

	result := &RecoveryResult{
		Recovered: false,
		Attempts:  0,
	}

	// Get current device info for logging
	info, err := r.checker.GetStaleInfo(mountPath, nqn)
	if err == nil {
		result.OldDevice = info.MountDevice
	}

	// Attempt recovery with exponential backoff
	backoff := r.config.InitialBackoff

	for attempt := 1; attempt <= r.config.MaxAttempts; attempt++ {
		result.Attempts = attempt
		klog.V(4).Infof("Mount recovery attempt %d/%d for %s", attempt, r.config.MaxAttempts, mountPath)

		// Check context cancellation
		select {
		case <-ctx.Done():
			result.FinalError = ctx.Err()
			return result, fmt.Errorf("recovery cancelled: %w", ctx.Err())
		default:
		}

		// Step 1: Try to unmount the stale mount
		klog.V(4).Infof("Attempting ForceUnmount for %s with timeout %v", mountPath, r.config.NormalUnmountWait)
		err := r.mounter.ForceUnmount(mountPath, r.config.NormalUnmountWait)
		if err != nil {
			// Check if mount is in use - if so, refuse to retry
			inUse, pids, checkErr := r.mounter.IsMountInUse(mountPath)
			if checkErr != nil {
				klog.V(4).Infof("Failed to check if mount is in use: %v", checkErr)
			}

			if inUse {
				result.FinalError = fmt.Errorf("mount is in use by processes %v, refusing to force unmount", pids)
				klog.Warningf("Recovery failed for %s: mount is in use by processes %v", mountPath, pids)
				return result, result.FinalError
			}

			// Unmount failed but mount is not in use - may be transient, continue
			klog.Warningf("ForceUnmount failed for %s (attempt %d/%d): %v", mountPath, attempt, r.config.MaxAttempts, err)
			result.FinalError = fmt.Errorf("unmount failed: %w", err)

			// Sleep before next attempt if not last attempt
			if attempt < r.config.MaxAttempts {
				klog.V(4).Infof("Sleeping %v before retry", backoff)
				select {
				case <-ctx.Done():
					result.FinalError = ctx.Err()
					return result, fmt.Errorf("recovery cancelled during backoff: %w", ctx.Err())
				case <-time.After(backoff):
					backoff = time.Duration(float64(backoff) * r.config.BackoffMultiplier)
				}
			}
			continue
		}

		klog.V(4).Infof("Successfully unmounted stale mount %s", mountPath)

		// Step 2: Resolve new device path from NQN
		newDevice, err := r.resolver.ResolveDevicePath(nqn)
		if err != nil {
			result.FinalError = fmt.Errorf("failed to resolve NQN after unmount: %w", err)
			klog.Warningf("Failed to resolve NQN %s after unmount (attempt %d/%d): %v", nqn, attempt, r.config.MaxAttempts, err)

			// Sleep before next attempt if not last attempt
			if attempt < r.config.MaxAttempts {
				klog.V(4).Infof("Sleeping %v before retry", backoff)
				select {
				case <-ctx.Done():
					result.FinalError = ctx.Err()
					return result, fmt.Errorf("recovery cancelled during backoff: %w", ctx.Err())
				case <-time.After(backoff):
					backoff = time.Duration(float64(backoff) * r.config.BackoffMultiplier)
				}
			}
			continue
		}

		result.NewDevice = newDevice
		klog.V(4).Infof("Resolved new device for NQN %s: %s", nqn, newDevice)

		// Step 3: Mount new device to mount path
		klog.V(4).Infof("Attempting to mount %s to %s with fsType %s", newDevice, mountPath, fsType)
		err = r.mounter.Mount(newDevice, mountPath, fsType, mountOptions)
		if err != nil {
			result.FinalError = fmt.Errorf("mount failed: %w", err)
			klog.Warningf("Failed to mount %s to %s (attempt %d/%d): %v", newDevice, mountPath, attempt, r.config.MaxAttempts, err)

			// Sleep before next attempt if not last attempt
			if attempt < r.config.MaxAttempts {
				klog.V(4).Infof("Sleeping %v before retry", backoff)
				select {
				case <-ctx.Done():
					result.FinalError = ctx.Err()
					return result, fmt.Errorf("recovery cancelled during backoff: %w", ctx.Err())
				case <-time.After(backoff):
					backoff = time.Duration(float64(backoff) * r.config.BackoffMultiplier)
				}
			}
			continue
		}

		// Success!
		klog.V(2).Infof("Recovered mount %s (old device: %s, new device: %s) after %d attempt(s)",
			mountPath, result.OldDevice, result.NewDevice, attempt)
		result.Recovered = true
		result.FinalError = nil
		// Record successful recovery metric
		if r.metrics != nil {
			r.metrics.RecordStaleRecovery(nil)
		}
		return result, nil
	}

	// All attempts failed
	klog.Errorf("Mount recovery failed for %s after %d attempts: %v", mountPath, r.config.MaxAttempts, result.FinalError)
	// Record failed recovery metric
	if r.metrics != nil {
		r.metrics.RecordStaleRecovery(result.FinalError)
	}
	return result, result.FinalError
}
