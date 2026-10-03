VERSION   := $(shell tr -d '[:space:]' < VERSION)
VCS_REF   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
IMAGE     ?= radut/kube-dns-checker
PLATFORMS ?= linux/amd64,linux/arm64,linux/arm/v7,linux/arm/v6,linux/386,linux/ppc64le,linux/s390x,linux/riscv64
GO        ?= go
LDFLAGS   := -s -w -X main.version=$(VERSION)

.PHONY: build test lint image image-multiarch version

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o kube-dns-checker .

test:
	$(GO) test -race -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

lint:
	test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	$(GO) vet ./...

image:
	docker build --build-arg VERSION=$(VERSION) --build-arg VCS_REF=$(VCS_REF) -t $(IMAGE):v$(VERSION) -t $(IMAGE):latest .

image-multiarch:
	docker buildx build --platform $(PLATFORMS) --provenance=false \
		--build-arg VERSION=$(VERSION) --build-arg VCS_REF=$(VCS_REF) \
		-t $(IMAGE):v$(VERSION) -t $(IMAGE):latest --push .

version:
	@echo v$(VERSION)
