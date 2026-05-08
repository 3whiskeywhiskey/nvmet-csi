# Scripts

Utility scripts for development and CI/CD workflows.

## gitea-actions

A CLI tool for monitoring Gitea Actions workflows (similar to `gh` for GitHub Actions).

### Setup

```bash
export GITEA_TOKEN="your-gitea-token"
export GITEA_HOST="https://gitea.whiskey.works"  # optional, defaults to this
export GITEA_REPO_OWNER="whiskey"  # optional
export GITEA_REPO_NAME="nvmet-csi"   # optional
```

### Usage

```bash
# List recent workflow runs
./scripts/gitea-actions run list

# View details of run #3
./scripts/gitea-actions run view 3

# Watch a running workflow (auto-refresh every 5s)
./scripts/gitea-actions run watch 6

# Get logs for completed run
./scripts/gitea-actions run logs 3

# List all workflows
./scripts/gitea-actions workflow list
```

### Requirements

- **Gitea 1.21+** with Actions API support (PR #35382)
- **jq** - JSON processor (`brew install jq`)
- **curl** - HTTP client (usually pre-installed)

### Testing API Support

Run the test script to check if your Gitea instance supports the Actions API:

```bash
./scripts/test-gitea-actions.sh
```

This will probe the API endpoints and report what's available.

## Why tea doesn't have this

The official `tea` CLI doesn't yet support Actions monitoring because:

1. **Gitea Actions is relatively new** (added in Gitea 1.19, March 2023)
2. **Actions API is newer** (workflow management endpoints added in PR #35382)
3. **tea development hasn't caught up yet** - it still focuses on repos, issues, PRs, releases

This script fills that gap until tea adds native support!
