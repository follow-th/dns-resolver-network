package cache

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	c := New[string](10, func() time.Time { return now })
	c.Set("a", "x", 60*time.Second)
	if v, exp, ok := c.Get("a"); !ok || v != "x" || !exp.Equal(now.Add(60*time.Second)) {
		t.Fatalf("get = %v %v %v", v, exp, ok)
	}
	now = now.Add(59 * time.Second)
	if _, _, ok := c.Get("a"); !ok {
		t.Fatal("entry should still be valid")
	}
	now = now.Add(time.Second)
	if _, _, ok := c.Get("a"); ok {
		t.Fatal("entry should have expired")
	}
	if c.Len() != 0 {
		t.Fatal("expired entry should be removed on read")
	}
}

func TestCacheBounded(t *testing.T) {
	now := time.Unix(0, 0)
	c := New[int](3, func() time.Time { return now })
	for i, k := range []string{"a", "b", "c", "d", "e"} {
		c.Set(k, i, time.Minute)
		if c.Len() > 3 {
			t.Fatalf("cache grew to %d", c.Len())
		}
	}
	if _, _, ok := c.Get("e"); !ok {
		t.Fatal("newest entry must be present")
	}
}

func TestSingleFlight(t *testing.T) {
	var g Group[int]
	var calls atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]int, 8)
	sharedCount := atomic.Int32{}
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, shared, err := g.Do(context.Background(), "k", func() int {
				calls.Add(1)
				<-release
				return 42
			})
			if err != nil {
				t.Error(err)
			}
			if shared {
				sharedCount.Add(1)
			}
			results[i] = v
		}()
	}
	// Wait until all goroutines are inside Do (one leader plus waiters).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		g.mu.Lock()
		_, running := g.m["k"]
		g.mu.Unlock()
		if running {
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("fn ran %d times, want 1", n)
	}
	for _, v := range results {
		if v != 42 {
			t.Fatalf("result %d", v)
		}
	}
	if sharedCount.Load() != int32(len(results)-1) {
		t.Fatalf("shared = %d", sharedCount.Load())
	}
	// A later call runs again.
	v, shared, _ := g.Do(context.Background(), "k", func() int { calls.Add(1); return 7 })
	if v != 7 || shared || calls.Load() != 2 {
		t.Fatalf("second flight: v=%d shared=%v calls=%d", v, shared, calls.Load())
	}
}

func TestSingleFlightWaiterContext(t *testing.T) {
	var g Group[int]
	release := make(chan struct{})
	started := make(chan struct{})
	go g.Do(context.Background(), "k", func() int { close(started); <-release; return 1 })
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _, err := g.Do(ctx, "k", func() int { return 2 })
	if err == nil {
		t.Fatal("waiter should return the context error")
	}
	close(release)
}
