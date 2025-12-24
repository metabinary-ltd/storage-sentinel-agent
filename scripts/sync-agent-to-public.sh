#!/bin/bash
set -euo pipefail

# Sync Agent/CLI to Public Repository
# This script syncs agent and CLI code to a separate public repository using Git Subtree
#
# Usage:
#   ./scripts/sync-agent-to-public.sh [REMOTE_URL] [BRANCH]
#
# If REMOTE_URL is not provided, it will use the 'storage-sentinel-agent' remote if it exists.
# If BRANCH is not provided, it defaults to 'main'.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AGENT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
# Find the master repository root (where .git is)
MASTER_ROOT="$(cd "$AGENT_ROOT/.." && pwd)"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Default values
REMOTE_NAME="storage-sentinel-agent"
BRANCH="${2:-main}"
REMOTE_URL="${1:-https://github.com/metabinary-ltd/storage-sentinel-agent.git}"

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

step() {
    echo -e "${BLUE}→${NC} $1"
}

check_git_repo() {
    if [ ! -d "$MASTER_ROOT/.git" ]; then
        error "Not a git repository. Please run this from within the agent directory of the storage-sentinel repository."
    fi
}

check_remote() {
    cd "$MASTER_ROOT"
    if [ -n "$REMOTE_URL" ]; then
        # Add or update remote
        if git remote get-url "$REMOTE_NAME" &>/dev/null; then
            info "Updating remote '$REMOTE_NAME' to $REMOTE_URL"
            git remote set-url "$REMOTE_NAME" "$REMOTE_URL"
        else
            info "Adding remote '$REMOTE_NAME' -> $REMOTE_URL"
            git remote add "$REMOTE_NAME" "$REMOTE_URL"
        fi
    else
        # Check if remote exists
        if ! git remote get-url "$REMOTE_NAME" &>/dev/null; then
            error "Remote '$REMOTE_NAME' not found. Please provide REMOTE_URL as first argument or add the remote manually:\n  git remote add $REMOTE_NAME <public-repo-url>"
        fi
        REMOTE_URL=$(git remote get-url "$REMOTE_NAME")
        info "Using existing remote '$REMOTE_NAME' -> $REMOTE_URL"
    fi
}

check_clean_working_tree() {
    cd "$MASTER_ROOT"
    if ! git diff-index --quiet HEAD --; then
        warn "Working tree has uncommitted changes."
        read -p "Continue anyway? (y/N) " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            exit 1
        fi
    fi
}

sync_directory() {
    local dir=$1
    local description=$2
    
    if [ ! -d "$PROJECT_ROOT/$dir" ]; then
        warn "Directory '$dir' not found, skipping..."
        return
    fi
    
    step "Syncing $description ($dir/)..."
    
    if git subtree push --prefix="$dir" "$REMOTE_NAME" "$BRANCH" --squash; then
        info "✓ Successfully synced $dir/"
    else
        error "Failed to sync $dir/"
    fi
}

sync_agent_files() {
    step "Syncing agent files to public repository..."
    
    cd "$MASTER_ROOT"
    
    # Verify agent directory exists
    if [ ! -d "agent" ]; then
        error "Agent directory not found in $MASTER_ROOT"
    fi
    
    # Create a temporary directory for the new repo (completely separate from master repo)
    local temp_repo=$(mktemp -d)
    
    info "Creating fresh repository with agent files only (no history)..."
    
    # Initialize a new git repository with no history
    cd "$temp_repo"
    git init -b main
    
    # Configure git user (use existing config or defaults)
    git config user.name "${GIT_AUTHOR_NAME:-$(git config user.name 2>/dev/null || echo 'Storage Sentinel')}"
    git config user.email "${GIT_AUTHOR_EMAIL:-$(git config user.email 2>/dev/null || echo 'noreply@storagesentinel.io')}"
    
    # Copy agent files to the temp repo
    info "Copying agent files..."
    
    # Copy all agent files (including hidden files like .gitignore, .github)
    shopt -s dotglob
    cp -r "$AGENT_ROOT"/* "$temp_repo/" 2>/dev/null || true
    shopt -u dotglob
    
    # Remove the agent directory if it was copied as a subdirectory
    if [ -d "agent" ]; then
        # This shouldn't happen, but handle it just in case
        shopt -s dotglob
        mv agent/* . 2>/dev/null || true
        shopt -u dotglob
        rmdir agent 2>/dev/null || true
    fi
    
    # Verify we have the essential files
    if [ ! -d "cmd" ] || [ ! -f "go.mod" ]; then
        error "Essential agent files not found after copy. Expected cmd/ and go.mod"
    fi
    
    # Remove any binary files that might have been copied
    find . -type f -name "storagesentinel" -o -name "storagesentinelctl" | xargs rm -f 2>/dev/null || true
    
    # Create initial commit with only agent files
    git add -A
    local commit_msg="Sync agent code to public repo
    
This repository contains only the Storage Sentinel agent and CLI code.
The cloud dashboard remains private in the main repository."
    
    git commit -m "$commit_msg" || error "Failed to create commit"
    
    # Add the public repo as a remote
    git remote add public "$REMOTE_URL"
    
    # Force push to replace everything (this creates a completely fresh history)
    warn "This will replace ALL history in the public repository with a fresh commit."
    info "Pushing to public repository..."
    if git push public main:$BRANCH --force; then
        info "✓ Successfully synced all files with fresh history"
    else
        error "Failed to push to public repository"
    fi
    
    # Cleanup
    cd "$MASTER_ROOT"
    rm -rf "$temp_repo"
}

usage() {
    cat <<EOF
Usage: $0 [REMOTE_URL] [BRANCH]

Sync agent/CLI code to public repository using Git Subtree.

Arguments:
    REMOTE_URL    URL of the public repository (optional if 'storage-sentinel-agent' remote exists)
    BRANCH        Branch name to push to (default: main)

Examples:
    # Using existing remote
    $0

    # Adding/updating remote
    $0 https://github.com/metabinary-ltd/storage-sentinel-agent.git

    # Specify branch
    $0 https://github.com/metabinary-ltd/storage-sentinel-agent.git main

    # Using existing remote with different branch
    $0 "" develop

The script will sync:
  - cmd/ directory (agent and CLI)
  - internal/ directory (Go packages)
  - configs/ directory (configuration samples)
  - scripts/ directory (deployment scripts)
  - systemd/ directory (service files)
  - docs/ directory (documentation)
  - Root files: go.mod, go.sum, Makefile, LICENSE, README.md, .gitignore

EOF
}

main() {
    echo "=========================================="
    echo "Storage Sentinel - Sync to Public Repo"
    echo "=========================================="
    echo
    
    if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
        usage
        exit 0
    fi
    
    check_git_repo
    check_remote
    check_clean_working_tree
    
    info "Syncing to: $REMOTE_URL (branch: $BRANCH)"
    info "Syncing agent directory from: $AGENT_ROOT"
    echo
    
    # Use worktree approach to sync agent/ directory contents to public repo root
    sync_agent_files
    
    echo
    echo "=========================================="
    info "Sync complete!"
    echo "=========================================="
    echo
    echo "Public repository: $REMOTE_URL"
    echo "Branch: $BRANCH"
    echo
    echo "Note: The public repo now contains only agent/CLI code."
    echo "      Cloud dashboard remains private in this repository."
}

main "$@"

