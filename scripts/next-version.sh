#!/bin/bash
# Script to determine what the next version will be based on commits since last tag
# Usage: ./scripts/next-version.sh

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}=== nvmet-csi driver - Next Version Calculator ===${NC}\n"

# Get the latest tag
LATEST_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "")

if [ -z "$LATEST_TAG" ]; then
    echo -e "${YELLOW}No previous tags found${NC}"
    echo -e "First release will be: ${GREEN}v0.1.0${NC}"
    echo ""
    echo "Recent commits:"
    git log --oneline -10
    exit 0
fi

echo -e "Current version: ${GREEN}${LATEST_TAG}${NC}"

# Get commits since last tag
COMMITS=$(git log ${LATEST_TAG}..HEAD --pretty=format:"%s")

if [ -z "$COMMITS" ]; then
    echo -e "${YELLOW}No new commits since last tag${NC}"
    echo -e "Current version remains: ${GREEN}${LATEST_TAG}${NC}"
    exit 0
fi

echo ""
echo "Commits since ${LATEST_TAG}:"
echo "---"
echo "$COMMITS" | while IFS= read -r line; do
    echo "  $line"
done
echo "---"
echo ""

# Remove 'v' prefix for version manipulation
VERSION=${LATEST_TAG#v}
IFS='.' read -r MAJOR MINOR PATCH <<< "$VERSION"

# Determine bump type
RELEASE_TYPE="none"

# Check for breaking changes (major version bump)
if echo "$COMMITS" | grep -qE "^(feat|fix|chore|refactor|perf|test|build|ci|docs|style)(\(.+\))?!:"; then
    RELEASE_TYPE="major"
    echo -e "${RED}🚨 Breaking changes detected!${NC}"
elif echo "$COMMITS" | grep -qE "BREAKING CHANGE:"; then
    RELEASE_TYPE="major"
    echo -e "${RED}🚨 Breaking changes detected!${NC}"
# Check for features (minor version bump)
elif echo "$COMMITS" | grep -qE "^feat(\(.+\))?:"; then
    RELEASE_TYPE="minor"
    echo -e "${GREEN}🚀 New features detected${NC}"
# Check for fixes (patch version bump)
elif echo "$COMMITS" | grep -qE "^fix(\(.+\))?:"; then
    RELEASE_TYPE="patch"
    echo -e "${YELLOW}🐛 Bug fixes detected${NC}"
else
    echo -e "${YELLOW}ℹ️  No release-worthy commits found${NC}"
    echo ""
    echo "Commits must follow conventional commit format:"
    echo "  - feat: for new features (minor bump)"
    echo "  - fix: for bug fixes (patch bump)"
    echo "  - feat!: or fix!: for breaking changes (major bump)"
    echo ""
    echo -e "Current version remains: ${GREEN}${LATEST_TAG}${NC}"
    exit 0
fi

# Calculate new version
case "$RELEASE_TYPE" in
    major)
        MAJOR=$((MAJOR + 1))
        MINOR=0
        PATCH=0
        ;;
    minor)
        MINOR=$((MINOR + 1))
        PATCH=0
        ;;
    patch)
        PATCH=$((PATCH + 1))
        ;;
esac

NEW_VERSION="v${MAJOR}.${MINOR}.${PATCH}"

echo ""
echo -e "Release type: ${BLUE}${RELEASE_TYPE}${NC}"
echo -e "Next version: ${GREEN}${NEW_VERSION}${NC}"
echo ""

# Show what will be in the changelog
echo "=== Changelog Preview ==="
echo ""
echo "## Changes since ${LATEST_TAG}"
echo ""

echo "### 🚀 Features"
if echo "$COMMITS" | grep -q "^feat"; then
    echo "$COMMITS" | grep "^feat" | sed 's/^/- /'
else
    echo "_No new features_"
fi
echo ""

echo "### 🐛 Bug Fixes"
if echo "$COMMITS" | grep -q "^fix"; then
    echo "$COMMITS" | grep "^fix" | sed 's/^/- /'
else
    echo "_No bug fixes_"
fi
echo ""

echo "### 🔧 Other Changes"
OTHER=$(echo "$COMMITS" | grep -v "^feat" | grep -v "^fix" || true)
if [ -n "$OTHER" ]; then
    echo "$OTHER" | sed 's/^/- /'
else
    echo "_No other changes_"
fi
echo ""

echo "=== How to Release ==="
echo ""
echo "Option 1: Automatic (via GitHub Actions)"
echo "  1. Merge to main branch"
echo "  2. Release workflow will automatically detect and create ${NEW_VERSION}"
echo ""
echo "Option 2: Manual (via GitHub Actions UI)"
echo "  1. Go to Actions → Release → Run workflow"
echo "  2. Select 'auto' or specific bump type"
echo ""
echo "Option 3: Manual (via git tag)"
echo "  git tag -a ${NEW_VERSION} -m \"Release ${NEW_VERSION}\""
echo "  git push origin ${NEW_VERSION}"
echo "  (then manually create GitHub Release)"
