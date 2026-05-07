package rds

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type recordingMutationObserver struct {
	mutations []recordedMutation
	waits     []time.Duration
}

type recordedMutation struct {
	operation string
	err       error
	dryRun    bool
}

func (o *recordingMutationObserver) RecordRDSMutation(operation string, err error, dryRun bool, duration time.Duration) {
	o.mutations = append(o.mutations, recordedMutation{
		operation: operation,
		err:       err,
		dryRun:    dryRun,
	})
}

func (o *recordingMutationObserver) RecordRDSMutationRateLimitWait(operation string, duration time.Duration) {
	o.waits = append(o.waits, duration)
}

func TestGuardedClientDryRunDoesNotMutate(t *testing.T) {
	inner := NewMockClient()
	observer := &recordingMutationObserver{}
	client, err := NewGuardedClient(inner, GuardedClientConfig{
		DryRun:   true,
		Observer: observer,
	})
	require.NoError(t, err)

	err = client.CreateVolume(CreateVolumeOptions{
		Slot:          "pvc-11111111-1111-1111-1111-111111111111",
		FilePath:      "/storage-pool/metal-csi/pvc-11111111-1111-1111-1111-111111111111.img",
		FileSizeBytes: 1024,
		NVMETCPPort:   4420,
		NVMETCPNQN:    "nqn.2000-02.com.example:pvc-11111111-1111-1111-1111-111111111111",
	})
	require.NoError(t, err)

	_, err = inner.GetVolume("pvc-11111111-1111-1111-1111-111111111111")
	require.Error(t, err)
	require.Len(t, observer.mutations, 1)
	require.Equal(t, "create_volume", observer.mutations[0].operation)
	require.True(t, observer.mutations[0].dryRun)
}

func TestGuardedClientRecordsMutationFailure(t *testing.T) {
	inner := NewMockClient()
	observer := &recordingMutationObserver{}
	client, err := NewGuardedClient(inner, GuardedClientConfig{Observer: observer})
	require.NoError(t, err)

	opts := CreateVolumeOptions{
		Slot:          "pvc-22222222-2222-2222-2222-222222222222",
		FilePath:      "/storage-pool/metal-csi/pvc-22222222-2222-2222-2222-222222222222.img",
		FileSizeBytes: 1024,
		NVMETCPPort:   4420,
		NVMETCPNQN:    "nqn.2000-02.com.example:pvc-22222222-2222-2222-2222-222222222222",
	}
	require.NoError(t, client.CreateVolume(opts))
	err = client.CreateVolume(opts)
	require.Error(t, err)

	require.Len(t, observer.mutations, 2)
	require.NoError(t, observer.mutations[0].err)
	require.Error(t, observer.mutations[1].err)
}

func TestGuardedClientRateLimitsMutations(t *testing.T) {
	inner := NewMockClient()
	observer := &recordingMutationObserver{}
	client, err := NewGuardedClient(inner, GuardedClientConfig{
		MinInterval: 20 * time.Millisecond,
		Burst:       1,
		Timeout:     time.Second,
		Observer:    observer,
	})
	require.NoError(t, err)

	first := CreateVolumeOptions{
		Slot:          "pvc-33333333-3333-3333-3333-333333333333",
		FilePath:      "/storage-pool/metal-csi/pvc-33333333-3333-3333-3333-333333333333.img",
		FileSizeBytes: 1024,
		NVMETCPPort:   4420,
		NVMETCPNQN:    "nqn.2000-02.com.example:pvc-33333333-3333-3333-3333-333333333333",
	}
	second := first
	second.Slot = "pvc-44444444-4444-4444-4444-444444444444"
	second.FilePath = "/storage-pool/metal-csi/pvc-44444444-4444-4444-4444-444444444444.img"
	second.NVMETCPNQN = "nqn.2000-02.com.example:pvc-44444444-4444-4444-4444-444444444444"

	start := time.Now()
	require.NoError(t, client.CreateVolume(first))
	require.NoError(t, client.CreateVolume(second))
	elapsed := time.Since(start)

	require.GreaterOrEqual(t, elapsed, 15*time.Millisecond)
	require.Len(t, observer.waits, 2)
}
