# Multi-stage Dockerfile for nvmet-csi driver
# Creates a minimal container image with the CSI plugin binary

# Stage 1: Build the Go binary (cross-compiles on the build platform;
# CGO is disabled so only the Go toolchain's native cross support is needed)
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache \
    git \
    make \
    bash

# Set working directory
WORKDIR /build

# Copy go.mod and go.sum (if exists)
COPY go.mod go.sum* ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build arguments for versioning
ARG GIT_COMMIT=unknown
ARG GIT_TAG=unknown
ARG BUILD_DATE=unknown

# Target platform provided by buildx (defaults preserve plain docker build)
ARG TARGETOS
ARG TARGETARCH

# Build the binary
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-} go build \
    -ldflags "-s -w \
      -X gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/driver.version=${GIT_TAG} \
      -X gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/driver.gitCommit=${GIT_COMMIT} \
      -X gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/driver.buildDate=${BUILD_DATE}" \
    -o /nvmet-csi-plugin \
    ./cmd/nvmet-csi-plugin

# Stage 2: Create minimal runtime image
FROM alpine:3.21

# Install runtime dependencies
RUN apk add --no-cache \
    ca-certificates \
    e2fsprogs \
    e2fsprogs-extra \
    xfsprogs \
    blkid \
    nvme-cli \
    openssh-client \
    util-linux

# Copy binary from builder
COPY --from=builder /nvmet-csi-plugin /usr/local/bin/nvmet-csi-plugin

# Create directories for CSI socket and staging
RUN mkdir -p /var/lib/kubelet/plugins/nvmet.csi.whiskey.works \
             /var/lib/kubelet/pods \
             /csi

# Add labels
LABEL org.opencontainers.image.source="https://gitea.whiskey.works/whiskey-works/nvmet-csi"
LABEL org.opencontainers.image.description="CSI driver for NVMe/TCP targets backed by Linux nvmet or RouterOS/RDS"
LABEL org.opencontainers.image.licenses="Apache-2.0"

# Entrypoint
ENTRYPOINT ["/usr/local/bin/nvmet-csi-plugin"]
