MODULE  := github.com/fclaude/nodwyn
VERSION := $(shell git describe --always --dirty --abbrev=12 --exclude='*' 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build build-webview egress app test lint exfil accept validate promote selfcheck smoke book expert clean

build: egress
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/nodwyn ./cmd/nodwyn

## build-webview: bin/nodwyn whose `nodwyn app` opens its own window (cgo; WebKitGTK on Linux, docs/app.md)
build-webview: egress
	CGO_ENABLED=1 scripts/webview-env.sh run go build -trimpath -tags "$$(scripts/webview-env.sh tags)" -ldflags '$(LDFLAGS)' -o bin/nodwyn ./cmd/nodwyn

## egress: bin/nodwyn-egress, the proxy a contained MCP server reaches the network through
## (ADR 23 W3b); static and for Linux, since it runs inside the server's image
egress:
	CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags '$(LDFLAGS)' -o bin/nodwyn-egress ./cmd/nodwyn-egress

## app: bin/Nodwyn.app, nodwyn with its own window (macOS; needs Xcode's command line tools)
app:
	scripts/app-macos.sh $(VERSION)

test:
	go test -race $(GO_TEST_FLAGS) ./...

# lint runs the analyzer from the module (a `tool` in go.mod), so an installed
# staticcheck that cannot read the toolchain's export data does not stop it.
lint:
	go vet ./...
	go tool staticcheck ./...
	scripts/lint-boundaries.sh
	scripts/lint-ingress.sh

exfil:
	go test -race $(GO_TEST_FLAGS) ./test/exfil/...

## accept: everything a candidate must pass before promotion (ADR 13)
accept: lint test exfil

## validate: the checks what changed since upstream needs, before work is called done (ADR 37)
validate:
	@go run ./scripts/validate

## promote: accept + selfcheck, then swap in nodwyn-stable (the webview build where it can be built); its version is the commit
promote:
	scripts/promote.sh

## selfcheck: the candidate binary proves itself (ADR 13)
selfcheck: build
	scripts/selfcheck.sh bin/nodwyn

## smoke: manual two-turn session against the real control plane (not CI)
smoke:
	scripts/smoke.sh

## book: docs/book/nodwyn.pdf (needs texlive)
book:
	cd docs/book && latexmk -pdf -interaction=nonstopmode -halt-on-error nodwyn.tex >/dev/null && latexmk -c >/dev/null

## expert: docs/expert/expert.pdf, the field guide (needs texlive)
expert:
	cd docs/expert && latexmk -pdf -interaction=nonstopmode -halt-on-error expert.tex >/dev/null && latexmk -c >/dev/null

clean:
	rm -rf bin
