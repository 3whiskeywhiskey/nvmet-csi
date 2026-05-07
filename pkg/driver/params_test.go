package driver

import (
	"strings"
	"testing"
	"time"

	"git.srvlab.io/whiskey/rds-csi-driver/pkg/backend"
)

func TestParseBackendSelection(t *testing.T) {
	tests := []struct {
		name     string
		params   map[string]string
		wantType string
		wantExec string
	}{
		{
			name:     "defaults to routeros rds over ssh",
			params:   map[string]string{},
			wantType: backend.TypeRouterOSRDS,
			wantExec: backend.ExecutorSSH,
		},
		{
			name: "explicit routeros rds over ssh",
			params: map[string]string{
				"backend":  backend.TypeRouterOSRDS,
				"executor": backend.ExecutorSSH,
			},
			wantType: backend.TypeRouterOSRDS,
			wantExec: backend.ExecutorSSH,
		},
		{
			name: "linux zol nvmet local",
			params: map[string]string{
				"backend":       backend.TypeLinuxZOLNVMET,
				"executor":      backend.ExecutorLocal,
				"zfsPool":       "tank/nvmet",
				"targetAddress": "10.42.0.10",
			},
			wantType: backend.TypeLinuxZOLNVMET,
			wantExec: backend.ExecutorLocal,
		},
		{
			name: "linux zol nvmet ssh",
			params: map[string]string{
				"backend":       backend.TypeLinuxZOLNVMET,
				"executor":      backend.ExecutorSSH,
				"zfsPool":       "tank/nvmet",
				"targetAddress": "10.42.0.10",
				"sshTarget":     "storage@r740xd",
			},
			wantType: backend.TypeLinuxZOLNVMET,
			wantExec: backend.ExecutorSSH,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseBackendSelection(tt.params)
			if err != nil {
				t.Fatalf("ParseBackendSelection() error = %v", err)
			}
			if got.Type != tt.wantType || got.Executor != tt.wantExec {
				t.Fatalf("ParseBackendSelection() = %+v, want type=%s executor=%s", got, tt.wantType, tt.wantExec)
			}
		})
	}
}

func TestParseBackendSelectionInvalid(t *testing.T) {
	tests := []struct {
		name          string
		params        map[string]string
		errorContains string
	}{
		{
			name:          "unknown backend",
			params:        map[string]string{"backend": "unknown"},
			errorContains: "unsupported backend",
		},
		{
			name: "routeros local rejected",
			params: map[string]string{
				"backend":  backend.TypeRouterOSRDS,
				"executor": backend.ExecutorLocal,
			},
			errorContains: "requires executor=ssh",
		},
		{
			name: "linux invalid executor rejected",
			params: map[string]string{
				"backend":  backend.TypeLinuxZOLNVMET,
				"executor": "routeros",
				"zfsPool":  "tank/nvmet",
			},
			errorContains: "requires executor",
		},
		{
			name: "linux missing zfs pool rejected",
			params: map[string]string{
				"backend":       backend.TypeLinuxZOLNVMET,
				"executor":      backend.ExecutorLocal,
				"targetAddress": "10.42.0.10",
			},
			errorContains: "requires zfsPool",
		},
		{
			name: "linux missing target address rejected",
			params: map[string]string{
				"backend":  backend.TypeLinuxZOLNVMET,
				"executor": backend.ExecutorLocal,
				"zfsPool":  "tank/nvmet",
			},
			errorContains: "requires targetAddress or nvmeAddress",
		},
		{
			name: "linux ssh missing target rejected",
			params: map[string]string{
				"backend":       backend.TypeLinuxZOLNVMET,
				"executor":      backend.ExecutorSSH,
				"zfsPool":       "tank/nvmet",
				"targetAddress": "10.42.0.10",
			},
			errorContains: "requires sshTarget",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseBackendSelection(tt.params)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.errorContains) {
				t.Fatalf("expected error containing %q, got %v", tt.errorContains, err)
			}
		})
	}
}

func TestParseNVMEConnectionParams_Defaults(t *testing.T) {
	// Empty map should return default values
	params := map[string]string{}

	config, err := ParseNVMEConnectionParams(params)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify defaults
	if config.CtrlLossTmo != -1 {
		t.Errorf("Expected CtrlLossTmo=-1 (unlimited), got %d", config.CtrlLossTmo)
	}
	if config.ReconnectDelay != 5 {
		t.Errorf("Expected ReconnectDelay=5, got %d", config.ReconnectDelay)
	}
	if config.KeepAliveTmo != 0 {
		t.Errorf("Expected KeepAliveTmo=0 (kernel default), got %d", config.KeepAliveTmo)
	}
}

func TestParseNVMEConnectionParams_ValidInputs(t *testing.T) {
	tests := []struct {
		name                   string
		params                 map[string]string
		expectedCtrlLossTmo    int
		expectedReconnectDelay int
		expectedKeepAliveTmo   int
	}{
		{
			name:                   "ctrlLossTmo=600 (explicit timeout)",
			params:                 map[string]string{"ctrlLossTmo": "600"},
			expectedCtrlLossTmo:    600,
			expectedReconnectDelay: 5, // default
			expectedKeepAliveTmo:   0, // default
		},
		{
			name:                   "ctrlLossTmo=-1 (explicit unlimited)",
			params:                 map[string]string{"ctrlLossTmo": "-1"},
			expectedCtrlLossTmo:    -1,
			expectedReconnectDelay: 5, // default
			expectedKeepAliveTmo:   0, // default
		},
		{
			name:                   "ctrlLossTmo=0 (kernel default)",
			params:                 map[string]string{"ctrlLossTmo": "0"},
			expectedCtrlLossTmo:    0,
			expectedReconnectDelay: 5, // default
			expectedKeepAliveTmo:   0, // default
		},
		{
			name:                   "reconnectDelay=10",
			params:                 map[string]string{"reconnectDelay": "10"},
			expectedCtrlLossTmo:    -1, // default
			expectedReconnectDelay: 10,
			expectedKeepAliveTmo:   0, // default
		},
		{
			name:                   "keepAliveTmo=30",
			params:                 map[string]string{"keepAliveTmo": "30"},
			expectedCtrlLossTmo:    -1, // default
			expectedReconnectDelay: 5,  // default
			expectedKeepAliveTmo:   30,
		},
		{
			name: "all params set",
			params: map[string]string{
				"ctrlLossTmo":    "300",
				"reconnectDelay": "15",
				"keepAliveTmo":   "60",
			},
			expectedCtrlLossTmo:    300,
			expectedReconnectDelay: 15,
			expectedKeepAliveTmo:   60,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := ParseNVMEConnectionParams(tt.params)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if config.CtrlLossTmo != tt.expectedCtrlLossTmo {
				t.Errorf("Expected CtrlLossTmo=%d, got %d", tt.expectedCtrlLossTmo, config.CtrlLossTmo)
			}
			if config.ReconnectDelay != tt.expectedReconnectDelay {
				t.Errorf("Expected ReconnectDelay=%d, got %d", tt.expectedReconnectDelay, config.ReconnectDelay)
			}
			if config.KeepAliveTmo != tt.expectedKeepAliveTmo {
				t.Errorf("Expected KeepAliveTmo=%d, got %d", tt.expectedKeepAliveTmo, config.KeepAliveTmo)
			}
		})
	}
}

func TestParseNVMEConnectionParams_InvalidInputs(t *testing.T) {
	tests := []struct {
		name          string
		params        map[string]string
		errorContains string
	}{
		{
			name:          "ctrlLossTmo=abc (not a number)",
			params:        map[string]string{"ctrlLossTmo": "abc"},
			errorContains: "invalid ctrlLossTmo",
		},
		{
			name:          "ctrlLossTmo=-2 (below -1)",
			params:        map[string]string{"ctrlLossTmo": "-2"},
			errorContains: "ctrlLossTmo must be -1",
		},
		{
			name:          "reconnectDelay=0 (must be > 0)",
			params:        map[string]string{"reconnectDelay": "0"},
			errorContains: "reconnectDelay must be positive",
		},
		{
			name:          "reconnectDelay=-1 (must be > 0)",
			params:        map[string]string{"reconnectDelay": "-1"},
			errorContains: "reconnectDelay must be positive",
		},
		{
			name:          "reconnectDelay=abc (not a number)",
			params:        map[string]string{"reconnectDelay": "abc"},
			errorContains: "invalid reconnectDelay",
		},
		{
			name:          "keepAliveTmo=-1 (must be >= 0)",
			params:        map[string]string{"keepAliveTmo": "-1"},
			errorContains: "keepAliveTmo must be non-negative",
		},
		{
			name:          "keepAliveTmo=abc (not a number)",
			params:        map[string]string{"keepAliveTmo": "abc"},
			errorContains: "invalid keepAliveTmo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseNVMEConnectionParams(tt.params)
			if err == nil {
				t.Fatal("Expected error but got nil")
			}
			if !strings.Contains(err.Error(), tt.errorContains) {
				t.Errorf("Expected error containing %q, got: %v", tt.errorContains, err)
			}
		})
	}
}

func TestToVolumeContext(t *testing.T) {
	params := NVMEConnectionParams{
		CtrlLossTmo:    -1,
		ReconnectDelay: 5,
		KeepAliveTmo:   30,
	}

	ctx := ToVolumeContext(params)

	// Verify all values are present as strings
	if ctx["ctrlLossTmo"] != "-1" {
		t.Errorf("Expected ctrlLossTmo=%q, got %q", "-1", ctx["ctrlLossTmo"])
	}
	if ctx["reconnectDelay"] != "5" {
		t.Errorf("Expected reconnectDelay=%q, got %q", "5", ctx["reconnectDelay"])
	}
	if ctx["keepAliveTmo"] != "30" {
		t.Errorf("Expected keepAliveTmo=%q, got %q", "30", ctx["keepAliveTmo"])
	}

	// Verify map has exactly 3 entries
	if len(ctx) != 3 {
		t.Errorf("Expected 3 entries in context, got %d", len(ctx))
	}
}

func TestToVolumeContext_RoundTrip(t *testing.T) {
	// Test that ToVolumeContext output can be parsed back
	original := NVMEConnectionParams{
		CtrlLossTmo:    600,
		ReconnectDelay: 10,
		KeepAliveTmo:   45,
	}

	ctx := ToVolumeContext(original)
	parsed, err := ParseNVMEConnectionParams(ctx)
	if err != nil {
		t.Fatalf("Failed to parse round-trip context: %v", err)
	}

	if parsed.CtrlLossTmo != original.CtrlLossTmo {
		t.Errorf("CtrlLossTmo: expected %d, got %d", original.CtrlLossTmo, parsed.CtrlLossTmo)
	}
	if parsed.ReconnectDelay != original.ReconnectDelay {
		t.Errorf("ReconnectDelay: expected %d, got %d", original.ReconnectDelay, parsed.ReconnectDelay)
	}
	if parsed.KeepAliveTmo != original.KeepAliveTmo {
		t.Errorf("KeepAliveTmo: expected %d, got %d", original.KeepAliveTmo, parsed.KeepAliveTmo)
	}
}

func TestDefaultNVMEConnectionParams(t *testing.T) {
	params := DefaultNVMEConnectionParams()

	// Verify matches documented defaults
	if params.CtrlLossTmo != -1 {
		t.Errorf("Expected CtrlLossTmo=-1, got %d", params.CtrlLossTmo)
	}
	if params.ReconnectDelay != 5 {
		t.Errorf("Expected ReconnectDelay=5, got %d", params.ReconnectDelay)
	}
	if params.KeepAliveTmo != 0 {
		t.Errorf("Expected KeepAliveTmo=0, got %d", params.KeepAliveTmo)
	}
}

func TestParseMigrationTimeout(t *testing.T) {
	tests := []struct {
		name          string
		params        map[string]string
		expectedMin   time.Duration
		expectedMax   time.Duration
		expectDefault bool
	}{
		{
			name:          "not specified - returns default",
			params:        map[string]string{},
			expectedMin:   DefaultMigrationTimeout,
			expectedMax:   DefaultMigrationTimeout,
			expectDefault: true,
		},
		{
			name:          "empty string - returns default",
			params:        map[string]string{"migrationTimeoutSeconds": ""},
			expectedMin:   DefaultMigrationTimeout,
			expectedMax:   DefaultMigrationTimeout,
			expectDefault: true,
		},
		{
			name:        "valid value - 300 seconds",
			params:      map[string]string{"migrationTimeoutSeconds": "300"},
			expectedMin: 300 * time.Second,
			expectedMax: 300 * time.Second,
		},
		{
			name:        "valid value - 600 seconds",
			params:      map[string]string{"migrationTimeoutSeconds": "600"},
			expectedMin: 600 * time.Second,
			expectedMax: 600 * time.Second,
		},
		{
			name:          "invalid - not a number",
			params:        map[string]string{"migrationTimeoutSeconds": "abc"},
			expectedMin:   DefaultMigrationTimeout,
			expectedMax:   DefaultMigrationTimeout,
			expectDefault: true,
		},
		{
			name:          "invalid - negative",
			params:        map[string]string{"migrationTimeoutSeconds": "-300"},
			expectedMin:   DefaultMigrationTimeout,
			expectedMax:   DefaultMigrationTimeout,
			expectDefault: true,
		},
		{
			name:          "invalid - zero",
			params:        map[string]string{"migrationTimeoutSeconds": "0"},
			expectedMin:   DefaultMigrationTimeout,
			expectedMax:   DefaultMigrationTimeout,
			expectDefault: true,
		},
		{
			name:        "clamped - too short (10s -> 30s min)",
			params:      map[string]string{"migrationTimeoutSeconds": "10"},
			expectedMin: MinMigrationTimeout,
			expectedMax: MinMigrationTimeout,
		},
		{
			name:        "clamped - too long (7200s -> 3600s max)",
			params:      map[string]string{"migrationTimeoutSeconds": "7200"},
			expectedMin: MaxMigrationTimeout,
			expectedMax: MaxMigrationTimeout,
		},
		{
			name:        "boundary - exactly min (30s)",
			params:      map[string]string{"migrationTimeoutSeconds": "30"},
			expectedMin: 30 * time.Second,
			expectedMax: 30 * time.Second,
		},
		{
			name:        "boundary - exactly max (3600s)",
			params:      map[string]string{"migrationTimeoutSeconds": "3600"},
			expectedMin: 3600 * time.Second,
			expectedMax: 3600 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseMigrationTimeout(tt.params)

			if result < tt.expectedMin || result > tt.expectedMax {
				t.Errorf("ParseMigrationTimeout() = %v, want between %v and %v",
					result, tt.expectedMin, tt.expectedMax)
			}

			if tt.expectDefault && result != DefaultMigrationTimeout {
				t.Errorf("ParseMigrationTimeout() = %v, want default %v",
					result, DefaultMigrationTimeout)
			}
		})
	}
}
