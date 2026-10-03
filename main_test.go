package main

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/radut/kube-dns-checker/internal/config"
	"github.com/radut/kube-dns-checker/internal/dnstest"
)

func freeTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func TestRunEndToEnd(t *testing.T) {
	srv := dnstest.Start(t)
	srv.Set("ok.test", dnstest.Behavior{Answers: []string{"10.0.0.1"}})
	srv.Set("bad.test", dnstest.Behavior{Drop: true})

	resolvConf := filepath.Join(t.TempDir(), "resolv.conf")
	host, port, _ := net.SplitHostPort(srv.Addr)
	if err := os.WriteFile(resolvConf, []byte("nameserver "+host+"\noptions ndots:5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addr := freeTCPAddr(t)

	// DEFAULT resolves to the fake server's host via resolv.conf, but on the
	// standard port 53, so the explicit host:port entry carries the traffic.
	t.Setenv("DOMAINS", "ok.test.,bad.test.")
	t.Setenv("NAMESERVERS", "DEFAULT,"+srv.Addr)
	t.Setenv("RESOLV_CONF", resolvConf)
	t.Setenv("PROTOCOLS", "udp,tcp")
	t.Setenv("INTERVAL", "100ms")
	t.Setenv("TIMEOUT", "100ms")
	t.Setenv("LISTEN_ADDR", addr)
	t.Setenv("LOG_LEVEL", "error")
	_ = port

	done := make(chan error, 1)
	go func() { done <- run() }()

	deadline := time.Now().Add(5 * time.Second)
	var body string
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/metrics")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			body = string(b)
			if strings.Contains(body, `dns_query_failures_total{domain="bad.test.",nameserver="`+srv.Addr+`",protocol="tcp",reason="timeout"}`) &&
				strings.Contains(body, `dns_query_success{domain="ok.test.",nameserver="`+srv.Addr+`",protocol="udp"} 1`) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(body, `dns_checker_info{query_type="A",resolver="dns",version="dev"} 1`) {
		t.Errorf("dns_checker_info missing or wrong version in:\n%s", body)
	}
	if !strings.Contains(body, "dns_query_duration_seconds_bucket") {
		t.Fatalf("metrics never showed expected series; last body:\n%s", body)
	}
	for _, path := range []string{"/live", "/ready"} {
		resp, err := http.Get("http://" + addr + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("%s: status %d body %q", path, resp.StatusCode, b)
		}
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop after SIGTERM")
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	t.Setenv("TIMEOUT", "never")
	if err := run(); err == nil || !strings.Contains(err.Error(), "TIMEOUT") {
		t.Errorf("expected TIMEOUT config error, got %v", err)
	}
}

func TestRunRejectsMissingResolvConf(t *testing.T) {
	t.Setenv("RESOLV_CONF", filepath.Join(t.TempDir(), "missing"))
	if err := run(); err == nil {
		t.Error("expected error for missing resolv.conf")
	}
}

func TestNewResolver(t *testing.T) {
	if _, err := newResolver(config.Config{Resolver: config.ResolverGo, QueryType: "SOA"}); err == nil {
		t.Error("go resolver with SOA should be rejected")
	}
	if r, err := newResolver(config.Config{Resolver: config.ResolverGo, QueryType: "A"}); err != nil || r == nil {
		t.Errorf("go resolver with A: %v", err)
	}
	if r, err := newResolver(config.Config{Resolver: config.ResolverDNS, QueryType: "SOA"}); err != nil || r == nil {
		t.Errorf("dns resolver with SOA: %v", err)
	}
}

func TestNewLoggerFormats(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		if l := newLogger(config.Config{LogLevel: "debug", LogFormat: format}); l == nil {
			t.Errorf("logger for %s is nil", format)
		}
	}
}
