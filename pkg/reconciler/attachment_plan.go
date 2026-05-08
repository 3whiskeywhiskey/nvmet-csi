package reconciler

import (
	"fmt"
	"time"

	"git.srvlab.io/whiskey/nvmet-csi/pkg/attachment"
)

// AttachmentAction is the smallest safe state change needed for an attachment.
type AttachmentAction string

const (
	AttachmentActionNoop             AttachmentAction = "noop"
	AttachmentActionAttach           AttachmentAction = "attach"
	AttachmentActionAttachSecondary  AttachmentAction = "attach_secondary"
	AttachmentActionClearGraceAttach AttachmentAction = "clear_grace_attach"
	AttachmentActionClearStaleAttach AttachmentAction = "clear_stale_attach"
	AttachmentActionDetach           AttachmentAction = "detach"
	AttachmentActionDetachNode       AttachmentAction = "detach_node"
	AttachmentActionDetachAll        AttachmentAction = "detach_all"
	AttachmentActionConflict         AttachmentAction = "conflict"
)

// PublishAttachmentInput is the observed and desired state for a publish request.
type PublishAttachmentInput struct {
	VolumeID           string
	NodeID             string
	AccessMode         string
	AllowMultiNode     bool
	WithinGracePeriod  bool
	BlockingNodeExists *bool
	Existing           *attachment.AttachmentState
}

// UnpublishAttachmentInput is the observed and desired state for an unpublish request.
type UnpublishAttachmentInput struct {
	VolumeID string
	NodeID   string
	Existing *attachment.AttachmentState
}

// AttachmentPlan describes the observed state and the conservative action to apply.
type AttachmentPlan struct {
	Action         AttachmentAction
	VolumeID       string
	NodeID         string
	AccessMode     string
	Reason         string
	BlockingNodeID string
	Existing       *attachment.AttachmentState
}

// BuildPublishAttachmentPlan observes attachment state and decides how to publish
// without flapping backing exports or deleting state blindly.
func BuildPublishAttachmentPlan(input PublishAttachmentInput) (*AttachmentPlan, error) {
	if input.VolumeID == "" {
		return nil, fmt.Errorf("volume ID is required")
	}
	if input.NodeID == "" {
		return nil, fmt.Errorf("node ID is required")
	}

	if input.Existing == nil {
		return &AttachmentPlan{
			Action:     AttachmentActionAttach,
			VolumeID:   input.VolumeID,
			NodeID:     input.NodeID,
			AccessMode: input.AccessMode,
			Reason:     "volume has no tracked attachment",
		}, nil
	}

	if input.Existing.IsAttachedToNode(input.NodeID) {
		return &AttachmentPlan{
			Action:     AttachmentActionNoop,
			VolumeID:   input.VolumeID,
			NodeID:     input.NodeID,
			AccessMode: input.AccessMode,
			Reason:     "volume already attached to requested node",
			Existing:   input.Existing,
		}, nil
	}

	if input.AllowMultiNode {
		return buildMultiNodePublishPlan(input), nil
	}

	if input.WithinGracePeriod {
		return &AttachmentPlan{
			Action:         AttachmentActionClearGraceAttach,
			VolumeID:       input.VolumeID,
			NodeID:         input.NodeID,
			AccessMode:     input.AccessMode,
			Reason:         "blocking attachment is within handoff grace period",
			BlockingNodeID: input.Existing.NodeID,
			Existing:       input.Existing,
		}, nil
	}

	if input.BlockingNodeExists == nil {
		return conflictAttachmentPlan(input, "blocking node existence is unknown"), nil
	}
	if !*input.BlockingNodeExists {
		return &AttachmentPlan{
			Action:         AttachmentActionClearStaleAttach,
			VolumeID:       input.VolumeID,
			NodeID:         input.NodeID,
			AccessMode:     input.AccessMode,
			Reason:         "blocking node no longer exists",
			BlockingNodeID: input.Existing.NodeID,
			Existing:       input.Existing,
		}, nil
	}

	return conflictAttachmentPlan(input, "volume already attached to a live node"), nil
}

// BuildUnpublishAttachmentPlan observes attachment state and decides how to
// unpublish idempotently. Empty node IDs are treated as explicit force-detach.
func BuildUnpublishAttachmentPlan(input UnpublishAttachmentInput) (*AttachmentPlan, error) {
	if input.VolumeID == "" {
		return nil, fmt.Errorf("volume ID is required")
	}
	if input.Existing == nil {
		return &AttachmentPlan{
			Action:   AttachmentActionNoop,
			VolumeID: input.VolumeID,
			NodeID:   input.NodeID,
			Reason:   "volume has no tracked attachment",
		}, nil
	}
	if input.NodeID == "" {
		return &AttachmentPlan{
			Action:   AttachmentActionDetachAll,
			VolumeID: input.VolumeID,
			Reason:   "empty node ID requests force-detach",
			Existing: input.Existing,
		}, nil
	}
	if !input.Existing.IsAttachedToNode(input.NodeID) {
		return &AttachmentPlan{
			Action:   AttachmentActionNoop,
			VolumeID: input.VolumeID,
			NodeID:   input.NodeID,
			Reason:   "volume is not attached to requested node",
			Existing: input.Existing,
		}, nil
	}
	if input.Existing.NodeCount() > 1 {
		return &AttachmentPlan{
			Action:   AttachmentActionDetachNode,
			VolumeID: input.VolumeID,
			NodeID:   input.NodeID,
			Reason:   "volume has multiple node attachments",
			Existing: input.Existing,
		}, nil
	}
	return &AttachmentPlan{
		Action:   AttachmentActionDetach,
		VolumeID: input.VolumeID,
		NodeID:   input.NodeID,
		Reason:   "volume attached to requested node",
		Existing: input.Existing,
	}, nil
}

func buildMultiNodePublishPlan(input PublishAttachmentInput) *AttachmentPlan {
	if input.Existing.NodeCount() == 0 {
		return conflictAttachmentPlan(input, "attachment state has no nodes")
	}
	if input.Existing.NodeCount() >= 2 {
		return conflictAttachmentPlan(input, "volume already attached to 2 nodes")
	}
	if input.Existing.IsMigrationTimedOut() {
		elapsed := time.Since(*input.Existing.MigrationStartedAt).Round(time.Second)
		return conflictAttachmentPlan(input, fmt.Sprintf("migration timeout exceeded (%v elapsed, %v max)", elapsed, input.Existing.MigrationTimeout))
	}
	return &AttachmentPlan{
		Action:         AttachmentActionAttachSecondary,
		VolumeID:       input.VolumeID,
		NodeID:         input.NodeID,
		AccessMode:     input.AccessMode,
		Reason:         "multi-node access allows secondary attachment",
		BlockingNodeID: input.Existing.NodeID,
		Existing:       input.Existing,
	}
}

func conflictAttachmentPlan(input PublishAttachmentInput, reason string) *AttachmentPlan {
	blockingNodeID := ""
	if input.Existing != nil {
		blockingNodeID = input.Existing.NodeID
	}
	return &AttachmentPlan{
		Action:         AttachmentActionConflict,
		VolumeID:       input.VolumeID,
		NodeID:         input.NodeID,
		AccessMode:     input.AccessMode,
		Reason:         reason,
		BlockingNodeID: blockingNodeID,
		Existing:       input.Existing,
	}
}
