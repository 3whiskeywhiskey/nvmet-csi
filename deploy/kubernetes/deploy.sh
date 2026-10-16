#!/bin/bash
# Deployment script for nvmet-csi driver

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}nvmet-csi driver Deployment Script${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""

# Check if kubectl is available
if ! command -v kubectl &> /dev/null; then
    echo -e "${RED}Error: kubectl not found${NC}"
    echo "Please install kubectl: https://kubernetes.io/docs/tasks/tools/"
    exit 1
fi

# Check if we can connect to cluster
if ! kubectl cluster-info &> /dev/null; then
    echo -e "${RED}Error: Cannot connect to Kubernetes cluster${NC}"
    echo "Please configure your kubeconfig"
    exit 1
fi

echo -e "${GREEN}✓${NC} kubectl found and cluster accessible"

# Check if configuration has been updated
if [ ! -f "deploy/kubernetes/controller.yaml" ]; then
    echo -e "${RED}Error: controller.yaml not found${NC}"
    echo "Please run this script from the repository root"
    exit 1
fi

# Require an external credential Secret before deploying
if ! kubectl -n nvmet-csi get secret nvmet-csi-secret &> /dev/null; then
    echo -e "${RED}Error: nvmet-csi-secret is missing in namespace nvmet-csi${NC}"
    echo "Create it from your private key and verified RDS host key as documented in README.md"
    exit 1
fi

# Warn about configuration
echo ""
echo -e "${YELLOW}⚠ Important:${NC} Make sure you have updated the following in controller.yaml:"
echo "  1. RDS address in the ConfigMap"
echo "  2. RDS user and volume base path"
echo ""
read -p "Have you updated the configuration? (yes/no): " confirm

if [ "$confirm" != "yes" ]; then
    echo -e "${YELLOW}Please update deploy/kubernetes/controller.yaml before deploying${NC}"
    exit 1
fi

# Deploy components
echo ""
echo -e "${GREEN}Deploying nvmet-csi driver components...${NC}"
echo ""

kubectl apply -k deploy/kubernetes/
echo -e "${GREEN}✓${NC} Driver resources deployed"

echo ""
echo "Creating StorageClass..."
kubectl apply -f examples/storageclass.yaml
echo -e "${GREEN}✓${NC} StorageClass created"

# Wait for controller to be ready
echo ""
echo "Waiting for controller to be ready..."
kubectl wait --for=condition=Ready pod -l app=nvmet-csi-controller -n nvmet-csi --timeout=60s || true

# Wait for at least one node pod to be ready
echo "Waiting for node pods to be ready..."
kubectl wait --for=condition=Ready pod -l app=nvmet-csi-node -n nvmet-csi --timeout=60s || true

# Show deployment status
echo ""
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}Deployment Status${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""

echo "Controller Pods:"
kubectl get pods -n nvmet-csi -l app=nvmet-csi-controller
echo ""

echo "Node Pods:"
kubectl get pods -n nvmet-csi -l app=nvmet-csi-node
echo ""

echo "CSI Driver:"
kubectl get csidrivers nvmet.csi.whiskey.works
echo ""

echo "StorageClass:"
kubectl get storageclass rds-nvme
echo ""

echo "CSI Nodes:"
kubectl get csinodes
echo ""

echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}Deployment Complete!${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""
echo "Next steps:"
echo "  1. Create a PVC: kubectl apply -f examples/pvc.yaml"
echo "  2. Create a Pod: kubectl apply -f examples/pod.yaml"
echo "  3. Check logs: kubectl logs -n nvmet-csi -l app=nvmet-csi-controller -c nvmet-csi-plugin"
echo ""
echo "For detailed documentation, see: docs/kubernetes-setup.md"
