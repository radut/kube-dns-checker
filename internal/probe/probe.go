// Package probe performs a single DNS lookup against one nameserver and
// classifies the outcome. Two resolver implementations exist: a raw DNS
// client (exact rcode and RTT) and Go's net.Resolver (application-like).
package probe

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/radut/kube-dns-checker/internal/config"
)

// Target is one nameserver/domain/protocol combination that gets probed on
// its own schedule.
type Target struct {
	Nameserver string // host:port
	Domain     string
	Protocol   config.Protocol
	QueryType  uint16
}

// Key uniquely identifies the target; used for scheduling and liveness.
func (t Target) Key() string {
	return t.Nameserver + "|" + t.Domain + "|" + string(t.Protocol)
}

// QueryTypeName returns the textual record type (A, AAAA, ...).
func (t Target) QueryTypeName() string {
	return dns.TypeToString[t.QueryType]
}

// Failure reasons that are not DNS response codes. Response codes
// (NXDOMAIN, SERVFAIL, REFUSED, ...) are used verbatim.
const (
	ReasonTimeout      = "timeout"
	ReasonNetworkError = "network_error"
	ReasonNoAnswer     = "no_answer"
	ReasonBadResponse  = "bad_response"
	ReasonUnsupported  = "unsupported"
)

// Result is the outcome of probing one target once (including retries).
type Result struct {
	Target      Target
	Success     bool
	Reason      string // empty on success; otherwise an rcode or Reason* constant
	Rcode       string // rcode name when a response was received, else ""
	Duration    time.Duration
	Answers     []string
	Attempts    int  // attempts made, 1..n
	TCPFallback bool // UDP answer was truncated and the query was retried over TCP
	Err         error
}

// Resolver performs one attempt against a target. The context carries the
// per-attempt deadline.
type Resolver interface {
	Query(ctx context.Context, target Target) Result
}

// Run probes a target with up to attempts tries, each with its own timeout.
// It returns the first successful result, or the last failed one with the
// total number of attempts made.
func Run(ctx context.Context, r Resolver, target Target, attempts int, timeout time.Duration) Result {
	if attempts < 1 {
		attempts = 1
	}
	var last Result
	for i := 1; i <= attempts; i++ {
		attemptCtx, cancel := context.WithTimeout(ctx, timeout)
		res := r.Query(attemptCtx, target)
		cancel()
		res.Attempts = i
		if res.Success || ctx.Err() != nil {
			return res
		}
		last = res
	}
	return last
}

// BuildTargets expands the configuration into the cross product of
// nameservers, domains and protocols.
func BuildTargets(nameservers, domains []string, protocols []config.Protocol, queryType string) ([]Target, error) {
	qtype, ok := dns.StringToType[strings.ToUpper(queryType)]
	if !ok {
		return nil, fmt.Errorf("unknown query type %q", queryType)
	}
	targets := make([]Target, 0, len(nameservers)*len(domains)*len(protocols))
	for _, ns := range nameservers {
		for _, d := range domains {
			for _, p := range protocols {
				targets = append(targets, Target{Nameserver: ns, Domain: d, Protocol: p, QueryType: qtype})
			}
		}
	}
	return targets, nil
}

// String renders a short description for logs.
func (t Target) String() string {
	return fmt.Sprintf("%s %s %s/%s", t.Nameserver, t.Domain, t.QueryTypeName(), t.Protocol)
}
