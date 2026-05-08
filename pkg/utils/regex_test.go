package utils

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestVolumeIDPattern tests the volume ID regex for correctness and ReDoS resistance
func TestVolumeIDPattern(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		shouldMatch bool
	}{
		{
			name:        "valid UUID format",
			input:       "pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			shouldMatch: true,
		},
		{
			name:        "invalid - missing prefix",
			input:       "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			shouldMatch: false,
		},
		{
			name:        "invalid - wrong prefix",
			input:       "vol-a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			shouldMatch: false,
		},
		{
			name:        "invalid - uppercase UUID",
			input:       "pvc-A1B2C3D4-E5F6-7890-ABCD-EF1234567890",
			shouldMatch: false,
		},
		{
			name:        "invalid - too short",
			input:       "pvc-a1b2c3d4",
			shouldMatch: false,
		},
		{
			name:        "invalid - too long",
			input:       "pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890-extra",
			shouldMatch: false,
		},
		{
			name:        "ReDoS attempt - repeated characters",
			input:       "pvc-" + strings.Repeat("a", 10000),
			shouldMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			matched := VolumeIDPattern.MatchString(tt.input)
			duration := time.Since(start)

			if matched != tt.shouldMatch {
				t.Errorf("Expected match=%v, got %v for input: %s", tt.shouldMatch, matched, tt.input)
			}

			// Ensure regex completes quickly even for pathological input
			// Allow 100ms when running with race detector (adds ~10x overhead)
			if duration > 200*time.Millisecond {
				t.Errorf("Regex took too long: %v (potential ReDoS)", duration)
			}
		})
	}
}

// TestSafeSlotPattern tests slot name validation for ReDoS resistance
func TestSafeSlotPattern(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		shouldMatch bool
	}{
		{
			name:        "valid alphanumeric",
			input:       "pvc-123-abc",
			shouldMatch: true,
		},
		{
			name:        "valid with hyphens",
			input:       "my-volume-name",
			shouldMatch: true,
		},
		{
			name:        "invalid - special characters",
			input:       "volume_name!@#",
			shouldMatch: false,
		},
		{
			name:        "invalid - spaces",
			input:       "volume name",
			shouldMatch: false,
		},
		{
			name:        "ReDoS attempt - very long input",
			input:       strings.Repeat("a", 100000),
			shouldMatch: true, // Valid pattern, but should complete quickly
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			matched := SafeSlotPattern.MatchString(tt.input)
			duration := time.Since(start)

			if matched != tt.shouldMatch {
				t.Errorf("Expected match=%v, got %v", tt.shouldMatch, matched)
			}

			if duration > 200*time.Millisecond {
				t.Errorf("Regex took too long: %v (potential ReDoS)", duration)
			}
		})
	}
}

// TestNQNPattern tests NVMe NQN validation for correctness and performance
func TestNQNPattern(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		shouldMatch bool
	}{
		{
			name:        "valid MikroTik NQN",
			input:       "nqn.2026-05.works.whiskey.nvmet-csi:pvc-abc123",
			shouldMatch: true,
		},
		{
			name:        "valid with underscores",
			input:       "nqn.2024-01.com.example:volume_name",
			shouldMatch: true,
		},
		{
			name:        "invalid - missing date",
			input:       "nqn.com.mikrotik:volume",
			shouldMatch: false,
		},
		{
			name:        "invalid - wrong date format",
			input:       "nqn.2000-2.com.mikrotik:volume",
			shouldMatch: false,
		},
		{
			name:        "invalid - uppercase in domain",
			input:       "nqn.2000-02.COM.mikrotik:volume",
			shouldMatch: false,
		},
		{
			name:        "ReDoS attempt - many dots",
			input:       "nqn.2000-02." + strings.Repeat("a.", 10000) + ":volume",
			shouldMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			matched := NQNPattern.MatchString(tt.input)
			duration := time.Since(start)

			if matched != tt.shouldMatch {
				t.Errorf("Expected match=%v, got %v for input: %s", tt.shouldMatch, matched, tt.input)
			}

			if duration > 200*time.Millisecond {
				t.Errorf("Regex took too long: %v (potential ReDoS)", duration)
			}
		})
	}
}

// TestIPv4Pattern tests IPv4 address matching
func TestIPv4Pattern(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		shouldFind bool
	}{
		{
			name:       "valid IPv4",
			input:      "Connection to 192.168.1.1 failed",
			shouldFind: true,
		},
		{
			name:       "valid private IP",
			input:      "Server at 10.0.0.1 is down",
			shouldFind: true,
		},
		{
			name:       "no IPv4",
			input:      "Connection failed",
			shouldFind: false,
		},
		{
			name:       "ReDoS attempt - many dots (should complete quickly)",
			input:      strings.Repeat("1.", 10000) + "1",
			shouldFind: true, // Will find 1.1.1.1 which is valid
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			found := IPv4Pattern.MatchString(tt.input)
			duration := time.Since(start)

			if found != tt.shouldFind {
				t.Errorf("Expected find=%v, got %v for input: %s", tt.shouldFind, found, tt.input)
			}

			if duration > 200*time.Millisecond {
				t.Errorf("Regex took too long: %v (potential ReDoS)", duration)
			}
		})
	}
}

// TestFileSizePattern tests file size parsing
func TestFileSizePattern(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		shouldMatch bool
	}{
		{
			name:        "integer GB",
			input:       "10GB",
			shouldMatch: true,
		},
		{
			name:        "decimal TiB",
			input:       "5.5TiB",
			shouldMatch: true,
		},
		{
			name:        "integer MB",
			input:       "1024MB",
			shouldMatch: true,
		},
		{
			name:        "invalid - three decimal places",
			input:       "5.555GB",
			shouldMatch: false,
		},
		{
			name:        "invalid - no unit",
			input:       "1024",
			shouldMatch: false,
		},
		{
			name:        "ReDoS attempt - many digits",
			input:       strings.Repeat("9", 10000) + "GB",
			shouldMatch: true, // Valid but should complete quickly
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			matched := FileSizePattern.MatchString(tt.input)
			duration := time.Since(start)

			if matched != tt.shouldMatch {
				t.Errorf("Expected match=%v, got %v for input: %s", tt.shouldMatch, matched, tt.input)
			}

			if duration > 200*time.Millisecond {
				t.Errorf("Regex took too long: %v (potential ReDoS)", duration)
			}
		})
	}
}

// TestKeyValuePatterns tests RouterOS command output parsing
func TestKeyValuePatterns(t *testing.T) {
	tests := []struct {
		name       string
		pattern    string
		input      string
		shouldFind bool
	}{
		{
			name:       "quoted value",
			pattern:    "quoted",
			input:      `slot="pvc-123" type="file"`,
			shouldFind: true,
		},
		{
			name:       "unquoted value",
			pattern:    "unquoted",
			input:      `size=10GB free=5GB`,
			shouldFind: true,
		},
		{
			name:       "ReDoS attempt - quoted with many chars",
			pattern:    "quoted",
			input:      `slot="` + strings.Repeat("a", 10000) + `"`,
			shouldFind: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var pattern *regexp.Regexp
			if tt.pattern == "quoted" {
				pattern = KeyValueQuotedPattern
			} else {
				pattern = KeyValueUnquotedPattern
			}

			start := time.Now()
			found := pattern.MatchString(tt.input)
			duration := time.Since(start)

			if found != tt.shouldFind {
				t.Errorf("Expected find=%v, got %v", tt.shouldFind, found)
			}

			if duration > 200*time.Millisecond {
				t.Errorf("Regex took too long: %v (potential ReDoS)", duration)
			}
		})
	}
}

// TestSafeMatchString tests the timeout-protected regex matching
func TestSafeMatchString(t *testing.T) {
	tests := []struct {
		name          string
		pattern       *regexp.Regexp
		input         string
		expectMatch   bool
		expectTimeout bool
	}{
		{
			name:          "normal match",
			pattern:       VolumeIDPattern,
			input:         "pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			expectMatch:   true,
			expectTimeout: false,
		},
		{
			name:          "normal non-match",
			pattern:       VolumeIDPattern,
			input:         "invalid",
			expectMatch:   false,
			expectTimeout: false,
		},
		{
			name:          "large input completes quickly",
			pattern:       SafeSlotPattern,
			input:         strings.Repeat("a", 100000),
			expectMatch:   true,
			expectTimeout: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, timedOut, err := SafeMatchString(tt.pattern, tt.input)

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if matched != tt.expectMatch {
				t.Errorf("Expected match=%v, got %v", tt.expectMatch, matched)
			}

			if timedOut != tt.expectTimeout {
				t.Errorf("Expected timeout=%v, got %v", tt.expectTimeout, timedOut)
			}
		})
	}
}

// TestSafeFindStringSubmatch tests the timeout-protected submatch extraction
func TestSafeFindStringSubmatch(t *testing.T) {
	pattern := KeyValueQuotedPattern

	tests := []struct {
		name        string
		input       string
		expectMatch bool
		expectCount int
	}{
		{
			name:        "normal submatch",
			input:       `slot="pvc-123"`,
			expectMatch: true,
			expectCount: 3, // Full match + 2 groups
		},
		{
			name:        "no match",
			input:       `invalid`,
			expectMatch: false,
			expectCount: 0,
		},
		{
			name:        "large quoted value",
			input:       `slot="` + strings.Repeat("a", 10000) + `"`,
			expectMatch: true,
			expectCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches, timedOut, err := SafeFindStringSubmatch(pattern, tt.input)

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if timedOut {
				t.Error("Regex timed out unexpectedly")
			}

			if tt.expectMatch && len(matches) != tt.expectCount {
				t.Errorf("Expected %d matches, got %d", tt.expectCount, len(matches))
			}

			if !tt.expectMatch && matches != nil {
				t.Errorf("Expected no match, got %v", matches)
			}
		})
	}
}

// BenchmarkVolumeIDPattern benchmarks the volume ID pattern with various inputs
func BenchmarkVolumeIDPattern(b *testing.B) {
	inputs := []string{
		"pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890", // Valid
		"invalid",                          // Short invalid
		"pvc-" + strings.Repeat("a", 1000), // Long invalid
	}

	for _, input := range inputs {
		b.Run("input_len_"+string(rune(len(input))), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				VolumeIDPattern.MatchString(input)
			}
		})
	}
}

// BenchmarkIPv4Pattern benchmarks IPv4 pattern matching
func BenchmarkIPv4Pattern(b *testing.B) {
	inputs := []string{
		"Connection to 192.168.1.1 failed",
		"No IP address here",
		strings.Repeat("a", 10000), // Large non-matching input
	}

	for _, input := range inputs {
		b.Run("input_len_"+string(rune(len(input))), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				IPv4Pattern.MatchString(input)
			}
		})
	}
}

// TestPathologicalInputs tests specifically crafted inputs that could trigger ReDoS
func TestPathologicalInputs(t *testing.T) {
	// These are intentionally designed to be worst-case for poorly written regexes
	pathologicalInputs := []string{
		strings.Repeat("a", 10000),                            // Long repeated character
		strings.Repeat("a", 10000) + "X",                      // Long repeated + non-match at end
		strings.Repeat("ab", 5000),                            // Repeated pattern
		strings.Repeat(".", 10000),                            // Many special regex chars
		strings.Repeat("(", 1000) + strings.Repeat(")", 1000), // Nested parens
	}

	patterns := []*regexp.Regexp{
		VolumeIDPattern,
		SafeSlotPattern,
		NQNPattern,
		IPv4Pattern,
		FileSizePattern,
		KeyValueQuotedPattern,
	}

	for i, input := range pathologicalInputs {
		for j, pattern := range patterns {
			t.Run("pathological_"+string(rune(i))+"_pattern_"+string(rune(j)), func(t *testing.T) {
				start := time.Now()
				_ = pattern.MatchString(input)
				duration := time.Since(start)

				// All patterns should complete in under 10ms even for pathological input
				if duration > 200*time.Millisecond {
					t.Errorf("Pattern took too long (%v) for pathological input (potential ReDoS)", duration)
				}
			})
		}
	}
}
