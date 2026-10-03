// Package dnstest provides an in-process DNS server with scriptable
// behaviour (delays, drops, flakiness, truncation, rcodes) for tests.
package dnstest

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// FlakyMode selects what a flaky behaviour does on its failing turns.
type FlakyMode int

const (
	FlakyNone     FlakyMode = iota
	FlakyDrop               // do not answer at all
	FlakyServfail           // answer with SERVFAIL
	FlakyDelay              // answer after FlakyDelay
)

// Behavior scripts how the server answers queries for one name.
type Behavior struct {
	Rcode      int           // dns.RcodeSuccess, dns.RcodeNameError, ...
	Answers    []string      // IPv4/IPv6 literals returned as A/AAAA records
	Delay      time.Duration // sleep before answering
	Drop       bool          // never answer
	Truncate   bool          // over UDP, answer with TC set and no records
	FlakyEvery int           // every Nth query misbehaves according to FlakyMode
	FlakyMode  FlakyMode
	FlakyDelay time.Duration
}

// Server serves DNS over UDP and TCP on the same loopback port.
type Server struct {
	Addr string // host:port

	mu        sync.Mutex
	behaviors map[string]Behavior
	counts    map[string]int
	udp       *dns.Server
	tcp       *dns.Server
}

// Start launches a server and registers cleanup with t.
func Start(t testing.TB) *Server {
	t.Helper()
	s := &Server{behaviors: map[string]Behavior{}, counts: map[string]int{}}
	pc, ln := listenBoth(t)
	s.Addr = pc.LocalAddr().String()
	s.udp = &dns.Server{PacketConn: pc, Handler: s}
	s.tcp = &dns.Server{Listener: ln, Handler: s}
	go func() { _ = s.udp.ActivateAndServe() }()
	go func() { _ = s.tcp.ActivateAndServe() }()
	t.Cleanup(func() {
		_ = s.udp.Shutdown()
		_ = s.tcp.Shutdown()
	})
	return s
}

// listenBoth finds a port free for both UDP and TCP.
func listenBoth(t testing.TB) (net.PacketConn, net.Listener) {
	t.Helper()
	for i := 0; i < 20; i++ {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen udp: %v", err)
		}
		ln, err := net.Listen("tcp", pc.LocalAddr().String())
		if err == nil {
			return pc, ln
		}
		_ = pc.Close()
	}
	t.Fatal("could not find a port free for both udp and tcp")
	return nil, nil
}

// Set scripts the behaviour for a name (trailing dot optional).
func (s *Server) Set(name string, b Behavior) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.behaviors[dns.Fqdn(strings.ToLower(name))] = b
}

// Count returns how many queries were received for a name.
func (s *Server) Count(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[dns.Fqdn(strings.ToLower(name))]
}

// ServeDNS implements dns.Handler.
func (s *Server) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	if len(r.Question) == 0 {
		return
	}
	q := r.Question[0]
	name := strings.ToLower(q.Name)
	b, n := s.record(name)

	if b.FlakyEvery > 0 && n%b.FlakyEvery == 0 {
		b = applyFlaky(b)
	}
	if b.Delay > 0 {
		time.Sleep(b.Delay)
	}
	if b.Drop {
		return
	}

	m := new(dns.Msg)
	m.SetReply(r)
	m.Rcode = b.Rcode
	if b.Truncate && w.RemoteAddr().Network() == "udp" {
		m.Truncated = true
		_ = w.WriteMsg(m)
		return
	}
	if b.Rcode == dns.RcodeSuccess {
		m.Answer = buildAnswers(q, b.Answers)
	}
	_ = w.WriteMsg(m)
}

// record looks up the behaviour and bumps the query counter. Unknown names
// get NXDOMAIN.
func (s *Server) record(name string) (Behavior, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts[name]++
	b, ok := s.behaviors[name]
	if !ok {
		return Behavior{Rcode: dns.RcodeNameError}, s.counts[name]
	}
	return b, s.counts[name]
}

func applyFlaky(b Behavior) Behavior {
	switch b.FlakyMode {
	case FlakyDrop:
		b.Drop = true
	case FlakyServfail:
		b.Rcode = dns.RcodeServerFailure
	case FlakyDelay:
		b.Delay = b.FlakyDelay
	}
	return b
}

func buildAnswers(q dns.Question, ips []string) []dns.RR {
	var rrs []dns.RR
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		hdr := dns.RR_Header{Name: q.Name, Class: dns.ClassINET, Ttl: 30}
		switch {
		case q.Qtype == dns.TypeA && ip.To4() != nil:
			hdr.Rrtype = dns.TypeA
			rrs = append(rrs, &dns.A{Hdr: hdr, A: ip.To4()})
		case q.Qtype == dns.TypeAAAA && ip.To4() == nil:
			hdr.Rrtype = dns.TypeAAAA
			rrs = append(rrs, &dns.AAAA{Hdr: hdr, AAAA: ip})
		}
	}
	return rrs
}

// String describes the server for log output.
func (s *Server) String() string {
	return fmt.Sprintf("dnstest.Server(%s)", s.Addr)
}
