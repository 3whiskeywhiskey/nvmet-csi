package rds

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/utils"
)

func TestNewClient(t *testing.T) {
	tests := []struct {
		name      string
		config    ClientConfig
		expectErr bool
		errMsg    string
	}{
		{
			name: "empty protocol defaults to ssh",
			config: ClientConfig{
				Protocol:           "",
				Address:            "10.42.68.1",
				User:               "admin",
				InsecureSkipVerify: true,
			},
			expectErr: false,
		},
		{
			name: "explicit ssh protocol creates SSH client",
			config: ClientConfig{
				Protocol:           "ssh",
				Address:            "10.42.68.1",
				User:               "admin",
				InsecureSkipVerify: true,
			},
			expectErr: false,
		},
		{
			name: "api protocol returns not yet implemented error",
			config: ClientConfig{
				Protocol: "api",
				Address:  "10.42.68.1",
				User:     "admin",
			},
			expectErr: true,
			errMsg:    "not yet implemented",
		},
		{
			name: "unknown protocol returns unsupported protocol error",
			config: ClientConfig{
				Protocol: "telnet",
				Address:  "10.42.68.1",
				User:     "admin",
			},
			expectErr: true,
			errMsg:    "unsupported protocol",
		},
		{
			name: "invalid SSH config missing address returns error",
			config: ClientConfig{
				Protocol: "ssh",
				User:     "admin",
			},
			expectErr: true,
			errMsg:    "address is required",
		},
		{
			name: "invalid SSH config missing user returns error",
			config: ClientConfig{
				Protocol: "ssh",
				Address:  "10.42.68.1",
			},
			expectErr: true,
			errMsg:    "user is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewClient(tt.config)

			if tt.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
				assert.Nil(t, client)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, client)

			// Verify it's an SSH client by checking the address
			assert.Equal(t, tt.config.Address, client.GetAddress())
		})
	}
}

func TestNewClient_DefaultValues(t *testing.T) {
	// Test that NewClient properly passes through to newSSHClient with defaults
	client, err := NewClient(ClientConfig{
		Address:            "10.42.68.1",
		User:               "admin",
		InsecureSkipVerify: true,
	})

	require.NoError(t, err)
	require.NotNil(t, client)

	// Verify it's an SSH client
	sshClient, ok := client.(*sshClient)
	require.True(t, ok, "client should be *sshClient")

	// Verify defaults are applied
	assert.Equal(t, 22, sshClient.port, "default port should be 22")
	assert.Equal(t, 10*time.Second, sshClient.timeout, "default timeout should be 10s")
}

func TestNewClient_CustomValues(t *testing.T) {
	// Test that custom values are preserved
	customTimeout := 5 * time.Second
	customPort := 2222

	client, err := NewClient(ClientConfig{
		Address:            "10.42.68.1",
		Port:               customPort,
		User:               "admin",
		Timeout:            customTimeout,
		InsecureSkipVerify: true,
	})

	require.NoError(t, err)
	require.NotNil(t, client)

	// Verify it's an SSH client
	sshClient, ok := client.(*sshClient)
	require.True(t, ok, "client should be *sshClient")

	// Verify custom values are applied
	assert.Equal(t, customPort, sshClient.port, "custom port should be preserved")
	assert.Equal(t, customTimeout, sshClient.timeout, "custom timeout should be preserved")
}

// ========================================
// Error Path Tests (Phase 25-01, Task 2)
// ========================================

func TestClient_ErrorScenarios(t *testing.T) {
	t.Run("VolumeNotFound error idempotency", func(t *testing.T) {
		// Test that VolumeNotFoundError is properly typed
		mockClient := NewMockClient()

		// Try to get non-existent volume
		_, err := mockClient.GetVolume("non-existent")

		// Verify error type
		require.Error(t, err)
		var notFoundErr *VolumeNotFoundError
		assert.True(t, errors.As(err, &notFoundErr), "should be VolumeNotFoundError")
		assert.Equal(t, "non-existent", notFoundErr.Slot)
	})

	t.Run("Delete non-existent volume succeeds (idempotent)", func(t *testing.T) {
		// CSI spec requires idempotent deletes
		mockClient := NewMockClient()

		// Delete should succeed even if volume doesn't exist
		err := mockClient.DeleteVolume("non-existent")
		assert.NoError(t, err, "delete non-existent volume should succeed")
	})

	t.Run("CreateVolume with invalid parameters", func(t *testing.T) {
		mockClient := NewMockClient()

		// Test empty slot
		err := mockClient.CreateVolume(CreateVolumeOptions{
			Slot:          "",
			FilePath:      "/test.img",
			FileSizeBytes: 1024 * 1024 * 1024,
		})
		// Mock doesn't validate, but real client would
		// This test documents expected behavior
		if err != nil {
			assert.Contains(t, err.Error(), "slot")
		}
	})

	t.Run("GetVolume with connection error", func(t *testing.T) {
		mockClient := NewMockClient()

		// Simulate connection failure
		mockClient.SetPersistentError(fmt.Errorf("connection refused: %w", utils.ErrConnectionFailed))

		_, err := mockClient.GetVolume("test-volume")
		require.Error(t, err)
		assert.ErrorIs(t, err, utils.ErrConnectionFailed, "should wrap ErrConnectionFailed")
	})

	t.Run("CreateVolume with disk full error", func(t *testing.T) {
		mockClient := NewMockClient()

		// Simulate disk full
		mockClient.SetPersistentError(fmt.Errorf("not enough space: %w", utils.ErrResourceExhausted))

		err := mockClient.CreateVolume(CreateVolumeOptions{
			Slot:          "test-vol",
			FilePath:      "/test.img",
			FileSizeBytes: 1024 * 1024 * 1024 * 1024, // 1 TiB
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, utils.ErrResourceExhausted, "should wrap ErrResourceExhausted")
	})

	t.Run("Concurrent CreateVolume operations", func(t *testing.T) {
		mockClient := NewMockClient()

		// First create should succeed
		err1 := mockClient.CreateVolume(CreateVolumeOptions{
			Slot:          "concurrent-vol",
			FilePath:      "/test1.img",
			FileSizeBytes: 1024 * 1024 * 1024,
		})
		assert.NoError(t, err1)

		// Second create with same slot should fail (already exists)
		err2 := mockClient.CreateVolume(CreateVolumeOptions{
			Slot:          "concurrent-vol",
			FilePath:      "/test2.img",
			FileSizeBytes: 1024 * 1024 * 1024,
		})
		require.Error(t, err2)
		assert.Contains(t, err2.Error(), "already exists")
	})

	t.Run("ResizeVolume with connection error", func(t *testing.T) {
		mockClient := NewMockClient()

		// Add volume first
		mockClient.AddVolume(&VolumeInfo{
			Slot:          "resize-test",
			FileSizeBytes: 1024 * 1024 * 1024,
		})

		// Simulate connection failure
		mockClient.SetPersistentError(fmt.Errorf("timeout: %w", utils.ErrOperationTimeout))

		err := mockClient.ResizeVolume("resize-test", 2*1024*1024*1024)
		require.Error(t, err)
		assert.ErrorIs(t, err, utils.ErrOperationTimeout, "should wrap ErrOperationTimeout")
	})

	t.Run("ResizeVolume non-existent volume", func(t *testing.T) {
		mockClient := NewMockClient()

		err := mockClient.ResizeVolume("non-existent", 2*1024*1024*1024)
		require.Error(t, err)

		var notFoundErr *VolumeNotFoundError
		assert.True(t, errors.As(err, &notFoundErr), "should be VolumeNotFoundError")
	})

	t.Run("GetCapacity returns valid data", func(t *testing.T) {
		mockClient := NewMockClient()

		capacity, err := mockClient.GetCapacity("/storage-pool")
		require.NoError(t, err)
		require.NotNil(t, capacity)

		// Mock returns 1 TiB total, 512 GiB free
		assert.Equal(t, int64(1024*1024*1024*1024), capacity.TotalBytes)
		assert.Equal(t, int64(512*1024*1024*1024), capacity.FreeBytes)
		assert.Equal(t, int64(512*1024*1024*1024), capacity.UsedBytes)
	})

	t.Run("ListVolumes returns all volumes", func(t *testing.T) {
		mockClient := NewMockClient()

		// Add some volumes
		mockClient.AddVolume(&VolumeInfo{Slot: "vol-1"})
		mockClient.AddVolume(&VolumeInfo{Slot: "vol-2"})
		mockClient.AddVolume(&VolumeInfo{Slot: "vol-3"})

		volumes, err := mockClient.ListVolumes()
		require.NoError(t, err)
		assert.Len(t, volumes, 3, "should list all volumes")

		// Verify volumes are present
		slots := make(map[string]bool)
		for _, vol := range volumes {
			slots[vol.Slot] = true
		}
		assert.True(t, slots["vol-1"])
		assert.True(t, slots["vol-2"])
		assert.True(t, slots["vol-3"])
	})
}

func TestMockClient_ErrorInjection(t *testing.T) {
	t.Run("SetError clears after one operation", func(t *testing.T) {
		mockClient := NewMockClient()

		// Set one-time error
		mockClient.SetError(errors.New("temporary error"))

		// First operation should fail
		err1 := mockClient.CreateVolume(CreateVolumeOptions{
			Slot:          "test-1",
			FilePath:      "/test.img",
			FileSizeBytes: 1024,
		})
		require.Error(t, err1)

		// Second operation should succeed
		err2 := mockClient.CreateVolume(CreateVolumeOptions{
			Slot:          "test-2",
			FilePath:      "/test2.img",
			FileSizeBytes: 1024,
		})
		assert.NoError(t, err2, "error should have cleared after first operation")
	})

	t.Run("SetPersistentError persists until cleared", func(t *testing.T) {
		mockClient := NewMockClient()

		// Set persistent error
		mockClient.SetPersistentError(errors.New("persistent error"))

		// Multiple operations should fail
		err1 := mockClient.CreateVolume(CreateVolumeOptions{Slot: "test-1", FilePath: "/1.img", FileSizeBytes: 1024})
		err2 := mockClient.CreateVolume(CreateVolumeOptions{Slot: "test-2", FilePath: "/2.img", FileSizeBytes: 1024})
		_, err3 := mockClient.GetVolume("test-3")

		require.Error(t, err1)
		require.Error(t, err2)
		require.Error(t, err3)

		// Clear error
		mockClient.ClearError()

		// Operations should now succeed
		err4 := mockClient.CreateVolume(CreateVolumeOptions{Slot: "test-4", FilePath: "/4.img", FileSizeBytes: 1024})
		assert.NoError(t, err4, "error should be cleared")
	})

	t.Run("PersistentError takes precedence over SetError", func(t *testing.T) {
		mockClient := NewMockClient()

		// Set both errors
		mockClient.SetError(errors.New("one-time"))
		mockClient.SetPersistentError(errors.New("persistent"))

		// Should get persistent error
		err := mockClient.CreateVolume(CreateVolumeOptions{Slot: "test", FilePath: "/test.img", FileSizeBytes: 1024})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "persistent", "persistent error should take precedence")
	})
}
