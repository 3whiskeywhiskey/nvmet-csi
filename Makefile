# RDS CSI Driver Makefile

# Image configuration
REGISTRY ?= ghcr.io/3whiskeywhiskey
IMAGE_NAME ?= rds-csi-driver
IMAGE_TAG ?= dev
IMAGE = $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG)

# Build configuration
GOARCH ?= amd64
GOOS ?= linux
CGO_ENABLED ?= 0

# Detect local OS and architecture
LOCAL_OS := $(shell go env GOOS)
LOCAL_ARCH := $(shell go env GOARCH)

# Binary output
BINARY_NAME = rds-csi-plugin
BUILD_DIR = bin
DOCKERFILE = Dockerfile

# Go build flags
LDFLAGS ?= -s -w
GOFLAGS ?= -v

# Git versioning
GIT_COMMIT := $(shell git rev-parse --short HEAD)
GIT_TAG := $(shell git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
VERSION_PKG = git.srvlab.io/whiskey/rds-csi-driver/pkg/driver

VERSION_LDFLAGS := -X $(VERSION_PKG).version=$(GIT_TAG) \
                    -X $(VERSION_PKG).gitCommit=$(GIT_COMMIT) \
                    -X $(VERSION_PKG).buildDate=$(BUILD_DATE)

# Targets
.PHONY: all
all: build

.PHONY: help
help:
	@echo "RDS CSI Driver - Build Targets:"
	@echo ""
	@echo "Build Commands:"
	@echo "  make build            - Build binary for Linux/amd64 (container target)"
	@echo "  make build-local      - Build binary for local OS/arch ($(LOCAL_OS)/$(LOCAL_ARCH))"
	@echo "  make build-darwin-amd64  - Build for macOS Intel"
	@echo "  make build-darwin-arm64  - Build for macOS Apple Silicon"
	@echo "  make build-linux-amd64   - Build for Linux x86_64"
	@echo "  make build-linux-arm64   - Build for Linux ARM64"
	@echo "  make build-all        - Build for all supported platforms"
	@echo ""
	@echo "Container Commands:"
	@echo "  make docker           - Build Docker image"
	@echo "  make docker-push      - Push Docker image to registry"
	@echo "  make docker-multiarch - Build and push multi-arch images"
	@echo ""
	@echo "Testing Commands:"
	@echo "  make test                - Run unit tests"
	@echo "  make test-coverage       - Run tests with coverage report"
	@echo "  make test-integration    - Run integration tests with mock RDS"
	@echo "  make e2e-test            - Run E2E tests"
	@echo "  make e2e-test-verbose    - Run E2E tests with verbose Ginkgo output"
	@echo "  make test-sanity         - Run CSI sanity tests (requires RDS or uses mock)"
	@echo "  make test-sanity-mock    - Run CSI sanity tests with mock RDS"
	@echo "  make test-sanity-real    - Run CSI sanity tests with real RDS (requires env vars)"
	@echo "  make test-docker         - Run all tests in Docker Compose"
	@echo "  make lint                - Run linters (golangci-lint)"
	@echo "  make fmt                 - Format Go code"
	@echo "  make verify-helm         - Lint and render Helm chart"
	@echo "  make verify              - Run all verification checks"
	@echo ""
	@echo "Utility Commands:"
	@echo "  make clean            - Remove build artifacts"
	@echo "  make mod-tidy         - Tidy Go modules"
	@echo "  make install-tools    - Install development tools"
	@echo ""

.PHONY: build
build: mod-tidy
	@echo "Building $(BINARY_NAME) for $(GOOS)/$(GOARCH)..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) go build \
		-ldflags "$(LDFLAGS) $(VERSION_LDFLAGS)" \
		$(GOFLAGS) \
		-o $(BUILD_DIR)/$(BINARY_NAME) \
		./cmd/rds-csi-plugin
	@echo "Binary created: $(BUILD_DIR)/$(BINARY_NAME)"

.PHONY: build-local
build-local:
	@echo "Building $(BINARY_NAME) for local OS ($(LOCAL_OS)/$(LOCAL_ARCH))..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=$(LOCAL_OS) GOARCH=$(LOCAL_ARCH) go build \
		-ldflags "$(LDFLAGS) $(VERSION_LDFLAGS)" \
		$(GOFLAGS) \
		-o $(BUILD_DIR)/$(BINARY_NAME)-$(LOCAL_OS)-$(LOCAL_ARCH) \
		./cmd/rds-csi-plugin
	@echo "Binary created: $(BUILD_DIR)/$(BINARY_NAME)-$(LOCAL_OS)-$(LOCAL_ARCH)"
	@echo ""
	@echo "To run locally: ./$(BUILD_DIR)/$(BINARY_NAME)-$(LOCAL_OS)-$(LOCAL_ARCH) --help"

# Platform-specific builds
.PHONY: build-darwin-amd64
build-darwin-amd64:
	@echo "Building $(BINARY_NAME) for macOS Intel..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build \
		-ldflags "$(LDFLAGS) $(VERSION_LDFLAGS)" \
		$(GOFLAGS) \
		-o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 \
		./cmd/rds-csi-plugin
	@echo "Binary created: $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64"

.PHONY: build-darwin-arm64
build-darwin-arm64:
	@echo "Building $(BINARY_NAME) for macOS Apple Silicon..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build \
		-ldflags "$(LDFLAGS) $(VERSION_LDFLAGS)" \
		$(GOFLAGS) \
		-o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 \
		./cmd/rds-csi-plugin
	@echo "Binary created: $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64"

.PHONY: build-linux-amd64
build-linux-amd64:
	@echo "Building $(BINARY_NAME) for Linux x86_64..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
		-ldflags "$(LDFLAGS) $(VERSION_LDFLAGS)" \
		$(GOFLAGS) \
		-o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 \
		./cmd/rds-csi-plugin
	@echo "Binary created: $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64"

.PHONY: build-linux-arm64
build-linux-arm64:
	@echo "Building $(BINARY_NAME) for Linux ARM64..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build \
		-ldflags "$(LDFLAGS) $(VERSION_LDFLAGS)" \
		$(GOFLAGS) \
		-o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 \
		./cmd/rds-csi-plugin
	@echo "Binary created: $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64"

.PHONY: build-all
build-all: build-darwin-amd64 build-darwin-arm64 build-linux-amd64 build-linux-arm64
	@echo ""
	@echo "All binaries built successfully!"
	@ls -lh $(BUILD_DIR)/

.PHONY: docker
docker:
	@echo "Building Docker image: $(IMAGE)"
	docker build \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg GIT_TAG=$(GIT_TAG) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(IMAGE) \
		-f $(DOCKERFILE) \
		.
	@echo "Image built: $(IMAGE)"

.PHONY: docker-push
docker-push: docker
	@echo "Pushing Docker image: $(IMAGE)"
	docker push $(IMAGE)

.PHONY: docker-multiarch
docker-multiarch:
	@echo "Building multi-arch Docker images: $(IMAGE)"
	docker buildx build \
		--platform linux/amd64,linux/arm64 \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg GIT_TAG=$(GIT_TAG) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(IMAGE) \
		-f $(DOCKERFILE) \
		--push \
		.

.PHONY: test
test:
	@echo "Running unit tests..."
	@echo "Go version: $$(go version)"
	@echo "Test packages:"
	@go list -f '{{.ImportPath}}: {{.TestGoFiles}}' ./pkg/... | grep -v '\[\]'
	@echo ""
	go test -v -race -timeout 5m -json ./pkg/... 2>&1 | tee /tmp/test-output.json || (echo "=== Test output ===" && cat /tmp/test-output.json && exit 1)

.PHONY: test-coverage
test-coverage:
	@echo "Running tests with coverage..."
	go test -v -race -coverprofile=coverage.out -covermode=atomic ./pkg/...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# Coverage enforcement
.PHONY: test-coverage-check install-coverage-tool

# Install go-test-coverage tool
install-coverage-tool:
	@echo "Installing go-test-coverage..."
	go install github.com/vladopajic/go-test-coverage/v2@latest

# Run tests with coverage and check thresholds
test-coverage-check: install-coverage-tool
	@echo "Running tests with coverage..."
	go test -coverprofile=coverage.out -covermode=atomic ./pkg/...
	@echo "Checking coverage thresholds..."
	go-test-coverage --config=.go-test-coverage.yml --profile=coverage.out
	@echo "Coverage check passed!"

# Generate coverage report without enforcement (for local development)
test-coverage-report:
	go test -coverprofile=coverage.out -covermode=atomic ./pkg/...
	go tool cover -func=coverage.out
	@echo ""
	@echo "For HTML report: go tool cover -html=coverage.out"

# Integration Testing
.PHONY: test-integration
test-integration:
	@echo "Running integration tests with mock RDS..."
	go test -v -race -timeout 10m ./test/integration/...

# E2E tests with Ginkgo
.PHONY: e2e-test
e2e-test:
	@echo "Running E2E tests..."
	go test -v ./test/e2e/... -count=1 -timeout 10m
	@echo "E2E tests completed"

# E2E tests with verbose Ginkgo output
.PHONY: e2e-test-verbose
e2e-test-verbose:
	@echo "Running E2E tests (verbose)..."
	go test -v ./test/e2e/... -ginkgo.v -count=1 -timeout 10m
	@echo "E2E tests completed"

# CSI Sanity Tests
.PHONY: test-sanity
test-sanity:
	@echo "Running CSI sanity tests..."
	@if [ ! -x test/sanity/run-sanity-tests.sh ]; then \
		chmod +x test/sanity/run-sanity-tests.sh; \
	fi
	@test/sanity/run-sanity-tests.sh

.PHONY: test-sanity-mock
test-sanity-mock:
	@echo "Running CSI sanity tests with mock RDS..."
	go test -v -race -timeout 10m ./test/sanity/...

.PHONY: test-sanity-real
test-sanity-real: build-local
	@echo "Running CSI sanity tests with real RDS..."
	@if [ -z "$(RDS_ADDRESS)" ]; then \
		echo "ERROR: RDS_ADDRESS environment variable must be set"; \
		echo "Example: make test-sanity-real RDS_ADDRESS=192.168.88.1 RDS_SSH_KEY=~/.ssh/id_rsa"; \
		exit 1; \
	fi
	@if [ -z "$(RDS_SSH_KEY)" ]; then \
		echo "ERROR: RDS_SSH_KEY environment variable must be set"; \
		exit 1; \
	fi
	@test/sanity/run-sanity-tests.sh

# Docker Compose Testing
.PHONY: test-docker
test-docker:
	@echo "Running all tests in Docker Compose..."
	docker-compose -f docker-compose.test.yml up --abort-on-container-exit --build
	docker-compose -f docker-compose.test.yml down -v

.PHONY: test-docker-sanity
test-docker-sanity:
	@echo "Running CSI sanity tests in Docker Compose..."
	docker-compose -f docker-compose.test.yml up csi-sanity --abort-on-container-exit --build
	docker-compose -f docker-compose.test.yml down -v

.PHONY: test-docker-integration
test-docker-integration:
	@echo "Running integration tests in Docker Compose..."
	docker-compose -f docker-compose.test.yml up integration-tests --abort-on-container-exit --build
	docker-compose -f docker-compose.test.yml down -v

# Legacy alias
.PHONY: sanity
sanity: test-sanity-mock

.PHONY: lint
lint:
	@echo "Running linters..."
	@if ! command -v golangci-lint &> /dev/null; then \
		echo "Installing golangci-lint v2.1.5..."; \
		curl -sSfL https://github.com/golangci/golangci-lint/releases/download/v2.1.5/golangci-lint-2.1.5-linux-amd64.tar.gz | \
		tar -xz -C /tmp && \
		mkdir -p $(shell go env GOPATH)/bin && \
		mv /tmp/golangci-lint-2.1.5-linux-amd64/golangci-lint $(shell go env GOPATH)/bin/golangci-lint && \
		rm -rf /tmp/golangci-lint-2.1.5-linux-amd64; \
	fi
	golangci-lint run --timeout 5m

.PHONY: fmt
fmt:
	@echo "Formatting Go code..."
	go fmt ./...
	@echo "Running goimports..."
	@if ! command -v goimports &> /dev/null; then \
		echo "Installing goimports..."; \
		go install golang.org/x/tools/cmd/goimports@latest; \
	fi
	goimports -w -local git.srvlab.io/whiskey/rds-csi-driver .

.PHONY: vet
vet:
	@echo "Running go vet..."
	go vet ./...

.PHONY: verify-helm
verify-helm:
	@echo "Verifying Helm chart rendering..."
	helm lint deploy/helm/rds-csi-driver
	helm template rds-csi deploy/helm/rds-csi-driver --namespace rds-csi > /tmp/rds-csi-helm-default.yaml
	@if grep -q -- "-node-orphan-cleanup" /tmp/rds-csi-helm-default.yaml; then \
		echo "ERROR: node orphan cleanup rendered in default Helm values"; \
		exit 1; \
	fi
	helm template rds-csi deploy/helm/rds-csi-driver --namespace rds-csi \
		--set node.orphanCleanup.enabled=true \
		--set node.orphanCleanup.dryRun=false \
		--set node.orphanCleanup.maxDisconnects=2 > /tmp/rds-csi-helm-node-orphan-cleanup.yaml
	grep -q -- "-node-orphan-cleanup" /tmp/rds-csi-helm-node-orphan-cleanup.yaml
	grep -q -- "-node-orphan-cleanup-dry-run=false" /tmp/rds-csi-helm-node-orphan-cleanup.yaml
	grep -q -- "-node-orphan-cleanup-max-disconnects=2" /tmp/rds-csi-helm-node-orphan-cleanup.yaml
	helm template nvmet-zol deploy/helm/rds-csi-driver --namespace nvmet-csi \
		-f deploy/helm/rds-csi-driver/ci/nvmet-zol-values.yaml > /tmp/nvmet-zol-helm.yaml
	grep -q -- "name: nvmet.csi.whiskey.works" /tmp/nvmet-zol-helm.yaml
	grep -q -- "provisioner: nvmet.csi.whiskey.works" /tmp/nvmet-zol-helm.yaml
	grep -q -- "-driver-name=nvmet.csi.whiskey.works" /tmp/nvmet-zol-helm.yaml
	grep -q -- "name: host-nvmet-configfs" /tmp/nvmet-zol-helm.yaml
	@if grep -q -- "-rds-address" /tmp/nvmet-zol-helm.yaml; then \
		echo "ERROR: RDS address rendered for ZoL-only Helm install"; \
		exit 1; \
	fi
	@if grep -q -- "name: rds-credentials" /tmp/nvmet-zol-helm.yaml; then \
		echo "ERROR: RDS credentials rendered for ZoL-only Helm install"; \
		exit 1; \
	fi

.PHONY: verify
verify: fmt vet lint test verify-helm
	@echo "All verification checks passed!"

.PHONY: mod-tidy
mod-tidy:
	@echo "Tidying Go modules..."
	go mod tidy

.PHONY: mod-vendor
mod-vendor: mod-tidy
	@echo "Vendoring dependencies..."
	go mod vendor

.PHONY: clean
clean:
	@echo "Cleaning build artifacts..."
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html
	rm -rf vendor/
	@echo "Clean complete."

.PHONY: install-tools
install-tools:
	@echo "Installing development tools..."
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install golang.org/x/tools/cmd/goimports@latest
	@echo "Tools installed."

.PHONY: install-sanity
install-sanity:
	@echo "Installing CSI sanity test tool..."
	go install github.com/kubernetes-csi/csi-test/v5/cmd/csi-sanity@latest
	@echo "CSI sanity tool installed."

# Deployment targets
.PHONY: deploy
deploy:
	@echo "Deploying to Kubernetes..."
	kubectl apply -f deploy/kubernetes/

.PHONY: deploy-helm
deploy-helm:
	@echo "Installing via Helm..."
	helm upgrade --install rds-csi \
		deploy/helm/rds-csi-driver \
		--namespace kube-system \
		--create-namespace

.PHONY: undeploy
undeploy:
	@echo "Removing from Kubernetes..."
	kubectl delete -f deploy/kubernetes/ --ignore-not-found=true

.PHONY: logs-controller
logs-controller:
	kubectl logs -n kube-system -l app=rds-csi-controller -c rds-csi-plugin -f

.PHONY: logs-node
logs-node:
	kubectl logs -n kube-system -l app=rds-csi-node -c rds-csi-plugin -f

# Version info
.PHONY: version
version:
	@echo "Version:     $(GIT_TAG)"
	@echo "Git Commit:  $(GIT_COMMIT)"
	@echo "Build Date:  $(BUILD_DATE)"

# CI targets
.PHONY: ci-build
ci-build: mod-tidy lint test build

.PHONY: ci-image
ci-image: docker

.PHONY: ci-release
ci-release: verify docker-multiarch
	@echo "Release build complete"
