// Package server implements the HTTP API and the agent WebSocket endpoint.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/coder/websocket"

	"github.com/follow-th/dns-resolver-network/internal/allow"
	"github.com/follow-th/dns-resolver-network/internal/auth"
	"github.com/follow-th/dns-resolver-network/internal/config"
	"github.com/follow-th/dns-resolver-network/internal/hub"
	"github.com/follow-th/dns-resolver-network/internal/lookup"
	"github.com/follow-th/dns-resolver-network/internal/proto"
)

// Server wires the hub, lookup service and HTTP handlers together.
type Server struct {
	cfg        config.Server
	log        *slog.Logger
	hub        *hub.Hub
	svc        *lookup.Service
	allow      *allow.List
	clientKeys *auth.Set
	agentKeys  *auth.Set
	ctx        context.Context
}

// Options are test hooks; the zero value is correct for production.
type Options struct {
	Hub hub.Options
}

// New builds a Server from a validated configuration. ctx bounds agent
// connections: cancelling it disconnects every agent.
func New(ctx context.Context, cfg config.Server, log *slog.Logger, opt Options) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	al, err := allow.New(cfg.Allow)
	if err != nil {
		return nil, err
	}
	ck, err := auth.NewSet(cfg.ClientKeys)
	if err != nil {
		return nil, err
	}
	ak, err := auth.NewSet(cfg.AgentKeys)
	if err != nil {
		return nil, err
	}
	opt.Hub.Logger = log
	opt.Hub.MaxInflight = cfg.MaxInflightPerAgent
	h := hub.New(opt.Hub)
	return &Server{
		cfg: cfg, log: log, hub: h, allow: al, clientKeys: ck, agentKeys: ak, ctx: ctx,
		svc: lookup.New(h, cfg.CacheTTL, cfg.AskTimeout, nil),
	}, nil
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/resolve", s.handleResolve)
	mux.HandleFunc("GET "+"/v1/agents", s.handleAgents)
	mux.HandleFunc("GET "+proto.ConnectPath, s.handleConnect)
	return mux
}

// Run serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	errc := make(chan error, 1)
	go func() {
		if s.cfg.TLSCert != "" {
			errc <- srv.ListenAndServeTLS(s.cfg.TLSCert, s.cfg.TLSKey)
		} else {
			errc <- srv.ListenAndServe()
		}
	}()
	s.log.Info("server listening", "addr", s.cfg.Listen, "tls", s.cfg.TLSCert != "")
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.hub.CloseAll()
	err := srv.Shutdown(sctx)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

func (s *Server) authClient(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := s.clientKeys.Match(auth.BearerToken(r.Header)); !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	if !s.authClient(w, r) {
		return
	}
	host := allow.Normalize(r.URL.Query().Get("host"))
	if !allow.ValidHost(host) {
		writeError(w, http.StatusBadRequest, "invalid_host")
		return
	}
	if !s.allow.Allowed(host) {
		writeError(w, http.StatusForbidden, "host_not_allowed")
		return
	}
	ans, err := s.svc.Resolve(r.Context(), host)
	switch {
	case errors.Is(err, lookup.ErrNoAgent):
		writeError(w, http.StatusServiceUnavailable, "no_agent")
	case errors.Is(err, lookup.ErrNXDomain):
		writeError(w, http.StatusNotFound, "nxdomain")
	case err != nil:
		writeError(w, http.StatusServiceUnavailable, "no_agent")
	default:
		writeJSON(w, http.StatusOK, ans)
	}
}

type agentJSON struct {
	Name           string    `json:"name"`
	ConnectedSince time.Time `json:"connected_since"`
	LastSeen       time.Time `json:"last_seen"`
	RTTMillis      float64   `json:"rtt_ms"`
	Addresses      int       `json:"addresses"`
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	if !s.authClient(w, r) {
		return
	}
	list := s.hub.List()
	out := make([]agentJSON, 0, len(list))
	for _, a := range list {
		out = append(out, agentJSON{
			Name: a.Name, ConnectedSince: a.ConnectedSince.UTC(), LastSeen: a.LastSeen.UTC(),
			RTTMillis: float64(a.RTT.Microseconds()) / 1000, Addresses: a.Addresses,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	id, ok := s.agentKeys.Match(auth.BearerToken(r.Header))
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	name := r.Header.Get(proto.AgentHeader)
	if !nameRe.MatchString(name) {
		name = "agent-" + id[:8]
	}
	// The connection outlives the HTTP server's per-request deadlines.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	s.hub.Serve(s.ctx, conn, id, name)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
