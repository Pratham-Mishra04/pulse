BINARY     := pulse
BUILD_DIR  := ./tmp
CMD        := ./cmd/pulse

.PHONY: build run clean test lint tidy

build:
	go build -o $(BUILD_DIR)/$(BINARY) $(CMD)

run: build
	$(BUILD_DIR)/$(BINARY)

test:
	go test ./...

lint:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf $(BUILD_DIR)
