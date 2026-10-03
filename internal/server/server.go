// Package server exposes metrics and health endpoints.
package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/radut/kube-dns-checker/internal/probe"
)

// Health is what the liveness and readiness endpoints consult.
type Health interface {
	Started() bool
	Stale(maxAge time.Duration) []probe.Target
}

// Options configure the HTTP handler.
type Options struct {
	// MaxStaleness is how old a target's last probe may be before /live
	// reports failure. Zero disables the staleness check.
	MaxStaleness time.Duration
	Logger       *slog.Logger
}

const homePage = `<!doctype html>
<html><head><title>kube-dns-checker</title></head>
<body><h1>kube-dns-checker</h1>
<ul><li><a href="/metrics">/metrics</a></li><li><a href="/live">/live</a></li><li><a href="/ready">/ready</a></li></ul>
</body></html>`

// NewHandler builds the HTTP routes.
func NewHandler(gatherer prometheus.Gatherer, health Health, opts Options) http.Handler {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(homePage))
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{
		ErrorLog: slog.NewLogLogger(opts.Logger.Handler(), slog.LevelError),
	}))
	mux.HandleFunc("GET /ready", readyHandler(health))
	mux.HandleFunc("GET /live", liveHandler(health, opts.MaxStaleness))
	return mux
}

// readyHandler reports 200 once the probe loops are running. It does not
// reflect DNS health: a checker that observes failures must stay ready so
// its metrics keep being scraped.
func readyHandler(health Health) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if !health.Started() {
			http.Error(w, "probes not started", http.StatusServiceUnavailable)
			return
		}
		writeOK(w)
	}
}

// liveHandler reports 503 when a probe loop has stopped producing results,
// so Kubernetes restarts a wedged checker.
func liveHandler(health Health, maxStaleness time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if maxStaleness <= 0 || !health.Started() {
			writeOK(w)
			return
		}
		stale := health.Stale(maxStaleness)
		if len(stale) == 0 {
			writeOK(w)
			return
		}
		names := make([]string, 0, len(stale))
		for _, t := range stale {
			names = append(names, t.String())
		}
		http.Error(w, fmt.Sprintf("stale probes (no result within %s): %s", maxStaleness, strings.Join(names, "; ")), http.StatusServiceUnavailable)
	}
}

func writeOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}
