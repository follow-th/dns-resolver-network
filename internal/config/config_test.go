package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/follow-th/dns-resolver-network/internal/auth"
)

func env(m map[string]string) Getenv { return func(k string) string { return m[k] } }

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestServerFileAndDefaults(t *testing.T) {
	_, h1, _ := auth.GenerateKey()
	_, h2, _ := auth.GenerateKey()
	p := writeFile(t, "allow: ['*.example.com']\nclient_keys: ["+h1+"]\nagent_keys: ["+h2+"]\ncache_ttl: 30s\n")
	c, err := LoadServer(p, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" || c.CacheTTL != 30*time.Second || c.AskTimeout != 2*time.Second || c.MaxInflightPerAgent != 64 {
		t.Fatalf("unexpected config %+v", c)
	}
}

func TestServerEnvOverrides(t *testing.T) {
	_, h1, _ := auth.GenerateKey()
	_, h2, _ := auth.GenerateKey()
	_, h3, _ := auth.GenerateKey()
	p := writeFile(t, "allow: ['a.example.com']\nclient_keys: ["+h1+"]\nagent_keys: ["+h2+"]\n")
	c, err := LoadServer(p, env(map[string]string{
		"DRN_LISTEN":      "127.0.0.1:9000",
		"DRN_ALLOW":       "*, b.example.com",
		"DRN_CLIENT_KEYS": h3,
		"DRN_CACHE_TTL":   "5s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:9000" || len(c.Allow) != 2 || c.ClientKeys[0] != h3 || c.CacheTTL != 5*time.Second {
		t.Fatalf("overrides not applied: %+v", c)
	}
}

func TestServerEnvOnly(t *testing.T) {
	_, h, _ := auth.GenerateKey()
	_, err := LoadServer("", env(map[string]string{"DRN_ALLOW": "*", "DRN_CLIENT_KEYS": h, "DRN_AGENT_KEYS": h}))
	if err != nil {
		t.Fatal(err)
	}
}

func TestServerValidation(t *testing.T) {
	_, h, _ := auth.GenerateKey()
	good := map[string]string{"DRN_ALLOW": "*", "DRN_CLIENT_KEYS": h, "DRN_AGENT_KEYS": h}
	cases := map[string]map[string]string{
		"no allow":       {"DRN_CLIENT_KEYS": h, "DRN_AGENT_KEYS": h},
		"plain key":      {"DRN_ALLOW": "*", "DRN_CLIENT_KEYS": "drn_plain", "DRN_AGENT_KEYS": h},
		"no agent keys":  {"DRN_ALLOW": "*", "DRN_CLIENT_KEYS": h},
		"tls half":       {"DRN_ALLOW": "*", "DRN_CLIENT_KEYS": h, "DRN_AGENT_KEYS": h, "DRN_TLS_CERT": "x"},
		"bad duration":   {"DRN_ALLOW": "*", "DRN_CLIENT_KEYS": h, "DRN_AGENT_KEYS": h, "DRN_CACHE_TTL": "soon"},
		"bad allow name": {"DRN_ALLOW": "bad name", "DRN_CLIENT_KEYS": h, "DRN_AGENT_KEYS": h},
	}
	if _, err := LoadServer("", env(good)); err != nil {
		t.Fatal(err)
	}
	for name, e := range cases {
		if _, err := LoadServer("", env(e)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestServerUnknownField(t *testing.T) {
	p := writeFile(t, "listn: ':1'\n")
	if _, err := LoadServer(p, env(nil)); err == nil || !strings.Contains(err.Error(), "listn") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestAgent(t *testing.T) {
	p := writeFile(t, "server: https://drn.example.com\nkey: drn_secret\nname: home-a\ndns: [192.0.2.53]\nipv6: true\n")
	c, err := LoadAgent(p, env(map[string]string{"DRN_NAME": "home-b", "DRN_DNS": "198.51.100.1, 198.51.100.2"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "home-b" || len(c.DNS) != 2 || !c.IPv6 {
		t.Fatalf("%+v", c)
	}
	u, _ := c.ConnectURL()
	if u != "wss://drn.example.com/v1/agent/connect" {
		t.Fatalf("url %q", u)
	}
}

func TestAgentURLsAndValidation(t *testing.T) {
	cases := map[string]string{
		"drn.example.com":            "wss://drn.example.com/v1/agent/connect",
		"http://127.0.0.1:8080/":     "ws://127.0.0.1:8080/v1/agent/connect",
		"wss://drn.example.com/base": "wss://drn.example.com/base/v1/agent/connect",
	}
	for in, want := range cases {
		got, err := Agent{Server: in, Key: "k"}.ConnectURL()
		if err != nil || got != want {
			t.Errorf("%q: got %q %v want %q", in, got, err, want)
		}
	}
	if _, err := LoadAgent("", env(map[string]string{"DRN_SERVER": "ftp://x"})); err == nil {
		t.Error("bad scheme accepted")
	}
	if _, err := LoadAgent("", env(map[string]string{"DRN_SERVER": "drn.example.com"})); err == nil {
		t.Error("missing key accepted")
	}
	c, err := LoadAgent("", env(map[string]string{"DRN_SERVER": "drn.example.com", "DRN_KEY": "k"}))
	if err != nil || c.Name == "" {
		t.Errorf("default name: %v %+v", err, c)
	}
}
