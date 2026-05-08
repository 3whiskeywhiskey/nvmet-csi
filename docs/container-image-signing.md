# Container Image Signing and Public Release Guide

This document describes recommended practices for signing container images and publishing releases of the nvmet-csi driver.

## Overview

Container image signing provides cryptographic verification that images are authentic and haven't been tampered with. This is critical for production deployments where supply chain security is a concern.

## Tools and Technologies

### Cosign (Recommended)

[Cosign](https://github.com/sigstore/cosign) is the recommended tool for signing container images. It integrates with GitHub Actions, supports keyless signing with OIDC, and works with OCI registries.

**Key Features:**
- Keyless signing using Sigstore/Fulcio (no key management)
- Traditional key-based signing (for air-gapped environments)
- Transparency log (Rekor) for audit trail
- Policy enforcement with Cosign verify
- Integration with admission controllers (Kyverno, OPA Gatekeeper)

**Installation:**
```bash
# macOS
brew install sigstore/tap/cosign

# Linux
wget https://github.com/sigstore/cosign/releases/latest/download/cosign-linux-amd64
chmod +x cosign-linux-amd64
sudo mv cosign-linux-amd64 /usr/local/bin/cosign

# Verify installation
cosign version
```

### Alternatives

- **Docker Content Trust (DCT)**: Built into Docker, uses Notary
- **Red Hat signing**: For RHEL/OpenShift environments
- **AWS Signer**: For AWS-specific deployments

## Signing Methods

### Method 1: Keyless Signing with GitHub Actions (Recommended for Public Releases)

This approach uses GitHub's OIDC provider for identity and doesn't require managing private keys.

**Advantages:**
- No key management or rotation required
- Automatic identity binding to GitHub repository
- Transparency log provides audit trail
- Free and open source

**GitHub Actions Workflow:**

```yaml
name: Build and Sign Container Image

on:
  push:
    tags:
      - 'v*'

env:
  REGISTRY: ghcr.io
  IMAGE_NAME: ${{ github.repository }}

jobs:
  build-and-sign:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write
      id-token: write  # Required for cosign keyless signing

    steps:
      - name: Checkout code
        uses: actions/checkout@v4

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Log in to GitHub Container Registry
        uses: docker/login-action@v3
        with:
          registry: ${{ env.REGISTRY }}
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Extract metadata
        id: meta
        uses: docker/metadata-action@v5
        with:
          images: ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}
          tags: |
            type=semver,pattern={{version}}
            type=semver,pattern={{major}}.{{minor}}
            type=sha

      - name: Build and push image
        id: build
        uses: docker/build-push-action@v5
        with:
          context: .
          push: true
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          platforms: linux/amd64,linux/arm64

      - name: Install cosign
        uses: sigstore/cosign-installer@v3

      - name: Sign the container image
        env:
          DIGEST: ${{ steps.build.outputs.digest }}
          TAGS: ${{ steps.meta.outputs.tags }}
        run: |
          echo "Signing image with digest: ${DIGEST}"
          for tag in ${TAGS}; do
            echo "Signing ${tag}@${DIGEST}"
            cosign sign --yes "${tag}@${DIGEST}"
          done

      - name: Generate SBOM
        uses: anchore/sbom-action@v0
        with:
          image: ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}@${{ steps.build.outputs.digest }}
          format: spdx-json
          output-file: sbom.spdx.json

      - name: Attach SBOM to image
        run: |
          cosign attach sbom --sbom sbom.spdx.json \
            ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}@${{ steps.build.outputs.digest }}

      - name: Sign SBOM
        run: |
          cosign sign --yes \
            ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}:sbom-${{ steps.build.outputs.digest }}
```

**Verification (Users):**

```bash
# Verify signature
cosign verify \
  --certificate-identity-regexp="https://github.com/3whiskeywhiskey/nvmet-csi/.*" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com" \
  ghcr.io/3whiskeywhiskey/nvmet-csi:v0.1.0

# Verify and download SBOM
cosign verify-attestation \
  --type spdx \
  --certificate-identity-regexp="https://github.com/3whiskeywhiskey/nvmet-csi/.*" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com" \
  ghcr.io/3whiskeywhiskey/nvmet-csi:v0.1.0
```

### Method 2: Key-Based Signing (for Air-Gapped Environments)

For environments without internet access or GitHub Actions.

**Generate signing keys:**

```bash
# Generate key pair (store private key securely!)
cosign generate-key-pair

# This creates:
# - cosign.key (private key - PROTECT THIS)
# - cosign.pub (public key - distribute to users)
```

**Sign image:**

```bash
# Sign with private key
export COSIGN_PASSWORD="your-secure-password"
cosign sign --key cosign.key ghcr.io/3whiskeywhiskey/nvmet-csi:v0.1.0
```

**Distribute public key:**

Place `cosign.pub` in your repository at `deploy/cosign.pub` or publish to your website.

**Verification:**

```bash
# Users verify with public key
cosign verify --key cosign.pub ghcr.io/3whiskeywhiskey/nvmet-csi:v0.1.0
```

## Release Process

### 1. Prepare Release

```bash
# Tag release
git tag -a v0.1.0 -m "Release v0.1.0: Initial CSI driver implementation"
git push origin v0.1.0

# This triggers GitHub Actions workflow
# Workflow builds, signs, and publishes images
```

### 2. Verify Release Artifacts

```bash
# Verify image was signed
cosign verify \
  --certificate-identity-regexp="https://github.com/3whiskeywhiskey/nvmet-csi/.*" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com" \
  ghcr.io/3whiskeywhiskey/nvmet-csi:v0.1.0

# Inspect transparency log
rekor-cli search --artifact ghcr.io/3whiskeywhiskey/nvmet-csi:v0.1.0
```

### 3. Create GitHub Release

```bash
# Use GitHub CLI or web interface
gh release create v0.1.0 \
  --title "nvmet-csi driver v0.1.0" \
  --notes "See CHANGELOG.md for details" \
  deploy/kubernetes/*.yaml \
  deploy/helm/nvmet-csi-*.tgz
```

### 4. Publish Helm Chart

```bash
# Sign Helm chart
helm package deploy/helm/nvmet-csi
cosign sign-blob --yes nvmet-csi-0.1.0.tgz > nvmet-csi-0.1.0.tgz.sig

# Publish to Helm repository (e.g., GitHub Pages)
cr upload --owner 3whiskeywhiskey --git-repo nvmet-csi
cr index --owner 3whiskeywhiskey --git-repo nvmet-csi --push
```

## Security Policies

### Image Verification Policy (Kubernetes)

Use admission controllers to enforce signature verification:

**Kyverno Policy:**

```yaml
apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: verify-nvmet-csi-images
spec:
  validationFailureAction: enforce
  background: false
  rules:
    - name: verify-signature
      match:
        any:
          - resources:
              kinds:
                - Pod
      verifyImages:
        - imageReferences:
            - "ghcr.io/3whiskeywhiskey/nvmet-csi:*"
          attestors:
            - entries:
                - keyless:
                    subject: "https://github.com/3whiskeywhiskey/nvmet-csi/.github/workflows/*"
                    issuer: "https://token.actions.githubusercontent.com"
                    rekor:
                      url: https://rekor.sigstore.dev
```

**OPA Gatekeeper with Cosign:**

```rego
package admission

deny[msg] {
  input.request.kind.kind == "Pod"
  container := input.request.object.spec.containers[_]
  not verified(container.image)
  msg := sprintf("Image %v is not signed or signature verification failed", [container.image])
}

verified(image) {
  # Call cosign verify via external data provider
  ...
}
```

## SBOM (Software Bill of Materials)

Generate and attach SBOMs to images for supply chain transparency:

```bash
# Generate SBOM with Syft
syft packages ghcr.io/3whiskeywhiskey/nvmet-csi:v0.1.0 -o spdx-json > sbom.spdx.json

# Attach to image
cosign attach sbom --sbom sbom.spdx.json ghcr.io/3whiskeywhiskey/nvmet-csi:v0.1.0

# Sign SBOM
cosign sign --yes ghcr.io/3whiskeywhiskey/nvmet-csi:sbom-<digest>

# Users can verify and inspect
cosign verify-attestation --type spdx \
  --certificate-identity-regexp="https://github.com/3whiskeywhiskey/nvmet-csi/.*" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com" \
  ghcr.io/3whiskeywhiskey/nvmet-csi:v0.1.0 | jq .
```

## Vulnerability Scanning

Integrate vulnerability scanning into the release process:

```yaml
- name: Scan image for vulnerabilities
  uses: aquasecurity/trivy-action@master
  with:
    image-ref: ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}@${{ steps.build.outputs.digest }}
    format: 'sarif'
    output: 'trivy-results.sarif'
    severity: 'CRITICAL,HIGH'

- name: Upload scan results
  uses: github/codeql-action/upload-sarif@v2
  with:
    sarif_file: 'trivy-results.sarif'

- name: Fail on critical vulnerabilities
  uses: aquasecurity/trivy-action@master
  with:
    image-ref: ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}@${{ steps.build.outputs.digest }}
    exit-code: '1'
    severity: 'CRITICAL'
```

## Best Practices

### 1. Automated Signing

- ✅ Sign all release images automatically in CI/CD
- ✅ Use keyless signing for public releases
- ✅ Store signatures in transparency log
- ❌ Don't commit private keys to repository
- ❌ Don't sign images manually

### 2. Verification

- ✅ Provide verification instructions in README
- ✅ Enforce verification in production via admission controllers
- ✅ Verify images in deployment scripts
- ✅ Document expected certificate identity

### 3. Key Management (if using key-based signing)

- ✅ Store private keys in secrets manager (AWS Secrets Manager, HashiCorp Vault)
- ✅ Rotate keys annually
- ✅ Use hardware security modules (HSM) for production keys
- ✅ Maintain key revocation list
- ❌ Don't share keys across environments
- ❌ Don't store keys in CI/CD configuration

### 4. Release Artifacts

- ✅ Sign container images
- ✅ Sign Helm charts
- ✅ Sign release binaries
- ✅ Generate and sign SBOMs
- ✅ Include checksums (SHA256)
- ✅ Publish signatures alongside artifacts

### 5. Documentation

- ✅ Document verification process in README
- ✅ Provide example verification commands
- ✅ Explain security policies
- ✅ Maintain transparency log URLs
- ✅ Update documentation with each release

## Troubleshooting

### Verification Fails

```bash
# Check certificate identity
cosign verify \
  --certificate-identity="https://github.com/3whiskeywhiskey/nvmet-csi/.github/workflows/release.yml@refs/tags/v0.1.0" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com" \
  ghcr.io/3whiskeywhiskey/nvmet-csi:v0.1.0

# Inspect signature
cosign tree ghcr.io/3whiskeywhiskey/nvmet-csi:v0.1.0

# Check Rekor transparency log
rekor-cli search --artifact ghcr.io/3whiskeywhiskey/nvmet-csi:v0.1.0
rekor-cli get --uuid <UUID-from-search>
```

### Image Not Signed

```bash
# Check if signatures exist
cosign tree ghcr.io/3whiskeywhiskey/nvmet-csi:v0.1.0

# Manually sign if needed
cosign sign --yes ghcr.io/3whiskeywhiskey/nvmet-csi:v0.1.0
```

## Example: Complete Release Workflow

```bash
#!/bin/bash
# release.sh - Complete release process

set -e

VERSION=${1:-}
if [ -z "$VERSION" ]; then
  echo "Usage: $0 <version>"
  exit 1
fi

# 1. Update version in files
sed -i "s/version: .*/version: $VERSION/" deploy/helm/nvmet-csi/Chart.yaml
sed -i "s/appVersion: .*/appVersion: $VERSION/" deploy/helm/nvmet-csi/Chart.yaml

# 2. Commit and tag
git add deploy/helm/nvmet-csi/Chart.yaml
git commit -m "Release $VERSION"
git tag -a "$VERSION" -m "Release $VERSION"

# 3. Push tag (triggers CI/CD)
git push origin "$VERSION"

# 4. Wait for CI/CD to complete
echo "Waiting for GitHub Actions to complete..."
gh run watch

# 5. Verify signed image
echo "Verifying signed image..."
cosign verify \
  --certificate-identity-regexp="https://github.com/3whiskeywhiskey/nvmet-csi/.*" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com" \
  "ghcr.io/3whiskeywhiskey/nvmet-csi:$VERSION"

# 6. Create GitHub release
gh release create "$VERSION" \
  --title "nvmet-csi driver $VERSION" \
  --notes-file "CHANGELOG.md" \
  deploy/kubernetes/*.yaml

echo "Release $VERSION complete!"
```

## References

- [Cosign Documentation](https://docs.sigstore.dev/cosign/overview/)
- [Sigstore Project](https://www.sigstore.dev/)
- [SLSA Framework](https://slsa.dev/)
- [Supply Chain Security Best Practices](https://cloud.google.com/software-supply-chain-security/docs/best-practices)
- [Kubernetes Image Policy Webhook](https://kubernetes.io/docs/reference/access-authn-authz/admission-controllers/#imagepolicywebhook)
- [CNCF Security TAG](https://github.com/cncf/tag-security)

## Next Steps

1. Implement GitHub Actions workflow for automated signing
2. Configure cosign keyless signing with GitHub OIDC
3. Add verification instructions to README.md
4. Set up Kyverno/OPA policies for production clusters
5. Enable SBOM generation and vulnerability scanning
6. Document release process in CONTRIBUTING.md
