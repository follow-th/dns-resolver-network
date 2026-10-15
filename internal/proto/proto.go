// Package proto defines the JSON frames exchanged between server and agents
// over the WebSocket connection.
package proto

const (
	// TypeResolve is sent by the server to ask an agent to resolve a name.
	TypeResolve = "resolve"
	// TypeResult is the agent's answer to a resolve frame.
	TypeResult = "result"
)

const (
	// ConnectPath is the server endpoint agents dial.
	ConnectPath = "/v1/agent/connect"
	// AgentHeader carries the agent name during the WebSocket handshake.
	AgentHeader = "X-DRN-Agent"
	// MaxFrame bounds every frame in both directions.
	MaxFrame = 64 << 10
	// MaxIPs bounds the addresses an agent reports for one name.
	MaxIPs = 64
)

// Message is the single frame shape; Type selects which fields are used.
type Message struct {
	Type string `json:"type"`
	ID   uint64 `json:"id"`

	// resolve
	Host string `json:"host,omitempty"`

	// result
	IPs      []string `json:"ips,omitempty"`
	TTL      uint32   `json:"ttl,omitempty"`
	DNS      string   `json:"dns,omitempty"`
	NXDomain bool     `json:"nxdomain,omitempty"`
	Error    string   `json:"error,omitempty"`
}
