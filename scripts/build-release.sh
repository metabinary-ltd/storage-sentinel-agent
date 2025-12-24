#!/bin/bash
set -euo pipefail

# Build Release Script
# Builds binaries for all supported architectures

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info() {
    echo -e "${GREEN}INFO:${NC} $1"
}

warn() {
    echo -e "${YELLOW}WARN:${NC} $1"
}

# Get version from tag or argument
VERSION="${1:-}"
if [ -z "$VERSION" ]; then
    # Try to get version from git tag
    if git describe --tags --exact-match HEAD &>/dev/null; then
        VERSION=$(git describe --tags --exact-match HEAD)
    elif git describe --tags HEAD &>/dev/null; then
        VERSION=$(git describe --tags HEAD)
    else
        warn "No version provided and no git tag found. Using 'dev'"
        VERSION="dev"
    fi
fi

# Remove 'v' prefix if present
VERSION="${VERSION#v}"

info "Building version: $VERSION"

cd "$PROJECT_ROOT"

# Create release directory
RELEASE_DIR="release-${VERSION}"
rm -rf "$RELEASE_DIR"
mkdir -p "$RELEASE_DIR"

# Build flags
LDFLAGS="-s -w"

# Architectures to build
ARCHITECTURES=(
    "linux:amd64"
    "linux:arm64"
    "linux:arm"
)

build_binary() {
    local os=$1
    local arch=$2
    local suffix="${os}-${arch}"
    
    info "Building for ${os}/${arch}..."
    
    export CGO_ENABLED=0
    export GOOS="$os"
    export GOARCH="$arch"
    
    # Build agent
    go build -ldflags="$LDFLAGS" -o "$RELEASE_DIR/storagesentinel-${suffix}" ./cmd/storagesentinel
    
    # Build CLI
    go build -ldflags="$LDFLAGS" -o "$RELEASE_DIR/storagesentinelctl-${suffix}" ./cmd/storagesentinelctl
    
    # Create tarball for this architecture
    cd "$RELEASE_DIR"
    tar -czf "storagesentinel-${VERSION}-${suffix}.tar.gz" \
        "storagesentinel-${suffix}" \
        "storagesentinelctl-${suffix}"
    cd "$PROJECT_ROOT"
    
    info "✓ Built ${suffix}"
}

# Build for all architectures
for arch_spec in "${ARCHITECTURES[@]}"; do
    IFS=':' read -r os arch <<< "$arch_spec"
    build_binary "$os" "$arch"
done

# Create checksums
info "Creating checksums..."
cd "$RELEASE_DIR"
sha256sum *.tar.gz > "checksums.txt"
cd "$PROJECT_ROOT"

info "Build complete! Release files in: $RELEASE_DIR"
echo
echo "Files created:"
ls -lh "$RELEASE_DIR" | grep -v "^total"

