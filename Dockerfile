# syntax=docker/dockerfile:1

# The build stage always runs on the host's native platform and
# cross-compiles for the target, so multi-arch builds need no emulation
# for the Go compile step:
#   docker buildx build --platform linux/amd64,linux/arm64,linux/arm/v7 -t radut/kube-dns-checker --push .
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags="-s -w" -o /out/kube-dns-checker .

# bind-tools (dig, nslookup, host) are kept on purpose: this is a
# troubleshooting pod and having them available via kubectl exec is useful.
FROM alpine:3.21
RUN apk --no-cache add ca-certificates bind-tools \
    && addgroup -S -g 10001 dnscheck \
    && adduser -S -u 10001 -G dnscheck -H -s /sbin/nologin dnscheck

COPY --from=builder /out/kube-dns-checker /usr/local/bin/kube-dns-checker

USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/kube-dns-checker"]
