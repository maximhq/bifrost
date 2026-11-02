package chaosmock

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Requests on protocol version 2026-07-28 belong to no session. Each carries its version in
// _meta and, over HTTP, in the MCP-Protocol-Version header, names its method in Mcp-Method and,
// for a request aimed at one tool, prompt or resource, that target in Mcp-Name. A server serves
// them only once a scenario sets it to speak 2026-07-28 (SetProtocolVersions); until then
// server/discover answers method-not-found and a client falls back to initialize, exactly as
// before.

// Methods, headers and codes of the sessionless protocol core.
const (
	MethodServerDiscover                  = "server/discover"
	MethodSubscriptionsListen             = "subscriptions/listen"
	NotificationSubscriptionsAcknowledged = "notifications/subscriptions/acknowledged"
	HeaderMcpMethod                       = "Mcp-Method"
	HeaderMcpName                         = "Mcp-Name"
	CodeHeaderMismatch                    = -32020
	metaKeyProtocolVersion                = "io.modelcontextprotocol/protocolVersion"
	metaKeySubscriptionID                 = "io.modelcontextprotocol/subscriptionId"
)

// HeaderStdioBridge marks a request the stdio bridge forwarded. Stdio has no HTTP headers to
// check, and the bridge answers one message at a time, so it cannot hold a listen stream open:
// bridged requests stay on initialize.
const HeaderStdioBridge = "X-Chaos-Stdio-Bridge"

// modernParams is what a sessionless request carries besides its method's own parameters.
type modernParams struct {
	Meta map[string]any `json:"_meta"`
	Name string         `json:"name"`
	URI  string         `json:"uri"`
}

// requestEra reports the version a request declares in its header and in its _meta, and whether
// either names the sessionless core.
func requestEra(r *http.Request, req *request) (headerVersion, metaVersion string, modern bool) {
	headerVersion = r.Header.Get(HeaderProtocolVersion)
	var params modernParams
	if len(req.Params) > 0 && json.Unmarshal(req.Params, &params) == nil {
		metaVersion, _ = params.Meta[metaKeyProtocolVersion].(string)
	}
	return headerVersion, metaVersion, isModernVersion(headerVersion) || isModernVersion(metaVersion)
}

// headerProblem names the first way a sessionless request's headers disagree with its body, or
// returns "" when they agree. The version must appear in both, and the method and target must
// match the body.
func headerProblem(r *http.Request, req *request, headerVersion, metaVersion string) string {
	if headerVersion == "" {
		return HeaderProtocolVersion + " header is required for requests carrying " + metaKeyProtocolVersion
	}
	if metaVersion != "" && headerVersion != metaVersion {
		return fmt.Sprintf("%s header %s does not match request %s %s", HeaderProtocolVersion, headerVersion, metaKeyProtocolVersion, metaVersion)
	}
	if got := r.Header.Get(HeaderMcpMethod); got != req.Method {
		return fmt.Sprintf("%s header %q does not match the request method %q", HeaderMcpMethod, got, req.Method)
	}
	var params modernParams
	_ = json.Unmarshal(req.Params, &params)
	want := ""
	switch req.Method {
	case MethodToolsCall, MethodPromptsGet:
		want = params.Name
	case MethodResourcesRead:
		want = params.URI
	}
	if got := r.Header.Get(HeaderMcpName); want != "" && got != want {
		return fmt.Sprintf("%s header %q does not match the request target %q", HeaderMcpName, got, want)
	}
	return ""
}

// serveModern answers one sessionless request. Transport faults and credentials were checked by
// the caller; the headers, the declared version and the method come next, then the request is
// served without a session.
func (s *Server) serveModern(w http.ResponseWriter, r *http.Request, req *request, headerVersion, metaVersion, identity string, f Faults) {
	if req.isNotification() {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if problem := headerProblem(r, req, headerVersion, metaVersion); problem != "" {
		writeModern(w, newError(req.ID, CodeHeaderMismatch, problem))
		return
	}
	if metaVersion == "" {
		writeModern(w, newError(req.ID, CodeInvalidParams, "missing or invalid _meta field "+metaKeyProtocolVersion))
		return
	}
	if !s.versions.supports(metaVersion) {
		writeModern(w, unsupportedVersion(req.ID, metaVersion, s.versions.list()))
		return
	}
	switch req.Method {
	case MethodServerDiscover:
		s.wait(f)
		writeModern(w, s.handleDiscover(req, metaVersion, f))
		return
	case MethodSubscriptionsListen:
		s.serveListen(w, r, req)
		return
	case MethodInitialize, MethodPing:
		writeModern(w, newError(req.ID, CodeMethodNotFound, fmt.Sprintf("method %q is not part of protocol version %s", req.Method, metaVersion)))
		return
	}
	f = s.callFaults(req, identity, f)
	if s.failRequest(w, f) {
		return
	}
	s.writeHandled(w, s.Handle(req, "", identity, f), f, true)
}

// discoverResult is the payload of server/discover.
type discoverResult struct {
	SupportedVersions []string     `json:"supportedVersions"`
	Capabilities      capabilities `json:"capabilities"`
	ServerInfo        serverInfo   `json:"serverInfo"`
	Instructions      string       `json:"instructions,omitempty"`
}

// handleDiscover answers server/discover, the sessionless core's handshake. init_fail fails it as
// it fails initialize, and bad_version advertises that one version instead of the real list.
func (s *Server) handleDiscover(req *request, version string, f Faults) *response {
	if f.InitFail {
		s.counters.faulted()
		return newError(req.ID, CodeInternalError, f.text("server/discover failed (injected)"))
	}
	supported := s.versions.list()
	if f.BadVersion != "" {
		supported = []string{f.BadVersion}
		s.counters.faulted()
	}
	negotiated := ""
	for _, v := range supported {
		if v == version {
			negotiated = v
		}
	}
	s.counters.handshake(Handshake{Method: MethodServerDiscover, Requested: version, Negotiated: negotiated})
	return newResult(req.ID, discoverResult{
		SupportedVersions: supported,
		Capabilities:      s.capabilities(),
		ServerInfo:        serverInfo{Name: s.name, Version: s.version},
		Instructions:      s.Instructions(),
	})
}

// serveListen holds a subscriptions/listen stream open: an acknowledgement first, then every
// notification the server broadcasts, as the GET stream carries them for a session-era client.
// The stream ends when the client goes away, or with a closing result when the server drops it.
func (s *Server) serveListen(w http.ResponseWriter, r *http.Request, req *request) {
	var params struct {
		Notifications struct {
			ToolsListChanged bool `json:"toolsListChanged"`
		} `json:"notifications"`
	}
	_ = json.Unmarshal(req.Params, &params)
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	subscription := map[string]any{metaKeySubscriptionID: req.ID}
	allowed := map[string]any{}
	if params.Notifications.ToolsListChanged {
		allowed["toolsListChanged"] = true
	}
	writeEvent := func(v any) bool {
		payload, err := json.Marshal(v)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	closing := newResult(req.ID, map[string]any{"_meta": subscription})
	acknowledged := newNotification(NotificationSubscriptionsAcknowledged, map[string]any{"_meta": subscription, "notifications": allowed})
	if len(allowed) == 0 {
		// Nothing was subscribed, so there is nothing to deliver.
		if writeEvent(acknowledged) {
			writeEvent(closing)
		}
		return
	}

	// Register before acknowledging, as mcp-go's server does, so a broadcast sent once the client
	// has the acknowledgement always reaches this stream.
	ch := s.streams.add()
	s.counters.streamOpened()
	defer func() {
		s.streams.remove(ch)
		s.counters.streamClosed()
	}()
	if !writeEvent(acknowledged) {
		return
	}
	keepAlive := time.NewTicker(sseKeepAlive)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case payload, open := <-ch:
			if !open {
				writeEvent(closing)
				return
			}
			if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		case <-keepAlive.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeModern writes a sessionless response, with the HTTP status 2026-07-28 gives its error.
func writeModern(w http.ResponseWriter, resp *response) {
	status := http.StatusOK
	if resp.Error != nil {
		status = modernErrorStatus(resp.Error.Code)
	}
	writeJSON(w, status, resp)
}

// modernErrorStatus maps a JSON-RPC error code to the HTTP status 2026-07-28 prescribes for it.
func modernErrorStatus(code int) int {
	switch code {
	case CodeMethodNotFound:
		return http.StatusNotFound
	case CodeHeaderMismatch, CodeUnsupportedProtocolVersion, CodeInvalidRequest, CodeInvalidParams, CodeParseError:
		return http.StatusBadRequest
	default:
		return http.StatusOK
	}
}
