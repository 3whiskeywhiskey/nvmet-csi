package rds

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

type recordingCommandObserver struct {
	records []recordedCommand
}

type recordedCommand struct {
	family string
	action string
	class  string
	status string
}

func (o *recordingCommandObserver) RecordRDSCommand(family, action, class, status string, duration time.Duration) {
	o.records = append(o.records, recordedCommand{
		family: family,
		action: action,
		class:  class,
		status: status,
	})
}

// ============================================================================
// Part A: Pure function tests (no SSH connection needed)
// ============================================================================

func TestNewSSHClient(t *testing.T) {
	tests := []struct {
		name      string
		config    ClientConfig
		expectErr bool
		errMsg    string
	}{
		{
			name: "valid config with all fields",
			config: ClientConfig{
				Address:            "198.51.100.20",
				Port:               22,
				User:               "admin",
				PrivateKey:         []byte("test-key"),
				HostKey:            []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGfHgLqW+tDlnDvIhZBXCCLvJqzVFQxVX0H5K6fqnZxE"),
				Timeout:            5 * time.Second,
				InsecureSkipVerify: false,
			},
			expectErr: false,
		},
		{
			name: "missing address returns error",
			config: ClientConfig{
				User:       "admin",
				PrivateKey: []byte("test-key"),
			},
			expectErr: true,
			errMsg:    "address is required",
		},
		{
			name: "missing user returns error",
			config: ClientConfig{
				Address:    "198.51.100.20",
				PrivateKey: []byte("test-key"),
			},
			expectErr: true,
			errMsg:    "user is required",
		},
		{
			name: "default port when not specified",
			config: ClientConfig{
				Address:            "198.51.100.20",
				User:               "admin",
				InsecureSkipVerify: true,
			},
			expectErr: false,
		},
		{
			name: "default timeout when not specified",
			config: ClientConfig{
				Address:            "198.51.100.20",
				User:               "admin",
				InsecureSkipVerify: true,
			},
			expectErr: false,
		},
		{
			name: "invalid HostKeyCallback type returns error",
			config: ClientConfig{
				Address:         "198.51.100.20",
				User:            "admin",
				HostKeyCallback: "not-a-callback", // Wrong type
			},
			expectErr: true,
			errMsg:    "HostKeyCallback must be of type ssh.HostKeyCallback",
		},
		{
			name: "custom HostKeyCallback is used",
			config: ClientConfig{
				Address: "198.51.100.20",
				User:    "admin",
				HostKeyCallback: ssh.HostKeyCallback(func(hostname string, remote net.Addr, key ssh.PublicKey) error {
					return nil
				}),
			},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := newSSHClient(tt.config)

			if tt.expectErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
				return
			}

			require.NoError(t, err)
			require.NotNil(t, client)

			// Verify defaults
			if tt.config.Port == 0 {
				assert.Equal(t, 22, client.port, "default port should be 22")
			} else {
				assert.Equal(t, tt.config.Port, client.port)
			}

			if tt.config.Timeout == 0 {
				assert.Equal(t, 10*time.Second, client.timeout, "default timeout should be 10s")
			} else {
				assert.Equal(t, tt.config.Timeout, client.timeout)
			}

			// Verify custom HostKeyCallback is set
			if tt.config.HostKeyCallback != nil {
				assert.NotNil(t, client.hostKeyCallback, "custom HostKeyCallback should be set")
			}
		})
	}
}

func TestRunCommandRecordsCommandFailure(t *testing.T) {
	observer := &recordingCommandObserver{}
	client := &sshClient{
		address:         "198.51.100.20",
		commandObserver: observer,
	}

	_, err := client.runCommand("/disk add type=file slot=pvc-test")
	require.Error(t, err)
	require.Len(t, observer.records, 1)
	require.Equal(t, "disk", observer.records[0].family)
	require.Equal(t, "add", observer.records[0].action)
	require.Equal(t, string(CommandClassMutate), observer.records[0].class)
	require.Equal(t, "failure", observer.records[0].status)
}

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{
			name:      "nil error returns false",
			err:       nil,
			retryable: false,
		},
		{
			name:      "network timeout returns true",
			err:       &net.DNSError{IsTimeout: true},
			retryable: true,
		},
		{
			name:      "io.EOF returns true",
			err:       io.EOF,
			retryable: true,
		},
		{
			name:      "not enough space returns false",
			err:       errors.New("command failed: not enough space on disk"),
			retryable: false,
		},
		{
			name:      "invalid parameter returns false",
			err:       errors.New("command failed: invalid parameter"),
			retryable: false,
		},
		{
			name:      "no such item returns false",
			err:       errors.New("failure: no such item"),
			retryable: false,
		},
		{
			name:      "authentication failed returns false",
			err:       errors.New("authentication failed"),
			retryable: false,
		},
		{
			name:      "generic error returns true (retryable by default)",
			err:       errors.New("connection reset by peer"),
			retryable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isRetryableError(tt.err)
			assert.Equal(t, tt.retryable, result)
		})
	}
}

func TestParseHostKey(t *testing.T) {
	tests := []struct {
		name      string
		keyData   []byte
		expectErr bool
	}{
		{
			name:      "valid OpenSSH ed25519 public key",
			keyData:   []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGfHgLqW+tDlnDvIhZBXCCLvJqzVFQxVX0H5K6fqnZxE root@router"),
			expectErr: false,
		},
		{
			name:      "invalid key data returns error",
			keyData:   []byte("not-a-valid-key"),
			expectErr: true,
		},
		{
			name:      "empty key data returns error",
			keyData:   []byte(""),
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := parseHostKey(tt.keyData)

			if tt.expectErr {
				require.Error(t, err)
				assert.Nil(t, key)
				return
			}

			require.NoError(t, err)
			assert.NotNil(t, key)
		})
	}
}

func TestContainsString(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		substr   string
		expected bool
	}{
		{
			name:     "basic substring match",
			s:        "hello world",
			substr:   "world",
			expected: true,
		},
		{
			name:     "no match",
			s:        "hello world",
			substr:   "goodbye",
			expected: false,
		},
		{
			name:     "case sensitive",
			s:        "Hello World",
			substr:   "hello",
			expected: false,
		},
		{
			name:     "empty substring",
			s:        "hello",
			substr:   "",
			expected: true,
		},
		{
			name:     "empty string",
			s:        "",
			substr:   "test",
			expected: false,
		},
		{
			name:     "full match",
			s:        "test",
			substr:   "test",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsString(tt.s, tt.substr)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIndexString(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		substr   string
		expected int
	}{
		{
			name:     "substring at start",
			s:        "hello world",
			substr:   "hello",
			expected: 0,
		},
		{
			name:     "substring in middle",
			s:        "hello world",
			substr:   "lo wo",
			expected: 3,
		},
		{
			name:     "substring at end",
			s:        "hello world",
			substr:   "world",
			expected: 6,
		},
		{
			name:     "no match",
			s:        "hello world",
			substr:   "goodbye",
			expected: -1,
		},
		{
			name:     "empty substring",
			s:        "hello",
			substr:   "",
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := indexString(tt.s, tt.substr)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// ============================================================================
// Part B: SSH mock server tests for Connect/runCommand/runCommandWithRetry
// ============================================================================

// mockSSHServer is a simple SSH server for testing
type mockSSHServer struct {
	listener net.Listener
	address  string
	port     int
	config   *ssh.ServerConfig
	handler  func(channel ssh.Channel, requests <-chan *ssh.Request)
	stopChan chan struct{}
}

// startMockSSHServer creates and starts an in-process SSH server for testing
func startMockSSHServer(t *testing.T, handler func(channel ssh.Channel, requests <-chan *ssh.Request)) *mockSSHServer {
	t.Helper()

	// Generate host key for the server
	hostKey, err := generateTestHostKey()
	require.NoError(t, err, "failed to generate test host key")

	// Configure SSH server
	config := &ssh.ServerConfig{
		NoClientAuth: true, // Accept any connection for testing
	}
	config.AddHostKey(hostKey)

	// Listen on random port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "failed to start listener")

	addr := listener.Addr().(*net.TCPAddr)
	srv := &mockSSHServer{
		listener: listener,
		address:  "127.0.0.1",
		port:     addr.Port,
		config:   config,
		handler:  handler,
		stopChan: make(chan struct{}),
	}

	// Start accepting connections
	go srv.acceptConnections(t)

	t.Cleanup(func() {
		srv.Close()
	})

	return srv
}

func (s *mockSSHServer) acceptConnections(t *testing.T) {
	for {
		select {
		case <-s.stopChan:
			return
		default:
		}

		// Set accept deadline to allow checking stopChan
		_ = s.listener.(*net.TCPListener).SetDeadline(time.Now().Add(100 * time.Millisecond))

		conn, err := s.listener.Accept()
		if err != nil {
			// Check if it's a timeout (expected when stopChan is closed)
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			// Server closed
			return
		}

		// Handle connection in goroutine
		go s.handleConnection(t, conn)
	}
}

func (s *mockSSHServer) handleConnection(t *testing.T, netConn net.Conn) {
	defer func() { _ = netConn.Close() }()

	// Perform SSH handshake
	sshConn, chans, reqs, err := ssh.NewServerConn(netConn, s.config)
	if err != nil {
		t.Logf("SSH handshake failed: %v", err)
		return
	}
	defer func() { _ = sshConn.Close() }()

	// Discard global requests
	go ssh.DiscardRequests(reqs)

	// Handle channels
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}

		channel, requests, err := newChannel.Accept()
		if err != nil {
			t.Logf("Failed to accept channel: %v", err)
			continue
		}

		// Handle channel in goroutine
		go s.handler(channel, requests)
	}
}

func (s *mockSSHServer) Close() error {
	close(s.stopChan)
	return s.listener.Close()
}

// generateTestHostKey generates an Ed25519 host key for testing
func generateTestHostKey() (ssh.Signer, error) {
	// Generate a new Ed25519 key pair for each test run
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}

	// Convert to SSH signer
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		return nil, err
	}

	return signer, nil
}

// createConnectedTestClient creates an SSH client and connects to the mock server
func createConnectedTestClient(t *testing.T, srv *mockSSHServer) *sshClient {
	t.Helper()

	client, err := newSSHClient(ClientConfig{
		Address:            srv.address,
		Port:               srv.port,
		User:               "admin",
		InsecureSkipVerify: true,
	})
	require.NoError(t, err)

	err = client.Connect()
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = client.Close()
	})

	return client
}

func TestSSHClientConnect(t *testing.T) {
	// Start mock SSH server that accepts any connection
	srv := startMockSSHServer(t, func(channel ssh.Channel, requests <-chan *ssh.Request) {
		defer func() { _ = channel.Close() }()

		// Handle exec requests
		for req := range requests {
			if req.Type == "exec" {
				_ = req.Reply(true, nil)
				_, _ = channel.Write([]byte("connected"))
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{0}))
				return
			}
		}
	})

	client, err := newSSHClient(ClientConfig{
		Address:            srv.address,
		Port:               srv.port,
		User:               "admin",
		InsecureSkipVerify: true,
	})
	require.NoError(t, err)

	// Test Connect
	err = client.Connect()
	require.NoError(t, err)
	assert.True(t, client.IsConnected(), "client should be connected")

	// Test Close
	err = client.Close()
	require.NoError(t, err)
	assert.False(t, client.IsConnected(), "client should be disconnected after Close")
}

func TestSSHClientRunCommand(t *testing.T) {
	tests := []struct {
		name           string
		command        string
		expectedOutput string
		expectError    bool
		exitStatus     uint32
	}{
		{
			name:           "successful command returns output",
			command:        "/disk print",
			expectedOutput: "type=file slot=\"pvc-test\"",
			expectError:    false,
			exitStatus:     0,
		},
		{
			name:        "failed command returns error",
			command:     "/disk remove invalid",
			expectError: true,
			exitStatus:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := startMockSSHServer(t, func(channel ssh.Channel, requests <-chan *ssh.Request) {
				defer func() { _ = channel.Close() }()

				for req := range requests {
					if req.Type == "exec" {
						_ = req.Reply(true, nil)

						// Parse command from payload
						cmdLen := int(req.Payload[3])
						cmd := string(req.Payload[4 : 4+cmdLen])

						if cmd == tt.command {
							if tt.exitStatus == 0 {
								_, _ = channel.Write([]byte(tt.expectedOutput))
							} else {
								_, _ = channel.Stderr().Write([]byte("command failed"))
							}
						}

						// Send exit status
						_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{tt.exitStatus}))
						return
					}
				}
			})

			client := createConnectedTestClient(t, srv)

			output, err := client.runCommand(tt.command)

			if tt.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Contains(t, output, tt.expectedOutput)
		})
	}
}

func TestSSHClientRunCommandWithRetry(t *testing.T) {
	t.Run("retry on transient error then succeed", func(t *testing.T) {
		attemptCount := 0

		srv := startMockSSHServer(t, func(channel ssh.Channel, requests <-chan *ssh.Request) {
			defer func() { _ = channel.Close() }()

			for req := range requests {
				if req.Type == "exec" {
					_ = req.Reply(true, nil)

					attemptCount++
					exitStatus := uint32(1)
					if attemptCount >= 2 {
						// Succeed on second attempt
						_, _ = channel.Write([]byte("success"))
						exitStatus = 0
					} else {
						// Fail first attempt (transient error)
						_, _ = channel.Stderr().Write([]byte("transient error"))
					}

					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{exitStatus}))
					return
				}
			}
		})

		client := createConnectedTestClient(t, srv)

		output, err := client.runCommandWithRetry("/disk print", 3)
		require.NoError(t, err)
		assert.Contains(t, output, "success")
		assert.Equal(t, 2, attemptCount, "should succeed on second attempt")
	})

	t.Run("non-retryable error fails immediately", func(t *testing.T) {
		srv := startMockSSHServer(t, func(channel ssh.Channel, requests <-chan *ssh.Request) {
			defer func() { _ = channel.Close() }()

			for req := range requests {
				if req.Type == "exec" {
					_ = req.Reply(true, nil)
					_, _ = channel.Stderr().Write([]byte("not enough space"))
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{1}))
					return
				}
			}
		})

		client := createConnectedTestClient(t, srv)

		_, err := client.runCommandWithRetry("/disk add", 3)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not enough space")
	})

	t.Run("max retries exceeded", func(t *testing.T) {
		attemptCount := 0

		srv := startMockSSHServer(t, func(channel ssh.Channel, requests <-chan *ssh.Request) {
			defer func() { _ = channel.Close() }()

			for req := range requests {
				if req.Type == "exec" {
					_ = req.Reply(true, nil)
					attemptCount++

					// Always fail with retryable error
					_, _ = channel.Stderr().Write([]byte("connection timeout"))
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{1}))
					return
				}
			}
		})

		client := createConnectedTestClient(t, srv)

		_, err := client.runCommandWithRetry("/disk print", 3)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "max retries")
		assert.Equal(t, 3, attemptCount, "should attempt exactly 3 times")
	})
}

func TestSSHClientNotConnected(t *testing.T) {
	client := &sshClient{
		address: "198.51.100.20",
		port:    22,
		user:    "admin",
	}

	// runCommand should fail when not connected
	_, err := client.runCommand("/disk print")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not connected")

	// IsConnected should return false
	assert.False(t, client.IsConnected())
}

func TestSSHClientConnectFailure(t *testing.T) {
	// Try to connect to a non-existent server
	client, err := newSSHClient(ClientConfig{
		Address:            "127.0.0.1",
		Port:               54321, // Port that's unlikely to be in use
		User:               "admin",
		Timeout:            100 * time.Millisecond,
		InsecureSkipVerify: true,
	})
	require.NoError(t, err)

	err = client.Connect()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to connect")
}

// ============================================================================
// RouterOS compatibility: CommandOutputError and routerOSOutputError regression tests
// ============================================================================

func TestRouterOSOutputError(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		wantErr bool
	}{
		{"bad parameter lower", "bad parameter mount", true},
		{"bad parameter upper", "BAD PARAMETER foo", true},
		{"syntax error", "syntax error: unexpected", true},
		{"failure colon", "failure: disk full", true},
		{"expected end of command", "expected end of command", true},
		{"input does not match", "input does not match any", true},
		{"no such item", "no such item", true},
		{"invalid value", "invalid value for size", true},
		{"not enough permissions", "not enough permissions", true},
		{"ambiguous command", "ambiguous command /disk", true},
		{"mixed case prefix", "Bad Parameter mount=no", true},
		{"prefix on second line", "some header\nbad parameter x", true},
		// legitimate output
		{"legit disk print detail with flags", " 0 slot=\"pvc-123\" type=file file-path=/x.img\nflags: D - dynamic; X - disabled; ... \n     file-size=1.0GiB", false},
		{"empty output", "", false},
		{"failure word mid-line not at line start", "some result containing the word failure: but not start of its line", false},
		{"normal output", "name=foo slot=bar", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := routerOSOutputError("/disk add", tt.output)
			if tt.wantErr {
				require.Error(t, err)
				var coe *CommandOutputError
				require.True(t, errors.As(err, &coe), "expected *CommandOutputError")
				assert.Equal(t, "/disk add", coe.Command)
				assert.Equal(t, tt.output, coe.Output)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestIsRetryableError_CommandOutputError(t *testing.T) {
	coe := &CommandOutputError{
		Command: "/disk add",
		Output:  "bad parameter mount",
	}
	assert.False(t, isRetryableError(coe), "CommandOutputError must not be retryable")

	wrapped := fmt.Errorf("outer: %w", coe)
	assert.False(t, isRetryableError(wrapped), "wrapped CommandOutputError must not be retryable")
}
