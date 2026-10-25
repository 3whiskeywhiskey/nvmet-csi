package nvme

import (
	"fmt"
)

// ConnectionConfig holds NVMe/TCP connection resilience parameters
// These parameters control kernel-level reconnection behavior
type ConnectionConfig struct {
	// CtrlLossTmo is the controller loss timeout in seconds
	// -1 = unlimited (recommended for production, prevents filesystem read-only mount)
	// 0 = use kernel default (typically 600 seconds)
	// >0 = timeout in seconds
	CtrlLossTmo int

	// ReconnectDelay is the delay between reconnect attempts in seconds
	// Must be > 0
	// Default: 5 seconds
	ReconnectDelay int

	// KeepAliveTmo is the keep-alive timeout in seconds
	// 0 = use kernel default
	// >0 = timeout in seconds
	KeepAliveTmo int

	// NrIOQueues is the number of I/O queues to request
	// 0 = kernel default (one per online CPU)
	// >0 = request this many I/O queues
	NrIOQueues int

	// QueueSize is the depth of each I/O queue
	// 0 = kernel default (128)
	// >0 = queue depth, between MinQueueSize and MaxQueueSize
	QueueSize int
}

// Kernel limits for the nvme-fabrics queue_size option
// (NVMF_MIN_QUEUE_SIZE / NVMF_MAX_QUEUE_SIZE).
const (
	MinQueueSize = 16
	MaxQueueSize = 1024
)

// ValidateQueueLimits checks NrIOQueues and QueueSize against the limits the
// kernel accepts. Zero means "kernel default" for both.
func ValidateQueueLimits(nrIOQueues, queueSize int) error {
	if nrIOQueues < 0 {
		return fmt.Errorf("nr_io_queues must be 0 (kernel default) or positive; got %d", nrIOQueues)
	}
	if queueSize != 0 && (queueSize < MinQueueSize || queueSize > MaxQueueSize) {
		return fmt.Errorf("queue_size must be 0 (kernel default) or between %d and %d; got %d", MinQueueSize, MaxQueueSize, queueSize)
	}
	return nil
}

// DefaultConnectionConfig returns the recommended connection configuration
// for production use with unlimited reconnection
func DefaultConnectionConfig() ConnectionConfig {
	return ConnectionConfig{
		CtrlLossTmo:    -1, // Unlimited reconnection - prevents filesystem read-only mount
		ReconnectDelay: 5,  // 5 second retry interval
		KeepAliveTmo:   0,  // Use kernel default
	}
}

// BuildConnectArgs builds the nvme connect command arguments with connection parameters
// The returned slice starts with "connect" and includes all necessary flags
func BuildConnectArgs(target Target, config ConnectionConfig) []string {
	args := []string{
		"connect",
		"-t", target.Transport,
		"-a", target.TargetAddress,
		"-s", fmt.Sprintf("%d", target.TargetPort),
		"-n", target.NQN,
	}

	// Add controller loss timeout if not using kernel default (0)
	// -1 = unlimited, >0 = specific timeout
	if config.CtrlLossTmo != 0 {
		args = append(args, "-l", fmt.Sprintf("%d", config.CtrlLossTmo))
	}

	// Add reconnect delay if specified (must be > 0)
	if config.ReconnectDelay > 0 {
		args = append(args, "-c", fmt.Sprintf("%d", config.ReconnectDelay))
	}

	// Add keep-alive timeout if specified (must be > 0)
	if config.KeepAliveTmo > 0 {
		args = append(args, "-k", fmt.Sprintf("%d", config.KeepAliveTmo))
	}

	// Limit the I/O queue count and depth if specified. Each queue makes the
	// target allocate a command array sized by the queue depth; the kernel
	// default (one queue per host CPU, depth 128) asks a RouterOS target for
	// ~80 order-6 allocations per connect.
	if config.NrIOQueues > 0 {
		args = append(args, "-i", fmt.Sprintf("%d", config.NrIOQueues))
	}
	if config.QueueSize > 0 {
		args = append(args, "-Q", fmt.Sprintf("%d", config.QueueSize))
	}

	// Add host NQN if specified
	if target.HostNQN != "" {
		args = append(args, "-q", target.HostNQN)
	}

	return args
}
