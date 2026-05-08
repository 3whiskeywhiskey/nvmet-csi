package driver

import (
	"context"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// VMIGrouper provides per-VMI operation serialization to prevent concurrent
// volume operations on the same VMI from causing conflicts in upstream
// kubevirt-csi-driver.
//
// When multiple volumes belonging to the same VMI are attached/detached
// concurrently, the upstream kubevirt-csi-driver can encounter race conditions
// that cause VM pause events. By serializing operations per-VMI at the
// nvmet-csi level, we reduce the likelihood of these conflicts.
type VMIGrouper struct {
	k8sClient kubernetes.Interface

	// Per-VMI locks: key is "namespace/vmi-name"
	mu    sync.Mutex
	locks map[string]*vmiLock

	// Cache: PVC -> VMI mapping with TTL
	cacheMu    sync.RWMutex
	cache      map[string]*vmiCacheEntry
	cacheTTL   time.Duration
	cacheClean time.Time

	// Metrics
	enabled bool
}

// vmiLock tracks a per-VMI mutex and reference count
type vmiLock struct {
	mu       sync.Mutex
	refCount int
}

// vmiCacheEntry caches the VMI lookup result for a PVC
type vmiCacheEntry struct {
	vmiKey    string // "namespace/vmi-name" or empty if not found
	expiresAt time.Time
	found     bool
}

// VMIGrouperConfig contains configuration for VMIGrouper
type VMIGrouperConfig struct {
	K8sClient kubernetes.Interface
	CacheTTL  time.Duration // How long to cache PVC->VMI mappings (default: 60s)
	Enabled   bool          // Whether VMI serialization is enabled
}

// NewVMIGrouper creates a new VMI grouper for operation serialization
func NewVMIGrouper(config VMIGrouperConfig) *VMIGrouper {
	if config.CacheTTL <= 0 {
		config.CacheTTL = 60 * time.Second
	}

	return &VMIGrouper{
		k8sClient: config.K8sClient,
		locks:     make(map[string]*vmiLock),
		cache:     make(map[string]*vmiCacheEntry),
		cacheTTL:  config.CacheTTL,
		enabled:   config.Enabled,
	}
}

// LockVMI acquires a lock for the VMI that owns the given PVC.
// Returns the VMI key and a function to release the lock.
// If VMI cannot be determined, returns empty string and a no-op unlock function.
func (g *VMIGrouper) LockVMI(ctx context.Context, pvcNamespace, pvcName string) (vmiKey string, unlock func()) {
	if !g.enabled || g.k8sClient == nil {
		return "", func() {}
	}

	// Resolve VMI for this PVC
	vmiKey = g.resolveVMIForPVC(ctx, pvcNamespace, pvcName)
	if vmiKey == "" {
		// Could not determine VMI - proceed without serialization
		klog.V(4).Infof("Could not determine VMI for PVC %s/%s, skipping serialization", pvcNamespace, pvcName)
		return "", func() {}
	}

	// Acquire per-VMI lock
	lock := g.getOrCreateLock(vmiKey)
	lock.mu.Lock()

	klog.V(4).Infof("Acquired VMI lock for %s (PVC: %s/%s)", vmiKey, pvcNamespace, pvcName)

	return vmiKey, func() {
		lock.mu.Unlock()
		g.releaseLock(vmiKey)
		klog.V(4).Infof("Released VMI lock for %s", vmiKey)
	}
}

// resolveVMIForPVC looks up which VMI owns a PVC by finding the pod that
// mounts it and checking the pod's ownerReferences.
func (g *VMIGrouper) resolveVMIForPVC(ctx context.Context, pvcNamespace, pvcName string) string {
	// Check cache first
	cacheKey := pvcNamespace + "/" + pvcName
	if entry := g.getCached(cacheKey); entry != nil {
		if entry.found {
			klog.V(4).Infof("VMI cache hit for PVC %s: %s", cacheKey, entry.vmiKey)
			return entry.vmiKey
		}
		klog.V(4).Infof("VMI cache hit (not found) for PVC %s", cacheKey)
		return ""
	}

	// Look up VMI via API
	vmiKey, found := g.lookupVMIFromAPI(ctx, pvcNamespace, pvcName)

	// Cache result
	g.setCache(cacheKey, vmiKey, found)

	return vmiKey
}

// lookupVMIFromAPI queries the Kubernetes API to find the VMI owning a PVC
func (g *VMIGrouper) lookupVMIFromAPI(ctx context.Context, pvcNamespace, pvcName string) (string, bool) {
	// Use a timeout for API calls
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// List pods in the namespace
	pods, err := g.k8sClient.CoreV1().Pods(pvcNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.V(4).Infof("Failed to list pods in namespace %s: %v", pvcNamespace, err)
		return "", false
	}

	// Find pod(s) mounting this PVC
	for _, pod := range pods.Items {
		if g.podMountsPVC(&pod, pvcName) {
			// Check ownerReferences for VMI
			vmiKey := g.extractVMIFromPod(&pod)
			if vmiKey != "" {
				klog.V(4).Infof("Found VMI %s for PVC %s/%s via pod %s",
					vmiKey, pvcNamespace, pvcName, pod.Name)
				return vmiKey, true
			}
		}
	}

	klog.V(4).Infof("No VMI found for PVC %s/%s", pvcNamespace, pvcName)
	return "", false
}

// podMountsPVC checks if a pod mounts the given PVC
func (g *VMIGrouper) podMountsPVC(pod *corev1.Pod, pvcName string) bool {
	for _, vol := range pod.Spec.Volumes {
		if vol.PersistentVolumeClaim != nil && vol.PersistentVolumeClaim.ClaimName == pvcName {
			return true
		}
	}
	return false
}

// extractVMIFromPod extracts the VMI identity from a pod's ownerReferences
func (g *VMIGrouper) extractVMIFromPod(pod *corev1.Pod) string {
	// Check ownerReferences for VirtualMachineInstance
	for _, ownerRef := range pod.OwnerReferences {
		if ownerRef.Kind == "VirtualMachineInstance" {
			return pod.Namespace + "/" + ownerRef.Name
		}
	}

	// Check for KubeVirt labels (fallback for hotplug pods)
	if vmiName, ok := pod.Labels["kubevirt.io/vmi"]; ok {
		return pod.Namespace + "/" + vmiName
	}
	if vmiName, ok := pod.Labels["vm.kubevirt.io/name"]; ok {
		return pod.Namespace + "/" + vmiName
	}

	// Check if this is a hotplug volume pod (hp-volume-*)
	// These typically have ownerReferences to the launcher pod
	for _, ownerRef := range pod.OwnerReferences {
		if ownerRef.Kind == "Pod" {
			// This might be a hotplug attachment pod - try to get the parent pod's VMI
			// We don't recurse here to avoid complexity; the cache will help
			klog.V(4).Infof("Pod %s owned by another pod %s, may be hotplug attachment",
				pod.Name, ownerRef.Name)
		}
	}

	return ""
}

// getOrCreateLock gets or creates a per-VMI lock
func (g *VMIGrouper) getOrCreateLock(vmiKey string) *vmiLock {
	g.mu.Lock()
	defer g.mu.Unlock()

	lock, exists := g.locks[vmiKey]
	if !exists {
		lock = &vmiLock{}
		g.locks[vmiKey] = lock
	}
	lock.refCount++
	return lock
}

// releaseLock decrements the reference count and cleans up if zero
func (g *VMIGrouper) releaseLock(vmiKey string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	lock, exists := g.locks[vmiKey]
	if !exists {
		return
	}
	lock.refCount--
	if lock.refCount <= 0 {
		delete(g.locks, vmiKey)
	}
}

// getCached returns a cached VMI lookup result, or nil if not cached/expired
func (g *VMIGrouper) getCached(pvcKey string) *vmiCacheEntry {
	g.cacheMu.RLock()
	defer g.cacheMu.RUnlock()

	entry, exists := g.cache[pvcKey]
	if !exists {
		return nil
	}
	if time.Now().After(entry.expiresAt) {
		return nil // Expired
	}
	return entry
}

// setCache stores a VMI lookup result in the cache
func (g *VMIGrouper) setCache(pvcKey, vmiKey string, found bool) {
	g.cacheMu.Lock()
	defer g.cacheMu.Unlock()

	// Periodic cleanup of expired entries
	now := time.Now()
	if now.Sub(g.cacheClean) > 5*time.Minute {
		g.cleanExpiredLocked(now)
		g.cacheClean = now
	}

	g.cache[pvcKey] = &vmiCacheEntry{
		vmiKey:    vmiKey,
		expiresAt: now.Add(g.cacheTTL),
		found:     found,
	}
}

// cleanExpiredLocked removes expired cache entries (caller must hold cacheMu)
func (g *VMIGrouper) cleanExpiredLocked(now time.Time) {
	for key, entry := range g.cache {
		if now.After(entry.expiresAt) {
			delete(g.cache, key)
		}
	}
}

// InvalidateCache removes a PVC from the cache (call on unpublish)
func (g *VMIGrouper) InvalidateCache(pvcNamespace, pvcName string) {
	g.cacheMu.Lock()
	defer g.cacheMu.Unlock()
	delete(g.cache, pvcNamespace+"/"+pvcName)
}

// IsEnabled returns whether VMI serialization is enabled
func (g *VMIGrouper) IsEnabled() bool {
	return g.enabled
}
