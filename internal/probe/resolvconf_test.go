package probe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/radut/kube-dns-checker/internal/config"
)

func writeResolvConf(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "resolv.conf")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadResolvConf(t *testing.T) {
	path := writeResolvConf(t, "nameserver 10.96.0.10\nnameserver fd00::1\nsearch default.svc.cluster.local svc.cluster.local\noptions ndots:5\n")
	rc, err := ReadResolvConf(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rc.Nameservers, ","); got != "10.96.0.10:53,[fd00::1]:53" {
		t.Errorf("nameservers = %q", got)
	}
	if rc.Ndots != 5 || len(rc.Search) != 2 {
		t.Errorf("ndots/search = %d/%v", rc.Ndots, rc.Search)
	}
}

func TestReadResolvConfErrors(t *testing.T) {
	if _, err := ReadResolvConf(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("expected error for missing file")
	}
	if _, err := ReadResolvConf(writeResolvConf(t, "search example\n")); err == nil {
		t.Error("expected error for file without nameservers")
	}
}

func TestExpandNameservers(t *testing.T) {
	path := writeResolvConf(t, "nameserver 10.96.0.10\n")
	out, rc, err := ExpandNameservers([]string{"8.8.8.8:53", config.NameserverDefault, "10.96.0.10:53", "default"}, path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(out, ","); got != "8.8.8.8:53,10.96.0.10:53" {
		t.Errorf("expanded = %q", got)
	}
	if rc == nil {
		t.Error("expected resolv.conf to be returned")
	}

	out, rc, err = ExpandNameservers([]string{"1.1.1.1:53"}, filepath.Join(t.TempDir(), "missing"))
	if err != nil || rc != nil || len(out) != 1 {
		t.Errorf("resolv.conf should not be read without DEFAULT: %v %v %v", out, rc, err)
	}

	if _, _, err := ExpandNameservers([]string{config.NameserverDefault}, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("expected error when DEFAULT is used and resolv.conf is missing")
	}
}
