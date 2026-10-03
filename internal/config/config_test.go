package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) Getenv {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Resolver != ResolverDNS {
		t.Errorf("resolver = %q, want dns", cfg.Resolver)
	}
	if len(cfg.Domains) != 1 || cfg.Domains[0] != DefaultDomains {
		t.Errorf("domains = %v", cfg.Domains)
	}
	if len(cfg.Nameservers) != 1 || cfg.Nameservers[0] != NameserverDefault {
		t.Errorf("nameservers = %v", cfg.Nameservers)
	}
	if cfg.Timeout != DefaultTimeout || cfg.Interval != DefaultInterval {
		t.Errorf("timeout/interval = %v/%v", cfg.Timeout, cfg.Interval)
	}
	if cfg.Attempts != DefaultAttempts || cfg.Concurrency != DefaultConcurrency {
		t.Errorf("attempts/concurrency = %d/%d", cfg.Attempts, cfg.Concurrency)
	}
	if cfg.QueryType != "A" || len(cfg.Protocols) != 1 || cfg.Protocols[0] != ProtocolUDP {
		t.Errorf("query type/protocols = %s/%v", cfg.QueryType, cfg.Protocols)
	}
}

func TestLoadFull(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"RESOLVER":    "go",
		"DOMAINS":     " a.example., b.example. ,a.example.,",
		"NAMESERVERS": "DEFAULT, 10.0.0.1, 10.0.0.2:5353, [::1]:53, ::1, default",
		"PROTOCOLS":   "UDP,tcp",
		"QUERY_TYPE":  "aaaa",
		"TIMEOUT":     "250ms",
		"INTERVAL":    "1s",
		"ATTEMPTS":    "3",
		"CONCURRENCY": "2",
		"LISTEN_ADDR": ":9090",
		"LOG_LEVEL":   "DEBUG",
		"LOG_FORMAT":  "json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Resolver != ResolverGo {
		t.Errorf("resolver = %q", cfg.Resolver)
	}
	if got := strings.Join(cfg.Domains, ","); got != "a.example.,b.example." {
		t.Errorf("domains = %q", got)
	}
	wantNS := "DEFAULT,10.0.0.1:53,10.0.0.2:5353,[::1]:53"
	if got := strings.Join(cfg.Nameservers, ","); got != wantNS {
		t.Errorf("nameservers = %q, want %q", got, wantNS)
	}
	if len(cfg.Protocols) != 2 || cfg.Protocols[1] != ProtocolTCP {
		t.Errorf("protocols = %v", cfg.Protocols)
	}
	if cfg.QueryType != "AAAA" {
		t.Errorf("query type = %q", cfg.QueryType)
	}
	if cfg.Timeout != 250*time.Millisecond || cfg.Interval != time.Second {
		t.Errorf("timeout/interval = %v/%v", cfg.Timeout, cfg.Interval)
	}
	if cfg.Attempts != 3 || cfg.Concurrency != 2 || cfg.ListenAddr != ":9090" {
		t.Errorf("attempts/concurrency/addr = %d/%d/%s", cfg.Attempts, cfg.Concurrency, cfg.ListenAddr)
	}
	if cfg.LogLevel != "debug" || cfg.LogFormat != "json" {
		t.Errorf("log = %s/%s", cfg.LogLevel, cfg.LogFormat)
	}
}

func TestLegacyFlags(t *testing.T) {
	cfg, err := Load(env(map[string]string{"GO_RESOLVER": "true", "DEBUG": "true"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Resolver != ResolverGo || cfg.LogLevel != "debug" {
		t.Errorf("legacy flags ignored: %+v", cfg)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := map[string]map[string]string{
		"bad resolver":     {"RESOLVER": "dig"},
		"empty domains":    {"DOMAINS": " , "},
		"bad domain":       {"DOMAINS": "foo bar"},
		"bad nameserver":   {"NAMESERVERS": "10.0.0.1:abc"},
		"bad protocol":     {"PROTOCOLS": "sctp"},
		"bad query type":   {"QUERY_TYPE": "FOO"},
		"bad timeout":      {"TIMEOUT": "3"},
		"tiny timeout":     {"TIMEOUT": "1ms"},
		"bad interval":     {"INTERVAL": "soon"},
		"bad attempts":     {"ATTEMPTS": "0"},
		"huge concurrency": {"CONCURRENCY": "99999"},
		"bad log level":    {"LOG_LEVEL": "loud"},
		"bad log format":   {"LOG_FORMAT": "xml"},
	}
	for name, vars := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(env(vars)); err == nil {
				t.Errorf("expected an error for %v", vars)
			}
		})
	}
}

func TestLoadReportsAllErrors(t *testing.T) {
	_, err := Load(env(map[string]string{"TIMEOUT": "x", "INTERVAL": "y"}))
	if err == nil || !strings.Contains(err.Error(), "TIMEOUT") || !strings.Contains(err.Error(), "INTERVAL") {
		t.Errorf("expected both errors, got %v", err)
	}
}

func TestNormalizeNameserver(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4":        "1.2.3.4:53",
		"1.2.3.4:5353":   "1.2.3.4:5353",
		"::1":            "[::1]:53",
		"[::1]:5353":     "[::1]:5353",
		"dns.local":      "dns.local:53",
		"dns.local:1053": "dns.local:1053",
	}
	for in, want := range cases {
		got, err := NormalizeNameserver(in)
		if err != nil || got != want {
			t.Errorf("NormalizeNameserver(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", ":53", "1.2.3.4:x", "a b"} {
		if _, err := NormalizeNameserver(bad); err == nil {
			t.Errorf("NormalizeNameserver(%q) expected error", bad)
		}
	}
}
