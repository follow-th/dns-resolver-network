# dns-resolver-network design

## Overview

dns-resolver-network resolves host names through remote agents and serves the merged answers over HTTPS.

```
client ── HTTPS GET /v1/resolve?host=example.com ──► server ── WebSocket ──► agents
                                                       │                       └─ resolve via their configured DNS servers
                                                       └─ in-memory cache (60 s)
```

## Goals

- Two programs in one Go module: `server` and `agent`. Single static binaries, plus container images.
- No database, no message broker. All state is in memory; configuration is a file and/or environment.
- Agents only dial out (WebSocket over HTTPS), so no inbound port is needed on the agent side.
- Simple HTTPS JSON API for clients. No DNS wire protocol in v1.

## Agent

- Connects to `wss://<server>/v1/agent/connect` with `Authorization: Bearer <agent key>`.
- Resolves A records (AAAA when `ipv6: true`) through the configured DNS servers (`dns: ["a.b.c.d", ...]`,
  tried in order, UDP then TCP on truncation). With `dns` empty it uses the system resolver.
- Answers with every address, the minimum TTL and which DNS server answered.
- Reconnects forever with exponential backoff and jitter (1 s doubling to 30 s, reset after a stable
  connection). Ping every 15 s; a connection with no traffic for 30 s is dropped and redialed.
- Agent name comes from config (default: hostname).

## Server

- `GET /v1/resolve?host=<name>` with `Authorization: Bearer <client key>`.
  - Validates the name (RFC 1123, max 253 chars) and the allowlist (`allow: ["*"]` means every name;
    `*.example.com` matches the domain and its subdomains; exact names match exactly).
  - Asks every connected agent in parallel, waits until all answer or 2 s pass, merges the addresses,
    removes duplicates, and orders them by how many agents returned them (then by first seen).
  - Answer:
    `{"host":"...","ips":["..."],"sources":{"<ip>":["agent-a","agent-b"]},"ttl":60,"cached":false,"agents":{"asked":3,"answered":2}}`
  - Caches the merged answer in memory for `cache_ttl` (default 60 s). Concurrent requests for the same
    name share one lookup (single flight). Empty or failed lookups are cached for 5 s only.
  - No agent connected, or no agent answered: `503 {"error":"no_agent"}`; a name every agent reports as
    nonexistent: `404 {"error":"nxdomain"}`. Clients fall back to their own DNS.
- `GET /v1/agents` (client key): name, connected since, last seen, last round-trip time, address count.
- `GET /healthz`: liveness, no auth.
- Keys are stored as SHA-256 hex in config; plain keys never appear in config or logs.
  `server keygen` prints a new random key once with its hash.
- An agent key may be connected once; a second connection with the same key replaces the first.
- Limits: request body/frames small and bounded, per-agent in-flight cap, read/write timeouts.
- TLS: either terminated by a reverse proxy, or `tls_cert`/`tls_key` in config.

## Configuration

YAML file (`-config path`) with every value overridable by environment variables (`DRN_*`).
Examples live in `examples/`.

## Packaging

- Multi-stage Dockerfile producing distroless/static images for `server` and `agent`.
- GitHub Actions: test on every push and pull request; on `main` and `v*` tags build multi-arch images
  (linux/amd64, linux/arm64) and push to `ghcr.io/<owner>/dns-resolver-network-server` and
  `...-agent` (public), and attach static binaries for linux, windows and darwin to tag releases.
- `deploy/server/compose.yaml` and `deploy/agent/compose.yaml` run the public images.

## Out of scope (v1)

- DNS wire protocol (UDP/TCP 53) and DoH.
- Grouping agents by network; every agent is in one pool.
- Persistent state.
