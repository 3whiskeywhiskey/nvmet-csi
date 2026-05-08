# Manual End-to-End Testing Guide

This guide walks through manual testing of the nvmet-csi driver with a real MikroTik RDS server.

## Prerequisites

1. **RDS Server**: MikroTik RouterOS 7.x with RDS (ROSE Data Server) enabled
2. **SSH Access**: SSH key-based authentication configured to RDS
3. **Network Access**: Your test machine can reach the RDS server on SSH (port 22) and NVMe/TCP (port 4420)
4. **Storage Pool**: RDS has a Btrfs filesystem mounted at `/storage-pool` with available space

## Setup

### 1. Build the Driver

```bash
# Build for your local platform
make build-local

# Binary will be at: bin/nvmet-csi-plugin-<os>-<arch>
```

### 2. Configure RDS Credentials

Create an environment file with your RDS credentials:

```bash
# Create test/e2e/.env
cat > test/e2e/.env <<EOF
RDS_ADDRESS=192.168.88.1
RDS_PORT=22
RDS_USER=admin
RDS_SSH_KEY=$HOME/.ssh/id_rsa
EOF
```

### 3. Verify RDS Connectivity

Test SSH connection manually:

```bash
ssh -i $HOME/.ssh/id_rsa admin@192.168.88.1 '/system resource print'
```

Expected output should show RouterOS system information.

## Test Scenarios

### Scenario 1: Identity Service

Test the CSI Identity service responds correctly.

```bash
# Source environment
source test/e2e/.env

# Start driver in a terminal
./bin/nvmet-csi-plugin-$(go env GOOS)-$(go env GOARCH) \
  --endpoint=unix:///tmp/csi-test.sock \
  --controller-mode \
  --rds-address=${RDS_ADDRESS} \
  --rds-port=${RDS_PORT} \
  --rds-user=${RDS_USER} \
  --rds-ssh-key=${RDS_SSH_KEY} \
  --v=5
```

In another terminal, test Identity RPCs:

```bash
# Install grpcurl if needed
go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest

# Test GetPluginInfo
grpcurl -plaintext -unix /tmp/csi-test.sock \
  csi.v1.Identity/GetPluginInfo

# Expected output:
# {
#   "name": "nvmet.csi.whiskey.works",
#   "vendorVersion": "dev"
# }

# Test Probe
grpcurl -plaintext -unix /tmp/csi-test.sock \
  csi.v1.Identity/Probe

# Expected output:
# {
#   "ready": true
# }

# Test GetPluginCapabilities
grpcurl -plaintext -unix /tmp/csi-test.sock \
  csi.v1.Identity/GetPluginCapabilities

# Expected output:
# {
#   "capabilities": [
#     {
#       "service": {
#         "type": "CONTROLLER_SERVICE"
#       }
#     }
#   ]
# }
```

### Scenario 2: Create Volume

Create a test volume on RDS.

```bash
# Create a 5 GiB volume
grpcurl -plaintext -unix /tmp/csi-test.sock \
  csi.v1.Controller/CreateVolume \
  -d '{
    "name": "manual-test-vol-1",
    "capacity_range": {
      "required_bytes": 5368709120
    },
    "volume_capabilities": [{
      "mount": {},
      "access_mode": {
        "mode": "SINGLE_NODE_WRITER"
      }
    }]
  }'

# Expected output includes volume_id like "pvc-xxxx-xxxx-xxxx-xxxx"
# Save this volume ID for next steps
```

Verify on RDS:

```bash
# SSH to RDS and check disk was created
ssh -i ${RDS_SSH_KEY} ${RDS_USER}@${RDS_ADDRESS} '/disk print detail where slot~"pvc-"'

# Expected output should show:
# - Slot: pvc-xxxx-xxxx-xxxx-xxxx
# - File Path: /storage-pool/kubernetes-volumes/pvc-xxxx.img
# - File Size: 5368709120
# - NVMe TCP Export: yes
# - NVMe TCP Server Port: 4420
# - NVMe TCP Server NQN: nqn.2025-01.io.srvlab.rds:pvc-xxxx
```

### Scenario 3: List Volumes

```bash
grpcurl -plaintext -unix /tmp/csi-test.sock \
  csi.v1.Controller/ListVolumes

# Expected output shows all volumes including the one just created
```

### Scenario 4: Get Capacity

Query available storage on RDS.

```bash
grpcurl -plaintext -unix /tmp/csi-test.sock \
  csi.v1.Controller/GetCapacity \
  -d '{}'

# Expected output:
# {
#   "availableCapacity": "5962963271680"  # (bytes)
# }
```

Verify on RDS:

```bash
ssh -i ${RDS_SSH_KEY} ${RDS_USER}@${RDS_ADDRESS} '/file print detail where name="/storage-pool"'

# Check "Free:" value matches the capacity reported by driver
```

### Scenario 5: Validate Volume Capabilities

```bash
# Replace VOLUME_ID with the actual ID from CreateVolume
export VOLUME_ID="pvc-xxxx-xxxx-xxxx-xxxx"

# Test with supported capabilities
grpcurl -plaintext -unix /tmp/csi-test.sock \
  csi.v1.Controller/ValidateVolumeCapabilities \
  -d "{
    \"volume_id\": \"${VOLUME_ID}\",
    \"volume_capabilities\": [{
      \"mount\": {},
      \"access_mode\": {
        \"mode\": \"SINGLE_NODE_WRITER\"
      }
    }]
  }"

# Expected output:
# {
#   "confirmed": {
#     "volumeCapabilities": [...]
#   }
# }

# Test with unsupported capabilities (MULTI_NODE)
grpcurl -plaintext -unix /tmp/csi-test.sock \
  csi.v1.Controller/ValidateVolumeCapabilities \
  -d "{
    \"volume_id\": \"${VOLUME_ID}\",
    \"volume_capabilities\": [{
      \"mount\": {},
      \"access_mode\": {
        \"mode\": \"MULTI_NODE_MULTI_WRITER\"
      }
    }]
  }"

# Expected output:
# {
#   "message": "access mode MULTI_NODE_MULTI_WRITER is not supported"
# }
```

### Scenario 6: Idempotency Test

Create the same volume twice - should return the same volume ID.

```bash
# Create volume
grpcurl -plaintext -unix /tmp/csi-test.sock \
  csi.v1.Controller/CreateVolume \
  -d '{
    "name": "idempotency-test-1",
    "capacity_range": {
      "required_bytes": 1073741824
    },
    "volume_capabilities": [{
      "mount": {},
      "access_mode": {
        "mode": "SINGLE_NODE_WRITER"
      }
    }]
  }' | tee /tmp/create1.json

# Create same volume again
grpcurl -plaintext -unix /tmp/csi-test.sock \
  csi.v1.Controller/CreateVolume \
  -d '{
    "name": "idempotency-test-1",
    "capacity_range": {
      "required_bytes": 1073741824
    },
    "volume_capabilities": [{
      "mount": {},
      "access_mode": {
        "mode": "SINGLE_NODE_WRITER"
      }
    }]
  }' | tee /tmp/create2.json

# Compare volume IDs - should be identical
diff /tmp/create1.json /tmp/create2.json
```

### Scenario 7: Delete Volume

Delete a test volume and verify cleanup.

```bash
# Delete the volume
grpcurl -plaintext -unix /tmp/csi-test.sock \
  csi.v1.Controller/DeleteVolume \
  -d "{
    \"volume_id\": \"${VOLUME_ID}\"
  }"

# Expected output:
# {}
```

Verify on RDS:

```bash
# Check volume is gone
ssh -i ${RDS_SSH_KEY} ${RDS_USER}@${RDS_ADDRESS} "/disk print detail where slot=\"${VOLUME_ID}\""

# Expected: No output (volume deleted)

# Also verify file is gone
ssh -i ${RDS_SSH_KEY} ${RDS_USER}@${RDS_ADDRESS} "/file print detail where name~\"${VOLUME_ID}\""

# Expected: No output (file deleted)
```

### Scenario 8: Delete Idempotency

Delete a non-existent volume - should succeed.

```bash
grpcurl -plaintext -unix /tmp/csi-test.sock \
  csi.v1.Controller/DeleteVolume \
  -d '{
    "volume_id": "pvc-nonexistent-volume-12345"
  }'

# Expected output:
# {}  (success, even though volume doesn't exist)
```

### Scenario 9: Error Handling - Insufficient Space

Try to create a volume larger than available space.

```bash
# Try to create a 100 TiB volume (likely larger than your RDS pool)
grpcurl -plaintext -unix /tmp/csi-test.sock \
  csi.v1.Controller/CreateVolume \
  -d '{
    "name": "too-large-volume",
    "capacity_range": {
      "required_bytes": 109951162777600
    },
    "volume_capabilities": [{
      "mount": {},
      "access_mode": {
        "mode": "SINGLE_NODE_WRITER"
      }
    }]
  }'

# Expected output:
# ERROR:
#   Code: ResourceExhausted
#   Message: "insufficient storage on RDS: ..."
```

### Scenario 10: Error Handling - Invalid Volume ID

Try to delete with an invalid volume ID format.

```bash
# Invalid format (missing pvc- prefix)
grpcurl -plaintext -unix /tmp/csi-test.sock \
  csi.v1.Controller/DeleteVolume \
  -d '{
    "volume_id": "invalid-format"
  }'

# Expected output:
# ERROR:
#   Code: InvalidArgument
#   Message: "invalid volume ID: ..."

# Command injection attempt
grpcurl -plaintext -unix /tmp/csi-test.sock \
  csi.v1.Controller/DeleteVolume \
  -d '{
    "volume_id": "pvc-test; rm -rf /"
  }'

# Expected output:
# ERROR:
#   Code: InvalidArgument
#   Message: "invalid volume ID: ..."
```

## Cleanup

After testing, clean up all test volumes:

```bash
# List all volumes
ssh -i ${RDS_SSH_KEY} ${RDS_USER}@${RDS_ADDRESS} '/disk print where slot~"pvc-"'

# Remove each test volume manually if needed
ssh -i ${RDS_SSH_KEY} ${RDS_USER}@${RDS_ADDRESS} '/disk remove [find slot~"manual-test"]'
ssh -i ${RDS_SSH_KEY} ${RDS_USER}@${RDS_ADDRESS} '/disk remove [find slot~"idempotency-test"]'
```

## Automated E2E Test Script

For convenience, use the provided automated test script:

```bash
# Run all E2E scenarios automatically
./test/e2e/run-e2e-tests.sh

# The script will:
# 1. Start the driver
# 2. Run all test scenarios
# 3. Verify results
# 4. Clean up test volumes
# 5. Report PASS/FAIL for each scenario
```

## Troubleshooting

### Driver Won't Start

```bash
# Check logs for errors
# Common issues:
# - SSH key permissions: chmod 600 ~/.ssh/id_rsa
# - RDS not reachable: ping ${RDS_ADDRESS}
# - Wrong credentials: ssh -i ${RDS_SSH_KEY} ${RDS_USER}@${RDS_ADDRESS}
```

### Volumes Not Appearing on RDS

```bash
# Check driver logs for SSH errors
# Verify RDS has space:
ssh -i ${RDS_SSH_KEY} ${RDS_USER}@${RDS_ADDRESS} '/file print detail where name="/storage-pool"'

# Check RouterOS version supports /disk add type=file
ssh -i ${RDS_SSH_KEY} ${RDS_USER}@${RDS_ADDRESS} '/system resource print'
```

### gRPC Connection Failed

```bash
# Check socket exists
ls -l /tmp/csi-test.sock

# Check driver is running
ps aux | grep nvmet-csi-plugin

# Try restarting driver
pkill nvmet-csi-plugin
# ... then restart
```

## Success Criteria

All scenarios should pass with expected results:

- ✅ Identity service responds
- ✅ Can create volumes on RDS
- ✅ Can list volumes
- ✅ Can query capacity
- ✅ Can validate capabilities
- ✅ Idempotency works for create/delete
- ✅ Can delete volumes
- ✅ Error handling works correctly
- ✅ Security validation prevents injection attacks

## Next Steps

After manual testing succeeds:

1. Run CSI sanity tests: `./test/sanity/run-sanity-tests.sh`
2. Run integration tests: `make test-integration`
3. Proceed to Milestone 3 (Node Service implementation)
