#!/bin/bash
set -e

# CSI Sanity Test Runner
# This script runs the official CSI sanity tests against the nvmet-csi driver

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# Add GOPATH/bin to PATH for csi-sanity
export PATH="$(go env GOPATH)/bin:$PATH"

# Configuration
CSI_ENDPOINT="${CSI_ENDPOINT:-unix:///tmp/csi-sanity.sock}"
SOCKET_PATH="/tmp/csi-sanity.sock"
DRIVER_BINARY="${PROJECT_ROOT}/bin/nvmet-csi-plugin-$(go env GOOS)-$(go env GOARCH)"
RDS_ADDRESS="${RDS_ADDRESS:-}"
RDS_PORT="${RDS_PORT:-22}"
RDS_USER="${RDS_USER:-admin}"
RDS_SSH_KEY="${RDS_SSH_KEY:-}"

# Test configuration
TEST_VOLUME_SIZE="${TEST_VOLUME_SIZE:-1073741824}"  # 1 GiB
SANITY_VERSION="${SANITY_VERSION:-latest}"

echo "======================================"
echo "CSI Sanity Test Configuration"
echo "======================================"
echo "Endpoint: ${CSI_ENDPOINT}"
echo "Driver Binary: ${DRIVER_BINARY}"
echo "Test Volume Size: ${TEST_VOLUME_SIZE} bytes"
echo "Sanity Version: ${SANITY_VERSION}"
echo ""

# Check if driver binary exists
if [ ! -f "${DRIVER_BINARY}" ]; then
    echo "Error: Driver binary not found at ${DRIVER_BINARY}"
    echo "Run 'make build-local' first"
    exit 1
fi

# Check if csi-sanity is installed
if ! command -v csi-sanity &> /dev/null; then
    echo "Installing csi-sanity ${SANITY_VERSION}..."
    go install github.com/kubernetes-csi/csi-test/v5/cmd/csi-sanity@${SANITY_VERSION}
fi

# Cleanup any existing socket
rm -f "${SOCKET_PATH}"

# Determine test mode
if [ -z "${RDS_ADDRESS}" ]; then
    echo "======================================"
    echo "Running in MOCK MODE"
    echo "======================================"
    echo "Set RDS_ADDRESS to test with real RDS"
    echo ""

    # In mock mode, we can only test basic RPCs
    # Enable node mode (doesn't require RDS connection for Identity service)
    echo "Starting driver in node mode (for Identity service testing)..."
    "${DRIVER_BINARY}" \
        --endpoint="${CSI_ENDPOINT}" \
        --node-id="test-node" \
        --node \
        --v=5 &
    DRIVER_PID=$!

    # Wait for socket
    echo "Waiting for CSI socket to be ready..."
    for i in {1..30}; do
        if [ -S "${SOCKET_PATH}" ]; then
            echo "Socket ready!"
            break
        fi
        sleep 1
    done

    if [ ! -S "${SOCKET_PATH}" ]; then
        echo "Error: CSI socket not created"
        kill ${DRIVER_PID} 2>/dev/null || true
        exit 1
    fi

    # Run only Identity tests in mock mode
    echo ""
    echo "Running CSI Sanity Tests (Identity only)..."
    csi-sanity \
        --csi.endpoint="${CSI_ENDPOINT}" \
        --ginkgo.skip="Controller|Node" \
        --ginkgo.v

    SANITY_EXIT_CODE=$?

else
    echo "======================================"
    echo "Running with REAL RDS"
    echo "======================================"
    echo "RDS Address: ${RDS_ADDRESS}:${RDS_PORT}"
    echo "RDS User: ${RDS_USER}"
    echo ""

    # Validate RDS configuration
    if [ -z "${RDS_SSH_KEY}" ]; then
        echo "Error: RDS_SSH_KEY must be set when using real RDS"
        exit 1
    fi

    if [ ! -f "${RDS_SSH_KEY}" ]; then
        echo "Error: SSH key not found at ${RDS_SSH_KEY}"
        exit 1
    fi

    echo "Starting driver in controller mode..."
    "${DRIVER_BINARY}" \
        --endpoint="${CSI_ENDPOINT}" \
        --controller-mode \
        --rds-address="${RDS_ADDRESS}" \
        --rds-port="${RDS_PORT}" \
        --rds-user="${RDS_USER}" \
        --rds-ssh-key="${RDS_SSH_KEY}" \
        --v=5 &
    DRIVER_PID=$!

    # Wait for socket
    echo "Waiting for CSI socket to be ready..."
    for i in {1..30}; do
        if [ -S "${SOCKET_PATH}" ]; then
            echo "Socket ready!"
            break
        fi
        sleep 1
    done

    if [ ! -S "${SOCKET_PATH}" ]; then
        echo "Error: CSI socket not created"
        kill ${DRIVER_PID} 2>/dev/null || true
        exit 1
    fi

    # Run Controller + Identity tests (skip Node tests)
    echo ""
    echo "Running CSI Sanity Tests (Controller + Identity)..."
    csi-sanity \
        --csi.endpoint="${CSI_ENDPOINT}" \
        --csi.testvolumesize="${TEST_VOLUME_SIZE}" \
        --ginkgo.skip="Node" \
        --ginkgo.v

    SANITY_EXIT_CODE=$?
fi

# Cleanup
echo ""
echo "Cleaning up..."
kill ${DRIVER_PID} 2>/dev/null || true
rm -f "${SOCKET_PATH}"

# Report results
echo ""
echo "======================================"
if [ ${SANITY_EXIT_CODE} -eq 0 ]; then
    echo "✅ CSI Sanity Tests PASSED"
else
    echo "❌ CSI Sanity Tests FAILED"
fi
echo "======================================"

exit ${SANITY_EXIT_CODE}
