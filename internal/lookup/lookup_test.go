package lookup

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/follow-th/dns-resolver-network/internal/hub"
	"github.com/follow-th/dns-resolver-network/internal/proto"
)

type fakeAsker struct {
	calls   atomic.Int32
	delay   time.Duration
	asked   int
	replies []hub.Reply
}

func (f *fakeAsker) AskAll(ctx context.Context, host string) ([]hub.Reply, int) {
	f.calls.Add(1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return f.replies, f.asked
}

func ok(agent string, ips ...string) hub.Reply {
	return hub.Reply{Agent: agent, Msg: proto.Message{Type: proto.TypeResult, IPs: ips}}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestCacheHitAndExpiry(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	f := &fakeAsker{asked: 2, replies: []hub.Reply{ok("a", "192.0.2.1"), ok("b", "192.0.2.1", "192.0.2.2")}}
	s := New(f, 60*time.Second, time.Second, clk.now)

	a, err := s.Resolve(context.Background(), "x.example.com")
	if err != nil || a.Cached || a.TTL != 60 || a.IPs[0] != "192.0.2.1" || a.Agents.Asked != 2 || a.Agents.Answered != 2 {
		t.Fatalf("first: %+v %v", a, err)
	}
	clk.add(10 * time.Second)
	a, _ = s.Resolve(context.Background(), "x.example.com")
	if !a.Cached || a.TTL != 50 || f.calls.Load() != 1 {
		t.Fatalf("second: %+v calls=%d", a, f.calls.Load())
	}
	clk.add(51 * time.Second)
	a, _ = s.Resolve(context.Background(), "x.example.com")
	if a.Cached || f.calls.Load() != 2 {
		t.Fatalf("after expiry: %+v calls=%d", a, f.calls.Load())
	}
}

func TestSingleFlightLookups(t *testing.T) {
	f := &fakeAsker{asked: 1, delay: 100 * time.Millisecond, replies: []hub.Reply{ok("a", "192.0.2.1")}}
	s := New(f, time.Minute, time.Second, nil)
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Resolve(context.Background(), "x.example.com"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("asked agents %d times, want 1", n)
	}
}

func TestNoAgent(t *testing.T) {
	f := &fakeAsker{}
	s := New(f, time.Minute, time.Second, nil)
	for range 2 {
		if _, err := s.Resolve(context.Background(), "x.example.com"); !errors.Is(err, ErrNoAgent) {
			t.Fatalf("err = %v", err)
		}
	}
	if f.calls.Load() != 2 {
		t.Fatal("with no agent connected nothing should be cached")
	}
}

func TestNegativeCache(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	failed := hub.Reply{Agent: "a", Msg: proto.Message{Type: proto.TypeResult, Error: "timeout"}}
	f := &fakeAsker{asked: 1, replies: []hub.Reply{failed}}
	s := New(f, time.Minute, time.Second, clk.now)
	for range 2 {
		if _, err := s.Resolve(context.Background(), "x.example.com"); !errors.Is(err, ErrNoAgent) {
			t.Fatalf("err = %v", err)
		}
	}
	if f.calls.Load() != 1 {
		t.Fatal("failure should be cached")
	}
	clk.add(6 * time.Second)
	_, _ = s.Resolve(context.Background(), "x.example.com")
	if f.calls.Load() != 2 {
		t.Fatal("negative entry should expire after 5 s")
	}
}

func TestNXDomainAndMixed(t *testing.T) {
	nx := func(n string) hub.Reply {
		return hub.Reply{Agent: n, Msg: proto.Message{Type: proto.TypeResult, NXDomain: true}}
	}
	s := New(&fakeAsker{asked: 2, replies: []hub.Reply{nx("a"), nx("b")}}, time.Minute, time.Second, nil)
	if _, err := s.Resolve(context.Background(), "gone.example.com"); !errors.Is(err, ErrNXDomain) {
		t.Fatalf("err = %v", err)
	}
	s = New(&fakeAsker{asked: 2, replies: []hub.Reply{nx("a"), ok("b", "192.0.2.9")}}, time.Minute, time.Second, nil)
	a, err := s.Resolve(context.Background(), "mixed.example.com")
	if err != nil || len(a.IPs) != 1 || a.Agents.Answered != 2 {
		t.Fatalf("mixed: %+v %v", a, err)
	}
}

func TestPartialAnswers(t *testing.T) {
	s := New(&fakeAsker{asked: 3, replies: []hub.Reply{ok("a", "192.0.2.1")}}, time.Minute, time.Second, nil)
	a, err := s.Resolve(context.Background(), "x.example.com")
	if err != nil || a.Agents.Asked != 3 || a.Agents.Answered != 1 {
		t.Fatalf("%+v %v", a, err)
	}
}
