package allow

import (
	"strings"
	"testing"
)

func TestValidHost(t *testing.T) {
	long := strings.Repeat("a", 63)
	valid := []string{"example.com", "a.b.c.example.com", "xn--bcher-kva.example", "a-b.example.com", "localhost", "1abc.example.com", long + ".com"}
	invalid := []string{"", "-a.com", "a-.com", "a..com", ".com", "a_b.com", "a b.com", "192.0.2.1", "2001:db8::1", "example.123",
		strings.Repeat("a", 64) + ".com", strings.Repeat("a.", 127) + "com", "exa$mple.com"}
	for _, h := range valid {
		if !ValidHost(h) {
			t.Errorf("%q should be valid", h)
		}
	}
	for _, h := range invalid {
		if ValidHost(h) {
			t.Errorf("%q should be invalid", h)
		}
	}
}

func TestNormalize(t *testing.T) {
	if got := Normalize(" Example.COM. "); got != "example.com" {
		t.Fatalf("got %q", got)
	}
}

func TestAllowList(t *testing.T) {
	l, err := New([]string{"exact.example.org", "*.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	yes := []string{"exact.example.org", "example.com", "a.example.com", "a.b.example.com"}
	no := []string{"other.example.org", "sub.exact.example.org", "badexample.com", "example.com.evil.net", "com"}
	for _, h := range yes {
		if !l.Allowed(h) {
			t.Errorf("%q should be allowed", h)
		}
	}
	for _, h := range no {
		if l.Allowed(h) {
			t.Errorf("%q should not be allowed", h)
		}
	}
}

func TestAllowAll(t *testing.T) {
	l, err := New([]string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	if !l.Allowed("anything.example") {
		t.Fatal("* must allow everything")
	}
}

func TestNewErrors(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Error("empty list must be rejected")
	}
	for _, p := range []string{"*.", "*.bad_name", "bad name", "192.0.2.1"} {
		if _, err := New([]string{p}); err == nil {
			t.Errorf("pattern %q should be rejected", p)
		}
	}
}
