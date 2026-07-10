# Stamp the binary version from the git tag so `kdraigo-mcp version` can never
# drift from source again (D7). Falls back to a short commit if no tag is present.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
CMD     := ./cmd/kdraigo-mcp

.PHONY: build install version test

build:
	go build -ldflags "$(LDFLAGS)" -o bin/kdraigo-mcp $(CMD)

install:
	go install -ldflags "$(LDFLAGS)" $(CMD)

version:
	@echo $(VERSION)

test:
	go test ./...
