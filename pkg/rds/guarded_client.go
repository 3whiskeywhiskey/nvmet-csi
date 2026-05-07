package rds

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/time/rate"
	"k8s.io/klog/v2"
)

// MutationObserver records guarded RouterOS/RDS mutation behavior.
type MutationObserver interface {
	RecordRDSMutation(operation string, err error, dryRun bool, duration time.Duration)
	RecordRDSMutationRateLimitWait(operation string, duration time.Duration)
}

// MutationAuditObserver records recent guarded mutation decisions for operators.
type MutationAuditObserver interface {
	RecordRDSMutationAudit(operation, target, status string, dryRun bool, duration time.Duration)
}

// GuardedClientConfig controls mutation guardrails around an RDS client.
type GuardedClientConfig struct {
	MinInterval time.Duration
	Burst       int
	Timeout     time.Duration
	DryRun      bool
	Observer    MutationObserver
}

// GuardedClient wraps an RDSClient and rate-limits mutating operations.
type GuardedClient struct {
	inner    RDSClient
	limiter  *rate.Limiter
	timeout  time.Duration
	dryRun   bool
	observer MutationObserver
}

// NewGuardedClient wraps client with conservative mutation guardrails.
func NewGuardedClient(client RDSClient, config GuardedClientConfig) (*GuardedClient, error) {
	if client == nil {
		return nil, fmt.Errorf("RDS client is required")
	}
	if config.Burst <= 0 {
		config.Burst = 1
	}
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Second
	}

	var limiter *rate.Limiter
	if config.MinInterval > 0 {
		limiter = rate.NewLimiter(rate.Every(config.MinInterval), config.Burst)
	}

	return &GuardedClient{
		inner:    client,
		limiter:  limiter,
		timeout:  config.Timeout,
		dryRun:   config.DryRun,
		observer: config.Observer,
	}, nil
}

func (c *GuardedClient) Connect() error                             { return c.inner.Connect() }
func (c *GuardedClient) Close() error                               { return c.inner.Close() }
func (c *GuardedClient) IsConnected() bool                          { return c.inner.IsConnected() }
func (c *GuardedClient) GetAddress() string                         { return c.inner.GetAddress() }
func (c *GuardedClient) GetVolume(slot string) (*VolumeInfo, error) { return c.inner.GetVolume(slot) }
func (c *GuardedClient) VerifyVolumeExists(slot string) error {
	return c.inner.VerifyVolumeExists(slot)
}
func (c *GuardedClient) ListVolumes() ([]VolumeInfo, error)        { return c.inner.ListVolumes() }
func (c *GuardedClient) ListFiles(path string) ([]FileInfo, error) { return c.inner.ListFiles(path) }
func (c *GuardedClient) GetCapacity(basePath string) (*CapacityInfo, error) {
	return c.inner.GetCapacity(basePath)
}
func (c *GuardedClient) GetSnapshot(snapshotID string) (*SnapshotInfo, error) {
	return c.inner.GetSnapshot(snapshotID)
}
func (c *GuardedClient) ListSnapshots() ([]SnapshotInfo, error) { return c.inner.ListSnapshots() }
func (c *GuardedClient) GetDiskMetrics(slot string) (*DiskMetrics, error) {
	return c.inner.GetDiskMetrics(slot)
}
func (c *GuardedClient) GetHardwareHealth(snmpHost string, snmpCommunity string) (*HardwareHealthMetrics, error) {
	return c.inner.GetHardwareHealth(snmpHost, snmpCommunity)
}

func (c *GuardedClient) CreateVolume(opts CreateVolumeOptions) error {
	return c.mutate("create_volume", opts.Slot, func() error {
		return c.inner.CreateVolume(opts)
	})
}

func (c *GuardedClient) DeleteVolume(slot string) error {
	return c.mutate("delete_volume", slot, func() error {
		return c.inner.DeleteVolume(slot)
	})
}

func (c *GuardedClient) ResizeVolume(slot string, newSizeBytes int64) error {
	return c.mutate("resize_volume", slot, func() error {
		return c.inner.ResizeVolume(slot, newSizeBytes)
	})
}

func (c *GuardedClient) DeleteFile(path string) error {
	return c.mutate("delete_file", path, func() error {
		return c.inner.DeleteFile(path)
	})
}

func (c *GuardedClient) CreateSnapshot(opts CreateSnapshotOptions) (*SnapshotInfo, error) {
	var snapshot *SnapshotInfo
	err := c.mutate("create_snapshot", opts.Name, func() error {
		var err error
		snapshot, err = c.inner.CreateSnapshot(opts)
		return err
	})
	if err != nil {
		return nil, err
	}
	if c.dryRun && snapshot == nil {
		return &SnapshotInfo{
			Name:         opts.Name,
			SourceVolume: opts.SourceVolume,
			FilePath:     opts.BasePath + "/" + opts.Name + ".img",
		}, nil
	}
	return snapshot, nil
}

func (c *GuardedClient) DeleteSnapshot(snapshotID string) error {
	return c.mutate("delete_snapshot", snapshotID, func() error {
		return c.inner.DeleteSnapshot(snapshotID)
	})
}

func (c *GuardedClient) RestoreSnapshot(snapshotID string, newVolumeOpts CreateVolumeOptions) error {
	return c.mutate("restore_snapshot", newVolumeOpts.Slot, func() error {
		return c.inner.RestoreSnapshot(snapshotID, newVolumeOpts)
	})
}

func (c *GuardedClient) mutate(operation string, target string, fn func() error) error {
	start := time.Now()
	var err error

	defer func() {
		if c.observer != nil {
			duration := time.Since(start)
			c.observer.RecordRDSMutation(operation, err, c.dryRun, duration)
			if auditObserver, ok := c.observer.(MutationAuditObserver); ok {
				status := "success"
				if err != nil {
					status = "failure"
				}
				auditObserver.RecordRDSMutationAudit(operation, target, status, c.dryRun, duration)
			}
		}
	}()

	if c.dryRun {
		klog.Infof("RDS mutation dry-run: operation=%s target=%s", operation, target)
		return nil
	}

	if c.limiter != nil {
		waitStart := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
		defer cancel()
		err = c.limiter.Wait(ctx)
		waitDuration := time.Since(waitStart)
		if c.observer != nil {
			c.observer.RecordRDSMutationRateLimitWait(operation, waitDuration)
		}
		if err != nil {
			return fmt.Errorf("timed out waiting for RDS mutation rate limit (%s %s): %w", operation, target, err)
		}
	}

	err = fn()
	return err
}
