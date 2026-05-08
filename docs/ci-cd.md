# CI/CD Documentation

## Overview

The nvmet-csi driver uses GitHub Actions for continuous integration and deployment. The workflows are designed to:

1. Verify code quality on every PR
2. Build and push development images on `dev` branch
3. Build and push production images with `:latest` tag on `main` branch
4. Automatically create versioned releases based on conventional commits

## Workflows

### 1. Pull Request CI (`pr.yml`)

**Triggers:** All pull requests to `main` or `dev` branches

**Purpose:** Verify code quality before merging

**Steps:**
- Run linters and formatters (`make verify`)
- Run unit tests (`make test`)
- Run integration tests (`make test-integration`)
- Build Docker image (no push) to verify it builds correctly
- Upload code coverage to Codecov

### 2. Dev Branch CI/CD (`dev.yml`)

**Triggers:** Pushes to `dev` branch

**Purpose:** Continuous deployment of development builds

**Images Published:**
- `ghcr.io/3whiskeywhiskey/nvmet-csi:dev`
- `ghcr.io/3whiskeywhiskey/nvmet-csi:dev-<commit-sha>`

**Steps:**
- Run full verification suite
- Build multi-arch images (linux/amd64, linux/arm64)
- Push to GitHub Container Registry

### 3. Main Branch CI/CD (`main.yml`)

**Triggers:** Pushes to `main` branch (typically from merged PRs)

**Purpose:** Keep `:latest` tag up-to-date with main branch

**Images Published:**
- `ghcr.io/3whiskeywhiskey/nvmet-csi:latest`
- `ghcr.io/3whiskeywhiskey/nvmet-csi:<version>` (current version from git tags)
- `ghcr.io/3whiskeywhiskey/nvmet-csi:main-<commit-sha>`

**Steps:**
- Run full verification suite
- Detect current version from git tags
- Build multi-arch images
- Push to GitHub Container Registry with `:latest` tag

### 4. Release Workflow (`release.yml`)

**Triggers:**
- Automatically on pushes to `main` (when release-worthy commits are detected)
- Manually via workflow_dispatch (GitHub Actions UI)

**Purpose:** Create versioned releases with automatic semantic versioning

**Semantic Versioning Rules:**
- **Major bump** (x.0.0): Breaking changes detected via:
  - Commit messages with `!` suffix (e.g., `feat!: remove old API`)
  - Commit messages with `BREAKING CHANGE:` in body
- **Minor bump** (x.y.0): New features detected via:
  - Commit messages starting with `feat:`
- **Patch bump** (x.y.z): Bug fixes detected via:
  - Commit messages starting with `fix:`

**Images Published:**
- `ghcr.io/3whiskeywhiskey/nvmet-csi:latest`
- `ghcr.io/3whiskeywhiskey/nvmet-csi:v1.2.3` (full version)
- `ghcr.io/3whiskeywhiskey/nvmet-csi:v1.2` (major.minor)
- `ghcr.io/3whiskeywhiskey/nvmet-csi:v1` (major only)

**Steps:**
1. Check commits since last tag to determine if release is needed
2. Calculate new version based on conventional commits
3. Generate changelog from commits
4. Create and push git tag
5. Create GitHub Release with changelog
6. Build and push multi-arch Docker images with version tags

## Conventional Commits

This project follows the [Conventional Commits](https://www.conventionalcommits.org/) specification for automatic versioning.

### Commit Message Format

```
<type>[optional scope]: <description>

[optional body]

[optional footer(s)]
```

### Commit Types

- `feat:` - New feature (triggers **minor** version bump)
- `fix:` - Bug fix (triggers **patch** version bump)
- `feat!:` or `fix!:` - Breaking change (triggers **major** version bump)
- `docs:` - Documentation changes
- `test:` - Adding or updating tests
- `chore:` - Maintenance tasks
- `refactor:` - Code refactoring
- `perf:` - Performance improvements
- `build:` - Build system changes
- `ci:` - CI/CD changes

### Examples

#### Feature (Minor Bump: v1.2.0 → v1.3.0)
```
feat: Add support for volume expansion

Implement ControllerExpandVolume RPC to allow resizing of mounted volumes.
```

#### Bug Fix (Patch Bump: v1.2.0 → v1.2.1)
```
fix: Prevent deletion of files referenced by active PVs

Add validation to ensure orphan reconciler doesn't delete files that are
still in use by PersistentVolumes.
```

#### Breaking Change (Major Bump: v1.2.0 → v2.0.0)
```
feat!: Change volume ID format to use UUID

BREAKING CHANGE: Volume IDs now use UUID format instead of sequential
integers. Existing volumes will need to be migrated.
```

or

```
feat: Change volume ID format to use UUID

The volume ID format has been changed from sequential integers to UUIDs
for better uniqueness guarantees.

BREAKING CHANGE: Existing volumes using integer IDs are no longer compatible.
```

## Manual Release Process

### Option 1: Via GitHub Actions UI

1. Go to the "Actions" tab in the GitHub repository
2. Select "Release" workflow from the left sidebar
3. Click "Run workflow" button
4. Choose version bump type:
   - `auto` - Automatically detect from commits (recommended)
   - `major` - Force major version bump (x.0.0)
   - `minor` - Force minor version bump (x.y.0)
   - `patch` - Force patch version bump (x.y.z)
5. Click "Run workflow"

### Option 2: Via Git Tags (Manual)

```bash
# Get current version
git describe --tags --abbrev=0

# Create new tag (replace v1.2.3 with your new version)
git tag -a v1.2.3 -m "Release v1.2.3"

# Push tag to trigger release
git push origin v1.2.3
```

Note: Manual tagging will **not** trigger the automatic release workflow. You'll need to create the GitHub Release manually.

## Version Detection

The workflows detect version information in the following priority:

1. **Git Tags**: Primary source of version numbers
2. **Fallback**: If no tags exist, defaults to `v0.0.0`

The version is embedded into the binary at build time via ldflags:
- `pkg/driver.version` - Full version tag (e.g., `v1.2.3`)
- `pkg/driver.gitCommit` - Short commit SHA
- `pkg/driver.buildDate` - ISO 8601 build timestamp

## Image Tags

### Development Images
- `dev` - Latest commit on dev branch
- `dev-<sha>` - Specific commit on dev branch

### Production Images
- `latest` - Latest commit on main branch
- `v1.2.3` - Specific version release
- `v1.2` - Latest patch version for 1.2.x
- `v1` - Latest minor version for 1.x.x
- `main-<sha>` - Specific commit on main branch

## Registry

All images are published to GitHub Container Registry:
- **Registry**: `ghcr.io`
- **Organization**: `3whiskeywhiskey`
- **Repository**: `nvmet-csi`
- **Full Image**: `ghcr.io/3whiskeywhiskey/nvmet-csi:<tag>`

## Authentication

The workflows use `GITHUB_TOKEN` for authentication, which is automatically provided by GitHub Actions. No additional secrets are required.

## Caching

Docker builds use GitHub Actions cache to speed up builds:
- **Type**: `gha` (GitHub Actions cache)
- **Mode**: `max` (cache all layers)

This significantly reduces build times for subsequent builds.

## Multi-Architecture Support

All images are built for multiple architectures using Docker Buildx:
- `linux/amd64` - Intel/AMD 64-bit
- `linux/arm64` - ARM 64-bit (Apple Silicon, ARM servers)

## Adding a New Test Job

When you add a new test type that should gate merges, add a job to `.github/workflows/pr.yml`.

### Template

```yaml
  new-test-job:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Run tests
        run: make test-new-type
      - name: Upload logs on failure
        if: failure()
        uses: actions/upload-artifact@v4
        with:
          name: new-test-logs
          path: /tmp/test-output/
```

### Checklist for New Test Jobs

1. **Makefile target**: Add `test-new-type` target in Makefile before adding CI job
2. **Local verification**: Ensure `make test-new-type` passes locally
3. **Timeout**: Set job timeout (default: 10 minutes; extend for integration tests)
4. **Artifacts**: Always upload logs on failure for debugging
5. **Dependencies**: If test needs tools (nvme-cli, etc.), install in a setup step
6. **PR scope**: Run on all PRs (not just main) for early feedback

### Decision Guide

| Question | Yes | No |
|----------|-----|-----|
| Does it test spec compliance? | Run on all PRs | - |
| Does it need real hardware? | Manual only, don't add to CI | Run in CI |
| Does it take >10 minutes? | Consider running only on main | Run on all PRs |
| Does it test new capability? | Add as separate job (parallel) | Add to existing test job |

## Interpreting Test Results

When a CI job fails, use this guide to understand what went wrong and how to fix it.

### verify job
**What it tests:** Code formatting, static analysis, linting, unit tests, integration tests
**Common failure patterns:**

| Pattern | Cause | Fix |
|---------|-------|-----|
| `golangci-lint errors found` | New code violates linter rules | Run `make lint` locally, fix reported issues |
| `FAIL: TestXxx` | Unit test failure | Run `make test` locally, check specific test |
| `gofmt -d` shows diff | Code not formatted | Run `make fmt` |
| `go vet` findings | Suspicious code patterns | Address vet warnings |

### sanity-tests job
**What it tests:** CSI specification compliance using official csi-test package
**Common failure patterns:**

| Pattern | Cause | Fix |
|---------|-------|-----|
| `idempotency check failed` | CreateVolume/DeleteVolume not idempotent | Check volume name handling for duplicate requests |
| `capability not supported` | Driver reports capability but doesn't implement it | Update GetCapabilities or implement the RPC |
| `unexpected error code` | Wrong gRPC status code returned | Check CSI spec for required error codes per RPC |

**Debugging:** Download `sanity-test-logs` artifact from failed run for full output.

### e2e job
**What it tests:** Full-stack driver behavior with mock RDS
**Common failure patterns:**

| Pattern | Cause | Fix |
|---------|-------|-----|
| `Ginkgo timed out` | Eventually/Consistently timeout too short | Increase timeout in test, or check for deadlock |
| `expected volume to be bound` | Volume provisioning failed in mock | Check mock RDS state, verify CreateVolume logic |
| `block volume test failed` | Block volume capability issue | Verify BLOCK_VOLUME capability in driver |

### build-test job
**What it tests:** Docker image builds correctly for linux/amd64 and linux/arm64
**Common failure patterns:**

| Pattern | Cause | Fix |
|---------|-------|-----|
| `go build failed` | Compilation error | Fix build errors, ensure `make build` works locally |
| `docker buildx failed` | Dockerfile issue | Test with `make docker` locally |

## Troubleshooting

### Release Not Created

**Problem**: Pushed to main but no release was created

**Solution**: Check that commits follow conventional commit format:
```bash
# View recent commits
git log --oneline -10

# Should see commits like:
# feat: Add new feature
# fix: Fix bug
```

If commits don't follow the format, no release will be created automatically. You can trigger a manual release via the GitHub Actions UI.

### Version Not Detected

**Problem**: Build shows version as `v0.0.0` or `unknown`

**Solution**: Ensure git tags are fetched with full history:
```bash
git fetch --tags --unshallow
git describe --tags --abbrev=0
```

In workflows, this is handled by:
```yaml
- uses: actions/checkout@v4
  with:
    fetch-depth: 0  # Full history for version detection
```

### Image Not Published

**Problem**: Workflow succeeded but image not visible in GitHub Container Registry

**Solution**:
1. Check that the workflow has `packages: write` permission
2. Verify authentication succeeded in workflow logs
3. Check image visibility settings in GitHub Package settings (should be public)

### Build Failed

**Problem**: Docker build fails with error

**Solution**:
1. Test build locally: `make docker`
2. Check Dockerfile syntax and dependencies
3. Verify build args are correctly passed
4. Review workflow logs for specific error messages

## Best Practices

### Commit Messages
1. Always use conventional commit format
2. Include scope when applicable: `feat(controller): add feature`
3. Use imperative mood: "add" not "added" or "adds"
4. Keep subject line under 72 characters
5. Provide detailed body for complex changes

### Releases
1. Let automatic versioning handle releases when possible
2. Only use manual releases for special cases (hotfixes, first release)
3. Review changelog before finalizing release
4. Test `:latest` image before creating versioned release

### Tags
1. Always use `v` prefix: `v1.2.3` not `1.2.3`
2. Follow semantic versioning strictly
3. Don't delete tags unless absolutely necessary
4. Use annotated tags, not lightweight tags

## Future Enhancements

Potential improvements to the CI/CD pipeline:

- [ ] Add e2e tests against a real Kubernetes cluster
- [ ] Implement Helm chart versioning and publishing
- [ ] Add security scanning (Trivy, Snyk)
- [ ] Set up automated dependency updates (Dependabot, Renovate)
- [ ] Add performance benchmarking
- [ ] Create Gitea mirror and Actions compatibility
- [ ] Add artifact signing with Sigstore/cosign
- [ ] Implement SBOM (Software Bill of Materials) generation

## Related Documentation

- [Testing Guide](TESTING.md) - How to run tests locally and contribute new tests
- [Hardware Validation](HARDWARE_VALIDATION.md) - Manual testing procedures for production hardware
