# MikroTik RDS RouterOS Commands Reference

This document provides a reference for the RouterOS CLI commands used by the RDS CSI Driver to manage NVMe/TCP volumes on MikroTik ROSE Data Server (RDS).

## Overview

The RDS CSI Driver interacts with the RDS via **SSH** using RouterOS command-line interface (CLI). All volume operations are executed remotely using these commands.

**Connection Method**: `ssh <user>@<rds-ip> '<command>'`

**Authentication**: SSH public key authentication (password-less)

## Volume Management Commands

### Creating a File-backed NVMe/TCP Volume

**Command**:
```bash
/disk add \
  type=file \
  file-path=/storage-pool/kubernetes-volumes/pvc-<uuid>.img \
  file-size=<size> \
  slot=pvc-<uuid> \
  mount=no \
  nvme-tcp-export=yes \
  nvme-tcp-server-port=4420 \
  nvme-tcp-server-nqn=nqn.2000-02.com.mikrotik:pvc-<uuid>
```

**Parameters**:
- `type=file`: Create a file-backed disk (vs. physical disk or RAID)
- `file-path`: Full path to backing file on Btrfs filesystem
- `file-size`: Volume size (e.g., `50G`, `100G`, `1T`)
- `slot`: Unique identifier for the disk (we use PVC UUID)
- `mount=no`: Prevent RouterOS from trying to mount or scan the PVC backing disk
- `nvme-tcp-export=yes`: Enable NVMe/TCP export for this disk
- `nvme-tcp-server-port`: NVMe/TCP listening port (default 4420)
- `nvme-tcp-server-nqn`: NVMe Qualified Name (must be unique per volume)

**Example**:
```bash
ssh admin@10.42.68.1 '/disk add \
  type=file \
  file-path=/storage-pool/kubernetes-volumes/pvc-abc123.img \
  file-size=50G \
  slot=pvc-abc123 \
  mount=no \
  nvme-tcp-export=yes \
  nvme-tcp-server-port=4420 \
  nvme-tcp-server-nqn=nqn.2000-02.com.mikrotik:pvc-abc123'
```

**Expected Output** (on success):
```
# Returns nothing on success
# Exit code: 0
```

**Error Cases**:
- `not enough space`: Insufficient free space on filesystem
- `file already exists`: File at `file-path` already exists
- `invalid parameter`: Malformed parameter value

---

### Listing Disks

**Command**:
```bash
/disk print detail
```

**Output Format**:
```
 0  slot="disk1" type="file" file-path="/storage-pool/kubernetes-volumes/pvc-abc123.img"
    file-size=53687091200 nvme-tcp-export=yes nvme-tcp-server-port=4420
    nvme-tcp-server-nqn="nqn.2000-02.com.mikrotik:pvc-abc123" status="ready"

 1  slot="disk2" type="file" file-path="/storage-pool/kubernetes-volumes/pvc-def456.img"
    file-size=107374182400 nvme-tcp-export=yes nvme-tcp-server-port=4420
    nvme-tcp-server-nqn="nqn.2000-02.com.mikrotik:pvc-def456" status="ready"
```

**Filtering by Slot**:
```bash
/disk print detail where slot=pvc-abc123
```

**Use Case**: Verify volume was created successfully

---

### Deleting a Volume

**Command**:
```bash
/disk remove [find slot=pvc-<uuid>]
```

**Example**:
```bash
ssh admin@10.42.68.1 '/disk remove [find slot=pvc-abc123]'
```

**Expected Output** (on success):
```
# Returns nothing on success
# Exit code: 0
```

**Error Cases**:
- `no such item`: Disk with specified slot not found
- `disk is in use`: Volume is currently connected via NVMe/TCP

**Note**: This command removes both the disk export AND deletes the backing file.

---

### Querying Filesystem Capacity

**Command**:
```bash
/file print detail where name="/storage-pool"
```

**Output Format**:
```
name: /storage-pool
type: directory
size: 0
creation-time: jan/01/2025 00:00:00

Total: 7.23TiB
Free: 5.12TiB
Used: 2.11TiB
```

**Parsing**:
- Extract `Free:` value to determine available capacity
- Convert units (TiB, GiB, MiB) to bytes

**Alternative Command** (for total/free space):
```bash
/disk monitor-once 0
```

**Output**:
```
total-space: 7751451648000
free-space: 5497558138880
```

**Use Case**: Implement `GetCapacity` CSI method

---

## NVMe/TCP-specific Commands

### Viewing NVMe/TCP Exports

**Command**:
```bash
/interface nvme-tcp print detail
```

**Output Format**:
```
 0  name="nvme-tcp1" nqn="nqn.2000-02.com.mikrotik:pvc-abc123" port=4420
    status="running" connections=2
```

**Use Case**: Verify NVMe/TCP subsystem is active and accepting connections

---

### Checking Active NVMe Connections

**Command**:
```bash
/interface nvme-tcp connection print
```

**Output Format**:
```
 0  interface="nvme-tcp1" remote-address=10.42.67.8 state="connected"
 1  interface="nvme-tcp1" remote-address=10.42.67.9 state="connected"
```

**Use Case**: Debug connection issues, verify which nodes are connected

---

## Troubleshooting Commands

### Check Disk Status

**Command**:
```bash
/disk print detail where slot=pvc-<uuid>
```

**Status Values**:
- `ready`: Disk is operational and can be exported
- `formatting`: Disk is being formatted (initial creation)
- `error`: Disk encountered an error

---

### View System Logs

**Command**:
```bash
/log print where topics~"nvme|disk"
```

**Output**:
```
12:34:56 disk,info disk added: pvc-abc123
12:35:10 nvme,info nvme-tcp connection from 10.42.67.8
12:40:22 nvme,warning nvme-tcp connection timeout from 10.42.67.9
```

**Use Case**: Debug volume creation failures or connection issues

---

### Check RouterOS Version

**Command**:
```bash
/system resource print
```

**Output**:
```
platform: "MikroTik"
board-name: "ROSE"
version: "7.16 (stable)"
...
```

**Use Case**: Verify RDS supports required NVMe/TCP features

---

## SSH Connection Examples

### Basic Connection Test

```bash
ssh admin@10.42.68.1 '/system identity print'
```

**Expected Output**:
```
name: rds
```

---

### Authenticating with SSH Key

```bash
ssh -i /path/to/rds-key admin@10.42.68.1 '/disk print'
```

---

### Connection with Timeout

```bash
ssh -o ConnectTimeout=10 admin@10.42.68.1 '/disk print'
```

**Use Case**: Prevent indefinite hanging if RDS is unreachable

---

### Handling Command Output

**Successful Command** (exit code 0):
```bash
$ ssh admin@10.42.68.1 '/disk print'
$ echo $?
0
```

**Failed Command** (exit code != 0):
```bash
$ ssh admin@10.42.68.1 '/disk remove [find slot=nonexistent]'
failure: no such item
$ echo $?
1
```

**Driver Implementation**: Check exit code to determine success/failure

---

## Volume Naming and NQN Conventions

### Volume ID Format

**Format**: `pvc-<uuid>`

**Example**: `pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890`

**Rationale**:
- UUID ensures uniqueness across cluster
- `pvc-` prefix identifies volumes created by CSI driver
- Compatible with Kubernetes PVC naming

---

### NVMe Qualified Name (NQN) Format

**Format**: `nqn.2000-02.com.mikrotik:<volume-id>`

**Example**: `nqn.2000-02.com.mikrotik:pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890`

**Components**:
- `nqn.2000-02.com.mikrotik`: MikroTik's registered NQN prefix
- `<volume-id>`: Unique volume identifier (same as slot)

**NQN Spec**: RFC 8881 (NVMe over Fabrics)

---

### File Path Convention

**Format**: `<base-path>/<volume-id>.img`

**Example**: `/storage-pool/kubernetes-volumes/pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890.img`

**Components**:
- `<base-path>`: Configurable via StorageClass (default: `/storage-pool/kubernetes-volumes`)
- `<volume-id>.img`: Volume ID with `.img` extension

**Rationale**:
- `.img` extension indicates disk image file
- Full path prevents collisions
- Easy to identify CSI-managed volumes vs. manual disks

---

## Error Handling Patterns

### Command Parsing Strategy

**Approach**: Regex-based parsing of RouterOS CLI output

**Example** (Go code):
```go
output, err := sshClient.Run("/disk print detail where slot=" + volumeID)
if err != nil {
    return fmt.Errorf("SSH command failed: %w", err)
}

// Check if volume exists
if strings.Contains(output, "slot=\"" + volumeID + "\"") {
    // Volume found
    statusRe := regexp.MustCompile(`status="(\w+)"`)
    matches := statusRe.FindStringSubmatch(output)
    if len(matches) > 1 {
        status := matches[1]
        if status == "ready" {
            return nil // Success
        }
        return fmt.Errorf("disk status is %s", status)
    }
}
return fmt.Errorf("volume not found")
```

---

### Retry Logic for Transient Errors

**Retry-worthy Errors**:
- SSH connection timeout
- `disk is busy` (wait and retry)
- Temporary filesystem locks

**Non-retry Errors**:
- `not enough space` (permanent capacity issue)
- `invalid parameter` (bad input)
- Authentication failure

**Implementation**:
```go
func runWithRetry(cmd string, maxRetries int) (string, error) {
    for i := 0; i < maxRetries; i++ {
        output, err := sshClient.Run(cmd)
        if err == nil {
            return output, nil
        }
        if !isRetryable(err) {
            return "", err
        }
        time.Sleep(time.Second * (1 << i)) // Exponential backoff
    }
    return "", fmt.Errorf("max retries exceeded")
}
```

---

## Performance Considerations

### Command Execution Time

Typical latencies (measured from homelab environment):

| Command | Latency | Notes |
|---------|---------|-------|
| `/disk print` | 50-100ms | Fast, no disk I/O |
| `/disk add` (1GB) | 500-1000ms | Allocates sparse file |
| `/disk add` (100GB) | 2-5s | Larger allocation |
| `/disk remove` | 500-1000ms | Deletes backing file |
| `/file print` | 100-200ms | Filesystem stat |

**Implication**: Volume creation takes 2-5 seconds minimum (SSH overhead + disk allocation)

---

### Connection Pooling

**Recommendation**: Maintain persistent SSH connection pool

**Benefits**:
- Avoid repeated SSH handshake overhead (200-500ms per connection)
- Reuse authenticated sessions
- Handle multiple concurrent operations

**Implementation**: Use `golang.org/x/crypto/ssh` with connection pooling

---

## Security Considerations

### SSH Key Management

**Key Generation**:
```bash
ssh-keygen -t ed25519 -f rds-csi-key -N "" -C "rds-csi-driver"
```

**Public Key Installation on RDS**:
```bash
/user ssh-keys import public-key-file=rds-csi-key.pub user=admin
```

**Key Storage in Kubernetes**:
```bash
kubectl create secret generic rds-ssh-key \
  --from-file=id_ed25519=rds-csi-key \
  --namespace kube-system
```

---

### Command Injection Prevention

**Risk**: User-controlled input (volume size, ID) could inject malicious commands

**Mitigation**:
1. **Strict Validation**: Only allow alphanumeric + hyphen in volume IDs
2. **Parameterized Commands**: Use proper escaping for shell commands
3. **Input Sanitization**: Reject any input containing shell metacharacters (`; | & $ \` etc.`)

**Example** (Go code):
```go
// Validate volume ID
volumeIDPattern := regexp.MustCompile(`^pvc-[a-f0-9-]+$`)
if !volumeIDPattern.MatchString(volumeID) {
    return fmt.Errorf("invalid volume ID format")
}

// Safe to use in command
cmd := fmt.Sprintf("/disk remove [find slot=%s]", volumeID)
```

---

## Testing Commands Manually

### Create Test Volume

```bash
ssh admin@10.42.68.1 '
/disk add \
  type=file \
  file-path=/storage-pool/test/test-volume.img \
  file-size=1G \
  slot=test-volume \
  nvme-tcp-export=yes \
  nvme-tcp-server-port=4420 \
  nvme-tcp-server-nqn=nqn.2000-02.com.mikrotik:test-volume
'
```

---

### Connect from Worker Node

```bash
# Discover target
nvme discover -t tcp -a 10.42.68.1 -s 4420

# Connect
nvme connect -t tcp -a 10.42.68.1 -s 4420 \
  -n nqn.2000-02.com.mikrotik:test-volume

# Verify device appeared
lsblk | grep nvme

# Disconnect
nvme disconnect -n nqn.2000-02.com.mikrotik:test-volume
```

---

### Delete Test Volume

```bash
ssh admin@10.42.68.1 '/disk remove [find slot=test-volume]'
```

---

## Reference Links

- [MikroTik RouterOS Documentation](https://help.mikrotik.com/docs/spaces/ROS/overview)
- [RouterOS Container Documentation](https://help.mikrotik.com/docs/spaces/ROS/pages/328073/Container)
- [NVMe-oF Specification](https://nvmexpress.org/developers/nvme-of-specification/)
- [RFC 8881 - NVMe Qualified Names](https://datatracker.ietf.org/doc/html/rfc8881)

---

**Last Updated**: 2025-11-05
**Tested On**: RouterOS 7.16 (stable)
