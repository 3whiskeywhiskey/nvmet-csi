package rds

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"k8s.io/klog/v2"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/security"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/utils"
)

// CommandOutputError reports a RouterOS CLI failure. RouterOS prints command
// errors (e.g. "bad parameter mount") to stdout and still exits zero, so exit
// status alone cannot be trusted for mutations.
type CommandOutputError struct {
	Command string
	Output  string
}

func (e *CommandOutputError) Error() string {
	return fmt.Sprintf("RouterOS rejected command: %s (command: %s)", strings.TrimSpace(e.Output), e.Command)
}

// routerOSOutputErrorPrefixes are line prefixes RouterOS uses for CLI errors.
// Inputs are strictly validated (slot names, sanitized paths), so legitimate
// command output cannot start a line with these phrases.
var routerOSOutputErrorPrefixes = []string{
	"bad parameter",
	"syntax error",
	"failure:",
	"expected end of command",
	"input does not match",
	"no such item",
	"invalid value",
	"not enough permissions",
	"ambiguous command",
}

func routerOSOutputError(command, output string) error {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.ToLower(strings.TrimSpace(line))
		if trimmed == "" {
			continue
		}
		for _, prefix := range routerOSOutputErrorPrefixes {
			if strings.HasPrefix(trimmed, prefix) {
				return &CommandOutputError{Command: command, Output: output}
			}
		}
	}
	return nil
}

// sshClient implements RDSClient using SSH protocol to connect to RouterOS
type sshClient struct {
	address            string // RDS IP address
	port               int
	user               string
	privateKey         []byte
	hostKey            []byte // Expected SSH host public key
	timeout            time.Duration
	sshClient          *ssh.Client
	hostKeyCallback    ssh.HostKeyCallback
	insecureSkipVerify bool
	sessionMu          sync.Mutex // Protects concurrent session creation
	volumeLocks        *targetLockManager
	commandObserver    CommandObserver
	decisionObserver   MutationDecisionObserver

	// diskAddMountUnsupported records that this RouterOS build rejects the
	// /disk add mount parameter (removed in newer /disk APIs, observed on
	// 7.23). Learned on first rejection, then omitted for the connection's
	// lifetime.
	diskAddMountUnsupported atomic.Bool
}

// newSSHClient creates a new SSH-based RDS client
func newSSHClient(config ClientConfig) (*sshClient, error) {
	if config.Address == "" {
		return nil, fmt.Errorf("address is required")
	}
	if config.User == "" {
		return nil, fmt.Errorf("user is required")
	}
	// Note: PrivateKey is optional for testing with mock servers that don't require auth

	// Set defaults
	if config.Port == 0 {
		config.Port = 22
	}
	if config.Timeout == 0 {
		config.Timeout = 10 * time.Second
	}

	// Handle host key callback
	var hostKeyCallback ssh.HostKeyCallback
	if config.HostKeyCallback != nil {
		// Use provided callback (must be ssh.HostKeyCallback)
		if cb, ok := config.HostKeyCallback.(ssh.HostKeyCallback); ok {
			hostKeyCallback = cb
		} else {
			return nil, fmt.Errorf("HostKeyCallback must be of type ssh.HostKeyCallback")
		}
	} else if len(config.HostKey) > 0 {
		// Create host key callback from provided public key
		expectedKey, err := parseHostKey(config.HostKey)
		if err != nil {
			return nil, fmt.Errorf("failed to parse host key: %w", err)
		}
		hostKeyCallback = createHostKeyCallback(expectedKey, config.Address)
	}

	return &sshClient{
		address:            config.Address,
		port:               config.Port,
		user:               config.User,
		privateKey:         config.PrivateKey,
		hostKey:            config.HostKey,
		timeout:            config.Timeout,
		hostKeyCallback:    hostKeyCallback,
		insecureSkipVerify: config.InsecureSkipVerify,
		volumeLocks:        newTargetLockManager(),
		commandObserver:    config.CommandObserver,
		decisionObserver:   config.MutationDecisionObserver,
	}, nil
}

// GetAddress returns the RDS server address
func (c *sshClient) GetAddress() string {
	return c.address
}

// Connect establishes SSH connection to RDS
func (c *sshClient) Connect() error {
	klog.V(4).Infof("Connecting to RDS at %s:%d as user %s", c.address, c.port, c.user)

	// Log authentication attempt
	secLogger := security.GetLogger()
	secLogger.LogSSHConnectionAttempt(c.user, c.address)

	// Configure SSH client with host key callback
	var hostKeyCallback ssh.HostKeyCallback
	if c.hostKeyCallback != nil {
		hostKeyCallback = c.hostKeyCallback
		klog.V(4).Info("Using host key verification")
	} else if c.insecureSkipVerify {
		hostKeyCallback = ssh.InsecureIgnoreHostKey()
		klog.Warning("SECURITY WARNING: Skipping SSH host key verification - INSECURE and not recommended for production!")
	} else {
		// No host key verification configured - this is a security error
		return fmt.Errorf("SSH host key verification not configured: must provide HostKey or enable InsecureSkipVerify (not recommended)")
	}

	sshConfig := &ssh.ClientConfig{
		User:            c.user,
		HostKeyCallback: hostKeyCallback,
		Timeout:         c.timeout,
	}

	// Add authentication if private key is provided
	if len(c.privateKey) > 0 {
		// Parse private key
		signer, err := ssh.ParsePrivateKey(c.privateKey)
		if err != nil {
			return fmt.Errorf("failed to parse private key: %w", err)
		}
		sshConfig.Auth = []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		}
	} else {
		// No authentication (for testing with mock servers)
		klog.V(4).Info("No private key provided, attempting connection without authentication")
	}

	// Establish connection
	addr := fmt.Sprintf("%s:%d", c.address, c.port)
	client, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		// Log authentication failure
		secLogger.LogSSHConnectionFailure(c.user, c.address, err)
		return fmt.Errorf("failed to connect to %s: %w", addr, err)
	}

	c.sshClient = client
	klog.V(4).Infof("Successfully connected to RDS at %s:%d", c.address, c.port)

	// Log successful authentication
	secLogger.LogSSHConnectionSuccess(c.user, c.address)
	return nil
}

// Close closes the SSH connection
func (c *sshClient) Close() error {
	if c.sshClient != nil {
		klog.V(4).Infof("Closing SSH connection to RDS")
		return c.sshClient.Close()
	}
	return nil
}

// IsConnected returns true if SSH connection is active
func (c *sshClient) IsConnected() bool {
	if c.sshClient == nil {
		return false
	}

	// Test connection by trying to create a session
	// RouterOS may not support keepalive requests, so use session creation as test
	// Serialize session creation to prevent concurrent calls
	c.sessionMu.Lock()
	session, err := c.sshClient.NewSession()
	c.sessionMu.Unlock()
	if err != nil {
		return false
	}
	_ = session.Close()
	return true
}

// runCommand executes a RouterOS CLI command via SSH
func (c *sshClient) runCommand(command string) (string, error) {
	start := time.Now()
	classification := ClassifyRouterOSCommand(command)
	var err error
	defer func() {
		if c.commandObserver == nil {
			return
		}
		status := "success"
		if err != nil {
			status = "failure"
		}
		c.commandObserver.RecordRDSCommand(
			classification.Family,
			classification.Action,
			string(classification.Class),
			status,
			time.Since(start),
		)
	}()

	if c.sshClient == nil {
		err = fmt.Errorf("not connected to RDS")
		return "", err
	}

	klog.V(5).Infof("Executing RouterOS command: %s", command)

	// Serialize session creation to prevent concurrent NewSession() calls
	// which can cause RouterOS to block or fail (session limits per connection)
	c.sessionMu.Lock()
	session, err := c.sshClient.NewSession()
	c.sessionMu.Unlock()
	if err != nil {
		return "", fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer func() { _ = session.Close() }()

	// Capture stdout and stderr
	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// Run command
	err = session.Run(command)
	if err != nil {
		// Check if it's an exit error (command failed)
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			err = fmt.Errorf("command failed (exit %d): %s", exitErr.ExitStatus(), stderr.String())
			return stdout.String(), err
		}
		err = fmt.Errorf("failed to run command: %w", err)
		return "", err
	}

	output := stdout.String()
	klog.V(5).Infof("Command output: %s", output)
	if err = routerOSOutputError(command, output); err != nil {
		return output, err
	}
	return output, nil
}

func (c *sshClient) recordMutationDecision(operation, target, decision, reason string) {
	if c.decisionObserver == nil {
		return
	}
	c.decisionObserver.RecordRDSMutationDecision(operation, target, decision, reason)
}

// runCommandWithRetry executes a command with retry logic for transient errors
func (c *sshClient) runCommandWithRetry(command string, maxRetries int) (string, error) {
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			klog.V(4).Infof("Retrying command after %v (attempt %d/%d)", backoff, attempt+1, maxRetries)
			time.Sleep(backoff)
		}

		// Reconnect if connection is lost
		if !c.IsConnected() {
			klog.V(4).Info("Reconnecting to RDS before retry")
			if err := c.Connect(); err != nil {
				lastErr = err
				continue
			}
		}

		output, err := c.runCommand(command)
		if err == nil {
			return output, nil
		}

		lastErr = err

		// Check if error is retryable
		if !isRetryableError(err) {
			klog.V(4).Infof("Non-retryable error: %v", err)
			// Wrap with sentinel if it's a known error type
			errStr := lastErr.Error()
			if strings.Contains(errStr, "not enough space") {
				return "", fmt.Errorf("%w: %s", utils.ErrResourceExhausted, errStr)
			}
			return "", lastErr
		}

		klog.V(4).Infof("Retryable error: %v", err)
	}

	return "", fmt.Errorf("max retries (%d) exceeded: %w", maxRetries, lastErr)
}

// isRetryableError determines if an error is worth retrying
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// RouterOS CLI rejections are deterministic; retrying repeats a doomed
	// mutation against fragile shared infrastructure.
	var outputErr *CommandOutputError
	if errors.As(err, &outputErr) {
		return false
	}

	// Network errors are retryable
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	// EOF and connection reset errors are retryable
	if errors.Is(err, io.EOF) {
		return true
	}

	errStr := err.Error()

	// Don't retry command errors like "not enough space", "invalid parameter"
	nonRetryablePatterns := []string{
		"not enough space",
		"invalid parameter",
		"no such item",
		"authentication failed",
	}

	for _, pattern := range nonRetryablePatterns {
		if containsString(errStr, pattern) {
			return false
		}
	}

	// Retry everything else (connection issues, transient errors)
	return true
}

// containsString checks if a string contains a substring (case-insensitive)
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && indexString(s, substr) >= 0)
}

// indexString finds the index of substr in s
func indexString(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// parseHostKey parses an SSH public key from various formats
// Supports: OpenSSH authorized_keys format, SSH known_hosts format, raw public key
func parseHostKey(keyData []byte) (ssh.PublicKey, error) {
	// Try parsing as OpenSSH public key (ssh-rsa AAAA..., ssh-ed25519 AAAA..., etc.)
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey(keyData)
	if err == nil {
		return pubKey, nil
	}

	// Try parsing as raw public key
	pubKey, err = ssh.ParsePublicKey(keyData)
	if err == nil {
		return pubKey, nil
	}

	return nil, fmt.Errorf("failed to parse host key in any supported format")
}

// createHostKeyCallback creates an SSH host key callback that verifies against expected key
func createHostKeyCallback(expectedKey ssh.PublicKey, hostname string) ssh.HostKeyCallback {
	return func(h string, remote net.Addr, key ssh.PublicKey) error {
		// Compare the key type first
		if key.Type() != expectedKey.Type() {
			return fmt.Errorf("SSH host key type mismatch for %s: expected %s, got %s", hostname, expectedKey.Type(), key.Type())
		}

		// Compare the key fingerprints
		expectedFingerprint := ssh.FingerprintSHA256(expectedKey)
		actualFingerprint := ssh.FingerprintSHA256(key)

		if expectedFingerprint != actualFingerprint {
			klog.Errorf("SSH HOST KEY VERIFICATION FAILED for %s!", hostname)
			klog.Errorf("Expected fingerprint: %s", expectedFingerprint)
			klog.Errorf("Actual fingerprint:   %s", actualFingerprint)
			klog.Errorf("This could indicate a man-in-the-middle attack or host key change!")

			// Log critical security event
			secLogger := security.GetLogger()
			secLogger.LogSSHHostKeyMismatch(hostname, expectedFingerprint, actualFingerprint)

			return fmt.Errorf("SSH host key verification failed for %s: fingerprint mismatch (expected %s, got %s)",
				hostname, expectedFingerprint, actualFingerprint)
		}

		klog.V(4).Infof("SSH host key verified for %s: %s", hostname, actualFingerprint)

		// Log successful host key verification
		secLogger := security.GetLogger()
		secLogger.LogSSHHostKeyVerified(hostname, actualFingerprint)
		return nil
	}
}
