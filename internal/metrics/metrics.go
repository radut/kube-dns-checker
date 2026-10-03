// Package metrics defines the Prometheus metrics exposed by the checker.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/radut/kube-dns-checker/internal/probe"
)

var probeLabels = []string{"nameserver", "domain", "protocol"}

// Buckets cover 1ms to 5s, which spans healthy in-cluster lookups up to a
// typical timeout.
var durationBuckets = []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5}

// Metrics holds every collector. Observe is safe for concurrent use.
type Metrics struct {
	Duration  *prometheus.HistogramVec
	Queries   *prometheus.CounterVec
	Failures  *prometheus.CounterVec
	Success   *prometheus.GaugeVec
	LastCheck *prometheus.GaugeVec
	Info      *prometheus.GaugeVec
}

// New registers all metrics with reg.
func New(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "dns_query_duration_seconds",
			Help:    "DNS query round-trip time in seconds (final attempt, including TCP fallback).",
			Buckets: durationBuckets,
		}, probeLabels),
		Queries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dns_queries_total",
			Help: "Total DNS probes run.",
		}, probeLabels),
		Failures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dns_query_failures_total",
			Help: "DNS probes that failed, by reason (rcode, timeout, network_error, no_answer).",
		}, append(probeLabels, "reason")),
		Success: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dns_query_success",
			Help: "Result of the most recent probe: 1 success, 0 failure.",
		}, probeLabels),
		LastCheck: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dns_last_check_timestamp_seconds",
			Help: "Unix time of the most recent probe.",
		}, probeLabels),
		Info: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dns_checker_info",
			Help: "Static information about the checker configuration.",
		}, []string{"resolver", "query_type", "version"}),
	}
	reg.MustRegister(m.Duration, m.Queries, m.Failures, m.Success, m.LastCheck, m.Info)
	return m
}

// Init pre-creates the per-target series so rate() works from the first
// scrape and absent targets are visible.
func (m *Metrics) Init(targets []probe.Target, resolver, queryType, version string) {
	m.Info.WithLabelValues(resolver, queryType, version).Set(1)
	for _, t := range targets {
		labels := labelsFor(t)
		m.Queries.With(labels).Add(0)
		m.Success.With(labels)
		m.Duration.With(labels)
	}
}

// Observe records one probe result.
func (m *Metrics) Observe(res probe.Result, at time.Time) {
	labels := labelsFor(res.Target)
	m.Queries.With(labels).Inc()
	m.Duration.With(labels).Observe(res.Duration.Seconds())
	m.LastCheck.With(labels).Set(float64(at.Unix()))
	if res.Success {
		m.Success.With(labels).Set(1)
		return
	}
	m.Success.With(labels).Set(0)
	m.Failures.With(prometheus.Labels{
		"nameserver": res.Target.Nameserver,
		"domain":     res.Target.Domain,
		"protocol":   string(res.Target.Protocol),
		"reason":     res.Reason,
	}).Inc()
}

func labelsFor(t probe.Target) prometheus.Labels {
	return prometheus.Labels{
		"nameserver": t.Nameserver,
		"domain":     t.Domain,
		"protocol":   string(t.Protocol),
	}
}
