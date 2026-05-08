package nvme

import (
	"testing"
)

func TestDefaultConnectionConfig(t *testing.T) {
	config := DefaultConnectionConfig()

	// Verify CtrlLossTmo == -1 (unlimited reconnection)
	if config.CtrlLossTmo != -1 {
		t.Errorf("Expected CtrlLossTmo=-1, got %d", config.CtrlLossTmo)
	}

	// Verify ReconnectDelay == 5 (5 second retry interval)
	if config.ReconnectDelay != 5 {
		t.Errorf("Expected ReconnectDelay=5, got %d", config.ReconnectDelay)
	}

	// Verify KeepAliveTmo == 0 (kernel default)
	if config.KeepAliveTmo != 0 {
		t.Errorf("Expected KeepAliveTmo=0, got %d", config.KeepAliveTmo)
	}
}

func TestBuildConnectArgs(t *testing.T) {
	tests := []struct {
		name           string
		target         Target
		config         ConnectionConfig
		expectedArgs   []string
		unexpectedArgs []string // Args that should NOT be present
	}{
		{
			name: "minimal target with default config",
			target: Target{
				Transport:     "tcp",
				NQN:           "nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123",
				TargetAddress: "10.0.0.1",
				TargetPort:    4420,
			},
			config: DefaultConnectionConfig(),
			expectedArgs: []string{
				"connect",
				"-t", "tcp",
				"-a", "10.0.0.1",
				"-s", "4420",
				"-n", "nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123",
				"-l", "-1", // ctrl_loss_tmo=-1
				"-c", "5", // reconnect_delay=5
			},
			unexpectedArgs: []string{"-k"}, // KeepAliveTmo=0 means don't set
		},
		{
			name: "with host NQN",
			target: Target{
				Transport:     "tcp",
				NQN:           "nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123",
				TargetAddress: "10.0.0.1",
				TargetPort:    4420,
				HostNQN:       "nqn.2014-08.org.nvmexpress:uuid:host-123",
			},
			config: DefaultConnectionConfig(),
			expectedArgs: []string{
				"connect",
				"-q", "nqn.2014-08.org.nvmexpress:uuid:host-123",
			},
			unexpectedArgs: nil,
		},
		{
			name: "CtrlLossTmo=0 (kernel default, should NOT add -l flag)",
			target: Target{
				Transport:     "tcp",
				NQN:           "nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123",
				TargetAddress: "10.0.0.1",
				TargetPort:    4420,
			},
			config: ConnectionConfig{
				CtrlLossTmo:    0,
				ReconnectDelay: 5,
				KeepAliveTmo:   0,
			},
			expectedArgs:   []string{"connect", "-c", "5"},
			unexpectedArgs: []string{"-l"}, // CtrlLossTmo=0 means don't set
		},
		{
			name: "CtrlLossTmo=600 (explicit timeout)",
			target: Target{
				Transport:     "tcp",
				NQN:           "nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123",
				TargetAddress: "10.0.0.1",
				TargetPort:    4420,
			},
			config: ConnectionConfig{
				CtrlLossTmo:    600,
				ReconnectDelay: 5,
				KeepAliveTmo:   0,
			},
			expectedArgs:   []string{"-l", "600"},
			unexpectedArgs: nil,
		},
		{
			name: "custom ReconnectDelay=10",
			target: Target{
				Transport:     "tcp",
				NQN:           "nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123",
				TargetAddress: "10.0.0.1",
				TargetPort:    4420,
			},
			config: ConnectionConfig{
				CtrlLossTmo:    -1,
				ReconnectDelay: 10,
				KeepAliveTmo:   0,
			},
			expectedArgs:   []string{"-c", "10"},
			unexpectedArgs: nil,
		},
		{
			name: "KeepAliveTmo=30",
			target: Target{
				Transport:     "tcp",
				NQN:           "nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123",
				TargetAddress: "10.0.0.1",
				TargetPort:    4420,
			},
			config: ConnectionConfig{
				CtrlLossTmo:    -1,
				ReconnectDelay: 5,
				KeepAliveTmo:   30,
			},
			expectedArgs:   []string{"-k", "30"},
			unexpectedArgs: nil,
		},
		{
			name: "ReconnectDelay=0 (should NOT add -c flag)",
			target: Target{
				Transport:     "tcp",
				NQN:           "nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123",
				TargetAddress: "10.0.0.1",
				TargetPort:    4420,
			},
			config: ConnectionConfig{
				CtrlLossTmo:    -1,
				ReconnectDelay: 0,
				KeepAliveTmo:   0,
			},
			expectedArgs:   []string{"connect", "-l", "-1"},
			unexpectedArgs: []string{"-c"}, // ReconnectDelay=0 means don't set
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := BuildConnectArgs(tt.target, tt.config)

			// Check expected args are present
			for _, expected := range tt.expectedArgs {
				found := false
				for _, arg := range args {
					if arg == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected arg %q not found in %v", expected, args)
				}
			}

			// Check unexpected args are NOT present
			for _, unexpected := range tt.unexpectedArgs {
				for _, arg := range args {
					if arg == unexpected {
						t.Errorf("Unexpected arg %q found in %v", unexpected, args)
					}
				}
			}
		})
	}
}

func TestBuildConnectArgs_FirstArg(t *testing.T) {
	// Verify first arg is always "connect"
	target := Target{
		Transport:     "tcp",
		NQN:           "nqn.2026-05.works.whiskey.nvmet-csi:pvc-test",
		TargetAddress: "10.0.0.1",
		TargetPort:    4420,
	}
	config := DefaultConnectionConfig()

	args := BuildConnectArgs(target, config)

	if len(args) == 0 {
		t.Fatal("BuildConnectArgs returned empty slice")
	}
	if args[0] != "connect" {
		t.Errorf("Expected first arg to be 'connect', got %q", args[0])
	}
}
