// Package allow validates host names and checks them against an allowlist.
package allow

import (
	"errors"
	"net/netip"
	"strings"
)

// Normalize lower-cases a host name and removes one trailing dot.
func Normalize(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// ValidHost reports whether host (already normalised) is a valid RFC 1123 host
// name of at most 253 characters. IP literals are rejected.
func ValidHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	labels := strings.Split(host, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	// The top-level label must not be all digits (RFC 3696 section 2).
	last := labels[len(labels)-1]
	allDigits := true
	for i := 0; i < len(last); i++ {
		if last[i] < '0' || last[i] > '9' {
			allDigits = false
			break
		}
	}
	return !allDigits
}

// List is a set of allowed name patterns.
type List struct {
	all    bool
	exact  map[string]struct{}
	suffix []string // ".example.com" for "*.example.com"
	domain map[string]struct{}
}

// New compiles patterns. "*" allows every name, "*.example.com" allows
// example.com and every subdomain, anything else must match exactly.
func New(patterns []string) (*List, error) {
	l := &List{exact: map[string]struct{}{}, domain: map[string]struct{}{}}
	if len(patterns) == 0 {
		return nil, errors.New("allowlist is empty: use \"*\" to allow every name")
	}
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		switch {
		case p == "*":
			l.all = true
		case strings.HasPrefix(p, "*."):
			base := Normalize(p[2:])
			if !ValidHost(base) {
				return nil, errors.New("invalid allowlist pattern: " + p)
			}
			l.domain[base] = struct{}{}
			l.suffix = append(l.suffix, "."+base)
		default:
			p = Normalize(p)
			if !ValidHost(p) {
				return nil, errors.New("invalid allowlist pattern: " + p)
			}
			l.exact[p] = struct{}{}
		}
	}
	return l, nil
}

// Allowed reports whether host (normalised) matches the list.
func (l *List) Allowed(host string) bool {
	if l.all {
		return true
	}
	if _, ok := l.exact[host]; ok {
		return true
	}
	if _, ok := l.domain[host]; ok {
		return true
	}
	for _, s := range l.suffix {
		if strings.HasSuffix(host, s) {
			return true
		}
	}
	return false
}
