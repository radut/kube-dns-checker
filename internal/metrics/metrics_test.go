package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/radut/kube-dns-checker/internal/config"
	"github.com/radut/kube-dns-checker/internal/probe"
)

func TestObserve(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	tgt := probe.Target{Nameserver: "10.0.0.1:53", Domain: "a.test.", Protocol: config.ProtocolUDP, QueryType: dns.TypeA}
	m.Init([]probe.Target{tgt}, "dns", "A")

	at := time.Unix(1_700_000_000, 0)
	m.Observe(probe.Result{Target: tgt, Success: true, Duration: 20 * time.Millisecond}, at)
	m.Observe(probe.Result{Target: tgt, Reason: "SERVFAIL", Duration: 5 * time.Millisecond}, at.Add(time.Second))
	m.Observe(probe.Result{Target: tgt, Reason: probe.ReasonTimeout, Duration: 2 * time.Second}, at.Add(2*time.Second))

	labels := prometheus.Labels{"nameserver": "10.0.0.1:53", "domain": "a.test.", "protocol": "udp"}
	if got := testutil.ToFloat64(m.Queries.With(labels)); got != 3 {
		t.Errorf("queries = %v, want 3", got)
	}
	if got := testutil.ToFloat64(m.Success.With(labels)); got != 0 {
		t.Errorf("success gauge = %v, want 0", got)
	}
	if got := testutil.ToFloat64(m.LastCheck.With(labels)); got != float64(at.Unix()+2) {
		t.Errorf("last check = %v", got)
	}
	for _, reason := range []string{"SERVFAIL", probe.ReasonTimeout} {
		l := prometheus.Labels{"nameserver": "10.0.0.1:53", "domain": "a.test.", "protocol": "udp", "reason": reason}
		if got := testutil.ToFloat64(m.Failures.With(l)); got != 1 {
			t.Errorf("failures[%s] = %v, want 1", reason, got)
		}
	}
	if got := testutil.CollectAndCount(m.Duration); got != 1 {
		t.Errorf("duration series = %d, want 1", got)
	}
	if got := testutil.ToFloat64(m.Info.WithLabelValues("dns", "A")); got != 1 {
		t.Errorf("info = %v", got)
	}
}

func TestInitCreatesSeriesBeforeAnyProbe(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	tgt := probe.Target{Nameserver: "10.0.0.1:53", Domain: "a.test.", Protocol: config.ProtocolTCP, QueryType: dns.TypeA}
	m.Init([]probe.Target{tgt}, "dns", "A")

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range families {
		names = append(names, f.GetName())
	}
	joined := strings.Join(names, " ")
	for _, want := range []string{"dns_queries_total", "dns_query_success", "dns_query_duration_seconds", "dns_checker_info"} {
		if !strings.Contains(joined, want) {
			t.Errorf("metric %s missing after Init; got %s", want, joined)
		}
	}
}
