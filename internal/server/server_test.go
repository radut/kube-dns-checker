package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/radut/kube-dns-checker/internal/config"
	"github.com/radut/kube-dns-checker/internal/metrics"
	"github.com/radut/kube-dns-checker/internal/probe"
)

type fakeHealth struct {
	started bool
	stale   []probe.Target
}

func (f *fakeHealth) Started() bool                      { return f.started }
func (f *fakeHealth) Stale(time.Duration) []probe.Target { return f.stale }

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(body)
}

func TestEndpoints(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	tgt := probe.Target{Nameserver: "10.0.0.1:53", Domain: "a.test.", Protocol: config.ProtocolUDP, QueryType: dns.TypeA}
	m.Init([]probe.Target{tgt}, "dns", "A", "test")
	health := &fakeHealth{}
	h := NewHandler(reg, health, Options{MaxStaleness: time.Minute})

	if code, body := get(t, h, "/"); code != 200 || !strings.Contains(body, "/metrics") {
		t.Errorf("/ = %d %q", code, body)
	}
	if code, _ := get(t, h, "/ready"); code != http.StatusServiceUnavailable {
		t.Errorf("/ready before start = %d, want 503", code)
	}
	if code, _ := get(t, h, "/live"); code != 200 {
		t.Errorf("/live before start = %d, want 200", code)
	}

	health.started = true
	if code, _ := get(t, h, "/ready"); code != 200 {
		t.Errorf("/ready after start = %d, want 200", code)
	}
	if code, _ := get(t, h, "/live"); code != 200 {
		t.Errorf("/live healthy = %d, want 200", code)
	}

	health.stale = []probe.Target{tgt}
	if code, body := get(t, h, "/live"); code != http.StatusServiceUnavailable || !strings.Contains(body, "a.test.") {
		t.Errorf("/live stale = %d %q, want 503 naming the target", code, body)
	}

	if code, body := get(t, h, "/metrics"); code != 200 || !strings.Contains(body, "dns_queries_total") {
		t.Errorf("/metrics = %d, body missing dns_queries_total", code)
	}
	if code, _ := get(t, h, "/nope"); code != http.StatusNotFound {
		t.Errorf("/nope = %d, want 404", code)
	}
}

func TestLiveWithoutStalenessCheck(t *testing.T) {
	reg := prometheus.NewRegistry()
	h := NewHandler(reg, &fakeHealth{started: true, stale: []probe.Target{{Domain: "x."}}}, Options{})
	if code, _ := get(t, h, "/live"); code != 200 {
		t.Errorf("/live with staleness disabled = %d, want 200", code)
	}
}
