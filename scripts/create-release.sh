#!/bin/bash
set -euo pipefail

# Create GitHub Release Script
# Creates a GitHub release with tag and uploads binaries

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

error() {
    echo -e "${RED}ERROR:${NC} $1" >&2
    exit 1
}

info() {
    echo -e "${GREEN}INFO:${NC} $1"
}

warn() {
    echo -e "${YELLOW}WARN:${NC} $1"
}

# Configuration
REPO="${GITHUB_REPOSITORY:-metabinary-ltd/storage-sentinel-agent}"
REMOTE="${GITHUB_REMOTE:-origin}"

usage() {
    cat <<EOF
Usage: $0 <VERSION> [RELEASE_NOTES]

Creates a GitHub release with the specified version tag.

Arguments:
    VERSION        Version tag (e.g., v1.0.0 or 1.0.0)
    RELEASE_NOTES  Optional release notes file (default: generates from git)

Examples:
    $0 v1.0.0
    $0 1.0.0 RELEASE_NOTES.md

EOF
}

# Check for required tools
command -v gh >/dev/null 2>&1 || error "GitHub CLI (gh) is required. Install from: https://cli.github.com/"

# Get version
VERSION="${1:-}"
if [ -z "$VERSION" ]; then
    usage
    error "Version is required"
fi

# Remove 'v' prefix if present for tag, but keep it for display
TAG_VERSION="${VERSION#v}"
TAG="v${TAG_VERSION}"
DISPLAY_VERSION="$VERSION"

# Check if tag already exists
if git rev-parse "$TAG" &>/dev/null; then
    warn "Tag $TAG already exists"
    read -p "Continue anyway? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

# Build binaries first
info "Building binaries..."
"$SCRIPT_DIR/build-release.sh" "$TAG_VERSION"

RELEASE_DIR="$PROJECT_ROOT/release-${TAG_VERSION}"

# Prepare release notes
RELEASE_NOTES="${2:-}"
if [ -z "$RELEASE_NOTES" ]; then
    # Generate release notes from git
    RELEASE_NOTES=$(mktemp)
    {
        echo "## Storage Sentinel Agent ${DISPLAY_VERSION}"
        echo ""
        if git rev-parse "$TAG" &>/dev/null; then
            # Get commits since last tag
            LAST_TAG=$(git describe --tags --abbrev=0 HEAD^ 2>/dev/null || echo "")
            if [ -n "$LAST_TAG" ]; then
                echo "### Changes since ${LAST_TAG}"
                echo ""
                git log --pretty=format:"- %s (%h)" "${LAST_TAG}..HEAD" >> "$RELEASE_NOTES" || true
            fi
        else
            echo "### Installation"
            echo ""
            echo "Download the appropriate binary for your architecture:"
            echo ""
            echo "- \`storagesentinel-${TAG_VERSION}-linux-amd64.tar.gz\` - Linux AMD64"
            echo "- \`storagesentinel-${TAG_VERSION}-linux-arm64.tar.gz\` - Linux ARM64"
            echo "- \`storagesentinel-${TAG_VERSION}-linux-arm.tar.gz\` - Linux ARM (32-bit)"
            echo ""
            echo "See [README.md](README.md) for installation instructions."
        fi
    } > "$RELEASE_NOTES"
fi

# Create git tag if it doesn't exist
if ! git rev-parse "$TAG" &>/dev/null; then
    info "Creating git tag: $TAG"
    git tag -a "$TAG" -m "Release ${DISPLAY_VERSION}"
    info "Pushing tag to remote..."
    git push "$REMOTE" "$TAG" || error "Failed to push tag"
fi

# Create GitHub release
info "Creating GitHub release: $TAG"

# Upload assets
ASSETS=()
for file in "$RELEASE_DIR"/*.tar.gz "$RELEASE_DIR"/checksums.txt; do
    if [ -f "$file" ]; then
        ASSETS+=("$file")
    fi
done

# Create release with assets
if [ ${#ASSETS[@]} -gt 0 ]; then
    gh release create "$TAG" \
        --repo "$REPO" \
        --title "Storage Sentinel Agent ${DISPLAY_VERSION}" \
        --notes-file "$RELEASE_NOTES" \
        "${ASSETS[@]}" || error "Failed to create release"
else
    gh release create "$TAG" \
        --repo "$REPO" \
        --title "Storage Sentinel Agent ${DISPLAY_VERSION}" \
        --notes-file "$RELEASE_NOTES" || error "Failed to create release"
fi

# Cleanup temp file if we created it
if [ -z "${2:-}" ] && [ -f "$RELEASE_NOTES" ]; then
    rm -f "$RELEASE_NOTES"
fi

info "Release created successfully: https://github.com/${REPO}/releases/tag/${TAG}"

