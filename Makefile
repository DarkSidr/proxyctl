GO ?= go
GOFLAGS ?=
PKG ?= ./...

GOCACHE ?= $(CURDIR)/.cache/go-build
GOMODCACHE ?= $(CURDIR)/.cache/go-mod

GOENV = GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE)

.PHONY: build test vet fmt fmt-check check smoke-help clean-cache

build:
	@mkdir -p "$(GOCACHE)" "$(GOMODCACHE)"
	@$(GOENV) $(GO) build $(GOFLAGS) $(PKG)

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

smoke-help:
	@mkdir -p "$(GOCACHE)" "$(GOMODCACHE)"
	@$(GOENV) $(GO) run ./cmd/proxyctl --help

clean-cache:
	@rm -rf .cache
