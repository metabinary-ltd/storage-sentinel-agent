# Storage Sentinel

The Storage Sentinel Host Agent is a lightweight daemon that runs directly on NAS / server hosts (e.g., Proxmox, bare-metal Linux, ZFS-based systems). 
It continuously monitors disk and pool health, automates core maintenance tasks (SMART tests, ZFS scrubs), stores historical metrics, and exposes a local HTTP API for dashboards and tooling.

## Quick start

### Production Installation

**One-line install (recommended) - automatically downloads latest release:**
```bash
curl -fsSL https://raw.githubusercontent.com/metabinary-ltd/storage-sentinel-agent/main/scripts/deploy.sh | sudo bash
```

This will:
- Detect your system architecture
- Automatically download the latest release binaries from GitHub
- Install and configure Storage Sentinel
- Start the service

**Manual download and install:**
```bash
# Detect architecture and download latest release
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/;s/armv7l/arm/')
LATEST=$(curl -s https://api.github.com/repos/metabinary-ltd/storage-sentinel-agent/releases/latest | grep "tag_name" | cut -d '"' -f 4)
curl -L -o storagesentinel.tar.gz "https://github.com/metabinary-ltd/storage-sentinel-agent/releases/download/${LATEST}/storagesentinel-${LATEST#v}-linux-${ARCH}.tar.gz"
tar -xzf storagesentinel.tar.gz
cd storagesentinel-${LATEST#v}-linux-${ARCH}
sudo ./scripts/deploy.sh
```

## First Run & Getting Started

After installing, the Storage Sentinel Agent should already be running as a background service. Here’s how to verify your installation and perform your first health check:

### 1. Check Service Status

Make sure the agent is running:
```bash
sudo systemctl status storagesentinel
```
You should see output that indicates the service is **active (running)**. If not, try starting it:
```bash
sudo systemctl start storagesentinel
```

### 2. Confirm the HTTP API Is Working

The agent opens a local HTTP API by default on port `8200`. Try a basic health check:
```bash
curl http://127.0.0.1:8200/health
```
Expected output:
```json
{"ok":true,"version":"<version>","time":"<timestamp>"}
```

Get a summary of monitored disks and pools:
```bash
curl http://127.0.0.1:8200/api/v1/summary | jq
```
If you don't have `jq`, you can omit it and view raw JSON.

### 3. Run First Manual Health Check

Trigger a full health scan (optional; auto-scans occur in the background):
```bash
storagesentinelctl scan
```
You should see scan output summarizing disk and pool health.

### 4. View Logs for Troubleshooting

To see recent agent logs:
```bash
sudo journalctl -u storagesentinel -e
```

### 5. Connect to Cloud Dashboard (Optional)

**To view metrics and receive notifications:**

1. Go to [Storage Sentinel Cloud Dashboard](https://dashboard.storage-sentinel.com/).
2. Sign in and add a new Host Agent.
3. Obtain your **enrollment token** from the dashboard.
4. Register your agent with the cloud:
    ```bash
    sudo storagesentinelctl cloud enroll --token <YOUR_TOKEN>
    ```
5. The agent will securely connect and start reporting metrics.

Check connection status:
```bash
storagesentinelctl cloud status
```
Cloud-connected agents provide remote monitoring, notifications, and historical metrics in the dashboard.



### Development

```bash
go run ./cmd/storagesentinel
curl http://127.0.0.1:8200/health
curl http://127.0.0.1:8200/api/v1/summary
```

## Building

### Prerequisites

#### To Build
- Go 1.24 or later

#### To Deploy
- System tools (required on target machine, not for building):
  - `smartctl` (from smartmontools package)
  - `nvme` (from nvme-cli package)
  - `zpool` and `zfs` (from zfsutils-linux package, if using ZFS)

### Building Standalone Binaries

The project compiles to standalone binaries that don't require Go on the target machine. All dependencies are statically linked.

**For the same architecture:**
```bash
go build -o storagesentinel ./cmd/storagesentinel
go build -o storagesentinelctl ./cmd/storagesentinelctl
```

**For cross-compilation (building on macOS/Windows for Linux):**

Linux AMD64 (most common):
```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o storagesentinel-linux-amd64 ./cmd/storagesentinel
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o storagesentinelctl-linux-amd64 ./cmd/storagesentinelctl
```

Linux ARM64 (for Raspberry Pi, ARM servers):
```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o storagesentinel-linux-arm64 ./cmd/storagesentinel
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o storagesentinelctl-linux-arm64 ./cmd/storagesentinelctl
```

Linux ARM (32-bit):
```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm go build -ldflags="-s -w" -o storagesentinel-linux-arm ./cmd/storagesentinel
CGO_ENABLED=0 GOOS=linux GOARCH=arm go build -ldflags="-s -w" -o storagesentinelctl-linux-arm ./cmd/storagesentinelctl
```

**Build flags explained:**
- `CGO_ENABLED=0` - Disables CGO for fully static binaries (not required but recommended)
- `-ldflags="-s -w"` - Strips debug symbols to reduce binary size
- `GOOS` and `GOARCH` - Target operating system and architecture

**Testing the build:**
```bash
./storagesentinel --version
./storagesentinelctl --help
```

## Deployment

### System Requirements

- Linux (Debian/Ubuntu, Proxmox, or other common distros)
- Root or sudo access (for systemd service installation)
- Required system tools installed:
  ```bash
  # Debian/Ubuntu
  sudo apt-get install smartmontools nvme-cli zfsutils-linux

  # Or on systems without ZFS:
  sudo apt-get install smartmontools nvme-cli
  ```

### Automated Deployment (Recommended)

The easiest way to deploy Storage Sentinel is using the automated deployment script:

**Quick Install (one-liner) - Downloads latest release:**
```bash
curl -fsSL https://raw.githubusercontent.com/metabinary-ltd/storage-sentinel-agent/main/scripts/deploy.sh | sudo bash
```

**Option 1: Download and run the script directly**
```bash
# Download the deployment script
curl -fsSL https://raw.githubusercontent.com/metabinary-ltd/storage-sentinel-agent/main/scripts/deploy.sh -o deploy.sh
sudo bash deploy.sh
```

**Option 2: Download latest release and install**
```bash
# The deploy script will automatically download the latest release if no binaries are found
# Just run it without any options:
sudo bash scripts/deploy.sh

# Or manually download and install:
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/;s/armv7l/arm/')
LATEST=$(curl -s https://api.github.com/repos/metabinary-ltd/storage-sentinel-agent/releases/latest | grep "tag_name" | cut -d '"' -f 4)
curl -L -o storagesentinel.tar.gz "https://github.com/metabinary-ltd/storage-sentinel-agent/releases/download/${LATEST}/storagesentinel-${LATEST#v}-linux-${ARCH}.tar.gz"
tar -xzf storagesentinel.tar.gz
cd storagesentinel-${LATEST#v}-linux-${ARCH}
sudo ./scripts/deploy.sh
```

**Option 3: Use binaries from current directory**
```bash
# If you have the binaries in the current directory
sudo bash scripts/deploy.sh
```

**Option 4: Use local binaries**
```bash
# Use binaries from a specific directory
sudo bash scripts/deploy.sh --local-dir /path/to/binaries
```

The deployment script will:
- Detect your system architecture
- Check for required dependencies
- Install binaries to `/usr/local/bin`
- Create necessary directories
- Set up configuration file
- Install and start the systemd service
- Verify the installation

**Script options:**
- `--url URL` - Download binaries from URL
- `--local-dir DIR` - Use binaries from local directory
- `--skip-deps` - Skip dependency checks
- `--skip-config` - Skip configuration file setup
- `--help` - Show help message

### Manual Installation Steps

If you prefer to install manually:

1. **Copy binaries to system:**
   ```bash
   sudo cp storagesentinel /usr/local/bin/
   sudo cp storagesentinelctl /usr/local/bin/
   sudo chmod +x /usr/local/bin/storagesentinel /usr/local/bin/storagesentinelctl
   ```

2. **Create directories:**
   ```bash
   sudo mkdir -p /etc/storagesentinel
   sudo mkdir -p /var/lib/storagesentinel
   sudo mkdir -p /var/log
   ```

3. **Create configuration file:**
   ```bash
   sudo cp configs/config.sample.yml /etc/storagesentinel/config.yml
   sudo nano /etc/storagesentinel/config.yml  # Edit as needed
   ```

4. **Install systemd service:**
   ```bash
   sudo cp systemd/storagesentinel-agent.service /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable storagesentinel-agent
   sudo systemctl start storagesentinel-agent
   ```

5. **Verify installation:**
   ```bash
   sudo systemctl status storagesentinel-agent
   curl http://127.0.0.1:8200/health
   ```

### Service Management

```bash
# Start the service
sudo systemctl start storagesentinel-agent

# Stop the service
sudo systemctl stop storagesentinel-agent

# Restart the service
sudo systemctl restart storagesentinel-agent

# Check status
sudo systemctl status storagesentinel-agent

# View logs
sudo journalctl -u storagesentinel-agent -f

# Enable auto-start on boot
sudo systemctl enable storagesentinel-agent

# Disable auto-start
sudo systemctl disable storagesentinel-agent
```

### Configuration

- Default config path: `/etc/storagesentinel/config.yml`
- Sample: `configs/config.sample.yml`
- Env overrides (examples):
  - `STORAGESENTINEL_API_BIND=0.0.0.0`
  - `STORAGESENTINEL_API_PORT=8200`
  - `STORAGESENTINEL_LOG_LEVEL=debug`
  - `STORAGESENTINEL_DB_PATH=/var/lib/storagesentinel/state.db`
  - `STORAGESENTINEL_API_TOKEN=your-token-here`

### Verification

After deployment, verify the service is working:

```bash
# Check service status
sudo systemctl status storagesentinel-agent

# Test health endpoint
curl http://127.0.0.1:8200/health

# Test summary endpoint (may require auth token)
curl http://127.0.0.1:8200/api/v1/summary

# Use CLI tool
storagesentinelctl status
```
