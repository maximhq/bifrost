package semanticcache

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type runConfig struct {
	BifrostURL     string
	OpenAIModel    string
	OpenAIModelAlt string // different model, same provider — for cache_by_model cases
	OpenAIEmbed    string
	GeminiModel    string
	AnthroModel    string
	Namespace      string
	HTTPClient     *http.Client
}

var cfg runConfig

func loadConfig() {
	cfg.BifrostURL = strings.TrimRight(getenv("BIFROST_URL", "http://localhost:8080"), "/")
	cfg.OpenAIModel = getenv("SC_CHAT_MODEL_OPENAI", "openai/gpt-4o-mini")
	cfg.OpenAIModelAlt = getenv("SC_CHAT_MODEL_OPENAI_ALT", "openai/gpt-4o")
	cfg.OpenAIEmbed = getenv("SC_EMBED_MODEL_OPENAI", "text-embedding-3-small")
	cfg.GeminiModel = getenv("SC_CHAT_MODEL_GEMINI", "gemini/gemini-2.5-flash")
	cfg.AnthroModel = getenv("SC_CHAT_MODEL_ANTHROPIC", "anthropic/claude-haiku-4-5")
	cfg.Namespace = getenv("SC_NAMESPACE", "BifrostSemanticCachePluginE2E")
	cfg.HTTPClient = &http.Client{Timeout: 120 * time.Second}
}

func getenv(k, fallback string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return fallback
}

// cacheDebug mirrors schemas.BifrostCacheDebug as it arrives over the wire.
type cacheDebug struct {
	CacheHit          bool     `json:"cache_hit"`
	CacheID           *string  `json:"cache_id,omitempty"`
	HitType           *string  `json:"hit_type,omitempty"`
	RequestedProvider *string  `json:"requested_provider,omitempty"`
	RequestedModel    *string  `json:"requested_model,omitempty"`
	ProviderUsed      *string  `json:"provider_used,omitempty"`
	ModelUsed         *string  `json:"model_used,omitempty"`
	InputTokens       *int     `json:"input_tokens,omitempty"`
	Threshold         *float64 `json:"threshold,omitempty"`
	Similarity        *float64 `json:"similarity,omitempty"`
	CacheHitLatency   *int64   `json:"cache_hit_latency,omitempty"`
}

// extraFields subset — only what we read in assertions.
type extraFields struct {
	RequestType string      `json:"request_type,omitempty"`
	Provider    string      `json:"provider,omitempty"`
	CacheDebug  *cacheDebug `json:"cache_debug,omitempty"`
}

type chatChoice struct {
	Index        int             `json:"index"`
	Message      json.RawMessage `json:"message"`
	FinishReason *string         `json:"finish_reason,omitempty"`
}

type chatResponse struct {
	ID          string       `json:"id"`
	Object      string       `json:"object,omitempty"`
	Model       string       `json:"model,omitempty"`
	Choices     []chatChoice `json:"choices"`
	ExtraFields *extraFields `json:"extra_fields,omitempty"`
	// Captured at HTTP layer, not part of body.
	bodyRaw    []byte
	respHeader http.Header
	statusCode int
}

func (c *chatResponse) cacheDebug() *cacheDebug {
	if c.ExtraFields == nil {
		return nil
	}
	return c.ExtraFields.CacheDebug
}

// chatRequest is the minimum we need on the wire — OpenAI-compatible. Optional
// pointer fields keep "unset" distinguishable from "zero" for cache_key
// composition tests (e.g. seed=0 differs from seed unset).
type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Temperature    *float64      `json:"temperature,omitempty"`
	TopP           *float64      `json:"top_p,omitempty"`
	MaxTokens      *int          `json:"max_tokens,omitempty"`
	Seed           *int          `json:"seed,omitempty"`
	Stream         *bool         `json:"stream,omitempty"`
	Tools          []chatTool    `json:"tools,omitempty"`
	PromptCacheKey *string       `json:"prompt_cache_key,omitempty"`
	ServiceTier    *string       `json:"service_tier,omitempty"`
	Store          *bool         `json:"store,omitempty"`
	LogProbs       *bool         `json:"logprobs,omitempty"`
	TopLogProbs    *int          `json:"top_logprobs,omitempty"`
}

// chatMessage uses RawContent so it can carry either a plain string or a
// content-block array (image_url, text, etc.). Helpers below build both shapes.
type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCalls  []chatToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatToolCallFunc `json:"function"`
}

type chatToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// textContent returns a JSON-encoded plain-string content payload.
func textContent(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return json.RawMessage(b)
}

// blocksContent returns a JSON-encoded content-block array (used for image_url
// inputs and other multi-modal messages).
func blocksContent(blocks []map[string]any) json.RawMessage {
	b, _ := json.Marshal(blocks)
	return json.RawMessage(b)
}

type chatTool struct {
	Type     string        `json:"type"`               // "function"
	Function *toolFunction `json:"function,omitempty"` // required when type=function
}

type toolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type cacheHeaders struct {
	Key       string // x-bf-cache-key
	TTL       string // x-bf-cache-ttl
	Threshold *float64
	Type      string // x-bf-cache-type
	NoStore   string // x-bf-cache-no-store
}

func (h cacheHeaders) apply(req *http.Request) {
	if h.Key != "" {
		req.Header.Set("x-bf-cache-key", h.Key)
	}
	if h.TTL != "" {
		req.Header.Set("x-bf-cache-ttl", h.TTL)
	}
	if h.Threshold != nil {
		req.Header.Set("x-bf-cache-threshold", fmt.Sprintf("%v", *h.Threshold))
	}
	if h.Type != "" {
		req.Header.Set("x-bf-cache-type", h.Type)
	}
	if h.NoStore != "" {
		req.Header.Set("x-bf-cache-no-store", h.NoStore)
	}
}

// doJSON sends a JSON request and returns status, body, headers.
func doJSON(t *testing.T, method, path string, body any, extra http.Header) (int, []byte, http.Header, error) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return 0, nil, nil, fmt.Errorf("marshal: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	url := cfg.BifrostURL + path
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("new request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vv := range extra {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	resp, err := cfg.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, resp.Header, fmt.Errorf("read body: %w", err)
	}
	return resp.StatusCode, respBytes, resp.Header, nil
}

// postChat sends a chat completion and parses the response.
func postChat(t *testing.T, lc logCtx, step int, req chatRequest, ch cacheHeaders) *chatResponse {
	t.Helper()
	logf(t, lc.at(step), "INFO", "request", map[string]any{
		"method":    "POST",
		"path":      "/v1/chat/completions",
		"model":     req.Model,
		"cache_key": ch.Key,
		"ttl":       ch.TTL,
		"type":      ch.Type,
		"no_store":  ch.NoStore,
	})

	// Dump request body for forensics.
	if rb, err := json.MarshalIndent(req, "", "  "); err == nil {
		dumpJSON(t, fmt.Sprintf("p%s-%s-s%d.req.json", lc.phase, lc.name, step), rb)
	}

	hdr := http.Header{}
	ch.apply(&http.Request{Header: hdr})

	status, body, respHdr, err := doJSON(t, "POST", "/v1/chat/completions", req, hdr)
	if err != nil {
		t.Fatalf("postChat http error: %v", err)
	}
	dumpJSON(t, fmt.Sprintf("p%s-%s-s%d.resp.json", lc.phase, lc.name, step), body)

	out := &chatResponse{bodyRaw: body, respHeader: respHdr, statusCode: status}
	if status != http.StatusOK {
		logf(t, lc.at(step), "ERROR", "response", map[string]any{
			"status":   status,
			"body_len": len(body),
		})
		t.Fatalf("chat completion failed: status=%d body=%s", status, truncate(string(body), 500))
	}
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("decode chat response: %v\nbody=%s", err, truncate(string(body), 500))
	}
	cd := out.cacheDebug()
	fields := map[string]any{"status": status}
	if cd != nil {
		fields["cache_hit"] = cd.CacheHit
		if cd.CacheID != nil {
			fields["cache_id"] = *cd.CacheID
		}
		if cd.HitType != nil {
			fields["hit_type"] = *cd.HitType
		}
		if cd.CacheHitLatency != nil {
			fields["cache_hit_latency"] = *cd.CacheHitLatency
		}
	} else {
		fields["cache_debug"] = "<absent>"
	}
	logf(t, lc.at(step), "INFO", "response", fields)
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// -----------------------------------------------------------------------------
// Text completion (/v1/completions)
// -----------------------------------------------------------------------------

type textCompletionRequest struct {
	Model       string   `json:"model"`
	Prompt      string   `json:"prompt"`
	MaxTokens   *int     `json:"max_tokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

type textCompletionResponse struct {
	ExtraFields *extraFields `json:"extra_fields,omitempty"`
	bodyRaw     []byte
	statusCode  int
}

func (r *textCompletionResponse) cacheDebug() *cacheDebug {
	if r.ExtraFields == nil {
		return nil
	}
	return r.ExtraFields.CacheDebug
}

func postTextCompletion(t *testing.T, lc logCtx, step int, req textCompletionRequest, ch cacheHeaders) *textCompletionResponse {
	t.Helper()
	logf(t, lc.at(step), "INFO", "request", map[string]any{
		"method": "POST", "path": "/v1/completions", "model": req.Model, "cache_key": ch.Key,
	})
	if rb, err := json.MarshalIndent(req, "", "  "); err == nil {
		dumpJSON(t, fmt.Sprintf("p%s-%s-s%d.req.json", lc.phase, lc.name, step), rb)
	}
	hdr := http.Header{}
	ch.apply(&http.Request{Header: hdr})
	status, body, _, err := doJSON(t, "POST", "/v1/completions", req, hdr)
	if err != nil {
		t.Fatalf("postTextCompletion http error: %v", err)
	}
	dumpJSON(t, fmt.Sprintf("p%s-%s-s%d.resp.json", lc.phase, lc.name, step), body)
	if status != http.StatusOK {
		t.Fatalf("text completion failed: status=%d body=%s", status, truncate(string(body), 500))
	}
	out := &textCompletionResponse{bodyRaw: body, statusCode: status}
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("decode text completion response: %v\nbody=%s", err, truncate(string(body), 500))
	}
	logCacheDebugFields(t, lc.at(step), out.cacheDebug())
	return out
}

// -----------------------------------------------------------------------------
// Embeddings (/v1/embeddings)
// -----------------------------------------------------------------------------

type embeddingRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embeddingResponse struct {
	ExtraFields *extraFields `json:"extra_fields,omitempty"`
	bodyRaw     []byte
	statusCode  int
}

func (r *embeddingResponse) cacheDebug() *cacheDebug {
	if r.ExtraFields == nil {
		return nil
	}
	return r.ExtraFields.CacheDebug
}

func postEmbedding(t *testing.T, lc logCtx, step int, req embeddingRequest, ch cacheHeaders) *embeddingResponse {
	t.Helper()
	logf(t, lc.at(step), "INFO", "request", map[string]any{
		"method": "POST", "path": "/v1/embeddings", "model": req.Model, "cache_key": ch.Key,
	})
	if rb, err := json.MarshalIndent(req, "", "  "); err == nil {
		dumpJSON(t, fmt.Sprintf("p%s-%s-s%d.req.json", lc.phase, lc.name, step), rb)
	}
	hdr := http.Header{}
	ch.apply(&http.Request{Header: hdr})
	status, body, _, err := doJSON(t, "POST", "/v1/embeddings", req, hdr)
	if err != nil {
		t.Fatalf("postEmbedding http error: %v", err)
	}
	dumpJSON(t, fmt.Sprintf("p%s-%s-s%d.resp.json", lc.phase, lc.name, step), body)
	if status != http.StatusOK {
		t.Fatalf("embedding failed: status=%d body=%s", status, truncate(string(body), 500))
	}
	out := &embeddingResponse{bodyRaw: body, statusCode: status}
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("decode embedding response: %v\nbody=%s", err, truncate(string(body), 500))
	}
	logCacheDebugFields(t, lc.at(step), out.cacheDebug())
	return out
}

// -----------------------------------------------------------------------------
// Image generation (/v1/images/generations)
// -----------------------------------------------------------------------------

type imageGenRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	N      *int   `json:"n,omitempty"`
	Size   string `json:"size,omitempty"`
}

type imageGenResponse struct {
	ExtraFields *extraFields `json:"extra_fields,omitempty"`
	bodyRaw     []byte
	statusCode  int
}

func (r *imageGenResponse) cacheDebug() *cacheDebug {
	if r.ExtraFields == nil {
		return nil
	}
	return r.ExtraFields.CacheDebug
}

func postImageGen(t *testing.T, lc logCtx, step int, req imageGenRequest, ch cacheHeaders) *imageGenResponse {
	t.Helper()
	logf(t, lc.at(step), "INFO", "request", map[string]any{
		"method": "POST", "path": "/v1/images/generations", "model": req.Model, "cache_key": ch.Key,
	})
	if rb, err := json.MarshalIndent(req, "", "  "); err == nil {
		dumpJSON(t, fmt.Sprintf("p%s-%s-s%d.req.json", lc.phase, lc.name, step), rb)
	}
	hdr := http.Header{}
	ch.apply(&http.Request{Header: hdr})
	status, body, _, err := doJSON(t, "POST", "/v1/images/generations", req, hdr)
	if err != nil {
		t.Fatalf("postImageGen http error: %v", err)
	}
	dumpJSON(t, fmt.Sprintf("p%s-%s-s%d.resp.json", lc.phase, lc.name, step), body)
	if status != http.StatusOK {
		t.Fatalf("image gen failed: status=%d body=%s", status, truncate(string(body), 500))
	}
	out := &imageGenResponse{bodyRaw: body, statusCode: status}
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("decode image gen response: %v\nbody=%s", err, truncate(string(body), 500))
	}
	logCacheDebugFields(t, lc.at(step), out.cacheDebug())
	return out
}

// -----------------------------------------------------------------------------
// Responses API (/v1/responses) — OpenAI's newer interface
// -----------------------------------------------------------------------------

type responsesRequest struct {
	Model              string  `json:"model"`
	Input              string  `json:"input"`
	Instructions       *string `json:"instructions,omitempty"`
	PreviousResponseID *string `json:"previous_response_id,omitempty"`
}

type responsesResponse struct {
	ExtraFields *extraFields `json:"extra_fields,omitempty"`
	bodyRaw     []byte
	statusCode  int
}

func (r *responsesResponse) cacheDebug() *cacheDebug {
	if r.ExtraFields == nil {
		return nil
	}
	return r.ExtraFields.CacheDebug
}

func postResponses(t *testing.T, lc logCtx, step int, req responsesRequest, ch cacheHeaders) *responsesResponse {
	t.Helper()
	logf(t, lc.at(step), "INFO", "request", map[string]any{
		"method": "POST", "path": "/v1/responses", "model": req.Model, "cache_key": ch.Key,
	})
	if rb, err := json.MarshalIndent(req, "", "  "); err == nil {
		dumpJSON(t, fmt.Sprintf("p%s-%s-s%d.req.json", lc.phase, lc.name, step), rb)
	}
	hdr := http.Header{}
	ch.apply(&http.Request{Header: hdr})
	status, body, _, err := doJSON(t, "POST", "/v1/responses", req, hdr)
	if err != nil {
		t.Fatalf("postResponses http error: %v", err)
	}
	dumpJSON(t, fmt.Sprintf("p%s-%s-s%d.resp.json", lc.phase, lc.name, step), body)
	if status != http.StatusOK {
		t.Fatalf("responses API failed: status=%d body=%s", status, truncate(string(body), 500))
	}
	out := &responsesResponse{bodyRaw: body, statusCode: status}
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("decode responses API response: %v\nbody=%s", err, truncate(string(body), 500))
	}
	logCacheDebugFields(t, lc.at(step), out.cacheDebug())
	return out
}

// -----------------------------------------------------------------------------
// Streaming chat (/v1/chat/completions with stream:true) — SSE
// -----------------------------------------------------------------------------

// streamChunk is one decoded SSE data event from a chat completion stream.
type streamChunk struct {
	Index       int
	Raw         []byte
	Parsed      map[string]any
	ExtraFields *extraFields
	Done        bool // true for the terminal [DONE] sentinel
}

func (c *streamChunk) cacheDebug() *cacheDebug {
	if c.ExtraFields == nil {
		return nil
	}
	return c.ExtraFields.CacheDebug
}

// chunkText extracts choices[0].delta.content (or .message.content) as a
// string. Used to compare chunk order/content across A and B in case 1.25.
func (c *streamChunk) chunkText() string {
	if c.Parsed == nil {
		return ""
	}
	choices, _ := c.Parsed["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	c0, _ := choices[0].(map[string]any)
	if c0 == nil {
		return ""
	}
	if delta, ok := c0["delta"].(map[string]any); ok {
		if s, ok := delta["content"].(string); ok {
			return s
		}
	}
	if msg, ok := c0["message"].(map[string]any); ok {
		if s, ok := msg["content"].(string); ok {
			return s
		}
	}
	return ""
}

// streamResponse aggregates every chunk received from one streamed chat
// completion. cacheDebug() returns the stamp from the final chunk — that's
// the only chunk the plugin tags (search.go:628 guard).
type streamResponse struct {
	Chunks     []streamChunk
	statusCode int
	headers    http.Header
}

func (s *streamResponse) cacheDebug() *cacheDebug {
	for i := len(s.Chunks) - 1; i >= 0; i-- {
		if cd := s.Chunks[i].cacheDebug(); cd != nil {
			return cd
		}
	}
	return nil
}

// dataChunks returns the chunks excluding the terminal [DONE] sentinel.
func (s *streamResponse) dataChunks() []streamChunk {
	out := make([]streamChunk, 0, len(s.Chunks))
	for _, c := range s.Chunks {
		if !c.Done {
			out = append(out, c)
		}
	}
	return out
}

func postChatStream(t *testing.T, lc logCtx, step int, req chatRequest, ch cacheHeaders) *streamResponse {
	t.Helper()
	streamFlag := true
	req.Stream = &streamFlag

	logf(t, lc.at(step), "INFO", "request", map[string]any{
		"method": "POST", "path": "/v1/chat/completions", "model": req.Model,
		"cache_key": ch.Key, "stream": true,
	})
	if rb, err := json.MarshalIndent(req, "", "  "); err == nil {
		dumpJSON(t, fmt.Sprintf("p%s-%s-s%d.req.json", lc.phase, lc.name, step), rb)
	}

	bodyBytes, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal stream req: %v", err)
	}
	httpReq, err := http.NewRequest("POST", cfg.BifrostURL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("new stream req: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	ch.apply(httpReq)

	resp, err := cfg.HTTPClient.Do(httpReq)
	if err != nil {
		t.Fatalf("stream do: %v", err)
	}
	defer resp.Body.Close()

	out := &streamResponse{statusCode: resp.StatusCode, headers: resp.Header}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("stream request failed: status=%d body=%s", resp.StatusCode, truncate(string(body), 500))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	rawDump := &bytes.Buffer{}
	idx := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		rawDump.Write(line)
		rawDump.WriteByte('\n')
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		payload := bytes.TrimPrefix(line, []byte("data: "))
		payload = bytes.TrimSpace(payload)
		if len(payload) == 0 {
			continue
		}
		if bytes.Equal(payload, []byte("[DONE]")) {
			out.Chunks = append(out.Chunks, streamChunk{Index: idx, Done: true})
			idx++
			break
		}
		ck := streamChunk{Index: idx, Raw: append([]byte(nil), payload...)}
		if err := json.Unmarshal(payload, &ck.Parsed); err != nil {
			t.Logf("warning: chunk %d unparseable JSON: %v\nraw=%s", idx, err, truncate(string(payload), 200))
		} else {
			var ef struct {
				ExtraFields *extraFields `json:"extra_fields,omitempty"`
			}
			_ = json.Unmarshal(payload, &ef)
			ck.ExtraFields = ef.ExtraFields
		}
		out.Chunks = append(out.Chunks, ck)
		idx++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("stream scanner: %v", err)
	}

	dumpJSON(t, fmt.Sprintf("p%s-%s-s%d.resp.sse.txt", lc.phase, lc.name, step), rawDump.Bytes())

	fields := map[string]any{
		"status":      resp.StatusCode,
		"chunk_count": len(out.dataChunks()),
	}
	if cd := out.cacheDebug(); cd != nil {
		fields["cache_hit"] = cd.CacheHit
		if cd.CacheID != nil {
			fields["cache_id"] = *cd.CacheID
		}
		if cd.HitType != nil {
			fields["hit_type"] = *cd.HitType
		}
	} else {
		fields["cache_debug"] = "<absent>"
	}
	logf(t, lc.at(step), "INFO", "response", fields)
	return out
}

// logCacheDebugFields emits a single response-event log line with the standard
// cache_debug fields, used by every postXxx helper above.
func logCacheDebugFields(t *testing.T, lc logCtx, cd *cacheDebug) {
	t.Helper()
	fields := map[string]any{"status": 200}
	if cd != nil {
		fields["cache_hit"] = cd.CacheHit
		if cd.CacheID != nil {
			fields["cache_id"] = *cd.CacheID
		}
		if cd.HitType != nil {
			fields["hit_type"] = *cd.HitType
		}
		if cd.CacheHitLatency != nil {
			fields["cache_hit_latency"] = *cd.CacheHitLatency
		}
	} else {
		fields["cache_debug"] = "<absent>"
	}
	logf(t, lc, "INFO", "response", fields)
}
