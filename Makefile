.PHONY: build test lint clean install

BINARY := jai
BUILD_FLAGS := -tags fts5
LDFLAGS := -ldflags "-s -w"

build:
	go build $(BUILD_FLAGS) $(LDFLAGS) -o $(BINARY) ./cmd/jai

test:
	go test $(BUILD_FLAGS) ./...

lint:
	golangci-lint run ./...

clean:
	rm -f $(BINARY)

install:
	go install $(BUILD_FLAGS) $(LDFLAGS) ./cmd/jai

.DEFAULT_GOAL := build
