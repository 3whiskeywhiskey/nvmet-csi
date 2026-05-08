# Kubernetes Setup Guide

This guide explains how to deploy and use the nvmet-csi driver in a Kubernetes cluster.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Detailed Installation](#detailed-installation)
- [Configuration](#configuration)
- [Usage Examples](#usage-examples)
- [Verification](#verification)
- [Troubleshooting](#troubleshooting)
- [Uninstallation](#uninstallation)

## Prerequisites

### Cluster Requirements

- **Kubernetes**: v1.20 or later
- **CSI Spec**: v1.5.0 or later
- **Architecture**: amd64 or arm64

### Node Requirements

- **Linux Kernel**: 5.0+ with `nvme-tcp` kernel module
- **Required Tools**:
  - `nvme-cli` - NVMe command line tools
  - `mkfs.ext4`, `mkfs.xfs` - Filesystem utilities
  - `mount`, `umount` - Mount utilities
  - `findmnt`, `blkid`, `df` - Filesystem inspection tools

### RDS Requirements

- **MikroTik RouterOS**: v7.0 or later
- **SSH Access**: Configured user with disk management permissions
- **Network**: Connectivity from all Kubernetes nodes to RDS NVMe/TCP endpoint

### Verify Node Prerequisites

Run these commands on each Kubernetes node:

```bash
# Check kernel version
uname -r  # Should be 5.0 or higher

# Check nvme-tcp module
lsmod | grep nvme_tcp
# If not loaded, load it:
sudo modprobe nvme-tcp

# Verify nvme-cli is installed
nvme version

# Verify filesystem tools
which mkfs.ext4 mkfs.xfs blkid findmnt
```

## Quick Start

For a quick deployment using default settings:

```bash
# Clone the repository
git clone https://git.srvlab.io/whiskey/nvmet-csi.git
cd nvmet-csi

# Edit RDS credentials and address
vi deploy/kubernetes/controller.yaml
# Update the Secret and ConfigMap sections

# Deploy the driver
kubectl apply -f deploy/kubernetes/rbac.yaml
kubectl apply -f deploy/kubernetes/csidriver.yaml
kubectl apply -f deploy/kubernetes/controller.yaml
kubectl apply -f deploy/kubernetes/node.yaml

# Create StorageClass
kubectl apply -f examples/storageclass.yaml

# Verify deployment
kubectl get pods -n kube-system -l app=nvmet-csi-controller
kubectl get pods -n kube-system -l app=nvmet-csi-node
kubectl get csidrivers
kubectl get storageclass rds-nvme
```

## Detailed Installation

### Step 1: Prepare RDS SSH Credentials

Generate an SSH key pair for the CSI driver (if you haven't already):

```bash
# Generate ed25519 key (recommended)
ssh-keygen -t ed25519 -f nvmet-csi-key -C "nvmet-csi"

# Copy public key to RDS
# Log in to RouterOS and add the public key:
# /user ssh-keys import public-key-file=nvmet-csi-key.pub user=metal-csi
```

### Step 1.5: Get RDS SSH Host Key (IMPORTANT for Security)

**SECURITY:** The CSI driver requires the RDS SSH host key for verification to prevent man-in-the-middle attacks.

```bash
# Get the RDS host public key (replace 10.42.241.3 with your RDS IP)
ssh-keyscan -t ed25519 10.42.241.3 2>/dev/null | cut -d' ' -f2- > rds-host-key.pub

# Verify the fingerprint matches your RDS server
# On your RDS (RouterOS), run:
#   /system ssh-key print
# Or generate fingerprint from the saved key:
ssh-keygen -lf rds-host-key.pub

# The fingerprint should match what you see when you first SSH to the RDS:
ssh 10.42.241.3
# It will show: "ED25519 key fingerprint is SHA256:xxxx..."
```

**Alternative method** - Extract from known_hosts:

```bash
# If you've already connected to the RDS, get the key from known_hosts
ssh-keygen -H -F 10.42.241.3 | grep "^[^#]" | cut -d' ' -f2- > rds-host-key.pub
```

**IMPORTANT:** Verify the host key fingerprint before deployment to ensure you have the correct key!

### Step 2: Configure RDS User

On your MikroTik RouterOS device:

```routeros
# Create dedicated user for CSI driver
/user add name=metal-csi group=full

# Import SSH public key
/user ssh-keys import public-key-file=nvmet-csi-key.pub user=metal-csi

# Create base directory for volumes
/file add type=directory name=storage-pool/metal-csi/volumes
```

### Step 3: Update Kubernetes Manifests

Edit `deploy/kubernetes/controller.yaml`:

```yaml
# Update Secret with your private key AND host key
apiVersion: v1
kind: Secret
metadata:
  name: nvmet-csi-secret
stringData:
  rds-private-key: |
    -----BEGIN OPENSSH PRIVATE KEY-----
    # Paste your private key here (from nvmet-csi-key)
    -----END OPENSSH PRIVATE KEY-----
  rds-host-key: |
    # Paste your RDS host public key here (from rds-host-key.pub)
    ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIxxx...

---
# Update ConfigMap with your RDS settings
apiVersion: v1
kind: ConfigMap
metadata:
  name: nvmet-csi-config
data:
  rds-address: "10.42.241.3"  # Your RDS IP
  rds-port: "22"
  rds-user: "metal-csi"
  rds-volume-base-path: "/storage-pool/metal-csi/volumes"
```

**SECURITY NOTE:** If you need to skip host key verification for testing (NOT RECOMMENDED), you can add this arg to the controller container:
```yaml
- "-rds-insecure-skip-verify=true"
```
**WARNING:** This is INSECURE and should NEVER be used in production! Always use proper host key verification.

### Step 4: Deploy RBAC

```bash
kubectl apply -f deploy/kubernetes/rbac.yaml
```

This creates:
- ServiceAccounts: `nvmet-csi-controller`, `nvmet-csi-node`
- ClusterRoles: `nvmet-csi-controller-role`, `nvmet-csi-node-role`
- ClusterRoleBindings

### Step 5: Register CSI Driver

```bash
kubectl apply -f deploy/kubernetes/csidriver.yaml
```

This registers the `nvmet.csi.whiskey.works` driver with Kubernetes.

### Step 6: Deploy Controller

```bash
kubectl apply -f deploy/kubernetes/controller.yaml
```

This deploys:
- Controller Deployment (1 replica)
- CSI sidecars (provisioner, resizer, liveness-probe)

Verify:

```bash
kubectl get pods -n kube-system -l app=nvmet-csi-controller
kubectl logs -n kube-system -l app=nvmet-csi-controller -c nvmet-csi-plugin
```

### Step 7: Deploy Node DaemonSet

```bash
kubectl apply -f deploy/kubernetes/node.yaml
```

This deploys:
- Node DaemonSet (runs on all nodes)
- CSI sidecars (node-driver-registrar, liveness-probe)

Verify:

```bash
kubectl get pods -n kube-system -l app=nvmet-csi-node
kubectl get csinodes  # Should show all nodes with nvmet.csi.whiskey.works driver
```

### Step 8: Create StorageClass

```bash
kubectl apply -f examples/storageclass.yaml
```

Optionally, make it the default StorageClass:

```bash
kubectl patch storageclass rds-nvme -p '{"metadata": {"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}'
```

## Configuration

### StorageClass Parameters

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: rds-nvme
provisioner: nvmet.csi.whiskey.works
parameters:
  # Filesystem type
  csi.storage.k8s.io/fstype: ext4  # ext4, ext3, or xfs

  # NVMe/TCP port
  nvmePort: "4420"

volumeBindingMode: Immediate  # or WaitForFirstConsumer
reclaimPolicy: Delete  # or Retain
allowVolumeExpansion: false  # Volume expansion not yet implemented
```

### Advanced Configuration

#### Use WaitForFirstConsumer Binding

For better pod scheduling (volume created on node where pod lands):

```yaml
volumeBindingMode: WaitForFirstConsumer
```

#### Retain Volumes After PVC Deletion

```yaml
reclaimPolicy: Retain
```

#### Custom Mount Options

```yaml
mountOptions:
  - noatime
  - nodiratime
```

## Usage Examples

### Example 1: Simple PVC and Pod

```bash
# Create PVC
kubectl apply -f examples/pvc.yaml

# Verify PVC is bound
kubectl get pvc rds-test-pvc

# Create Pod using the PVC
kubectl apply -f examples/pod.yaml

# Verify Pod is running
kubectl get pod rds-test-pod

# Check volume mount
kubectl exec rds-test-pod -- df -h /data
kubectl exec rds-test-pod -- cat /data/test.txt
```

### Example 2: StatefulSet with Persistent Storage

```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: my-statefulset
spec:
  serviceName: my-service
  replicas: 3
  selector:
    matchLabels:
      app: my-app
  template:
    metadata:
      labels:
        app: my-app
    spec:
      containers:
      - name: app
        image: nginx
        volumeMounts:
        - name: data
          mountPath: /data
  volumeClaimTemplates:
  - metadata:
      name: data
    spec:
      accessModes: ["ReadWriteOnce"]
      storageClassName: rds-nvme
      resources:
        requests:
          storage: 20Gi
```

### Example 3: Multiple Volumes in a Pod

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: multi-volume-pod
spec:
  containers:
  - name: app
    image: nginx
    volumeMounts:
    - name: data
      mountPath: /data
    - name: logs
      mountPath: /logs
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: data-pvc
  - name: logs
    persistentVolumeClaim:
      claimName: logs-pvc
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: data-pvc
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: rds-nvme
  resources:
    requests:
      storage: 50Gi
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: logs-pvc
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: rds-nvme
  resources:
    requests:
      storage: 10Gi
```

## Verification

### Check Driver Status

```bash
# Check controller pod
kubectl get pods -n kube-system -l app=nvmet-csi-controller
kubectl describe pod -n kube-system -l app=nvmet-csi-controller

# Check node pods
kubectl get pods -n kube-system -l app=nvmet-csi-node -o wide
kubectl describe pod -n kube-system -l app=nvmet-csi-node

# Check CSI driver registration
kubectl get csidrivers
kubectl describe csidriver nvmet.csi.whiskey.works

# Check CSI nodes
kubectl get csinodes
```

### Check Volume Operations

```bash
# Create test PVC
kubectl apply -f examples/pvc.yaml

# Watch PVC status
kubectl get pvc rds-test-pvc -w

# Check PV details
kubectl get pv
kubectl describe pv <pv-name>

# Check volume on RDS
# SSH to RDS and run:
# /disk print detail where slot~"pvc-"
```

### Check Logs

```bash
# Controller logs
kubectl logs -n kube-system -l app=nvmet-csi-controller -c nvmet-csi-plugin --tail=100 -f

# Node logs
kubectl logs -n kube-system -l app=nvmet-csi-node -c nvmet-csi-plugin --tail=100 -f

# Provisioner logs
kubectl logs -n kube-system -l app=nvmet-csi-controller -c csi-provisioner --tail=100 -f
```

## Troubleshooting

### Issue: Controller Pod Not Starting

**Symptoms**: Controller pod in CrashLoopBackOff

**Check**:
```bash
kubectl logs -n kube-system -l app=nvmet-csi-controller -c nvmet-csi-plugin
```

**Common Causes**:
1. Invalid RDS credentials
   - Solution: Verify Secret contains valid private key
2. Cannot connect to RDS
   - Solution: Check network connectivity, RDS IP, SSH port
3. RDS user lacks permissions
   - Solution: Ensure user is in `full` group on RouterOS

### Issue: PVC Stuck in Pending

**Symptoms**: PVC remains in Pending state

**Check**:
```bash
kubectl describe pvc <pvc-name>
kubectl logs -n kube-system -l app=nvmet-csi-controller -c csi-provisioner
```

**Common Causes**:
1. Controller not running
   - Solution: Fix controller pod issues first
2. Insufficient storage on RDS
   - Solution: Check RDS free space
3. Volume creation failed
   - Solution: Check controller logs for errors

### Issue: Pod Cannot Mount Volume

**Symptoms**: Pod stuck in ContainerCreating, mount errors

**Check**:
```bash
kubectl describe pod <pod-name>
kubectl logs -n kube-system -l app=nvmet-csi-node -c nvmet-csi-plugin
```

**Common Causes**:
1. NVMe/TCP connection failed
   - Solution: Check network connectivity to RDS NVMe/TCP port
   - Verify: `sudo nvme list-subsys` on node
2. nvme-tcp module not loaded
   - Solution: `sudo modprobe nvme-tcp` on all nodes
3. Filesystem format failed
   - Solution: Check node logs, verify mkfs tools installed

### Issue: Volume Not Accessible After Mount

**Symptoms**: Pod runs but cannot access /data

**Check**:
```bash
kubectl exec <pod-name> -- ls -la /data
kubectl exec <pod-name> -- mount | grep /data
```

**Common Causes**:
1. Permission issues
   - Solution: Check fsGroup in pod securityContext
2. Filesystem corruption
   - Solution: Check node logs during mount

### Debug Mode

Enable debug logging:

```bash
# Edit controller deployment
kubectl edit deployment -n kube-system nvmet-csi-controller
# Change --v=5 to --v=9

# Edit node daemonset
kubectl edit daemonset -n kube-system nvmet-csi-node
# Change --v=5 to --v=9
```

## Uninstallation

### Remove Test Resources

```bash
# Delete test pod and PVC
kubectl delete -f examples/pod.yaml
kubectl delete -f examples/pvc.yaml

# Verify PV is deleted (if reclaimPolicy=Delete)
kubectl get pv
```

### Remove Driver

```bash
# Delete node daemonset
kubectl delete -f deploy/kubernetes/node.yaml

# Wait for all node pods to terminate
kubectl get pods -n kube-system -l app=nvmet-csi-node -w

# Delete controller
kubectl delete -f deploy/kubernetes/controller.yaml

# Delete StorageClass
kubectl delete -f examples/storageclass.yaml

# Delete CSI driver registration
kubectl delete -f deploy/kubernetes/csidriver.yaml

# Delete RBAC
kubectl delete -f deploy/kubernetes/rbac.yaml
```

### Clean Up RDS

SSH to RDS and remove orphaned volumes:

```routeros
# List CSI volumes
/disk print detail where slot~"pvc-"

# Remove specific volume
/disk remove [find slot="pvc-xxxxx"]

# Or remove all CSI volumes (CAREFUL!)
/disk remove [find slot~"pvc-"]
```

## Additional Resources

- [CSI Spec](https://github.com/container-storage-interface/spec)
- [Kubernetes Storage Documentation](https://kubernetes.io/docs/concepts/storage/)
- [MikroTik RouterOS Documentation](https://help.mikrotik.com/docs/)
- [NVMe/TCP RFC](https://nvmexpress.org/specification/nvme-tcp/)

## Support

For issues and feature requests, please use the [GitHub issue tracker](https://git.srvlab.io/whiskey/nvmet-csi/issues).
