GO ?= go
GOFLAGS ?=
PKG ?= ./...

GOCACHE ?= $(CURDIR)/.cache/go-build
GOMODCACHE ?= $(CURDIR)/.cache/go-mod

GOENV = GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE)

VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null || git describe --tags 2>/dev/null || echo dev)
LDFLAGS = -X proxyctl/internal/cli.Version=$(VERSION)

.PHONY: build test vet fmt fmt-check check smoke-help clean-cache

build:
	@mkdir -p "$(GOCACHE)" "$(GOMODCACHE)"
	@$(GOENV) $(GO) build -ldflags="$(LDFLAGS)" $(GOFLAGS) $(PKG)

test:
	@mkdir -p "$(GOCACHE)" "$(GOMODCACHE)"
	@$(GOENV) $(GO) test $(GOFLAGS) $(PKG)

vet:
	@mkdir -p "$(GOCACHE)" "$(GOMODCACHE)"
	@$(GOENV) $(GO) vet $(GOFLAGS) $(PKG)

fmt:
	@$(GO) fmt $(PKG)

fmt-check:
	@out="$$(gofmt -l $$(find cmd internal -name '*.go' -type f))"; \
	if [ -n "$$out" ]; then \
		echo "These files are not gofmt-formatted:"; \
		echo "$$out"; \
		exit 1; \
	fi

check: fmt-check vet test build

build-binary:
	@mkdir -p "$(GOCACHE)" "$(GOMODCACHE)"
	@$(GOENV) $(GO) build -ldflags="$(LDFLAGS)" -o proxyctl ./cmd/proxyctl/

smoke-help:
	@mkdir -p "$(GOCACHE)" "$(GOMODCACHE)"
	@$(GOENV) $(GO) run ./cmd/proxyctl --help

clean-cache:
	@rm -rf .cache
