// Package agent implements the client that connects to the server and
// resolves names on request.
package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/follow-th/dns-resolver-network/internal/config"
	"github.com/follow-th/dns-resolver-network/internal/proto"
)

// Agent keeps a connection to the server and answers resolve requests.
type Agent struct {
	cfg config.Agent
	res Resolver
	log *slog.Logger

	// Tunables; New sets the documented defaults and tests may shorten them.
	BackoffMin   time.Duration // 1 s
	BackoffMax   time.Duration // 30 s
	StableAfter  time.Duration // 30 s of connection resets the backoff
	PingEvery    time.Duration // 15 s
	DeadAfter    time.Duration // 30 s
	LookupTimout time.Duration // 5 s per name
	MaxParallel  int           // 32 concurrent lookups
}

// New creates an agent.
func New(cfg config.Agent, res Resolver, log *slog.Logger) *Agent {
	if log == nil {
		log = slog.Default()
	}
	return &Agent{
		cfg: cfg, res: res, log: log,
		BackoffMin: time.Second, BackoffMax: 30 * time.Second, StableAfter: 30 * time.Second,
		PingEvery: 15 * time.Second, DeadAfter: 30 * time.Second,
		LookupTimout: 5 * time.Second, MaxParallel: 32,
	}
}

// Run connects and reconnects until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	url, err := a.cfg.ConnectURL()
	if err != nil {
		return err
	}
	backoff := a.BackoffMin
	for ctx.Err() == nil {
		connected, err := a.session(ctx, url)
		if ctx.Err() != nil {
			break
		}
		if connected >= a.StableAfter {
			backoff = a.BackoffMin
		}
		wait := jitter(backoff)
		a.log.Warn("disconnected, will retry", "error", errText(err), "retry_in", wait.Round(time.Millisecond))
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
		backoff = min(backoff*2, a.BackoffMax)
	}
	return nil
}

// jitter returns a duration between d/2 and d.
func jitter(d time.Duration) time.Duration {
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + rand.N(half)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// session runs one connection and returns how long it was connected.
func (a *Agent) session(ctx context.Context, url string) (time.Duration, error) {
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, resp, err := websocket.Dial(dctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":   {"Bearer " + a.cfg.Key},
			proto.AgentHeader: {a.cfg.Name},
		},
	})
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			a.log.Error("server rejected the agent key")
		}
		return 0, err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(proto.MaxFrame)
	start := time.Now()
	a.log.Info("connected", "server", a.cfg.Server, "name", a.cfg.Name)

	sctx, stop := context.WithCancel(ctx)
	defer stop()
	go a.pingLoop(sctx, conn, stop)

	sem := make(chan struct{}, a.MaxParallel)
	for {
		typ, data, err := conn.Read(sctx)
		if err != nil {
			return time.Since(start), err
		}
		if typ != websocket.MessageText {
			continue
		}
		var m proto.Message
		if json.Unmarshal(data, &m) != nil || m.Type != proto.TypeResolve {
			continue
		}
		select {
		case sem <- struct{}{}:
			go func() {
				defer func() { <-sem }()
				a.answer(sctx, conn, m)
			}()
		default:
			a.send(sctx, conn, proto.Message{Type: proto.TypeResult, ID: m.ID, Error: "busy"})
		}
	}
}

func (a *Agent) answer(ctx context.Context, conn *websocket.Conn, req proto.Message) {
	lctx, cancel := context.WithTimeout(ctx, a.LookupTimout)
	defer cancel()
	out := proto.Message{Type: proto.TypeResult, ID: req.ID}
	r, err := a.res.Lookup(lctx, req.Host)
	if err != nil {
		a.log.Debug("lookup failed", "host", req.Host, "error", err)
		out.Error = "lookup_failed"
	} else {
		out.IPs, out.TTL, out.DNS, out.NXDomain = r.IPs, r.TTL, r.DNS, r.NXDomain
	}
	a.send(ctx, conn, out)
}

func (a *Agent) send(ctx context.Context, conn *websocket.Conn, m proto.Message) {
	data, _ := json.Marshal(m)
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = conn.Write(wctx, websocket.MessageText, data)
}

// pingLoop pings the server; if a ping gets no answer within DeadAfter the
// connection is considered dead and the session is cancelled.
func (a *Agent) pingLoop(ctx context.Context, conn *websocket.Conn, stop context.CancelFunc) {
	t := time.NewTicker(a.PingEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, a.DeadAfter)
		err := conn.Ping(pctx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				a.log.Warn("server ping failed, reconnecting")
			}
			stop()
			_ = conn.CloseNow()
			return
		}
	}
}
