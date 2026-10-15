package auth

import (
	"net/http"
	"strings"
	"testing"
)

func TestGenerateAndMatch(t *testing.T) {
	key, hash, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "drn_") || len(hash) != 64 || Hash(key) != hash {
		t.Fatalf("bad key/hash: %q %q", key, hash)
	}
	other, otherHash, _ := GenerateKey()
	if other == key {
		t.Fatal("keys must be random")
	}
	set, err := NewSet([]string{hash, strings.ToUpper(otherHash)})
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := set.Match(key); !ok || id != hash {
		t.Fatalf("expected match for first key, got %q %v", id, ok)
	}
	if _, ok := set.Match(other); !ok {
		t.Fatal("uppercase hash should be normalised")
	}
	if _, ok := set.Match("drn_wrong"); ok {
		t.Fatal("wrong key matched")
	}
	if _, ok := set.Match(""); ok {
		t.Fatal("empty key matched")
	}
	if _, ok := set.Match(hash); ok {
		t.Fatal("the hash itself must not work as a key")
	}
}

func TestNewSetRejectsBadHash(t *testing.T) {
	for _, bad := range []string{"", "abc", strings.Repeat("z", 64), "drn_plainkey"} {
		if _, err := NewSet([]string{bad}); err == nil {
			t.Errorf("hash %q should be rejected", bad)
		}
	}
}

func TestBearerToken(t *testing.T) {
	cases := map[string]string{
		"Bearer abc":  "abc",
		"bearer abc ": "abc",
		"Basic abc":   "",
		"Bearer":      "",
		"":            "",
	}
	for in, want := range cases {
		h := http.Header{}
		if in != "" {
			h.Set("Authorization", in)
		}
		if got := BearerToken(h); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
