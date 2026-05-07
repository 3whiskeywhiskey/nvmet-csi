package nvme

import (
	"fmt"
	"os"
	"strings"
)

// EnvManagedNQNPrefix is the environment variable for configuring the NQN prefix
// that this CSI driver should manage. Volumes with NQNs not matching this prefix
// will be skipped by orphan cleanup and other automated operations.
const EnvManagedNQNPrefix = "CSI_MANAGED_NQN_PREFIX"

// ValidateNQNPrefix validates an NQN prefix according to NVMe spec and CSI requirements.
// Returns an error if the prefix is invalid.
//
// Requirements:
// - Must not be empty
// - Must start with "nqn." per NVMe spec
// - Must contain ":" per NQN format (nqn.yyyy-mm.domain:identifier)
// - Must not exceed 223 bytes (NVMe spec limit)
func ValidateNQNPrefix(prefix string) error {
	if prefix == "" {
		return fmt.Errorf("managed NQN prefix is required (set %s)", EnvManagedNQNPrefix)
	}

	if len(prefix) > 223 {
		return fmt.Errorf("NQN prefix must not exceed 223 bytes (got %d bytes)", len(prefix))
	}

	if !strings.HasPrefix(prefix, "nqn.") {
		return fmt.Errorf("NQN prefix must start with 'nqn.' per NVMe spec (got %q)", prefix)
	}

	if !strings.Contains(prefix, ":") {
		return fmt.Errorf("NQN prefix must contain ':' per NVMe format (nqn.yyyy-mm.domain:identifier), got %q", prefix)
	}

	return nil
}

// NQNMatchesPrefix checks if an NQN matches the given prefix.
// Comparison is case-sensitive per NVMe spec.
func NQNMatchesPrefix(nqn, prefix string) bool {
	if prefix == "" {
		return false
	}
	return strings.HasPrefix(nqn, prefix)
}

// GetManagedNQNPrefix reads the managed NQN prefix from the environment and validates it.
// Returns an error if the environment variable is not set or the prefix is invalid.
func GetManagedNQNPrefix() (string, error) {
	prefix := os.Getenv(EnvManagedNQNPrefix)
	if err := ValidateNQNPrefix(prefix); err != nil {
		return "", err
	}
	return prefix, nil
}
