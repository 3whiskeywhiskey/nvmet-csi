package driver

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	storagebackend "gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/backend"
)

func newTestDriverWithK8sAndFactory(t *testing.T, k8s kubernetes.Interface, factory func(BackendSelection) (storagebackend.Backend, error)) *Driver {
	t.Helper()
	config := DriverConfig{
		EnableController: true,
		K8sClient:        k8s,
	}
	d, err := NewDriver(config)
	if err != nil {
		t.Fatalf("NewDriver() error = %v", err)
	}
	d.backendFactory = factory
	d.name = DriverName
	return d
}

func testBackendFactory(t *testing.T) func(BackendSelection) (storagebackend.Backend, error) {
	return func(selection BackendSelection) (storagebackend.Backend, error) {
		return &testBackend{
			name:    selection.Type,
			address: selection.TargetAddr,
		}, nil
	}
}

func TestListExportTargets_GroupsPVsBySelection(t *testing.T) {
	selA := map[string]string{
		"backend":       storagebackend.TypeLinuxZOLNVMET,
		"executor":      storagebackend.ExecutorLocal,
		"zfsPool":       "tank/a",
		"targetAddress": "203.0.113.10",
		"nvmePort":      "4420",
		"nqn":           "nqn.test:a1",
	}
	selB := map[string]string{
		"backend":       storagebackend.TypeLinuxZOLNVMET,
		"executor":      storagebackend.ExecutorLocal,
		"zfsPool":       "tank/b",
		"targetAddress": "203.0.113.10",
		"nvmePort":      "4421",
		"nqn":           "nqn.test:b1",
	}

	pv1 := testPersistentVolume("vol-a1", selA)
	pv1.Spec.CSI.VolumeHandle = "vol-a1"
	pv2 := testPersistentVolume("vol-a2", selA)
	pv2.Spec.CSI.VolumeHandle = "vol-a2"
	pv2.Name = "pv-vol-a2"
	pv3 := testPersistentVolume("vol-b1", selB)
	pv3.Spec.CSI.VolumeHandle = "vol-b1"

	k8s := fake.NewSimpleClientset(pv1, pv2, pv3)
	d := newTestDriverWithK8sAndFactory(t, k8s, testBackendFactory(t))

	targets, err := d.listExportTargets(context.Background())
	if err != nil {
		t.Fatalf("listExportTargets() error = %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d: %+v", len(targets), targets)
	}

	// deterministic sort by key
	if targets[0].Key != "linux-zol-nvmet/203.0.113.10/tank/a" || targets[1].Key != "linux-zol-nvmet/203.0.113.10/tank/b" {
		t.Fatalf("targets not sorted by key: got keys %q, %q", targets[0].Key, targets[1].Key)
	}

	// first target has two desired from same selection
	if len(targets[0].Desired) != 2 {
		t.Fatalf("expected 2 desired for group A, got %d", len(targets[0].Desired))
	}
	if targets[0].Desired[0].VolumeID != "vol-a1" || targets[0].Desired[0].NQN != "nqn.test:a1" || targets[0].Desired[0].Port != 4420 {
		t.Fatalf("bad desired[0]: %+v", targets[0].Desired[0])
	}
	if targets[0].Desired[1].VolumeID != "vol-a2" || targets[0].Desired[1].NQN != "nqn.test:a1" || targets[0].Desired[1].Port != 4420 {
		t.Fatalf("bad desired[1]: %+v", targets[0].Desired[1])
	}

	if len(targets[1].Desired) != 1 || targets[1].Desired[0].VolumeID != "vol-b1" || targets[1].Desired[0].Port != 4421 {
		t.Fatalf("bad target B: %+v", targets[1].Desired)
	}
}

func TestListExportTargets_FiltersForeignRouterosDeletedUnparseable(t *testing.T) {
	good := map[string]string{
		"backend":       storagebackend.TypeLinuxZOLNVMET,
		"executor":      storagebackend.ExecutorLocal,
		"zfsPool":       "tank/good",
		"targetAddress": "203.0.113.10",
		"nvmePort":      "4420",
		"nqn":           "nqn.test:good",
	}
	foreignDriver := map[string]string{
		"backend":       storagebackend.TypeLinuxZOLNVMET,
		"executor":      storagebackend.ExecutorLocal,
		"zfsPool":       "tank/foreign",
		"targetAddress": "203.0.113.10",
	}
	routeros := map[string]string{
		"backend": storagebackend.TypeRouterOSRDS,
	}
	unparseable := map[string]string{
		"backend":  storagebackend.TypeLinuxZOLNVMET,
		"executor": storagebackend.ExecutorLocal,
		// missing required zfsPool -> parse error, must skip not fail
		"targetAddress": "203.0.113.10",
	}

	pvGood := testPersistentVolume("vol-good", good)
	pvGood.Spec.CSI.VolumeHandle = "vol-good"

	pvForeign := testPersistentVolume("vol-foreign", foreignDriver)
	pvForeign.Spec.CSI.Driver = "other.csi.driver"
	pvForeign.Spec.CSI.VolumeHandle = "vol-foreign"

	pvRouter := testPersistentVolume("vol-router", routeros)
	pvRouter.Spec.CSI.VolumeHandle = "vol-router"

	pvDeleted := testPersistentVolume("vol-del", good)
	pvDeleted.Spec.CSI.VolumeHandle = "vol-del"
	now := metav1.Now()
	pvDeleted.DeletionTimestamp = &now

	pvUnparse := testPersistentVolume("vol-unparse", unparseable)
	pvUnparse.Spec.CSI.VolumeHandle = "vol-unparse"

	k8s := fake.NewSimpleClientset(pvGood, pvForeign, pvRouter, pvDeleted, pvUnparse)
	d := newTestDriverWithK8sAndFactory(t, k8s, testBackendFactory(t))

	targets, err := d.listExportTargets(context.Background())
	if err != nil {
		t.Fatalf("listExportTargets() should not fail on unparseable: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected only 1 target (the good one), got %d", len(targets))
	}
	if targets[0].Desired[0].VolumeID != "vol-good" {
		t.Fatalf("wrong volume survived filter: %+v", targets[0].Desired)
	}
}

func TestListExportTargets_MissingOrGarbagePortFallsToZero(t *testing.T) {
	attrsNoPort := map[string]string{
		"backend":       storagebackend.TypeLinuxZOLNVMET,
		"executor":      storagebackend.ExecutorLocal,
		"zfsPool":       "tank/p",
		"targetAddress": "203.0.113.10",
		"nqn":           "nqn.test:noport",
		// nvmePort absent -> 0
	}
	attrsGarbage := map[string]string{
		"backend":       storagebackend.TypeLinuxZOLNVMET,
		"executor":      storagebackend.ExecutorLocal,
		"zfsPool":       "tank/p",
		"targetAddress": "203.0.113.10",
		"nvmePort":      "not-a-number",
		"nqn":           "nqn.test:garbage",
	}

	pvNo := testPersistentVolume("vol-no", attrsNoPort)
	pvNo.Spec.CSI.VolumeHandle = "vol-no"
	pvGarb := testPersistentVolume("vol-garb", attrsGarbage)
	pvGarb.Spec.CSI.VolumeHandle = "vol-garb"

	k8s := fake.NewSimpleClientset(pvNo, pvGarb)
	d := newTestDriverWithK8sAndFactory(t, k8s, testBackendFactory(t))

	targets, err := d.listExportTargets(context.Background())
	if err != nil {
		t.Fatalf("listExportTargets() error = %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	des := targets[0].Desired
	if len(des) != 2 {
		t.Fatalf("expected 2 desired, got %d", len(des))
	}
	for i, dd := range des {
		if dd.Port != 0 {
			t.Fatalf("expected Port==0 for garbage/missing, got %d for %d: %+v", dd.Port, i, dd)
		}
	}
}
