#!/usr/bin/env bash
# Test script to determine what Gitea Actions API support is available

set -e

GITEA_HOST="${GITEA_HOST:-https://gitea.whiskey.works}"
GITEA_TOKEN="${GITEA_TOKEN:-}"

if [ -z "$GITEA_TOKEN" ]; then
    echo "❌ GITEA_TOKEN not set. Export it first:"
    echo "   export GITEA_TOKEN='your-token'"
    exit 1
fi

echo "🔍 Probing Gitea Actions API support..."
echo "   Host: $GITEA_HOST"
echo "   Repo: whiskey/nvmet-csi"
echo

# Test 1: Gitea version
echo "1️⃣  Checking Gitea version..."
VERSION=$(curl -s -H "Authorization: token $GITEA_TOKEN" \
    "$GITEA_HOST/api/v1/version" | jq -r '.version // "unknown"')
echo "   Version: $VERSION"
echo

# Test 2: Check if Actions is enabled on repo
echo "2️⃣  Checking if Actions is enabled on repo..."
HAS_ACTIONS=$(curl -s -H "Authorization: token $GITEA_TOKEN" \
    "$GITEA_HOST/api/v1/repos/whiskey/nvmet-csi" | jq -r '.has_actions // false')
echo "   has_actions: $HAS_ACTIONS"
echo

# Test 3: Try /actions/runs endpoint
echo "3️⃣  Testing /actions/runs endpoint..."
RUNS_RESPONSE=$(curl -s -w "\n%{http_code}" -H "Authorization: token $GITEA_TOKEN" \
    "$GITEA_HOST/api/v1/repos/whiskey/nvmet-csi/actions/runs")
RUNS_CODE=$(echo "$RUNS_RESPONSE" | tail -n 1)
RUNS_BODY=$(echo "$RUNS_RESPONSE" | sed '$d')
echo "   HTTP Status: $RUNS_CODE"
if [ "$RUNS_CODE" = "200" ]; then
    echo "   ✅ Endpoint available!"
    echo "$RUNS_BODY" | jq -r '
        if .workflow_runs then
            "   Found \(.total_count // (.workflow_runs | length)) runs"
        else
            "   Response: " + (. | tostring)
        end
    '
elif [ "$RUNS_CODE" = "404" ]; then
    echo "   ❌ Endpoint not found (404)"
    echo "   This means your Gitea version doesn't have Actions API support yet"
else
    echo "   ⚠️  Unexpected status code"
    echo "$RUNS_BODY" | jq -C '.' 2>/dev/null || echo "$RUNS_BODY"
fi
echo

# Test 4: Try /actions/workflows endpoint
echo "4️⃣  Testing /actions/workflows endpoint..."
WORKFLOWS_RESPONSE=$(curl -s -w "\n%{http_code}" -H "Authorization: token $GITEA_TOKEN" \
    "$GITEA_HOST/api/v1/repos/whiskey/nvmet-csi/actions/workflows")
WORKFLOWS_CODE=$(echo "$WORKFLOWS_RESPONSE" | tail -n 1)
WORKFLOWS_BODY=$(echo "$WORKFLOWS_RESPONSE" | sed '$d')
echo "   HTTP Status: $WORKFLOWS_CODE"
if [ "$WORKFLOWS_CODE" = "200" ]; then
    echo "   ✅ Endpoint available!"
    echo "$WORKFLOWS_BODY" | jq -r '
        if .workflows then
            "   Found \(.total_count // (.workflows | length)) workflows"
        else
            "   Response: " + (. | tostring)
        end
    '
elif [ "$WORKFLOWS_CODE" = "404" ]; then
    echo "   ❌ Endpoint not found (404)"
else
    echo "   ⚠️  Unexpected status code"
fi
echo

# Test 5: Try alternate endpoint patterns
echo "5️⃣  Testing alternate endpoint patterns..."

# Some Gitea versions might use different paths
for path in "actions/tasks" "actionRuns" "actions/action-runs"; do
    echo "   Trying /$path..."
    STATUS=$(curl -s -o /dev/null -w "%{http_code}" -H "Authorization: token $GITEA_TOKEN" \
        "$GITEA_HOST/api/v1/repos/whiskey/nvmet-csi/$path")
    if [ "$STATUS" = "200" ]; then
        echo "      ✅ Found! ($STATUS)"
    else
        echo "      ❌ Not found ($STATUS)"
    fi
done
echo

# Test 6: Check web UI for Actions tab
echo "6️⃣  Web UI check..."
echo "   Visit: $GITEA_HOST/whiskey/nvmet-csi/actions"
echo "   If you see an Actions tab with workflow runs, the feature exists but may not have full API support yet."
echo

# Summary
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "📊 Summary:"
echo
if [ "$RUNS_CODE" = "200" ]; then
    echo "✅ Your Gitea instance HAS Actions API support!"
    echo "   The gitea-actions script should work."
    echo
    echo "   Try: ./scripts/gitea-actions run list"
elif [ "$RUNS_CODE" = "404" ]; then
    echo "❌ Your Gitea instance DOES NOT have Actions API support yet."
    echo
    echo "   Current version: $VERSION"
    echo "   Required: Gitea 1.21+ with PR #35382 merged"
    echo
    echo "   Workarounds:"
    echo "   1. Check the web UI at $GITEA_HOST/whiskey/nvmet-csi/actions"
    echo "   2. Upgrade Gitea to latest version"
    echo "   3. Use the web interface for now"
    echo "   4. Contribute Actions API support to gitea-mcp project!"
else
    echo "⚠️  Inconclusive results. Manual investigation needed."
    echo "   Status code: $RUNS_CODE"
fi
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
