package probe

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/radut/kube-dns-checker/internal/config"
)

// ednsBufferSize follows the DNS flag day 2020 recommendation.
const ednsBufferSize = 1232

// DNSClient sends raw DNS queries with miekg/dns. Names are queried exactly
// as given (made fully qualified), so no search domains are applied.
type DNSClient struct{}

// NewDNSClient returns a raw DNS resolver.
func NewDNSClient() *DNSClient {
	return &DNSClient{}
}

// Query performs one lookup. A truncated UDP answer is retried over TCP, as
// any real resolver would do, and reported with TCPFallback set.
func (c *DNSClient) Query(ctx context.Context, target Target) Result {
	res := Result{Target: target}

	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(target.Domain), target.QueryType)
	msg.RecursionDesired = true
	msg.SetEdns0(ednsBufferSize, false)

	start := time.Now()
	resp, err := exchange(ctx, msg, target.Nameserver, target.Protocol)
	if err == nil && resp.Truncated && target.Protocol == config.ProtocolUDP {
		res.TCPFallback = true
		resp, err = exchange(ctx, msg, target.Nameserver, config.ProtocolTCP)
	}
	res.Duration = time.Since(start)

	if err != nil {
		res.Err = err
		res.Reason = classifyNetError(ctx, err)
		return res
	}
	return classifyResponse(res, resp)
}

func exchange(ctx context.Context, msg *dns.Msg, server string, proto config.Protocol) (*dns.Msg, error) {
	client := &dns.Client{Net: string(proto), UDPSize: ednsBufferSize}
	if deadline, ok := ctx.Deadline(); ok {
		client.Timeout = time.Until(deadline)
	}
	resp, _, err := client.ExchangeContext(ctx, msg, server)
	return resp, err
}

// classifyNetError maps transport errors to a failure reason.
func classifyNetError(ctx context.Context, err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return ReasonTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ReasonTimeout
	}
	if ctx.Err() != nil {
		return ReasonTimeout
	}
	return ReasonNetworkError
}

// classifyResponse fills in rcode, answers and success for a received reply.
func classifyResponse(res Result, resp *dns.Msg) Result {
	if resp == nil {
		res.Reason = ReasonBadResponse
		return res
	}
	res.Rcode = dns.RcodeToString[resp.Rcode]
	if res.Rcode == "" {
		res.Rcode = ReasonBadResponse
	}
	res.Answers = answersOf(resp)

	switch {
	case resp.Rcode != dns.RcodeSuccess:
		res.Reason = res.Rcode
	case len(res.Answers) == 0:
		res.Reason = ReasonNoAnswer
	default:
		res.Success = true
	}
	return res
}

// answersOf returns the rdata of each answer record, e.g. "142.250.74.4"
// or "CNAME target.example." for compact logging.
func answersOf(resp *dns.Msg) []string {
	answers := make([]string, 0, len(resp.Answer))
	for _, rr := range resp.Answer {
		rdata := strings.TrimSpace(strings.TrimPrefix(rr.String(), rr.Header().String()))
		if rr.Header().Rrtype != dns.TypeA && rr.Header().Rrtype != dns.TypeAAAA {
			rdata = dns.TypeToString[rr.Header().Rrtype] + " " + rdata
		}
		answers = append(answers, rdata)
	}
	return answers
}
