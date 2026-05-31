.PHONY: build install test lint clean release snapshot

BINARY   := deepact
VERSION  ?= $(shell git describe --tags --always 2>/dev/null || echo "dev")
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)
GOFLAGS  := -ldflags="$(LDFLAGS)"

build:
	go build $(GOFLAGS) -o $(BINARY) .

install:
	go install $(GOFLAGS) .

test:
	go test ./... -count=1 -race -timeout=120s

test-short:
	go test ./... -count=1 -short -timeout=60s

lint:
	golangci-lint run ./...

clean:
	rm -f $(BINARY)
	rm -rf dist/

release:
	goreleaser release --clean

snapshot:
	goreleaser release --snapshot --clean

.PHONY: help
help:
	@echo "Targets:"
	@echo "  build       - Build binary (./deepact)"
	@echo "  install     - go install to GOPATH/bin"
	@echo "  test        - Run all tests with race detector"
	@echo "  test-short  - Run tests without race (faster)"
	@echo "  lint        - Run golangci-lint"
	@echo "  clean       - Remove build artifacts"
	@echo "  release     - Run goreleaser (requires GITHUB_TOKEN)"
	@echo "  snapshot    - Build snapshot without publishing"
