package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/miekg/dns"

	"github.com/radut/kube-dns-checker/internal/config"
)

// GoResolver uses Go's net.Resolver pointed at a specific nameserver. It
// behaves like an application does: search domains and ndots from
// resolv.conf apply, and A/AAAA may be looked up together. Failure reasons
// are approximate because net.Resolver hides the exact rcode.
type GoResolver struct{}

// NewGoResolver returns a resolver backed by net.Resolver.
func NewGoResolver() *GoResolver {
	return &GoResolver{}
}

// Query performs one lookup.
func (g *GoResolver) Query(ctx context.Context, target Target) Result {
	res := Result{Target: target}
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			if target.Protocol == config.ProtocolTCP {
				network = "tcp"
			}
			// For UDP targets we keep whatever network Go asks for, so its
			// own TCP fallback on truncated answers keeps working.
			return (&net.Dialer{}).DialContext(ctx, network, target.Nameserver)
		},
	}

	start := time.Now()
	answers, err := lookup(ctx, resolver, target)
	res.Duration = time.Since(start)

	if err != nil {
		res.Err = err
		res.Reason = classifyGoError(err)
		return res
	}
	res.Answers = answers
	if len(answers) == 0 {
		res.Reason = ReasonNoAnswer
		return res
	}
	res.Rcode = dns.RcodeToString[dns.RcodeSuccess]
	res.Success = true
	return res
}

func lookup(ctx context.Context, r *net.Resolver, target Target) ([]string, error) {
	switch target.QueryType {
	case dns.TypeA:
		return lookupIP(ctx, r, "ip4", target.Domain)
	case dns.TypeAAAA:
		return lookupIP(ctx, r, "ip6", target.Domain)
	case dns.TypeCNAME:
		cname, err := r.LookupCNAME(ctx, target.Domain)
		return nonEmpty(cname), err
	case dns.TypeMX:
		mxs, err := r.LookupMX(ctx, target.Domain)
		out := make([]string, 0, len(mxs))
		for _, mx := range mxs {
			out = append(out, fmt.Sprintf("%d %s", mx.Pref, mx.Host))
		}
		return out, err
	case dns.TypeNS:
		nss, err := r.LookupNS(ctx, target.Domain)
		out := make([]string, 0, len(nss))
		for _, ns := range nss {
			out = append(out, ns.Host)
		}
		return out, err
	case dns.TypeTXT:
		return r.LookupTXT(ctx, target.Domain)
	case dns.TypePTR:
		return r.LookupAddr(ctx, target.Domain)
	default:
		return nil, &unsupportedError{qtype: target.QueryTypeName()}
	}
}

func lookupIP(ctx context.Context, r *net.Resolver, network, host string) ([]string, error) {
	ips, err := r.LookupIP(ctx, network, host)
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return out, err
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

type unsupportedError struct{ qtype string }

func (e *unsupportedError) Error() string {
	return "query type " + e.qtype + " is not supported by the go resolver"
}

// classifyGoError maps net.Resolver errors onto failure reasons.
func classifyGoError(err error) string {
	var unsupported *unsupportedError
	if errors.As(err, &unsupported) {
		return ReasonUnsupported
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		switch {
		case dnsErr.IsNotFound:
			return dns.RcodeToString[dns.RcodeNameError]
		case dnsErr.IsTimeout:
			return ReasonTimeout
		case dnsErr.IsTemporary:
			return dns.RcodeToString[dns.RcodeServerFailure]
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ReasonTimeout
	}
	return ReasonNetworkError
}

// SupportsQueryType reports whether the Go resolver can look up a type.
func (g *GoResolver) SupportsQueryType(qtype string) bool {
	switch dns.StringToType[qtype] {
	case dns.TypeA, dns.TypeAAAA, dns.TypeCNAME, dns.TypeMX, dns.TypeNS, dns.TypeTXT, dns.TypePTR:
		return true
	}
	return false
}
