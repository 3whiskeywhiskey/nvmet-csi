package utils

import (
	"strings"
	"testing"
)

func TestGenerateVolumeID(t *testing.T) {
	id := GenerateVolumeID()

	if !strings.HasPrefix(id, VolumeIDPrefix) {
		t.Errorf("Generated volume ID does not have expected prefix: %s", id)
	}

	if err := ValidateVolumeID(id); err != nil {
		t.Errorf("Generated volume ID is invalid: %v", err)
	}

	// Generate another and ensure uniqueness
	id2 := GenerateVolumeID()
	if id == id2 {
		t.Error("Generated volume IDs are not unique")
	}
}

func TestValidateVolumeID(t *testing.T) {
	tests := []struct {
		name      string
		volumeID  string
		expectErr bool
	}{
		{
			name:      "valid volume ID",
			volumeID:  "pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			expectErr: false,
		},
		{
			name:      "empty volume ID",
			volumeID:  "",
			expectErr: true,
		},
		{
			name:      "missing prefix",
			volumeID:  "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			expectErr: true,
		},
		{
			name:      "invalid format",
			volumeID:  "pvc-invalid",
			expectErr: true,
		},
		{
			name:      "uppercase UUID",
			volumeID:  "pvc-A1B2C3D4-E5F6-7890-ABCD-EF1234567890",
			expectErr: true,
		},
		{
			name:      "special characters",
			volumeID:  "pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890; rm -rf /",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateVolumeID(tt.volumeID)
			if tt.expectErr && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectErr && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestValidateSlotName(t *testing.T) {
	tests := []struct {
		name      string
		slot      string
		expectErr bool
	}{
		{
			name:      "valid slot",
			slot:      "pvc-abc123",
			expectErr: false,
		},
		{
			name:      "valid slot with hyphens",
			slot:      "pvc-a1b2c3d4-e5f6-7890",
			expectErr: false,
		},
		{
			name:      "empty slot",
			slot:      "",
			expectErr: true,
		},
		{
			name:      "slot with spaces",
			slot:      "pvc abc123",
			expectErr: true,
		},
		{
			name:      "slot with semicolon",
			slot:      "pvc-abc123; rm -rf /",
			expectErr: true,
		},
		{
			name:      "slot with pipe",
			slot:      "pvc-abc|evil",
			expectErr: true,
		},
		{
			name:      "slot with dollar sign",
			slot:      "pvc-$var",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSlotName(tt.slot)
			if tt.expectErr && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectErr && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestVolumeIDToNQN(t *testing.T) {
	tests := []struct {
		name        string
		volumeID    string
		expectedNQN string
		expectErr   bool
	}{
		{
			name:        "valid volume ID",
			volumeID:    "pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			expectedNQN: "nqn.2026-05.works.whiskey.nvmet-csi:pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			expectErr:   false,
		},
		{
			name:      "invalid volume ID",
			volumeID:  "invalid; rm -rf /",
			expectErr: true,
		},
		{
			name:      "empty volume ID",
			volumeID:  "",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nqn, err := VolumeIDToNQN(tt.volumeID)
			if tt.expectErr {
				if err == nil {
					t.Error("Expected error but got nil")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if nqn != tt.expectedNQN {
					t.Errorf("Expected NQN %s, got %s", tt.expectedNQN, nqn)
				}
			}
		})
	}
}

func TestVolumeIDToNQNWithPrefix(t *testing.T) {
	volumeID := "pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890"
	tests := []struct {
		name        string
		prefix      string
		expectedNQN string
	}{
		{
			name:        "bare namespace",
			prefix:      "nqn.2000-02.com.example.storage",
			expectedNQN: "nqn.2000-02.com.example.storage:pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		},
		{
			name:        "managed safety prefix",
			prefix:      "nqn.2000-02.com.example.storage:pvc-",
			expectedNQN: "nqn.2000-02.com.example.storage:pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		},
		{
			name:        "lowercases prefix and volume id",
			prefix:      "NQN.2000-02.COM.EXAMPLE.STORAGE",
			expectedNQN: "nqn.2000-02.com.example.storage:pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nqn, err := VolumeIDToNQNWithPrefix(volumeID, tt.prefix)
			if err != nil {
				t.Fatalf("VolumeIDToNQNWithPrefix() error = %v", err)
			}
			if nqn != tt.expectedNQN {
				t.Fatalf("VolumeIDToNQNWithPrefix() = %q, want %q", nqn, tt.expectedNQN)
			}
		})
	}
}

func TestVolumeIDToFilePath(t *testing.T) {
	tests := []struct {
		name         string
		volumeID     string
		basePath     string
		expectedPath string
		expectErr    bool
	}{
		{
			name:         "valid with custom base path",
			volumeID:     "pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			basePath:     "/storage-pool/test",
			expectedPath: "/storage-pool/test/pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890.img",
			expectErr:    false,
		},
		{
			name:         "valid with default base path",
			volumeID:     "pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			basePath:     "",
			expectedPath: "/storage-pool/kubernetes-volumes/pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890.img",
			expectErr:    false,
		},
		{
			name:      "invalid volume ID",
			volumeID:  "invalid; rm -rf /",
			basePath:  "/storage-pool/test",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, err := VolumeIDToFilePath(tt.volumeID, tt.basePath)
			if tt.expectErr {
				if err == nil {
					t.Error("Expected error but got nil")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if path != tt.expectedPath {
					t.Errorf("Expected path %s, got %s", tt.expectedPath, path)
				}
			}
		})
	}
}

func TestExtractVolumeIDFromNQN(t *testing.T) {
	tests := []struct {
		name             string
		nqn              string
		expectedVolumeID string
		expectErr        bool
	}{
		{
			name:             "valid NQN",
			nqn:              "nqn.2026-05.works.whiskey.nvmet-csi:pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			expectedVolumeID: "pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			expectErr:        false,
		},
		{
			name:      "invalid prefix",
			nqn:       "nqn.other:pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			expectErr: true,
		},
		{
			name:      "missing volume ID",
			nqn:       "nqn.2026-05.works.whiskey.nvmet-csi:",
			expectErr: true,
		},
		{
			name:      "invalid volume ID in NQN",
			nqn:       "nqn.2026-05.works.whiskey.nvmet-csi:invalid; rm -rf /",
			expectErr: true,
		},
		{
			name:      "empty NQN",
			nqn:       "",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			volumeID, err := ExtractVolumeIDFromNQN(tt.nqn)
			if tt.expectErr {
				if err == nil {
					t.Error("Expected error but got nil")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if volumeID != tt.expectedVolumeID {
					t.Errorf("Expected volume ID %s, got %s", tt.expectedVolumeID, volumeID)
				}
			}
		})
	}
}

func TestValidateNQN(t *testing.T) {
	tests := []struct {
		name      string
		nqn       string
		expectErr bool
	}{
		// Valid NQNs
		{
			name:      "valid MikroTik NQN",
			nqn:       "nqn.2026-05.works.whiskey.nvmet-csi:pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			expectErr: false,
		},
		{
			name:      "valid NQN with hyphens in identifier",
			nqn:       "nqn.2014-08.org.nvmexpress:subsystem-name",
			expectErr: false,
		},
		{
			name:      "valid NQN with underscores",
			nqn:       "nqn.2014-08.com.example:storage_01",
			expectErr: false,
		},
		{
			name:      "valid NQN with dots in domain",
			nqn:       "nqn.2019-12.io.example.storage:vol-123",
			expectErr: false,
		},
		// Empty NQN
		{
			name:      "empty NQN",
			nqn:       "",
			expectErr: true,
		},
		// Invalid format
		{
			name:      "missing nqn prefix",
			nqn:       "2000-02.com.mikrotik:pvc-123",
			expectErr: true,
		},
		{
			name:      "invalid date format",
			nqn:       "nqn.00-02.com.mikrotik:pvc-123",
			expectErr: true,
		},
		{
			name:      "missing colon separator",
			nqn:       "nqn.2026-05.works.whiskey.nvmet-csi-pvc-123",
			expectErr: true,
		},
		{
			name:      "uppercase in domain",
			nqn:       "nqn.2000-02.COM.mikrotik:pvc-123",
			expectErr: true,
		},
		// Command injection attempts
		{
			name:      "semicolon injection",
			nqn:       "nqn.2026-05.works.whiskey.nvmet-csi:pvc-123; rm -rf /",
			expectErr: true,
		},
		{
			name:      "pipe injection",
			nqn:       "nqn.2026-05.works.whiskey.nvmet-csi:pvc-123 | cat /etc/passwd",
			expectErr: true,
		},
		{
			name:      "ampersand injection",
			nqn:       "nqn.2026-05.works.whiskey.nvmet-csi:pvc-123 && ls",
			expectErr: true,
		},
		{
			name:      "dollar sign injection",
			nqn:       "nqn.2026-05.works.whiskey.nvmet-csi:pvc-$USER",
			expectErr: true,
		},
		{
			name:      "backtick injection",
			nqn:       "nqn.2026-05.works.whiskey.nvmet-csi:pvc-`whoami`",
			expectErr: true,
		},
		{
			name:      "parenthesis injection",
			nqn:       "nqn.2026-05.works.whiskey.nvmet-csi:pvc-(test)",
			expectErr: true,
		},
		{
			name:      "redirect injection",
			nqn:       "nqn.2026-05.works.whiskey.nvmet-csi:pvc-123 > /tmp/evil",
			expectErr: true,
		},
		{
			name:      "newline injection",
			nqn:       "nqn.2026-05.works.whiskey.nvmet-csi:pvc-123\nrm -rf /",
			expectErr: true,
		},
		{
			name:      "space in NQN",
			nqn:       "nqn.2026-05.works.whiskey.nvmet-csi:pvc 123",
			expectErr: true,
		},
		{
			name:      "quote injection",
			nqn:       "nqn.2026-05.works.whiskey.nvmet-csi:pvc-'test'",
			expectErr: true,
		},
		{
			name:      "double quote injection",
			nqn:       "nqn.2026-05.works.whiskey.nvmet-csi:pvc-\"test\"",
			expectErr: true,
		},
		{
			name:      "backslash injection",
			nqn:       "nqn.2026-05.works.whiskey.nvmet-csi:pvc-test\\n",
			expectErr: true,
		},
		{
			name:      "wildcard injection",
			nqn:       "nqn.2026-05.works.whiskey.nvmet-csi:pvc-*",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateNQN(tt.nqn)
			if tt.expectErr && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectErr && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestValidateIPAddress(t *testing.T) {
	tests := []struct {
		name      string
		address   string
		expectErr bool
	}{
		// Valid IPv4
		{
			name:      "valid IPv4",
			address:   "192.168.1.1",
			expectErr: false,
		},
		{
			name:      "valid IPv4 localhost",
			address:   "127.0.0.1",
			expectErr: false,
		},
		{
			name:      "valid IPv4 10.x",
			address:   "198.51.100.20",
			expectErr: false,
		},
		// Valid IPv6
		{
			name:      "valid IPv6",
			address:   "2001:db8::1",
			expectErr: false,
		},
		{
			name:      "valid IPv6 localhost",
			address:   "::1",
			expectErr: false,
		},
		// Invalid
		{
			name:      "empty address",
			address:   "",
			expectErr: true,
		},
		{
			name:      "invalid IP format",
			address:   "not-an-ip",
			expectErr: true,
		},
		{
			name:      "invalid IPv4 octets",
			address:   "256.256.256.256",
			expectErr: true,
		},
		{
			name:      "hostname instead of IP",
			address:   "example.com",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateIPAddress(tt.address)
			if (err != nil) != tt.expectErr {
				t.Errorf("ValidateIPAddress() error = %v, expectErr %v", err, tt.expectErr)
			}
		})
	}
}

func TestValidatePort(t *testing.T) {
	tests := []struct {
		name            string
		port            int
		allowPrivileged bool
		expectErr       bool
	}{
		// Valid ports
		{
			name:            "valid high port",
			port:            8080,
			allowPrivileged: false,
			expectErr:       false,
		},
		{
			name:            "valid privileged port with allow",
			port:            80,
			allowPrivileged: true,
			expectErr:       false,
		},
		{
			name:            "valid NVMe/TCP port",
			port:            4420,
			allowPrivileged: false,
			expectErr:       false,
		},
		{
			name:            "port 1024 (first non-privileged)",
			port:            1024,
			allowPrivileged: false,
			expectErr:       false,
		},
		{
			name:            "port 65535 (maximum)",
			port:            65535,
			allowPrivileged: false,
			expectErr:       false,
		},
		// Invalid ports
		{
			name:            "port 0",
			port:            0,
			allowPrivileged: true,
			expectErr:       true,
		},
		{
			name:            "negative port",
			port:            -1,
			allowPrivileged: true,
			expectErr:       true,
		},
		{
			name:            "port too high",
			port:            65536,
			allowPrivileged: true,
			expectErr:       true,
		},
		{
			name:            "privileged port without allow",
			port:            22,
			allowPrivileged: false,
			expectErr:       true,
		},
		{
			name:            "port 1023 without allow",
			port:            1023,
			allowPrivileged: false,
			expectErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePort(tt.port, tt.allowPrivileged)
			if (err != nil) != tt.expectErr {
				t.Errorf("ValidatePort() error = %v, expectErr %v", err, tt.expectErr)
			}
		})
	}
}

func TestValidatePortString(t *testing.T) {
	tests := []struct {
		name            string
		portStr         string
		allowPrivileged bool
		expectedPort    int
		expectErr       bool
	}{
		{
			name:            "valid port string",
			portStr:         "4420",
			allowPrivileged: false,
			expectedPort:    4420,
			expectErr:       false,
		},
		{
			name:            "valid string with privileged",
			portStr:         "22",
			allowPrivileged: true,
			expectedPort:    22,
			expectErr:       false,
		},
		{
			name:            "empty string",
			portStr:         "",
			allowPrivileged: false,
			expectErr:       true,
		},
		{
			name:            "non-numeric string",
			portStr:         "abc",
			allowPrivileged: false,
			expectErr:       true,
		},
		{
			name:            "privileged port without allow",
			portStr:         "80",
			allowPrivileged: false,
			expectErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port, err := ValidatePortString(tt.portStr, tt.allowPrivileged)
			if (err != nil) != tt.expectErr {
				t.Errorf("ValidatePortString() error = %v, expectErr %v", err, tt.expectErr)
				return
			}
			if !tt.expectErr && port != tt.expectedPort {
				t.Errorf("ValidatePortString() = %d, expected %d", port, tt.expectedPort)
			}
		})
	}
}

func TestValidateNVMEAddress(t *testing.T) {
	tests := []struct {
		name      string
		address   string
		port      int
		expectErr bool
	}{
		{
			name:      "valid NVMe address",
			address:   "198.51.100.20",
			port:      4420,
			expectErr: false,
		},
		{
			name:      "valid with IPv6",
			address:   "2001:db8::1",
			port:      4420,
			expectErr: false,
		},
		{
			name:      "invalid IP",
			address:   "not-an-ip",
			port:      4420,
			expectErr: true,
		},
		{
			name:      "invalid port",
			address:   "198.51.100.20",
			port:      0,
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateNVMEAddress(tt.address, tt.port)
			if (err != nil) != tt.expectErr {
				t.Errorf("ValidateNVMEAddress() error = %v, expectErr %v", err, tt.expectErr)
			}
		})
	}
}

func TestValidateNVMETargetContext(t *testing.T) {
	tests := []struct {
		name            string
		nqn             string
		address         string
		port            int
		expectedAddress string
		expectErr       bool
	}{
		{
			name:            "valid without expected address",
			nqn:             "nqn.2026-05.works.whiskey.nvmet-csi:pvc-123",
			address:         "198.51.100.20",
			port:            4420,
			expectedAddress: "",
			expectErr:       false,
		},
		{
			name:            "valid with matching expected address",
			nqn:             "nqn.2026-05.works.whiskey.nvmet-csi:pvc-123",
			address:         "198.51.100.20",
			port:            4420,
			expectedAddress: "198.51.100.20",
			expectErr:       false,
		},
		{
			name:            "mismatched expected address",
			nqn:             "nqn.2026-05.works.whiskey.nvmet-csi:pvc-123",
			address:         "198.51.100.20",
			port:            4420,
			expectedAddress: "198.51.100.21",
			expectErr:       true,
		},
		{
			name:            "empty NQN",
			nqn:             "",
			address:         "198.51.100.20",
			port:            4420,
			expectedAddress: "",
			expectErr:       true,
		},
		{
			name:            "invalid address",
			nqn:             "nqn.2026-05.works.whiskey.nvmet-csi:pvc-123",
			address:         "invalid-ip",
			port:            4420,
			expectedAddress: "",
			expectErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateNVMETargetContext(tt.nqn, tt.address, tt.port, tt.expectedAddress)
			if (err != nil) != tt.expectErr {
				t.Errorf("ValidateNVMETargetContext() error = %v, expectErr %v", err, tt.expectErr)
			}
		})
	}
}

// Benchmark validation functions
func BenchmarkValidateNQN(b *testing.B) {
	nqn := "nqn.2026-05.works.whiskey.nvmet-csi:pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890"
	for i := 0; i < b.N; i++ {
		_ = ValidateNQN(nqn)
	}
}

func BenchmarkValidateNQNMalicious(b *testing.B) {
	nqn := "nqn.2026-05.works.whiskey.nvmet-csi:pvc-123; rm -rf /"
	for i := 0; i < b.N; i++ {
		_ = ValidateNQN(nqn)
	}
}

func BenchmarkValidateIPAddress(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = ValidateIPAddress("198.51.100.20")
	}
}

func BenchmarkValidatePort(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = ValidatePort(4420, false)
	}
}

func BenchmarkValidateNVMETargetContext(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = ValidateNVMETargetContext("nqn.2026-05.works.whiskey.nvmet-csi:pvc-123", "198.51.100.20", 4420, "")
	}
}
