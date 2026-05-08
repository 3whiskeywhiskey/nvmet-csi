# CI/CD Status and Monitoring

## GitHub Actions Workflow

**Workflow URL:** https://github.com/3whiskeywhiskey/nvmet-csi/actions

### Current Status

**Dev Branch CI/CD:**
- **Status:** ✅ Fixed and running
- **Last Fix:** Removed csi-sanity from install-tools target
- **Commit:** 501447e

### What the Workflow Does

When you push to the `dev` branch, GitHub Actions automatically:

1. **Verify Code Quality** (runs in parallel):
   - `make verify` - Runs format, vet, lint checks
   - `make test` - Runs unit tests
   - `make test-integration` - Runs integration tests with mock RDS

2. **Build and Push Dev Image** (only if verify passes):
   - Sets up Docker Buildx for multi-arch builds
   - Authenticates to GitHub Container Registry (ghcr.io)
   - Builds for linux/amd64 and linux/arm64
   - Tags with:
     - `ghcr.io/3whiskeywhiskey/nvmet-csi:dev`
     - `ghcr.io/3whiskeywhiskey/nvmet-csi:dev-<git-sha>`
   - Pushes to registry

### Expected Duration

- **Verify Code Quality:** ~3-5 minutes
- **Build and Push:** ~8-12 minutes
- **Total:** ~12-17 minutes

### Monitoring

#### Via GitHub Web UI
```
https://github.com/3whiskeywhiskey/nvmet-csi/actions
```

#### Via GitHub CLI (if installed)
```bash
gh run list --repo 3whiskeywhiskey/nvmet-csi --branch dev
gh run watch --repo 3whiskeywhiskey/nvmet-csi
```

#### Via API (curl)
```bash
# Get latest workflow run status
curl -s -H "Accept: application/vnd.github+json" \
  https://api.github.com/repos/3whiskeywhiskey/nvmet-csi/actions/runs?branch=dev \
  | jq '.workflow_runs[0] | {status, conclusion, created_at, html_url}'
```

### Troubleshooting

#### Build Failing on Verification

**Check logs:**
1. Go to Actions tab
2. Click on the failing run
3. Expand "Verify Code Quality" job
4. Check which step failed

**Common issues:**
- **Linter errors:** Run `make lint` locally to see issues
- **Test failures:** Run `make test` and `make test-integration` locally
- **Format issues:** Run `make fmt` to auto-fix

**Fix locally, then push:**
```bash
make verify  # Should pass locally
git add .
git commit -m "Fix verification issues"
git push github dev
```

#### Build Failing on Docker Push

**Common issues:**
- **Authentication:** GitHub token must have packages:write permission
- **Disk space:** Multi-arch builds require significant space
- **Network issues:** Retry the workflow

**Manual workaround (local build and push):**
```bash
# Login to GitHub Container Registry
echo $GITHUB_TOKEN | docker login ghcr.io -u USERNAME --password-stdin

# Build and push manually
make docker-multiarch IMAGE_TAG=dev
```

#### Image Not Appearing After Successful Build

**Check package visibility:**
1. Go to https://github.com/3whiskeywhiskey?tab=packages
2. Find `nvmet-csi` package
3. Ensure visibility is correct (public or private with access)

**Pull the image to verify:**
```bash
docker pull ghcr.io/3whiskeywhiskey/nvmet-csi:dev
```

### Workflow Triggers

The workflow runs on:
- **Push to dev branch** - Full verification + build + push
- **Pull request to dev** - Verification only (no push)

### After Successful Build

Once CI/CD completes successfully:

1. **Verify image exists:**
   ```bash
   docker pull ghcr.io/3whiskeywhiskey/nvmet-csi:dev
   docker inspect ghcr.io/3whiskeywhiskey/nvmet-csi:dev | jq '.[0].Config.Labels'
   ```

2. **Deploy to cluster:**
   ```bash
   ./scripts/deploy-dev.sh --wait
   ```

3. **Run tests:**
   ```bash
   ./scripts/test-security-fix.sh <fix-number>
   ```

### Manual Override

If you need to skip CI/CD and build locally:

```bash
# Build locally
make build-local

# Build and push manually
make docker-push IMAGE_TAG=dev

# Or build multi-arch
make docker-multiarch IMAGE_TAG=dev
```

### Workflow File Location

The workflow is defined in:
```
.github/workflows/dev.yml
```

To modify the workflow, edit this file and push to dev branch.

---

## Quick Commands

**Check if image is ready:**
```bash
docker pull ghcr.io/3whiskeywhiskey/nvmet-csi:dev && echo "✅ Image ready"
```

**Watch for new image:**
```bash
while true; do
  if docker pull ghcr.io/3whiskeywhiskey/nvmet-csi:dev 2>&1 | grep -q "Downloaded newer image"; then
    echo "✅ New image available!"
    break
  fi
  echo "Waiting for new image... (checking every 30s)"
  sleep 30
done
```

**Check current deployed version:**
```bash
kubectl get deployment nvmet-csi-controller -n kube-system -o jsonpath='{.spec.template.spec.containers[0].image}'
kubectl get daemonset nvmet-csi-node -n kube-system -o jsonpath='{.spec.template.spec.containers[0].image}'
```

---

## Timeline for Security Fix Workflow

For each security fix merge:

1. **Merge and push** (~1 minute)
2. **CI/CD runs** (~12-17 minutes)
3. **Deploy to cluster** (~2-3 minutes)
4. **Run tests** (~5-10 minutes)
5. **Verify and document** (~2-3 minutes)

**Total per fix:** ~22-34 minutes

**For all 11 fixes:** ~4-6 hours (if done sequentially)
