# dns-resolver-network

dns-resolver-network resolves host names through remote agents and serves the merged answers over HTTPS.

- **Agents** run on machines you choose. They dial out to the server over a WebSocket (no inbound port needed),
  and resolve names through the DNS servers you configure for them.
- **The server** exposes a small JSON API. For each request it asks every connected agent in parallel,
  merges and orders the addresses, and caches the answer for 60 seconds.
- Single static binaries and container images. No database, no message broker; all state is in memory.

## Architecture

```
client ── HTTPS GET /v1/resolve?host=example.com ──► server ── WebSocket ──► agent A ─► DNS servers
                                                       │      (agents dial   agent B ─► DNS servers
                                                       │       out to the    agent C ─► system resolver
                                                       │       server)
                                                       └─ in-memory cache (60 s), single flight
```

## Quick start

1. Create keys. Each run prints a new random key and its hash. Create one client key and one agent key.

   ```sh
   docker run --rm ghcr.io/follow-th/dns-resolver-network-server:latest keygen
   ```

   Give the **key** to the client or agent. Put only the **hash** in the server config.

2. Server: copy `deploy/server/compose.yaml`, set `DRN_ALLOW`, `DRN_CLIENT_KEYS` and `DRN_AGENT_KEYS`
   (hashes, comma separated), then start it. A YAML file (`examples/server.yaml`) works too; see the comments
   in the compose file.

   ```sh
   docker compose up -d
   ```

   Put a reverse proxy with TLS in front of port 8080, or set `tls_cert` and `tls_key`.

3. Agent: copy `deploy/agent/compose.yaml`, set `DRN_SERVER`, `DRN_KEY` (the plain key), `DRN_NAME` and
   optionally `DRN_DNS`, then start it. A YAML file (`examples/agent.yaml`) works too.

   ```sh
   docker compose up -d
   ```

4. Resolve:

   ```sh
   curl -H "Authorization: Bearer $CLIENT_KEY" "https://drn.example.com/v1/resolve?host=www.example.com"
   ```

   ```json
   {"host":"www.example.com","ips":["192.0.2.2","192.0.2.1","192.0.2.3"],
    "sources":{"192.0.2.2":["agent-a","agent-b","agent-c"],"192.0.2.1":["agent-a"],"192.0.2.3":["agent-b"]},
    "ttl":60,"cached":false,"agents":{"asked":3,"answered":3}}
   ```

## API

All endpoints except `/healthz` need `Authorization: Bearer <client key>`.

### `GET /v1/resolve?host=<name>`

Validates the name (RFC 1123, at most 253 characters, case-insensitive, one trailing dot allowed) and checks
it against the allowlist. Every connected agent is asked in parallel; the server waits until all have answered
or 2 seconds have passed, then merges the addresses, removes duplicates and orders them by how many agents
returned them (ties keep first-seen order).

| Field | Meaning |
|-------|---------|
| `host` | normalised name |
| `ips` | merged addresses, most agents first |
| `sources` | for each address, the agents that returned it |
| `ttl` | seconds the answer remains cached |
| `cached` | `true` when served from the cache |
| `agents.asked` / `agents.answered` | agents contacted / agents that answered in time |

Errors are `{"error":"<code>"}`:

| Status | Code | Meaning |
|--------|------|---------|
| 400 | `invalid_host` | not a valid host name |
| 401 | `unauthorized` | missing or unknown key |
| 403 | `host_not_allowed` | name not in the allowlist |
| 404 | `nxdomain` | every answering agent reports the name does not exist |
| 503 | `no_agent` | no agent connected, or none answered in time |

Answers are cached for `cache_ttl` (default 60 s). Concurrent requests for the same name share one lookup.
Empty or failed lookups are cached for 5 s only (nothing is cached while no agent is connected).

### `GET /v1/agents`

```json
{"agents":[{"name":"agent-a","connected_since":"2026-01-01T10:00:00Z","last_seen":"2026-01-01T10:05:00Z",
            "rtt_ms":12.4,"addresses":2}]}
```

`addresses` is the number of addresses in the agent's most recent answer.

### `GET /healthz`

Liveness check, no authentication. Returns `{"status":"ok"}`.

### `GET /v1/agent/connect`

WebSocket endpoint for agents, authenticated with an agent key. A second connection with the same key
replaces the first.

## Configuration

Both programs read a YAML file given with `-config <path>` (or `DRN_CONFIG`). Every value can be overridden by
an environment variable. Unknown YAML fields are rejected. Examples are in [`examples/`](examples).

### Server

| YAML key | Environment variable | Default | Description |
|----------|----------------------|---------|-------------|
| `listen` | `DRN_LISTEN` | `:8080` | listen address |
| `tls_cert`, `tls_key` | `DRN_TLS_CERT`, `DRN_TLS_KEY` | none | serve TLS directly (both or neither) |
| `cache_ttl` | `DRN_CACHE_TTL` | `60s` | cache lifetime of merged answers |
| `ask_timeout` | `DRN_ASK_TIMEOUT` | `2s` | how long to wait for agents |
| `max_inflight_per_agent` | `DRN_MAX_INFLIGHT_PER_AGENT` | `64` | concurrent requests per agent |
| `allow` | `DRN_ALLOW` (comma separated) | required | allowed names: `*`, `*.example.com`, or exact |
| `client_keys` | `DRN_CLIENT_KEYS` (comma separated) | required | SHA-256 hex hashes of client keys |
| `agent_keys` | `DRN_AGENT_KEYS` (comma separated) | required | SHA-256 hex hashes of agent keys |

`server keygen` prints a new random key and its hash. `server -version` prints the version.

### Agent

| YAML key | Environment variable | Default | Description |
|----------|----------------------|---------|-------------|
| `server` | `DRN_SERVER` | required | host name, or `https://` / `wss://` URL of the server |
| `key` | `DRN_KEY` | required | agent key |
| `name` | `DRN_NAME` | host name | agent name shown in answers |
| `dns` | `DRN_DNS` (comma separated) | empty | DNS servers, tried in order; empty uses the system resolver |
| `ipv6` | `DRN_IPV6` | `false` | also resolve AAAA records |

DNS servers may be written as `192.0.2.53` or `192.0.2.53:5353`. Queries go over UDP and are retried over TCP
when the answer is truncated; if a server fails or answers with an error other than "name does not exist", the
next one is tried.

The agent reconnects forever with exponential backoff and jitter (1 s doubling up to 30 s, reset after a stable
connection). Connections are pinged every 15 s and dropped after 30 s without an answer.

## Security notes

- Keys are random 256-bit values. Only their SHA-256 hashes are stored in the server config; plain keys are
  never logged. Client keys and agent keys are separate, and a key of one kind is rejected for the other.
- Run the server behind TLS (a reverse proxy, or `tls_cert` / `tls_key`). Keys are sent as bearer tokens.
- The allowlist decides which names a client key may resolve. **`allow: ["*"]` lets every holder of a client key
  resolve any name through your agents' DNS servers, which makes the deployment an open resolver for key
  holders.** Prefer exact names or `*.example.com` patterns and treat client keys as secrets.
- Agents only make outbound connections. An agent key lets a machine answer lookups for the server, so issue one
  key per agent and rotate it by replacing its hash in the config.
- Frames, requests, in-flight lookups per agent and timeouts are bounded.

## Image retention

A scheduled workflow (`.github/workflows/retention.yml`, weekly and on demand) deletes untagged image versions
and keeps the newest 20 other versions of each package. `latest` and version tags are never deleted.
Build artifacts, where used, are kept for 7 days.

## Building from source

Requires Go 1.27 or newer.

```sh
go vet ./...
go test -race ./...
go build -o bin/ ./cmd/server ./cmd/agent
```

Container images:

```sh
docker build --target server -t dns-resolver-network-server .
docker build --target agent  -t dns-resolver-network-agent .
```

Releases (tags `v*`) attach static binaries for linux (amd64, arm64), windows (amd64) and macOS (arm64), and
publish multi-arch images to `ghcr.io/follow-th/dns-resolver-network-server` and `...-agent`.

## License

MIT, see [LICENSE](LICENSE).
