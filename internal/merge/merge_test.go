package merge

import (
	"reflect"
	"testing"
)

func TestMergeOrderAndSources(t *testing.T) {
	ips, src := Merge([]Result{
		{Agent: "b", IPs: []string{"192.0.2.1", "192.0.2.2"}},
		{Agent: "a", IPs: []string{"192.0.2.2", "192.0.2.3", "192.0.2.2"}},
		{Agent: "c", IPs: []string{"192.0.2.3", "192.0.2.2", "not-an-ip"}},
	})
	// .2 from 3 agents, .3 from 2 agents, .1 from 1 agent.
	want := []string{"192.0.2.2", "192.0.2.3", "192.0.2.1"}
	if !reflect.DeepEqual(ips, want) {
		t.Fatalf("ips = %v, want %v", ips, want)
	}
	if got := src["192.0.2.2"]; !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("sources .2 = %v", got)
	}
	if got := src["192.0.2.1"]; !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("sources .1 = %v", got)
	}
}

func TestMergeTieKeepsFirstSeen(t *testing.T) {
	ips, _ := Merge([]Result{
		{Agent: "a", IPs: []string{"198.51.100.9", "198.51.100.1"}},
		{Agent: "b", IPs: []string{"198.51.100.5"}},
	})
	want := []string{"198.51.100.9", "198.51.100.1", "198.51.100.5"}
	if !reflect.DeepEqual(ips, want) {
		t.Fatalf("ips = %v, want %v", ips, want)
	}
}

func TestMergeNormalisesAndHandlesEmpty(t *testing.T) {
	ips, src := Merge([]Result{
		{Agent: "a", IPs: []string{"::ffff:203.0.113.7", "2001:db8::1"}},
		{Agent: "b", IPs: []string{"203.0.113.7"}},
	})
	if !reflect.DeepEqual(ips, []string{"203.0.113.7", "2001:db8::1"}) {
		t.Fatalf("ips = %v", ips)
	}
	if len(src["203.0.113.7"]) != 2 {
		t.Fatalf("mapped address should merge with plain IPv4: %v", src)
	}
	ips, src = Merge(nil)
	if len(ips) != 0 || len(src) != 0 {
		t.Fatal("empty input must give empty output")
	}
}
