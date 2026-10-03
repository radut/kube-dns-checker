package dnstest

import (
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestServerAnswersAndCounts(t *testing.T) {
	s := Start(t)
	s.Set("A.Test", Behavior{Answers: []string{"10.0.0.1", "fd00::1", "not-an-ip"}})
	if !strings.Contains(s.String(), s.Addr) {
		t.Errorf("String() = %q", s.String())
	}

	m := new(dns.Msg)
	m.SetQuestion("a.test.", dns.TypeA)
	c := &dns.Client{Timeout: time.Second}
	resp, _, err := c.Exchange(m, s.Addr)
	if err != nil || resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
		t.Fatalf("A query: err=%v resp=%v", err, resp)
	}
	m.SetQuestion("a.test.", dns.TypeAAAA)
	resp, _, err = c.Exchange(m, s.Addr)
	if err != nil || len(resp.Answer) != 1 {
		t.Fatalf("AAAA query: err=%v resp=%v", err, resp)
	}
	if s.Count("a.test") != 2 || s.Count("other.test") != 0 {
		t.Errorf("counts: %d %d", s.Count("a.test"), s.Count("other.test"))
	}

	m.SetQuestion("unknown.test.", dns.TypeA)
	resp, _, err = c.Exchange(m, s.Addr)
	if err != nil || resp.Rcode != dns.RcodeNameError {
		t.Errorf("unknown name should be NXDOMAIN: err=%v rcode=%d", err, resp.Rcode)
	}
}
