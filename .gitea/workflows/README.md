# Gitea Actions Workflows

Optimized CI/CD workflows for Gitea Actions with fast feedback and comprehensive testing.

## Strategy

### Quick Build (on push to dev/main)
**File:** `quick-build.yml`
**Trigger:** Push to `dev` or `main` branches
**Duration:** ~5-8 minutes
**Purpose:** Fast multi-arch builds for mixed-architecture deployments

**Runs:**
- ✅ Build binary
- ✅ Quick smoke test (verify binary runs)
- ✅ Build container (linux/amd64, linux/arm64)
- ✅ Uses buildx cache for speed
- ❌ NO tests (saves time)

**When it runs:**
```bash
git push origin dev
git push origin main
```

### Full Test Suite (on PRs)
**File:** `full-test.yml`
**Trigger:** Pull request to `dev` or `main`
**Duration:** ~10-15 minutes
**Purpose:** Comprehensive validation before merge

**Runs:**
- ✅ Code verification (fmt, vet, lint)
- ✅ Unit tests with coverage (60% threshold)
- ✅ CSI sanity tests (15 min timeout)
- ✅ Mock stress tests (concurrent operations)
- ✅ Container build test

**When it runs:**
```bash
tea pr create --title "Feature: xyz" --base dev
```

### Release Build (on tags)
**File:** `release.yml`
**Trigger:** Push tag or manual dispatch
**Duration:** ~20-30 minutes
**Purpose:** Production-ready release artifacts

**Runs:**
- ✅ Full test suite
- ✅ Multi-arch build (amd64, arm64)
- ✅ Create release notes
- ✅ Push to registry (when configured)

**When it runs:**
```bash
git tag v0.9.0
git push origin v0.9.0
```

---

## Configuration Required

### 1. Container Registry

Update the registry settings in all workflows:

```yaml
env:
  REGISTRY: git.whiskey.works          # Your Gitea instance
  IMAGE_NAME: whiskey-works/nvmet-csi          # Your repository
```

**Options:**
- **Gitea built-in registry:** `git.whiskey.works` (requires Gitea 1.17+)
- **External registry:** `registry.example.com`
- **Harbor:** `harbor.example.com`

### 2. Registry Secrets

Add secrets in Gitea UI: **Settings → Secrets**

```bash
REGISTRY_USER=your-username
REGISTRY_TOKEN=your-access-token
```

### 3. Enable Container Push

Once registry is configured:

**In `quick-build.yml`:**
1. Uncomment the login step:
   ```yaml
   - name: Log in to registry
     uses: docker/login-action@v3
     with:
       registry: ${{ env.REGISTRY }}
       username: ${{ secrets.REGISTRY_USER }}
       password: ${{ secrets.REGISTRY_TOKEN }}
   ```

2. Change `push: false` to `push: true`:
   ```yaml
   - name: Build and push multi-arch container
     uses: docker/build-push-action@v5
     with:
       push: true  # Changed from false
   ```

**In `release.yml`:**
```yaml
- name: Log in to registry
  run: ...

- name: Push images
  run: docker push ...
```

### 4. Enable tea CLI for Releases

Install `tea` on your Gitea Actions runner:

```bash
# On the runner host
apt install tea  # or yum, nix, etc.

# Configure tea
tea login add --name local --url https://git.whiskey.works --token <token>
```

Then uncomment in `release.yml`:
```yaml
- name: Create Gitea release
  run: tea release create ...
```

---

## Differences from GitHub Actions

### What Works the Same ✅
- `actions/checkout@v4`
- `actions/setup-go@v5`
- `actions/upload-artifact@v4`
- `docker/setup-qemu-action@v3`
- `docker/setup-buildx-action@v3`
- Basic shell scripts and commands

### What Changed ⚠️
- **codecov:** Removed, using inline coverage check instead
- **GHCR:** Changed to Gitea/custom registry
- **create-release:** Use `tea` CLI instead of GitHub action
- **Registry auth:** Use secrets instead of GITHUB_TOKEN

### What Doesn't Work ❌
- `codecov/codecov-action` - External service
- `actions/create-release` - GitHub-specific API
- GitHub-specific context vars (use Gitea equivalents)

---

## Testing Locally

### Test workflow syntax
```bash
# Install act (Gitea Actions runner)
nix-shell -p act

# Run workflows locally
act -W .gitea/workflows/quick-build.yml
act -W .gitea/workflows/full-test.yml
```

### Test without CI
```bash
# Quick build (what CI does on push)
make build-local
docker build -t test .

# Full test suite (what CI does on PR)
make verify
make test
make test-coverage
go test -v -race -timeout 15m ./test/sanity/...
go test -v -race ./test/mock/... -run TestConcurrent
```

---

## Comparison: Old vs New

### Old (GitHub Actions)
```
Push to dev:
├─ verify (fmt, vet, lint)
├─ unit tests
├─ integration tests
├─ upload coverage to codecov
└─ build + push container
⏱️  ~12-15 minutes

PR:
├─ verify
├─ unit tests
├─ integration tests
├─ sanity tests
├─ build test (multi-arch)
└─ upload artifacts
⏱️  ~15-20 minutes
```

### New (Gitea Actions)
```
Push to dev/main:
├─ build binary
├─ smoke test
└─ build multi-arch container (amd64 + arm64)
⏱️  ~5-8 minutes  (50% faster, with multi-arch!)

PR:
├─ verify + unit tests + coverage
├─ sanity tests
├─ stress tests
├─ build test (multi-arch)
└─ summary
⏱️  ~10-15 minutes  (organized in parallel jobs)
```

---

## Monitoring

### Check workflow status
```bash
# Via tea CLI
tea runs list
tea runs view <run-id>

# Via Gitea UI
https://git.whiskey.works/whiskey-works/nvmet-csi/actions
```

### View logs
```bash
tea runs view <run-id> --log
```

### Cancel running workflow
```bash
tea runs cancel <run-id>
```

---

## Troubleshooting

### "Runner not found"
Check your Gitea instance has Actions enabled and runners registered:
```bash
# On Gitea server
gitea admin runner list
```

### "Permission denied" on docker push
Ensure registry secrets are set correctly in Gitea UI.

### "Timeout" on sanity tests
The 15-minute timeout is intentional. If tests take longer, check for:
- Slow mock RDS server
- Insufficient runner resources
- Deadlocks in tests

### "Coverage below threshold"
Current threshold is 60%. Phase 22-23 work should maintain this.

---

## Future Improvements

- [ ] Add performance benchmarking workflow
- [ ] Add security scanning (trivy, gosec)
- [ ] Add Kubernetes integration tests (requires k3s)
- [ ] Add changelog generation
- [ ] Add Slack/Discord notifications

---

## Migration Checklist

- [x] Create `.gitea/workflows/` directory
- [x] Port workflows from `.github/workflows/`
- [x] Remove codecov dependency
- [x] Simplify push workflows (no tests)
- [x] Keep PR workflows comprehensive
- [ ] Configure registry settings
- [ ] Add registry secrets
- [ ] Uncomment push steps
- [ ] Configure tea CLI
- [ ] Test workflows on Gitea
- [ ] Document runner requirements

---

*Last updated: 2026-02-04*
*See also: `.github/workflows/README.md` for GitHub Actions docs*
