MODULE  := github.com/fclaude/nibb
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build app test lint exfil accept promote selfcheck smoke book clean

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/nibb ./cmd/nibb

## app: bin/Nibb.app, nibb with its own window (macOS; needs Xcode's command line tools)
app:
	scripts/app-macos.sh $(VERSION)

test:
	go test -race $(GO_TEST_FLAGS) ./...

lint:
	go vet ./...
	staticcheck ./...
	scripts/lint-boundaries.sh
	scripts/lint-ingress.sh

exfil:
	go test -race $(GO_TEST_FLAGS) ./test/exfil/...

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

## book: docs/book/nibb.pdf (needs texlive)
book:
	cd docs/book && latexmk -pdf -interaction=nonstopmode -halt-on-error nibb.tex >/dev/null && latexmk -c >/dev/null

clean:
	rm -rf bin
