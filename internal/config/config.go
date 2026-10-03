// Package config loads and validates the checker configuration from
// environment variables. All validation happens here so the rest of the
// program can trust the values it receives.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Resolver selects which DNS client implementation runs the probes.
type Resolver string

const (
	// ResolverDNS sends raw DNS packets with the miekg/dns library. It reports
	// the exact response code and round-trip time and supports sub-second
	// timeouts. Names are queried exactly as given (no search domains).
	ResolverDNS Resolver = "dns"
	// ResolverGo uses Go's net.Resolver, which behaves like an application
	// would: it honours search domains and ndots from resolv.conf.
	ResolverGo Resolver = "go"
)

// Protocol is the transport used for a probe.
type Protocol string

const (
	ProtocolUDP Protocol = "udp"
	ProtocolTCP Protocol = "tcp"
)

// NameserverDefault is the NAMESERVERS entry that expands to the servers
// listed in resolv.conf.
const NameserverDefault = "DEFAULT"

// Defaults for every setting. Exported so tests and docs can reference them.
const (
	DefaultDomains     = "www.google.com."
	DefaultNameservers = NameserverDefault
	DefaultProtocols   = "udp"
	DefaultQueryType   = "A"
	DefaultTimeout     = 2 * time.Second
	DefaultInterval    = 5 * time.Second
	DefaultAttempts    = 1
	DefaultConcurrency = 8
	DefaultListenAddr  = ":8080"
	DefaultResolvConf  = "/etc/resolv.conf"
	DefaultLogLevel    = "info"
	DefaultLogFormat   = "text"
	DefaultDNSPort     = "53"

	minTimeout  = 10 * time.Millisecond
	minInterval = 100 * time.Millisecond
	maxAttempts = 10
)

// Config is the fully validated configuration.
type Config struct {
	Resolver    Resolver
	Domains     []string
	Nameservers []string // "DEFAULT" or host:port
	Protocols   []Protocol
	QueryType   string
	Timeout     time.Duration // per attempt
	Interval    time.Duration // per probe
	Attempts    int           // attempts per probe run before it counts as failed
	Concurrency int           // max probes in flight at once
	ListenAddr  string
	ResolvConf  string
	LogLevel    string
	LogFormat   string
}

// Getenv abstracts os.LookupEnv so tests can inject values.
type Getenv func(key string) (string, bool)

// FromEnv builds a Config from the process environment.
func FromEnv() (Config, error) {
	return Load(os.LookupEnv)
}

// Load builds a Config from the given environment lookup function.
func Load(getenv Getenv) (Config, error) {
	var errs []error
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	resolver, err := parseResolver(getenv)
	collect(err)
	domains, err := parseDomains(lookup(getenv, "DOMAINS", DefaultDomains))
	collect(err)
	nameservers, err := parseNameservers(lookup(getenv, "NAMESERVERS", DefaultNameservers))
	collect(err)
	protocols, err := parseProtocols(lookup(getenv, "PROTOCOLS", DefaultProtocols))
	collect(err)
	queryType, err := parseQueryType(lookup(getenv, "QUERY_TYPE", DefaultQueryType))
	collect(err)
	timeout, err := parseDuration("TIMEOUT", lookup(getenv, "TIMEOUT", DefaultTimeout.String()), minTimeout)
	collect(err)
	interval, err := parseDuration("INTERVAL", lookup(getenv, "INTERVAL", DefaultInterval.String()), minInterval)
	collect(err)
	attempts, err := parseInt("ATTEMPTS", lookup(getenv, "ATTEMPTS", strconv.Itoa(DefaultAttempts)), 1, maxAttempts)
	collect(err)
	concurrency, err := parseInt("CONCURRENCY", lookup(getenv, "CONCURRENCY", strconv.Itoa(DefaultConcurrency)), 1, 1024)
	collect(err)
	logLevel, err := parseLogLevel(getenv)
	collect(err)
	logFormat, err := parseOneOf("LOG_FORMAT", lookup(getenv, "LOG_FORMAT", DefaultLogFormat), "text", "json")
	collect(err)

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}

	return Config{
		Resolver:    resolver,
		Domains:     domains,
		Nameservers: nameservers,
		Protocols:   protocols,
		QueryType:   queryType,
		Timeout:     timeout,
		Interval:    interval,
		Attempts:    attempts,
		Concurrency: concurrency,
		ListenAddr:  lookup(getenv, "LISTEN_ADDR", DefaultListenAddr),
		ResolvConf:  lookup(getenv, "RESOLV_CONF", DefaultResolvConf),
		LogLevel:    logLevel,
		LogFormat:   logFormat,
	}, nil
}

func lookup(getenv Getenv, key, def string) string {
	if v, ok := getenv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

// parseResolver reads RESOLVER, honouring the legacy GO_RESOLVER=true flag.
func parseResolver(getenv Getenv) (Resolver, error) {
	if legacy, ok := getenv("GO_RESOLVER"); ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(legacy)); err == nil && b {
			return ResolverGo, nil
		}
	}
	v, err := parseOneOf("RESOLVER", lookup(getenv, "RESOLVER", string(ResolverDNS)), string(ResolverDNS), string(ResolverGo))
	return Resolver(v), err
}

// parseLogLevel reads LOG_LEVEL, honouring the legacy DEBUG=true flag.
func parseLogLevel(getenv Getenv) (string, error) {
	if legacy, ok := getenv("DEBUG"); ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(legacy)); err == nil && b {
			return "debug", nil
		}
	}
	return parseOneOf("LOG_LEVEL", lookup(getenv, "LOG_LEVEL", DefaultLogLevel), "debug", "info", "warn", "error")
}

func parseOneOf(key, value string, allowed ...string) (string, error) {
	v := strings.ToLower(value)
	for _, a := range allowed {
		if v == a {
			return v, nil
		}
	}
	return "", fmt.Errorf("%s: %q is not one of %s", key, value, strings.Join(allowed, ", "))
}

func parseDuration(key, value string, min time.Duration) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a duration (e.g. 500ms, 2s): %w", key, value, err)
	}
	if d < min {
		return 0, fmt.Errorf("%s: %s is below the minimum of %s", key, d, min)
	}
	return d, nil
}

func parseInt(key, value string, min, max int) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not an integer: %w", key, value, err)
	}
	if n < min || n > max {
		return 0, fmt.Errorf("%s: %d is outside the range %d..%d", key, n, min, max)
	}
	return n, nil
}

// splitList splits a comma separated list, trimming blanks and dropping
// empties and duplicates while keeping order.
func splitList(value string) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func parseDomains(value string) ([]string, error) {
	domains := splitList(value)
	if len(domains) == 0 {
		return nil, errors.New("DOMAINS: at least one domain is required")
	}
	for _, d := range domains {
		if strings.ContainsAny(d, " \t/@") {
			return nil, fmt.Errorf("DOMAINS: %q is not a valid domain name", d)
		}
	}
	return domains, nil
}

// parseNameservers accepts DEFAULT, IPs, [IPv6], and host:port forms.
// Entries without a port get port 53.
func parseNameservers(value string) ([]string, error) {
	entries := splitList(value)
	if len(entries) == 0 {
		return nil, errors.New("NAMESERVERS: at least one nameserver is required")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		addr := NameserverDefault
		if !strings.EqualFold(e, NameserverDefault) {
			var err error
			if addr, err = NormalizeNameserver(e); err != nil {
				return nil, fmt.Errorf("NAMESERVERS: %w", err)
			}
		}
		if !seen[addr] {
			seen[addr] = true
			out = append(out, addr)
		}
	}
	return out, nil
}

// NormalizeNameserver turns "1.2.3.4", "1.2.3.4:5353", "::1" or "[::1]:53"
// into a host:port string, adding port 53 when missing.
func NormalizeNameserver(entry string) (string, error) {
	host, port, err := net.SplitHostPort(entry)
	if err != nil {
		// No port present (or bare IPv6 literal).
		host, port = strings.Trim(entry, "[]"), DefaultDNSPort
	}
	if host == "" {
		return "", fmt.Errorf("%q has an empty host", entry)
	}
	if _, err := strconv.Atoi(port); err != nil {
		return "", fmt.Errorf("%q has an invalid port %q", entry, port)
	}
	if net.ParseIP(host) == nil && strings.ContainsAny(host, " \t/@") {
		return "", fmt.Errorf("%q is not a valid nameserver", entry)
	}
	return net.JoinHostPort(host, port), nil
}

func parseProtocols(value string) ([]Protocol, error) {
	entries := splitList(strings.ToLower(value))
	if len(entries) == 0 {
		return nil, errors.New("PROTOCOLS: at least one of udp, tcp is required")
	}
	out := make([]Protocol, 0, len(entries))
	for _, e := range entries {
		switch Protocol(e) {
		case ProtocolUDP, ProtocolTCP:
			out = append(out, Protocol(e))
		default:
			return nil, fmt.Errorf("PROTOCOLS: %q is not one of udp, tcp", e)
		}
	}
	return out, nil
}

var allowedQueryTypes = []string{"A", "AAAA", "CNAME", "MX", "NS", "TXT", "SRV", "PTR", "SOA"}

func parseQueryType(value string) (string, error) {
	return parseOneOfUpper("QUERY_TYPE", value, allowedQueryTypes...)
}

func parseOneOfUpper(key, value string, allowed ...string) (string, error) {
	v := strings.ToUpper(value)
	for _, a := range allowed {
		if v == a {
			return v, nil
		}
	}
	return "", fmt.Errorf("%s: %q is not one of %s", key, value, strings.Join(allowed, ", "))
}
