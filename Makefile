BINARY    := pulse
BUILD_DIR := ./tmp
CMD       := .
VERSION   := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS   := -ldflags "-X github.com/Pratham-Mishra04/pulse/internal/cli.Version=$(VERSION)"

.PHONY: all build install run test e2e test-all lint tidy clean snapshot

all: build

## build: compile the binary into ./tmp/pulse
build:
	mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY) $(CMD)

## install: build and install to $GOPATH/bin for local development
install:
	go install $(LDFLAGS) $(CMD)

## run: build then run pulse in the current directory
run: build
	$(BUILD_DIR)/$(BINARY)

## test: run unit tests
test:
	go test ./...

## e2e: run end-to-end tests (slow — spawns real processes)
e2e:
	RUN_E2E=1 go test ./tests/ -v -timeout 120s

## test-all: run unit tests then e2e tests
test-all: test e2e

## lint: run go vet
lint:
	go vet ./...

## tidy: tidy and verify go.mod
tidy:
	go mod tidy
	go mod verify

## clean: remove build artifacts
clean:
	rm -rf $(BUILD_DIR)

## snapshot: build binaries for linux, macos (arm+amd64), and windows
snapshot:
	mkdir -p $(BUILD_DIR)
	GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-linux-amd64   $(CMD)
	GOOS=darwin  GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-darwin-amd64  $(CMD)
	GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-darwin-arm64  $(CMD)
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-windows-amd64.exe $(CMD)
