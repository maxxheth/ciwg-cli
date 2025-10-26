# Simple Makefile for a Go project

# Build the application
all: build test

build:
	@echo "Building..."
	@rm -rf ./dist/ciwg-cli-utils
	@mkdir -p ./dist/ciwg-cli-utils
	@go build -o ./dist/ciwg-cli-utils/ciwg-cli ./cmd/cli/main.go 
	@chmod +x ./dist/ciwg-cli-utils/ciwg-cli
	@mkdir -p ./dist/ciwg-cli-utils/.skel
	@cp -r ./.skel/. ./dist/ciwg-cli-utils/.skel/
	@cp .env ./dist/ciwg-cli-utils/
	@cp docker-compose.yml ./dist/ciwg-cli-utils/
	@tar -czf ./dist/ciwg-cli-utils.tgz -C ./dist ciwg-cli-utils
	@echo "Build completed: ./dist/ciwg-cli-utils.tgz"

alias:
	@echo "Creating alias..."
	@echo 'alias ciwg-cli-dev="$$(pwd)/dist/ciwg-cli-utils/ciwg-cli"' >> ~/.bashrc
	@echo "Alias added to ~/.bashrc"
	@echo "Run 'source ~/.bashrc' or restart your terminal to use: ciwg-cli-dev"

# Run the application
run:
	@go run cmd/cli/main.go
# Create DB container
docker-run:
	@if docker compose up --build 2>/dev/null; then \
		: ; \
	else \
		echo "Falling back to Docker Compose V1"; \
		docker-compose up --build; \
	fi

# Shutdown DB container
docker-down:
	@if docker compose down 2>/dev/null; then \
		: ; \
	else \
		echo "Falling back to Docker Compose V1"; \
		docker-compose down; \
	fi

# Test the application
test:
	@echo "Testing..."
	@go test ./... -v
# Integrations Tests for the application
itest:
	@echo "Running integration tests..."
	@go test ./internal/database -v

# Clean the binary
clean:
	@echo "Cleaning..."
	@rm -rf ./dist

# Live Reload
watch:
	@if command -v air > /dev/null; then \
            air; \
            echo "Watching...";\
        else \
            read -p "Go's 'air' is not installed on your machine. Do you want to install it? [Y/n] " choice; \
            if [ "$$choice" != "n" ] && [ "$$choice" != "N" ]; then \
                go install github.com/air-verse/air@latest; \
                air; \
                echo "Watching...";\
            else \
                echo "You chose not to install air. Exiting..."; \
                exit 1; \
            fi; \
        fi

run-domains:
	@echo "Running domains..."
	@go run cmd/cli/main.go domains

.PHONY: all build run test clean watch docker-run docker-down itest
