.PHONY: lint format lint-fix build release help

# Go linting and formatting targets
# These use golangci-lint and gofumpt for code quality

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

lint: ## Run golangci-lint on the codebase
	@echo "Running golangci-lint..."
	@golangci-lint run

lint-fix: ## Run golangci-lint with auto-fix enabled
	@echo "Running golangci-lint with auto-fix..."
	@golangci-lint run --fix

format: ## Format Go code using gofumpt (stricter than gofmt)
	@echo "Formatting Go code with gofumpt..."
	@gofumpt -w .

format-check: ## Check if Go code is formatted correctly
	@echo "Checking Go code formatting..."
	@gofumpt -l .

build: ## Build binaries for all architectures
	@bash scripts/build-release.sh

release: ## Create a GitHub release (usage: make release VERSION=v1.0.0)
	@if [ -z "$(VERSION)" ]; then \
		echo "Error: VERSION is required. Usage: make release VERSION=v1.0.0"; \
		exit 1; \
	fi
	@bash scripts/create-release.sh $(VERSION)

# Note: gofumpt is a stricter version of gofmt that enforces additional formatting rules.
# It's included in golangci-lint, but you can also run it directly:
#   go install mvdan.cc/gofumpt@latest
#   gofumpt -w .          # Format all files
#   gofumpt -l .          # List files that need formatting

