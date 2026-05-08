// Package attachment provides thread-safe tracking of volume-to-node attachments
// for the nvmet-csi driver.
package attachment

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"git.srvlab.io/whiskey/nvmet-csi/pkg/observability"
)

// NodeWatcher integrates with Kubernetes node informers to trigger attachment
// reconciliation when nodes become NotReady or are deleted.
type NodeWatcher struct {
	reconciler *AttachmentReconciler
	metrics    *observability.Metrics // Optional, may be nil
}

// NewNodeWatcher creates a new NodeWatcher that triggers reconciliation on node events.
func NewNodeWatcher(reconciler *AttachmentReconciler, metrics *observability.Metrics) *NodeWatcher {
	return &NodeWatcher{
		reconciler: reconciler,
		metrics:    metrics,
	}
}

// GetEventHandlers returns ResourceEventHandlerFuncs for node informer integration.
// Register these handlers with a node informer to enable event-driven reconciliation.
func (nw *NodeWatcher) GetEventHandlers() cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			// No action needed for new nodes - they don't create stale attachments
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldNode, ok := oldObj.(*corev1.Node)
			if !ok {
				klog.Warningf("NodeWatcher.UpdateFunc: expected *corev1.Node, got %T", oldObj)
				return
			}
			newNode, ok := newObj.(*corev1.Node)
			if !ok {
				klog.Warningf("NodeWatcher.UpdateFunc: expected *corev1.Node, got %T", newObj)
				return
			}

			// Check if node transitioned from Ready to NotReady
			oldReady := isNodeReady(oldNode)
			newReady := isNodeReady(newNode)

			if oldReady && !newReady {
				// Node became NotReady - trigger immediate reconciliation
				klog.Infof("Node %s became NotReady, triggering attachment reconciliation", newNode.Name)
				nw.reconciler.TriggerReconcile()

				// Record metric if available
				if nw.metrics != nil {
					nw.metrics.RecordReconcileAction("node_watcher_trigger")
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			// Handle tombstone objects properly (cache.DeletedFinalStateUnknown)
			node, ok := obj.(*corev1.Node)
			if !ok {
				// Try to extract from tombstone
				tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
				if !ok {
					klog.Warningf("NodeWatcher.DeleteFunc: expected *corev1.Node or cache.DeletedFinalStateUnknown, got %T", obj)
					return
				}
				node, ok = tombstone.Obj.(*corev1.Node)
				if !ok {
					klog.Warningf("NodeWatcher.DeleteFunc: tombstone contained unexpected type %T", tombstone.Obj)
					return
				}
			}

			// Node deleted - trigger immediate reconciliation
			klog.Infof("Node %s deleted, triggering attachment reconciliation", node.Name)
			nw.reconciler.TriggerReconcile()

			// Record metric if available
			if nw.metrics != nil {
				nw.metrics.RecordReconcileAction("node_watcher_trigger")
			}
		},
	}
}

// isNodeReady checks if a node has NodeReady condition with status True.
func isNodeReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}
