package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/follow-th/dns-resolver-network/internal/agent"
	"github.com/follow-th/dns-resolver-network/internal/auth"
	"github.com/follow-th/dns-resolver-network/internal/config"
)

// startFakeDNS runs a UDP DNS server that answers every A query with ips,
// except "gone.example.com" which does not exist.
func startFakeDNS(t *testing.T, ips ...string) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		q := r.Question[0]
		if q.Name == "gone.example.com." {
			m.Rcode = dns.RcodeNameError
		} else if q.Qtype == dns.TypeA {
			for _, ip := range ips {
				rr, _ := dns.NewRR(q.Name + " 30 IN A " + ip)
				m.Answer = append(m.Answer, rr)
			}
		}
		_ = w.WriteMsg(m)
	})
	srv := &dns.Server{PacketConn: pc, Handler: mux}
	go func() { _ = srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = srv.Shutdown() })
	return pc.LocalAddr().String()
}

type testEnv struct {
	t         *testing.T
	srv       *Server
	ts        *httptest.Server
	clientKey string
	agentKeys map[string]string
}

func (e *testEnv) get(path, key string) (int, map[string]any) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.ts.URL+path, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		e.t.Fatalf("bad json %q: %v", b, err)
	}
	return resp.StatusCode, out
}

func (e *testEnv) resolve(host string) (int, map[string]any) {
	return e.get("/v1/resolve?host="+host, e.clientKey)
}

func (e *testEnv) waitAgents(n int) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e.srv.hub.Count() == n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for %d agents, have %d", n, e.srv.hub.Count())
}

func (e *testEnv) startAgent(name, dnsAddr string) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	a := agent.New(config.Agent{Server: e.ts.URL, Key: e.agentKeys[name], Name: name},
		agent.NewResolver([]string{dnsAddr}, false), slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.BackoffMin, a.BackoffMax = 20*time.Millisecond, 100*time.Millisecond
	done := make(chan struct{})
	go func() { defer close(done); _ = a.Run(ctx) }()
	e.t.Cleanup(func() { cancel(); <-done })
	return cancel
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	ck, ch, _ := auth.GenerateKey()
	keys := map[string]string{}
	var hashes []string
	for _, n := range []string{"a", "b", "c"} {
		k, h, _ := auth.GenerateKey()
		keys[n] = k
		hashes = append(hashes, h)
	}
	cfg := config.Server{
		Listen: "127.0.0.1:0", CacheTTL: time.Minute, AskTimeout: 2 * time.Second, MaxInflightPerAgent: 8,
		Allow: []string{"*.example.com"}, ClientKeys: []string{ch}, AgentKeys: hashes,
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv, err := New(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { cancel(); srv.hub.CloseAll(); ts.Close() })
	return &testEnv{t: t, srv: srv, ts: ts, clientKey: ck, agentKeys: keys}
}

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestEndToEnd(t *testing.T) {
	e := newEnv(t)

	// Health needs no auth; the API needs a key.
	if code, _ := e.get("/healthz", ""); code != 200 {
		t.Fatalf("healthz = %d", code)
	}
	if code, _ := e.get("/v1/resolve?host=x.example.com", ""); code != 401 {
		t.Fatalf("no key = %d", code)
	}
	if code, _ := e.get("/v1/resolve?host=x.example.com", "drn_wrong"); code != 401 {
		t.Fatalf("wrong key = %d", code)
	}
	if code, _ := e.get("/v1/agents", ""); code != 401 {
		t.Fatalf("agents without key = %d", code)
	}

	// No agent connected: 503.
	if code, body := e.resolve("x.example.com"); code != 503 || body["error"] != "no_agent" {
		t.Fatalf("no agents: %d %v", code, body)
	}

	// Validation and allowlist.
	if code, body := e.resolve("bad_name.example.com"); code != 400 || body["error"] != "invalid_host" {
		t.Fatalf("invalid host: %d %v", code, body)
	}
	if code, body := e.resolve("evil.net"); code != 403 || body["error"] != "host_not_allowed" {
		t.Fatalf("not allowed: %d %v", code, body)
	}

	// An agent key cannot be used as a client key and vice versa.
	if code, _ := e.get("/v1/resolve?host=x.example.com", e.agentKeys["a"]); code != 401 {
		t.Fatalf("agent key as client key = %d", code)
	}

	e.startAgent("a", startFakeDNS(t, "192.0.2.1", "192.0.2.2"))
	e.startAgent("b", startFakeDNS(t, "192.0.2.2", "192.0.2.3"))
	cancelC := e.startAgent("c", startFakeDNS(t, "192.0.2.2"))
	e.waitAgents(3)

	// Merge, order, sources.
	code, body := e.resolve("www.example.com")
	if code != 200 {
		t.Fatalf("resolve = %d %v", code, body)
	}
	ips := strs(body["ips"])
	if len(ips) != 3 || ips[0] != "192.0.2.2" {
		t.Fatalf("ips = %v", ips)
	}
	src := body["sources"].(map[string]any)
	if got := strs(src["192.0.2.2"]); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("sources = %v", got)
	}
	if got := strs(src["192.0.2.1"]); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("sources .1 = %v", got)
	}
	agents := body["agents"].(map[string]any)
	if agents["asked"] != 3.0 || agents["answered"] != 3.0 || body["cached"] != false || body["host"] != "www.example.com" {
		t.Fatalf("body = %v", body)
	}

	// Cache hit (also with different case and trailing dot).
	code, body = e.resolve("WWW.Example.com.")
	if code != 200 || body["cached"] != true || body["ttl"].(float64) > 60 {
		t.Fatalf("cache: %d %v", code, body)
	}

	// Nonexistent name.
	if code, body := e.resolve("gone.example.com"); code != 404 || body["error"] != "nxdomain" {
		t.Fatalf("nxdomain: %d %v", code, body)
	}

	// Agent listing.
	code, body = e.get("/v1/agents", e.clientKey)
	list := body["agents"].([]any)
	if code != 200 || len(list) != 3 || list[0].(map[string]any)["name"] != "a" {
		t.Fatalf("agents: %d %v", code, body)
	}
	if list[0].(map[string]any)["addresses"].(float64) != 2 {
		t.Fatalf("address count: %v", list[0])
	}

	// An agent disconnects: later lookups ask only the remaining two.
	cancelC()
	e.waitAgents(2)
	code, body = e.resolve("after-disconnect.example.com")
	agents = body["agents"].(map[string]any)
	if code != 200 || agents["asked"] != 2.0 || agents["answered"] != 2.0 {
		t.Fatalf("after disconnect: %d %v", code, body)
	}

	// The server drops every connection; the agents reconnect on their own.
	dropped := time.Now()
	e.srv.hub.CloseAll()
	deadline := time.Now().Add(5 * time.Second)
	for {
		list := e.srv.hub.List()
		fresh := len(list) == 2
		for _, a := range list {
			fresh = fresh && a.ConnectedSince.After(dropped)
		}
		if fresh {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agents did not reconnect: %+v", list)
		}
		time.Sleep(10 * time.Millisecond)
	}
	code, body = e.resolve("after-reconnect.example.com")
	agents = body["agents"].(map[string]any)
	if code != 200 || agents["answered"] != 2.0 {
		t.Fatalf("after reconnect: %d %v", code, body)
	}
}

func TestSameKeyReplacesConnection(t *testing.T) {
	e := newEnv(t)
	dnsAddr := startFakeDNS(t, "192.0.2.5")
	e.startAgent("a", dnsAddr)
	e.waitAgents(1)
	// A second agent using the same key replaces the first; the first then
	// keeps retrying and replaces the second, so only one is ever connected.
	e.startAgent("a", dnsAddr)
	time.Sleep(300 * time.Millisecond)
	if n := e.srv.hub.Count(); n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
}
