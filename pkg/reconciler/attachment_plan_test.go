package reconciler

import (
	"testing"
	"time"

	"git.srvlab.io/whiskey/nvmet-csi/pkg/attachment"
)

func TestBuildPublishAttachmentPlan(t *testing.T) {
	liveNode := true
	deletedNode := false
	now := time.Now()
	migrationStartedAt := now.Add(-10 * time.Minute)

	tests := []struct {
		name  string
		input PublishAttachmentInput
		want  AttachmentAction
	}{
		{
			name: "missing attachment attaches primary",
			input: PublishAttachmentInput{
				VolumeID:   "pvc-test",
				NodeID:     "node-a",
				AccessMode: "RWO",
			},
			want: AttachmentActionAttach,
		},
		{
			name: "same node noops",
			input: PublishAttachmentInput{
				VolumeID: "pvc-test",
				NodeID:   "node-a",
				Existing: attachmentState("pvc-test", "RWO", "node-a"),
			},
			want: AttachmentActionNoop,
		},
		{
			name: "live blocking node conflicts",
			input: PublishAttachmentInput{
				VolumeID:           "pvc-test",
				NodeID:             "node-b",
				Existing:           attachmentState("pvc-test", "RWO", "node-a"),
				BlockingNodeExists: &liveNode,
			},
			want: AttachmentActionConflict,
		},
		{
			name: "deleted blocking node clears stale state",
			input: PublishAttachmentInput{
				VolumeID:           "pvc-test",
				NodeID:             "node-b",
				Existing:           attachmentState("pvc-test", "RWO", "node-a"),
				BlockingNodeExists: &deletedNode,
			},
			want: AttachmentActionClearStaleAttach,
		},
		{
			name: "grace period clears handoff state",
			input: PublishAttachmentInput{
				VolumeID:          "pvc-test",
				NodeID:            "node-b",
				Existing:          attachmentState("pvc-test", "RWO", "node-a"),
				WithinGracePeriod: true,
			},
			want: AttachmentActionClearGraceAttach,
		},
		{
			name: "RWX secondary attach",
			input: PublishAttachmentInput{
				VolumeID:       "pvc-test",
				NodeID:         "node-b",
				AccessMode:     "RWX",
				AllowMultiNode: true,
				Existing:       attachmentState("pvc-test", "RWX", "node-a"),
			},
			want: AttachmentActionAttachSecondary,
		},
		{
			name: "RWX third node conflicts",
			input: PublishAttachmentInput{
				VolumeID:       "pvc-test",
				NodeID:         "node-c",
				AccessMode:     "RWX",
				AllowMultiNode: true,
				Existing:       attachmentState("pvc-test", "RWX", "node-a", "node-b"),
			},
			want: AttachmentActionConflict,
		},
		{
			name: "timed out RWX migration conflicts",
			input: PublishAttachmentInput{
				VolumeID:       "pvc-test",
				NodeID:         "node-b",
				AccessMode:     "RWX",
				AllowMultiNode: true,
				Existing: &attachment.AttachmentState{
					VolumeID:           "pvc-test",
					NodeID:             "node-a",
					Nodes:              []attachment.NodeAttachment{{NodeID: "node-a", AttachedAt: now.Add(-10 * time.Minute)}},
					AccessMode:         "RWX",
					MigrationStartedAt: &migrationStartedAt,
					MigrationTimeout:   time.Minute,
				},
			},
			want: AttachmentActionConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := BuildPublishAttachmentPlan(tt.input)
			if err != nil {
				t.Fatalf("BuildPublishAttachmentPlan returned error: %v", err)
			}
			if plan.Action != tt.want {
				t.Fatalf("action = %s, want %s (reason=%q)", plan.Action, tt.want, plan.Reason)
			}
		})
	}
}

func TestBuildUnpublishAttachmentPlan(t *testing.T) {
	tests := []struct {
		name  string
		input UnpublishAttachmentInput
		want  AttachmentAction
	}{
		{
			name:  "missing attachment noops",
			input: UnpublishAttachmentInput{VolumeID: "pvc-test", NodeID: "node-a"},
			want:  AttachmentActionNoop,
		},
		{
			name: "empty node ID detaches all",
			input: UnpublishAttachmentInput{
				VolumeID: "pvc-test",
				Existing: attachmentState("pvc-test", "RWO", "node-a"),
			},
			want: AttachmentActionDetachAll,
		},
		{
			name: "unattached requested node noops",
			input: UnpublishAttachmentInput{
				VolumeID: "pvc-test",
				NodeID:   "node-b",
				Existing: attachmentState("pvc-test", "RWO", "node-a"),
			},
			want: AttachmentActionNoop,
		},
		{
			name: "single node detaches",
			input: UnpublishAttachmentInput{
				VolumeID: "pvc-test",
				NodeID:   "node-a",
				Existing: attachmentState("pvc-test", "RWO", "node-a"),
			},
			want: AttachmentActionDetach,
		},
		{
			name: "multi-node detaches requested node",
			input: UnpublishAttachmentInput{
				VolumeID: "pvc-test",
				NodeID:   "node-a",
				Existing: attachmentState("pvc-test", "RWX", "node-a", "node-b"),
			},
			want: AttachmentActionDetachNode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := BuildUnpublishAttachmentPlan(tt.input)
			if err != nil {
				t.Fatalf("BuildUnpublishAttachmentPlan returned error: %v", err)
			}
			if plan.Action != tt.want {
				t.Fatalf("action = %s, want %s (reason=%q)", plan.Action, tt.want, plan.Reason)
			}
		})
	}
}

func attachmentState(volumeID, accessMode string, nodes ...string) *attachment.AttachmentState {
	state := &attachment.AttachmentState{
		VolumeID:   volumeID,
		AccessMode: accessMode,
		AttachedAt: time.Now(),
	}
	for i, nodeID := range nodes {
		if i == 0 {
			state.NodeID = nodeID
		}
		state.Nodes = append(state.Nodes, attachment.NodeAttachment{
			NodeID:     nodeID,
			AttachedAt: time.Now(),
		})
	}
	return state
}
