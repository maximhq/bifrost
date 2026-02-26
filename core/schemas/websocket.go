package schemas

import "encoding/json"

// WebSocketEventType represents event types in the Responses API WebSocket protocol.
type WebSocketEventType string

const (
	WSEventResponseCreate WebSocketEventType = "response.create"
	WSEventError          WebSocketEventType = "error"
)

// WebSocketResponsesEvent represents a client-sent event over the Responses WebSocket connection.
// The payload mirrors the Responses API create body, with transport-specific fields
// (stream, background) omitted since they're implicit in the WebSocket context.
type WebSocketResponsesEvent struct {
	Type               WebSocketEventType `json:"type"`
	Model              string             `json:"model,omitempty"`
	Store              *bool              `json:"store,omitempty"`
	Input              json.RawMessage    `json:"input,omitempty"`
	Instructions       string             `json:"instructions,omitempty"`
	PreviousResponseID string             `json:"previous_response_id,omitempty"`
	Generate           *bool              `json:"generate,omitempty"`
	Tools              json.RawMessage    `json:"tools,omitempty"`
	ToolChoice         json.RawMessage    `json:"tool_choice,omitempty"`
	Temperature        *float64           `json:"temperature,omitempty"`
	TopP               *float64           `json:"top_p,omitempty"`
	MaxOutputTokens    *int               `json:"max_output_tokens,omitempty"`
	Reasoning          json.RawMessage    `json:"reasoning,omitempty"`
	Metadata           json.RawMessage    `json:"metadata,omitempty"`
	Text               json.RawMessage    `json:"text,omitempty"`
	Truncation         string             `json:"truncation,omitempty"`
}

// WebSocketErrorEvent represents a server-sent error event over WebSocket.
type WebSocketErrorEvent struct {
	Type   WebSocketEventType   `json:"type"`
	Status int                  `json:"status,omitempty"`
	Error  *WebSocketErrorBody  `json:"error,omitempty"`
}

// WebSocketErrorBody is the error detail within a WebSocketErrorEvent.
type WebSocketErrorBody struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Param   string `json:"param,omitempty"`
}

// WebSocketConfig provides optional tuning for WebSocket gateway features.
// WebSocket is always enabled. These fields allow overriding the high defaults.
type WebSocketConfig struct {
	MaxConnections int           `json:"max_connections"`
	TranscriptBufferSize  int           `json:"transcript_buffer_size"`
	Pool                  *WSPoolConfig `json:"pool,omitempty"`
}

// WSPoolConfig configures the upstream WebSocket connection pool.
type WSPoolConfig struct {
	MaxIdlePerKey                int `json:"max_idle_per_key"`
	MaxTotalConnections          int `json:"max_total_connections"`
	IdleTimeoutSeconds           int `json:"idle_timeout_seconds"`
	MaxConnectionLifetimeSeconds int `json:"max_connection_lifetime_seconds"`
}

// Default pool configuration values (set high for production workloads)
const (
	DefaultWSMaxIdlePerKey                = 50
	DefaultWSMaxTotalConnections          = 1000
	DefaultWSIdleTimeoutSeconds           = 600
	DefaultWSMaxConnectionLifetimeSeconds = 7200
	DefaultWSMaxConnections               = 10000
	DefaultWSTranscriptBufferSize         = 100
)

// CheckAndSetDefaults fills in default values for WebSocketConfig.
func (c *WebSocketConfig) CheckAndSetDefaults() {
	if c.MaxConnections <= 0 {
		c.MaxConnections = DefaultWSMaxConnections
	}
	if c.TranscriptBufferSize <= 0 {
		c.TranscriptBufferSize = DefaultWSTranscriptBufferSize
	}
	if c.Pool == nil {
		c.Pool = &WSPoolConfig{}
	}
	c.Pool.CheckAndSetDefaults()
}

// CheckAndSetDefaults fills in default values for WSPoolConfig.
func (c *WSPoolConfig) CheckAndSetDefaults() {
	if c.MaxIdlePerKey <= 0 {
		c.MaxIdlePerKey = DefaultWSMaxIdlePerKey
	}
	if c.MaxTotalConnections <= 0 {
		c.MaxTotalConnections = DefaultWSMaxTotalConnections
	}
	if c.IdleTimeoutSeconds <= 0 {
		c.IdleTimeoutSeconds = DefaultWSIdleTimeoutSeconds
	}
	if c.MaxConnectionLifetimeSeconds <= 0 {
		c.MaxConnectionLifetimeSeconds = DefaultWSMaxConnectionLifetimeSeconds
	}
}
