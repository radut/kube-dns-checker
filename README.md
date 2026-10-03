# kube-dns-checker

Probes DNS servers on a schedule and exposes the results as Prometheus
metrics. Run it as a DaemonSet to see, per node, whether DNS works, how fast
it is, and *why* it fails (timeout vs SERVFAIL vs NXDOMAIN, UDP vs TCP, which
nameserver).

## How it works

* Every nameserver × domain × protocol combination is a **target** with its
  own ticker, so a dead nameserver never delays sampling of the others.
* Two resolver implementations:
  * `RESOLVER=dns` (default): raw DNS packets via `miekg/dns`. Exact response
    code and RTT, sub-second timeouts, UDP or TCP. Names are queried exactly
    as given, so use FQDNs with a trailing dot.
  * `RESOLVER=go`: Go's `net.Resolver`, which behaves like an application:
    search domains and `ndots` from `resolv.conf` apply. Failure reasons are
    approximate because the stdlib hides the rcode.
* `NAMESERVERS=DEFAULT` expands to the `nameserver` entries of
  `/etc/resolv.conf` (the kube-dns service IP inside a pod). Mix it with
  CoreDNS pod IPs, node-local-dns (`169.254.20.10`) or an upstream (`8.8.8.8`)
  to tell the paths apart.

## Run

```bash
make build          # go build with the version from the VERSION file
make test lint
DOMAINS=www.google.com. NAMESERVERS=1.1.1.1,8.8.8.8 PROTOCOLS=udp,tcp TIMEOUT=500ms ./kube-dns-checker

docker build -t radut/kube-dns-checker .
docker run --rm -p 8080:8080 -e NAMESERVERS=1.1.1.1 radut/kube-dns-checker

kubectl apply -f kubernetes/00-namespace.yml -f kubernetes/ds-kube-dns-checker.yml
```

### Versioning and CI

`VERSION` holds the release version (currently 2.0.0) and is the single
source of truth. `make build` and the Dockerfile inject it into the binary;
it appears in the startup log, the `dns_checker_info{version}` metric and
the image's `org.opencontainers.image.version` label.

`.gitlab-ci.yml` runs the tests, then pushes multi-arch images to Nexus,
Docker Hub and the GitLab registry:

| Pipeline for      | Tags pushed                          |
|-------------------|--------------------------------------|
| `master`          | `latest`, `v<VERSION>`, `<short sha>` |
| git tag `vX.Y.Z`  | `vX.Y.Z`, `<short sha>` (must equal `v<VERSION>`) |
| other branches    | `<branch slug>`, `<short sha>`        |

To release: bump `VERSION`, commit, then `git tag v$(cat VERSION) && git push --tags`.

### Multi-arch image

The build stage cross-compiles on the host, so only the small alpine stage
runs under QEMU. Any platform alpine supports works:

```bash
docker buildx create --name multiarch --driver docker-container --use   # once
docker buildx build \
  --platform linux/amd64,linux/arm64,linux/arm/v7,linux/arm/v6,linux/386,linux/ppc64le,linux/s390x,linux/riscv64 \
  -t radut/kube-dns-checker:latest --push .
```

## Configuration

| Variable      | Default            | Description |
|---------------|--------------------|-------------|
| `RESOLVER`    | `dns`              | `dns` (raw client) or `go` (net.Resolver). `GO_RESOLVER=true` still works. |
| `DOMAINS`     | `www.google.com.`  | Comma separated names to look up. Use trailing dots. |
| `NAMESERVERS` | `DEFAULT`          | Comma separated `DEFAULT`, `ip`, `ip:port`, `[ipv6]:port`. Port defaults to 53. |
| `PROTOCOLS`   | `udp`              | `udp`, `tcp` or both. Each is probed separately. Truncated UDP answers are retried over TCP automatically. |
| `QUERY_TYPE`  | `A`                | A, AAAA, CNAME, MX, NS, TXT, SRV, PTR, SOA (`go` resolver: no SRV/SOA). |
| `TIMEOUT`     | `2s`               | Per attempt. Milliseconds are fine, e.g. `250ms`. Minimum `10ms`. |
| `INTERVAL`    | `5s`               | Time between probes of the same target. |
| `ATTEMPTS`    | `1`                | Tries per probe before it counts as failed. `2` hides single packet drops. |
| `CONCURRENCY` | `8`                | Max probes in flight at once. |
| `LISTEN_ADDR` | `:8080`            | HTTP listen address. |
| `RESOLV_CONF` | `/etc/resolv.conf` | File read for `DEFAULT`. |
| `LOG_LEVEL`   | `info`             | `debug`, `info`, `warn`, `error`. `DEBUG=true` still works. |
| `LOG_FORMAT`  | `text`             | `text` or `json`. |

Successful lookups are logged at `info`, failures at `warn`. Set
`LOG_LEVEL=warn` on busy clusters.

## Endpoints

* `/metrics` Prometheus metrics
* `/ready` 200 once the probe loops run. It does **not** reflect DNS health:
  a checker that observes failures must stay ready so it keeps being scraped.
* `/live` 503 when a target has produced no result for 3 intervals, so
  Kubernetes restarts a wedged checker.

## Metrics

All probe metrics carry `nameserver`, `domain` and `protocol` labels.

| Metric | Type | Description |
|--------|------|-------------|
| `dns_query_duration_seconds` | histogram | Round-trip time of the final attempt. |
| `dns_queries_total` | counter | Probes run. |
| `dns_query_failures_total` | counter | Failed probes, with a `reason` label: an rcode (`NXDOMAIN`, `SERVFAIL`, `REFUSED`, ...), `timeout`, `network_error`, `no_answer`. |
| `dns_query_success` | gauge | 1 if the most recent probe succeeded, else 0. |
| `dns_last_check_timestamp_seconds` | gauge | Unix time of the most recent probe. |
| `dns_checker_info` | gauge | `resolver` and `query_type` labels. |

A probe is successful only when the response is `NOERROR` **and** contains at
least one answer record.

### Useful queries

```promql
# failure ratio per node and nameserver
sum(rate(dns_query_failures_total[2m])) by (kubernetes_node, nameserver)
  / sum(rate(dns_queries_total[2m])) by (kubernetes_node, nameserver)

# why it fails
sum(rate(dns_query_failures_total[5m])) by (reason, nameserver, protocol)

# p99 latency
histogram_quantile(0.99, sum(rate(dns_query_duration_seconds_bucket[5m])) by (le, nameserver, protocol))
```

See `kubernetes/alert.rules` for alert examples.

## Development

```bash
go test -race ./...
go test -race -coverprofile=cover.out ./... && go tool cover -func=cover.out
```

Tests use an in-process DNS server (`internal/dnstest`) that can delay, drop,
truncate or flake on demand, so no network access is needed.
