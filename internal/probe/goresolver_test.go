package probe

import (
	"testing"

	"github.com/miekg/dns"

	"github.com/radut/kube-dns-checker/internal/config"
	"github.com/radut/kube-dns-checker/internal/dnstest"
)

func TestGoResolverSuccess(t *testing.T) {
	srv := dnstest.Start(t)
	srv.Set("go.test", dnstest.Behavior{Answers: []string{"10.5.5.5"}})

	res := query(t, NewGoResolver(), target(srv.Addr, "go.test.", config.ProtocolUDP))
	if !res.Success || len(res.Answers) != 1 || res.Answers[0] != "10.5.5.5" {
		t.Fatalf("expected success, got %+v", res)
	}
}

func TestGoResolverTCP(t *testing.T) {
	srv := dnstest.Start(t)
	srv.Set("gotcp.test", dnstest.Behavior{Answers: []string{"10.5.5.6"}})

	res := query(t, NewGoResolver(), target(srv.Addr, "gotcp.test.", config.ProtocolTCP))
	if !res.Success {
		t.Fatalf("expected success over tcp, got %+v", res)
	}
}

func TestGoResolverNXDomain(t *testing.T) {
	srv := dnstest.Start(t)
	res := query(t, NewGoResolver(), target(srv.Addr, "nope.test.", config.ProtocolUDP))
	if res.Success || res.Reason != "NXDOMAIN" {
		t.Fatalf("expected NXDOMAIN, got %+v", res)
	}
}

func TestGoResolverTimeout(t *testing.T) {
	srv := dnstest.Start(t)
	srv.Set("drop.test", dnstest.Behavior{Drop: true})
	res := query(t, NewGoResolver(), target(srv.Addr, "drop.test.", config.ProtocolUDP))
	if res.Success || res.Reason != ReasonTimeout {
		t.Fatalf("expected timeout, got %+v", res)
	}
}

func TestGoResolverUnsupportedType(t *testing.T) {
	srv := dnstest.Start(t)
	tgt := target(srv.Addr, "x.test.", config.ProtocolUDP)
	tgt.QueryType = dns.TypeSOA
	res := query(t, NewGoResolver(), tgt)
	if res.Success || res.Reason != ReasonUnsupported {
		t.Fatalf("expected unsupported, got %+v", res)
	}
	r := NewGoResolver()
	if r.SupportsQueryType("SOA") || !r.SupportsQueryType("A") {
		t.Error("SupportsQueryType mismatch")
	}
}
