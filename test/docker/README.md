# Docker Compose Test Environment

This directory contains configuration for running automated integration tests using Docker Compose.

## Quick Start

```bash
# Run all tests
docker-compose -f docker-compose.test.yml up --abort-on-container-exit

# Run only CSI sanity tests
docker-compose -f docker-compose.test.yml up csi-sanity --abort-on-container-exit

# Run only integration tests
docker-compose -f docker-compose.test.yml up integration-tests --abort-on-container-exit

# Clean up
docker-compose -f docker-compose.test.yml down -v
```

## Components

### mock-rds

A containerized SSH server that simulates MikroTik RDS RouterOS CLI. This allows testing without real RDS hardware.

**Note**: The current setup uses a basic OpenSSH server. For full RDS simulation, you would need custom scripts that mock RouterOS commands.

### csi-controller

The nvmet-csi driver controller service connected to the mock RDS.

### csi-sanity

Runs the official CSI sanity test suite against the controller.

### integration-tests

Runs Go integration tests with the mock RDS server.

## Setup SSH Keys for Mock RDS

The mock RDS server requires SSH keys for authentication:

```bash
# Generate test SSH keys
mkdir -p test/docker/ssh-keys test/docker/mock-rds-config

# Generate key pair
ssh-keygen -t rsa -b 4096 -f test/docker/ssh-keys/id_rsa -N ""

# Copy public key to authorized_keys
cp test/docker/ssh-keys/id_rsa.pub test/docker/mock-rds-config/authorized_keys

# Set permissions
chmod 600 test/docker/ssh-keys/id_rsa
chmod 644 test/docker/ssh-keys/id_rsa.pub
chmod 644 test/docker/mock-rds-config/authorized_keys
```

## Customizing Mock RDS

To simulate RouterOS commands, create custom scripts in `test/docker/mock-rds-scripts/`:

```bash
# Example: Create a script that handles /disk commands
cat > test/docker/mock-rds-scripts/disk-commands.sh <<'EOF'
#!/bin/sh
# Mock RouterOS /disk commands
case "$1" in
  "/disk add"*)
    echo "Created disk"
    ;;
  "/disk remove"*)
    echo "Removed disk"
    ;;
  "/disk print"*)
    echo "Slot: pvc-test-123"
    echo "File Path: /storage/test.img"
    ;;
esac
EOF

chmod +x test/docker/mock-rds-scripts/disk-commands.sh
```

Then configure the SSH server to use these scripts as the shell.

## Limitations

- Current mock RDS is simplified and may not handle all RouterOS CLI nuances
- For full E2E testing, use real RDS hardware (see test/e2e/MANUAL_TESTING.md)
- Mock does not simulate actual NVMe/TCP exports (only controller operations are tested)

## Troubleshooting

### SSH Connection Failed

```bash
# Check if mock-rds is running
docker-compose -f docker-compose.test.yml ps

# Check SSH server logs
docker-compose -f docker-compose.test.yml logs mock-rds

# Test SSH connection manually
ssh -i test/docker/ssh-keys/id_rsa -p 12222 admin@localhost
```

### CSI Socket Not Created

```bash
# Check controller logs
docker-compose -f docker-compose.test.yml logs csi-controller

# Verify socket in volume
docker-compose -f docker-compose.test.yml exec csi-controller ls -l /csi/
```

### Tests Failing

```bash
# Run with verbose output
docker-compose -f docker-compose.test.yml up --abort-on-container-exit

# Check individual component logs
docker-compose -f docker-compose.test.yml logs csi-sanity
docker-compose -f docker-compose.test.yml logs integration-tests
```
