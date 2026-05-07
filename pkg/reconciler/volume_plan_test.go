package reconciler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"git.srvlab.io/whiskey/rds-csi-driver/pkg/rds"
)

func TestBuildEnsureVolumePlan(t *testing.T) {
	desired := testDesiredVolume("pvc-11111111-1111-1111-1111-111111111111")

	t.Run("missing volume plans create", func(t *testing.T) {
		client := rds.NewMockClient()

		plan, err := BuildEnsureVolumePlan(client, desired)
		require.NoError(t, err)
		require.Equal(t, VolumeActionCreate, plan.Action)
		require.Equal(t, "volume missing", plan.Reason)
	})

	t.Run("matching volume plans noop", func(t *testing.T) {
		client := rds.NewMockClient()
		require.NoError(t, client.CreateVolume(desired))

		plan, err := BuildEnsureVolumePlan(client, desired)
		require.NoError(t, err)
		require.Equal(t, VolumeActionNoop, plan.Action)
	})

	t.Run("smaller volume plans resize", func(t *testing.T) {
		client := rds.NewMockClient()
		smaller := desired
		smaller.FileSizeBytes = desired.FileSizeBytes / 2
		require.NoError(t, client.CreateVolume(smaller))

		plan, err := BuildEnsureVolumePlan(client, desired)
		require.NoError(t, err)
		require.Equal(t, VolumeActionResize, plan.Action)
	})

	t.Run("shape mismatch plans conflict", func(t *testing.T) {
		client := rds.NewMockClient()
		existing := desired
		existing.NVMETCPNQN = "nqn.2000-02.com.example:other"
		require.NoError(t, client.CreateVolume(existing))

		plan, err := BuildEnsureVolumePlan(client, desired)
		require.NoError(t, err)
		require.Equal(t, VolumeActionConflict, plan.Action)
		require.Contains(t, plan.Reason, "NQN")
	})
}

func TestBuildDeleteVolumePlan(t *testing.T) {
	slot := "pvc-22222222-2222-2222-2222-222222222222"

	t.Run("missing volume is noop", func(t *testing.T) {
		client := rds.NewMockClient()

		plan, err := BuildDeleteVolumePlan(client, slot)
		require.NoError(t, err)
		require.Equal(t, VolumeActionNoop, plan.Action)
	})

	t.Run("existing volume plans delete", func(t *testing.T) {
		client := rds.NewMockClient()
		require.NoError(t, client.CreateVolume(testDesiredVolume(slot)))

		plan, err := BuildDeleteVolumePlan(client, slot)
		require.NoError(t, err)
		require.Equal(t, VolumeActionDelete, plan.Action)
	})
}

func TestApplyVolumePlanDryRunDoesNotMutate(t *testing.T) {
	client := rds.NewMockClient()
	desired := testDesiredVolume("pvc-33333333-3333-3333-3333-333333333333")

	plan, err := BuildEnsureVolumePlan(client, desired)
	require.NoError(t, err)
	require.Equal(t, VolumeActionCreate, plan.Action)

	result, err := ApplyVolumePlan(context.Background(), client, plan, VolumeApplyOptions{DryRun: true})
	require.NoError(t, err)
	require.False(t, result.Applied)
	require.True(t, result.DryRun)

	_, err = client.GetVolume(desired.Slot)
	require.Error(t, err)
}

func TestApplyVolumePlanMutatesOnlyWhenNeeded(t *testing.T) {
	client := rds.NewMockClient()
	desired := testDesiredVolume("pvc-44444444-4444-4444-4444-444444444444")

	plan, err := BuildEnsureVolumePlan(client, desired)
	require.NoError(t, err)

	result, err := ApplyVolumePlan(context.Background(), client, plan, VolumeApplyOptions{})
	require.NoError(t, err)
	require.True(t, result.Applied)

	plan, err = BuildEnsureVolumePlan(client, desired)
	require.NoError(t, err)
	require.Equal(t, VolumeActionNoop, plan.Action)

	result, err = ApplyVolumePlan(context.Background(), client, plan, VolumeApplyOptions{})
	require.NoError(t, err)
	require.False(t, result.Applied)
}

func TestApplyVolumePlanRefusesConflict(t *testing.T) {
	client := rds.NewMockClient()
	desired := testDesiredVolume("pvc-55555555-5555-5555-5555-555555555555")
	require.NoError(t, client.CreateVolume(desired))

	desired.FilePath = "/storage-pool/metal-csi/other.img"
	plan, err := BuildEnsureVolumePlan(client, desired)
	require.NoError(t, err)

	_, err = ApplyVolumePlan(context.Background(), client, plan, VolumeApplyOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing unsafe")
}

func testDesiredVolume(slot string) rds.CreateVolumeOptions {
	return rds.CreateVolumeOptions{
		Slot:          slot,
		FilePath:      "/storage-pool/metal-csi/" + slot + ".img",
		FileSizeBytes: 10 * 1024 * 1024 * 1024,
		NVMETCPPort:   4420,
		NVMETCPNQN:    "nqn.2000-02.com.example:" + slot,
	}
}
