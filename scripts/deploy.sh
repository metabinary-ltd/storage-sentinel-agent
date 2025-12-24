#!/bin/bash
set -euo pipefail

# Storage Sentinel Deployment Script
# This script automates the installation of Storage Sentinel on Linux systems

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Default values
BINARY_URL=""
LOCAL_BINARY_DIR=""
SKIP_DEPS=false
SKIP_CONFIG=false

# Functions
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

check_root() {
    if [ "$EUID" -ne 0 ]; then
        error "This script must be run as root or with sudo"
    fi
}

detect_arch() {
    local arch
    arch=$(uname -m)
    case "$arch" in
        x86_64|amd64)
            echo "amd64"
            ;;
        aarch64|arm64)
            echo "arm64"
            ;;
        armv7l|arm)
            echo "arm"
            ;;
        *)
            error "Unsupported architecture: $arch"
            ;;
    esac
}

check_dependencies() {
    local missing=()
    
    if ! command -v smartctl &> /dev/null; then
        missing+=("smartmontools")
    fi
    
    if ! command -v nvme &> /dev/null; then
        missing+=("nvme-cli")
    fi
    
    if [ ${#missing[@]} -gt 0 ]; then
        warn "Missing dependencies: ${missing[*]}"
        echo "Install with:"
        if command -v apt-get &> /dev/null; then
            echo "  sudo apt-get install ${missing[*]}"
        elif command -v yum &> /dev/null; then
            echo "  sudo yum install ${missing[*]}"
        elif command -v dnf &> /dev/null; then
            echo "  sudo dnf install ${missing[*]}"
        else
            echo "  Please install: ${missing[*]}"
        fi
        
        if [ "$SKIP_DEPS" = false ]; then
            read -p "Continue anyway? (y/N) " -n 1 -r
            echo
            if [[ ! $REPLY =~ ^[Yy]$ ]]; then
                exit 1
            fi
        fi
    fi
}

download_binaries() {
    local arch=$1
    local temp_dir
    
    if [ -n "$BINARY_URL" ]; then
        temp_dir=$(mktemp -d)
        info "Downloading binaries from $BINARY_URL"
        
        if command -v curl &> /dev/null; then
            curl -L -o "$temp_dir/storagesentinel.tar.gz" "$BINARY_URL/storagesentinel-linux-${arch}.tar.gz" || \
            curl -L -o "$temp_dir/storagesentinel" "$BINARY_URL/storagesentinel-linux-${arch}" || \
            error "Failed to download binaries"
        elif command -v wget &> /dev/null; then
            wget -O "$temp_dir/storagesentinel.tar.gz" "$BINARY_URL/storagesentinel-linux-${arch}.tar.gz" || \
            wget -O "$temp_dir/storagesentinel" "$BINARY_URL/storagesentinel-linux-${arch}" || \
            error "Failed to download binaries"
        else
            error "Neither curl nor wget found. Please install one of them."
        fi
        
        if [ -f "$temp_dir/storagesentinel.tar.gz" ]; then
            tar -xzf "$temp_dir/storagesentinel.tar.gz" -C "$temp_dir" || error "Failed to extract archive"
        fi
        
        echo "$temp_dir"
    elif [ -n "$LOCAL_BINARY_DIR" ]; then
        if [ ! -d "$LOCAL_BINARY_DIR" ]; then
            error "Local binary directory not found: $LOCAL_BINARY_DIR"
        fi
        echo "$LOCAL_BINARY_DIR"
    else
        # Check current directory and project root
        if [ -f "./storagesentinel" ] && [ -f "./storagesentinelctl" ]; then
            echo "$(pwd)"
        elif [ -f "$PROJECT_ROOT/storagesentinel" ] && [ -f "$PROJECT_ROOT/storagesentinelctl" ]; then
            echo "$PROJECT_ROOT"
        else
            error "Binaries not found. Please provide --url, --local-dir, or place binaries in current directory."
        fi
    fi
}

install_binaries() {
    local binary_dir=$1
    
    info "Installing binaries..."
    
    if [ ! -f "$binary_dir/storagesentinel" ]; then
        error "storagesentinel binary not found in $binary_dir"
    fi
    
    if [ ! -f "$binary_dir/storagesentinelctl" ]; then
        warn "storagesentinelctl binary not found, skipping..."
    fi
    
    cp "$binary_dir/storagesentinel" /usr/local/bin/storagesentinel
    chmod +x /usr/local/bin/storagesentinel
    
    if [ -f "$binary_dir/storagesentinelctl" ]; then
        cp "$binary_dir/storagesentinelctl" /usr/local/bin/storagesentinelctl
        chmod +x /usr/local/bin/storagesentinelctl
    fi
    
    info "Binaries installed to /usr/local/bin"
}

create_directories() {
    info "Creating directories..."
    mkdir -p /etc/storagesentinel
    mkdir -p /var/lib/storagesentinel
    mkdir -p /var/log
    info "Directories created"
}

setup_cloud_config() {
    if [ "$SKIP_CONFIG" = true ]; then
        return
    fi
    
    info "Cloud Dashboard Setup"
    echo
    echo "Would you like to connect this host to Storage Sentinel Cloud for remote monitoring?"
    read -p "Connect to cloud? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        info "Skipping cloud setup. You can configure it later with: storagesentinelctl cloud init"
        return
    fi
    
    echo
    echo "To generate an API key, visit your dashboard at:"
    echo "  https://dashboard.storage-sentinel.com/settings/api-keys"
    echo
    read -p "Enter your API key: " -r API_KEY
    API_KEY=$(echo "$API_KEY" | tr -d '[:space:]')
    
    if [ -z "$API_KEY" ]; then
        warn "No API key provided. Skipping cloud configuration."
        echo "You can configure it later with: storagesentinelctl cloud init"
        return
    fi
    
    read -p "Enter cloud endpoint [https://api.storage-sentinel.com]: " -r ENDPOINT
    ENDPOINT=$(echo "$ENDPOINT" | tr -d '[:space:]')
    if [ -z "$ENDPOINT" ]; then
        ENDPOINT="https://api.storage-sentinel.com"
    fi
    
    # Update config file
    if [ -f /etc/storagesentinel/config.yml ]; then
        # Use sed or yq to update config, or append if section doesn't exist
        if grep -q "^cloud:" /etc/storagesentinel/config.yml; then
            # Update existing cloud section
            sed -i "s|endpoint:.*|endpoint: \"$ENDPOINT\"|" /etc/storagesentinel/config.yml
            sed -i "s|api_token:.*|api_token: \"$API_KEY\"|" /etc/storagesentinel/config.yml
            sed -i "s|enabled:.*|enabled: true|" /etc/storagesentinel/config.yml
        else
            # Append cloud section
            cat >> /etc/storagesentinel/config.yml <<EOF

cloud:
  enabled: true
  endpoint: "$ENDPOINT"
  api_token: "$API_KEY"
  upload_interval: 15m
  command_poll_interval: 5m
EOF
        fi
        info "Cloud configuration added to config file"
        echo
        echo "Note: The agent will register with the cloud dashboard on first start."
        echo "You can check the status with: storagesentinelctl cloud status"
    else
        warn "Config file not found. Cloud setup will be skipped."
        echo "You can configure it later with: storagesentinelctl cloud init"
    fi
}

install_config() {
    if [ "$SKIP_CONFIG" = true ]; then
        warn "Skipping configuration setup"
        return
    fi
    
    info "Setting up configuration..."
    
    if [ ! -f /etc/storagesentinel/config.yml ]; then
        if [ -f "$PROJECT_ROOT/configs/config.sample.yml" ]; then
            cp "$PROJECT_ROOT/configs/config.sample.yml" /etc/storagesentinel/config.yml
            info "Configuration file created at /etc/storagesentinel/config.yml"
            warn "Please review and edit the configuration file as needed"
        else
            warn "Sample config not found, creating minimal config..."
            cat > /etc/storagesentinel/config.yml <<EOF
storage:
  include_devices: []
  exclude_devices: []
  zfs_enable: true

scheduling:
  smart_collect_interval: "6h"
  zfs_status_interval: "15m"
  smart_short_interval: "168h"
  smart_long_interval: "720h"
  zfs_scrub_interval: "720h"

alerts:
  min_severity: "warning"
  debounce_window: "6h"
  temperature_thresholds:
    hdd_warning: 55.0   # in Celsius (default: 55°C)
    hdd_critical: 70.0  # in Celsius (default: 70°C)
    nvme_warning: 70.0  # in Celsius (default: 70°C)
    nvme_critical: 85.0 # in Celsius (default: 85°C)

notifications:
  email:
    enabled: false
    smtp_server: ""
    smtp_port: 587
    username: ""
    password: ""
    from: ""
    to: []
  webhooks: []

cloud:
  enabled: false
  endpoint: "https://api.storage-sentinel.com"
  api_token: ""

api:
  bind_address: "127.0.0.1"
  port: 8200
  auth_token: ""

logging:
  level: "info"
  debug_enable: true
  debug_log: "/var/log/storagesentinel-debug.log"

paths:
  db_path: "/var/lib/storagesentinel/state.db"
  log_path: "/var/log/storagesentinel.log"

tools:
  smartctl: "smartctl"
  nvme: "nvme"
  zpool: "zpool"
  zfs: "zfs"

EOF
            info "Minimal configuration file created"
        fi
    else
        warn "Configuration file already exists at /etc/storagesentinel/config.yml, skipping..."
    fi
}

install_systemd_service() {
    info "Installing systemd service..."
    
    if [ -f "$PROJECT_ROOT/systemd/storagesentinel-agent.service" ]; then
        cp "$PROJECT_ROOT/systemd/storagesentinel-agent.service" /etc/systemd/system/storagesentinel-agent.service
    else
        # Create service file inline if not found
        cat > /etc/systemd/system/storagesentinel-agent.service <<EOF
[Unit]
Description=Storage Sentinel Host Agent
Documentation=https://github.com/metabinary-ltd/storage-sentinel
After=network.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/storagesentinel
Restart=on-failure
RestartSec=5s
StandardOutput=journal
StandardError=journal

# Security settings
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/storagesentinel /var/log

# Resource limits
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
    fi
    
    systemctl daemon-reload
    info "Systemd service installed"
}

start_service() {
    info "Starting Storage Sentinel service..."
    
    if systemctl is-active --quiet storagesentinel-agent; then
        warn "Service is already running, restarting..."
        systemctl restart storagesentinel-agent
    else
        systemctl enable storagesentinel-agent
        systemctl start storagesentinel-agent
    fi
    
    sleep 2
    
    if systemctl is-active --quiet storagesentinel-agent; then
        info "Service started successfully"
    else
        error "Service failed to start. Check logs with: journalctl -u storagesentinel-agent"
    fi
}

verify_installation() {
    info "Verifying installation..."
    
    # Check binaries
    if [ -x /usr/local/bin/storagesentinel ]; then
        info "✓ storagesentinel binary is installed"
    else
        error "✗ storagesentinel binary not found or not executable"
    fi
    
    # Check service
    if systemctl is-active --quiet storagesentinel-agent; then
        info "✓ Service is running"
    else
        error "✗ Service is not running"
    fi
    
    # Check API
    sleep 1
    if curl -s -f http://127.0.0.1:8200/health > /dev/null; then
        info "✓ API is responding"
    else
        warn "✗ API health check failed (service may still be starting)"
    fi
    
    info "Installation verification complete"
}

show_summary() {
    echo
    echo "=========================================="
    echo "Storage Sentinel Installation Complete"
    echo "=========================================="
    echo
    echo "Service Management:"
    echo "  sudo systemctl status storagesentinel-agent"
    echo "  sudo systemctl restart storagesentinel-agent"
    echo "  sudo journalctl -u storagesentinel-agent -f"
    echo
    echo "Configuration:"
    echo "  /etc/storagesentinel/config.yml"
    echo
    echo "Test the API:"
    echo "  curl http://127.0.0.1:8200/health"
    echo "  curl http://127.0.0.1:8200/api/v1/summary"
    echo
    if [ -x /usr/local/bin/storagesentinelctl ]; then
        echo "CLI Tool:"
        echo "  storagesentinelctl status"
        echo
    fi
}

usage() {
    cat <<EOF
Usage: $0 [OPTIONS]

Deploy Storage Sentinel on this system.

OPTIONS:
    --url URL              Download binaries from URL (should provide tar.gz or binary)
    --local-dir DIR       Use binaries from local directory
    --skip-deps           Skip dependency checks
    --skip-config         Skip configuration file setup
    -h, --help            Show this help message

EXAMPLES:
    # Use binaries in current directory
    sudo $0

    # Download from URL
    sudo $0 --url https://github.com/metabinary-ltd/storage-sentinel/releases/latest/download

    # Use local binaries
    sudo $0 --local-dir /path/to/binaries

    # Skip dependency checks
    sudo $0 --skip-deps

EOF
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --url)
            BINARY_URL="$2"
            shift 2
            ;;
        --local-dir)
            LOCAL_BINARY_DIR="$2"
            shift 2
            ;;
        --skip-deps)
            SKIP_DEPS=true
            shift
            ;;
        --skip-config)
            SKIP_CONFIG=true
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            error "Unknown option: $1. Use --help for usage."
            ;;
    esac
done

# Main execution
main() {
    echo "=========================================="
    echo "Storage Sentinel Deployment Script"
    echo "=========================================="
    echo
    
    check_root
    ARCH=$(detect_arch)
    info "Detected architecture: $ARCH"
    
    if [ "$SKIP_DEPS" = false ]; then
        check_dependencies
    fi
    
    BINARY_DIR=$(download_binaries "$ARCH")
    install_binaries "$BINARY_DIR"
    create_directories
    install_config
    setup_cloud_config
    install_systemd_service
    start_service
    verify_installation
    show_summary
}

main

