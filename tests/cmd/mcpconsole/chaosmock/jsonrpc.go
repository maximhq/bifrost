package chaosmock

import "encoding/json"

// JSON-RPC 2.0 framing, narrowed to what MCP uses. Hand-rolled rather than
// taken from an MCP library on purpose: the whole point of this mock is to
// violate the protocol on demand (malformed payloads, wrong ids, missing
// fields, bare HTTP statuses where a JSON-RPC error belongs), and a library
// that guarantees well-formed output cannot produce those.

const jsonRPCVersion = "2.0"

// Standard JSON-RPC error codes, plus the MCP-specific range.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// MCP method names this mock understands. Anything else answers
// method-not-found, which is itself a useful fault to exercise.
const (
	MethodInitialize  = "initialize"
	MethodInitialized = "notifications/initialized"
	MethodPing        = "ping"
	MethodToolsList   = "tools/list"
	MethodToolsCall   = "tools/call"
)

// NotificationToolListChanged is the server-to-client notification announcing
// that the tool set changed. The mock can emit it on any channel that happens
// to be open, which is what makes it useful for measuring whether a client
// acts on it at all.
const NotificationToolListChanged = "notifications/tools/list_changed"

// request is an inbound JSON-RPC message. ID is held as raw JSON because MCP
// permits both string and number ids and a mock must echo back exactly what it
// was given, including shapes a stricter decoder would normalize.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// isNotification reports whether the message expects no response. A JSON-RPC
// notification is exactly a request with no id.
func (r *request) isNotification() bool {
	return len(r.ID) == 0 || string(r.ID) == "null"
}

// response is an outbound JSON-RPC message. Result and Error are raw so a
// fault can substitute a deliberately malformed body without the encoder
// rejecting it first.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// notification is an outbound server-to-client message with no id.
type notification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

func newResult(id json.RawMessage, result any) *response {
	raw, err := json.Marshal(result)
	if err != nil {
		return newError(id, CodeInternalError, "failed to marshal result")
	}
	return &response{JSONRPC: jsonRPCVersion, ID: id, Result: raw}
}

func newError(id json.RawMessage, code int, msg string) *response {
	return &response{
		JSONRPC: jsonRPCVersion,
		ID:      id,
		Error:   &rpcError{Code: code, Message: msg},
	}
}

func newNotification(method string, params any) *notification {
	return &notification{JSONRPC: jsonRPCVersion, Method: method, Params: params}
}
