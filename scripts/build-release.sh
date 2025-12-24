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
RELEASE_DIR="release/${VERSION}"
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
    
    # Create package directory for this architecture
    local package_dir="$RELEASE_DIR/storagesentinel-${VERSION}-${suffix}"
    rm -rf "$package_dir"
    mkdir -p "$package_dir"
    
    # Copy binaries
    cp "$RELEASE_DIR/storagesentinel-${suffix}" "$package_dir/storagesentinel"
    cp "$RELEASE_DIR/storagesentinelctl-${suffix}" "$package_dir/storagesentinelctl"
    chmod +x "$package_dir/storagesentinel" "$package_dir/storagesentinelctl"
    
    # Copy config sample
    if [ -f "$PROJECT_ROOT/configs/config.sample.yml" ]; then
        mkdir -p "$package_dir/configs"
        cp "$PROJECT_ROOT/configs/config.sample.yml" "$package_dir/configs/"
    else
        warn "config.sample.yml not found, skipping..."
    fi
    
    # Copy systemd service file
    if [ -d "$PROJECT_ROOT/systemd" ]; then
        mkdir -p "$package_dir/systemd"
        cp -r "$PROJECT_ROOT/systemd"/* "$package_dir/systemd/"
    else
        warn "systemd directory not found, skipping..."
    fi
    
    # Copy deploy script
    if [ -f "$PROJECT_ROOT/scripts/deploy.sh" ]; then
        mkdir -p "$package_dir/scripts"
        cp "$PROJECT_ROOT/scripts/deploy.sh" "$package_dir/scripts/"
        chmod +x "$package_dir/scripts/deploy.sh"
    else
        warn "deploy.sh not found, skipping..."
    fi
    
    # Create tarball from package directory
    cd "$RELEASE_DIR"
    tar -czf "storagesentinel-${VERSION}-${suffix}.tar.gz" "storagesentinel-${VERSION}-${suffix}"
    cd "$PROJECT_ROOT"
    
    # Clean up package directory (keep tarball)
    rm -rf "$package_dir"
    
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

