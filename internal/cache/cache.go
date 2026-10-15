// Package cache provides a small in-memory TTL cache and a single-flight group.
package cache

import (
	"context"
	"sync"
	"time"
)

type entry[V any] struct {
	v       V
	expires time.Time
}

// Cache is a size-bounded TTL cache safe for concurrent use.
type Cache[V any] struct {
	mu  sync.Mutex
	m   map[string]entry[V]
	max int
	now func() time.Time
}

// New returns a cache holding at most max entries. now may be nil.
func New[V any](max int, now func() time.Time) *Cache[V] {
	if now == nil {
		now = time.Now
	}
	return &Cache[V]{m: map[string]entry[V]{}, max: max, now: now}
}

// Get returns the value and its expiry time if present and not expired.
func (c *Cache[V]) Get(key string) (v V, expires time.Time, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, found := c.m[key]
	if !found {
		return v, time.Time{}, false
	}
	if !c.now().Before(e.expires) {
		delete(c.m, key)
		return v, time.Time{}, false
	}
	return e.v, e.expires, true
}

// Set stores a value for ttl. When full, expired entries are purged first and
// then an arbitrary entry is dropped.
func (c *Cache[V]) Set(key string, v V, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if _, exists := c.m[key]; !exists && len(c.m) >= c.max {
		for k, e := range c.m {
			if !now.Before(e.expires) {
				delete(c.m, k)
			}
		}
		for k := range c.m {
			if len(c.m) < c.max {
				break
			}
			delete(c.m, k)
		}
	}
	c.m[key] = entry[V]{v: v, expires: now.Add(ttl)}
}

// Len returns the number of stored entries (including expired ones not yet purged).
func (c *Cache[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

type call[V any] struct {
	done chan struct{}
	v    V
}

// Group suppresses duplicate concurrent calls for the same key.
type Group[V any] struct {
	mu sync.Mutex
	m  map[string]*call[V]
}

// Do runs fn once per key at a time; concurrent callers with the same key wait
// for that single run and share its result. A waiting caller returns early with
// the context error if ctx ends first (the shared run continues).
func (g *Group[V]) Do(ctx context.Context, key string, fn func() V) (v V, shared bool, err error) {
	g.mu.Lock()
	if g.m == nil {
		g.m = map[string]*call[V]{}
	}
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		select {
		case <-c.done:
			return c.v, true, nil
		case <-ctx.Done():
			return v, true, ctx.Err()
		}
	}
	c := &call[V]{done: make(chan struct{})}
	g.m[key] = c
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		delete(g.m, key)
		g.mu.Unlock()
		close(c.done)
	}()
	c.v = fn()
	return c.v, false, nil
}
