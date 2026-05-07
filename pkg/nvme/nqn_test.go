package nvme

import (
	"os"
	"strings"
	"testing"
)

func TestValidateNQNPrefix_EmptyPrefix(t *testing.T) {
	err := ValidateNQNPrefix("")
	if err == nil {
		t.Fatal("Expected error for empty prefix, got nil")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("Expected 'required' in error message, got: %v", err)
	}
	if !strings.Contains(err.Error(), EnvManagedNQNPrefix) {
		t.Errorf("Expected env var name %q in error message, got: %v", EnvManagedNQNPrefix, err)
	}
}

func TestValidateNQNPrefix_TooLong(t *testing.T) {
	// Create a prefix longer than 223 bytes
	longPrefix := "nqn.2000-02.com.mikrotik:" + strings.Repeat("a", 200)
	err := ValidateNQNPrefix(longPrefix)
	if err == nil {
		t.Fatal("Expected error for too-long prefix, got nil")
	}
	if !strings.Contains(err.Error(), "223 bytes") {
		t.Errorf("Expected '223 bytes' in error message, got: %v", err)
	}
}

func TestValidateNQNPrefix_MissingNqnDot(t *testing.T) {
	err := ValidateNQNPrefix("invalid-prefix:pvc-")
	if err == nil {
		t.Fatal("Expected error for prefix missing 'nqn.', got nil")
	}
	if !strings.Contains(err.Error(), "must start with 'nqn.'") {
		t.Errorf("Expected 'must start with nqn.' in error message, got: %v", err)
	}
}

func TestValidateNQNPrefix_MissingColon(t *testing.T) {
	err := ValidateNQNPrefix("nqn.2000-02.com.mikrotik")
	if err == nil {
		t.Fatal("Expected error for prefix missing ':', got nil")
	}
	if !strings.Contains(err.Error(), "must contain ':'") {
		t.Errorf("Expected 'must contain colon' in error message, got: %v", err)
	}
}

func TestValidateNQNPrefix_ValidPrefix(t *testing.T) {
	validPrefixes := []string{
		"nqn.2000-02.com.mikrotik:pvc-",
		"nqn.2014-08.org.nvmexpress:uuid:",
		"nqn.1988-11.com.dell:PowerStore:",
	}

	for _, prefix := range validPrefixes {
		err := ValidateNQNPrefix(prefix)
		if err != nil {
			t.Errorf("Expected no error for valid prefix %q, got: %v", prefix, err)
		}
	}
}

func TestNQNMatchesPrefix_Matching(t *testing.T) {
	tests := []struct {
		nqn    string
		prefix string
		expect bool
	}{
		{
			nqn:    "nqn.2000-02.com.mikrotik:pvc-12345",
			prefix: "nqn.2000-02.com.mikrotik:pvc-",
			expect: true,
		},
		{
			nqn:    "nqn.2000-02.com.mikrotik:pvc-67890",
			prefix: "nqn.2000-02.com.mikrotik:pvc-",
			expect: true,
		},
		{
			nqn:    "nqn.2000-02.com.mikrotik:nixos-node1",
			prefix: "nqn.2000-02.com.mikrotik:pvc-",
			expect: false,
		},
		{
			nqn:    "nqn.2000-02.com.mikrotik:nixos-node2",
			prefix: "nqn.2000-02.com.mikrotik:nixos-",
			expect: true,
		},
		{
			nqn:    "nqn.2014-08.org.nvmexpress:uuid:12345",
			prefix: "nqn.2000-02.com.mikrotik:pvc-",
			expect: false,
		},
		{
			nqn:    "nqn.2000-02.com.mikrotik:pvc-12345",
			prefix: "",
			expect: false,
		},
	}

	for _, tt := range tests {
		result := NQNMatchesPrefix(tt.nqn, tt.prefix)
		if result != tt.expect {
			t.Errorf("NQNMatchesPrefix(%q, %q) = %v, expected %v",
				tt.nqn, tt.prefix, result, tt.expect)
		}
	}
}

func TestNQNMatchesPrefix_CaseSensitive(t *testing.T) {
	// NVMe spec requires case-sensitive NQN comparison
	nqn := "nqn.2000-02.com.mikrotik:pvc-12345"
	prefix := "nqn.2000-02.com.mikrotik:PVC-" // uppercase PVC

	result := NQNMatchesPrefix(nqn, prefix)
	if result {
		t.Error("Expected case-sensitive match to fail (PVC vs pvc)")
	}
}

func TestGetManagedNQNPrefix_NotSet(t *testing.T) {
	// Clear the environment variable
	oldValue := os.Getenv(EnvManagedNQNPrefix)
	os.Unsetenv(EnvManagedNQNPrefix)
	defer func() {
		if oldValue != "" {
			os.Setenv(EnvManagedNQNPrefix, oldValue)
		}
	}()

	prefix, err := GetManagedNQNPrefix()
	if err == nil {
		t.Fatal("Expected error when env var not set, got nil")
	}
	if prefix != "" {
		t.Errorf("Expected empty prefix on error, got %q", prefix)
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("Expected 'required' in error message, got: %v", err)
	}
}

func TestGetManagedNQNPrefix_Invalid(t *testing.T) {
	// Set invalid prefix (missing colon, but has nqn. prefix)
	oldValue := os.Getenv(EnvManagedNQNPrefix)
	os.Setenv(EnvManagedNQNPrefix, "nqn.2000-02.com.mikrotik")
	defer func() {
		if oldValue != "" {
			os.Setenv(EnvManagedNQNPrefix, oldValue)
		} else {
			os.Unsetenv(EnvManagedNQNPrefix)
		}
	}()

	prefix, err := GetManagedNQNPrefix()
	if err == nil {
		t.Fatal("Expected error for invalid prefix, got nil")
	}
	if prefix != "" {
		t.Errorf("Expected empty prefix on error, got %q", prefix)
	}
	if !strings.Contains(err.Error(), "must contain ':'") {
		t.Errorf("Expected colon error in message, got: %v", err)
	}
}

func TestGetManagedNQNPrefix_Valid(t *testing.T) {
	validPrefix := "nqn.2000-02.com.mikrotik:pvc-"

	// Set valid prefix
	oldValue := os.Getenv(EnvManagedNQNPrefix)
	os.Setenv(EnvManagedNQNPrefix, validPrefix)
	defer func() {
		if oldValue != "" {
			os.Setenv(EnvManagedNQNPrefix, oldValue)
		} else {
			os.Unsetenv(EnvManagedNQNPrefix)
		}
	}()

	prefix, err := GetManagedNQNPrefix()
	if err != nil {
		t.Fatalf("Expected no error for valid prefix, got: %v", err)
	}
	if prefix != validPrefix {
		t.Errorf("Expected prefix %q, got %q", validPrefix, prefix)
	}
}
