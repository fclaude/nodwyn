MODULE  := github.com/fclaude/nibb
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test lint exfil accept promote selfcheck smoke clean

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

## promote: accept + selfcheck, swap in nibb-stable, tag. Usage: make promote TAG=v0.1-bootstrap
promote:
	scripts/promote.sh $(TAG)

## selfcheck: the candidate binary proves itself (ADR 13)
selfcheck: build
	scripts/selfcheck.sh bin/nibb

## smoke: manual two-turn session against the real control plane (not CI)
smoke:
	scripts/smoke.sh

clean:
	rm -rf bin
