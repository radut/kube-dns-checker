package probe

import (
	"fmt"
	"net"
	"strings"

	"github.com/miekg/dns"

	"github.com/radut/kube-dns-checker/internal/config"
)

// ResolvConf is the subset of resolv.conf the checker cares about.
type ResolvConf struct {
	Nameservers []string // host:port
	Search      []string
	Ndots       int
}

// ReadResolvConf parses a resolv.conf file.
func ReadResolvConf(path string) (ResolvConf, error) {
	cfg, err := dns.ClientConfigFromFile(path)
	if err != nil {
		return ResolvConf{}, fmt.Errorf("read %s: %w", path, err)
	}
	if len(cfg.Servers) == 0 {
		return ResolvConf{}, fmt.Errorf("read %s: no nameserver entries", path)
	}
	servers := make([]string, 0, len(cfg.Servers))
	for _, s := range cfg.Servers {
		servers = append(servers, net.JoinHostPort(s, cfg.Port))
	}
	return ResolvConf{Nameservers: servers, Search: cfg.Search, Ndots: cfg.Ndots}, nil
}

// ExpandNameservers replaces the DEFAULT entry with the resolv.conf servers
// and removes duplicates. resolv.conf is only read when DEFAULT is present.
func ExpandNameservers(entries []string, resolvConfPath string) ([]string, *ResolvConf, error) {
	var rc *ResolvConf
	seen := map[string]bool{}
	var out []string
	add := func(ns string) {
		if !seen[ns] {
			seen[ns] = true
			out = append(out, ns)
		}
	}
	for _, e := range entries {
		if !strings.EqualFold(e, config.NameserverDefault) {
			add(e)
			continue
		}
		if rc == nil {
			parsed, err := ReadResolvConf(resolvConfPath)
			if err != nil {
				return nil, nil, err
			}
			rc = &parsed
		}
		for _, ns := range rc.Nameservers {
			add(ns)
		}
	}
	return out, rc, nil
}
