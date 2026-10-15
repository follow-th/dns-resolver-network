package agent

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func serve(t *testing.T, network string, h dns.HandlerFunc) string {
	t.Helper()
	srv := &dns.Server{Handler: h}
	var addr string
	if network == "udp" {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv.PacketConn, addr = pc, pc.LocalAddr().String()
	} else {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv.Listener, addr = l, l.Addr().String()
	}
	go func() { _ = srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = srv.Shutdown() })
	return addr
}

func answerA(ip string) dns.HandlerFunc {
	return func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		if r.Question[0].Qtype == dns.TypeA {
			rr, _ := dns.NewRR(r.Question[0].Name + " 20 IN A " + ip)
			m.Answer = append(m.Answer, rr)
		}
		_ = w.WriteMsg(m)
	}
}

func TestFallsBackToNextServer(t *testing.T) {
	dead := serve(t, "udp", func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeServerFailure)
		_ = w.WriteMsg(m)
	})
	good := serve(t, "udp", answerA("192.0.2.7"))
	res, err := NewResolver([]string{dead, good}, false).Lookup(context.Background(), "x.example.com")
	if err != nil || len(res.IPs) != 1 || res.IPs[0] != "192.0.2.7" || res.DNS != good || res.TTL != 20 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestAllServersFail(t *testing.T) {
	addr := serve(t, "udp", func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeRefused)
		_ = w.WriteMsg(m)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := NewResolver([]string{addr}, false).Lookup(ctx, "x.example.com"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestTruncatedUDPRetriesOverTCP(t *testing.T) {
	// UDP and TCP must share a port: bind TCP first, then UDP on the same port.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Skip("cannot bind udp on the tcp port:", err)
	}
	tcp := &dns.Server{Listener: l, Handler: answerA("192.0.2.8")}
	udp := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		m.Truncated = true
		_ = w.WriteMsg(m)
	})}
	go func() { _ = tcp.ActivateAndServe() }()
	go func() { _ = udp.ActivateAndServe() }()
	t.Cleanup(func() { _ = tcp.Shutdown(); _ = udp.Shutdown() })

	res, err := NewResolver([]string{addr}, false).Lookup(context.Background(), "x.example.com")
	if err != nil || len(res.IPs) != 1 || res.IPs[0] != "192.0.2.8" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestIPv6OptionalAndNXDomain(t *testing.T) {
	addr := serve(t, "udp", func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		q := r.Question[0]
		if q.Name == "gone.example.com." {
			m.Rcode = dns.RcodeNameError
		} else if q.Qtype == dns.TypeAAAA {
			rr, _ := dns.NewRR(q.Name + " 10 IN AAAA 2001:db8::1")
			m.Answer = append(m.Answer, rr)
		} else {
			rr, _ := dns.NewRR(q.Name + " 30 IN A 192.0.2.1")
			m.Answer = append(m.Answer, rr)
		}
		_ = w.WriteMsg(m)
	})
	res, err := NewResolver([]string{addr}, true).Lookup(context.Background(), "x.example.com")
	if err != nil || len(res.IPs) != 2 || res.TTL != 10 {
		t.Fatalf("ipv6 on: %+v %v", res, err)
	}
	res, _ = NewResolver([]string{addr}, false).Lookup(context.Background(), "x.example.com")
	if len(res.IPs) != 1 {
		t.Fatalf("ipv6 off: %+v", res)
	}
	res, err = NewResolver([]string{addr}, false).Lookup(context.Background(), "gone.example.com")
	if err != nil || !res.NXDomain {
		t.Fatalf("nxdomain: %+v %v", res, err)
	}
}

func TestWithPort(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.1": "192.0.2.1:53", "192.0.2.1:5353": "192.0.2.1:5353",
		"2001:db8::1": "[2001:db8::1]:53", "[2001:db8::1]": "[2001:db8::1]:53",
	} {
		if got := withPort(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
