package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/miekg/dns"

	"github.com/follow-th/dns-resolver-network/internal/proto"
)

// Result is what an agent reports for one name.
type Result struct {
	IPs      []string
	TTL      uint32
	DNS      string // the DNS server that answered, or "system"
	NXDomain bool
}

// Resolver resolves a host name to addresses.
type Resolver interface {
	Lookup(ctx context.Context, host string) (Result, error)
}

// NewResolver returns a resolver using the given DNS servers in order, or the
// system resolver when servers is empty.
func NewResolver(servers []string, ipv6 bool) Resolver {
	if len(servers) == 0 {
		return &systemResolver{ipv6: ipv6, r: net.DefaultResolver}
	}
	addrs := make([]string, len(servers))
	for i, s := range servers {
		addrs[i] = withPort(s)
	}
	return &dnsResolver{servers: addrs, ipv6: ipv6, timeout: 2 * time.Second}
}

func withPort(s string) string {
	if _, _, err := net.SplitHostPort(s); err == nil {
		return s
	}
	return net.JoinHostPort(trimBrackets(s), "53")
}

func trimBrackets(s string) string {
	if len(s) > 2 && s[0] == '[' && s[len(s)-1] == ']' {
		return s[1 : len(s)-1]
	}
	return s
}

type dnsResolver struct {
	servers []string
	ipv6    bool
	timeout time.Duration
}

func (r *dnsResolver) Lookup(ctx context.Context, host string) (Result, error) {
	fqdn := dns.Fqdn(host)
	var lastErr error
	for _, srv := range r.servers {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		res, err := r.queryServer(ctx, srv, fqdn)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", srv, err)
			continue
		}
		res.DNS = srv
		return res, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no dns servers")
	}
	return Result{}, lastErr
}

type answer struct {
	msg *dns.Msg
	err error
}

func (r *dnsResolver) queryServer(ctx context.Context, srv, fqdn string) (Result, error) {
	var v6 chan answer
	if r.ipv6 {
		v6 = make(chan answer, 1)
		go func() {
			m, err := r.exchange(ctx, srv, fqdn, dns.TypeAAAA)
			v6 <- answer{m, err}
		}()
	}
	m4, err := r.exchange(ctx, srv, fqdn, dns.TypeA)
	if err != nil {
		return Result{}, err
	}
	switch m4.Rcode {
	case dns.RcodeSuccess:
	case dns.RcodeNameError:
		return Result{NXDomain: true}, nil
	default:
		return Result{}, fmt.Errorf("rcode %s", dns.RcodeToString[m4.Rcode])
	}
	res := Result{}
	collect(&res, m4)
	if v6 != nil {
		if a := <-v6; a.err == nil && a.msg.Rcode == dns.RcodeSuccess {
			collect(&res, a.msg)
		}
	}
	return res, nil
}

// collect appends the A/AAAA records of m to res and keeps the minimum TTL.
func collect(res *Result, m *dns.Msg) {
	for _, rr := range m.Answer {
		var ip net.IP
		switch v := rr.(type) {
		case *dns.A:
			ip = v.A
		case *dns.AAAA:
			ip = v.AAAA
		default:
			continue
		}
		addr, ok := netip.AddrFromSlice(ip)
		if !ok || len(res.IPs) >= proto.MaxIPs {
			continue
		}
		res.IPs = append(res.IPs, addr.Unmap().String())
		if ttl := rr.Header().Ttl; len(res.IPs) == 1 || ttl < res.TTL {
			res.TTL = ttl
		}
	}
}

// exchange queries over UDP and retries over TCP when the answer is truncated.
func (r *dnsResolver) exchange(ctx context.Context, srv, fqdn string, qtype uint16) (*dns.Msg, error) {
	q := new(dns.Msg)
	q.SetQuestion(fqdn, qtype)
	q.RecursionDesired = true
	q.SetEdns0(1232, false)
	for _, network := range []string{"udp", "tcp"} {
		c := &dns.Client{Net: network, Timeout: r.timeout}
		in, _, err := c.ExchangeContext(ctx, q, srv)
		if err != nil {
			return nil, err
		}
		if in.Truncated && network == "udp" {
			continue
		}
		return in, nil
	}
	return nil, errors.New("truncated answer over tcp")
}

type systemResolver struct {
	ipv6 bool
	r    *net.Resolver
}

func (r *systemResolver) Lookup(ctx context.Context, host string) (Result, error) {
	networks := []string{"ip4"}
	if r.ipv6 {
		networks = append(networks, "ip6")
	}
	res := Result{DNS: "system", TTL: 60}
	var firstErr error
	for _, n := range networks {
		addrs, err := r.r.LookupNetIP(ctx, n, host)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, a := range addrs {
			if len(res.IPs) < proto.MaxIPs {
				res.IPs = append(res.IPs, a.Unmap().String())
			}
		}
	}
	if len(res.IPs) == 0 && firstErr != nil {
		var de *net.DNSError
		if errors.As(firstErr, &de) && de.IsNotFound {
			return Result{DNS: "system", NXDomain: true}, nil
		}
		return Result{}, firstErr
	}
	return res, nil
}
