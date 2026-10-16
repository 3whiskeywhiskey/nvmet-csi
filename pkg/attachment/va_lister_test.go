package attachment

import (
	"context"
	"testing"

	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

// createTestVolumeAttachment creates a test VolumeAttachment with the given parameters.
func createTestVolumeAttachment(name, attacher, volumeID, nodeID string, attached bool) *storagev1.VolumeAttachment {
	va := &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: storagev1.VolumeAttachmentSpec{
			Attacher: attacher,
			NodeName: nodeID,
			Source: storagev1.VolumeAttachmentSource{
				PersistentVolumeName: &volumeID,
			},
		},
		Status: storagev1.VolumeAttachmentStatus{
			Attached: attached,
		},
	}
	return va
}

func TestListDriverVolumeAttachments(t *testing.T) {
	tests := []struct {
		name             string
		existingVAs      []*storagev1.VolumeAttachment
		expectedCount    int
		expectEmptySlice bool
	}{
		{
			name: "filters by driver name - returns only our driver",
			existingVAs: []*storagev1.VolumeAttachment{
				createTestVolumeAttachment("va1", "nvmet.csi.whiskey.works", "pvc-vol1", "node1", true),
				createTestVolumeAttachment("va2", "nvmet.csi.whiskey.works", "pvc-vol2", "node2", true),
				createTestVolumeAttachment("va3", "ebs.csi.aws.com", "pvc-vol3", "node3", true),
			},
			expectedCount: 2,
		},
		{
			name: "empty result returns empty slice not nil",
			existingVAs: []*storagev1.VolumeAttachment{
				createTestVolumeAttachment("va1", "ebs.csi.aws.com", "pvc-vol1", "node1", true),
			},
			expectedCount:    0,
			expectEmptySlice: true,
		},
		{
			name:             "no VAs returns empty slice not nil",
			existingVAs:      []*storagev1.VolumeAttachment{},
			expectedCount:    0,
			expectEmptySlice: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake clientset with test VAs
			objs := make([]runtime.Object, len(tt.existingVAs))
			for i := range tt.existingVAs {
				objs[i] = tt.existingVAs[i]
			}
			client := fake.NewSimpleClientset(objs...)

			// List VolumeAttachments
			result, err := ListDriverVolumeAttachments(context.Background(), client, "nvmet.csi.whiskey.works")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Verify count
			if len(result) != tt.expectedCount {
				t.Errorf("expected %d VAs, got %d", tt.expectedCount, len(result))
			}

			// Verify empty slice behavior
			if tt.expectEmptySlice && result == nil {
				t.Errorf("expected empty slice, got nil")
			}

			// Verify all returned VAs are for our driver
			for _, va := range result {
				if va.Spec.Attacher != "nvmet.csi.whiskey.works" {
					t.Errorf("expected driver nvmet.csi.whiskey.works, got %s", va.Spec.Attacher)
				}
			}
		})
	}
}

func TestFilterAttachedVolumeAttachments(t *testing.T) {
	tests := []struct {
		name          string
		input         []*storagev1.VolumeAttachment
		expectedCount int
	}{
		{
			name: "filters only attached volumes",
			input: []*storagev1.VolumeAttachment{
				createTestVolumeAttachment("va1", "nvmet.csi.whiskey.works", "pvc-vol1", "node1", true),
				createTestVolumeAttachment("va2", "nvmet.csi.whiskey.works", "pvc-vol2", "node2", false),
				createTestVolumeAttachment("va3", "nvmet.csi.whiskey.works", "pvc-vol3", "node3", true),
			},
			expectedCount: 2,
		},
		{
			name: "all detached returns empty slice",
			input: []*storagev1.VolumeAttachment{
				createTestVolumeAttachment("va1", "nvmet.csi.whiskey.works", "pvc-vol1", "node1", false),
				createTestVolumeAttachment("va2", "nvmet.csi.whiskey.works", "pvc-vol2", "node2", false),
			},
			expectedCount: 0,
		},
		{
			name:          "empty input returns empty slice",
			input:         []*storagev1.VolumeAttachment{},
			expectedCount: 0,
		},
		{
			name:          "nil input returns empty slice",
			input:         nil,
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FilterAttachedVolumeAttachments(tt.input)

			// Verify count
			if len(result) != tt.expectedCount {
				t.Errorf("expected %d attached VAs, got %d", tt.expectedCount, len(result))
			}

			// Verify empty slice behavior (not nil)
			if result == nil {
				t.Errorf("expected empty slice, got nil")
			}

			// Verify all returned VAs are attached
			for _, va := range result {
				if !va.Status.Attached {
					t.Errorf("expected attached=true, got attached=false for VA %s", va.Name)
				}
			}
		})
	}
}

func TestGroupVolumeAttachmentsByVolume(t *testing.T) {
	vol1 := "pvc-vol1"
	vol2 := "pvc-vol2"
	var nilVol *string = nil

	tests := []struct {
		name           string
		input          []*storagev1.VolumeAttachment
		expectedGroups map[string]int // volumeID -> count
		expectWarning  bool
	}{
		{
			name: "groups by volume ID - multiple nodes for same volume",
			input: []*storagev1.VolumeAttachment{
				createTestVolumeAttachment("va1", "nvmet.csi.whiskey.works", vol1, "node1", true),
				createTestVolumeAttachment("va2", "nvmet.csi.whiskey.works", vol1, "node2", true), // migration
				createTestVolumeAttachment("va3", "nvmet.csi.whiskey.works", vol2, "node3", true),
			},
			expectedGroups: map[string]int{
				vol1: 2,
				vol2: 1,
			},
		},
		{
			name: "skips VAs with nil PersistentVolumeName",
			input: []*storagev1.VolumeAttachment{
				createTestVolumeAttachment("va1", "nvmet.csi.whiskey.works", vol1, "node1", true),
				{
					ObjectMeta: metav1.ObjectMeta{Name: "va-nil"},
					Spec: storagev1.VolumeAttachmentSpec{
						Attacher: "nvmet.csi.whiskey.works",
						NodeName: "node2",
						Source: storagev1.VolumeAttachmentSource{
							PersistentVolumeName: nilVol, // nil pointer
						},
					},
					Status: storagev1.VolumeAttachmentStatus{Attached: true},
				},
			},
			expectedGroups: map[string]int{
				vol1: 1,
			},
			expectWarning: true,
		},
		{
			name: "skips VAs with empty PersistentVolumeName",
			input: []*storagev1.VolumeAttachment{
				createTestVolumeAttachment("va1", "nvmet.csi.whiskey.works", vol1, "node1", true),
				createTestVolumeAttachment("va2", "nvmet.csi.whiskey.works", "", "node2", true), // empty string
			},
			expectedGroups: map[string]int{
				vol1: 1,
			},
			expectWarning: true,
		},
		{
			name:           "empty input returns empty map",
			input:          []*storagev1.VolumeAttachment{},
			expectedGroups: map[string]int{},
		},
		{
			name:           "nil input returns empty map",
			input:          nil,
			expectedGroups: map[string]int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GroupVolumeAttachmentsByVolume(tt.input)

			// Verify not nil
			if result == nil {
				t.Errorf("expected empty map, got nil")
			}

			// Verify group counts
			if len(result) != len(tt.expectedGroups) {
				t.Errorf("expected %d groups, got %d", len(tt.expectedGroups), len(result))
			}

			for volumeID, expectedCount := range tt.expectedGroups {
				vas, exists := result[volumeID]
				if !exists {
					t.Errorf("expected group for volume %s, not found", volumeID)
					continue
				}
				if len(vas) != expectedCount {
					t.Errorf("volume %s: expected %d VAs, got %d", volumeID, expectedCount, len(vas))
				}
			}

			// Verify no unexpected groups
			for volumeID := range result {
				if _, expected := tt.expectedGroups[volumeID]; !expected {
					t.Errorf("unexpected group for volume %s", volumeID)
				}
			}
		})
	}
}

// TestListDriverVolumeAttachments_DriverNameIsolation is a regression test for
// configurable CSI driver-name isolation. It exercises the desired explicit API
// signature ListDriverVolumeAttachments(ctx, client, driverName) with two
// attached VAs for different drivers. It asserts that the lister returns *only*
// the VAs for the supplied driverName, never cross-contaminating production
// "nvmet.csi.whiskey.works" state when a custom driver name is configured.
func TestListDriverVolumeAttachments_DriverNameIsolation(t *testing.T) {
	prodDriver := "nvmet.csi.whiskey.works"
	testDriver := "example-rds.invalid"

	prodVol := "pvc-prod"
	testVol := "pvc-test"

	// Two attached VAs, different drivers (attacher field)
	vaProd := createTestVolumeAttachment("va-prod", prodDriver, prodVol, "node-prod", true)
	vaTest := createTestVolumeAttachment("va-test", testDriver, testVol, "node-test", true)

	client := fake.NewSimpleClientset(vaProd, vaTest)

	// Call the DESIRED explicit API passing driverName (will not compile until
	// production ListDriverVolumeAttachments accepts the third parameter).
	result, err := ListDriverVolumeAttachments(context.Background(), client, testDriver)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Must return exactly the one VA for the configured driver.
	if len(result) != 1 {
		t.Fatalf("expected 1 VA for test driver, got %d", len(result))
	}

	if result[0].Spec.Attacher != testDriver {
		t.Errorf("expected attacher %s, got %s", testDriver, result[0].Spec.Attacher)
	}

	// Explicitly verify no cross-driver contamination.
	for _, va := range result {
		if va.Spec.Attacher == prodDriver {
			t.Errorf("cross-driver contamination: production driver VA leaked into test-driver list")
		}
		if *va.Spec.Source.PersistentVolumeName == prodVol {
			t.Errorf("cross-driver contamination: production volume %s appeared for test driver", prodVol)
		}
	}

	// Also sanity-check that calling with prod driver would see the other one
	// (proves the filter is using the passed driverName, not a global).
	resultProd, err := ListDriverVolumeAttachments(context.Background(), client, prodDriver)
	if err != nil {
		t.Fatalf("unexpected error listing prod driver: %v", err)
	}
	if len(resultProd) != 1 {
		t.Fatalf("expected 1 VA for prod driver, got %d", len(resultProd))
	}
	if resultProd[0].Spec.Attacher != prodDriver {
		t.Errorf("prod list: expected attacher %s, got %s", prodDriver, resultProd[0].Spec.Attacher)
	}
}
