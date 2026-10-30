package chaosmock

import (
	"encoding/json"
	"fmt"
	"time"
)

// Server is one virtual MCP server. A fleet hosts many, each independently
// mutable, so a single test process can hold a healthy server next to a
// failing one and prove that a client's handling of the second did not disturb
// the first.
//
// Nothing here touches HTTP. This layer decides what a well-formed JSON-RPC
// exchange should answer given the current tool set, auth and faults; the
// transport layers decide whether a well-formed answer is what actually goes
// on the wire. Splitting it that way is what lets the same protocol behaviour
// be served over streamable HTTP, legacy SSE and stdio without three copies.
type Server struct {
	name     string
	tools    *toolSet
	faults   *faultState
	auth     *authState
	sessions *sessionStore
	counters *Counters
	streams  *streamRegistry

	// version is reported in serverInfo. Scenarios that restart a server with
	// changed behaviour bump it so a client's own logs distinguish the two.
	version string
}

// NewServer creates a healthy virtual server with no tools and no auth.
func NewServer(name string) *Server {
	return &Server{
		name:     name,
		tools:    newToolSet(),
		faults:   newFaultState(),
		auth:     newAuthState(),
		sessions: newSessionStore(),
		counters: newCounters(),
		streams:  newStreamRegistry(),
		version:  "1.0.0",
	}
}

// Name returns the server's fleet-unique name.
func (s *Server) Name() string { return s.name }

// Tools exposes the mutable tool set for direct manipulation from a test that
// would rather call Go than the control API.
func (s *Server) Tools() *toolSet { return s.tools }

// Counters exposes the served-side ground truth.
func (s *Server) Counters() *Counters { return s.counters }

// SetFaults replaces the fault set wholesale.
func (s *Server) SetFaults(f Faults) { s.faults.set(f) }

// UpdateFaults mutates the fault set under its lock.
func (s *Server) UpdateFaults(fn func(*Faults)) { s.faults.update(fn) }

// Faults returns the current fault set without consuming one-shot state.
func (s *Server) Faults() Faults { return s.faults.peek() }

// ClearFaults returns the server to healthy.
func (s *Server) ClearFaults() { s.faults.clear() }

// SetAuth replaces the auth requirements.
func (s *Server) SetAuth(a Auth) { s.auth.set(a) }

// Auth returns the current auth requirements.
func (s *Server) Auth() Auth { return s.auth.get() }

// RevokeToken drops one bearer token, leaving the rest working.
func (s *Server) RevokeToken(token string) bool { return s.auth.revoke(token) }

// GrantToken adds or replaces one bearer token.
func (s *Server) GrantToken(token, identity string) { s.auth.grant(token, identity) }

// KillSessions invalidates every live session without touching the transport.
func (s *Server) KillSessions() int { return s.sessions.killAll() }

// SessionCount reports how many sessions are live, which is the cheapest
// signal that a client is re-initializing more than it should.
func (s *Server) SessionCount() int { return s.sessions.count() }

// NotifyToolListChanged pushes the tool-list-changed notification to every
// open server-to-client stream and reports how many accepted it. Zero is a
// result, not a failure: a client that opened no listening channel cannot be
// told, and proving that is often the point.
func (s *Server) NotifyToolListChanged() int {
	payload, err := json.Marshal(newNotification(NotificationToolListChanged, struct{}{}))
	if err != nil {
		return 0
	}
	return s.streams.broadcast(payload)
}

// DropStreams closes every open server-to-client stream without touching
// sessions or the listener. A client sees its stream end while its session
// stays valid, which is the shape of a proxy reaping an idle connection.
func (s *Server) DropStreams() int { return s.streams.closeAll() }

// OpenStreams reports how many server-to-client streams are currently held.
func (s *Server) OpenStreams() int { return s.streams.count() }

// initializeResult is the payload of a successful initialize.
type initializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    capabilities `json:"capabilities"`
	ServerInfo      serverInfo   `json:"serverInfo"`
}

type capabilities struct {
	Tools *toolsCapability `json:"tools,omitempty"`
}

// toolsCapability advertises listChanged. The mock sets it true because it can
// genuinely emit the notification on any open channel; whether a client acts on
// that is one of the things worth finding out.
type toolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
	ClientInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"clientInfo"`
}

type toolsListResult struct {
	Tools []Tool `json:"tools"`
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// handleResult is what the protocol layer hands back to a transport. Most
// exchanges produce Response. The other fields describe outcomes a JSON-RPC
// response cannot express, which the transport must render itself.
type handleResult struct {
	// Response is the message to send, or nil for a notification that needs
	// no reply.
	Response *response
	// NewSession is set when initialize issued one, so the transport can put
	// the id on the response headers.
	NewSession *Session
	// HTTPStatus, when non-zero, tells the transport to abandon JSON-RPC and
	// answer with this bare status instead. Injected 5xx on a tool call takes
	// this path, as does an expired session's 404.
	HTTPStatus int
	// Malformed tells the transport to write a body that is not valid
	// JSON-RPC, to exercise a client's decoder rather than its error handling.
	Malformed bool
}

// Handle runs one JSON-RPC message against the server's current state. It is
// transport-agnostic: sessionID and identity are resolved by the caller, since
// only the transport knows where they came from.
//
// Faults are snapshotted once at entry so a control-API write landing mid-call
// cannot change how this exchange finishes. A request that started against a
// healthy server completes against a healthy server.
func (s *Server) Handle(req *request, sessionID, identity string) handleResult {
	f := s.faults.snapshot()
	s.counters.method(req.Method, sessionID)

	// Hang before anything else. A client's own timeout is what should cut
	// this short, and putting the sleep first means even a method that would
	// have failed still takes the full time, which is what a wedged upstream
	// looks like.
	if d := time.Duration(f.Hang); d > 0 {
		time.Sleep(d)
	}
	if d := s.faults.delay(f); d > 0 {
		time.Sleep(d)
	}

	switch req.Method {
	case MethodInitialize:
		return s.handleInitialize(req, sessionID, identity, f)
	case MethodInitialized:
		// A notification. Nothing to answer, but it is counted above so a test
		// can assert the handshake completed rather than merely started.
		return handleResult{}
	case MethodPing:
		return s.handlePing(req, f)
	case MethodToolsList:
		return s.handleToolsList(req, identity, f)
	case MethodToolsCall:
		return s.handleToolsCall(req, identity, f)
	default:
		return handleResult{Response: newError(req.ID, CodeMethodNotFound,
			fmt.Sprintf("method %q is not supported by chaosmock", req.Method))}
	}
}

func (s *Server) handleInitialize(req *request, sessionID, identity string, f Faults) handleResult {
	if f.InitFail {
		s.counters.faulted()
		return handleResult{Response: newError(req.ID, CodeInternalError, "initialize failed (injected)")}
	}

	var params initializeParams
	if len(req.Params) > 0 {
		// A malformed params block is the client's problem to hear about, not
		// grounds for the mock to fail the handshake: MCP permits initialize
		// with an unrecognized shape, and the version negotiation below
		// already handles a missing version.
		_ = json.Unmarshal(req.Params, &params)
	}

	version := DefaultProtocolVersion
	if params.ProtocolVersion != "" && supportedProtocolVersions[params.ProtocolVersion] {
		version = params.ProtocolVersion
	}
	if f.BadVersion != "" {
		// Deliberately answer with a version the client did not ask for and
		// may not support, which is the one thing a strict client must reject.
		version = f.BadVersion
		s.counters.faulted()
	}

	// A session the caller already holds is reused rather than replaced. The
	// two transports arrive here differently: under streamable HTTP initialize
	// is what mints the session, while under the legacy transport the stream
	// itself is the session and already exists by the time initialize is sent.
	// Minting unconditionally would double-count the second case and leave the
	// client addressing a session the server no longer routes its responses to.
	sess, existing := s.sessions.get(sessionID)
	if existing {
		s.sessions.negotiated(sess.ID, version)
	} else {
		sess = s.sessions.create(identity, version)
		s.counters.session()
	}
	s.counters.initialize()

	result := initializeResult{
		ProtocolVersion: version,
		Capabilities:    capabilities{Tools: &toolsCapability{ListChanged: true}},
		ServerInfo:      serverInfo{Name: s.name, Version: s.version},
	}
	return handleResult{Response: newResult(req.ID, result), NewSession: sess}
}

func (s *Server) handlePing(req *request, f Faults) handleResult {
	s.counters.ping()
	switch {
	case f.NoPing:
		// A server that never implemented ping. Distinct from PingFail: a
		// client should treat this as "cannot ping here" and fall back to
		// another liveness signal, not as a failure.
		s.counters.faulted()
		return handleResult{Response: newError(req.ID, CodeMethodNotFound, "ping is not supported")}
	case f.PingFail:
		s.counters.faulted()
		return handleResult{Response: newError(req.ID, CodeInternalError, "ping failed (injected)")}
	}
	return handleResult{Response: newResult(req.ID, struct{}{})}
}

func (s *Server) handleToolsList(req *request, identity string, f Faults) handleResult {
	s.counters.toolsList()
	if f.ListFail {
		s.counters.faulted()
		return handleResult{Response: newError(req.ID, CodeInternalError, "tools/list failed (injected)")}
	}
	if f.Malformed {
		s.counters.faulted()
		return handleResult{Malformed: true}
	}
	tools := s.tools.List(identity)
	return handleResult{Response: newResult(req.ID, toolsListResult{Tools: tools})}
}

func (s *Server) handleToolsCall(req *request, identity string, f Faults) handleResult {
	var params callParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return handleResult{Response: newError(req.ID, CodeInvalidParams, "invalid tools/call params")}
	}

	tool, found := s.tools.Lookup(identity, params.Name)
	if !found {
		// Counted before the error so a test can prove the call arrived and
		// was rejected for the right reason, rather than never arriving. The
		// name is recorded as asked for, which is precisely what catches a
		// caller-facing name being mapped to the wrong upstream tool.
		s.counters.call(params.Name, identity)
		return handleResult{Response: newError(req.ID, CodeInvalidParams,
			fmt.Sprintf("unknown tool %q", params.Name))}
	}

	s.counters.call(tool.Name, identity)

	// A per-tool fault overrides the server-wide set entirely, so one broken
	// tool on an otherwise healthy server is expressible. Merging instead
	// would make "this tool alone is slow" impossible to state.
	effective := f
	if tool.Fault != nil {
		effective = *tool.Fault
		if d := time.Duration(effective.Hang); d > 0 {
			time.Sleep(d)
		}
	}

	if effective.Malformed {
		s.counters.faulted()
		return handleResult{Malformed: true}
	}

	if s.faults.shouldFail(effective.ErrRate) {
		s.counters.faulted()
		switch effective.ErrKind {
		case ErrKindHTTP:
			return handleResult{HTTPStatus: 500}
		case ErrKindProtocol:
			return handleResult{Response: newError(req.ID, CodeInternalError,
				fmt.Sprintf("tool %q failed (injected)", tool.Name))}
		default:
			// ErrKindTool, and the zero value. A successful MCP exchange
			// reporting a failed tool is the most common real-world shape, so
			// it is what an unspecified ErrKind produces.
			return handleResult{Response: newResult(req.ID, callResult{
				Content: []contentBlock{{Type: "text",
					Text: fmt.Sprintf("tool %q reported an error (injected)", tool.Name)}},
				IsError: true,
			})}
		}
	}

	reply := tool.Reply
	if reply == "" {
		// Echo the tool name and arguments. Naming the tool in its own reply
		// means a test can catch a misdispatch from the response alone, before
		// it even consults the counters.
		reply = fmt.Sprintf("%s:%s", tool.Name, string(params.Arguments))
	}
	return handleResult{Response: newResult(req.ID, callResult{
		Content: []contentBlock{{Type: "text", Text: reply}},
	})}
}
