package main

import (
	"bytes"
	"strings"
	"testing"

	storagebackend "gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/backend"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/utils"
)

func TestResolveLinuxZOLPlanNQN(t *testing.T) {
	volumeID := "pvc-11111111-1111-1111-1111-111111111111"

	got, err := resolveLinuxZOLPlanNQN(volumeID, "", utils.NQNPrefix)
	if err != nil {
		t.Fatalf("resolveLinuxZOLPlanNQN() error = %v", err)
	}
	want := utils.NQNPrefix + ":" + volumeID
	if got != want {
		t.Fatalf("resolveLinuxZOLPlanNQN() = %q, want %q", got, want)
	}

	exact := "nqn.2026-05.works.whiskey.nvmet-csi:pvc-explicit"
	got, err = resolveLinuxZOLPlanNQN(volumeID, exact, "nqn.2026-05.example.invalid")
	if err != nil {
		t.Fatalf("resolveLinuxZOLPlanNQN(exact) error = %v", err)
	}
	if got != exact {
		t.Fatalf("resolveLinuxZOLPlanNQN(exact) = %q, want %q", got, exact)
	}

	if _, err := resolveLinuxZOLPlanNQN(volumeID, "bad nqn", utils.NQNPrefix); err == nil {
		t.Fatal("expected invalid exact NQN to fail")
	}
}

func TestLinuxZOLPlanOptionsValidate(t *testing.T) {
	base := linuxZOLPlanOptions{
		Action:    "ensure",
		ZFSPool:   "tank/nvmet",
		Target:    "203.0.113.10",
		Port:      4420,
		VolumeID:  "pvc-11111111-1111-1111-1111-111111111111",
		SizeBytes: 1 << 30,
	}
	if err := base.validate(); err != nil {
		t.Fatalf("validate() error = %v", err)
	}

	deletePlan := base
	deletePlan.Action = "delete"
	deletePlan.SizeBytes = 0
	if err := deletePlan.validate(); err != nil {
		t.Fatalf("delete validate() error = %v", err)
	}

	missingSize := base
	missingSize.SizeBytes = 0
	if err := missingSize.validate(); err == nil || !strings.Contains(err.Error(), "size-bytes") {
		t.Fatalf("expected size validation error, got %v", err)
	}

	unsafeID := base
	unsafeID.VolumeID = "../not-a-volume"
	if err := unsafeID.validate(); err == nil || !strings.Contains(err.Error(), "volume-id") {
		t.Fatalf("expected volume ID validation error, got %v", err)
	}

	unknownAction := base
	unknownAction.Action = "repair"
	if err := unknownAction.validate(); err == nil || !strings.Contains(err.Error(), "ensure or delete") {
		t.Fatalf("expected action validation error, got %v", err)
	}
}

func TestRenderLinuxZOLPlan(t *testing.T) {
	plan := &storagebackend.VolumePlan{
		Backend:  storagebackend.TypeLinuxZOLNVMET,
		Action:   storagebackend.VolumeActionCreate,
		VolumeID: "pvc-11111111-1111-1111-1111-111111111111",
		Reason:   "zvol missing",
		Desired: &storagebackend.CreateVolumeRequest{
			Path:        "/dev/zvol/tank/nvmet/pvc-11111111-1111-1111-1111-111111111111",
			SizeBytes:   1 << 30,
			NVMETCPNQN:  "nqn.2026-05.works.whiskey.nvmet-csi:pvc-11111111-1111-1111-1111-111111111111",
			NVMETCPPort: 4420,
		},
		Commands: []string{
			"mkdir -p /sys/kernel/config/nvmet/ports/4420/subsystems",
			"zfs create -V 1073741824 tank/nvmet/pvc-11111111-1111-1111-1111-111111111111",
		},
	}

	var out bytes.Buffer
	if err := renderLinuxZOLPlan(&out, plan); err != nil {
		t.Fatalf("renderLinuxZOLPlan() error = %v", err)
	}
	body := out.String()

	for _, want := range []string{
		"backend\tlinux-zol-nvmet\n",
		"action\tcreate\n",
		"reason\tzvol missing\n",
		"desired_size_bytes\t1073741824\n",
		"command\tmkdir -p /sys/kernel/config/nvmet/ports/4420/subsystems\n",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered plan missing %q:\n%s", want, body)
		}
	}
}
