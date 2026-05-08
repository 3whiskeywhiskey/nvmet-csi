package driver

import (
	"fmt"
	"strconv"
	"time"

	"k8s.io/klog/v2"

	"git.srvlab.io/whiskey/nvmet-csi/pkg/backend"
)

// NVMe connection parameter keys for StorageClass
const (
	paramBackend  = "backend"
	paramExecutor = "executor"

	paramZFSPool      = "zfsPool"
	paramTargetAddr   = "targetAddress"
	paramSSHTarget    = "sshTarget"
	paramDeviceRoot   = "deviceRoot"
	paramConfigFSRoot = "configFSRoot"

	// paramCtrlLossTmo is the controller loss timeout parameter key
	// Value: integer seconds, -1 for unlimited, 0 for kernel default
	paramCtrlLossTmo = "ctrlLossTmo"

	// paramReconnectDelay is the reconnect delay parameter key
	// Value: integer seconds, must be > 0
	paramReconnectDelay = "reconnectDelay"

	// paramKeepAliveTmo is the keep-alive timeout parameter key
	// Value: integer seconds, 0 for kernel default
	paramKeepAliveTmo = "keepAliveTmo"
)

type BackendSelection struct {
	Type         string
	Executor     string
	ZFSPool      string
	TargetAddr   string
	SSHTarget    string
	DeviceRoot   string
	ConfigFSRoot string
}

func DefaultBackendSelection() BackendSelection {
	return BackendSelection{
		Type:     backend.TypeRouterOSRDS,
		Executor: backend.ExecutorSSH,
	}
}

func ParseBackendSelection(params map[string]string) (BackendSelection, error) {
	selection := DefaultBackendSelection()
	if value, ok := params[paramBackend]; ok && value != "" {
		selection.Type = value
	}
	if value, ok := params[paramExecutor]; ok && value != "" {
		selection.Executor = value
	}
	if value, ok := params[paramZFSPool]; ok && value != "" {
		selection.ZFSPool = value
	}
	if value, ok := params[paramTargetAddr]; ok && value != "" {
		selection.TargetAddr = value
	}
	if value, ok := params[paramNVMEAddress]; ok && value != "" {
		selection.TargetAddr = value
	}
	if value, ok := params[paramSSHTarget]; ok && value != "" {
		selection.SSHTarget = value
	}
	if value, ok := params[paramDeviceRoot]; ok && value != "" {
		selection.DeviceRoot = value
	}
	if value, ok := params[paramConfigFSRoot]; ok && value != "" {
		selection.ConfigFSRoot = value
	}

	switch selection.Type {
	case backend.TypeRouterOSRDS:
		if selection.Executor != backend.ExecutorSSH {
			return selection, fmt.Errorf("%s backend requires %s=%s", backend.TypeRouterOSRDS, paramExecutor, backend.ExecutorSSH)
		}
	case backend.TypeLinuxZOLNVMET:
		switch selection.Executor {
		case backend.ExecutorLocal, backend.ExecutorSSH, backend.ExecutorHostNamespace:
		default:
			return selection, fmt.Errorf("%s backend requires %s to be %q, %q, or %q", backend.TypeLinuxZOLNVMET, paramExecutor, backend.ExecutorLocal, backend.ExecutorSSH, backend.ExecutorHostNamespace)
		}
		if selection.ZFSPool == "" {
			return selection, fmt.Errorf("%s backend requires %s", backend.TypeLinuxZOLNVMET, paramZFSPool)
		}
		if selection.TargetAddr == "" {
			return selection, fmt.Errorf("%s backend requires %s or %s", backend.TypeLinuxZOLNVMET, paramTargetAddr, paramNVMEAddress)
		}
		if selection.Executor == backend.ExecutorSSH && selection.SSHTarget == "" {
			return selection, fmt.Errorf("%s backend with %s=%s requires %s", backend.TypeLinuxZOLNVMET, paramExecutor, backend.ExecutorSSH, paramSSHTarget)
		}
	default:
		return selection, fmt.Errorf("unsupported %s %q", paramBackend, selection.Type)
	}

	return selection, nil
}

func (s BackendSelection) volumeContext() map[string]string {
	out := map[string]string{
		paramBackend:  s.Type,
		paramExecutor: s.Executor,
	}
	if s.ZFSPool != "" {
		out[paramZFSPool] = s.ZFSPool
	}
	if s.TargetAddr != "" {
		out[paramTargetAddr] = s.TargetAddr
	}
	if s.SSHTarget != "" {
		out[paramSSHTarget] = s.SSHTarget
	}
	if s.DeviceRoot != "" {
		out[paramDeviceRoot] = s.DeviceRoot
	}
	if s.ConfigFSRoot != "" {
		out[paramConfigFSRoot] = s.ConfigFSRoot
	}
	return out
}

// NVMEConnectionParams holds parsed NVMe connection parameters from StorageClass
type NVMEConnectionParams struct {
	// CtrlLossTmo is the controller loss timeout in seconds
	// -1 = unlimited (recommended), 0 = kernel default, >0 = specific timeout
	CtrlLossTmo int

	// ReconnectDelay is the delay between reconnect attempts in seconds
	ReconnectDelay int

	// KeepAliveTmo is the keep-alive timeout in seconds
	KeepAliveTmo int
}

// DefaultNVMEConnectionParams returns the default connection parameters
func DefaultNVMEConnectionParams() NVMEConnectionParams {
	return NVMEConnectionParams{
		CtrlLossTmo:    -1, // Unlimited reconnection
		ReconnectDelay: 5,  // 5 seconds between retries
		KeepAliveTmo:   0,  // Use kernel default
	}
}

// ParseNVMEConnectionParams parses NVMe connection parameters from StorageClass parameters
// Returns default values for missing parameters and errors for invalid values
func ParseNVMEConnectionParams(params map[string]string) (NVMEConnectionParams, error) {
	// Start with defaults
	config := DefaultNVMEConnectionParams()

	// Parse ctrl_loss_tmo if present
	if val, ok := params[paramCtrlLossTmo]; ok {
		parsed, err := strconv.Atoi(val)
		if err != nil {
			return config, fmt.Errorf("invalid %s value %q: %w", paramCtrlLossTmo, val, err)
		}
		// Validate: must be >= -1
		// -1 = unlimited, 0 = kernel default, >0 = specific timeout
		if parsed < -1 {
			return config, fmt.Errorf("%s must be -1 (unlimited), 0 (kernel default), or positive; got %d", paramCtrlLossTmo, parsed)
		}
		config.CtrlLossTmo = parsed
	}

	// Parse reconnect_delay if present
	if val, ok := params[paramReconnectDelay]; ok {
		parsed, err := strconv.Atoi(val)
		if err != nil {
			return config, fmt.Errorf("invalid %s value %q: %w", paramReconnectDelay, val, err)
		}
		// Validate: must be > 0
		if parsed < 1 {
			return config, fmt.Errorf("%s must be positive; got %d", paramReconnectDelay, parsed)
		}
		config.ReconnectDelay = parsed
	}

	// Parse keep_alive_tmo if present
	if val, ok := params[paramKeepAliveTmo]; ok {
		parsed, err := strconv.Atoi(val)
		if err != nil {
			return config, fmt.Errorf("invalid %s value %q: %w", paramKeepAliveTmo, val, err)
		}
		// Validate: must be >= 0
		if parsed < 0 {
			return config, fmt.Errorf("%s must be non-negative; got %d", paramKeepAliveTmo, parsed)
		}
		config.KeepAliveTmo = parsed
	}

	return config, nil
}

// ToVolumeContext converts NVMEConnectionParams to a string map for inclusion in VolumeContext
// This allows the parameters to be passed from Controller to Node via CSI VolumeContext
func ToVolumeContext(params NVMEConnectionParams) map[string]string {
	return map[string]string{
		paramCtrlLossTmo:    fmt.Sprintf("%d", params.CtrlLossTmo),
		paramReconnectDelay: fmt.Sprintf("%d", params.ReconnectDelay),
		paramKeepAliveTmo:   fmt.Sprintf("%d", params.KeepAliveTmo),
	}
}

const (
	// Default migration timeout (5 minutes)
	DefaultMigrationTimeout = 5 * time.Minute
	// Minimum allowed timeout (30 seconds - anything less is unrealistic)
	MinMigrationTimeout = 30 * time.Second
	// Maximum allowed timeout (1 hour - prevent indefinite dual-attach)
	MaxMigrationTimeout = 1 * time.Hour
)

// ParseMigrationTimeout extracts and validates migrationTimeoutSeconds from parameters.
// Returns DefaultMigrationTimeout if not specified or invalid.
// Clamps value to [MinMigrationTimeout, MaxMigrationTimeout] range.
func ParseMigrationTimeout(params map[string]string) time.Duration {
	timeoutStr, ok := params["migrationTimeoutSeconds"]
	if !ok || timeoutStr == "" {
		return DefaultMigrationTimeout
	}

	seconds, err := strconv.Atoi(timeoutStr)
	if err != nil {
		klog.Warningf("Invalid migrationTimeoutSeconds '%s' (not an integer), using default %v",
			timeoutStr, DefaultMigrationTimeout)
		return DefaultMigrationTimeout
	}

	if seconds <= 0 {
		klog.Warningf("Invalid migrationTimeoutSeconds '%d' (must be positive), using default %v",
			seconds, DefaultMigrationTimeout)
		return DefaultMigrationTimeout
	}

	timeout := time.Duration(seconds) * time.Second

	// Clamp to valid range
	if timeout < MinMigrationTimeout {
		klog.Warningf("migrationTimeoutSeconds %v too short (min %v), using minimum",
			timeout, MinMigrationTimeout)
		return MinMigrationTimeout
	}

	if timeout > MaxMigrationTimeout {
		klog.Warningf("migrationTimeoutSeconds %v too long (max %v), using maximum",
			timeout, MaxMigrationTimeout)
		return MaxMigrationTimeout
	}

	return timeout
}
