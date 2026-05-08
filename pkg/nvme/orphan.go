package nvme

import (
	"context"
	"fmt"

	"k8s.io/klog/v2"

	"git.srvlab.io/whiskey/nvmet-csi/pkg/observability"
)

const (
	// DefaultOrphanCleanupMaxDisconnects bounds cleanup blast radius per pass.
	DefaultOrphanCleanupMaxDisconnects = 4
)

// OrphanCleaner detects and removes orphaned NVMe subsystem connections
type OrphanCleaner struct {
	connector        Connector
	resolver         *DeviceResolver
	metrics          *observability.Metrics
	managedNQNPrefix string
	maxDisconnects   int
	dryRun           bool
}

// SetMetrics sets the Prometheus metrics for recording orphan cleanup
func (oc *OrphanCleaner) SetMetrics(m *observability.Metrics) {
	oc.metrics = m
}

// NewOrphanCleaner creates an OrphanCleaner using the connector's resolver
// and the given managed NQN prefix for filtering which volumes to manage.
func NewOrphanCleaner(connector Connector, managedNQNPrefix string) *OrphanCleaner {
	return &OrphanCleaner{
		connector:        connector,
		resolver:         connector.GetResolver(),
		managedNQNPrefix: managedNQNPrefix,
		maxDisconnects:   DefaultOrphanCleanupMaxDisconnects,
	}
}

// SetMaxDisconnects bounds disconnect attempts per cleanup pass.
// Values <= 0 allow all detected managed orphans to be disconnected.
func (oc *OrphanCleaner) SetMaxDisconnects(maxDisconnects int) {
	oc.maxDisconnects = maxDisconnects
}

// SetDryRun reports orphaned subsystems without disconnecting them.
func (oc *OrphanCleaner) SetDryRun(dryRun bool) {
	oc.dryRun = dryRun
}

// CleanupOrphanedConnections scans for and disconnects orphaned NVMe subsystems.
// An orphaned subsystem appears connected but has no working block device.
// This is best-effort: individual disconnect failures are logged but don't stop cleanup.
func (oc *OrphanCleaner) CleanupOrphanedConnections(ctx context.Context) error {
	klog.V(2).Info("Scanning for orphaned NVMe connections")

	if err := ValidateNQNPrefix(oc.managedNQNPrefix); err != nil {
		return fmt.Errorf("unsafe managed NQN prefix for orphan cleanup: %w", err)
	}

	// Get all connected NQNs using ListConnectedSubsystems
	nqns, err := oc.resolver.ListConnectedSubsystems()
	if err != nil {
		return fmt.Errorf("failed to list connected subsystems: %w", err)
	}

	klog.V(4).Infof("Found %d connected NVMe subsystems", len(nqns))

	orphanCount := 0
	disconnectAttempts := 0
	for _, nqn := range nqns {
		// CRITICAL: Only manage volumes matching configured NQN prefix
		// This prevents accidentally disconnecting system volumes (nixos-*, etc.)
		// that use NVMe-oF for critical mounts like /var
		if !NQNMatchesPrefix(nqn, oc.managedNQNPrefix) {
			klog.V(4).Infof("Skipping non-managed volume (NQN %s doesn't match prefix %s)", nqn, oc.managedNQNPrefix)
			continue
		}

		// Check context cancellation
		select {
		case <-ctx.Done():
			klog.Warning("Orphan cleanup interrupted by context cancellation")
			return ctx.Err()
		default:
		}

		// Check if this subsystem is orphaned
		orphaned, err := oc.resolver.IsOrphanedSubsystem(nqn)
		if err != nil {
			klog.Warningf("Error checking if subsystem %s is orphaned: %v", nqn, err)
			continue
		}

		if !orphaned {
			continue
		}

		klog.Warningf("Found orphaned subsystem: %s", nqn)
		orphanCount++
		if oc.metrics != nil {
			oc.metrics.RecordOrphanDetected(oc.dryRun)
		}
		if oc.dryRun {
			klog.Infof("[DRY-RUN] Would disconnect orphaned subsystem: %s", nqn)
			if oc.metrics != nil {
				oc.metrics.RecordOrphanDisconnectSkippedDryRun()
			}
			continue
		}

		if oc.maxDisconnects > 0 && disconnectAttempts >= oc.maxDisconnects {
			klog.Warningf("Orphan cleanup disconnect limit reached (max=%d); leaving remaining managed orphans for a later explicit cleanup pass", oc.maxDisconnects)
			if oc.metrics != nil {
				oc.metrics.RecordOrphanDisconnectLimitReached()
			}
			break
		}
		disconnectAttempts++
		if oc.metrics != nil {
			oc.metrics.RecordOrphanDisconnectAttempt()
		}

		// Attempt to disconnect
		if err := oc.connector.DisconnectWithContext(ctx, nqn); err != nil {
			klog.Warningf("Failed to disconnect orphaned subsystem %s: %v", nqn, err)
			if oc.metrics != nil {
				oc.metrics.RecordOrphanDisconnectFailure()
			}
			// Continue to next orphan - best effort cleanup
		} else {
			klog.V(2).Infof("Successfully disconnected orphaned subsystem: %s", nqn)
			// Record metric for successful cleanup
			if oc.metrics != nil {
				oc.metrics.RecordOrphanCleaned()
				oc.metrics.RecordOrphanDisconnectSuccess()
			}
		}
	}

	if orphanCount > 0 {
		klog.Infof("Orphan cleanup complete: found %d orphaned subsystems, attempted %d disconnects", orphanCount, disconnectAttempts)
	} else {
		klog.V(2).Info("Orphan cleanup complete: no orphaned subsystems found")
	}

	return nil
}
