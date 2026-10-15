// Package hub tracks connected agents and asks them to resolve names.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/follow-th/dns-resolver-network/internal/proto"
)

// ErrBusy means the agent already has the maximum number of requests in flight.
var ErrBusy = errors.New("agent busy")

// ErrClosed means the agent disconnected while a request was pending.
var ErrClosed = errors.New("agent disconnected")

// Options tunes the hub; zero values use the documented defaults.
type Options struct {
	MaxInflight  int           // per agent, default 64
	PingEvery    time.Duration // default 15 s
	DeadAfter    time.Duration // default 30 s
	WriteTimeout time.Duration // default 5 s
	Logger       *slog.Logger
}

// Reply is one agent's answer.
type Reply struct {
	Agent string
	Msg   proto.Message
}

// Info describes a connected agent for the /v1/agents endpoint.
type Info struct {
	Name           string
	ConnectedSince time.Time
	LastSeen       time.Time
	RTT            time.Duration
	Addresses      int
}

// Hub is the registry of connected agents.
type Hub struct {
	opt    Options
	mu     sync.Mutex
	agents map[string]*agent // by key id
}

// New creates a hub.
func New(opt Options) *Hub {
	if opt.MaxInflight <= 0 {
		opt.MaxInflight = 64
	}
	if opt.PingEvery <= 0 {
		opt.PingEvery = 15 * time.Second
	}
	if opt.DeadAfter <= 0 {
		opt.DeadAfter = 30 * time.Second
	}
	if opt.WriteTimeout <= 0 {
		opt.WriteTimeout = 5 * time.Second
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	return &Hub{opt: opt, agents: map[string]*agent{}}
}

type agent struct {
	keyID string
	name  string
	conn  *websocket.Conn
	since time.Time

	lastSeen atomic.Int64
	rtt      atomic.Int64
	addrs    atomic.Int32
	nextID   atomic.Uint64
	sem      chan struct{}

	mu      sync.Mutex
	pending map[uint64]chan proto.Message

	done chan struct{}
	once sync.Once
}

func (a *agent) close() {
	a.once.Do(func() {
		close(a.done)
		_ = a.conn.CloseNow()
	})
}

// Serve registers an authenticated connection and blocks until it ends. An
// existing connection with the same keyID is replaced.
func (h *Hub) Serve(ctx context.Context, conn *websocket.Conn, keyID, name string) {
	a := &agent{
		keyID: keyID, name: name, conn: conn, since: time.Now(),
		sem: make(chan struct{}, h.opt.MaxInflight), pending: map[uint64]chan proto.Message{},
		done: make(chan struct{}),
	}
	a.lastSeen.Store(a.since.UnixNano())
	conn.SetReadLimit(proto.MaxFrame)

	h.mu.Lock()
	old := h.agents[keyID]
	h.agents[keyID] = a
	h.mu.Unlock()
	if old != nil {
		h.opt.Logger.Info("agent replaced by a new connection", "agent", old.name)
		old.close()
	}
	h.opt.Logger.Info("agent connected", "agent", name)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go h.pingLoop(ctx, a)
	h.readLoop(ctx, a)

	a.close()
	h.mu.Lock()
	if h.agents[keyID] == a {
		delete(h.agents, keyID)
	}
	h.mu.Unlock()
	h.opt.Logger.Info("agent disconnected", "agent", name)
}

func (h *Hub) readLoop(ctx context.Context, a *agent) {
	for {
		typ, data, err := a.conn.Read(ctx)
		if err != nil {
			return
		}
		a.lastSeen.Store(time.Now().UnixNano())
		if typ != websocket.MessageText {
			continue
		}
		var m proto.Message
		if json.Unmarshal(data, &m) != nil || m.Type != proto.TypeResult {
			continue
		}
		if m.Error == "" && !m.NXDomain {
			a.addrs.Store(int32(len(m.IPs)))
		}
		a.mu.Lock()
		ch := a.pending[m.ID]
		a.mu.Unlock()
		if ch != nil {
			select {
			case ch <- m:
			default:
			}
		}
	}
}

func (h *Hub) pingLoop(ctx context.Context, a *agent) {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.done:
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, h.opt.DeadAfter)
		start := time.Now()
		err := a.conn.Ping(pctx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				h.opt.Logger.Warn("agent ping failed, dropping connection", "agent", a.name)
			}
			a.close()
			return
		}
		a.rtt.Store(int64(time.Since(start)))
		a.lastSeen.Store(time.Now().UnixNano())
		t.Reset(h.opt.PingEvery)
	}
}

func (a *agent) ask(ctx context.Context, host string, writeTimeout time.Duration) (proto.Message, error) {
	select {
	case a.sem <- struct{}{}:
		defer func() { <-a.sem }()
	default:
		return proto.Message{}, ErrBusy
	}
	id := a.nextID.Add(1)
	ch := make(chan proto.Message, 1)
	a.mu.Lock()
	a.pending[id] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
	}()

	data, _ := json.Marshal(proto.Message{Type: proto.TypeResolve, ID: id, Host: host})
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	err := a.conn.Write(wctx, websocket.MessageText, data)
	cancel()
	if err != nil {
		return proto.Message{}, err
	}
	select {
	case m := <-ch:
		return m, nil
	case <-ctx.Done():
		return proto.Message{}, ctx.Err()
	case <-a.done:
		return proto.Message{}, ErrClosed
	}
}

// AskAll sends the name to every connected agent in parallel and returns the
// replies received before ctx ends, in arrival order, and the number of agents
// asked.
func (h *Hub) AskAll(ctx context.Context, host string) (replies []Reply, asked int) {
	h.mu.Lock()
	agents := make([]*agent, 0, len(h.agents))
	for _, a := range h.agents {
		agents = append(agents, a)
	}
	h.mu.Unlock()
	asked = len(agents)
	ch := make(chan Reply, asked)
	for _, a := range agents {
		go func() {
			m, err := a.ask(ctx, host, h.opt.WriteTimeout)
			if err != nil {
				m = proto.Message{Type: proto.TypeResult, Error: "ask_failed"}
			}
			ch <- Reply{Agent: a.name, Msg: m}
		}()
	}
	for range asked {
		select {
		case r := <-ch:
			replies = append(replies, r)
		case <-ctx.Done():
			return replies, asked
		}
	}
	return replies, asked
}

// List returns the connected agents sorted by name.
func (h *Hub) List() []Info {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Info, 0, len(h.agents))
	for _, a := range h.agents {
		out = append(out, Info{
			Name: a.name, ConnectedSince: a.since,
			LastSeen: time.Unix(0, a.lastSeen.Load()), RTT: time.Duration(a.rtt.Load()),
			Addresses: int(a.addrs.Load()),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Count returns the number of connected agents.
func (h *Hub) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.agents)
}

// CloseAll drops every agent connection.
func (h *Hub) CloseAll() {
	h.mu.Lock()
	list := make([]*agent, 0, len(h.agents))
	for _, a := range h.agents {
		list = append(list, a)
	}
	h.mu.Unlock()
	for _, a := range list {
		a.close()
	}
}
