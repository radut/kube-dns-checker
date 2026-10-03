package probe

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/radut/kube-dns-checker/internal/config"
	"github.com/radut/kube-dns-checker/internal/dnstest"
)

const testTimeout = 300 * time.Millisecond

func target(server, domain string, proto config.Protocol) Target {
	return Target{Nameserver: server, Domain: domain, Protocol: proto, QueryType: dns.TypeA}
}

func query(t *testing.T, r Resolver, tgt Target) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	return r.Query(ctx, tgt)
}

func TestDNSClientSuccess(t *testing.T) {
	srv := dnstest.Start(t)
	srv.Set("ok.test", dnstest.Behavior{Answers: []string{"10.1.2.3", "10.1.2.4"}})

	res := query(t, NewDNSClient(), target(srv.Addr, "ok.test", config.ProtocolUDP))
	if !res.Success || res.Reason != "" || res.Rcode != "NOERROR" {
		t.Fatalf("expected success, got %+v", res)
	}
	if len(res.Answers) != 2 || res.Answers[0] != "10.1.2.3" || res.Answers[1] != "10.1.2.4" {
		t.Errorf("answers = %v, want compact rdata", res.Answers)
	}
	if res.Duration <= 0 || res.Duration > testTimeout {
		t.Errorf("duration = %v", res.Duration)
	}
}

func TestDNSClientRcodes(t *testing.T) {
	srv := dnstest.Start(t)
	srv.Set("servfail.test", dnstest.Behavior{Rcode: dns.RcodeServerFailure})
	srv.Set("refused.test", dnstest.Behavior{Rcode: dns.RcodeRefused})
	srv.Set("nodata.test", dnstest.Behavior{Rcode: dns.RcodeSuccess})

	cases := map[string]string{
		"missing.test":  "NXDOMAIN",
		"servfail.test": "SERVFAIL",
		"refused.test":  "REFUSED",
		"nodata.test":   ReasonNoAnswer,
	}
	for domain, want := range cases {
		res := query(t, NewDNSClient(), target(srv.Addr, domain, config.ProtocolUDP))
		if res.Success || res.Reason != want {
			t.Errorf("%s: success=%v reason=%q, want reason %q", domain, res.Success, res.Reason, want)
		}
	}
}

func TestDNSClientTimeoutIsFast(t *testing.T) {
	srv := dnstest.Start(t)
	srv.Set("drop.test", dnstest.Behavior{Drop: true})

	start := time.Now()
	res := query(t, NewDNSClient(), target(srv.Addr, "drop.test", config.ProtocolUDP))
	elapsed := time.Since(start)
	if res.Success || res.Reason != ReasonTimeout {
		t.Fatalf("expected timeout, got %+v", res)
	}
	if elapsed > testTimeout+200*time.Millisecond {
		t.Errorf("timeout took %v, want about %v", elapsed, testTimeout)
	}
}

func TestDNSClientMeasuresDelay(t *testing.T) {
	srv := dnstest.Start(t)
	const delay = 80 * time.Millisecond
	srv.Set("slow.test", dnstest.Behavior{Answers: []string{"10.0.0.1"}, Delay: delay})

	res := query(t, NewDNSClient(), target(srv.Addr, "slow.test", config.ProtocolUDP))
	if !res.Success {
		t.Fatalf("expected success, got %+v", res)
	}
	if res.Duration < delay {
		t.Errorf("duration %v shorter than server delay %v", res.Duration, delay)
	}
}

func TestDNSClientTruncatedFallsBackToTCP(t *testing.T) {
	srv := dnstest.Start(t)
	srv.Set("big.test", dnstest.Behavior{Answers: []string{"10.0.0.1"}, Truncate: true})

	res := query(t, NewDNSClient(), target(srv.Addr, "big.test", config.ProtocolUDP))
	if !res.Success || !res.TCPFallback {
		t.Fatalf("expected tcp fallback success, got %+v", res)
	}
	if srv.Count("big.test") != 2 {
		t.Errorf("expected 2 queries (udp + tcp), got %d", srv.Count("big.test"))
	}
}

func TestDNSClientTCP(t *testing.T) {
	srv := dnstest.Start(t)
	srv.Set("tcp.test", dnstest.Behavior{Answers: []string{"10.0.0.9"}})

	res := query(t, NewDNSClient(), target(srv.Addr, "tcp.test", config.ProtocolTCP))
	if !res.Success || res.TCPFallback {
		t.Fatalf("expected plain tcp success, got %+v", res)
	}
}

func TestDNSClientConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	res := query(t, NewDNSClient(), target(addr, "x.test", config.ProtocolTCP))
	if res.Success || res.Reason != ReasonNetworkError {
		t.Fatalf("expected network_error, got %+v", res)
	}
}

func TestRunRetriesFlakyServer(t *testing.T) {
	srv := dnstest.Start(t)
	// Every odd query (1st, 3rd, ...) is dropped; even ones succeed.
	srv.Set("flaky.test", dnstest.Behavior{Answers: []string{"10.0.0.1"}, FlakyEvery: 1, FlakyMode: dnstest.FlakyDrop})
	srv.Set("flaky.test", dnstest.Behavior{Answers: []string{"10.0.0.1"}, FlakyEvery: 2, FlakyMode: dnstest.FlakyDrop})

	tgt := target(srv.Addr, "flaky.test", config.ProtocolUDP)
	ctx := context.Background()

	// 1st query: ok (1%2 != 0). 2nd: dropped. With attempts=1 the 2nd run fails.
	if res := Run(ctx, NewDNSClient(), tgt, 1, testTimeout); !res.Success {
		t.Fatalf("first run should succeed: %+v", res)
	}
	if res := Run(ctx, NewDNSClient(), tgt, 1, testTimeout); res.Success || res.Reason != ReasonTimeout {
		t.Fatalf("second run should time out: %+v", res)
	}
	// 3rd ok. 4th dropped, 5th ok: attempts=2 turns the drop into a retry success.
	Run(ctx, NewDNSClient(), tgt, 1, testTimeout)
	res := Run(ctx, NewDNSClient(), tgt, 2, testTimeout)
	if !res.Success || res.Attempts != 2 {
		t.Fatalf("expected success on attempt 2, got %+v", res)
	}
}

func TestRunStopsAfterAttempts(t *testing.T) {
	srv := dnstest.Start(t)
	srv.Set("dead.test", dnstest.Behavior{Drop: true})

	start := time.Now()
	res := Run(context.Background(), NewDNSClient(), target(srv.Addr, "dead.test", config.ProtocolUDP), 3, 50*time.Millisecond)
	if res.Success || res.Attempts != 3 || res.Reason != ReasonTimeout {
		t.Fatalf("expected 3 timed-out attempts, got %+v", res)
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond || elapsed > 600*time.Millisecond {
		t.Errorf("3 attempts of 50ms took %v", elapsed)
	}
}

func TestRunHonoursCancelledContext(t *testing.T) {
	srv := dnstest.Start(t)
	srv.Set("dead.test", dnstest.Behavior{Drop: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := Run(ctx, NewDNSClient(), target(srv.Addr, "dead.test", config.ProtocolUDP), 5, time.Second)
	if res.Attempts != 1 {
		t.Errorf("cancelled context should stop after one attempt, got %d", res.Attempts)
	}
}

func TestFlakyServfailAndDelay(t *testing.T) {
	srv := dnstest.Start(t)
	srv.Set("sf.test", dnstest.Behavior{Answers: []string{"10.0.0.1"}, FlakyEvery: 2, FlakyMode: dnstest.FlakyServfail})
	srv.Set("sd.test", dnstest.Behavior{Answers: []string{"10.0.0.1"}, FlakyEvery: 2, FlakyMode: dnstest.FlakyDelay, FlakyDelay: 60 * time.Millisecond})

	c := NewDNSClient()
	if res := query(t, c, target(srv.Addr, "sf.test", config.ProtocolUDP)); !res.Success {
		t.Errorf("1st sf query should succeed: %+v", res)
	}
	if res := query(t, c, target(srv.Addr, "sf.test", config.ProtocolUDP)); res.Success || res.Reason != "SERVFAIL" {
		t.Errorf("2nd sf query should SERVFAIL: %+v", res)
	}
	fast := query(t, c, target(srv.Addr, "sd.test", config.ProtocolUDP))
	slow := query(t, c, target(srv.Addr, "sd.test", config.ProtocolUDP))
	if !fast.Success || !slow.Success {
		t.Fatalf("sd queries should succeed: %+v %+v", fast, slow)
	}
	if slow.Duration < 60*time.Millisecond || slow.Duration <= fast.Duration {
		t.Errorf("expected 2nd query to be delayed: fast=%v slow=%v", fast.Duration, slow.Duration)
	}
}

func TestBuildTargets(t *testing.T) {
	targets, err := BuildTargets([]string{"a:53", "b:53"}, []string{"x.", "y."}, []config.Protocol{config.ProtocolUDP, config.ProtocolTCP}, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 8 {
		t.Fatalf("expected 8 targets, got %d", len(targets))
	}
	if targets[0].QueryType != dns.TypeA || targets[0].QueryTypeName() != "A" {
		t.Errorf("query type = %v", targets[0].QueryType)
	}
	keys := map[string]bool{}
	for _, tg := range targets {
		keys[tg.Key()] = true
	}
	if len(keys) != 8 {
		t.Errorf("target keys are not unique: %v", keys)
	}
	if _, err := BuildTargets([]string{"a:53"}, []string{"x."}, []config.Protocol{config.ProtocolUDP}, "NOPE"); err == nil {
		t.Error("expected error for unknown query type")
	}
}
