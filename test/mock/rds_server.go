package mock

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"k8s.io/klog/v2"
)

// MockRDSServer simulates a MikroTik RDS server for testing
type MockRDSServer struct {
	address        string
	port           int
	listener       net.Listener
	sshConfig      *ssh.ServerConfig
	config         MockRDSConfig
	timing         *TimingSimulator
	errorInjector  *ErrorInjector
	volumes        map[string]*MockVolume   // Disk objects indexed by slot
	snapshots      map[string]*MockSnapshot // Snapshot disk entries indexed by slot
	files          map[string]*MockFile     // Files indexed by path
	commandHistory []CommandLog             // Command execution history for debugging
	mu             sync.RWMutex
	shutdown       chan struct{}
}

// CommandLog represents a single command execution record
type CommandLog struct {
	Timestamp time.Time
	Command   string
	Response  string
	ExitCode  int
}

// MockVolume represents a simulated volume on the mock RDS
type MockVolume struct {
	Slot          string
	FilePath      string
	FileSizeBytes int64
	Mount         bool
	NVMETCPPort   int
	NVMETCPNQN    string
	Exported      bool
}

// MockFile represents a file on the mock RDS filesystem
type MockFile struct {
	Path      string
	SizeBytes int64
	Type      string
	CreatedAt string
}

// MockSnapshot represents a copy-from snapshot disk entry on the mock RDS.
// Snapshot disks are independent copies (not linked to source after creation)
// and are NOT NVMe-exported (they are immutable backing files only).
type MockSnapshot struct {
	Slot          string    // Snapshot slot name (e.g., snap-<uuid>-at-<hash>)
	SourceVolume  string    // Source volume slot (pvc-<uuid>) — for CSI tracking
	FilePath      string    // Backing file path (e.g., /storage-pool/metal-csi/snap-xxx.img)
	FileSizeBytes int64     // Size copied from source volume
	Mount         bool      // RouterOS disk mount setting
	CreatedAt     time.Time // Creation timestamp
}

// NewMockRDSServer creates a new mock RDS server for testing
func NewMockRDSServer(port int) (*MockRDSServer, error) {
	// Load configuration from environment
	config := LoadConfigFromEnv()

	// Create SSH server config
	sshConfig := &ssh.ServerConfig{
		NoClientAuth: true, // Simplified for testing
	}

	// Generate a temporary host key
	hostKey, err := generateHostKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate host key: %w", err)
	}
	sshConfig.AddHostKey(hostKey)

	server := &MockRDSServer{
		address:        "127.0.0.1",
		port:           port,
		sshConfig:      sshConfig,
		config:         config,
		timing:         NewTimingSimulator(config),
		errorInjector:  NewErrorInjector(config),
		volumes:        make(map[string]*MockVolume),
		snapshots:      make(map[string]*MockSnapshot),
		files:          make(map[string]*MockFile),
		commandHistory: make([]CommandLog, 0),
		shutdown:       make(chan struct{}),
	}

	return server, nil
}

// Start starts the mock RDS SSH server
func (s *MockRDSServer) Start() error {
	addr := fmt.Sprintf("%s:%d", s.address, s.port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	s.listener = listener

	// Update port if it was 0 (random port assignment)
	if s.port == 0 {
		if tcpAddr, ok := listener.Addr().(*net.TCPAddr); ok {
			s.port = tcpAddr.Port
		}
	}

	klog.Infof("Mock RDS server listening on %s:%d", s.address, s.port)

	go s.acceptConnections()

	return nil
}

// Stop stops the mock RDS server
func (s *MockRDSServer) Stop() error {
	close(s.shutdown)
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// Address returns the server address
func (s *MockRDSServer) Address() string {
	return s.address
}

// Port returns the server port
func (s *MockRDSServer) Port() int {
	return s.port
}

// GetVolume returns a volume by slot ID
func (s *MockRDSServer) GetVolume(slot string) (*MockVolume, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	vol, ok := s.volumes[slot]
	return vol, ok
}

// ListVolumes returns all volumes
func (s *MockRDSServer) ListVolumes() []*MockVolume {
	s.mu.RLock()
	defer s.mu.RUnlock()
	volumes := make([]*MockVolume, 0, len(s.volumes))
	for _, vol := range s.volumes {
		volumes = append(volumes, vol)
	}
	return volumes
}

// GetFile returns a file by path
func (s *MockRDSServer) GetFile(path string) (*MockFile, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	file, ok := s.files[path]
	return file, ok
}

// ListFiles returns all files
func (s *MockRDSServer) ListFiles() []*MockFile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	files := make([]*MockFile, 0, len(s.files))
	for _, file := range s.files {
		files = append(files, file)
	}
	return files
}

// GetSnapshot returns a snapshot by slot name
func (s *MockRDSServer) GetSnapshot(slot string) (*MockSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap, ok := s.snapshots[slot]
	return snap, ok
}

// ListSnapshots returns all snapshots
func (s *MockRDSServer) ListSnapshots() []*MockSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshots := make([]*MockSnapshot, 0, len(s.snapshots))
	for _, snap := range s.snapshots {
		snapshots = append(snapshots, snap)
	}
	return snapshots
}

// CreateOrphanedFile creates a file without a corresponding disk object (for testing)
func (s *MockRDSServer) CreateOrphanedFile(path string, sizeBytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = &MockFile{
		Path:      path,
		SizeBytes: sizeBytes,
		Type:      ".img",
		CreatedAt: "2025-11-11 12:00:00",
	}
}

// CreateOrphanedVolume creates a disk object without a file (for testing)
func (s *MockRDSServer) CreateOrphanedVolume(slot, filePath string, sizeBytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Create volume but DON'T create the backing file
	s.volumes[slot] = &MockVolume{
		Slot:          slot,
		FilePath:      filePath,
		FileSizeBytes: sizeBytes,
		Mount:         true,
		NVMETCPPort:   4420,
		NVMETCPNQN:    fmt.Sprintf("nqn.2000-02.com.mikrotik:%s", slot),
		Exported:      true,
	}
}

// DeleteFile deletes a file (for testing cleanup scenarios)
func (s *MockRDSServer) DeleteFile(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, path)
}

// GetCommandHistory returns a copy of the command execution history
// Thread-safe for concurrent access during test debugging
func (s *MockRDSServer) GetCommandHistory() []CommandLog {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Return a copy to prevent external modification
	history := make([]CommandLog, len(s.commandHistory))
	copy(history, s.commandHistory)
	return history
}

// ClearCommandHistory clears the command execution history
// Useful for resetting state between test cases
func (s *MockRDSServer) ClearCommandHistory() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commandHistory = make([]CommandLog, 0)
}

// ResetErrorInjector resets the error injector's operation counter
// Useful for test isolation between test cases
func (s *MockRDSServer) ResetErrorInjector() {
	s.errorInjector.Reset()
}

// SetErrorMode changes the error injection mode at runtime.
// Use ErrorModeNone to clear errors, ErrorModeCommandFail to simulate failures.
// This is the primary API for resilience tests to inject/clear errors dynamically.
func (s *MockRDSServer) SetErrorMode(mode ErrorMode) {
	s.errorInjector.SetErrorMode(mode)
}

func (s *MockRDSServer) acceptConnections() {
	for {
		select {
		case <-s.shutdown:
			return
		default:
			conn, err := s.listener.Accept()
			if err != nil {
				select {
				case <-s.shutdown:
					return
				default:
					klog.Errorf("Failed to accept connection: %v", err)
					continue
				}
			}

			go s.handleConnection(conn)
		}
	}
}

func (s *MockRDSServer) handleConnection(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	// Perform SSH handshake
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, s.sshConfig)
	if err != nil {
		klog.Errorf("Failed to handshake: %v", err)
		return
	}
	defer func() { _ = sshConn.Close() }()

	klog.V(4).Infof("New SSH connection from %s", sshConn.RemoteAddr())

	// Discard all global requests
	go ssh.DiscardRequests(reqs)

	// Handle channels
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}

		channel, requests, err := newChannel.Accept()
		if err != nil {
			klog.Errorf("Could not accept channel: %v", err)
			continue
		}

		go s.handleSession(channel, requests)
	}
}

func (s *MockRDSServer) handleSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer func() { _ = channel.Close() }()

	// Simulate SSH latency at session start
	s.timing.SimulateSSHLatency()

	for req := range requests {
		klog.V(4).Infof("Mock RDS received request type: %s, payload len: %d", req.Type, len(req.Payload))

		switch req.Type {
		case "exec":
			if len(req.Payload) > 4 {
				// Parse command from payload (SSH exec request format)
				cmdLen := uint32(req.Payload[0])<<24 | uint32(req.Payload[1])<<16 |
					uint32(req.Payload[2])<<8 | uint32(req.Payload[3])

				klog.V(4).Infof("Mock RDS command length: %d, total payload: %d", cmdLen, len(req.Payload))

				if len(req.Payload) >= 4+int(cmdLen) {
					command := string(req.Payload[4 : 4+cmdLen])
					klog.Infof("Mock RDS executing command: %s", command)

					// Execute the command and get response
					response, exitStatus := s.executeCommand(command)

					// Send response
					if response != "" {
						klog.V(4).Infof("Mock RDS sending response (%d bytes)", len(response))
						_, _ = channel.Write([]byte(response))
					}

					// Send exit status
					_ = req.Reply(true, nil)
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{Status: uint32(exitStatus)}))
					klog.V(4).Infof("Mock RDS sent exit status: %d", exitStatus)
					return
				}
			}
			klog.Warningf("Mock RDS: Invalid exec payload format")
			_ = req.Reply(false, nil)

		case "shell":
			// Not supported for now
			_ = req.Reply(false, nil)

		default:
			_ = req.Reply(false, nil)
		}
	}
}

func (s *MockRDSServer) executeCommand(command string) (string, int) {
	command = strings.TrimSpace(command)
	klog.V(3).Infof("Mock RDS executing command: %s", command)

	var output string
	var exitCode int

	// Parse /disk add command
	if strings.HasPrefix(command, "/disk add") {
		output, exitCode = s.handleDiskAdd(command)
		klog.V(3).Infof("Mock RDS /disk add returned code %d, output: %s", exitCode, output)
	} else if strings.HasPrefix(command, "/disk set") {
		// Parse /disk set command (for resize)
		output, exitCode = s.handleDiskSet(command)
		klog.V(3).Infof("Mock RDS /disk set returned code %d", exitCode)
	} else if strings.HasPrefix(command, "/disk remove") {
		// Parse /disk remove command
		output, exitCode = s.handleDiskRemove(command)
		klog.V(3).Infof("Mock RDS /disk remove returned code %d", exitCode)
	} else if strings.HasPrefix(command, "/disk print detail") {
		// Parse /disk print detail command
		output, exitCode = s.handleDiskPrintDetail(command)
		klog.V(3).Infof("Mock RDS /disk print detail returned code %d", exitCode)
	} else if strings.HasPrefix(command, "/file print detail") {
		// Parse /file print detail command
		output, exitCode = s.handleFilePrintDetail(command)
		klog.V(3).Infof("Mock RDS /file print detail returned code %d", exitCode)
	} else if strings.HasPrefix(command, "/file remove") {
		// Parse /file remove command
		output, exitCode = s.handleFileRemove(command)
		klog.V(3).Infof("Mock RDS /file remove returned code %d", exitCode)
	} else {
		klog.Warningf("Mock RDS: Unrecognized command: %s", command)
		output = fmt.Sprintf("bad command name %s\n", command)
		exitCode = 1
	}

	// Record command in history for debugging
	s.recordCommand(command, output, exitCode)

	return output, exitCode
}

// recordCommand adds a command execution to the history log
func (s *MockRDSServer) recordCommand(command, response string, exitCode int) {
	if !s.config.EnableHistory {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Trim history if over depth limit
	if len(s.commandHistory) >= s.config.HistoryDepth {
		s.commandHistory = s.commandHistory[1:]
	}

	s.commandHistory = append(s.commandHistory, CommandLog{
		Timestamp: time.Now(),
		Command:   command,
		Response:  response,
		ExitCode:  exitCode,
	})
}

func (s *MockRDSServer) handleDiskAdd(command string) (string, int) {
	// Check error injection BEFORE normal processing
	if shouldFail, errMsg := s.errorInjector.ShouldFailDiskAdd(); shouldFail {
		klog.V(2).Infof("MOCK ERROR INJECTION: Disk add failed - %s", strings.TrimSpace(errMsg))
		return errMsg, 1
	}

	// Detect copy-from parameter — route to snapshot/restore creation logic
	if strings.Contains(command, "copy-from=") {
		return s.handleDiskAddCopyFrom(command)
	}

	// Regular volume creation
	// Example: /disk add type=file file-path=/storage/vol.img file-size=1G slot=pvc-123 nvme-tcp-export=yes nvme-tcp-server-port=4420 nvme-tcp-server-nqn=nqn.2025-01.io.srvlab.rds:pvc-123

	slot := extractParam(command, "slot")
	filePath := extractParam(command, "file-path")
	fileSizeStr := extractParam(command, "file-size")
	mountParam := extractParam(command, "mount")
	nvmePortStr := extractParam(command, "nvme-tcp-server-port")
	nqn := extractParam(command, "nvme-tcp-server-nqn")

	if slot == "" || filePath == "" || fileSizeStr == "" {
		return "failure: missing required parameters\n", 1
	}

	// Parse file size (supports formats like "1G", "50G", "1T", or raw bytes)
	fileSize, err := parseSize(fileSizeStr)
	if err != nil {
		return fmt.Sprintf("failure: invalid file size %s: %v\n", fileSizeStr, err), 1
	}

	var nvmePort int
	if nvmePortStr != "" {
		_, _ = fmt.Sscanf(nvmePortStr, "%d", &nvmePort)
	} else {
		nvmePort = 4420 // default
	}

	// Simulate disk operation delay BEFORE state modification
	s.timing.SimulateDiskOperation("add")

	// Check if volume already exists
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.volumes[slot]; exists {
		return "failure: volume already exists\n", 1
	}

	// Create volume
	s.volumes[slot] = &MockVolume{
		Slot:          slot,
		FilePath:      filePath,
		FileSizeBytes: fileSize,
		Mount:         mountParam != "no",
		NVMETCPPort:   nvmePort,
		NVMETCPNQN:    nqn,
		Exported:      true,
	}

	// Also create the backing file (simulating real RDS behavior)
	s.files[filePath] = &MockFile{
		Path:      filePath,
		SizeBytes: fileSize,
		Type:      ".img",
		CreatedAt: "2025-11-11 12:00:00",
	}

	klog.V(2).Infof("Mock RDS: Created volume %s with backing file %s", slot, filePath)
	return "", 0
}

// handleDiskAddCopyFrom handles /disk add copy-from= commands for snapshot creation and restore.
//
// Two use cases:
//  1. Snapshot creation: copy-from present, nvme-tcp-export absent → creates entry in s.snapshots
//  2. Volume restore: copy-from present, nvme-tcp-export=yes present → creates entry in s.volumes
//
// The copy-from source is looked up first in s.volumes, then s.snapshots.
func (s *MockRDSServer) handleDiskAddCopyFrom(command string) (string, int) {
	// Parse copy-from=[find slot=<name>] to extract source slot name
	copyFromRe := regexp.MustCompile(`copy-from=\[find slot=([^\]]+)\]`)
	copyFromMatches := copyFromRe.FindStringSubmatch(command)
	if len(copyFromMatches) < 2 {
		return "failure: invalid copy-from format\n", 1
	}
	sourceSlot := copyFromMatches[1]

	// Extract the destination slot — must NOT match the slot inside copy-from=[find slot=...]
	// Use a regex that anchors to whitespace before "slot=" (avoids matching inside copy-from).
	destSlotRe := regexp.MustCompile(`(?:^|\s)slot=([^\s\]]+)`)
	destSlotMatches := destSlotRe.FindAllStringSubmatch(command, -1)
	// The last match is the destination slot (the one outside copy-from)
	var slot string
	for _, m := range destSlotMatches {
		if len(m) >= 2 {
			candidate := m[1]
			// Skip if candidate is the source slot (same value, or part of copy-from construct)
			if candidate != sourceSlot {
				slot = candidate
			}
		}
	}

	filePath := extractParam(command, "file-path")

	if slot == "" || filePath == "" {
		return "failure: missing required parameters\n", 1
	}

	// Simulate disk operation delay BEFORE state modification
	s.timing.SimulateDiskOperation("add")

	s.mu.Lock()
	defer s.mu.Unlock()

	// Look up source size: check volumes first, then snapshots
	var sourceSize int64
	if sourceVol, ok := s.volumes[sourceSlot]; ok {
		sourceSize = sourceVol.FileSizeBytes
	} else if sourceSnap, ok := s.snapshots[sourceSlot]; ok {
		sourceSize = sourceSnap.FileSizeBytes
	} else {
		return "failure: no such item\n", 1
	}

	// Determine if this is a restore operation (nvme-tcp-export=yes present)
	isRestore := strings.Contains(command, "nvme-tcp-export=yes")

	if isRestore {
		// RestoreSnapshot path: create an NVMe-exported volume from snapshot
		fileSizeStr := extractParam(command, "file-size")
		fileSize := sourceSize
		if fileSizeStr != "" {
			if parsed, err := parseSize(fileSizeStr); err == nil {
				fileSize = parsed
			}
		}

		nvmePortStr := extractParam(command, "nvme-tcp-server-port")
		mountParam := extractParam(command, "mount")
		nqn := extractParam(command, "nvme-tcp-server-nqn")

		var nvmePort int
		if nvmePortStr != "" {
			_, _ = fmt.Sscanf(nvmePortStr, "%d", &nvmePort)
		} else {
			nvmePort = 4420
		}

		if _, exists := s.volumes[slot]; exists {
			return "failure: volume already exists\n", 1
		}

		s.volumes[slot] = &MockVolume{
			Slot:          slot,
			FilePath:      filePath,
			FileSizeBytes: fileSize,
			Mount:         mountParam != "no",
			NVMETCPPort:   nvmePort,
			NVMETCPNQN:    nqn,
			Exported:      true,
		}
		s.files[filePath] = &MockFile{
			Path:      filePath,
			SizeBytes: fileSize,
			Type:      ".img",
			CreatedAt: "2025-11-11 12:00:00",
		}

		klog.V(2).Infof("Mock RDS: Restored snapshot %s to new volume %s", sourceSlot, slot)
		return "", 0
	}

	// Snapshot creation path: create independent snapshot disk entry (NOT NVMe-exported)
	if _, exists := s.snapshots[slot]; exists {
		return "failure: snapshot already exists\n", 1
	}

	s.snapshots[slot] = &MockSnapshot{
		Slot:          slot,
		SourceVolume:  sourceSlot,
		FilePath:      filePath,
		FileSizeBytes: sourceSize,
		Mount:         extractParam(command, "mount") != "no",
		CreatedAt:     time.Now(),
	}

	// Also create backing file entry
	s.files[filePath] = &MockFile{
		Path:      filePath,
		SizeBytes: sourceSize,
		Type:      ".img",
		CreatedAt: "2025-11-11 12:00:00",
	}

	klog.V(2).Infof("Mock RDS: Created snapshot %s from volume %s (size=%d)", slot, sourceSlot, sourceSize)
	return "", 0
}

func (s *MockRDSServer) handleDiskSet(command string) (string, int) {
	// Parse: /disk set [find slot=pvc-123] file-size=10G
	re := regexp.MustCompile(`slot=([^\s\]]+)`)
	matches := re.FindStringSubmatch(command)

	if len(matches) < 2 {
		return "failure: invalid command format\n", 1
	}

	slot := matches[1]
	fileSizeStr := extractParam(command, "file-size")

	if fileSizeStr == "" {
		return "failure: file-size parameter required\n", 1
	}

	// Parse new file size
	newSize, err := parseSize(fileSizeStr)
	if err != nil {
		return fmt.Sprintf("failure: invalid file size %s: %v\n", fileSizeStr, err), 1
	}

	// Simulate disk operation delay BEFORE state modification
	s.timing.SimulateDiskOperation("set")

	s.mu.Lock()
	defer s.mu.Unlock()

	vol, exists := s.volumes[slot]
	if !exists {
		return "failure: no such item\n", 1
	}

	// Update volume size
	oldSize := vol.FileSizeBytes
	vol.FileSizeBytes = newSize

	// Also update backing file size if it exists
	if vol.FilePath != "" {
		if file, fileExists := s.files[vol.FilePath]; fileExists {
			file.SizeBytes = newSize
		}
	}

	klog.V(2).Infof("Mock RDS: Resized volume %s from %d to %d bytes", slot, oldSize, newSize)
	return "", 0
}

func (s *MockRDSServer) handleDiskRemove(command string) (string, int) {
	// Check error injection BEFORE normal processing
	if shouldFail, errMsg := s.errorInjector.ShouldFailDiskRemove(); shouldFail {
		klog.V(2).Infof("MOCK ERROR INJECTION: Disk remove failed - %s", strings.TrimSpace(errMsg))
		return errMsg, 1
	}

	// Parse: /disk remove [find slot=pvc-123]
	re := regexp.MustCompile(`slot=([^\s\]]+)`)
	matches := re.FindStringSubmatch(command)

	if len(matches) < 2 {
		return "failure: invalid command format\n", 1
	}

	slot := matches[1]

	// Simulate disk operation delay BEFORE state modification
	s.timing.SimulateDiskOperation("remove")

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check volumes map first
	if vol, exists := s.volumes[slot]; exists {
		delete(s.volumes, slot)

		// Also delete the backing file (simulating normal RDS behavior)
		// Note: In some failure scenarios, the file might remain (orphaned)
		// Tests can use DeleteFile() separately to simulate this
		if vol.FilePath != "" {
			delete(s.files, vol.FilePath)
			klog.V(2).Infof("Mock RDS: Deleted volume %s and backing file %s", slot, vol.FilePath)
		} else {
			klog.V(2).Infof("Mock RDS: Deleted volume %s (no backing file)", slot)
		}
		return "", 0
	}

	// Check snapshots map
	if snap, exists := s.snapshots[slot]; exists {
		delete(s.snapshots, slot)

		// Also delete the backing file
		if snap.FilePath != "" {
			delete(s.files, snap.FilePath)
			klog.V(2).Infof("Mock RDS: Deleted snapshot %s and backing file %s", slot, snap.FilePath)
		} else {
			klog.V(2).Infof("Mock RDS: Deleted snapshot %s (no backing file)", slot)
		}
		return "", 0
	}

	// Idempotent - not an error if slot exists in neither volumes nor snapshots
	return "", 0
}

func (s *MockRDSServer) handleDiskPrintDetail(command string) (string, int) {
	// Parse: /disk print detail where slot=pvc-123 OR slot~"snap-" OR mount-point="storage-pool"

	// Check for mount-point query (capacity query)
	if strings.Contains(command, "mount-point=") {
		// Return mock filesystem capacity info
		return s.formatMountPointCapacity(), 0
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check for slot~ pattern query (prefix/substring match, RouterOS wildcard syntax)
	// Format: slot~"prefix" — matches slots that contain the pattern
	if strings.Contains(command, "slot~") {
		slotPatternRe := regexp.MustCompile(`slot~"([^"]+)"`)
		if matches := slotPatternRe.FindStringSubmatch(command); len(matches) >= 2 {
			pattern := matches[1]
			var output strings.Builder
			i := 0
			for _, vol := range s.volumes {
				if strings.Contains(vol.Slot, pattern) {
					fmt.Fprintf(&output, "%2d %s\n", i, s.formatDiskDetail(vol))
					i++
				}
			}
			for _, snap := range s.snapshots {
				if strings.Contains(snap.Slot, pattern) {
					fmt.Fprintf(&output, "%2d %s\n", i, s.formatSnapshotDetail(snap))
					i++
				}
			}
			return output.String(), 0
		}
	}

	// Check for exact slot= query
	slot := ""
	if strings.Contains(command, "slot=") {
		re := regexp.MustCompile(`slot=([^\s]+)`)
		matches := re.FindStringSubmatch(command)
		if len(matches) >= 2 {
			slot = matches[1]
		}
	}

	if slot != "" {
		// Check volumes first
		if vol, exists := s.volumes[slot]; exists {
			return s.formatDiskDetail(vol), 0
		}
		// Check snapshots
		if snap, exists := s.snapshots[slot]; exists {
			return s.formatSnapshotDetail(snap), 0
		}
		return "", 0 // No output if not found
	}

	// Return all volumes and snapshots (with line numbers for proper RouterOS format)
	var output strings.Builder
	i := 0
	for _, vol := range s.volumes {
		// RouterOS formats list output with line numbers
		fmt.Fprintf(&output, "%2d %s\n", i, s.formatDiskDetail(vol))
		i++
	}
	for _, snap := range s.snapshots {
		fmt.Fprintf(&output, "%2d %s\n", i, s.formatSnapshotDetail(snap))
		i++
	}
	return output.String(), 0
}

func (s *MockRDSServer) formatDiskDetail(vol *MockVolume) string {
	exported := "no"
	if vol.Exported {
		exported = "yes"
	}
	mount := "no"
	if vol.Mount {
		mount = "yes"
	}

	// Format as RouterOS key="value" pairs on a single line
	return fmt.Sprintf(`slot="%s" type="file" file-path="%s" file-size=%d mount=%s nvme-tcp-export=%s nvme-tcp-server-port=%d nvme-tcp-server-nqn="%s" status="ready"`,
		vol.Slot, vol.FilePath, vol.FileSizeBytes, mount, exported, vol.NVMETCPPort, vol.NVMETCPNQN)
}

// formatSnapshotDetail formats a snapshot disk entry for /disk print detail output.
// Snapshots are NOT NVMe-exported — nvme-tcp-export, nvme-tcp-server-port, and
// nvme-tcp-server-nqn fields are intentionally omitted.
// source-volume is included so that parseSnapshotInfo can recover the original source
// volume ID for CSI idempotency checks (without this, the parser would fall back to
// extracting a hashed UUID from the slot name, which doesn't match the original ID).
func (s *MockRDSServer) formatSnapshotDetail(snap *MockSnapshot) string {
	// Format creation time as RouterOS month/day/year format with lowercase month abbreviation.
	// parseRouterOSTime expects e.g. "jan/02/2026 14:30:00" — title-cases the month internally.
	creationTime := strings.ToLower(snap.CreatedAt.Format("Jan/02/2006 15:04:05"))
	mount := "no"
	if snap.Mount {
		mount = "yes"
	}
	return fmt.Sprintf(`slot="%s" type="file" file-path="%s" file-size=%d mount=%s source-volume="%s" creation-time=%s status="ready"`,
		snap.Slot, snap.FilePath, snap.FileSizeBytes, mount, snap.SourceVolume, creationTime)
}

func (s *MockRDSServer) handleFilePrintDetail(command string) (string, int) {
	// Parse: /file print detail where name~"storage-pool/metal-csi"
	// Extract the search pattern
	re := regexp.MustCompile(`name~"([^"]+)"`)
	matches := re.FindStringSubmatch(command)

	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(matches) < 2 {
		// No pattern specified, return all files (shouldn't happen in practice)
		return "", 0
	}

	pattern := matches[1]
	klog.V(4).Infof("Mock RDS: Listing files matching pattern: %s", pattern)

	// Find all files matching the pattern
	var output strings.Builder
	i := 0

	// Always include the directory entry first (if pattern matches)
	if strings.Contains(pattern, "/") {
		dirPath := "/" + pattern
		fmt.Fprintf(&output, " %d   name=%s type=directory\n", i, dirPath)
		output.WriteString("     last-modified=2025-11-11 16:47:07\n\n")
		i++
	}

	// Then list all matching files
	for path, file := range s.files {
		// Check if file path matches the pattern (simple substring match)
		if !strings.Contains(path, pattern) {
			continue
		}

		// Format size with units (matching RouterOS format)
		sizeStr := formatSizeWithUnits(file.SizeBytes)

		fmt.Fprintf(&output, " %d   name=%s\n", i, strings.TrimPrefix(path, "/"))
		fmt.Fprintf(&output, "     type=%s file size=%s last-modified=%s\n\n",
			file.Type, sizeStr, file.CreatedAt)
		i++
	}

	return output.String(), 0
}

// formatSizeWithUnits formats bytes as human-readable size (e.g., "10.0GiB", "1024.0MiB")
func formatSizeWithUnits(bytes int64) string {
	const (
		KiB = 1024
		MiB = 1024 * KiB
		GiB = 1024 * MiB
		TiB = 1024 * GiB
	)

	switch {
	case bytes >= TiB:
		return fmt.Sprintf("%.1fTiB", float64(bytes)/float64(TiB))
	case bytes >= GiB:
		return fmt.Sprintf("%.1fGiB", float64(bytes)/float64(GiB))
	case bytes >= MiB:
		return fmt.Sprintf("%.1fMiB", float64(bytes)/float64(MiB))
	case bytes >= KiB:
		return fmt.Sprintf("%.1fKiB", float64(bytes)/float64(KiB))
	default:
		return fmt.Sprintf("%d", bytes)
	}
}

func (s *MockRDSServer) handleFileRemove(command string) (string, int) {
	// Parse: /file remove [find name="storage-pool/metal-csi/pvc-123.img"]
	re := regexp.MustCompile(`name="([^"]+)"`)
	matches := re.FindStringSubmatch(command)

	if len(matches) < 2 {
		return "failure: invalid command format\n", 1
	}

	// RouterOS file paths don't have leading slash, but we normalize to have it
	filePath := matches[1]
	if !strings.HasPrefix(filePath, "/") {
		filePath = "/" + filePath
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.files[filePath]; !exists {
		// Idempotent - not an error if file doesn't exist
		klog.V(3).Infof("Mock RDS: File %s not found (idempotent)", filePath)
		return "", 0
	}

	delete(s.files, filePath)
	klog.V(2).Infof("Mock RDS: Deleted file %s", filePath)
	return "", 0
}

func (s *MockRDSServer) formatMountPointCapacity() string {
	// Return mock capacity info for mount point query
	// This simulates: /disk print detail where mount-point="storage-pool"
	// RouterOS format uses size= and free= with space-separated numbers
	return `slot=storage-pool type=partition mount-point=storage-pool file-system=btrfs size=7 949 127 950 336 free=5 963 595 964 416 use=25%
`
}

func extractParam(command, param string) string {
	re := regexp.MustCompile(param + `=([^\s]+)`)
	matches := re.FindStringSubmatch(command)
	if len(matches) >= 2 {
		return matches[1]
	}
	return ""
}

// parseSize parses human-readable size strings like "1G", "50G", "1T" or raw bytes
func parseSize(sizeStr string) (int64, error) {
	// Parse with suffix (K, M, G, T) first
	re := regexp.MustCompile(`^(\d+)([KMGT])$`)
	matches := re.FindStringSubmatch(sizeStr)
	if len(matches) == 3 {
		// Parse the number part
		var num int64
		if _, err := fmt.Sscanf(matches[1], "%d", &num); err != nil {
			return 0, fmt.Errorf("invalid number: %s", matches[1])
		}

		// Multiply by the appropriate factor
		switch matches[2] {
		case "K":
			return num * 1024, nil
		case "M":
			return num * 1024 * 1024, nil
		case "G":
			return num * 1024 * 1024 * 1024, nil
		case "T":
			return num * 1024 * 1024 * 1024 * 1024, nil
		}

		return 0, fmt.Errorf("unknown size suffix: %s", matches[2])
	}

	// Try to parse as raw number (only if no suffix)
	var size int64
	if _, err := fmt.Sscanf(sizeStr, "%d", &size); err == nil {
		return size, nil
	}

	return 0, fmt.Errorf("invalid size format: %s", sizeStr)
}

func generateHostKey() (ssh.Signer, error) {
	// Generate a new RSA key for the mock SSH server
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("failed to generate RSA key: %w", err)
	}

	// Encode the private key to PEM format
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})

	// Parse it back to create an ssh.Signer
	signer, err := ssh.ParsePrivateKey(privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to parse generated key: %w", err)
	}

	return signer, nil
}
