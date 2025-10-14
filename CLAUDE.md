# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

kube-dns-checker is a Kubernetes DNS monitoring tool that periodically queries DNS servers and exposes Prometheus metrics. It's designed to run as either a Deployment or DaemonSet in Kubernetes clusters to ensure DNS is functioning correctly across all nodes.

## Build and Development Commands

```bash
# Build the binary
go build kube-dns-checker.go

# Run locally
go run kube-dns-checker.go

# Build Docker image
docker build -t kube-dns-checker .

# Run Docker container locally
docker run -p8080:8080 kube-dns-checker

# Push to Docker Hub (example)
docker build -t radut/kube-dns-checker .
docker push radut/kube-dns-checker
```

## Architecture

### Core Components

**Single Binary Application**: All functionality is contained in `kube-dns-checker.go` - there is no multi-package structure.

**DNS Resolution Modes**: The application supports two DNS resolution methods:
- **DIG mode** (default): Uses the `dig` command-line tool via the `bitfield/script` library. Supports TCP fallback retry and custom dig arguments. Parses dig output to extract query time and response details.
- **GO Resolver mode**: Uses Go's built-in `net.Resolver` with custom dialers to query specific nameservers.

**Concurrency Model**:
- Uses goroutines with a semaphore pattern (`MAX_CONCURRENT = 2`) to limit concurrent DNS queries
- Each domain/nameserver combination is queried in a separate goroutine
- `sync.WaitGroup` ensures all queries complete before the next interval
- A global mutex protects Prometheus metric updates

**HTTP Server**: Exposes endpoints on port 8080:
- `/` - Simple HTML homepage with link to metrics
- `/metrics` - Prometheus metrics (mutex-protected)
- `/live` - Liveness probe endpoint
- `/ready` - Readiness probe endpoint

**Prometheus Metrics Exposed**:
- `dns_query_time_ms` - Query time in milliseconds (Gauge)
- `dns_query_success` - 1 for success, 0 for failure (Gauge)
- `dns_query_total_count` - Total query count (Gauge)
- `dns_query_success_count` - Success counter (Gauge)
- `dns_query_fail_count` - Failure counter (Gauge)

All metrics are labeled by `nameserver` and `domain`.

**Timer Loop**: Uses `time.Ticker` to trigger DNS checks at the configured interval. Initial check runs immediately on startup, subsequent checks run on each tick.

### Configuration via Environment Variables

All configuration is done through environment variables:

- `GO_RESOLVER` - boolean, use Go resolver (true) or dig (false), default: false
- `DOMAINS` - comma-separated list of domains to query, default: "www.google.com"
- `NAMESERVERS` - comma-separated nameservers, use "DEFAULT" for /etc/resolv.conf, default: "DEFAULT"
- `TIMEOUT` - query timeout duration, default: "3s"
- `INTERVAL` - check interval duration, default: "5s"
- `DEBUG` - boolean, enable debug logging, default: false
- `DIG_RETRY_TCP` - boolean, retry failed queries with TCP, default: false (only for DIG mode)
- `DIG_ARGS` - space-separated additional dig arguments (only for DIG mode)

### Kubernetes Deployment

The application can be deployed in two modes:

1. **Deployment** (`kubernetes/deployment-kube-dns-checker.yml`): Runs 2 replicas with pod anti-affinity to spread across nodes
2. **DaemonSet** (`kubernetes/ds-kube-dns-checker.yml`): Runs one pod per node for comprehensive cluster-wide DNS monitoring

Both include:
- Extensive tolerations to run on all nodes including masters and tainted nodes
- Prometheus scrape annotations for automatic metric collection
- Readiness probes on `/ready` endpoint
- Resource limits: 500m CPU, 100Mi memory

### Docker Image

Multi-stage Dockerfile:
- Stage 1: Build Go binary in `golang:latest` with CGO disabled for static binary
- Stage 2: Run in `alpine:latest` with `bind-tools` package (provides dig command)

## Prometheus Alert Example

From README, recommended alert query:
```promql
sum(rate(dns_query_fail_count[1m])) by (kubernetes_node,node_ip,job) / sum(rate(dns_query_total_count[1m])) by (kubernetes_node,node_ip,job) * 100 > 0
```

This alerts when error rate exceeds 0% over a 1-minute window.

## Known Issues

Bug in `resetCounters` function at kube-dns-checker.go:445 - it iterates over `domains` twice instead of iterating over `dnsServers` in the inner loop. This causes incorrect counter initialization.
