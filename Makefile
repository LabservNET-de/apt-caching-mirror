.PHONY: all build clean test run install docker help

BINARY_NAME=apt-cache-proxy
BINARY_PATH=./$(BINARY_NAME)
GO=go
GOFLAGS=-ldflags="-w -s"
INSTALL_PATH=/opt/apt-cache-proxy
SERVICE_FILE=apt-cache-proxy-go.service

all: build

help:
	@echo "APT Cache Proxy (Go) - Makefile"
	@echo ""
	@echo "Targets:"
	@echo "  build      - Build the binary"
	@echo "  clean      - Remove build artifacts"
	@echo "  test       - Run tests"
	@echo "  run        - Build and run locally"
	@echo "  install    - Install as system service"
	@echo "  uninstall  - Remove system service"
	@echo "  docker     - Build Docker image"
	@echo "  bench      - Run benchmarks"

build:
	@echo "Building $(BINARY_NAME)..."
	@CGO_ENABLED=1 $(GO) build $(GOFLAGS) -o $(BINARY_PATH) .
	@echo "Build complete: $(BINARY_PATH)"
	@ls -lh $(BINARY_PATH)

clean:
	@echo "Cleaning..."
	@rm -f $(BINARY_NAME)
	@rm -rf storage/* data/*.db*
	@echo "Clean complete"

test:
	@echo "Running tests..."
	@$(GO) test -v ./...

run: build
	@echo "Starting $(BINARY_NAME)..."
	@$(BINARY_PATH)

install: build
	@echo "Installing to $(INSTALL_PATH)..."
	@sudo mkdir -p $(INSTALL_PATH)
	@sudo cp $(BINARY_PATH) $(INSTALL_PATH)/
	@sudo cp config.json $(INSTALL_PATH)/ 2>/dev/null || true
	@sudo mkdir -p $(INSTALL_PATH)/storage $(INSTALL_PATH)/data
	@sudo useradd -r -s /bin/false aptproxy 2>/dev/null || true
	@sudo chown -R aptproxy:aptproxy $(INSTALL_PATH)
	@sudo cp $(SERVICE_FILE) /etc/systemd/system/
	@sudo systemctl daemon-reload
	@sudo systemctl enable $(SERVICE_FILE)
	@echo "Installation complete!"
	@echo "Start with: sudo systemctl start apt-cache-proxy-go"

uninstall:
	@echo "Uninstalling..."
	@sudo systemctl stop apt-cache-proxy-go 2>/dev/null || true
	@sudo systemctl disable apt-cache-proxy-go 2>/dev/null || true
	@sudo rm -f /etc/systemd/system/$(SERVICE_FILE)
	@sudo systemctl daemon-reload
	@echo "Service removed. To delete data, run: sudo rm -rf $(INSTALL_PATH)"

docker:
	@echo "Building Docker image..."
	@docker build -t apt-cache-proxy-go:latest .
	@echo "Docker image built: apt-cache-proxy-go:latest"

bench:
	@echo "Running benchmarks..."
	@$(GO) test -bench=. -benchmem ./...

fmt:
	@echo "Formatting code..."
	@$(GO) fmt ./...

lint:
	@echo "Linting code..."
	@golangci-lint run || echo "Install golangci-lint for linting"

deps:
	@echo "Downloading dependencies..."
	@$(GO) mod download
	@$(GO) mod tidy

upgrade-deps:
	@echo "Upgrading dependencies..."
	@$(GO) get -u ./...
	@$(GO) mod tidy

size:
	@echo "Binary size breakdown:"
	@size $(BINARY_PATH) 2>/dev/null || ls -lh $(BINARY_PATH)

compress: build
	@echo "Compressing binary with UPX..."
	@upx --best --lzma $(BINARY_PATH) 2>/dev/null || echo "Install UPX for compression"
	@ls -lh $(BINARY_PATH)
