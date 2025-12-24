#!/bin/bash
# Setup script for creating a test ZFS pool in WSL2

set -e

echo "Setting up test ZFS pool..."

# Load ZFS kernel module
echo "Loading ZFS kernel module..."
sudo modprobe zfs

# Check if ZFS is working
if ! zpool list >/dev/null 2>&1; then
    echo "ZFS is not working. Trying to load modules..."
    sudo modprobe zfs
    sudo modprobe zcommon
    sudo modprobe znvpair
    sudo modprobe zunicode
    sudo modprobe zavl
    sudo modprobe zic
    sudo modprobe spl
fi

# Create test loop devices if they don't exist
LOOP1="/tmp/test-zfs-1.img"
LOOP2="/tmp/test-zfs-2.img"

if [ ! -f "$LOOP1" ]; then
    echo "Creating test loop device 1..."
    dd if=/dev/zero of="$LOOP1" bs=1M count=100
    sudo losetup /dev/loop10 "$LOOP1" 2>/dev/null || true
fi

if [ ! -f "$LOOP2" ]; then
    echo "Creating test loop device 2..."
    dd if=/dev/zero of="$LOOP2" bs=1M count=100
    sudo losetup /dev/loop11 "$LOOP2" 2>/dev/null || true
fi

# Check if pool already exists
if zpool list testpool >/dev/null 2>&1; then
    echo "Test pool 'testpool' already exists. Destroying it..."
    sudo zpool destroy testpool
fi

# Create test pool
echo "Creating test ZFS pool 'testpool'..."
sudo zpool create testpool mirror /dev/loop10 /dev/loop11

echo "Test pool created successfully!"
echo "Run 'zpool list' to verify"
echo "Run 'storagesentinelctl pools' to test the CLI"

