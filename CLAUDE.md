# CLAUDE.md

Guidance for Claude Code when working in this repository.

## Project Overview

kube-dns-checker probes DNS servers on a schedule and exposes Prometheus
metrics. It runs as a DaemonSet or Deployment in Kubernetes to show, per node,
whether DNS works, how fast it is and why it fails. See README.md for
configuration, metrics and endpoints.

## Commands

```bash
go build -o kube-dns-checker .     # build
go test -race ./...                # tests (no network needed)
go vet ./... && gofmt -l .         # lint
docker build -t kube-dns-checker . # image
```

If `go` on PATH is the MacPorts build and fails with a toolchain version
mismatch, use `/usr/local/go/bin/go` with `GOTOOLCHAIN=local`.

## Layout

| Path | Purpose |
|------|---------|
| `main.go` | Wires config, resolver, metrics, scheduler and HTTP server. Signal handling and graceful shutdown. |
| `internal/config` | Environment parsing and validation. All validation lives here. |
| `internal/probe` | `Target`, `Result`, the `Resolver` interface, `Run` (attempts), `BuildTargets`, resolv.conf parsing. `dnsclient.go` is the miekg/dns resolver, `goresolver.go` the net.Resolver one. |
| `internal/scheduler` | One goroutine + ticker per target, global concurrency semaphore, staleness tracking for liveness. |
| `internal/metrics` | Prometheus collectors and `Observe`. |
| `internal/server` | `/`, `/metrics`, `/ready`, `/live`. |
| `internal/dnstest` | In-process DNS server for tests: delays, drops, truncation, rcodes, flakiness. |
| `kubernetes/` | DaemonSet, Deployment, namespace and alert rules. |

## Design notes

* A probe succeeds only on `NOERROR` with at least one answer. Any rcode,
  timeout, network error or empty answer is a failure with a `reason` label.
* Targets are independent on purpose: a dead nameserver must not reduce the
  sampling rate of the others. The same target never overlaps with itself.
* `/ready` never reflects DNS health. `/live` reflects whether the probe
  loops are still producing results.
* Results from probes cancelled by shutdown are discarded, not counted as
  failures.
* Metrics are a Histogram for latency and Counters for totals/failures.
  Changing label sets is a breaking change for dashboards and alerts.
* The runtime image keeps `bind-tools` so `dig` is available via
  `kubectl exec` for manual troubleshooting.
