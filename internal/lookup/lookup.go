// Package lookup resolves a name through all agents, merges the answers and
// caches the outcome.
package lookup

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/follow-th/dns-resolver-network/internal/cache"
	"github.com/follow-th/dns-resolver-network/internal/hub"
	"github.com/follow-th/dns-resolver-network/internal/merge"
)

var (
	// ErrNoAgent means no agent is connected or none answered in time.
	ErrNoAgent = errors.New("no_agent")
	// ErrNXDomain means every answering agent reported that the name does not exist.
	ErrNXDomain = errors.New("nxdomain")
)

// NegativeTTL is how long empty and failed lookups are cached.
const NegativeTTL = 5 * time.Second

const maxEntries = 10000

// Counts reports how many agents were asked and how many answered.
type Counts struct {
	Asked    int `json:"asked"`
	Answered int `json:"answered"`
}

// Answer is the merged result returned to clients.
type Answer struct {
	Host    string              `json:"host"`
	IPs     []string            `json:"ips"`
	Sources map[string][]string `json:"sources"`
	TTL     int                 `json:"ttl"`
	Cached  bool                `json:"cached"`
	Agents  Counts              `json:"agents"`
}

// Asker asks every connected agent about a name.
type Asker interface {
	AskAll(ctx context.Context, host string) ([]hub.Reply, int)
}

type outcome struct {
	ans Answer
	err error
}

// Service performs cached, single-flight lookups.
type Service struct {
	asker  Asker
	ttl    time.Duration
	window time.Duration
	now    func() time.Time
	cache  *cache.Cache[outcome]
	group  cache.Group[outcome]
}

// New creates a Service. now may be nil.
func New(asker Asker, cacheTTL, window time.Duration, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{asker: asker, ttl: cacheTTL, window: window, now: now, cache: cache.New[outcome](maxEntries, now)}
}

// Resolve returns the merged answer for a normalised, validated host.
func (s *Service) Resolve(ctx context.Context, host string) (Answer, error) {
	if o, exp, ok := s.cache.Get(host); ok {
		return s.finish(o, exp, true)
	}
	o, _, err := s.group.Do(ctx, host, func() outcome {
		// Re-check: a flight that just finished may have filled the cache.
		if o, _, ok := s.cache.Get(host); ok {
			return o
		}
		return s.lookup(host)
	})
	if err != nil {
		return Answer{}, err
	}
	// Shared and fresh results report the remaining cache lifetime too.
	if _, exp, ok := s.cache.Get(host); ok {
		return s.finish(o, exp, false)
	}
	return s.finish(o, s.now().Add(NegativeTTL), false)
}

func (s *Service) finish(o outcome, expires time.Time, cached bool) (Answer, error) {
	if o.err != nil {
		return Answer{}, o.err
	}
	a := o.ans
	a.Cached = cached
	a.TTL = int(math.Ceil(expires.Sub(s.now()).Seconds()))
	if a.TTL < 0 {
		a.TTL = 0
	}
	return a, nil
}

func (s *Service) lookup(host string) outcome {
	ctx, cancel := context.WithTimeout(context.Background(), s.window)
	defer cancel()
	replies, asked := s.asker.AskAll(ctx, host)

	var results []merge.Result
	answered, nx := 0, 0
	for _, r := range replies {
		if r.Msg.Error != "" {
			continue
		}
		answered++
		if r.Msg.NXDomain {
			nx++
		}
		results = append(results, merge.Result{Agent: r.Agent, IPs: r.Msg.IPs})
	}
	if answered == 0 {
		o := outcome{err: ErrNoAgent}
		if asked > 0 { // nothing is worth caching while no agent is connected
			s.cache.Set(host, o, NegativeTTL)
		}
		return o
	}
	ips, sources := merge.Merge(results)
	if len(ips) == 0 && nx == answered {
		o := outcome{err: ErrNXDomain}
		s.cache.Set(host, o, NegativeTTL)
		return o
	}
	o := outcome{ans: Answer{Host: host, IPs: ips, Sources: sources, Agents: Counts{Asked: asked, Answered: answered}}}
	ttl := s.ttl
	if len(ips) == 0 {
		ttl = NegativeTTL
	}
	s.cache.Set(host, o, ttl)
	return o
}
