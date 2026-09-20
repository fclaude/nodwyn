MODULE  := github.com/fclaude/nibb
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test lint exfil accept promote smoke clean

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/nibb ./cmd/nibb

test:
	go test -race ./...

lint:
	go vet ./...
	staticcheck ./...
	scripts/lint-boundaries.sh

exfil:
	go test -race ./test/exfil/...

## accept: everything a candidate must pass before promotion (ADR 13)
accept: lint test exfil

## promote: accept, then scripts/promote.sh (arrives in B3)
promote: accept
	@test -x scripts/promote.sh || { echo "scripts/promote.sh does not exist yet (B3)"; exit 1; }
	scripts/promote.sh

## smoke: manual two-turn session against the real control plane (B2)
smoke:
	@echo "make smoke arrives in B2"; exit 1

clean:
	rm -rf bin
