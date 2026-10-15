// Package merge combines the addresses reported by several agents.
package merge

import (
	"net/netip"
	"sort"
)

// Result is one agent's list of addresses.
type Result struct {
	Agent string
	IPs   []string
}

// Merge removes duplicates and orders addresses by how many agents returned
// them (most first), then by first appearance. It also returns, for each
// address, the sorted names of the agents that returned it. Invalid addresses
// are dropped; IPv4-mapped IPv6 addresses are reported as IPv4.
func Merge(results []Result) (ips []string, sources map[string][]string) {
	type entry struct {
		ip     string
		first  int
		agents map[string]struct{}
	}
	byIP := map[string]*entry{}
	var order []*entry
	for _, r := range results {
		for _, raw := range r.IPs {
			addr, err := netip.ParseAddr(raw)
			if err != nil {
				continue
			}
			ip := addr.Unmap().String()
			e := byIP[ip]
			if e == nil {
				e = &entry{ip: ip, first: len(order), agents: map[string]struct{}{}}
				byIP[ip] = e
				order = append(order, e)
			}
			e.agents[r.Agent] = struct{}{}
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		return len(order[i].agents) > len(order[j].agents)
	})
	ips = make([]string, 0, len(order))
	sources = make(map[string][]string, len(order))
	for _, e := range order {
		names := make([]string, 0, len(e.agents))
		for n := range e.agents {
			names = append(names, n)
		}
		sort.Strings(names)
		ips = append(ips, e.ip)
		sources[e.ip] = names
	}
	return ips, sources
}
