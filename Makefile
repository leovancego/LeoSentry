APP     := leosentry
PKG     := ./cmd/leosentry
BIN_DIR := bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build build-arm64 test vet clean

# Native build for local development
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(APP) $(PKG)

# R2S / RK3328 (Cortex-A53, ARM64)
build-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(APP)-linux-arm64 $(PKG)

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -rf $(BIN_DIR)
