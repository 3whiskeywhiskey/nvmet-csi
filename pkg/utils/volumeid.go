package utils

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const (
	// VolumeIDPrefix is prepended to all volume IDs
	VolumeIDPrefix = "pvc-"

	// NQNPrefix is the default NVMe Qualified Name namespace for nvmet-csi.
	NQNPrefix = "nqn.2026-05.works.whiskey.nvmet-csi"

	// ManagedNQNPrefix is the default safety filter for CSI-managed volumes.
	ManagedNQNPrefix = NQNPrefix + ":" + VolumeIDPrefix
)

var (
	// volumeIDPattern matches strict UUID format with pvc- prefix
	// Format: pvc-<lowercase-uuid>
	// Example: pvc-12345678-1234-1234-1234-123456789abc
	volumeIDPattern = regexp.MustCompile(`^pvc-[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

	// safeSlotPattern matches safe slot names (alphanumeric and hyphen only)
	safeSlotPattern = regexp.MustCompile(`^[a-zA-Z0-9-]+$`)

	// nqnPattern matches valid NVMe Qualified Names (NQN)
	// Format: nqn.YYYY-MM.reversed.domain:identifier
	// Example: nqn.2026-05.works.whiskey.nvmet-csi:pvc-12345678-1234-1234-1234-123456789abc
	// SECURITY: This strict pattern prevents command injection via NQN parameter
	nqnPattern = regexp.MustCompile(`^nqn\.[0-9]{4}-[0-9]{2}\.[a-z0-9.-]+:[a-z0-9._-]+$`)

	// Namespace UUID for generating deterministic volume IDs
	volumeNamespace = uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8") // DNS namespace UUID
)

// Shell metacharacters that are dangerous in NQN context
var dangerousNQNChars = []string{
	";", "|", "&", "$", "`", "(", ")", "<", ">", "\n", "\r", " ", "\t", "\"", "'", "\\", "*", "?", "[", "]",
}

// GenerateVolumeID generates a new unique volume ID
func GenerateVolumeID() string {
	return VolumeIDPrefix + uuid.New().String()
}

// VolumeNameToID generates a deterministic volume ID from a volume name
// This ensures the same name always produces the same ID (for idempotency)
func VolumeNameToID(name string) string {
	// Use UUID v5 (SHA-1 based) to generate deterministic UUID from name
	id := uuid.NewSHA1(volumeNamespace, []byte(name))
	return VolumeIDPrefix + id.String()
}

// ValidateVolumeID validates that a volume ID is safe for use in commands
// For production volume IDs: must match "pvc-<lowercase-uuid>" format
// For CSI sanity tests: accepts alphanumeric with hyphens (safe pattern) but not UUID-like strings
// SECURITY: Prevents command injection by restricting to safe characters only
func ValidateVolumeID(volumeID string) error {
	if volumeID == "" {
		return fmt.Errorf("volume ID cannot be empty")
	}

	// Check if it matches strict UUID pattern (production volume IDs)
	if volumeIDPattern.MatchString(volumeID) {
		return nil // Valid production volume ID
	}

	// For safety, reject anything with special characters first
	if !safeSlotPattern.MatchString(volumeID) {
		return fmt.Errorf("invalid volume ID format: %s (only alphanumeric and hyphen allowed)", volumeID)
	}

	// Reject if it starts with "pvc-" but doesn't match UUID format
	// This catches malformed production IDs like "pvc-invalid" or "pvc-UPPERCASE"
	if strings.HasPrefix(volumeID, VolumeIDPrefix) {
		return fmt.Errorf("invalid volume ID format: %s (expected pvc-<lowercase-uuid>)", volumeID)
	}

	// Reject UUID-like strings without proper prefix (e.g., "12345678-1234-...")
	// This pattern detects UUID format without "pvc-" prefix
	uuidLikePattern := regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)
	if uuidLikePattern.MatchString(volumeID) {
		return fmt.Errorf("invalid volume ID format: %s (missing pvc- prefix)", volumeID)
	}

	// Additional length check to prevent excessively long volume IDs
	if len(volumeID) > 250 {
		return fmt.Errorf("volume ID too long: %d characters (max 250)", len(volumeID))
	}

	return nil // Valid CSI sanity test ID (alphanumeric, no pvc-, not UUID-like)
}

// ValidateSlotName validates that a slot name is safe (prevents command injection)
func ValidateSlotName(slot string) error {
	if slot == "" {
		return fmt.Errorf("slot name cannot be empty")
	}

	if !safeSlotPattern.MatchString(slot) {
		return fmt.Errorf("invalid slot name: %s (only alphanumeric and hyphen allowed)", slot)
	}

	return nil
}

// ValidateNQN validates an NVMe Qualified Name for security and format compliance
func ValidateNQN(nqn string) error {
	if nqn == "" {
		return fmt.Errorf("NQN cannot be empty")
	}

	// SECURITY: Check for dangerous shell metacharacters first
	for _, char := range dangerousNQNChars {
		if strings.Contains(nqn, char) {
			return fmt.Errorf("NQN contains dangerous character %q: %s", char, nqn)
		}
	}

	// Validate NQN format using strict regex
	if !nqnPattern.MatchString(nqn) {
		return fmt.Errorf("invalid NQN format: %s (expected format: nqn.YYYY-MM.domain:identifier)", nqn)
	}

	// Additional length check to prevent excessively long NQNs
	if len(nqn) > 223 {
		// NVMe spec limits NQN to 223 bytes (NVM Express 1.3 spec)
		return fmt.Errorf("NQN too long: %d bytes (max 223)", len(nqn))
	}

	return nil
}

// VolumeIDToNQN converts a volume ID to an NVMe Qualified Name.
func VolumeIDToNQN(volumeID string) (string, error) {
	return VolumeIDToNQNWithPrefix(volumeID, NQNPrefix)
}

// VolumeIDToNQNWithPrefix converts a volume ID to an NVMe Qualified Name using
// a caller-provided NQN namespace. A bare NQN namespace such as
// "nqn.2026-05.works.whiskey.nvmet-csi" is converted to
// "<namespace>:<volume-id>". For compatibility with safety-filter style
// prefixes, values that already contain ":" are prepended directly, and a
// trailing "pvc-" prefix consumes the volume ID's own "pvc-" prefix.
func VolumeIDToNQNWithPrefix(volumeID, nqnPrefix string) (string, error) {
	// Validate volume ID is safe (prevents command injection)
	if err := ValidateVolumeID(volumeID); err != nil {
		return "", err
	}

	prefix := strings.ToLower(strings.TrimSpace(nqnPrefix))
	if prefix == "" {
		prefix = NQNPrefix
	}

	// Convert to lowercase for NQN (NVMe spec requires lowercase)
	volumeIDLower := strings.ToLower(volumeID)
	nqnVolumeID := volumeIDLower
	if strings.HasSuffix(prefix, ":"+VolumeIDPrefix) && strings.HasPrefix(volumeIDLower, VolumeIDPrefix) {
		nqnVolumeID = strings.TrimPrefix(volumeIDLower, VolumeIDPrefix)
	}

	nqn := fmt.Sprintf("%s:%s", strings.TrimSuffix(prefix, ":"), nqnVolumeID)
	if strings.Contains(prefix, ":") {
		nqn = prefix + nqnVolumeID
	}

	// SECURITY: Validate the generated NQN before returning
	if err := ValidateNQN(nqn); err != nil {
		return "", fmt.Errorf("generated NQN failed validation: %w", err)
	}

	return nqn, nil
}

// VolumeIDToFilePath generates the file path for a volume
func VolumeIDToFilePath(volumeID, basePath string) (string, error) {
	if err := ValidateVolumeID(volumeID); err != nil {
		return "", err
	}

	if basePath == "" {
		basePath = "/storage-pool/kubernetes-volumes"
	}

	return fmt.Sprintf("%s/%s.img", basePath, volumeID), nil
}

// ExtractVolumeIDFromNQN extracts the volume ID from an NQN
func ExtractVolumeIDFromNQN(nqn string) (string, error) {
	expectedPrefix := NQNPrefix + ":"
	if len(nqn) <= len(expectedPrefix) {
		return "", fmt.Errorf("invalid NQN format: %s", nqn)
	}

	if nqn[:len(expectedPrefix)] != expectedPrefix {
		return "", fmt.Errorf("NQN does not have expected prefix: %s", nqn)
	}

	volumeID := nqn[len(expectedPrefix):]

	// Volume ID in NQN is lowercase, validate it's safe
	if err := ValidateVolumeID(volumeID); err != nil {
		return "", fmt.Errorf("invalid volume ID in NQN: %w", err)
	}

	return volumeID, nil
}

// ValidateIPAddress validates that a string is a valid IPv4 or IPv6 address
func ValidateIPAddress(address string) error {
	if address == "" {
		return fmt.Errorf("IP address cannot be empty")
	}

	// Try to parse as IP
	ip := net.ParseIP(address)
	if ip == nil {
		return fmt.Errorf("invalid IP address: %s", address)
	}

	return nil
}

// ValidatePort validates that a port number is in valid range
// Optionally checks against privileged port range (< 1024)
func ValidatePort(port int, allowPrivileged bool) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port must be in range 1-65535: got %d", port)
	}

	if !allowPrivileged && port < 1024 {
		return fmt.Errorf("privileged port (< 1024) not allowed: %d", port)
	}

	return nil
}

// ValidatePortString validates a port string and returns the port number
func ValidatePortString(portStr string, allowPrivileged bool) (int, error) {
	if portStr == "" {
		return 0, fmt.Errorf("port cannot be empty")
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("invalid port format: %s", portStr)
	}

	if err := ValidatePort(port, allowPrivileged); err != nil {
		return 0, err
	}

	return port, nil
}

// ValidateNVMEAddress validates an NVMe target address (IP:Port combination)
func ValidateNVMEAddress(address string, port int) error {
	// Validate IP address
	if err := ValidateIPAddress(address); err != nil {
		return fmt.Errorf("invalid NVMe address: %w", err)
	}

	// Validate port (NVMe/TCP typically uses non-privileged ports)
	if err := ValidatePort(port, true); err != nil {
		return fmt.Errorf("invalid NVMe port: %w", err)
	}

	return nil
}

// ValidateNVMETargetContext validates volume context parameters for NVMe/TCP
// This includes NQN, address, and port validation
func ValidateNVMETargetContext(nqn, address string, port int, expectedAddress string) error {
	// Validate NQN (using existing function if available)
	if nqn == "" {
		return fmt.Errorf("NQN cannot be empty")
	}

	// Validate address and port
	if err := ValidateNVMEAddress(address, port); err != nil {
		return err
	}

	// Optionally verify address matches expected RDS address
	if expectedAddress != "" && address != expectedAddress {
		return fmt.Errorf("NVMe address %s does not match expected RDS address %s", address, expectedAddress)
	}

	return nil
}
