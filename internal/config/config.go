// Package config loads the server and agent configuration from a YAML file
// with DRN_* environment variable overrides.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/follow-th/dns-resolver-network/internal/allow"
	"github.com/follow-th/dns-resolver-network/internal/auth"
)

// Server is the server configuration.
type Server struct {
	Listen              string        `yaml:"listen"`
	TLSCert             string        `yaml:"tls_cert"`
	TLSKey              string        `yaml:"tls_key"`
	CacheTTL            time.Duration `yaml:"cache_ttl"`
	AskTimeout          time.Duration `yaml:"ask_timeout"`
	MaxInflightPerAgent int           `yaml:"max_inflight_per_agent"`
	Allow               []string      `yaml:"allow"`
	ClientKeys          []string      `yaml:"client_keys"`
	AgentKeys           []string      `yaml:"agent_keys"`
}

// Agent is the agent configuration.
type Agent struct {
	Server string   `yaml:"server"`
	Key    string   `yaml:"key"`
	Name   string   `yaml:"name"`
	DNS    []string `yaml:"dns"`
	IPv6   bool     `yaml:"ipv6"`
}

// Getenv looks up an environment variable; tests pass their own.
type Getenv func(string) string

func readYAML(path string, out any) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func envList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// LoadServer reads path (may be empty), applies defaults and DRN_* overrides
// and validates the result.
func LoadServer(path string, getenv Getenv) (Server, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	c := Server{}
	if err := readYAML(path, &c); err != nil {
		return c, err
	}
	if v := getenv("DRN_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := getenv("DRN_TLS_CERT"); v != "" {
		c.TLSCert = v
	}
	if v := getenv("DRN_TLS_KEY"); v != "" {
		c.TLSKey = v
	}
	for _, o := range []struct {
		name string
		dst  *time.Duration
	}{{"DRN_CACHE_TTL", &c.CacheTTL}, {"DRN_ASK_TIMEOUT", &c.AskTimeout}} {
		if v := getenv(o.name); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				return c, fmt.Errorf("%s: %w", o.name, err)
			}
			*o.dst = d
		}
	}
	if v := getenv("DRN_MAX_INFLIGHT_PER_AGENT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return c, fmt.Errorf("DRN_MAX_INFLIGHT_PER_AGENT: %w", err)
		}
		c.MaxInflightPerAgent = n
	}
	if v := getenv("DRN_ALLOW"); v != "" {
		c.Allow = envList(v)
	}
	if v := getenv("DRN_CLIENT_KEYS"); v != "" {
		c.ClientKeys = envList(v)
	}
	if v := getenv("DRN_AGENT_KEYS"); v != "" {
		c.AgentKeys = envList(v)
	}

	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.CacheTTL == 0 {
		c.CacheTTL = 60 * time.Second
	}
	if c.AskTimeout == 0 {
		c.AskTimeout = 2 * time.Second
	}
	if c.MaxInflightPerAgent == 0 {
		c.MaxInflightPerAgent = 64
	}
	return c, c.Validate()
}

// Validate checks the server configuration.
func (c Server) Validate() error {
	if c.CacheTTL < 0 || c.AskTimeout <= 0 || c.MaxInflightPerAgent < 0 {
		return errors.New("cache_ttl, ask_timeout and max_inflight_per_agent must be positive")
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return errors.New("tls_cert and tls_key must be set together")
	}
	if _, err := allow.New(c.Allow); err != nil {
		return err
	}
	for name, keys := range map[string][]string{"client_keys": c.ClientKeys, "agent_keys": c.AgentKeys} {
		if len(keys) == 0 {
			return fmt.Errorf("%s is empty: create a key with `server keygen` and put its hash here", name)
		}
		if _, err := auth.NewSet(keys); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// LoadAgent reads path (may be empty), applies defaults and DRN_* overrides
// and validates the result.
func LoadAgent(path string, getenv Getenv) (Agent, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	c := Agent{}
	if err := readYAML(path, &c); err != nil {
		return c, err
	}
	if v := getenv("DRN_SERVER"); v != "" {
		c.Server = v
	}
	if v := getenv("DRN_KEY"); v != "" {
		c.Key = v
	}
	if v := getenv("DRN_NAME"); v != "" {
		c.Name = v
	}
	if v := getenv("DRN_DNS"); v != "" {
		c.DNS = envList(v)
	}
	if v := getenv("DRN_IPV6"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return c, fmt.Errorf("DRN_IPV6: %w", err)
		}
		c.IPv6 = b
	}
	if c.Name == "" {
		c.Name, _ = os.Hostname()
	}
	if c.Name == "" {
		c.Name = "agent"
	}
	return c, c.Validate()
}

// Validate checks the agent configuration.
func (c Agent) Validate() error {
	if c.Key == "" {
		return errors.New("key is required")
	}
	if _, err := c.ConnectURL(); err != nil {
		return err
	}
	return nil
}

// ConnectURL returns the WebSocket URL the agent dials. The server setting may
// be a bare host, an http(s) or a ws(s) URL.
func (c Agent) ConnectURL() (string, error) {
	s := strings.TrimSpace(c.Server)
	if s == "" {
		return "", errors.New("server is required")
	}
	if !strings.Contains(s, "://") {
		s = "wss://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid server %q", c.Server)
	}
	switch u.Scheme {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("invalid server scheme %q", u.Scheme)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/v1/agent/connect"
	u.RawQuery = ""
	return u.String(), nil
}
